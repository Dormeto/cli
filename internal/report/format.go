// Text as the Node report writes it (manager packages/estimate/src/report.ts): dollars as Intl writes them, JavaScript's
// Math.round, String.length in UTF-16 units, and the small helpers n, day, windowAdjective and windowWords.
package report

import (
	"math"
	"strconv"
	"strings"

	"github.com/Dormeto/cli/internal/estimate"
)

// usd is v.toLocaleString('en-US', {style: 'currency', currency: 'USD'}): two decimals, thousands separated by commas,
// a minus before the dollar sign (also for -0 and for a small negative that rounds to zero). ICU rounds half away from
// zero the shortest decimal that reads back as v (1.005 gives $1.01), not v's exact binary value.
func usd(v float64) string {
	if math.IsNaN(v) {
		return "$NaN"
	}
	sign := ""
	if math.Signbit(v) {
		sign = "-"
	}
	if math.IsInf(v, 0) {
		return sign + "$∞"
	}
	whole, frac, _ := strings.Cut(strconv.FormatFloat(math.Abs(v), 'f', -1, 64), ".")
	frac += "000"
	digits := []byte(whole + frac[:2])
	if frac[2] >= '5' {
		i := len(digits) - 1
		for ; i >= 0 && digits[i] == '9'; i-- {
			digits[i] = '0'
		}
		if i < 0 {
			digits = append([]byte{'1'}, digits...)
		} else {
			digits[i]++
		}
	}
	whole, cents := string(digits[:len(digits)-2]), string(digits[len(digits)-2:])
	var grouped strings.Builder
	for i, d := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(d)
	}
	return sign + "$" + grouped.String() + "." + cents
}

// jsRound is Math.round: the nearest whole number, the larger on a tie (also for a negative: -2.5 gives -2).
func jsRound(v float64) float64 {
	f := math.Floor(v)
	if v-f >= 0.5 {
		return f + 1
	}
	return f
}

// len16 is JavaScript's String.length: the UTF-16 code units (Go's rune count differs for a character above U+FFFF,
// two units in JavaScript and one rune).
func len16(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// padStart and padEnd are String.padStart and padEnd: they pad to a width in UTF-16 units.
func padStart(s string, width int) string {
	if pad := width - len16(s); pad > 0 {
		return strings.Repeat(" ", pad) + s
	}
	return s
}

func padEnd(s string, width int) string {
	if pad := width - len16(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

// num is a number in a template literal.
func num(v int) string { return strconv.Itoa(v) }

// n is `${v} ${one}` or the plural: "1 workload", "2 workloads".
func n(v int, one string, many ...string) string {
	if v == 1 {
		return num(v) + " " + one
	}
	if len(many) > 0 {
		return num(v) + " " + many[0]
	}
	return num(v) + " " + one + "s"
}

// day is a figure to a tenth, as Math.round(v * 10) / 10 writes it.
func day(v float64) string { return estimate.JSNumber(jsRound(v*10) / 10) }

// percentOf is `${Math.round(v)}%`.
func percentOf(v float64) string { return estimate.JSNumber(jsRound(v)) + "%" }

// nullable is a number that may be null, in a template literal.
func nullable(v *float64) string {
	if v == nil {
		return "null"
	}
	return estimate.JSNumber(*v)
}

// The report's window in words: "1-hour" or "15-minute" before a noun, "1 hour" or "15 minutes" alone.
func hours(m int) bool { return m%60 == 0 }

func windowAdjective(m int) string {
	if hours(m) {
		return num(m/60) + "-hour"
	}
	return num(m) + "-minute"
}

func windowWords(m int) string {
	if hours(m) {
		return n(m/60, "hour")
	}
	return n(m, "minute")
}

// cell escapes the pipes of a Markdown table cell.
func cell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

// lastSegment is s.split('/').at(-1).
func lastSegment(s string) string {
	if i := strings.LastIndexByte(s, '/'); i >= 0 {
		return s[i+1:]
	}
	return s
}

// hostsOr is hosts.join(', ') || fallback.
func hostsOr(hosts []string, fallback string) string {
	if joined := strings.Join(hosts, ", "); joined != "" {
		return joined
	}
	return fallback
}
