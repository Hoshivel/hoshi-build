// Package run executes external commands.
//
// Everything that shells out goes through the Runner interface so tests can
// assert on the command line. For a build tool the exact flags are the product,
// and `-trimpath` silently going missing is the kind of regression that only
// shows up months later on someone else's machine.
package run

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Cmd is one external command. Env holds additions to the parent environment,
// written `KEY=value`.
type Cmd struct {
	Dir  string
	Env  []string
	Name string
	Args []string
}

func (c Cmd) String() string {
	return strings.TrimSpace(strings.Join(append([]string{c.Name}, c.Args...), " "))
}

// Runner executes external commands.
type Runner interface {
	// Run streams the command's output to the user.
	Run(ctx context.Context, c Cmd) error
	// Capture returns trimmed stdout and discards stderr.
	Capture(ctx context.Context, c Cmd) (string, error)
	// Look resolves an executable, like exec.LookPath.
	Look(name string) (string, error)
}

// ExecRunner is the real Runner.
type ExecRunner struct {
	Stdout io.Writer
	Stderr io.Writer
}

// NewExecRunner returns a Runner wired to the process's own streams.
func NewExecRunner() *ExecRunner {
	return &ExecRunner{Stdout: os.Stdout, Stderr: os.Stderr}
}

func (r *ExecRunner) command(ctx context.Context, c Cmd) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	cmd.Dir = c.Dir
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	return cmd
}

func (r *ExecRunner) Run(ctx context.Context, c Cmd) error {
	cmd := r.command(ctx, c)
	cmd.Stdout = r.Stdout
	cmd.Stderr = r.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("`%s` 失敗：%w", c, err)
	}
	return nil
}

func (r *ExecRunner) Capture(ctx context.Context, c Cmd) (string, error) {
	cmd := r.command(ctx, c)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("`%s` 失敗：%w", c, err)
	}
	return strings.TrimSpace(out.String()), nil
}

func (r *ExecRunner) Look(name string) (string, error) {
	return exec.LookPath(name)
}

// CaptureLines is Capture split into non-empty lines — the shape `gofmt -l`
// wants, where the answer is a file list and an empty list means success.
func CaptureLines(ctx context.Context, r Runner, c Cmd) ([]string, error) {
	out, err := r.Capture(ctx, c)
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// RequireTool turns a missing executable into an instruction rather than
// "executable file not found in $PATH".
func RequireTool(r Runner, name, hint string) error {
	if _, err := r.Look(name); err != nil {
		return fmt.Errorf("找不到 %s —— %s", name, hint)
	}
	return nil
}

// Hints for the two toolchains, so the wording stays the same everywhere.
const (
	HintGo  = "請安裝 Go 1.24+"
	HintNpm = "請安裝 Node.js 22+"
)
