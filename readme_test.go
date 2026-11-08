package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/aws"
	"github.com/Dormeto/cli/internal/options"
	"github.com/Dormeto/cli/internal/schedule"
)

func readme(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// section is the text of the "## <title>" section, up to the next "## ".
func section(t *testing.T, text, title string) string {
	t.Helper()
	_, rest, ok := strings.Cut(text, "\n## "+title+"\n")
	if !ok {
		t.Fatalf("README.md has no '## %s' section", title)
	}
	body, _, _ := strings.Cut(rest, "\n## ")
	return body
}

func names(re *regexp.Regexp, text string) []string {
	var out []string
	for _, m := range re.FindAllStringSubmatch(text, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	slices.Sort(out)
	return out
}

// The README's "Options" section explains each option as a bullet starting with "- `--name`": exactly the options
// of the usage, none missing and none extra.
func TestReadmeOptionsAreTheUsageOptions(t *testing.T) {
	want := names(regexp.MustCompile(`--([a-z][a-z0-9-]*)`), options.Usage)
	if len(want) == 0 {
		t.Fatal("no option found in the usage")
	}
	text := section(t, readme(t), "Options")
	got := names(regexp.MustCompile("(?m)^- `--([a-z][a-z0-9-]*)"), text)
	if !slices.Equal(want, got) {
		t.Errorf("README options %v, usage options %v", got, want)
	}
	if !strings.Contains(text, options.Usage) {
		t.Errorf("the Options section does not contain the usage text verbatim")
	}
}

// The relative links of the README name files that exist; the website's links are https://dormeto.com/... ones,
// which are answered 200 at review (no network in the tests).
func TestReadmeRelativeLinksResolve(t *testing.T) {
	text := readme(t)
	links := regexp.MustCompile(`\]\(([^)\s]+)\)`).FindAllStringSubmatch(text, -1)
	if len(links) == 0 {
		t.Fatal("no link in README.md")
	}
	for _, m := range links {
		target := m[1]
		if strings.Contains(target, "://") || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "mailto:") {
			continue
		}
		path, _, _ := strings.Cut(target, "#")
		if _, err := os.Stat(filepath.FromSlash(path)); err != nil {
			t.Errorf("link %q: %v", target, err)
		}
	}
}

func TestReadmeSections(t *testing.T) {
	text := readme(t)
	for _, title := range []string{"Install", "First run", "Options", "Install the manager", "Permissions", "Report", "Cache", "Privacy",
		"Routers and workloads", "Discovery and exclusions", "Operations", "Users and groups", "Audit, savings and alerts", "The manager",
		"Raw calls", "Releasing", "License"} {
		section(t, text, title)
	}
}

// The manager's commands of CLI 0.7.0, as `dormeto --help` and each group's --help list them.
var managerCommands = []string{
	"whoami", "api", "overview",
	"routers list", "routers show", "routers install", "routers uninstall", "routers upgrade", "routers pause", "routers resume", "routers settings", "routers description",
	"workloads list", "workloads show", "workloads history", "workloads idle-window", "workloads wake", "workloads nap",
	"workloads pause", "workloads keep-awake", "workloads resume", "workloads rename", "workloads tags",
	"workloads schedule show", "workloads schedule set", "workloads schedule clear",
	"workloads waiting-page show", "workloads waiting-page set", "workloads waiting-page clear",
	"routers waiting-page show", "routers waiting-page set", "routers waiting-page clear",
	"settings waiting-page show", "settings waiting-page set", "settings waiting-page clear",
	"workloads settings", "workloads description", "workloads onboard", "workloads offboard", "workloads exclude", "settings show", "settings set",
	"discovery status", "discovery run", "discovery candidates", "discovery dismiss", "discovery restore", "discovery access",
	"discovery config show", "discovery config set",
	"exclusions list", "exclusions add", "exclusions lift",
	"operations list", "operations show", "operations watch",
	"users list", "users invite", "users role", "users groups", "users disable", "users enable", "users resend", "users reset-mfa",
	"groups list", "groups show", "groups create", "groups update", "groups delete",
	"audit", "savings show", "savings discounts", "alerts list", "ip-lists list",
	"manager status", "manager check", "manager changes", "manager update",
}

// Every command of the manager is shown in the README with its usage: a line "dormeto aws <command> ..." in a code block.
func TestReadmeShowsEveryManagerCommand(t *testing.T) {
	var code strings.Builder
	for _, m := range regexp.MustCompile("(?s)```[a-z]*\n(.*?)```").FindAllStringSubmatch(readme(t), -1) {
		code.WriteString(m[1])
	}
	for _, command := range managerCommands {
		if !regexp.MustCompile(`(?m)^dormeto aws ` + regexp.QuoteMeta(command) + `( |$)`).MatchString(code.String()) {
			t.Errorf("no code block of the README shows dormeto aws %s", command)
		}
	}
}

// The permission to call the manager gives its policies as JSON that IAM takes: execute-api:Invoke on routes of the
// manager's API under /iam/, one read-only, one that also wakes and naps, one with every route.
func TestReadmeManagerPoliciesAreIAMPolicies(t *testing.T) {
	_, sub, ok := strings.Cut(section(t, readme(t), "Routers and workloads"), "\n### Permission to call the manager\n")
	if !ok {
		t.Fatal("no '### Permission to call the manager' subsection")
	}
	sub, _, _ = strings.Cut(sub, "\n### ")
	var resources []string
	blocks := regexp.MustCompile("(?s)```json\n(.*?)```").FindAllStringSubmatch(sub, -1)
	for _, m := range blocks {
		var policy struct {
			Version   string
			Statement []struct {
				Effect, Action string
				Resource       any
			}
		}
		if err := json.Unmarshal([]byte(m[1]), &policy); err != nil {
			t.Fatalf("a policy is not JSON: %v\n%s", err, m[1])
		}
		if policy.Version != "2012-10-17" || len(policy.Statement) == 0 {
			t.Errorf("a policy without its version or a statement:\n%s", m[1])
		}
		for _, s := range policy.Statement {
			if s.Effect != "Allow" || s.Action != "execute-api:Invoke" {
				t.Errorf("a statement is not Allow execute-api:Invoke: %+v", s)
			}
			switch r := s.Resource.(type) {
			case string:
				resources = append(resources, r)
			case []any:
				for _, v := range r {
					s, _ := v.(string)
					resources = append(resources, s)
				}
			}
		}
	}
	if len(blocks) != 3 {
		t.Errorf("%d policies, want 3 (read-only, wake and nap, every route)", len(blocks))
	}
	route := regexp.MustCompile(`^arn:aws:execute-api:<region>:<account>:<api id>/\*/(\*|GET|POST|PUT|DELETE)/iam/[a-z*/-]*$`)
	for _, r := range resources {
		if !route.MatchString(r) {
			t.Errorf("resource %q is not a route of the manager's API under /iam/", r)
		}
	}
	// The wake-and-nap policy reads /me and /health too, so that whoami, run first, works under it.
	for _, want := range []string{"/*/GET/iam/*", "/*/GET/iam/me", "/*/GET/iam/health", "/*/POST/iam/workloads/*/wake", "/*/POST/iam/workloads/*/nap", "/*/*/iam/*"} {
		if !slices.ContainsFunc(resources, func(r string) bool { return strings.HasSuffix(r, want) }) {
			t.Errorf("no policy has the resource .../%s", want)
		}
	}
}

// Every operation the adapters call appears in the Permissions section as service:Operation, so a user granting the
// list literally has what the estimate needs. elasticloadbalancing:Describe* covers the load balancer operations.
func TestReadmePermissionsCoverTheAdaptersOperations(t *testing.T) {
	text := section(t, readme(t), "Permissions")
	service := map[string]string{"elasticloadbalancingv2": "elasticloadbalancing"}
	for _, op := range aws.ReadOnlyOperations {
		svc, name, _ := strings.Cut(op, ".")
		if s, ok := service[svc]; ok {
			svc = s
		}
		var ok bool
		for _, line := range strings.Split(text, "\n") {
			rest, found := strings.CutPrefix(strings.TrimSpace(line), "- `"+svc+":")
			if !found {
				continue
			}
			rest, _, _ = strings.Cut(rest, " (")
			for _, item := range strings.Split(rest, ",") {
				item = strings.Trim(strings.TrimSpace(item), "`")
				if item == name || (strings.HasSuffix(item, "*") && strings.HasPrefix(name, strings.TrimSuffix(item, "*"))) {
					ok = true
				}
			}
		}
		if !ok {
			t.Errorf("the Permissions section does not list %s:%s", svc, name)
		}
	}
}

func TestReadmeGoogleCloudSaysWhoHoldsTheInvokerRoleAndHowLongGoogleHoldsDeletedNames(t *testing.T) {
	text := strings.Join(strings.Fields(section(t, readme(t), "Google Cloud")), " ")
	if strings.Contains(text, "install gives it to its caller") {
		t.Error("install grants the invoker role only to the router's service account")
	}
	for _, want := range []string{
		"the controller's invoker binding (`roles/run.invoker`, to the router's service account only)",
		"the waker's invoker check off",
		"`roles/run.invoker` on the controller",
		"a deleted queue's name blocked for up to 3 days",
		"a deleted database's ID for about 5 minutes",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the Google Cloud section lacks %q", want)
		}
	}
}

// Google Cloud's coverage (vision docs/specs/2026-10-30-google-cloud-coverage-design.md, sections 4 to 9, and the
// measures of section 10): the README names each feature, flag and command, what Google adds per feature, and the limits
// a user must know before relying on them, with the router version they ship in.
func TestReadmeGoogleCloudCoversTheFeaturesTheirFlagsAndTheirLimits(t *testing.T) {
	text := strings.Join(strings.Fields(section(t, readme(t), "Google Cloud")), " ")
	for _, want := range []string{
		// the features, and what Google adds for each (measured 2026-11-04)
		"--with cloud-sql,gke", "dormetoRouterDatabasesRead", "dormetoRouterDatabases", "dormetoRouterKubernetes",
		"`roles/container.serviceAgent`", "`roles/file.serviceAgent`", "`roles/container.defaultNodeServiceAgent`",
		"container-engine-robot", "cloud-filer", "gcp-sa-gkenode", "gcp-sa-cloud-sql",
		"`autoscaling`", "`containerfilesystem`", "`dns`", "`file`", "`gkebackup`", "`networkconnectivity`",
		// IP lists
		"--ip-list name=<n>,url=<u>[,refresh=<hours>]", "--no-ip-lists", "`DMT_IP_LISTS`", "512 MiB",
		"a listed address's `POST /_dormeto/wake` still wakes",
		// schedules and pauses
		"workloads schedule show", "workloads schedule set", "workloads schedule clear", "--reason", "--until", "at most 10 scheduled naps",
		"`--keep-asleep`", "a visitor still wakes it",
		// the onboarding's flags
		"--load-balancer global|regional|classic", "--wake-timeout", "60 to 1700", "--cloud-sql <instance>", "--gke-cluster <location>/<name>",
		// the Kubernetes access
		"workloads access show", "workloads access grant", "workloads access revoke", "dormeto-<workload>", "nothing cluster-wide",
		"`container.roles.escalate`", "`container.roles.bind`",
		// the limits
		"read replicas", "keep running, and billing", "a read replica itself is refused", "A database belongs to one router",
		"Standard cluster", "`k8s2-`", "`gkegw1-`", "`gkemcg1-`", "`notGivenBack`", "exits 1",
		// the versions and how far it was tried: accepted on 2026-11-04 but for ten workloads on one URL map (vision
		// docs/plans/2026-10-30-google-cloud-coverage.md, "Acceptance results"), with the acceptance's times
		"router 0.37.0", "CLI 0.11.0",
		"*Accepted on a real project on 2026-11-04*, with router 0.37.0 and CLI 0.11.0, both released on 2026-11-05",
		"Not run: an operator holding exactly the permissions `gcp check` lists for `onboard`, and a Cloud SQL start refused for lack of capacity",
		"*Accepted on a real project on 2026-11-08*, with router image 0.38.0-dev.2 and the CLI built from `main`",
		"Ten workloads on one URL map in one nap window were accepted on 2026-11-08, global and regional",
		"Ten naps on one regional map were all asleep within 283 s, none failed",
		"which are not released yet. Their acceptance passed on 2026-11-08",
		"62 s after a `POST /_dormeto/wake`", "`AWAKE` 123 to 188 s and 287 s after the wake", "551 s",
		// the router's image (vision docs/specs/2026-11-07-google-cloud-fixes-design.md, §4), in the words the CLI prints
		"`<europe|us|asia>-docker.pkg.dev/dormeto-releases/router/router:<version>`", "`<name>@sha256:…`",
		"Unlike `dormeto aws install`, whose default is the latest manager release because that release carries its own access, `dormeto gcp install` writes the router's IAM from the manifest this CLI embeds",
		"`router <v> needs a newer CLI: brew upgrade dormeto`",
		"`router <v> is newer than this CLI knows (<default>): brew upgrade dormeto`",
		"`the router isn't installed: dormeto gcp install --version <default>`",
		"`router <v> is available: dormeto gcp install --version <v>`",
		"`--image`, `--version` and `--access-only` exclude each other",
		// the request logs (§5) and the log view, in the words the CLI prints
		"`! the idle check counts every request, scanners included: --request-logs reads the requests instead (Cloud Logging's price past its free tier)`",
		"`--request-logs needs router 0.38.0 or later: dormeto gcp install --version <default>`",
		"`! the router's log view isn't created: the project's _Default log bucket isn't in global; the idle check counts every request`",
		"log_config { enable = true sample_rate = 1.0 }",
		"router 0.38.0 and CLI 0.12.0, which are not released yet",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the Google Cloud section lacks %q", want)
		}
	}
	// The warning that Cloud SQL and GKE never ran against Google is gone (CLI 564f41b).
	for _, gone := range []string{"router:0.35.0", "Written, never run against Google", "acceptance not run yet", "prints one warning on stderr", "were not run in the acceptance", "only 2 or 3 of them may nap", "and a nap cut short by a new revision of the controller",
		// a one-line block takes one argument: not HCL
		"log_config { enable = true, sample_rate = 1.0 }"} {
		if strings.Contains(text, gone) {
			t.Errorf("the Google Cloud section still says %q", gone)
		}
	}
	// G4 is the plan's name, not the user's.
	if strings.Contains(readme(t), "G4") {
		t.Error("the README names the plan G4")
	}
}

// A caller of the manager's /iam routes counts as an administrator, so the permission to write a waiting page is the
// permission to set custom HTML on the application's address: the README says it in the section and at the table's rows.
func TestReadmeWarnsThatWaitingPageWritesGiveCustomHTML(t *testing.T) {
	text := readme(t)
	_, sub, ok := strings.Cut(text, "### Waiting page\n")
	if !ok {
		t.Fatal("no '### Waiting page' subsection")
	}
	sub, _, _ = strings.Cut(sub, "\n### ")
	for _, want := range []string{"administrator", "/iam/", "custom HTML"} {
		if !strings.Contains(sub, want) {
			t.Errorf("the Waiting page section does not say %q", want)
		}
	}
	for _, row := range regexp.MustCompile("(?m)^\\| `[a-z]+ waiting-page set`.*$").FindAllString(text, -1) {
		if !strings.Contains(row, "custom HTML") {
			t.Errorf("a permission row does not warn of custom HTML: %s", row)
		}
	}
}

// The recurrences' table shows a window of each pattern and the words `schedule show` prints for it, and says the rules a
// reader needs before the router refuses a file.
func TestReadmeRecurrencesShowEachPatternInTheCLIsWords(t *testing.T) {
	_, sub, ok := strings.Cut(section(t, readme(t), "Routers and workloads"), "\n### Schedule recurrences\n")
	if !ok {
		t.Fatal("no '### Schedule recurrences' subsection")
	}
	sub, _, _ = strings.Cut(sub, "\n### ")
	patterns := map[string]bool{}
	rows := regexp.MustCompile("(?m)^\\| [^|]+ \\| `(\\{.*\\})` \\| `([^`]+)` \\|$").FindAllStringSubmatch(sub, -1)
	for _, row := range rows {
		var w schedule.Window
		if err := json.Unmarshal([]byte(row[1]), &w); err != nil {
			t.Fatalf("%s: %v", row[1], err)
		}
		if got := w.Text(); got != row[2] {
			t.Errorf("%s reads %q, the README says %q", row[1], got, row[2])
		}
		var r struct {
			Pattern string `json:"pattern"`
		}
		if json.Unmarshal(w.Repeat, &r) == nil {
			patterns[r.Pattern] = true
		}
	}
	for _, p := range []string{"daily", "weekly", "monthly", "cron"} {
		if !patterns[p] {
			t.Errorf("no row of the %s pattern in %d rows", p, len(rows))
		}
	}
	for _, want := range []string{"router 0.36.0", "five fields", "multiple of 5", "`cron(...)`", "`from`", "`until`", "no end date", "last day",
		"dormeto aws workloads schedule set", "--tag"} {
		if !strings.Contains(sub, want) {
			t.Errorf("the recurrences lack %q", want)
		}
	}
}

// Google Cloud's AWS features (vision docs/specs/2026-11-08-google-cloud-router-features-design.md, sections 3 to 8 and 10): the README
// names each command and flag, the values Google Cloud fixes with their reasons, what each request lets a rule see, the page's
// custom switch, the fallback machine types, the internal kinds and GKE's exact refusal, in the words the router and the CLI print.
func TestReadmeGoogleCloudDocumentsSettingsTheWaitingPageFallbackTypesAndTheInternalKinds(t *testing.T) {
	text := strings.Join(strings.Fields(section(t, readme(t), "Google Cloud")), " ")
	for _, want := range []string{
		// the levels and the commands
		"### Google Cloud settings and their levels",
		"dormeto gcp workloads settings", "dormeto gcp routers settings", "`--idle-minutes`", "`--failed-retry-seconds`", "`--no-wake-rules-file`", "`--wake-rules-file`", "`--hibernate`", "`--type`", "`--join-minutes`",
		"The organization level and the custom types are the manager's",
		"`instance` is a VM of an unmanaged instance group or of a zonal network endpoint group, `asg` a managed instance group, `k8s` GKE pods and `databases` a Cloud SQL companion",
		"`router <version> doesn't have the settings levels: dormeto gcp install --version <this CLI's>`",
		"`set on this workload`, `type <name>`, `router`, `built-in` or `fixed on Google Cloud`",
		// what Google Cloud fixes, and why
		"| `--wait-seconds` | 0 | The waker holds no request.",
		"| `--max-holders` | 0 | No request is held. |",
		"| `--drain-seconds` | 0 | The nap's switch is a URL map change, and the backend service's own connection draining applies. |",
		"| `--wake-mode` | `browser` behind the global load balancer's policy |",
		"| `--wake-timeout-seconds` | at most 1700 | One controller request lasts at most 1800 s. |",
		"more than the swap's wait + 60 s behind a swap",
		"An explicit workload value outside these bounds is refused; an inherited one is brought into them and shown as `fixed`",
		"`the global load balancer's waiting page can't see the visitor's request, so only the page's probe, an explicit wake or a wake rule wakes it (wake mode browser)`",
		// the wake mode and the rules
		"Behind a swap (a regional, classic or internal load balancer), the wake mode is `any` unless you set `browser`",
		"a request the filters let through wakes the workload and is answered with the waiting page at once",
		"at most 20 rules per list and level",
		"the first match decides",
		"| the page's probe | the client's address | the browser's request | the visitor's path, which the page sends in `x-dormeto-path` |",
		"| the global load balancer's error fetch | the client's address | what the fetch carries: `Host`, `User-Agent`, `Referer` | none: a path condition never matches |",
		"| an explicit `POST /_dormeto/wake` | the client's address | the request's own | `/_dormeto/wake` |",
		"A visitor on a `.php` URL or on a path a no-wake rule keeps gets a page whose probe never wakes the workload",
		// the page
		"### The waiting page on Google Cloud", "dormeto gcp workloads waiting-page", "dormeto gcp routers waiting-page",
		"`custom pages are off on this router: an administrator turns them on with dormeto gcp install --custom-pages`",
		"anyone who can invoke the controller sets a page",
		"`--custom-pages=false`",
		"A custom page stored while they are off is shown as `off`",
		"`DMT_CUSTOM_PAGES=1`",
		// the capacity and the fallback machine types
		"### Capacity and fallback machine types", "30 s, 60 s, then every 120 s until the wake's deadline",
		"`fallback machine types are for VMs of unmanaged instance groups: a managed group's machine type is its template's`",
		"`compute.instances.setMachineType`", "`the router's role lacks compute.instances.setMachineType: run dormeto gcp install again`",
		"`InstanceTypeNotReverted`",
		// the internal kinds
		"### Internal load balancers", "`cross-region-internal`", "`loadbalancing.googleapis.com/https/internal/request_count`", "`internal_http_lb_rule`",
		"`INTERNAL_SELF_MANAGED`",
		// GKE's refusal
		"`k8s-um-`", "`mci-`", "`k8sResource`", "`kubernetes.io/ingress-name`", "a standalone network endpoint group",
		// the wake timeout's default
		"The router's default is 600 s for every kind of workload but one with a Cloud SQL companion",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the Google Cloud section lacks %q", want)
		}
	}
	for _, gone := range []string{"600 s for VMs, 300 s for pods", "A plain request (a `curl`, a webhook) gets the page and does not wake the workload", "and the branded waiting page does not apply to it"} {
		if strings.Contains(text, gone) {
			t.Errorf("the Google Cloud section still says %q", gone)
		}
	}
}
