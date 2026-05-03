// install_native_openclaw_mcp.go —— openclaw 部署:把各类 MCP server 注入
// ~/.openclaw/openclaw.json 的 mcp.servers map。
//
// 多源 + 每 env 独立注册:nacos 类型逐源 × env 一份;grafana / loki / jaeger / elk
// 每 env 一份;lark / feishu_project 全局一份(单实例)。
//
// 命名加 agent-id 前缀(如 truss-bot-nacos-mcp-server-prod),跟 Claude Code/Cursor
// 的 install_native_mcp.buildMCPServersForCfg 三平台命名统一,routing config-map.yaml
// 里 mcp_server 字段三平台共用同一个值。多个 system 共存同一台机器不撞名。

package agent

import (
	"strings"

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
	agentID := cfg.MCPKeyPrefix()
	mcp, _ := root["mcp"].(map[string]any)
	if mcp == nil {
		mcp = map[string]any{}
		root["mcp"] = mcp
	}
	servers, _ := mcp["servers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
		mcp["servers"] = servers
	}

	envs := cfg.Environments

	// 多源配置中心:nacos 类型逐源 × env 注册独立 MCP 实例。
	// k8s/env-vars/apollo/consul 不走 MCP(走 creds.json + 配套 python 脚本)。
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		if cc.Type != "nacos" {
			continue
		}
		for _, e := range envs {
			env := map[string]any{
				"NACOS_ADDR":     get(envVar("CC_ADDR", cc.ID, e.ID)),
				"NACOS_USERNAME": get(envVar("CC_USER", cc.ID, e.ID)),
				"NACOS_PASSWORD": get(envVar("CC_PASS", cc.ID, e.ID)),
			}
			servers[mcpKeyForAgent(agentID, "nacos", cc.ID, e.ID)] = map[string]any{
				"command": "uvx",
				"args":    []any{"nacos-mcp-router@latest"},
				"env":     env,
			}
		}
	}

	// grafana per env
	gf := cfg.Infrastructure.Observability.Grafana
	if gf.Enabled {
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			env := map[string]any{
				"GRAFANA_URL":      get("GRAFANA_URL_" + up),
				"GRAFANA_USERNAME": get("GRAFANA_USER_" + up),
				"GRAFANA_PASSWORD": get("GRAFANA_PASS_" + up),
			}
			servers[mcpKeyForAgent(agentID, "grafana", "", e.ID)] = map[string]any{
				"command": "npx",
				"args": []any{
					"-y", "@leval/mcp-grafana",
					"--disable-incident", "--disable-alerting", "--disable-oncall",
					"--disable-admin", "--disable-sift", "--disable-pyroscope",
				},
				"env": env,
			}
		}
	}

	// loki per env(走 grafana mcp 但禁用 dashboard/datasource 那批 capability)
	if cfg.Infrastructure.Observability.Loki.Enabled {
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			env := map[string]any{
				"GRAFANA_URL":      get("GRAFANA_URL_" + up),
				"GRAFANA_USERNAME": get("GRAFANA_USER_" + up),
				"GRAFANA_PASSWORD": get("GRAFANA_PASS_" + up),
			}
			servers[mcpKeyForAgent(agentID, "loki", "", e.ID)] = map[string]any{
				"command": "npx",
				"args": []any{
					"-y", "@leval/mcp-grafana",
					"--disable-search", "--disable-dashboard", "--disable-datasource",
					"--disable-incident", "--disable-alerting", "--disable-oncall",
					"--disable-admin", "--disable-sift", "--disable-pyroscope",
				},
				"env": env,
			}
		}
	}

	// jaeger per env(没独立 MCP,只记 URL 给 agent 直查)
	if cfg.Infrastructure.Observability.Jaeger.Enabled {
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			servers[mcpKeyForAgent(agentID, "jaeger", "", e.ID)] = map[string]any{
				"command": "curl",
				"args":    []any{},
				"env": map[string]any{
					"JAEGER_URL": get("JAEGER_URL_" + up),
				},
				"_note": "Jaeger 无独立 MCP；此条目仅记录 URL 供 agent 通过 curl/HTTP 直查",
			}
		}
	}

	// elk per env
	if cfg.Infrastructure.Observability.ELK.Enabled {
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			servers[mcpKeyForAgent(agentID, "elk", "", e.ID)] = map[string]any{
				"command": "curl",
				"args":    []any{},
				"env": map[string]any{
					"KIBANA_URL":  get("KIBANA_URL_" + up),
					"ES_URL":      get("ELK_ES_URL_" + up),
					"ES_USERNAME": get("ELK_USERNAME"),
					"ES_PASSWORD": get("ELK_PASSWORD"),
				},
				"_note": "ELK 无独立 MCP；此条目仅记录 URL 供 agent 直查 ES API",
			}
		}
	}

	// messaging:lark
	for _, m := range cfg.Infrastructure.Messaging {
		if m.Enabled && m.Platform == "lark" {
			lk := "lark-openapi"
			if agentID != "" {
				lk = agentID + "-" + lk
			}
			servers[lk] = map[string]any{
				"command": "npx",
				"args":    []any{"-y", "@larksuite/lark-openapi-mcp"},
				"env": map[string]any{
					"APP_ID":     get("LARK_APP_ID"),
					"APP_SECRET": get("LARK_APP_SECRET"),
				},
			}
			break
		}
	}

	// project tracking:feishu_project
	for _, p := range cfg.Infrastructure.ProjectTracking {
		if p.Enabled && p.Platform == "feishu_project" {
			fk := "FeishuProjectMcp"
			if agentID != "" {
				fk = agentID + "-" + fk
			}
			servers[fk] = map[string]any{
				"command": "npx",
				"args":    []any{"-y", "@lark-project/mcp", "--domain", "https://project.feishu.cn"},
				"env": map[string]any{
					"MCP_USER_TOKEN": get("MCP_USER_TOKEN"),
				},
			}
			break
		}
	}
	return nil
}
