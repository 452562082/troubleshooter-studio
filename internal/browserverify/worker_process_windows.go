//go:build windows

package browserverify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProcessStage string

const (
	windowsProcessStageBeforeStart     windowsProcessStage = "before_start"
	windowsProcessStageWrapperStarted  windowsProcessStage = "wrapper_started"
	windowsProcessStageWrapperAssigned windowsProcessStage = "wrapper_assigned"
	windowsProcessStageTargetReleased  windowsProcessStage = "target_released"
)

type workerProcessController struct {
	ctx             context.Context
	mu              sync.Mutex
	job             windows.Handle
	gateRead        *os.File
	gateWrite       *os.File
	closed          bool
	stageHook       func(windowsProcessStage)
	cancelRequested atomic.Bool
	contextErr      error
	beforeCleanup   func() error
}

const windowsJobWrapperArgument = "--tshoot-browser-job-wrapper"

func init() {
	if len(os.Args) >= 4 && os.Args[1] == windowsJobWrapperArgument {
		gateHandle, err := strconv.ParseUint(os.Args[2], 10, 64)
		if err != nil {
			os.Exit(1)
		}
		os.Exit(runWindowsJobWrappedCommand(uintptr(gateHandle), os.Args[3], os.Args[4:]))
	}
}

func configureWorkerProcess(ctx context.Context, command *exec.Cmd, _ ...string) (*workerProcessController, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	job, err := createKillOnCloseJob()
	if err != nil {
		return nil, err
	}
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	if err := windows.SetHandleInformation(windows.Handle(gateRead.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		_ = gateRead.Close()
		_ = gateWrite.Close()
		_ = windows.CloseHandle(job)
		return nil, err
	}
	controller := &workerProcessController{ctx: ctx, job: job, gateRead: gateRead, gateWrite: gateWrite}
	originalPath := command.Path
	originalArgs := append([]string(nil), command.Args[1:]...)
	command.Path = executable
	command.Args = append([]string{
		executable,
		windowsJobWrapperArgument,
		strconv.FormatUint(uint64(gateRead.Fd()), 10),
		originalPath,
	}, originalArgs...)
	command.SysProcAttr = &syscall.SysProcAttr{
		AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(gateRead.Fd())},
	}
	command.Cancel = func() error { return controller.cancel(command) }
	return controller, nil
}

func (controller *workerProcessController) afterStart(command *exec.Cmd) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.closed || controller.cancelRequested.Load() {
		controller.closeContainmentLocked()
		return errors.New("browser process wrapper canceled before Job assignment")
	}
	if controller.gateRead != nil {
		_ = controller.gateRead.Close()
		controller.gateRead = nil
	}
	if controller.reachedLocked(windowsProcessStageWrapperStarted) {
		controller.closeContainmentLocked()
		return errors.New("browser process wrapper canceled before Job assignment")
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if err != nil {
		controller.closeContainmentLocked()
		return err
	}
	assignErr := windows.AssignProcessToJobObject(controller.job, process)
	closeErr := windows.CloseHandle(process)
	if err := errors.Join(assignErr, closeErr); err != nil {
		controller.closeContainmentLocked()
		return err
	}
	if controller.reachedLocked(windowsProcessStageWrapperAssigned) {
		controller.closeContainmentLocked()
		return errors.New("browser process wrapper canceled before target release")
	}
	if controller.gateWrite == nil {
		controller.closeContainmentLocked()
		return errors.New("browser process wrapper canceled before target release")
	}
	_, writeErr := controller.gateWrite.Write([]byte{1})
	closeGateErr := controller.gateWrite.Close()
	controller.gateWrite = nil
	if err := errors.Join(writeErr, closeGateErr); err != nil {
		controller.closeContainmentLocked()
		return err
	}
	if controller.reachedLocked(windowsProcessStageTargetReleased) {
		controller.closeContainmentLocked()
		return errors.New("browser process target canceled after release")
	}
	return nil
}

func (controller *workerProcessController) reachedLocked(stage windowsProcessStage) bool {
	if controller.stageHook != nil {
		controller.stageHook(stage)
	}
	return controller.closed || controller.cancelRequested.Load()
}

func (controller *workerProcessController) cancel(command *exec.Cmd) error {
	controller.cancelRequested.Store(true)
	controller.mu.Lock()
	if controller.ctx != nil && controller.ctx.Err() != nil {
		controller.contextErr = controller.ctx.Err()
	}
	controller.closeContainmentLocked()
	controller.mu.Unlock()
	return controller.killWrapper(command)
}

func (controller *workerProcessController) kill(command *exec.Cmd) error {
	controller.cancelRequested.Store(true)
	controller.closeContainment()
	return controller.killWrapper(command)
}

func (*workerProcessController) killWrapper(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func (controller *workerProcessController) wait(command *exec.Cmd) error {
	waitErr := command.Wait()
	var beforeCleanupErr error
	if controller.beforeCleanup != nil {
		beforeCleanupErr = controller.beforeCleanup()
	}
	controller.mu.Lock()
	contextErr := controller.contextErr
	controller.mu.Unlock()
	return errors.Join(waitErr, beforeCleanupErr, contextErr)
}

func (controller *workerProcessController) finish() error {
	controller.closeContainment()
	return nil
}

func (controller *workerProcessController) closeContainment() {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.closeContainmentLocked()
}

func (controller *workerProcessController) closeContainmentLocked() {
	if controller.closed {
		return
	}
	controller.closed = true
	if controller.gateWrite != nil {
		_ = controller.gateWrite.Close()
		controller.gateWrite = nil
	}
	if controller.gateRead != nil {
		_ = controller.gateRead.Close()
		controller.gateRead = nil
	}
	if controller.job != 0 {
		_ = windows.CloseHandle(controller.job)
		controller.job = 0
	}
}

func runWindowsJobWrappedCommand(gateHandle uintptr, executable string, args []string) int {
	gate := os.NewFile(gateHandle, "tshoot-browser-job-gate")
	if gate == nil {
		return 1
	}
	var release [1]byte
	_, readErr := io.ReadFull(gate, release[:])
	closeErr := gate.Close()
	if readErr != nil || closeErr != nil || release[0] != 1 {
		return 1
	}
	command := exec.Command(executable, args...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
			return exitErr.ExitCode()
		}
		_, _ = fmt.Fprintf(os.Stderr, "contained browser process failed: %v\n", err)
		return 1
	}
	return 0
}

func createKillOnCloseJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	information := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	information.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&information)),
		uint32(unsafe.Sizeof(information)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}
