// install_native_mcp.go —— Claude Code / Cursor 的 MCP server 自动注入。
//
// 之前 InstallNative 只做文件拷贝(agent .md / skills/ / scripts/);MCP 服务器配置
// 完全没动 IDE 的 settings.json,用户装完 agent 调任何 MCP 工具都失败。
//
// 这里补齐:从 cfg 推 mcpServers 配置,merge 进 ~/.claude/settings.json 或 ~/.cursor/mcp.json。
// merge 策略:用 Studio 管理的固定前缀(nacos-mcp-server-* / grafana-mcp-server-* 等)
// 全删后重写,避免改 yaml 后旧条目残留;用户手加的别名(其它前缀)保留。
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
)

// MergeMCPIntoIDESettings 把 cfg 派生的 mcpServers 写进对应 target 的 IDE settings 文件。
//   - target=claude-code → ~/.claude/settings.json,顶层 mcpServers 字段
//   - target=cursor      → ~/.cursor/mcp.json,顶层 mcpServers 字段
//
// creds 是 env-var-name → value 的 map(跟 InstallNativeOpenclaw 一样的 schema)。
// 桌面端 wizard 通过 buildOpenclawCreds() 拼出来传过来;CLI 没 creds 时传 nil,
// 注入的 env 字段值会变成 {{ENV_VAR}} 占位符让用户手填。
func MergeMCPIntoIDESettings(target string, cfg *config.SystemConfig, creds map[string]string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("read $HOME: %w", err)
	}
	var settingsPath string
	switch target {
	case "claude-code":
		settingsPath = filepath.Join(home, ".claude", "settings.json")
	case "cursor":
		settingsPath = filepath.Join(home, ".cursor", "mcp.json")
	default:
		return fmt.Errorf("MergeMCPIntoIDESettings: 不支持的 target %q(只接 claude-code / cursor)", target)
	}

	root, err := readJSONOrEmpty(settingsPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", settingsPath, err)
	}
	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}

	// 1) 写新:用 buildMCPServersForCfg 派生。get 返回空串表示"没值",
	// buildMCPServersForCfg 内部对空值字段直接 omit(不写进 env block),
	// 否则 IDE 启 MCP 时会把 "{{NACOS_ADDR_DEV}}" 这种字面值传给 nacos 进程导致连接失败。
	// 命名带 agent-id 前缀(如 "truss-bot-nacos-mcp-server-prod"),保证多 system 共存时不撞名。
	get := func(k string) string {
		if creds == nil {
			return ""
		}
		return creds[k]
	}
	new := buildMCPServersForCfg(cfg, cfg.ResolveID(), get)

	// 2) 删旧:**精确**只删本次实际生成的同名 key(替换式更新)。不再前缀通配,
	// 避免误伤用户手加的同前缀别人家 agent 的 server。环境删了 / 切了配置中心后
	// 不再生成的旧 key 会留下,需要用户手清(可接受 —— 比误删重要 server 强)。
	for k := range new {
		delete(servers, k)
	}
	for k, v := range new {
		servers[k] = v
	}
	root["mcpServers"] = servers

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(settingsPath), err)
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", settingsPath, err)
	}
	return nil
}

// buildMCPServersForCfg 从 cfg 派生 mcpServers map,跟 injectMCPServers 行为对齐
// (实质是同一份 schema,只是这里返回扁平 map 而不是写到 root["mcp"]["servers"])。
// get(envVarName) 返回 cred 值;**返回空串的字段会从 env block 里 omit**(不写 key),
// 避免 IDE 启 MCP 时把 "{{XXX}}" 占位字符串当真传给后端进程造成无效连接。
// 用户事后可以在 IDE settings.json 手填该字段。
//
// agentID 加到所有 key 前缀(如 "truss-bot-nacos-mcp-server-prod"),保证 Claude Code/Cursor
// 用户级共享 settings.json 池里多 system 共存不撞名。空字符串则不加前缀(单 agent 场景)。
func buildMCPServersForCfg(cfg *config.SystemConfig, agentID string, get func(string) string) map[string]any {
	servers := map[string]any{}
	envs := cfg.Environments
	keyFor := func(prefix, sourceID, envID string) string {
		return mcpKeyForAgent(agentID, prefix, sourceID, envID)
	}
	keyFixed := func(name string) string {
		if agentID == "" {
			return name
		}
		return agentID + "-" + name
	}

	// 把 envMap 里 value=="" 的 entry 删掉,空字段不进 settings.json
	pruneEmpty := func(m map[string]any) map[string]any {
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
			env := pruneEmpty(map[string]any{
				"NACOS_ADDR":     get(envVar("CC_ADDR", cc.ID, e.ID)),
				"NACOS_USERNAME": get(envVar("CC_USER", cc.ID, e.ID)),
				"NACOS_PASSWORD": get(envVar("CC_PASS", cc.ID, e.ID)),
			})
			servers[keyFor("nacos-mcp-server", cc.ID, e.ID)] = map[string]any{
				"command": "uvx",
				"args":    []any{"nacos-mcp-router@latest"},
				"env":     env,
			}
		}
	}

	if cfg.Infrastructure.Observability.Grafana.Enabled {
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			env := pruneEmpty(map[string]any{
				"GRAFANA_URL":      get("GRAFANA_URL_" + up),
				"GRAFANA_USERNAME": get("GRAFANA_USER_" + up),
				"GRAFANA_PASSWORD": get("GRAFANA_PASS_" + up),
			})
			servers[keyFor("grafana-mcp-server", "", e.ID)] = map[string]any{
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

	if cfg.Infrastructure.Observability.Loki.Enabled {
		for _, e := range envs {
			up := strings.ToUpper(e.ID)
			env := pruneEmpty(map[string]any{
				"GRAFANA_URL":      get("GRAFANA_URL_" + up),
				"GRAFANA_USERNAME": get("GRAFANA_USER_" + up),
				"GRAFANA_PASSWORD": get("GRAFANA_PASS_" + up),
			})
			servers[keyFor("loki-mcp-server", "", e.ID)] = map[string]any{
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

	// messaging:lark
	for _, m := range cfg.Infrastructure.Messaging {
		if m.Enabled && m.Platform == "lark" {
			servers[keyFixed("lark-openapi")] = map[string]any{
				"command": "npx",
				"args":    []any{"-y", "@larksuite/lark-openapi-mcp"},
				"env": pruneEmpty(map[string]any{
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
				"env": pruneEmpty(map[string]any{
					"MCP_USER_TOKEN": get("MCP_USER_TOKEN"),
				}),
			}
			break
		}
	}

	return servers
}

