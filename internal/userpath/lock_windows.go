//go:build windows

package userpath

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

var (
	createMutexProc   = kernelDLL.NewProc("CreateMutexW")
	releaseMutexProc  = kernelDLL.NewProc("ReleaseMutex")
	errMutexTimeout   = errors.New("等待使用者 PATH 寫入鎖逾時，未修改；請稍後重試")
	errMutexAbandoned = errors.New("上個持有者未正常釋放 PATH 寫入鎖，這次未修改；請重試")
)

// userMutexName uses the actual token SID, not an environment-controlled user
// name. Global covers the same user in different Windows logon sessions too.
func userMutexName() (string, error) {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return "", fmt.Errorf("讀取使用者權杖：%w", err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("讀取使用者 SID：%w", err)
	}
	sid, err := user.User.Sid.String()
	if err != nil {
		return "", fmt.Errorf("讀取使用者 SID：%w", err)
	}
	return `Global\Hoshivel.HoshiBuild.UserPath-` + sid, nil
}

func createNamedMutex(name string) (syscall.Handle, error) {
	wide, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	// NULL attributes use the caller token's default DACL and do not make the
	// handle inheritable. Existing inaccessible or wrong-type objects fail closed.
	handle, _, callErr := createMutexProc.Call(0, 0, uintptr(unsafe.Pointer(wide)))
	if handle == 0 {
		return 0, mutexCallError("CreateMutexW", callErr)
	}
	return syscall.Handle(handle), nil
}

// acquireNamedMutex pins the owning goroutine until release because Win32 mutex
// ownership belongs to an OS thread. The caller must defer release on that same
// goroutine. No handle is closed while WaitForSingleObject is pending.
func acquireNamedMutex(name string, timeoutMillis uint32) (func() error, error) {
	runtime.LockOSThread()
	handle, err := createNamedMutex(name)
	if err != nil {
		runtime.UnlockOSThread()
		return nil, err
	}
	finish := func(owned bool) error {
		defer runtime.UnlockOSThread()
		var releaseErr error
		if owned {
			ok, _, callErr := releaseMutexProc.Call(uintptr(handle))
			if ok == 0 {
				releaseErr = mutexCallError("ReleaseMutex", callErr)
			}
		}
		return errors.Join(releaseErr, syscall.CloseHandle(handle))
	}
	status, waitErr := syscall.WaitForSingleObject(handle, timeoutMillis)
	switch status {
	case syscall.WAIT_OBJECT_0:
		released := false
		return func() error {
			if released {
				return nil
			}
			released = true
			return finish(true)
		}, nil
	case syscall.WAIT_ABANDONED:
		// WAIT_ABANDONED grants ownership. Release it before rejecting this
		// operation; silently continuing could conceal an interrupted edit.
		return nil, errors.Join(errMutexAbandoned, finish(true))
	case syscall.WAIT_TIMEOUT:
		return nil, errors.Join(errMutexTimeout, finish(false))
	default:
		if waitErr == nil {
			waitErr = fmt.Errorf("WaitForSingleObject 回傳非預期狀態 %#x", status)
		}
		return nil, errors.Join(waitErr, finish(false))
	}
}

func mutexCallError(operation string, err error) error {
	if err == nil || errors.Is(err, syscall.Errno(0)) {
		return fmt.Errorf("%s 未成功", operation)
	}
	return fmt.Errorf("%s：%w", operation, err)
}
