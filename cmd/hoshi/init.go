package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/ui"
)

// cmdInit writes a starter .hoshi-build.yaml by looking at what is already in
// the repository. It guesses, prints what it guessed, and never overwrites: the
// point is to save typing, not to be authoritative.
func cmdInit(_ context.Context, args []string) error {
	var (
		dir     string
		force   bool
		typ     string
		name    string
		verbose bool
	)

	fs := newFlagSet("init")
	fs.StringVar(&dir, "C", "", "在這個目錄產生設定檔")
	fs.StringVar(&typ, "type", "", "指定 type，不自動偵測：go / go-npm / npm")
	fs.StringVar(&name, "name", "", "指定 name，不自動偵測")
	fs.BoolVar(&force, "force", false, "覆寫既有的設定檔")
	fs.BoolVar(&verbose, "v", false, "印出偵測到的東西")
	if err := fs.Parse(args); err != nil {
		return errAlreadyReported
	}

	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		dir = wd
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}

	p := ui.New(os.Stdout, os.Stderr, verbose)

	for _, existing := range config.Filenames {
		if _, err := os.Stat(filepath.Join(root, existing)); err == nil && !force {
			return fmt.Errorf("%s 已存在（要覆寫請加 -force）", existing)
		}
	}

	g := detect(root)
	if typ != "" {
		g.Type = typ
	}
	if name != "" {
		g.Name = name
	}
	if g.Type == "" {
		return fmt.Errorf("認不出這個倉庫的型別：根目錄與 backend/、frontend/、web/ " +
			"底下都沒有 go.mod 或 package.json。請用 -type 指定")
	}
	if g.Name == "" {
		g.Name = sanitiseName(filepath.Base(root))
	}

	dst := filepath.Join(root, config.Filenames[0])
	if err := os.WriteFile(dst, []byte(render(g)), 0o644); err != nil {
		return err
	}

	p.OK("已產生 %s", config.Filenames[0])
	p.Note("type=%s name=%s", g.Type, g.Name)
	if g.GoDir != "" {
		p.Note("Go：%s（%s）", g.GoPackage, g.GoDir)
	}
	if g.NpmDir != "" {
		p.Note("npm：%s", g.NpmDir)
	}
	p.Note("請看過一遍再提交，然後跑 `hoshi check`")
	return nil
}

// guess is what detection managed to work out about a repository.
type guess struct {
	Name      string
	Type      string
	GoDir     string
	GoPackage string
	NpmDir    string
}

// candidate directories, in the order Hoshivel repositories actually use them.
var (
	goDirs  = []string{".", "backend"}
	npmDirs = []string{".", "frontend", "web"}
)

func detect(root string) guess {
	var g guess

	for _, d := range goDirs {
		if _, err := os.Stat(filepath.Join(root, d, "go.mod")); err == nil {
			g.GoDir = d
			break
		}
	}
	for _, d := range npmDirs {
		if g.GoDir == d && d != "." {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, d, "package.json")); err == nil {
			g.NpmDir = d
			break
		}
	}

	switch {
	case g.GoDir != "" && g.NpmDir != "":
		g.Type = config.TypeGoNpm
	case g.GoDir != "":
		g.Type = config.TypeGo
	case g.NpmDir != "":
		g.Type = config.TypeNpm
	}

	if g.GoDir != "" {
		g.Name = moduleName(filepath.Join(root, g.GoDir, "go.mod"))
		g.GoPackage = mainPackage(filepath.Join(root, g.GoDir))
	}
	if g.Name == "" && g.NpmDir != "" {
		g.Name = packageName(filepath.Join(root, g.NpmDir, "package.json"))
	}
	return g
}

// moduleName reads the last element of the module path in go.mod.
func moduleName(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module ")
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		return sanitiseName(rest[strings.LastIndex(rest, "/")+1:])
	}
	return ""
}

// packageName reads "name" out of package.json, dropping any npm scope.
func packageName(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	// Deliberately not encoding/json: package.json carries keys this tool has
	// no business parsing, and only one string is wanted.
	for _, line := range strings.Split(string(data), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), `"name"`)
		if !ok {
			continue
		}
		rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest), ":"))
		rest = strings.Trim(strings.TrimSuffix(strings.TrimSpace(rest), ","), `"`)
		return sanitiseName(rest[strings.LastIndex(rest, "/")+1:])
	}
	return ""
}

// mainPackage picks the ./cmd/<x> directory when there is exactly one. Two of
// them is a real choice, and the config file is where it should be written down
// rather than guessed.
func mainPackage(goDir string) string {
	entries, err := os.ReadDir(filepath.Join(goDir, "cmd"))
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) != 1 {
		return ""
	}
	return "./cmd/" + names[0]
}

func sanitiseName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// render writes the config. Only keys that differ from the default are
// emitted: a file full of restated defaults is a file nobody reads, and every
// restated default is one more thing that can drift from the tool.
func render(g guess) string {
	var b strings.Builder

	b.WriteString("# hoshi 設定。完整說明見\n")
	b.WriteString("# https://github.com/Hoshivel/hoshi-build/blob/main/docs/config.md\n")
	fmt.Fprintf(&b, "name: %s\n", g.Name)
	fmt.Fprintf(&b, "type: %s\n", g.Type)
	b.WriteString("output: dist/\n")

	needGo := g.GoDir != "" && (g.GoDir != "." ||
		(g.GoPackage != "" && g.GoPackage != "./cmd/"+g.Name))
	if needGo {
		b.WriteString("\ngo:\n")
		if g.GoDir != "." {
			fmt.Fprintf(&b, "  dir: %s\n", g.GoDir)
		}
		if g.GoPackage != "" && g.GoPackage != "./cmd/"+g.Name {
			fmt.Fprintf(&b, "  package: %s\n", g.GoPackage)
		}
	}

	if g.NpmDir != "" && g.NpmDir != "." {
		b.WriteString("\nnpm:\n")
		fmt.Fprintf(&b, "  dir: %s\n", g.NpmDir)
	}

	b.WriteString("\n# test 與 dev 都有預設值（見 docs/config.md §4、§5）：\n")
	b.WriteString("#   test → gofmt + go vet + go build + go test -race ./...\n")
	b.WriteString("#   dev  → go run <go.package> ＋ npm run dev\n")
	b.WriteString("# 只有和上面不一樣時才需要寫出來。\n")
	// The one thing no default can supply: only the service knows the port it
	// binds. Left unsaid, `hoshi dev -open` has nothing to open — which is
	// exactly how every repository here ended up without one.
	b.WriteString("#\n")
	b.WriteString("# 唯一推不出來的是埠——只有服務自己知道它綁哪一個。\n")
	b.WriteString("# 要用 `hoshi dev -open` 就取消底下兩行的註解：\n")
	b.WriteString("# dev:\n")
	b.WriteString("#   port: 8080\n")

	return b.String()
}
