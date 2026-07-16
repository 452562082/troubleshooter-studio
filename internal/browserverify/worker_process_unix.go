//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package browserverify

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const (
	processGroupTerminationGrace = 2 * time.Second
	processGroupPollInterval     = 20 * time.Millisecond
)

type workerProcessController struct {
	command            *exec.Cmd
	signalProcessGroup func(int, syscall.Signal) error
	sleep              func(time.Duration)
	grace              time.Duration
}

func configureWorkerProcess(command *exec.Cmd) (*workerProcessController, error) {
	controller := &workerProcessController{
		command: command,
		signalProcessGroup: func(processGroup int, signal syscall.Signal) error {
			return syscall.Kill(-processGroup, signal)
		},
		sleep: time.Sleep,
		grace: processGroupTerminationGrace,
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return controller.cancel(command) }
	return controller, nil
}

func (*workerProcessController) afterStart(*exec.Cmd) error { return nil }

func (controller *workerProcessController) cancel(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	return controller.terminateProcessGroup(command.Process.Pid)
}

func (controller *workerProcessController) kill(command *exec.Cmd) error {
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
	if controller.command.Process == nil {
		return nil
	}
	err := controller.cleanupExitedProcessGroup(controller.command.Process.Pid)
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

// cleanupExitedProcessGroup runs only after the direct child has been reaped.
// Any remaining members are descendants, so there is no reason to wait through
// the graceful cancellation interval or schedule work after this call returns.
func (controller *workerProcessController) cleanupExitedProcessGroup(processGroup int) error {
	if err := controller.processGroupSignal(processGroup, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	if err := controller.processGroupSignal(processGroup, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

func (controller *workerProcessController) terminateProcessGroup(processGroup int) error {
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
