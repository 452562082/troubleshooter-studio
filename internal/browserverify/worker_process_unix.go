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
	"strconv"
	"sync"
	"syscall"
	"time"
)

const (
	processGroupTerminationGrace = 2 * time.Second
	processGroupPollInterval     = 20 * time.Millisecond
	unixProcessWrapperArgument   = "--tshoot-browser-unix-wrapper"
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
	signalProcessGroup func(int, syscall.Signal) error
	sleep              func(time.Duration)
	grace              time.Duration
	beforeGroupCleanup func()
	waitWrapper        func(*exec.Cmd) error
	terminationMu      sync.Mutex
	reaping            bool
	closed             bool
	contextErr         error
	parentEndsOnce     sync.Once
	parentEndsErr      error
}

func init() {
	if len(os.Args) >= 5 && os.Args[1] == unixProcessWrapperArgument {
		statusFD, statusErr := strconv.ParseUint(os.Args[2], 10, 64)
		controlFD, controlErr := strconv.ParseUint(os.Args[3], 10, 64)
		if statusErr != nil || controlErr != nil {
			os.Exit(1)
		}
		os.Exit(runUnixProcessWrapper(uintptr(statusFD), uintptr(controlFD), os.Args[4], os.Args[5:]))
	}
}

func configureWorkerProcess(ctx context.Context, command *exec.Cmd) (*workerProcessController, error) {
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
	originalPath := command.Path
	originalArgs := append([]string(nil), command.Args[1:]...)
	command.Path = executable
	command.Args = append([]string{
		executable,
		unixProcessWrapperArgument,
		"3",
		"4",
		originalPath,
	}, originalArgs...)
	command.ExtraFiles = []*os.File{statusWrite, controlRead}
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
	controller.terminationMu.Lock()
	cleanupErr := controller.killOwnedProcessGroupLocked(command)
	controller.reaping = true
	controlErr := controller.closeControlWriter()
	controller.terminationMu.Unlock()
	wrapperErr := controller.waitForWrapper(command)
	controller.terminationMu.Lock()
	controller.closed = true
	contextErr := controller.contextErr
	controller.terminationMu.Unlock()
	if statusErr != nil {
		return errors.Join(statusErr, cleanupErr, controlErr, wrapperErr, contextErr)
	}
	if status.Error != "" {
		return errors.Join(errors.New(status.Error), cleanupErr, controlErr, contextErr)
	}
	return errors.Join(cleanupErr, controlErr, contextErr)
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
	)
}

func (controller *workerProcessController) closeParentEnds() error {
	controller.parentEndsOnce.Do(func() {
		controller.parentEndsErr = errors.Join(
			controller.closeFile(&controller.statusWrite),
			controller.closeFile(&controller.controlRead),
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

func (controller *workerProcessController) killOwnedProcessGroupLocked(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	err := controller.processGroupSignal(command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func (controller *workerProcessController) waitForWrapper(command *exec.Cmd) error {
	if controller.waitWrapper != nil {
		return controller.waitWrapper(command)
	}
	return command.Wait()
}

func (controller *workerProcessController) closeControlWriter() error {
	return controller.closeFile(&controller.controlWrite)
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

func runUnixProcessWrapper(statusFD, controlFD uintptr, executable string, args []string) int {
	statusWriter := os.NewFile(statusFD, "tshoot-browser-target-status")
	controlReader := os.NewFile(controlFD, "tshoot-browser-wrapper-control")
	if statusWriter == nil || controlReader == nil {
		return 1
	}
	syscall.CloseOnExec(int(statusFD))
	syscall.CloseOnExec(int(controlFD))
	terminationSignals := make(chan os.Signal, 1)
	signal.Notify(terminationSignals, syscall.SIGTERM)
	defer signal.Stop(terminationSignals)
	command := exec.Command(executable, args...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	status := unixTargetStatus{}
	if err := command.Run(); err != nil {
		status.Error = err.Error()
	}
	encodeErr := json.NewEncoder(statusWriter).Encode(status)
	closeStatusErr := statusWriter.Close()
	if err := errors.Join(encodeErr, closeStatusErr); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "browser process wrapper status failed: %v\n", err)
		_ = controlReader.Close()
		return 1
	}
	_, _ = io.Copy(io.Discard, controlReader)
	_ = controlReader.Close()
	return 0
}
