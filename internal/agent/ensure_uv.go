// ensure_uv.go —— 检测 `uvx` 是否在 PATH。
//
// Grafana、Redis、Jaeger、ClickHouse 等 MCP 通过 uvx 启动。
// uv 是共享工具运行时，缺失时提供安装提示，不静默修改系统安装。
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

// CfgUsesUvx 判断 cfg 是否涉及任何走 uvx 启动的 MCP。
// 用于决定要不要发 uvx 检测警告 — 未启用相关能力时不用提醒。
func CfgUsesUvx(cfg *config.SystemConfig) bool {
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		if cc.Type == "nacos" {
			return true
		}
	}
	if cfg.Infrastructure.Observability.Jaeger.Enabled || cfg.Infrastructure.Observability.Grafana.Enabled {
		return true
	}
	for _, ds := range cfg.Infrastructure.DataStores {
		if ds.Enabled && (ds.Type == "clickhouse" || ds.Type == "redis") {
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
	sb.WriteString("nacos / jaeger / clickhouse / grafana / redis 几家 MCP 走 `uvx <pkg>` 启动,缺 uv 这几家会启动失败。\n")
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
