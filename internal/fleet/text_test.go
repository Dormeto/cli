package fleet

// The text helpers of the commands: labelled lines and times in a zone.

import (
	"testing"
	"time"
)

func TestLabelledAlignsAndSkipsEmptyValues(t *testing.T) {
	got := Labelled([][2]string{
		{"version", "0.30.0"},
		{"health", ""},
		{"check schedule", "daily"},
		{"problems", "a\nb"},
	})
	want := "version         0.30.0\n" +
		"check schedule  daily\n" +
		"problems        a\n" +
		"                b\n"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if got := Labelled([][2]string{{"a much longer label", ""}, {"id", "x"}}); got != "id  x\n" {
		t.Errorf("a skipped row widens the labels: %q", got)
	}
}

func TestWhenInAZone(t *testing.T) {
	paris := time.FixedZone("CEST", 2*60*60)
	for at, want := range map[string]string{
		"2026-10-19T12:00:00.000Z":  "2026-10-19 14:00 CEST",
		"2026-10-19T23:30:00+00:00": "2026-10-20 01:30 CEST",
		"":                          "",
		"tomorrow":                  "tomorrow",
	} {
		if got := When(at, paris); got != want {
			t.Errorf("When(%q) = %q, want %q", at, got, want)
		}
	}
}

func TestAgeAndTable(t *testing.T) {
	if got := Age("2026-10-19T09:00:00Z", renderNow); got != "3h" {
		t.Errorf("age %q", got)
	}
	if got := Table([][]string{{"A", "B"}, {"long", "x"}}); got != "A     B\nlong  x\n" {
		t.Errorf("table %q", got)
	}
}
