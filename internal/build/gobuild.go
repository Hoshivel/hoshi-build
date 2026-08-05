package build

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/run"
)

// goArgs builds the `go build` command line.
//
// The three mandatory pieces come from the organisation's deployment standard
// §1.2 and are not configurable:
//
//   - CGO_ENABLED=0  — the reason single-file deployment works at all. With cgo
//     the artifact links against the build machine's libc and deployment stops
//     being "copy one file".
//   - -trimpath      — reproducible builds, and no build-machine paths baked in.
//   - -ldflags "-s -w" — drop the symbol table and DWARF. Panic line numbers
//     survive both.
//
// `go.ldflags` is appended after `-s -w` rather than replacing it, so a repo
// cannot drop the standard flags by accident.
//
// `-mod=readonly` is passed explicitly rather than relied on as Go's default:
// a `GOFLAGS=-mod=mod` in the environment would otherwise let a build rewrite
// go.mod on its way through — silently upgrading a dependency and committing
// the result to whoever runs `git add` next. A build that changes its own
// inputs is not a build, which is the same reason npm dependencies go in
// through `npm ci` when a lockfile exists.
func goArgs(c *config.Config, t config.Target, outPath, version string) []string {
	args := []string{"build", "-mod=readonly", "-trimpath"}

	if len(c.Go.Tags) > 0 {
		args = append(args, "-tags", strings.Join(c.Go.Tags, ","))
	}

	ldflags := []string{"-s", "-w"}
	if c.Go.VersionVar != "" {
		ldflags = append(ldflags, "-X", c.Go.VersionVar+"="+version)
	}
	if extra := strings.TrimSpace(c.Go.Ldflags); extra != "" {
		ldflags = append(ldflags, extra)
	}
	args = append(args, "-ldflags", strings.Join(ldflags, " "))

	return append(args, "-o", outPath, c.Go.Package)
}

// goEnv is the environment overlay for one target.
func goEnv(t config.Target) []string {
	return []string{
		"CGO_ENABLED=0",
		"GOOS=" + t.OS,
		"GOARCH=" + t.Arch,
	}
}

// buildGo compiles one target and writes the executable to outPath.
func (b *builder) buildGo(ctx context.Context, t config.Target, outPath, version string) error {
	dir := filepath.Join(b.cfg.Root, filepath.FromSlash(b.cfg.Go.Dir))
	cmd := run.Cmd{
		Dir:  dir,
		Env:  goEnv(t),
		Name: "go",
		Args: goArgs(b.cfg, t, outPath, version),
	}
	b.ui.Command(rel(b.cfg.Root, dir), cmd.Env, cmd.Name, cmd.Args)
	return b.runner.Run(ctx, cmd)
}

// hostTarget asks the Go toolchain which platform it builds for by default.
//
// It asks `go env` rather than using this binary's own runtime.GOOS/GOARCH:
// the answer that matters is the toolchain's, and hoshi-build may well be
// running under emulation or from a differently-built binary. The resolved
// target is always printed, so there is nothing to guess about either way.
func hostTarget(ctx context.Context, r run.Runner) (config.Target, error) {
	out, err := r.Capture(ctx, run.Cmd{Name: "go", Args: []string{"env", "GOOS", "GOARCH"}})
	if err != nil {
		return config.Target{}, fmt.Errorf("問不到本機平臺：%w", err)
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return config.Target{}, fmt.Errorf("`go env GOOS GOARCH` 的回覆看不懂：%q", out)
	}
	return config.Target{OS: fields[0], Arch: fields[1]}, nil
}
