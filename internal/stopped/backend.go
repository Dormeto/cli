package stopped

// Ports the RouterBackend union of manager packages/estimate/src/stopped.ts
// (and the router's own rds-instance and rds-cluster backends).

import "github.com/Dormeto/cli/internal/kubernetes"

// Kinds of Backend.
const (
	BackendInstance    = "instance"
	BackendASG         = "asg"
	BackendECS         = "ecs"
	BackendK8s         = "k8s"
	BackendRDSInstance = "rds-instance"
	BackendRDSCluster  = "rds-cluster"
)

// Backend is what a nap stops: the union as one struct, Kind saying which
// fields it carries (JSON names as Node's).
//
//	instance:     id
//	asg:          name, desired, instances
//	ecs:          id, cluster, desired
//	k8s:          id, cluster, namespace, owner, autoscaler
//	rds-instance, rds-cluster: id
type Backend struct {
	Kind       string                `json:"kind"`
	ID         string                `json:"id,omitempty"`
	Name       string                `json:"name,omitempty"`
	Desired    int                   `json:"desired,omitempty"`
	Instances  []string              `json:"instances,omitempty"`
	Cluster    string                `json:"cluster,omitempty"`
	Namespace  string                `json:"namespace,omitempty"`
	Owner      kubernetes.Owner      `json:"owner,omitzero"`
	Autoscaler kubernetes.Autoscaler `json:"autoscaler,omitempty"`
}
