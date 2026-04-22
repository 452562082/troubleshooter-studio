//go:build !windows

package deploy

import (
	"os/exec"
	"syscall"
)

// setProcessGroup 让 cmd 启动时自己成为进程组长(pgid == pid)。
// 这样 install.sh 里面 fork 出来的 brew/npm/pip 子孙进程都继承同一个 pgid,
// ctx cancel 时 killProcessGroup 可以一口气把整棵树杀掉,不留孤儿。
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killProcessGroup 往 -pgid 发 SIGKILL,等于 kill(2) 给进程组全员。
// 即使 bash 已经退出,brew/npm 还在跑,这一发也能把它们收尾。
// 进程还没启动或已经 reaped 时 Pid==0/< 0,静默 no-op。
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process == nil || cmd.Process.Pid <= 0 {
		return
	}
	// 负号 = 给整个进程组发信号(syscall.Kill 约定)
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
