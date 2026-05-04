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
	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// injectMCPServers 按 cfg 的 infra 开关往 mcp.servers map 里塞每条 MCP 配置。
// 全量重写匹配前缀的旧条目(避免 env 删了 / 切了 config-center 类型留下死引用)。
func injectMCPServers(
	root map[string]any,
	cfg *config.SystemConfig,
	get func(string) string,
) error {
	// MCP server key 用短 prefix(system.id),跟 IDE 平台对齐 + 避免 tool 名超 60 字限制。
	servers := BuildMCPServers(cfg, MCPBuildOptions{
		AgentID:           cfg.MCPKeyPrefix(),
		PruneEmpty:        false, // 留全 schema,agent 自决
		IncludeRawObsCurl: true,  // jaeger / elk URL 占位条目
	}, get)

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
