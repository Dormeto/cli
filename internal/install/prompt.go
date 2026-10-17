package install

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrNoTerminal is what Ask returns when there is no terminal to ask: the flags must give every answer.
var ErrNoTerminal = errors.New("no terminal to ask")

// Prompter asks the person running the install.
type Prompter interface {
	// Ask writes the question (with the suggestion in brackets when there is one) and reads a line: an empty line gives
	// the suggestion. The end of the input is io.EOF.
	Ask(question, suggestion string) (string, error)
	// Confirm is true only for y or yes, in any case.
	Confirm(question string) (bool, error)
}

type prompter struct {
	in       *bufio.Reader
	out      io.Writer
	terminal bool
}

// NewPrompter reads answers from in and writes questions to out. terminal false (a run without a terminal, CI): Ask
// returns ErrNoTerminal and Confirm false, neither writing anything.
func NewPrompter(in io.Reader, out io.Writer, terminal bool) Prompter {
	return &prompter{in: bufio.NewReader(in), out: out, terminal: terminal}
}

// line is the next line, trimmed; io.EOF only when nothing is left to read.
func (p *prompter) line() (string, error) {
	l, err := p.in.ReadString('\n')
	if err != nil && (err != io.EOF || l == "") {
		return "", err
	}
	return strings.TrimSpace(l), nil
}

func (p *prompter) Ask(question, suggestion string) (string, error) {
	if !p.terminal {
		return "", ErrNoTerminal
	}
	if suggestion != "" {
		fmt.Fprintf(p.out, "%s [%s]: ", question, suggestion)
	} else {
		fmt.Fprintf(p.out, "%s: ", question)
	}
	l, err := p.line()
	if err != nil {
		return "", err
	}
	if l == "" {
		return suggestion, nil
	}
	return l, nil
}

func (p *prompter) Confirm(question string) (bool, error) {
	if !p.terminal {
		return false, nil
	}
	fmt.Fprintf(p.out, "%s [y/N]: ", question)
	l, err := p.line()
	if err == io.EOF {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	l = strings.ToLower(l)
	return l == "y" || l == "yes", nil
}
