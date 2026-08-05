package build

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/run"
	"github.com/hoshivel/hoshi-build/internal/ui"
)

// fakeRunner records commands instead of running them, and can stand in for
// tools that are not installed.
type fakeRunner struct {
	calls    []run.Cmd
	captures map[string]string   // command string -> stdout
	missing  map[string]bool     // executables to report as absent
	onRun    func(run.Cmd) error // side effects, e.g. creating output
}

func (r *fakeRunner) Run(_ context.Context, c run.Cmd) error {
	r.calls = append(r.calls, c)
	if r.onRun != nil {
		return r.onRun(c)
	}
	return nil
}

func (r *fakeRunner) Capture(_ context.Context, c run.Cmd) (string, error) {
	r.calls = append(r.calls, c)
	if out, ok := r.captures[c.String()]; ok {
		return out, nil
	}
	return "", nil
}

func (r *fakeRunner) Look(name string) (string, error) {
	if r.missing[name] {
		return "", os.ErrNotExist
	}
	return "/usr/bin/" + name, nil
}

func (r *fakeRunner) ran(prefix string) bool {
	for _, c := range r.calls {
		if strings.HasPrefix(c.String(), prefix) {
			return true
		}
	}
	return false
}

func quietPrinter() *ui.Printer { return ui.New(io.Discard, io.Discard, false) }

// testRepo writes a repository skeleton and its config, returning the root.
func testRepo(t *testing.T, body string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".hoshi-build.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func loadRepo(t *testing.T, root string) *config.Config {
	t.Helper()
	c, err := config.LoadFrom(root)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	return c
}

// A `type: go` build with nothing to carry produces one file named exactly
// <name>-<os>-<arch>, with no directory around it.
func TestGoArtifactIsASingleFile(t *testing.T) {
	root := testRepo(t, "name: demo-api\ntype: go\noutput: dist/\ntargets:\n  - linux/amd64\n", nil)
	c := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	res, err := Run(context.Background(), c, quietPrinter(), r, Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := filepath.Join(root, "dist", "demo-api-linux-amd64")
	if len(res.Artifacts) != 1 || res.Artifacts[0].Path != want {
		t.Fatalf("artifacts = %+v, want one file at %s", res.Artifacts, want)
	}
	if res.Artifacts[0].IsDir {
		t.Error("IsDir = true；沒有隨附內容就不該有目錄")
	}
	if st, err := os.Stat(want); err != nil || st.IsDir() {
		t.Errorf("產物不是一個檔案：%v", err)
	}
}

func TestWindowsArtifactGetsExeSuffix(t *testing.T) {
	root := testRepo(t, "name: demo-api\ntype: go\ntargets:\n  - windows/amd64\n", nil)
	c := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	res, err := Run(context.Background(), c, quietPrinter(), r, Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := filepath.Base(res.Artifacts[0].Path); got != "demo-api-windows-amd64.exe" {
		t.Errorf("產物 = %q, want demo-api-windows-amd64.exe", got)
	}
}

// include turns the artifact into a directory, and the executable inside drops
// the platform suffix because the directory already carries it.
func TestIncludeProducesADirectoryArtifact(t *testing.T) {
	root := testRepo(t,
		"name: srv\ntype: go\ntargets:\n  - linux/amd64\ninclude:\n  - story/\n",
		map[string]string{"story/chapter1.md": "# 序章\n"})
	c := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	res, err := Run(context.Background(), c, quietPrinter(), r, Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	stage := filepath.Join(root, "dist", "srv-linux-amd64")
	if res.Artifacts[0].Path != stage || !res.Artifacts[0].IsDir {
		t.Fatalf("artifact = %+v, want a directory at %s", res.Artifacts[0], stage)
	}
	for _, want := range []string{"srv", "story/chapter1.md"} {
		if _, err := os.Stat(filepath.Join(stage, filepath.FromSlash(want))); err != nil {
			t.Errorf("產物少了 %s：%v", want, err)
		}
	}
}

// A declared include that is not there would ship an artifact missing
// something the config says it needs, and nothing downstream would notice.
func TestMissingIncludeFailsTheBuild(t *testing.T) {
	root := testRepo(t, "name: srv\ntype: go\ntargets:\n  - linux/amd64\ninclude:\n  - story/\n", nil)
	c := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	_, err := Run(context.Background(), c, quietPrinter(), r, Options{})
	if err == nil || !strings.Contains(err.Error(), "story/") {
		t.Fatalf("Run() error = %v，預期指出 include 不存在", err)
	}
}

func TestGoNpmAssemblesFrontendAndRunsNpmFirst(t *testing.T) {
	root := testRepo(t,
		"name: sr\ntype: go-npm\ntargets:\n  - linux/amd64\ngo:\n  dir: backend\nnpm:\n  dir: frontend\n",
		map[string]string{
			"backend/go.mod":              "module x\n",
			"frontend/package.json":       `{"name":"f"}`,
			"frontend/dist/index.html":    "<!doctype html>",
			"frontend/node_modules/.keep": "",
		})
	c := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	res, err := Run(context.Background(), c, quietPrinter(), r, Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// Ordering is not cosmetic: the Go phase assembles the artifact around the
	// frontend, so a frontend built afterwards would never make it in.
	npmAt, goAt := -1, -1
	for i, c := range r.calls {
		if strings.HasPrefix(c.String(), "npm run") && npmAt < 0 {
			npmAt = i
		}
		if strings.HasPrefix(c.String(), "go build") && goAt < 0 {
			goAt = i
		}
	}
	if npmAt < 0 || goAt < 0 || npmAt > goAt {
		t.Fatalf("npm 應在 go 之前跑：npm=%d go=%d，calls=%v", npmAt, goAt, r.calls)
	}

	stage := res.Artifacts[0].Path
	if _, err := os.Stat(filepath.Join(stage, "web", "index.html")); err != nil {
		t.Errorf("產物少了 web/index.html：%v", err)
	}
	if _, err := os.Stat(filepath.Join(stage, "sr")); err != nil {
		t.Errorf("產物少了執行檔：%v", err)
	}
}

// Static-site shape: astro already writes into dist/, so there is nothing to move.
func TestNpmOnlyLeavesTheBundleWhereItIs(t *testing.T) {
	root := testRepo(t, "name: static-site\ntype: npm\noutput: dist/\n",
		map[string]string{
			"package.json":       `{"name":"static-site"}`,
			"dist/index.html":    "<!doctype html>",
			"node_modules/.keep": "",
		})
	c := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	res, err := Run(context.Background(), c, quietPrinter(), r, Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0].Path != filepath.Join(root, "dist") {
		t.Fatalf("artifacts = %+v", res.Artifacts)
	}
	if _, err := os.Stat(filepath.Join(root, "dist", "index.html")); err != nil {
		t.Errorf("靜態產物不見了：%v", err)
	}
	if r.ran("go build") {
		t.Error("type: npm 不該跑 go build")
	}
}

func TestNpmOutputIsCopiedWhenItDiffersFromOutput(t *testing.T) {
	root := testRepo(t, "name: site\ntype: npm\noutput: public/\nnpm:\n  output: build\n",
		map[string]string{
			"package.json":       `{"name":"site"}`,
			"build/index.html":   "<!doctype html>",
			"node_modules/.keep": "",
		})
	c := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	if _, err := Run(context.Background(), c, quietPrinter(), r, Options{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "public", "index.html")); err != nil {
		t.Errorf("沒有複製到 output：%v", err)
	}
}

func TestInstallSkippedWhenNodeModulesPresent(t *testing.T) {
	root := testRepo(t, "name: site\ntype: npm\n",
		map[string]string{
			"package.json":       `{"name":"site"}`,
			"dist/index.html":    "x",
			"node_modules/.keep": "",
		})
	c := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	if _, err := Run(context.Background(), c, quietPrinter(), r, Options{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if r.ran("npm ci") || r.ran("npm install") {
		t.Errorf("node_modules 已存在時不該安裝：%v", r.calls)
	}
}

// A lockfile means the exact versions are decided; `npm ci` honours that and
// `npm install` may quietly resolve something else.
func TestInstallPrefersCiWhenALockfileExists(t *testing.T) {
	root := testRepo(t, "name: site\ntype: npm\n",
		map[string]string{
			"package.json":      `{"name":"site"}`,
			"package-lock.json": `{"lockfileVersion":3}`,
			"dist/index.html":   "x",
		})
	c := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	if _, err := Run(context.Background(), c, quietPrinter(), r, Options{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !r.ran("npm ci") {
		t.Errorf("有 lockfile 就該用 npm ci：%v", r.calls)
	}
}

func TestInstallFallsBackWithoutALockfile(t *testing.T) {
	root := testRepo(t, "name: site\ntype: npm\n",
		map[string]string{"package.json": `{"name":"site"}`, "dist/index.html": "x"})
	c := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	if _, err := Run(context.Background(), c, quietPrinter(), r, Options{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !r.ran("npm install") {
		t.Errorf("沒有 lockfile 時該用 npm install：%v", r.calls)
	}
}

func TestCleanEmptiesOutputFirst(t *testing.T) {
	root := testRepo(t, "name: x\ntype: go\ntargets:\n  - linux/amd64\n",
		map[string]string{"dist/stale-artifact": "old"})
	c := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	if _, err := Run(context.Background(), c, quietPrinter(), r, Options{Clean: true}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "dist", "stale-artifact")); !os.IsNotExist(err) {
		t.Error("--clean 之後舊產物還在")
	}
}

func TestTargetOverrideBeatsTheConfig(t *testing.T) {
	root := testRepo(t, "name: x\ntype: go\ntargets:\n  - linux/amd64\n", nil)
	c := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	res, err := Run(context.Background(), c, quietPrinter(), r, Options{
		Targets: []config.Target{{OS: "darwin", Arch: "arm64"}},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(res.Artifacts) != 1 || filepath.Base(res.Artifacts[0].Path) != "x-darwin-arm64" {
		t.Errorf("artifacts = %+v，--target 沒有蓋過設定", res.Artifacts)
	}
}

func TestMultipleTargets(t *testing.T) {
	root := testRepo(t, "name: x\ntype: go\ntargets:\n  - linux/amd64\n  - linux/arm64\n", nil)
	c := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	res, err := Run(context.Background(), c, quietPrinter(), r, Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(res.Artifacts) != 2 {
		t.Fatalf("artifacts = %+v，want 2", res.Artifacts)
	}
}

func TestMissingToolchainSaysWhatToInstall(t *testing.T) {
	root := testRepo(t, "name: x\ntype: go\ntargets:\n  - linux/amd64\n", nil)
	c := loadRepo(t, root)

	r := &fakeRunner{missing: map[string]bool{"go": true}}
	_, err := Run(context.Background(), c, quietPrinter(), r, Options{})
	if err == nil || !strings.Contains(err.Error(), "請安裝 Go") {
		t.Fatalf("Run() error = %v，預期是一句可照做的指示", err)
	}
}

func TestOutputOverrideStaysInsideTheRepo(t *testing.T) {
	root := testRepo(t, "name: x\ntype: go\ntargets:\n  - linux/amd64\n", nil)
	c := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	_, err := Run(context.Background(), c, quietPrinter(), r, Options{Output: "../escaped"})
	if err == nil || !strings.Contains(err.Error(), "倉庫之外") {
		t.Fatalf("Run() error = %v，--output 不該能指到倉庫外面", err)
	}
}

func TestVersionOverride(t *testing.T) {
	root := testRepo(t, "name: x\ntype: go\ntargets:\n  - linux/amd64\n", nil)
	c := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	res, err := Run(context.Background(), c, quietPrinter(), r, Options{Version: "v9.9.9"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Version != "v9.9.9" {
		t.Errorf("Version = %q, want v9.9.9", res.Version)
	}
}

// touchOutput stands in for a compiler: it creates whatever -o points at, so
// the assembly and verification stages have a file to work with.
func touchOutput(c run.Cmd) error {
	for i, a := range c.Args {
		if a == "-o" && i+1 < len(c.Args) {
			if err := os.MkdirAll(filepath.Dir(c.Args[i+1]), 0o755); err != nil {
				return err
			}
			return os.WriteFile(c.Args[i+1], []byte("#!/bin/false\n"), 0o755)
		}
	}
	return nil
}
