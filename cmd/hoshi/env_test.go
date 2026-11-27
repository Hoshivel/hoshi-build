package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/hoshivel/hoshi-build/internal/userpath"
)

type fakePathManager struct {
	result userpath.Result
	err    error
	calls  []string
}

func (m *fakePathManager) Status() (userpath.Result, error) {
	m.calls = append(m.calls, "status")
	return m.result, m.err
}
func (m *fakePathManager) Add() (userpath.Result, error) {
	m.calls = append(m.calls, "add")
	return m.result, m.err
}
func (m *fakePathManager) Remove() (userpath.Result, error) {
	m.calls = append(m.calls, "remove")
	return m.result, m.err
}

func TestEnvPathVerbsUseInjectedManagerWithoutProjectOrHostMutation(t *testing.T) {
	for _, action := range []string{"", "status", "add", "remove"} {
		t.Run("action="+action, func(t *testing.T) {
			manager := &fakePathManager{result: userpath.Result{Directory: `C:\My Tools\hoshi`, Present: true}}
			args := []string{"path"}
			if action != "" {
				args = append(args, action)
			}
			var output bytes.Buffer
			err := runEnv(args, func() (pathManager, error) { return manager, nil }, &output)
			want := action
			if want == "" {
				want = "status"
			}
			if err != nil || len(manager.calls) != 1 || manager.calls[0] != want || !strings.Contains(output.String(), manager.result.Directory) {
				t.Fatalf("calls=%v output=%q error=%v", manager.calls, output.String(), err)
			}
		})
	}
}

func TestEnvHelpAndInvalidArgumentsDoNotOpenTheEnvironment(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"path", "--help"}, {"other"}, {"path", "delete"}, {"path", "add", "extra"}} {
		calls := 0
		var output bytes.Buffer
		err := runEnv(args, func() (pathManager, error) { calls++; return nil, nil }, &output)
		wantError := len(args) > 0 && args[0] != "--help" && !(len(args) == 2 && args[1] == "--help")
		if calls != 0 || (err != nil) != wantError {
			t.Errorf("args=%v calls=%d error=%v", args, calls, err)
		}
	}
}

func TestEnvUnsupportedPlatformAndStoreErrorsPropagate(t *testing.T) {
	var output bytes.Buffer
	if err := runEnv([]string{"path", "add"}, func() (pathManager, error) { return nil, userpath.ErrUnsupported }, &output); !errors.Is(err, userpath.ErrUnsupported) {
		t.Fatalf("unsupported error=%v", err)
	}
	denied := errors.New("read-only environment")
	manager := &fakePathManager{err: denied}
	if err := runEnv([]string{"path", "remove"}, func() (pathManager, error) { return manager, nil }, &output); !errors.Is(err, denied) {
		t.Fatalf("store error=%v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("failed operations claimed success: %q", output.String())
	}
}

func TestEnvAddExplainsTerminalScopeAndQuotesCurrentShellCommand(t *testing.T) {
	manager := &fakePathManager{result: userpath.Result{Directory: `C:\O'Brien 工具\hoshi`, Present: true,
		Changed: true, NotificationError: errors.New("receiver timeout")}}
	var output bytes.Buffer
	if err := runEnv([]string{"path", "add"}, func() (pathManager, error) { return manager, nil }, &output); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"已加入", "環境變更已儲存", "既有終端", `$env:Path += ';' + 'C:\O''Brien 工具\hoshi'`} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("output missing %q: %s", want, output.String())
		}
	}
}
