package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// write drops a config file into a temp directory and loads it.
func load(t *testing.T, filename, body string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

func mustLoad(t *testing.T, body string) *Config {
	t.Helper()
	c, err := Load(writeTemp(t, ".hoshi-build.yaml", body))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	return c
}

func writeTemp(t *testing.T, filename, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), filename)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadMinimalGo(t *testing.T) {
	c := mustLoad(t, "name: demo-api\ntype: go\noutput: dist/\n")

	if c.Name != "demo-api" || c.Type != TypeGo {
		t.Fatalf("name/type = %q/%q", c.Name, c.Type)
	}
	if c.Output != "dist/" {
		t.Errorf("Output = %q", c.Output)
	}
	// The default main package follows from the name, so four Go services do
	// not have to say the same thing four times.
	if c.Go.Package != "./cmd/demo-api" {
		t.Errorf("Go.Package = %q, want ./cmd/demo-api", c.Go.Package)
	}
	if c.Go.Dir != "." {
		t.Errorf("Go.Dir = %q", c.Go.Dir)
	}
	if c.Archive != ArchiveNone {
		t.Errorf("Archive = %q", c.Archive)
	}
	if c.NeedsDir() {
		t.Error("NeedsDir() = true；沒有隨附內容的 go 產物應該是單一檔案")
	}
}

func TestLoadGoNpm(t *testing.T) {
	c := mustLoad(t, `
name: my-app
type: go-npm
output: dist/

targets:
  - linux/amd64
  - windows/amd64

include:
  - story/

go:
  dir: backend
  package: ./cmd/server

npm:
  dir: frontend
`)

	if len(c.Targets) != 2 || c.Targets[0].String() != "linux/amd64" {
		t.Fatalf("Targets = %v", c.Targets)
	}
	if c.Go.Dir != "backend" || c.Npm.Dir != "frontend" {
		t.Errorf("dirs = %q / %q", c.Go.Dir, c.Npm.Dir)
	}
	if c.Npm.WebDir != "web" || c.Npm.Script != "build" || c.Npm.Install != InstallAuto {
		t.Errorf("npm defaults = %+v", c.Npm)
	}
	if !c.NeedsDir() {
		t.Error("NeedsDir() = false；go-npm 的產物要放得下前端")
	}
	if got, want := c.ArtifactName(c.Targets[0]), "my-app-linux-amd64"; got != want {
		t.Errorf("ArtifactName() = %q, want %q", got, want)
	}
	if got, want := c.BinaryName(c.Targets[1]), "my-app.exe"; got != want {
		t.Errorf("BinaryName(windows) = %q, want %q", got, want)
	}
}

func TestArtifactNaming(t *testing.T) {
	c := mustLoad(t, "name: demo-relay\ntype: go\n")
	linux := Target{OS: "linux", Arch: "amd64"}
	win := Target{OS: "windows", Arch: "arm64"}

	if got, want := c.ArtifactName(linux), "demo-relay-linux-amd64"; got != want {
		t.Errorf("ArtifactName() = %q, want %q", got, want)
	}
	if got, want := c.ArtifactName(win), "demo-relay-windows-arm64"; got != want {
		t.Errorf("ArtifactName() = %q, want %q", got, want)
	}
	// Inside a directory artifact the directory already carries the platform.
	if got, want := c.BinaryName(linux), "demo-relay"; got != want {
		t.Errorf("BinaryName() = %q, want %q", got, want)
	}
}

func TestIncludeForcesDirectoryArtifact(t *testing.T) {
	c := mustLoad(t, "name: svc\ntype: go\ninclude:\n  - story/\n")
	if !c.NeedsDir() {
		t.Error("NeedsDir() = false；有 include 就得有地方放它")
	}
}

// A typo in a key is the failure this rejection exists for: treated as
// "unset", it puts artifacts somewhere else and says nothing.
func TestUnknownKeysAreRejected(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"top level typo", "name: x\ntype: go\noutupt: dist/\n", "outupt"},
		{"nested typo", "name: x\ntype: go\ngo:\n  packge: ./cmd/x\n", "go.packge"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(t, ".hoshi-build.yaml", tc.body)
			if err == nil {
				t.Fatal("預期報錯")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, 想看到 %q", err, tc.want)
			}
		})
	}
}

func TestValidationRejects(t *testing.T) {
	tests := []struct {
		name, body, want string
	}{
		{"missing name", "type: go\n", "`name` 是必填"},
		{"missing type", "name: x\n", "`type` 是必填"},
		{"unknown type", "name: x\ntype: rust\n", "不認得"},
		{"uppercase name", "name: HoshiAdmin\ntype: go\n", "不合法"},
		{"name with a slash", "name: a/b\ntype: go\n", "不合法"},
		{"bad target", "name: x\ntype: go\ntargets:\n  - linux\n", "os/arch"},
		{"duplicate targets", "name: x\ntype: go\ntargets:\n  - linux/amd64\n  - linux/amd64\n", "重複"},
		{"unknown archive", "name: x\ntype: go\narchive: 7z\n", "archive"},
		{"unknown install policy", "name: x\ntype: npm\nnpm:\n  install: maybe\n", "npm.install"},
		{"absolute output", "name: x\ntype: go\noutput: /tmp/out\n", "相對路徑"},
		{"output escaping the repo", "name: x\ntype: go\noutput: ../out\n", "倉庫之外"},
		{"go section on an npm build", "name: x\ntype: npm\ngo:\n  dir: .\n", "不會跑 Go"},
		{"npm section on a go build", "name: x\ntype: go\nnpm:\n  dir: web\n", "不會跑 npm"},
		{"targets on an npm build", "name: x\ntype: npm\ntargets:\n  - linux/amd64\n", "沒有平臺之分"},
		{"include on an npm build", "name: x\ntype: npm\ninclude:\n  - a/\n", "不支援 `include`"},
		{"web_dir with a separator", "name: x\ntype: go-npm\nnpm:\n  web_dir: a/b\n", "單一目錄名"},
		{"go package not relative", "name: x\ntype: go\ngo:\n  package: cmd/x\n", "go.package"},
		{"wrong type for a scalar", "name: x\ntype: go\noutput:\n  - a\n", "期望字串"},
		{"wrong type for a list", "name: x\ntype: go\ntargets: linux/amd64\n", "期望清單"},
		{"section given a scalar", "name: x\ntype: go\ngo: backend\n", "期望區段"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(t, ".hoshi-build.yaml", tc.body)
			if err == nil {
				t.Fatal("預期報錯")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, 想看到 %q", err, tc.want)
			}
		})
	}
}

// Every problem at once, so a first run fixes the file rather than revealing
// the next error one build at a time.
func TestValidationReportsEveryProblem(t *testing.T) {
	_, err := load(t, ".hoshi-build.yaml", "name: BAD\ntype: nope\narchive: 7z\n")
	if err == nil {
		t.Fatal("預期報錯")
	}
	for _, want := range []string{"name", "type", "archive"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q，少了 %q", err, want)
		}
	}
}

// JSON and YAML are two spellings of one schema; they must not drift.
func TestJSONMatchesYAML(t *testing.T) {
	yamlCfg := mustLoad(t, `
name: demo-store
type: go
output: build/
targets:
  - linux/amd64
go:
  package: ./cmd/demo-store
  tags: [netgo]
`)

	jsonPath := writeTemp(t, ".hoshi-build.json", `{
  "name": "demo-store",
  "type": "go",
  "output": "build/",
  "targets": ["linux/amd64"],
  "go": {"package": "./cmd/demo-store", "tags": ["netgo"]}
}`)
	jsonCfg, err := Load(jsonPath)
	if err != nil {
		t.Fatalf("Load(json) error = %v", err)
	}

	// Path and Root differ by construction; everything else must match.
	yamlCfg.Path, yamlCfg.Root = "", ""
	jsonCfg.Path, jsonCfg.Root = "", ""
	if !reflect.DeepEqual(yamlCfg, jsonCfg) {
		t.Errorf("YAML 與 JSON 解出來不一樣：\n  yaml=%+v\n  json=%+v", *yamlCfg, *jsonCfg)
	}
}

func TestJSONRejectsUnknownKeys(t *testing.T) {
	_, err := load(t, ".hoshi-build.json", `{"name":"x","type":"go","outupt":"dist/"}`)
	if err == nil || !strings.Contains(err.Error(), "outupt") {
		t.Fatalf("error = %v，想看到 outupt 被指出來", err)
	}
}

func TestFindWalksUp(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".hoshi-build.yaml"), []byte("name: x\ntype: go\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	deep := filepath.Join(root, "internal", "httpapi")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := Find(deep)
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if filepath.Dir(got) != root {
		t.Errorf("Find() = %q，想找到倉庫根的那一份", got)
	}
}

func TestFindReportsMissing(t *testing.T) {
	if _, err := Find(t.TempDir()); err != ErrNotFound {
		t.Errorf("Find() error = %v, want ErrNotFound", err)
	}
}

func TestFilenamePrecedence(t *testing.T) {
	dir := t.TempDir()
	for _, name := range Filenames {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("name: x\ntype: go\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(got) != Filenames[0] {
		t.Errorf("Find() = %q, want %q", filepath.Base(got), Filenames[0])
	}
}

// --- test / dev / clean 區段（第二階段新增）---------------------------------

func TestTestDefaults(t *testing.T) {
	c := mustLoad(t, "name: svc\ntype: go\n")

	// 沒寫 = 開著。lint 與 race 是那種「關掉之後沒有人會發現」的東西，
	// 預設必須是開的。
	if !c.Test.LintEnabled() || !c.Test.RaceEnabled() {
		t.Errorf("lint=%v race=%v，兩者預設都該是 true", c.Test.LintEnabled(), c.Test.RaceEnabled())
	}
	if c.Test.Packages != "./..." {
		t.Errorf("Packages = %q, want ./...", c.Test.Packages)
	}
	if len(c.Test.Scripts) != 0 {
		t.Errorf("Scripts = %v；純 go 專案不該有 npm scripts", c.Test.Scripts)
	}
}

// 寫成 false 和沒寫必須是兩件事，否則 `lint: false` 會被預設值蓋掉。
func TestTestLintCanBeTurnedOff(t *testing.T) {
	c := mustLoad(t, "name: svc\ntype: go\ntest:\n  lint: false\n  race: false\n")
	if c.Test.LintEnabled() || c.Test.RaceEnabled() {
		t.Errorf("lint=%v race=%v，明寫 false 就該是 false", c.Test.LintEnabled(), c.Test.RaceEnabled())
	}
}

func TestNpmTestScriptsDefault(t *testing.T) {
	c := mustLoad(t, "name: site\ntype: npm\n")
	if len(c.Test.Scripts) != 1 || c.Test.Scripts[0] != "build" {
		t.Errorf("Scripts = %v, want [build]", c.Test.Scripts)
	}
}

// dev 沒設定時要能從 type 推出來，否則四個 Go 服務都得抄一段一樣的設定。
func TestDevProcessesDefaultFromType(t *testing.T) {
	tests := []struct {
		name, body string
		want       []string // 每個行程的命令列
	}{
		{"go", "name: svc\ntype: go\n", []string{"go run ./cmd/svc"}},
		{"npm", "name: site\ntype: npm\n", []string{"npm run dev"}},
		{
			"go-npm",
			"name: sr\ntype: go-npm\ngo:\n  dir: backend\n  package: ./cmd/server\nnpm:\n  dir: frontend\n",
			[]string{"go run ./cmd/server", "npm run dev"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := mustLoad(t, tc.body)
			if len(c.Dev.Processes) != len(tc.want) {
				t.Fatalf("Processes = %+v, want %d 個", c.Dev.Processes, len(tc.want))
			}
			for i, want := range tc.want {
				if got := c.Dev.Processes[i].Run.String(); got != want {
					t.Errorf("Processes[%d].Run = %q, want %q", i, got, want)
				}
			}
		})
	}
}

func TestDevProcessesExplicit(t *testing.T) {
	c := mustLoad(t, `
name: sr
type: go-npm
go:
  dir: backend
npm:
  dir: frontend
dev:
  open: http://localhost:5173
  processes:
    - name: backend
      dir: backend
      run: go run ./cmd/server
      ports: [8080, 8081]
    - name: frontend
      dir: frontend
      run: [npm, run, dev]
      ports: [5173]
`)
	if len(c.Dev.Processes) != 2 {
		t.Fatalf("Processes = %+v", c.Dev.Processes)
	}
	if got := c.Dev.Processes[0].Run.String(); got != "go run ./cmd/server" {
		t.Errorf("字串形式的 run 解錯了：%q", got)
	}
	if got := c.Dev.Processes[1].Run.String(); got != "npm run dev" {
		t.Errorf("清單形式的 run 解錯了：%q", got)
	}
	if got := c.Dev.Processes[0].ReadyPort(); got != 8080 {
		t.Errorf("ReadyPort() = %d, want 8080（沒寫 ready 就取第一個埠）", got)
	}
	if c.Dev.Open != "http://localhost:5173" {
		t.Errorf("Open = %q", c.Dev.Open)
	}
}

func TestTestCommands(t *testing.T) {
	c := mustLoad(t, `
name: svc
type: go
test:
  commands:
    - name: 資料庫測試
      dir: backend
      run: go test -tags db ./...
      env: [TEST_MYSQL_URL=x]
`)
	if len(c.Test.Commands) != 1 {
		t.Fatalf("Commands = %+v", c.Test.Commands)
	}
	cmd := c.Test.Commands[0]
	if cmd.Run.String() != "go test -tags db ./..." {
		t.Errorf("Run = %q", cmd.Run)
	}
	if len(cmd.Env) != 1 || cmd.Env[0] != "TEST_MYSQL_URL=x" {
		t.Errorf("Env = %v", cmd.Env)
	}
}

func TestStepValidationRejects(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"process without run", "name: x\ntype: go\ndev:\n  processes:\n    - name: a\n", "少了 `run`"},
		{"duplicate process names", "name: x\ntype: go\ndev:\n  processes:\n    - {name: a, run: sleep 1}\n    - {name: a, run: sleep 2}\n", "重複的 name"},
		{"bad port", "name: x\ntype: go\ndev:\n  processes:\n    - {name: a, run: sleep 1, ports: [99999]}\n", "不是合法的埠"},
		{"env without equals", "name: x\ntype: go\ndev:\n  processes:\n    - {name: a, run: sleep 1, env: [BAD]}\n", "不是 `KEY=value`"},
		{"dir escaping the repo", "name: x\ntype: go\ndev:\n  processes:\n    - {name: a, run: sleep 1, dir: ../x}\n", "倉庫之外"},
		{"command without run", "name: x\ntype: go\ntest:\n  commands:\n    - name: a\n", "少了 `run`"},
		{"clean.extra escaping", "name: x\ntype: go\nclean:\n  extra: [../oops]\n", "倉庫之外"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(t, ".hoshi-build.yaml", tc.body)
			if err == nil {
				t.Fatal("預期報錯")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, 想看到 %q", err, tc.want)
			}
		})
	}
}

// 錯誤訊息是這個解析器嚴格的理由；換成 yaml.v3 之後不能退回英文的型別名稱。
func TestYAMLErrorsAreTranslated(t *testing.T) {
	tests := []struct{ name, body, want string }{
		{"unknown nested key", "name: x\ntype: go\ngo:\n  packge: ./cmd/x\n", "不認得的鍵 `go.packge`"},
		{"unknown top key", "name: x\ntype: go\noutupt: dist/\n", "不認得的鍵 `outupt`"},
		{"scalar given a list", "name: x\ntype: go\noutput:\n  - a\n", "期望字串，得到清單"},
		{"list given a scalar", "name: x\ntype: go\ntargets: linux/amd64\n", "期望清單，得到字串"},
		{"section given a scalar", "name: x\ntype: go\ngo: backend\n", "期望區段，得到字串"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(t, ".hoshi-build.yaml", tc.body)
			if err == nil {
				t.Fatal("預期報錯")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, 想看到 %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "行") {
				t.Errorf("error = %q，訊息裡沒有行號", err)
			}
		})
	}
}

func TestSplitArgs(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"go run ./cmd/x", []string{"go", "run", "./cmd/x"}},
		{"  npm   run  dev ", []string{"npm", "run", "dev"}},
		{`go run . --db "sqlite://a b.db"`, []string{"go", "run", ".", "--db", "sqlite://a b.db"}},
		{`echo 'single quoted'`, []string{"echo", "single quoted"}},
		{`a "" b`, []string{"a", "", "b"}},
	}
	for _, tc := range tests {
		got, err := SplitArgs(tc.in)
		if err != nil {
			t.Errorf("SplitArgs(%q) error = %v", tc.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("SplitArgs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := SplitArgs(`a "unterminated`); err == nil {
		t.Error("引號沒收尾應該報錯")
	}
}
