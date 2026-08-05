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
	return exec.Command("cmd", "/c", "start", "", url).Start()
}
