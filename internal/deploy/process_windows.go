//go:build windows

package deploy

import "os/exec"

// Windows:没有 POSIX 进程组概念(有 JobObject 但复杂)。简单起见:
// setProcessGroup no-op; killProcessGroup 直接 Process.Kill(SIGKILL-等价)。
// install.sh 在 Windows 上本来就跑不了(依赖 bash + POSIX 工具链),
// 这层 stub 只是让跨平台编译过,实际用户不会走到这里。
func setProcessGroup(*exec.Cmd) {}

func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
