package dev

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/ui"
)

func TestMain(m *testing.M) {
	if handled, code := MaybeRunProcessHelper(os.Args[1:]); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func repo(t *testing.T, body string, files map[string]string) *config.Config {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".hoshi-build.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	c, err := config.LoadFrom(root)
	if err != nil {
		t.Fatalf("LoadFrom() error = %v", err)
	}
	return c
}

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("這些測試用 sh 當替身行程")
	}
}

func TestPrefixedWriterHandlesChunksCRLFAndFinalTail(t *testing.T) {
	var out bytes.Buffer
	w := newPrefixedWriter(ui.New(&out, &out, false), "worker", 0)

	for _, chunk := range [][]byte{
		[]byte("first\r"),
		[]byte("\nsecond\nthird"),
		[]byte("-tail"),
	} {
		if n, err := w.Write(chunk); err != nil || n != len(chunk) {
			t.Fatalf("Write() = %d, %v; want %d, nil", n, err, len(chunk))
		}
	}
	w.Flush()

	text := out.String()
	for _, want := range []string{
		"worker   │ first\n",
		"worker   │ second\n",
		"worker   │ third-tail\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("輸出少了 %q：\n%s", want, text)
		}
	}
	if strings.Contains(text, "\r") {
		t.Errorf("CRLF 的 CR 漏進輸出：%q", text)
	}
}

func TestPrefixedWriterBoundsLinesWithoutNewlines(t *testing.T) {
	var out bytes.Buffer
	w := newPrefixedWriter(ui.New(&out, &out, false), "long", 0)
	data := bytes.Repeat([]byte{'x'}, maxPrefixedLine+37)

	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if got := len(w.pending); got != 37 {
		t.Fatalf("pending = %d, want 37（超長行應先送出一塊）", got)
	}
	w.Flush()

	text := out.String()
	if got := strings.Count(text, "long     │ "); got != 2 {
		t.Errorf("前綴行數 = %d, want 2", got)
	}
	if got := strings.Count(text, "x"); got != len(data) {
		t.Errorf("輸出的 x = %d, want %d", got, len(data))
	}
}

// Two processes run at once and both their outputs reach the terminal, tagged.
// Interleaving is the entire reason this replaces separate windows.
//
// Each one waits for the other's marker before exiting. Without that barrier
// the test asserts something Run does not promise: the first process to end
// cancels the rest, so a plain `echo` in each raced its own teardown and this
// test failed roughly one run in twelve under load — an intermittent red light,
// which is worse than a steady one because it trains people to ignore it.
func TestRunsEveryProcessAndPrefixesOutput(t *testing.T) {
	requireUnix(t)
	c := repo(t, `
name: demo
type: go
dev:
  processes:
    - name: alpha
      run: [sh, -c, "echo hello-from-alpha; touch alpha.up; i=0; while [ ! -f beta.up ] && [ $i -lt 400 ]; do sleep 0.05; i=$((i+1)); done"]
    - name: beta
      run: [sh, -c, "echo hello-from-beta; touch beta.up; i=0; while [ ! -f alpha.up ] && [ $i -lt 400 ]; do sleep 0.05; i=$((i+1)); done"]
`, nil)

	var out bytes.Buffer
	p := ui.New(&out, &out, false)

	if err := Run(context.Background(), c, p, Options{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	text := out.String()
	for _, want := range []string{"alpha", "hello-from-alpha", "beta", "hello-from-beta"} {
		if !strings.Contains(text, want) {
			t.Errorf("輸出少了 %q：\n%s", want, text)
		}
	}
}

// stderr is where dev servers put the interesting parts.
func TestCapturesStderr(t *testing.T) {
	requireUnix(t)
	c := repo(t, `
name: demo
type: go
dev:
  processes:
    - name: noisy
      run: [sh, -c, "echo to-stderr >&2"]
`, nil)

	var out bytes.Buffer
	if err := Run(context.Background(), c, ui.New(&out, &out, false), Options{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(out.String(), "to-stderr") {
		t.Errorf("stderr 沒有被收進來：\n%s", out.String())
	}
}

// A dev environment with half its parts running looks alive but cannot serve a
// request, so one process ending takes the rest with it.
func TestOneProcessExitingStopsTheRest(t *testing.T) {
	requireUnix(t)
	c := repo(t, `
name: demo
type: go
dev:
  processes:
    - name: quick
      run: [sh, -c, "exit 0"]
    - name: forever
      run: [sh, -c, "sleep 300"]
`, nil)

	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), c, ui.New(&bytes.Buffer{}, &bytes.Buffer{}, false), Options{})
	}()

	select {
	case err := <-done:
		// quick ended cleanly and forever was torn down on purpose, so there is
		// nothing to report. Anything here would be the teardown wearing the
		// costume of a failure.
		if err != nil {
			t.Fatalf("收掉其餘行程不該變成錯誤：%v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("一個行程結束之後，其餘的沒有被收掉")
	}
}

// The teardown can arrive before a process has even been started, and then
// exec's own error is context.Canceled. Reporting that would name a bystander
// as the reason the session ended.
func TestStartAfterTeardownIsNotAFailure(t *testing.T) {
	requireUnix(t)
	c := repo(t, "name: demo\ntype: go\n", nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	proc := config.Process{Name: "late", Dir: ".", Run: config.CmdLine{"sh", "-c", "exit 0"}}
	err := runProcess(ctx, c, ui.New(&bytes.Buffer{}, &bytes.Buffer{}, false), proc, 0)
	if err != nil {
		t.Fatalf("被收掉的行程不該回報啟動失敗：%v", err)
	}
}

func TestFailingProcessIsReported(t *testing.T) {
	requireUnix(t)
	c := repo(t, `
name: demo
type: go
dev:
  processes:
    - name: broken
      run: [sh, -c, "exit 3"]
`, nil)

	err := Run(context.Background(), c, ui.New(&bytes.Buffer{}, &bytes.Buffer{}, false), Options{})
	if err == nil || !strings.Contains(err.Error(), "broken") {
		t.Fatalf("error = %v，預期指出是哪個行程掛了", err)
	}
}

// Ctrl+C is a cancelled context; it must stop everything and not be an error.
func TestCancelStopsEverythingCleanly(t *testing.T) {
	requireUnix(t)
	c := repo(t, `
name: demo
type: go
dev:
  processes:
    - name: forever
      run: [sh, -c, "sleep 300"]
`, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, c, ui.New(&bytes.Buffer{}, &bytes.Buffer{}, false), Options{})
	}()

	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Ctrl+C 之後不該回報錯誤，得到 %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("cancel 之後行程沒有停")
	}
}

func TestDryRunStartsNothing(t *testing.T) {
	c := repo(t, `
name: demo
type: go
dev:
  processes:
    - name: bad
      run: [definitely-not-a-real-command-xyz]
`, nil)

	var out bytes.Buffer
	if err := Run(context.Background(), c, ui.New(&out, &out, false), Options{DryRun: true}); err != nil {
		t.Fatalf("-dry-run 不該失敗：%v", err)
	}
	if !strings.Contains(out.String(), "definitely-not-a-real-command-xyz") {
		t.Errorf("-dry-run 應印出要跑什麼：\n%s", out.String())
	}
}

func TestOnlySelectsProcesses(t *testing.T) {
	requireUnix(t)
	c := repo(t, `
name: demo
type: go
dev:
  processes:
    - name: alpha
      run: [sh, -c, "echo alpha-ran"]
    - name: beta
      run: [sh, -c, "echo beta-ran"]
`, nil)

	var out bytes.Buffer
	err := Run(context.Background(), c, ui.New(&out, &out, false), Options{Only: []string{"beta"}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if strings.Contains(out.String(), "alpha-ran") {
		t.Errorf("-only beta 之下不該啟動 alpha：\n%s", out.String())
	}
	if !strings.Contains(out.String(), "beta-ran") {
		t.Errorf("-only beta 沒有啟動 beta：\n%s", out.String())
	}
}

func TestOnlyUnknownNameLists(t *testing.T) {
	c := repo(t, "name: demo\ntype: go\n", nil)
	err := Run(context.Background(), c, ui.New(&bytes.Buffer{}, &bytes.Buffer{}, false),
		Options{Only: []string{"nope"}})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("error = %v，預期指出名字不存在", err)
	}
	if !strings.Contains(err.Error(), "backend") {
		t.Errorf("error = %q，應該順便列出有哪些行程", err)
	}
}

// Starting a second dev session on a taken port would produce a process that
// exits with a confusing error from deep inside a dev server.
func TestBusyPortIsRefusedUpFront(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("開不了 socket")
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	c := repo(t, fmt.Sprintf(`
name: demo
type: go
dev:
  processes:
    - name: web
      run: [sh, -c, "sleep 300"]
      ports: [%d]
`, port), nil)

	err = Run(context.Background(), c, ui.New(&bytes.Buffer{}, &bytes.Buffer{}, false), Options{})
	if err == nil || !strings.Contains(err.Error(), "占用") {
		t.Fatalf("error = %v，預期埠被占用時直接拒絕啟動", err)
	}
	if !strings.Contains(err.Error(), "-no-check") {
		t.Errorf("error = %q，應該告訴使用者怎麼略過", err)
	}
}

// localhost commonly resolves to ::1 first on Windows. A Vite/Astro server
// can therefore own only the IPv6 loopback port even though its URL simply
// says localhost; preflight must still see that the port is occupied.
func TestBusyIPv6LoopbackPortIsRefusedUpFront(t *testing.T) {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback is unavailable: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	// Prove the port is free on IPv4. The old IPv4-only preflight therefore
	// missed this listener; this test is specifically guarding that regression.
	v4, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
	if err != nil {
		t.Skipf("cannot isolate an IPv6-only occupied port: %v", err)
	}
	v4.Close()

	err = checkPorts([]config.Process{{Name: "web", Ports: []int{port}}})
	if err == nil || !strings.Contains(err.Error(), "占用") {
		t.Fatalf("error = %v，預期 IPv6 loopback 埠被占用時直接拒絕啟動", err)
	}
}

func TestFreeLoopbackPortIsAvailable(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("IPv4 loopback is unavailable: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	if portInUse(port) {
		t.Fatalf("未占用的 loopback 埠 %d 被誤報為已占用", port)
	}
}

func TestArgsAppendToFirstProcess(t *testing.T) {
	requireUnix(t)
	c := repo(t, `
name: demo
type: go
dev:
  processes:
    - name: echoer
      run: [echo, base]
`, nil)

	var out bytes.Buffer
	err := Run(context.Background(), c, ui.New(&out, &out, false), Options{Args: []string{"--extra", "value"}})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(out.String(), "base --extra value") {
		t.Errorf("附加參數沒有傳進去：\n%s", out.String())
	}
}

// -dry-run has to answer "what would -open do", because that is the flag whose
// behaviour is hard to predict from the file. It used to return before the
// opener ran and say nothing at all.
func TestDryRunReportsTheOpenTarget(t *testing.T) {
	c := repo(t, "name: demo\ntype: go\ndev:\n  port: 8095\n", nil)

	var out bytes.Buffer
	if err := Run(context.Background(), c, ui.New(&out, &out, false), Options{
		Open: true, DryRun: true,
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(out.String(), "等 8095 就緒後開啟 http://localhost:8095") {
		t.Errorf("-dry-run 沒有講出 -open 會做什麼：\n%s", out.String())
	}
}

// With nothing to go on, the message has to name the key to add. "沒有設定" only
// restates what the reader already knows.
func TestDryRunNamesTheKeyToAdd(t *testing.T) {
	c := repo(t, "name: demo\ntype: go\n", nil)

	var out bytes.Buffer
	if err := Run(context.Background(), c, ui.New(&out, &out, false), Options{
		Open: true, DryRun: true,
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(out.String(), "dev:") || !strings.Contains(out.String(), "port:") {
		t.Errorf("警告沒有講出該加哪一個鍵：\n%s", out.String())
	}
}

// -only that leaves out the process holding the open port is worth saying at
// once: the alternative is a full timeout and a browser that never opens, with
// nothing naming the flag responsible.
func TestOnlyExcludingTheOpenPortWarnsImmediately(t *testing.T) {
	c := repo(t, "name: demo\ntype: go\ndev:\n  open: http://localhost:5173\n  processes:\n"+
		"    - {name: backend, run: sleep 1, ports: [8080]}\n"+
		"    - {name: frontend, run: sleep 1, ports: [5173]}\n", nil)

	var out bytes.Buffer
	if err := Run(context.Background(), c, ui.New(&out, &out, false), Options{
		Open: true, DryRun: true, Only: []string{"backend"},
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(out.String(), "-only 沒有選它") {
		t.Errorf("沒有警告 -only 排除了開啟目標：\n%s", out.String())
	}
}

func TestOpenTarget(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantURL  string
		wantPort int
	}{
		{
			// The wait follows the URL, not the first port some other process
			// happened to declare. This case used to expect 5173 — that is the
			// bug: it opened 9999 after waiting for something else entirely.
			name:     "explicit url decides the wait",
			body:     "name: d\ntype: go\ndev:\n  open: http://localhost:9999/x\n  processes:\n    - {name: a, run: sleep 1, ports: [5173]}\n",
			wantURL:  "http://localhost:9999/x",
			wantPort: 9999,
		},
		{
			// The shape that made this worth fixing: a backend that starts
			// first and a frontend the browser is actually pointed at.
			name: "waits for the frontend it opens, not the backend",
			body: "name: d\ntype: go\ndev:\n  open: http://localhost:5173\n  processes:\n" +
				"    - {name: backend, run: sleep 1, ports: [8080, 8081]}\n" +
				"    - {name: frontend, run: sleep 1, ports: [5173]}\n",
			wantURL:  "http://localhost:5173",
			wantPort: 5173,
		},
		{
			name:     "dev.port alone is enough",
			body:     "name: d\ntype: go\ndev:\n  port: 8095\n",
			wantURL:  "http://localhost:8095",
			wantPort: 8095,
		},
		{
			name:     "dev.port with a path-bearing open url",
			body:     "name: d\ntype: go\ndev:\n  port: 8095\n  open: http://localhost:8095/console\n",
			wantURL:  "http://localhost:8095/console",
			wantPort: 8095,
		},
		{
			// A remote URL has no local port to wait for, so the wait falls
			// back to what the processes declare.
			name:     "remote url falls back to the declared port",
			body:     "name: d\ntype: go\ndev:\n  open: https://example.test/app\n  processes:\n    - {name: a, run: sleep 1, ports: [4321]}\n",
			wantURL:  "https://example.test/app",
			wantPort: 4321,
		},
		{
			name:     "derived from the ready port",
			body:     "name: d\ntype: go\ndev:\n  processes:\n    - {name: a, run: sleep 1, ports: [4321]}\n",
			wantURL:  "http://localhost:4321",
			wantPort: 4321,
		},
		{
			name:     "ready overrides the first port",
			body:     "name: d\ntype: go\ndev:\n  processes:\n    - {name: a, run: sleep 1, ports: [8080, 5173], ready: 5173}\n",
			wantURL:  "http://localhost:5173",
			wantPort: 5173,
		},
		{
			name:    "nothing to go on",
			body:    "name: d\ntype: go\ndev:\n  processes:\n    - {name: a, run: sleep 1}\n",
			wantURL: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := repo(t, tc.body, nil)
			url, port := openTarget(c, c.Dev.Processes)
			if url != tc.wantURL || (tc.wantURL != "" && port != tc.wantPort) {
				t.Errorf("openTarget() = %q, %d；want %q, %d", url, port, tc.wantURL, tc.wantPort)
			}
		})
	}
}

func TestReadyHostFor(t *testing.T) {
	tests := []struct {
		name string
		url  string
		port int
		want string
	}{
		{name: "localhost", url: "http://localhost:4321", port: 4321, want: "localhost"},
		{name: "explicit IPv4", url: "http://127.0.0.1:4321", port: 4321, want: "127.0.0.1"},
		{name: "explicit IPv6", url: "http://[::1]:4321", port: 4321, want: "::1"},
		{name: "other ready port", url: "http://127.0.0.1:4321", port: 8080, want: "localhost"},
		{name: "remote URL", url: "https://example.test/app", port: 4321, want: "localhost"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := readyHostFor(tc.url, tc.port); got != tc.want {
				t.Errorf("readyHostFor(%q, %d) = %q, want %q", tc.url, tc.port, got, tc.want)
			}
		})
	}
}

func TestWaitPortAcceptsIPv4AndIPv6Loopback(t *testing.T) {
	tests := []struct {
		name    string
		network string
		host    string
	}{
		{name: "IPv4", network: "tcp4", host: "127.0.0.1"},
		{name: "IPv6", network: "tcp6", host: "::1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ln, err := net.Listen(tc.network, net.JoinHostPort(tc.host, "0"))
			if err != nil {
				t.Skipf("%s loopback is unavailable: %v", tc.name, err)
			}
			defer ln.Close()
			port := ln.Addr().(*net.TCPAddr).Port

			if !waitPort(context.Background(), tc.host, port, time.Second) {
				t.Fatalf("沒有偵測到 %s loopback 上已就緒的 %d", tc.name, port)
			}
		})
	}
}

// This is the exact Windows failure: localhost resolves to ::1, Astro listens
// there, but the old probe ignored the URL host and dialled 127.0.0.1 only.
func TestWaitPortAcceptsLocalhostOnIPv6(t *testing.T) {
	hasIPv6Localhost := false
	addrs, err := net.LookupIP("localhost")
	if err == nil {
		for _, addr := range addrs {
			if addr.Equal(net.IPv6loopback) {
				hasIPv6Localhost = true
				break
			}
		}
	}
	if !hasIPv6Localhost {
		t.Skip("localhost does not resolve to the IPv6 loopback on this machine")
	}

	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback is unavailable: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if !waitPort(context.Background(), "localhost", port, time.Second) {
		t.Fatalf("localhost 沒有偵測到 IPv6 loopback 上已就緒的 %d", port)
	}
}

func TestWaitPortAcceptsLocalhostOnIPv4(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("IPv4 loopback is unavailable: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	if !waitPort(context.Background(), "localhost", port, time.Second) {
		t.Fatalf("localhost 沒有偵測到 IPv4 loopback 上已就緒的 %d", port)
	}
}
