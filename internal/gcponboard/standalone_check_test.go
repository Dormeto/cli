package gcponboard

// The load-balancer check's reads, the onboard step resourceCheck (router/gcp/access.json): asked by the preflight of an
// onboarding by resource only, skipped with --unchecked, and a line of their own in gcp check.

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/gcp/access"
)

// resourceCheckReads are the seven reads of the load-balancer check.
func resourceCheckReads() []string { return access.StepNeeds("onboard", "resourceCheck") }

// withoutTheCheck makes the caller hold every permission of the workloads commands but the given ones.
func (g *workloadsRig) withoutTheCheck(lacking ...string) {
	g.s.Lock()
	defer g.s.Unlock()
	g.s.HoldsAll = false
	g.s.Held = map[string]bool{}
	for _, command := range access.Commands() {
		for _, p := range access.NeedsWith(command, []string{"cloud-sql"}) {
			g.s.Held[p] = !slices.Contains(lacking, p)
		}
	}
}

// The nine reads (router 6e0bf4a): Google's reference has listInstances and listNetworkEndpoints ask the groups' .get.
func TestTheResourceCheckReadsAreTheChecksNine(t *testing.T) {
	want := []string{"compute.backendServices.list", "compute.instanceGroupManagers.get", "compute.instanceGroups.get", "compute.instanceGroups.list",
		"compute.networkEndpointGroups.get", "compute.networkEndpointGroups.list", "compute.regionBackendServices.list", "compute.targetInstances.list",
		"compute.targetPools.list"}
	got := resourceCheckReads()
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("%v", got)
	}
}

// An HTTP onboarding's preflight does not ask them: it onboards without any.
func TestAnHTTPOnboardingNeedsNoneOfTheResourceChecksReads(t *testing.T) {
	g := newWorkloadsRig(t)
	g.withoutTheCheck(resourceCheckReads()...)
	if code, out := g.call(t, WorkloadsOnboard, shopOnboarding); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// An onboarding by resource's preflight asks them, a missing one said with the listing it serves; --unchecked skips them,
// saying so, and the check then says what it could not list.
func TestAnOnboardingByResourceAsksTheChecksReadsUnlessUnchecked(t *testing.T) {
	g := newStandaloneRig(t)
	g.withoutTheCheck("compute.targetPools.list", "compute.instanceGroups.get")
	mark := len(g.s.Calls())
	r := workloadsEurope
	r.Resource = agentPath
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 1 || len(g.controllerCalls(mark)) != 0 || len(g.bindings()) != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "  ✗ onboard --resource: missing compute.instanceGroups.get (the instance groups' VMs), compute.targetPools.list (target pools)\n",
		"The load-balancer check needs them to tell whether the resource serves a load balancer; --unchecked onboards without them.\nNothing was changed.")
	r.Unchecked = true
	code, out = g.call(t, WorkloadsOnboard, r)
	if code != 0 || len(g.bindings()) != 1 {
		t.Fatalf("--unchecked: exit %d:\n%s", code, out)
	}
	contains(t, out, "  ! --unchecked: the load-balancer check's permissions are not tested\n")
}

// gcp check: onboard's line stays the HTTP onboarding's; the reads have a line of their own, which does not fail the check.
func TestCheckSaysTheResourceChecksReadsOnALineOfTheirOwn(t *testing.T) {
	g := newWorkloadsRig(t)
	g.withoutTheCheck("compute.targetPools.list")
	rep, err := Check(context.Background(), g.d, Request{Project: "dormeto-dev"})
	if err != nil {
		t.Fatal(err)
	}
	if c := command(t, rep, "onboard"); !c.OK() || slices.Contains(c.Held, "compute.targetPools.list") {
		t.Fatalf("onboard %+v", c)
	}
	if c := command(t, rep, "onboard --resource"); !c.Optional || !slices.Equal(c.Missing, []string{"compute.targetPools.list"}) || len(c.Held) != 8 {
		t.Fatalf("onboard --resource %+v", c)
	}
	if !rep.OK() {
		t.Fatal("the optional line failed the check")
	}
	var out bytes.Buffer
	if err := PrintText(&out, rep); err != nil {
		t.Fatal(err)
	}
	contains(t, out.String(), "  ✓ onboard: every permission held\n",
		"  ✗ onboard --resource: missing compute.targetPools.list (needed to onboard a VM or a managed group by --resource: without them it stops, unless --unchecked)\n")
	b, _ := json.Marshal(rep)
	if !strings.Contains(string(b), `{"command":"onboard --resource","held":[`) || !strings.Contains(string(b), `"optional":true`) {
		t.Fatalf("%s", b)
	}
	g.withoutTheCheck()
	rep, _ = Check(context.Background(), g.d, Request{Project: "dormeto-dev"})
	out.Reset()
	_ = PrintText(&out, rep)
	contains(t, out.String(), "  ✓ onboard --resource: every permission held (needed to onboard a VM or a managed group by --resource)\n")
}

// A Cloud SQL instance has no load-balancer check: its onboarding asks none of its reads, and says nothing of --unchecked.
func TestACloudSQLOnboardingByResourceAsksNoneOfTheChecksReads(t *testing.T) {
	g := newStandaloneRig(t)
	g.withoutTheCheck(resourceCheckReads()...)
	r := workloadsEurope
	r.Resource = sqlPath
	code, out := g.call(t, WorkloadsOnboard, r)
	if code != 0 || len(g.bindings()) != 1 || strings.Contains(out, "--unchecked") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// An onboarding by resource reads no backend service (no request logs to read): its preflight does not ask their reads.
func TestAnOnboardingByResourceAsksNoBackendServiceRead(t *testing.T) {
	g := newStandaloneRig(t)
	g.withoutTheCheck("compute.backendServices.get", "compute.regionBackendServices.get")
	r := workloadsEurope
	r.Resource = agentPath
	if code, out := g.call(t, WorkloadsOnboard, r); code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// The HTTP onboarding still does.
	if code, out := g.call(t, WorkloadsOnboard, shopOnboarding); code != 1 || !strings.Contains(out, "compute.backendServices.get") {
		t.Fatalf("HTTP: exit %d:\n%s", code, out)
	}
}
