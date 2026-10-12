package replay

// Ports manager packages/fleet/test/idle.test.ts (the replay, the recommended window, the advice).

import (
	"context"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/Dormeto/cli/internal/prices"
	"github.com/Dormeto/cli/internal/stopped"
)

const (
	from = "2026-09-26T00:00:00.000Z"
	days = 14
	end  = days * 1440
)

var fromT = time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC)

func at(minute int) string {
	return fromT.Add(time.Duration(minute) * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
}

var to = at(end)

func requests(minutes ...[2]int) RequestMinutes {
	return RequestMinutes{From: from, To: to, Minutes: minutes}
}

func dailyRequests() RequestMinutes {
	var m [][2]int
	for d := 0; d < days; d++ {
		m = append(m, [2]int{600 + d*1440, 1})
	}
	return requests(m...)
}

type opts struct {
	wakeSeconds float64
	awake       bool // Node's helper defaults to startAsleep: true
	check       int
}

func replay(t *testing.T, minutes [][2]int, window int, o opts) ReplayedWindow {
	t.Helper()
	if o.check == 0 {
		o.check = 5
	}
	r, err := Replay(ReplayInput{Requests: requests(minutes...), Windows: []int{window}, CheckMinutes: o.check, WakeSeconds: o.wakeSeconds, StartAsleep: !o.awake})
	if err != nil || len(r) != 1 {
		t.Fatalf("replay: %v %v", r, err)
	}
	return r[0]
}

func eq(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func p(from, to int) Period { return Period{at(from), at(to)} }

func TestOneRequestADay(t *testing.T) {
	r, err := Replay(ReplayInput{Requests: dailyRequests(), Windows: []int{15}, CheckMinutes: 5, WakeSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	w := r[0]
	eq(t, w.Minutes, 15)
	eq(t, w.Wakes, days)
	eq(t, w.Asleep[0], p(15, 600))
	eq(t, w.Asleep[1], p(620, 600+1440))
	eq(t, w.Asleep[len(w.Asleep)-1], Period{at(600 + 13*1440 + 20), to})
}

func TestARequestEvery10MinutesForAnHour(t *testing.T) {
	var burst [][2]int
	for _, m := range []int{600, 610, 620, 630, 640, 650} {
		burst = append(burst, [2]int{m, 3})
	}
	eq(t, replay(t, burst, 15, opts{}).Wakes, 1)
	eq(t, replay(t, burst, 15, opts{}).Asleep, []Period{p(0, 600), {at(670), to}})
	eq(t, replay(t, burst, 5, opts{}).Wakes, 6)
	eq(t, replay(t, burst, 5, opts{}).Asleep, []Period{p(0, 600), {at(660), to}})
}

func TestASilenceJustUnderTheWindow(t *testing.T) {
	eq(t, replay(t, [][2]int{{600, 1}, {614, 1}, {630, 1}}, 15, opts{}).Wakes, 2)
}

func TestNapsOnlyAtACheck(t *testing.T) {
	eq(t, replay(t, [][2]int{{600, 1}}, 7, opts{}).Asleep[1].From, at(610))
}

func TestRequestsDuringAWakeAreActivity(t *testing.T) {
	r := replay(t, [][2]int{{600, 1}, {601, 2}, {602, 1}}, 15, opts{wakeSeconds: 180})
	eq(t, r.Wakes, 1)
	eq(t, r.Asleep[1].From, at(620))
}

func TestStaysAwakeAFullWindowAfterAWake(t *testing.T) {
	eq(t, replay(t, [][2]int{{600, 1}}, 15, opts{wakeSeconds: 600}).Asleep[1].From, at(625))
}

func TestAsleepFromTheStartWithoutAnyRequest(t *testing.T) {
	eq(t, replay(t, nil, 15, opts{}), ReplayedWindow{Minutes: 15, Wakes: 0, WokeAt: []string{}, Asleep: []Period{{from, to}}})
}

func TestARequestStartingAtTheCheckThatNaps(t *testing.T) {
	r := replay(t, [][2]int{{15, 1}}, 15, opts{awake: true})
	eq(t, r.Wakes, 1)
	eq(t, r.Asleep, []Period{{at(35), to}})
}

func TestAWindowOfADayOverARequestEvery6Hours(t *testing.T) {
	var m [][2]int
	for k := 0; k < end/360; k++ {
		m = append(m, [2]int{k * 360, 1})
	}
	eq(t, replay(t, m, 1440, opts{awake: true}), ReplayedWindow{Minutes: 1440, Wakes: 0, WokeAt: []string{}, Asleep: []Period{}})
}

func TestARequestInTheLastMinuteWakesIt(t *testing.T) {
	eq(t, replay(t, [][2]int{{end - 1, 1}}, 15, opts{}),
		ReplayedWindow{Minutes: 15, Wakes: 1, WokeAt: []string{at(end - 1)}, Asleep: []Period{{from, at(end - 1)}}})
}

func TestMinutesOutOfOrderOrRepeatedAreAddedUp(t *testing.T) {
	eq(t, replay(t, [][2]int{{650, 1}, {600, 2}, {600, 3}, {610, 1}}, 15, opts{}),
		replay(t, [][2]int{{600, 5}, {610, 1}, {650, 1}}, 15, opts{}))
}

func rows(wakes ...float64) []WakeRow {
	var out []WakeRow
	for i, m := range IdleWindows {
		out = append(out, WakeRow{m, wakes[i]})
	}
	return out
}

func TestRecommendKnee(t *testing.T) {
	eq(t, Recommend(rows(40, 30, 20, 12, 11, 10, 10, 9, 9)), 20)
}

func TestRecommendSameWakesOrNone(t *testing.T) {
	eq(t, Recommend(rows(7, 7, 7, 7, 7, 7, 7, 7, 7)), 5)
	eq(t, Recommend(rows(0, 0, 0, 0, 0, 0, 0, 0, 0)), 5)
}

func TestRecommendWakesRisingAgain(t *testing.T) {
	eq(t, Recommend(rows(40, 30, 12, 20, 11, 10, 10, 9, 9)), 15)
}

func TestRecommendOnlyTheNineAreCandidates(t *testing.T) {
	r := append(rows(40, 30, 20, 12, 11, 10, 10, 9, 9), WakeRow{25, 11}, WakeRow{1440, 0})
	eq(t, Recommend(r), 20)
	eq(t, Recommend(nil), 5)
}

func TestWindowsTried(t *testing.T) {
	eq(t, Windows(15), IdleWindows)
	eq(t, Windows(25), []int{5, 10, 15, 20, 25, 30, 45, 60, 90, 120})
	eq(t, Windows(1440), append(append([]int{}, IdleWindows...), 1440))
}

func TestCheckMinutes(t *testing.T) {
	s := func(v string) *string { return &v }
	eq(t, []int{CheckMinutes(s("rate(5 minutes)")), CheckMinutes(s("rate(1 minute)")), CheckMinutes(s("rate(1 hour)")), CheckMinutes(s("rate(2 hours)"))}, []int{5, 1, 60, 120})
	eq(t, []int{CheckMinutes(s("cron(0/5 * * * ? *)")), CheckMinutes(nil)}, []int{5, 5})
}

// The advice.

var (
	t3    = stopped.Item{Kind: stopped.KindEC2, Type: "t3.medium", Count: 1, OS: "Linux"}
	odd   = stopped.Item{Kind: stopped.KindUnknown, Backend: "something", Reason: "x"}
	paris = prices.Baseline{Kind: "business-hours", TimeZone: "Europe/Paris"}
)

func hourly(_ context.Context, item stopped.Item) (*float64, error) {
	if item.Kind == stopped.KindEC2 {
		v := 0.0456
		return &v, nil
	}
	return nil, nil
}

func f(v float64) *float64 { return &v }

func adviceWith(t *testing.T, over func(*Input)) Result {
	t.Helper()
	nowT, _ := time.Parse(time.RFC3339, to)
	in := Input{Requests: dailyRequests(), Current: 15, CheckMinutes: 5, WakeMs: f(30_000), ActualWakes: 14, ActualFrom: ptr(from),
		Stopped: []stopped.Item{t3}, Hourly: hourly, Baseline: prices.Baseline{Kind: "always"}, WakerTargetGroup: true, Now: nowT}
	if over != nil {
		over(&in)
	}
	r, err := Advice(in)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func ptr(s string) *string { return &s }

func row(r Result, minutes int) Row {
	for _, w := range r.Windows {
		if w.Minutes == minutes {
			return w
		}
	}
	panic("no row")
}

func near(t *testing.T, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestAdviceOfAWorkload(t *testing.T) {
	a := adviceWith(t, nil)
	var ms []int
	for _, w := range a.Windows {
		ms = append(ms, w.Minutes)
	}
	eq(t, ms, IdleWindows)
	eq(t, [3]string{a.ComputedAt, a.From, a.To}, [3]string{to, from, to})
	eq(t, [3]int{a.Current, a.CheckMinutes, a.Recommended}, [3]int{15, 5, 5})
	eq(t, *a.WakeSeconds, 30.0)
	eq(t, [2]bool{a.Priced, a.WakerTargetGroup}, [2]bool{true, true})
	asleepHours := float64(585+13*1420+820) / 60
	w15 := row(a, 15)
	near(t, w15.AsleepHours, asleepHours/days, 1e-6)
	near(t, *w15.USDMonth, asleepHours*0.0456*30/days, 1e-6)
	eq(t, w15.WakesPerDay, 1.0)
	eq(t, *w15.WaitSecondsPerDay, 30.0)
	eq(t, a.Actual, Actual{14, 14, false})
}

func TestAdviceWithinBusinessHours(t *testing.T) {
	a := adviceWith(t, func(in *Input) { in.Baseline = paris })
	r, _ := Replay(ReplayInput{Requests: dailyRequests(), Windows: []int{15}, CheckMinutes: 5, WakeSeconds: 30})
	var hours float64
	for _, ph := range r[0].Asleep {
		h, err := prices.BaselineHours(ph.From, ph.To, paris)
		if err != nil {
			t.Fatal(err)
		}
		hours += h
	}
	near(t, *row(a, 15).USDMonth, hours*0.0456*30/days, 1e-6)
	if !(*row(a, 15).USDMonth < *row(adviceWith(t, nil), 15).USDMonth) {
		t.Error("business hours should save less")
	}
}

func TestAdviceNothingPriced(t *testing.T) {
	for _, a := range []Result{
		adviceWith(t, func(in *Input) { in.Stopped = []stopped.Item{t3, odd} }),
		adviceWith(t, func(in *Input) { in.Stopped = nil }),
	} {
		if a.Priced {
			t.Error("priced")
		}
		for _, w := range a.Windows {
			if w.USDMonth != nil || !(w.AsleepHours > 0) {
				t.Errorf("window %+v", w)
			}
		}
	}
}

func TestAdviceRunningPeriods(t *testing.T) {
	var running []Period
	for d := 0; d < days; d++ {
		running = append(running, p(d*1440+480, d*1440+1200))
	}
	measured := adviceWith(t, func(in *Input) { in.Running = running })
	r, _ := Replay(ReplayInput{Requests: dailyRequests(), Windows: []int{15}, CheckMinutes: 5, WakeSeconds: 30})
	within := func(ps []Period, m int) bool {
		for _, q := range ps {
			if at(m) >= q.From && at(m) < q.To {
				return true
			}
		}
		return false
	}
	minutes := 0
	for m := 0; m < end; m++ {
		if within(r[0].Asleep, m) && within(running, m) {
			minutes++
		}
	}
	near(t, *row(measured, 15).USDMonth, 0.0456*(float64(minutes)/60)*30/days, 1e-9)
	base := row(adviceWith(t, nil), 15)
	m15 := row(measured, 15)
	eq(t, [2]float64{m15.AsleepHours, m15.WakesPerDay}, [2]float64{base.AsleepHours, base.WakesPerDay})
	all := adviceWith(t, func(in *Input) { in.Running = []Period{{from, to}} })
	near(t, *row(all, 15).USDMonth, *base.USDMonth, 1e-9)
	none := adviceWith(t, func(in *Input) { in.Running = []Period{} })
	eq(t, *row(none, 15).USDMonth, 0.0)
}

func TestAdviceNoWakeMeasured(t *testing.T) {
	a := adviceWith(t, func(in *Input) { in.WakeMs = nil })
	if a.WakeSeconds != nil {
		t.Error("wakeSeconds")
	}
	for _, w := range a.Windows {
		if w.WaitSecondsPerDay != nil {
			t.Error("wait")
		}
	}
}

func TestAdviceNoisy(t *testing.T) {
	eq(t, adviceWith(t, func(in *Input) { in.ActualWakes = 9 }).Actual, Actual{9, 14, true})
	eq(t, adviceWith(t, func(in *Input) { in.ActualWakes = 10 }).Actual.Noisy, false)
	four := requests([2]int{600, 1}, [2]int{2040, 1}, [2]int{3480, 1}, [2]int{4920, 1})
	eq(t, adviceWith(t, func(in *Input) { in.Requests = four; in.ActualWakes = 2 }).Actual, Actual{2, 4, false})
}

func TestAdviceOverTheTimeTheHistoryKnows(t *testing.T) {
	eq(t, adviceWith(t, func(in *Input) { in.ActualFrom = ptr(at(5 * 1440)); in.ActualWakes = 9 }).Actual, Actual{9, 9, false})
	eq(t, adviceWith(t, func(in *Input) { in.ActualFrom = nil; in.ActualWakes = 2 }).Actual, Actual{2, 14, false})
}

func TestAdviceCurrentWindowOfADay(t *testing.T) {
	a := adviceWith(t, func(in *Input) { in.Current = 1440 })
	var ms []int
	for _, w := range a.Windows {
		ms = append(ms, w.Minutes)
	}
	eq(t, ms, append(append([]int{}, IdleWindows...), 1440))
	found := false
	for _, m := range IdleWindows {
		found = found || m == a.Recommended
	}
	if !found {
		t.Errorf("recommended %d", a.Recommended)
	}
}
