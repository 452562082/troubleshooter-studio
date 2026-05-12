// ensure_kafka_mcp.go —— kafka-mcp-server(CefBoud Go binary)预装探测。
//
// 跟 ensure_uv.go 同款机制:其它 6 个数据层 MCP 走 npx/uvx 零安装,kafka 不是 —— 用户
// 必须先把 binary 装到 PATH 里(`go install` 或源码 `go build`)。装机时探测 PATH,
// 缺 binary 就打 [warn] 给指引,不阻塞 install(用户可以装完机器人再装 binary,反正
// 真正调 kafka MCP 是排障时才调,不影响其它平台 MCP)。
package agent

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// cfgUsesKafkaMCP 检查 yaml 是否启用 kafka data_store(决定要不要做 kafka-mcp-server 探测警告)。
func cfgUsesKafkaMCP(cfg *config.SystemConfig) bool {
	for _, ds := range cfg.Infrastructure.DataStores {
		if ds.Enabled && ds.Type == "kafka" {
			return true
		}
	}
	return false
}

// CheckKafkaMCPServerAvailable 探测 PATH 里有没有 `kafka-mcp-server` binary。
// 已装 → 返 nil;没装 → 返带平台分支安装指引的 error。caller 打 stderr [warn] 不阻塞。
func CheckKafkaMCPServerAvailable() error {
	if _, err := exec.LookPath("kafka-mcp-server"); err == nil {
		return nil
	}
	return fmt.Errorf("kafka-mcp-server 不在 PATH\n%s", kafkaMCPInstallHint())
}

// kafkaMCPInstallHint 给用户的装 kafka-mcp-server 指引(平台分支,直接抄上去就能跑)。
//
// 跟 7 家数据层 MCP 不同,这家是 Go 二进制不走 npx/uvx 零安装。
// 推荐路径用 `go install`(Go 1.18+ 支持),binary 自动落到 $GOPATH/bin。
func kafkaMCPInstallHint() string {
	var sb strings.Builder
	sb.WriteString("kafka 数据层 MCP 走 `kafka-mcp-server`(CefBoud,Go binary,带 --read-only flag),\n")
	sb.WriteString("缺 binary 时 kafka MCP 会启动 ENOENT,本机就只能 fallback 到 kafka CLI(kafka-topics.sh 等)。\n")
	sb.WriteString("装法(任选其一):\n")
	switch runtime.GOOS {
	case "darwin", "linux":
		sb.WriteString("  # 推荐:Go 工具链一键装(需 Go 1.18+,binary 落到 $GOPATH/bin)\n")
		sb.WriteString("  go install github.com/CefBoud/kafka-mcp-server/cmd/kafka-mcp-server@latest\n")
		sb.WriteString("  # 验证\n")
		sb.WriteString("  command -v kafka-mcp-server\n")
		if runtime.GOOS == "darwin" {
			sb.WriteString("  # 或从源码 build(没 Go 但有 brew)\n")
			sb.WriteString("  brew install go && go install github.com/CefBoud/kafka-mcp-server/cmd/kafka-mcp-server@latest\n")
		}
	case "windows":
		sb.WriteString("  # PowerShell:Go 工具链装(需 Go 1.18+,binary 落到 %GOPATH%\\bin)\n")
		sb.WriteString("  go install github.com/CefBoud/kafka-mcp-server/cmd/kafka-mcp-server@latest\n")
		sb.WriteString("  # 验证\n")
		sb.WriteString("  where kafka-mcp-server\n")
	}
	sb.WriteString("装完确保该 binary 在用户 PATH 里(macOS 通常 $HOME/go/bin,需要时加 PATH)。\n")
	return sb.String()
}
