//go:build windows

package userpath

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func privateMutexName() string {
	return fmt.Sprintf(`Global\HoshiBuildUserPathTest-%d-%d`, os.Getpid(), time.Now().UnixNano())
}

// TestUserPathMutexHelper is inert during ordinary test execution. A subprocess
// owns only a unique test mutex and writes its readiness marker under t.TempDir.
func TestUserPathMutexHelper(t *testing.T) {
	name := os.Getenv("HOSHI_USERPATH_MUTEX_HELPER")
	if name == "" {
		return
	}
	if !strings.HasPrefix(name, `Global\HoshiBuildUserPathTest-`) {
		t.Fatal("helper accepts private test mutexes only")
	}
	release, err := acquireNamedMutex(name, 2000)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := os.WriteFile(os.Getenv("HOSHI_USERPATH_MUTEX_READY"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Second)
	}
}

func TestNativeMutexSerializesProcessesAndRejectsAbandonedOwner(t *testing.T) {
	name := privateMutexName()
	ready := filepath.Join(t.TempDir(), "owned")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestUserPathMutexHelper$")
	child.Env = append(os.Environ(), "HOSHI_USERPATH_MUTEX_HELPER="+name, "HOSHI_USERPATH_MUTEX_READY="+ready)
	var childOutput bytes.Buffer
	child.Stdout, child.Stderr = &childOutput, &childOutput
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("private mutex helper did not acquire ownership")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Keep a handle open so killing the owner leaves an abandoned mutex, rather
	// than destroying the object before the next attempt can inspect its state.
	keepAlive, err := createNamedMutex(name)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(keepAlive)
	store := &memoryStore{}
	m := managerFor(t, `C:\hoshi`, store)
	m.acquire = func() (func() error, error) { return acquireNamedMutex(name, 40) }
	if _, err := m.Add(); !errors.Is(err, errMutexTimeout) || store.reads != 0 || store.writes != 0 {
		t.Fatalf("another process owns the lock: error=%v reads=%d writes=%d", err, store.reads, store.writes)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	waited = true
	if _, err := m.Add(); !errors.Is(err, errMutexAbandoned) || store.reads != 0 || store.writes != 0 {
		t.Fatalf("abandoned owner must fail closed: error=%v reads=%d writes=%d child=%s", err, store.reads, store.writes, &childOutput)
	}
	// Rejecting WAIT_ABANDONED must release its newly granted ownership, close
	// that handle and unpin the thread, so a deliberate retry can proceed.
	result, err := m.Add()
	if err != nil || !result.Changed || store.writes != 1 {
		t.Fatalf("retry after abandoned owner: result=%+v error=%v writes=%d", result, err, store.writes)
	}
}

func TestNativeMutexCanBeReacquiredAndReleasedOnlyOnce(t *testing.T) {
	name := privateMutexName()
	for i := 0; i < 2; i++ {
		release, err := acquireNamedMutex(name, 40)
		if err != nil {
			t.Fatal(err)
		}
		if err := release(); err != nil {
			t.Fatal(err)
		}
		if err := release(); err != nil {
			t.Fatalf("duplicate release: %v", err)
		}
	}
}

func TestNativeMutexCreationErrorsDoNotGrantOwnership(t *testing.T) {
	release, err := acquireNamedMutex("invalid\x00name", 40)
	if err == nil || release != nil {
		t.Fatalf("invalid mutex name: release=%v error=%v", release != nil, err)
	}
	release, err = acquireNamedMutex(privateMutexName(), 40)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestProductionMutexNameUsesTokenSIDAndIsInstalledOnManager(t *testing.T) {
	name, err := userMutexName()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(name, `Global\Hoshivel.HoshiBuild.UserPath-S-1-`) {
		t.Fatalf("unexpected mutex namespace: %q", name)
	}
	t.Setenv("USERNAME", "not-the-token-user")
	if again, err := userMutexName(); err != nil || again != name {
		t.Fatalf("environment changed mutex identity: %q, %v", again, err)
	}
	m, err := New()
	if err != nil || m.acquire == nil {
		t.Fatalf("production manager has no writer lock: %v", err)
	}
	// Never acquire this production mutex or touch the real Environment store.
}
