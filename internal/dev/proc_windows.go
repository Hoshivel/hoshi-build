//go:build windows

package dev

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
	"unsafe"
)

func environ() []string { return os.Environ() }

// processHelperMarker is deliberately private: the helper is an implementation
// detail used to put a command tree in a Job Object before that command gets a
// chance to spawn npm/node/go-run grandchildren.
const processHelperMarker = "__hoshi_dev_process_helper_v1__"

// processCommand starts this executable as a tiny supervisor. The supervisor
// joins a KILL_ON_JOB_CLOSE Job Object before it starts the configured command,
// eliminating the spawn-before-AssignProcessToJobObject race.
func processCommand(ctx context.Context, name string, args ...string) (*exec.Cmd, func() error, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, fmt.Errorf("找不到 hoshi 執行檔：%w", err)
	}
	parentRead, parentWrite, err := os.Pipe()
	if err != nil {
		return nil, nil, fmt.Errorf("建立 hoshi parent lifetime pipe：%w", err)
	}
	finish := func() error {
		return errors.Join(parentRead.Close(), parentWrite.Close())
	}

	helperArgs := make([]string, 0, len(args)+3)
	helperArgs = append(helperArgs, processHelperMarker,
		strconv.FormatUint(uint64(parentRead.Fd()), 16), name)
	helperArgs = append(helperArgs, args...)
	cmd := exec.CommandContext(ctx, exe, helperArgs...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags:              syscall.CREATE_NEW_PROCESS_GROUP,
		AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(parentRead.Fd())},
	}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		return generateCtrlBreak(cmd.Process.Pid)
	}
	cmd.WaitDelay = processStopGrace
	return cmd, finish, nil
}

// MaybeRunProcessHelper recognizes the private supervisor invocation. It is
// called before ordinary command dispatch by cmd/hoshi and by TestMain.
func MaybeRunProcessHelper(args []string) (bool, int) {
	if len(args) == 0 || args[0] != processHelperMarker {
		return false, 0
	}
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "hoshi：內部 dev supervisor 少了要執行的命令")
		return true, 2
	}
	parentLifetime, err := strconv.ParseUint(args[1], 16, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "hoshi：內部 dev supervisor 的 parent handle 無效：%v\n", err)
		return true, 2
	}
	return true, runProcessHelper(syscall.Handle(parentLifetime), args[2:])
}

func runProcessHelper(parentLifetime syscall.Handle, command []string) int {
	job, err := newKillOnCloseJob()
	if err != nil {
		fmt.Fprintf(os.Stderr, "hoshi：建立 dev process job 失敗：%v\n", err)
		return 1
	}
	if err := assignCurrentProcess(job); err != nil {
		_ = closeHandle(job) // not in the job yet, so closing is safe
		fmt.Fprintf(os.Stderr, "hoshi：加入 dev process job 失敗：%v\n", err)
		return 1
	}
	// Keep this handle open for the helper's whole lifetime. The caller exits
	// immediately after this function returns, and Windows closing the handle
	// at process exit is what applies KILL_ON_JOB_CLOSE to any descendants that
	// are still running.
	monitorParentLifetime(parentLifetime)

	// CTRL_BREAK reaches the whole new process group. Registering Interrupt here
	// keeps the supervisor alive while the real command and its descendants use
	// the same event for graceful shutdown. If they do not leave in time, the
	// outer Cmd.WaitDelay kills this helper; Windows then closes its last Job
	// handle and force-terminates every remaining descendant.
	interrupts := make(chan os.Signal, 4)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)

	cmd := exec.Command(command[0], command[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "hoshi：dev 命令啟動失敗：%v\n", err)
		return 1
	}
	err = cmd.Wait()

	// A shim can exit before the server it launched. Keep the Job handle alive
	// briefly so a gracefully stopping grandchild can finish; the outer helper
	// deadline remains the authoritative bound during cancellation.
	waitForJobChildren(job, processStopGrace)

	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
		return exitErr.ExitCode()
	}
	fmt.Fprintf(os.Stderr, "hoshi：dev 命令等待失敗：%v\n", err)
	return 1
}

func monitorParentLifetime(handle syscall.Handle) {
	parent := os.NewFile(uintptr(handle), "hoshi-parent-lifetime")
	if parent == nil {
		os.Exit(1)
	}
	go func() {
		var oneByte [1]byte
		_, _ = parent.Read(oneByte[:])
		// The outer hoshi never writes to this pipe. EOF (or any read error)
		// means it exited, so leave immediately; process exit closes the Job
		// handle and Windows removes the whole managed command tree.
		os.Exit(1)
	}()
}

const (
	jobObjectBasicAccountingInformation        = 1
	jobObjectExtendedLimitInformation          = 9
	jobObjectLimitKillOnJobClose        uint32 = 0x00002000
)

type jobObjectBasicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type jobObjectExtendedLimitInfo struct {
	BasicLimitInformation jobObjectBasicLimitInformation
	IOInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

type jobObjectBasicAccountingInfo struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

var (
	kernel32                    = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW        = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJob      = kernel32.NewProc("AssignProcessToJobObject")
	procQueryInformationJob     = kernel32.NewProc("QueryInformationJobObject")
	procGetCurrentProcess       = kernel32.NewProc("GetCurrentProcess")
	procGenerateConsoleCtrl     = kernel32.NewProc("GenerateConsoleCtrlEvent")
	procCloseHandle             = kernel32.NewProc("CloseHandle")
)

func newKillOnCloseJob() (syscall.Handle, error) {
	r1, _, callErr := procCreateJobObjectW.Call(0, 0)
	if r1 == 0 {
		return 0, win32CallError("CreateJobObjectW", callErr)
	}
	job := syscall.Handle(r1)
	info := jobObjectExtendedLimitInfo{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	r1, _, callErr = procSetInformationJobObject.Call(
		uintptr(job),
		jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if r1 == 0 {
		_ = closeHandle(job)
		return 0, win32CallError("SetInformationJobObject", callErr)
	}
	return job, nil
}

func assignCurrentProcess(job syscall.Handle) error {
	current, _, _ := procGetCurrentProcess.Call()
	r1, _, callErr := procAssignProcessToJob.Call(uintptr(job), current)
	if r1 == 0 {
		return win32CallError("AssignProcessToJobObject", callErr)
	}
	return nil
}

func activeJobProcesses(job syscall.Handle) (uint32, error) {
	info := jobObjectBasicAccountingInfo{}
	r1, _, callErr := procQueryInformationJob.Call(
		uintptr(job),
		jobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
		0,
	)
	if r1 == 0 {
		return 0, win32CallError("QueryInformationJobObject", callErr)
	}
	return info.ActiveProcesses, nil
}

func waitForJobChildren(job syscall.Handle, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		active, err := activeJobProcesses(job)
		if err != nil || active <= 1 || time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func generateCtrlBreak(pid int) error {
	r1, _, callErr := procGenerateConsoleCtrl.Call(
		uintptr(syscall.CTRL_BREAK_EVENT), uintptr(uint32(pid)))
	if r1 == 0 {
		return win32CallError("GenerateConsoleCtrlEvent", callErr)
	}
	return nil
}

func closeHandle(handle syscall.Handle) error {
	r1, _, callErr := procCloseHandle.Call(uintptr(handle))
	if r1 == 0 {
		return win32CallError("CloseHandle", callErr)
	}
	return nil
}

func win32CallError(name string, err error) error {
	if err == nil || errors.Is(err, syscall.Errno(0)) {
		return fmt.Errorf("%s failed", name)
	}
	return fmt.Errorf("%s: %w", name, err)
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
