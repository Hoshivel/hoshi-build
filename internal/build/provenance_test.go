package build

import (
	"context"
	"debug/buildinfo"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hoshivel/hoshi-build/internal/run"
	"github.com/hoshivel/hoshi-build/internal/toolchain"
)

func gitFixtureCommand(t *testing.T, root string, args ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("native Git provenance fixture requires git")
	}
	flags := []string{"-c", "user.name=Build fixture", "-c", "user.email=fixture@example.invalid",
		"-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + filepath.Join(root, ".git", "unused-hooks")}
	cmd := exec.Command("git", append(flags, args...)...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func initFixtureGit(t *testing.T, root string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("/dist/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitFixtureCommand(t, root, "init", "--quiet", "--object-format=sha1")
	gitFixtureCommand(t, root, "add", ".")
	gitFixtureCommand(t, root, "commit", "--quiet", "-m", "fixture source")
	return gitFixtureCommand(t, root, "rev-parse", "HEAD")
}

func TestActualVCSIdentityIsRequiredBeforeDescriptors(t *testing.T) {
	for name, settings := range map[string]string{
		"missing all":       "",
		"missing revision":  "\tbuild\tvcs=git\n\tbuild\tvcs.modified=false\n",
		"missing modified":  "\tbuild\tvcs=git\n\tbuild\tvcs.revision=" + cleanCommit + "\n",
		"wrong revision":    "\tbuild\tvcs=git\n\tbuild\tvcs.revision=" + strings.Repeat("a", 40) + "\n\tbuild\tvcs.modified=false\n",
		"wrong dirty":       "\tbuild\tvcs=git\n\tbuild\tvcs.revision=" + cleanCommit + "\n\tbuild\tvcs.modified=true\n",
		"unknown modified":  "\tbuild\tvcs=git\n\tbuild\tvcs.revision=" + cleanCommit + "\n\tbuild\tvcs.modified=unknown\n",
		"padded modified":   "\tbuild\tvcs=git\n\tbuild\tvcs.revision=" + cleanCommit + "\n\tbuild\tvcs.modified= false \n",
		"duplicate setting": "\tbuild\tvcs=git\n\tbuild\tvcs=git\n\tbuild\tvcs.revision=" + cleanCommit + "\n\tbuild\tvcs.modified=false\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := testRepo(t, "name: fixture\ntype: go\noutput: dist/\ntargets: [linux/amd64]\narchive: zip\n", nil)
			binary := filepath.Join(root, "dist", "fixture-linux-amd64")
			policy, _ := toolchain.Release()
			r := gitRunner(cleanCommit, "")
			r.captures["go version -m "+binary] = binary + ": " + policy.Toolchain + "\n" + settings
			if _, err := Run(context.Background(), loadRepo(t, root), quietPrinter(), r, Options{Verify: false}); err == nil {
				t.Fatal("unproven binary received a successful build")
			}
			assertNoDescriptors(t, root)
			if files, _ := filepath.Glob(filepath.Join(root, "dist", "*.zip")); len(files) != 0 {
				t.Fatal("unproven binary was archived", files)
			}
		})
	}
}

func assertNoDescriptors(t *testing.T, root string) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, "dist", "*"+DescriptorSuffix))
	if err != nil || len(files) != 0 {
		t.Fatalf("unexpected release descriptors %v: %v", files, err)
	}
}

func TestAllTargetsAreVerifiedBeforeFirstDescriptor(t *testing.T) {
	root := testRepo(t, "name: fixture\ntype: go\noutput: dist/\ntargets: [linux/amd64, windows/amd64]\n", nil)
	binary := filepath.Join(root, "dist", "fixture-windows-amd64.exe")
	policy, _ := toolchain.Release()
	r := gitRunner(cleanCommit, "")
	r.captures["go version -m "+binary] = binary + ": " + policy.Toolchain
	if _, err := Run(context.Background(), loadRepo(t, root), quietPrinter(), r, Options{}); err == nil {
		t.Fatal("accepted a target without actual source metadata")
	}
	assertNoDescriptors(t, root)
}

func TestGitSourceFailurePreservesExistingOutput(t *testing.T) {
	root := testRepo(t, "name: fixture\ntype: go\noutput: dist/\ntargets: [linux/amd64]\n", map[string]string{"dist/keep": "previous output"})
	r := &fakeRunner{onRun: touchOutput, missing: map[string]bool{"git": true}}
	if _, err := Run(context.Background(), loadRepo(t, root), quietPrinter(), r, Options{Clean: true}); err == nil {
		t.Fatal("unknown Git source was accepted")
	}
	if body, err := os.ReadFile(filepath.Join(root, "dist", "keep")); err != nil || string(body) != "previous output" || r.ran("go build") {
		t.Fatal("source resolution failure modified the output", err)
	}
}

func TestSourceChangeDuringBuildRefusesDescriptors(t *testing.T) {
	root := testRepo(t, "name: fixture\ntype: go\noutput: dist/\ntargets: [linux/amd64]\n", nil)
	r := gitRunner(cleanCommit, "")
	r.onRun = func(c run.Cmd) error {
		if err := touchOutput(c); err != nil {
			return err
		}
		if c.Name == "go" && len(c.Args) > 0 && c.Args[0] == "build" {
			r.captures["git rev-parse HEAD"] = strings.Repeat("b", 40)
		}
		return nil
	}
	if _, err := Run(context.Background(), loadRepo(t, root), quietPrinter(), r, Options{}); err == nil || !strings.Contains(err.Error(), "建置期間改變") {
		t.Fatalf("source changed without rejection: %v", err)
	}
	assertNoDescriptors(t, root)
}

type suppressVCSRunner struct{ run.Runner }

func (r suppressVCSRunner) Run(ctx context.Context, c run.Cmd) error {
	if c.Name == "go" && len(c.Args) > 0 && c.Args[0] == "build" {
		// Simulate a toolchain that completes compilation without VCS settings.
		args := append([]string{}, c.Args[:len(c.Args)-1]...)
		c.Args = append(args, "-buildvcs=false", c.Args[len(c.Args)-1])
	}
	return r.Runner.Run(ctx, c)
}

func nativeProvenanceRepo(t *testing.T, extra map[string]string) (string, string) {
	t.Helper()
	requireToolchain(t)
	t.Setenv("GOFLAGS", "-mod=mod -buildvcs=false")
	files := map[string]string{"go.mod": "module fixture\n\ngo 1.24\n", "cmd/fixture/main.go": "package main\nfunc main() {}\n"}
	for name, value := range extra {
		files[name] = value
	}
	yaml := fmt.Sprintf("name: fixture\ntype: go\noutput: dist/\ntargets: [%s/%s]\n", runtime.GOOS, runtime.GOARCH)
	if len(extra) != 0 {
		for name := range extra {
			yaml += "include: [" + name + "]\n"
		}
	}
	root := testRepo(t, yaml, files)
	return root, initFixtureGit(t, root)
}

func TestNativeBinaryWithoutVCSIsHeld(t *testing.T) {
	root, _ := nativeProvenanceRepo(t, nil)
	_, err := Run(context.Background(), loadRepo(t, root), quietPrinter(), suppressVCSRunner{run.NewExecRunner()}, Options{})
	if err == nil || !strings.Contains(err.Error(), "實際 VCS 身分缺失") {
		t.Fatalf("VCS-free executable was not held: %v", err)
	}
	name := "fixture-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	info, err := buildinfo.ReadFile(filepath.Join(root, "dist", name))
	if err != nil {
		t.Fatal(err)
	}
	policy, _ := toolchain.Release()
	if info.GoVersion != policy.Toolchain {
		t.Fatal("fixture did not use the accepted release compiler", info.GoVersion)
	}
	for _, setting := range info.Settings {
		if strings.HasPrefix(setting.Key, "vcs") {
			t.Fatal("fixture unexpectedly contains VCS settings", setting)
		}
	}
	assertNoDescriptors(t, root)
}

func TestNativeGitCheckoutAndWorktreeAgreeWithActualBinary(t *testing.T) {
	root, commit := nativeProvenanceRepo(t, nil)
	for _, kind := range []string{"checkout", "worktree"} {
		t.Run(kind, func(t *testing.T) {
			dir := root
			if kind == "worktree" {
				dir = filepath.Join(t.TempDir(), "checkout")
				gitFixtureCommand(t, root, "worktree", "add", "--detach", dir, "HEAD")
			}
			result, err := Run(context.Background(), loadRepo(t, dir), quietPrinter(), run.NewExecRunner(), Options{})
			if kind == "worktree" && err != nil && strings.Contains(err.Error(), "實際 VCS 身分缺失") {
				assertNoDescriptors(t, dir)
				t.Log("toolchain omitted worktree VCS; producer correctly held the binary")
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			info, err := buildinfo.ReadFile(result.Artifacts[0].Bin)
			if err != nil {
				t.Fatal(err)
			}
			settings := make(map[string]string)
			for _, setting := range info.Settings {
				settings[setting.Key] = setting.Value
			}
			d := readDescriptor(t, result.Artifacts[0].Descriptor)
			if settings["vcs.revision"] != commit || settings["vcs.modified"] != "false" || d["commit"] != commit || d["dirty"] != false {
				t.Fatalf("binary/descriptor provenance differs: %v / %v", settings, d)
			}
		})
	}
}

func TestAssembledIncludeCannotReplaceTheCheckedExecutable(t *testing.T) {
	name := "fixture"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	root, _ := nativeProvenanceRepo(t, map[string]string{name: "an included file is not the program"})
	if _, err := Run(context.Background(), loadRepo(t, root), quietPrinter(), run.NewExecRunner(), Options{Verify: false}); err == nil {
		t.Fatal("an include replaced the binary after its early compiler check")
	}
	assertNoDescriptors(t, root)
}
