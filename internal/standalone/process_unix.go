//go:build !windows

package standalone

import (
	"os/exec"
	"syscall"
)

// setProcessGroup / killProcessGroup:跟 internal/deploy 同样的目的 ——
// 让 python3 server.py 和它 fork 出的子孙都在同一进程组,Stop 时一锅端。
// 内容 1:1,如果再抽第三个地方要这个可以提公共包(目前 2 份 ok)。
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil || cmd.Process.Pid <= 0 {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
