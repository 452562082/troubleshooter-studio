//go:build windows

package standalone

import "os/exec"

// Windows stub：跟 internal/deploy 同样简化处理。实际桌面端在 macOS 跑为主。
func setProcessGroup(*exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
