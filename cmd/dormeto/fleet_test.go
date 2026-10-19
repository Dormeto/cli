package main

// `dormeto routers list` and `dormeto workloads list` against a fake manager API and a fake locator: the requests they
// make (path, query, signature), the tables and JSON they print, the exit codes.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/spf13/cobra"

	"github.com/Dormeto/cli/internal/fleet"
)

const routersAnswer = `{"routers":[
 {"name":"prod-eu","region":"eu-west-1","account":"111111111111","source":"manager","version":"0.29.0","build":"b1","workloads":4,"health":{"status":"healthy","problems":[]},"workloadStates":{"ASLEEP":3,"AWAKE":1},"reporting":true},
 {"name":"prod-us","region":"us-east-1","account":"222222222222","source":"manager","version":"0.30.0","workloads":0,"health":{"status":"healthy"},"reporting":true},
 {"name":"lost","region":"eu-west-1","account":"222222222222","source":"manager","version":null,"workloads":0,"reporting":false,"error":"timed out"}
],"available":{"version":"0.30.0"}}`

const workloadsAnswer = `{"workloads":[
 {"workloadId":"w-1","name":"api","router":"prod-eu","account":"111111111111","region":"eu-west-1","state":"ASLEEP","tags":{"team":"payments","env":"dev"},"stateAt":"2026-10-19T09:00:00Z"},
 {"workloadId":"w-2","router":"prod-us","account":"222222222222","region":"us-east-1","state":"AWAKE","updatedAt":"2026-10-17T12:00:00Z"}
],"total":2,"byState":{"ASLEEP":1,"AWAKE":1},"stats7d":{},"cursor":null,"errors":[{"router":"lost","message":"timed out"},{"router":"lost","message":"timed out again"}]}`

// fakeAPI answers /iam/routers and /iam/workloads, recording the requests.
type fakeAPI struct {
	mu         sync.Mutex
	requests   []*http.Request
	routers    string
	status     int
	body       string            // overrides the answers when set, with status
	posted     []string          // the bodies of POST /iam/operations
	startErr   map[string]string // router -> 409 message
	readStatus int               // when set, reading an operation is refused with this status
	endWith    string            // the status operations end with: succeeded (default), failed or running
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.mu.Unlock()
	if strings.HasPrefix(r.URL.Path, "/iam/operations") {
		f.operations(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if f.body != "" {
		w.WriteHeader(f.status)
		io.WriteString(w, f.body)
		return
	}
	switch r.URL.Path {
	case "/iam/routers":
		io.WriteString(w, orString(f.routers, routersAnswer))
	case "/iam/workloads":
		tag := r.URL.Query()["tag"]
		if len(tag) == 1 && tag[0] == "team=partial" { // prod-eu answered, lost did not
			io.WriteString(w, `{"workloads":[{"workloadId":"w-1","router":"prod-eu","state":"ASLEEP"}],"total":1,"cursor":null,"errors":[{"router":"lost","message":"timed out"},{"router":"lost","message":"again"}]}`)
			return
		}
		if len(tag) == 1 && tag[0] == "team=payments" { // only prod-eu serves these
			io.WriteString(w, `{"workloads":[{"workloadId":"w-1","router":"prod-eu","state":"ASLEEP"}],"total":1,"cursor":null}`)
			return
		}
		io.WriteString(w, workloadsAnswer)
	default:
		w.WriteHeader(404)
	}
}

// sameSet tells whether got holds exactly want, in any order.
func sameSet(got []string, want ...string) bool {
	return len(got) == len(want) && func() bool {
		g := slices.Clone(got)
		slices.Sort(g)
		slices.Sort(want)
		return slices.Equal(g, want)
	}()
}

func orString(s, d string) string {
	if s != "" {
		return s
	}
	return d
}

type locatorFunc func(ctx context.Context, env, region string) (string, error)

func (f locatorFunc) APIURL(ctx context.Context, env, region string) (string, error) {
	return f(ctx, env, region)
}

// fleetRan is a command line run with fake dependencies.
type fleetRan struct {
	stdout, stderr string
	exit           int
	api            *fakeAPI
	located        []string // env/region of each lookup
	profile        string
}

func runFleet(t *testing.T, api *fakeAPI, profileRegion string, args ...string) fleetRan {
	t.Helper()
	return runFleetWith(t, api, profileRegion, nil, args...)
}

// runFleetWith is runFleet with the dependencies adjusted by tune (nil for none).
func runFleetWith(t *testing.T, api *fakeAPI, profileRegion string, tune func(*fleetDeps), args ...string) fleetRan {
	t.Helper()
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	out := fleetRan{api: api}
	deps := fleetDeps{
		load: func(_ context.Context, profile string) (awssdk.Config, error) {
			out.profile = profile
			return awssdk.Config{Region: profileRegion, Credentials: credentials.NewStaticCredentialsProvider("AKID", "secret", "")}, nil
		},
		locator: func(awssdk.Config) fleet.Locator {
			return locatorFunc(func(_ context.Context, env, region string) (string, error) {
				out.located = append(out.located, env+"/"+region)
				return srv.URL, nil
			})
		},
		http:     srv.Client(),
		now:      func() time.Time { return time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC) },
		terminal: func(io.Reader) bool { return false },
		sleep:    func(context.Context, time.Duration) error { return nil },
		timeout:  context.WithTimeout,
		confirm: func(context.Context, io.Reader, io.Writer, string) (bool, error) {
			t.Error("asked for a confirmation")
			return false, nil
		},
	}
	if tune != nil {
		tune(&deps)
	}
	root := &cobra.Command{Use: "dormeto", SilenceUsage: true}
	root.AddCommand(newRouters(deps), newWorkloads(deps))
	var o, e bytes.Buffer
	root.SetOut(&o)
	root.SetErr(&e)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	out.exit = execute(root)
	out.stdout, out.stderr = o.String(), e.String()
	return out
}

func TestRoutersListTable(t *testing.T) {
	r := runFleet(t, &fakeAPI{}, "eu-west-1", "routers", "list", "--profile", "ops")
	t.Logf("\n%s", r.stdout)
	if r.exit != 0 || r.profile != "ops" || len(r.located) != 1 || r.located[0] != "prod/eu-west-1" {
		t.Fatalf("exit %d, profile %q, located %v, stderr %q", r.exit, r.profile, r.located, r.stderr)
	}
	for _, want := range []string{"NAME", "0.29.0 → 0.30.0", "4 (3 ASLEEP, 1 AWAKE)", "lost"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q", want)
		}
	}
	lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
	if last := lines[len(lines)-1]; last != "available 0.30.0 · 1 of 3 outdated" {
		t.Errorf("last line %q", last)
	}
	if r.stderr != "router lost did not answer: timed out\n" {
		t.Errorf("stderr %q", r.stderr)
	}
	req := r.api.requests[0]
	if req.Method != "GET" || req.URL.Path != "/iam/routers" || !strings.Contains(req.Header.Get("Authorization"), "Credential=AKID/20261019/eu-west-1/execute-api/") {
		t.Errorf("request %s %s, authorization %q", req.Method, req.URL, req.Header.Get("Authorization"))
	}
}

func TestRoutersListOutdatedJSON(t *testing.T) {
	r := runFleet(t, &fakeAPI{}, "eu-west-1", "routers", "list", "--outdated", "--json")
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	var got struct {
		Routers []struct {
			Name  string
			Build string
		}
		Available struct{ Version string }
	}
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil {
		t.Fatalf("%v: %q", err, r.stdout)
	}
	if len(got.Routers) != 1 || got.Routers[0].Name != "prod-eu" || got.Routers[0].Build != "b1" || got.Available.Version != "0.30.0" {
		t.Errorf("got %+v", got)
	}
}

func TestRoutersListFilters(t *testing.T) {
	r := runFleet(t, &fakeAPI{}, "eu-west-1", "routers", "list", "--account", "111111111111,222222222222", "--router-region", "us-east-1", "--name", "prod-*", "--json")
	if r.exit != 0 || !strings.Contains(r.stdout, `"prod-us"`) || strings.Contains(r.stdout, `"prod-eu"`) || strings.Contains(r.stdout, `"lost"`) {
		t.Errorf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
}

func TestRoutersListTagsSelectTheRoutersOfTheirWorkloads(t *testing.T) {
	r := runFleet(t, &fakeAPI{}, "eu-west-1", "routers", "list", "--tag", "team=payments", "--json")
	if r.exit != 0 || !strings.Contains(r.stdout, `"prod-eu"`) || strings.Contains(r.stdout, `"prod-us"`) {
		t.Fatalf("exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
	var paths []string
	for _, q := range r.api.requests {
		paths = append(paths, q.URL.Path)
	}
	if strings.Join(paths, " ") != "/iam/routers /iam/workloads" {
		t.Errorf("requests %v", paths)
	}
	if got := r.api.requests[1].URL.Query()["tag"]; len(got) != 1 || got[0] != "team=payments" {
		t.Errorf("tag = %v", got)
	}
}

func TestRoutersListTagsWarnOfRoutersThatDidNotAnswer(t *testing.T) {
	r := runFleet(t, &fakeAPI{}, "eu-west-1", "routers", "list", "--tag", "team=partial", "--json")
	if r.exit != 0 || !strings.Contains(r.stdout, `"prod-eu"`) || strings.Contains(r.stdout, `"prod-us"`) {
		t.Fatalf("exit %d, stdout %q", r.exit, r.stdout)
	}
	if want := "router lost did not answer: timed out (its workloads' tags are unknown)\n"; r.stderr != want {
		t.Errorf("stderr %q, want %q", r.stderr, want)
	}
}

func TestRoutersListReportsEveryRouterErrorWhateverIsShown(t *testing.T) {
	r := runFleet(t, &fakeAPI{}, "eu-west-1", "routers", "list", "--outdated", "--json")
	if strings.Contains(r.stdout, `"lost"`) {
		t.Errorf("the router with an unknown version is shown: %q", r.stdout)
	}
	if r.stderr != "router lost did not answer: timed out\n" {
		t.Errorf("stderr %q", r.stderr)
	}
}

func TestWorkloadsListSendsTheFilters(t *testing.T) {
	r := runFleet(t, &fakeAPI{}, "eu-west-1", "workloads", "list", "--tag", "team=payments", "--tag", "env=dev", "--state", "asleep",
		"--router", "prod-eu", "--search", "api")
	t.Logf("\n%s\n%s", r.stdout, r.stderr)
	if r.exit != 0 {
		t.Fatalf("exit %d: %s", r.exit, r.stderr)
	}
	req := r.api.requests[0]
	q := req.URL.Query()
	if req.URL.Path != "/iam/workloads" || !sameSet(q["tag"], "team=payments", "env=dev") || q.Get("state") != "ASLEEP" ||
		q.Get("router") != "prod-eu" || q.Get("q") != "api" {
		t.Errorf("request %s", req.URL)
	}
	// The SigV4 signer sorts the values of a parameter into the canonical query, so the order sent is not the flags'.
	if !strings.Contains(req.URL.RawQuery, "tag=env%3Ddev&tag=team%3Dpayments") {
		t.Errorf("query %q lacks the encoded tags", req.URL.RawQuery)
	}
	for _, want := range []string{"ROUTER", "env=dev,team=payments", "3h", "2d"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout lacks %q", want)
		}
	}
	lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
	if last := lines[len(lines)-1]; last != "2 workloads (1 ASLEEP, 1 AWAKE)" {
		t.Errorf("last line %q", last)
	}
	// A router that does not answer is warned about once, whatever the entries.
	if r.stderr != "router lost did not answer: timed out\n" {
		t.Errorf("stderr %q", r.stderr)
	}
}

func TestWorkloadsListStatesAreJoined(t *testing.T) {
	r := runFleet(t, &fakeAPI{}, "eu-west-1", "workloads", "list", "--state", "asleep,awake", "--state", "waking")
	if got := r.api.requests[0].URL.Query().Get("state"); r.exit != 0 || got != "ASLEEP,AWAKE,WAKING" {
		t.Errorf("exit %d, state %q", r.exit, got)
	}
}

func TestWorkloadsListJSONIsTheAnswer(t *testing.T) {
	r := runFleet(t, &fakeAPI{}, "eu-west-1", "workloads", "list", "--json")
	var got map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || got["total"] != float64(2) || got["byState"] == nil {
		t.Errorf("%v: %q", err, r.stdout)
	}
}

func TestFleetBadArguments(t *testing.T) {
	for _, args := range [][]string{
		{"routers", "list", "--tag", "team"},
		{"routers", "list", "--tag", "a=1", "--tag", "a=2"},
		{"workloads", "list", "--tag", "=x"},
		{"workloads", "list", "--tag", "a=b,c"},
		{"routers", "list", "--name", "[a"},
		{"routers", "list", "extra"},
		{"workloads", "list", "--nope"},
		{"routers", "list", "--env", "Prod"},
	} {
		api := &fakeAPI{}
		r := runFleet(t, api, "eu-west-1", args...)
		if r.exit != 2 || r.stderr == "" || len(api.requests) != 0 {
			t.Errorf("%v: exit %d, stderr %q, %d requests", args, r.exit, r.stderr, len(api.requests))
		}
	}
	r := runFleet(t, &fakeAPI{}, "", "routers", "list", "--tag", "team")
	if !strings.Contains(r.stderr, `--tag: "team" is not key=value`) {
		t.Errorf("stderr %q", r.stderr)
	}
}

func TestFleetNeedsTheManagersRegion(t *testing.T) {
	r := runFleet(t, &fakeAPI{}, "", "routers", "list")
	if r.exit != 2 || r.stderr != "--region: the manager's region is needed (the profile has none)\n" {
		t.Errorf("exit %d, stderr %q", r.exit, r.stderr)
	}
	r = runFleet(t, &fakeAPI{}, "", "routers", "list", "--region", "eu-west-3", "--env", "dev")
	if r.exit != 0 || r.located[0] != "dev/eu-west-3" {
		t.Errorf("exit %d, located %v, stderr %q", r.exit, r.located, r.stderr)
	}
}

func TestFleetAPIURLSkipsTheLookupAndSignsForItsRegion(t *testing.T) {
	api := &fakeAPI{}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	// The URL is the fake server's, so the signing region comes from the flag; the host form is checked on its own.
	r := runFleet(t, &fakeAPI{}, "eu-west-1", "routers", "list", "--api-url", srv.URL, "--region", "us-east-2")
	if r.exit != 0 || len(r.located) != 0 || len(api.requests) != 1 {
		t.Fatalf("exit %d, located %v, requests %d, stderr %q", r.exit, r.located, len(api.requests), r.stderr)
	}
	if a := api.requests[0].Header.Get("Authorization"); !strings.Contains(a, "/us-east-2/execute-api/") {
		t.Errorf("authorization %q", a)
	}
	for url, want := range map[string]string{
		"https://abc123.execute-api.eu-west-3.amazonaws.com":      "eu-west-3",
		"https://abc123.execute-api.cn-north-1.amazonaws.com.cn/": "cn-north-1",
		"https://example.com": "",
	} {
		if got := signingRegion(url); got != want {
			t.Errorf("signingRegion(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestFleetRefusals(t *testing.T) {
	cases := []struct {
		name         string
		api          *fakeAPI
		locate       error
		want         string
		wantRequests int
	}{
		{"forbidden", &fakeAPI{status: 403, body: `{"message":"Forbidden"}`}, nil, "execute-api:Invoke", 1},
		{"manager's message", &fakeAPI{status: 409, body: `{"error":"conflict","message":"an upgrade is running"}`}, nil, "an upgrade is running", 1},
		{"no api", &fakeAPI{}, errors.Join(fleet.ErrNoAPI, errors.New("no API named dmt-prod-manager-api for env prod in region eu-west-1")), "--api-url", 0},
	}
	for _, c := range cases {
		srv := httptest.NewServer(c.api)
		deps := fleetDeps{
			load: func(context.Context, string) (awssdk.Config, error) {
				return awssdk.Config{Region: "eu-west-1", Credentials: credentials.NewStaticCredentialsProvider("A", "s", "")}, nil
			},
			locator: func(awssdk.Config) fleet.Locator {
				return locatorFunc(func(context.Context, string, string) (string, error) { return srv.URL, c.locate })
			},
			http: srv.Client(), now: time.Now, terminal: func(io.Reader) bool { return false },
		}
		root := &cobra.Command{Use: "dormeto", SilenceUsage: true}
		root.AddCommand(newRouters(deps))
		var o, e bytes.Buffer
		root.SetOut(&o)
		root.SetErr(&e)
		root.SetArgs([]string{"routers", "list"})
		exit := execute(root)
		srv.Close()
		if exit != 1 || !strings.Contains(e.String(), c.want) || o.String() != "" || len(c.api.requests) != c.wantRequests {
			t.Errorf("%s: exit %d, stdout %q, stderr %q, %d requests", c.name, exit, o.String(), e.String(), len(c.api.requests))
		}
	}
}
