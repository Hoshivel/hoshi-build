package tasks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/npmdeps"
	"github.com/hoshivel/hoshi-build/internal/run"
	"github.com/hoshivel/hoshi-build/internal/ui"
)

// Fmt formats the Go sources. With check set it only reports, changing nothing
// — the shape CI wants.
func Fmt(ctx context.Context, c *config.Config, p *ui.Printer, r run.Runner, check bool) error {
	if !c.BuildsGo() {
		p.Note("沒有 Go 程式碼，沒有東西要格式化")
		return nil
	}
	if err := run.RequireTool(r, "gofmt", run.HintGo); err != nil {
		return err
	}

	dir := filepath.Join(c.Root, filepath.FromSlash(c.Go.Dir))
	listed, err := run.CaptureLines(ctx, r, run.Cmd{Dir: dir, Name: "gofmt", Args: []string{"-l", "."}})
	if err != nil {
		return err
	}

	if len(listed) == 0 {
		p.OK("gofmt clean，沒有東西要改")
		return nil
	}
	for _, f := range listed {
		p.Note("%s", f)
	}
	if check {
		return fmt.Errorf("%d 個檔案未格式化（拿掉 -check 即可修正）", len(listed))
	}

	p.Step("gofmt -w（%d 個檔案）", len(listed))
	if err := r.Run(ctx, run.Cmd{Dir: dir, Name: "gofmt", Args: []string{"-w", "."}}); err != nil {
		return err
	}
	p.OK("已格式化 %d 個檔案", len(listed))
	return nil
}

// CleanOptions selects how much `hoshi clean` removes.
type CleanOptions struct {
	Deps   bool // node_modules
	Caches bool // go build/test cache
	All    bool // both
}

// Clean removes build output, and optionally dependencies and caches.
func Clean(ctx context.Context, c *config.Config, p *ui.Printer, r run.Runner, opts CleanOptions) error {
	if opts.All {
		opts.Deps, opts.Caches = true, true
	}

	targets := []string{c.Output}
	if c.BuildsNpm() {
		targets = append(targets, path(c.Npm.Dir, c.Npm.Output))
	}
	targets = append(targets, c.Clean.Extra...)
	if opts.Deps && c.BuildsNpm() {
		targets = append(targets, path(c.Npm.Dir, "node_modules"))
	}

	removed := 0
	for _, rel := range targets {
		abs := filepath.Join(c.Root, filepath.FromSlash(rel))
		// validate already refused absolute paths and `..`, but this deletes a
		// directory tree, so it is worth being sure twice.
		if !withinRoot(c.Root, abs) || filepath.Clean(abs) == filepath.Clean(c.Root) {
			return fmt.Errorf("拒絕刪除 %q：它不是倉庫底下的子目錄", rel)
		}
		if !exists(abs) {
			continue
		}
		if err := os.RemoveAll(abs); err != nil {
			return err
		}
		p.OK("已刪除 %s", rel)
		removed++
	}

	if opts.Caches && c.BuildsGo() {
		p.Step("go clean -cache -testcache")
		dir := filepath.Join(c.Root, filepath.FromSlash(c.Go.Dir))
		if err := r.Run(ctx, run.Cmd{
			Dir: dir, Name: "go", Args: []string{"clean", "-cache", "-testcache"},
		}); err != nil {
			return err
		}
		p.OK("已清空 Go 快取")
		removed++
	}

	if removed == 0 {
		p.Note("沒有東西要刪")
	}
	return nil
}

// Setup installs the dependencies a fresh clone needs.
func Setup(ctx context.Context, c *config.Config, p *ui.Printer, r run.Runner) error {
	if c.BuildsGo() {
		p.Title("後端相依（Go）")
		if err := run.RequireTool(r, "go", run.HintGo); err != nil {
			return err
		}
		dir := filepath.Join(c.Root, filepath.FromSlash(c.Go.Dir))
		p.Step("go mod download")
		if err := r.Run(ctx, run.Cmd{Dir: dir, Name: "go", Args: []string{"mod", "download"}}); err != nil {
			return err
		}
		p.OK("Go 相依就緒")
	}

	if c.BuildsNpm() {
		p.Title("前端相依（npm）")
		if err := run.RequireTool(r, "npm", run.HintNpm); err != nil {
			return err
		}
		if err := EnsureDeps(ctx, c, p, r, true); err != nil {
			return err
		}
	}
	return nil
}

// EnsureDeps brings node_modules up. With force set it installs regardless of
// `npm.install`, which is what `hoshi setup` means.
func EnsureDeps(ctx context.Context, c *config.Config, p *ui.Printer, r run.Runner, force bool) error {
	return npmdeps.Ensure(ctx, c, p, r, force)
}

func path(dir, sub string) string {
	if dir == "" || dir == "." {
		return sub
	}
	return strings.TrimSuffix(dir, "/") + "/" + sub
}

func withinRoot(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
