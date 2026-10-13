// The estimate's reports, as manager packages/estimate/test/report.test.ts tests them (19 cases), the JSON's tool, the
// number helpers, and the Markdown of the shared estimate cases.
package report

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/accounts"
	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/estimate"
	"github.com/Dormeto/cli/internal/options"
	"github.com/Dormeto/cli/internal/prices"
)

const A = "111111111111"

func sp(s string) *string   { return &s }
func fp(f float64) *float64 { return &f }

func workloadOf(name string) discover.Workload {
	return discover.Workload{
		Account: A, Region: "eu-west-1", LoadBalancer: "web", LoadBalancerArn: "arn:lb", TargetGroup: name,
		TargetGroupArn: "arn:tg/" + name, Hosts: []string{name + ".example.com"},
		Backend: discover.WorkloadBackend{Kind: "asg", ID: name + "-asg"}, Stopped: nil, Tags: map[string][]string{},
	}
}

func workload(name string, usd15 float64, recommended int) estimate.WorkloadEstimate {
	return estimate.WorkloadEstimate{
		Workload: workloadOf(name), Quiet: name == "docs", ActualMonth: usd15 * 2,
		AtWindow:    estimate.AtWindow{USDMonth: usd15, WakesPerDay: 2, AsleepHours: 20},
		Recommended: estimate.Recommended{Minutes: recommended, USDMonth: usd15 - 1, WakesPerDay: 1},
	}
}

func ordered[V any](pairs ...any) estimate.Ordered[V] {
	var o estimate.Ordered[V]
	for i := 0; i < len(pairs); i += 2 {
		o.Set(pairs[i].(string), pairs[i+1].(V))
	}
	return o
}

func totalsOf(dormeto, with float64) estimate.Totals {
	return estimate.Totals{
		Workloads: 12, Kinds: estimate.Kinds{ASG: 9, EC2: 2, ECS: 1}, Excluded: 1, ActualMonth: 400, USDMonthWindow: 180.5,
		USDMonthRecommended: 168.5, DormetoMonth: dormeto, WithDormetoMonth: with,
	}
}

// E is the Node test's fixture.
func fixture() estimate.Estimate {
	var workloads []estimate.WorkloadEstimate
	for i := 0; i < 12; i++ {
		switch i {
		case 5:
			w := workload("napping", 15, 30)
			w.Workload.Managed = true
			workloads = append(workloads, w)
		case 3:
			workloads = append(workloads, workload("docs", float64(10+i), 30))
		default:
			workloads = append(workloads, workload(fmt.Sprintf("app%d", i), float64(10+i), 30))
		}
	}
	return estimate.Estimate{
		GeneratedAt: "2026-10-10T12:00:30.000Z", Organization: sp("o-abc123"),
		Options: options.Options{
			Regions: []string{"eu-west-1"}, ExcludeTags: []discover.TagWants{}, Role: "OrganizationAccountAccessRole",
			Baseline: prices.Baseline{Kind: "always"}, Discounts: prices.Discounts{Compute: 20, Databases: 0}, Window: 60,
			MaxWakes: fp(3), MinSaving: fp(1), Parallel: 5, MaxWakesRds: fp(2),
		},
		Accounts: 2, AccountsListed: 2,
		AccountNames: ordered[string](A, "prod", "333333333333", "sandbox"),
		Dormeto: estimate.DormetoCost{
			Manager: estimate.CostPart{Count: 1, UnitMonth: 5, USDMonth: 5},
			Routers: estimate.Routers{CostPart: estimate.CostPart{Count: 2, UnitMonth: 0.26, USDMonth: 0.52},
				Where: []estimate.Where{{Account: A, Region: "eu-west-1"}, {Account: A, Region: "eu-west-3"}}},
			Workloads: estimate.CostPart{Count: 12, UnitMonth: 0.13, USDMonth: 1.56}, USDMonth: 7.08,
		},
		Totals:    totalsOf(7.08, 226.58),
		ByAccount: ordered[estimate.Totals](A, totalsOf(1.82, 221.32)),
		ByRegion:  ordered[estimate.Totals]("eu-west-1", totalsOf(1.82, 221.32)),
		Workloads: workloads,
		LeftOut:   []estimate.LeftOut{{Workload: workloadOf("gpu"), Reason: "what it stops has no AWS list price"}},
		Excluded:  []estimate.Excluded{}, Busy: []estimate.Busy{}, Small: []estimate.Small{},
		DatabasesLeftOut: []estimate.DatabaseLeftOut{}, DatabasesUnread: []estimate.DatabasesUnread{},
		Ineligible: []estimate.IneligibleAt{{Account: A, Region: "eu-west-1", IneligibleRule: discover.IneligibleRule{
			LoadBalancer: "web", TargetGroup: sp("shared"), Hosts: []string{}, Reason: "its target group is shared"}}},
		Unreachable: []accounts.Unreachable{{Account: "333333333333", Name: "sandbox", Reason: "not authorized to perform: sts:AssumeRole",
			Fix: "create a role this profile may assume, with read access, and name it with --role (tried OrganizationAccountAccessRole)"}},
		FailedRegions: []estimate.FailedRegion{{Account: A, Region: "eu-west-3", Error: "opt-in region not enabled"}},
		UnreadableClusters: []estimate.UnreadableCluster{{Account: A, Region: "eu-west-1", Name: "eks-prod",
			Reason: "the cluster does not let the profile read it (an access entry with AmazonEKSViewPolicy): Unauthorized"}},
		Assumptions: []string{"Every request counts as activity and as a wake."},
	}
}

func nth(t *testing.T, lines []string, i int) string {
	t.Helper()
	if i >= len(lines) {
		t.Errorf("no line %d in %q", i, lines)
		return ""
	}
	return lines[i]
}

func contains(t *testing.T, text, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Errorf("missing %q in\n%s", want, text)
	}
}

func lacks(t *testing.T, text, unwanted string) {
	t.Helper()
	if strings.Contains(text, unwanted) {
		t.Errorf("unexpected %q in\n%s", unwanted, text)
	}
}

func TestMarkdownTargetTotalsWorkloadsLeftOutOptionsAssumptions(t *testing.T) {
	md := Markdown(fixture())
	for _, h := range []string{"# Dormeto savings estimate", "## Totals", "### By account", "### Dormeto's cost", "## Workloads", "## Left out",
		"## Not eligible", "## Accounts not reached", "## Regions that failed", "## Options", "## Assumptions"} {
		contains(t, md, h)
	}
	contains(t, md, "Organization o-abc123, 2 accounts")
	contains(t, md, "$180.50")
	contains(t, md, "$173.42")
	contains(t, md, "with a 1-hour idle window")
	lacks(t, md, "### By region")
	contains(t, md, "docs.example.com (no request in 14 days)")
	contains(t, md, "napping.example.com (managed by Dormeto)")
	contains(t, md, "what it stops has no AWS list price")
	contains(t, md, "create a role this profile may assume")
	contains(t, md, "compute 20%, databases 0%")
	contains(t, md, "- Accounts: every account reached")
	contains(t, md, "Every request counts")
}

func TestRestrictedToTheAccountsGiven(t *testing.T) {
	r := fixture()
	r.AccountsListed = 28
	r.Options.Accounts = []string{"111111111111", "222222222222"}
	md := Markdown(r)
	contains(t, md, "Organization o-abc123, 2 of its 28 accounts (--accounts), estimated")
	contains(t, md, "- Accounts: 111111111111, 222222222222 (--accounts)")
	contains(t, nth(t, Summary(r), 0), "for 12 workloads in 2 of the organization's 28 accounts;")
}

func TestTheEKSClustersTheProfileCannotRead(t *testing.T) {
	contains(t, Markdown(fixture()), strings.Join([]string{
		"## Clusters not read", "",
		"| Account | Region | Cluster | Why |", "|---|---|---|---|",
		"| 111111111111 | eu-west-1 | eks-prod | the cluster does not let the profile read it (an access entry with AmazonEKSViewPolicy): Unauthorized |",
	}, "\n"))
	e := fixture()
	e.UnreadableClusters = []estimate.UnreadableCluster{}
	lacks(t, Markdown(e), "## Clusters not read")
}

func TestTheAccountsARowEachAndTheTotal(t *testing.T) {
	contains(t, Markdown(fixture()), strings.Join([]string{
		"| Account | Name | Workloads | ASG | EC2 | ECS | EKS | RDS | Excluded | Actual monthly cost | Cost with Dormeto enabled | Savings |",
		"|---|---|---|---|---|---|---|---|---|---|---|---|",
		"| 111111111111 | prod | 12 | 9 | 2 | 1 | 0 | 0 | 1 | $400.00 | $221.32 | 45% |",
		"| **Total** | | **12** | **9** | **2** | **1** | **0** | **0** | **1** | **$400.00** | **$226.58** | **43%** |",
		"",
		"Cost with Dormeto enabled: at a 1-hour idle window, Dormeto's cost included; the total includes the manager ($5.00), once per installation.",
	}, "\n"))
}

func TestDormetosCostByPart(t *testing.T) {
	contains(t, Markdown(fixture()), strings.Join([]string{
		"| Part | Computation | A month |",
		"|---|---|---|",
		"| Manager | 1 × $5.00: one per installation, whatever the number of accounts and workloads | $5.00 |",
		"| Routers | 2 × $0.26: one per account and region with workloads (111111111111 eu-west-1, 111111111111 eu-west-3) | $0.52 |",
		"| Workloads | 12 × $0.13: each workload's naps, wakes and idle checks | $1.56 |",
		"| **Dormeto's cost** | | **$7.08** |",
		"| Actual cost | what naps would stop in the 12 workloads, running 24/7 | $400.00 |",
		"| Savings | at a 1-hour idle window | -$180.50 |",
		"| **Cost with Dormeto applied** | $400.00 - $180.50 + $7.08 | **$226.58** |",
	}, "\n"))
	e := fixture()
	e.Options.Baseline = prices.Baseline{Kind: "business-hours", TimeZone: "Europe/Paris"}
	contains(t, Markdown(e), "| Actual cost | what naps would stop in the 12 workloads, running in business hours (Europe/Paris) | $400.00 |")
}

func TestTheWorkloadsExcludedByTheirTags(t *testing.T) {
	e := fixture()
	e.Options.ExcludeTags = []discover.TagWants{
		{{Key: "env", Values: []string{"dev", "test"}}, {Key: "team", Values: []string{"web"}}},
		{{Key: "dormeto:ignore", Values: []string{}}},
	}
	e.Excluded = []estimate.Excluded{{Workload: workloadOf("legacy"), Criterion: "env=dev|test, team=web"}}
	md := Markdown(e)
	contains(t, md, strings.Join([]string{
		"## Excluded by tags", "",
		"| Account | Region | Load balancer | Target group | Hosts | Tags matched |", "|---|---|---|---|---|---|",
		`| 111111111111 | eu-west-1 | web | legacy | legacy.example.com | env=dev\|test, team=web |`,
	}, "\n"))
	contains(t, md, "- Excluded tags: env=dev|test, team=web; or dormeto:ignore")
	if !slices.Contains(Summary(e), "Excluded by tags: 1 workload (see the report)") {
		t.Errorf("summary lacks the excluded line: %q", Summary(e))
	}
	lacks(t, Markdown(fixture()), "## Excluded by tags")
	contains(t, Markdown(fixture()), "- Excluded tags: none")
	lacks(t, strings.Join(Summary(fixture()), "\n"), "Excluded by tags")
}

func TestEC2Uptime(t *testing.T) {
	e := fixture()
	vm := func(u estimate.Uptime) estimate.WorkloadEstimate {
		w := e.Workloads[0]
		w.Workload.Backend = discover.WorkloadBackend{Kind: "instance", ID: "i-0abc"}
		w.Uptime = &u
		return w
	}
	e.Options.Ec2Uptime = true
	e.Workloads = []estimate.WorkloadEstimate{vm(estimate.Uptime{HoursPerDay: fp(11.46)}), vm(estimate.Uptime{Unreadable: sp("CloudWatch could not read the uptime (Forbidden)")})}
	md := Markdown(e)
	contains(t, md, "| instance i-0abc | 48% |")
	contains(t, md, "| instance i-0abc | 100% assumed (uptime unreadable: CloudWatch could not read the uptime (Forbidden)) |")
	contains(t, md, "- EC2 uptime: measured over the 14 days (--ec2-uptime)")
	contains(t, Markdown(fixture()), "- EC2 uptime: 24/7 assumed (--ec2-uptime measures it)")
}

func TestBusyWorkloads(t *testing.T) {
	w := fixture().Workloads[0].Workload
	w.TargetGroup, w.Hosts, w.Backend = "busy", []string{"busy.example.com"}, discover.WorkloadBackend{Kind: "asg", ID: "busy-asg"}
	e := fixture()
	e.Busy = []estimate.Busy{{Workload: w, WakesPerDay: 5.93, USDMonth: 12.3, Minutes: 120}}
	md := Markdown(e)
	contains(t, md, "## More than 3 wakes a day")
	contains(t, md, "| 111111111111 | eu-west-1 | web | busy.example.com | asg busy-asg | 120 min | 5.9 | $12.30 |")
	contains(t, md, "- Wakes a day: at most 3 at each workload's recommended window, the busier left out (--max-wakes)")
	contains(t, strings.Join(Summary(e), "\n"), "More than 3 wakes a day: 1 workload left out (see the report)")
	none := fixture()
	none.Options.MaxWakes = nil
	contains(t, Markdown(none), "- Wakes a day: any (--max-wakes none)")
	lacks(t, Markdown(fixture()), "## More than")
}

func TestSmallWorkloads(t *testing.T) {
	w := fixture().Workloads[0].Workload
	w.TargetGroup, w.Hosts, w.Backend = "pico", []string{"pico.example.com"}, discover.WorkloadBackend{Kind: "asg", ID: "pico-asg"}
	e := fixture()
	e.Small = []estimate.Small{{Workload: w, USDMonth: 0.42, Minutes: 120}}
	md := Markdown(e)
	contains(t, md, "## Saving less than $1.00 a month")
	contains(t, md, "| 111111111111 | eu-west-1 | web | pico.example.com | asg pico-asg | 120 min | $0.42 |")
	contains(t, md, "- Minimum saving: $1.00 a month at each workload's recommended window, the smaller left out (--min-saving)")
	contains(t, strings.Join(Summary(e), "\n"), "Saving less than $1.00 a month: 1 workload left out (see the report)")
	none := fixture()
	none.Options.MinSaving = nil
	contains(t, Markdown(none), "- Minimum saving: none (--min-saving none)")
}

func TestCompanionDatabases(t *testing.T) {
	shop := fixture().Workloads[0]
	shop.Workload.TargetGroup, shop.Workload.Hosts = "shop", []string{"shop.example.com"}
	shop.Workload.Backend = discover.WorkloadBackend{Kind: "asg", ID: "shop-asg"}
	shop.Databases = []estimate.Companion{{Kind: "rds-instance", ID: "shop-db", USDMonth: 30.5, RecommendedUSDMonth: 31, Uptime: &estimate.Uptime{HoursPerDay: fp(12)}}}
	e := fixture()
	e.Workloads = []estimate.WorkloadEstimate{shop}
	e.DatabasesLeftOut = []estimate.DatabaseLeftOut{
		{Workload: shop.Workload, Database: estimate.DatabaseRef{Kind: "rds-instance", ID: "prd-db"}, Reason: "excluded by its tags (env=PRD)", Code: estimate.CodeTags},
		{Workload: shop.Workload, Database: estimate.DatabaseRef{Kind: "rds-instance", ID: "common-db"}, Reason: "reached by 3 workloads: stopping it with one would cut the others off", Code: estimate.CodeShared},
	}
	e.DatabasesUnread = []estimate.DatabasesUnread{{Account: A, Region: "eu-west-3", Reason: "AccessDenied: not authorized to perform: rds:DescribeDBInstances"}}
	md := Markdown(e)
	contains(t, md, "| asg shop-asg + rds-instance shop-db |")
	contains(t, md, "## Companion databases")
	contains(t, md, "| Account | Region | Workload | Database | Uptime | Saving |")
	contains(t, md, "| 111111111111 | eu-west-1 | shop.example.com | rds-instance shop-db | 50% | $31.00 |")
	contains(t, md, "| 111111111111 | eu-west-1 | shop.example.com | rds-instance prd-db | excluded by its tags (env=PRD) |")
	contains(t, md, "- 111111111111 eu-west-3: not read (AccessDenied: not authorized to perform: rds:DescribeDBInstances)")
	contains(t, md, "- Companion databases: found by security group, left out when their workload wakes more than 2 times a day at its recommended window (--max-wakes-rds)")
	contains(t, strings.Join(Summary(e), "\n"), "Companion databases: 1 attached, 2 left out: 1 shared with other workloads, 1 excluded by tags (see the report)")
	lacks(t, Markdown(fixture()), "## Companion databases")
	none := fixture()
	none.Options.MaxWakesRds = nil
	contains(t, Markdown(none), "- Companion databases: found by security group, whatever their workload's wakes (--max-wakes-rds none)")
}

func TestTheWorkloadsTable(t *testing.T) {
	md := Markdown(fixture())
	contains(t, md, "| Account | Region | Load balancer | Hosts | Backend | Uptime | Recommended | Saving | Wakes a day |")
	contains(t, md, "| 111111111111 | eu-west-1 | web | app11.example.com | asg app11-asg | 100% assumed | 30 min | $20.00 | 1 |")
	lacks(t, md, "At 1 hour")
}

func TestECSUptime(t *testing.T) {
	service := fixture().Workloads[0]
	service.Workload.Backend = discover.WorkloadBackend{Kind: "ecs", ID: "arn:aws:ecs:eu-west-1:111111111111:service/prod/api"}
	service.Uptime = &estimate.Uptime{HoursPerDay: fp(9.5)}
	e := fixture()
	e.Options.EcsUptime = true
	e.Workloads = []estimate.WorkloadEstimate{service}
	md := Markdown(e)
	contains(t, md, "| ecs api | 40% |")
	contains(t, md, "- ECS uptime: measured over the 14 days (--ecs-uptime)")
	r := fixture()
	r.Options.RdsUptime = true
	contains(t, Markdown(r), "- RDS uptime: companion databases measured over the 14 days (--rds-uptime)")
	contains(t, Markdown(fixture()), "- RDS uptime: companion databases available all the time assumed, one stopped left out (--rds-uptime measures it)")
	contains(t, Markdown(fixture()), "- ECS uptime: at the desired count when discovered, all the time (--ecs-uptime measures it)")
}

func TestManyRoutersTheirCount(t *testing.T) {
	e := fixture()
	var where []estimate.Where
	for i := 0; i < 7; i++ {
		where = append(where, estimate.Where{Account: fmt.Sprintf("11111111111%d", i), Region: "eu-west-1"})
	}
	e.Dormeto.Routers = estimate.Routers{CostPart: estimate.CostPart{Count: 7, UnitMonth: 0.26, USDMonth: 1.82}, Where: where}
	contains(t, Markdown(e), "| Routers | 7 × $0.26: one per account and region with workloads | $1.82 |")
}

var visual = []string{
	"Monthly cost of the 12 workloads estimated, at a 1-hour idle window:",
	"  Actual cost   " + strings.Repeat("█", 40) + "  $400.00  100%",
	"  With Dormeto  " + strings.Repeat("█", 22) + "▋" + strings.Repeat(" ", 17) + "  $226.58   57%",
	"  Saved: $173.42 a month, 43% of the actual cost",
}

func TestTheVisualInTheReportAndInTheTerminal(t *testing.T) {
	contains(t, Markdown(fixture()), strings.Join(append(append([]string{"```text"}, visual...), "```"), "\n"))
	lines := Summary(fixture())
	if len(lines) < 2+len(visual) {
		t.Fatalf("summary too short: %q", lines)
	}
	if got, want := lines[1:2+len(visual)], append([]string{""}, visual...); !slices.Equal(got, want) {
		t.Errorf("visual\n got %q\nwant %q", got, want)
	}
}

func TestTheVisualWhenDormetoWouldCostMoreThanItSaves(t *testing.T) {
	e := fixture()
	e.Totals.ActualMonth, e.Totals.USDMonthWindow, e.Totals.WithDormetoMonth = 10, 4, 13.09
	lines := Summary(e)
	for _, want := range []string{
		"  Actual cost   " + strings.Repeat("█", 30) + "▌" + strings.Repeat(" ", 9) + "  $10.00  100%",
		"  With Dormeto  " + strings.Repeat("█", 40) + "  $13.09  131%",
		"  Dormeto would cost $3.09 a month more than it saves, 31% of the actual cost",
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("summary lacks %q in %q", want, lines)
		}
	}
}

func TestTheJSONParsesBackToTheEstimate(t *testing.T) {
	e := fixture()
	var back estimate.Estimate
	if err := json.Unmarshal(JSON(e, "1.2.3"), &back); err != nil {
		t.Fatal(err)
	}
	want, _ := json.Marshal(e)
	got, _ := json.Marshal(back)
	if string(want) != string(got) || len(want) == 0 {
		t.Errorf("parsed back\n got %s\nwant %s", got, want)
	}
}

func TestTheJSONToolAndLayout(t *testing.T) {
	out := string(JSON(fixture(), "1.2.3"))
	if !strings.HasPrefix(out, "{\n  \"tool\": {\n    \"name\": \"dormeto\",\n    \"version\": \"1.2.3\"\n  },\n  \"generatedAt\": \"2026-10-10T12:00:30.000Z\",\n  \"organization\": \"o-abc123\",\n  \"why\": null,\n") {
		t.Errorf("start of the JSON:\n%.300s", out)
	}
	if !strings.HasSuffix(out, "\n  ]\n}\n") {
		t.Errorf("end of the JSON: %q", out[max(0, len(out)-30):])
	}
	// Two-space indent, empty arrays and objects inline, as JSON.stringify(e, null, 2).
	contains(t, out, "\n  \"excluded\": [],\n")
	// Not HTML-escaped, as JSON.stringify.
	e := fixture()
	e.Why = sp("a<b & c>d" + string(rune(0x2028)) + string(rune(8)))
	got := string(JSON(e, "v"))
	// Written as JSON.stringify does: the line separator as it is, the backspace as \b.
	contains(t, got, `"why": "a<b & c>d`+string(rune(0x2028))+`\b"`)
}

func TestTheTerminalsSummaryTables(t *testing.T) {
	lines := Summary(fixture())
	table := func(title string) []string {
		from := slices.Index(lines, title)
		if from <= 0 {
			t.Fatalf("no %q", title)
		}
		var rows []string
		for i := from + 1; i < len(lines) && regexp.MustCompile(`^ {2}[┌│├└]`).MatchString(lines[i]); i++ {
			rows = append(rows, lines[i])
		}
		return rows
	}
	cells := func(rows []string) [][]string {
		var out [][]string
		for _, l := range rows {
			if !strings.HasPrefix(l, "  │") {
				continue
			}
			parts := strings.Split(l, "│")
			var row []string
			for _, c := range parts[1 : len(parts)-1] {
				row = append(row, strings.TrimSpace(c))
			}
			out = append(out, row)
		}
		return out
	}
	joints := func(l string) []int {
		var at []int
		for i, c := range []rune(l) {
			if strings.ContainsRune("┬┼┴│┐┤┘", c) && i > 2 {
				at = append(at, i)
			}
		}
		return at
	}
	aligned := func(rows []string) {
		widths := map[int]bool{}
		for _, l := range rows {
			widths[len([]rune(l))] = true
			if !slices.Equal(joints(l), joints(rows[1])) {
				t.Errorf("joints of %q differ from %q", l, rows[1])
			}
		}
		if len(widths) != 1 {
			t.Errorf("lines of different widths: %v", widths)
		}
	}
	sides := func(rows []string) []string {
		var out []string
		for _, l := range rows {
			out = append(out, string([]rune(l)[2]))
		}
		return out
	}
	accountsRows := table("By account:")
	if len(accountsRows) != 7 {
		t.Fatalf("account table %q", accountsRows)
	}
	if got := sides(accountsRows); !slices.Equal(got, strings.Split("┌│├│├│└", "")) {
		t.Errorf("account borders %v", got)
	}
	if !regexp.MustCompile(`^ {2}┌─+┬[─┬]+┐$`).MatchString(accountsRows[0]) || !regexp.MustCompile(`^ {2}├─+┼[─┼]+┤$`).MatchString(accountsRows[2]) ||
		!regexp.MustCompile(`^ {2}└─+┴[─┴]+┘$`).MatchString(accountsRows[6]) {
		t.Errorf("rules %q", accountsRows)
	}
	wantAccounts := [][]string{
		{"Account", "Name", "Workloads", "ASG", "EC2", "ECS", "EKS", "RDS", "Excluded", "Actual monthly cost", "Cost with Dormeto enabled", "Savings"},
		{"111111111111", "prod", "12", "9", "2", "1", "0", "0", "1", "$400.00", "$221.32", "45%"},
		{"Total", "", "12", "9", "2", "1", "0", "0", "1", "$400.00", "$226.58", "43%"},
	}
	if got := cells(accountsRows); fmt.Sprint(got) != fmt.Sprint(wantAccounts) {
		t.Errorf("account cells\n got %q\nwant %q", got, wantAccounts)
	}
	aligned(accountsRows)
	// The figures are right-aligned under their headings (in characters before the border).
	r := func(s string) []rune { return []rune(s) }
	right := func(row, border string) int { return len(r(row[:strings.Index(row, border)])) }
	if a, b := right(accountsRows[3], "$221.32 │")+len("$221.32"), right(accountsRows[1], "Cost with Dormeto enabled │")+len("Cost with Dormeto enabled"); a != b {
		t.Errorf("figures not right-aligned: %d vs %d", a, b)
	}
	i := slices.Index(lines, accountsRows[6])
	if lines[i+1] != "  Cost with Dormeto enabled: at a 1-hour idle window, Dormeto's cost included; the total includes the manager ($5.00), once per installation." {
		t.Errorf("note %q", lines[i+1])
	}
	costRows := table("Dormeto's cost:")
	if len(costRows) != 14 {
		t.Fatalf("cost table %q", costRows)
	}
	if got := sides(costRows); !slices.Equal(got, strings.Split("┌│├│││├│├││├│└", "")) {
		t.Errorf("cost borders %v", got)
	}
	wantCost := [][]string{
		{"Part", "Computation", "A month"},
		{"Manager", "1 × $5.00: one per installation, whatever the number of accounts and workloads", "$5.00"},
		{"Routers", "2 × $0.26: one per account and region with workloads (111111111111 eu-west-1, 111111111111 eu-west-3)", "$0.52"},
		{"Workloads", "12 × $0.13: each workload's naps, wakes and idle checks", "$1.56"},
		{"Dormeto's cost", "", "$7.08"},
		{"Actual cost", "what naps would stop in the 12 workloads, running 24/7", "$400.00"},
		{"Savings", "at a 1-hour idle window", "-$180.50"},
		{"Cost with Dormeto applied", "$400.00 - $180.50 + $7.08", "$226.58"},
	}
	if got := cells(costRows); fmt.Sprint(got) != fmt.Sprint(wantCost) {
		t.Errorf("cost cells\n got %q\nwant %q", got, wantCost)
	}
	aligned(costRows)
}

func TestTheTerminalsAccountsTheTenSavingTheMost(t *testing.T) {
	var by estimate.Ordered[estimate.Totals]
	for i := 0; i < 12; i++ {
		by.Set(fmt.Sprintf("1000000000%02d", i), estimate.Totals{Workloads: 1, Kinds: estimate.Kinds{ASG: 1}, ActualMonth: float64(2 * i),
			USDMonthWindow: float64(i), USDMonthRecommended: float64(i), DormetoMonth: 0.39, WithDormetoMonth: float64(i) + 0.39})
	}
	e := fixture()
	e.ByAccount = by
	lines := Summary(e)
	from := slices.Index(lines, "By account:")
	if from < 0 {
		t.Fatalf("no accounts table in %q", lines)
	}
	if !strings.HasPrefix(nth(t, lines, from+4), "  │ 100000000011 ") {
		t.Errorf("first row %q", nth(t, lines, from+4))
	}
	for _, l := range lines[from:] {
		if strings.Contains(l, "100000000001") {
			t.Errorf("account 1 shown: %q", l)
		}
	}
	if !slices.Contains(lines, "  And 2 more accounts: see the report.") {
		t.Errorf("no count of the others: %q", lines)
	}
}

func TestTheTerminalsSummaryTheTotalsTheTenWorkloadsWhatWasLeftOut(t *testing.T) {
	lines := Summary(fixture())
	if nth(t, lines, 0) != "About $180.50 a month at a 1-hour idle window ($168.50 at the recommended windows) for 12 workloads in 2 accounts; Dormeto costs about $7.08 a month: net about $173.42." {
		t.Errorf("first line %q", nth(t, lines, 0))
	}
	var top []string
	for _, l := range lines {
		if strings.Contains(l, ".example.com") {
			top = append(top, l)
		}
	}
	if len(top) != 10 {
		t.Fatalf("top %d: %q", len(top), top)
	}
	contains(t, nth(t, top, 0), "app11.example.com")
	joined := strings.Join(lines, "\n")
	contains(t, joined, "Left out: 1 workload")
	contains(t, joined, "Not reached: 1 account")
	contains(t, joined, "Regions that failed: 111111111111/eu-west-3")
	// A line of the top: the figure right-aligned on ten characters.
	contains(t, joined, "\n      $21.00  app11.example.com (web, eu-west-1, 111111111111); recommended 30 min: $20.00")
}

func TestUSDAsIntlWritesIt(t *testing.T) {
	for _, c := range []struct {
		v    float64
		want string
	}{
		{0, "$0.00"}, {180.5, "$180.50"}, {1234567.891, "$1,234,567.89"}, {-180.5, "-$180.50"}, {1.005, "$1.01"}, {0.125, "$0.13"},
		{0.004, "$0.00"}, {0.005, "$0.01"}, {-0.001, "-$0.00"}, {999.995, "$1,000.00"}, {1e21, "$1,000,000,000,000,000,000,000.00"}, {0.1 + 0.2, "$0.30"},
	} {
		if got := usd(c.v); got != c.want {
			t.Errorf("usd(%v) = %q, want %q", c.v, got, c.want)
		}
	}
	if got := usd(negZero()); got != "-$0.00" {
		t.Errorf("usd(-0) = %q", got)
	}
}

func negZero() float64 { z := 0.0; return -z * 1 }

func TestJSRoundAndLen16(t *testing.T) {
	for in, want := range map[float64]float64{0.5: 1, 1.5: 2, 2.5: 3, -0.5: 0, -1.5: -1, -2.5: -2, 0.49999999999999994: 0, 43.2: 43} {
		if got := jsRound(in); got != want {
			t.Errorf("jsRound(%v) = %v, want %v", in, got, want)
		}
	}
	if got := len16("a×█😀"); got != 5 {
		t.Errorf("len16 = %d, want 5 (the emoji is two UTF-16 units)", got)
	}
}

func TestTheTableCountsUTF16UnitsAsNodesLength(t *testing.T) {
	e := fixture()
	e.AccountNames = ordered[string](A, "prod😀")
	lines := Summary(e)
	from := slices.Index(lines, "By account:")
	if from < 0 {
		t.Fatalf("no accounts table in %q", lines)
	}
	// Node pads by String.length: the emoji is two units, so its row is one Go character shorter than the others.
	head, row := nth(t, lines, from+2), nth(t, lines, from+4)
	if len([]rune(head))-len([]rune(row)) != 1 {
		t.Errorf("head %d runes, row %d runes", len([]rune(head)), len([]rune(row)))
	}
}

// The Markdown of the shared estimate cases, from the JSON report the Node estimate wrote.
func TestTheSharedEstimateCasesMarkdown(t *testing.T) {
	sharedMarkdown(t)
}

func TestTheJSONWritesNonFiniteNumbersAsNull(t *testing.T) {
	e := fixture()
	e.Totals.ActualMonth = math.NaN()
	e.Workloads[0].AtWindow.USDMonth = math.Inf(1)
	e.Workloads[1].Databases = []estimate.Companion{{Kind: "rds-instance", ID: "db", USDMonth: math.Inf(-1), RecommendedUSDMonth: 3, Uptime: &estimate.Uptime{HoursPerDay: fp(math.NaN())}}}
	e.ByAccount = ordered[estimate.Totals](A, e.Totals)
	e.Options.MaxWakes = fp(math.NaN())
	out := JSON(e, "1.2.3")
	if len(out) == 0 {
		t.Fatal("no report")
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if got := back["totals"].(map[string]any)["actualMonth"]; got != nil {
		t.Errorf("totals.actualMonth %v, want null", got)
	}
	if got, ok := back["byAccount"].(map[string]any)[A].(map[string]any)["actualMonth"]; !ok || got != nil {
		t.Errorf("byAccount actualMonth %v (present %v), want null", got, ok)
	}
	if got := back["options"].(map[string]any)["maxWakes"]; got != nil {
		t.Errorf("maxWakes %v", got)
	}
	w0 := back["workloads"].([]any)[0].(map[string]any)
	if got, ok := w0["atWindow"].(map[string]any)["usdMonth"]; !ok || got != nil {
		t.Errorf("atWindow.usdMonth %v, want null", got)
	}
	db := back["workloads"].([]any)[1].(map[string]any)["databases"].([]any)[0].(map[string]any)
	if db["usdMonth"] != nil || db["recommendedUsdMonth"] != 3.0 || db["uptime"].(map[string]any)["hoursPerDay"] != nil {
		t.Errorf("companion %v", db)
	}
	// The rest is intact: the tool first, the same names, the finite numbers, the keys in order.
	if !strings.HasPrefix(string(out), "{\n  \"tool\": {") || back["totals"].(map[string]any)["usdMonthWindow"] != 180.5 || len(back["workloads"].([]any)) != 12 {
		t.Errorf("report damaged:\n%.400s", out)
	}
	if i, j := strings.Index(string(out), `"accountNames"`), strings.Index(string(out), `"totals"`); i < 0 || j < i {
		t.Errorf("key order")
	}
}

// The fallback writes what the encoder writes: the same bytes for an estimate with only finite numbers.
func TestTheNullWritingTreeEqualsTheEncoderOnFiniteNumbers(t *testing.T) {
	e := fixture()
	e.Workloads[1].Databases = []estimate.Companion{{Kind: "rds-instance", ID: "db", USDMonth: 1, RecommendedUSDMonth: 3, Uptime: &estimate.Uptime{HoursPerDay: fp(2)}}}
	e.Excluded = []estimate.Excluded{{Workload: workloadOf("legacy"), Criterion: "x"}}
	e.Options.ExcludeTags = []discover.TagWants{{{Key: "env", Values: []string{"dev"}}}}
	report := withTool{Tool: tool{Name: "dormeto", Version: "1"}, Estimate: e}
	want, err := encode(report)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := treeOf(reflect.ValueOf(report))
	if err != nil {
		t.Fatal(err)
	}
	got, err := encode(tree)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("tree differs from the encoder's:\n%s\n---\n%s", got, want)
	}
}

func TestANaNTotalGivesNodesOutputAndNoPanic(t *testing.T) {
	// No actual cost to compare with (Node's !t.actualMonth): no visual, neither in the summary nor in the Markdown.
	e := fixture()
	e.Totals.ActualMonth = math.NaN()
	lines := Summary(e)
	if nth(t, lines, 1) == "" && strings.HasPrefix(nth(t, lines, 2), "Monthly cost") {
		t.Errorf("a visual: %q", lines[:6])
	}
	lacks(t, strings.Join(lines, "\n"), "Monthly cost of the")
	lacks(t, Markdown(e), "```text")
	contains(t, Markdown(e), "$NaN")
	// A NaN cost with Dormeto: the bars are empty ('█'.repeat(NaN) is ''), the figures read NaN.
	e = fixture()
	e.Totals.WithDormetoMonth = math.NaN()
	lines = Summary(e)
	for _, want := range []string{
		"  Actual cost   " + strings.Repeat(" ", 40) + "  $400.00  100%",
		"  With Dormeto  " + strings.Repeat(" ", 40) + "     $NaN  NaN%",
		"  Dormeto would cost $NaN a month more than it saves, NaN% of the actual cost",
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("summary lacks %q", want)
		}
	}
	// Infinite totals do not panic either.
	e = fixture()
	e.Totals.ActualMonth = math.Inf(1)
	_ = Summary(e)
	_ = Markdown(e)
	e.Totals.ActualMonth, e.Totals.WithDormetoMonth = 10, math.Inf(1)
	_ = Summary(e)
}
