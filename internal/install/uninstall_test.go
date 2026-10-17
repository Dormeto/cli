package install

// `dormeto uninstall` and `dormeto uninstall apply` (uninstall.go), and the plan's kind that keeps each explicit apply to
// its own kind of plan (ruling R15), through run_test.go's fakes.

import (
	"strings"
	"testing"
)

// install apply never applies a destroy plan: uninstall apply does.
func TestApplyRefusesADestroyPlan(t *testing.T) {
	w := rwPlanned(t)
	s := w.saved()
	s.LastPlanKind = "destroy"
	w.save(s)
	code, out, _ := w.run(Apply, Request{Profile: "p", Region: "eu-west-1"}, false)
	if code != 1 || !strings.HasSuffix(out, "the last plan destroys the manager: dormeto uninstall apply applies it\n") || len(w.writes) != 0 {
		t.Errorf("exit code %d, writes %v:\n%s", code, w.writes, out)
	}
}

// A new plan of install is an install plan, whatever the kind of the last one.
func TestInstallPlanIsOfKindApply(t *testing.T) {
	w := rwPlanned(t)
	s := w.saved()
	s.LastPlanKind = "destroy"
	w.save(s)
	code, out, _ := w.run(Install, Request{Profile: "p", Region: "eu-west-1"}, false)
	if s := w.saved(); code != 0 || s.LastPlan != "dmt-prod-install:b1" || s.LastPlanKind != "apply" {
		t.Errorf("exit code %d, install.json %+v:\n%s", code, s, out)
	}
}
