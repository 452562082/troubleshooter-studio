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
	processGroup int
	stopHardKill chan struct{}
	hardKillDone chan struct{}
}

func configureWorkerProcess(command *exec.Cmd) *workerProcessController {
	controller := &workerProcessController{}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		processGroup := command.Process.Pid
		if err := syscall.Kill(-processGroup, syscall.SIGINT); err != nil {
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
	controller.processGroup = processGroup
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
	controller.mu.Lock()
	processGroup := controller.processGroup
	stop := controller.stopHardKill
	done := controller.hardKillDone
	controller.mu.Unlock()
	if done == nil {
		return nil
	}
	if err := syscall.Kill(-processGroup, 0); errors.Is(err, syscall.ESRCH) {
		close(stop)
	}
	<-done
	return nil
}
