// Package mcplaunch 是 `<tshoot|troubleshooter-studio> mcp-launch <type>` 子命令的实现。
//
// 单独抽出来不放 cmd/tshoot 是因为 desktop app(cmd/tshoot-desktop)也要识别同名子命令 ——
// 否则 install 时 `os.Executable()` 拿到 desktop 二进制路径写进 ~/.claude.json,Claude
// 启动 MCP 时把 desktop 当 launcher 拉,每个 mongodb/postgresql/redis MCP 实例就开一个
// wails 窗口 ——"启动一堆工作台"的根因。两边 binary 共享这个 pkg,argv 识别后早 exec 走
// 真 npx,wails.Run 永远不会启动。
//
// 三家上游 MCP npm 包(mcp-mongo-server / @modelcontextprotocol/server-postgres /
// @gongrzhe/server-redis-mcp)只接位置参数,凭据写在 args 里 → IDE settings.json /
// openclaw.json 里 args 残留连接串,不便分享/审计。改走 launcher + env 注入。
//
// 跨平台:unix 走 syscall.Exec 原地替换让 IDE 直对 npx 收 stdio + signals 干净;
// windows 没 exec 走 spawn+wait+propagate-exit-code。
package mcplaunch

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

// IsLaunchCommand 判断 argv 是不是 mcp-launch 调用 — caller(各 binary 的 main)用这个
// 早识别,识别到就调 Run 然后 os.Exit,不该进各自的 wails / 自定义 dispatch。
func IsLaunchCommand(args []string) bool {
	return len(args) >= 2 && args[1] == "mcp-launch"
}

// Run 执行 mcp-launch <type>。args 应当是 os.Args[2:](去掉 binary 自身和 "mcp-launch")。
func Run(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: <binary> mcp-launch <mongodb|postgresql|redis>")
	}
	cmd, cmdArgs, err := launchSpec(args[0])
	if err != nil {
		return err
	}

	// windows 没 exec 系列,只能 spawn + wait + 透传退出码。
	// 代价:多一层进程,但 IDE 看到的 stdio 仍透明(我们 Std{in,out,err} 直接绑过去)。
	if runtime.GOOS == "windows" {
		c := exec.Command(cmd, cmdArgs...)
		c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := c.Run(); err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				os.Exit(ee.ExitCode())
			}
			return fmt.Errorf("spawn %s: %w", cmd, err)
		}
		return nil
	}

	// unix:syscall.Exec 直接替换 launcher 进程为 npx,IDE 监视的 PID 之后是 npx 自己,
	// JSON-RPC 不走中间代理,signals/SIGTERM 也由 IDE 直发给 npx 干净。
	bin, err := exec.LookPath(cmd)
	if err != nil {
		return fmt.Errorf("%s 不在 PATH(装 Node.js 后重试):%w", cmd, err)
	}
	fullArgs := append([]string{cmd}, cmdArgs...)
	if err := syscall.Exec(bin, fullArgs, os.Environ()); err != nil {
		return fmt.Errorf("exec %s: %w", bin, err)
	}
	return nil // 不会走到这,exec 成功后本进程已被替换
}

// launchSpec 把 (kind) → (command, args)。
// 凭据规约(BuildMCPServers 写 env 块时用同一组 key,改一处必同步):
//   - mongodb:    MONGODB_URI
//   - postgresql: POSTGRES_DSN
//   - redis:      REDIS_URL
//
// PruneEmpty=true 模式下 BuildMCPServers 已在生成时跳过没填凭据的条目;
// 这里再校验一次纯防御 — 用户/工具如果手改 settings 留了空 env 块,直接报错胜过让 npx
// 启动后再失败(npx 失败信息含糊,不如这里直说哪个 env 没填)。
func launchSpec(kind string) (string, []string, error) {
	switch kind {
	case "mongodb":
		uri := os.Getenv("MONGODB_URI")
		if uri == "" {
			return "", nil, fmt.Errorf("MONGODB_URI env 没设置(IDE settings 里 env 块漏了 / 被人清空)")
		}
		return "npx", []string{"-y", "mcp-mongo-server", uri, "--read-only"}, nil
	case "postgresql":
		dsn := os.Getenv("POSTGRES_DSN")
		if dsn == "" {
			return "", nil, fmt.Errorf("POSTGRES_DSN env 没设置")
		}
		return "npx", []string{"-y", "@modelcontextprotocol/server-postgres", dsn}, nil
	case "redis":
		// 钉死 1.0.0:见 internal/agent/install_native_mcp_common.go 注释 —
		// 这个包只发过 1.0.0,@latest 漂移到不兼容版本会无声 break。
		url := os.Getenv("REDIS_URL")
		if url == "" {
			return "", nil, fmt.Errorf("REDIS_URL env 没设置")
		}
		return "npx", []string{"-y", "@gongrzhe/server-redis-mcp@1.0.0", url}, nil
	default:
		return "", nil, fmt.Errorf("未知 mcp 类型 %q(支持:mongodb / postgresql / redis)", kind)
	}
}
