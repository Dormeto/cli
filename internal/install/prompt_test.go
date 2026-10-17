// The install's terminal prompts.
package install

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestAsk(t *testing.T) {
	cases := []struct {
		name, in, suggestion, want, out string
	}{
		{"answer", "eu-west-1\n", "", "eu-west-1", "Region: "},
		{"suggestion on empty line", "\n", "prd", "prd", "Region [prd]: "},
		{"trimmed", "  dev \r\n", "prd", "dev", "Region [prd]: "},
		{"last line without newline", "dev", "", "dev", "Region: "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out bytes.Buffer
			got, err := NewPrompter(strings.NewReader(c.in), &out, true).Ask("Region", c.suggestion)
			if err != nil || got != c.want || out.String() != c.out {
				t.Fatalf("got %q, %v, written %q; want %q, written %q", got, err, out.String(), c.want, c.out)
			}
		})
	}
}

func TestAskEOF(t *testing.T) {
	var out bytes.Buffer
	if _, err := NewPrompter(strings.NewReader(""), &out, true).Ask("Region", "x"); !errors.Is(err, io.EOF) {
		t.Fatalf("err %v, want io.EOF", err)
	}
}

func TestAskReadsLineByLine(t *testing.T) {
	p := NewPrompter(strings.NewReader("one\ntwo\n"), io.Discard, true)
	a, _ := p.Ask("q", "")
	b, _ := p.Ask("q", "")
	if a != "one" || b != "two" {
		t.Fatalf("%q %q", a, b)
	}
}

func TestConfirm(t *testing.T) {
	for in, want := range map[string]bool{"y\n": true, "YES \n": true, "Yes\n": true, "n\n": false, "\n": false, "yep\n": false, "": false} {
		var out bytes.Buffer
		got, err := NewPrompter(strings.NewReader(in), &out, true).Confirm("Go on?")
		if err != nil || got != want || out.String() != "Go on? [y/N]: " {
			t.Errorf("%q: got %v, %v, written %q; want %v", in, got, err, out.String(), want)
		}
	}
}

func TestNoTerminal(t *testing.T) {
	var out bytes.Buffer
	p := NewPrompter(strings.NewReader("y\n"), &out, false)
	if _, err := p.Ask("Region", "x"); !errors.Is(err, ErrNoTerminal) {
		t.Fatalf("Ask err %v", err)
	}
	if ok, err := p.Confirm("Go on?"); ok || err != nil {
		t.Fatalf("Confirm %v, %v", ok, err)
	}
	if out.Len() != 0 {
		t.Fatalf("written %q", out.String())
	}
}
