package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// GenerateCodex 输出 OpenAI Codex CLI subagent 装机 staging:
//   - agents/<name>.toml   (codex subagent 定义,TOML 格式;放到 ~/.codex/agents/<name>.toml 后
//     主 chat 通过自然语言"spawn the <name> agent ..."调用)
//   - skills/<name>/SKILL.md     (顶层调度入口 SKILL.md,让 codex 的 /skills picker 显单条)
//   - skills/<name>/<sub>/SKILL.md (11 个子能力 SKILL.md,主 agent toml 显式 [[skills.config]] 引用)
//   - scripts/                    (辅助 python 脚本)
//
// 装载位置:
//
//	`~/.codex/agents/<name>.toml`(subagent 注册;codex 启动时扫此目录)
//	`~/.codex/skills/<name>/...`(skill 集合;agent toml 里 path 字段用绝对路径引用)
//	`~/.codex/scripts/<name>/...`(辅助脚本)
//
// 这是"中间包"产物,装到 ~/.codex/ 由 InstallNative() 完成 —— install 时:
//  1. 把 staging agents/<name>.toml 里 SKILLS_ROOT 占位替换成实际安装根的绝对路径
//  2. 把 cfg 派生的 [mcp_servers.<x>] 段拼接到 toml 末尾(creds 由 install 时 IDECreds 注入)
//  3. 写到 <root>/agents/<name>.toml
//
// 跟 Claude Code/Cursor 不同点:
//   - codex agent 文件是 TOML 不是 markdown(官方文档 https://developers.openai.com/codex/subagents)
//   - codex agent 通过自然语言 spawn,不是 @<name>
//   - MCP 嵌入 agent toml 内 [mcp_servers.*] 段(每个 agent 自带,不污染主 chat)
func (g *Generator) GenerateCodex() error {
	outDir := g.OutputDir + "-codex"
	if err := os.RemoveAll(outDir); err != nil {
		return fmt.Errorf("clean output: %w", err)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create output: %w", err)
	}

	wsRoot, cleanup, err := g.resolveWorkspace()
	if err != nil {
		return err
	}
	defer cleanup()

	troubleshooterAgentName := agentIDForRole(g.Ctx, AgentRoleTroubleshooter)

	// 1) skills/(11 个子能力)
	skillsSrc := filepath.Join(wsRoot, "skills")
	skillsDst := filepath.Join(outDir, "skills")
	if err := copyDirRecursive(skillsSrc, skillsDst); err != nil {
		return fmt.Errorf("copy skills: %w", err)
	}

	// 2) skills/SKILL.md 顶层排障入口(装机时按 agent 名命名空间隔离)。validator agent
	//    不走这个入口,它在 TOML developer_instructions 里直接 Read bug-verifier。
	rootSkill, err := buildCodexRootSkillMD(wsRoot, g.Ctx, troubleshooterAgentName)
	if err != nil {
		return fmt.Errorf("build root SKILL.md: %w", err)
	}
	rootSkillDir := filepath.Join(outDir, "skills")
	if err := os.MkdirAll(rootSkillDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(rootSkillDir, "SKILL.md"), []byte(rootSkill), 0o644); err != nil {
		return err
	}

	// 3) scripts/ (config-executor 等辅助脚本)
	scriptsSrc := filepath.Join(wsRoot, "skills", "config-executor", "scripts")
	if _, err := os.Stat(scriptsSrc); err == nil {
		if err := copyDirRecursive(scriptsSrc, filepath.Join(outDir, "scripts")); err != nil {
			return fmt.Errorf("copy scripts: %w", err)
		}
	}

	// 4) agents/<name>.toml 主体(name/description/developer_instructions/skills.config)
	//    [mcp_servers.*] 段在 install 时按 cfg 拼接;skills.config.path 用 SKILLS_ROOT 占位
	//    install 时替换成绝对路径(codex 不解析 ~ / 环境变量)。
	agentsDir := filepath.Join(outDir, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		return err
	}
	for _, role := range internalAgentRoles() {
		agentName := agentIDForRole(g.Ctx, role)
		agentTOML, err := buildCodexAgentTOML(wsRoot, g.Ctx, agentName, role)
		if err != nil {
			return fmt.Errorf("build %s agent toml: %w", role, err)
		}
		if err := os.WriteFile(filepath.Join(agentsDir, agentName+".toml"), []byte(agentTOML), 0o644); err != nil {
			return err
		}
	}

	if err := g.writeIDEAgentMetas(outDir, "codex"); err != nil {
		return fmt.Errorf("write tshoot meta: %w", err)
	}
	return nil
}

// codexNicknameSafe 限制 nickname_candidates 字符集。
//
// 官方文档说允许 [A-Za-z0-9 _-],但实测 codex (gpt-5.3-codex) 加载时含空格 / 连字符
// 直接报 "Ignoring malformed agent role definition ... nickname_candidates may only
// contain ASCII letters, digits, spaces, hyphens, and underscores" —— 文档与实现不一致,
// 我们按更严的实现走:**纯字母数字**,确保所有 codex 版本都吃。
var codexNicknameSafe = regexp.MustCompile(`^[A-Za-z0-9]+$`)

// buildCodexAgentTOML 生成 agents/<name>.toml 主体。
//
// staging 阶段把 skill path 用 CodexPlaceholderSkillsRoot + "/<sub>/SKILL.md" 占位,InstallNative 时
// 替换成实际绝对路径(codex 要求 [[skills.config]].path 是绝对路径,不解析 ~ 或 $HOME)。
//
// [mcp_servers.*] 段不在 staging 写,InstallNative 时按 cfg + IDECreds 现拼接 —— 因为
// MCP env 含真凭证(GRAFANA_PASSWORD 等),凭证只在 install 阶段才有(staging 是无凭证版)。
func buildCodexAgentTOML(wsRoot string, ctx *Context, agentName string, role AgentRole) (string, error) {
	var sb strings.Builder

	// frontmatter 字段(顶层)
	sb.WriteString("# OpenAI Codex CLI subagent — https://developers.openai.com/codex/subagents\n\n")

	TomlWriteString(&sb, "name", agentName)

	// 子 skill 列表先读,后面 description 计数和 [[skills.config]] 枚举都复用。
	// 读不到必须立刻报错 —— 之前这里吞掉错误 + 下面再做"if nil 重试一次"假兜底,
	// IO 失败时静默生成 description="路由到 0 个子 skill" + 空 skills.config 段,
	// codex 还能 spawn agent 但起来啥也调不了。
	allSubSkills, err := listCodexSubSkills(wsRoot)
	if err != nil {
		return "", fmt.Errorf("list sub skills: %w", err)
	}
	subSkills := codexSubSkillsForRole(allSubSkills, role)

	// description:codex 用它判断"用户意图是否要 spawn 这个 agent",写**短而精准**(关键词触发)。
	// 子 skill 数量 + 配置中心类型从 ctx 派生,避免硬编码漂移。
	desc := buildCodexAgentDescription(ctx, len(subSkills), role)
	TomlWriteString(&sb, "description", desc)

	// nickname_candidates:codex 给 spawn 出的 thread instance 起的可读 display name。
	// 严格 [A-Za-z0-9]+(实测限制),用 PascalCase 单元素就够 —— 多元素只会让 codex 注册多个
	// 候选 instance 名,不是别名。
	nick := codexNicknameFromAgentID(agentName)
	if !codexNicknameSafe.MatchString(nick) {
		return "", fmt.Errorf("derived nickname %q failed ASCII check (agent name %q)", nick, agentName)
	}
	fmt.Fprintf(&sb, "nickname_candidates = [%s]\n", TomlString(nick))

	// model 仅在用户显式给 codex 配 target_models.codex 时写出
	if m := strings.TrimSpace(ctx.Agent.TargetModels["codex"]); m != "" {
		TomlWriteString(&sb, "model", m)
	}
	TomlWriteString(&sb, "model_reasoning_effort", "high")
	// sandbox_mode = workspace-write 是必要选择 —— codex 的 read-only 模式**同时禁止网络访问**
	// (含 MCP server 子进程),而本项目所有 MCP(grafana/nacos/jaeger/mongo/...)都需要外网到
	// 业务侧。用 read-only 会导致 codex 平台所有 MCP 静默 ENOTFOUND,排障机器人完全跑不起来。
	//
	// 数据安全靠 MCP 层自己保:mongo/postgres/redis/mysql 都已传 --read-only 或等价 RO env,
	// grafana/loki 走 --disable-* 屏蔽 admin/alerting/incident 等写操作,ES MCP 包默认 RO。
	// workspace-write 只多给 agent 写工作区 fs 的能力(日志 dump / 临时文件常用),不能动系统。
	//
	// 仍需用户在 ~/.codex/config.toml 全局加 `[sandbox_workspace_write]\nnetwork_access = true`
	// (codex 默认即使 workspace-write 也禁网)— install 时会打提示。
	TomlWriteString(&sb, "sandbox_mode", "workspace-write")

	// developer_instructions:三引号 multi-line,人格 + 路由 + 故障快报模板全装进去
	instr := buildCodexDeveloperInstructions(wsRoot, ctx, agentName, role)
	sb.WriteString("developer_instructions = \"\"\"\n")
	sb.WriteString(instr)
	if !strings.HasSuffix(instr, "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("\"\"\"\n")

	// skills.config:只引子 skill。顶层 <root>/skills/<name>/SKILL.md 被 codex /skills picker
	// 自动当主入口列出,不需要 [[skills.config]] 显式注册(冗余会占 description budget)。
	// path 用 CodexPlaceholderSkillsRoot 占位,install 时替换为绝对路径。
	sb.WriteString("\n# ── Skills(子能力;path 由 install 时替换为绝对路径)──\n")
	for _, s := range subSkills {
		writeSkillEntry(&sb, CodexPlaceholderSkillsRoot+"/"+s+"/SKILL.md")
	}

	// [mcp_servers.*] 段:install 时按 cfg + IDECreds 在 region marker 之间拼接。
	// staging 写空 region,install 替换 begin..end 整体(idempotent,重装不堆叠)。
	sb.WriteString("\n# ── MCP servers ──\n")
	sb.WriteString(CodexMCPRegionBegin + "\n")
	sb.WriteString(CodexMCPRegionEnd + "\n")

	return sb.String(), nil
}

// codexNicknameFromAgentID 把 agent 名(可能含连字符,如 "truss-troubleshooter")
// 转 PascalCase 纯字母数字(codex 实测限制)。例:"truss-troubleshooter" → "TrussTroubleshooter"。
func codexNicknameFromAgentID(agentID string) string {
	parts := regexp.MustCompile(`[^A-Za-z0-9]+`).Split(agentID, -1)
	var sb strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		sb.WriteString(strings.ToUpper(p[:1]))
		if len(p) > 1 {
			sb.WriteString(p[1:])
		}
	}
	out := sb.String()
	if out == "" {
		out = "Agent"
	}
	return out
}

// buildCodexAgentDescription 给 codex 主 chat "判断要不要 spawn 这个 agent"用的简介。
//
// 故意**只写触发词清单**,不写任何定义性长句 —— codex 主 chat 用关键词权重匹配,长句反而
// 稀释关键词密度。详细能力在 root SKILL.md / developer_instructions 里展开,description
// 只负责"用户一句话里有没有触发词"这一道判定。<80 字基线。
//
// 第二参数 _ 兼容旧签名(skillCount 之前用于"路由到 N 个子 skill",已挪到 root SKILL.md)。
func buildCodexAgentDescription(ctx *Context, _ int, role AgentRole) string {
	return projectBoundAgentDescription(ctx, agentIDForRole(ctx, role), role)
}

// buildCodexDeveloperInstructions 写最小路由提示,而不是把 SOUL/IDENTITY/输出形态/
// 故障快报模板/Skills 索引全塞进来。
//
// 历史教训:之前复用 writeIDEAgentBody(给 Claude/Cursor 拼整段提示词的渲染器),产物 ~5KB
// 全进 codex spawn 时的 system prompt,每次推理都要吃这部分 token。现在按 codex 推荐写法
// 收敛成"身份一句话 + 触发→Read 路由"两行,详细文档在 root `skills/SKILL.md` 里 picker
// 按需 read —— 主 chat 不付出常驻成本,正在排障的 thread 才 read。
//
// 注意:codex spawn 时,root `skills/<agentName>/SKILL.md` 会作为 picker 入口被自动列出,
// 所以这里只要明确"先 Read 入口"即可,**不再**列子 skill 清单(那已经在 root SKILL.md 里)。
//
// agentName 用来拼绝对路径,wsRoot/ctx 暂不用但保留签名以便后续按 ctx 动态调整(如不同
// 系统给不同的入口 skill 名)。
func buildCodexDeveloperInstructions(_ string, ctx *Context, agentName string, role AgentRole) string {
	gate := codexProjectOwnershipGate(ctx, agentName)
	if role == AgentRoleValidator {
		return gate + fmt.Sprintf(`你是 **%s 验证机器人**,从 codex 主 chat spawn 出来做 **验证 / 主动复现 / 修复后复查**,只输出验证报告,不做原因定位。

第一步:Read `+"`~/.codex/skills/%s/bug-verifier/SKILL.md`"+` —— 那里有复现、回归、证据收集、状态枚举和验证报告结构。信息不足时列阻塞项,不要猜测。

边界:不读取业务源码定位函数/文件行号/补丁点;只收集可复查证据和交接摘要,代码分析与原因判断交给排障 Agent。

`, ctx.System.Name, agentName)
	}
	if role == AgentRoleFixer {
		return gate + fmt.Sprintf(`你是 **%s 修复机器人**,从 codex 主 chat spawn 出来做 Bug 修复落地。只有用户明确要求修复时才执行。

第一步:Read `+"`~/.codex/skills/%s/bug-fixer/SKILL.md`"+` —— 那里有分支确认、脏工作区保护、最小改动、测试、提交、推送和部署通知契约。

边界:不要扩大重构;不要改无关文件;如果工作区已有用户未提交改动、无法确认分支、无法推送,立刻停止并说明阻塞。完成后只通知用户部署修复分支,不要自行部署。

`, ctx.System.Name, agentName)
	}
	return gate + fmt.Sprintf(`你是 **%s 排障机器人**,从 codex 主 chat spawn 出来做 **只读** 排障(日志 / 指标 / trace / 配置 / 代码),**不**直接落地修改。

第一步:Read `+"`~/.codex/skills/%s/SKILL.md`"+` —— 那里有完整的路由表 / 行为规则 / 输出模板,按形态指引你 Read 子 SKILL.md 走 7 步流程(含 Step 7 沉淀)。

`, ctx.System.Name, agentName)
}

func codexSubSkillsForRole(subSkills []string, role AgentRole) []string {
	filtered := make([]string, 0, len(subSkills))
	for _, s := range subSkills {
		if SkillAllowedForAgentRole(s, role) {
			filtered = append(filtered, s)
		}
	}
	return filtered
}

// buildCodexRootSkillMD 写顶层调度 SKILL.md(放在 staging skills/SKILL.md,装机时落到
// ~/.codex/skills/<name>/SKILL.md)。
//
// 设计意图:codex agent toml 里 developer_instructions 故意只留"身份 + 第一步 Read 本文件"
// 两行,**详细文档全部在这里**。这样 codex spawn 时 system prompt 极简、不烧每次推理 token,
// 真正在排障时再 read 这份 SKILL.md 把规则装进 thread context。
//
// 内容覆盖:运行环境 / 行为规则 / 排障入口路由 / 输出形态 / 子 skill 列表 / 故障快报模板。
// 跟 OpenClaw AGENTS.md 同款信息但 codex 视角(无 OpenClaw 命令 / 进度条说法保留)。
func buildCodexRootSkillMD(wsRoot string, ctx *Context, agentName string) (string, error) {
	subSkills, err := listCodexSubSkills(wsRoot)
	if err != nil {
		return "", err
	}
	desc := fmt.Sprintf(
		"%s 系统专属排障入口。仅在 tshoot-router 已解析到本项目，或用户明确点名 %s / %s 时使用；不得按通用故障词跨项目触发。",
		ctx.System.Name, ctx.System.ID, agentName,
	)
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "name: %s\n", agentName)
	fmt.Fprintf(&sb, "description: %s\n", desc)
	sb.WriteString("---\n\n")

	fmt.Fprintf(&sb, "# %s 排障机器人(codex 调度入口)\n\n", ctx.System.Name)

	sb.WriteString("## 运行环境\n\n")
	sb.WriteString("- Bash 可用,跑 Python 脚本与 shell 命令(sandbox_mode=workspace-write,只动 cwd,不碰系统)\n")
	sb.WriteString("- MCP server 已嵌入本 agent toml `[mcp_servers.*]` 段,spawn 时自动拉起,直接调对应 mcp_server\n")
	fmt.Fprintf(&sb, "- skills 脚本用**绝对路径**:`python3 ~/.codex/skills/%s/<skill>/scripts/<file>.py ...`\n\n", agentName)

	sb.WriteString("## 行为规则\n\n")
	sb.WriteString("1. **少说多查**。拿到一份工具结果立刻发下一个,不停顿写大段解释,数据齐了再统一总结。\n")
	sb.WriteString("2. **独立工具调用同消息发出**(并发执行);只有数据依赖才串行。\n")
	sb.WriteString("3. **3 步以上排障流程**在回复开头打 `[已完成: routing ✓ / 当前: 拉指标]` 进度条。\n")
	sb.WriteString("4. **每条排障回复首行**:`[ctx: <env> / <service> / <阶段>]`,让用户一眼看出当前在哪条线。\n")
	sb.WriteString("5. **多轮承继**:上轮 prod 服务 5xx,本轮\"那 dev 呢\" → 保持 service+阶段只换 env;**用户显式换话题**才丢上下文。\n\n")

	sb.WriteString("## 排障入口\n\n")
	sb.WriteString("排障关键词(`5xx / 报错 / 慢 / 不通 / 突增 / 失败 / 卡住 / 排查 / 故障 / 定位 / 为什么`) → 第一个 Read `incident-investigator/SKILL.md`(7 步主线含 Step 7 沉淀,会再编排其它 skill)。\n\n")
	sb.WriteString("简单查询(`prod grafana 地址 / dev nacos / 服务在哪个分支`) → 第一个 Read `routing/SKILL.md` 直接给映射答案。\n\n")

	sb.WriteString("## 输出形态\n\n")
	sb.WriteString("- 排障类(用了\"5xx/报错/慢/不通/排查/故障/定位/为什么\"等词) → **故障快报模板**(下方)\n")
	sb.WriteString("- 简单查询 → 直接给答案 + 来源,**不套快报模板**,一两行结束\n")
	sb.WriteString("- 图类(架构图/流程图/拓扑) → 图直发当前会话;失败才回 Mermaid 代码\n\n")

	fmt.Fprintf(&sb, "## 子 skill 索引(共 %d 个)\n\n", len(subSkills))
	fmt.Fprintf(&sb, "路径前缀:`~/.codex/skills/%s/`,按各 SKILL.md 流程操作,不要跳步骤。\n\n", agentName)
	for _, s := range subSkills {
		fmt.Fprintf(&sb, "- `%s/SKILL.md`\n", s)
	}
	sb.WriteString("\n")

	sb.WriteString("## 故障快报模板\n\n")
	sb.WriteString("```text\n")
	sb.WriteString("🚨 故障快报 | {环境} | {服务}\n")
	sb.WriteString("🕒 时间:{开始~结束,UTC+8}\n")
	sb.WriteString("📌 结论:{一句话}\n")
	sb.WriteString("1) 影响范围  用户影响 / 接口 / 错误量(环比)\n")
	sb.WriteString("2) 关键信号  TOP 3 错误 + 次数\n")
	if ctx.Infrastructure.Observability.Jaeger.Enabled {
		sb.WriteString("3) 证据      日志(time + trace_id + 核心报错) / 指标(PromQL 结论) / 链路(trace_id + TOP span)\n")
	} else {
		sb.WriteString("3) 证据      日志(time + trace_id + 核心报错) / 指标(PromQL 结论)\n")
	}
	sb.WriteString("4) 根因      直接根因 + 深层根因 + 置信度(高/中/低)\n")
	sb.WriteString("5) 处置      P0 止血(三段式 PRE/EXEC/POST) / P1 修复 / P2 预防\n")
	sb.WriteString("6) 状态      风险 🟢🟡🔴 / 是否升级 / 下次回报\n")
	sb.WriteString("```\n")

	return sb.String(), nil
}

// listCodexSubSkills 扫 wsRoot/skills/ 找所有 <子目录>/SKILL.md,返回子目录名(不含路径)。
// 顶层 skills/SKILL.md 不算(那是装机时单独生成的调度入口)。
func listCodexSubSkills(wsRoot string) ([]string, error) {
	skillsDir := filepath.Join(wsRoot, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, fmt.Errorf("read skills dir: %w", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(skillsDir, e.Name(), "SKILL.md")); err != nil {
			continue
		}
		out = append(out, e.Name())
	}
	return out, nil
}

// writeSkillEntry 写一条 [[skills.config]] inline-table 段。
func writeSkillEntry(sb *strings.Builder, path string) {
	sb.WriteString("[[skills.config]]\n")
	fmt.Fprintf(sb, "path = %s\n", TomlString(path))
	sb.WriteString("enabled = true\n\n")
}
