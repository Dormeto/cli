// The JSON report: ports jsonReport of manager packages/estimate/src/report.ts, JSON.stringify(e, null, 2) and a newline,
// with "tool" added first (the only difference from Node's by design).
package report

import (
	"bytes"
	"encoding/json"

	"github.com/Dormeto/cli/internal/estimate"
)

type tool struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// withTool writes "tool" before the estimate's own fields (the embedded struct's fields are inlined, in their order).
type withTool struct {
	Tool tool `json:"tool"`
	estimate.Estimate
}

// JSON is the estimate as JSON.stringify(e, null, 2) writes it, a newline after, with "tool": {"name": "dormeto",
// "version": version} first. It returns nil only for a number JSON cannot hold (NaN or infinity, which JavaScript writes
// as null): the estimate's figures are finite.
func JSON(e estimate.Estimate, version string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(withTool{Tool: tool{Name: "dormeto", Version: version}, Estimate: e}); err != nil {
		return nil
	}
	return unescapeLineSeparators(buf.Bytes())
}

// unescapeLineSeparators writes U+2028 and U+2029 as they are, as JSON.stringify does (Go escapes them as a backslash, "u" and
// 2028 or 2029). Each backslash starts an escape, so a text backslash followed by the same letters (written as two
// backslashes) is left alone.
func unescapeLineSeparators(b []byte) []byte {
	lineSeparator, paragraphSeparator := []byte("\\"+"u2028"), []byte("\\"+"u2029")
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			out = append(out, b[i])
			continue
		}
		switch rest := b[i:]; {
		case bytes.HasPrefix(rest, lineSeparator):
			out = append(out, string(rune(0x2028))...)
			i += len(lineSeparator) - 1
		case bytes.HasPrefix(rest, paragraphSeparator):
			out = append(out, string(rune(0x2029))...)
			i += len(paragraphSeparator) - 1
		default:
			// The escape's own second character is not a start.
			out = append(out, b[i])
			if i+1 < len(b) {
				i++
				out = append(out, b[i])
			}
		}
	}
	return out
}
