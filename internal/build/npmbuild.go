package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hoshivel/hoshi-build/internal/config"
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

// installDeps brings node_modules up before the build script runs.
//
// `npm ci` is preferred whenever a lockfile exists: it installs exactly what is
// locked and fails if package.json and the lockfile disagree, which is the
// property a build wants. `npm install` may quietly resolve a new version, and
// a build that changes its own inputs is not a build.
func (b *builder) installDeps(ctx context.Context) error {
	dir := b.npmDir()

	switch b.cfg.Npm.Install {
	case config.InstallNever:
		b.ui.Note("npm.install=never，跳過安裝相依")
		return nil
	case config.InstallAuto:
		if _, err := os.Stat(filepath.Join(dir, "node_modules")); err == nil {
			b.ui.Note("node_modules 已存在，跳過安裝（npm.install=auto）")
			return nil
		}
	}

	args := []string{"install"}
	if _, err := os.Stat(filepath.Join(dir, "package-lock.json")); err == nil {
		args = []string{"ci"}
	}

	b.ui.Step("npm %s", args[0])
	cmd := run.Cmd{Dir: dir, Name: "npm", Args: args}
	b.ui.Command(rel(b.cfg.Root, dir), nil, cmd.Name, cmd.Args)
	return b.runner.Run(ctx, cmd)
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
