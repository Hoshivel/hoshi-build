//go:build windows

package userpath

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func privateRegistryStore(t *testing.T) registryStore {
	t.Helper()
	// A unique leaf under Software, never the real HKCU\Environment key. Its
	// parent already exists, so removing the owned leaf leaves no test hierarchy.
	subkey := fmt.Sprintf(`Software\HoshiBuildUserPathTest-%d-%d`, os.Getpid(), time.Now().UnixNano())
	store := registryStore{syscall.HKEY_CURRENT_USER, subkey}
	t.Cleanup(func() {
		name, _ := syscall.UTF16PtrFromString(subkey)
		code, _, _ := registryDLL.NewProc("RegDeleteKeyW").Call(uintptr(store.root), uintptr(unsafe.Pointer(name)))
		if code != 0 && code != uintptr(syscall.ERROR_FILE_NOT_FOUND) {
			t.Errorf("remove private registry test key: %v", syscall.Errno(code))
		}
	})
	return store
}

func TestNativeRegistryRoundTripPreservesRawValueAndType(t *testing.T) {
	for _, kind := range []uint32{stringValue, expandStringValue} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			store := privateRegistryStore(t)
			if value, err := store.Read(); err != nil || value.Exists {
				t.Fatalf("missing key = %+v, %v", value, err)
			}
			before := pathValue{true, kind, `%LOCALAPPDATA%\保留;"C:\既有 工具";;D:\Other\`}
			if err := store.Write(before); err != nil {
				t.Fatal(err)
			}
			if got, err := store.Read(); err != nil || got != before {
				t.Fatalf("registry round trip = %+v, %v; want %+v", got, err, before)
			}
			m, err := newManager(`C:\測試\hoshi`, store, expandEnvironment, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.Add(); err != nil {
				t.Fatal(err)
			}
			after, err := store.Read()
			if err != nil || after.Kind != kind || after.Raw != before.Raw+`;C:\測試\hoshi` {
				t.Fatalf("native add = %+v, %v", after, err)
			}
			if _, err := m.Remove(); err != nil {
				t.Fatal(err)
			}
			if got, err := store.Read(); err != nil || got != before {
				t.Fatalf("native remove = %+v, %v; want %+v", got, err, before)
			}
		})
	}
}

func TestNativeExpansionFindsEquivalentEntryWithoutRewritingIt(t *testing.T) {
	t.Setenv("HOSHI_USERPATH_TEST_ROOT", `C:\Temporary\含空格 工具`)
	store := privateRegistryStore(t)
	before := pathValue{true, expandStringValue, `%HOSHI_USERPATH_TEST_ROOT%\hoshi`}
	if err := store.Write(before); err != nil {
		t.Fatal(err)
	}
	m, err := newManager(`C:\Temporary\含空格 工具\hoshi`, store, expandEnvironment, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := m.Add()
	if err != nil || !result.Present || result.Changed {
		t.Fatalf("native expanded add = %+v, %v", result, err)
	}
	if got, err := store.Read(); err != nil || got != before {
		t.Fatalf("native expansion changed raw registry data: %+v, %v", got, err)
	}
}

func TestNewUsesTheActualExecutableDirectory(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	m, err := New()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(filepath.Dir(executable))
	if err != nil || m.directory != want {
		t.Fatalf("directory=%q, want %q; error=%v", m.directory, want, err)
	}
	// Do not call Add, Remove or notify on this production store in a test.
}

func TestMalformedRegistryStringsAreRejectedWithoutLosingData(t *testing.T) {
	for _, data := range [][]byte{{1}, {0, 0, 'a', 0}, {0, 0xD8}, {0, 0xDC}} {
		if _, err := registryString(data); err == nil {
			t.Errorf("malformed bytes accepted: %x", data)
		}
	}
	data := make([]byte, 6)
	binary.LittleEndian.PutUint16(data, 0xD83C)
	binary.LittleEndian.PutUint16(data[2:], 0xDF1F)
	if got, err := registryString(data); err != nil || got != "🌟" {
		t.Fatalf("valid surrogate pair = %q, %v", got, err)
	}
}

func TestReadRetriesWhenEmptyValueGrowsBeforeDataQuery(t *testing.T) {
	// RegQueryValueEx with a nil data pointer is a metadata query. An empty
	// value may grow after sizing, so that second query can succeed with a size
	// larger than the allocated zero-length buffer instead of ERROR_MORE_DATA.
	data := []byte{'C', 0, ':', 0, '\\', 0, 'A', 0, 0, 0}
	calls := 0
	value, err := readRegistryPath(func(buffer []byte) (uint32, uint32, error) {
		calls++
		if calls == 1 {
			return stringValue, 0, nil
		}
		if len(buffer) < len(data) {
			return stringValue, uint32(len(data)), nil
		}
		copy(buffer, data)
		return stringValue, uint32(len(data)), nil
	})
	if err != nil || value != (pathValue{true, stringValue, `C:\A`}) || calls != 3 {
		t.Fatalf("grown empty value = %+v, %v; calls=%d", value, err, calls)
	}
}

func TestReadRetriesMoreDataAndBoundsRepeatedSuccessfulGrowth(t *testing.T) {
	t.Run("ERROR_MORE_DATA", func(t *testing.T) {
		data := []byte{'A', 0, 'B', 0, 0, 0}
		calls := 0
		value, err := readRegistryPath(func(buffer []byte) (uint32, uint32, error) {
			calls++
			if calls == 1 {
				return stringValue, 2, nil
			}
			if len(buffer) < len(data) {
				return stringValue, uint32(len(data)), syscall.ERROR_MORE_DATA
			}
			copy(buffer, data)
			return stringValue, uint32(len(data)), nil
		})
		if err != nil || value.Raw != "AB" || calls != 3 {
			t.Fatalf("MORE_DATA value = %+v, %v; calls=%d", value, err, calls)
		}
	})
	t.Run("continuous growth", func(t *testing.T) {
		calls := 0
		_, err := readRegistryPath(func(buffer []byte) (uint32, uint32, error) {
			calls++
			return stringValue, uint32(len(buffer) + 2), nil
		})
		if err == nil || !strings.Contains(err.Error(), "持續變更") || calls != 4 {
			t.Fatalf("continuous growth error=%v; calls=%d", err, calls)
		}
	})
}
