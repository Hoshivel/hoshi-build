// Package build turns a parsed .hoshi-build config into artifacts.
//
// The shape of what it produces follows the organisation's
// engineering/deployment.md: a backend is one statically linked executable, a
// frontend is a directory of static files, and an artifact only becomes a
// directory when something genuinely has to travel next to the executable.
package build

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/run"
	"github.com/hoshivel/hoshi-build/internal/ui"
)

// Options are the per-invocation overrides on top of the config file.
// A zero value in an override field means "use the config".
type Options struct {
	Targets []config.Target
	Output  string
	Version string
	Archive string
	Clean   bool
	SkipGo  bool
	SkipNpm bool
	Verify  bool
}

// Result reports what a build produced, for the caller to summarise.
type Result struct {
	Version   string
	Artifacts []Artifact
}

// Artifact is one thing that landed in the output directory.
type Artifact struct {
	Path    string        // absolute
	Target  config.Target // zero value for type: npm
	IsDir   bool
	Archive string // absolute path of the archive, when one was made
	Verify  VerifyResult
}

type builder struct {
	cfg    *config.Config
	ui     *ui.Printer
	runner run.Runner
	opts   Options
	out    string // absolute output directory
}

// Run executes a build.
func Run(ctx context.Context, cfg *config.Config, p *ui.Printer, r run.Runner, opts Options) (*Result, error) {
	b := &builder{cfg: cfg, ui: p, runner: r, opts: opts}

	outRel := cfg.Output
	if opts.Output != "" {
		outRel = opts.Output
	}
	b.out = filepath.Join(cfg.Root, filepath.FromSlash(outRel))
	if !withinRoot(cfg.Root, b.out) {
		return nil, fmt.Errorf("輸出目錄 %q 在倉庫之外", outRel)
	}

	targets, err := b.resolveTargets(ctx)
	if err != nil {
		return nil, err
	}
	version := resolveVersion(ctx, r, cfg.Root, firstNonEmpty(opts.Version, cfg.Version))

	b.ui.Title("%s %s（%s）", cfg.Name, version, cfg.Type)
	b.ui.Note("輸出：%s", rel(cfg.Root, b.out))
	if len(targets) > 0 {
		b.ui.Note("目標：%s", joinTargets(targets))
	}

	if opts.Clean {
		b.ui.Step("清空 %s", rel(cfg.Root, b.out))
		if err := os.RemoveAll(b.out); err != nil {
			return nil, err
		}
	}
	if err := os.MkdirAll(b.out, 0o755); err != nil {
		return nil, err
	}

	// The frontend goes first: for a go-npm artifact the Go phase assembles the
	// staging directory around it, and a stale web/ is worse than a missing one.
	npmRan := false
	if cfg.BuildsNpm() && !opts.SkipNpm {
		b.ui.Title("前端")
		if err := b.buildNpm(ctx); err != nil {
			return nil, err
		}
		npmRan = true
	} else if cfg.BuildsNpm() {
		b.ui.Note("--skip-npm：沿用既有的 %s", cfg.Npm.Output)
	}

	result := &Result{Version: version}

	switch cfg.Type {
	case config.TypeNpm:
		art, err := b.collectStatic()
		if err != nil {
			return nil, err
		}
		result.Artifacts = append(result.Artifacts, *art)

	default:
		if opts.SkipGo {
			b.ui.Note("--skip-go：跳過後端")
			break
		}
		if err := run.RequireTool(r, "go", run.HintGo); err != nil {
			return nil, err
		}
		b.ui.Title("後端")
		for _, t := range targets {
			art, err := b.buildTarget(ctx, t, version, npmRan)
			if err != nil {
				return nil, err
			}
			result.Artifacts = append(result.Artifacts, *art)
		}
	}

	if err := b.archiveAll(result); err != nil {
		return nil, err
	}
	return result, nil
}

// resolveTargets settles which platforms to build for.
func (b *builder) resolveTargets(ctx context.Context) ([]config.Target, error) {
	if !b.cfg.BuildsGo() {
		return nil, nil
	}
	if len(b.opts.Targets) > 0 {
		return b.opts.Targets, nil
	}
	if len(b.cfg.Targets) > 0 {
		return b.cfg.Targets, nil
	}
	if err := run.RequireTool(b.runner, "go", run.HintGo); err != nil {
		return nil, err
	}
	host, err := hostTarget(ctx, b.runner)
	if err != nil {
		return nil, err
	}
	return []config.Target{host}, nil
}

// buildTarget produces one target's artifact, file or directory.
func (b *builder) buildTarget(ctx context.Context, t config.Target, version string, npmRan bool) (*Artifact, error) {
	name := b.cfg.ArtifactName(t)
	art := &Artifact{Target: t, IsDir: b.cfg.NeedsDir()}

	var binPath string
	if art.IsDir {
		art.Path = filepath.Join(b.out, name)
		if err := os.RemoveAll(art.Path); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(art.Path, 0o755); err != nil {
			return nil, err
		}
		binPath = filepath.Join(art.Path, b.cfg.BinaryName(t))
	} else {
		binPath = filepath.Join(b.out, name)
		if t.IsWindows() {
			binPath += ".exe"
		}
		art.Path = binPath
	}

	b.ui.Step("go build %s", t)
	if err := b.buildGo(ctx, t, binPath, version); err != nil {
		return nil, err
	}

	if b.opts.Verify {
		res, err := verifyStatic(binPath)
		if err != nil {
			return nil, fmt.Errorf("%s：%w", rel(b.cfg.Root, binPath), err)
		}
		art.Verify = res
	}

	if art.IsDir {
		if err := b.assemble(art.Path, npmRan); err != nil {
			return nil, err
		}
	}

	size, err := dirSize(art.Path)
	if err != nil {
		return nil, err
	}
	b.ui.OK("%s（%s）%s", name, humanSize(size), verifySuffix(b.opts.Verify, art.Verify))
	return art, nil
}

// assemble copies the frontend and the `include` entries into a directory
// artifact.
func (b *builder) assemble(stage string, npmRan bool) error {
	if b.cfg.Type == config.TypeGoNpm {
		src := b.npmOutput()
		if _, err := os.Stat(src); err == nil {
			if err := copyTree(src, filepath.Join(stage, b.cfg.Npm.WebDir)); err != nil {
				return fmt.Errorf("複製前端產物失敗：%w", err)
			}
		} else if npmRan {
			// buildNpm already proved the directory exists, so losing it here
			// means something removed it mid-build.
			return fmt.Errorf("前端產物 %s 不見了", rel(b.cfg.Root, src))
		} else {
			b.ui.Warn("沒有 %s，這個產物不含前端（--skip-npm）", rel(b.cfg.Root, src))
		}
	}

	for _, inc := range b.cfg.Include {
		src := filepath.Join(b.cfg.Root, filepath.FromSlash(inc))
		if !withinRoot(b.cfg.Root, src) {
			return fmt.Errorf("include %q 在倉庫之外", inc)
		}
		if _, err := os.Stat(src); err != nil {
			// Declared and missing is a real problem: the artifact would ship
			// without something the config says it needs, and nothing later
			// would notice.
			return fmt.Errorf("include %q 不存在", inc)
		}
		if err := copyTree(src, filepath.Join(stage, filepath.Base(inc))); err != nil {
			return fmt.Errorf("複製 %s 失敗：%w", inc, err)
		}
	}
	return nil
}

// collectStatic handles `type: npm`, whose artifact is the static bundle
// itself. When the npm script already writes into the output directory — the
// common case, since deployment.md §2 asks for dist/ on both ends — there is
// nothing to copy.
func (b *builder) collectStatic() (*Artifact, error) {
	src := b.npmOutput()
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("找不到前端產物 %s", rel(b.cfg.Root, src))
	}

	if filepath.Clean(src) != filepath.Clean(b.out) {
		if err := copyTree(src, b.out); err != nil {
			return nil, err
		}
		b.ui.OK("靜態產物 → %s", rel(b.cfg.Root, b.out))
	} else {
		b.ui.Note("npm 直接輸出到 %s，不必複製", rel(b.cfg.Root, b.out))
	}

	size, err := dirSize(b.out)
	if err != nil {
		return nil, err
	}
	b.ui.OK("%s（%s）", rel(b.cfg.Root, b.out), humanSize(size))
	return &Artifact{Path: b.out, IsDir: true}, nil
}

// archiveAll packs each artifact when `archive` asks for it.
func (b *builder) archiveAll(result *Result) error {
	format := firstNonEmpty(b.opts.Archive, b.cfg.Archive)
	if format == "" || format == config.ArchiveNone {
		return nil
	}

	b.ui.Title("封裝（%s）", format)
	for i := range result.Artifacts {
		art := &result.Artifacts[i]

		base := b.cfg.Name + "-" + result.Version
		if art.Target != (config.Target{}) {
			base += "-" + art.Target.Slug()
		}
		dst := filepath.Join(b.out, base+archiveExt(format))

		// A directory artifact keeps its name inside the archive so unpacking
		// never scatters files into the current directory. A single executable
		// has nothing to scatter, so it sits at the archive root.
		prefix := ""
		if art.IsDir {
			prefix = filepath.Base(art.Path)
		}
		if err := makeArchive(format, art.Path, dst, prefix); err != nil {
			return err
		}
		art.Archive = dst

		size, err := dirSize(dst)
		if err != nil {
			return err
		}
		b.ui.OK("%s（%s）", filepath.Base(dst), humanSize(size))
	}
	return nil
}

func verifySuffix(enabled bool, res VerifyResult) string {
	switch {
	case !enabled:
		return ""
	case res.Checked:
		return "— " + res.Detail
	case res.Reason != "":
		return "— " + res.Reason
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func joinTargets(targets []config.Target) string {
	out := make([]string, len(targets))
	for i, t := range targets {
		out[i] = t.String()
	}
	return strings.Join(out, "、")
}
