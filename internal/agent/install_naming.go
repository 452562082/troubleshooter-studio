// install_naming.go —— 多源配置中心场景下的"凭证 env 变量名"和"MCP server key"
// 命名约定。把 source.id 作为命名空间嵌进去,同 env 下不同源就能各自有独立凭证。
//
// 兼容策略:source.id == "default"(单源 yaml 自动迁移产物的 sentinel)时,
// 名字回落到老 schema 形态(无 source 前缀),老的 .env / openclaw.json 不破。
// 用户显式声明多源(id != "default")才进入新形态。
package agent

import "strings"

// mcpKey 拼出 openclaw.json 里某个 source × env 对应的 MCP server key。
//   - 老 single-source 迁移路径(sourceID=="default"):返回 "<prefix>-<env>" 不破老结构
//   - sourceID == prefix 时(用户没改名,id 直接 = type 如 nacos / kuboard 等):
//     去重,返回 "<prefix>-<env>"(不再叠 "nacos-nacos-dev" 这种)
//   - 显式多源,id 跟 type 不同(如 id=legacy-nacos / type=nacos):返回
//     "<prefix>-<sourceID>-<env>" 区分多个 nacos 实例
//
// **注**:这是"未带 agent-id 前缀"的形态,只剩 OpenClaw 老 mcp 注册路径在用(它的 mcp.servers 是
// 项目级的,有 agents.list[i].id 隔离调用,key 重名也能各取各的)。Claude Code / Cursor 走 MCP
// 时是用户级 settings.json 共享池,key 必须加 agent-id 前缀避免多 system 同名 mcp 互相覆盖,
// 走 mcpKeyForAgent(agentID, prefix, sourceID, envID)。
func mcpKey(prefix, sourceID, envID string) string {
	if sourceID == "" || sourceID == "default" || sourceID == prefix {
		return prefix + "-" + envID
	}
	return prefix + "-" + sourceID + "-" + envID
}

// mcpKeyForAgent 在 mcpKey 基础上加 <agent-id> 前缀,Claude Code / Cursor 用。
// 例:agent-id=truss-bot,prefix=nacos-mcp-server,sourceID=default,envID=prod
//
//	→ "truss-bot-nacos-mcp-server-prod"
//
// 这样多个 system 的 agent 装到同一台 IDE 不会因 prefix 重名互相覆盖。agentID 强制大写转小写
// 不动(保持跟 system.id 形态一致,routing config-map.yaml 里 mcp_server 字段才能拼对)。
func mcpKeyForAgent(agentID, prefix, sourceID, envID string) string {
	base := mcpKey(prefix, sourceID, envID)
	if agentID == "" {
		return base
	}
	return agentID + "-" + base
}

// envVar 拼出 .env / 凭证表单字段的环境变量名。约定:
//   - 老 single-source(sourceID=="default"):"<PREFIX>_<ENV>"
//   - 显式多源:"<PREFIX>_<SOURCE>_<ENV>"
//
// SOURCE / ENV 都被强制大写 + 把 - 转 _,符合 bash 变量命名规则。
func envVar(prefix, sourceID, envID string) string {
	base := prefix + "_"
	if sourceID != "" && sourceID != "default" {
		base += strings.ToUpper(strings.ReplaceAll(sourceID, "-", "_")) + "_"
	}
	return base + strings.ToUpper(envID)
}
