package cli

// The terminal's Status (Ruling P15): the progress line rewritten in place with Node's sequence, only on a terminal.

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ruling P18: autowrap is off while the line is drawn (\x1b[?7l, then \x1b[?7h), so a line wider than the terminal
// is clipped, never wrapped: \x1b[2K erases one row, a wrapped line would leave its first rows behind at each redraw.
func TestATerminalsLineIsRewrittenInPlaceClippedNeverWrapped(t *testing.T) {
	var out bytes.Buffer
	s := Terminal(&out)
	s.Update("  Discovery 1")
	s.Update("  Discovery 2")
	s.End("  Discovery 3")
	want := "\r\x1b[2K\x1b[?7l  Discovery 1\x1b[?7h" +
		"\r\x1b[2K\x1b[?7l  Discovery 2\x1b[?7h" +
		"\r\x1b[2K\x1b[?7l  Discovery 3\x1b[?7h\n"
	if out.String() != want {
		t.Fatalf("%q, want %q", out.String(), want)
	}
}

func TestOnlyATerminalGetsTheStatusNeverAPipeAFileOrAWindowsConsole(t *testing.T) {
	cases := []struct {
		mode fs.FileMode
		goos string
		want bool
	}{
		{fs.ModeDevice | fs.ModeCharDevice | 0o620, "darwin", true},
		{fs.ModeDevice | fs.ModeCharDevice | 0o620, "linux", true},
		// A Windows console: Go writes the escape codes as they are; a line per account or region instead.
		{fs.ModeDevice | fs.ModeCharDevice, "windows", false},
		{fs.ModeNamedPipe | 0o600, "linux", false},
		{0o644, "darwin", false},
	}
	for _, c := range cases {
		if got := isTerminal(c.mode, c.goos); got != c.want {
			t.Errorf("%v on %s: %v", c.mode, c.goos, got)
		}
	}
	// What the command is given: a pipe, a file, a writer that is no file.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for name, out := range map[string]any{"a pipe": w, "a file": f, "a buffer": &bytes.Buffer{}} {
		if s := StatusOf(out.(interface{ Write([]byte) (int, error) })); s != nil {
			t.Errorf("%s has a Status", name)
		}
	}
}

func TestARunOnATerminalRewritesEachPhasesLineAndLeavesItsLastOne(t *testing.T) {
	e, _ := fakeAWS(fakeOptions{})
	r := newRun(t, e)
	var screen bytes.Buffer
	r.deps.Status = Terminal(&screen)
	if code := Main([]string{"--profile", "p", "--uptime", "--min-saving", "none", "--out", t.TempDir()}, r.deps); code != 0 {
		t.Fatalf("exit code %d: %q", code, r.errors())
	}
	for _, last := range []string{
		"\r\x1b[2K\x1b[?7l  Discovery [████████████████████] 100% · 1/1 account · 1 region · 1 workload · 0:00\x1b[?7h\n",
		"\r\x1b[2K\x1b[?7l  Requests [████████████████████] 100% · 1/1 region · 1 workload · 0:00\x1b[?7h\n",
		"\r\x1b[2K\x1b[?7l  Uptime [████████████████████] 100% · 1/1 region · 1 workload · 0:00\x1b[?7h\n",
	} {
		if !strings.Contains(screen.String(), last) {
			t.Errorf("no %q in %q", last, screen.String())
		}
	}
	if !strings.HasSuffix(screen.String(), "\n") || strings.Count(screen.String(), "\n") != 3 {
		t.Errorf("each phase's line not left once: %q", screen.String())
	}
	for _, l := range r.logged() {
		if strings.HasPrefix(l, "  discovery ") || strings.HasPrefix(l, "  requests ") || strings.HasPrefix(l, "  uptime ") || strings.Contains(l, "\x1b") {
			t.Errorf("logged on a terminal: %q", l)
		}
	}
}
