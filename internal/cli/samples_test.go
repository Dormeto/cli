package cli

// The estimate's sample (the CLI pages design §4; cmd/dormeto/samples_test.go writes the others): `dormeto aws estimate`,
// run through Main against the fake AWS of these tests (fakeAWS: one account, one region, one workload), as account
// 111122223333, its reports written in a directory of the working one, and kept in docs/samples/aws-estimate.json. It has no --json:
// its JSON is the report it writes. A file that differs from what the run prints now fails the test; rewrite it with
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
	e, _ := fakeAWS(fakeOptions{whoami: func(context.Context) (string, error) { return "111122223333", nil }})
	ports := e.PortsOf
	e.PortsOf = func(ctx context.Context, account, region string, creds *aws.Credentials) (discover.RegionPorts, error) {
		p, err := ports(ctx, account, region, creds)
		scan := p.Scan
		p.Scan = func(ctx context.Context) (discover.ScanResult, error) { // the rule of the profile's account
			r, err := scan(ctx)
			for i := range r.Rules {
				r.Rules[i].Account = account
			}
			return r, err
		}
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
	f := struct {
		Command string  `json:"command"`
		Line    string  `json:"line"`
		Table   string  `json:"table"`
		JSON    *string `json:"json"`
	}{Command: "aws estimate", Line: line, Table: strings.Join(r.logged(), "\n") + "\n"}
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
