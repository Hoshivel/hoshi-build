// Package npmdeps owns the shared npm install policy for build, test and dev.
package npmdeps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/run"
	"github.com/hoshivel/hoshi-build/internal/ui"
)

const (
	recordName     = ".hoshi-install.json"
	recordSchema   = "hoshi.npm-install/v1"
	nodeInfoScript = "process.stdout.write(JSON.stringify({versions:process.versions,platform:process.platform,arch:process.arch,env:process.env}))"
)

type inputs struct {
	Package string
	Lock    string
	Locked  string
	Npmrc   string
}

type record struct {
	Schema      string `json:"schema"`
	Fingerprint string `json:"fingerprint"`
}

type nodeInfo struct {
	Versions map[string]string `json:"versions"`
	Platform string            `json:"platform"`
	Arch     string            `json:"arch"`
	Env      map[string]string `json:"env"`
}

// Ensure installs through npm itself. A record only binds a successful install
// to its inputs; it does not attest to the current contents of node_modules.
// force is used by setup, which overrides even npm.install=never.
func Ensure(ctx context.Context, c *config.Config, p *ui.Printer, r run.Runner, force bool) error {
	dir := filepath.Join(c.Root, filepath.FromSlash(c.Npm.Dir))
	if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
		return fmt.Errorf("%s 沒有 package.json（npm.dir = %q）", c.Npm.Dir, c.Npm.Dir)
	}
	if !force && c.Npm.Install == config.InstallNever {
		p.Note("npm.install=never，跳過安裝相依")
		return nil
	}
	before, err := readInputs(dir)
	if err != nil {
		return err
	}

	modules := filepath.Join(dir, "node_modules")
	marker := filepath.Join(modules, recordName)
	// Without a lock, npm install can resolve new inputs. Do not certify that
	// result using a lock created or changed during the install itself.
	environment := ""
	if before.Lock != "" && singlePackage(dir) {
		environment, err = environmentKey(ctx, dir, r)
		if err != nil {
			p.Note("無法核對 npm 安裝環境，這次安裝不建立重用記錄")
		}
	}
	want := record{Schema: recordSchema, Fingerprint: fingerprint(before, environment)}
	if !force && c.Npm.Install == config.InstallAuto && environment != "" &&
		isDir(modules) && matches(marker, want) {
		p.Note("npm 安裝輸入相符，跳過安裝（npm.install=auto）")
		return nil
	}

	// Invalidate before npm can touch the tree, including forced installs with
	// the same inputs. A failed or interrupted install must never reuse it.
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("無法移除 npm 安裝記錄：%w", err)
	}
	verb := "install"
	if before.Lock != "" {
		verb = "ci"
	}
	p.Step("npm %s", verb)
	rel, _ := filepath.Rel(c.Root, dir)
	p.Command(filepath.ToSlash(rel), nil, "npm", []string{verb})
	if err := r.Run(ctx, run.Cmd{Dir: dir, Name: "npm", Args: []string{verb}}); err != nil {
		return err
	}

	if before.Lock != "" {
		after, err := readInputs(dir)
		if err != nil {
			return err
		}
		if before != after {
			return fmt.Errorf("npm 安裝期間 package.json、lockfile 或 .npmrc 改變，請重新執行")
		}
		if environment != "" && isDir(modules) {
			afterEnvironment, err := environmentKey(ctx, dir, r)
			if err != nil {
				p.Note("無法核對安裝後的 npm 環境，不建立重用記錄")
			} else if afterEnvironment != environment {
				return fmt.Errorf("npm 安裝期間環境改變，請重新執行")
			} else if err := writeRecord(marker, want); err != nil {
				return fmt.Errorf("無法寫入 npm 安裝記錄：%w", err)
			}
		}
	}
	p.OK("node_modules 就緒")
	return nil
}

func readInputs(dir string) (inputs, error) {
	var in inputs
	packageBytes, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return in, fmt.Errorf("讀取 npm.dir 的 package.json：%w", err)
	}
	in.Package = digest(packageBytes)
	// npm ignores package-lock.json when npm-shrinkwrap.json is present.
	for _, name := range []string{"npm-shrinkwrap.json", "package-lock.json"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return in, fmt.Errorf("讀取 %s：%w", name, err)
		}
		in.Lock, in.Locked = name, digest(data)
		break
	}
	data, err := os.ReadFile(filepath.Join(dir, ".npmrc"))
	if err != nil && !os.IsNotExist(err) {
		return in, fmt.Errorf("讀取 .npmrc：%w", err)
	}
	if err == nil {
		in.Npmrc = digest(data)
	}
	return in, nil
}

// Workspace manifests are additional install inputs. Until they are collected,
// always let npm check them instead of reusing a root-only fingerprint.
func singlePackage(dir string) bool {
	for path := dir; ; path = filepath.Dir(path) {
		data, err := os.ReadFile(filepath.Join(path, "package.json"))
		if err != nil && (path == dir || !os.IsNotExist(err)) {
			return false
		}
		if err == nil {
			var manifest map[string]json.RawMessage
			if json.Unmarshal(data, &manifest) != nil {
				return false
			}
			if _, workspace := manifest["workspaces"]; workspace {
				return false
			}
		}
		if filepath.Dir(path) == path {
			return true
		}
	}
}

func environmentKey(ctx context.Context, dir string, r run.Runner) (string, error) {
	// Ask the child for its environment so Runner overrides are included too.
	info, err := r.Capture(ctx, run.Cmd{Dir: dir, Name: "node", Args: []string{"--eval", nodeInfoScript}})
	if err != nil {
		return "", err
	}
	var node nodeInfo
	if json.Unmarshal([]byte(info), &node) != nil || node.Versions["node"] == "" ||
		node.Platform == "" || node.Arch == "" || len(node.Env) == 0 {
		return "", fmt.Errorf("無法取得 Node.js 安裝環境")
	}
	version, err := r.Capture(ctx, run.Cmd{Dir: dir, Name: "npm", Args: []string{"--version"}})
	if err != nil || strings.TrimSpace(version) == "" {
		return "", fmt.Errorf("無法取得 npm 版本")
	}
	settings, err := r.Capture(ctx, run.Cmd{Dir: dir, Name: "npm", Args: []string{"config", "list", "--json"}})
	if err != nil {
		return "", err
	}
	var effective map[string]json.RawMessage
	if err := json.Unmarshal([]byte(settings), &effective); err != nil || len(effective) == 0 {
		return "", fmt.Errorf("無法取得 npm 有效設定")
	}
	// Hash, never persist or print, configuration and inherited environment:
	// both can contain credentials. JSON map keys are sorted by the encoder.
	data, err := json.Marshal(struct {
		Node     nodeInfo
		Npm      string
		Settings map[string]json.RawMessage
	}{node, version, effective})
	if err != nil {
		return "", err
	}
	return digest(data), nil
}

func fingerprint(in inputs, environment string) string {
	data, _ := json.Marshal(struct {
		Inputs      inputs
		Environment string
	}{in, environment})
	return digest(data)
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

func matches(path string, want record) bool {
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 4096 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	var got record
	if d.Decode(&got) != nil || got != want {
		return false
	}
	return d.Decode(new(any)) == io.EOF
}

func writeRecord(path string, value record) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".hoshi-install-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
