// install_native_openclaw_mcp.go —— openclaw 部署:把各类 MCP server 注入
// ~/.openclaw/openclaw.json 的 mcp.servers map。
//
// 派生逻辑(nacos × env / grafana / loki / lark / feishu / jaeger / elk)收口在
// install_native_mcp_common.go::BuildMCPServers,跟 IDE 三家共用同一份。本文件只剩
// "把 servers 写到 root["mcp"]["servers"]" 这层 openclaw.json 专属容器逻辑。
//
// 区别:openclaw 走 PruneEmpty=false(留全 schema 让 agent 自决) + IncludeRawObsCurl=true
// (jaeger/elk 用 curl 占位条目,无独立 MCP);IDE 反过来。

package agent

import (
	"fmt"
	"os"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// injectMCPServers 按 cfg 的 infra 开关往 mcp.servers map 里塞每条 MCP 配置。
// 全量重写匹配前缀的旧条目(避免 env 删了 / 切了 config-center 类型留下死引用)。
//
// ocHome:openclaw 用户目录(~/.openclaw),用于 ensure grafana mcp 二进制下载到
// <ocHome>/bin/mcp-grafana,并把 BuildMCPServers 输出的 __GRAFANA_MCP_BIN__ 占位
// sentinel 替换成绝对路径 — 否则 spawn 时报 ENOENT。
func injectMCPServers(
	root map[string]any,
	cfg *config.SystemConfig,
	get func(string) string,
	ocHome string,
) error {
	// MCP server key 用短 prefix(system.id),跟 IDE 平台对齐 + 避免 tool 名超 60 字限制。
	servers := BuildMCPServers(cfg, MCPBuildOptions{
		AgentID:           cfg.MCPKeyPrefix(),
		PruneEmpty:        false, // 留全 schema,agent 自决
		IncludeRawObsCurl: true,  // jaeger / elk URL 占位条目
	}, get)

	// 跟 MergeMCPIntoIDESettings 同款:grafana/loki 用 mcp-grafana 二进制,BuildMCPServers
	// 输出的 command 是占位 sentinel(__GRAFANA_MCP_BIN__),install 时必须替换成 <ocHome>/bin/mcp-grafana
	// 绝对路径。漏替换 → openclaw 启动时报 spawn __GRAFANA_MCP_BIN__ ENOENT。
	if hasGrafanaPlaceholder(servers) {
		if binPath, err := EnsureMCPGrafanaBinary(ocHome, nil); err == nil {
			replaceGrafanaWithBinary(servers, binPath)
		} else {
			fmt.Fprintf(os.Stderr,
				"[warn] openclaw:自动装 mcp-grafana 二进制失败: %v\n%s"+
					"装好后重跑 openclaw 部署可一并修复 grafana/loki MCP。\n"+
					"暂时回退到 npx -y @leval/mcp-grafana(已知 stdout 污染风险)。\n",
				err, MCPGrafanaInstallHint(ocHome))
			replaceGrafanaWithNpxFallback(servers)
		}
	}

	mcp, _ := root["mcp"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
		root["mcp"] = mcp
	}
	existing, _ := mcp["servers"].(map[string]any)
	if existing == nil {
		existing = map[string]any{}
		mcp["servers"] = existing
	}
	// 全量重写匹配前缀的条目:env 删了 / 切了 config-center 类型留下的死引用由用户手清
	// (跟 IDE 行为一致 —— 比误删重要 server 强)。
	for k, v := range servers {
		existing[k] = v
	}
	return nil
}
