package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/toolchain"
)

func TestReleaseCompilerSelectionIsProcessLocalAndProven(t *testing.T) {
	t.Setenv("GOTOOLCHAIN", "local")
	root := testRepo(t, "name: fixture\ntype: go\noutput: dist/\ntargets: [linux/amd64]\n", nil)
	r := &fakeRunner{onRun: touchOutput}
	result, err := Run(context.Background(), loadRepo(t, root), quietPrinter(), r, Options{})
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := toolchain.Release()
	for _, c := range r.calls {
		if c.Name != "go" {
			continue
		}
		found := false
		for _, e := range c.Env {
			if e == "GOTOOLCHAIN="+policy.Toolchain {
				found = true
			}
		}
		if !found {
			t.Fatal("Go command escaped the fixed release compiler", c)
		}
	}
	if os.Getenv("GOTOOLCHAIN") != "local" || len(result.Artifacts) != 1 {
		t.Fatal("release selection changed the caller's environment")
	}
	d := readDescriptor(t, result.Artifacts[0].Descriptor)
	if d["builder"].(map[string]any)["go_version"] != policy.Toolchain {
		t.Fatal("descriptor did not record the verified compiler")
	}
}

func TestReleaseCompilerFailurePreservesExistingOutput(t *testing.T) {
	root := testRepo(t, "name: fixture\ntype: go\noutput: dist/\ntargets: [linux/amd64]\n", map[string]string{"dist/keep": "existing artifact"})
	r := &fakeRunner{onRun: touchOutput, captures: map[string]string{"go env GOVERSION": "go1.26.5"}}
	if _, err := Run(context.Background(), loadRepo(t, root), quietPrinter(), r, Options{Clean: true}); err == nil {
		t.Fatal("accepted the old host compiler")
	}
	if body, err := os.ReadFile(filepath.Join(root, "dist", "keep")); err != nil || string(body) != "existing artifact" || r.ran("go build") {
		t.Fatal("compiler resolution failure modified output", err)
	}
}

func TestReleaseRejectsArtifactBuiltByAnotherCompiler(t *testing.T) {
	root := testRepo(t, "name: fixture\ntype: go\noutput: dist/\ntargets: [linux/amd64]\n", nil)
	binary := filepath.Join(root, "dist", "fixture-linux-amd64")
	r := &fakeRunner{onRun: touchOutput, captures: map[string]string{"go version -m " + binary: binary + ": go1.26.5"}}
	if _, err := Run(context.Background(), loadRepo(t, root), quietPrinter(), r, Options{}); err == nil || !strings.Contains(err.Error(), "編譯器") {
		t.Fatal("mismatched artifact compiler was not rejected", err)
	}
	if _, err := os.Stat(binary + DescriptorSuffix); !os.IsNotExist(err) {
		t.Fatal("mismatched artifact received a success descriptor", err)
	}
}

func TestNpmBuildDoesNotResolveGoCompiler(t *testing.T) {
	root := testRepo(t, "name: fixture\ntype: npm\noutput: dist/\nnpm:\n  output: web/dist\n", map[string]string{"web/dist/index.html": "ok"})
	cfg := loadRepo(t, root)
	if cfg.Type != config.TypeNpm {
		t.Fatal(cfg.Type)
	}
	r := &fakeRunner{missing: map[string]bool{"go": true}}
	if _, err := Run(context.Background(), cfg, quietPrinter(), r, Options{SkipNpm: true}); err != nil || r.ran("go ") {
		t.Fatal("static build unexpectedly resolved a Go toolchain", err)
	}
}

func TestSkippedBackendNeverRelabelsExistingCompiler(t *testing.T) {
	old := `{"builder":{"go_version":"go1.26.5"}}`
	root := testRepo(t, "name: fixture\ntype: go\noutput: dist/\ntargets: [linux/amd64]\n", map[string]string{
		"dist/fixture-linux-amd64":              "old binary",
		"dist/fixture-linux-amd64.release.json": old,
	})
	r := &fakeRunner{captures: map[string]string{"go env GOVERSION": "go1.26.10"}}
	result, err := Run(context.Background(), loadRepo(t, root), quietPrinter(), r, Options{SkipGo: true})
	if err != nil || len(result.Artifacts) != 0 || r.ran("go build") || r.ran("go version -m") {
		t.Fatal("skipped backend was treated as a new artifact", result, err)
	}
	if body, err := os.ReadFile(filepath.Join(root, "dist", "fixture-linux-amd64.release.json")); err != nil || string(body) != old {
		t.Fatal("old descriptor was relabelled with the selected host compiler", err)
	}
}
