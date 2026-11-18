package build

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/hoshivel/hoshi-build/internal/config"
)

func TestIncludeExclusionsDoNotShipInDirectoriesOrArchives(t *testing.T) {
	source := map[string]string{
		"story/themes/opening.json":         "{\"id\":\"opening\"}",
		"story/drafts/notes/early.md":       "editorial draft",
		"story/drafts-current/reviewed.md":  "retained adjacent directory",
		"story/drafts.md":                   "retained adjacent file",
		"story/scenes/sample.json":          "excluded single file",
		"story/scenes/sample-reviewed.json": "retained adjacent scene",
		"other/drafts/asset.txt":            "different include root",
		"frontend/dist/story/drafts/a.txt":  "frontend content",
	}
	root := testRepo(t, `
name: srv
type: go-npm
include: [story/, other/]
include_exclude: [./story/drafts/, story/scenes/sample.json, story/not-created]
npm:
  dir: frontend
`, source)
	if runtime.GOOS != "windows" {
		// Copying or validating the excluded subtree would encounter this link.
		if err := os.Symlink(filepath.Join(root, "story", "drafts.md"), filepath.Join(root, "story", "drafts", "link")); err != nil {
			t.Fatal(err)
		}
	}
	c := loadRepo(t, root)
	for _, tc := range []struct {
		os, format, binary string
	}{
		{os: "linux", format: config.ArchiveTarGz, binary: "srv"},
		{os: "windows", format: config.ArchiveZip, binary: "srv.exe"},
	} {
		t.Run(tc.os, func(t *testing.T) {
			r := &fakeRunner{onRun: touchOutput}
			res, err := Run(context.Background(), c, quietPrinter(), r, Options{
				Targets: []config.Target{{OS: tc.os, Arch: "amd64"}}, Archive: tc.format, SkipNpm: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			artifact := res.Artifacts[0]
			expected := map[string]string{
				tc.binary:                           "#!/bin/false\n",
				"story/themes/opening.json":         source["story/themes/opening.json"],
				"story/drafts-current/reviewed.md":  source["story/drafts-current/reviewed.md"],
				"story/drafts.md":                   source["story/drafts.md"],
				"story/scenes/sample-reviewed.json": source["story/scenes/sample-reviewed.json"],
				"other/drafts/asset.txt":            source["other/drafts/asset.txt"],
				"web/story/drafts/a.txt":            source["frontend/dist/story/drafts/a.txt"],
			}
			if _, err := os.Lstat(filepath.Join(artifact.Path, "story", "drafts")); !os.IsNotExist(err) {
				t.Fatalf("excluded directory exists in the artifact: %v", err)
			}
			directory := map[string]string{}
			err = fs.WalkDir(os.DirFS(artifact.Path), ".", func(name string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return err
				}
				body, err := os.ReadFile(filepath.Join(artifact.Path, filepath.FromSlash(name)))
				directory[name] = string(body)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(directory, expected) {
				t.Fatalf("artifact contents = %q, want %q", directory, expected)
			}
			wantArchive := map[string]string{}
			for name, body := range expected {
				wantArchive[path.Join(filepath.Base(artifact.Path), name)] = body
			}
			if got := includeExclusionArchiveContents(t, artifact.Archive, tc.format); !reflect.DeepEqual(got, wantArchive) {
				t.Fatalf("archive contents = %q, want %q", got, wantArchive)
			}
		})
	}
	for name, want := range source {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil || string(got) != want {
			t.Errorf("source %s = %q, %v; want %q", name, got, err, want)
		}
	}
}

func TestIncludeExclusionsKeepSymlinkRejection(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires administrator privileges or developer mode on Windows")
	}
	root := testRepo(t, `
name: srv
type: go
include: [story]
include_exclude: [story/drafts]
targets: [linux/amd64]
`, map[string]string{"story/visible.txt": "visible", "outside.txt": "outside"})
	if err := os.Symlink(filepath.Join(root, "outside.txt"), filepath.Join(root, "story", "drafts")); err != nil {
		t.Fatal(err)
	}
	_, err := Run(context.Background(), loadRepo(t, root), quietPrinter(), &fakeRunner{onRun: touchOutput}, Options{})
	if err == nil || !strings.Contains(err.Error(), "是符號連結") {
		t.Fatalf("Run() error = %v, want a symlink rejection", err)
	}
}

func includeExclusionArchiveContents(t *testing.T, name, format string) map[string]string {
	t.Helper()
	files := map[string]string{}
	if format == config.ArchiveZip {
		zr, err := zip.OpenReader(name)
		if err != nil {
			t.Fatal(err)
		}
		defer zr.Close()
		for _, entry := range zr.File {
			body, err := fs.ReadFile(zr, entry.Name)
			if err != nil {
				t.Fatal(err)
			}
			files[entry.Name] = string(body)
		}
		return files
	}

	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	tr := tar.NewReader(z)
	for {
		entry, err := tr.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name] = string(body)
	}
}
