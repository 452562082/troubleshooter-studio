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
//   - 显式多源(sourceID!="default"):返回 "<prefix>-<sourceID>-<env>"
func mcpKey(prefix, sourceID, envID string) string {
	if sourceID == "" || sourceID == "default" {
		return prefix + "-" + envID
	}
	return prefix + "-" + sourceID + "-" + envID
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
