// Package ui prints build progress.
//
// A build tool is read while something else is happening, so the output is
// shaped for skimming: one line per step, the failure never colour-only.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// Printer writes progress to a stream. The zero value is not usable; call New.
//
// It is safe for concurrent use: `hoshi dev` streams several child processes
// through one Printer, two goroutines per process. Without the lock their
// writes interleave mid-line, which is worst exactly when the output matters —
// a stack trace arriving while another process logs a request.
type Printer struct {
	mu      sync.Mutex
	out     io.Writer
	err     io.Writer
	colour  bool
	Verbose bool
}

// New builds a Printer for the given streams. Colour is enabled only for a
// terminal, and NO_COLOR turns it off unconditionally (https://no-color.org).
func New(out, errw io.Writer, verbose bool) *Printer {
	return &Printer{out: out, err: errw, colour: wantColour(out), Verbose: verbose}
}

func wantColour(w io.Writer) bool {
	if _, set := os.LookupEnv("NO_COLOR"); set {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
)

func (p *Printer) paint(code, s string) string {
	if !p.colour {
		return s
	}
	return code + s + reset
}

// Title starts a phase.
func (p *Printer) Title(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()

	fmt.Fprintf(p.out, "\n%s\n", p.paint(bold, fmt.Sprintf(format, args...)))
}

// Step reports work about to happen.
func (p *Printer) Step(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()

	fmt.Fprintf(p.out, "  %s %s\n", p.paint(cyan, "→"), fmt.Sprintf(format, args...))
}

// OK reports work that finished.
func (p *Printer) OK(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()

	fmt.Fprintf(p.out, "  %s %s\n", p.paint(green, "✓"), fmt.Sprintf(format, args...))
}

// Note reports something worth knowing that is not a problem.
func (p *Printer) Note(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()

	fmt.Fprintf(p.out, "  %s %s\n", p.paint(dim, "·"), p.paint(dim, fmt.Sprintf(format, args...)))
}

// Warn reports something that did not fail the build but might be a mistake.
func (p *Printer) Warn(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()

	fmt.Fprintf(p.err, "  %s %s\n", p.paint(yellow, "!"), fmt.Sprintf(format, args...))
}

// Error reports a failure. The marker is a word, not only a colour, because
// build logs are read in CI where colour is stripped.
func (p *Printer) Error(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()

	fmt.Fprintf(p.err, "%s %s\n", p.paint(red, "錯誤："), fmt.Sprintf(format, args...))
}

// Command echoes a command line, only under --verbose.
func (p *Printer) Command(dir string, env []string, name string, args []string) {
	if !p.Verbose {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	var b strings.Builder
	if dir != "" {
		fmt.Fprintf(&b, "(cd %s) ", dir)
	}
	for _, kv := range env {
		b.WriteString(kv)
		b.WriteByte(' ')
	}
	b.WriteString(name)
	for _, a := range args {
		b.WriteByte(' ')
		b.WriteString(a)
	}
	fmt.Fprintf(p.out, "    %s\n", p.paint(dim, b.String()))
}

// procColours cycle per dev process so each one's lines stay recognisable.
var procColours = []string{cyan, "\033[35m", "\033[34m", yellow, green, "\033[31m"}

// Prefixed writes one line of a child process's output, tagged with its name.
//
// The tag is padded to a common width so the actual output stays aligned and
// stays readable as a column, which is the whole point of running several
// processes in one terminal.
func (p *Printer) Prefixed(name string, colour int, line string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	tag := fmt.Sprintf("%-8s", truncate(name, 8))
	if p.colour {
		tag = procColours[colour%len(procColours)] + tag + reset
	}
	fmt.Fprintf(p.out, "%s │ %s\n", tag, line)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// Plain writes a line with no decoration.
func (p *Printer) Plain(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()

	fmt.Fprintf(p.out, "%s\n", fmt.Sprintf(format, args...))
}
