package fleet

// --dry-run (the AWS and Google Cloud review's §4.2): what a write would send, said and kept, never sent. A client under a
// dry run sends its reads and stops at its first write with ErrDryRun, which the command reads as success: nothing was sent.
// Under --json it prints the released shape of both subtrees, {"dryRun": true, "targets": [...]}: what each request would send
// is in the text alone.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// ErrDryRun is the answer of a write under --dry-run: it was said, not sent.
var ErrDryRun = errors.New("dry run: nothing sent")

// DryRun is a client's --dry-run: each write is said on Out and kept in Requests, not sent. Context are the members the
// command adds to its --json beside them (onboard's candidate). Targets, when set, are the targets its --json names: those a
// command confirms and names itself (a workload, an exclusion's target in words), whatever its requests address.
type DryRun struct {
	Out     io.Writer
	Context map[string]any
	Targets []string

	mu       sync.Mutex
	requests []DryRequest
}

// JSON is what a dry run prints under --json: {"dryRun": true, "targets": [...]}, each target once (Targets when set), and
// its context.
func (d *DryRun) JSON() map[string]any {
	targets := d.Targets
	if targets == nil {
		targets = DryTargets(d.Requests())
	}
	out := map[string]any{"dryRun": true, "targets": targets}
	for k, v := range d.Context {
		out[k] = v
	}
	return out
}

// DryRequest is a write a dry run kept, as both subtrees' dry runs say theirs: what it addresses (RequestTargets), its method,
// its path and its body (none: absent).
type DryRequest struct {
	Targets []string
	Method  string
	Path    string
	Body    json.RawMessage
}

// DryTargets are the targets of the requests, each once, in their order: the released dry run's "targets".
func DryTargets(requests []DryRequest) []string {
	out := []string{}
	for _, r := range requests {
		for _, t := range r.Targets {
			if !slices.Contains(out, t) {
				out = append(out, t)
			}
		}
	}
	return out
}

// Requests are the writes kept, in their order.
func (d *DryRun) Requests() []DryRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]DryRequest{}, d.requests...)
}

// keep says the write as DryRunText says it, "dry run: r1: would PUT /iam/routers/r1/description, nothing sent" and its body on
// the next line, and keeps it.
func (d *DryRun) keep(method, path string, payload []byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r := DryRequest{Method: method, Path: path}
	if len(bytes.TrimSpace(payload)) > 0 {
		r.Body = append(json.RawMessage(nil), payload...)
	}
	r.Targets = RequestTargets(path, r.Body)
	d.requests = append(d.requests, r)
	if d.Out != nil {
		fmt.Fprint(d.Out, DryRunText(strings.Join(r.Targets, ", "), method, "/iam"+path, r.Body))
	}
}

// DryRunText is a request as a dry run says it, the same on both subtrees: "dry run: <target>: would <method> <path>, nothing
// sent" ("dry run: would …" for a request that addresses no target), then its body on the next line, indented (DryRunBody).
func DryRunText(target, method, path string, body json.RawMessage) string {
	var b strings.Builder
	b.WriteString("dry run: ")
	if target != "" {
		b.WriteString(target + ": ")
	}
	fmt.Fprintf(&b, "would %s %s, nothing sent\n", method, path)
	if len(body) > 0 {
		fmt.Fprintf(&b, "  %s\n", DryRunBody(body))
	}
	return b.String()
}

// organization is the target of the organization's own writes: its settings, discovery, the manager.
const organization = "organization"

// RequestTargets are what a write to the manager addresses, as its command names it: the <router>/<workload>, router, user,
// group or exclusion of its path; for a new user or group, the one its body names; for an operation, the <router>/<workload>,
// router, account or candidate its body names; for a new exclusion, its workload's <router>/<workload>, else its target's
// ARN, path or account; the candidates a dismissal or a restore names; else the organization.
func RequestTargets(path string, body json.RawMessage) []string {
	if t := requestTarget(path, body); t != "" {
		return []string{t}
	}
	var named struct{ IDs []string }
	if strings.HasPrefix(path, "/discovery/") && json.Unmarshal(body, &named) == nil && len(named.IDs) > 0 {
		return named.IDs
	}
	return []string{organization}
}

// requestTarget is the one target of RequestTargets a path or a body names; "" for none.
func requestTarget(path string, body json.RawMessage) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if u, err := url.PathUnescape(p); err == nil {
			parts[i] = u
		}
	}
	var fields map[string]any
	_ = json.Unmarshal(body, &fields)
	named := func(fields map[string]any, keys ...string) string {
		for _, k := range keys {
			if v, ok := fields[k].(string); ok && v != "" {
				return v
			}
		}
		return ""
	}
	member := func(key string) map[string]any {
		m, _ := fields[key].(map[string]any)
		return m
	}
	switch {
	case parts[0] == "workloads" && len(parts) >= 3:
		return parts[1] + "/" + parts[2]
	case slices.Contains([]string{"routers", "users", "groups", "exclusions"}, parts[0]) && len(parts) >= 2:
		return parts[1]
	case parts[0] == "users" || parts[0] == "groups":
		return named(fields, "email", "name")
	case parts[0] == "operations" && named(fields, "router") != "" && named(fields, "workloadId") != "": // an offboarding
		return named(fields, "router") + "/" + named(fields, "workloadId")
	case parts[0] == "operations":
		return named(fields, "router", "account", "candidateId")
	case parts[0] == "exclusions":
		if w := member("workload"); named(w, "router") != "" && named(w, "id") != "" { // a workload's exclusion
			return named(w, "router") + "/" + named(w, "id")
		}
		return named(member("target"), "arn", "path", "account")
	}
	return ""
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
