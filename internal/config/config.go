// Package config parses .hoshi-build.{yaml,yml,json}.
//
// One schema, two spellings. Both formats decode into the same structs with
// unknown keys rejected: a typo like `outupt:` that silently falls back to a
// default puts artifacts somewhere nobody asked for, and nothing in the output
// would say so.
package config

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Build types. The three of them are the Hoshivel convention: a Go backend, a
// Go backend shipped next to an npm-built frontend, or a frontend on its own.
const (
	TypeGo    = "go"
	TypeGoNpm = "go-npm"
	TypeNpm   = "npm"
)

// Archive formats for `archive:`.
const (
	ArchiveNone  = "none"
	ArchiveZip   = "zip"
	ArchiveTarGz = "tar.gz"
)

// npm dependency install policies for `npm.install:`.
const (
	InstallAuto   = "auto"
	InstallAlways = "always"
	InstallNever  = "never"
)

// Config is a parsed .hoshi-build file: everything `hoshi` needs to build,
// test, run and clean a repository.
type Config struct {
	Name    string   `yaml:"name"    json:"name"`
	Type    string   `yaml:"type"    json:"type"`
	Output  string   `yaml:"output"  json:"output"`
	Version string   `yaml:"version" json:"version"`
	Targets []Target `yaml:"targets" json:"targets"`
	Archive string   `yaml:"archive" json:"archive"`
	Include []string `yaml:"include" json:"include"`

	Go    GoConfig    `yaml:"go"    json:"go"`
	Npm   NpmConfig   `yaml:"npm"   json:"npm"`
	Test  TestConfig  `yaml:"test"  json:"test"`
	Dev   DevConfig   `yaml:"dev"   json:"dev"`
	Clean CleanConfig `yaml:"clean" json:"clean"`

	// Where this came from. Not settable from the file.
	Path string `yaml:"-" json:"-"` // absolute path of the config file
	Root string `yaml:"-" json:"-"` // absolute path of the repo root
}

// GoConfig is the `go:` section: where the module is and how to link it.
type GoConfig struct {
	Dir        string   `yaml:"dir"         json:"dir"`
	Package    string   `yaml:"package"     json:"package"`
	Tags       []string `yaml:"tags"        json:"tags"`
	Ldflags    string   `yaml:"ldflags"     json:"ldflags"`
	VersionVar string   `yaml:"version_var" json:"version_var"`
}

// isZero reports whether the file said nothing about the `go:` section.
func (g GoConfig) isZero() bool {
	return g.Dir == "" && g.Package == "" && len(g.Tags) == 0 &&
		g.Ldflags == "" && g.VersionVar == ""
}

// NpmConfig is the `npm:` section.
type NpmConfig struct {
	Dir     string `yaml:"dir"     json:"dir"`
	Script  string `yaml:"script"  json:"script"`
	Output  string `yaml:"output"  json:"output"`
	WebDir  string `yaml:"web_dir" json:"web_dir"`
	Install string `yaml:"install" json:"install"`
}

// TestConfig is the `test:` section — what `hoshi test` runs.
//
// Lint and Race are pointers so that "not written down" is distinguishable
// from "written down as false"; both default to true.
type TestConfig struct {
	Lint     *bool     `yaml:"lint"     json:"lint"`
	Race     *bool     `yaml:"race"     json:"race"`
	Packages string    `yaml:"packages" json:"packages"`
	Flags    []string  `yaml:"flags"    json:"flags"`
	Scripts  []string  `yaml:"scripts"  json:"scripts"`
	Commands []Command `yaml:"commands" json:"commands"`
}

// DevConfig is the `dev:` section — what `hoshi dev` starts.
type DevConfig struct {
	Open      string    `yaml:"open"      json:"open"`
	Processes []Process `yaml:"processes" json:"processes"`
}

// CleanConfig is the `clean:` section.
type CleanConfig struct {
	Extra []string `yaml:"extra" json:"extra"`
}

// Command is one extra step for `hoshi test`.
type Command struct {
	Name string   `yaml:"name" json:"name"`
	Dir  string   `yaml:"dir"  json:"dir"`
	Run  CmdLine  `yaml:"run"  json:"run"`
	Env  []string `yaml:"env"  json:"env"`
}

// Process is one long-running program for `hoshi dev`.
type Process struct {
	Name  string   `yaml:"name"  json:"name"`
	Dir   string   `yaml:"dir"   json:"dir"`
	Run   CmdLine  `yaml:"run"   json:"run"`
	Env   []string `yaml:"env"   json:"env"`
	Ports []int    `yaml:"ports" json:"ports"`
	Ready int      `yaml:"ready" json:"ready"`
}

// ReadyPort is the port to wait on before opening a browser. It defaults to the
// first declared port, because a process that declares exactly one port has
// already said which one matters.
func (p Process) ReadyPort() int {
	if p.Ready != 0 {
		return p.Ready
	}
	if len(p.Ports) > 0 {
		return p.Ports[0]
	}
	return 0
}

func (t Target) String() string { return t.OS + "/" + t.Arch }

// Slug is the `<os>-<arch>` half of an artifact name.
func (t Target) Slug() string { return t.OS + "-" + t.Arch }

// IsWindows reports whether artifacts for this target need a .exe suffix.
func (t Target) IsWindows() bool { return t.OS == "windows" }

// ArtifactName is the per-target artifact name: `<name>-<os>-<arch>`. It names
// a file when the target ships nothing but the executable, and a directory
// otherwise — see NeedsDir.
func (c *Config) ArtifactName(t Target) string { return c.Name + "-" + t.Slug() }

// BinaryName is the executable's own name. Inside a directory artifact the
// directory already carries the platform, so the file does not repeat it.
func (c *Config) BinaryName(t Target) string {
	if t.IsWindows() {
		return c.Name + ".exe"
	}
	return c.Name
}

// NeedsDir reports whether a target's artifact is a directory rather than a
// single file. It is a directory exactly when something has to travel next to
// the executable — the frontend, or `include` entries.
//
// This is not a special case per type: deployment.md §1.1 asks for single-file
// deployment, so an artifact grows a directory only when it actually carries
// more than one thing.
func (c *Config) NeedsDir() bool {
	return c.Type == TypeGoNpm || len(c.Include) > 0
}

// BuildsGo reports whether this configuration runs the Go toolchain.
func (c *Config) BuildsGo() bool { return c.Type == TypeGo || c.Type == TypeGoNpm }

// BuildsNpm reports whether this configuration runs npm.
func (c *Config) BuildsNpm() bool { return c.Type == TypeNpm || c.Type == TypeGoNpm }

// LintEnabled reports whether `hoshi test` should run gofmt and go vet.
func (t TestConfig) LintEnabled() bool { return t.Lint == nil || *t.Lint }

// RaceEnabled reports whether `hoshi test` defaults to -race.
func (t TestConfig) RaceEnabled() bool { return t.Race == nil || *t.Race }

// nameRe keeps `name` usable as a filename on every platform we target.
var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// applyDefaults fills every optional key. It runs after validation, so error
// messages point at the real problem rather than at a filled-in default.
func (c *Config) applyDefaults() {
	if c.Output == "" {
		c.Output = "dist"
	}
	if c.Archive == "" {
		c.Archive = ArchiveNone
	}

	if c.BuildsGo() {
		if c.Go.Dir == "" {
			c.Go.Dir = "."
		}
		if c.Go.Package == "" {
			c.Go.Package = "./cmd/" + c.Name
		}
	}
	if c.BuildsNpm() {
		if c.Npm.Dir == "" {
			c.Npm.Dir = "."
		}
		if c.Npm.Script == "" {
			c.Npm.Script = "build"
		}
		if c.Npm.Output == "" {
			c.Npm.Output = "dist"
		}
		if c.Npm.WebDir == "" {
			c.Npm.WebDir = "web"
		}
		if c.Npm.Install == "" {
			c.Npm.Install = InstallAuto
		}
	}

	if c.Test.Packages == "" {
		c.Test.Packages = "./..."
	}
	if c.Test.Scripts == nil && c.BuildsNpm() {
		// The frontends here have no unit-test runner, so "testing the
		// frontend" is the strict build — which is what their package.json
		// already calls `build` (astro check, tsc && vite build).
		c.Test.Scripts = []string{"build"}
	}

	if len(c.Dev.Processes) == 0 {
		c.Dev.Processes = c.defaultProcesses()
	}
	for i := range c.Dev.Processes {
		if c.Dev.Processes[i].Name == "" {
			c.Dev.Processes[i].Name = fmt.Sprintf("proc%d", i+1)
		}
		if c.Dev.Processes[i].Dir == "" {
			c.Dev.Processes[i].Dir = "."
		}
	}
}

// defaultProcesses is what `hoshi dev` runs when `dev.processes` is absent:
// the obvious thing for the type. Four Go services and one static site then
// need no dev configuration at all.
func (c *Config) defaultProcesses() []Process {
	var out []Process
	if c.BuildsGo() {
		out = append(out, Process{
			Name: "backend",
			Dir:  c.Go.Dir,
			Run:  CmdLine{"go", "run", c.Go.Package},
		})
	}
	if c.BuildsNpm() {
		out = append(out, Process{
			Name: "frontend",
			Dir:  c.Npm.Dir,
			Run:  CmdLine{"npm", "run", "dev"},
		})
	}
	return out
}

// validate checks everything that can be checked without touching the disk,
// and reports every problem at once so one pass fixes the file.
func (c *Config) validate() error {
	var errs []error

	switch {
	case c.Name == "":
		errs = append(errs, fmt.Errorf("`name` 是必填的"))
	case !nameRe.MatchString(c.Name):
		errs = append(errs, fmt.Errorf(
			"`name` %q 不合法：只允許小寫英數與 `.`、`_`、`-`，且須以英數開頭"+
				"（它會成為檔名的一部分）", c.Name))
	}

	switch c.Type {
	case TypeGo, TypeGoNpm, TypeNpm:
	case "":
		errs = append(errs, fmt.Errorf("`type` 是必填的（go / go-npm / npm）"))
	default:
		errs = append(errs, fmt.Errorf("`type` %q 不認得，只有 go / go-npm / npm", c.Type))
	}

	switch c.Archive {
	case "", ArchiveNone, ArchiveZip, ArchiveTarGz:
	default:
		errs = append(errs, fmt.Errorf("`archive` %q 不認得，只有 none / zip / tar.gz", c.Archive))
	}

	switch c.Npm.Install {
	case "", InstallAuto, InstallAlways, InstallNever:
	default:
		errs = append(errs, fmt.Errorf(
			"`npm.install` %q 不認得，只有 auto / always / never", c.Npm.Install))
	}

	// A section the chosen type can never read is a lie in the file: someone
	// set npm.script on a `type: go` repo and is waiting for it to take effect.
	if !c.Go.isZero() && !c.BuildsGo() {
		errs = append(errs, fmt.Errorf("`type: %s` 不會跑 Go，但設定了 `go:` 區段", c.Type))
	}
	if c.Npm != (NpmConfig{}) && !c.BuildsNpm() {
		errs = append(errs, fmt.Errorf("`type: %s` 不會跑 npm，但設定了 `npm:` 區段", c.Type))
	}
	if len(c.Targets) > 0 && !c.BuildsGo() {
		errs = append(errs, fmt.Errorf(
			"`type: npm` 的產物是靜態檔，沒有平臺之分，不該設定 `targets`"))
	}
	if len(c.Include) > 0 && c.Type == TypeNpm {
		errs = append(errs, fmt.Errorf(
			"`type: npm` 不支援 `include`：靜態產物就是 npm 產出的那一疊"))
	}

	for _, spec := range []struct{ key, value string }{
		{"output", c.Output},
		{"go.dir", c.Go.Dir},
		{"npm.dir", c.Npm.Dir},
		{"npm.output", c.Npm.Output},
	} {
		if err := checkRelPath(spec.key, spec.value); err != nil {
			errs = append(errs, err)
		}
	}
	for _, inc := range c.Include {
		if err := checkRelPath("include", inc); err != nil {
			errs = append(errs, err)
		}
	}
	for _, extra := range c.Clean.Extra {
		if err := checkRelPath("clean.extra", extra); err != nil {
			errs = append(errs, err)
		}
	}
	if strings.ContainsAny(c.Npm.WebDir, `/\`) {
		errs = append(errs, fmt.Errorf(
			"`npm.web_dir` %q 必須是單一目錄名，不含路徑分隔符", c.Npm.WebDir))
	}

	if c.Go.Package != "" && !strings.HasPrefix(c.Go.Package, "./") {
		errs = append(errs, fmt.Errorf(
			"`go.package` %q 應為相對於 `go.dir` 的套件路徑（例：./cmd/%s）", c.Go.Package, c.Name))
	}

	seen := map[string]bool{}
	for _, t := range c.Targets {
		if seen[t.String()] {
			errs = append(errs, fmt.Errorf("targets 有重複的 %s", t))
		}
		seen[t.String()] = true
	}

	errs = append(errs, c.validateSteps()...)

	if len(errs) > 0 {
		return joinErrs(errs)
	}
	return nil
}

// validateSteps checks the test commands and dev processes.
func (c *Config) validateSteps() []error {
	var errs []error

	for i, cmd := range c.Test.Commands {
		where := fmt.Sprintf("test.commands[%d]", i)
		if len(cmd.Run) == 0 {
			errs = append(errs, fmt.Errorf("`%s` 少了 `run`", where))
		}
		if err := checkRelPath(where+".dir", cmd.Dir); err != nil {
			errs = append(errs, err)
		}
		for _, kv := range cmd.Env {
			if !strings.Contains(kv, "=") {
				errs = append(errs, fmt.Errorf("`%s.env` 的 %q 不是 `KEY=value`", where, kv))
			}
		}
	}

	names := map[string]bool{}
	for i, proc := range c.Dev.Processes {
		where := fmt.Sprintf("dev.processes[%d]", i)
		if len(proc.Run) == 0 {
			errs = append(errs, fmt.Errorf("`%s` 少了 `run`", where))
		}
		if err := checkRelPath(where+".dir", proc.Dir); err != nil {
			errs = append(errs, err)
		}
		// Names prefix every output line, so duplicates make the log unreadable
		// in exactly the situation the log exists for.
		if proc.Name != "" {
			if names[proc.Name] {
				errs = append(errs, fmt.Errorf("`dev.processes` 有重複的 name %q", proc.Name))
			}
			names[proc.Name] = true
		}
		for _, port := range proc.Ports {
			if port < 1 || port > 65535 {
				errs = append(errs, fmt.Errorf("`%s.ports` 的 %d 不是合法的埠", where, port))
			}
		}
		for _, kv := range proc.Env {
			if !strings.Contains(kv, "=") {
				errs = append(errs, fmt.Errorf("`%s.env` 的 %q 不是 `KEY=value`", where, kv))
			}
		}
	}
	return errs
}

// checkRelPath keeps configured paths inside the repository. `clean` removes
// what they point at, so a path that escapes the repo is not a style question.
func checkRelPath(key, value string) error {
	if value == "" {
		return nil
	}
	if path.IsAbs(value) || strings.HasPrefix(value, `\`) ||
		(len(value) > 1 && value[1] == ':') {
		return fmt.Errorf("`%s` %q 必須是相對路徑", key, value)
	}
	clean := path.Clean(strings.ReplaceAll(value, `\`, "/"))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("`%s` %q 指向倉庫之外", key, value)
	}
	return nil
}

func joinErrs(errs []error) error {
	if len(errs) == 1 {
		return errs[0]
	}
	msgs := make([]string, len(errs))
	for i, err := range errs {
		msgs[i] = "  - " + err.Error()
	}
	return fmt.Errorf("設定有 %d 個問題：\n%s", len(errs), strings.Join(msgs, "\n"))
}
