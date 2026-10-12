package prices

// Ports fleet/test/savings.test.ts.

import (
	"context"
	"math"
	"reflect"
	"testing"

	"github.com/Dormeto/cli/internal/stopped"
)

var (
	t3    = stopped.Item{Kind: "ec2", Type: "t3.medium", Count: 1, OS: "Linux"}
	db    = stopped.Item{Kind: "rds", Class: "db.t4g.micro", Engine: "postgres"}
	paris = Baseline{Kind: "business-hours", TimeZone: "Europe/Paris"}
	other = stopped.Item{Kind: "unknown", Backend: "ecs:web", Reason: "denied"}
)

func price(item stopped.Item) (*float64, error) {
	var v float64
	switch item.Kind {
	case "ec2":
		v = 0.0456
	case "rds":
		v = 0.017
	default:
		return nil, nil
	}
	return &v, nil
}

func hoursOf(from, to string, b Baseline) float64 { return BaselineHours(from, to, b) }

func closeTo(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestBaselineHoursAlwaysEveryHour(t *testing.T) {
	if h := hoursOf("2026-10-08T00:00:00.000Z", "2026-10-08T10:00:00.000Z", Baseline{Kind: "always"}); h != 10 {
		t.Errorf("got %v", h)
	}
}

func TestBaselineHoursBusinessHoursWeekdays8To20InItsTimeZoneAcrossAWeekend(t *testing.T) {
	// Friday 18:00 to Monday 10:00 in Paris (UTC+2): Friday 18-20 and Monday 08-10.
	closeTo(t, hoursOf("2026-10-09T16:00:00.000Z", "2026-10-12T08:00:00.000Z", paris), 4)
}

func TestBaselineHoursFollowADaylightSavingChange(t *testing.T) {
	// Monday 26 October 07:30 to 08:30 in Paris is 06:30 to 07:30 UTC: half an hour of business.
	closeTo(t, hoursOf("2026-10-26T06:30:00.000Z", "2026-10-26T07:30:00.000Z", paris), 0.5)
	// The Monday before (UTC+2), the same UTC hour is 08:30 to 09:30 in Paris: a whole hour.
	closeTo(t, hoursOf("2026-10-19T06:30:00.000Z", "2026-10-19T07:30:00.000Z", paris), 1)
}

func TestBaselineHoursOfAnEmptyOrInvalidPeriodIsZero(t *testing.T) {
	for _, c := range [][2]string{{"2026-10-12T08:00:00.000Z", "2026-10-12T08:00:00.000Z"}, {"2026-10-12T09:00:00.000Z", "2026-10-12T08:00:00.000Z"}, {"x", "2026-10-12T08:00:00.000Z"}} {
		if h := hoursOf(c[0], c[1], paris); h != 0 {
			t.Errorf("%v = %v", c, h)
		}
	}
}

func hourlyOf(item stopped.Item) func(context.Context, stopped.Item) (*float64, error) {
	return func(_ context.Context, i stopped.Item) (*float64, error) { return price(i) }
}

func TestSavingsPricesEachAsleepPhasesHoursByWhatItStoppedTheItemsSummed(t *testing.T) {
	s, err := SavingsOf(context.Background(), []Phase{
		{State: "AWAKE", From: "2026-10-08T00:00:00.000Z", To: "2026-10-08T02:00:00.000Z"},
		{State: "ASLEEP", From: "2026-10-08T02:00:00.000Z", To: "2026-10-08T12:00:00.000Z", Stopped: []stopped.Item{t3, db}},
		{State: "ASLEEP", From: "2026-10-08T14:00:00.000Z", To: "2026-10-08T16:00:00.000Z", Stopped: []stopped.Item{t3}},
	}, hourlyOf(t3), Baseline{Kind: "always"})
	if err != nil {
		t.Fatal(err)
	}
	closeTo(t, s.USD, 12*0.0456+10*0.017)
	if s.Hours != 12 {
		t.Errorf("hours = %v", s.Hours)
	}
	type kh struct {
		Kind  string
		Hours float64
	}
	var got []kh
	for _, i := range s.Items {
		got = append(got, kh{i.Item.Kind, i.Hours})
	}
	if !reflect.DeepEqual(got, []kh{{"ec2", 12}, {"rds", 10}}) {
		t.Errorf("items = %v", got)
	}
	if s.Unpriced == nil || len(s.Unpriced) != 0 {
		t.Errorf("unpriced = %#v, want empty non-nil", s.Unpriced)
	}
}

func TestSavingsSaysWhatItCouldNotPriceNeverAsNothingSaved(t *testing.T) {
	s, err := SavingsOf(context.Background(), []Phase{
		{State: "ASLEEP", From: "2026-10-08T00:00:00.000Z", To: "2026-10-08T05:00:00.000Z"},
		{State: "ASLEEP", From: "2026-10-08T06:00:00.000Z", To: "2026-10-08T08:00:00.000Z", Stopped: []stopped.Item{t3, other}},
	}, hourlyOf(t3), Baseline{Kind: "always"})
	if err != nil {
		t.Fatal(err)
	}
	closeTo(t, s.USD, 2*0.0456)
	want := []Unpriced{{Reason: "not described (a router before 0.27.0)", Hours: 5}, {Reason: "not priced", Item: &other, Hours: 2}}
	if !reflect.DeepEqual(s.Unpriced, want) {
		t.Errorf("unpriced = %+v, want %+v", s.Unpriced, want)
	}
}

func TestSavingsCountsOnlyTheBaselinesHours(t *testing.T) {
	s, err := SavingsOf(context.Background(), []Phase{{State: "ASLEEP", From: "2026-10-09T16:00:00.000Z", To: "2026-10-12T08:00:00.000Z", Stopped: []stopped.Item{t3}}}, hourlyOf(t3), paris)
	if err != nil {
		t.Fatal(err)
	}
	closeTo(t, s.Hours, 4)
	closeTo(t, s.USD, 4*0.0456)
}

func TestSavingsReturnsThePricesError(t *testing.T) {
	_, err := SavingsOf(context.Background(), []Phase{{State: "ASLEEP", From: "2026-10-08T00:00:00.000Z", To: "2026-10-08T05:00:00.000Z", Stopped: []stopped.Item{t3}}},
		func(context.Context, stopped.Item) (*float64, error) { return nil, context.Canceled }, Baseline{Kind: "always"})
	if err != context.Canceled {
		t.Fatalf("err = %v", err)
	}
}
