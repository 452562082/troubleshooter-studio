//go:build !windows

// self_test_mcp_probe_unix.go —— MCP probe 子进程组管理(darwin/linux/bsd)。
//
// npx / uvx 不是单进程:`npx -y @x/y-mcp` 内部 fork node → npm → 实际 mcp server。
// 默认 SIGKILL 顶层 npx 后,孙子进程不一起死,持有 stdin/stdout pipe 让父 cmd.Wait()
// 等不到子进程退出 → 父 goroutine 卡 → wg.Wait() 卡满 self-test 120s timeout。
//
// 修法:setpgid 让 npx 在自己的进程组,defer 时 syscall.Kill(-pgid, SIGKILL) 杀整组
// (负号 = 杀进程组),孙子 node/npm 一并干掉,cmd.Wait() 立即返回 fd 不泄漏。
package agent

import (
	"os/exec"
	"syscall"
)

// setProcessGroup 把 cmd 放到独立进程组(子进程 pid == pgid)。
// cmd.Start() 之前调用。
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup 杀进程组(包括所有子孙)。pid 是顶层进程 pid,setpgid 后 pgid == pid。
// 负号 pid 让 kill(2) 把 SIGKILL 发给整组,不是单个进程。
func killProcessGroup(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
