//go:build !windows

package dev

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

func environ() []string { return os.Environ() }

// setProcessGroup puts the child in its own process group so the whole tree
// can be signalled at once.
//
// `npm run dev` is a shim: it spawns the real dev server as a grandchild.
// Killing only the direct child leaves that grandchild running and holding the
// port, which is exactly the orphan the previous scripts had to hunt down on
// the next run.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid = the whole group. SIGTERM first, so a dev server gets
		// to flush and close its listener.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
	// A child that ignores SIGTERM would otherwise hang the whole command:
	// Wait blocks until the output pipes close, and a grandchild holding them
	// open keeps them open. After the grace period Go closes the pipes and
	// sends SIGKILL, which nothing can ignore.
	cmd.WaitDelay = 5 * time.Second
}

// openBrowser opens a URL with the platform's handler.
func openBrowser(url string) error {
	for _, opener := range []string{"xdg-open", "open"} {
		if path, err := exec.LookPath(opener); err == nil {
			return exec.Command(path, url).Start()
		}
	}
	return errNoOpener
}
