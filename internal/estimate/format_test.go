// Go only: numbers in text as Node writes them.
package estimate

import "testing"

func TestNumbersAsATemplateLiteralWritesThem(t *testing.T) {
	for v, want := range map[float64]string{0: "0", 5: "5", 0.26: "0.26", 0.13: "0.13", 20: "20", 2.5: "2.5", 1e-6: "0.000001", 123456789012: "123456789012"} {
		if got := jsNumber(v); got != want {
			t.Errorf("jsNumber(%v) = %q, want %q", v, got, want)
		}
	}
}

func TestToFixed1AsJavaScriptRoundsIt(t *testing.T) {
	// JavaScript picks the larger of two equally near: 0.25 is exactly between 0.2 and 0.3 (Go's strconv rounds it to
	// even, 0.2); 0.35 is not exactly 0.35 (a little under), so 0.3 in both.
	for v, want := range map[float64]string{0: "0.0", 1: "1.0", 2.4642857142857144: "2.5", 0.25: "0.3", 0.75: "0.8", 1.25: "1.3", 0.35: "0.3", 0.05: "0.1", 6.000000000000001: "6.0", 2.95: "3.0"} {
		if got := toFixed1(v); got != want {
			t.Errorf("toFixed1(%v) = %q, want %q", v, got, want)
		}
	}
}
