package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hoshivel/hoshi-build/internal/npmdeps"
	"github.com/hoshivel/hoshi-build/internal/run"
)

// npmDir is the absolute directory holding package.json.
func (b *builder) npmDir() string {
	return filepath.Join(b.cfg.Root, filepath.FromSlash(b.cfg.Npm.Dir))
}

// npmOutput is the absolute directory the npm script writes into.
func (b *builder) npmOutput() string {
	return filepath.Join(b.npmDir(), filepath.FromSlash(b.cfg.Npm.Output))
}

// installDeps shares the install policy used by test, dev and setup.
func (b *builder) installDeps(ctx context.Context) error {
	return npmdeps.Ensure(ctx, b.cfg, b.ui, b.runner, false)
}

// buildNpm runs the configured npm script and confirms it produced something.
func (b *builder) buildNpm(ctx context.Context) error {
	if err := run.RequireTool(b.runner, "npm", run.HintNpm); err != nil {
		return err
	}

	dir := b.npmDir()
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		return fmt.Errorf("%s 沒有 package.json（npm.dir = %q）", rel(b.cfg.Root, dir), b.cfg.Npm.Dir)
	}

	if err := b.installDeps(ctx); err != nil {
		return err
	}

	b.ui.Step("npm run %s", b.cfg.Npm.Script)
	cmd := run.Cmd{Dir: dir, Name: "npm", Args: []string{"run", b.cfg.Npm.Script}}
	b.ui.Command(rel(b.cfg.Root, dir), nil, cmd.Name, cmd.Args)
	if err := b.runner.Run(ctx, cmd); err != nil {
		return err
	}

	out := b.npmOutput()
	st, err := os.Stat(out)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("`npm run %s` 沒有產生 %s（npm.output = %q）",
			b.cfg.Npm.Script, rel(b.cfg.Root, out), b.cfg.Npm.Output)
	}

	b.ui.OK("前端產物：%s", rel(b.cfg.Root, out))
	return nil
}
