// Package output is what the commands of both subtrees, `dormeto aws …` and `dormeto gcp …`, print alike: where their
// words go (stdout, or stderr under --json, whose stdout holds the JSON alone), their JSON, the question asked before a
// write (--yes skips it and is needed without a terminal) with its decline, and the JSON of a write that stopped before
// sending anything. It knows no cloud: each subtree passes its flags, its streams and its terminal.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Words is where a command says what it does and asks its questions: stdout, or stderr under its --json, whose stdout
// holds the JSON alone.
func Words(asJSON bool, stdout, stderr io.Writer) io.Writer {
	if asJSON {
		return stderr
	}
	return stdout
}

// JSON prints v on w as the commands print their JSON: indented by two spaces, a line feed after it (a raw answer
// indented too).
func JSON(w io.Writer, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", b)
	return err
}

// Stop prints on stdout, under --json, why nothing was sent and to what: {<key>: <value>, "targets": [names]}, [] for
// none ({"confirmed": false, …} for a question declined, {"dryRun": true, …} for a dry run).
func Stop(stdout io.Writer, asJSON bool, key string, value bool, names []string) error {
	if !asJSON {
		return nil
	}
	return JSON(stdout, map[string]any{key: value, "targets": EmptyIfNil(names)})
}

// EmptyIfNil is s, or an empty list when nil: JSON's [] rather than null.
func EmptyIfNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// NotConfirmed is the line of a question declined: nothing is sent, exit 0.
const NotConfirmed = "not confirmed: nothing sent"

// ErrNeedsYes is a question that cannot be asked, stdin not being a terminal: --yes acts without asking. Both subtrees
// end with exit 2 on it.
var ErrNeedsYes = errors.New("confirmation needed")

// NeedsYes is ErrNeedsYes for n targets, each a noun: "confirmation needed: pass --yes to act on 2 workloads without a
// terminal".
func NeedsYes(n int, noun string) error {
	if n != 1 {
		noun += "s"
	}
	return fmt.Errorf("%w: pass --yes to act on %d %s without a terminal", ErrNeedsYes, n, noun)
}

// Asker asks the questions of a command: its --json and --yes, its streams, whether stdin is a terminal (read only when
// a question is to be asked) and how a question is asked there (on words, the words' stream; true for a yes).
type Asker struct {
	JSON, Yes      bool
	Stdout, Stderr io.Writer
	Terminal       func() bool
	Confirm        func(words io.Writer, question string) (bool, error)
}

// Question is asked before a write acts on Names, each a Noun ("router", "workload"): Text as it is written (it names the
// targets and what happens), for several names, or for one too when Always; the names listed first, each on its line,
// indented, when Listed.
type Question struct {
	Names          []string
	Noun, Text     string
	Always, Listed bool
}

// Ask asks q on the terminal: --yes skips it, and it is refused without a terminal (NeedsYes). done is true when it is
// declined: NotConfirmed said on the words' stream and, under --json, {"confirmed": false, "targets": [names]} printed on
// stdout; nothing is sent.
func (a Asker) Ask(q Question) (done bool, err error) {
	n := len(q.Names)
	if (n < 2 && !q.Always) || a.Yes {
		return false, nil
	}
	if !a.Terminal() {
		return false, NeedsYes(n, q.Noun)
	}
	words := Words(a.JSON, a.Stdout, a.Stderr)
	if q.Listed {
		for _, name := range q.Names {
			fmt.Fprintf(words, "  %s\n", name)
		}
	}
	ok, err := a.Confirm(words, q.Text)
	if err != nil || ok {
		return false, err
	}
	fmt.Fprintln(words, NotConfirmed)
	return true, Stop(a.Stdout, a.JSON, "confirmed", false, q.Names)
}
