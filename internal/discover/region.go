package discover

// Phase 1 of the estimate, a region's part (vision
// docs/specs/2026-10-10-savings-estimate-design.md §4): discovery's scan of the
// region (the product's eligibility), what each eligible workload's backend
// would stop (the router's description, priced later), the region's clusters
// the profile cannot read and its companion databases.
//
// Ports manager packages/estimate/src/discover.ts (Workload, Database,
// Ineligible, RegionDiscovery, clusterOf, routerBackend, wakerOf,
// securityGroupsOf, databasesOf, discoverRegion, describeRule).

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/Dormeto/cli/internal/awserr"
	"github.com/Dormeto/cli/internal/limit"
	"github.com/Dormeto/cli/internal/stopped"
)

// WorkloadBackend is a workload's backend as kept: its kind and id.
type WorkloadBackend struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// Workload is an eligible (or managed) workload of a region, described as the
// router would: what a nap stops, its tags and its security groups.
type Workload struct {
	Account         string          `json:"account"`
	Region          string          `json:"region"`
	LoadBalancer    string          `json:"loadBalancer"`
	LoadBalancerArn string          `json:"loadBalancerArn"`
	TargetGroup     string          `json:"targetGroup"`
	TargetGroupArn  string          `json:"targetGroupArn"`
	Hosts           []string        `json:"hosts"`
	Backend         WorkloadBackend `json:"backend"`
	Stopped         []stopped.Item  `json:"stopped"`
	// Every value of each tag on its load balancer, target group or rule.
	Tags map[string][]string `json:"tags"`
	// An instance's state when discovered (not one Dormeto manages).
	InstanceState string `json:"instanceState,omitempty"`
	// An ECS service's desired count when discovered (not one Dormeto manages).
	EcsDesired *int `json:"ecsDesired,omitempty"`
	// Already managed by Dormeto, with its waker target group when found.
	Managed             bool   `json:"managed,omitempty"`
	WakerTargetGroupArn string `json:"wakerTargetGroupArn,omitempty"`
	// Its backend's security groups: a database letting one in is its companion.
	SecurityGroups []string `json:"securityGroups"`
}

// Database is a companion database candidate of the region.
type Database struct {
	Kind         string              `json:"kind"` // rds-instance or rds-cluster
	ID           string              `json:"id"`
	Status       string              `json:"status"`
	AllowedFrom  []string            `json:"allowedFrom"`
	Tags         map[string][]string `json:"tags"`
	Stopped      []stopped.Item      `json:"stopped"`
	NotStoppable string              `json:"notStoppable,omitempty"`
}

// IneligibleRule is a rule that is not a workload, and why.
type IneligibleRule struct {
	LoadBalancer string   `json:"loadBalancer"`
	TargetGroup  *string  `json:"targetGroup"`
	Hosts        []string `json:"hosts"`
	Reason       string   `json:"reason"`
}

// UnreadableCluster is an EKS cluster the profile cannot read, and why.
type UnreadableCluster struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// RegionDiscovery is what discovery kept of an account and region (the cache's
// value). Databases nil: not read (a file kept before the databases existed).
type RegionDiscovery struct {
	Account        string
	Region         string
	Workloads      []Workload
	Ineligible     []IneligibleRule
	Clusters       []UnreadableCluster
	Error          string
	Databases      []Database
	DatabasesError string
}

type regionJSON struct {
	Account        string              `json:"account"`
	Region         string              `json:"region"`
	Workloads      []Workload          `json:"workloads"`
	Ineligible     []IneligibleRule    `json:"ineligible"`
	Clusters       []UnreadableCluster `json:"clusters,omitempty"`
	Error          string              `json:"error,omitempty"`
	Databases      *[]Database         `json:"databases,omitempty"`
	DatabasesError string              `json:"databasesError,omitempty"`
}

// MarshalJSON writes Node's shape: workloads and ineligible always arrays,
// clusters, error and databasesError only when there are some, databases when
// they were read (an empty array included).
func (r RegionDiscovery) MarshalJSON() ([]byte, error) {
	j := regionJSON{Account: r.Account, Region: r.Region, Workloads: r.Workloads, Ineligible: r.Ineligible, Clusters: r.Clusters, Error: r.Error, DatabasesError: r.DatabasesError}
	if j.Workloads == nil {
		j.Workloads = []Workload{}
	}
	if j.Ineligible == nil {
		j.Ineligible = []IneligibleRule{}
	}
	if r.Databases != nil {
		j.Databases = &r.Databases
	}
	return json.Marshal(j)
}

// UnmarshalJSON reads the shape MarshalJSON writes (and the Node estimate's files).
func (r *RegionDiscovery) UnmarshalJSON(b []byte) error {
	var j regionJSON
	if err := json.Unmarshal(b, &j); err != nil {
		return err
	}
	*r = RegionDiscovery{Account: j.Account, Region: j.Region, Workloads: j.Workloads, Ineligible: j.Ineligible, Clusters: j.Clusters, Error: j.Error, DatabasesError: j.DatabasesError}
	if j.Databases != nil {
		r.Databases = *j.Databases
	}
	return nil
}

// workloadsAtOnce: a region's workloads are described this many at a time (an
// account's own calls: AWS throttles per account), in their order.
const workloadsAtOnce = 8

// ClusterOf is the ECS cluster of a service ARN
// (arn:aws:ecs:<region>:<account>:service/<cluster>/<service>); the old format
// (service/<service>) is in the default cluster.
func ClusterOf(serviceArn string) string {
	parts := strings.Split(serviceArn[strings.LastIndex(serviceArn, ":")+1:], "/")
	if len(parts) >= 3 {
		return parts[1]
	}
	return "default"
}

// unique drops the empty values and the repeated ones, keeping the order. It
// never gives nil.
func unique(values []string) []string {
	out := []string{}
	for _, v := range values {
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}

// backendNow is discovery's backend as the router describes it: an Auto
// Scaling group's desired capacity and instances, an ECS service's cluster and
// desired count, an instance (with its state, unless Dormeto manages it).
// running nil: nothing to stop.
type backendNow struct {
	running       *stopped.Backend
	instanceState string
	ecsDesired    *int
}

func routerBackend(ctx context.Context, ports RegionPorts, backend Backend, managed bool) (backendNow, error) {
	switch backend.Kind {
	case KindK8s:
		k := backend.Kubernetes
		if k == nil {
			return backendNow{}, fmt.Errorf("Cannot read properties of undefined (reading 'cluster')")
		}
		return backendNow{running: &stopped.Backend{Kind: stopped.BackendK8s, ID: backend.ID, Cluster: k.Cluster, Namespace: k.Namespace, Owner: k.Owner, Autoscaler: k.Autoscaler}}, nil
	case KindASG:
		group, err := ports.AutoScaling.Group(ctx, backend.ID)
		if err != nil {
			return backendNow{}, err
		}
		b := stopped.Backend{Kind: stopped.BackendASG, Name: backend.ID, Instances: []string{}}
		if group != nil {
			b.Desired = group.Desired
			b.Instances = append(b.Instances, unique(group.Instances)...)
		}
		return backendNow{running: &b}, nil
	case KindECS:
		cluster := ClusterOf(backend.ID)
		service, err := ports.ECS.Service(ctx, cluster, backend.ID)
		if err != nil {
			return backendNow{}, err
		}
		desired := 0
		if service != nil {
			desired = service.DesiredCount
		}
		if managed {
			return backendNow{running: &stopped.Backend{Kind: stopped.BackendECS, ID: backend.ID, Cluster: cluster, Desired: desired}}, nil
		}
		return backendNow{running: &stopped.Backend{Kind: stopped.BackendECS, ID: backend.ID, Cluster: cluster, Desired: max(desired, 1)}, ecsDesired: &desired}, nil
	}
	// Dormeto manages it: stopped for a nap, it runs when woken. Otherwise its
	// state: one stopped is described all the same (the estimate says it stops
	// nothing, unless its measured uptime shows it ran); one terminated stops
	// nothing. No state said: running.
	if managed {
		return backendNow{running: &stopped.Backend{Kind: stopped.BackendInstance, ID: backend.ID}}, nil
	}
	found, err := ports.EC2.Instances(ctx, []string{backend.ID})
	if err != nil {
		return backendNow{}, err
	}
	state := ""
	if len(found) > 0 {
		state = found[0].State
	}
	if state == "" {
		state = "running"
	}
	if state == "terminated" || state == "shutting-down" {
		return backendNow{instanceState: state}, nil
	}
	return backendNow{running: &stopped.Backend{Kind: stopped.BackendInstance, ID: backend.ID}, instanceState: state}, nil
}

// wakerOf is a managed workload's waker target group: the other target group of
// its Dormeto rule's forward (application and waker only), or none found (its
// requests while asleep are then not counted).
func wakerOf(ctx context.Context, ports RegionPorts, rule ScannedRule) (string, error) {
	var ref *RuleRef
	for i := range rule.Rules {
		if rule.Rules[i].Managed {
			ref = &rule.Rules[i]
			break
		}
	}
	if ref == nil && len(rule.Rules) > 0 {
		ref = &rule.Rules[0]
	}
	if ref == nil {
		return "", nil
	}
	rules, err := ports.ELBv2.DescribeRules(ctx, []string{ref.ID})
	if err != nil {
		return "", err
	}
	if len(rules) == 0 {
		return "", nil
	}
	own := ""
	if rule.TargetGroupArn != nil {
		own = *rule.TargetGroupArn
	}
	for _, action := range rules[0].Actions {
		if action.ForwardConfig == nil {
			continue
		}
		var groups []string
		for _, g := range action.ForwardConfig.TargetGroups {
			if g.TargetGroupArn != "" {
				groups = append(groups, g.TargetGroupArn)
			}
		}
		if len(groups) == 2 && slices.Contains(groups, own) {
			for _, arn := range groups {
				if arn != own {
					return arn, nil
				}
			}
		}
	}
	return "", nil
}

// securityGroupsOf is the security groups of a workload's backend: an
// instance's, its Auto Scaling group's instances', an ECS service's (awsvpc).
// None for a Kubernetes owner (its pods share their nodes' groups) or when they
// cannot be read.
func securityGroupsOf(ctx context.Context, ports RegionPorts, backend Backend, running *stopped.Backend) []string {
	switch backend.Kind {
	case KindInstance, KindASG:
		ids := []string{}
		if backend.Kind == KindInstance {
			ids = []string{backend.ID}
		} else if running != nil && running.Kind == stopped.BackendASG {
			ids = running.Instances
		}
		if len(ids) == 0 {
			return []string{}
		}
		instances, err := ports.EC2.Instances(ctx, ids)
		if err != nil {
			return []string{} // Not read: the workload gets no companion.
		}
		var groups []string
		for _, i := range instances {
			groups = append(groups, i.SecurityGroups...)
		}
		return unique(groups)
	case KindECS:
		service, err := ports.ECS.Service(ctx, ClusterOf(backend.ID), backend.ID)
		if err != nil || service == nil {
			return []string{}
		}
		return unique(service.SecurityGroups)
	}
	return []string{}
}

func tagsOfList(list []Tag) map[string][]string {
	tags := map[string][]string{}
	for _, t := range list {
		if t.Key != "" {
			tags[t.Key] = append(tags[t.Key], t.Value)
		}
	}
	return tags
}

// dbItem is what a nap of the database would stop, as the router describes it
// (router lambda/src/lib/stopped.mjs).
func dbItem(db DBInstance) stopped.Item {
	return stopped.Item{Kind: stopped.KindRDS, Class: db.DBInstanceClass, Engine: db.Engine, MultiAZ: db.MultiAZ}
}

// nappable are the statuses of a database a nap can stop, or one stopped (a
// schedule may start it again: its uptime says when).
var nappable = []string{"available", "stopped", "stopping", "starting"}

func activeGroups(groups []VpcSecurityGroup) []string {
	var ids []string
	for _, g := range groups {
		if g.Status != "inactive" {
			ids = append(ids, g.VpcSecurityGroupId)
		}
	}
	return unique(ids)
}

// databasesOf is the region's databases letting in one of groups, with their
// tags, what they stop, why they cannot be stopped. A failure is the error said
// as Node says it (name: message), the databases then none.
func databasesOf(ctx context.Context, ports RegionPorts, groups []string) (databases []Database, failure string) {
	databases = []Database{}
	if ports.RDS == nil || len(groups) == 0 {
		return databases, ""
	}
	type found struct {
		Database
		groups []string
	}
	read := func() ([]Database, error) {
		instances, err := pages(func(m string) ([]DBInstance, string, error) { return ports.RDS.DescribeDBInstances(ctx, m) })
		if err != nil {
			return nil, err
		}
		clusters, err := pages(func(m string) ([]DBCluster, string, error) { return ports.RDS.DescribeDBClusters(ctx, m) })
		if err != nil {
			return nil, err
		}
		var all []found
		for _, db := range instances {
			if db.DBClusterIdentifier != "" {
				continue
			}
			var notStoppable string
			switch {
			case !slices.Contains(nappable, db.DBInstanceStatus):
				notStoppable = db.DBInstanceStatus + " when discovered"
			case db.ReadReplicaSourceDBInstanceIdentifier != "":
				notStoppable = "a read replica: cannot be stopped"
			case len(db.ReadReplicaDBInstanceIdentifiers) > 0:
				notStoppable = "it has read replicas: cannot be stopped"
			case strings.HasPrefix(db.Engine, "sqlserver") && db.MultiAZ:
				notStoppable = "SQL Server Multi-AZ: cannot be stopped"
			}
			d := found{Database{Kind: "rds-instance", ID: db.DBInstanceIdentifier, Status: db.DBInstanceStatus, Tags: tagsOfList(db.TagList), Stopped: []stopped.Item{}, NotStoppable: notStoppable}, activeGroups(db.VpcSecurityGroups)}
			if notStoppable == "" {
				d.Stopped = []stopped.Item{dbItem(db)}
			}
			all = append(all, d)
		}
		for _, cluster := range clusters {
			var members []DBInstance
			serverless := cluster.EngineMode == "serverless"
			for _, i := range instances {
				if i.DBClusterIdentifier == cluster.DBClusterIdentifier {
					members = append(members, i)
					serverless = serverless || i.DBInstanceClass == "db.serverless"
				}
			}
			var notStoppable string
			switch {
			case serverless:
				notStoppable = "Aurora Serverless: not yet"
			case !strings.HasPrefix(cluster.Engine, "aurora"):
				notStoppable = "a Multi-AZ DB cluster: not yet"
			case !slices.Contains(nappable, cluster.Status):
				notStoppable = cluster.Status + " when discovered"
			case len(members) == 0:
				notStoppable = "no instance"
			}
			d := found{Database{Kind: "rds-cluster", ID: cluster.DBClusterIdentifier, Status: cluster.Status, Tags: tagsOfList(cluster.TagList), Stopped: []stopped.Item{}, NotStoppable: notStoppable}, activeGroups(cluster.VpcSecurityGroups)}
			if notStoppable == "" {
				for _, m := range members {
					d.Stopped = append(d.Stopped, dbItem(m))
				}
			}
			all = append(all, d)
		}
		// Who each database security group lets in (rules naming a security
		// group; address ranges are not followed).
		var own []string
		for _, f := range all {
			own = append(own, f.groups...)
		}
		asked := unique(own)
		ingress := map[string][]string{}
		for first := 0; first < len(asked); first += 100 {
			found, err := ports.EC2.SecurityGroups(ctx, asked[first:min(first+100, len(asked))])
			if err != nil {
				return nil, err
			}
			for _, sg := range found {
				ingress[sg.ID] = unique(sg.AllowedFrom)
			}
		}
		var out []Database
		for _, f := range all {
			var allowed []string
			for _, g := range f.groups {
				allowed = append(allowed, ingress[g]...)
			}
			f.AllowedFrom = unique(allowed)
			if slices.ContainsFunc(f.AllowedFrom, func(g string) bool { return slices.Contains(groups, g) }) {
				out = append(out, f.Database)
			}
		}
		return out, nil
	}
	got, err := read()
	if err != nil {
		return []Database{}, awserr.Said(err)
	}
	if got != nil {
		databases = got
	}
	return databases, ""
}

// unknownItem is an item that cannot be priced, said in the estimate: the
// first 200 characters (UTF-16 units, as Node's slice) of the message.
func unknownItem(backend string, err error) stopped.Item {
	units := utf16.Encode([]rune(awserr.Message(err)))
	if len(units) > 200 {
		units = units[:200]
	}
	return stopped.Item{Kind: stopped.KindUnknown, Backend: backend, Reason: string(utf16.Decode(units))}
}

// described is a rule of the scan: a workload or why not.
type described struct {
	workload   *Workload
	ineligible *IneligibleRule
}

// describeRule describes one rule of the scan: an eligible workload as the
// router would (what it stops, its security groups), else why not.
func describeRule(ctx context.Context, ports RegionPorts, rule ScannedRule, where Where) (described, error) {
	if rule.Eligibility == Ineligible || rule.Backend == nil || rule.TargetGroupArn == nil {
		reason := rule.Reason
		if reason == "" {
			reason = "no backend"
		}
		hosts := rule.Hosts
		if hosts == nil {
			hosts = []string{}
		}
		return described{ineligible: &IneligibleRule{LoadBalancer: rule.LoadBalancer, TargetGroup: rule.TargetGroup, Hosts: hosts, Reason: reason}}, nil
	}
	managed := rule.Eligibility == Managed
	waker := ""
	if managed {
		var err error
		if waker, err = wakerOf(ctx, ports, rule); err != nil {
			return described{}, err
		}
	}
	var items []stopped.Item
	var found backendNow
	err := func() error {
		var err error
		if found, err = routerBackend(ctx, ports, *rule.Backend, managed); err != nil {
			return err
		}
		if found.running != nil {
			items, err = stopped.Describe(ctx, ports.Stopped, []stopped.Backend{*found.running})
		}
		return err
	}()
	if err != nil {
		if ctx.Err() != nil {
			return described{}, ctx.Err()
		}
		// Never guessed: an item that cannot be priced, said in the estimate.
		items = []stopped.Item{unknownItem(rule.Backend.Kind+":"+rule.Backend.ID, err)}
	}
	if items == nil {
		items = []stopped.Item{}
	}
	w := Workload{
		Account: where.Account, Region: where.Region, LoadBalancer: rule.LoadBalancer, LoadBalancerArn: rule.LoadBalancerArn,
		TargetGroupArn: *rule.TargetGroupArn, Hosts: rule.Hosts, Backend: WorkloadBackend{Kind: rule.Backend.Kind, ID: rule.Backend.ID},
		Stopped: items, Tags: rule.Tags, InstanceState: found.instanceState, EcsDesired: found.ecsDesired,
		SecurityGroups: securityGroupsOf(ctx, ports, *rule.Backend, found.running),
	}
	if rule.TargetGroup != nil {
		w.TargetGroup = *rule.TargetGroup
	}
	if w.Hosts == nil {
		w.Hosts = []string{}
	}
	if w.Tags == nil {
		w.Tags = map[string][]string{}
	}
	if managed {
		w.Managed, w.WakerTargetGroupArn = true, waker
	}
	if err := ctx.Err(); err != nil {
		return described{}, err
	}
	return described{workload: &w}, nil
}

// Region discovers one account and region: the scan, the workloads described
// eight at a time in the scan's order, the clusters unreadable and the
// companion databases. A scan that fails (or a rule whose waker cannot be read)
// fails the region.
func Region(ctx context.Context, ports RegionPorts, where Where) (RegionDiscovery, error) {
	scan, err := ports.Scan(ctx)
	if err != nil {
		return RegionDiscovery{}, err
	}
	all, err := limit.Map(ctx, scan.Rules, workloadsAtOnce, func(ctx context.Context, rule ScannedRule) (described, error) {
		return describeRule(ctx, ports, rule, where)
	})
	if err != nil {
		return RegionDiscovery{}, err
	}
	found := RegionDiscovery{Account: where.Account, Region: where.Region, Workloads: []Workload{}, Ineligible: []IneligibleRule{}}
	var groups []string
	for _, d := range all {
		if d.workload != nil {
			found.Workloads = append(found.Workloads, *d.workload)
			groups = append(groups, d.workload.SecurityGroups...)
		} else {
			found.Ineligible = append(found.Ineligible, *d.ineligible)
		}
	}
	for _, c := range scan.Clusters {
		if c.Reason != "" {
			found.Clusters = append(found.Clusters, UnreadableCluster{Name: c.Name, Reason: c.Reason})
		}
	}
	found.Databases, found.DatabasesError = databasesOf(ctx, ports, unique(groups))
	return found, nil
}
