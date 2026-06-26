// ensure_nacos_mcp.go —— 把内嵌的 nacos MCP 脚本 (templates/.../scripts/nacos_mcp.py)
// 落到一个稳定共享路径 ~/.tshoot/scripts/nacos_mcp.py,供四家 AI 平台的 MCP 配置统一引用。
//
// 为什么走共享路径而不是各 target 工作区里的相对路径:每个 IDE 装出来的工作区布局不同
// (~/.claude/ vs ~/.cursor/ vs ~/.codex/ vs openclaw),MCP 配置里要写绝对路径。复制一份
// 到 ~/.tshoot/scripts/ 让所有 target 指同一个路径 —— 跟 kafka-mcp-server binary 落
// ~/.tshoot/bin/ 同款思路(EnsureKafkaMCPInstalled)。
//
// 跟 kafka 不同:nacos 脚本是仓库内嵌产物(embed.FS),不需要联网下载,直接 extract 即可。
//
// 决策背景(为什么这次走 MCP 能成,而 23d503a 不能):nacos_mcp.py 自己拿 username/password
// 跑 login + 后台 refresh,token 短 TTL 无所谓;不像上游 nacos-mcp-server 只接 --access_token
// 进程内固定。详见 install_native_mcp_common.go::BuildMCPServers 决策注释。
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tshoot "github.com/xiaolong/troubleshooter-studio"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

const nacosMCPScriptName = "nacos_mcp.py"

// nacosMCPEmbedPath 内嵌源路径(templates/ 被 //go:embed all:templates 收进二进制)。
const nacosMCPEmbedPath = "templates/workspace/skills/config-executor/scripts/nacos_mcp.py"

// splitNacosAddr 把 "host:port" / "http://host:port" / "host" 拆成 host, port。
// 不带 port → 默认 8848(nacos 标准端口;truss 现场也可能是别的,以 addr 里显式写的为准)。
// 8d05068 删掉的同名 helper,plan D 重新需要(脚本 --host/--port 分开收)。
func splitNacosAddr(addr string) (host, port string) {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimPrefix(addr, "https://")
	addr = strings.TrimSuffix(addr, "/")
	if i := strings.IndexByte(addr, '/'); i >= 0 {
		addr = addr[:i] // 去掉 path 段(如 /nacos)
	}
	host = addr
	port = "8848"
	if h, p, ok := strings.Cut(addr, ":"); ok {
		host, port = h, p
	}
	return host, port
}

// CfgUsesNacosMCP 判断 cfg 是否声明了 nacos 配置中心(决定要不要 ensure 脚本)。
func CfgUsesNacosMCP(cfg *config.SystemConfig) bool {
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		if cc.Type == "nacos" {
			return true
		}
	}
	return false
}

// EnsureNacosMCPScript 把内嵌的 nacos_mcp.py extract 到 ~/.tshoot/scripts/nacos_mcp.py,
// 返回绝对路径。BuildMCPServers 把该路径塞进 MCPBuildOptions.NacosMCPScriptPath,buildNacos
// 用它拼 `uv run --script <path>` 启动命令。
//
// 无条件覆盖:内嵌副本是唯一真值,tshoot 升级后脚本应跟着刷新。脚本很小(<300 行),无脑写
// 既便宜又避免"装过老版本后残留旧脚本"的坑(参考 kafka 版本号 cache 那段教训的反面 —— 这里
// 不带版本号是因为 extract 不联网、每次 install 必刷,不存在"该不该重下"的判断)。
//
// 失败返回 ("", err):调用方应打 [warn] 不阻塞 install(其它 MCP 不受影响),nacos 走
// config-executor SKILL 的 HTTP fallback(scripts/nacos_config.py)兜底。
func EnsureNacosMCPScript(onLog func(string)) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}
	dst := filepath.Join(home, ".tshoot", "scripts", nacosMCPScriptName)

	data, err := tshoot.TemplatesFS.ReadFile(nacosMCPEmbedPath)
	if err != nil {
		return "", fmt.Errorf("read embedded %s: %w", nacosMCPScriptName, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", filepath.Dir(dst), err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		return "", fmt.Errorf("write %s: %w", dst, err)
	}
	if onLog != nil {
		onLog(fmt.Sprintf("[ok] nacos MCP 脚本就位: %s", dst))
	}
	return dst, nil
}
