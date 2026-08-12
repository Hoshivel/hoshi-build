//go:build windows

package dev

import (
	"os"
	"os/exec"
	"time"
)

func environ() []string { return os.Environ() }

// setProcessGroup is a no-op on Windows.
//
// exec.CommandContext already kills the direct child, and Windows has no
// process groups in the POSIX sense. A grandchild spawned by `npm run dev` can
// therefore outlive the run; `hoshi dev` reports a busy port on the next start
// rather than pretending otherwise.
func setProcessGroup(cmd *exec.Cmd) {
	// Still bound the teardown: without this, a grandchild holding the output
	// pipes open would keep Wait blocked after the direct child is gone.
	cmd.WaitDelay = 5 * time.Second
}

func openBrowser(url string) error {
	// `start` is a cmd.exe builtin, not an executable. The empty string is the
	// window title — without it, a quoted URL becomes the title and nothing
	// opens.
	cmd := exec.Command("cmd", "/c", "start", "", url)
	// Do not let the desktop handler inherit the dev process's working
	// directory. Some browsers keep it open after cmd.exe exits, which can
	// prevent a temporary checkout or renamed project directory from being
	// removed for the rest of the browser session.
	cmd.Dir = os.TempDir()
	if err := cmd.Start(); err != nil {
		return err
	}
	// Reap it: cmd.exe exits as soon as the URL is handed over, and an
	// unwaited child holds its handles for the life of the dev session.
	go cmd.Wait()
	return nil
}
