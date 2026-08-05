package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoshivel/hoshi-build/internal/build"
	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/dev"
	"github.com/hoshivel/hoshi-build/internal/run"
	"github.com/hoshivel/hoshi-build/internal/tasks"
	"github.com/hoshivel/hoshi-build/internal/ui"
)

// commonFlags are shared by every subcommand that reads a config.
type commonFlags struct {
	dir     string
	config  string
	verbose bool
}

func (f *commonFlags) bind(fs *flag.FlagSet) {
	fs.StringVar(&f.dir, "C", "", "先切換到這個目錄")
	fs.StringVar(&f.config, "config", "", "指定設定檔")
	fs.BoolVar(&f.verbose, "v", false, "印出實際執行的指令")
}

// load resolves the configuration these flags point at.
func (f *commonFlags) load() (*config.Config, *ui.Printer, error) {
	p := ui.New(os.Stdout, os.Stderr, f.verbose)

	if f.config != "" {
		cfg, err := config.Load(f.config)
		return cfg, p, err
	}

	start := f.dir
	if start == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, p, err
		}
		start = wd
	}
	cfg, err := config.LoadFrom(start)
	if err != nil {
		return nil, p, err
	}
	return cfg, p, nil
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	return fs
}

func cmdBuild(ctx context.Context, args []string) error {
	var (
		common  commonFlags
		targets string
		output  string
		setVer  string
		archive string
		pkg     bool
		clean   bool
		skipGo  bool
		skipNpm bool
		noVerif bool
	)

	fs := newFlagSet("build")
	common.bind(fs)
	fs.StringVar(&targets, "target", "", "覆寫 targets，逗號分隔")
	fs.StringVar(&output, "output", "", "覆寫 output")
	// Not `-version`: that would read as "print the version" to anyone who has
	// used another CLI, and `hoshi version` already means exactly that.
	fs.StringVar(&setVer, "set-version", "", "覆寫版本字串")
	fs.StringVar(&archive, "archive", "", "none / zip / tar.gz")
	fs.BoolVar(&pkg, "package", false, "每個目標另外壓一包（＝ -archive zip）")
	fs.BoolVar(&clean, "clean", false, "建置前清空輸出目錄")
	fs.BoolVar(&skipGo, "skip-go", false, "不建後端")
	fs.BoolVar(&skipNpm, "skip-npm", false, "不建前端")
	fs.BoolVar(&noVerif, "no-verify", false, "跳過靜態連結驗證")
	if err := fs.Parse(args); err != nil {
		return errAlreadyReported
	}

	cfg, p, err := common.load()
	if err != nil {
		return err
	}

	parsed, err := parseTargets(targets)
	if err != nil {
		return err
	}
	if skipGo && skipNpm {
		return fmt.Errorf("-skip-go 與 -skip-npm 同時給，沒有東西要建")
	}
	if pkg && archive == "" {
		archive = config.ArchiveZip
	}

	result, err := build.Run(ctx, cfg, p, run.NewExecRunner(), build.Options{
		Targets: parsed,
		Output:  output,
		Version: setVer,
		Archive: archive,
		Clean:   clean,
		SkipGo:  skipGo,
		SkipNpm: skipNpm,
		Verify:  !noVerif,
	})
	if err != nil {
		return err
	}

	p.Title("完成")
	for _, art := range result.Artifacts {
		p.OK("%s", relTo(cfg.Root, art.Path))
		if art.Archive != "" {
			p.OK("%s", relTo(cfg.Root, art.Archive))
		}
	}
	return nil
}

func cmdTest(ctx context.Context, args []string) error {
	var (
		common commonFlags
		opts   tasks.TestOptions
	)

	fs := newFlagSet("test")
	common.bind(fs)
	fs.BoolVar(&opts.Race, "race", false, "強制開啟競態偵測")
	fs.BoolVar(&opts.NoRace, "no-race", false, "強制關閉競態偵測")
	fs.BoolVar(&opts.Short, "short", false, "go test -short")
	fs.BoolVar(&opts.Verbose, "verbose", false, "go test -v")
	fs.BoolVar(&opts.NoLint, "no-lint", false, "跳過 gofmt 與 go vet")
	fs.StringVar(&opts.Run, "run", "", "go test -run 的樣式")
	fs.StringVar(&opts.Packages, "pkg", "", "限定套件範圍")
	fs.IntVar(&opts.Count, "count", 1, "go test -count")
	fs.BoolVar(&opts.OnlyGo, "go", false, "只跑後端")
	fs.BoolVar(&opts.OnlyNpm, "npm", false, "只跑前端")
	if err := fs.Parse(args); err != nil {
		return errAlreadyReported
	}

	if opts.Race && opts.NoRace {
		return fmt.Errorf("-race 與 -no-race 不能同時給")
	}
	if opts.OnlyGo && opts.OnlyNpm {
		return fmt.Errorf("-go 與 -npm 同時給，等於沒有限定")
	}

	cfg, p, err := common.load()
	if err != nil {
		return err
	}
	// `hoshi test -v` reads as "verbose tests"; the shared -v means "echo the
	// commands", and wanting one almost always means wanting the other.
	if common.verbose {
		opts.Verbose = true
	}
	return tasks.Test(ctx, cfg, p, run.NewExecRunner(), opts)
}

func cmdDev(ctx context.Context, args []string) error {
	var (
		common  commonFlags
		only    string
		extra   string
		opts    dev.Options
		dryRun  bool
		noCheck bool
		open    bool
	)

	fs := newFlagSet("dev")
	common.bind(fs)
	fs.BoolVar(&open, "open", false, "前端就緒後開啟瀏覽器")
	fs.StringVar(&only, "only", "", "只啟動這些行程，逗號分隔")
	fs.StringVar(&extra, "args", "", "附加參數給第一個行程")
	fs.BoolVar(&noCheck, "no-check", false, "不檢查埠是否被占用")
	fs.BoolVar(&dryRun, "dry-run", false, "只印出要跑什麼")
	if err := fs.Parse(args); err != nil {
		return errAlreadyReported
	}

	cfg, p, err := common.load()
	if err != nil {
		return err
	}

	opts.Open, opts.DryRun, opts.NoCheck = open, dryRun, noCheck
	if only != "" {
		for _, name := range strings.Split(only, ",") {
			if name = strings.TrimSpace(name); name != "" {
				opts.Only = append(opts.Only, name)
			}
		}
	}
	if extra != "" {
		parts, err := config.SplitArgs(extra)
		if err != nil {
			return err
		}
		opts.Args = parts
	}
	return dev.Run(ctx, cfg, p, opts)
}

func cmdFmt(ctx context.Context, args []string) error {
	var (
		common commonFlags
		check  bool
	)
	fs := newFlagSet("fmt")
	common.bind(fs)
	fs.BoolVar(&check, "check", false, "只回報未格式化的檔案，不修改")
	if err := fs.Parse(args); err != nil {
		return errAlreadyReported
	}

	cfg, p, err := common.load()
	if err != nil {
		return err
	}
	return tasks.Fmt(ctx, cfg, p, run.NewExecRunner(), check)
}

func cmdClean(ctx context.Context, args []string) error {
	var (
		common commonFlags
		opts   tasks.CleanOptions
	)
	fs := newFlagSet("clean")
	common.bind(fs)
	fs.BoolVar(&opts.Deps, "deps", false, "連 node_modules 一起刪")
	fs.BoolVar(&opts.Caches, "caches", false, "清空 Go 的建置與測試快取")
	fs.BoolVar(&opts.All, "all", false, "以上全部")
	if err := fs.Parse(args); err != nil {
		return errAlreadyReported
	}

	cfg, p, err := common.load()
	if err != nil {
		return err
	}
	return tasks.Clean(ctx, cfg, p, run.NewExecRunner(), opts)
}

func cmdSetup(ctx context.Context, args []string) error {
	var common commonFlags
	fs := newFlagSet("setup")
	common.bind(fs)
	if err := fs.Parse(args); err != nil {
		return errAlreadyReported
	}

	cfg, p, err := common.load()
	if err != nil {
		return err
	}
	if err := tasks.Setup(ctx, cfg, p, run.NewExecRunner()); err != nil {
		return err
	}
	p.Plain("")
	p.OK("相依就緒，可以跑 `hoshi dev` 或 `hoshi test` 了")
	return nil
}

func cmdCheck(_ context.Context, args []string) error {
	var common commonFlags
	fs := newFlagSet("check")
	common.bind(fs)
	if err := fs.Parse(args); err != nil {
		return errAlreadyReported
	}

	cfg, p, err := common.load()
	if err != nil {
		return err
	}

	p.Title("%s（%s）", cfg.Name, cfg.Type)
	p.Note("設定檔：%s", relTo(cfg.Root, cfg.Path))

	problems := checkLayout(cfg)
	for _, problem := range problems {
		p.Warn("%s", problem)
	}
	if len(problems) > 0 {
		return fmt.Errorf("設定與倉庫佈局對不上（%d 項）", len(problems))
	}

	describe(p, cfg)
	p.OK("設定沒有問題")
	return nil
}

// checkLayout confirms the paths in the config actually exist. Load already
// validated the shape of the file; this is the half that needs the disk.
func checkLayout(cfg *config.Config) []string {
	var problems []string

	if cfg.BuildsGo() {
		goDir := filepath.Join(cfg.Root, filepath.FromSlash(cfg.Go.Dir))
		if _, err := os.Stat(filepath.Join(goDir, "go.mod")); err != nil {
			problems = append(problems, fmt.Sprintf("`go.dir` %q 底下沒有 go.mod", cfg.Go.Dir))
		}
		if strings.HasPrefix(cfg.Go.Package, "./") {
			pkgDir := filepath.Join(goDir, filepath.FromSlash(strings.TrimPrefix(cfg.Go.Package, "./")))
			if st, err := os.Stat(pkgDir); err != nil || !st.IsDir() {
				problems = append(problems, fmt.Sprintf(
					"`go.package` %q 不存在（相對於 go.dir）", cfg.Go.Package))
			}
		}
	}

	if cfg.BuildsNpm() {
		npmDir := filepath.Join(cfg.Root, filepath.FromSlash(cfg.Npm.Dir))
		if _, err := os.Stat(filepath.Join(npmDir, "package.json")); err != nil {
			problems = append(problems, fmt.Sprintf("`npm.dir` %q 底下沒有 package.json", cfg.Npm.Dir))
		}
	}

	for _, inc := range cfg.Include {
		if _, err := os.Stat(filepath.Join(cfg.Root, filepath.FromSlash(inc))); err != nil {
			problems = append(problems, fmt.Sprintf("`include` 的 %q 不存在", inc))
		}
	}

	for i, proc := range cfg.Dev.Processes {
		dir := filepath.Join(cfg.Root, filepath.FromSlash(proc.Dir))
		if st, err := os.Stat(dir); err != nil || !st.IsDir() {
			problems = append(problems, fmt.Sprintf(
				"`dev.processes[%d].dir` %q 不存在", i, proc.Dir))
		}
	}

	// deployment.md §2 requires the frontend output to be ignored by git, and
	// §1.1 makes the same true of an executable. A committed dist/ turns every
	// build into a diff.
	if !gitIgnores(cfg.Root, cfg.Output) {
		problems = append(problems, fmt.Sprintf(
			"`output` %q 不在 .gitignore 裡（部署標準 §2：產物不進版控）", cfg.Output))
	}

	return problems
}

// gitIgnores reports whether .gitignore mentions the output directory. It is a
// text match, not a call to git: `check` has to work in an export with no git
// available, and the answer only feeds a warning.
func gitIgnores(root, output string) bool {
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return false
	}
	want := strings.Trim(filepath.ToSlash(filepath.Clean(output)), "/")
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Trim(strings.TrimPrefix(line, "/"), "/") == want {
			return true
		}
	}
	return false
}

func describe(p *ui.Printer, cfg *config.Config) {
	p.Note("輸出：%s", cfg.Output)
	if len(cfg.Targets) > 0 {
		out := make([]string, len(cfg.Targets))
		for i, t := range cfg.Targets {
			out[i] = t.String()
		}
		p.Note("目標：%s", strings.Join(out, "、"))
	} else if cfg.BuildsGo() {
		p.Note("目標：本機平臺（未設定 targets）")
	}
	if cfg.BuildsGo() {
		p.Note("Go：%s（在 %s）", cfg.Go.Package, cfg.Go.Dir)
	}
	if cfg.BuildsNpm() {
		p.Note("npm：run %s（在 %s）→ %s", cfg.Npm.Script, cfg.Npm.Dir, cfg.Npm.Output)
	}
	if len(cfg.Include) > 0 {
		p.Note("隨附：%s", strings.Join(cfg.Include, "、"))
	}
	if cfg.Archive != config.ArchiveNone {
		p.Note("封裝：%s", cfg.Archive)
	}

	p.Note("測試：%s", describeTest(cfg))
	for _, proc := range cfg.Dev.Processes {
		p.Note("dev／%s：%s（在 %s）", proc.Name, proc.Run, proc.Dir)
	}
}

func describeTest(cfg *config.Config) string {
	var parts []string
	if cfg.BuildsGo() {
		step := "go build + test " + cfg.Test.Packages
		if cfg.Test.LintEnabled() {
			step = "gofmt + vet + " + step
		}
		if cfg.Test.RaceEnabled() {
			step += "（-race）"
		}
		parts = append(parts, step)
	}
	if cfg.BuildsNpm() && len(cfg.Test.Scripts) > 0 {
		parts = append(parts, "npm run "+strings.Join(cfg.Test.Scripts, " / "))
	}
	for _, cmd := range cfg.Test.Commands {
		name := cmd.Name
		if name == "" {
			name = cmd.Run.String()
		}
		parts = append(parts, name)
	}
	if len(parts) == 0 {
		return "（沒有設定任何步驟）"
	}
	return strings.Join(parts, "，")
}

func parseTargets(spec string) ([]config.Target, error) {
	if strings.TrimSpace(spec) == "" {
		return nil, nil
	}
	var out []config.Target
	for _, part := range strings.Split(spec, ",") {
		if part = strings.TrimSpace(part); part == "" {
			continue
		}
		t, err := config.ParseTarget(part)
		if err != nil {
			return nil, fmt.Errorf("-target：%w", err)
		}
		out = append(out, t)
	}
	return out, nil
}

func relTo(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}
