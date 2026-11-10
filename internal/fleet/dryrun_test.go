package fleet

import (
	"encoding/json"
	"testing"
)

// A dry run names what each write addresses, as its command does: the target of its path, or the one its body names.
func TestRequestTargetNamesWhatAWriteAddresses(t *testing.T) {
	for _, c := range []struct {
		path, body, want string
	}{
		{"/workloads/r1/w-1/description", `{"text":"x"}`, "r1/w-1"},
		{"/workloads/r1/w%2F1/waiting-page", ``, "r1/w/1"},
		{"/routers/r1/waiting-page", ``, "r1"},
		{"/users/ann@x.io/mfa/reset", ``, "ann@x.io"},
		{"/users", `{"email":"ann@x.io","role":"viewer"}`, "ann@x.io"},
		{"/groups/payments", `{"description":"x"}`, "payments"},
		{"/groups", `{"name":"payments"}`, "payments"},
		{"/exclusions/ex-1", ``, "ex-1"},
		{"/operations", `{"type":"install-router","account":"123456789012","region":"eu-west-1"}`, "123456789012"},
		{"/operations", `{"type":"uninstall-router","router":"r1"}`, "r1"},
		{"/operations", `{"type":"onboard","candidateId":"arn:x"}`, "arn:x"},
		{"/settings/workloads", `{"hibernate":true}`, ""},
		{"/discovery/run", ``, ""},
	} {
		if got := RequestTarget(c.path, json.RawMessage(c.body)); got != c.want {
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

// A client under a dry run keeps each write with its target, and sends none.
func TestDryRunKeepsEachWriteWithItsTarget(t *testing.T) {
	d := &DryRun{}
	d.keep("DELETE", "/exclusions/ex-1", nil)
	got, _ := json.Marshal(d.JSON())
	if string(got) != `{"dryRun":true,"requests":[{"target":"ex-1","method":"DELETE","path":"/exclusions/ex-1"}]}` {
		t.Errorf("%s", got)
	}
}
