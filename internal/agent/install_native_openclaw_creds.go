// install_native_openclaw_creds.go —— openclaw 部署:apollo / consul / env-vars / kuboard
// 类型的凭证写到 ~/.openclaw/<agent_id>-creds.json,agent 直读这份(不走 MCP)。
//
// 多源场景按"类型分顶层 section"组织:
//
//	{
//	  "apollo":     { "<source-id>": { "<env>": {meta_url,token} } },
//	  "consul":     { "<source-id>": { "<env>": {host,token} } },
//	  "static":     { "<source-id>": { "<env>": {redis,mysql,...} } },
//	  "kuboard":    { "<source-id>": { "<env>": {url,user,pass,access_key,service_map} } },
//	}
//
// 单源迁移(source.id == "default" / 空)保留老两层结构 {<env>: ...} 兼容。

package agent

import (
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// needsCreds 决定是否需要写 <agent_id>-creds.json:nacos 不需要(已经在 mcp 里),
// 其它类型(apollo/consul/env-vars/kuboard)需要,因为 agent 直读这份 json,不走 MCP。
func needsCreds(ccType string) bool {
	switch ccType {
	case "apollo", "consul", "env-vars", "kuboard":
		return true
	}
	return false
}

// writeCredsByType 按 cfg.Infrastructure.ConfigCenters 各源类型,把对应字段写入
// creds map 的 topKey section。同一类型多源平铺到同 section 下,以 source.id 二级 key 区隔。
func writeCredsByType(creds map[string]any, cfg *config.SystemConfig, get func(string) string) {
	envs := cfg.Environments

	for _, cc := range cfg.Infrastructure.ConfigCenters {
		switch cc.Type {
		case "apollo":
			writeCredsSection(creds, "apollo", cc, envs, func(e config.Environment) map[string]any {
				return map[string]any{
					"meta_url": get(envVar("APOLLO_META", cc.ID, e.ID)),
					"token":    get(envVar("APOLLO_TOKEN", cc.ID, e.ID)),
				}
			})
		case "consul":
			writeCredsSection(creds, "consul", cc, envs, func(e config.Environment) map[string]any {
				return map[string]any{
					"host":  get(envVar("CONSUL_HOST", cc.ID, e.ID)),
					"token": get(envVar("CONSUL_TOKEN", cc.ID, e.ID)),
				}
			})
		case "env-vars":
			writeCredsSection(creds, "static", cc, envs, func(e config.Environment) map[string]any {
				envSection := map[string]any{}
				for _, ds := range cfg.Infrastructure.DataStores {
					if !ds.Enabled {
						continue
					}
					envSection[ds.Type] = get(envVar("STATIC_"+strings.ToUpper(ds.Type), cc.ID, e.ID))
				}
				return envSection
			})
		case "kuboard":
			// kuboard:走 Kuboard HTTP API。每 env 一份连接凭证(url + 鉴权);
			// 鉴权二选一:access_key(API 访问凭证,推荐)或 username+password。两条都写入 creds.json,
			// 让 bot 运行时按"access_key 优先"取用。cluster/namespace/configmap 是 per-service,
			// 从 cc.ServiceMap 落到 service_map 子字段。
			writeCredsSection(creds, "kuboard", cc, envs, func(e config.Environment) map[string]any {
				row := map[string]any{
					"url":        get(envVar("KUBOARD_URL", cc.ID, e.ID)),
					"username":   get(envVar("KUBOARD_USER", cc.ID, e.ID)),
					"password":   get(envVar("KUBOARD_PASS", cc.ID, e.ID)),
					"access_key": get(envVar("KUBOARD_ACCESS_KEY", cc.ID, e.ID)),
				}
				if envSvcMap, ok := cc.ServiceMap[e.ID]; ok && len(envSvcMap) > 0 {
					svcMap := map[string]any{}
					for svc, entry := range envSvcMap {
						svcMap[svc] = map[string]any{
							"cluster":   entry.Cluster,
							"namespace": entry.Namespace,
							"configmap": entry.ConfigMap,
						}
					}
					row["service_map"] = svcMap
				}
				return row
			})
		}
	}
}

// writeCredsSection 把一个源的 (env → fields) 写到 creds[topKey] 下。
// 单源迁移(cc.id == "default"):保留老两层结构 creds[topKey][env] = fields(向后兼容)。
// 显式多源:三层结构 creds[topKey][source.id][env] = fields。
func writeCredsSection(
	creds map[string]any,
	topKey string,
	cc config.ConfigCenter,
	envs []config.Environment,
	rowFn func(config.Environment) map[string]any,
) {
	if cc.ID == "" || cc.ID == "default" {
		section := map[string]any{}
		for _, e := range envs {
			section[e.ID] = rowFn(e)
		}
		creds[topKey] = section
		return
	}
	// 多源:已有 section 合并(同 topKey 下不同源共存)
	top, _ := creds[topKey].(map[string]any)
	if top == nil {
		top = map[string]any{}
		creds[topKey] = top
	}
	bySource := map[string]any{}
	for _, e := range envs {
		bySource[e.ID] = rowFn(e)
	}
	top[cc.ID] = bySource
}
