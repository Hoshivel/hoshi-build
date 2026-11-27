package userpath

import (
	"errors"
	"strings"
	"testing"
)

type memoryStore struct {
	value    pathValue
	reads    int
	writes   int
	readErr  error
	writeErr error
	onRead   func(int) pathValue
}

func (s *memoryStore) Read() (pathValue, error) {
	s.reads++
	if s.onRead != nil {
		return s.onRead(s.reads), s.readErr
	}
	return s.value, s.readErr
}

func (s *memoryStore) Write(value pathValue) error {
	s.writes++
	if s.writeErr == nil {
		s.value = value
	}
	return s.writeErr
}

func managerFor(t *testing.T, directory string, store *memoryStore) *Manager {
	t.Helper()
	m, err := newManager(directory, store, func(value string) (string, error) {
		return strings.ReplaceAll(value, "%LOCALAPPDATA%", `C:\Users\測試\AppData\Local`), nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestQuotedSemicolonEntryIsPreserved(t *testing.T) {
	const directory = `\\server\share\hoshi`
	const raw = `"C:\one;\\server\share\hoshi;tail";C:\other`
	store := &memoryStore{value: pathValue{true, stringValue, raw}}
	m := managerFor(t, directory, store)
	status, err := m.Status()
	if err != nil || status.Present {
		t.Fatalf("status=%+v err=%v", status, err)
	}
	removed, err := m.Remove()
	if err != nil || removed.Changed || store.value.Raw != raw {
		t.Fatalf("remove=%+v err=%v raw=%q", removed, err, store.value.Raw)
	}
	added, err := m.Add()
	if err != nil || !added.Changed || store.value.Raw != raw+";"+directory {
		t.Fatalf("add=%+v err=%v raw=%q", added, err, store.value.Raw)
	}
	removed, err = m.Remove()
	if err != nil || !removed.Changed || store.value.Raw != raw {
		t.Fatalf("remove added=%+v err=%v raw=%q", removed, err, store.value.Raw)
	}
}

func TestUnclosedPathQuoteRefusesChanges(t *testing.T) {
	const raw = `"C:\one;C:\hoshi`
	for _, action := range []string{"status", "add", "remove"} {
		t.Run(action, func(t *testing.T) {
			store := &memoryStore{value: pathValue{true, stringValue, raw}}
			m := managerFor(t, `C:\hoshi`, store)
			call := map[string]func() (Result, error){"status": m.Status, "add": m.Add, "remove": m.Remove}[action]
			if _, err := call(); err == nil || store.writes != 0 || store.value.Raw != raw {
				t.Fatalf("err=%v writes=%d raw=%q", err, store.writes, store.value.Raw)
			}
		})
	}
}

func TestAddPreservesRawUnrelatedEntriesAndRegistryType(t *testing.T) {
	for _, kind := range []uint32{stringValue, expandStringValue} {
		t.Run(map[uint32]string{stringValue: "REG_SZ", expandStringValue: "REG_EXPAND_SZ"}[kind], func(t *testing.T) {
			raw := `%LOCALAPPDATA%\Other; "C:\既有 工具\";;D:/Tools/;`
			store := &memoryStore{value: pathValue{true, kind, raw}}
			m := managerFor(t, `C:\Users\測試\AppData\Local\Programs\hoshi\bin`, store)
			notifications := 0
			m.notify = func() error { notifications++; return nil }

			added, err := m.Add()
			if err != nil {
				t.Fatal(err)
			}
			if !added.Changed || !added.Present || store.value.Kind != kind || store.value.Raw != raw+";"+m.directory {
				t.Fatalf("add = %+v; persisted = %+v", added, store.value)
			}
			again, err := m.Add()
			if err != nil || again.Changed || !again.Present || store.writes != 1 || notifications != 1 {
				t.Fatalf("repeated add = %+v, %v; writes=%d notifications=%d", again, err, store.writes, notifications)
			}
		})
	}
}

func TestRemoveOnlyMatchingEntriesAndKeepsRawOrder(t *testing.T) {
	store := &memoryStore{value: pathValue{true, expandStringValue,
		`;C:\Other;"c:/users/測試/appdata/local/programs/hoshi/bin/";;%LOCALAPPDATA%\Programs\hoshi\bin;D:\保留\`}}
	m := managerFor(t, `C:\Users\測試\AppData\Local\Programs\hoshi\bin`, store)

	removed, err := m.Remove()
	if err != nil {
		t.Fatal(err)
	}
	if !removed.Changed || removed.Present || store.value.Raw != `;C:\Other;;D:\保留\` || store.value.Kind != expandStringValue {
		t.Fatalf("remove = %+v; persisted = %+v", removed, store.value)
	}
	again, err := m.Remove()
	if err != nil || again.Changed || store.writes != 1 {
		t.Fatalf("repeated remove = %+v, %v; writes=%d", again, err, store.writes)
	}
}

func TestPathComparisonPreservesWindowsDriveAndUNCRootSemantics(t *testing.T) {
	for _, tc := range []struct {
		name, directory, entry string
		want                   bool
	}{
		{"quoted Unicode and separators", `C:\工具\hoshi`, `"c:/工具/./hoshi/"`, true},
		{"parent directory", `C:\tools\hoshi`, `C:\tools\old\..\hoshi\`, true},
		{"drive root", `C:\`, `c:/`, true},
		{"drive relative is different", `C:\`, `C:`, false},
		{"root relative is different", `C:\tools`, `\tools`, false},
		{"UNC", `\\server\share\hoshi`, `\\SERVER\SHARE\old\..\hoshi\`, true},
		{"UNC share root", `\\server\share\`, `\\server\share\child\..`, true},
		{"UNC cannot escape share", `\\server\other`, `\\server\share\..\other`, false},
		{"UNC is not root relative", `\\server\share\hoshi`, `\server\share\hoshi`, false},
		{"extended drive", `\\?\C:\tools\hoshi`, `C:\tools\hoshi`, true},
		{"extended UNC", `\\?\UNC\server\share\hoshi`, `\\server\share\hoshi`, true},
		{"another install", `C:\tools\hoshi`, `C:\tools\hoshi-old`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryStore{value: pathValue{true, stringValue, tc.entry}}
			result, err := managerFor(t, tc.directory, store).Status()
			if err != nil || result.Present != tc.want || store.writes != 0 {
				t.Fatalf("status = %+v, %v; want present %v", result, err, tc.want)
			}
		})
	}
}

func TestExpandStringMatchesVariablesWithoutRewritingThem(t *testing.T) {
	raw := `%LOCALAPPDATA%\Programs\hoshi\bin;C:\Other`
	store := &memoryStore{value: pathValue{true, expandStringValue, raw}}
	result, err := managerFor(t, `C:\Users\測試\AppData\Local\Programs\hoshi\bin`, store).Add()
	if err != nil || result.Changed || !result.Present || store.writes != 0 || store.value.Raw != raw {
		t.Fatalf("add = %+v, %v; persisted=%+v", result, err, store.value)
	}
}

func TestStringTypeDoesNotExpandLiteralVariableNames(t *testing.T) {
	store := &memoryStore{value: pathValue{true, stringValue, `%LOCALAPPDATA%\Programs\hoshi\bin`}}
	m := managerFor(t, `C:\Users\測試\AppData\Local\Programs\hoshi\bin`, store)
	m.expand = func(string) (string, error) { t.Fatal("REG_SZ must not expand variables"); return "", nil }
	result, err := m.Add()
	if err != nil || !result.Changed || store.value.Kind != stringValue {
		t.Fatalf("add = %+v, %v; persisted=%+v", result, err, store.value)
	}
}

func TestQuotesAreInterpretedAfterVariableExpansion(t *testing.T) {
	for _, tc := range []struct {
		entry string
		want  bool
	}{
		{`%TOOLS%`, true},
		{`"%TOOLS%"`, false}, // Expansion produces two pairs, not one valid quoted entry.
	} {
		store := &memoryStore{value: pathValue{true, expandStringValue, tc.entry}}
		m := managerFor(t, `C:\工具\hoshi`, store)
		m.expand = func(value string) (string, error) {
			return strings.ReplaceAll(value, "%TOOLS%", `"C:\工具\hoshi"`), nil
		}
		result, err := m.Status()
		if err != nil || result.Present != tc.want {
			t.Errorf("entry %q: result=%+v error=%v", tc.entry, result, err)
		}
	}
}

func TestMissingPathStatusAndRemoveDoNotCreateIt(t *testing.T) {
	store := &memoryStore{}
	m := managerFor(t, `C:\tools\hoshi`, store)
	for _, operation := range []func() (Result, error){m.Status, m.Remove} {
		result, err := operation()
		if err != nil || result.Present || result.Changed || store.writes != 0 {
			t.Fatalf("missing path = %+v, %v; writes=%d", result, err, store.writes)
		}
	}
	result, err := m.Add()
	if err != nil || !result.Changed || store.value != (pathValue{true, expandStringValue, m.directory}) {
		t.Fatalf("first add = %+v, %v; persisted=%+v", result, err, store.value)
	}
}

func TestReadAndReadOnlyErrorsNeverOverwritePath(t *testing.T) {
	denied := errors.New("access denied")
	for _, stage := range []string{"read", "write"} {
		t.Run(stage, func(t *testing.T) {
			before := pathValue{true, expandStringValue, `C:\Other`}
			store := &memoryStore{value: before}
			if stage == "read" {
				store.readErr = denied
			} else {
				store.writeErr = denied
			}
			_, err := managerFor(t, `C:\tools\hoshi`, store).Add()
			if !errors.Is(err, denied) || store.value != before {
				t.Fatalf("error=%v persisted=%+v", err, store.value)
			}
		})
	}
}

func TestConcurrentObservedEditIsPreserved(t *testing.T) {
	before := pathValue{true, stringValue, `C:\Other`}
	concurrent := pathValue{true, expandStringValue, `C:\Other;D:\NewTool`}
	store := &memoryStore{value: concurrent, onRead: func(n int) pathValue {
		if n == 1 {
			return before
		}
		return concurrent
	}}
	_, err := managerFor(t, `C:\tools\hoshi`, store).Add()
	if err == nil || !strings.Contains(err.Error(), "其他程序變更") || store.writes != 0 || store.value != concurrent {
		t.Fatalf("error=%v writes=%d persisted=%+v", err, store.writes, store.value)
	}
}

func TestUnsupportedRegistryTypeIsNotChanged(t *testing.T) {
	store := &memoryStore{value: pathValue{true, 3, "binary"}}
	m := managerFor(t, `C:\tools\hoshi`, store)
	for _, operation := range []func() (Result, error){m.Status, m.Add, m.Remove} {
		if _, err := operation(); err == nil {
			t.Fatal("non-string registry value accepted")
		}
	}
	if store.writes != 0 {
		t.Fatal("non-string registry value changed")
	}
}

func TestUnsafeOrRelativeDirectoryIsRejectedBeforeReadingStore(t *testing.T) {
	for _, directory := range []string{`C:\tools;C:\injected`, `relative\tools`, `C:tools`, `\tools`, "C:\\tools\x00bad"} {
		store := &memoryStore{}
		if _, err := newManager(directory, store, nil, nil); err == nil || store.reads != 0 || store.writes != 0 {
			t.Errorf("directory %q: err=%v store=%+v", directory, err, store)
		}
	}
}

func TestLiteralPercentDirectoryCannotChangeMeaningInExpandablePath(t *testing.T) {
	store := &memoryStore{value: pathValue{true, expandStringValue, `C:\Other`}}
	m := managerFor(t, `C:\%LOCALAPPDATA%\hoshi`, store)
	if _, err := m.Add(); err == nil || store.writes != 0 {
		t.Fatalf("ambiguous percent directory: error=%v writes=%d", err, store.writes)
	}
}

func TestPathLengthLimitRejectsBeforeWriting(t *testing.T) {
	store := &memoryStore{value: pathValue{true, stringValue, strings.Repeat("界", maxPathUnits)}}
	if _, err := managerFor(t, `C:\hoshi`, store).Add(); err == nil || store.writes != 0 {
		t.Fatalf("oversized path: error=%v writes=%d", err, store.writes)
	}
}

func TestNotificationFailureDoesNotUndoSuccessfulWrite(t *testing.T) {
	store := &memoryStore{}
	m := managerFor(t, `C:\hoshi`, store)
	notifyError := errors.New("a receiving window timed out")
	m.notify = func() error { return notifyError }
	result, err := m.Add()
	if err != nil || !result.Changed || !errors.Is(result.NotificationError, notifyError) || store.value.Raw != m.directory {
		t.Fatalf("add=%+v error=%v persisted=%+v", result, err, store.value)
	}
}
