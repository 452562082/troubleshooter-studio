//go:build windows

package browserverify

import (
	"os"
	"os/exec"
)

type workerProcessController struct{}

func configureWorkerProcess(command *exec.Cmd) *workerProcessController {
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		return command.Process.Kill()
	}
	return &workerProcessController{}
}

func (*workerProcessController) kill(command *exec.Cmd) error {
	if command.Process == nil {
		return os.ErrProcessDone
	}
	return command.Process.Kill()
}

func (*workerProcessController) finish() error { return nil }
