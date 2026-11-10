package gcponboard

import (
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/fleet"
)

func str(s string) *string { return &s }

func TestWaitingPageSetSendsThePageAsTheOperatorAndSaysIt(t *testing.T) {
	g, r := settingsRig(t)
	r.ID = "shop"
	r.Page = &fleet.PageDoc{Mode: "template", Title: str("Back soon"), Icon: str("none")}
	code, out := g.call(t, WorkloadsPageSet, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	body := g.body(t, "PUT", controllerHost, "/v1/workloads/shop/waiting-page")
	page, _ := body["page"].(map[string]any)
	if page["mode"] != "template" || page["title"] != "Back soon" || body["by"] != "me@example.com" {
		t.Fatalf("%v", body)
	}
	contains(t, out, shopTarget+": waiting page set (template)")
	r.ID = ""
	code, out = g.call(t, RouterPageSet, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	g.body(t, "PUT", controllerHost, "/v1/waiting-page")
	contains(t, out, shopRouter+": waiting page set (template)")
}

func TestWaitingPageShowSaysAwsLines(t *testing.T) {
	g, r := settingsRig(t)
	r.ID = "shop"
	r.Page = &fleet.PageDoc{Mode: "template", Title: str("Back soon")}
	g.call(t, WorkloadsPageSet, r)
	code, out := g.call(t, WorkloadsPageShow, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, shopTarget+": waiting page, template (from the workload)", "title", "Back soon (from the workload)", "set on this workload by me@example.com")
	// The router's level, and clearing.
	g.call(t, RouterPageSet, r)
	_, out = g.call(t, RouterPageShow, r)
	contains(t, out, shopRouter+": waiting page, template", "set on this router by me@example.com")
	code, out = g.call(t, WorkloadsPageClear, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, shopTarget+": waiting page cleared")
	_, out = g.call(t, WorkloadsPageShow, r)
	contains(t, out, "no document at this level: it shows what it inherits")
}

func TestWaitingPageShowSaysACustomPageOffWhileTheSwitchIsOff(t *testing.T) {
	g, r := settingsRig(t)
	g.s.WaitingPages = map[string]map[string]any{"shop": {"mode": "custom", "html": "<h1>zzz</h1>", "by": "me@example.com", "at": "2026-11-08T10:00:00.000Z"}}
	g.s.CustomPagesOff = true
	r.ID = "shop"
	code, out := g.call(t, WorkloadsPageShow, r)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "page     off: custom page, 12 bytes", "warning: custom pages are off on this router: this page is not served")
	// On, it is a custom page as AWS shows it.
	g.s.CustomPagesOff = false
	_, out = g.call(t, WorkloadsPageShow, r)
	contains(t, out, "waiting page, custom (from the workload)", "custom page, 12 bytes")
	if strings.Contains(out, "off") {
		t.Fatalf("%s", out)
	}
}

func TestWaitingPageSetOfACustomPageWhileTheSwitchIsOffPrintsTheRoutersWords(t *testing.T) {
	g, r := settingsRig(t)
	g.s.CustomPagesOff = true
	r.ID, r.Page = "shop", &fleet.PageDoc{Mode: "custom", HTML: str("<p>x</p>")}
	code, out := g.call(t, WorkloadsPageSet, r)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	contains(t, out, "custom pages are off on this router: an administrator turns them on with dormeto gcp routers install --custom-pages")
}

func TestWaitingPageJSONPrintsTheRoutersAnswer(t *testing.T) {
	g, r := settingsRig(t)
	r.ID, r.JSON = "shop", true
	code, out := g.call(t, WorkloadsPageShow, r)
	if code != 0 || !strings.Contains(out, `"effective"`) || !strings.Contains(out, `"warnings"`) {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

func TestWaitingPageSaysAnOlderRouterByItsVersion(t *testing.T) {
	g, r := settingsRig(t)
	g.s.ControllerBeforeWaitingPage = true
	g.s.HealthVersion = "0.38.0"
	r.ID, r.Page = "shop", &fleet.PageDoc{Mode: "template", Title: str("x")}
	want := "router 0.38.0 doesn't have the waiting page: dormeto gcp routers install --version " + embeddedRouterVersion()
	for name, f := range map[string]func() (int, string){
		"show":   func() (int, string) { return g.call(t, WorkloadsPageShow, r) },
		"set":    func() (int, string) { return g.call(t, WorkloadsPageSet, r) },
		"clear":  func() (int, string) { return g.call(t, WorkloadsPageClear, r) },
		"router": func() (int, string) { return g.call(t, RouterPageShow, r) },
	} {
		if code, out := f(); code != 1 || !strings.Contains(out, want) {
			t.Errorf("%s: exit %d, want %q in:\n%s", name, code, want, out)
		}
	}
}

func TestWaitingPageNeedsAWorkloadsID(t *testing.T) {
	g, r := settingsRig(t)
	if code, _ := g.call(t, WorkloadsPageShow, r); code != 2 {
		t.Fatalf("exit %d", code)
	}
}
