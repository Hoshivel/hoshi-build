// Package userpath manages only the running hoshi installation's Windows user PATH entry.
package userpath

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"unicode/utf16"
)

const (
	stringValue       uint32 = 1 // REG_SZ
	expandStringValue uint32 = 2 // REG_EXPAND_SZ
	maxPathUnits             = 32767
)

// ErrUnsupported is returned without editing shell startup files on other platforms.
var ErrUnsupported = errors.New("hoshi env path 僅支援 Windows 的使用者 PATH；其他系統請依 README 的安裝方式設定 PATH")

type pathValue struct {
	Exists bool
	Kind   uint32
	Raw    string
}

type pathStore interface {
	Read() (pathValue, error)
	Write(pathValue) error
}

// Result describes this installation's entry, never the full user or machine PATH.
type Result struct {
	Directory         string
	Present           bool
	Changed           bool
	NotificationError error
}

// Manager edits one directory and leaves unrelated PATH entries and their order intact.
type Manager struct {
	directory string
	store     pathStore
	expand    func(string) (string, error)
	notify    func() error
	acquire   func() (func() error, error)
}

func newManager(directory string, store pathStore, expand func(string) (string, error), notify func() error) (*Manager, error) {
	if strings.ContainsAny(directory, ";\x00\r\n\"") {
		return nil, fmt.Errorf("hoshi 所在目錄不能含分號、引號或控制字元：%q", directory)
	}
	if _, absolute := windowsPath(directory); !absolute {
		return nil, fmt.Errorf("hoshi 所在目錄必須是 Windows 絕對路徑：%q", directory)
	}
	return &Manager{directory: directory, store: store, expand: expand, notify: notify}, nil
}

func (m *Manager) read() (pathValue, error) {
	value, err := m.store.Read()
	if err != nil {
		return pathValue{}, fmt.Errorf("讀取使用者 PATH：%w", err)
	}
	if value.Exists && value.Kind != stringValue && value.Kind != expandStringValue {
		return pathValue{}, fmt.Errorf("使用者 PATH 的登錄型別 %d 不是 REG_SZ 或 REG_EXPAND_SZ，未修改", value.Kind)
	}
	return value, nil
}

func (m *Manager) matches(entry string, kind uint32) (bool, error) {
	entry = strings.TrimSpace(entry)
	if kind == expandStringValue && strings.Contains(entry, "%") {
		var err error
		entry, err = m.expand(entry)
		if err != nil {
			return false, fmt.Errorf("展開使用者 PATH 的目錄：%w", err)
		}
	}
	if len(entry) >= 2 && entry[0] == '"' && entry[len(entry)-1] == '"' {
		entry = entry[1 : len(entry)-1]
	}
	actual, absolute := windowsPath(entry)
	want, _ := windowsPath(m.directory)
	return absolute && strings.EqualFold(actual, want), nil
}

// Split only at unquoted separators and retain every byte of unrelated entries.
// An unfinished quote makes appending or removing an entry ambiguous.
func pathEntries(raw string) ([]string, error) {
	var entries []string
	start, quoted := 0, false
	for i, ch := range raw {
		if ch == '"' {
			quoted = !quoted
		} else if ch == ';' && !quoted {
			entries = append(entries, raw[start:i])
			start = i + 1
		}
	}
	if quoted {
		return nil, fmt.Errorf("使用者 PATH 有未閉合的引號，未修改；請先修正該項目")
	}
	return append(entries, raw[start:]), nil
}

// Status reports whether the executable directory is already in the persistent user PATH.
func (m *Manager) Status() (Result, error) {
	value, err := m.read()
	if err != nil {
		return Result{}, err
	}
	result := Result{Directory: m.directory}
	if !value.Exists {
		return result, nil
	}
	entries, err := pathEntries(value.Raw)
	if err != nil {
		return Result{}, err
	}
	for _, entry := range entries {
		matched, err := m.matches(entry, value.Kind)
		if err != nil {
			return Result{}, err
		}
		if matched {
			result.Present = true
			break
		}
	}
	return result, nil
}

// Add appends the executable directory unless an equivalent user entry already exists.
func (m *Manager) Add() (Result, error) { return m.change(true) }

// Remove removes only entries equivalent to the executable directory, without deleting files.
func (m *Manager) Remove() (Result, error) { return m.change(false) }

func (m *Manager) change(add bool) (Result, error) {
	result, err := m.changeUnderLock(add)
	if err == nil && result.Changed && m.notify != nil {
		// Broadcasting to other applications must not hold the registry edit lock.
		result.NotificationError = m.notify()
	}
	return result, err
}

func (m *Manager) changeUnderLock(add bool) (result Result, err error) {
	if m.acquire != nil {
		release, lockErr := m.acquire()
		if lockErr != nil {
			return Result{}, fmt.Errorf("取得使用者 PATH 寫入鎖：%w", lockErr)
		}
		defer func() {
			if releaseErr := release(); releaseErr != nil {
				err = errors.Join(err, fmt.Errorf("釋放 PATH 寫入鎖失敗，PATH 可能已更新：%w", releaseErr))
			}
		}()
	}
	return m.planAndWrite(add)
}

func (m *Manager) planAndWrite(add bool) (Result, error) {
	before, err := m.read()
	if err != nil {
		return Result{}, err
	}
	result := Result{Directory: m.directory}
	after := before
	if !before.Exists {
		if !add {
			return result, nil
		}
		after = pathValue{Exists: true, Kind: expandStringValue}
	}
	var kept []string
	if before.Exists {
		entries, err := pathEntries(before.Raw)
		if err != nil {
			return Result{}, err
		}
		for _, entry := range entries {
			matched, err := m.matches(entry, before.Kind)
			if err != nil {
				return Result{}, err
			}
			if matched {
				result.Present = true
			} else {
				kept = append(kept, entry)
			}
		}
	}
	if add {
		if result.Present {
			return result, nil
		}
		// An absolute directory may itself contain a literal %NAME%. Do not write
		// it as expandable data if that would point at a different directory.
		if after.Kind == expandStringValue && strings.Contains(m.directory, "%") {
			expanded, err := m.expand(m.directory)
			if err != nil {
				return Result{}, err
			}
			if expanded != m.directory {
				return Result{}, fmt.Errorf("hoshi 所在目錄含有會被展開的環境變數字樣，請移到固定目錄後重試：%q", m.directory)
			}
		}
		after.Raw = m.directory
		if before.Raw != "" {
			after.Raw = before.Raw + ";" + m.directory
		}
		result.Present = true
	} else {
		if !result.Present {
			return result, nil
		}
		after.Raw = strings.Join(kept, ";")
		result.Present = false
	}
	if len(utf16.Encode([]rune(after.Raw)))+1 > maxPathUnits {
		return Result{}, fmt.Errorf("使用者 PATH 超過 Windows 環境變數長度限制，未修改")
	}
	// The mutex serializes cooperating hoshi writers. Keep this second read to
	// detect intervening external edits: Registry has no compare-and-swap API,
	// and unrelated applications do not participate in the hoshi mutex.
	latest, err := m.read()
	if err != nil {
		return Result{}, err
	}
	if latest != before {
		return Result{}, fmt.Errorf("使用者 PATH 已由其他程序變更，未寫入；請重試")
	}
	if err := m.store.Write(after); err != nil {
		return Result{}, fmt.Errorf("寫入使用者 PATH：%w", err)
	}
	result.Changed = true
	return result, nil
}

// windowsPath performs lexical comparison without resolving relative entries or
// changing stored text. Keep drive roots and UNC shares distinct on every test OS.
func windowsPath(value string) (string, bool) {
	value = strings.ReplaceAll(value, `\`, "/")
	if len(value) >= 8 && strings.EqualFold(value[:8], "//?/UNC/") {
		value = "//" + value[8:]
	} else if strings.HasPrefix(value, "//?/") {
		value = value[4:]
	}
	if len(value) >= 3 && value[1] == ':' && value[2] == '/' &&
		((value[0] >= 'a' && value[0] <= 'z') || (value[0] >= 'A' && value[0] <= 'Z')) {
		return value[:2] + path.Clean("/"+value[3:]), true
	}
	if strings.HasPrefix(value, "//") {
		parts := strings.SplitN(value[2:], "/", 3)
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" || parts[0] == "." || parts[0] == "?" {
			return value, false
		}
		rest := ""
		if len(parts) == 3 {
			rest = parts[2]
		}
		return "//" + parts[0] + "/" + parts[1] + path.Clean("/"+rest), true
	}
	return value, false
}
