// ensure_uv.go —— 检测 `uvx` 是否在 PATH。
//
// nacos / jaeger / clickhouse 三家 MCP 都走 `uvx <python-pkg>` 启动(运行时拉 PyPI 包)。
// 如果用户机器没装 astral-sh/uv,这三家在所有 4 个 AI 平台启动时都会拿到 ENOENT 静默挂掉,
// IDE 端报错文案("spawn uvx ENOENT")对不熟悉 Python 工具链的用户极不友好。
//
// 策略选择:**不自动装 uv**。原因:
//   - uv 官方装法是 `curl -LsSf https://astral.sh/uv/install.sh | sh`,管道脚本到 shell
//     不适合 install 时静默执行(用户看不到内容、风险不透明)
//   - mcp-grafana 自动下载是因为它**只**给 grafana/loki MCP 用,装错位置不污染系统;uv 是
//     系统级 Python 工具,装哪儿、哪个版本牵扯用户的 PATH 偏好,只该用户自己决定
//
// 当前作法:install 时探测 → 缺失打 [warn] 给安装指引,继续装机不阻塞(其它 MCP 还能用)。
package agent

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// CfgUsesUvx 判断 cfg 是否涉及任何走 uvx 启动的 MCP(nacos / jaeger / clickhouse)。
// 用于决定要不要发 uvx 检测警告 — 用户没启这三家就不用提醒。
func CfgUsesUvx(cfg *config.SystemConfig) bool {
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		if cc.Type == "nacos" {
			return true
		}
	}
	if cfg.Infrastructure.Observability.Jaeger.Enabled {
		return true
	}
	for _, ds := range cfg.Infrastructure.DataStores {
		if ds.Enabled && (ds.Type == "clickhouse" || ds.Type == "rabbitmq") {
			return true
		}
	}
	return false
}

// CheckUvxAvailable 探测 uvx 是否在 PATH 里。命中返回 nil,缺失返回带安装指引的 error。
// caller 拿到 error 应当打 stderr 警告但不阻塞 install — 其它 MCP 不受 uv 影响,完全 abort
// 装机损失更大。
func CheckUvxAvailable() error {
	if _, err := exec.LookPath("uvx"); err == nil {
		return nil
	}
	return fmt.Errorf("uvx 不在 PATH\n%s", uvInstallHint())
}

// uvInstallHint 给用户的装 uv 指引(平台分支,直接抄上去就能跑)。
// 不写 `pipx install uv` —— pipx 比 uv 还少装(pipx 用户基本都装了 uv),提示路径乱。
// macOS 走 brew 最稳;Linux/Win 走官方一键脚本。
func uvInstallHint() string {
	var sb strings.Builder
	sb.WriteString("nacos / jaeger / clickhouse / rabbitmq 几家 MCP 走 `uvx <pkg>` 启动,缺 uv 这几家会启动失败。\n")
	sb.WriteString("装法(任选其一):\n")
	switch runtime.GOOS {
	case "darwin":
		sb.WriteString("  brew install uv\n")
		sb.WriteString("  # 或:curl -LsSf https://astral.sh/uv/install.sh | sh\n")
	case "windows":
		sb.WriteString("  powershell -ExecutionPolicy ByPass -c \"irm https://astral.sh/uv/install.ps1 | iex\"\n")
	default: // linux + 其它 unix
		sb.WriteString("  curl -LsSf https://astral.sh/uv/install.sh | sh\n")
		sb.WriteString("  # 或包管理:apt/dnf 主线还没收 uv,先走官方脚本\n")
	}
	sb.WriteString("装好后重跑 install,uvx 会被自动 LookPath 命中,无需改 yaml。\n")
	sb.WriteString("(其它 MCP 不依赖 uv,本次 install 不阻塞继续装。)")
	return sb.String()
}
