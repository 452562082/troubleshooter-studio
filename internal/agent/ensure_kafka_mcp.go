// ensure_kafka_mcp.go —— 检测 `kafka-mcp-server` binary 是否在 PATH。
//
// kafka 这家 MCP 业界没有靠谱的 npx/uvx 实现:
//   - npm 上唯一活跃的 `@confluentinc/mcp-confluent` 依赖 native librdkafka 绑定
//     (`@confluentinc/kafka-javascript`),prebuilt Node ABI 矩阵滞后(Node 26 没出 v147 prebuilt),
//     install scripts 静默失败导致 binding 缺失,跨平台脆弱(2026-05 实战踩坑确诊)
//   - PyPI 上 `kafka-mcp-server` 0.1.1 只 2 个工具 placeholder,生产不可用;其余 PyPI 候选
//     都依赖 `confluent-kafka` Python wrapper(同样 librdkafka)
//
// 所以 kafka 这家**走 binary 安装**(franz-go 纯 Go,无 native deps,跨平台 GoReleaser):
//   - 上游:tuannvm/kafka-mcp-server(MIT,brew tap + GitHub Release 5 个 triple 全)
//   - 装一次永远稳,不受 Node / glibc / librdkafka 版本飘移影响
//
// 跟其它 7 家(npx/uvx 零安装)的 trade-off:用户多一步 install 命令,换跨机器一致性。
//
// 策略:install 时探测 → 缺失打 [warn] 给安装指引,继续装机不阻塞(其它 MCP 不受影响)。
package agent

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// CfgUsesKafkaMCP 判断 cfg 是否启用了 kafka 数据层。
// 用于决定要不要发 kafka-mcp-server 探测警告 — 没启 kafka 就不用提醒。
func CfgUsesKafkaMCP(cfg *config.SystemConfig) bool {
	for _, ds := range cfg.Infrastructure.DataStores {
		if ds.Enabled && ds.Type == "kafka" {
			return true
		}
	}
	return false
}

// CheckKafkaMCPServerAvailable 探测 kafka-mcp-server 是否在 PATH 里。命中返回 nil,
// 缺失返回带安装指引的 error。caller 拿到 error 应当打 stderr 警告但不阻塞 install。
func CheckKafkaMCPServerAvailable() error {
	if _, err := exec.LookPath("kafka-mcp-server"); err == nil {
		return nil
	}
	return fmt.Errorf("kafka-mcp-server 不在 PATH\n%s", kafkaMCPInstallHint())
}

// kafkaMCPInstallHint 给用户的装 kafka-mcp-server 指引(平台分支)。
// 不写 `go install` —— tuannvm 仓库不是 main package,go install 装不上;且 Release tarball
// 比源码编译稳得多。
func kafkaMCPInstallHint() string {
	var sb strings.Builder
	sb.WriteString("kafka 数据层 MCP 走 `kafka-mcp-server` binary 启动(tuannvm/kafka-mcp-server,MIT,franz-go 纯 Go 无 native deps)。\n")
	sb.WriteString("装法(任选其一):\n")
	switch runtime.GOOS {
	case "darwin":
		sb.WriteString("  brew install tuannvm/mcp/kafka-mcp-server\n")
		sb.WriteString("  # 或从 GitHub Release 直下 binary:\n")
		sb.WriteString("  # https://github.com/tuannvm/kafka-mcp-server/releases/latest\n")
	case "windows":
		sb.WriteString("  # 从 GitHub Release 下载 windows zip 包,解压放 PATH:\n")
		sb.WriteString("  # https://github.com/tuannvm/kafka-mcp-server/releases/latest\n")
	default: // linux + 其它 unix
		sb.WriteString("  # 从 GitHub Release 下载对应 arch 的 tarball,解压放 PATH:\n")
		sb.WriteString("  # https://github.com/tuannvm/kafka-mcp-server/releases/latest\n")
		sb.WriteString("  # brew 也可(Linuxbrew):brew install tuannvm/mcp/kafka-mcp-server\n")
	}
	sb.WriteString("装好后重跑 install,kafka-mcp-server 会被自动 LookPath 命中,无需改 yaml。\n")
	sb.WriteString("(其它 MCP 不依赖 kafka-mcp-server,本次 install 不阻塞继续装。)")
	return sb.String()
}
