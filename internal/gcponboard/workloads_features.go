package gcponboard

// What `dormeto gcp workloads onboard` and `offboard` do for the optional features: the install record must hold those a flag
// needs; a Cloud SQL companion's tag; a GKE workload's owner, read as the operator, and its Kubernetes RBAC, undone with a
// refused onboarding and removed with the offboarding.

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/Dormeto/cli/internal/gcp"
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
