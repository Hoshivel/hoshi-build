package build

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/hoshivel/hoshi-build/internal/config"
)

func zipNames(t *testing.T, path string) []string {
	t.Helper()
	r, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	var names []string
	for _, f := range r.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}

func tarGzNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
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
	}
	sort.Strings(names)
	return names
}

// A directory artifact keeps its own name inside the archive, so unpacking in
// a download folder does not scatter files across it.
func TestArchiveKeepsDirectoryArtifactsUnderOneRoot(t *testing.T) {
	root := testRepo(t,
		"name: srv\ntype: go\narchive: zip\ntargets:\n  - linux/amd64\ninclude:\n  - story/\n",
		map[string]string{"story/ch1.md": "序章"})
	cfg := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	res, err := Run(context.Background(), cfg, quietPrinter(), r, Options{Version: "v1.0.0"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := filepath.Join(root, "dist", "srv-v1.0.0-linux-amd64.zip")
	if res.Artifacts[0].Archive != want {
		t.Fatalf("Archive = %q, want %q", res.Artifacts[0].Archive, want)
	}

	got := zipNames(t, want)
	expect := []string{"srv-linux-amd64/srv", "srv-linux-amd64/story/ch1.md"}
	if len(got) != len(expect) {
		t.Fatalf("內容 = %q, want %q", got, expect)
	}
	for i := range got {
		if got[i] != expect[i] {
			t.Errorf("內容 = %q, want %q", got, expect)
			break
		}
	}
}

// A lone executable has nothing to scatter, so it sits at the archive root.
func TestArchiveOfASingleFileHasNoWrapperDirectory(t *testing.T) {
	root := testRepo(t, "name: svc\ntype: go\narchive: tar.gz\ntargets:\n  - linux/amd64\n", nil)
	cfg := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	res, err := Run(context.Background(), cfg, quietPrinter(), r, Options{Version: "v2"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	got := tarGzNames(t, res.Artifacts[0].Archive)
	if len(got) != 1 || got[0] != "svc-linux-amd64" {
		t.Errorf("內容 = %q, want [svc-linux-amd64]", got)
	}
}

// An artifact that unpacks non-executable is not an artifact.
func TestArchivePreservesTheExecutableBit(t *testing.T) {
	root := testRepo(t, "name: svc\ntype: go\narchive: zip\ntargets:\n  - linux/amd64\n", nil)
	cfg := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	res, err := Run(context.Background(), cfg, quietPrinter(), r, Options{Version: "v1"})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	zr, err := zip.OpenReader(res.Artifacts[0].Archive)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()

	if len(zr.File) != 1 {
		t.Fatalf("壓縮檔有 %d 個項目", len(zr.File))
	}
	if mode := zr.File[0].Mode(); mode&0o111 == 0 {
		t.Errorf("%s 的權限是 %v，執行位元掉了", zr.File[0].Name, mode)
	}
}

func TestArchiveOverrideBeatsTheConfig(t *testing.T) {
	root := testRepo(t, "name: svc\ntype: go\ntargets:\n  - linux/amd64\n", nil)
	cfg := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	res, err := Run(context.Background(), cfg, quietPrinter(), r, Options{
		Archive: config.ArchiveZip,
		Version: "v1",
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Artifacts[0].Archive == "" {
		t.Error("--archive zip 沒有產生壓縮檔")
	}
}

func TestNoArchiveByDefault(t *testing.T) {
	root := testRepo(t, "name: svc\ntype: go\ntargets:\n  - linux/amd64\n", nil)
	cfg := loadRepo(t, root)

	r := &fakeRunner{onRun: touchOutput}
	res, err := Run(context.Background(), cfg, quietPrinter(), r, Options{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if res.Artifacts[0].Archive != "" {
		t.Errorf("Archive = %q，預設不該封裝", res.Artifacts[0].Archive)
	}
}
