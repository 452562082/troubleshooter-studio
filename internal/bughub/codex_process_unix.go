//go:build !windows

package bughub

import (
	"os/exec"
	"syscall"
)

func setCodexProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killCodexProcessGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
