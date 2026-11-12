package npmdeps

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/run"
	"github.com/hoshivel/hoshi-build/internal/ui"
)

type installer struct {
	t        *testing.T
	installs []string
	captures map[string]string
	fail     error
	onRun    func()
	onQuery  func(string) error
	nodeOS   string
	nodeArch string
}

func newInstaller(t *testing.T) *installer {
	return &installer{t: t, nodeOS: runtime.GOOS, nodeArch: runtime.GOARCH, captures: map[string]string{
		"node --version":         "v24.0.0",
		"npm --version":          "11.0.0",
		"npm config list --json": `{"omit":[],"ignore-scripts":false}`,
	}}
}

func (r *installer) Run(_ context.Context, c run.Cmd) error {
	r.installs = append(r.installs, c.String())
	if r.onRun != nil {
		r.onRun()
	}
	if r.fail != nil {
		return r.fail
	}
	if err := os.MkdirAll(filepath.Join(c.Dir, "node_modules"), 0o755); err != nil {
		r.t.Fatal(err)
	}
	return nil
}

func (r *installer) Capture(_ context.Context, c run.Cmd) (string, error) {
	if r.onQuery != nil {
		if err := r.onQuery(c.String()); err != nil {
			return "", err
		}
	}
	if c.Name == "node" {
		env := make(map[string]string)
		for _, entry := range os.Environ() {
			key, value, _ := strings.Cut(entry, "=")
			env[key] = value
		}
		data, err := json.Marshal(nodeInfo{
			Versions: map[string]string{"node": r.captures["node --version"]},
			Platform: r.nodeOS, Arch: r.nodeArch, Env: env,
		})
		return string(data), err
	}
	return r.captures[c.String()], nil
}

func (r *installer) Look(name string) (string, error) { return name, nil }

func put(t *testing.T, dir, name, value string) {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}

func repo(t *testing.T, locked bool) (*config.Config, *installer) {
	t.Helper()
	dir := t.TempDir()
	put(t, dir, ".hoshi-build.yaml", "name: site\ntype: npm\n")
	put(t, dir, "package.json", `{"name":"site","dependencies":{"compiler":"*"}}`)
	if locked {
		put(t, dir, "package-lock.json", `{"lockfileVersion":3,"version":"A"}`)
	}
	c, err := config.LoadFrom(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c, newInstaller(t)
}

func ensure(t *testing.T, c *config.Config, r *installer, force bool) {
	t.Helper()
	if err := Ensure(context.Background(), c, ui.New(io.Discard, io.Discard, false), r, force); err != nil {
		t.Fatal(err)
	}
}

func marker(c *config.Config) string {
	return filepath.Join(c.Root, filepath.FromSlash(c.Npm.Dir), "node_modules", recordName)
}

func assertNoRecord(t *testing.T, c *config.Config) {
	t.Helper()
	if _, err := os.Stat(marker(c)); !os.IsNotExist(err) {
		t.Fatalf("install record remains after failure: %v", err)
	}
}

func TestAutoInstallsUnrecordedTreeThenReusesSameInputs(t *testing.T) {
	c, r := repo(t, true)
	put(t, c.Root, "node_modules/old-compiler", "A")
	ensure(t, c, r, false)
	ensure(t, c, r, false)
	if strings.Join(r.installs, ",") != "npm ci" {
		t.Fatalf("commands = %v; want one install followed by reuse", r.installs)
	}
	data, err := os.ReadFile(marker(c))
	if err != nil {
		t.Fatal(err)
	}
	var got record
	if json.Unmarshal(data, &got) != nil || got.Schema != recordSchema || len(got.Fingerprint) != 64 {
		t.Fatalf("record = %s", data)
	}
}

func TestAutoReinstallsChangedInputs(t *testing.T) {
	changes := map[string]func(*testing.T, *config.Config, *installer){
		"lock bytes": func(t *testing.T, c *config.Config, _ *installer) {
			// Same size and timestamp: reuse must depend on bytes, not stat.
			st, err := os.Stat(filepath.Join(c.Root, "package-lock.json"))
			if err != nil {
				t.Fatal(err)
			}
			put(t, c.Root, "package-lock.json", `{"lockfileVersion":3,"version":"B"}`)
			if err := os.Chtimes(filepath.Join(c.Root, "package-lock.json"), st.ModTime(), st.ModTime()); err != nil {
				t.Fatal(err)
			}
		},
		"package bytes": func(t *testing.T, c *config.Config, _ *installer) {
			put(t, c.Root, "package.json", `{"name":"site","dependencies":{"compiler":"^2"}}`)
		},
		"project npmrc": func(t *testing.T, c *config.Config, _ *installer) {
			put(t, c.Root, ".npmrc", "ignore-scripts=true\n")
		},
		"node version": func(_ *testing.T, _ *config.Config, r *installer) {
			r.captures["node --version"] = "v24.1.0"
		},
		"npm version": func(_ *testing.T, _ *config.Config, r *installer) {
			r.captures["npm --version"] = "11.1.0"
		},
		"node platform": func(_ *testing.T, _ *config.Config, r *installer) {
			r.nodeOS = "other-os"
		},
		"node architecture": func(_ *testing.T, _ *config.Config, r *installer) {
			r.nodeArch = "other-arch"
		},
		"effective user config": func(_ *testing.T, _ *config.Config, r *installer) {
			r.captures["npm config list --json"] = `{"omit":["dev"],"ignore-scripts":false}`
		},
		"NODE_ENV": func(t *testing.T, _ *config.Config, _ *installer) {
			t.Setenv("NODE_ENV", os.Getenv("NODE_ENV")+"changed")
		},
		"NPM_CONFIG_OMIT": func(t *testing.T, _ *config.Config, _ *installer) {
			t.Setenv("NPM_CONFIG_OMIT", os.Getenv("NPM_CONFIG_OMIT")+"changed")
		},
		"install script environment": func(t *testing.T, _ *config.Config, _ *installer) {
			t.Setenv("HOSHI_NPM_TEST_INPUT", os.Getenv("HOSHI_NPM_TEST_INPUT")+"changed")
		},
		"lost node_modules": func(t *testing.T, c *config.Config, _ *installer) {
			if err := os.RemoveAll(filepath.Join(c.Root, "node_modules")); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			c, r := repo(t, true)
			ensure(t, c, r, false)
			change(t, c, r)
			ensure(t, c, r, false)
			ensure(t, c, r, false)
			if len(r.installs) != 2 {
				t.Fatalf("commands = %v; changed inputs need exactly one new install", r.installs)
			}
		})
	}
}

func TestInvalidRecordsAreReinstalled(t *testing.T) {
	for _, value := range []string{
		"", "{", "null", `{"schema":"future","fingerprint":"x"}`,
		`{"schema":"hoshi.npm-install/v1","fingerprint":"wrong"}`,
	} {
		t.Run(value, func(t *testing.T) {
			c, r := repo(t, true)
			ensure(t, c, r, false)
			put(t, c.Root, "node_modules/"+recordName, value)
			ensure(t, c, r, false)
			if len(r.installs) != 2 {
				t.Fatalf("invalid record reused: %v", r.installs)
			}
		})
	}
	for _, suffix := range []string{` {}`, `,"unknown":true}`} {
		t.Run("extra"+suffix, func(t *testing.T) {
			c, r := repo(t, true)
			ensure(t, c, r, false)
			data, err := os.ReadFile(marker(c))
			if err != nil {
				t.Fatal(err)
			}
			value := strings.TrimSpace(string(data))
			if strings.HasPrefix(suffix, ",") {
				value = strings.TrimSuffix(value, "}") + suffix
			} else {
				value += suffix
			}
			put(t, c.Root, "node_modules/"+recordName, value)
			ensure(t, c, r, false)
			if len(r.installs) != 2 {
				t.Fatal("record with trailing data or unknown fields reused")
			}
		})
	}
}

func TestShrinkwrapPrecedenceAndLockIdentity(t *testing.T) {
	c, r := repo(t, true)
	put(t, c.Root, "npm-shrinkwrap.json", "same lock bytes")
	ensure(t, c, r, false)
	put(t, c.Root, "package-lock.json", "ignored change")
	ensure(t, c, r, false)
	if len(r.installs) != 1 || r.installs[0] != "npm ci" {
		t.Fatalf("package-lock must be ignored when shrinkwrap exists: %v", r.installs)
	}
	put(t, c.Root, "npm-shrinkwrap.json", "new lock bytes")
	ensure(t, c, r, false)
	put(t, c.Root, "package-lock.json", "new lock bytes")
	if err := os.Remove(filepath.Join(c.Root, "npm-shrinkwrap.json")); err != nil {
		t.Fatal(err)
	}
	ensure(t, c, r, false)
	if len(r.installs) != 3 {
		t.Fatalf("lock content and lock name changes each invalidate: %v", r.installs)
	}
}

func TestShrinkwrapAloneUsesCi(t *testing.T) {
	c, r := repo(t, false)
	put(t, c.Root, "npm-shrinkwrap.json", `{"lockfileVersion":3}`)
	ensure(t, c, r, false)
	if strings.Join(r.installs, ",") != "npm ci" {
		t.Fatal(r.installs)
	}
}

func TestUnreadableShrinkwrapDoesNotFallBack(t *testing.T) {
	c, r := repo(t, true)
	if err := os.Mkdir(filepath.Join(c.Root, "npm-shrinkwrap.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := Ensure(context.Background(), c, ui.New(io.Discard, io.Discard, false), r, false)
	if err == nil || !strings.Contains(err.Error(), "npm-shrinkwrap.json") || len(r.installs) != 0 {
		t.Fatalf("error = %v, installs = %v", err, r.installs)
	}
}

func TestFailedInstallInvalidatesOldRecordEvenForSameInputs(t *testing.T) {
	for _, mode := range []string{config.InstallAlways, config.InstallAuto} {
		t.Run(mode, func(t *testing.T) {
			c, r := repo(t, true)
			ensure(t, c, r, false)
			c.Npm.Install = mode
			if mode == config.InstallAuto {
				put(t, c.Root, "package-lock.json", "B")
			}
			failure := errors.New("npm ci failed")
			r.fail = failure
			err := Ensure(context.Background(), c, ui.New(io.Discard, io.Discard, false), r, false)
			if !errors.Is(err, failure) {
				t.Fatalf("original npm failure lost: %v", err)
			}
			assertNoRecord(t, c)
			// Restoring the old lock cannot resurrect a record removed before
			// the partial install. The next auto call must actually reinstall.
			put(t, c.Root, "package-lock.json", `{"lockfileVersion":3,"version":"A"}`)
			c.Npm.Install, r.fail = config.InstallAuto, nil
			ensure(t, c, r, false)
			if len(r.installs) != 3 {
				t.Fatal(r.installs)
			}
		})
	}
}

func TestInputChangesDuringInstallAreRejected(t *testing.T) {
	for _, name := range []string{"package.json", "package-lock.json", ".npmrc", "npm-shrinkwrap.json"} {
		t.Run(name, func(t *testing.T) {
			c, r := repo(t, true)
			r.onRun = func() { put(t, c.Root, name, "changed") }
			err := Ensure(context.Background(), c, ui.New(io.Discard, io.Discard, false), r, false)
			if err == nil || !strings.Contains(err.Error(), "安裝期間") {
				t.Fatalf("input change accepted: %v", err)
			}
			assertNoRecord(t, c)
		})
	}
}

func TestEnvironmentChangesDuringInstallAreRejected(t *testing.T) {
	c, r := repo(t, true)
	r.onRun = func() { r.captures["npm --version"] = "12.0.0" }
	if err := Ensure(context.Background(), c, ui.New(io.Discard, io.Discard, false), r, false); err == nil {
		t.Fatal("changing environment was accepted")
	}
	assertNoRecord(t, c)
}

func TestUnknownEnvironmentInstallsWithoutRecording(t *testing.T) {
	for _, phase := range []string{"before", "after", "invalid config"} {
		t.Run(phase, func(t *testing.T) {
			c, r := repo(t, true)
			ensure(t, c, r, false)
			c.Npm.Install = config.InstallAlways
			queries := 0
			r.onQuery = func(string) error {
				queries++
				if phase == "before" || (phase == "after" && queries > 3) {
					return os.ErrPermission
				}
				return nil
			}
			if phase == "invalid config" {
				r.captures["npm config list --json"] = "null"
			}
			ensure(t, c, r, false)
			assertNoRecord(t, c)
			if len(r.installs) != 2 {
				t.Fatal(r.installs)
			}
		})
	}
}

func TestAlwaysNeverAndSetupOverride(t *testing.T) {
	c, r := repo(t, true)
	c.Npm.Install = config.InstallAlways
	ensure(t, c, r, false)
	ensure(t, c, r, false)
	if len(r.installs) != 2 {
		t.Fatal("always reused", r.installs)
	}
	c.Npm.Install = config.InstallNever
	r.onQuery = func(string) error { t.Fatal("never queried npm metadata"); return nil }
	ensure(t, c, r, false)
	if len(r.installs) != 2 {
		t.Fatal("never installed", r.installs)
	}
	r.onQuery = nil
	ensure(t, c, r, true)
	if len(r.installs) != 3 {
		t.Fatal("setup did not override never", r.installs)
	}
}

func TestNeverDoesNotReadLockOrWriteRecord(t *testing.T) {
	c, r := repo(t, false)
	c.Npm.Install = config.InstallNever
	if err := os.Mkdir(filepath.Join(c.Root, "npm-shrinkwrap.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	ensure(t, c, r, false)
	if len(r.installs) != 0 {
		t.Fatal(r.installs)
	}
	assertNoRecord(t, c)
}

func TestNoLockAlwaysUsesInstallAndNeverRecords(t *testing.T) {
	c, r := repo(t, false)
	ensure(t, c, r, false)
	ensure(t, c, r, false)
	if strings.Join(r.installs, ",") != "npm install,npm install" {
		t.Fatal(r.installs)
	}
	assertNoRecord(t, c)
}

func TestGeneratedLockNeedsCiBeforeReuse(t *testing.T) {
	c, r := repo(t, false)
	r.onRun = func() { put(t, c.Root, "package-lock.json", `{"lockfileVersion":3}`) }
	ensure(t, c, r, false)
	assertNoRecord(t, c)
	r.onRun = nil
	ensure(t, c, r, false)
	ensure(t, c, r, false)
	if strings.Join(r.installs, ",") != "npm install,npm ci" {
		t.Fatal(r.installs)
	}
}

func TestWorkspaceCannotReuseRootOnlyInputs(t *testing.T) {
	c, r := repo(t, true)
	put(t, c.Root, "package.json", `{"name":"site","workspaces":["packages/*"]}`)
	ensure(t, c, r, false)
	ensure(t, c, r, false)
	assertNoRecord(t, c)
	if strings.Join(r.installs, ",") != "npm ci,npm ci" {
		t.Fatal(r.installs)
	}
}

func TestWorkspaceChildCannotReuseOnlyItsOwnManifest(t *testing.T) {
	c, r := repo(t, true)
	put(t, c.Root, "package.json", `{"name":"root","workspaces":["site"]}`)
	put(t, c.Root, "site/package.json", `{"name":"site"}`)
	put(t, c.Root, "site/package-lock.json", `{"lockfileVersion":3}`)
	c.Npm.Dir = "site"
	ensure(t, c, r, false)
	ensure(t, c, r, false)
	assertNoRecord(t, c)
	if strings.Join(r.installs, ",") != "npm ci,npm ci" {
		t.Fatal(r.installs)
	}
}

func TestRecordWriteFailureLeavesNoTempOrValidRecord(t *testing.T) {
	c, r := repo(t, true)
	r.onRun = func() { put(t, c.Root, "node_modules/"+recordName+"/obstruction", "x") }
	if err := Ensure(context.Background(), c, ui.New(io.Discard, io.Discard, false), r, false); err == nil {
		t.Fatal("record write failure ignored")
	}
	files, err := filepath.Glob(filepath.Join(c.Root, "node_modules", ".hoshi-install-*.tmp"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary records remain: %v, %v", files, err)
	}
	if matches(marker(c), record{}) {
		t.Fatal("failed record treated as valid")
	}
}

func TestRecordDoesNotExposeConfigurationOrEnvironment(t *testing.T) {
	c, r := repo(t, true)
	const secret = "synthetic-test-only-credential"
	t.Setenv("HOSHI_NPM_TEST_SECRET", secret)
	r.captures["npm config list --json"] = `{"registry":"https://fixture.invalid/","token":"` + secret + `"}`
	ensure(t, c, r, false)
	data, err := os.ReadFile(marker(c))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || strings.Contains(string(data), "fixture.invalid") {
		t.Fatal("raw environment or configuration leaked to the record")
	}
}
