//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package browserverify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	processGroupTerminationGrace = 2 * time.Second
	processGroupPollInterval     = 20 * time.Millisecond
	unixProcessWrapperArgument   = "--tshoot-browser-unix-wrapper"
	unixWrapperCleanupCommand    = byte('C')
)

type unixTargetStatus struct {
	Error string `json:"error,omitempty"`
}

type workerProcessController struct {
	ctx                context.Context
	command            *exec.Cmd
	statusRead         *os.File
	statusWrite        *os.File
	controlRead        *os.File
	controlWrite       *os.File
	cleanupIdentity    *os.File
	signalProcessGroup func(int, syscall.Signal) error
	sleep              func(time.Duration)
	grace              time.Duration
	beforeGroupCleanup func()
	beforeCleanup      func() error
	waitWrapper        func(*exec.Cmd) error
	terminationMu      sync.Mutex
	reaping            bool
	closed             bool
	contextErr         error
	parentEndsOnce     sync.Once
	parentEndsErr      error
}

func init() {
	if len(os.Args) >= 7 && os.Args[1] == unixProcessWrapperArgument {
		statusFD, statusErr := strconv.ParseUint(os.Args[2], 10, 64)
		controlFD, controlErr := strconv.ParseUint(os.Args[3], 10, 64)
		cleanupFD, cleanupErr := strconv.ParseUint(os.Args[4], 10, 64)
		if statusErr != nil || controlErr != nil || cleanupErr != nil {
			os.Exit(1)
		}
		os.Exit(runUnixProcessWrapper(uintptr(statusFD), uintptr(controlFD), uintptr(cleanupFD), os.Args[5], os.Args[6], os.Args[7:]))
	}
}

func configureWorkerProcess(ctx context.Context, command *exec.Cmd, cleanupPaths ...string) (*workerProcessController, error) {
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
		command:      command,
		statusRead:   statusRead,
		statusWrite:  statusWrite,
		controlRead:  controlRead,
		controlWrite: controlWrite,
		signalProcessGroup: func(processGroup int, signal syscall.Signal) error {
			return syscall.Kill(-processGroup, signal)
		},
		sleep: time.Sleep,
		grace: processGroupTerminationGrace,
	}
	cleanupFD := "0"
	cleanupPath := ""
	if len(cleanupPaths) > 1 {
		_ = controller.finish()
		return nil, errors.New("browser process wrapper accepts at most one cleanup path")
	}
	if len(cleanupPaths) == 1 && cleanupPaths[0] != "" {
		cleanupIdentity, err := openUnixPlaintextCleanupIdentity(cleanupPaths[0])
		if err != nil {
			_ = controller.finish()
			return nil, err
		}
		controller.cleanupIdentity = cleanupIdentity
		cleanupFD = "5"
		cleanupPath = cleanupPaths[0]
	}
	originalPath := command.Path
	originalArgs := append([]string(nil), command.Args[1:]...)
	command.Path = executable
	command.Args = append([]string{
		executable,
		unixProcessWrapperArgument,
		"3",
		"4",
		cleanupFD,
		cleanupPath,
		originalPath,
	}, originalArgs...)
	command.ExtraFiles = []*os.File{statusWrite, controlRead}
	if controller.cleanupIdentity != nil {
		command.ExtraFiles = append(command.ExtraFiles, controller.cleanupIdentity)
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return controller.cancel(command) }
	return controller, nil
}

func (controller *workerProcessController) afterStart(*exec.Cmd) error {
	return controller.closeParentEnds()
}

func (controller *workerProcessController) wait(command *exec.Cmd) error {
	status, statusErr := controller.readTargetStatus()
	if controller.beforeGroupCleanup != nil {
		controller.beforeGroupCleanup()
	}
	var beforeCleanupErr error
	if controller.beforeCleanup != nil {
		beforeCleanupErr = controller.beforeCleanup()
	}
	controller.terminationMu.Lock()
	controller.reaping = true
	controlErr := controller.requestWrapperCleanup()
	controller.terminationMu.Unlock()
	wrapperErr := controller.waitForWrapper(command)
	controller.terminationMu.Lock()
	controller.closed = true
	contextErr := controller.contextErr
	controller.terminationMu.Unlock()
	if statusErr != nil {
		return errors.Join(statusErr, beforeCleanupErr, controlErr, wrapperErr, contextErr)
	}
	if status.Error != "" {
		return errors.Join(errors.New(status.Error), beforeCleanupErr, controlErr, contextErr)
	}
	return errors.Join(beforeCleanupErr, controlErr, contextErr)
}

func (controller *workerProcessController) cancel(command *exec.Cmd) error {
	controller.terminationMu.Lock()
	defer controller.terminationMu.Unlock()
	if controller.ctx != nil && controller.ctx.Err() != nil {
		controller.contextErr = controller.ctx.Err()
	}
	if controller.reaping || controller.closed {
		return os.ErrProcessDone
	}
	if command.Process == nil {
		return os.ErrProcessDone
	}
	return controller.terminateProcessGroupLocked(command.Process.Pid)
}

func (controller *workerProcessController) kill(command *exec.Cmd) error {
	controller.terminationMu.Lock()
	defer controller.terminationMu.Unlock()
	if controller.reaping || controller.closed {
		return os.ErrProcessDone
	}
	if command.Process == nil {
		return os.ErrProcessDone
	}
	err := controller.processGroupSignal(command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func (controller *workerProcessController) finish() error {
	return errors.Join(
		controller.closeParentEnds(),
		controller.closeFile(&controller.statusRead),
		controller.closeFile(&controller.statusWrite),
		controller.closeFile(&controller.controlRead),
		controller.closeFile(&controller.controlWrite),
		controller.closeFile(&controller.cleanupIdentity),
	)
}

func (controller *workerProcessController) closeParentEnds() error {
	controller.parentEndsOnce.Do(func() {
		controller.parentEndsErr = errors.Join(
			controller.closeFile(&controller.statusWrite),
			controller.closeFile(&controller.controlRead),
			controller.closeFile(&controller.cleanupIdentity),
		)
	})
	return controller.parentEndsErr
}

func (controller *workerProcessController) readTargetStatus() (unixTargetStatus, error) {
	if controller.statusRead == nil {
		return unixTargetStatus{}, errors.New("browser process wrapper status pipe is closed")
	}
	reader := controller.statusRead
	controller.statusRead = nil
	defer reader.Close()
	var status unixTargetStatus
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&status); err != nil {
		return unixTargetStatus{}, err
	}
	if err := requireUnixTargetStatusEOF(decoder); err != nil {
		return unixTargetStatus{}, err
	}
	return status, nil
}

func requireUnixTargetStatusEOF(decoder *json.Decoder) error {
	var extra json.RawMessage
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.New("browser process wrapper returned more than one status value")
}

func (controller *workerProcessController) waitForWrapper(command *exec.Cmd) error {
	if controller.waitWrapper != nil {
		return controller.waitWrapper(command)
	}
	return command.Wait()
}

func (controller *workerProcessController) requestWrapperCleanup() error {
	if controller.controlWrite == nil {
		return errors.New("browser process wrapper control pipe is closed")
	}
	_, writeErr := controller.controlWrite.Write([]byte{unixWrapperCleanupCommand})
	return errors.Join(writeErr, controller.closeFile(&controller.controlWrite))
}

func (*workerProcessController) closeFile(file **os.File) error {
	if *file == nil {
		return nil
	}
	err := (*file).Close()
	*file = nil
	return err
}

func (controller *workerProcessController) terminateProcessGroupLocked(processGroup int) error {
	if err := controller.processGroupSignal(processGroup, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	if err := controller.processGroupSignal(processGroup, syscall.SIGTERM); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	grace := controller.grace
	if grace <= 0 {
		grace = processGroupTerminationGrace
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		remaining := time.Until(deadline)
		pause := processGroupPollInterval
		if remaining < pause {
			pause = remaining
		}
		controller.processGroupSleep(pause)
		if err := controller.processGroupSignal(processGroup, 0); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return nil
			}
			return err
		}
	}
	if err := controller.processGroupSignal(processGroup, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}

func (controller *workerProcessController) processGroupSignal(processGroup int, signal syscall.Signal) error {
	if controller.signalProcessGroup != nil {
		return controller.signalProcessGroup(processGroup, signal)
	}
	return syscall.Kill(-processGroup, signal)
}

func (controller *workerProcessController) processGroupSleep(duration time.Duration) {
	if controller.sleep != nil {
		controller.sleep(duration)
		return
	}
	time.Sleep(duration)
}

func runUnixProcessWrapper(statusFD, controlFD, cleanupFD uintptr, cleanupPath, executable string, args []string) int {
	statusWriter := os.NewFile(statusFD, "tshoot-browser-target-status")
	controlReader := os.NewFile(controlFD, "tshoot-browser-wrapper-control")
	if statusWriter == nil || controlReader == nil {
		return 1
	}
	syscall.CloseOnExec(int(statusFD))
	syscall.CloseOnExec(int(controlFD))
	var cleanupIdentity *os.File
	if cleanupFD != 0 {
		cleanupIdentity = os.NewFile(cleanupFD, "tshoot-browser-plaintext-cleanup")
		if cleanupIdentity == nil || validateUnixPlaintextCleanupIdentity(cleanupIdentity, cleanupPath) != nil {
			_ = statusWriter.Close()
			_ = controlReader.Close()
			if cleanupIdentity != nil {
				_ = cleanupIdentity.Close()
			}
			return 1
		}
		syscall.CloseOnExec(int(cleanupFD))
	}
	terminationSignals := make(chan os.Signal, 1)
	signal.Notify(terminationSignals, syscall.SIGTERM)
	defer signal.Stop(terminationSignals)
	command := exec.Command(executable, args...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		status := unixTargetStatus{Error: err.Error()}
		_ = writeUnixTargetStatus(statusWriter, status)
		_ = controlReader.Close()
		_ = cleanupUnixPlaintextSession(cleanupIdentity, cleanupPath)
		return 1
	}
	targetDone := make(chan error, 1)
	go func() { targetDone <- command.Wait() }()
	controlDone := make(chan bool, 1)
	go func() {
		command, err := io.ReadAll(io.LimitReader(controlReader, 2))
		_ = controlReader.Close()
		controlDone <- err == nil && len(command) == 1 && command[0] == unixWrapperCleanupCommand
	}()
	targetFinished := false
	for {
		select {
		case targetErr := <-targetDone:
			targetFinished = true
			status := unixTargetStatus{}
			if targetErr != nil {
				status.Error = targetErr.Error()
			}
			if err := writeUnixTargetStatus(statusWriter, status); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "browser process wrapper status failed: %v\n", err)
				return cleanupAndTerminateUnixProcessGroup(cleanupIdentity, cleanupPath, targetDone, true)
			}
			statusWriter = nil
		case <-controlDone:
			if statusWriter != nil {
				_ = statusWriter.Close()
			}
			return cleanupAndTerminateUnixProcessGroup(cleanupIdentity, cleanupPath, targetDone, targetFinished)
		case <-terminationSignals:
			if statusWriter != nil {
				_ = statusWriter.Close()
			}
			return cleanupAndTerminateUnixProcessGroup(cleanupIdentity, cleanupPath, targetDone, targetFinished)
		}
	}
}

func writeUnixTargetStatus(statusWriter *os.File, status unixTargetStatus) error {
	return errors.Join(json.NewEncoder(statusWriter).Encode(status), statusWriter.Close())
}

func cleanupAndTerminateUnixProcessGroup(cleanupIdentity *os.File, cleanupPath string, targetDone <-chan error, targetFinished bool) int {
	_ = cleanupUnixPlaintextSession(cleanupIdentity, cleanupPath)
	processGroup := os.Getpid()
	if unix.Getpgrp() != processGroup {
		return 1
	}
	if targetFinished {
		_ = syscall.Kill(-processGroup, syscall.SIGKILL)
		return 1
	}
	if err := syscall.Kill(-processGroup, syscall.SIGTERM); err != nil && !errors.Is(err, syscall.ESRCH) {
		return 1
	}
	timer := time.NewTimer(processGroupTerminationGrace)
	defer timer.Stop()
	select {
	case <-targetDone:
	case <-timer.C:
	}
	_ = syscall.Kill(-processGroup, syscall.SIGKILL)
	return 1
}

func openUnixPlaintextCleanupIdentity(path string) (*os.File, error) {
	if err := validateUnixPlaintextCleanupPath(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("browser plaintext cleanup path is unsafe")
	}
	identity, err := os.Open(path)
	if err != nil {
		return nil, errors.New("open browser plaintext cleanup identity")
	}
	if err := validateUnixPlaintextCleanupIdentity(identity, path); err != nil {
		_ = identity.Close()
		return nil, err
	}
	return identity, nil
}

func validateUnixPlaintextCleanupPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || filepath.Clean(filepath.Dir(path)) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(path), ".tshoot-browser-session-") {
		return errors.New("browser plaintext cleanup path is not a managed temporary file")
	}
	return nil
}

func validateUnixPlaintextCleanupIdentity(identity *os.File, path string) error {
	if identity == nil {
		return errors.New("browser plaintext cleanup identity is missing")
	}
	if err := validateUnixPlaintextCleanupPath(path); err != nil {
		return err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return errors.New("browser plaintext cleanup path is unsafe")
	}
	identityInfo, err := identity.Stat()
	if err != nil || !identityInfo.Mode().IsRegular() || !os.SameFile(pathInfo, identityInfo) {
		return errors.New("browser plaintext cleanup identity changed")
	}
	return nil
}

func cleanupUnixPlaintextSession(identity *os.File, path string) error {
	if identity == nil {
		return nil
	}
	defer identity.Close()
	if err := validateUnixPlaintextCleanupIdentity(identity, path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
