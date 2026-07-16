//go:build windows

package browserverify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
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

const (
	windowsJobWrapperArgument       = "--tshoot-browser-job-wrapper"
	windowsJobTargetWrapperArgument = "--tshoot-browser-job-target-wrapper"
	windowsWrapperReleaseCommand    = byte('R')
	windowsWrapperCleanupCommand    = byte('C')
	windowsTargetTerminationTimeout = 5 * time.Second
)

type windowsTargetStatus struct {
	Error string `json:"error,omitempty"`
}

type workerProcessController struct {
	ctx             context.Context
	mu              sync.Mutex
	statusRead      *os.File
	statusWrite     *os.File
	controlRead     *os.File
	controlWrite    *os.File
	cleanupIdentity *os.File
	closed          bool
	stageHook       func(windowsProcessStage)
	cancelRequested atomic.Bool
	contextErr      error
	beforeCleanup   func() error
}

func init() {
	if len(os.Args) >= 7 && os.Args[1] == windowsJobWrapperArgument {
		statusHandle, statusErr := strconv.ParseUint(os.Args[2], 10, 64)
		controlHandle, controlErr := strconv.ParseUint(os.Args[4], 10, 64)
		cleanupHandle, cleanupErr := strconv.ParseUint(os.Args[5], 10, 64)
		if statusErr != nil || controlErr != nil || cleanupErr != nil {
			os.Exit(1)
		}
		os.Exit(runWindowsJobWrappedCommand(
			uintptr(statusHandle),
			uintptr(controlHandle),
			uintptr(cleanupHandle),
			os.Args[6],
			os.Args[3],
			os.Args[7:],
		))
	}
	if len(os.Args) >= 4 && os.Args[1] == windowsJobTargetWrapperArgument {
		gateHandle, err := strconv.ParseUint(os.Args[2], 10, 64)
		if err != nil {
			os.Exit(1)
		}
		os.Exit(runWindowsJobTargetCommand(uintptr(gateHandle), os.Args[3], os.Args[4:]))
	}
}

func configureWorkerProcess(ctx context.Context, command *exec.Cmd, cleanupPaths ...string) (*workerProcessController, error) {
	if len(cleanupPaths) > 1 {
		return nil, errors.New("browser process wrapper accepts at most one cleanup path")
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	statusRead, statusWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	controlRead, controlWrite, err := os.Pipe()
	if err != nil {
		_ = statusRead.Close()
		_ = statusWrite.Close()
		return nil, err
	}
	controller := &workerProcessController{
		ctx:          ctx,
		statusRead:   statusRead,
		statusWrite:  statusWrite,
		controlRead:  controlRead,
		controlWrite: controlWrite,
	}
	cleanupHandle := "0"
	cleanupPath := ""
	if len(cleanupPaths) == 1 && cleanupPaths[0] != "" {
		cleanupIdentity, err := openWindowsPlaintextCleanupIdentity(cleanupPaths[0])
		if err != nil {
			_ = controller.finish()
			return nil, err
		}
		controller.cleanupIdentity = cleanupIdentity
		cleanupHandle = strconv.FormatUint(uint64(cleanupIdentity.Fd()), 10)
		cleanupPath = cleanupPaths[0]
	}
	for _, file := range []*os.File{statusWrite, controlRead, controller.cleanupIdentity} {
		if file == nil {
			continue
		}
		if err := windows.SetHandleInformation(windows.Handle(file.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			_ = controller.finish()
			return nil, err
		}
	}
	originalPath := command.Path
	originalArgs := append([]string(nil), command.Args[1:]...)
	command.Path = executable
	command.Args = append([]string{
		executable,
		windowsJobWrapperArgument,
		strconv.FormatUint(uint64(statusWrite.Fd()), 10),
		originalPath,
		strconv.FormatUint(uint64(controlRead.Fd()), 10),
		cleanupHandle,
		cleanupPath,
	}, originalArgs...)
	inheritedHandles := []syscall.Handle{syscall.Handle(statusWrite.Fd()), syscall.Handle(controlRead.Fd())}
	if controller.cleanupIdentity != nil {
		inheritedHandles = append(inheritedHandles, syscall.Handle(controller.cleanupIdentity.Fd()))
	}
	command.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: inheritedHandles}
	command.Cancel = func() error { return controller.cancel(command) }
	return controller, nil
}

func (controller *workerProcessController) afterStart(*exec.Cmd) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.statusWrite != nil {
		_ = controller.statusWrite.Close()
		controller.statusWrite = nil
	}
	if controller.controlRead != nil {
		_ = controller.controlRead.Close()
		controller.controlRead = nil
	}
	if controller.cleanupIdentity != nil {
		_ = controller.cleanupIdentity.Close()
		controller.cleanupIdentity = nil
	}
	if controller.closed || controller.reachedLocked(windowsProcessStageWrapperStarted) {
		_ = controller.signalCleanupLocked()
		return errors.New("browser process wrapper canceled before Job assignment")
	}
	if controller.reachedLocked(windowsProcessStageWrapperAssigned) {
		_ = controller.signalCleanupLocked()
		return errors.New("browser process wrapper canceled before target release")
	}
	if controller.controlWrite == nil {
		return errors.New("browser process wrapper canceled before target release")
	}
	if _, err := controller.controlWrite.Write([]byte{windowsWrapperReleaseCommand}); err != nil {
		_ = controller.signalCleanupLocked()
		return err
	}
	if controller.reachedLocked(windowsProcessStageTargetReleased) {
		_ = controller.signalCleanupLocked()
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

func (controller *workerProcessController) cancel(*exec.Cmd) error {
	controller.cancelRequested.Store(true)
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.ctx != nil && controller.ctx.Err() != nil {
		controller.contextErr = controller.ctx.Err()
	}
	return controller.signalCleanupLocked()
}

func (controller *workerProcessController) kill(*exec.Cmd) error {
	controller.cancelRequested.Store(true)
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.signalCleanupLocked()
}

func (controller *workerProcessController) signalCleanupLocked() error {
	if controller.controlWrite == nil {
		return nil
	}
	_, writeErr := controller.controlWrite.Write([]byte{windowsWrapperCleanupCommand})
	closeErr := controller.controlWrite.Close()
	controller.controlWrite = nil
	return errors.Join(writeErr, closeErr)
}

func (controller *workerProcessController) wait(command *exec.Cmd) error {
	status, statusErr := controller.readTargetStatus()
	var beforeCleanupErr error
	if controller.beforeCleanup != nil {
		beforeCleanupErr = controller.beforeCleanup()
	}
	controller.mu.Lock()
	controlErr := controller.signalCleanupLocked()
	contextErr := controller.contextErr
	controller.mu.Unlock()
	wrapperErr := command.Wait()
	if statusErr != nil {
		return errors.Join(statusErr, beforeCleanupErr, controlErr, wrapperErr, contextErr)
	}
	if status.Error != "" {
		return errors.Join(errors.New(status.Error), beforeCleanupErr, controlErr, wrapperErr, contextErr)
	}
	return errors.Join(beforeCleanupErr, controlErr, wrapperErr, contextErr)
}

func (controller *workerProcessController) readTargetStatus() (windowsTargetStatus, error) {
	controller.mu.Lock()
	reader := controller.statusRead
	controller.statusRead = nil
	controller.mu.Unlock()
	if reader == nil {
		return windowsTargetStatus{}, errors.New("browser process wrapper status pipe is closed")
	}
	defer reader.Close()
	var status windowsTargetStatus
	decoder := json.NewDecoder(io.LimitReader(reader, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&status); err != nil {
		return windowsTargetStatus{}, errors.New("browser process wrapper status is invalid")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return windowsTargetStatus{}, errors.New("browser process wrapper status is invalid")
	}
	return status, nil
}

func (controller *workerProcessController) finish() error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.closed {
		return nil
	}
	controller.closed = true
	return errors.Join(
		controller.signalCleanupLocked(),
		closeWindowsProcessFile(&controller.statusRead),
		closeWindowsProcessFile(&controller.statusWrite),
		closeWindowsProcessFile(&controller.controlRead),
		closeWindowsProcessFile(&controller.cleanupIdentity),
	)
}

func closeWindowsProcessFile(file **os.File) error {
	if file == nil || *file == nil {
		return nil
	}
	err := (*file).Close()
	*file = nil
	return err
}

func runWindowsJobWrappedCommand(statusHandle, controlHandle, cleanupHandle uintptr, cleanupPath, executable string, args []string) int {
	statusWriter := os.NewFile(statusHandle, "tshoot-browser-target-status")
	controlReader := os.NewFile(controlHandle, "tshoot-browser-wrapper-control")
	if statusWriter == nil || controlReader == nil {
		return 1
	}
	_ = windows.SetHandleInformation(windows.Handle(statusHandle), windows.HANDLE_FLAG_INHERIT, 0)
	_ = windows.SetHandleInformation(windows.Handle(controlHandle), windows.HANDLE_FLAG_INHERIT, 0)
	var cleanupIdentity *os.File
	if cleanupHandle != 0 {
		cleanupIdentity = os.NewFile(cleanupHandle, "tshoot-browser-plaintext-cleanup-directory")
		if cleanupIdentity == nil || validateWindowsPlaintextCleanupIdentity(cleanupIdentity, cleanupPath) != nil {
			_ = statusWriter.Close()
			_ = controlReader.Close()
			if cleanupIdentity != nil {
				_ = cleanupIdentity.Close()
			}
			return 1
		}
		_ = windows.SetHandleInformation(windows.Handle(cleanupHandle), windows.HANDLE_FLAG_INHERIT, 0)
	}
	job, err := createKillOnCloseJob()
	if err != nil {
		_ = statusWriter.Close()
		_ = controlReader.Close()
		_ = cleanupWindowsPlaintextSession(cleanupIdentity, cleanupPath)
		return 1
	}
	target, gateRead, gateWrite, err := startWindowsGatedTarget(executable, args)
	if err != nil {
		_ = windows.CloseHandle(job)
		_ = statusWriter.Close()
		_ = controlReader.Close()
		_ = cleanupWindowsPlaintextSession(cleanupIdentity, cleanupPath)
		return 1
	}
	_ = gateRead.Close()
	targetDone := make(chan error, 1)
	go func() { targetDone <- target.Wait() }()
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(target.Process.Pid))
	if err == nil {
		err = windows.AssignProcessToJobObject(job, process)
		closeErr := windows.CloseHandle(process)
		err = errors.Join(err, closeErr)
	}
	if err != nil {
		_ = gateWrite.Close()
		_ = terminateWindowsTargetJob(job, targetDone)
		_ = statusWriter.Close()
		_ = controlReader.Close()
		_ = cleanupWindowsPlaintextSession(cleanupIdentity, cleanupPath)
		return 1
	}
	var release [1]byte
	_, releaseErr := io.ReadFull(controlReader, release[:])
	if releaseErr != nil || release[0] != windowsWrapperReleaseCommand {
		_ = gateWrite.Close()
		_ = terminateWindowsTargetJob(job, targetDone)
		_ = statusWriter.Close()
		_ = controlReader.Close()
		if cleanupWindowsPlaintextSession(cleanupIdentity, cleanupPath) != nil {
			return 1
		}
		return 0
	}
	_, gateWriteErr := gateWrite.Write([]byte{1})
	gateCloseErr := gateWrite.Close()
	if errors.Join(gateWriteErr, gateCloseErr) != nil {
		_ = terminateWindowsTargetJob(job, targetDone)
		_ = statusWriter.Close()
		_ = controlReader.Close()
		if cleanupWindowsPlaintextSession(cleanupIdentity, cleanupPath) != nil {
			return 1
		}
		return 1
	}
	controlDone := make(chan bool, 1)
	go func() {
		command, err := io.ReadAll(io.LimitReader(controlReader, 2))
		_ = controlReader.Close()
		controlDone <- err == nil && len(command) == 1 && command[0] == windowsWrapperCleanupCommand
	}()
	select {
	case targetErr := <-targetDone:
		if err := windows.CloseHandle(job); err != nil {
			_ = statusWriter.Close()
			_ = cleanupWindowsPlaintextSession(cleanupIdentity, cleanupPath)
			return 1
		}
		status := windowsTargetStatus{}
		if targetErr != nil {
			status.Error = targetErr.Error()
		}
		if err := writeWindowsTargetStatus(statusWriter, status); err != nil {
			_ = cleanupWindowsPlaintextSession(cleanupIdentity, cleanupPath)
			return 1
		}
		<-controlDone
	case <-controlDone:
		_ = statusWriter.Close()
		if err := terminateWindowsTargetJob(job, targetDone); err != nil {
			_ = cleanupWindowsPlaintextSession(cleanupIdentity, cleanupPath)
			return 1
		}
	}
	if cleanupWindowsPlaintextSession(cleanupIdentity, cleanupPath) != nil {
		return 1
	}
	return 0
}

func startWindowsGatedTarget(executable string, args []string) (*exec.Cmd, *os.File, *os.File, error) {
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		return nil, nil, nil, err
	}
	if err := windows.SetHandleInformation(windows.Handle(gateRead.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		_ = gateRead.Close()
		_ = gateWrite.Close()
		return nil, nil, nil, err
	}
	currentExecutable, err := os.Executable()
	if err != nil {
		_ = gateRead.Close()
		_ = gateWrite.Close()
		return nil, nil, nil, err
	}
	target := exec.Command(currentExecutable, append([]string{
		windowsJobTargetWrapperArgument,
		strconv.FormatUint(uint64(gateRead.Fd()), 10),
		executable,
	}, args...)...)
	target.Stdin = os.Stdin
	target.Stdout = os.Stdout
	target.Stderr = os.Stderr
	target.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(gateRead.Fd())}}
	if err := target.Start(); err != nil {
		_ = gateRead.Close()
		_ = gateWrite.Close()
		return nil, nil, nil, err
	}
	return target, gateRead, gateWrite, nil
}

func runWindowsJobTargetCommand(gateHandle uintptr, executable string, args []string) int {
	gate := os.NewFile(gateHandle, "tshoot-browser-job-gate")
	if gate == nil {
		return 1
	}
	_ = windows.SetHandleInformation(windows.Handle(gateHandle), windows.HANDLE_FLAG_INHERIT, 0)
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

func terminateWindowsTargetJob(job windows.Handle, targetDone <-chan error) error {
	closeErr := windows.CloseHandle(job)
	timer := time.NewTimer(windowsTargetTerminationTimeout)
	defer timer.Stop()
	select {
	case <-targetDone:
		return closeErr
	case <-timer.C:
		return errors.Join(closeErr, errors.New("browser target did not exit after Job termination"))
	}
}

func writeWindowsTargetStatus(statusWriter *os.File, status windowsTargetStatus) error {
	return errors.Join(json.NewEncoder(statusWriter).Encode(status), statusWriter.Close())
}

func openWindowsPlaintextCleanupIdentity(path string) (*os.File, error) {
	if err := validateWindowsPlaintextCleanupPath(path); err != nil {
		return nil, err
	}
	directory := filepath.Dir(path)
	encodedDirectory, err := windows.UTF16PtrFromString(directory)
	if err != nil {
		return nil, errors.New("encode browser plaintext cleanup directory identity")
	}
	handle, err := windows.CreateFile(
		encodedDirectory,
		windows.FILE_LIST_DIRECTORY|windows.FILE_READ_ATTRIBUTES|windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, errors.New("open browser plaintext cleanup directory identity")
	}
	identity := os.NewFile(uintptr(handle), directory)
	if identity == nil {
		_ = windows.CloseHandle(handle)
		return nil, errors.New("create browser plaintext cleanup directory identity")
	}
	if err := validateWindowsPlaintextCleanupIdentity(identity, path); err != nil {
		_ = identity.Close()
		return nil, err
	}
	return identity, nil
}

func validateWindowsPlaintextCleanupPath(path string) error {
	directory, ok := plaintextSessionWorkspace(path)
	if !ok || filepath.Clean(filepath.Dir(directory)) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(directory), plaintextSessionDirectoryPrefix) {
		return errors.New("browser plaintext cleanup path is not a managed temporary file")
	}
	return nil
}

func validateWindowsPlaintextCleanupIdentity(identity *os.File, path string) error {
	if identity == nil {
		return errors.New("browser plaintext cleanup identity is missing")
	}
	if err := validateWindowsPlaintextCleanupPath(path); err != nil {
		return err
	}
	directoryInfo, err := os.Lstat(filepath.Dir(path))
	reparsePoint, reparseErr := windowsPathIsReparsePoint(filepath.Dir(path))
	if err != nil || reparseErr != nil || reparsePoint || directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() {
		return errors.New("browser plaintext cleanup directory is unsafe")
	}
	identityInfo, err := identity.Stat()
	if err != nil || !identityInfo.IsDir() || !os.SameFile(directoryInfo, identityInfo) {
		return errors.New("browser plaintext cleanup identity changed")
	}
	return nil
}

func cleanupWindowsPlaintextSession(identity *os.File, path string) error {
	if identity == nil {
		return nil
	}
	if err := validateWindowsPlaintextCleanupIdentity(identity, path); err != nil {
		_ = identity.Close()
		return err
	}
	return cleanupWindowsPlaintextSessionAfterValidation(identity, path)
}

func cleanupWindowsPlaintextSessionAfterValidation(identity *os.File, path string) error {
	entries, err := identity.ReadDir(-1)
	if err != nil {
		_ = identity.Close()
		return errors.New("list browser plaintext cleanup directory")
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != plaintextSessionFileName && !strings.HasPrefix(name, "."+plaintextSessionFileName+"-") {
			_ = identity.Close()
			return errors.New("browser plaintext cleanup directory contains an unmanaged entry")
		}
		if err := removeWindowsPlaintextCleanupEntry(identity, name); err != nil {
			_ = identity.Close()
			return err
		}
	}
	if err := validateWindowsPlaintextCleanupIdentity(identity, path); err != nil {
		_ = identity.Close()
		return err
	}
	if err := markWindowsHandleForDeletion(windows.Handle(identity.Fd())); err != nil {
		_ = identity.Close()
		return errors.New("remove browser plaintext cleanup directory")
	}
	if err := identity.Close(); err != nil {
		return errors.New("close browser plaintext cleanup directory identity")
	}
	return nil
}

func removeWindowsPlaintextCleanupEntry(directory *os.File, name string) error {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return errors.New("encode browser plaintext cleanup entry")
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: windows.Handle(directory.Fd()),
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE,
	}
	var (
		handle         windows.Handle
		ioStatus       windows.IO_STATUS_BLOCK
		allocationSize int64
	)
	if err := windows.NtCreateFile(
		&handle,
		windows.FILE_READ_ATTRIBUTES|windows.DELETE|windows.SYNCHRONIZE,
		attributes,
		&ioStatus,
		&allocationSize,
		0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	); err != nil {
		return errors.New("open browser plaintext cleanup entry relative to directory identity")
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		_ = windows.CloseHandle(handle)
		return errors.New("inspect browser plaintext cleanup entry")
	}
	size := int64(information.FileSizeHigh)<<32 | int64(information.FileSizeLow)
	if information.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 || size < 0 || size > maxBrowserSessionBytes {
		_ = windows.CloseHandle(handle)
		return errors.New("browser plaintext cleanup entry is unsafe")
	}
	deleteErr := markWindowsHandleForDeletion(handle)
	closeErr := windows.CloseHandle(handle)
	if deleteErr != nil || closeErr != nil {
		return errors.New("remove browser plaintext cleanup entry")
	}
	return nil
}

func markWindowsHandleForDeletion(handle windows.Handle) error {
	deleteFile := byte(1)
	return windows.SetFileInformationByHandle(handle, windows.FileDispositionInfo, &deleteFile, 1)
}

func windowsPathIsReparsePoint(path string) (bool, error) {
	encoded, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attributes, err := windows.GetFileAttributes(encoded)
	if err != nil {
		return false, err
	}
	return attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
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
