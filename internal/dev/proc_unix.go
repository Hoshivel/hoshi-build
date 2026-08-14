//go:build !windows

package dev

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func environ() []string { return os.Environ() }

func processCommand(ctx context.Context, name string, args ...string) (*exec.Cmd, func() error, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	shutdownStarted := setProcessGroup(cmd)
	return cmd, func() error {
		return finishProcessGroup(cmd, shutdownStarted)
	}, nil
}

// MaybeRunProcessHelper exists for the command entry point's platform-neutral
// dispatch. Unix launches the configured command directly and never uses the
// private Windows supervisor mode.
func MaybeRunProcessHelper(_ []string) (bool, int) { return false, 0 }

// setProcessGroup puts the child in its own process group so normally inherited
// descendants can be signalled at once.
//
// `npm run dev` is a shim: it spawns the real dev server as a grandchild.
// Killing only the direct child leaves that grandchild running and holding the
// port, which is exactly the orphan the previous scripts had to hunt down on
// the next run.
func setProcessGroup(cmd *exec.Cmd) <-chan time.Time {
	shutdownStarted := make(chan time.Time, 1)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		select {
		case shutdownStarted <- time.Now():
		default:
		}
		// Negative pid = the whole group. SIGTERM first, so a dev server gets
		// to flush and close its listener.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	// A child that ignores SIGTERM would otherwise hang the whole command:
	// Wait blocks until the output pipes close, and a grandchild holding them
	// open keeps them open. After the grace period Go kills the direct child and
	// closes its copy pipes; finishProcessGroup applies the same bound to the
	// other members of the process group.
	cmd.WaitDelay = processStopGrace
	return shutdownStarted
}

// finishProcessGroup closes the last gap in Cmd.WaitDelay: os/exec force-kills
// only the direct child after the grace period. A shim can also exit while a
// descendant keeps running after closing its output. In both cases, wait out
// the remainder of the group's grace period before forcing its members down.
func finishProcessGroup(cmd *exec.Cmd, shutdownStarted <-chan time.Time) error {
	if cmd.Process == nil || cmd.Process.Pid <= 0 {
		return nil
	}
	pid := cmd.Process.Pid
	alive, err := processGroupAlive(pid)
	if err != nil || !alive {
		return err
	}

	var started time.Time
	select {
	case started = <-shutdownStarted:
	default:
		// The direct command ended without context cancellation but left group
		// members behind. Give them the same graceful signal and deadline.
		started = time.Now()
		if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return nil
			}
			return fmt.Errorf("send SIGTERM: %w", err)
		}
	}

	deadline := started.Add(processStopGrace)
	for time.Now().Before(deadline) {
		alive, err := processGroupAlive(pid)
		if err != nil || !alive {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("send SIGKILL: %w", err)
	}
	return nil
}

func processGroupAlive(pid int) (bool, error) {
	err := syscall.Kill(-pid, 0)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.ESRCH):
		return false, nil
	default:
		return false, fmt.Errorf("inspect process group: %w", err)
	}
}

// openBrowser opens a URL with the platform's handler.
func openBrowser(url string) error {
	for _, opener := range []string{"xdg-open", "open"} {
		path, err := exec.LookPath(opener)
		if err != nil {
			continue
		}
		cmd := exec.Command(path, url)
		if err := cmd.Start(); err != nil {
			return err
		}
		// The opener hands the URL to the desktop and exits immediately. Nobody
		// waits for it, so without this it stays a zombie for as long as the
		// dev session runs — which is the whole working day.
		go cmd.Wait()
		return nil
	}
	return errNoOpener
}
