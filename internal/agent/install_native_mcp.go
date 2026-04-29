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

	// 1) 删旧:Studio 管理的前缀全清,避免环境删了 / 切了配置中心后留死引用
	stripStudioManagedKeys(servers)

	// 2) 写新:用 buildMCPServersForCfg 派生。get 返回空串表示"没值",
	// buildMCPServersForCfg 内部对空值字段直接 omit(不写进 env block),
	// 否则 IDE 启 MCP 时会把 "{{NACOS_ADDR_DEV}}" 这种字面值传给 nacos 进程导致连接失败。
	get := func(k string) string {
		if creds == nil {
			return ""
		}
		return creds[k]
	}
	new := buildMCPServersForCfg(cfg, get)
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
func buildMCPServersForCfg(cfg *config.SystemConfig, get func(string) string) map[string]any {
	servers := map[string]any{}
	envs := cfg.Environments

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
			servers[mcpKey("nacos-mcp-server", cc.ID, e.ID)] = map[string]any{
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
			servers["grafana-mcp-server-"+e.ID] = map[string]any{
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
			servers["loki-mcp-server-"+e.ID] = map[string]any{
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
			servers["lark-openapi"] = map[string]any{
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
			servers["FeishuProjectMcp"] = map[string]any{
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

// stripStudioManagedKeys 把 Studio 管理的固定前缀 / 固定 key 从 servers map 里删掉。
// 用户手加的(自定义前缀)不动,保留它们的现有配置。
func stripStudioManagedKeys(servers map[string]any) {
	prefixes := []string{
		"nacos-mcp-server-", "nacos-mcp-server", // 含 source-id 时形如 nacos-mcp-server-<id>-<env>
		"grafana-mcp-server-",
		"loki-mcp-server-",
	}
	fixedKeys := []string{"lark-openapi", "FeishuProjectMcp"}
	for k := range servers {
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				delete(servers, k)
				break
			}
		}
		for _, f := range fixedKeys {
			if k == f {
				delete(servers, k)
				break
			}
		}
	}
}
