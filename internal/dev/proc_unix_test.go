//go:build !windows

package dev

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/ui"
)

const (
	unixFixtureMode  = "HOSHI_DEV_UNIX_FIXTURE"
	unixFixtureReady = "HOSHI_DEV_UNIX_READY"
	unixFixtureDone  = "HOSHI_DEV_UNIX_DONE"
	unixFixturePort  = "HOSHI_DEV_UNIX_PORT"
)

// TestUnixProcessFixture is a subprocess entry point selected with -test.run.
// With no fixture environment it is an ordinary no-op test.
func TestUnixProcessFixture(t *testing.T) {
	switch os.Getenv(unixFixtureMode) {
	case "grace-parent":
		unixTreeParentFixture("grace-child", false)
	case "stubborn-closed-parent":
		unixTreeParentFixture("stubborn-closed-child", false)
	case "stubborn-pipes-parent":
		unixTreeParentFixture("stubborn-pipes-child", true)
	case "grace-child":
		unixTreeChildFixture(true, true)
	case "stubborn-closed-child":
		unixTreeChildFixture(true, false)
	case "stubborn-pipes-child":
		unixTreeChildFixture(false, false)
	}
}

func unixTreeParentFixture(childMode string, ignoreTerm bool) {
	fixtureSafetyExit()
	if ignoreTerm {
		signal.Ignore(syscall.SIGTERM)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestUnixProcessFixture$")
	cmd.Env = unixFixtureEnv(childMode)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func unixTreeChildFixture(closeOutput, finishGracefully bool) {
	fixtureSafetyExit()
	signal.Ignore(syscall.SIGTERM)
	port, err := strconv.Atoi(os.Getenv(unixFixturePort))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	ln, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer ln.Close()
	if err := os.WriteFile(os.Getenv(unixFixtureReady), []byte("ready"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if closeOutput {
		_ = os.Stdout.Close()
		_ = os.Stderr.Close()
	}
	if finishGracefully {
		time.Sleep(750 * time.Millisecond)
		if err := os.WriteFile(os.Getenv(unixFixtureDone), []byte("graceful"), 0o644); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func fixtureSafetyExit() {
	time.AfterFunc(30*time.Second, func() { os.Exit(3) })
}

func unixFixtureEnv(mode string) []string {
	prefix := unixFixtureMode + "="
	out := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, unixFixtureMode+"="+mode)
}

func TestUnixCancelAllowsClosedOutputGrandchildToFinish(t *testing.T) {
	result := runUnixCancellationFixture(t, "grace-parent")
	if body, err := os.ReadFile(result.doneMarker); err != nil || string(body) != "graceful" {
		t.Fatalf("graceful marker = %q, %v", body, err)
	}
	if result.elapsed > processStopGrace+3*time.Second {
		t.Errorf("graceful grandchild exceeded the group deadline: %s", result.elapsed)
	}
	requireUnixPortReleased(t, result.port)
}

func TestUnixCancelKillsGrandchildGroupAndReleasesPort(t *testing.T) {
	for _, mode := range []string{"stubborn-closed-parent", "stubborn-pipes-parent"} {
		t.Run(mode, func(t *testing.T) {
			result := runUnixCancellationFixture(t, mode)
			if result.elapsed < processStopGrace-time.Second {
				t.Errorf("stubborn group stopped after %s; expected the force deadline", result.elapsed)
			}
			if result.elapsed > processStopGrace+3*time.Second {
				t.Errorf("stubborn group exceeded the force deadline: %s", result.elapsed)
			}
			requireUnixPortReleased(t, result.port)
		})
	}
}

type unixCancellationResult struct {
	elapsed    time.Duration
	doneMarker string
	port       int
}

func runUnixCancellationFixture(t *testing.T, mode string) unixCancellationResult {
	t.Helper()
	dir := t.TempDir()
	ready := filepath.Join(dir, "ready")
	doneMarker := filepath.Join(dir, "done")
	port := reserveUnixIPv4Port(t)
	c := repo(t, "name: demo\ntype: go\n", nil)
	proc := config.Process{
		Name: "stubborn",
		Dir:  ".",
		Run:  config.CmdLine{os.Args[0], "-test.run=^TestUnixProcessFixture$"},
		Env: []string{
			unixFixtureMode + "=" + mode,
			unixFixtureReady + "=" + ready,
			unixFixtureDone + "=" + doneMarker,
			unixFixturePort + "=" + strconv.Itoa(port),
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	finished := make(chan struct{})
	var output bytes.Buffer
	go func() {
		done <- runProcess(ctx, c, ui.New(&output, &output, false), proc, 0)
		close(finished)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(processStopGrace + 3*time.Second):
		}
	})
	waitForUnixFile(t, ready, 10*time.Second)
	started := time.Now()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled runProcess() = %v\n%s", err, output.String())
		}
	case <-time.After(processStopGrace + 5*time.Second):
		t.Fatal("process group did not stop")
	}
	return unixCancellationResult{
		elapsed:    time.Since(started),
		doneMarker: doneMarker,
		port:       port,
	}
}

func requireUnixPortReleased(t *testing.T, port int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		ln, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			ln.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("grandchild still holds port %d: %v", port, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func waitForUnixFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func reserveUnixIPv4Port(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}
