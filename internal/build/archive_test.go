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
	"github.com/hoshivel/hoshi-build/internal/run"
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

// A host filesystem that cannot represent the executable bit must not get to
// decide what lands in the archive. NTFS is that filesystem — `os.Stat` reports
// 0666 for every regular file — and this organisation builds on Windows and
// deploys to Debian, so that is the normal path.
//
// The assertion is written to fail **everywhere**, not just on Windows: the
// test above (TestArchivePreservesTheExecutableBit) only fails on a host
// without the bit, and a test that only fails on one platform is only found by
// that platform's job. This one fakes the condition instead of needing it.
func TestArchiveForcesTheExecutableBitTheHostCannotStore(t *testing.T) {
	for _, tc := range []struct {
		format string
		names  func(t *testing.T, archive string) []os.FileMode
	}{
		{config.ArchiveZip, zipModes},
		{config.ArchiveTarGz, tarGzModes},
	} {
		t.Run(tc.format, func(t *testing.T) {
			root := testRepo(t, "name: svc\ntype: go\narchive: "+tc.format+
				"\ntargets:\n  - linux/amd64\n", nil)
			cfg := loadRepo(t, root)

			// 0o644 is what a build on Windows leaves behind.
			r := &fakeRunner{onRun: touchOutputMode(0o644)}
			res, err := Run(context.Background(), cfg, quietPrinter(), r, Options{Version: "v1"})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			modes := tc.names(t, res.Artifacts[0].Archive)
			if len(modes) != 1 {
				t.Fatalf("壓縮檔有 %d 個項目", len(modes))
			}
			if modes[0]&0o111 == 0 {
				t.Errorf("權限是 %v，執行位元掉了——磁碟上沒有那個位元時，"+
					"它必須由「這是執行檔」推得，而不是照抄檔案系統", modes[0])
			}
		})
	}
}

// touchOutputMode writes the built file with a given permission, so a test can
// stand in for a filesystem that cannot store the executable bit.
func touchOutputMode(perm os.FileMode) func(run.Cmd) error {
	return func(c run.Cmd) error {
		for i, a := range c.Args {
			if a == "-o" && i+1 < len(c.Args) {
				if err := os.MkdirAll(filepath.Dir(c.Args[i+1]), 0o755); err != nil {
					return err
				}
				return os.WriteFile(c.Args[i+1], []byte("#!/bin/false\n"), perm)
			}
		}
		return nil
	}
}

func zipModes(t *testing.T, archive string) []os.FileMode {
	t.Helper()
	zr, err := zip.OpenReader(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	out := make([]os.FileMode, 0, len(zr.File))
	for _, f := range zr.File {
		out = append(out, f.Mode())
	}
	return out
}

func tarGzModes(t *testing.T, archive string) []os.FileMode {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	var out []os.FileMode
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, os.FileMode(hdr.Mode))
	}
	return out
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
