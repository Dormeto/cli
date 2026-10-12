// Package parity replays the shared test cases the manager's Node export writes
// into testdata/parity, and compares Go results with the Node ones.
package parity

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Topic is one file of shared cases.
type Topic struct {
	Manager string `json:"manager"`
	Router  string `json:"router"`
	Commit  string `json:"commit"`
	Cases   []Case `json:"cases"`
}

// Case is one shared case: an input and the output the Node code gave.
type Case struct {
	Name   string          `json:"name"`
	Input  json.RawMessage `json:"input"`
	Output json.RawMessage `json:"output"`
}

// moduleRoot walks up from the working directory to the directory holding go.mod.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no go.mod above the working directory")
		}
		dir = parent
	}
}

// Load reads testdata/parity/<topic>.json from the module root.
func Load(t testing.TB, topic string) Topic {
	t.Helper()
	root, err := moduleRoot()
	if err != nil {
		t.Fatalf("parity: %v", err)
	}
	path := filepath.Join(root, "testdata", "parity", topic+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("parity: %v", err)
	}
	var out Topic
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("parity: %s: %v", path, err)
	}
	return out
}

func decode(b []byte) (any, error) {
	var v any
	err := json.Unmarshal(b, &v)
	return v, err
}

// Equal fails t unless want and got are the same JSON: object key order is
// ignored and numbers are compared to a relative 1e-9.
func Equal(t testing.TB, want, got json.RawMessage) {
	t.Helper()
	w, err := decode(want)
	if err != nil {
		t.Fatalf("parity: want is not JSON: %v", err)
		return
	}
	g, err := decode(got)
	if err != nil {
		t.Fatalf("parity: got is not JSON: %v", err)
		return
	}
	if path, ok := diff("$", w, g); !ok {
		t.Errorf("parity: differs at %s\nwant: %s\ngot:  %s", path, want, got)
	}
}

// diff returns the path of the first difference between two decoded JSON values.
func diff(path string, w, g any) (string, bool) {
	switch wv := w.(type) {
	case map[string]any:
		gv, ok := g.(map[string]any)
		if !ok || len(gv) != len(wv) {
			return path, false
		}
		for k, x := range wv {
			y, present := gv[k]
			if !present {
				return path + "." + k, false
			}
			if p, ok := diff(path+"."+k, x, y); !ok {
				return p, false
			}
		}
	case []any:
		gv, ok := g.([]any)
		if !ok || len(gv) != len(wv) {
			return path, false
		}
		for i := range wv {
			if p, ok := diff(fmt.Sprintf("%s[%d]", path, i), wv[i], gv[i]); !ok {
				return p, false
			}
		}
	case float64:
		gv, ok := g.(float64)
		if !ok || math.Abs(wv-gv) > 1e-9*math.Max(math.Abs(wv), math.Abs(gv)) {
			return path, false
		}
	default:
		if !reflect.DeepEqual(w, g) {
			return path, false
		}
	}
	return "", true
}

// Answers answers port calls from the recorded answers of a case.
type Answers struct {
	entries map[string][]entry
}

type entry struct {
	Input  any             `json:"input"`
	Output json.RawMessage `json:"output"`
}

// Recorded reads a case's input.answers: {"<port>.<method>": [{"input", "output"}]}.
func Recorded(t testing.TB, answers json.RawMessage) *Answers {
	t.Helper()
	a := &Answers{}
	if err := json.Unmarshal(answers, &a.entries); err != nil {
		t.Fatalf("parity: recorded answers: %v", err)
	}
	return a
}

// Call answers port.method(input) with the first recorded entry whose input
// equals input (JSON compared, key order ignored), decoded into out. A call
// with no entry returns an error.
func (a *Answers) Call(port, method string, input any, out any) error {
	b, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("parity: %s.%s: %w", port, method, err)
	}
	in, err := decode(b)
	if err != nil {
		return fmt.Errorf("parity: %s.%s: %w", port, method, err)
	}
	for _, e := range a.entries[port+"."+method] {
		if _, same := diff("$", e.Input, in); same {
			if len(bytes.TrimSpace(e.Output)) == 0 {
				return nil
			}
			return json.Unmarshal(e.Output, out)
		}
	}
	return fmt.Errorf("parity: no recorded answer for %s.%s(%s)", port, method, b)
}
