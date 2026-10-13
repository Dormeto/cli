package cli

// The terminal's progress line (Ruling P15): each phase's progress on one line rewritten in place, only when stdout
// is a terminal; elsewhere (piped, redirected, CI) a line per account or region and the minute's "still reading"
// lines (internal/progress).
//
// Ports manager packages/estimate/src/bin.ts (the status given to main when process.stdout.isTTY).

import (
	"io"
	"io/fs"
	"os"
	"runtime"

	"github.com/Dormeto/cli/internal/progress"
)

// terminal writes a Status to a terminal.
type terminal struct{ w io.Writer }

// Ruling P18: the line is drawn with autowrap off (DECAWM reset, then set again), so a line wider than the terminal
// is clipped at its last column, never wrapped: "\x1b[2K" erases the cursor's row only, and a wrapped line would leave
// its first rows behind at each redraw.
const (
	rewrite = "\r\x1b[2K\x1b[?7l" // back to the line's start, the line erased, autowrap off
	drawn   = "\x1b[?7h"          // autowrap on again
)

// Update rewrites the line: back to its start, the line erased, the new one, clipped to the terminal's width.
func (t terminal) Update(line string) { _, _ = io.WriteString(t.w, rewrite+line+drawn) }

// End rewrites the line a last time and leaves it.
func (t terminal) End(line string) { _, _ = io.WriteString(t.w, rewrite+line+drawn+"\n") }

// Terminal is the Status of a terminal written to w: Update writes "\r\x1b[2K\x1b[?7l", the line and "\x1b[?7h"
// (Node's sequence, autowrap off while the line is drawn: Ruling P18), End the same and a newline. It only writes: it
// never calls back into the view that calls it.
func Terminal(w io.Writer) progress.Status { return terminal{w} }

// isTerminal says a file of that mode is a terminal the line can be rewritten on: a character device (as
// process.stdout.isTTY, without golang.org/x/term), not on Windows, whose consoles Go writes the escape codes to as
// they are (Review focus 3: a line per account or region there).
func isTerminal(mode fs.FileMode, goos string) bool {
	return mode&fs.ModeCharDevice != 0 && goos != "windows"
}

// StatusOf is the Status of out when it is a terminal (the command's stdout), else nil: a line per account or region.
func StatusOf(out io.Writer) progress.Status {
	f, ok := out.(*os.File)
	if !ok {
		return nil
	}
	fi, err := f.Stat()
	if err != nil || !isTerminal(fi.Mode(), runtime.GOOS) {
		return nil
	}
	return Terminal(f)
}
