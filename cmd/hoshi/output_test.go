package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Exercise the actual flag parser and process exit on the current platform.
// Windows CI runs this same binary natively, rather than only cross-compiling it.
func TestBuildOutputCLI(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "hoshi")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	emptyPath := t.TempDir()
	run := func(t *testing.T, args ...string) (string, error) {
		t.Helper()
		command := exec.CommandContext(ctx, bin, args...)
		// Invalid output must be diagnosed before Go/Git/npm availability.
		command.Env = append(os.Environ(), "PATH="+emptyPath)
		output, err := command.CombinedOutput()
		return string(output), err
	}
	write := func(t *testing.T, root, name, body string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("invalid-before-tools-or-clean", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, ".hoshi-build.yaml", "name: demo\ntype: go\n")
		write(t, root, "dist/keep.txt", "existing output")
		child := filepath.Join(root, "nested")
		if err := os.MkdirAll(child, 0o755); err != nil {
			t.Fatal(err)
		}
		invalid := []string{
			filepath.Join(root, "dist"), "C:/release", `D:\release`, "C:relative",
			`\\server\share\release`, "//server/share/release", `\root-relative`, "/posix-absolute",
			"../outside", `..\outside`, `nested\..\..\outside`,
		}
		for _, value := range invalid {
			t.Run(value, func(t *testing.T) {
				before := cliOutputTree(t, root)
				output, err := run(t, "build", "-C", child, "-output", value, "-clean")
				if err == nil || !strings.Contains(output, "-output") || !strings.Contains(output, "倉庫內相對路徑") {
					t.Fatalf("invalid override error=%v output=%q, want actionable path refusal before tools", err, output)
				}
				if after := cliOutputTree(t, root); !reflect.DeepEqual(after, before) {
					t.Fatalf("invalid CLI override changed files: before=%v after=%v", before, after)
				}
			})
		}
	})

	for _, tc := range []struct{ name, configured, override, want string }{
		{"default", "", "", "dist"},
		{"configured", "release-files", "", "release-files"},
		{"override-from-C", "release-files", "dist/release", "dist/release"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			body := "name: static-demo\ntype: npm\nnpm:\n  dir: frontend\n  output: built\n"
			if tc.configured != "" {
				body += "output: " + tc.configured + "\n"
			}
			write(t, root, ".hoshi-build.yaml", body)
			write(t, root, "frontend/built/index.html", "<p>actual static artifact</p>")
			child := filepath.Join(root, "nested")
			if err := os.MkdirAll(child, 0o755); err != nil {
				t.Fatal(err)
			}
			args := []string{"build", "-C", child, "-skip-npm"}
			if tc.override != "" {
				args = append(args, "-output", tc.override)
			}
			if output, err := run(t, args...); err != nil {
				t.Fatalf("valid output: %v\n%s", err, output)
			}
			got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(tc.want), "index.html"))
			if err != nil || string(got) != "<p>actual static artifact</p>" {
				t.Fatalf("artifact=%q err=%v", got, err)
			}
			if _, err := os.Stat(filepath.Join(child, filepath.FromSlash(tc.want))); !os.IsNotExist(err) {
				t.Fatalf("-C resolved output beneath the child instead of the loaded repository: %v", err)
			}
		})
	}
}

func cliOutputTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			tree[rel] = "directory"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tree[rel] = string(data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return tree
}
