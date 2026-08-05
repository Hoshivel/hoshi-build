package tasks

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

// fakeRunner records commands instead of running them.
type fakeRunner struct {
	calls    []run.Cmd
	captures map[string]string
	fail     map[string]error
	missing  map[string]bool
}

func (r *fakeRunner) Run(_ context.Context, c run.Cmd) error {
	r.calls = append(r.calls, c)
	return r.fail[c.String()]
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

func (r *fakeRunner) find(prefix string) (run.Cmd, bool) {
	for _, c := range r.calls {
		if strings.HasPrefix(c.String(), prefix) {
			return c, true
		}
	}
	return run.Cmd{}, false
}

func quiet() *ui.Printer { return ui.New(io.Discard, io.Discard, false) }

func repo(t *testing.T, body string, files map[string]string) *config.Config {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".hoshi-build.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c, err := config.LoadFrom(root)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	return c
}

// raceRunner reports a working cgo toolchain so -race is not degraded.
func raceRunner() *fakeRunner {
	return &fakeRunner{captures: map[string]string{
		"go env CGO_ENABLED": "1",
		"go env CC":          "cc",
	}}
}

func TestGoPipelineOrder(t *testing.T) {
	c := repo(t, "name: svc\ntype: go\n", nil)
	r := raceRunner()

	if err := Test(context.Background(), c, quiet(), r, TestOptions{}); err != nil {
		t.Fatalf("Test() error = %v", err)
	}

	// vet before build before test: each one's failure explains the next one's,
	// so running them out of order buries the cause under its consequences.
	want := []string{"gofmt -l .", "go vet ./...", "go build ./...", "go test"}
	at := -1
	for _, prefix := range want {
		found := -1
		for i, call := range r.calls {
			if i > at && strings.HasPrefix(call.String(), prefix) {
				found = i
				break
			}
		}
		if found < 0 {
			t.Fatalf("沒有跑 %q，實際跑了 %v", prefix, r.calls)
		}
		at = found
	}
}

// gofmt -l exits 0 whether or not it listed files, so the file list is the
// result. Checking the exit code here would pass on an unformatted tree.
func TestGofmtFailsOnListedFiles(t *testing.T) {
	c := repo(t, "name: svc\ntype: go\n", nil)
	r := raceRunner()
	r.captures["gofmt -l ."] = "internal/a.go\ninternal/b.go"

	err := Test(context.Background(), c, quiet(), r, TestOptions{})
	if err == nil {
		t.Fatal("gofmt 列出檔案時必須失敗")
	}
	if !strings.Contains(err.Error(), "2 個檔案") {
		t.Errorf("error = %q，想看到未格式化的檔案數", err)
	}
	if r.ran("go test") {
		t.Error("gofmt 失敗之後不該繼續跑測試")
	}
}

func TestNoLintSkipsGofmtAndVet(t *testing.T) {
	c := repo(t, "name: svc\ntype: go\n", nil)
	r := raceRunner()

	if err := Test(context.Background(), c, quiet(), r, TestOptions{NoLint: true}); err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if r.ran("gofmt") || r.ran("go vet") {
		t.Errorf("-no-lint 之下不該跑 gofmt / vet：%v", r.calls)
	}
	if !r.ran("go test") {
		t.Error("-no-lint 仍然要跑測試")
	}
}

func TestRaceOnByDefault(t *testing.T) {
	c := repo(t, "name: svc\ntype: go\n", nil)
	r := raceRunner()

	if err := Test(context.Background(), c, quiet(), r, TestOptions{}); err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	cmd, ok := r.find("go test")
	if !ok {
		t.Fatal("沒有跑 go test")
	}
	if !strings.Contains(cmd.String(), "-race") {
		t.Errorf("go test = %q，預設應該帶 -race", cmd)
	}
}

// A green "passed" that never ran the detector is worse than a visible gap.
func TestRaceDegradesWhenCgoMissing(t *testing.T) {
	c := repo(t, "name: svc\ntype: go\n", nil)
	r := &fakeRunner{captures: map[string]string{"go env CGO_ENABLED": "0"}}

	if err := Test(context.Background(), c, quiet(), r, TestOptions{}); err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	cmd, _ := r.find("go test")
	if strings.Contains(cmd.String(), "-race") {
		t.Errorf("go test = %q，沒有 cgo 時不該假裝跑了 -race", cmd)
	}
	if !strings.Contains(cmd.String(), "-count 2") {
		t.Errorf("go test = %q，應退化為 -count 2", cmd)
	}
}

func TestNoRaceOverridesConfig(t *testing.T) {
	c := repo(t, "name: svc\ntype: go\ntest:\n  race: true\n", nil)
	r := raceRunner()

	if err := Test(context.Background(), c, quiet(), r, TestOptions{NoRace: true}); err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	cmd, _ := r.find("go test")
	if strings.Contains(cmd.String(), "-race") {
		t.Errorf("go test = %q，-no-race 應該蓋過設定", cmd)
	}
}

func TestGoTestFlagsAndPackages(t *testing.T) {
	c := repo(t, "name: svc\ntype: go\ntest:\n  packages: ./internal/...\n  flags: [-timeout, 5m]\n", nil)
	r := raceRunner()

	err := Test(context.Background(), c, quiet(), r, TestOptions{Short: true, Run: "TestFoo"})
	if err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	cmd, _ := r.find("go test")
	for _, want := range []string{"-short", "-run TestFoo", "-timeout 5m", "./internal/..."} {
		if !strings.Contains(cmd.String(), want) {
			t.Errorf("go test = %q，少了 %q", cmd, want)
		}
	}
}

func TestPkgOverridesConfiguredPackages(t *testing.T) {
	c := repo(t, "name: svc\ntype: go\ntest:\n  packages: ./internal/...\n", nil)
	r := raceRunner()

	err := Test(context.Background(), c, quiet(), r, TestOptions{Packages: "./cmd/..."})
	if err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	cmd, _ := r.find("go test")
	if !strings.HasSuffix(cmd.String(), "./cmd/...") {
		t.Errorf("go test = %q，-pkg 應該蓋過設定", cmd)
	}
}

func TestNpmScriptsRunInOrder(t *testing.T) {
	c := repo(t, "name: site\ntype: npm\ntest:\n  scripts: [typecheck, build]\n",
		map[string]string{"package.json": `{"name":"site"}`, "node_modules/.keep": ""})
	r := &fakeRunner{}

	if err := Test(context.Background(), c, quiet(), r, TestOptions{}); err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	typecheckAt, buildAt := -1, -1
	for i, call := range r.calls {
		switch call.String() {
		case "npm run typecheck":
			typecheckAt = i
		case "npm run build":
			buildAt = i
		}
	}
	if typecheckAt < 0 || buildAt < 0 || typecheckAt > buildAt {
		t.Errorf("scripts 應照設定順序跑：%v", r.calls)
	}
	if r.ran("go ") {
		t.Error("type: npm 不該碰 Go 工具鏈")
	}
}

func TestCustomCommands(t *testing.T) {
	c := repo(t, `
name: svc
type: go
test:
  commands:
    - name: 資料庫測試
      run: go test -tags db ./...
      env: [TEST_MYSQL_URL=mysql://x]
`, nil)
	r := raceRunner()

	if err := Test(context.Background(), c, quiet(), r, TestOptions{}); err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	cmd, ok := r.find("go test -tags db")
	if !ok {
		t.Fatalf("自訂步驟沒有跑：%v", r.calls)
	}
	if len(cmd.Env) != 1 || cmd.Env[0] != "TEST_MYSQL_URL=mysql://x" {
		t.Errorf("Env = %v，自訂步驟的環境變數沒有傳進去", cmd.Env)
	}
}

func TestOnlyGoSkipsNpm(t *testing.T) {
	c := repo(t, "name: sr\ntype: go-npm\ngo:\n  dir: backend\nnpm:\n  dir: frontend\n",
		map[string]string{"backend/go.mod": "module x\n", "frontend/package.json": `{"name":"f"}`})
	r := raceRunner()

	if err := Test(context.Background(), c, quiet(), r, TestOptions{OnlyGo: true}); err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if r.ran("npm") {
		t.Errorf("-go 之下不該跑 npm：%v", r.calls)
	}
}

func TestMissingToolchainSaysWhatToInstall(t *testing.T) {
	c := repo(t, "name: svc\ntype: go\n", nil)
	r := &fakeRunner{missing: map[string]bool{"go": true}}

	err := Test(context.Background(), c, quiet(), r, TestOptions{})
	if err == nil || !strings.Contains(err.Error(), "請安裝 Go") {
		t.Fatalf("error = %v，預期是一句可照做的指示", err)
	}
}
