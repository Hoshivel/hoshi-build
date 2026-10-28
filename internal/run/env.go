package run

import (
	"context"
	"strings"
)

// WithEnv applies fixed process-local values to every command in one build.
// It never changes os.Environ or the caller's Runner.
func WithEnv(r Runner, env []string) Runner {
	return &envRunner{runner: r, env: append([]string(nil), env...)}
}

type envRunner struct {
	runner Runner
	env    []string
}

func (r *envRunner) command(c Cmd) Cmd {
	env := make([]string, 0, len(c.Env)+len(r.env))
	for _, current := range c.Env {
		key, _, _ := strings.Cut(current, "=")
		replaced := false
		for _, fixed := range r.env {
			fixedKey, _, _ := strings.Cut(fixed, "=")
			if strings.EqualFold(key, fixedKey) {
				replaced = true
				break
			}
		}
		if !replaced {
			env = append(env, current)
		}
	}
	c.Env = append(env, r.env...)
	return c
}

func (r *envRunner) Run(ctx context.Context, c Cmd) error {
	return r.runner.Run(ctx, r.command(c))
}

func (r *envRunner) Capture(ctx context.Context, c Cmd) (string, error) {
	return r.runner.Capture(ctx, r.command(c))
}

func (r *envRunner) Look(name string) (string, error) { return r.runner.Look(name) }
