// The fixtures of the estimate's tests, as manager packages/estimate/test/estimate.test.ts sets them.
package estimate

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/accounts"
	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/metrics"
	"github.com/Dormeto/cli/internal/options"
	"github.com/Dormeto/cli/internal/prices"
	"github.com/Dormeto/cli/internal/replay"
	"github.com/Dormeto/cli/internal/stopped"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"
)

const (
	A    = "111111111111"
	B    = "222222222222"
	TO   = "2026-10-10T12:00:00.000Z"
	FROM = "2026-09-26T12:00:00.000Z"
)

var (
	NOW     = time.Date(2026, 10, 10, 12, 0, 30, 0, time.UTC)
	T3      = stopped.Item{Kind: "ec2", Type: "t3.medium", Count: 2, OS: "Linux"}
	FARGATE = stopped.Item{Kind: "fargate", VCPU: 0.25, MemoryGb: 0.5, Count: 1, Arch: "arm64", OS: "Linux"}
	HUGE    = stopped.Item{Kind: "ec2", Type: "x9.huge", Count: 1, OS: "Linux"}
	always  = prices.Baseline{Kind: "always"}
	paris   = prices.Baseline{Kind: "business-hours", TimeZone: "Europe/Paris"}
	cost    = prices.DormetoCost
)

func f(v float64) *float64 { return &v }
func s(v string) *string   { return &v }

func hourly(_ context.Context, _ string, item stopped.Item) (*float64, error) {
	switch {
	case item.Kind == "ec2" && item.Type == "t3.medium":
		return f(0.0456), nil
	case item.Kind == "ec2" && item.Type == "t4g.pico":
		return f(0.001), nil
	case item.Kind == "fargate":
		return f(0.0099), nil
	case item.Kind == "rds":
		return f(0.068), nil
	}
	return nil, nil
}

// daily: 3 requests once a day, at minute 600 of each of the 14 days.
func daily() [][2]float64 {
	out := [][2]float64{}
	for d := range 14 {
		out = append(out, [2]float64{float64(600 + d*1440), 3})
	}
	return out
}

func w(account, region, name string, items ...stopped.Item) discover.Workload {
	if items == nil {
		items = []stopped.Item{}
	}
	return discover.Workload{
		Account: account, Region: region, LoadBalancer: "web", LoadBalancerArn: "arn:lb/" + name, TargetGroup: name, TargetGroupArn: "arn:tg/" + name,
		Hosts: []string{name + ".example.com"}, Backend: discover.WorkloadBackend{Kind: "asg", ID: name}, Stopped: items, Tags: map[string][]string{},
	}
}

// The workloads of the default world.
var (
	wPriced   = w(A, "eu-west-1", "shop", T3)
	wQuiet    = w(A, "eu-west-1", "docs", FARGATE)
	wUnpriced = w(A, "eu-west-1", "gpu", HUGE)
	wEmpty    = w(A, "eu-west-1", "batch")
	wUnread   = w(A, "eu-west-1", "blog", T3)
	wOther    = w(B, "eu-west-1", "crm", T3)
)

// reachedPorts: the organization o-abc123 of A (prod) and B (staging); 333333333333, given with --accounts, is not in
// it, listed apart.
type reachedPorts struct{}

func (reachedPorts) Whoami(context.Context) (string, error)       { return A, nil }
func (reachedPorts) Organization(context.Context) (string, error) { return "o-abc123", nil }
func (reachedPorts) Inventory(context.Context) (discover.OrgInventory, error) {
	return discover.OrgInventory{Accounts: []discover.Account{{ID: A, Name: "prod"}, {ID: B, Name: "staging"}}}, nil
}
func (reachedPorts) Assume(context.Context, string, string) (aws.Credentials, error) {
	return aws.Credentials{}, &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"}
}

func reachedOf(t *testing.T) accounts.Reached {
	t.Helper()
	r, err := accounts.Reach(context.Background(), reachedPorts{}, accounts.Options{Role: "OrganizationAccountAccessRole", Only: []string{A, B, "333333333333"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Unreachable.List()) != 1 {
		t.Fatalf("unreachable %+v", r.Unreachable.List())
	}
	return r
}

func defaultRegions() []discover.RegionDiscovery {
	shared := "shared"
	return []discover.RegionDiscovery{
		{Account: A, Region: "eu-west-1", Workloads: []discover.Workload{wPriced, wQuiet, wUnpriced, wEmpty, wUnread},
			Ineligible: []discover.IneligibleRule{{LoadBalancer: "web", TargetGroup: &shared, Hosts: []string{}, Reason: "its target group is shared"}}},
		{Account: A, Region: "eu-west-3", Workloads: []discover.Workload{}, Ineligible: []discover.IneligibleRule{}, Error: "opt-in region not enabled"},
		{Account: B, Region: "eu-west-1", Workloads: []discover.Workload{wOther}, Ineligible: []discover.IneligibleRule{}},
	}
}

func defaultRequests() []metrics.RegionRequests {
	return []metrics.RegionRequests{
		{Account: A, Region: "eu-west-1", From: FROM, To: TO,
			Minutes:    map[string][][2]float64{"arn:tg/shop": daily(), "arn:tg/docs": {}, "arn:tg/gpu": daily(), "arn:tg/batch": daily()},
			Unreadable: map[string]string{"arn:tg/blog": "CloudWatch could not read the requests (Forbidden)"}},
		{Account: B, Region: "eu-west-1", From: FROM, To: TO, Minutes: map[string][][2]float64{"arn:tg/crm": daily()}, Unreadable: map[string]string{}},
	}
}

// regionRequests: one region of A in eu-west-1, the target groups' minutes given.
func regionRequests(minutes map[string][][2]float64) []metrics.RegionRequests {
	return []metrics.RegionRequests{{Account: A, Region: "eu-west-1", From: FROM, To: TO, Minutes: minutes, Unreadable: map[string]string{}}}
}

// region: A in eu-west-1 with the workloads given.
func region(workloads ...discover.Workload) []discover.RegionDiscovery {
	return []discover.RegionDiscovery{{Account: A, Region: "eu-west-1", Workloads: workloads, Ineligible: []discover.IneligibleRule{}}}
}

func defaultOptions() options.Options {
	return options.Options{
		ExcludeTags: []discover.TagWants{}, Role: "OrganizationAccountAccessRole", Baseline: always, Window: 60,
		MaxWakes: f(3), MinSaving: f(1), Parallel: 5, MaxWakesRds: f(2),
	}
}

func run(t *testing.T, change ...func(*Input)) Estimate {
	t.Helper()
	in := Input{Reached: reachedOf(t), Regions: defaultRegions(), Requests: defaultRequests(), Hourly: hourly, Baseline: always, Options: defaultOptions(), Now: NOW}
	for _, c := range change {
		c(&in)
	}
	e, err := All(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// The product's replay, as the estimate must call it (decision 5).
func advise(t *testing.T, minutes [][2]float64, items []stopped.Item, baseline prices.Baseline, current int, running []replay.Period) replay.Result {
	t.Helper()
	pairs := make([][2]int, len(minutes))
	for i, m := range minutes {
		pairs[i] = [2]int{int(m[0]), int(m[1])}
	}
	wake := 60_000.0
	a, err := replay.Advice(replay.Input{
		Requests: replay.RequestMinutes{From: FROM, To: TO, Minutes: pairs}, Current: current, CheckMinutes: 5, WakeMs: &wake, Stopped: items,
		Hourly: func(ctx context.Context, item stopped.Item) (*float64, error) { return hourly(ctx, "eu-west-1", item) }, Baseline: baseline,
		Now: NOW, Running: running,
	})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func row(t *testing.T, a replay.Result, minutes int) replay.Row {
	t.Helper()
	for _, r := range a.Windows {
		if r.Minutes == minutes {
			return r
		}
	}
	t.Fatalf("no %d-minute window in %+v", minutes, a.Windows)
	return replay.Row{}
}

func usd(t *testing.T, r replay.Row) float64 {
	t.Helper()
	if r.USDMonth == nil {
		t.Fatalf("the %d-minute window is not priced", r.Minutes)
	}
	return *r.USDMonth
}

// closeTo is vitest's toBeCloseTo(want, digits): |got - want| < 10^-digits / 2.
func closeTo(t *testing.T, what string, got, want float64, digits int) {
	t.Helper()
	if !(math.Abs(got-want) < math.Pow(10, -float64(digits))/2) {
		t.Fatalf("%s: %v, want %v (to %d digits)", what, got, want, digits)
	}
}

func estimatedGroups(workloads []WorkloadEstimate) []string {
	out := []string{}
	for _, x := range workloads {
		out = append(out, x.Workload.TargetGroup)
	}
	return out
}

func find(t *testing.T, workloads []WorkloadEstimate, targetGroup string) WorkloadEstimate {
	t.Helper()
	for _, x := range workloads {
		if x.Workload.TargetGroup == targetGroup {
			return x
		}
	}
	t.Fatalf("%s not estimated: %q", targetGroup, estimatedGroups(workloads))
	return WorkloadEstimate{}
}

func nth(t *testing.T, workloads []WorkloadEstimate, i int) WorkloadEstimate {
	t.Helper()
	if i >= len(workloads) {
		t.Fatalf("no workload %d: %q", i, estimatedGroups(workloads))
	}
	return workloads[i]
}

func reasons(leftOut []LeftOut) []string {
	out := []string{}
	for _, l := range leftOut {
		out = append(out, l.Reason)
	}
	return out
}

func totalsOf(t *testing.T, by Ordered[Totals], key string) Totals {
	t.Helper()
	v, ok := by.Get(key)
	if !ok {
		t.Fatalf("no totals for %s among %q", key, by.Keys())
	}
	return v
}

func hours(v float64) *Uptime          { return &Uptime{HoursPerDay: &v} }
func unread(v string) *Uptime          { return &Uptime{Unreadable: &v} }
func said(assumptions []string) string { return strings.Join(assumptions, " ") }

// half: 12:00 to 24:00 UTC each day, 12 hours of the 24.
func half() [][2]int {
	out := [][2]int{}
	for d := range 14 {
		out = append(out, [2]int{d * 1440, d*1440 + 720})
	}
	return out
}

func periodsOf(running [][2]int) []replay.Period {
	from, _ := time.Parse(time.RFC3339Nano, FROM)
	out := []replay.Period{}
	for _, p := range running {
		at := func(m int) string {
			return from.Add(time.Duration(m) * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
		}
		out = append(out, replay.Period{From: at(p[0]), To: at(p[1])})
	}
	return out
}

// every: 2 requests every so many hours over the 14 days, from minute 10 (Node's rounding of the count and the
// minutes).
func every(h float64) [][2]float64 {
	out := [][2]float64{}
	n := int(math.Round(14 * 24 / h))
	for k := range n {
		out = append(out, [2]float64{10 + math.Round(float64(k)*h*60), 2})
	}
	return out
}

func jsonOf(v any) ([]byte, error) { return json.Marshal(v) }

// topKeys are the keys of a JSON object, in order.
func topKeys(b []byte) ([]string, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	keys := []string{}
	for dec.More() {
		k, err := dec.Token()
		if err != nil {
			return nil, err
		}
		keys = append(keys, k.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return nil, err
		}
	}
	return keys, nil
}
