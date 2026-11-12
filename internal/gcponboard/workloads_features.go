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
	"os"
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
	case !regionPattern.MatchString(instance.Region):
		// It names the host the operator's token goes to.
		return target{}, false, fmt.Errorf("Cloud SQL answers %q as its region", instance.Region)
	}
	return target{label: backend{Kind: kindDatabase, Name: name}.String(), location: instance.Region, read: true,
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
	fmt.Fprintf(x.textOut(), "  ! Cluster %s is a Standard cluster: scaling pods saves money only on Autopilot, or with the node autoscaler freeing a node; its fixed nodes are paid all the same.\n", x.r.GKECluster)
}

// warnReplicas says that a database's read replicas keep running while the workload sleeps (M8: Google stops only the
// primary), without stopping anything. Under --json, on the error stream, as warnStandard.
func (x *run) warnReplicas(dry dryRun) {
	w := x.textOut()
	for _, d := range dry.Databases {
		if len(d.Replicas) > 0 {
			fmt.Fprintf(w, "  ! Cloud SQL instance %s has read replicas (%s): Google stops only the primary, its replicas keep running, and billing, while the workload sleeps.\n", d.Name, strings.Join(d.Replicas, ", "))
		}
	}
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
	case err != nil && x.ctx.Err() != nil:
		return kubernetes.Owner{}, err
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
// router's subject it added to a binding that was there. What was there before stays, updated or not, and an update is said. The
// objects are read again, for the versions to delete, and what a removal would leave stays here too: what someone else holds, or
// a Role while a binding names it.
func (x *run) undoGrant(g rbacGrant) []string {
	rb, role, binding := g.plan.Rbac, g.plan.Role, g.plan.RoleBinding
	did := func(verb string, c rbacChange) bool { return slices.Contains(g.done, verb+" "+c.Wanted.String()) }
	subjectAdded := did("updated", binding) && !slices.ContainsFunc(binding.Current.Subjects, rb.routerSubject)
	var updated []string
	if did("updated", role) {
		updated = append(updated, role.Wanted.String())
	}
	if did("updated", binding) && !subjectAdded {
		updated = append(updated, binding.Wanted.String())
	}
	updates := func(lines []string) []string {
		if len(updated) == 0 {
			return lines
		}
		return append(lines, "Of what was there, this run updated: "+strings.Join(updated, ", ")+".")
	}
	if !did("created", role) && !did("created", binding) && !subjectAdded {
		return updates([]string{fmt.Sprintf("The Role and RoleBinding %s of namespace %s were there before this run: they stay.", rb.name(), rb.Namespace)})
	}
	revoke := "Remove it with: " + x.revokeCommand(rb.entry())
	now, err := x.PlanRbac(rb)
	if err == nil {
		err = now.RoleBinding.routerRefusal()
	}
	if err != nil {
		return []string{fmt.Sprintf("The Kubernetes access this run added is not removed: %s\n%s", strings.TrimSuffix(refusal(err), "\n"+nothingChanged), revoke)}
	}
	removal := now.removal()
	var lines, removed []string
	whole := len(removal.foreign) == 0 && len(removal.kept) == 0
	for _, f := range removal.foreign {
		lines = append(lines, f+" is not Dormeto's: it stays.")
	}
	for _, k := range removal.kept {
		lines = append(lines, fmt.Sprintf("%s stays: %s.", k.name, k.why))
	}
	notRemoved := func(what string, err error) {
		whole = false
		lines = append(lines, fmt.Sprintf("%s of namespace %s is not removed: %s\n%s", what, rb.Namespace, refusal(err), revoke))
	}
	if c := now.RoleBinding; c.Current != nil && c.Action != rbacRefused && (subjectAdded || (did("created", binding) && len(removal.others) > 0)) {
		whole = false // the binding stays
		o := *c.Current
		o.Subjects = slices.DeleteFunc(slices.Clone(o.Subjects), rb.routerSubject)
		subject := "subject " + rb.roleBinding().Subjects[0].String() + " of " + o.String()
		if err := x.kubeDo(http.MethodPut, kubeObjectURL(now.Endpoint, o), o, nil); err != nil && !gcp.IsNotFound(err) {
			notRemoved(subject, kubeFailure(rb.Cluster, "update", o, err))
		} else {
			removed = append(removed, subject)
		}
	}
	for i, c := range removal.deleted {
		if !did("created", c) {
			whole = false
			lines = append(lines, c.Wanted.String()+" was there before this run: it stays.")
			continue
		}
		o := *c.Current
		if err := x.kubeDo(http.MethodDelete, kubeObjectURL(now.Endpoint, o), deleteOptions(o), nil); err != nil && !gcp.IsNotFound(err) {
			notRemoved(c.Wanted.String(), kubeFailure(rb.Cluster, "delete", o, err))
			for _, rest := range removal.deleted[i+1:] {
				lines = append(lines, rest.Wanted.String()+" stays, as that binding names it.")
			}
			break
		}
		removed = append(removed, c.Wanted.String())
	}
	if len(removed) > 0 {
		lines = append([]string{"The Kubernetes access this run added is removed: " + strings.Join(removed, ", ") + "."}, lines...)
	}
	if whole && g.listed {
		if err := x.unrecordRbac(rb); err != nil {
			lines = append(lines, "The install record still lists this access: "+refusal(err))
		}
	}
	return updates(lines)
}

// recordedAccess are the entries of a workload in the install record.
func (x *run) recordedAccess(id string) []RecordRbac {
	if x.rec == nil {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(x.rec.Rbac), func(e RecordRbac) bool { return e.Workload != id })
}

// accessOf is the Kubernetes access of a workload: what its pods' backends say and what the install record lists.
func (x *run) accessOf(id string, backends []backend) []RecordRbac {
	var out []RecordRbac
	for _, b := range backends {
		if b.Kind == kindPods && b.Cluster != nil {
			out = append(out, RecordRbac{Cluster: b.Cluster.String(), Namespace: b.Namespace, Workload: id})
		}
	}
	for _, e := range x.recordedAccess(id) {
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

// removeAccess removes the Kubernetes access of an offboarded workload, in out. The router's own access goes once its workload
// no longer needs it: never before the controller's delete, which may wake it. A cluster gone holds no access. With force, what
// it cannot remove is named and stays in the record, which any uninstall lists; without, it stops on the first, and a rerun
// finishes.
func (x *run) removeAccess(id string, backends []backend, force bool, out *offboarding) error {
	if x.rec == nil && slices.ContainsFunc(backends, func(b backend) bool { return b.Kind == kindPods }) {
		if _, err := x.rbacRecord(); err != nil {
			out.kept = append(out.kept, "! the install record is not read, and it may still list this access: "+refusal(err))
		}
	}
	for _, e := range x.accessOf(id, backends) {
		rb := Rbac{Cluster: e.Cluster, Namespace: e.Namespace, Workload: e.Workload, ServiceAccount: x.n.email}
		removal, err := x.RemoveRbac(rb)
		out.removed = append(out.removed, removal.Removed...)
		var gone *clusterGone
		if errors.As(err, &gone) {
			err = nil
		}
		if err != nil {
			why := strings.TrimSuffix(x.why(err), "\n"+nothingChanged)
			if !force {
				return x.ended(fmt.Sprintf("workload %s is offboarded, but its Kubernetes access is not removed: %s\n(run the same command again to finish, or remove it with: %s)", id, why, x.revokeCommand(e)))
			}
			out.accessLeft = append(out.accessLeft, x.rbacLeftLine(e)+" ("+why+")")
			if x.rec != nil {
				x.rec.addRbac(e)
			}
			continue
		}
		for _, l := range removal.Left {
			out.kept = append(out.kept, fmt.Sprintf("%s stays in namespace %s: it is not Dormeto's, or another subject holds it", l, e.Namespace))
		}
		if err := x.unrecordRbac(rb); err != nil {
			out.kept = append(out.kept, "! the install record still lists this access: "+refusal(err))
		}
	}
	return nil
}

// accessLines are the plan's lines of the access an offboarding of a workload removes.
func (x *run) accessLines(id string) []string {
	var lines []string
	for _, e := range x.recordedAccess(id) {
		lines = append(lines, fmt.Sprintf("- Role and RoleBinding dormeto-%s, namespace %s, cluster %s (what the cluster refuses is said at the end)", e.Workload, e.Namespace, e.Cluster))
	}
	return lines
}

// endOffboarding is the end of an uninstall's offboarding: a tag left stops it, as Google refuses to delete a tag still bound; an
// access or request logs left are said, and listed with what the uninstall leaves, so nothing waits in the cache for them.
func (x *run) endOffboarding(id string, out offboarding) error {
	for _, r := range out.removed {
		x.say("  workload %s: removed %s", id, r)
	}
	for _, r := range out.restored {
		x.say("  workload %s: restored %s", id, r)
	}
	for _, l := range out.logsLeft {
		line := fmt.Sprintf("workload %s: %s", id, l)
		x.say("  ! %s", line)
		x.notGivenBack = append(x.notGivenBack, line)
	}
	for _, k := range out.kept {
		x.say("  workload %s: %s", id, k)
	}
	for _, l := range out.accessLeft {
		x.say("  ! workload %s: %s", id, l)
	}
	if len(out.tagsLeft) > 0 {
		return fmt.Errorf("workload %s is offboarded, but %s\n(run the same command again to finish)", id, strings.Join(out.tagsLeft, "; "))
	}
	if p := x.pendingPath(id); p != "" {
		os.Remove(p)
	}
	return nil
}
