// install_native_mcp_common.go —— Claude Code / Cursor / Codex / Openclaw 四家共享的
// MCP server 派生逻辑。
//
// 之前 install_native_mcp.go::buildMCPServersForCfg(IDE 用)和 install_native_openclaw_mcp.go::
// injectMCPServers(openclaw 用)两套实现长得几乎一样:都按 cfg.Infrastructure.ConfigCenters
// 跑 nacos × env、grafana per env、loki per env、lark messaging、feishu_project tracking。
// 改一处忘改另一处的事故已经踩过,抽一个 BuildMCPServers 共用。
//
// 区别用 MCPBuildOptions 控制:
//   - PruneEmpty:IDE 要(避免 settings.json 里把 "" 喂给后端,触发"无效连接"重试风暴);
//                openclaw 不要(保留全 schema 让 agent 自决)。
//   - IncludeRawObsCurl:openclaw 额外写 jaeger / elk 的 curl 占位条目(无独立 MCP,只记 URL
//                       让 agent 直查 ES API);IDE 不写(IDE 没"代理 curl 调 API"的运行时)。
//
// 命名:统一走 mcpKeyForAgent(agentID, prefix, sourceID, envID),单源走 "<prefix>-<env>",
// 多源走 "<prefix>-<sourceID>-<env>",IDE 共享 settings 池下加 agentID 前缀防撞名。
package agent

import (
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// MCPBuildOptions 控制 BuildMCPServers 的行为差异。
type MCPBuildOptions struct {
	// AgentID:MCP server key 前缀(如 "truss-bot")。空字符串 = 不加前缀(单 agent 项目级)。
	// IDE 共享 settings.json 池必须设非空,避免多 system 同名 mcp 互相覆盖。
	AgentID string

	// PruneEmpty:env block 里 value=="" 的 entry 丢掉(IDE 走这条,避免 IDE 把字面 "" 当
	// 真值传给后端进程造成无效连接);openclaw 留着等 agent 自决,所以 false。
	PruneEmpty bool

	// IncludeRawObsCurl:写入 jaeger / elk 的 "curl 占位" 条目(无独立 MCP,只记 URL 让
	// agent 通过 curl/HTTP 直查)。openclaw 走这条;IDE 没"代理 curl 调 API"的运行时,
	// 所以不写。
	IncludeRawObsCurl bool
}

// BuildMCPServers 按 cfg.Infrastructure 派生 {server_key: spec} 扁平 map。
// 调用方:
//   - install_native_mcp.go(IDE)→ 把返回值 merge 进 settings["mcpServers"]
//   - install_native_openclaw_mcp.go → 把返回值 merge 进 root["mcp"]["servers"]
//
// get(envVarName) 由调用方提供:从 creds map / 老 .env merge 后的合并视图取值。返回 ""
// 表示该字段没填,IDE 模式下整条字段会被 prune(见 PruneEmpty)。
func BuildMCPServers(cfg *config.SystemConfig, opts MCPBuildOptions, get func(string) string) map[string]any {
	servers := map[string]any{}
	envs := cfg.Environments

	keyFor := func(prefix, sourceID, envID string) string {
		return mcpKeyForAgent(opts.AgentID, prefix, sourceID, envID)
	}
	keyFixed := func(name string) string {
		if opts.AgentID == "" {
			return name
		}
		return opts.AgentID + "-" + name
	}

	// envBlock 处理 PruneEmpty:opts.PruneEmpty=true 时把 value=="" 的 entry 删掉。
	envBlock := func(m map[string]any) map[string]any {
		if !opts.PruneEmpty {
			return m
		}
		for k, v := range m {
			if s, ok := v.(string); ok && s == "" {
				delete(m, k)
			}
		}
		return m
	}

	// nacos per (source × env):多源 + 每 env 一个独立 MCP 实例
	for _, cc := range cfg.Infrastructure.ConfigCenters {
		if cc.Type != "nacos" {
			continue
		}
		for _, e := range envs {
			servers[keyFor("nacos", cc.ID, e.ID)] = map[string]any{
				"command": "uvx",
				"args":    []any{"nacos-mcp-router@latest"},
				"env": envBlock(map[string]any{
					"NACOS_ADDR":     get(envVar("CC_ADDR", cc.ID, e.ID)),
					"NACOS_USERNAME": get(envVar("CC_USER", cc.ID, e.ID)),
					"NACOS_PASSWORD": get(envVar("CC_PASS", cc.ID, e.ID)),
				}),
			}
		}
	}

	if cfg.Infrastructure.Observability.Grafana.Enabled {
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			servers[keyFor("grafana", "", e.ID)] = map[string]any{
				"command": "npx",
				"args": []any{
					"-y", "@leval/mcp-grafana",
					"--disable-incident", "--disable-alerting", "--disable-oncall",
					"--disable-admin", "--disable-sift", "--disable-pyroscope",
				},
				"env": envBlock(map[string]any{
					"GRAFANA_URL":      get("GRAFANA_URL_" + up),
					"GRAFANA_USERNAME": get("GRAFANA_USER_" + up),
					"GRAFANA_PASSWORD": get("GRAFANA_PASS_" + up),
				}),
			}
		}
	}

	if cfg.Infrastructure.Observability.Loki.Enabled {
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			servers[keyFor("loki", "", e.ID)] = map[string]any{
				"command": "npx",
				"args": []any{
					"-y", "@leval/mcp-grafana",
					"--disable-search", "--disable-dashboard", "--disable-datasource",
					"--disable-incident", "--disable-alerting", "--disable-oncall",
					"--disable-admin", "--disable-sift", "--disable-pyroscope",
				},
				"env": envBlock(map[string]any{
					"GRAFANA_URL":      get("GRAFANA_URL_" + up),
					"GRAFANA_USERNAME": get("GRAFANA_USER_" + up),
					"GRAFANA_PASSWORD": get("GRAFANA_PASS_" + up),
				}),
			}
		}
	}

	// jaeger / elk 是 openclaw 专用的"curl 占位"条目:agent 走 curl/HTTP 直查,无独立 MCP。
	// IDE 没"代理 curl 调 API"的运行时,所以不写;由 IncludeRawObsCurl 开关控制。
	if opts.IncludeRawObsCurl {
		if cfg.Infrastructure.Observability.Jaeger.Enabled {
			for _, e := range envs {
				up := strings.ToUpper(e.ID)
				servers[keyFor("jaeger", "", e.ID)] = map[string]any{
					"command": "curl",
					"args":    []any{},
					"env": map[string]any{
						"JAEGER_URL": get("JAEGER_URL_" + up),
					},
					"_note": "Jaeger 无独立 MCP；此条目仅记录 URL 供 agent 通过 curl/HTTP 直查",
				}
			}
		}
		if cfg.Infrastructure.Observability.ELK.Enabled {
			for _, e := range envs {
				up := strings.ToUpper(e.ID)
				servers[keyFor("elk", "", e.ID)] = map[string]any{
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
	}

	// messaging:lark
	for _, m := range cfg.Infrastructure.Messaging {
		if m.Enabled && m.Platform == "lark" {
			servers[keyFixed("lark-openapi")] = map[string]any{
				"command": "npx",
				"args":    []any{"-y", "@larksuite/lark-openapi-mcp"},
				"env": envBlock(map[string]any{
					"APP_ID":     get("LARK_APP_ID"),
					"APP_SECRET": get("LARK_APP_SECRET"),
				}),
			}
			break
		}
	}

	// project tracking:feishu_project
	for _, p := range cfg.Infrastructure.ProjectTracking {
		if p.Enabled && p.Platform == "feishu_project" {
			servers[keyFixed("FeishuProjectMcp")] = map[string]any{
				"command": "npx",
				"args":    []any{"-y", "@lark-project/mcp", "--domain", "https://project.feishu.cn"},
				"env": envBlock(map[string]any{
					"MCP_USER_TOKEN": get("MCP_USER_TOKEN"),
				}),
			}
			break
		}
	}

	return servers
}
