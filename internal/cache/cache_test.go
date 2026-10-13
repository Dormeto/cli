package cache

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)

func always(time.Time) bool              { return true }
func never(time.Time) bool               { return false }
func fixed(t time.Time) func() time.Time { return func() time.Time { return t } }

func TestReadsBackWhatItWroteWhileFresh(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, fixed(t0))
	p := "discovery/111122223333-eu-west-1.json"
	if err := c.Put(p, map[string]int{"workloads": 3}); err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	ok, err := c.Get(p, always, &got)
	if err != nil || !ok || got["workloads"] != 3 {
		t.Fatalf("got %v %v %v", ok, err, got)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "discovery"))
	if len(entries) != 1 || entries[0].Name() != "111122223333-eu-west-1.json" {
		t.Fatalf("entries %v", entries)
	}
	if ok, _ := c.Get(p, never, &got); ok {
		t.Fatal("stale read")
	}
	if ok, err := c.Get("discovery/missing.json", always, &got); ok || err != nil {
		t.Fatal("missing")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, p))
	if string(raw) != `{"writtenAt":1791633600000,"value":{"workloads":3}}` {
		t.Fatalf("envelope %s", raw)
	}
}

func TestUnparsableIsMissing(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "requests"), 0o755)
	os.WriteFile(filepath.Join(dir, "requests", "broken.json"), []byte(`{"writtenAt": 1, "value": `), 0o644)
	os.WriteFile(filepath.Join(dir, "requests", "nokey.json"), []byte(`{"writtenAt": 1}`), 0o644)
	os.WriteFile(filepath.Join(dir, "requests", "nostamp.json"), []byte(`{"value": 1}`), 0o644)
	var v any
	for _, n := range []string{"broken", "nokey", "nostamp"} {
		if ok, err := New(dir, fixed(t0)).Get("requests/"+n+".json", always, &v); ok || err != nil {
			t.Fatalf("%s: %v %v", n, ok, err)
		}
	}
}

func TestFreshness(t *testing.T) {
	ms := func(s string) time.Time { x, _ := time.Parse(time.RFC3339, s); return x }
	d := FreshDiscovery(t0)
	if !d(t0.Add(-day+time.Second)) || d(t0.Add(-day-time.Second)) {
		t.Fatal("discovery")
	}
	r := FreshRequests(t0)
	if !r(ms("2026-10-10T00:10:00Z")) || r(ms("2026-10-09T23:50:00Z")) {
		t.Fatal("requests")
	}
}

func TestPrices(t *testing.T) {
	dir := t.TempDir()
	now := t0
	c := New(dir, func() time.Time { return now })
	f := func(x float64) *float64 { return &x }
	pc := c.Prices()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pc.PutPrice("a", f(0.0456), 30))
	must(pc.PutPrice("b", nil, 30))
	must(pc.PutPrice("s", f(0.015), 1))
	again := New(dir, func() time.Time { return now }).Prices()
	p, err := again.GetPrice("a")
	if err != nil || p == nil || p.USD == nil || *p.USD != 0.0456 {
		t.Fatalf("a %v %v", p, err)
	}
	p, _ = again.GetPrice("b")
	if p == nil || p.USD != nil {
		t.Fatalf("b %v", p)
	}
	now = now.Add(day + time.Second)
	if p, _ := again.GetPrice("b"); p != nil {
		t.Fatal("b should expire")
	}
	if p, _ := again.GetPrice("s"); p != nil {
		t.Fatal("s should expire")
	}
	if p, _ := again.GetPrice("a"); p == nil {
		t.Fatal("a stays")
	}
}

func TestDir(t *testing.T) {
	if got := Dir(func(k string) string {
		if k == "XDG_CACHE_HOME" {
			return "/var/cache/me"
		}
		return ""
	}); got != "/var/cache/me/dormeto/estimate" {
		t.Fatal(got)
	}
	home, _ := os.UserHomeDir()
	if got := Dir(func(string) string { return "" }); got != filepath.Join(home, ".cache", "dormeto", "estimate") {
		t.Fatal(got)
	}
}

func TestNodeWrittenFiles(t *testing.T) {
	root := "../../testdata/parity/cache/o-a1b2c3d4e5"
	c := New(root, fixed(t0))
	n := 0
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Base(p) == "prices.json" {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		var got, want any
		ok, err := c.Get(rel, always, &got)
		if err != nil || !ok {
			t.Errorf("%s: %v %v", rel, ok, err)
			return nil
		}
		raw, _ := os.ReadFile(p)
		var env struct{ Value any }
		json.Unmarshal(raw, &env)
		want = env.Value
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s differs", rel)
		}
		n++
		return nil
	})
	if n != 9 {
		t.Fatalf("read %d files", n)
	}
	pc := c.Prices()
	raw, _ := os.ReadFile(filepath.Join(root, "prices.json"))
	var all map[string]struct {
		USD *float64 `json:"usd"`
	}
	json.Unmarshal(raw, &all)
	late := New(root, fixed(time.UnixMilli(1791000000000))).Prices()
	_ = pc
	for k, v := range all {
		p, err := late.GetPrice(k)
		if err != nil || p == nil || (p.USD == nil) != (v.USD == nil) {
			t.Fatalf("%s: %v %v", k, p, err)
		}
	}
	// A null price reads back as unpriced.
	nulls := 0
	for _, v := range all {
		if v.USD == nil {
			nulls++
		}
	}
	if nulls == 0 {
		t.Log("no null price in the recorded cache")
	}
}

func TestFailedRenameLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, fixed(t0))
	// Target is a directory: the rename fails.
	os.MkdirAll(filepath.Join(dir, "discovery", "x.json", "inner"), 0o755)
	if err := c.Put("discovery/x.json", 1); err == nil {
		t.Fatal("expected an error")
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "discovery"))
	if len(entries) != 1 || entries[0].Name() != "x.json" {
		t.Fatalf("stray: %v", entries)
	}
	// Injected failure on a fresh path: no file, no temporary.
	old := rename
	defer func() { rename = old }()
	rename = func(string, string) error { return errors.New("boom") }
	if err := c.Put("uptime/y.json", 1); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, "uptime")); len(entries) != 0 {
		t.Fatalf("stray: %v", entries)
	}
	// An existing file survives a failed replacement.
	rename = old
	must := c.Put("uptime/z.json", "old")
	if must != nil {
		t.Fatal(must)
	}
	rename = func(string, string) error { return errors.New("boom") }
	c.Put("uptime/z.json", "new")
	var s string
	if ok, _ := c.Get("uptime/z.json", always, &s); !ok || s != "old" {
		t.Fatal(s)
	}
}
