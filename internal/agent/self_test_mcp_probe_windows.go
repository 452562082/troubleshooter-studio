//go:build windows

// self_test_mcp_probe_windows.go —— Windows 没 POSIX process group,noop 兜底。
// MCP probe 在 windows 走原 cmd.Process.Kill() + cmd.Wait() 1s 上限路径。
package agent

import "os/exec"

// setProcessGroup 在 windows 上无操作(无 setpgid 等价概念)。
func setProcessGroup(cmd *exec.Cmd) {}

// killProcessGroup 在 windows 上无操作,caller 仍会调 cmd.Process.Kill() 杀顶层进程。
func killProcessGroup(pid int) {}
