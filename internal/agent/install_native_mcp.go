// install_native_mcp.go —— Claude Code / Cursor / Codex 的 MCP server 自动注入。
//
// 三家配置位置/格式不一样(对应代码踩过的坑):
//   - claude-code → ~/.claude.json(dotfile,顶层 "mcpServers" JSON 字段)。
//     注意:**不是** ~/.claude/settings.json —— 那个文件给 hooks/permissions/env 用,
//     Claude Code CLI 不在那里读 mcpServers。早期实现写到 settings.json 看似无报错,
//     但 `claude mcp list` 永远看不到装入的 server,只能用 ~/.claude.json。
//     迁移期顺手清掉旧 settings.json 里残留的同名 keys(避免新旧并存)。
//   - cursor      → ~/.cursor/mcp.json,顶层 "mcpServers" JSON 字段
//   - codex       → ~/.codex/agents/<name>.toml 内联 [mcp_servers.<x>] 段(交互式 subagent)，
//     并同步 ~/.codex/tshoot-<name>.config.toml(Studio 后台 codex exec profile)。
//     **不要**走 `codex mcp add` 写到 ~/.codex/config.toml —— 那会让主 chat 启动时
//     也拉一遍这些 MCP,而排障 MCP 只对 truss-troubleshooter agent 有意义,主 chat
//     不该被拖累(node 25 + npx 包并发 EPIPE 崩溃风险)。官方文档明确每个 agent 自带:
//     https://developers.openai.com/codex/subagents
//
// merge 策略:cfg 派生的 server key 先 remove 同名再 add(替换式),用户手加的别名
// (其它前缀)保留不动。codex 走"替换 agent toml 里的 {{MCP_SERVERS}} 占位"路径,
// 整段重写,用户没法手加别名(因为 toml 是 generator 全量生成的);若用户手改 agent toml
// 加自定义段,下次 apply 会被覆盖 —— 跟 wizard "重新生成 = 完整覆盖"语义一致。
package agent

import (
	"errors"
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
//   - target=codex       → ~/.codex/agents/<name>.toml + ~/.codex/tshoot-<name>.config.toml
//
// creds 是 env-var-name → value 的 map(跟 InstallNativeOpenclaw 一样的 schema)。
// 桌面端 wizard 通过 buildOpenclawCreds() 拼出来传过来;CLI 没 creds 时传 nil,
// 注入的 env 字段值会变成 {{ENV_VAR}} 占位符让用户手填。
//
// onProgress(可空)用于 install 链路里"用户感知"的进度回调。当前 install 步骤本身
// 都已是常数时间,onProgress 主要给 wails event "install:log"——保留参数避免破坏调用方
// (apply/desktop binding 链路),但不再有耗时操作要回调。
func MergeMCPIntoIDESettings(target string, cfg *config.SystemConfig, creds map[string]string, onProgress func(string)) error {
	// creds==nil 不再整体跳过 — 数据层 mcp(elasticsearch/mongodb/redis/...)走 yaml endpoints[]
	// fallback,即便 install creds 没有 ES_URL_<env> 等也能从 cfg.Infrastructure.DataStores[].Endpoints[]
	// 派生连接串。挡住会让首次注册的数据层 mcp 永远写不进去(踩过这个坑:CLI install 不传 -env-file
	// + claude-code staging 不带 .env → creds==nil → 跳过 → ~/.claude.json 全是 0 个数据层 mcp)。
	//
	// 但要保护"老 wizard 凭证不被空值覆盖":creds 为空时改走 mergeOnlyNew 模式,只新增 existing
	// 里没有的派生 key,不动有 env 段的老条目(grafana/loki/nacos 等首次部署时 wizard 灌过真凭证)。
	//
	// 用 len(creds) == 0 而不是 creds == nil — 桌面 app bindings_deploy 走非 BestEffort 路径
	// 时可能传 map[string]string{}(空 map ≠ nil),用 nil 检测会让空 map 走替换式覆盖,
	// 把首次部署灌入的真凭证抹掉。len() 兼顾两种零状态。
	mergeOnlyNew := len(creds) == 0
	t, err := ParseIDETarget(target)
	if err != nil {
		return err
	}
	get := func(k string) string {
		if creds == nil {
			return ""
		}
		return creds[k]
	}
	// MCP key 前缀用 system.id(短)而不是 ResolveID()(常见 = "<id>-troubleshooter"),
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("read $HOME: %w", err)
	}
	root := t.RootDir(home)

	// emit 把 install 进度桥到调用方的 onProgress(桌面 app 走 wails event "install:log"
	// 实时推前端日志面板)。onProgress 为空时降级到 stderr,保持 CLI 行为不变。
	//
	// 关键场景:EnsureKafkaMCPInstalled 首次从 GitHub Release 拉 tarball 国内 30-60s,
	// 之前 onLog 写死 stderr,前端 UI 看着"部署中..."不动、日志面板也没新内容,体感死锁;
	// 走 emit 之后 "[info] 拉 tarball: ..." 这类进度行能实时刷到用户眼前。
	emit := func(line string) {
		if onProgress != nil {
			onProgress(line)
			return
		}
		fmt.Fprintln(os.Stderr, line)
	}

	// 清老版本下载到 <root>/bin/ 的 mcp-grafana 孤儿二进制(本会话改 npx mcp-grafana-npx 后
	// 不再用)。re-install 顺手清,不用等 uninstall — 几十 MiB 留在那纯占盘。
	removeLegacyGrafanaBin(root)

	// nacos / jaeger / clickhouse 三家走 uvx 启动,缺 uv 整批挂 — 装机前探一下,缺失打提示。
	// 不阻塞:其它 MCP 还能用,完全 abort 装机损失更大。
	if CfgUsesUvx(cfg) {
		if err := CheckUvxAvailable(); err != nil {
			emit(fmt.Sprintf("[warn] install --target %s: %v", target, err))
		}
	}

	// kafka 走 binary 启动(tuannvm/kafka-mcp-server)。PATH 没有就自动从 GitHub Release 拉
	// tarball 解到 ~/.tshoot/bin/。下载失败不阻塞,warn 给手动指引;buildKafka 仍写 PATH 形式,
	// 用户事后手动装到 PATH 也能直接生效不需要重跑 install。
	kafkaBinPath := ""
	if CfgUsesKafkaMCP(cfg) {
		var err error
		kafkaBinPath, err = EnsureKafkaMCPInstalled(emit)
		if err != nil {
			emit(fmt.Sprintf("[warn] install --target %s: %v", target, err))
		}
	}

	// nacos 走自研本地 MCP 脚本,extract 内嵌 nacos_mcp.py 到 ~/.tshoot/scripts/ 拿绝对路径。
	// 失败不阻塞:buildNacos 拿到空路径会跳过注册,nacos 回落 config-executor SKILL 的 HTTP fallback。
	nacosScriptPath := ""
	if CfgUsesNacosMCP(cfg) {
		var err error
		nacosScriptPath, err = EnsureNacosMCPScript(emit)
		if err != nil {
			emit(fmt.Sprintf("[warn] install --target %s: %v", target, err))
		}
	}

	codeGraphBinPath := ""
	if CfgUsesCodeGraph(cfg) {
		var err error
		codeGraphBinPath, err = EnsureCodeGraphInstalled(emit)
		if err != nil {
			emit(fmt.Sprintf("[warn] CodeGraph 安装失败,跳过 MCP 注册并启用 rg/read fallback: %v", err))
		}
	}

	// 避免 server_key + tool_name 拼起来超过 IDE 60 字符的 tool 名限制。
	// IDE 走 PruneEmpty=true 模式 —— 避免把 "" 当真值喂给后端进程触发无效连接。
	servers := BuildMCPServers(cfg, MCPBuildOptions{
		AgentID:             cfg.MCPKeyPrefix(),
		PruneEmpty:          true,
		KafkaMCPBinaryPath:  kafkaBinPath,
		NacosMCPScriptPath:  nacosScriptPath,
		CodeGraphBinaryPath: codeGraphBinPath,
	}, get)

	if t == TargetCodex {
		// codex 全局 sandbox 默认禁网,workspace-write 也要显式 network_access=true 才放行 —
		// 没配的话装好后所有 MCP 启动 ENOTFOUND。自动 patch ~/.codex/config.toml,
		// backup 原文件后再写;patch 失败降级到 [warn] + 手抄指引(EnsureCodexNetworkAccess 注释)。
		if changed, err := EnsureCodexNetworkAccess(root); err != nil {
			emit(fmt.Sprintf("[warn] codex 自动开启 network_access 失败: %v", err))
		} else if changed {
			emit(fmt.Sprintf("[ok] 已自动写入 %s/config.toml: [sandbox_workspace_write] network_access = true(MCP 才能连业务侧;原文件已 backup)", root))
		}
		return injectMCPIntoCodexAgentTOML(root, cfg, servers)
	}

	settingsPath := t.MCPConfigPath(home)
	if settingsPath == "" {
		return fmt.Errorf("target %s 没有 MCP 配置文件", target)
	}

	if err := writeMCPServersWithVerify(settingsPath, servers, mcpWriteMaxRetries, mergeOnlyNew, cfg.MCPKeyPrefix()+"-"); err != nil {
		return fmt.Errorf("write %s: %w", settingsPath, err)
	}

	// claude-code 迁移期:老版本误把 mcpServers 写到 ~/.claude/settings.json(那里
	// Claude Code CLI 不读),已经装过老版本的用户那边会有残留 keys 跟新位置并存,
	// 重启 Claude Code 后看不到 MCP 还以为没装上。把 settings.json 里 cfg 派生的
	// 同名 keys 删掉(整个 mcpServers 字段空了顺手把字段也删,保持文件干净)。
	if t == TargetClaudeCode {
		if err := pruneLegacyClaudeSettingsMCP(filepath.Join(root, "settings.json"), servers); err != nil {
			emit(fmt.Sprintf("[warn] 清理 ~/.claude/settings.json 老 MCP 残留失败: %v", err))
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

// writeMCPServersWithVerify 把 servers 合进 path 顶层 mcpServers,写后 read-back
// 校验派生 keys 是否齐全,丢了就重试合并+写。详见 mcpWriteMaxRetries 注释。
//
// 两种合并模式:
//   - mergeOnlyNew=false(默认,有 creds 重灌):cfg 派生的同名 key 覆盖,且按 agentPrefix
//     清掉"前缀属于本系统但本次不再生成"的死 key(env 缩容 / 数据层删了 / multi-source nacos
//     删 source / system.id 改名 等场景留下的死引用)。每条删的会打 [info] 让用户感知。
//     用户手加同前缀的别名会被一起清 — 不常见,且 [info] log 兜底,比死 key 永远留更干净。
//   - mergeOnlyNew=true(无 creds 兜底):existing 已有的凭证型派生 key **不动**(env 段保持
//     首次部署灌入的真凭证),只 add existing 没有的(数据层 mcp 首次注册场景)。CodeGraph
//     不含用户凭证,其固定 key 例外地按本次 ensure 结果覆盖或删除,避免 fallback 时残留死引用。
//     除这个固定 key 外不做 prefix 清理 — 没 creds 时拿不到完整意图,清了可能误删在线 mcp。
//
// agentPrefix 通常是 `<system.id>-`(BuildMCPServers 的 AgentID + "-"),空串关闭 prefix 清理。
func writeMCPServersWithVerify(path string, servers map[string]any, maxRetries int, mergeOnlyNew bool, agentPrefix string) error {
	apply := func() error {
		settings, err := readJSONOrEmpty(path)
		if err != nil {
			return err
		}
		existing, _ := settings["mcpServers"].(map[string]any)
		if existing == nil {
			existing = map[string]any{}
		}
		if mergeOnlyNew {
			reconcileCodeGraphServer(existing, servers, strings.TrimSuffix(agentPrefix, "-"))
			// 除无凭证的 CodeGraph 固定 key 外,只 add 不删/不覆盖 — 保护老条目的 env 真凭证。
			for k, v := range servers {
				if _, hit := existing[k]; !hit {
					existing[k] = v
				}
			}
		} else {
			// 重灌模式:同名覆盖 + agentPrefix 死 key 清理
			for k := range servers {
				delete(existing, k)
			}
			if agentPrefix != "" {
				for k := range existing {
					if !strings.HasPrefix(k, agentPrefix) {
						continue
					}
					if _, want := servers[k]; want {
						continue // 还在生成的 key 不动,即使前缀匹配
					}
					delete(existing, k)
					fmt.Fprintf(os.Stderr, "[info] 清掉死引用 mcpServers.%s(本次 cfg 不再生成此 mcp)\n", k)
				}
			}
			maps.Copy(existing, servers)
		}
		settings["mcpServers"] = existing
		// 0o600:mcpServers env 段含 wizard 注入的 plaintext creds(NACOS_PASS / API token /
		// kubeconfig 等),world-readable 0o644 是真 leak —— 多用户 macOS / Linux 主机
		// 上其它账号能直接 cat 出来。settings.json 自身没机密,但跟凭证混存只能按下限走。
		return writeJSONFile(path, settings, 0o600)
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

// removeLegacyGrafanaBin 清掉早期版本下载到 <root>/bin/mcp-grafana[.exe] 的孤儿二进制。
// 改走 npx mcp-grafana-npx 后,这文件留着也没人用(每个 IDE root ~30 MiB)。
// install / uninstall 都该跑一次确保收尸,文件不存在 / 没权限删 / 任何错误都吞掉(只是清理优化,不该阻断主流程)。
func removeLegacyGrafanaBin(root string) {
	for _, name := range []string{"mcp-grafana", "mcp-grafana.exe"} {
		legacy := filepath.Join(root, "bin", name)
		if _, err := os.Stat(legacy); err == nil {
			if rmErr := os.Remove(legacy); rmErr == nil {
				fmt.Fprintf(os.Stderr, "[info] 清掉老 %s 孤儿二进制(已改走 npx mcp-grafana-npx)\n", legacy)
			}
		}
	}
	// 空目录顺带清(已有 grafana 二进制时 bin/ 里只这一个文件)
	_ = os.Remove(filepath.Join(root, "bin"))
}

// pruneLegacyClaudeSettingsMCP 把 ~/.claude/settings.json 里 servers map 同名的 keys 删掉。
// 老版本(写错位置那阵)的残留迁移用,保留文件其它字段(hooks/permissions/env)。
// 文件不存在 / 没 mcpServers 字段 / 没命中任何 key → no-op。
func pruneLegacyClaudeSettingsMCP(legacyPath string, servers map[string]any) error {
	if _, err := os.Stat(legacyPath); err != nil {
		return nil //nolint:nilerr // 文件不存在就 no-op
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
	// 0o600:同 writeMCPServersWithVerify 注释,mcpServers env 段含 plaintext creds。
	return writeJSONFile(legacyPath, settings, 0o600)
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
	body := renderCodexMCPSection(servers)
	for _, agentName := range codexAgentNamesForConfig(cfg) {
		tomlPath := filepath.Join(root, "agents", agentName+".toml")
		raw, err := os.ReadFile(tomlPath)
		if err != nil {
			if os.IsNotExist(err) && agentName != cfg.ResolveID() {
				continue
			}
			return fmt.Errorf("read codex agent toml %s: %w", tomlPath, err)
		}

		patched, err := replaceCodexMCPRegion(string(raw), body)
		if err != nil {
			return fmt.Errorf("patch codex agent toml %s: %w", tomlPath, err)
		}

		// 0o600:codex agent toml 的 [mcp_servers.*.env] 段含 plaintext creds(同 ~/.claude.json
		// / ~/.cursor/mcp.json),不能 world-readable。
		// 注意:os.WriteFile 在文件**已存在**时不改 mode,而本函数总是 patch 已经存在的 toml,
		// 所以必须 chmod 显式收 mode 否则继承先前 install_native.go 第一次写时的 0o644。
		if err := os.WriteFile(tomlPath, []byte(patched), 0o600); err != nil {
			return fmt.Errorf("write codex agent toml %s: %w", tomlPath, err)
		}
		if err := os.Chmod(tomlPath, 0o600); err != nil {
			return fmt.Errorf("chmod codex agent toml %s: %w", tomlPath, err)
		}
		if err := writeCodexAgentRuntimeProfile(root, agentName, body); err != nil {
			return err
		}
	}
	return nil
}

// codexAgentRuntimeProfileName is passed to `codex exec --profile` by Studio's
// durable phase runner. Codex agent TOML is loaded when another Codex session
// spawns a subagent, but a direct background `codex exec` does not consume it.
// Keeping a narrow profile with the same MCP section makes both entrypoints use
// the installed runtime capabilities without polluting the global main chat.
func codexAgentRuntimeProfileName(agentName string) string {
	return "tshoot-" + strings.TrimSpace(agentName)
}

func writeCodexAgentRuntimeProfile(root, agentName, body string) error {
	profileName := codexAgentRuntimeProfileName(agentName)
	if profileName == "tshoot-" {
		return errors.New("codex runtime profile requires an agent name")
	}
	path := filepath.Join(root, profileName+".config.toml")
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refuse to replace symlinked codex runtime profile %s", path)
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect codex runtime profile %s: %w", path, err)
	}
	content := "# Managed by tshoot. Used by Studio background codex exec.\n" + body
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write codex runtime profile %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod codex runtime profile %s: %w", path, err)
	}
	return nil
}

func codexAgentNamesForConfig(cfg *config.SystemConfig) []string {
	troubleshooter := cfg.ResolveID()
	base := strings.TrimSpace(cfg.System.ID)
	if base == "" {
		base = strings.TrimSuffix(troubleshooter, "-troubleshooter")
	}
	validator := base + "-validator"
	fixer := base + "-fixer"
	names := []string{troubleshooter}
	for _, candidate := range []string{validator, fixer} {
		if strings.TrimSpace(candidate) == "" || candidate == troubleshooter {
			continue
		}
		names = append(names, candidate)
	}
	return names
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
			// HTTP MCP:type(如 "streamable-http")+ headers
			if typ, ok := spec["type"].(string); ok && typ != "" {
				sb.WriteString("type = ")
				sb.WriteString(generator.TomlString(typ))
				sb.WriteString("\n")
			}
			if hdrs, ok := spec["headers"].(map[string]string); ok && len(hdrs) > 0 {
				hdrKeys := make([]string, 0, len(hdrs))
				for k := range hdrs {
					hdrKeys = append(hdrKeys, k)
				}
				sort.Strings(hdrKeys)
				fmt.Fprintf(&sb, "[%s.headers]\n", header)
				for _, k := range hdrKeys {
					sb.WriteString(k)
					sb.WriteString(" = ")
					sb.WriteString(generator.TomlString(hdrs[k]))
					sb.WriteString("\n")
				}
			}
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
