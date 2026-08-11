// Package dev runs a repository's development processes.
//
// The processes run in the foreground as children of `hoshi dev`, with their
// output interleaved and prefixed, and one Ctrl+C stops all of them.
//
// The scripts this replaces detached each process into its own terminal window
// (Windows) or a background process writing to a log file (Unix), which is why
// they also needed to hunt down and kill whatever was holding a port on the
// next run. Keeping the children attached removes that whole category: nothing
// outlives the command that started it, so "run dev again" cannot collide with
// the last one.
package dev

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hoshivel/hoshi-build/internal/config"
	"github.com/hoshivel/hoshi-build/internal/ui"
)

// Options are the per-invocation overrides for `hoshi dev`.
type Options struct {
	Open    bool     // open a browser once the ready port answers
	DryRun  bool     // print what would run, start nothing
	Only    []string // process names to run; empty means all
	Args    []string // extra arguments appended to the first process
	NoCheck bool     // skip the port availability check
}

// Run starts every selected process and waits for them.
func Run(ctx context.Context, c *config.Config, p *ui.Printer, opts Options) error {
	procs, err := selectProcesses(c, opts.Only)
	if err != nil {
		return err
	}
	if len(procs) == 0 {
		return errors.New("沒有可啟動的行程（dev.processes 是空的）")
	}
	if len(opts.Args) > 0 {
		procs[0].Run = append(append(config.CmdLine{}, procs[0].Run...), opts.Args...)
	}

	p.Title("開發環境")
	for _, proc := range procs {
		where := proc.Dir
		if where == "." {
			where = c.Name
		}
		p.Note("%s：%s（在 %s）%s", proc.Name, proc.Run, where, portNote(proc))
	}

	// -open is the half of the plan that is easiest to get wrong and, until
	// now, the only half -dry-run stayed silent about: it returned before the
	// opener ran, so the flag that needed previewing was the one you could not
	// preview.
	if opts.Open {
		url, port := openTarget(c, procs)
		switch {
		case url == "":
			p.Warn("-open：不知道要開什麼。%s", openHint(c))
		case port > 0:
			p.Note("-open：等 %d 就緒後開啟 %s", port, url)
			warnIfExcluded(c, p, procs, port)
		default:
			p.Note("-open：立刻開啟 %s（沒有可等的埠）", url)
		}
	}

	if opts.DryRun {
		p.Note("-dry-run：以上都沒有真的啟動")
		return nil
	}

	if !opts.NoCheck {
		if err := checkPorts(procs); err != nil {
			return err
		}
	}

	// One cancel drives everything: a child failing, Ctrl+C, or the caller's
	// context all end up here, and every other child is torn down.
	//
	// parent is kept separate so the two reasons stay distinguishable at the
	// end. Asking the derived context "were you cancelled?" always says yes —
	// this function cancels it itself — which would swallow the exit status of
	// a process that genuinely crashed.
	parent := ctx
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)

	for i, proc := range procs {
		wg.Add(1)
		go func(i int, proc config.Process) {
			defer wg.Done()
			err := runProcess(ctx, c, p, proc, colourFor(i))
			mu.Lock()
			if err != nil && first == nil {
				first = err
			}
			mu.Unlock()
			// Any process ending ends the session: a dev environment with half
			// its parts running looks alive but cannot serve a request.
			cancel()
		}(i, proc)
	}

	if opts.Open {
		go openWhenReady(ctx, c, p, procs)
	}

	wg.Wait()

	// Ctrl+C is how a dev session is meant to end, so it is not a failure.
	if parent.Err() == nil && first != nil {
		return first
	}
	p.Plain("")
	p.OK("開發環境已停止")
	return nil
}

// selectProcesses filters by name, preserving configuration order.
func selectProcesses(c *config.Config, only []string) ([]config.Process, error) {
	if len(only) == 0 {
		return c.Dev.Processes, nil
	}
	want := map[string]bool{}
	for _, name := range only {
		want[name] = true
	}

	var out []config.Process
	for _, proc := range c.Dev.Processes {
		if want[proc.Name] {
			out = append(out, proc)
			delete(want, proc.Name)
		}
	}
	if len(want) > 0 {
		var missing []string
		for name := range want {
			missing = append(missing, name)
		}
		return nil, fmt.Errorf("dev.processes 裡沒有 %s；有的是 %s",
			strings.Join(missing, "、"), strings.Join(names(c.Dev.Processes), "、"))
	}
	return out, nil
}

func names(procs []config.Process) []string {
	out := make([]string, len(procs))
	for i, proc := range procs {
		out[i] = proc.Name
	}
	return out
}

func portNote(proc config.Process) string {
	if len(proc.Ports) == 0 {
		return ""
	}
	parts := make([]string, len(proc.Ports))
	for i, port := range proc.Ports {
		parts[i] = fmt.Sprint(port)
	}
	return " 埠 " + strings.Join(parts, "、")
}

// runProcess starts one child and streams its output with a prefix.
func runProcess(ctx context.Context, c *config.Config, p *ui.Printer, proc config.Process, colour int) error {
	dir := filepath.Join(c.Root, filepath.FromSlash(proc.Dir))
	cmd := exec.CommandContext(ctx, proc.Run[0], proc.Run[1:]...)
	cmd.Dir = dir
	if len(proc.Env) > 0 {
		cmd.Env = append(environ(), proc.Env...)
	}
	// Kill the whole child process group, not just the direct child: `npm run
	// dev` is a shim that spawns the real server, and killing only the shim
	// leaves the server holding the port.
	setProcessGroup(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		// The first process to end cancels ctx for everybody, and a process
		// still waiting to be started then fails with context.Canceled. That is
		// the teardown arriving, not a broken command line — reporting it would
		// name a bystander as the reason the session ended, and hide the process
		// that actually finished. Same reasoning as the ctx check after Wait.
		if ctx.Err() != nil {
			return nil // torn down before it got going
		}
		return fmt.Errorf("%s 啟動失敗：%w", proc.Name, err)
	}
	p.OK("%s 已啟動（PID %d）", proc.Name, cmd.Process.Pid)

	// Both streams are prefixed the same way. cmd.Wait closes the pipes, so
	// both pumps must finish first or their tail is lost.
	var pumps sync.WaitGroup
	pumps.Add(2)
	go func() { defer pumps.Done(); pump(p, proc.Name, colour, stdout) }()
	go func() { defer pumps.Done(); pump(p, proc.Name, colour, stderr) }()
	pumps.Wait()

	err = cmd.Wait()
	if ctx.Err() != nil {
		return nil // torn down on purpose
	}
	if err != nil {
		return fmt.Errorf("%s 結束：%w", proc.Name, err)
	}
	p.Note("%s 已結束", proc.Name)
	return nil
}

// pump copies a child's output, one prefixed line at a time.
func pump(p *ui.Printer, name string, colour int, src io.Reader) {
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		p.Prefixed(name, colour, scanner.Text())
	}
}

// checkPorts refuses to start when a declared port is already taken.
//
// It reports rather than killing the holder. With the children attached, a
// port that is still busy belongs to something else — and terminating an
// unrelated process because it happened to pick the same number is not a thing
// a build tool should do quietly.
func checkPorts(procs []config.Process) error {
	var busy []string
	for _, proc := range procs {
		for _, port := range proc.Ports {
			if portInUse(port) {
				busy = append(busy, fmt.Sprintf("%d（%s）", port, proc.Name))
			}
		}
	}
	if len(busy) == 0 {
		return nil
	}
	return fmt.Errorf("這些埠已經被占用：%s\n"+
		"      先停掉占用的行程，或加 -no-check 略過這項檢查",
		strings.Join(busy, "、"))
}

func portInUse(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return true
	}
	ln.Close()
	return false
}

// openWhenReady waits for the opened URL's port to answer, then opens a
// browser. What it is about to do was already printed with the plan, so this
// reports outcomes only.
func openWhenReady(ctx context.Context, c *config.Config, p *ui.Printer, procs []config.Process) {
	url, port := openTarget(c, procs)
	if url == "" {
		return // already reported with the plan
	}

	if port > 0 {
		if !waitPort(ctx, port, readyTimeout) {
			// Cancelled means the session is being torn down — a process
			// exited or Ctrl+C arrived — and the port never answering is a
			// consequence of that, not a second thing that went wrong.
			if ctx.Err() == nil {
				p.Warn("%d 在 %s 內沒有回應，沒有開啟瀏覽器", port, readyTimeout)
			}
			return
		}
	}
	if err := openBrowser(url); err != nil {
		p.Warn("開不了瀏覽器（%v）；請自行開啟 %s", err, url)
		return
	}
	p.OK("已開啟 %s", url)
}

// readyTimeout bounds the wait for the opened URL's port. A cold `go run`
// compiles the whole module before it listens, so this is generous on purpose.
const readyTimeout = 90 * time.Second

// openTarget decides what `-open` opens, and which port to wait for first.
//
// The rule is one sentence: **open `dev.open` (or the URL built from
// `dev.port`), and wait for that URL's own port.**
//
// Waiting used to mean "the first port any process declared", which is a
// different thing whenever more than one process declares one. With a backend
// on 8080 and a Vite frontend on 5173, `-open` waited for the backend and then
// opened the frontend — and Vite is the slower of the two, so the browser
// arrived at a port nothing was listening on yet. Tying the wait to the URL
// removes the possibility rather than reordering it.
func openTarget(c *config.Config, procs []config.Process) (url string, port int) {
	switch {
	case c.Dev.Open != "":
		url = c.Dev.Open
	case c.Dev.Port != 0:
		url = fmt.Sprintf("http://localhost:%d", c.Dev.Port)
	default:
		if rp := firstReadyPort(procs); rp != 0 {
			url = fmt.Sprintf("http://localhost:%d", rp)
		}
	}
	if url == "" {
		return "", 0
	}
	return url, waitPortFor(c, url, procs)
}

// waitPortFor picks the port to wait on before opening url.
func waitPortFor(c *config.Config, url string, procs []config.Process) int {
	// An explicit `ready` says "wait for this one" in as many words, so it
	// outranks anything derived.
	for _, proc := range procs {
		if proc.Ready != 0 {
			return proc.Ready
		}
	}
	if p := config.LoopbackPort(url); p != 0 {
		return p
	}
	if c.Dev.Port != 0 {
		return c.Dev.Port
	}
	return firstReadyPort(procs)
}

func firstReadyPort(procs []config.Process) int {
	for _, proc := range procs {
		if rp := proc.ReadyPort(); rp != 0 {
			return rp
		}
	}
	return 0
}

// warnIfExcluded reports a `-only` selection that left out the process holding
// the port `-open` is about to wait for. Without it the symptom is a full
// timeout followed by a browser that never opens, and nothing naming the flag
// that caused it.
func warnIfExcluded(c *config.Config, p *ui.Printer, selected []config.Process, port int) {
	if port == 0 || len(selected) == len(c.Dev.Processes) {
		return
	}
	for _, proc := range selected {
		for _, declared := range proc.Ports {
			if declared == port {
				return
			}
		}
	}
	for _, proc := range c.Dev.Processes {
		for _, declared := range proc.Ports {
			if declared == port {
				p.Warn("-open 要等的 %d 屬於 %s，而 -only 沒有選它", port, proc.Name)
				return
			}
		}
	}
}

// openHint names the key to add, rather than the keys that are missing. The
// reader already knows nothing is set; what they need is the one line to write.
func openHint(c *config.Config) string {
	return fmt.Sprintf("在 .hoshi-build.yaml 加上服務監聽的埠：\n"+
		"        dev:\n"+
		"          port: 8080\n"+
		"      網址不是 http://localhost:<port> 時改用 `dev.open` 指定完整網址（例：%s）",
		"http://localhost:8080/console")
}

func waitPort(ctx context.Context, port int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return false
		}
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			conn.Close()
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// colourFor keeps each process's prefix a consistent colour.
func colourFor(i int) int { return i }

// errNoOpener is returned when no browser launcher exists (headless boxes,
// minimal containers). It is a note, not a failure of the dev session.
var errNoOpener = errors.New("找不到 xdg-open / open")
