package build

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedClock pins built_at so a test can assert the exact string the standard
// asks for rather than a shape that happens to match.
func fixedClock(t *testing.T) {
	t.Helper()
	saved := now
	now = func() time.Time { return time.Date(2026, 9, 8, 4, 5, 6, 0, time.UTC) }
	t.Cleanup(func() { now = saved })
}

func gitRunner(commit, status string) *fakeRunner {
	return &fakeRunner{
		onRun: touchOutput,
		captures: map[string]string{
			"git rev-parse HEAD":     commit,
			"git status --porcelain": status,
			"go env GOVERSION":       "go1.24.7",
		},
	}
}

func readDescriptor(t *testing.T, path string) map[string]any {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("讀描述子失敗：%v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("描述子不是合法 JSON：%v", err)
	}
	return out
}

const cleanCommit = "9f2c1b7a4e6d0938c5a1f2e3b4d5c6a7089e1f20"

// Every build field the standard marks 必須 has to be there, with the value the
// standard asks for. A descriptor that is merely well-formed JSON is worth
// nothing: the whole point is that a second implementation can read it.
func TestDescriptorCarriesTheRequiredBuildFields(t *testing.T) {
	fixedClock(t)
	root := testRepo(t, "name: demo-api\ntype: go\noutput: dist/\ntargets:\n  - linux/amd64\n", nil)
	c := loadRepo(t, root)

	res, err := Run(context.Background(), c, quietPrinter(), gitRunner(cleanCommit, ""),
		Options{ToolVersion: "v0.9.0"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := filepath.Join(root, "dist", "demo-api-linux-amd64.release.json")
	if res.Artifacts[0].Descriptor != want {
		t.Fatalf("Descriptor = %q, want %q", res.Artifacts[0].Descriptor, want)
	}

	d := readDescriptor(t, want)
	for key, expected := range map[string]any{
		"schema":   "hoshi.release/v1",
		"service":  "demo-api",
		"type":     "go",
		"commit":   cleanCommit,
		"dirty":    false,
		"built_at": "2026-09-08T04:05:06Z",
	} {
		if d[key] != expected {
			t.Errorf("%s = %v, want %v", key, d[key], expected)
		}
	}

	target, _ := d["target"].(map[string]any)
	if target["os"] != "linux" || target["arch"] != "amd64" {
		t.Errorf("target = %v, want linux/amd64", d["target"])
	}
	artifact, _ := d["artifact"].(map[string]any)
	if artifact["kind"] != "file" || artifact["files"] != float64(1) {
		t.Errorf("artifact = %v, want kind file / files 1", d["artifact"])
	}
	if sha, _ := artifact["sha256"].(string); len(sha) != 64 {
		t.Errorf("artifact.sha256 = %q，長度 %d；判等要用完整值（發佈標準 §4.2）",
			sha, len(sha))
	}
	builder, _ := d["builder"].(map[string]any)
	if builder["tool"] != "hoshi-build" || builder["tool_version"] != "v0.9.0" ||
		builder["go_version"] != "go1.24.7" {
		t.Errorf("builder = %v", d["builder"])
	}
	if _, found := d["deployment"]; found {
		t.Error("建置端寫了 deployment 段——那一段是部署端的（發佈標準 §2）")
	}
}

// A directory artifact's descriptor sits beside the directory. Putting it
// inside would change the hash it exists to record.
func TestDirectoryDescriptorSitsBesideNotInside(t *testing.T) {
	fixedClock(t)
	root := testRepo(t,
		"name: demo-api\ntype: go\noutput: dist/\ninclude:\n  - config.example.json\ntargets:\n  - linux/amd64\n",
		map[string]string{"config.example.json": "{}\n"})
	c := loadRepo(t, root)

	res, err := Run(context.Background(), c, quietPrinter(), gitRunner(cleanCommit, ""), Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	art := res.Artifacts[0]
	if !art.IsDir {
		t.Fatal("這個設定應該產生目錄產物")
	}
	if got := filepath.Dir(art.Descriptor); got != filepath.Dir(art.Path) {
		t.Errorf("描述子在 %s，產物在 %s——應該同層", got, filepath.Dir(art.Path))
	}
	if strings.HasPrefix(art.Descriptor, art.Path+string(filepath.Separator)) {
		t.Fatal("描述子寫進了產物目錄裡，它記的雜湊會因此改變")
	}

	// The recorded hash has to be the hash of what is actually on disk. If the
	// descriptor had landed inside, this is the assertion that would fail.
	d := readDescriptor(t, art.Descriptor)
	artifact, _ := d["artifact"].(map[string]any)
	sha, _, _, err := artifactDigest(art.Path)
	if err != nil {
		t.Fatal(err)
	}
	if artifact["sha256"] != sha {
		t.Errorf("artifact.sha256 = %v，重算是 %s", artifact["sha256"], sha)
	}
	if artifact["kind"] != "dir" {
		t.Errorf("artifact.kind = %v, want dir", artifact["kind"])
	}
}

// The directory rule has to produce the same digits as the shell pipeline
// hoshi-deploy already names release directories after. If it does not, every
// artifact reads as a mismatch from the first day.
func TestDirectoryDigestMatchesTheShellPipeline(t *testing.T) {
	for _, tool := range []string{"sh", "find", "sort", "xargs", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("沒有 %s，跳過與 shell 管線的交叉驗證", tool)
		}
	}

	root := t.TempDir()
	// Names chosen so byte order and locale-dependent order disagree: an
	// uppercase entry, and paths where one is a prefix of another.
	for name, body := range map[string]string{
		"Z":            "4",
		"a.txt":        "1",
		"a-x":          "5",
		"a/b":          "2",
		"ab/c":         "3",
		"web/app.css":  "body{color:#111}\n",
		"web/index.js": "export default 1\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	got, _, files, err := artifactDigest(root)
	if err != nil {
		t.Fatal(err)
	}
	if files != 7 {
		t.Fatalf("files = %d, want 7", files)
	}

	out, err := exec.Command("sh", "-c",
		"cd \""+root+"\" && LC_ALL=C find . -type f -print0 | LC_ALL=C sort -z"+
			" | xargs -0 sha256sum | sha256sum").Output()
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Fields(string(out))[0]
	if got != want {
		t.Errorf("artifactDigest = %s，shell 管線是 %s——節點上的版本目錄名會對不上", got, want)
	}
}

// A tree that cannot be proven clean is reported dirty. The standard forbids
// shipping a dirty artifact into a production wave, so "cannot tell" has to
// fall on the side that stops.
func TestUnprovableTreeIsReportedDirty(t *testing.T) {
	fixedClock(t)
	root := testRepo(t, "name: demo-api\ntype: go\ntargets:\n  - linux/amd64\n", nil)
	c := loadRepo(t, root)

	r := gitRunner(cleanCommit, "")
	r.failCapture = map[string]bool{"git status --porcelain": true}

	res, err := Run(context.Background(), c, quietPrinter(), r, Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if d := readDescriptor(t, res.Artifacts[0].Descriptor); d["dirty"] != true {
		t.Error("git status 問不出來，dirty 卻是 false")
	}
}

func TestDirtyTreeIsRecorded(t *testing.T) {
	fixedClock(t)
	root := testRepo(t, "name: demo-api\ntype: go\ntargets:\n  - linux/amd64\n", nil)
	c := loadRepo(t, root)

	res, err := Run(context.Background(), c, quietPrinter(),
		gitRunner(cleanCommit, " M internal/build/build.go\n"), Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if d := readDescriptor(t, res.Artifacts[0].Descriptor); d["dirty"] != true {
		t.Error("工作樹髒的，描述子卻說乾淨")
	}
}

// No git, or a short answer from it, means no commit — not a guessed one.
func TestUnresolvableCommitIsEmptyRatherThanGuessed(t *testing.T) {
	fixedClock(t)
	for name, r := range map[string]*fakeRunner{
		"沒有 git": {onRun: touchOutput, missing: map[string]bool{"git": true}},
		"短雜湊":    gitRunner("9f2c1b7", ""),
	} {
		t.Run(name, func(t *testing.T) {
			root := testRepo(t, "name: demo-api\ntype: go\ntargets:\n  - linux/amd64\n", nil)
			res, err := Run(context.Background(), loadRepo(t, root), quietPrinter(), r, Options{})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if d := readDescriptor(t, res.Artifacts[0].Descriptor); d["commit"] != "" {
				t.Errorf("commit = %v，認不出來時應該留空而不是猜", d["commit"])
			}
		})
	}
}

// type: npm has no place to put one, and the standard says so rather than
// leaving three implementations to invent three answers.
func TestNpmBuildWritesNoDescriptor(t *testing.T) {
	fixedClock(t)
	root := testRepo(t, "name: demo-site\ntype: npm\noutput: dist/\n",
		map[string]string{"package.json": "{}\n", "dist/index.html": "<!doctype html>\n"})
	c := loadRepo(t, root)

	res, err := Run(context.Background(), c, quietPrinter(), gitRunner(cleanCommit, ""), Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Artifacts[0].Descriptor != "" {
		t.Errorf("Descriptor = %q, want 空", res.Artifacts[0].Descriptor)
	}
	if _, err := os.Stat(filepath.Join(root, "dist.release.json")); !os.IsNotExist(err) {
		t.Error("在倉庫根目錄留下了 dist.release.json")
	}
}

// The descriptor records the artifact's hash, so it must not travel inside the
// archive of that artifact either — unpacking one would otherwise produce a
// tree whose hash no longer matches what the descriptor in it claims.
func TestDescriptorIsNotPackedIntoTheArchive(t *testing.T) {
	fixedClock(t)
	root := testRepo(t,
		"name: demo-api\ntype: go\noutput: dist/\narchive: tar.gz\ninclude:\n  - config.example.json\ntargets:\n  - linux/amd64\n",
		map[string]string{"config.example.json": "{}\n"})
	c := loadRepo(t, root)

	res, err := Run(context.Background(), c, quietPrinter(), gitRunner(cleanCommit, ""), Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	art := res.Artifacts[0]
	if !art.IsDir || art.Archive == "" {
		t.Fatalf("需要一個有封裝的目錄產物：IsDir=%v Archive=%q", art.IsDir, art.Archive)
	}

	f, err := os.Open(art.Archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
		if strings.HasSuffix(hdr.Name, DescriptorSuffix) {
			t.Errorf("封裝裡有 %s", hdr.Name)
		}
	}
	if len(names) == 0 {
		t.Fatal("封裝是空的，這個測試沒有驗到東西")
	}
}
