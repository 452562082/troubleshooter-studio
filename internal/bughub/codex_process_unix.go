//go:build !windows

package bughub

import (
	"os/exec"
	"syscall"
)

func setCodexProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// CommandContext normally kills only the parent. Node-based CLIs may leave
	// children holding stdout open, preventing deadline/cancellation completion.
	if cmd.Cancel != nil {
		cmd.Cancel = func() error {
			killCodexProcessGroup(cmd.Process.Pid)
			return cmd.Process.Kill()
		}
	}
}

func killCodexProcessGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
