package fleet

// --dry-run (the AWS and Google Cloud review's §4.2): what a write would send, said and kept, never sent. A client under a
// dry run sends its reads and stops at its first write with ErrDryRun, which the command reads as success: nothing was sent.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"unicode/utf8"
)

// ErrDryRun is the answer of a write under --dry-run: it was said, not sent.
var ErrDryRun = errors.New("dry run: nothing sent")

// DryRun is a client's --dry-run: each write is said on Out and kept in Requests, not sent.
type DryRun struct {
	Out io.Writer

	mu       sync.Mutex
	requests []DryRequest
}

// DryRequest is a write a dry run kept: its method, its path under /iam and its body (none: absent).
type DryRequest struct {
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Body   json.RawMessage `json:"body,omitempty"`
}

// Requests are the writes kept, in their order.
func (d *DryRun) Requests() []DryRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]DryRequest{}, d.requests...)
}

// keep says the write, "dry run: would PUT /iam/settings/workloads, nothing sent" and its body on the next line, and keeps it.
func (d *DryRun) keep(method, path string, payload []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := DryRequest{Method: method, Path: path}
	if len(bytes.TrimSpace(payload)) > 0 {
		r.Body = append(json.RawMessage(nil), payload...)
	}
	d.requests = append(d.requests, r)
	if d.Out == nil {
		return
	}
	fmt.Fprintf(d.Out, "dry run: would %s /iam%s, nothing sent\n", method, path)
	if r.Body != nil {
		fmt.Fprintf(d.Out, "  %s\n", DryRunBody(r.Body))
	}
}

// maxShown is the longest string a dry run's body shows: a longer one (an icon, a custom page) is said by its size.
const maxShown = 200

// DryRunBody is a request's body as a dry run says it: compact JSON, its keys sorted, HTML as it is, and a string longer than
// 200 characters said by its size, "(1234 characters)", as `waiting-page show` says an icon or a page.
func DryRunBody(body any) string {
	data, ok := body.(json.RawMessage)
	if !ok {
		var err error
		if data, err = json.Marshal(body); err != nil {
			return fmt.Sprint(body)
		}
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return string(data)
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(shortened(v)); err != nil {
		return string(data)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// shortened is v with each string longer than maxShown replaced by its size.
func shortened(v any) any {
	switch t := v.(type) {
	case string:
		if n := utf8.RuneCountInString(t); n > maxShown {
			return fmt.Sprintf("(%d characters)", n)
		}
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = shortened(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = shortened(e)
		}
		return out
	}
	return v
}
