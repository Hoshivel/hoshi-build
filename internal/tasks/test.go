// Package tasks holds the everyday commands: test, fmt, clean and setup.
//
// These are the parts of a repository's own scripts that were never build
// logic — the ones each repository used to write again, slightly differently.
package tasks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/run"
	"github.com/hoshivel/hoshi-build/internal/ui"
)

// TestOptions are the per-invocation overrides for `hoshi test`.
type TestOptions struct {
	Race     bool // -race, forced on
	NoRace   bool // -no-race, forced off
	Short    bool
	Verbose  bool
	NoLint   bool
	Run      string
	Packages string
	Count    int
	OnlyGo   bool
	OnlyNpm  bool
}

// step is one thing that ran, for the summary.
type step struct {
	name string
	ok   bool
}

// Test runs the repository's verification: lint, build and test for Go, the
// configured scripts for npm, then any extra commands.
//
// It stops at the first failure. A summary that continues past a broken build
// reports a list of consequences, and the first line was the cause.
func Test(ctx context.Context, c *config.Config, p *ui.Printer, r run.Runner, opts TestOptions) error {
	started := time.Now()
	var steps []step

	record := func(name string, err error) error {
		steps = append(steps, step{name: name, ok: err == nil})
		if err != nil {
			summarise(p, steps, started)
		}
		return err
	}

	runGo := c.BuildsGo() && !opts.OnlyNpm
	runNpm := c.BuildsNpm() && !opts.OnlyGo

	if runGo {
		p.Title("後端（Go）")
		if err := run.RequireTool(r, "go", run.HintGo); err != nil {
			return err
		}
		dir := filepath.Join(c.Root, filepath.FromSlash(c.Go.Dir))

		if c.Test.LintEnabled() && !opts.NoLint {
			if err := record("gofmt", goFmtCheck(ctx, c, p, r, dir)); err != nil {
				return err
			}
			if err := record("go vet", goStep(ctx, p, r, dir, "vet", "./...")); err != nil {
				return err
			}
		}
		if err := record("go build", goStep(ctx, p, r, dir, "build", "./...")); err != nil {
			return err
		}
		if err := record("go test", goTest(ctx, c, p, r, dir, opts)); err != nil {
			return err
		}
	}

	if runNpm {
		p.Title("前端（npm）")
		if err := run.RequireTool(r, "npm", run.HintNpm); err != nil {
			return err
		}
		dir := filepath.Join(c.Root, filepath.FromSlash(c.Npm.Dir))
		if err := EnsureDeps(ctx, c, p, r, false); err != nil {
			return err
		}
		for _, script := range c.Test.Scripts {
			name := "npm run " + script
			p.Step("%s", name)
			cmd := run.Cmd{Dir: dir, Name: "npm", Args: []string{"run", script}}
			p.Command(relTo(c.Root, dir), nil, cmd.Name, cmd.Args)
			if err := record(name, r.Run(ctx, cmd)); err != nil {
				return err
			}
			p.OK("%s 通過", name)
		}
	}

	if len(c.Test.Commands) > 0 {
		p.Title("自訂步驟")
		for _, custom := range c.Test.Commands {
			name := custom.Name
			if name == "" {
				name = custom.Run.String()
			}
			p.Step("%s", name)
			dir := filepath.Join(c.Root, filepath.FromSlash(custom.Dir))
			cmd := run.Cmd{
				Dir:  dir,
				Env:  custom.Env,
				Name: custom.Run[0],
				Args: custom.Run[1:],
			}
			p.Command(relTo(c.Root, dir), cmd.Env, cmd.Name, cmd.Args)
			if err := record(name, r.Run(ctx, cmd)); err != nil {
				return err
			}
			p.OK("%s 通過", name)
		}
	}

	if len(steps) == 0 {
		p.Warn("沒有任何可跑的步驟")
		return nil
	}
	summarise(p, steps, started)
	return nil
}

// goFmtCheck fails when gofmt would change something.
//
// `gofmt -l` exits 0 whether or not it listed files, so the file list is the
// result — an exit code check here would pass on an unformatted tree.
func goFmtCheck(ctx context.Context, c *config.Config, p *ui.Printer, r run.Runner, dir string) error {
	p.Step("gofmt -l .")
	cmd := run.Cmd{Dir: dir, Name: "gofmt", Args: []string{"-l", "."}}
	p.Command(relTo(c.Root, dir), nil, cmd.Name, cmd.Args)

	files, err := run.CaptureLines(ctx, r, cmd)
	if err != nil {
		return err
	}
	if len(files) > 0 {
		for _, f := range files {
			p.Warn("未格式化：%s", f)
		}
		return fmt.Errorf("gofmt 有 %d 個檔案未格式化（跑 `hoshi fmt` 修正）", len(files))
	}
	p.OK("gofmt clean")
	return nil
}

func goStep(ctx context.Context, p *ui.Printer, r run.Runner, dir, verb string, args ...string) error {
	p.Step("go %s %s", verb, strings.Join(args, " "))
	if err := r.Run(ctx, run.Cmd{Dir: dir, Name: "go", Args: append([]string{verb}, args...)}); err != nil {
		return err
	}
	p.OK("go %s 通過", verb)
	return nil
}

// goTest assembles the `go test` invocation.
//
// -race needs cgo and a C toolchain. Where that is missing the run degrades to
// repeating the tests rather than pretending the race detector ran — a green
// "passed" that never checked anything is worse than a visible gap.
func goTest(ctx context.Context, c *config.Config, p *ui.Printer, r run.Runner, dir string, opts TestOptions) error {
	args := []string{"test"}

	wantRace := c.Test.RaceEnabled() || opts.Race
	if opts.NoRace {
		wantRace = false
	}
	count := opts.Count

	if wantRace {
		if raceSupported(ctx, r) {
			args = append(args, "-race")
		} else {
			if count < 2 {
				count = 2
			}
			p.Warn("-race 需要 cgo 與 C 編譯器，本機沒有；改跑 -count %d，"+
				"競態偵測「未」執行", count)
		}
	}
	if count > 1 {
		args = append(args, "-count", fmt.Sprint(count))
	}
	if opts.Short {
		args = append(args, "-short")
	}
	if opts.Verbose {
		args = append(args, "-v")
	}
	if opts.Run != "" {
		args = append(args, "-run", opts.Run)
	}
	args = append(args, c.Test.Flags...)

	pkgs := c.Test.Packages
	if opts.Packages != "" {
		pkgs = opts.Packages
	}
	args = append(args, pkgs)

	p.Step("go %s", strings.Join(args, " "))
	cmd := run.Cmd{Dir: dir, Name: "go", Args: args}
	p.Command(relTo(c.Root, dir), nil, cmd.Name, cmd.Args)
	if err := r.Run(ctx, cmd); err != nil {
		return err
	}
	p.OK("go test 通過")
	return nil
}

// raceSupported reports whether the race detector can actually run here.
func raceSupported(ctx context.Context, r run.Runner) bool {
	out, err := r.Capture(ctx, run.Cmd{Name: "go", Args: []string{"env", "CGO_ENABLED"}})
	if err != nil || strings.TrimSpace(out) != "1" {
		return false
	}
	cc, err := r.Capture(ctx, run.Cmd{Name: "go", Args: []string{"env", "CC"}})
	if err != nil || cc == "" {
		return false
	}
	_, err = r.Look(cc)
	return err == nil
}

func summarise(p *ui.Printer, steps []step, started time.Time) {
	p.Title("總結")
	failed := 0
	for _, s := range steps {
		if s.ok {
			p.OK("%s", s.name)
		} else {
			p.Warn("%s（失敗）", s.name)
			failed++
		}
	}
	elapsed := time.Since(started).Round(100 * time.Millisecond)
	if failed == 0 {
		p.Plain("")
		p.OK("全部通過（%s）", elapsed)
	}
}

func relTo(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(rel)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
