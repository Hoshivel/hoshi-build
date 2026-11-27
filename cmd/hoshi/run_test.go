package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRunCLIFixture(t *testing.T) {
	mode := os.Getenv("HOSHI_NAMED_FIXTURE")
	if mode == "" {
		return
	}
	if mode == "heartbeat" {
		for i := 0; ; i++ {
			if os.WriteFile(os.Getenv("HOSHI_NAMED_HEARTBEAT"), []byte(strconv.Itoa(i)), 0o600) != nil {
				os.Exit(0)
			}
			time.Sleep(40 * time.Millisecond)
		}
	}
	if mode == "tree" {
		child := exec.Command(os.Args[0], "-test.run=^TestRunCLIFixture$")
		child.Env = append(os.Environ(), "HOSHI_NAMED_FIXTURE=heartbeat")
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		_ = child.Wait()
		os.Exit(0)
	}
	if mode == "fail" {
		os.Exit(7)
	}
	wd, _ := os.Getwd()
	args := os.Args[1:]
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:]
	}
	data, _ := json.Marshal(struct {
		Dir  string
		Args []string
		Env  string
	}{wd, args, os.Getenv("HOSHI_NAMED_VALUE")})
	fmt.Printf("RESULT %s\n", data)
	os.Exit(0)
}

func TestNamedRunCLI(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "hoshi")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if output, err := exec.CommandContext(ctx, "go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tools"), 0o755); err != nil {
		t.Fatal(err)
	}
	command := map[string]any{"run": []string{os.Args[0], "-test.run=^TestRunCLIFixture$", "--", ""}, "dir": "tools", "env": []string{"HOSHI_NAMED_FIXTURE=emit", "HOSHI_NAMED_VALUE=space = value"}, "description": "Independent service"}
	body, _ := json.Marshal(map[string]any{"name": "fixture", "type": "npm", "commands": map[string]any{"server": command, "alpha": map[string]any{"run": []string{"must-not-run"}}}})
	if err := os.WriteFile(filepath.Join(root, ".hoshi-build.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		process := exec.CommandContext(ctx, bin, append([]string{"run", "-C", root}, args...)...)
		process.Env = append(os.Environ(), "PATH="+t.TempDir())
		output, err := process.CombinedOutput()
		return string(output), err
	}
	t.Run("list-does-not-run", func(t *testing.T) {
		output, err := run()
		if err != nil || strings.Index(output, "alpha") > strings.Index(output, "server") || !strings.Contains(output, "Independent service") || strings.Contains(output, "RESULT ") {
			t.Fatalf("error=%v output=%s", err, output)
		}
	})
	t.Run("dry-run-does-not-run", func(t *testing.T) {
		output, err := run("-dry-run", "server", "--", "two words")
		if err != nil || strings.Contains(output, "RESULT ") || !strings.Contains(output, "two words") {
			t.Fatalf("error=%v output=%s", err, output)
		}
	})
	t.Run("dry-run-after-name-does-not-start-server", func(t *testing.T) {
		output, err := run("server", "-dry-run", "--", "two words")
		if err != nil || strings.Contains(output, "RESULT ") || !strings.Contains(output, "two words") {
			t.Fatalf("error=%v output=%s", err, output)
		}
	})
	t.Run("child-arguments-require-explicit-boundary", func(t *testing.T) {
		output, err := run("server", "bare-argument")
		if err == nil || !strings.Contains(output, "--") || strings.Contains(output, "RESULT ") {
			t.Fatalf("error=%v output=%s", err, output)
		}
	})
	t.Run("missing-name-does-not-launch-forwarded-command", func(t *testing.T) {
		output, err := run("--", "server")
		if err == nil || !strings.Contains(output, "--") || strings.Contains(output, "RESULT ") {
			t.Fatalf("error=%v output=%s", err, output)
		}
	})
	t.Run("only-named-argv-env-cwd-without-dependency-tools", func(t *testing.T) {
		extra := []string{"two words", "世界", "&", "a=b", "a\"b", "-flag"}
		output, err := run(append([]string{"server", "--"}, extra...)...)
		if err != nil {
			t.Fatalf("run: %v\n%s", err, output)
		}
		var got struct {
			Dir  string
			Args []string
			Env  string
		}
		found := false
		for _, line := range strings.Split(output, "\n") {
			if i := strings.Index(line, "RESULT "); i >= 0 {
				if err := json.Unmarshal([]byte(strings.TrimSpace(line[i+7:])), &got); err != nil {
					t.Fatal(err)
				}
				found = true
			}
		}
		if !found || got.Dir != filepath.Join(root, "tools") || !reflect.DeepEqual(got.Args, append([]string{""}, extra...)) || got.Env != "space = value" {
			t.Fatalf("got=%+v output=%s", got, output)
		}
	})
	t.Run("unknown-fails-without-running", func(t *testing.T) {
		output, err := run("missing")
		if err == nil || !strings.Contains(output, "commands") || strings.Contains(output, "RESULT ") {
			t.Fatalf("error=%v output=%s", err, output)
		}
	})
	t.Run("child-error-is-not-success", func(t *testing.T) {
		// Override the configured mode in a separate fixture config.
		failBody := strings.Replace(string(body), "HOSHI_NAMED_FIXTURE=emit", "HOSHI_NAMED_FIXTURE=fail", 1)
		path := filepath.Join(root, "failure.json")
		if err := os.WriteFile(path, []byte(failBody), 0o600); err != nil {
			t.Fatal(err)
		}
		process := exec.CommandContext(ctx, bin, "run", "-config", path, "server")
		output, err := process.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "7") {
			t.Fatalf("error=%v output=%s", err, output)
		}
	})
	t.Run("cancellation-kills-grandchild", func(t *testing.T) {
		heartbeat := filepath.Join(root, "heartbeat")
		command["env"] = []string{"HOSHI_NAMED_FIXTURE=tree", "HOSHI_NAMED_HEARTBEAT=" + heartbeat}
		treeBody, _ := json.Marshal(map[string]any{"name": "fixture", "type": "npm", "commands": map[string]any{"server": command}})
		path := filepath.Join(root, "tree.json")
		if err := os.WriteFile(path, treeBody, 0o600); err != nil {
			t.Fatal(err)
		}
		childCtx, stop := context.WithCancel(ctx)
		defer stop()
		process := exec.CommandContext(childCtx, bin, "run", "-config", path, "server")
		if err := process.Start(); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(8 * time.Second)
		for {
			if _, err := os.Stat(heartbeat); err == nil {
				break
			}
			if time.Now().After(deadline) {
				stop()
				_ = process.Wait()
				t.Fatal("grandchild did not start")
			}
			time.Sleep(20 * time.Millisecond)
		}
		if runtime.GOOS == "windows" {
			// The Job Object must also cover a host that disappears abruptly.
			stop()
		} else if err := process.Process.Signal(os.Interrupt); err != nil {
			stop()
			t.Fatal(err)
		}
		_ = process.Wait()
		deadline = time.Now().Add(6 * time.Second)
		for {
			before, err := os.ReadFile(heartbeat)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(300 * time.Millisecond)
			after, err := os.ReadFile(heartbeat)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) == string(after) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("grandchild outlived process-group cleanup: %q -> %q", before, after)
			}
		}
	})
}
