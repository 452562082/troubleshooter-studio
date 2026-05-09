// mcp_launch.go —— `tshoot mcp-launch <type>` 子命令薄壳。
// 真正逻辑在 internal/mcplaunch,跟 cmd/tshoot-desktop 共享(否则 desktop 二进制被
// install 选作 launcher 路径时不识别 argv 会开 wails 窗口 —"启动一堆工作台"的根因)。
package main

import "github.com/xiaolong/troubleshooter-studio/internal/mcplaunch"

func runMCPLaunch(args []string) error {
	return mcplaunch.Run(args)
}
