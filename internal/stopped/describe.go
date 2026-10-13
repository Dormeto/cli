package stopped

// What a nap stops (vision docs/specs/2026-10-08-savings-design.md, R9a): each
// backend by what prices it. A backend that cannot be described is said (an
// unknown item), never guessed; describing never stops the nap.
//
// Ports router lambda/src/lib/stopped.mjs (strategyCounts, describeStopped and
// the item builders) and lambda/src/lib/kubernetes.mjs's k8sItems path
// (see k8s.go).

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/Dormeto/cli/internal/awserr"
)

const itemsMax = 50

func osOf(platform string) string {
	if platform == "" || platform == "Linux/UNIX" {
		return "Linux"
	}
	return platform
}

// unknown is the item for a backend that could not be described; the reason
// is the first 200 characters (UTF-16 units, as Node's slice) of the message
// (Node's err.message: awserr.Message).
func unknown(backend string, err error) Item {
	units := utf16.Encode([]rune(awserr.Message(err)))
	if len(units) > 200 {
		units = units[:200]
	}
	return Item{Kind: KindUnknown, Backend: backend, Reason: string(utf16.Decode(units))}
}

type described struct {
	typ, os string
	spot    bool
}

func instances(ctx context.Context, ec2 EC2Port, ids []string) ([]described, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	out, err := ec2.DescribeInstances(ctx, DescribeInstancesInput{InstanceIds: ids})
	if err != nil {
		return nil, err
	}
	// A Map keyed by id: a repeated id keeps its first place and its last value.
	var found []described
	at := map[string]int{}
	for _, r := range out.Reservations {
		for _, i := range r.Instances {
			d := described{i.InstanceType, osOf(i.PlatformDetails), i.InstanceLifecycle == "spot"}
			if n, ok := at[i.InstanceID]; ok {
				found[n] = d
				continue
			}
			at[i.InstanceID] = len(found)
			found = append(found, d)
		}
	}
	return found, nil
}

// ec2Item is an EC2 item; spot only when it ran on Spot (R9.5a).
func ec2Item(typ string, count int, os string, spot bool) Item {
	return Item{Kind: KindEC2, Type: typ, Count: count, OS: os, Spot: spot}
}

func instanceItems(ctx context.Context, ec2 EC2Port, b Backend) ([]Item, error) {
	found, err := instances(ctx, ec2, []string{b.ID})
	if err != nil || len(found) == 0 {
		return nil, err
	}
	return []Item{ec2Item(found[0].typ, 1, found[0].os, found[0].spot)}, nil
}

// groupItems: an Auto Scaling group's desired capacity before the nap, by the
// types of its instances, on-demand or Spot.
func groupItems(ctx context.Context, ec2 EC2Port, b Backend) ([]Item, error) {
	if b.Desired == 0 {
		return nil, nil
	}
	found, err := instances(ctx, ec2, b.Instances)
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return []Item{unknown("asg:"+b.Name, errors.New("no instance to read its type from"))}, nil
	}
	var items []Item
	at := map[described]int{}
	for _, d := range found {
		if n, ok := at[d]; ok {
			items[n].Count++
			continue
		}
		at[d] = len(items)
		items = append(items, ec2Item(d.typ, 1, d.os, d.spot))
	}
	if missing := b.Desired - len(found); missing > 0 {
		items[0].Count += missing
	}
	return items, nil
}

// StrategyCounts splits a desired count by a capacity provider strategy, as
// ECS places tasks (R9.5a): the provider with a base gets that many first, the
// rest is shared by weight, rounded with the largest remainder so the counts
// add up; without any weight, the base's provider (else the first) takes the
// rest.
func StrategyCounts(strategy []CapacityProviderStrategyItem, desired int) map[string]int {
	counts := map[string]int{}
	for _, s := range strategy {
		counts[s.CapacityProvider] = 0
	}
	if len(strategy) == 0 || desired <= 0 {
		return counts
	}
	var based *CapacityProviderStrategyItem
	for i := range strategy {
		if strategy[i].Base > 0 {
			based = &strategy[i]
			break
		}
	}
	base := 0
	if based != nil {
		base = min(based.Base, desired)
		counts[based.CapacityProvider] = base
	}
	rest := desired - base
	total := 0
	for _, s := range strategy {
		total += s.Weight
	}
	if total == 0 {
		taker := strategy[0].CapacityProvider
		if based != nil {
			taker = based.CapacityProvider
		}
		counts[taker] += rest
		return counts
	}
	type share struct {
		provider string
		exact    float64
	}
	shares := make([]share, len(strategy))
	floors := 0
	for i, s := range strategy {
		shares[i] = share{s.CapacityProvider, float64(rest*s.Weight) / float64(total)}
		counts[s.CapacityProvider] += int(math.Floor(shares[i].exact))
		floors += int(math.Floor(shares[i].exact))
	}
	left := rest - floors
	sorted := append([]share(nil), shares...)
	sort.SliceStable(sorted, func(a, b int) bool { return math.Mod(sorted[a].exact, 1) > math.Mod(sorted[b].exact, 1) })
	for _, s := range sorted {
		if left <= 0 {
			break
		}
		counts[s.provider]++
		left--
	}
	return counts
}

// serviceItems: a service on Fargate (its launch type, or Fargate capacity
// providers): its task size times its desired count, the share on Fargate
// Spot an item of its own. On EC2 capacity, the cluster's instances are not
// stopped by Dormeto: nothing to count.
func serviceItems(ctx context.Context, ecs ECSPort, b Backend) ([]Item, error) {
	out, err := ecs.DescribeServices(ctx, DescribeServicesInput{Cluster: b.Cluster, Services: []string{b.ID}})
	if err != nil {
		return nil, err
	}
	if len(out.Services) == 0 {
		return nil, nil
	}
	svc := out.Services[0]
	fargate := svc.LaunchType == "FARGATE"
	for _, s := range svc.CapacityProviderStrategy {
		if strings.HasPrefix(s.CapacityProvider, "FARGATE") {
			fargate = true
		}
	}
	if !fargate || b.Desired <= 0 {
		return nil, nil
	}
	td, err := ecs.DescribeTaskDefinition(ctx, DescribeTaskDefinitionInput{TaskDefinition: svc.TaskDefinition})
	if err != nil {
		return nil, err
	}
	task := td.TaskDefinition
	if task == nil {
		return nil, errors.New("Cannot read properties of undefined (reading 'cpu')")
	}
	family, arch := "LINUX", "x86_64"
	if p := task.RuntimePlatform; p != nil {
		if p.OperatingSystemFamily != "" {
			family = p.OperatingSystemFamily
		}
		if p.CPUArchitecture == "ARM64" {
			arch = "arm64"
		}
	}
	os := family
	if family == "LINUX" {
		os = "Linux"
	} else if strings.HasPrefix(family, "WINDOWS") {
		os = "Windows"
	}
	size := Item{Kind: KindFargate, VCPU: number(task.CPU) / 1024, MemoryGb: number(task.Memory) / 1024, Arch: arch, OS: os}
	if svc.LaunchType == "FARGATE" {
		size.Count = b.Desired
		return []Item{size}, nil
	}
	counts := StrategyCounts(svc.CapacityProviderStrategy, b.Desired)
	var items []Item
	if n := counts["FARGATE"]; n != 0 {
		item := size
		item.Count = n
		items = append(items, item)
	}
	if n := counts["FARGATE_SPOT"]; n != 0 {
		item := size
		item.Count, item.Spot = n, true
		items = append(items, item)
	}
	return items, nil
}

// number is JavaScript's Number(text) for a task size ("512"), 0 for text that
// is not a number.
func number(text string) float64 {
	n, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	if err != nil {
		return 0
	}
	return n
}

func dbItem(db DBInstance, cluster *DBCluster) Item {
	if db.DBInstanceClass == "db.serverless" {
		acu := 0.5
		if cluster != nil && cluster.ServerlessV2ScalingConfiguration != nil && cluster.ServerlessV2ScalingConfiguration.MinCapacity != nil {
			acu = *cluster.ServerlessV2ScalingConfiguration.MinCapacity
		}
		return Item{Kind: KindAuroraServerless, Engine: db.Engine, ACU: acu}
	}
	return Item{Kind: KindRDS, Class: db.DBInstanceClass, Engine: db.Engine, MultiAZ: db.MultiAZ}
}

func databaseItems(ctx context.Context, rds RDSPort, b Backend) ([]Item, error) {
	if b.Kind == BackendRDSInstance {
		out, err := rds.DescribeDBInstances(ctx, DescribeDBInstancesInput{DBInstanceIdentifier: b.ID})
		if err != nil || len(out.DBInstances) == 0 {
			return nil, err
		}
		return []Item{dbItem(out.DBInstances[0], nil)}, nil
	}
	clusters, err := rds.DescribeDBClusters(ctx, DescribeDBClustersInput{DBClusterIdentifier: b.ID})
	if err != nil {
		return nil, err
	}
	var cluster *DBCluster
	if len(clusters.DBClusters) > 0 {
		cluster = &clusters.DBClusters[0]
	}
	dbs, err := rds.DescribeDBInstances(ctx, DescribeDBInstancesInput{Filters: []Filter{{Name: "db-cluster-id", Values: []string{b.ID}}}})
	if err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(dbs.DBInstances))
	for _, db := range dbs.DBInstances {
		items = append(items, dbItem(db, cluster))
	}
	return items, nil
}

// Describe says what a nap of each backend stops, in order, at most 50 items.
// A backend that cannot be described (a port's error, a recorded one included)
// is an unknown item with the error's message; Describe itself fails only when
// ctx is done.
func Describe(ctx context.Context, ports Ports, backends []Backend) ([]Item, error) {
	items := []Item{}
	for _, b := range backends {
		name := fmt.Sprintf("%s:%s", b.Kind, b.ID)
		if b.Kind == BackendASG {
			name = "asg:" + b.Name
		}
		var got []Item
		var err error
		switch b.Kind {
		case BackendInstance:
			got, err = instanceItems(ctx, ports.EC2, b)
		case BackendASG:
			got, err = groupItems(ctx, ports.EC2, b)
		case BackendECS:
			got, err = serviceItems(ctx, ports.ECS, b)
		case BackendK8s:
			got, err = k8sItems(ctx, ports, b)
		case BackendRDSInstance, BackendRDSCluster:
			got, err = databaseItems(ctx, ports.RDS, b)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			got = []Item{unknown(name, err)}
		}
		items = append(items, got...)
	}
	return items[:min(len(items), itemsMax)], nil
}
