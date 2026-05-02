// install_native_mcp.go —— Claude Code / Cursor / Codex 的 MCP server 自动注入。
//
// 三家配置位置/格式不一样(对应代码踩过的坑):
//   - claude-code → ~/.claude/settings.json,顶层 "mcpServers" JSON 字段
//   - cursor      → ~/.cursor/mcp.json,顶层 "mcpServers" JSON 字段
//   - codex       → ~/.codex/config.toml `[mcp_servers.<name>]`;通过 `codex mcp add/remove`
//                   CLI 注册,**不能**手 marshal TOML(会破坏 [projects.*] 等其它段)。
//
// merge 策略:cfg 派生的 server key 先 remove 同名再 add(替换式),用户手加的别名
// (其它前缀)保留不动。
package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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
	return MergeMCPIntoIDESettingsAt(target, cfg, creds, "")
}

// MergeMCPIntoIDESettingsAt 跟 MergeMCPIntoIDESettings 同,只是允许指定 IDE 安装根目录。
// customRoot 非空时 settings 落到 <customRoot>/<settingsFile>;空时回退默认 ~/.<target>。
func MergeMCPIntoIDESettingsAt(target string, cfg *config.SystemConfig, creds map[string]string, customRoot string) error {
	// creds=nil → BotsPage 重生成 / CLI install 无凭证场景,直接跳过。
	// 走下去会拿空值覆盖初次 wizard 部署时写入的真凭证,把整个连接断掉。
	if creds == nil {
		return nil
	}
	t, err := ParseIDETarget(target)
	if err != nil {
		return err
	}
	get := func(k string) string { return creds[k] }
	// MCP key 前缀用 system.id(短)而不是 ResolveID()(常见 = "<id>-troubleshooter"),
	// 避免 server_key + tool_name 拼起来超过 IDE 60 字符的 tool 名限制。
	servers := buildMCPServersForCfg(cfg, cfg.MCPKeyPrefix(), get)

	if t == TargetCodex {
		return mergeMCPIntoCodexCLI(servers)
	}

	root := customRoot
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("read $HOME: %w", err)
		}
		root = filepath.Join(home, t.DirName())
	}
	settingsPath := filepath.Join(root, t.SettingsFilename())

	settings, err := readJSONOrEmpty(settingsPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", settingsPath, err)
	}
	existing, _ := settings["mcpServers"].(map[string]any)
	if existing == nil {
		existing = map[string]any{}
	}

	// 替换式更新:cfg 派生的同名 key 先删后写,用户手加的别名(其它前缀)保留。
	// 环境删了 / 切了配置中心后不再生成的旧 key 会留下,需用户手清(可接受 —— 比误删重要 server 强)。
	for k := range servers {
		delete(existing, k)
	}
	for k, v := range servers {
		existing[k] = v
	}
	settings["mcpServers"] = existing

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(settingsPath), err)
	}
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(settingsPath, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", settingsPath, err)
	}
	return nil
}

// mergeMCPIntoCodexCLI 通过 `codex mcp remove` + `codex mcp add` 注册 servers 到
// codex 的 ~/.codex/config.toml。让 codex 自己管理 TOML 格式,避免手 marshal 把
// [projects.*] / [marketplaces.*] / 用户手加的 [mcp_servers.<别名>] 段搞坏。
//
// 行为:对每个 server 名先 remove(忽略 "not found" 错误)再 add,实现"替换式更新"。
// codex CLI 必须在 PATH 里 —— wizard 部署到 codex 之前会过 aitools.DetectCodex,
// 没装 codex 就不让选这个 target;走到这里仍找不到 binary 也直接报错给用户。
func mergeMCPIntoCodexCLI(servers map[string]any) error {
	codexBin, err := exec.LookPath("codex")
	if err != nil {
		return fmt.Errorf("找不到 codex CLI(PATH 里没 'codex'),无法注册 MCP 到 codex 的 config.toml;请先安装 codex(brew install codex)再重试: %w", err)
	}
	// 排序后注册,日志稳定可读(codex 会把同名先后写入 toml,顺序无业务影响)
	keys := make([]string, 0, len(servers))
	for k := range servers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, name := range keys {
		spec, _ := servers[name].(map[string]any)
		if spec == nil {
			continue
		}
		// 先 remove 同名(注册过的会成功,没注册的报 "not found",非致命)
		_ = exec.Command(codexBin, "mcp", "remove", name).Run()

		args := []string{"mcp", "add", name}
		// env vars 走 --env KEY=VALUE,可重复
		if envMap, ok := spec["env"].(map[string]any); ok {
			envKeys := make([]string, 0, len(envMap))
			for k := range envMap {
				envKeys = append(envKeys, k)
			}
			sort.Strings(envKeys)
			for _, k := range envKeys {
				v, _ := envMap[k].(string)
				args = append(args, "--env", fmt.Sprintf("%s=%s", k, v))
			}
		}
		// stdio:--  COMMAND ARGS...    HTTP:--url URL
		if url, ok := spec["url"].(string); ok && url != "" {
			args = append(args, "--url", url)
		} else if cmd, ok := spec["command"].(string); ok && cmd != "" {
			args = append(args, "--", cmd)
			if rawArgs, ok := spec["args"].([]any); ok {
				for _, a := range rawArgs {
					if s, ok := a.(string); ok {
						args = append(args, s)
					}
				}
			}
		} else {
			// 既没 command 又没 url,跳过(理论上不会出现,buildMCPServersForCfg 都填了 command)
			continue
		}
		out, runErr := exec.Command(codexBin, args...).CombinedOutput()
		if runErr != nil {
			return fmt.Errorf("codex mcp add %s 失败: %w\n%s", name, runErr, string(out))
		}
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
			servers[keyFor("nacos", cc.ID, e.ID)] = map[string]any{
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
			servers[keyFor("grafana", "", e.ID)] = map[string]any{
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
			servers[keyFor("loki", "", e.ID)] = map[string]any{
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

