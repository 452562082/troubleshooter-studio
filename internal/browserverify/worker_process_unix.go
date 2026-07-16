//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package browserverify

import (
	"errors"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type workerProcessController struct {
	mu           sync.Mutex
	command      *exec.Cmd
	stopHardKill chan struct{}
	hardKillDone chan struct{}
	stopOnce     sync.Once
}

func configureWorkerProcess(command *exec.Cmd) *workerProcessController {
	controller := &workerProcessController{command: command}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		processGroup := command.Process.Pid
		if err := syscall.Kill(-processGroup, syscall.SIGTERM); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		controller.scheduleHardKill(processGroup)
		return nil
	}
	return controller
}

func (controller *workerProcessController) scheduleHardKill(processGroup int) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.hardKillDone != nil {
		return
	}
	controller.stopHardKill = make(chan struct{})
	controller.hardKillDone = make(chan struct{})
	go func(stop <-chan struct{}, done chan<- struct{}) {
		defer close(done)
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			_ = syscall.Kill(-processGroup, syscall.SIGKILL)
		case <-stop:
		}
	}(controller.stopHardKill, controller.hardKillDone)
}

func (controller *workerProcessController) kill(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

func (controller *workerProcessController) finish() error {
	if controller.command.Process == nil {
		return nil
	}
	processGroup := controller.command.Process.Pid
	groupErr := syscall.Kill(-processGroup, 0)
	if errors.Is(groupErr, syscall.ESRCH) {
		controller.stopScheduledHardKill()
		return nil
	}
	var terminateErr error
	controller.mu.Lock()
	scheduled := controller.hardKillDone != nil
	controller.mu.Unlock()
	if !scheduled {
		terminateErr = syscall.Kill(-processGroup, syscall.SIGTERM)
		if errors.Is(terminateErr, syscall.ESRCH) {
			return nil
		}
		controller.scheduleHardKill(processGroup)
	}

	controller.mu.Lock()
	done := controller.hardKillDone
	controller.mu.Unlock()
	if err := syscall.Kill(-processGroup, 0); errors.Is(err, syscall.ESRCH) {
		controller.stopScheduledHardKill()
	}
	<-done
	if errors.Is(terminateErr, syscall.ESRCH) {
		terminateErr = nil
	}
	return terminateErr
}

func (controller *workerProcessController) stopScheduledHardKill() {
	controller.mu.Lock()
	stop := controller.stopHardKill
	controller.mu.Unlock()
	if stop != nil {
		controller.stopOnce.Do(func() { close(stop) })
	}
}
