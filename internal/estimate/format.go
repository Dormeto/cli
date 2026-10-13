// Numbers and dates in text as Node writes them: a number in a template literal (Number.prototype.toString),
// toFixed(1), Date's toISOString and Date.parse.
package estimate

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// jsNumber is a number as a JavaScript template literal writes it: the shortest digits that read back the same, in
// plain notation from 1e-6 to 1e21, else in exponent notation (1e+21, 1e-7).
func jsNumber(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "Infinity"
	case math.IsInf(v, -1):
		return "-Infinity"
	case v == 0:
		return "0"
	}
	if a := math.Abs(v); a >= 1e-6 && a < 1e21 {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	mantissa, exponent, _ := strings.Cut(strconv.FormatFloat(v, 'e', -1, 64), "e")
	sign, digits := exponent[:1], strings.TrimLeft(exponent[1:], "0")
	return mantissa + "e" + sign + digits
}

// toFixed1 is JavaScript's x.toFixed(1): the nearest number of tenths, the larger when two are as near. Go's strconv
// rounds the exact value too, but to even on a tie; a tie is exactly x = m/4 with m odd (0.25, 0.75, 1.25...).
func toFixed1(x float64) string {
	if math.IsNaN(x) || math.Abs(x) >= 1e21 {
		return jsNumber(x)
	}
	sign := ""
	if x < 0 {
		sign, x = "-", -x
	}
	if q := x * 4; q == math.Trunc(q) && math.Mod(q, 2) == 1 {
		n := int64(math.Floor(x*10)) + 1
		return fmt.Sprintf("%s%d.%d", sign, n/10, n%10)
	}
	return sign + strconv.FormatFloat(x, 'f', 1, 64)
}

const isoMillis = "2006-01-02T15:04:05.000Z"

// isoOf is a time as Node's toISOString writes it.
func isoOf(t time.Time) string { return t.UTC().Format(isoMillis) }

// parseISO reads a date as the cache and the requests write it (toISOString), as Date.parse.
func parseISO(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("estimate: invalid date %q", s)
	}
	return t, nil
}
