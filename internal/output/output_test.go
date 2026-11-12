package output

import (
	"bytes"
	"errors"
	"io"
	"slices"
	"testing"
)

func TestWordsGoToStdoutAndToStderrUnderJSON(t *testing.T) {
	var o, e bytes.Buffer
	if got := Words(false, &o, &e); got != &o {
		t.Errorf("without --json: %v", got)
	}
	if got := Words(true, &o, &e); got != &e {
		t.Errorf("under --json: %v", got)
	}
}

func TestJSONIsIndentedByTwoSpacesWithALineFeedAfterIt(t *testing.T) {
	var b bytes.Buffer
	if err := JSON(&b, map[string]any{"b": []int{1}, "a": true}); err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"a\": true,\n  \"b\": [\n    1\n  ]\n}\n"; b.String() != want {
		t.Errorf("%q, want %q", b.String(), want)
	}
	if err := JSON(&b, func() {}); err == nil {
		t.Error("a value JSON cannot hold printed without an error")
	}
}

func TestStopPrintsUnderJSONAloneItsTargetsAnEmptyListForNone(t *testing.T) {
	var b bytes.Buffer
	if err := Stop(&b, false, "dryRun", true, []string{"r1"}); err != nil || b.Len() != 0 {
		t.Errorf("without --json: %v %q", err, b.String())
	}
	if err := Stop(&b, true, "dryRun", true, nil); err != nil || b.String() != "{\n  \"dryRun\": true,\n  \"targets\": []\n}\n" {
		t.Errorf("no targets: %v %q", err, b.String())
	}
	b.Reset()
	if err := Stop(&b, true, "confirmed", false, []string{"r1", "r2"}); err != nil ||
		b.String() != "{\n  \"confirmed\": false,\n  \"targets\": [\n    \"r1\",\n    \"r2\"\n  ]\n}\n" {
		t.Errorf("targets: %v %q", err, b.String())
	}
}

func TestEmptyIfNilIsAnEmptyListForNilAndTheListOtherwise(t *testing.T) {
	if got := EmptyIfNil[string](nil); got == nil || len(got) != 0 {
		t.Errorf("nil: %#v", got)
	}
	if got := EmptyIfNil([]int{1}); !slices.Equal(got, []int{1}) {
		t.Errorf("%#v", got)
	}
}

func TestNeedsYesCountsTheTargetsAndIsErrNeedsYes(t *testing.T) {
	for n, want := range map[int]string{
		1: "confirmation needed: pass --yes to act on 1 workload without a terminal",
		2: "confirmation needed: pass --yes to act on 2 workloads without a terminal",
		0: "confirmation needed: pass --yes to act on 0 workloads without a terminal",
	} {
		if err := NeedsYes(n, "workload"); !errors.Is(err, ErrNeedsYes) || err.Error() != want {
			t.Errorf("%d: %v", n, err)
		}
	}
}

// asking is an Asker on buffers whose terminal answers answer, the questions asked recorded, each written on the words'
// stream as a terminal shows it.
type asking struct {
	stdout, stderr bytes.Buffer
	asked          []string
	terminalRead   bool
}

func (a *asking) asker(asJSON, yes, terminal, answer bool) Asker {
	return Asker{JSON: asJSON, Yes: yes, Stdout: &a.stdout, Stderr: &a.stderr,
		Terminal: func() bool { a.terminalRead = true; return terminal },
		Confirm: func(words io.Writer, q string) (bool, error) {
			a.asked = append(a.asked, q)
			io.WriteString(words, q+" [y/N]\n")
			return answer, nil
		}}
}

func TestAskAsksForSeveralNamesOrAlwaysAndNotUnderYes(t *testing.T) {
	one, two := []string{"r1"}, []string{"r1", "r2"}
	for _, c := range []struct {
		name      string
		names     []string
		always    bool
		yes       bool
		wantAsked bool
	}{
		{"one name", one, false, false, false},
		{"one name, always", one, true, false, true},
		{"several names", two, false, false, true},
		{"several names, --yes", two, false, true, false},
		{"always, --yes", one, true, true, false},
	} {
		var a asking
		done, err := a.asker(false, c.yes, true, true).Ask(Question{Names: c.names, Noun: "router", Text: "Uninstall?", Always: c.always})
		if done || err != nil || (len(a.asked) == 1) != c.wantAsked || a.terminalRead != c.wantAsked {
			t.Errorf("%s: done %v, err %v, asked %q, terminal read %v", c.name, done, err, a.asked, a.terminalRead)
		}
	}
}

func TestAskWithoutATerminalNeedsYesAndSaysNothing(t *testing.T) {
	var a asking
	done, err := a.asker(true, false, false, true).Ask(Question{Names: []string{"r1", "r2"}, Noun: "router", Text: "Uninstall?", Listed: true})
	if done || !errors.Is(err, ErrNeedsYes) || err.Error() != "confirmation needed: pass --yes to act on 2 routers without a terminal" ||
		len(a.asked) != 0 || a.stdout.Len() != 0 || a.stderr.Len() != 0 {
		t.Errorf("done %v, err %v, asked %q, stdout %q, stderr %q", done, err, a.asked, a.stdout.String(), a.stderr.String())
	}
}

func TestAskListsTheNamesWhenListedThenAsksOnTheWordsStream(t *testing.T) {
	for _, asJSON := range []bool{false, true} {
		var a asking
		done, err := a.asker(asJSON, false, true, true).Ask(Question{Names: []string{"r/a", "r/b"}, Noun: "workload", Text: "Offboard 2 workloads?", Listed: true})
		words, other := &a.stdout, &a.stderr
		if asJSON {
			words, other = other, words
		}
		if done || err != nil || words.String() != "  r/a\n  r/b\nOffboard 2 workloads? [y/N]\n" || other.Len() != 0 {
			t.Errorf("--json %v: done %v, err %v, words %q, other %q", asJSON, done, err, words.String(), other.String())
		}
	}
	var a asking
	if _, err := a.asker(false, false, true, true).Ask(Question{Names: []string{"r/a", "r/b"}, Noun: "workload", Text: "Set?"}); err != nil || a.stdout.String() != "Set? [y/N]\n" {
		t.Errorf("not listed: %v %q", err, a.stdout.String())
	}
}

func TestAskDeclinedSaysNothingSentAndPrintsTheStopUnderJSON(t *testing.T) {
	var a asking
	done, err := a.asker(false, false, true, false).Ask(Question{Names: []string{"r1", "r2"}, Noun: "router", Text: "Uninstall?"})
	if !done || err != nil || a.stdout.String() != "Uninstall? [y/N]\nnot confirmed: nothing sent\n" || a.stderr.Len() != 0 {
		t.Errorf("declined: done %v, err %v, stdout %q, stderr %q", done, err, a.stdout.String(), a.stderr.String())
	}
	a = asking{}
	done, err = a.asker(true, false, true, false).Ask(Question{Names: []string{"r1"}, Noun: "router", Text: "Uninstall?", Always: true})
	if !done || err != nil || a.stderr.String() != "Uninstall? [y/N]\nnot confirmed: nothing sent\n" ||
		a.stdout.String() != "{\n  \"confirmed\": false,\n  \"targets\": [\n    \"r1\"\n  ]\n}\n" {
		t.Errorf("declined under --json: done %v, err %v, stdout %q, stderr %q", done, err, a.stdout.String(), a.stderr.String())
	}
}

func TestAskGivesTheTerminalsErrorAndSaysNothingMore(t *testing.T) {
	var o bytes.Buffer
	interrupted := errors.New("interrupted")
	done, err := Asker{Stdout: &o, Stderr: &o, Terminal: func() bool { return true },
		Confirm: func(io.Writer, string) (bool, error) { return false, interrupted }}.Ask(Question{Names: []string{"r1"}, Always: true})
	if done || err != interrupted || o.Len() != 0 {
		t.Errorf("done %v, err %v, out %q", done, err, o.String())
	}
}
