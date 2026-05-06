// install_native_mcp.go —— Claude Code / Cursor / Codex 的 MCP server 自动注入。
//
// 三家配置位置/格式不一样(对应代码踩过的坑):
//   - claude-code → ~/.claude.json(dotfile,顶层 "mcpServers" JSON 字段)。
//                   注意:**不是** ~/.claude/settings.json —— 那个文件给 hooks/permissions/env 用,
//                   Claude Code CLI 不在那里读 mcpServers。早期实现写到 settings.json 看似无报错,
//                   但 `claude mcp list` 永远看不到装入的 server,只能用 ~/.claude.json。
//                   迁移期顺手清掉旧 settings.json 里残留的同名 keys(避免新旧并存)。
//   - cursor      → ~/.cursor/mcp.json,顶层 "mcpServers" JSON 字段
//   - codex       → ~/.codex/agents/<name>.toml 内联 [mcp_servers.<x>] 段(每个 subagent 自带 MCP)。
//                   **不要**走 `codex mcp add` 写到 ~/.codex/config.toml —— 那会让主 chat 启动时
//                   也拉一遍这些 MCP,而排障 MCP 只对 truss-troubleshooter agent 有意义,主 chat
//                   不该被拖累(node 25 + npx 包并发 EPIPE 崩溃风险)。官方文档明确每个 agent 自带:
//                   https://developers.openai.com/codex/subagents
//
// merge 策略:cfg 派生的 server key 先 remove 同名再 add(替换式),用户手加的别名
// (其它前缀)保留不动。codex 走"替换 agent toml 里的 {{MCP_SERVERS}} 占位"路径,
// 整段重写,用户没法手加别名(因为 toml 是 generator 全量生成的);若用户手改 agent toml
// 加自定义段,下次 apply 会被覆盖 —— 跟 wizard "重新生成 = 完整覆盖"语义一致。
package agent

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

// MergeMCPIntoIDESettings 把 cfg 派生的 mcpServers 写进对应 target 的 IDE 配置文件。
//   - target=claude-code → ~/.claude.json(dotfile),顶层 mcpServers 字段
//   - target=cursor      → ~/.cursor/mcp.json,顶层 mcpServers 字段
//   - target=codex       → ~/.codex/agents/<name>.toml 内联 [mcp_servers.<x>] 段
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
	// IDE 走 PruneEmpty=true 模式 —— 避免把 "" 当真值喂给后端进程触发无效连接。
	servers := BuildMCPServers(cfg, MCPBuildOptions{
		AgentID:    cfg.MCPKeyPrefix(),
		PruneEmpty: true,
	}, get)

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("read $HOME: %w", err)
	}
	root := customRoot
	if root == "" {
		root = filepath.Join(home, t.DirName())
	}

	// grafana/loki 共用 mcp-grafana go 二进制:确保 <root>/bin/mcp-grafana 就位 + 把 servers
	// 里的占位替换成绝对路径。三家 IDE 都要做(BuildMCPServers 输出的 command 是占位 sentinel,
	// 不替换的话 settings.json/agent toml 里就直接写 "__GRAFANA_MCP_BIN__",MCP 启动必失败)。
	// 下载失败 → 退化到 npx 兜底(打印警告,不阻塞 install)。
	if hasGrafanaPlaceholder(servers) {
		if binPath, err := EnsureMCPGrafanaBinary(root); err == nil {
			replaceGrafanaWithBinary(servers, binPath)
		} else {
			fmt.Fprintf(os.Stderr,
				"[warn] 自动装 mcp-grafana 二进制失败: %v\n"+
					"%s"+
					"装好后重跑 `tshoot install --target %s` 可一并修复 grafana/loki MCP。\n"+
					"暂时回退到 npx -y @leval/mcp-grafana(已知 stdout 污染风险)。\n",
				err, MCPGrafanaInstallHint(root), target)
			replaceGrafanaWithNpxFallback(servers)
		}
	}

	if t == TargetCodex {
		return injectMCPIntoCodexAgentTOML(root, cfg, servers)
	}

	settingsPath := t.MCPConfigPath(home, customRoot)
	if settingsPath == "" {
		return fmt.Errorf("target %s 没有 MCP 配置文件", target)
	}

	if err := writeMCPServersWithVerify(settingsPath, servers, mcpWriteMaxRetries); err != nil {
		return fmt.Errorf("write %s: %w", settingsPath, err)
	}

	// claude-code 迁移期:老版本误把 mcpServers 写到 ~/.claude/settings.json(那里
	// Claude Code CLI 不读),已经装过老版本的用户那边会有残留 keys 跟新位置并存,
	// 重启 Claude Code 后看不到 MCP 还以为没装上。把 settings.json 里 cfg 派生的
	// 同名 keys 删掉(整个 mcpServers 字段空了顺手把字段也删,保持文件干净)。
	if t == TargetClaudeCode {
		if err := pruneLegacyClaudeSettingsMCP(filepath.Join(root, "settings.json"), servers); err != nil {
			fmt.Fprintf(os.Stderr, "[warn] 清理 ~/.claude/settings.json 老 MCP 残留失败: %v\n", err)
		}
	}
	return nil
}

// mcpWriteMaxRetries 写完 ~/.claude.json 后 verify 失败的最大重试次数。
//
// Why: ~/.claude.json 是 Claude Code CLI 自己也会持续写的状态文件(firstStartTime /
// cachedGrowthBookFeatures / lastReleaseNotesSeen 等运行时状态)。如果 install 在
// read-modify-write 中段被 CLI 自己的写入夹击,可能丢失我们刚写入的 mcpServers 字段
// (lost-update),用户体感是"装好的 MCP 一会儿又不见了"。
//
// writeMCPServersWithVerify 写完立刻 read-back 看派生 keys 是否还在;不在就重新合并
// + 写一次,最多重试本数。3 次足够覆盖偶发并发(CLI 后台写文件是离散事件,一次重试已
// 大概率躲过窗口);仍失败 = 长期被并发覆盖,返回 error 让用户感知去 debug。
//
// 反方向(我们写时盖掉 CLI 刚写的 cache 字段)不在本机制处理范围 —— 那一向丢的是 CLI
// 自己的运行时缓存,CLI 重启会重建,影响小。
const mcpWriteMaxRetries = 3

// writeMCPServersWithVerify 把 servers 替换式合进 path 顶层 mcpServers,写后 read-back
// 校验派生 keys 是否齐全,丢了就重试合并+写。详见 mcpWriteMaxRetries 注释。
//
// 替换式语义:cfg 派生的同名 key 先删后写,用户手加的别名(其它前缀)保留。环境删了
// /切了配置中心后不再生成的旧 key 会留下,需用户手清(可接受 —— 比误删重要 server 强)。
func writeMCPServersWithVerify(path string, servers map[string]any, maxRetries int) error {
	apply := func() error {
		settings, err := readJSONOrEmpty(path)
		if err != nil {
			return err
		}
		existing, _ := settings["mcpServers"].(map[string]any)
		if existing == nil {
			existing = map[string]any{}
		}
		for k := range servers {
			delete(existing, k)
		}
		maps.Copy(existing, servers)
		settings["mcpServers"] = existing
		return writeJSONFile(path, settings, 0o644)
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := apply(); err != nil {
			return err
		}
		// read-back verify:派生 keys 在不在
		check, err := readJSONOrEmpty(path)
		if err != nil {
			return err
		}
		got, _ := check["mcpServers"].(map[string]any)
		complete := true
		for k := range servers {
			if _, ok := got[k]; !ok {
				complete = false
				break
			}
		}
		if complete {
			if attempt > 0 {
				fmt.Fprintf(os.Stderr, "[info] %s 在第 %d 次重试后写入成功(说明遇到了并发写)\n", path, attempt)
			}
			return nil
		}
	}
	return fmt.Errorf("装完 verify %s 仍发现派生 mcpServers 被并发写丢(已重试 %d 次,建议装机器人时不要在主 chat 跑 Claude Code)",
		path, maxRetries)
}

// pruneLegacyClaudeSettingsMCP 把 ~/.claude/settings.json 里 servers map 同名的 keys 删掉。
// 老版本(写错位置那阵)的残留迁移用,保留文件其它字段(hooks/permissions/env)。
// 文件不存在 / 没 mcpServers 字段 / 没命中任何 key → no-op。
func pruneLegacyClaudeSettingsMCP(legacyPath string, servers map[string]any) error {
	if _, err := os.Stat(legacyPath); err != nil {
		return nil
	}
	settings, err := readJSONOrEmpty(legacyPath)
	if err != nil {
		return err
	}
	existing, _ := settings["mcpServers"].(map[string]any)
	if existing == nil {
		return nil
	}
	pruned := 0
	for k := range servers {
		if _, ok := existing[k]; ok {
			delete(existing, k)
			pruned++
		}
	}
	if pruned == 0 {
		return nil
	}
	if len(existing) == 0 {
		delete(settings, "mcpServers")
	} else {
		settings["mcpServers"] = existing
	}
	return writeJSONFile(legacyPath, settings, 0o644)
}

// injectMCPIntoCodexAgentTOML 把 servers 拼成 TOML [mcp_servers.<x>] 段,写到
// <root>/agents/<name>.toml 里 CodexMCPRegionBegin..CodexMCPRegionEnd 之间。
//
// agent name = ResolveID(),跟 GenerateCodex 写 staging toml 时同一来源。
//
// 写法选"region marker 替换"而不是"内存里 parse → re-marshal":
//  1. 不引外部 TOML 库
//  2. agent toml 的 developer_instructions 是 multi-line """ string,parse-marshal 来回容易丢转义
//  3. region marker 明确 idempotent,重装不堆叠;debug 也直观
//
// 用户手改 toml 时只要保留两行 marker 就能继续重装;两行都丢了 install 报错而不是默默
// 拼到末尾(避免无限堆叠出多个 [mcp_servers.*] 段、codex 加载时冲突)。
func injectMCPIntoCodexAgentTOML(root string, cfg *config.SystemConfig, servers map[string]any) error {
	agentName := cfg.ResolveID()
	tomlPath := filepath.Join(root, "agents", agentName+".toml")
	raw, err := os.ReadFile(tomlPath)
	if err != nil {
		return fmt.Errorf("read codex agent toml %s: %w", tomlPath, err)
	}

	patched, err := replaceCodexMCPRegion(string(raw), renderCodexMCPSection(servers))
	if err != nil {
		return fmt.Errorf("patch codex agent toml %s: %w", tomlPath, err)
	}

	if err := os.WriteFile(tomlPath, []byte(patched), 0o644); err != nil {
		return fmt.Errorf("write codex agent toml %s: %w", tomlPath, err)
	}
	return nil
}

// replaceCodexMCPRegion 找 begin..end 两行 marker,把中间(含两行)整体换成
//
//	<begin>
//	<body>
//	<end>
//
// body 由 renderCodexMCPSection 给出,自身不含 marker,可以是空字符串。
//
// marker 找不到就返回错误 —— 而不是悄悄追加,避免用户手改弄丢 marker 后无限堆 [mcp_servers.*]
// 段,codex 加载时报 "duplicate key" 一片红。
func replaceCodexMCPRegion(toml, body string) (string, error) {
	beginIdx := strings.Index(toml, generator.CodexMCPRegionBegin)
	endIdx := strings.Index(toml, generator.CodexMCPRegionEnd)
	if beginIdx < 0 || endIdx < 0 || endIdx < beginIdx {
		return "", fmt.Errorf("MCP region markers (%q .. %q) missing or out of order — refusing to patch (manual cleanup needed)",
			generator.CodexMCPRegionBegin, generator.CodexMCPRegionEnd)
	}
	endLineEnd := endIdx + len(generator.CodexMCPRegionEnd)
	// 把 end marker 那一行的换行也吞掉,避免重复换行
	if endLineEnd < len(toml) && toml[endLineEnd] == '\n' {
		endLineEnd++
	}
	var rebuilt strings.Builder
	rebuilt.WriteString(toml[:beginIdx])
	rebuilt.WriteString(generator.CodexMCPRegionBegin)
	rebuilt.WriteString("\n")
	if body != "" {
		rebuilt.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			rebuilt.WriteString("\n")
		}
	}
	rebuilt.WriteString(generator.CodexMCPRegionEnd)
	rebuilt.WriteString("\n")
	rebuilt.WriteString(toml[endLineEnd:])
	return rebuilt.String(), nil
}

// renderCodexMCPSection 把 servers map 渲染成多个 [mcp_servers.<x>] 段(含可选 [mcp_servers.<x>.env])。
// keys 字典序输出,产物可 diff。空 map 返回空字符串 + 一行注释,避免 toml parse 时 {{MCP_SERVERS}}
// 残留导致整体 fail。
func renderCodexMCPSection(servers map[string]any) string {
	if len(servers) == 0 {
		return "# (本机 install 时无凭证 / cfg 没派生 MCP server,本段为空)"
	}

	keys := make([]string, 0, len(servers))
	for k := range servers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var sb strings.Builder
	for _, name := range keys {
		spec, _ := servers[name].(map[string]any)
		if spec == nil {
			continue
		}
		header := fmt.Sprintf("mcp_servers.%s", name)

		fmt.Fprintf(&sb, "[%s]\n", header)
		// stdio:command + args;HTTP:url
		if cmd, ok := spec["command"].(string); ok && cmd != "" {
			sb.WriteString("command = ")
			sb.WriteString(generator.TomlString(cmd))
			sb.WriteString("\n")
			if rawArgs, ok := spec["args"].([]any); ok && len(rawArgs) > 0 {
				sb.WriteString("args = [")
				for i, a := range rawArgs {
					if i > 0 {
						sb.WriteString(", ")
					}
					if s, ok := a.(string); ok {
						sb.WriteString(generator.TomlString(s))
					}
				}
				sb.WriteString("]\n")
			}
		} else if url, ok := spec["url"].(string); ok && url != "" {
			sb.WriteString("url = ")
			sb.WriteString(generator.TomlString(url))
			sb.WriteString("\n")
		}

		// env table
		if envMap, ok := spec["env"].(map[string]any); ok && len(envMap) > 0 {
			envKeys := make([]string, 0, len(envMap))
			for k := range envMap {
				envKeys = append(envKeys, k)
			}
			sort.Strings(envKeys)
			fmt.Fprintf(&sb, "[%s.env]\n", header)
			for _, k := range envKeys {
				v, _ := envMap[k].(string)
				sb.WriteString(k)
				sb.WriteString(" = ")
				sb.WriteString(generator.TomlString(v))
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// hasGrafanaPlaceholder 判断 servers map 里有没有任何条目用了 grafana 二进制占位
// (用于决定要不要 ensure 二进制下载;cfg 没启用 grafana/loki 时跳过)。
func hasGrafanaPlaceholder(servers map[string]any) bool {
	for _, v := range servers {
		spec, _ := v.(map[string]any)
		if spec != nil {
			if cmd, _ := spec["command"].(string); cmd == generator.CodexPlaceholderGrafanaBin {
				return true
			}
		}
	}
	return false
}

// npxGrafanaFallbackArgs 是 ensure binary 失败时退回 npx 走 @leval/mcp-grafana 包的前置参数。
// 已知该包有 stdout 污染问题,只在用户机器没法装本地二进制时兜底;成功路径不走这里。
var npxGrafanaFallbackArgs = []any{"-y", "@leval/mcp-grafana"}

// replaceGrafanaWithBinary 把 command 占位换成本地 mcp-grafana 二进制绝对路径,args 不动
// (BuildMCPServers 输出的 args 已经是 go 版二进制兼容的形态)。
func replaceGrafanaWithBinary(servers map[string]any, binPath string) {
	for _, spec := range eachGrafanaPlaceholder(servers) {
		spec["command"] = binPath
	}
}

// replaceGrafanaWithNpxFallback 把 command 占位换成 npx,并把 "-y @leval/mcp-grafana"
// 拼到原 args 之前,让原 --disable-* 参数被传给那个 npm 包。
func replaceGrafanaWithNpxFallback(servers map[string]any) {
	for _, spec := range eachGrafanaPlaceholder(servers) {
		spec["command"] = "npx"
		origArgs, _ := spec["args"].([]any)
		spec["args"] = append(append([]any{}, npxGrafanaFallbackArgs...), origArgs...)
	}
}

// eachGrafanaPlaceholder 遍历返回所有 command=grafana 占位的 spec map(原 map 引用,可就地改)。
func eachGrafanaPlaceholder(servers map[string]any) []map[string]any {
	var out []map[string]any
	for _, v := range servers {
		spec, _ := v.(map[string]any)
		if spec == nil {
			continue
		}
		if cmd, _ := spec["command"].(string); cmd == generator.CodexPlaceholderGrafanaBin {
			out = append(out, spec)
		}
	}
	return out
}
