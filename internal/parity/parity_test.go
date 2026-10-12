package parity

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// spy records the failures Equal reports, instead of failing the test.
type spy struct {
	testing.TB
	failed bool
	msg    string
}

func (s *spy) Helper() {}
func (s *spy) Errorf(f string, a ...any) {
	s.failed = true
	s.msg = strings.TrimSpace(f)
}
func (s *spy) Fatalf(f string, a ...any) { s.Errorf(f, a...) }

func TestEqual(t *testing.T) {
	tests := []struct {
		name      string
		want, got string
		equal     bool
	}{
		{"same number", `1.5`, `1.5`, true},
		{"within 1e-9 relative", `1000000`, `1000000.0005`, true},
		{"beyond 1e-9 relative", `1000000`, `1000000.01`, false},
		{"small within", `1e-12`, `1.0000000005e-12`, true},
		{"small beyond", `1e-12`, `1.00000002e-12`, false},
		{"zero vs tiny", `0`, `1e-6`, false},
		{"key order ignored", `{"a":1,"b":{"c":[1,2],"d":null}}`, `{"b":{"d":null,"c":[1,2]},"a":1}`, true},
		{"array order kept", `[1,2]`, `[2,1]`, false},
		{"missing key", `{"a":1}`, `{"a":1,"b":2}`, false},
		{"nested number beyond", `{"a":[{"x":1}]}`, `{"a":[{"x":1.001}]}`, false},
		{"string differs", `"a"`, `"b"`, false},
		{"zero vs null", `0`, `null`, false},
		{"null vs absent", `{"a":null}`, `{}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &spy{TB: t}
			Equal(s, json.RawMessage(tt.want), json.RawMessage(tt.got))
			if s.failed == tt.equal {
				t.Fatalf("Equal(%s, %s) failed=%v, want equal=%v (%s)", tt.want, tt.got, s.failed, tt.equal, s.msg)
			}
		})
	}
}

const answers = `{
  "ec2.describeInstances": [
    {"input": {"InstanceIds": ["i-1"], "Filters": [{"Name": "a", "Values": ["x"]}]}, "output": {"Reservations": []}},
    {"input": {"InstanceIds": ["i-2"]}, "output": {"Reservations": [{"Id": "r"}]}}
  ]
}`

func TestRecordedMatchesInputWithKeysInAnotherOrder(t *testing.T) {
	a := Recorded(t, json.RawMessage(answers))
	var out struct{ Reservations []struct{ Id string } }
	in := map[string]any{"InstanceIds": []string{"i-2"}}
	if err := a.Call("ec2", "describeInstances", in, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Reservations) != 1 || out.Reservations[0].Id != "r" {
		t.Fatalf("out = %+v", out)
	}
	out.Reservations = nil
	in2 := map[string]any{"Filters": []map[string]any{{"Values": []string{"x"}, "Name": "a"}}, "InstanceIds": []string{"i-1"}}
	if err := a.Call("ec2", "describeInstances", in2, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Reservations) != 0 {
		t.Fatalf("out = %+v", out)
	}
}

func TestRecordedMissingCallFails(t *testing.T) {
	a := Recorded(t, json.RawMessage(answers))
	var out any
	for _, c := range []struct {
		port, method string
		in           any
	}{
		{"ec2", "describeInstances", map[string]any{"InstanceIds": []string{"i-3"}}},
		{"ec2", "describeVolumes", map[string]any{}},
		{"rds", "describeInstances", map[string]any{"InstanceIds": []string{"i-1"}}},
	} {
		err := a.Call(c.port, c.method, c.in, &out)
		if err == nil || !strings.Contains(err.Error(), c.port+"."+c.method) {
			t.Errorf("%s.%s: err = %v", c.port, c.method, err)
		}
	}
}

func TestLoad(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "testdata", "parity")
	file := filepath.Join(dir, "zz-load-test.json")
	body := `{"manager":"0.10.0","router":"0.29.0","commit":"abc","cases":[{"name":"n","input":{"a":1},"output":[2]}]}`
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(file) })
	topic := Load(t, "zz-load-test")
	if topic.Manager != "0.10.0" || topic.Router != "0.29.0" || topic.Commit != "abc" || len(topic.Cases) != 1 {
		t.Fatalf("topic = %+v", topic)
	}
	c := topic.Cases[0]
	if c.Name != "n" || string(c.Input) != `{"a":1}` || string(c.Output) != `[2]` {
		t.Fatalf("case = %+v", c)
	}
}

const errorAnswers = `{
  "pricing.getProducts": [
    {"input": {"a": 1}, "error": {"name": "ThrottlingException", "message": "Rate exceeded"}},
    {"input": {"a": 2}, "error": {"name": "KubernetesError", "message": "forbidden", "status": 403, "reason": "Forbidden", "code": "E403"}},
    {"input": {"a": 3}, "output": {"ok": true}}
  ]
}`

func TestRecordedErrorIsReturnedAsTheCallsError(t *testing.T) {
	a := Recorded(t, json.RawMessage(errorAnswers))
	var out any
	err := a.Call("pricing", "getProducts", map[string]any{"a": 1}, &out)
	var re *RecordedError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v (%T), want *RecordedError", err, err)
	}
	if re.Name != "ThrottlingException" || re.Message != "Rate exceeded" || re.Status != 0 || re.Reason != "" {
		t.Fatalf("re = %+v", re)
	}
	if err.Error() != "Rate exceeded" {
		t.Fatalf("Error() = %q", err.Error())
	}

	err = a.Call("pricing", "getProducts", map[string]any{"a": 2}, &out)
	if !errors.As(err, &re) || re.Name != "KubernetesError" || re.Status != 403 || re.Reason != "Forbidden" || re.Code != "E403" {
		t.Fatalf("err = %#v", err)
	}

	var ok struct{ Ok bool }
	if err := a.Call("pricing", "getProducts", map[string]any{"a": 3}, &ok); err != nil || !ok.Ok {
		t.Fatalf("output entry: %v %+v", err, ok)
	}
}
