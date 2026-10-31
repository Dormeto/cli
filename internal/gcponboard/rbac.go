package gcponboard

// The Kubernetes RBAC of a GKE workload (spec §8) and `dormeto gcp workloads access show|grant|revoke`: a Role and a
// RoleBinding dormeto-<workload> in the workload's namespace, for the router's service account, nothing cluster-wide. The
// router's IAM grants nothing inside a cluster, so the operator writes them, as themselves: HTTPS with the public CAs to the
// cluster's DNS endpoint, their access token as bearer. An object of that name without the label managed-by is someone
// else's: never changed, never removed. On our RoleBinding, the subjects Dormeto did not add stay: an update keeps them, and
// while one remains a removal takes the router's subject alone and leaves both objects. Another router's service account
// there is a refusal, as two routers cannot share one dormeto-<workload>. The install record lists each workload's access
// before it is applied and until it is removed. Exit codes: 0; 1 when Google or Kubernetes refuses, an object is someone
// else's or names another router, or the cluster has no DNS endpoint; 2 for a bad flag or a missing answer; 130 when
// interrupted.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/install"
)

const (
	containerV1    = "https://container.googleapis.com/v1/"
	rbacAPIGroup   = "rbac.authorization.k8s.io"
	rbacAPIVersion = rbacAPIGroup + "/v1"
	managedByLabel = "app.kubernetes.io/managed-by"
	managedBy      = "dormeto"
)

var (
	clusterLocationPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	clusterNamePattern     = regexp.MustCompile(`^[a-z]([a-z0-9-]*[a-z0-9])?$`)
	namespacePattern       = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// dnsEndpointPattern is a cluster's DNS endpoint: the operator's token goes there, so nothing else may.
	dnsEndpointPattern = regexp.MustCompile(`^[a-z0-9-]+(\.[a-z0-9-]+)*\.gke\.goog$`)
)

// Rbac is the Kubernetes access of one workload: its cluster as <location>/<name>, its namespace, its ID, and the router's
// service account (an email) that gets the access.
type Rbac struct {
	Cluster        string `json:"cluster"`
	Namespace      string `json:"namespace"`
	Workload       string `json:"workload"`
	ServiceAccount string `json:"serviceAccount"`
}

func (rb Rbac) entry() RecordRbac {
	return RecordRbac{Cluster: rb.Cluster, Namespace: rb.Namespace, Workload: rb.Workload}
}

type rbacRule struct {
	APIGroups     []string `json:"apiGroups"`
	Resources     []string `json:"resources"`
	ResourceNames []string `json:"resourceNames,omitempty"` // none of ours: read so that one added by hand differs
	Verbs         []string `json:"verbs"`
}

type rbacSubject struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	APIGroup  string `json:"apiGroup,omitempty"`  // a Kubernetes ServiceAccount has none
	Namespace string `json:"namespace,omitempty"` // a Kubernetes ServiceAccount's
}

func (s rbacSubject) String() string { return s.Kind + " " + s.Name }

type rbacRoleRef struct {
	APIGroup string `json:"apiGroup"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
}

type kubeMetadata struct {
	Name            string            `json:"name"`
	Namespace       string            `json:"namespace"`
	Labels          map[string]string `json:"labels,omitempty"`
	Annotations     map[string]string `json:"annotations,omitempty"`
	ResourceVersion string            `json:"resourceVersion,omitempty"`
	// Not Dormeto's: read so that an update keeps them.
	OwnerReferences json.RawMessage `json:"ownerReferences,omitempty"`
	Finalizers      []string        `json:"finalizers,omitempty"`
}

// kubeObject is a Role or a RoleBinding.
type kubeObject struct {
	APIVersion string        `json:"apiVersion"`
	Kind       string        `json:"kind"`
	Metadata   kubeMetadata  `json:"metadata"`
	Rules      []rbacRule    `json:"rules,omitempty"`
	Subjects   []rbacSubject `json:"subjects,omitempty"`
	RoleRef    *rbacRoleRef  `json:"roleRef,omitempty"`
}

func (o kubeObject) String() string { return o.Kind + " " + o.Metadata.Name }

// ours tells that the object is Dormeto's: the label and the name.
func (o kubeObject) ours(name string) bool {
	return o.Metadata.Name == name && o.Metadata.Labels[managedByLabel] == managedBy
}

// sameContent tells that the object says what the wanted one says: its metadata is its own.
func (o kubeObject) sameContent(wanted kubeObject) bool {
	return reflect.DeepEqual(o.Rules, wanted.Rules) && reflect.DeepEqual(o.Subjects, wanted.Subjects) && reflect.DeepEqual(o.RoleRef, wanted.RoleRef)
}

func (rb Rbac) name() string { return "dormeto-" + rb.Workload }

func (rb Rbac) metadata() kubeMetadata {
	return kubeMetadata{Name: rb.name(), Namespace: rb.Namespace, Labels: map[string]string{managedByLabel: managedBy}}
}

// role is the Role of spec §8: what the router's nap, wake and checks read and scale in the namespace.
func (rb Rbac) role() kubeObject {
	return kubeObject{APIVersion: rbacAPIVersion, Kind: "Role", Metadata: rb.metadata(), Rules: []rbacRule{
		{APIGroups: []string{"apps"}, Resources: []string{"deployments", "statefulsets", "replicasets"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{"apps"}, Resources: []string{"deployments/scale", "statefulsets/scale"}, Verbs: []string{"get", "patch"}},
		{APIGroups: []string{""}, Resources: []string{"pods", "services", "events"}, Verbs: []string{"get", "list", "watch"}},
		{APIGroups: []string{"keda.sh"}, Resources: []string{"scaledobjects"}, Verbs: []string{"get", "list", "watch", "patch"}},
	}}
}

func (rb Rbac) roleBinding() kubeObject {
	return kubeObject{APIVersion: rbacAPIVersion, Kind: "RoleBinding", Metadata: rb.metadata(),
		Subjects: []rbacSubject{{Kind: "User", Name: rb.ServiceAccount, APIGroup: rbacAPIGroup}},
		RoleRef:  &rbacRoleRef{APIGroup: rbacAPIGroup, Kind: "Role", Name: rb.name()}}
}

const (
	rbacCreate  = "create"
	rbacUpdate  = "update"
	rbacNone    = "none"
	rbacRefused = "refused"
)

// rbacChange is what a grant would do to one object: create, update, none, or refused (the object is someone else's, or
// binds OtherRouters).
type rbacChange struct {
	Action       string      `json:"action"`
	Current      *kubeObject `json:"current"`
	Wanted       kubeObject  `json:"wanted"`
	OtherRouters []string    `json:"otherRouters,omitempty"`
}

// roleRefDiffers tells that an update would change the binding's roleRef, which Kubernetes refuses.
func (c rbacChange) roleRefDiffers() bool {
	return c.Current != nil && !reflect.DeepEqual(c.Current.RoleRef, c.Wanted.RoleRef)
}

// roleRefFixed is Kubernetes's refusal of a roleRef changed: no retry of the update ever ends.
type roleRefFixed struct{ error }

func (e *roleRefFixed) Unwrap() error { return e.error }

// isInvalid tells that Kubernetes answered 422.
func isInvalid(err error) bool {
	var e *gcp.Error
	return errors.As(err, &e) && e.Code == http.StatusUnprocessableEntity
}

// routerRefusal is the refusal of a binding that names another router: nil when it names none.
func (c rbacChange) routerRefusal() error {
	if len(c.OtherRouters) == 0 {
		return nil
	}
	return &stop{1, fmt.Sprintf("%s in namespace %s binds %s, another router's service account: two routers cannot share one %s, so Dormeto changes neither. Take that subject out by hand if it should not be there, or use another workload ID.\nNothing was changed.",
		c.Wanted, c.Wanted.Metadata.Namespace, strings.Join(c.OtherRouters, ", "), c.Wanted.Metadata.Name)}
}

// RbacPlan is what is in the cluster and what a grant would write.
type RbacPlan struct {
	Rbac
	Endpoint    string     `json:"endpoint"`
	Role        rbacChange `json:"role"`
	RoleBinding rbacChange `json:"roleBinding"`
}

// Changes tells that a grant would write something.
func (p RbacPlan) Changes() bool {
	return p.Role.Action != rbacNone || p.RoleBinding.Action != rbacNone
}

// Refused is the refusal of an object of that name that is someone else's, or that names another router: nil when there
// is none.
func (p RbacPlan) Refused() error {
	if err := p.RoleBinding.routerRefusal(); err != nil {
		return err
	}
	for _, c := range []rbacChange{p.Role, p.RoleBinding} {
		if c.Action == rbacRefused {
			return &stop{1, fmt.Sprintf("%s exists in namespace %s and is not Dormeto's (it lacks the label %s: %s): rename it, or name the workload otherwise.\nNothing was changed.",
				c.Wanted, p.Namespace, managedByLabel, managedBy)}
		}
	}
	return nil
}

// clusterGone is a cluster Google does not know.
type clusterGone struct{ cluster, project string }

func (e *clusterGone) Error() string {
	return fmt.Sprintf("Cluster %s is not found in project %s.\nNothing was changed.", e.cluster, e.project)
}

// clusterEndpoint reads the cluster's DNS endpoint. The router reaches a cluster by it alone (no IP endpoint, no VPC
// connector), so one that is off or closed to the outside is refused with what to turn on.
func (x *run) clusterEndpoint(cluster string) (string, error) {
	location, name, _ := strings.Cut(cluster, "/")
	var got struct {
		ControlPlaneEndpointsConfig struct {
			DNSEndpointConfig struct {
				Endpoint             string `json:"endpoint"`
				AllowExternalTraffic bool   `json:"allowExternalTraffic"`
			} `json:"dnsEndpointConfig"`
		} `json:"controlPlaneEndpointsConfig"`
	}
	u := containerV1 + "projects/" + esc(x.n.project) + "/locations/" + esc(location) + "/clusters/" + esc(name)
	err := x.d.Google.Do(x.ctx, http.MethodGet, u, nil, &got)
	switch {
	case gcp.IsNotFound(err):
		return "", &clusterGone{cluster, x.n.project}
	case gcp.IsDenied(err) && !serviceDisabled(err):
		return "", &stop{1, fmt.Sprintf("Cluster %s is not read: you need container.clusters.get on project %s (%s).\nNothing was changed.", cluster, x.n.project, refusal(err))}
	case refused(err):
		return "", &stop{1, fmt.Sprintf("Cluster %s is not read: %s\nNothing was changed.", cluster, refusal(err))}
	case err != nil:
		return "", err
	}
	dns := got.ControlPlaneEndpointsConfig.DNSEndpointConfig
	if dns.Endpoint == "" || !dns.AllowExternalTraffic {
		hint := fmt.Sprintf("gcloud container clusters update %s --location %s --project %s --enable-dns-access", name, location, x.n.project)
		if x.r.Configuration != "" {
			hint += " --configuration " + x.r.Configuration
		}
		return "", &stop{1, fmt.Sprintf("Cluster %s has no DNS endpoint open to traffic from outside its network, and that is how Dormeto reaches a cluster. Turn it on:\n  %s\nNothing was changed.", cluster, hint)}
	}
	if !dnsEndpointPattern.MatchString(dns.Endpoint) {
		return "", &stop{1, fmt.Sprintf("Cluster %s: Google names %q as its DNS endpoint, which is not a host under gke.goog, so your access token is not sent there.\nNothing was changed.", cluster, dns.Endpoint)}
	}
	return dns.Endpoint, nil
}

// kubeDo is a call of the cluster's API. A redirect is not followed, as the operator's token would go with it: its answer
// is an error.
func (x *run) kubeDo(method, u string, in, out any) error {
	c := *x.d.Google
	h := http.Client{}
	if c.HTTP != nil {
		h = *c.HTTP
	}
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.HTTP = &h
	return c.Do(x.ctx, method, u, in, out)
}

func kubeCollectionURL(endpoint string, o kubeObject) string {
	return "https://" + endpoint + "/apis/" + rbacAPIVersion + "/namespaces/" + esc(o.Metadata.Namespace) + "/" + strings.ToLower(o.Kind) + "s"
}

func kubeObjectURL(endpoint string, o kubeObject) string {
	return kubeCollectionURL(endpoint, o) + "/" + esc(o.Metadata.Name)
}

// kubeFailure says why Kubernetes answered no to a verb (get, create, update or delete) on an object: a 403 names what the
// operator lacks. Any other refusal is said in Kubernetes's words. No answer, a 5xx or a redirect says nothing of what was
// done: only a *stop means that Kubernetes refused.
func kubeFailure(cluster, verb string, o kubeObject, err error) error {
	what := fmt.Sprintf("%s in namespace %s", o, o.Metadata.Namespace)
	var e *gcp.Error
	switch {
	case errors.As(err, &e) && e.Code >= 300 && e.Code < 400:
		return fmt.Errorf("%s %s: the cluster's endpoint answers %d, a redirect, which is not followed as your access token would go with it", verb, what, e.Code)
	case !refused(err) || !errors.As(err, &e):
		// As text: the error's own text would be the answer's alone, without the call.
		return fmt.Errorf("%s %s: %s", verb, what, refusal(err))
	case e.Code != http.StatusForbidden:
		return &stop{1, fmt.Sprintf("Kubernetes refuses to %s %s: %s", verb, what, e.Message)}
	case e.Reason != "Forbidden":
		// Not a Kubernetes Status: the endpoint's own IAM check, before the cluster's API.
		return &stop{1, fmt.Sprintf("The DNS endpoint of cluster %s refuses you: you need container.clusters.connect on its project.\n%s", cluster, e.Message)}
	case escalation(e.Message):
		return &stop{1, fmt.Sprintf("Kubernetes refuses to %s %s: it lets no one grant rights they do not hold. You need container.roles.escalate and container.roles.bind, or those rights themselves.\n%s", verb, what, e.Message)}
	}
	return &stop{1, fmt.Sprintf("Kubernetes refuses to %s %s: you need %s, by IAM or by Kubernetes RBAC.\n%s", verb, what, kubePermission(verb, o), e.Message)}
}

// escalation tells that Kubernetes's 403 is its escalation rule, and no missing right on the object itself.
func escalation(message string) bool {
	m := strings.ToLower(message)
	return strings.Contains(m, "not currently held") || strings.Contains(m, "escalat") || strings.Contains(m, "extra privileges")
}

// kubePermission is the IAM permission of a verb on a Role or a RoleBinding.
func kubePermission(verb string, o kubeObject) string {
	if o.Kind == "RoleBinding" {
		return "container.roleBindings." + verb
	}
	return "container.roles." + verb
}

const nothingChanged = "Nothing was changed."

// withNothingChanged adds that nothing changed to a refusal that does not say it yet.
func withNothingChanged(err error) error {
	var s *stop
	if errors.As(err, &s) && !strings.HasSuffix(s.msg, nothingChanged) {
		return &stop{s.code, s.msg + "\n" + nothingChanged}
	}
	return err
}

// recordFailure says what was not done with the install record. A refusal is said whole: the error's text would be Google's alone.
func recordFailure(what string, err error) error {
	if refused(err) {
		return &stop{1, what + ": " + refusal(err)}
	}
	return fmt.Errorf("%s: %s", what, refusal(err))
}

// readChange reads an object and compares it with the wanted one.
func (x *run) readChange(rb Rbac, endpoint string, wanted kubeObject) (rbacChange, error) {
	var current kubeObject
	err := x.kubeDo(http.MethodGet, kubeObjectURL(endpoint, wanted), nil, &current)
	switch {
	case gcp.IsNotFound(err):
		return rbacChange{Action: rbacCreate, Wanted: wanted}, nil
	case err != nil:
		return rbacChange{}, withNothingChanged(kubeFailure(rb.Cluster, "get", wanted, err))
	}
	c := rbacChange{Action: rbacUpdate, Current: &current, Wanted: wanted}
	if !current.ours(rb.name()) {
		c.Action = rbacRefused
		return c, nil
	}
	if wanted.Kind == "RoleBinding" {
		if c.OtherRouters = rb.otherRouters(current); len(c.OtherRouters) > 0 {
			c.Action = rbacRefused
			return c, nil
		}
		c.Wanted.Subjects = rb.subjectsWithRouter(current)
	}
	if current.sameContent(c.Wanted) {
		c.Action = rbacNone
	}
	return c, nil
}

// routerSubject tells that a subject is this router's.
func (rb Rbac) routerSubject(s rbacSubject) bool {
	return s.Kind == "User" && s.Name == rb.ServiceAccount
}

// otherRouters are the subjects of a binding that are another router's service account: dmtr-<region>@…, a Kubernetes
// User or ServiceAccount, in this project or another.
func (rb Rbac) otherRouters(o kubeObject) []string {
	var out []string
	for _, s := range o.Subjects {
		if (s.Kind == "User" || s.Kind == "ServiceAccount") && s.Name != rb.ServiceAccount && strings.HasPrefix(s.Name, "dmtr-") && strings.Contains(s.Name, "@") {
			out = append(out, s.String())
		}
	}
	return out
}

// subjectsWithRouter are a binding's subjects as they stand, the router's among them.
func (rb Rbac) subjectsWithRouter(o kubeObject) []rbacSubject {
	if slices.ContainsFunc(o.Subjects, rb.routerSubject) {
		return o.Subjects
	}
	return append(slices.Clone(o.Subjects), rb.roleBinding().Subjects...)
}

// PlanRbac reads the cluster's DNS endpoint and the workload's objects, and compares them with what they should be. It
// changes nothing. A cluster with no DNS endpoint is an error; an object that is someone else's is the plan's Refused.
func (x *run) PlanRbac(rb Rbac) (RbacPlan, error) {
	plan := RbacPlan{Rbac: rb}
	var err error
	if plan.Endpoint, err = x.clusterEndpoint(rb.Cluster); err != nil {
		return plan, err
	}
	if plan.Role, err = x.readChange(rb, plan.Endpoint, rb.role()); err != nil {
		return plan, err
	}
	plan.RoleBinding, err = x.readChange(rb, plan.Endpoint, rb.roleBinding())
	return plan, err
}

// ApplyRbac writes what the plan says differs, the Role first as the binding names it, and returns what it did, also when
// it fails on the way. An error that is a *stop is Kubernetes's refusal of the write after those. A plan refused writes
// nothing.
func (x *run) ApplyRbac(plan RbacPlan) ([]string, error) {
	if err := plan.Refused(); err != nil {
		return nil, err
	}
	done := []string{}
	for _, c := range []rbacChange{plan.Role, plan.RoleBinding} {
		switch c.Action {
		case rbacCreate:
			if err := x.kubeDo(http.MethodPost, kubeCollectionURL(plan.Endpoint, c.Wanted), c.Wanted, nil); err != nil {
				return done, kubeFailure(plan.Cluster, "create", c.Wanted, err)
			}
			done = append(done, "created "+c.Wanted.String())
		case rbacUpdate:
			// Its metadata stays, with the version read: someone's write since is a conflict, not lost.
			o := c.Wanted
			o.Metadata = c.Current.Metadata
			if err := x.kubeDo(http.MethodPut, kubeObjectURL(plan.Endpoint, o), o, nil); err != nil {
				if c.roleRefDiffers() && isInvalid(err) {
					return done, &roleRefFixed{kubeFailure(plan.Cluster, "update", o, err)}
				}
				return done, kubeFailure(plan.Cluster, "update", o, err)
			}
			done = append(done, "updated "+c.Wanted.String())
		}
	}
	return done, nil
}

// RbacRemoval is what a removal did: what it removed, the objects it left (someone else's, or ours while another subject
// holds them), and the subjects that still hold those of ours.
type RbacRemoval struct {
	Removed  []string `json:"removed"`
	Left     []string `json:"left"`
	Subjects []string `json:"subjects,omitempty"`
}

// rbacRemovalPlan is what a removal does.
type rbacRemovalPlan struct {
	deleted  []rbacChange // the objects it deletes, the binding first as the router then loses its rights before the Role goes
	stripped *kubeObject  // our binding without the router's subject, when others remain
	subject  string       // the router's subject, when stripped
	kept     []keptObject // our objects that stay, for those a binding may still give them to
	foreign  []string     // the objects that are someone else's
	others   []string     // the subjects that remain on our binding
}

// keptObject is one of our objects that a removal leaves, and why.
type keptObject struct{ name, why string }

func (r rbacRemovalPlan) left() []string {
	out := slices.Clone(r.foreign)
	for _, k := range r.kept {
		out = append(out, k.name)
	}
	return out
}

// removal sorts the objects there. While a subject other than the router's holds our binding, the binding stays without the
// router's subject, and the Role with it; both go only when the router is the sole subject. A binding of that name that is
// not ours may name our Role, and its subjects would be left with nothing: our Role stays too. Asked once routerRefusal is
// nil: another router's subject leaves everything.
func (p RbacPlan) removal() rbacRemovalPlan {
	var out rbacRemovalPlan
	out.others = p.otherSubjects()
	bindingForeign := p.RoleBinding.Current != nil && p.RoleBinding.Action == rbacRefused
	for _, c := range []rbacChange{p.RoleBinding, p.Role} {
		switch {
		case c.Current == nil:
		case c.Action == rbacRefused:
			out.foreign = append(out.foreign, c.Wanted.String())
		case bindingForeign:
			out.kept = append(out.kept, keptObject{c.Wanted.String(), p.RoleBinding.Wanted.String() + ", not Dormeto's, may name it"})
		case len(out.others) > 0 && c.Wanted.Kind == "RoleBinding":
			out.kept = append(out.kept, keptObject{c.Wanted.String(), "it also binds " + strings.Join(out.others, ", ")})
		case len(out.others) > 0:
			out.kept = append(out.kept, keptObject{c.Wanted.String(), "that binding names it"})
		default:
			out.deleted = append(out.deleted, c)
		}
	}
	if len(out.others) > 0 {
		binding := *p.RoleBinding.Current
		binding.Subjects = slices.DeleteFunc(slices.Clone(binding.Subjects), p.routerSubject)
		if len(binding.Subjects) != len(p.RoleBinding.Current.Subjects) {
			out.stripped = &binding
			out.subject = "subject " + p.roleBinding().Subjects[0].String() + " of " + binding.String()
		}
	}
	return out
}

// otherSubjects are the subjects of our binding other than the router's: someone added them by hand.
func (p RbacPlan) otherSubjects() []string {
	var out []string
	if c := p.RoleBinding.Current; c != nil && p.RoleBinding.Action != rbacRefused {
		for _, s := range c.Subjects {
			if !p.routerSubject(s) {
				out = append(out, s.String())
			}
		}
	}
	return out
}

// there are the objects that exist.
func (p RbacPlan) there() []string {
	out := []string{}
	for _, c := range []rbacChange{p.RoleBinding, p.Role} {
		if c.Current != nil {
			out = append(out, c.Wanted.String())
		}
	}
	return out
}

// deleteOptions make a delete of what was read: an object written since is a conflict, not lost.
func deleteOptions(o kubeObject) any {
	return map[string]any{"apiVersion": "v1", "kind": "DeleteOptions", "preconditions": map[string]string{"resourceVersion": o.Metadata.ResourceVersion}}
}

// RemoveRbac reads the workload's objects again and removes what the router holds of them, with no prompt: its subject, or
// both objects when it is the sole subject. An object not there is no error, and another of that name is left, and said. A
// binding naming another router is refused, and leaves everything. It returns what it did and what it left, also when it
// fails on the way. An error that is a *stop is Kubernetes's refusal, or that of another router.
func (x *run) RemoveRbac(rb Rbac) (RbacRemoval, error) {
	out := RbacRemoval{Removed: []string{}, Left: []string{}}
	plan, err := x.PlanRbac(rb)
	if err != nil {
		return out, err
	}
	if err := plan.RoleBinding.routerRefusal(); err != nil {
		out.Left = plan.there()
		return out, err
	}
	removal := plan.removal()
	out.Left, out.Subjects = removal.left(), removal.others
	if o := removal.stripped; o != nil {
		// With the version read: someone's write since is a conflict, not lost.
		if err := x.kubeDo(http.MethodPut, kubeObjectURL(plan.Endpoint, *o), *o, nil); err != nil && !gcp.IsNotFound(err) {
			return out, kubeFailure(rb.Cluster, "update", *o, err)
		}
		out.Removed = append(out.Removed, removal.subject)
	}
	for i, c := range removal.deleted {
		o := *c.Current
		if err := x.kubeDo(http.MethodDelete, kubeObjectURL(plan.Endpoint, o), deleteOptions(o), nil); err != nil && !gcp.IsNotFound(err) {
			for _, rest := range removal.deleted[i:] {
				out.Left = append(out.Left, rest.Wanted.String())
			}
			return out, kubeFailure(rb.Cluster, "delete", o, err)
		}
		out.Removed = append(out.Removed, c.Wanted.String())
	}
	return out, nil
}

// addRbac adds a workload's access the record lacks, and tells whether it did.
func (r *Record) addRbac(e RecordRbac) bool {
	if slices.Contains(r.Rbac, e) {
		return false
	}
	r.Rbac = append(r.Rbac, e)
	return true
}

// removeRbac removes a workload's access from the record, and tells whether it was listed.
func (r *Record) removeRbac(e RecordRbac) bool {
	i := slices.Index(r.Rbac, e)
	if i < 0 {
		return false
	}
	r.Rbac = slices.Delete(r.Rbac, i, i+1)
	return true
}

// rbacRecord reads the install record where it is kept, and tells whether there is one. What was removed needs none: a
// revoke follows an uninstall. An API that is off holds no record.
func (x *run) rbacRecord() (bool, error) {
	x.policy, x.turningOn = &Policy{}, []string{firestoreAPI}
	if err := x.loadRecord(); err != nil {
		return false, withNothingChanged(recordFailure("The install record is not read", err))
	}
	return x.recorded || x.resumed, nil
}

// writeRbacRecord writes the record where it is kept, by the account that changes it.
func (x *run) writeRbacRecord() error {
	account, err := x.d.Google.Tokens.Account(x.ctx)
	if err != nil {
		return err
	}
	x.account = account
	return x.writeKept()
}

// recordRbac lists a workload's access in the record, before it is applied, and tells whether it did. The record follows
// rbacRecord.
func (x *run) recordRbac(rb Rbac) (bool, error) {
	if !x.rec.addRbac(rb.entry()) {
		return false, nil
	}
	if err := x.writeRbacRecord(); err != nil {
		x.rec.removeRbac(rb.entry())
		return false, withNothingChanged(recordFailure("The install record is not written before the access is applied", err))
	}
	return true, nil
}

// unrecordRbac takes a workload's access out of the record, after its removal. The record follows rbacRecord.
func (x *run) unrecordRbac(rb Rbac) error {
	if !x.rec.removeRbac(rb.entry()) {
		return nil
	}
	return x.writeRbacRecord()
}

// revokeCommand is the command that removes a workload's access.
func (x *run) revokeCommand(e RecordRbac) string {
	c := fmt.Sprintf("dormeto gcp workloads access revoke --project %s --region %s --gke-cluster %s --namespace %s --workload %s",
		x.n.project, x.n.region, e.Cluster, e.Namespace, e.Workload)
	if x.r.Configuration != "" {
		c += " --gcloud-configuration " + x.r.Configuration
	}
	return c
}

// rbacLeft are the lines of the access the record lists: the uninstall does not reach clusters, as the operator's access to
// a cluster may differ from their access to the project.
func (x *run) rbacLeft() []string {
	var lines []string
	for _, e := range x.rec.Rbac {
		lines = append(lines, fmt.Sprintf("Role and RoleBinding dormeto-%s, namespace %s, cluster %s: left in the cluster; remove them with: %s", e.Workload, e.Namespace, e.Cluster, x.revokeCommand(e)))
	}
	return lines
}

// WorkloadsAccessShow is `dormeto gcp workloads access show`: what is in the cluster and what grant would write.
func WorkloadsAccessShow(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.accessShow())
}

// WorkloadsAccessGrant is `dormeto gcp workloads access grant`.
func WorkloadsAccessGrant(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.accessGrant())
}

// WorkloadsAccessRevoke is `dormeto gcp workloads access revoke`.
func WorkloadsAccessRevoke(ctx context.Context, d Deps, r Request) int {
	x := newRun(ctx, d, r)
	return x.workloadsEnd(x.accessRevoke())
}

// rbacRequest validates the flags, before any call. A write with --json is not asked: the answer is for a program.
func (x *run) rbacRequest(writes bool) (Rbac, error) {
	if err := x.validate(); err != nil {
		return Rbac{}, err
	}
	location, name, found := strings.Cut(x.r.GKECluster, "/")
	switch {
	case !found || !clusterLocationPattern.MatchString(location) || !clusterNamePattern.MatchString(name):
		return Rbac{}, &stop{2, fmt.Sprintf("--gke-cluster: %q is not <location>/<name>, such as europe-west1-b/my-cluster", x.r.GKECluster)}
	case !namespacePattern.MatchString(x.r.Namespace):
		return Rbac{}, &stop{2, fmt.Sprintf("--namespace: %q is not a Kubernetes namespace", x.r.Namespace)}
	case !workloadIDPattern.MatchString(x.r.ID):
		return Rbac{}, &stop{2, fmt.Sprintf("--workload: %q is not a valid workload ID: %s", x.r.ID, workloadIDRule)}
	case writes && x.r.JSON && !x.r.Yes:
		return Rbac{}, &stop{2, "--yes: needed with --json"}
	}
	return Rbac{Cluster: x.r.GKECluster, Namespace: x.r.Namespace, Workload: x.r.ID, ServiceAccount: x.n.email}, nil
}

// objectLines describes an object's content.
func objectLines(o kubeObject) []string {
	var lines []string
	for _, r := range o.Rules {
		groups := slices.Clone(r.APIGroups)
		for i, g := range groups {
			if g == "" {
				groups[i] = `""`
			}
		}
		named := ""
		if len(r.ResourceNames) > 0 {
			named = " (named " + strings.Join(r.ResourceNames, ", ") + ")"
		}
		lines = append(lines, fmt.Sprintf("%s: %s%s: %s", strings.Join(groups, ", "), strings.Join(r.Resources, ", "), named, strings.Join(r.Verbs, ", ")))
	}
	for _, s := range o.Subjects {
		lines = append(lines, "subject: "+s.Kind+" "+s.Name)
	}
	if o.RoleRef != nil {
		lines = append(lines, "role: "+o.RoleRef.Kind+" "+o.RoleRef.Name)
	}
	var labels []string
	for k, v := range o.Metadata.Labels {
		labels = append(labels, "label "+k+": "+v)
	}
	sort.Strings(labels)
	return append(lines, labels...)
}

// tell is a line of text, that --json does not print.
func (x *run) tell(format string, a ...any) {
	if !x.r.JSON {
		x.say(format, a...)
	}
}

func (x *run) rbacHeader(p RbacPlan) {
	x.tell("Kubernetes access of workload %s, namespace %s, cluster %s (%s):", p.Workload, p.Namespace, p.Cluster, p.Endpoint)
}

// printWrites prints what a grant would write, each object in full; with replaced, what an update replaces too.
func (x *run) printWrites(p RbacPlan, replaced bool) {
	for _, c := range []rbacChange{p.Role, p.RoleBinding} {
		switch c.Action {
		case rbacNone:
			x.say("  = %s, as it should be", c.Wanted)
			continue
		case rbacRefused:
			if len(c.OtherRouters) > 0 {
				x.say("  ! %s binds %s, another router's: grant refuses it", c.Wanted, strings.Join(c.OtherRouters, ", "))
				continue
			}
			x.say("  ! %s is not Dormeto's: grant refuses it, and would write:", c.Wanted)
		case rbacUpdate:
			x.say("  ~ %s", c.Wanted)
			if replaced {
				x.say("      now:")
				for _, l := range objectLines(*c.Current) {
					x.say("      %s", l)
				}
				x.say("      to be:")
			}
		default:
			x.say("  + %s", c.Wanted)
		}
		for _, l := range objectLines(c.Wanted) {
			x.say("      %s", l)
		}
	}
}

func (x *run) printJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	fmt.Fprintf(x.d.Out, "%s\n", b)
	return nil
}

func (x *run) accessShow() error {
	rb, err := x.rbacRequest(false)
	if err != nil {
		return err
	}
	plan, err := x.PlanRbac(rb)
	if err != nil {
		return err
	}
	if x.r.JSON {
		return x.printJSON(plan)
	}
	x.rbacHeader(plan)
	x.say("In the cluster:")
	for _, c := range []rbacChange{plan.Role, plan.RoleBinding} {
		switch {
		case c.Current == nil:
			x.say("  %s: none", c.Wanted)
			continue
		case len(c.OtherRouters) > 0:
			x.say("  %s: Dormeto's, and it binds another router", c.Wanted)
		case c.Action == rbacRefused:
			x.say("  %s: not Dormeto's", c.Wanted)
		default:
			x.say("  %s: Dormeto's", c.Wanted)
		}
		for _, l := range objectLines(*c.Current) {
			x.say("      %s", l)
		}
	}
	x.say("What grant would write, for the router's service account:")
	x.printWrites(plan, false)
	return nil
}

// confirm goes on with --yes or a yes on a terminal.
func (x *run) confirm(question string) error {
	if x.r.Yes {
		return nil
	}
	if !x.d.Terminal {
		return &stop{2, "--yes: needed"}
	}
	ok, err := install.Prompted(x.ctx, x.d.Out, func() (bool, error) { return x.d.Prompter.Confirm(question) })
	if err != nil {
		return err
	}
	if !ok {
		return &stop{0, nothingChanged}
	}
	return nil
}

// grantWarnings say what a grant cannot do alone, without refusing it: the feature gke is what gives the router
// container.clusters.connect, and without it the router's calls answer 403 for that reason, not for this access's.
func (x *run) grantWarnings() []string {
	if slices.Contains(x.rec.Features, "gke") {
		return nil
	}
	hint := fmt.Sprintf("dormeto gcp install --project %s --region %s --with gke", x.n.project, x.n.region)
	if x.r.Configuration != "" {
		hint += " --gcloud-configuration " + x.r.Configuration
	}
	return []string{fmt.Sprintf("the install record lacks the feature gke: router %s holds no container.clusters.connect, so its calls to this cluster would answer 403, whatever this Role allows. Add it with: %s", x.n.router, hint)}
}

func (x *run) noRouter() error {
	return &stop{1, fmt.Sprintf("No router %s is installed in project %s: there is no install record. Run dormeto gcp install first.\nNothing was changed.", x.n.router, x.n.project)}
}

func (x *run) accessGrant() error {
	rb, err := x.rbacRequest(true)
	if err != nil {
		return err
	}
	found, err := x.rbacRecord()
	if err != nil {
		return err
	}
	if !found {
		return x.noRouter()
	}
	plan, err := x.PlanRbac(rb)
	if err != nil {
		return err
	}
	x.rbacHeader(plan)
	warnings := x.grantWarnings()
	if !x.r.JSON {
		x.printWrites(plan, true)
		x.warn(warnings)
	}
	if err := plan.Refused(); err != nil {
		return err
	}
	done := []string{}
	if !plan.Changes() {
		// A record that lost it is whole again.
		if _, err := x.recordRbac(rb); err != nil {
			return err
		}
	} else {
		if err := x.confirm(fmt.Sprintf("Apply these to namespace %s of cluster %s?", rb.Namespace, rb.Cluster)); err != nil {
			return err
		}
		// A prompt can wait long: what is listed is read again, so that an entry revoked meanwhile is listed again.
		if found, err = x.rbacRecord(); err != nil {
			return err
		}
		if !found {
			return x.noRouter()
		}
		added, err := x.recordRbac(rb)
		if err != nil {
			return err
		}
		if done, err = x.applyGrant(rb, plan, added); err != nil {
			return err
		}
	}
	if x.r.JSON {
		return x.printJSON(struct {
			RbacPlan
			Changed  []string `json:"changed"`
			Warnings []string `json:"warnings,omitempty"`
		}{plan, done, warnings})
	}
	if len(done) == 0 {
		x.say("The Role and RoleBinding %s of namespace %s are up to date.", rb.name(), rb.Namespace)
	}
	return nil
}

// applyGrant applies the plan and says what it did. A first write refused made nothing: the record's entry this run added
// goes. Otherwise the entry stays, as the access is there or may be.
func (x *run) applyGrant(rb Rbac, plan RbacPlan, added bool) ([]string, error) {
	done, err := x.ApplyRbac(plan)
	for _, d := range done {
		x.tell("  ✓ %s", d)
	}
	if err == nil {
		return done, nil
	}
	var fixed *roleRefFixed
	if errors.As(err, &fixed) {
		next := "A RoleBinding's roleRef never changes: remove it first with: " + x.revokeCommand(rb.entry()) + ", then run this command again."
		if others := plan.otherSubjects(); len(others) > 0 {
			next = "A RoleBinding's roleRef never changes, and it also binds " + strings.Join(others, ", ") + ", so remove or rename it by hand, then run this command again."
		}
		return done, x.stopped(err, done, next)
	}
	var s *stop
	if errors.As(err, &s) && len(done) == 0 {
		switch {
		case !added:
			return done, withNothingChanged(err)
		case plan.Role.Current == nil && plan.RoleBinding.Current == nil:
			if err := x.unrecordRbac(rb); err != nil {
				fmt.Fprintf(x.d.Err, "  ! the install record still lists this access: %s\n", refusal(err))
			}
			return done, withNothingChanged(err)
		}
	}
	return done, x.stopped(err, done, fmt.Sprintf("The install record lists this access. Run the same command again to finish, or remove it with: %s", x.revokeCommand(rb.entry())))
}

// stopped is the end of a run that stopped on err, after doing done: what happened, what it did and what to do. --json
// prints no line of it otherwise.
func (x *run) stopped(err error, done []string, next string) error {
	code, msg := 1, refusal(err)
	var s *stop
	switch {
	case errors.As(err, &s):
		code, msg = s.code, s.msg
	case x.ctx.Err() != nil:
		code, msg = exitInterrupted, "Interrupted."
	}
	if len(done) > 0 {
		msg += "\nDone: " + strings.Join(done, ", ") + "."
	}
	return &stop{code, msg + "\n" + next}
}

func (x *run) accessRevoke() error {
	rb, err := x.rbacRequest(true)
	if err != nil {
		return err
	}
	if _, err := x.rbacRecord(); err != nil {
		return err
	}
	plan, err := x.PlanRbac(rb)
	var gone *clusterGone
	switch {
	case errors.As(err, &gone):
		x.tell("Cluster %s is not found in project %s: no access is left in it.", gone.cluster, gone.project)
		return x.endRevoke(rb, RbacRemoval{Removed: []string{}, Left: []string{}})
	case err != nil:
		return err
	}
	x.rbacHeader(plan)
	if err := plan.RoleBinding.routerRefusal(); err != nil {
		return err
	}
	removal := plan.removal()
	for _, c := range removal.deleted {
		x.tell("  - %s", c.Wanted)
	}
	if removal.stripped != nil {
		x.tell("  - %s", removal.subject)
	}
	for _, l := range removal.foreign {
		x.tell("  = %s is not Dormeto's: left as it is", l)
	}
	for _, k := range removal.kept {
		x.tell("  = %s stays: %s", k.name, k.why)
	}
	if len(removal.deleted) == 0 && removal.stripped == nil {
		x.tell("  nothing to remove")
		return x.endRevoke(rb, RbacRemoval{Removed: []string{}, Left: removal.left(), Subjects: removal.others})
	}
	if err := x.confirm(fmt.Sprintf("Remove these from namespace %s of cluster %s?", rb.Namespace, rb.Cluster)); err != nil {
		return err
	}
	out, err := x.RemoveRbac(rb)
	for _, r := range out.Removed {
		x.tell("  ✓ removed %s", r)
	}
	if err == nil {
		return x.endRevoke(rb, out)
	}
	var s *stop
	if errors.As(err, &s) && len(out.Removed) == 0 {
		return withNothingChanged(err)
	}
	var removed []string
	for _, r := range out.Removed {
		removed = append(removed, "removed "+r)
	}
	return x.stopped(err, removed, "The install record still lists this access. Run the same command again to finish.")
}

// endRevoke takes the access out of the record, once nothing of it is left in the cluster, and says so.
func (x *run) endRevoke(rb Rbac, out RbacRemoval) error {
	if err := x.unrecordRbac(rb); err != nil {
		return recordFailure("The access is removed, but the install record still lists it (run the same command again to finish)", err)
	}
	if !x.r.JSON {
		return nil
	}
	return x.printJSON(struct {
		Cluster   string `json:"cluster"`
		Namespace string `json:"namespace"`
		Workload  string `json:"workload"`
		RbacRemoval
	}{rb.Cluster, rb.Namespace, rb.Workload, out})
}
