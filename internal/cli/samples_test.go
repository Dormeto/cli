package cli

// The estimate's sample (the CLI pages design §4; cmd/dormeto/samples_test.go writes the others): `dormeto aws estimate`,
// run through Main against the fake AWS of these tests (fakeAWS: one account, one region, one workload), as account
// 111122223333, its reports written in a directory of the working one, and kept in docs/samples/aws-estimate.json. It has no --json:
// its json is the JSON report it writes, and its markdown the Markdown report. A file that differs from what the run prints now fails the test; rewrite it with
// go test ./internal/cli -run Samples -update.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/Dormeto/cli/internal/cache"
	"github.com/Dormeto/cli/internal/discover"
)

var updateSamples = flag.Bool("update", false, "rewrite docs/samples from what the command prints")

func TestSamples(t *testing.T) {
	const line = "dormeto aws estimate --profile prod --out reports"
	samples, err := filepath.Abs(filepath.Join("..", "..", "docs", "samples"))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir()) // the reports are written in the working directory
	// Its region holds standalone resources too (standalone_test.go): an instance and a database estimated, a Spot
	// instance left out, the RDS clusters not read.
	e, _, _, _ := standaloneAWSOf(fakeOptions{whoami: func(context.Context) (string, error) { return "111122223333", nil }})
	ports := e.PortsOf
	e.PortsOf = func(ctx context.Context, account, region string, creds *aws.Credentials) (discover.RegionPorts, error) {
		p, err := ports(ctx, account, region, creds)
		// The rule of the profile's account, its ARNs too.
		inAccount := func(scan func(context.Context) (discover.ScanResult, error)) func(context.Context) (discover.ScanResult, error) {
			return func(ctx context.Context) (discover.ScanResult, error) {
				r, err := scan(ctx)
				for i := range r.Rules {
					x := &r.Rules[i]
					x.Account = account
					x.ID, x.LoadBalancerArn = strings.ReplaceAll(x.ID, acctA, account), strings.ReplaceAll(x.LoadBalancerArn, acctA, account)
					if x.TargetGroupArn != nil {
						tg := strings.ReplaceAll(*x.TargetGroupArn, acctA, account)
						x.TargetGroupArn = &tg
					}
				}
				return r, err
			}
		}
		p.Scan, p.Standalone = inAccount(p.Scan), inAccount(p.Standalone)
		return p, err
	}
	r := newRun(t, e)
	r.deps.Now = func() time.Time { return time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC) }
	// The default cache, ~/.cache/dormeto/estimate, as it reads: "~" is a directory of the working one here.
	r.deps.CacheRoot = cache.Dir(func(name string) string { return map[string]string{"XDG_CACHE_HOME": "~/.cache"}[name] })
	if err := os.Mkdir("reports", 0o755); err != nil {
		t.Fatal(err)
	}
	if code := Main(strings.Fields(line)[3:], r.deps); code != 0 {
		t.Fatalf("exit %d: %q", code, r.errors())
	}
	// Its JSON is the report it writes (the time it was written left as it is: the clock is fixed), and its Markdown
	// the other report.
	report, err := os.ReadFile(filepath.Join("reports", "estimate-2026-11-14.json"))
	if err != nil {
		t.Fatal(err)
	}
	markdown, err := os.ReadFile(filepath.Join("reports", "estimate-2026-11-14.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(report)
	f := struct {
		Command  string  `json:"command"`
		Line     string  `json:"line"`
		Table    string  `json:"table"`
		JSON     *string `json:"json"`
		Markdown string  `json:"markdown"`
	}{Command: "aws estimate", Line: line, Table: strings.Join(r.logged(), "\n") + "\n", JSON: &text, Markdown: string(markdown)}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"111111111111", "222222222222", "121014831971", "038606077307", "dormeto-dev", "kloudy", "/var/", "/tmp/", "/private/"} {
		if strings.Contains(b.String(), bad) {
			t.Errorf("the sample holds %q", bad)
		}
	}
	path := filepath.Join(samples, "aws-estimate.json")
	if *updateSamples {
		if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if was, err := os.ReadFile(path); err != nil || !bytes.Equal(was, b.Bytes()) {
		t.Errorf("docs/samples/aws-estimate.json differs from what %q prints now; update it: go test ./internal/cli -run Samples -update\nnow:\n%s", line, b.String())
	}
}
