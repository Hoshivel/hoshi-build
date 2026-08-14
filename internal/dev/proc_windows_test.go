//go:build windows

package dev

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/ui"
)

const (
	windowsFixtureMode  = "HOSHI_DEV_WINDOWS_FIXTURE"
	windowsFixtureReady = "HOSHI_DEV_WINDOWS_READY"
	windowsFixtureDone  = "HOSHI_DEV_WINDOWS_DONE"
	windowsFixturePort  = "HOSHI_DEV_WINDOWS_PORT"
)

// TestWindowsProcessFixture is a subprocess entry point selected with
// -test.run. With no fixture environment it is an ordinary no-op test.
func TestWindowsProcessFixture(t *testing.T) {
	switch os.Getenv(windowsFixtureMode) {
	case "graceful":
		windowsGracefulFixture()
	case "outer-host":
		windowsOuterHostFixture(t)
	case "tree-parent":
		windowsTreeParentFixture()
	case "tree-child":
		windowsTreeChildFixture()
	}
}

func windowsGracefulFixture() {
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	if err := os.WriteFile(os.Getenv(windowsFixtureReady), []byte("ready"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	<-interrupts
	if err := os.WriteFile(os.Getenv(windowsFixtureDone), []byte("graceful"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func windowsTreeParentFixture() {
	windowsFixtureSafetyExit()
	interrupts := make(chan os.Signal, 4)
	signal.Notify(interrupts, os.Interrupt)
	cmd := exec.Command(os.Args[0], "-test.run=^TestWindowsProcessFixture$")
	cmd.Env = fixtureEnv("tree-child")
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

func windowsTreeChildFixture() {
	windowsFixtureSafetyExit()
	interrupts := make(chan os.Signal, 4)
	signal.Notify(interrupts, os.Interrupt)
	port, err := strconv.Atoi(os.Getenv(windowsFixturePort))
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
	if err := os.WriteFile(os.Getenv(windowsFixtureReady), []byte("ready"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func windowsOuterHostFixture(t *testing.T) {
	c := windowsFixtureRepo(t)
	proc := config.Process{
		Name: "outer-child",
		Dir:  ".",
		Run:  config.CmdLine{os.Args[0], "-test.run=^TestWindowsProcessFixture$"},
		Env: []string{
			windowsFixtureMode + "=tree-parent",
			windowsFixtureReady + "=" + os.Getenv(windowsFixtureReady),
			windowsFixturePort + "=" + os.Getenv(windowsFixturePort),
		},
	}
	if err := runProcess(context.Background(), c, ui.New(os.Stdout, os.Stderr, false), proc, 0); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(0)
}

func windowsFixtureSafetyExit() {
	time.AfterFunc(30*time.Second, func() { os.Exit(3) })
}

func fixtureEnv(mode string) []string {
	prefix := strings.ToUpper(windowsFixtureMode) + "="
	out := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(entry), prefix) {
			out = append(out, entry)
		}
	}
	return append(out, windowsFixtureMode+"="+mode)
}

func TestWindowsCancelSendsGracefulInterrupt(t *testing.T) {
	dir := t.TempDir()
	ready := dir + "\\ready"
	doneMarker := dir + "\\done"
	c := windowsFixtureRepo(t)
	proc := config.Process{
		Name: "graceful",
		Dir:  ".",
		Run:  config.CmdLine{os.Args[0], "-test.run=^TestWindowsProcessFixture$"},
		Env: []string{
			windowsFixtureMode + "=graceful",
			windowsFixtureReady + "=" + ready,
			windowsFixtureDone + "=" + doneMarker,
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
	waitForFile(t, ready, 10*time.Second)
	started := time.Now()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled runProcess() = %v\n%s", err, output.String())
		}
	case <-time.After(processStopGrace + 5*time.Second):
		t.Fatal("graceful child did not stop")
	}
	if elapsed := time.Since(started); elapsed >= processStopGrace {
		t.Errorf("graceful stop took %s; helper reached the force deadline", elapsed)
	}
	if body, err := os.ReadFile(doneMarker); err != nil || string(body) != "graceful" {
		t.Fatalf("graceful marker = %q, %v", body, err)
	}
}

func TestWindowsCancelKillsGrandchildTreeAndReleasesPort(t *testing.T) {
	dir := t.TempDir()
	ready := dir + "\\ready"
	port := reserveIPv4Port(t)

	c := windowsFixtureRepo(t)
	proc := config.Process{
		Name: "stubborn",
		Dir:  ".",
		Run:  config.CmdLine{os.Args[0], "-test.run=^TestWindowsProcessFixture$"},
		Env: []string{
			windowsFixtureMode + "=tree-parent",
			windowsFixtureReady + "=" + ready,
			windowsFixturePort + "=" + strconv.Itoa(port),
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
	waitForFile(t, ready, 10*time.Second)
	started := time.Now()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("cancelled runProcess() = %v\n%s", err, output.String())
		}
	case <-time.After(processStopGrace + 5*time.Second):
		t.Fatal("stubborn process tree was not forced down")
	}
	elapsed := time.Since(started)
	if elapsed < processStopGrace-time.Second {
		t.Errorf("stubborn tree stopped after %s; expected the force deadline", elapsed)
	}
	if elapsed > processStopGrace+3*time.Second {
		t.Errorf("stubborn tree exceeded the force deadline: %s", elapsed)
	}

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

func TestWindowsOuterProcessDeathKillsGrandchildTreeAndReleasesPort(t *testing.T) {
	dir := t.TempDir()
	ready := dir + "\\ready"
	port := reserveIPv4Port(t)

	outer := exec.Command(os.Args[0], "-test.run=^TestWindowsProcessFixture$")
	outer.Env = append(fixtureEnv("outer-host"),
		windowsFixtureReady+"="+ready,
		windowsFixturePort+"="+strconv.Itoa(port),
	)
	var stdout, stderr bytes.Buffer
	outer.Stdout = &stdout
	outer.Stderr = &stderr
	if err := outer.Start(); err != nil {
		t.Fatal(err)
	}
	waitErr := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		waitErr <- outer.Wait()
		close(finished)
	}()
	t.Cleanup(func() {
		_ = outer.Process.Kill()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
		}
	})

	waitForFile(t, ready, 10*time.Second)
	if err := outer.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-waitErr:
	case <-time.After(5 * time.Second):
		t.Fatalf("outer process did not exit\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		ln, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err == nil {
			ln.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper survived outer death and still holds port %d: %v\nstdout:\n%s\nstderr:\n%s",
				port, err, stdout.String(), stderr.String())
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func windowsFixtureRepo(t *testing.T) *config.Config {
	t.Helper()
	return repo(t, "name: demo\ntype: go\n", nil)
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
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

func reserveIPv4Port(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}
