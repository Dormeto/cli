// The standalone resources (vision standalone workloads design §7): each resource's default signals over the days
// read, a 5-minute point busy when a signal is at or over its threshold (fleet busyMinutes), the busy points replayed by
// the idle window advisor at --standalone-window (the windows of a standalone workload, 5 minutes to 24 hours, the knee
// from 5 to 120), what a nap of the resource stops priced for the hours asleep. A resource never counts twice: an HTTP
// workload's backend is not a standalone candidate (the scan leaves it out) and a database an HTTP workload reaches (its
// companion, attached or not) is left out here. A failed read prices nothing.
//
// When the resource ran is told by its signal's points: EC2, ECS and RDS publish them only while it runs (an instance
// started, a service with a task, a database available), as the measured uptime reads them. A point is then busy or
// idle; the time without points is a stop nobody would have to make, saving nothing (the M49 handover: the periods it
// ran are both the busy filter and the replay's running).
package estimate

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Dormeto/cli/internal/discover"
	"github.com/Dormeto/cli/internal/metrics"
	"github.com/Dormeto/cli/internal/replay"
	"github.com/Dormeto/cli/internal/stopped"
)

// StandaloneAssumption is what the estimate assumes of a standalone resource it would nap (design §7), printed once
// with the standalone totals.
const StandaloneAssumption = "A stopped standalone resource is counted as started again when its history next shows activity, by a schedule or a person."

// NoData is the reason a resource without a point of its signals in the days read is left out: it did not run.
const NoData = "no data: already stopped"

// ResourceRef names a standalone resource: its account and region, kind (instance, asg, ecs, rds-instance,
// rds-cluster), backend id, ARN and name; Managed when Dormeto already naps it.
type ResourceRef struct {
	Account string `json:"account"`
	Region  string `json:"region"`
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	ARN     string `json:"arn"`
	Name    string `json:"name"`
	Managed bool   `json:"managed,omitempty"`
}

// ResourceEstimate is a standalone resource estimated: the hours a day it ran (its signal's points), its cost a month
// while it ran, its figures at --standalone-window and at its recommended window.
type ResourceEstimate struct {
	Resource       ResourceRef `json:"resource"`
	RanHoursPerDay float64     `json:"ranHoursPerDay"`
	ActualMonth    float64     `json:"actualMonth"`
	AtWindow       AtWindow    `json:"atWindow"`
	Recommended    Recommended `json:"recommended"`
}

// ResourceLeftOut is a standalone resource not estimated, and why (the scan's reason for an ineligible one).
type ResourceLeftOut struct {
	Resource ResourceRef `json:"resource"`
	Reason   string      `json:"reason"`
}

// StandaloneUnread is what the standalone scan of a region could not read (a list the profile may not make: the
// databases without rds:Describe*), in its words.
type StandaloneUnread struct {
	Account string `json:"account"`
	Region  string `json:"region"`
	Reason  string `json:"reason"`
}

// SplitTotal is the figures of one kind of workloads (HTTP, standalone, both): how many, their actual cost, their
// savings at the report's windows (HTTP: --window, else each one's recommended; standalone: --standalone-window) and
// at their recommended ones.
type SplitTotal struct {
	Count               int     `json:"count"`
	ActualMonth         float64 `json:"actualMonth"`
	USDMonthWindow      float64 `json:"usdMonthWindow"`
	USDMonthRecommended float64 `json:"usdMonthRecommended"`
}

// SplitTotals are the totals for HTTP, standalone and both.
type SplitTotals struct {
	HTTP       SplitTotal `json:"http"`
	Standalone SplitTotal `json:"standalone"`
	Both       SplitTotal `json:"both"`
}

// StandaloneEstimate is the estimate's standalone part (--standalone on).
type StandaloneEstimate struct {
	// The idle window of the figures, --standalone-window.
	Window     int                `json:"window"`
	Totals     SplitTotals        `json:"totals"`
	Assumption string             `json:"assumption"`
	Resources  []ResourceEstimate `json:"resources"`
	LeftOut    []ResourceLeftOut  `json:"leftOut"`
	Unread     []StandaloneUnread `json:"unread"`
}

func refOf(r discover.StandaloneResource) ResourceRef {
	return ResourceRef{Account: r.Account, Region: r.Region, Kind: r.Kind, ID: r.ID, ARN: r.ARN, Name: r.Name, Managed: r.Managed}
}

// standalone estimates the standalone resources, once the HTTP workloads and their companions are.
func (s *state) standalone() error {
	o := s.in.Options
	part := &StandaloneEstimate{Window: o.Standalone.Window, Assumption: StandaloneAssumption, Resources: []ResourceEstimate{}, LeftOut: []ResourceLeftOut{}, Unread: []StandaloneUnread{}}
	// The databases counted as an HTTP workload's companions, with their workload.
	companions := map[string]discover.Workload{}
	for _, x := range s.workloads {
		for _, d := range x.Databases {
			companions[x.Workload.Account+"#"+x.Workload.Region+"#"+d.Kind+"#"+d.ID] = x.Workload
		}
	}
	reached := s.reachedDatabases()
	for _, region := range s.in.Standalone {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		for _, p := range region.Problems {
			part.Unread = append(part.Unread, StandaloneUnread{Account: region.Account, Region: region.Region, Reason: p})
		}
		for _, c := range region.Ineligible {
			part.LeftOut = append(part.LeftOut, ResourceLeftOut{Resource: ResourceRef{Account: region.Account, Region: region.Region, Kind: c.Kind, ID: c.ID, ARN: c.ARN, Name: c.Name}, Reason: c.Reason})
		}
		var read *metrics.RegionSignals
		if i := slices.IndexFunc(s.in.Signals, func(r metrics.RegionSignals) bool { return r.Account == region.Account && r.Region == region.Region }); i >= 0 {
			read = &s.in.Signals[i]
		}
		for _, r := range region.Resources {
			x, why, err := s.resource(r, read, companions, reached)
			if err != nil {
				return err
			}
			if why != "" {
				part.LeftOut = append(part.LeftOut, ResourceLeftOut{Resource: refOf(r), Reason: why})
				continue
			}
			part.Resources = append(part.Resources, *x)
		}
	}
	s.standalonePart = part
	return nil
}

// resource estimates one standalone resource, or says why it is left out.
func (s *state) resource(r discover.StandaloneResource, read *metrics.RegionSignals, companions map[string]discover.Workload, reached map[string][]discover.Workload) (*ResourceEstimate, string, error) {
	o := s.in.Options
	key := r.Account + "#" + r.Region + "#" + r.Kind + "#" + r.ID
	if w, ok := companions[key]; ok {
		return nil, "the companion database of the HTTP workload " + hostsOrTargetGroup(w) + ": counted with it", nil
	}
	if users := reached[key]; len(users) > 0 {
		names := make([]string, len(users))
		for i, w := range users {
			names[i] = hostsOrTargetGroup(w)
		}
		if len(users) == 1 {
			return nil, "reached by the HTTP workload " + names[0] + ": its companion, never a standalone resource", nil
		}
		return nil, "reached by the HTTP workloads " + strings.Join(names, ", ") + ": their companion, never a standalone resource", nil
	}
	if matched, excluded := matching(o.ExcludeTags, r.Tags); excluded {
		return nil, "excluded by its tags (" + DescribeTags(matched) + ")", nil
	}
	if i := slices.IndexFunc(r.Stopped, func(item stopped.Item) bool { return item.Kind == stopped.KindUnknown }); i >= 0 {
		reason := r.Stopped[i].Reason
		if reason == "" {
			reason = "unknown"
		}
		return nil, "it could not be described (" + reason + ")", nil
	}
	if len(r.Stopped) == 0 {
		return nil, nothing, nil
	}
	// As an HTTP service: one at 0 tasks stops nothing, unless its tasks are measured.
	if r.Kind == discover.KindECS && r.EcsDesired != nil && *r.EcsDesired == 0 && !o.EcsUptime {
		return nil, "an ECS service at 0 tasks: nothing running to stop (--ecs-uptime estimates it as one task from the hours it ran)", nil
	}
	if read == nil {
		return nil, "signals unreadable: not read", nil
	}
	if why, ok := read.Unreadable[r.ARN]; ok {
		return nil, "signals unreadable: " + why, nil
	}
	plan := metrics.StandalonePlan(r)
	values := metrics.SignalValues(plan.Signals, read.Of(r.ARN))
	from, err := parseISO(read.From)
	if err != nil {
		return nil, "", err
	}
	to, err := parseISO(read.To)
	if err != nil {
		return nil, "", err
	}
	ran := ranPeriods(values, from.UnixMilli())
	if len(ran) == 0 {
		return nil, NoData, nil
	}
	// An ECS service's tasks counted (--ecs-uptime): its price scaled to its average tasks while it ran, as an HTTP
	// service's is; else its count when discovered.
	scale := 1.0
	if tasks, counted := read.Tasks[r.ARN]; counted && o.EcsUptime && r.Kind == discover.KindECS {
		scale, _ = tasksScale(tasks, ranMinutes(ran, from.UnixMilli()), r.Stopped)
	}
	wake := float64(wakeMs)
	advice, err := replay.Advice(replay.Input{
		Ctx: s.ctx, Requests: replay.BusyMinutes(plan.Signals, values, read.From, read.To, ran), Current: o.Standalone.Window, CheckMinutes: 5,
		WakeMs: &wake, Stopped: r.Stopped, Hourly: s.priceOf(r.Region, scale), Baseline: s.in.Baseline, Now: s.in.Now, Running: ran,
		Windows: replay.StandaloneWindows, Recommendable: replay.StandaloneRecommendable,
	})
	if err != nil {
		return nil, "", err
	}
	if !advice.Priced {
		failure := ""
		if s.in.PriceFailure != nil {
			failure = s.in.PriceFailure()
		}
		if failure != "" {
			return nil, "prices unreadable (" + failure + ")", nil
		}
		return nil, "what it stops has no AWS list price", nil
	}
	at, err := rowOf(advice, o.Standalone.Window)
	if err != nil {
		return nil, "", err
	}
	best, err := rowOf(advice, advice.Recommended)
	if err != nil {
		return nil, "", err
	}
	if o.MinSaving != nil && usdOf(best) < *o.MinSaving {
		return nil, fmt.Sprintf("it would save less than --min-saving (%s) a month at its recommended window, %d min", JSNumber(*o.MinSaving), advice.Recommended), nil
	}
	actual, err := s.cost(r.Region, ran, r.Stopped, scale)
	if err != nil {
		return nil, "", err
	}
	days := float64(to.UnixMilli()-from.UnixMilli()) / dayMs
	hours := 0.0
	for _, p := range ran {
		a, _ := parseISO(p.From)
		b, _ := parseISO(p.To)
		hours += float64(b.UnixMilli()-a.UnixMilli()) / 3_600_000
	}
	return &ResourceEstimate{
		Resource: refOf(r), RanHoursPerDay: hours / days, ActualMonth: (actual * 30) / days,
		AtWindow:    AtWindow{USDMonth: usdOf(at), WakesPerDay: at.WakesPerDay, AsleepHours: at.AsleepHours},
		Recommended: Recommended{Minutes: advice.Recommended, USDMonth: usdOf(best), WakesPerDay: best.WakesPerDay},
	}, "", nil
}

// reachedDatabases are the databases each HTTP workload of their region reaches (a database whose security group lets
// a workload's in, as attach finds its companions), whatever the HTTP side did with the workload or the database: the
// workloads excluded by their tags included. Keyed account#region#kind#id.
func (s *state) reachedDatabases() map[string][]discover.Workload {
	out := map[string][]discover.Workload{}
	for _, region := range s.in.Regions {
		workloads := slices.Clone(region.Workloads)
		for _, x := range s.in.Excluded {
			if x.Workload.Account == region.Account && x.Workload.Region == region.Region {
				workloads = append(workloads, x.Workload)
			}
		}
		for _, d := range region.Databases {
			key := region.Account + "#" + region.Region + "#" + d.Kind + "#" + d.ID
			for _, w := range workloads {
				if slices.ContainsFunc(d.AllowedFrom, func(g string) bool { return slices.Contains(w.SecurityGroups, g) }) {
					out[key] = append(out[key], w)
				}
			}
		}
	}
	return out
}

// ranMinutes are the periods it ran in minutes from fromMs ([start, end)), as the measured uptime has them.
func ranMinutes(ran []replay.Period, fromMs int64) [][2]int {
	out := make([][2]int, 0, len(ran))
	for _, p := range ran {
		a, _ := parseISO(p.From)
		b, _ := parseISO(p.To)
		out = append(out, [2]int{int((a.UnixMilli() - fromMs) / 60_000), int((b.UnixMilli() - fromMs) / 60_000)})
	}
	return out
}

// ranPeriods are the periods a resource ran: its 5-minute points where every signal has a value, joined.
func ranPeriods(values []metrics.SignalValue, fromMs int64) []replay.Period {
	const point = 300_000
	count := 0
	for _, v := range values {
		count = max(count, len(v.Values))
	}
	var out []replay.Period
	start := -1
	flush := func(end int) {
		if start >= 0 {
			out = append(out, replay.Period{From: isoOfMs(fromMs + int64(start)*point), To: isoOfMs(fromMs + int64(end)*point)})
			start = -1
		}
	}
	for p := 0; p < count; p++ {
		present := len(values) > 0
		for _, v := range values {
			if p >= len(v.Values) || v.Values[p] == nil {
				present = false
			}
		}
		if present && start < 0 {
			start = p
		}
		if !present {
			flush(p)
		}
	}
	flush(count)
	return out
}

func hostsOrTargetGroup(w discover.Workload) string {
	if len(w.Hosts) > 0 {
		return w.Hosts[0]
	}
	return w.TargetGroup
}

func isoOfMs(ms int64) string { return isoOf(time.UnixMilli(ms)) }
