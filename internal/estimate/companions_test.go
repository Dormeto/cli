// Companion databases (the user, 2026-10-11): found by security group in discovery, attached after the wakes filter;
// the tags exclusion applies to them; left out when their workload wakes more than --max-wakes-rds a day. Ported from
// manager packages/estimate/test/estimate.test.ts (its describe block "companion databases").
package estimate

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/metrics"
	"github.com/Dormeto/cli/internal/stopped"
)

var rds = stopped.Item{Kind: "rds", Class: "db.t3.medium", Engine: "mysql", MultiAZ: false}

func withGroup(x discover.Workload, group string) discover.Workload {
	x.SecurityGroups = []string{group}
	return x
}

func database(id string, allowedFrom []string, change ...func(*discover.Database)) discover.Database {
	d := discover.Database{Kind: "rds-instance", ID: id, Status: "available", AllowedFrom: allowedFrom, Tags: map[string][]string{}, Stopped: []stopped.Item{rds}}
	for _, c := range change {
		c(&d)
	}
	return d
}

var (
	dbShop = withGroup(w(A, "eu-west-1", "shop", T3), "sg-shop")
	dbBlog = withGroup(w(A, "eu-west-1", "blog", T3), "sg-blog")
	dbCalm = withGroup(w(A, "eu-west-1", "calm", T3), "sg-calm")
)

func companionRegions() []discover.RegionDiscovery {
	return []discover.RegionDiscovery{{
		Account: A, Region: "eu-west-1", Workloads: []discover.Workload{dbShop, dbBlog, dbCalm}, Ineligible: []discover.IneligibleRule{},
		Databases: []discover.Database{
			database("shop-db", []string{"sg-shop"}), database("blog-db", []string{"sg-blog"}),
			database("prd-db", []string{"sg-shop"}, func(d *discover.Database) { d.Tags = map[string][]string{"env": {"PRD"}} }),
			database("calm-db", []string{"sg-calm"}), database("shared-db", []string{"sg-shop", "sg-calm"}),
			database("replica", []string{"sg-shop"}, func(d *discover.Database) {
				d.Stopped = []stopped.Item{}
				d.NotStoppable = "a read replica: cannot be stopped"
			}),
		},
	}}
}

// companions: shop about 1 wake a day; blog every 3 hours (busy); calm about 2.5 a day; databases with env=PRD
// excluded.
func companions(in *Input) {
	in.Regions = companionRegions()
	in.Requests = regionRequests(map[string][][2]float64{"arn:tg/shop": daily(), "arn:tg/blog": every(3), "arn:tg/calm": every(9.6)})
	in.Options.ExcludeTags = []discover.TagWants{{tag("env", "PRD")}}
}

func TestAttachesADatabaseToTheElectedWorkloadItsSecurityGroupLetsIn(t *testing.T) {
	e := run(t, companions)
	x := find(t, e.Workloads, "shop")
	if got := databaseRefs(x.Databases); !reflect.DeepEqual(got, [][2]string{{"rds-instance", "shop-db"}}) {
		t.Fatalf("databases %q", got)
	}
	alone := find(t, run(t, companions, func(in *Input) { in.Regions[0].Databases = []discover.Database{} }).Workloads, "shop")
	closeTo(t, "the database's saving", x.Databases[0].USDMonth, x.AtWindow.USDMonth-alone.AtWindow.USDMonth, 6)
	closeTo(t, "the database's recommended saving", x.Databases[0].RecommendedUSDMonth, x.Recommended.USDMonth-alone.Recommended.USDMonth, 6)
	closeTo(t, "actual", x.ActualMonth, (0.0456+0.068)*24*30, 6)
	if a := totalsOf(t, e.ByAccount, A); e.Totals.Databases != 1 || a.Databases != 1 {
		t.Fatalf("databases %d, A's %d", e.Totals.Databases, a.Databases)
	}
	if !strings.Contains(said(e.Assumptions), "Companion databases are found by security group") {
		t.Fatal("not said")
	}
	// blog is busy: left out, its database never considered.
	if len(e.Busy) != 1 || e.Busy[0].Workload.TargetGroup != "blog" {
		t.Fatalf("busy %+v", e.Busy)
	}
}

func TestLeavesOutADatabaseExcludedSharedNotStoppableOrOfAWorkloadWakingTooOften(t *testing.T) {
	e := run(t, companions)
	calm := find(t, e.Workloads, "calm")
	got := [][3]string{}
	codes := []LeftOutCode{}
	for _, d := range e.DatabasesLeftOut {
		got = append(got, [3]string{d.Database.ID, d.Workload.TargetGroup, d.Reason})
		codes = append(codes, d.Code)
	}
	want := [][3]string{
		{"prd-db", "shop", "excluded by its tags (env=PRD)"},
		{"calm-db", "calm", fmt.Sprintf("its workload wakes %s times a day at its recommended window, more than --max-wakes-rds 2 (a database takes minutes to start)", toFixed1(calm.Recommended.WakesPerDay))},
		{"shared-db", "shop", "reached by 2 workloads: stopping it with one would cut the others off"},
		{"replica", "shop", "a read replica: cannot be stopped"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("databases left out %q, want %q", got, want)
	}
	if len(calm.Databases) != 0 {
		t.Fatalf("calm's databases %+v", calm.Databases)
	}
	if !reflect.DeepEqual(codes, []LeftOutCode{CodeTags, CodeWakes, CodeShared, CodeStoppable}) {
		t.Fatalf("codes %q", codes)
	}
	kept := run(t, companions, func(in *Input) { in.Options.MaxWakesRds = nil })
	if got := databaseRefs(find(t, kept.Workloads, "calm").Databases); !reflect.DeepEqual(got, [][2]string{{"rds-instance", "calm-db"}}) {
		t.Fatalf("calm's databases %q", got)
	}
}

func databaseRefs(databases []Companion) [][2]string {
	out := [][2]string{}
	for _, d := range databases {
		out = append(out, [2]string{d.Kind, d.ID})
	}
	return out
}

// The user, 2026-10-11: a database stopped when discovered may be started by a schedule; as an EC2 instance, its
// CPUUtilization says when it ran, and its saving counts only those hours.
var dbNight = withGroup(w(A, "eu-west-1", "night", T3), "sg-night")

func nightWith(status string) func(*Input) {
	return func(in *Input) {
		in.Regions = []discover.RegionDiscovery{{Account: A, Region: "eu-west-1", Workloads: []discover.Workload{dbNight}, Ineligible: []discover.IneligibleRule{},
			Databases: []discover.Database{database("night-db", []string{"sg-night"}, func(d *discover.Database) { d.Status = status })}}}
		in.Requests = regionRequests(map[string][][2]float64{"arn:tg/night": daily()})
	}
}

// rdsMeasured: --rds-uptime, the database running so.
func rdsMeasured(running [][2]int) func(*Input) {
	return func(in *Input) {
		in.Options.RdsUptime = true
		in.Uptime = []metrics.RegionUptime{{Account: A, Region: "eu-west-1", From: FROM, To: TO, Running: map[string][][2]int{"rds-instance:night-db": running}, Unreadable: map[string]string{}}}
	}
}

func TestADatabasesSavingAndCostCountOnlyTheHoursItRan(t *testing.T) {
	allTime := nth(t, run(t, nightWith("available")).Workloads, 0)
	halfTime := nth(t, run(t, nightWith("available"), rdsMeasured(half())).Workloads, 0)
	if len(halfTime.Databases) != 1 || halfTime.Databases[0].ID != "night-db" || !reflect.DeepEqual(halfTime.Databases[0].Uptime, hours(12)) {
		t.Fatalf("databases %+v", halfTime.Databases)
	}
	if len(allTime.Databases) != 1 {
		t.Fatalf("databases %+v", allTime.Databases)
	}
	if !(halfTime.Databases[0].USDMonth < allTime.Databases[0].USDMonth) {
		t.Fatalf("measured saves %v, all the time %v", halfTime.Databases[0].USDMonth, allTime.Databases[0].USDMonth)
	}
	closeTo(t, "actual", halfTime.ActualMonth, 0.0456*24*30+0.068*12*30, 6)
	if allTime.Databases[0].Uptime != nil {
		t.Fatalf("uptime %+v", allTime.Databases[0].Uptime)
	}
}

func TestADatabaseStoppedWhenDiscoveredEstimatedWhenMeasuredElseLeftOut(t *testing.T) {
	unmeasured := run(t, nightWith("stopped"))
	if len(unmeasured.DatabasesLeftOut) != 1 || unmeasured.DatabasesLeftOut[0].Database.ID != "night-db" ||
		unmeasured.DatabasesLeftOut[0].Reason != "stopped when discovered (--rds-uptime estimates it from the hours it ran)" || unmeasured.DatabasesLeftOut[0].Code != CodeStopped {
		t.Fatalf("databases left out %+v", unmeasured.DatabasesLeftOut)
	}
	measured := run(t, nightWith("stopped"), rdsMeasured(half()))
	if d := nth(t, measured.Workloads, 0).Databases; len(d) != 1 || d[0].ID != "night-db" || !reflect.DeepEqual(d[0].Uptime, hours(12)) {
		t.Fatalf("databases %+v", d)
	}
	idle := run(t, nightWith("stopped"), rdsMeasured([][2]int{}))
	if len(idle.DatabasesLeftOut) != 1 || idle.DatabasesLeftOut[0].Reason != "it did not run in the last 14 days (its measured uptime)" || idle.DatabasesLeftOut[0].Code != CodeIdle {
		t.Fatalf("databases left out %+v", idle.DatabasesLeftOut)
	}
}
