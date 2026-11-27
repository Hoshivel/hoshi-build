//go:build windows

package userpath

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	registryDLL        = syscall.NewLazyDLL("advapi32.dll")
	createRegistryKey  = registryDLL.NewProc("RegCreateKeyExW")
	setRegistryValue   = registryDLL.NewProc("RegSetValueExW")
	user32DLL          = syscall.NewLazyDLL("user32.dll")
	sendSettingChanged = user32DLL.NewProc("SendMessageTimeoutW")
	kernelDLL          = syscall.NewLazyDLL("kernel32.dll")
	expandStrings      = kernelDLL.NewProc("ExpandEnvironmentStringsW")
)

type registryStore struct {
	root   syscall.Handle
	subkey string
}

// New locates the running executable and touches only HKCU\Environment\Path.
func New() (*Manager, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("找不到 hoshi 執行檔：%w", err)
	}
	directory, err := filepath.Abs(filepath.Dir(executable))
	if err != nil {
		return nil, err
	}
	manager, err := newManager(directory, registryStore{syscall.HKEY_CURRENT_USER, `Environment`}, expandEnvironment, notifyEnvironment)
	if err != nil {
		return nil, err
	}
	name, err := userMutexName()
	if err != nil {
		return nil, err
	}
	manager.acquire = func() (func() error, error) { return acquireNamedMutex(name, 5000) }
	return manager, nil
}

func (s registryStore) Read() (pathValue, error) {
	subkey, err := syscall.UTF16PtrFromString(s.subkey)
	if err != nil {
		return pathValue{}, err
	}
	var key syscall.Handle
	err = syscall.RegOpenKeyEx(s.root, subkey, 0, syscall.KEY_QUERY_VALUE, &key)
	if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) || errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
		return pathValue{}, nil
	}
	if err != nil {
		return pathValue{}, err
	}
	defer syscall.RegCloseKey(key)
	name, _ := syscall.UTF16PtrFromString("Path")
	return readRegistryPath(func(data []byte) (uint32, uint32, error) {
		var ptr *byte
		if len(data) > 0 {
			ptr = &data[0]
		}
		var kind uint32
		size := uint32(len(data))
		err := syscall.RegQueryValueEx(key, name, nil, &kind, ptr, &size)
		return kind, size, err
	})
}

// readRegistryPath factors the Win32 size/data query sequence for deterministic
// concurrent-value tests without touching the real user environment key.
func readRegistryPath(query func([]byte) (uint32, uint32, error)) (pathValue, error) {
	kind, size, err := query(nil)
	if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
		return pathValue{}, nil
	}
	if err != nil {
		return pathValue{}, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		if size > 1<<20 {
			return pathValue{}, fmt.Errorf("使用者 PATH 登錄值過大，未讀取")
		}
		data := make([]byte, size)
		kind, size, err = query(data)
		// A nil data pointer asks only for metadata. If a zero-length value
		// grows, Win32 can return SUCCESS and its new size without copying data.
		if errors.Is(err, syscall.ERROR_MORE_DATA) || (err == nil && size > uint32(len(data))) {
			continue
		}
		if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) {
			return pathValue{}, nil
		}
		if err != nil {
			return pathValue{}, err
		}
		value := pathValue{Exists: true, Kind: kind}
		if kind != stringValue && kind != expandStringValue {
			return value, nil
		}
		value.Raw, err = registryString(data[:size])
		return value, err
	}
	return pathValue{}, fmt.Errorf("使用者 PATH 持續變更，請重試")
}

func (s registryStore) Write(value pathValue) error {
	if !value.Exists || (value.Kind != stringValue && value.Kind != expandStringValue) {
		return fmt.Errorf("只可寫入字串型別的使用者 PATH")
	}
	data, err := syscall.UTF16FromString(value.Raw)
	if err != nil {
		return err
	}
	subkey, err := syscall.UTF16PtrFromString(s.subkey)
	if err != nil {
		return err
	}
	var key syscall.Handle
	code, _, _ := createRegistryKey.Call(uintptr(s.root), uintptr(unsafe.Pointer(subkey)), 0, 0, 0,
		syscall.KEY_SET_VALUE, 0, uintptr(unsafe.Pointer(&key)), 0)
	if code != 0 {
		return syscall.Errno(code)
	}
	defer syscall.RegCloseKey(key)
	name, _ := syscall.UTF16PtrFromString("Path")
	code, _, _ = setRegistryValue.Call(uintptr(key), uintptr(unsafe.Pointer(name)), 0, uintptr(value.Kind),
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)*2))
	runtime.KeepAlive(data)
	if code != 0 {
		return syscall.Errno(code)
	}
	return nil
}

func registryString(data []byte) (string, error) {
	if len(data)%2 != 0 {
		return "", fmt.Errorf("PATH 登錄字串不是完整 UTF-16，未修改")
	}
	units := make([]uint16, len(data)/2)
	for i := range units {
		units[i] = binary.LittleEndian.Uint16(data[i*2:])
	}
	for len(units) > 0 && units[len(units)-1] == 0 {
		units = units[:len(units)-1]
	}
	for i := 0; i < len(units); i++ {
		unit := units[i]
		if unit == 0 {
			return "", fmt.Errorf("PATH 登錄字串含內嵌 NUL，未修改")
		}
		if unit >= 0xD800 && unit <= 0xDBFF {
			if i+1 >= len(units) || units[i+1] < 0xDC00 || units[i+1] > 0xDFFF {
				return "", fmt.Errorf("PATH 登錄字串含無效 UTF-16，未修改")
			}
			i++
		} else if unit >= 0xDC00 && unit <= 0xDFFF {
			return "", fmt.Errorf("PATH 登錄字串含無效 UTF-16，未修改")
		}
	}
	return string(utf16.Decode(units)), nil
}

func expandEnvironment(value string) (string, error) {
	input, err := syscall.UTF16PtrFromString(value)
	if err != nil {
		return "", err
	}
	size, _, callErr := expandStrings.Call(uintptr(unsafe.Pointer(input)), 0, 0)
	for attempt := 0; attempt < 3; attempt++ {
		if size == 0 {
			return "", fmt.Errorf("ExpandEnvironmentStringsW：%v", callErr)
		}
		if size > maxPathUnits {
			return "", fmt.Errorf("展開的 PATH 目錄超過 Windows 環境變數長度限制")
		}
		data := make([]uint16, size)
		written, _, err := expandStrings.Call(uintptr(unsafe.Pointer(input)), uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
		if written > uintptr(len(data)) {
			size = written
			continue
		}
		if written == 0 {
			return "", fmt.Errorf("ExpandEnvironmentStringsW：%v", err)
		}
		return syscall.UTF16ToString(data[:written]), nil
	}
	return "", fmt.Errorf("環境變數持續變更，請重試")
}

func notifyEnvironment() error {
	name, _ := syscall.UTF16PtrFromString("Environment")
	var result uintptr
	// HWND_BROADCAST applies this 1000ms timeout per receiving window, not globally.
	ok, _, err := sendSettingChanged.Call(0xffff, 0x001a, 0, uintptr(unsafe.Pointer(name)), 0x0002, 1000,
		uintptr(unsafe.Pointer(&result)))
	if ok == 0 {
		return fmt.Errorf("WM_SETTINGCHANGE 通知未完成：%v", err)
	}
	return nil
}
