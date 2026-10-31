package gcponboard

// What `dormeto gcp workloads onboard` and `offboard` do for the optional features: the install record must hold those a flag
// needs; a Cloud SQL companion's tag; a GKE workload's owner, read as the operator, and its Kubernetes RBAC, undone with a
// refused onboarding and removed with the offboarding.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Dormeto/cli/internal/gcp"
	"github.com/Dormeto/cli/internal/kubernetes"
)

const sqlAdminV1 = "https://sqladmin.googleapis.com/v1/"

// onboardFeatures are the features the flags of an onboarding need.
func (x *run) onboardFeatures() []string {
	var features []string
	if len(x.r.CloudSQL) > 0 {
		features = append(features, "cloud-sql")
	}
	if x.r.GKECluster != "" {
		features = append(features, "gke")
	}
	return features
}

// needFeatures stops an onboarding whose flags need features the router's install record lacks: Google would refuse the
// router's calls, for a reason no refusal of its would say.
func (x *run) needFeatures(features []string) error {
	if len(features) == 0 {
		return nil
	}
	recorded, err := recordedFeatures(x)
	if err != nil {
		return withNothingChanged(recordFailure("The install record is not read, and it says which features the router has", err))
	}
	var missing []string
	for _, f := range features {
		if !slices.Contains(recorded, f) {
			missing = append(missing, f)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &stop{1, fmt.Sprintf("The install record of router %s lacks %s, which this onboarding needs. Add it with: %s\n%s", x.n.router, strings.Join(missing, ", "), x.installWith(missing...), nothingChanged)}
}

// databaseTarget is a Cloud SQL instance as a tag binding names it: its parent, by the project's ID and the instance's name,
// at the endpoint of the instance's region, which only the instance says. Its form is M7's to confirm, here alone. found false:
// Cloud SQL knows no such instance, and an instance gone holds no binding.
func (x *run) databaseTarget(name string) (t target, found bool, err error) {
	var instance struct {
		Region string `json:"region"`
	}
	err = x.d.Google.Do(x.ctx, http.MethodGet, sqlAdminV1+"projects/"+esc(x.n.project)+"/instances/"+esc(name), nil, &instance)
	switch {
	case gcp.IsNotFound(err):
		return target{}, false, nil
	case gcp.IsDenied(err):
		return target{}, false, fmt.Errorf("you need cloudsql.instances.get on project %s, or it does not exist (%s)", x.n.project, refusal(err))
	case err != nil:
		return target{}, false, err
	case instance.Region == "":
		return target{}, false, fmt.Errorf("Cloud SQL answers no region for it")
	}
	return target{label: backend{Kind: kindDatabase, Name: name}.String(), location: instance.Region,
		resource: "//sqladmin.googleapis.com/projects/" + x.n.project + "/instances/" + name}, true, nil
}

// onboardingRbac is the access of a workload the dry run placed.
func (x *run) onboardingRbac(id string, gke *gkeRef) Rbac {
	return Rbac{Cluster: x.r.GKECluster, Namespace: gke.Namespace, Workload: id, ServiceAccount: x.n.email}
}

// warnStandard says what spec §8's limits say of a Standard cluster, without stopping anything. Under --json, on the error
// stream, as the answer stays JSON.
func (x *run) warnStandard(dry dryRun) {
	if dry.GKE == nil || dry.GKE.Autopilot {
		return
	}
	w := x.d.Out
	if x.r.JSON {
		w = x.d.Err
	}
	fmt.Fprintf(w, "  ! Cluster %s is a Standard cluster: scaling pods saves money only on Autopilot, or with the node autoscaler freeing a node; its fixed nodes are paid all the same.\n", x.r.GKECluster)
}

// kubeReader is the lookup's client of a cluster: kubeDo, the operator's access token on its DNS endpoint. Its errors are the
// lookup's.
type kubeReader struct {
	x        *run
	endpoint string
}

func (k kubeReader) read(path string, query url.Values) (json.RawMessage, error) {
	u := "https://" + k.endpoint + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var raw json.RawMessage
	err := k.x.kubeDo(http.MethodGet, u, nil, &raw)
	var e *gcp.Error
	if errors.As(err, &e) {
		return nil, &kubernetes.Error{Status: e.Code, Reason: e.Reason, Message: e.Message}
	}
	return raw, err
}

// Get reads an object.
func (k kubeReader) Get(_ context.Context, path string) (json.RawMessage, error) {
	return k.read(path, nil)
}

// List reads every item of a collection, following the API's continue tokens.
func (k kubeReader) List(_ context.Context, path string, query map[string]string) ([]json.RawMessage, error) {
	values := url.Values{}
	for name, value := range query {
		values.Set(name, value)
	}
	var items []json.RawMessage
	for {
		raw, err := k.read(path, values)
		if err != nil {
			return nil, err
		}
		var page struct {
			Metadata struct {
				Continue string `json:"continue"`
			} `json:"metadata"`
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return nil, fmt.Errorf("GET %s: %w", path, err)
		}
		items = append(items, page.Items...)
		if page.Metadata.Continue == "" {
			return items, nil
		}
		values.Set("continue", page.Metadata.Continue)
	}
}

// readOwner is the CLI's reading of the cluster, as the operator: the Service's owner, which the onboarding's request names.
// It changes nothing.
func (x *run) readOwner(rb Rbac, service string) (kubernetes.Owner, error) {
	endpoint, err := x.clusterEndpoint(rb.Cluster)
	if err != nil {
		return kubernetes.Owner{}, err
	}
	owner, reason, err := kubernetes.ServiceOwner(x.ctx, kubeReader{x, endpoint}, rb.Namespace, service)
	var e *kubernetes.Error
	switch {
	case errors.As(err, &e) && e.Status == http.StatusForbidden && e.Reason != "Forbidden":
		return kubernetes.Owner{}, &stop{1, endpointRefusal(rb.Cluster, e.Message) + "\n" + nothingChanged}
	case errors.As(err, &e) && e.Status == http.StatusForbidden:
		return kubernetes.Owner{}, &stop{1, fmt.Sprintf("Kubernetes refuses to show the owner of Service %s in namespace %s: you need container.services.get, container.pods.list and container.replicaSets.list, by IAM or by Kubernetes RBAC.\n%s\n%s", service, rb.Namespace, e.Message, nothingChanged)}
	case err != nil:
		return kubernetes.Owner{}, &stop{1, fmt.Sprintf("The owner of Service %s in namespace %s is not read: %s\n%s", service, rb.Namespace, err, nothingChanged)}
	case owner == nil:
		return kubernetes.Owner{}, &stop{1, fmt.Sprintf("Cluster %s, namespace %s: %s.\n%s", rb.Cluster, rb.Namespace, reason, nothingChanged)}
	}
	return *owner, nil
}

// undoGrant removes what this run's grant added to a cluster, and says what it did and what is left: an object it created, the
// router's subject it added to a binding that was there. What was there before stays, updated or not. The objects are read
// again, for the versions to delete and to find what someone else added since.
func (x *run) undoGrant(g rbacGrant) []string {
	rb, role, binding := g.plan.Rbac, g.plan.Role, g.plan.RoleBinding
	roleMade := slices.Contains(g.done, "created "+role.Wanted.String())
	bindingMade := slices.Contains(g.done, "created "+binding.Wanted.String())
	subjectAdded := slices.Contains(g.done, "updated "+binding.Wanted.String()) && !slices.ContainsFunc(binding.Current.Subjects, rb.routerSubject)
	if !roleMade && !bindingMade && !subjectAdded {
		return []string{fmt.Sprintf("The Role and RoleBinding %s of namespace %s were there before this run: they stay.", rb.name(), rb.Namespace)}
	}
	revoke := "Remove it with: " + x.revokeCommand(rb.entry())
	now, err := x.PlanRbac(rb)
	if err == nil {
		err = now.RoleBinding.routerRefusal()
	}
	if err != nil {
		return []string{fmt.Sprintf("The Kubernetes access this run added is not removed: %s\n%s", strings.TrimSuffix(refusal(err), "\n"+nothingChanged), revoke)}
	}
	var lines, removed []string
	whole := true
	notRemoved := func(what string, err error) {
		whole = false
		lines = append(lines, fmt.Sprintf("%s of namespace %s is not removed: %s\n%s", what, rb.Namespace, refusal(err), revoke))
	}
	stays := func(what, why string) {
		whole = false
		lines = append(lines, fmt.Sprintf("%s %s: it stays.", what, why))
	}
	remove := func(c rbacChange) {
		o := *c.Current
		if err := x.kubeDo(http.MethodDelete, kubeObjectURL(now.Endpoint, o), deleteOptions(o), nil); err != nil && !gcp.IsNotFound(err) {
			notRemoved(c.Wanted.String(), kubeFailure(rb.Cluster, "delete", o, err))
		} else {
			removed = append(removed, c.Wanted.String())
		}
	}
	others := now.otherSubjects()
	switch c := now.RoleBinding; {
	case c.Current == nil:
	case c.Action == rbacRefused:
		stays(c.Wanted.String(), "is not Dormeto's")
	case bindingMade && len(others) == 0:
		remove(c)
	case bindingMade || subjectAdded:
		whole = false // the binding stays
		o := *c.Current
		o.Subjects = slices.DeleteFunc(slices.Clone(o.Subjects), rb.routerSubject)
		subject := "subject " + rb.roleBinding().Subjects[0].String() + " of " + o.String()
		if err := x.kubeDo(http.MethodPut, kubeObjectURL(now.Endpoint, o), o, nil); err != nil && !gcp.IsNotFound(err) {
			notRemoved(subject, kubeFailure(rb.Cluster, "update", o, err))
		} else {
			removed = append(removed, subject)
		}
	default:
		stays(c.Wanted.String(), "was there before this run")
	}
	switch c := now.Role; {
	case c.Current == nil:
	case !roleMade:
		stays(c.Wanted.String(), "was there before this run")
	case c.Action == rbacRefused:
		stays(c.Wanted.String(), "is not Dormeto's")
	case len(others) > 0:
		stays(c.Wanted.String(), binding.Wanted.String()+", which also binds "+strings.Join(others, ", ")+", names it")
	default:
		remove(c)
	}
	if len(removed) > 0 {
		lines = append([]string{"The Kubernetes access this run added is removed: " + strings.Join(removed, ", ") + "."}, lines...)
	}
	if whole && g.listed {
		if err := x.unrecordRbac(rb); err != nil {
			lines = append(lines, "The install record still lists this access: "+refusal(err))
		}
	}
	return lines
}
