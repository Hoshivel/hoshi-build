package tasks

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFmtCheckReportsWithoutWriting(t *testing.T) {
	c := repo(t, "name: svc\ntype: go\n", nil)
	r := &fakeRunner{captures: map[string]string{"gofmt -l .": "a.go\nb.go"}}

	err := Fmt(context.Background(), c, quiet(), r, true)
	if err == nil || !strings.Contains(err.Error(), "2 個檔案") {
		t.Fatalf("error = %v，-check 有未格式化的檔案時必須失敗", err)
	}
	if r.ran("gofmt -w") {
		t.Error("-check 不該真的改檔案")
	}
}

func TestFmtWrites(t *testing.T) {
	c := repo(t, "name: svc\ntype: go\n", nil)
	r := &fakeRunner{captures: map[string]string{"gofmt -l .": "a.go"}}

	if err := Fmt(context.Background(), c, quiet(), r, false); err != nil {
		t.Fatalf("Fmt() error = %v", err)
	}
	if !r.ran("gofmt -w") {
		t.Errorf("沒有跑 gofmt -w：%v", r.calls)
	}
}

func TestFmtCleanTreeDoesNothing(t *testing.T) {
	c := repo(t, "name: svc\ntype: go\n", nil)
	r := &fakeRunner{}

	if err := Fmt(context.Background(), c, quiet(), r, false); err != nil {
		t.Fatalf("Fmt() error = %v", err)
	}
	if r.ran("gofmt -w") {
		t.Error("已經格式化的樹不必再寫一次")
	}
}

func TestFmtSkipsNpmOnlyRepos(t *testing.T) {
	c := repo(t, "name: site\ntype: npm\n", map[string]string{"package.json": `{"name":"site"}`})
	r := &fakeRunner{}

	if err := Fmt(context.Background(), c, quiet(), r, false); err != nil {
		t.Fatalf("Fmt() error = %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("純前端專案沒有 Go 可格式化：%v", r.calls)
	}
}

func TestCleanRemovesOutputAndExtras(t *testing.T) {
	c := repo(t, "name: svc\ntype: go\nclean:\n  extra: [.dev-logs]\n",
		map[string]string{"dist/old": "x", ".dev-logs/a.log": "x", "keep.txt": "x"})
	r := &fakeRunner{}

	if err := Clean(context.Background(), c, quiet(), r, CleanOptions{}); err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	for _, gone := range []string{"dist", ".dev-logs"} {
		if _, err := os.Stat(filepath.Join(c.Root, gone)); !os.IsNotExist(err) {
			t.Errorf("%s 還在", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(c.Root, "keep.txt")); err != nil {
		t.Error("clean 刪掉了不該刪的東西")
	}
}

func TestCleanDepsRemovesNodeModules(t *testing.T) {
	c := repo(t, "name: site\ntype: npm\n",
		map[string]string{"package.json": `{"name":"s"}`, "node_modules/x/index.js": "x", "dist/a": "x"})
	r := &fakeRunner{}

	if err := Clean(context.Background(), c, quiet(), r, CleanOptions{Deps: true}); err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.Root, "node_modules")); !os.IsNotExist(err) {
		t.Error("-deps 應該刪掉 node_modules")
	}
}

// Without -deps, node_modules survives: reinstalling it is the slowest thing
// in the whole toolchain, and `clean` is not the command people reach for when
// they want to wait.
func TestCleanKeepsNodeModulesByDefault(t *testing.T) {
	c := repo(t, "name: site\ntype: npm\n",
		map[string]string{"package.json": `{"name":"s"}`, "node_modules/x/index.js": "x"})
	r := &fakeRunner{}

	if err := Clean(context.Background(), c, quiet(), r, CleanOptions{}); err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(c.Root, "node_modules")); err != nil {
		t.Error("預設不該刪 node_modules")
	}
}

func TestCleanCachesCallsGoClean(t *testing.T) {
	c := repo(t, "name: svc\ntype: go\n", nil)
	r := &fakeRunner{}

	if err := Clean(context.Background(), c, quiet(), r, CleanOptions{All: true}); err != nil {
		t.Fatalf("Clean() error = %v", err)
	}
	if !r.ran("go clean -cache -testcache") {
		t.Errorf("-all 應該清 Go 快取：%v", r.calls)
	}
}

func TestSetupInstallsBothToolchains(t *testing.T) {
	c := repo(t, "name: sr\ntype: go-npm\ngo:\n  dir: backend\nnpm:\n  dir: frontend\n",
		map[string]string{
			"backend/go.mod":             "module x\n",
			"frontend/package.json":      `{"name":"f"}`,
			"frontend/package-lock.json": `{"lockfileVersion":3}`,
		})
	r := &fakeRunner{}

	if err := Setup(context.Background(), c, quiet(), r); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if !r.ran("go mod download") {
		t.Errorf("沒有跑 go mod download：%v", r.calls)
	}
	if !r.ran("npm ci") {
		t.Errorf("有 lockfile 就該用 npm ci：%v", r.calls)
	}
}

// setup means "install", so it installs even when node_modules already exists —
// unlike the build path, where npm.install=auto skips.
func TestSetupInstallsEvenWhenNodeModulesExists(t *testing.T) {
	c := repo(t, "name: site\ntype: npm\n",
		map[string]string{"package.json": `{"name":"s"}`, "node_modules/.keep": ""})
	r := &fakeRunner{}

	if err := Setup(context.Background(), c, quiet(), r); err != nil {
		t.Fatalf("Setup() error = %v", err)
	}
	if !r.ran("npm install") {
		t.Errorf("setup 應該無條件安裝：%v", r.calls)
	}
}

func TestEnsureDepsRespectsNever(t *testing.T) {
	c := repo(t, "name: site\ntype: npm\nnpm:\n  install: never\n",
		map[string]string{"package.json": `{"name":"s"}`})
	r := &fakeRunner{}

	if err := EnsureDeps(context.Background(), c, quiet(), r, false); err != nil {
		t.Fatalf("EnsureDeps() error = %v", err)
	}
	if len(r.calls) != 0 {
		t.Errorf("npm.install=never 不該安裝任何東西：%v", r.calls)
	}
}
