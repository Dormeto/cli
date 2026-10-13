package discover

// Ports manager packages/estimate/test/discover.test.ts (the discovery of every
// account and region), and the Review focus tests of Task 11: a cache the Node
// estimate wrote, a file Get cannot parse, results in the Node order under
// concurrency, credentials asked once per region.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/smithy-go"

	"github.com/Dormeto/cli/internal/cache"
	"github.com/Dormeto/cli/internal/stopped"
)

var nowT = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func target(id string, credentials ...func(context.Context) (*aws.Credentials, error)) Target {
	c := func(context.Context) (*aws.Credentials, error) { return nil, nil }
	if len(credentials) > 0 {
		c = credentials[0]
	}
	return Target{Account: id, Credentials: c}
}

type run struct {
	t       *testing.T
	dir     string
	accts   []Target
	regions []string
	failing map[string]bool
	delay   time.Duration
	unreach func(string) bool

	mu     sync.Mutex
	scans  []string
	listed int
	active int
	most   int
	events []AccountEvent
	prog   []ProgressEvent
	now    time.Time
}

func newRun(t *testing.T, mutate ...func(*run)) *run {
	w := &run{t: t, dir: t.TempDir(), accts: []Target{target(acctA)}, regions: []string{regR, "eu-west-3"}, failing: map[string]bool{}, now: nowT}
	for _, m := range mutate {
		m(w)
	}
	return w
}

type runOver struct {
	refresh     string
	regions     []string
	concurrency int
}

func (w *run) options(over runOver) AllOptions {
	return AllOptions{
		Accounts: w.accts, Unreachable: w.unreach, Cache: cache.New(w.dir, func() time.Time { return w.now }), Refresh: over.refresh,
		Now: func() time.Time { return w.now }, Regions: over.regions, Concurrency: over.concurrency,
		AccountProgress: func(e AccountEvent) { w.mu.Lock(); w.events = append(w.events, e); w.mu.Unlock() },
		Progress:        func(e ProgressEvent) { w.mu.Lock(); w.prog = append(w.prog, e); w.mu.Unlock() },
		RegionsOf: func(context.Context, string, *aws.Credentials) ([]string, error) {
			w.mu.Lock()
			w.listed++
			w.mu.Unlock()
			return w.regions, nil
		},
		PortsOf: func(_ context.Context, account, region string, _ *aws.Credentials) (RegionPorts, error) {
			f := newFake(wl(account+"-"+region, func(r *ScannedRule) { r.Account = account }))
			p := f.ports()
			p.Scan = func(context.Context) (ScanResult, error) {
				w.mu.Lock()
				w.scans = append(w.scans, account+"/"+region)
				w.active++
				w.most = max(w.most, w.active)
				w.mu.Unlock()
				time.Sleep(w.delay)
				w.mu.Lock()
				w.active--
				w.mu.Unlock()
				if w.failing[region] {
					return ScanResult{}, errors.New("The security token included in the request is invalid (an opt-in region not enabled)")
				}
				return ScanResult{Rules: f.rules}, nil
			}
			return p, nil
		},
	}
}

func (w *run) run(over ...runOver) []RegionDiscovery {
	w.t.Helper()
	var o runOver
	if len(over) > 0 {
		o = over[0]
	}
	regions, err := All(context.Background(), w.options(o))
	if err != nil {
		w.t.Fatal(err)
	}
	return regions
}

func (w *run) put(path string, value any) {
	w.t.Helper()
	if err := cache.New(w.dir, func() time.Time { return w.now }).Put(path, value); err != nil {
		w.t.Fatal(err)
	}
}

func (w *run) scanned() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.scans)
}

func rows(regions []RegionDiscovery) [][]any {
	var out [][]any
	for _, r := range regions {
		var e any
		if r.Error != "" {
			e = r.Error
		}
		out = append(out, []any{r.Region, len(r.Workloads), e})
	}
	return out
}

func TestEveryAccountsRegionsARegionThatFailsIsSaidAndTheOthersGoOn(t *testing.T) {
	w := newRun(t, func(w *run) { w.failing["eu-west-3"] = true })
	got := rows(w.run())
	want := [][]any{{regR, 1, nil}, {"eu-west-3", 0, "The security token included in the request is invalid (an opt-in region not enabled)"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestKeptPerAccountAndRegionASecondRunReadsTheCacheAFailedRegionIsTriedAgainRefreshDiscoveryScansAgain(t *testing.T) {
	w := newRun(t, func(w *run) { w.failing["eu-west-3"] = true })
	w.run()
	if want := []string{acctA + "/" + regR, acctA + "/eu-west-3"}; !reflect.DeepEqual(w.scanned(), want) {
		t.Fatalf("scans %v", w.scanned())
	}
	w.run()
	// The cached region is not scanned again, nor its regions listed; the failed one is.
	if want := []string{acctA + "/" + regR, acctA + "/eu-west-3", acctA + "/eu-west-3"}; !reflect.DeepEqual(w.scanned(), want) {
		t.Errorf("scans %v", w.scanned())
	}
	if w.listed != 1 {
		t.Errorf("regions listed %d times", w.listed)
	}
	w.run(runOver{refresh: "discovery"})
	if want := []string{acctA + "/" + regR, acctA + "/eu-west-3"}; !reflect.DeepEqual(w.scanned()[3:], want) {
		t.Errorf("scans %v", w.scanned())
	}
}

// asRaw is a region as a JSON object, to take fields out of it as an older file lacks them.
func asRaw(t *testing.T, r RegionDiscovery) map[string]any {
	t.Helper()
	var m map[string]any
	b, _ := json.Marshal(r)
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestARegionKeptBeforeInstancesHadTheirStateIsScannedAgain(t *testing.T) {
	w := newRun(t, func(w *run) { w.regions = []string{regR} })
	regions := w.run()
	instance := regions[0].Workloads[0]
	instance.Backend, instance.Stopped = WorkloadBackend{Kind: "instance", ID: "i-1"}, []stopped.Item{}
	kept := regions[0]
	kept.Workloads = []Workload{instance}
	w.put(fmt.Sprintf("discovery/%s-%s.json", acctA, regR), kept)
	w.run()
	if want := []string{acctA + "/" + regR, acctA + "/" + regR}; !reflect.DeepEqual(w.scanned(), want) {
		t.Fatalf("scans %v", w.scanned())
	}
	// With its state: kept.
	instance.InstanceState = "terminated"
	kept.Workloads = []Workload{instance}
	w.put(fmt.Sprintf("discovery/%s-%s.json", acctA, regR), kept)
	w.run()
	if len(w.scanned()) != 2 {
		t.Errorf("scans %v", w.scanned())
	}
}

func TestARegionKeptBeforeWorkloadsHadTheirTagsIsScannedAgain(t *testing.T) {
	w := newRun(t, func(w *run) { w.regions = []string{regR} })
	regions := w.run()
	raw := asRaw(t, regions[0])
	delete(raw["workloads"].([]any)[0].(map[string]any), "tags")
	w.put(fmt.Sprintf("discovery/%s-%s.json", acctA, regR), raw)
	again := w.run()
	if want := []string{acctA + "/" + regR, acctA + "/" + regR}; !reflect.DeepEqual(w.scanned(), want) {
		t.Fatalf("scans %v", w.scanned())
	}
	same(t, "tags", again[0].Workloads[0].Tags, `{}`)
}

// Kept before the databases had their status, a stopped one was said not stoppable (then never measured).
func TestARegionKeptBeforeItsDatabasesHadTheirStatusIsScannedAgain(t *testing.T) {
	w := newRun(t, func(w *run) { w.regions = []string{regR} })
	regions := w.run()
	raw := asRaw(t, regions[0])
	raw["databases"] = []any{map[string]any{"kind": "rds-instance", "id": "night-db", "allowedFrom": []string{"sg-a"}, "tags": map[string]any{}, "stopped": []any{}, "notStoppable": "stopped when discovered"}}
	w.put(fmt.Sprintf("discovery/%s-%s.json", acctA, regR), raw)
	w.run()
	if want := []string{acctA + "/" + regR, acctA + "/" + regR}; !reflect.DeepEqual(w.scanned(), want) {
		t.Errorf("scans %v", w.scanned())
	}
}

func TestAnAccountWhoseRoleCannotBeAssumedIsNotScannedAnAccountCachedEntirelyAssumesNothing(t *testing.T) {
	assumed := 0
	var mu sync.Mutex
	unreachable := map[string]bool{}
	// As Reach does: a refusal is listed in Unreachable before it is returned.
	refused := target(acctB, func(context.Context) (*aws.Credentials, error) {
		mu.Lock()
		assumed++
		unreachable[acctB] = true
		mu.Unlock()
		return nil, errors.New("not authorized to perform: sts:AssumeRole")
	})
	w := newRun(t, func(w *run) {
		w.accts = []Target{target(acctA), refused}
		w.unreach = func(a string) bool { mu.Lock(); defer mu.Unlock(); return unreachable[a] }
	})
	regions := w.run()
	var accounts []string
	for _, r := range regions {
		accounts = append(accounts, r.Account)
	}
	if !reflect.DeepEqual(accounts, []string{acctA, acctA}) || assumed != 1 {
		t.Errorf("accounts %v, assumed %d", accounts, assumed)
	}
	asked := target(acctA, func(context.Context) (*aws.Credentials, error) { return nil, errors.New("should not be asked") })
	o := w.options(runOver{})
	o.Accounts, o.Unreachable = []Target{asked}, nil
	o.RegionsOf = func(context.Context, string, *aws.Credentials) ([]string, error) {
		return nil, errors.New("should not be asked")
	}
	o.PortsOf = func(context.Context, string, string, *aws.Credentials) (RegionPorts, error) {
		return RegionPorts{}, errors.New("should not be asked")
	}
	again, err := All(context.Background(), o)
	if err != nil || len(again) != 2 {
		t.Errorf("%v %v", again, err)
	}
}

func TestRegionsIsUsedAsGivenWhateverTheCacheHoldsAndNeverCachedAsTheEnabledRegions(t *testing.T) {
	w := newRun(t)
	w.run()
	only := w.run(runOver{regions: []string{regR}})
	if got := rows(only); len(got) != 1 || got[0][0] != regR {
		t.Errorf("only %v", got)
	}
	t2 := newRun(t)
	t2.run(runOver{regions: []string{regR}})
	if t2.listed != 0 {
		t.Errorf("listed %d", t2.listed)
	}
	all := t2.run()
	if got := rows(all); len(got) != 2 || got[0][0] != regR || got[1][0] != "eu-west-3" {
		t.Errorf("all %v", got)
	}
	if t2.listed != 1 {
		t.Errorf("listed %d", t2.listed)
	}
}

func TestAnAccountThatFailsForAnotherReasonThanItsRoleItsRegionsUnreadableIsSaidNotDropped(t *testing.T) {
	w := newRun(t)
	o := w.options(runOver{})
	o.RegionsOf = func(context.Context, string, *aws.Credentials) ([]string, error) {
		return nil, &smithy.GenericAPIError{Code: "UnauthorizedOperation", Message: "You are not authorized to perform this operation."}
	}
	o.PortsOf = func(context.Context, string, string, *aws.Credentials) (RegionPorts, error) {
		return RegionPorts{}, errors.New("not asked")
	}
	out, err := All(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	if want := fmt.Sprintf(`[{"account":%q,"region":"*","workloads":[],"ineligible":[],"error":"UnauthorizedOperation: You are not authorized to perform this operation."}]`, acctA); string(b) != want {
		t.Errorf("got %s", b)
	}
}

func TestFiveAccountsAtATimeByDefaultAsManyAsAsked(t *testing.T) {
	var accounts []Target
	for _, n := range "12345678" {
		accounts = append(accounts, target(string(n)+"1111111111"+string(n)))
	}
	w := newRun(t, func(w *run) { w.accts, w.regions, w.delay = accounts, []string{regR}, 20*time.Millisecond })
	w.run()
	if len(w.scanned()) != 8 || w.most != 5 {
		t.Errorf("scans %d, most %d", len(w.scanned()), w.most)
	}
	for _, concurrency := range []int{1, 2} {
		x := newRun(t, func(x *run) { x.accts, x.regions, x.delay = accounts, []string{regR}, 10*time.Millisecond })
		x.run(runOver{concurrency: concurrency})
		if x.most != concurrency {
			t.Errorf("concurrency %d: most %d", concurrency, x.most)
		}
	}
}

func TestSaysWhenEachAccountStartsWithItsRegionsAndWhenItIsDoneThePhasesProgress(t *testing.T) {
	w := newRun(t, func(w *run) { w.accts = []Target{target(acctA), target(acctB)}; w.failing["eu-west-3"] = true })
	w.run(runOver{concurrency: 1})
	want := []AccountEvent{
		{Account: acctA, State: "started", Regions: 2}, {Account: acctA, State: "done", Regions: 2},
		{Account: acctB, State: "started", Regions: 2}, {Account: acctB, State: "done", Regions: 2},
	}
	if !reflect.DeepEqual(w.events, want) {
		t.Errorf("events %v", w.events)
	}
	x := newRun(t)
	o := x.options(runOver{})
	o.RegionsOf = func(context.Context, string, *aws.Credentials) ([]string, error) {
		return nil, &smithy.GenericAPIError{Code: "UnauthorizedOperation", Message: "not authorized"}
	}
	if _, err := All(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if want := []AccountEvent{{Account: acctA, State: "done", Regions: 0, Error: "UnauthorizedOperation: not authorized"}}; !reflect.DeepEqual(x.events, want) {
		t.Errorf("events %v", x.events)
	}
}

// What the views consume: a region's event says where it came from.
func TestEachRegionSaysWhereItCameFromAndWhatItFound(t *testing.T) {
	w := newRun(t, func(w *run) { w.failing["eu-west-3"] = true })
	w.run()
	w.run()
	want := []ProgressEvent{
		{Phase: "discovery", Account: acctA, Region: regR, From: "aws", Workloads: 1},
		{Phase: "discovery", Account: acctA, Region: "eu-west-3", From: "aws", Error: "The security token included in the request is invalid (an opt-in region not enabled)"},
		{Phase: "discovery", Account: acctA, Region: regR, From: "cache", Workloads: 1},
		{Phase: "discovery", Account: acctA, Region: "eu-west-3", From: "aws", Error: "The security token included in the request is invalid (an opt-in region not enabled)"},
	}
	if !reflect.DeepEqual(w.prog, want) {
		t.Errorf("events %+v", w.prog)
	}
	b, _ := json.Marshal(w.prog[1])
	if string(b) != `{"phase":"discovery","account":"111111111111","region":"eu-west-3","from":"aws","workloads":0,"error":"The security token included in the request is invalid (an opt-in region not enabled)"}` {
		t.Errorf("json %s", b)
	}
}

// Review focus 4: the results are in the organization's order, whatever finished first.
func TestResultsAreInTheOrganizationsOrderUnderConcurrencyWithPortsAnsweringOutOfOrder(t *testing.T) {
	var accounts []Target
	for _, n := range "12345" {
		accounts = append(accounts, target(string(n)+"1111111111"+string(n)))
	}
	w := newRun(t, func(w *run) { w.accts = accounts })
	o := w.options(runOver{concurrency: 5})
	portsOf := o.PortsOf
	o.PortsOf = func(ctx context.Context, account, region string, c *aws.Credentials) (RegionPorts, error) {
		p, err := portsOf(ctx, account, region, c)
		scan := p.Scan
		p.Scan = func(ctx context.Context) (ScanResult, error) {
			// The first accounts answer last.
			time.Sleep(time.Duration(6-int(account[0]-'0')) * 15 * time.Millisecond)
			return scan(ctx)
		}
		return p, err
	}
	out, err := All(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range out {
		got = append(got, r.Account+"/"+r.Region)
	}
	var want []string
	for _, a := range accounts {
		want = append(want, a.Account+"/"+regR, a.Account+"/eu-west-3")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order %v", got)
	}
	if w.scanned()[0][0] != '5' {
		t.Errorf("the ports did not answer out of order: %v", w.scanned())
	}
}

// Credentials: asked once per region scanned (so the five-minute renewal takes effect on a long scan), never for a cached one.
func TestCredentialsAreAskedForEachRegionScannedNotForACachedOne(t *testing.T) {
	asked := 0
	n := 0
	creds := target(acctA, func(context.Context) (*aws.Credentials, error) {
		asked++
		n++
		return &aws.Credentials{AccessKeyID: fmt.Sprintf("key-%d", n)}, nil
	})
	w := newRun(t, func(w *run) { w.accts = []Target{creds}; w.regions = []string{regR, "eu-west-3", "eu-north-1"} })
	var keys []string
	o := w.options(runOver{})
	portsOf := o.PortsOf
	o.PortsOf = func(ctx context.Context, account, region string, c *aws.Credentials) (RegionPorts, error) {
		keys = append(keys, region+"="+c.AccessKeyID)
		return portsOf(ctx, account, region, c)
	}
	// One region is already kept: no credentials for it.
	w.put(fmt.Sprintf("discovery/%s-eu-west-3.json", acctA), RegionDiscovery{Account: acctA, Region: "eu-west-3", Workloads: []Workload{}, Ineligible: []IneligibleRule{}, Databases: []Database{}})
	if _, err := All(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	// regions listed (1), then one per region scanned (2).
	if want := []string{regR + "=key-2", "eu-north-1=key-3"}; !reflect.DeepEqual(keys, want) || asked != 3 {
		t.Errorf("keys %v, asked %d", keys, asked)
	}
}

// Review focus 2: a cache the Node estimate wrote.
func copyParityCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join("..", "..", "testdata", "parity", "cache", "o-a1b2c3d4e5")
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func nodeWritten(t *testing.T, mutate func(path string)) (*run, []RegionDiscovery) {
	t.Helper()
	w := newRun(t, func(w *run) {
		w.dir = copyParityCache(t)
		w.now = time.UnixMilli(1791792000000).Add(time.Hour)
		w.regions = []string{"eu-west-1", "us-east-1"}
	})
	if mutate != nil {
		mutate(filepath.Join(w.dir, "discovery", acctA+"-eu-west-1.json"))
	}
	return w, w.run()
}

func TestANodeWrittenDiscoveryFileIsReadAsIsAndWrittenBackTheSame(t *testing.T) {
	w, regions := nodeWritten(t, nil)
	if len(w.scanned()) != 0 {
		t.Fatalf("scanned %v", w.scanned())
	}
	if len(regions) != 2 || regions[0].Region != "eu-west-1" || len(regions[0].Workloads) != 14 || len(regions[0].Databases) != 10 || len(regions[0].Clusters) != 1 {
		t.Fatalf("regions %+v", regions)
	}
	// What this program writes of it is what Node wrote, field by field.
	raw, _ := os.ReadFile(filepath.Join(w.dir, "discovery", acctA+"-eu-west-1.json"))
	var env struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var want, got any
	json.Unmarshal(env.Value, &want)
	b, _ := json.Marshal(regions[0])
	json.Unmarshal(b, &got)
	if !reflect.DeepEqual(want, got) {
		t.Errorf("round trip:\n got  %s\n want %s", b, env.Value)
	}
}

func TestANodeWrittenDiscoveryFileWhoseDatabasesLackStatusIsScannedAgainTheRegionReadFromThePorts(t *testing.T) {
	w, regions := nodeWritten(t, func(path string) {
		var env map[string]any
		raw, _ := os.ReadFile(path)
		json.Unmarshal(raw, &env)
		for _, d := range env["value"].(map[string]any)["databases"].([]any) {
			delete(d.(map[string]any), "status")
		}
		raw, _ = json.Marshal(env)
		os.WriteFile(path, raw, 0o644)
	})
	if want := []string{acctA + "/eu-west-1"}; !reflect.DeepEqual(w.scanned(), want) {
		t.Fatalf("scanned %v", w.scanned())
	}
	// The region of the ports (one workload), not the cache's (fourteen).
	if len(regions[0].Workloads) != 1 || regions[0].Workloads[0].TargetGroup != acctA+"-eu-west-1" {
		t.Errorf("region %+v", regions[0])
	}
}

func TestAFileGetCannotParseIsScannedAgainNeverACrash(t *testing.T) {
	for name, content := range map[string]string{
		"truncated":   `{"writtenAt":1791792000000,"value":{"account":"1111`,
		"wrong types": `{"writtenAt":1791792000000,"value":{"account":1,"workloads":"nope"}}`,
		"empty":       ``,
	} {
		t.Run(name, func(t *testing.T) {
			w, regions := nodeWritten(t, func(path string) { os.WriteFile(path, []byte(content), 0o644) })
			if want := []string{acctA + "/eu-west-1"}; !reflect.DeepEqual(w.scanned(), want) {
				t.Fatalf("scanned %v", w.scanned())
			}
			if len(regions[0].Workloads) != 1 {
				t.Errorf("region %+v", regions[0])
			}
		})
	}
}
