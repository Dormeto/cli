package fleet

import (
	"encoding/json"
	"slices"
	"testing"
)

// A dry run names what each write addresses, as its command does: the target of its path, or the ones its body names; the
// organization for its own writes.
func TestRequestTargetsNameWhatAWriteAddresses(t *testing.T) {
	for _, c := range []struct {
		path, body string
		want       []string
	}{
		{"/workloads/r1/w-1/description", `{"text":"x"}`, []string{"r1/w-1"}},
		{"/workloads/r1/w%2F1/waiting-page", ``, []string{"r1/w/1"}},
		{"/routers/r1/waiting-page", ``, []string{"r1"}},
		{"/users/ann@x.io/mfa/reset", ``, []string{"ann@x.io"}},
		{"/users", `{"email":"ann@x.io","role":"viewer"}`, []string{"ann@x.io"}},
		{"/groups/payments", `{"description":"x"}`, []string{"payments"}},
		{"/groups", `{"name":"payments"}`, []string{"payments"}},
		{"/exclusions/ex-1", ``, []string{"ex-1"}},
		{"/operations", `{"type":"install-router","account":"123456789012","region":"eu-west-1"}`, []string{"123456789012"}},
		{"/operations", `{"type":"uninstall-router","router":"r1"}`, []string{"r1"}},
		{"/operations", `{"type":"onboard","candidateId":"arn:x"}`, []string{"arn:x"}},
		{"/discovery/dismiss", `{"ids":["arn:a","arn:b"]}`, []string{"arn:a", "arn:b"}},
		{"/settings/workloads", `{"hibernate":true}`, []string{"organization"}},
		{"/discovery/run", ``, []string{"organization"}},
	} {
		if got := RequestTargets(c.path, json.RawMessage(c.body)); !slices.Equal(got, c.want) {
			t.Errorf("%s %s: %q, want %q", c.path, c.body, got, c.want)
		}
	}
}

// One text on both subtrees: the target, the request, then its body.
func TestDryRunTextSaysTheTargetTheRequestAndTheBody(t *testing.T) {
	if got := DryRunText("r1", "PUT", "/iam/routers/r1/description", json.RawMessage(`{"text":"x"}`)); got != "dry run: r1: would PUT /iam/routers/r1/description, nothing sent\n  {\"text\":\"x\"}\n" {
		t.Errorf("%q", got)
	}
	if got := DryRunText("", "POST", "/iam/discovery/run", nil); got != "dry run: would POST /iam/discovery/run, nothing sent\n" {
		t.Errorf("%q", got)
	}
}

// A client under a dry run keeps each write's targets, and sends none: --json prints the released {"dryRun":true,"targets":[…]},
// each target once, the requests said in the text alone.
func TestDryRunKeepsEachWritesTargets(t *testing.T) {
	d := &DryRun{}
	d.keep("DELETE", "/exclusions/ex-1", nil)
	d.keep("DELETE", "/exclusions/ex-2", nil)
	d.keep("DELETE", "/exclusions/ex-1", nil)
	got, _ := json.Marshal(d.JSON())
	if string(got) != `{"dryRun":true,"targets":["ex-1","ex-2"]}` {
		t.Errorf("%s", got)
	}
}
