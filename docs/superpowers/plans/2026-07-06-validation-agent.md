# Validation Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One-click deploy creates both a troubleshooting agent and a validation agent; Bug Workbench runs validator first, then passes its report to the troubleshooter.

**Architecture:** Add an agent role dimension (`troubleshooter`, `validator`) to generated metadata, agent filenames, install/discovery, and Bug Workbench orchestration. Keep one shared skills/scripts/references/MCP capability base per target, while generating two user-visible agent definitions.

**Tech Stack:** Go generator/install/discover/bughub packages, Wails desktop bindings, Vue 3 Bug Workbench/Bots UI, Vitest, Go tests.

---

## File Structure

- Modify `internal/discover/types.go`: add `AgentID`, `Role`, and role constants to `Meta`.
- Modify `internal/generator/tshoot_meta.go`: write role-aware metadata.
- Create `internal/generator/agent_role.go`: role helpers and role-specific agent IDs.
- Modify `internal/generator/claude_code.go`, `cursor.go`, `codex.go`, `ide_prompt.go`: emit two agent definitions and role-specific prompts.
- Create `templates/workspace/skills/bug-verifier/SKILL.md.tmpl`: validator entry skill.
- Modify `internal/agent/install_native.go`: install multiple agent files from one staging directory.
- Modify `internal/agent/install_native_openclaw.go` and helpers: register two OpenClaw agents in one workspace.
- Modify `internal/discover/scan.go`: dedupe by agent identity and expose role.
- Modify `internal/bughub/types.go`, `match.go`, `codex_runner.go`: route validator/troubleshooter separately.
- Modify `cmd/tshoot-desktop/bindings_bug_investigation.go`: pass available validator refs into investigation start.
- Modify `web/src/pages/BugWorkbenchPage.vue` and tests: show selected validator and filter selectable bots by troubleshooter role.
- Modify `web/src/pages/BotsPage.vue` and tests if present: show role labels and both installed agents.

---

### Task 1: Role-Aware Metadata

**Files:**
- Modify: `internal/discover/types.go`
- Modify: `internal/generator/tshoot_meta.go`
- Create: `internal/generator/agent_role.go`
- Test: `internal/generator/generator_test.go`
- Test: `internal/discover/scan_test.go`

- [ ] **Step 1: Write failing metadata tests**

Add to `internal/generator/generator_test.go`:

```go
func TestWriteTshootMetaIncludesAgentRole(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-troubleshooter.yaml")
	out := filepath.Join(t.TempDir(), "sys")
	tr := filepath.Join(projectRoot(t), "templates")
	g := New(cfg, tr, out)
	g.TroubleshooterYAMLSource = []byte("system:\n  id: shop\n")

	dir := filepath.Join(out, "meta")
	if err := g.writeTshootMetaForRole(dir, "codex", AgentRoleValidator); err != nil {
		t.Fatalf("writeTshootMetaForRole: %v", err)
	}
	data := readFile(t, filepath.Join(dir, "tshoot.json"))
	if !strings.Contains(data, `"agent_id": "shop-validator"`) {
		t.Fatalf("agent_id missing from meta:\n%s", data)
	}
	if !strings.Contains(data, `"role": "validator"`) {
		t.Fatalf("role missing from meta:\n%s", data)
	}
}
```

Add to `internal/discover/scan_test.go`:

```go
func TestMetaDefaultsMissingRoleToTroubleshooter(t *testing.T) {
	root := t.TempDir()
	writeMeta(t, filepath.Join(root, "old"), Meta{
		SchemaVersion: 1,
		SystemID:      "shop",
		SystemName:    "Shop",
		Target:        "codex",
	})
	agents, err := Scan([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Fatalf("want 1 agent, got %d", len(agents))
	}
	if agents[0].Meta.Role != RoleTroubleshooter {
		t.Fatalf("missing role should default to %q, got %+v", RoleTroubleshooter, agents[0].Meta)
	}
	if agents[0].Meta.AgentID != "shop-troubleshooter" {
		t.Fatalf("missing agent_id should derive from system+role, got %+v", agents[0].Meta)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run:

```bash
go test ./internal/generator -run TestWriteTshootMetaIncludesAgentRole -count=1
go test ./internal/discover -run TestMetaDefaultsMissingRoleToTroubleshooter -count=1
```

Expected: both fail because `writeTshootMetaForRole`, `AgentRoleValidator`, `RoleTroubleshooter`, `Meta.Role`, and `Meta.AgentID` do not exist.

- [ ] **Step 3: Add role fields and constants**

Modify `internal/discover/types.go`:

```go
const (
	RoleTroubleshooter = "troubleshooter"
	RoleValidator      = "validator"
)

type Meta struct {
	SchemaVersion int `json:"schema_version"`
	TshootVersion string `json:"tshoot_version"`
	SystemID      string `json:"system_id"`
	SystemName    string `json:"system_name"`
	AgentID       string `json:"agent_id,omitempty"`
	Role          string `json:"role,omitempty"`
	Target        string `json:"target"`
	GeneratedAt   string `json:"generated_at"`
	TroubleshooterYAML string `json:"troubleshooter_yaml"`
	UserEdits map[string]any `json:"user_edits,omitempty"`
}
```

Add helper in `internal/discover/scan.go`:

```go
func normalizeMeta(meta *Meta) {
	if meta == nil {
		return
	}
	if strings.TrimSpace(meta.Role) == "" {
		meta.Role = RoleTroubleshooter
	}
	if strings.TrimSpace(meta.AgentID) == "" && strings.TrimSpace(meta.SystemID) != "" {
		meta.AgentID = meta.SystemID + "-" + meta.Role
	}
}
```

Call `normalizeMeta(&meta)` in `readAgent` after JSON unmarshal and before validation.

- [ ] **Step 4: Add generator role helpers**

Create `internal/generator/agent_role.go`:

```go
package generator

import (
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

type AgentRole string

const (
	AgentRoleTroubleshooter AgentRole = discover.RoleTroubleshooter
	AgentRoleValidator      AgentRole = discover.RoleValidator
)

func agentIDForRole(ctx *Context, role AgentRole) string {
	base := strings.TrimSpace(ctx.System.ID)
	if base == "" {
		base = strings.TrimSuffix(agentSlug(ctx), "-troubleshooter")
	}
	switch role {
	case AgentRoleValidator:
		return base + "-validator"
	default:
		if explicit := strings.TrimSpace(ctx.Agent.ID); explicit != "" && strings.HasSuffix(explicit, "-troubleshooter") {
			return explicit
		}
		return base + "-troubleshooter"
	}
}

func roleDisplayName(ctx *Context, role AgentRole) string {
	name := strings.TrimSpace(ctx.System.Name)
	if name == "" {
		name = strings.TrimSpace(ctx.System.ID)
	}
	if role == AgentRoleValidator {
		return name + " 验证"
	}
	return name + " 排障"
}
```

- [ ] **Step 5: Write role-aware metadata**

Modify `internal/generator/tshoot_meta.go`:

```go
func (g *Generator) writeTshootMeta(dir, target string) error {
	return g.writeTshootMetaForRole(dir, target, AgentRoleTroubleshooter)
}

func (g *Generator) writeTshootMetaForRole(dir, target string, role AgentRole) error {
	meta := discover.Meta{
		SchemaVersion:      1,
		TshootVersion:      g.TshootVersion,
		SystemID:           g.Ctx.System.ID,
		SystemName:         g.Ctx.System.Name,
		AgentID:            agentIDForRole(g.Ctx, role),
		Role:               string(role),
		Target:             target,
		GeneratedAt:        time.Now().UTC().Format(time.RFC3339),
		TroubleshooterYAML: string(g.TroubleshooterYAMLSource),
	}
	// keep existing marshal/write body unchanged
}
```

- [ ] **Step 6: Run tests**

Run:

```bash
gofmt -w internal/discover/types.go internal/discover/scan.go internal/generator/tshoot_meta.go internal/generator/agent_role.go internal/generator/generator_test.go internal/discover/scan_test.go
go test ./internal/generator -run TestWriteTshootMetaIncludesAgentRole -count=1
go test ./internal/discover -run TestMetaDefaultsMissingRoleToTroubleshooter -count=1
```

Expected: both pass.

- [ ] **Step 7: Commit**

```bash
git add internal/discover/types.go internal/discover/scan.go internal/generator/tshoot_meta.go internal/generator/agent_role.go internal/generator/generator_test.go internal/discover/scan_test.go
git commit -m "feat: add agent role metadata"
```

---

### Task 2: Add Validator Skill Entry

**Files:**
- Create: `templates/workspace/skills/bug-verifier/SKILL.md.tmpl`
- Modify: `internal/generator/generator_test.go`

- [ ] **Step 1: Write failing generator test**

Add to `internal/generator/generator_test.go`:

```go
func TestGenerateIncludesBugVerifierSkill(t *testing.T) {
	cfg := loadCfg(t, "examples/shop-troubleshooter.yaml")
	out := filepath.Join(t.TempDir(), "sys")
	tr := filepath.Join(projectRoot(t), "templates")

	if err := New(cfg, tr, out).Generate(); err != nil {
		t.Fatalf("generate: %v", err)
	}
	root := filepath.Join(out, "templates/workspace-template")
	assertExists(t, root, []string{
		"skills/bug-verifier/SKILL.md",
		"skills/frontend-repro-investigator/SKILL.md",
	})
	body := readFile(t, filepath.Join(root, "skills/bug-verifier/SKILL.md"))
	for _, want := range []string{"verification_status", "fixed_verified", "still_reproduces", "frontend-repro-investigator"} {
		if !strings.Contains(body, want) {
			t.Fatalf("bug-verifier missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "最可能根因") || strings.Contains(body, "RCA") {
		t.Fatalf("bug-verifier should not request RCA:\n%s", body)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./internal/generator -run TestGenerateIncludesBugVerifierSkill -count=1
```

Expected: FAIL because `skills/bug-verifier/SKILL.md` is not generated.

- [ ] **Step 3: Create bug-verifier skill template**

Create `templates/workspace/skills/bug-verifier/SKILL.md.tmpl`:

```markdown
---
name: bug-verifier
description: Bug 验证 Agent 统一入口。用于禅道/工单类问题的主动复现、修复后回归复查、截图/Network/console/API/trace 证据收集；只输出验证报告，不做根因分析。
---

# Bug 验证 Agent

你负责验证 Bug 是否可复现或是否已修复。先取证，再输出报告；不要给根因判断，不要给修复方案。

## 必读顺序

1. Read `skills/routing/SKILL.md`，确定环境、前端入口、仓库、服务、配置和可观测性映射。
2. 如果工单涉及 Web/PC/H5 页面，Read `skills/frontend-repro-investigator/SKILL.md`。
3. 如工单包含附件，先读取或预览附件，图片附件优先用于确认页面状态和期望现象。

## 验证流程

1. 提取工单字段：标题、复现步骤、期望结果、实际结果、环境、附件、产品、模块、严重程度、操作系统、浏览器、提交人、指派人。
2. 定位入口：优先使用工单中的 URL/API；没有时从 routing references 推导。
3. 主动验证：能打开页面就打开页面，能请求接口就请求接口。不能访问时明确写入 gaps。
4. 收集证据：截图、Network 请求、接口响应摘要、console errors、trace_ids、request_ids。
5. 修复后复查时，按相同步骤验证问题是否消失。

## 输出格式

只输出下面结构，不输出 RCA：

```text
verification_status: reproduced | not_reproduced | insufficient_info | fixed_verified | still_reproduces
environment: <bug env / bot env>
entry:
  frontend_url: <实际入口或 ->
  api_url: <实际接口或 ->
observed_behavior: <实际看到的现象>
expected_behavior: <工单期望>
evidence:
  screenshots: []
  network: []
  console_errors: []
  trace_ids: []
  request_ids: []
gaps: []
```

## 边界

- 不做“最可能根因”判断。
- 不输出修复代码。
- 信息不足时输出 `insufficient_info`，并把缺口写入 `gaps`。
- 验证失败不等于业务根因，必须区分“无法访问/缺账号/缺数据”和“现象不可复现”。
```

- [ ] **Step 4: Run test**

Run:

```bash
go test ./internal/generator -run TestGenerateIncludesBugVerifierSkill -count=1
```

Expected: PASS.

- [ ] **Step 5: Run skill script test**

Run:

```bash
scripts/test-skill-scripts.sh
```

Expected: PASS. This ensures adding the skill did not break existing script tests.

- [ ] **Step 6: Commit**

```bash
git add templates/workspace/skills/bug-verifier/SKILL.md.tmpl internal/generator/generator_test.go
git commit -m "feat: add bug verifier skill"
```

---

### Task 3: Generate Two IDE Agent Definitions

**Files:**
- Modify: `internal/generator/claude_code.go`
- Modify: `internal/generator/cursor.go`
- Modify: `internal/generator/codex.go`
- Modify: `internal/generator/ide_prompt.go`
- Test: `internal/generator/generator_test.go`

- [ ] **Step 1: Write failing multi-agent generation test**

Update `TestGenerate_MultiTargets_All` assertions in `internal/generator/generator_test.go`:

```go
assertExists(t, out+"-claude-code", []string{
	"agents/shop-troubleshooter.md",
	"agents/shop-validator.md",
	"skills/routing/SKILL.md",
	"skills/bug-verifier/SKILL.md",
})
assertExists(t, out+"-cursor", []string{
	"agents/shop-troubleshooter.md",
	"agents/shop-validator.md",
	"skills/routing/SKILL.md",
	"skills/bug-verifier/SKILL.md",
})
```

Add a Codex-specific assertion to the same test:

```go
if err := g.GenerateCodex(); err != nil {
	t.Fatalf("codex: %v", err)
}
assertExists(t, out+"-codex", []string{
	"agents/shop-troubleshooter.toml",
	"agents/shop-validator.toml",
	"skills/SKILL.md",
	"skills/bug-verifier/SKILL.md",
})
validatorToml := readFile(t, filepath.Join(out+"-codex", "agents/shop-validator.toml"))
if !strings.Contains(validatorToml, "bug-verifier") {
	t.Fatalf("validator codex toml should mention bug-verifier:\n%s", validatorToml)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./internal/generator -run TestGenerate_MultiTargets_All -count=1
```

Expected: FAIL because only `shop-bot` or one current slug is generated.

- [ ] **Step 3: Split IDE body builder and add role-specific entry**

Modify `internal/generator/ide_prompt.go`:

```go
func writeIDEAgentBody(sb *strings.Builder, wsRoot string, ctx *Context, profile IDEPlatform) {
	if intro := strings.TrimSpace(profile.Intro); intro != "" {
		sb.WriteString(intro)
		sb.WriteString("\n\n")
	}
	writeIDEAgentSharedBody(sb, wsRoot, ctx, profile)
}

func writeIDEAgentSharedBody(sb *strings.Builder, wsRoot string, ctx *Context, profile IDEPlatform) {
	// Move the existing body of writeIDEAgentBody here, starting at the current
	// "SOUL 主体" block and ending after the Skills index block.
}

func writeIDEAgentBodyForRole(sb *strings.Builder, wsRoot string, ctx *Context, profile IDEPlatform, role AgentRole) {
	if intro := strings.TrimSpace(profile.Intro); intro != "" {
		sb.WriteString(intro)
		sb.WriteString("\n\n")
	}
	sb.WriteString("## 入口\n\n")
	if role == AgentRoleValidator {
		sb.WriteString("先 Read `skills/bug-verifier/SKILL.md`，按验证流程执行。输出验证报告，不输出 RCA。\n\n")
	} else {
		sb.WriteString("先 Read `skills/incident-investigator/SKILL.md`，按 7 步排障流程执行。\n\n")
	}
	writeIDEAgentSharedBody(sb, wsRoot, ctx, profile)
}
```

The final file must keep `writeIDEAgentBody` behavior unchanged for existing callers and must make validator/troubleshooter builders call `writeIDEAgentBodyForRole`.

- [ ] **Step 4: Generate Claude Code agent files for both roles**

Modify `GenerateClaudeCode`:

```go
for _, role := range []AgentRole{AgentRoleTroubleshooter, AgentRoleValidator} {
	agentName := agentIDForRole(g.Ctx, role)
	agentMD, err := buildClaudeAgentMDForRole(wsRoot, g.Ctx, agentName, role)
	if err != nil {
		return fmt.Errorf("build %s agent .md: %w", role, err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, agentName+".md"), []byte(agentMD), 0o644); err != nil {
		return err
	}
}
```

Create:

```go
func buildClaudeAgentMDForRole(wsRoot string, ctx *Context, agentName string, role AgentRole) (string, error) {
	var sb strings.Builder
	sb.WriteString("---\n")
	fmt.Fprintf(&sb, "name: %s\n", agentName)
	fmt.Fprintf(&sb, "description: %s\n", roleDisplayName(ctx, role))
	if role == AgentRoleTroubleshooter {
		if m := strings.TrimSpace(ctx.Agent.TargetModels["claude-code"]); m != "" {
			fmt.Fprintf(&sb, "model: %s\n", m)
		}
	}
	sb.WriteString("---\n\n")
	fmt.Fprintf(&sb, "# %s\n\n", roleDisplayName(ctx, role))
	intro := "本 agent 在 Claude Code 通过 `@" + agentName + "` 调用。"
	if role == AgentRoleValidator {
		intro += "用于 Bug 主动复现和修复后回归验证，只输出验证报告。"
	} else {
		intro += "用于只读排障和 RCA 分析。"
	}
	writeIDEAgentBodyForRole(&sb, wsRoot, ctx, IDEAgentProfile{
		Role: role,
		Intro: intro,
		SkillsScriptPathPrefix: "~/.claude/skills/" + agentName,
		SkillsHeader: "## Skills 索引",
	})
	return sb.String(), nil
}
```

- [ ] **Step 5: Generate Cursor agent files for both roles**

Apply the same loop and builder pattern in `internal/generator/cursor.go`, using `.cursor` paths and Cursor-specific intro. Validator intro must state that commands should be emitted as copyable commands when Cursor cannot execute them directly.

- [ ] **Step 6: Generate Codex agent TOML for both roles**

Modify `GenerateCodex` to write:

```go
for _, role := range []AgentRole{AgentRoleTroubleshooter, AgentRoleValidator} {
	agentName := agentIDForRole(g.Ctx, role)
	agentTOML, err := buildCodexAgentTOMLForRole(wsRoot, g.Ctx, agentName, role)
	if err != nil {
		return fmt.Errorf("build %s agent toml: %w", role, err)
	}
	if err := os.WriteFile(filepath.Join(agentsDir, agentName+".toml"), []byte(agentTOML), 0o644); err != nil {
		return err
	}
}
```

Refactor existing `buildCodexAgentTOML` to call:

```go
func buildCodexAgentTOMLForRole(wsRoot string, ctx *Context, agentName string, role AgentRole) (string, error)
```

Role-specific description:

```go
func buildCodexAgentDescriptionForRole(ctx *Context, role AgentRole) string {
	if role == AgentRoleValidator {
		return fmt.Sprintf("%s Bug验证/复现/回归(只读)。触发词:复现/验证/回归/截图/Network/console/接口响应/工单/禅道。", ctx.System.Name)
	}
	return buildCodexAgentDescription(ctx, 0)
}
```

Role-specific developer instructions:

```go
if role == AgentRoleValidator {
	instr = fmt.Sprintf("你是 **%s Bug 验证机器人**。第一步:Read `~/.codex/skills/%s/bug-verifier/SKILL.md`。只输出验证报告,不输出 RCA。\n", ctx.System.Name, agentName)
} else {
	instr = buildCodexDeveloperInstructions(wsRoot, ctx, agentName)
}
```

- [ ] **Step 7: Write role-aware metadata in IDE staging**

For Claude/Cursor/Codex staging, write `tshoot.json` for `AgentRoleTroubleshooter` at the staging root to preserve existing workdir behavior, and also add per-agent metadata files under `agents-meta/<agentName>/tshoot.json` for installer use:

```go
func (g *Generator) writeAgentRoleMetas(outDir, target string) error {
	for _, role := range []AgentRole{AgentRoleTroubleshooter, AgentRoleValidator} {
		dir := filepath.Join(outDir, "agents-meta", agentIDForRole(g.Ctx, role))
		if err := g.writeTshootMetaForRole(dir, target, role); err != nil {
			return err
		}
	}
	return g.writeTshootMetaForRole(outDir, target, AgentRoleTroubleshooter)
}
```

Call `writeAgentRoleMetas` in each IDE generator instead of plain `writeTshootMeta`.

- [ ] **Step 8: Run generator tests**

Run:

```bash
gofmt -w internal/generator
go test ./internal/generator -run 'TestGenerate_MultiTargets_All|TestGenerate_MultiTargets_NoOpenclaw|TestGenerateIncludesBugVerifierSkill' -count=1
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/generator templates/workspace/skills/bug-verifier/SKILL.md.tmpl
git commit -m "feat: generate validator agent definitions"
```

---

### Task 4: Install Multiple IDE Agents

**Files:**
- Modify: `internal/agent/install_native.go`
- Test: `internal/agent/install_native_test.go`

- [ ] **Step 1: Write failing install test**

Add to `internal/agent/install_native_test.go`:

```go
func TestInstallNative_InstallsMultipleClaudeAgents(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	staging := t.TempDir()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(staging, "agents"), 0o755))
	must(os.WriteFile(filepath.Join(staging, "agents", "shop-troubleshooter.md"), []byte("---\nname: shop-troubleshooter\n---\n"), 0o644))
	must(os.WriteFile(filepath.Join(staging, "agents", "shop-validator.md"), []byte("---\nname: shop-validator\n---\n"), 0o644))
	must(os.MkdirAll(filepath.Join(staging, "skills", "bug-verifier"), 0o755))
	must(os.WriteFile(filepath.Join(staging, "skills", "bug-verifier", "SKILL.md"), []byte("# bug verifier\n"), 0o644))
	must(os.MkdirAll(filepath.Join(staging, "scripts"), 0o755))
	must(os.WriteFile(filepath.Join(staging, "scripts", "helper.py"), []byte("# helper\n"), 0o644))
	must(os.MkdirAll(filepath.Join(staging, "agents-meta", "shop-troubleshooter"), 0o755))
	must(os.WriteFile(filepath.Join(staging, "agents-meta", "shop-troubleshooter", "tshoot.json"), []byte(`{"schema_version":1,"system_id":"shop","target":"claude-code","agent_id":"shop-troubleshooter","role":"troubleshooter"}`), 0o644))
	must(os.MkdirAll(filepath.Join(staging, "agents-meta", "shop-validator"), 0o755))
	must(os.WriteFile(filepath.Join(staging, "agents-meta", "shop-validator", "tshoot.json"), []byte(`{"schema_version":1,"system_id":"shop","target":"claude-code","agent_id":"shop-validator","role":"validator"}`), 0o644))

	if err := InstallNative(staging, "claude-code"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"shop-troubleshooter", "shop-validator"} {
		if _, err := os.Stat(filepath.Join(fakeHome, ".claude", "agents", name+".md")); err != nil {
			t.Fatalf("%s agent not installed: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(fakeHome, ".claude", "skills", name, "bug-verifier", "SKILL.md")); err != nil {
			t.Fatalf("%s skills not installed: %v", name, err)
		}
		meta := filepath.Join(fakeHome, ".claude", "skills", name, "tshoot.json")
		body, err := os.ReadFile(meta)
		if err != nil {
			t.Fatalf("%s meta missing: %v", name, err)
		}
		if !strings.Contains(string(body), `"agent_id":"`+name+`"`) {
			t.Fatalf("%s meta wrong: %s", name, body)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./internal/agent -run TestInstallNative_InstallsMultipleClaudeAgents -count=1
```

Expected: FAIL because `findStagingAgentFile` rejects multiple agent files.

- [ ] **Step 3: Replace single-agent lookup with list lookup**

Modify `internal/agent/install_native.go`:

```go
type stagingAgentFile struct {
	File string
	Name string
}

func findStagingAgentFiles(stagingDir string, t IDETarget) ([]stagingAgentFile, error) {
	dir := filepath.Join(stagingDir, "agents")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read staging agents dir: %w", err)
	}
	ext := t.UserAgentExt()
	var matches []stagingAgentFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if strings.Contains(n, ".bak.") || !strings.HasSuffix(n, ext) {
			continue
		}
		matches = append(matches, stagingAgentFile{
			File: filepath.Join(dir, n),
			Name: strings.TrimSuffix(n, ext),
		})
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].Name < matches[j].Name })
	if len(matches) == 0 {
		return nil, fmt.Errorf("no agents/*%s in staging %s", ext, dir)
	}
	return matches, nil
}
```

- [ ] **Step 4: Install all agent files and namespaced shared assets**

In `InstallNative`, replace single `agentFile, name` logic with:

```go
agentFiles, err := findStagingAgentFiles(stagingDir, t)
if err != nil {
	return err
}
for _, sub := range []string{"agents", "skills", "scripts"} {
	if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", sub, err)
	}
}
for _, ag := range agentFiles {
	if err := installOneNativeAgent(stagingDir, root, t, ag); err != nil {
		return err
	}
}
return nil
```

Create:

```go
func installOneNativeAgent(stagingDir, root string, t IDETarget, ag stagingAgentFile) error {
	dstAgent := filepath.Join(root, "agents", ag.Name+t.UserAgentExt())
	if _, err := os.Stat(dstAgent); err == nil {
		ts := nanoTimestamp()
		if err := copyFileSimple(dstAgent, dstAgent+".bak."+ts); err != nil {
			return fmt.Errorf("backup existing agent: %w", err)
		}
	}
	if t == TargetCodex {
		raw, err := os.ReadFile(ag.File)
		if err != nil {
			return fmt.Errorf("read staging codex toml: %w", err)
		}
		skillsRoot := filepath.Join(root, "skills", ag.Name)
		patched := strings.ReplaceAll(string(raw), generator.CodexPlaceholderSkillsRoot, skillsRoot)
		if err := os.WriteFile(dstAgent, []byte(patched), 0o644); err != nil {
			return fmt.Errorf("install codex agent toml: %w", err)
		}
	} else if err := copyFileSimple(ag.File, dstAgent); err != nil {
		return fmt.Errorf("install agent file: %w", err)
	}
	if err := replaceDir(filepath.Join(stagingDir, "skills"), filepath.Join(root, "skills", ag.Name)); err != nil {
		return fmt.Errorf("install skills for %s: %w", ag.Name, err)
	}
	if err := replaceDir(filepath.Join(stagingDir, "scripts"), filepath.Join(root, "scripts", ag.Name)); err != nil {
		return fmt.Errorf("install scripts for %s: %w", ag.Name, err)
	}
	metaSrc := filepath.Join(stagingDir, "agents-meta", ag.Name, discover.MetaFilename)
	if _, err := os.Stat(metaSrc); os.IsNotExist(err) {
		metaSrc = filepath.Join(stagingDir, discover.MetaFilename)
	}
	if _, err := os.Stat(metaSrc); err == nil {
		dstMeta := filepath.Join(root, "skills", ag.Name, discover.MetaFilename)
		if err := copyFileSimple(metaSrc, dstMeta); err != nil {
			return fmt.Errorf("install tshoot.json anchor for %s: %w", ag.Name, err)
		}
	}
	return nil
}
```

- [ ] **Step 5: Keep old tests passing**

Run:

```bash
gofmt -w internal/agent/install_native.go internal/agent/install_native_test.go
go test ./internal/agent -run 'TestInstallNative_ClaudeCode|TestInstallNative_Cursor|TestInstallNative_BackupExistingAgent|TestInstallNative_InstallsMultipleClaudeAgents|TestInstallNative_MissingAgentsDir' -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/install_native.go internal/agent/install_native_test.go
git commit -m "feat: install multiple IDE agents"
```

---

### Task 5: Register Two OpenClaw Agents

**Files:**
- Modify: `internal/agent/install_native_openclaw.go`
- Modify: `internal/agent/install_native_openclaw_helpers.go`
- Test: `internal/agent/install_native_openclaw_test.go`

- [ ] **Step 1: Write failing OpenClaw test**

Update `TestInstallNativeOpenclaw` or add a new test:

```go
func TestInstallNativeOpenclaw_RegistersTroubleshooterAndValidator(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	staging := makeOpenclawStaging(t)
	opts := InstallOpenclawOptions{SkipGatewayRestart: true}
	if err := InstallNativeOpenclaw(context.Background(), staging, opts); err != nil {
		t.Fatal(err)
	}
	data := readJSON(t, filepath.Join(fakeHome, ".openclaw", "openclaw.json"))
	agents := getList(data, "agents", "list")
	ids := map[string]bool{}
	for _, item := range agents {
		m := item.(map[string]any)
		ids[m["id"].(string)] = true
	}
	for _, want := range []string{"shop-troubleshooter", "shop-validator"} {
		if !ids[want] {
			t.Fatalf("missing openclaw agent %s in %+v", want, agents)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
go test ./internal/agent -run TestInstallNativeOpenclaw_RegistersTroubleshooterAndValidator -count=1
```

Expected: FAIL because only one OpenClaw agent is injected.

- [ ] **Step 3: Add OpenClaw role descriptors**

In `internal/agent/install_native_openclaw.go`, derive both role IDs:

```go
func openclawAgentIDs(cfg *config.SystemConfig) []struct {
	ID   string
	Name string
	Role string
} {
	systemID := strings.TrimSpace(cfg.System.ID)
	return []struct {
		ID string
		Name string
		Role string
	}{
		{ID: systemID + "-troubleshooter", Name: cfg.System.Name + " 排障", Role: "troubleshooter"},
		{ID: systemID + "-validator", Name: cfg.System.Name + " 验证", Role: "validator"},
	}
}
```

If `cfg.Agent.ID` is explicitly set and ends with `-troubleshooter`, preserve that for the troubleshooter ID and still derive validator from `system.id`.

- [ ] **Step 4: Inject both OpenClaw agents**

Replace:

```go
agentID := cfg.ResolveID()
if err := injectAgent(ocData, agentID, cfg.Agent.Name, model, wsDir); err != nil {
	return err
}
```

With:

```go
for _, ag := range openclawAgentIDs(cfg) {
	if err := injectAgent(ocData, ag.ID, ag.Name, model, wsDir); err != nil {
		return err
	}
}
agentID := cfg.ResolveID()
```

Keep `agentID` for creds and MCP prefix to avoid duplicating MCP server configuration.

- [ ] **Step 5: Keep duplicate protection**

Update `TestInstallNativeOpenclaw_AgentNotDuplicated` expectation from one agent to two generated agents plus any pre-existing agents:

```go
if got := countAgentsWithPrefix(data, "shop-"); got != 2 {
	t.Errorf("二次 install 后 shop agents 应仍只有 2 条,got %d", got)
}
```

Add helper in test file:

```go
func countAgentsWithPrefix(data map[string]any, prefix string) int {
	n := 0
	for _, item := range getList(data, "agents", "list") {
		if m, ok := item.(map[string]any); ok {
			if id, _ := m["id"].(string); strings.HasPrefix(id, prefix) {
				n++
			}
		}
	}
	return n
}
```

- [ ] **Step 6: Run OpenClaw tests**

Run:

```bash
gofmt -w internal/agent/install_native_openclaw.go internal/agent/install_native_openclaw_helpers.go internal/agent/install_native_openclaw_test.go
go test ./internal/agent -run 'TestInstallNativeOpenclaw|TestInstallNativeOpenclaw_RegistersTroubleshooterAndValidator|TestInstallNativeOpenclaw_AgentNotDuplicated|TestInstallNativeOpenclaw_PreservesExistingAgents' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/agent/install_native_openclaw.go internal/agent/install_native_openclaw_helpers.go internal/agent/install_native_openclaw_test.go
git commit -m "feat: register validator in OpenClaw"
```

---

### Task 6: Discover Both Agents

**Files:**
- Modify: `internal/discover/scan.go`
- Modify: `internal/discover/types.go`
- Test: `internal/discover/scan_test.go`

- [ ] **Step 1: Replace old dedupe test**

Rename `TestScanDedupBySystemIDAndTarget` to `TestScanDedupByAgentIDAndTarget`. Use same `agent_id` for both roots:

```go
func TestScanDedupByAgentIDAndTarget(t *testing.T) {
	root1 := t.TempDir()
	root2 := t.TempDir()
	m := Meta{SchemaVersion: 1, SystemID: "shop", AgentID: "shop-troubleshooter", Role: RoleTroubleshooter, SystemName: "Shop", Target: "openclaw"}
	writeMeta(t, filepath.Join(root1, "a"), m)
	writeMeta(t, filepath.Join(root2, "b"), m)

	agents, err := Scan([]string{root1, root2})
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 1 {
		t.Errorf("want 1 dedup-ed agent, got %d", len(agents))
	}
}
```

Add:

```go
func TestScanKeepsTroubleshooterAndValidator(t *testing.T) {
	root := t.TempDir()
	writeMeta(t, filepath.Join(root, "troubleshooter"), Meta{SchemaVersion: 1, SystemID: "shop", AgentID: "shop-troubleshooter", Role: RoleTroubleshooter, Target: "codex"})
	writeMeta(t, filepath.Join(root, "validator"), Meta{SchemaVersion: 1, SystemID: "shop", AgentID: "shop-validator", Role: RoleValidator, Target: "codex"})

	agents, err := Scan([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 {
		t.Fatalf("want 2 role-specific agents, got %d", len(agents))
	}
	if agents[0].Meta.AgentID == agents[1].Meta.AgentID {
		t.Fatalf("agents should be distinct: %+v", agents)
	}
}
```

- [ ] **Step 2: Run tests to verify failure**

Run:

```bash
go test ./internal/discover -run 'TestScanDedupByAgentIDAndTarget|TestScanKeepsTroubleshooterAndValidator' -count=1
```

Expected: second test fails while dedupe key remains `systemID|target`.

- [ ] **Step 3: Change dedupe key**

Modify `Scan`:

```go
key := a.Meta.AgentID + "|" + a.Meta.Target
```

Update sorting:

```go
sort.Slice(out, func(i, j int) bool {
	if out[i].Meta.SystemID != out[j].Meta.SystemID {
		return out[i].Meta.SystemID < out[j].Meta.SystemID
	}
	if out[i].Meta.Target != out[j].Meta.Target {
		return out[i].Meta.Target < out[j].Meta.Target
	}
	return out[i].Meta.Role < out[j].Meta.Role
})
```

- [ ] **Step 4: Run discover tests**

Run:

```bash
gofmt -w internal/discover/scan.go internal/discover/scan_test.go
go test ./internal/discover -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/discover/scan.go internal/discover/scan_test.go internal/discover/types.go
git commit -m "feat: discover validator agents"
```

---

### Task 7: Route Bug Workbench Through Validator Agent

**Files:**
- Modify: `internal/bughub/types.go`
- Modify: `internal/bughub/match.go`
- Modify: `internal/bughub/codex_runner.go`
- Modify: `cmd/tshoot-desktop/bindings_bug_investigation.go`
- Test: `internal/bughub/investigation_test.go`
- Test: `internal/bughub/match_test.go`
- Test: `cmd/tshoot-desktop/bindings_bug_investigation_test.go`

- [ ] **Step 1: Add role to BotRef**

Modify `internal/bughub/types.go`:

```go
type BotRef struct {
	Key      string   `json:"key"`
	SystemID string   `json:"system_id"`
	Target   string   `json:"target"`
	Path     string   `json:"path"`
	Name     string   `json:"name,omitempty"`
	Role     string   `json:"role,omitempty"`
	Env      string   `json:"env,omitempty"`
	Envs     []string `json:"envs,omitempty"`
}
```

- [ ] **Step 2: Write failing match tests**

Add to `internal/bughub/match_test.go`:

```go
func TestMatchBotsExcludesValidatorByDefault(t *testing.T) {
	bug := Bug{ID: "b1", SystemID: "shop", Env: "test"}
	bots := []BotRef{
		{Key: "t", SystemID: "shop", Target: "codex", Role: "troubleshooter", Env: "test"},
		{Key: "v", SystemID: "shop", Target: "codex", Role: "validator", Env: "test"},
	}
	got := MatchBots(bug, bots)
	if len(got) != 1 || got[0].Bot.Key != "t" {
		t.Fatalf("want only troubleshooter match, got %+v", got)
	}
}

func TestFindValidatorForTroubleshooter(t *testing.T) {
	selected := BotRef{Key: "t", SystemID: "shop", Target: "codex", Role: "troubleshooter", Env: "test"}
	bots := []BotRef{
		selected,
		{Key: "v", SystemID: "shop", Target: "codex", Role: "validator", Env: "test"},
	}
	got, ok := FindValidatorForBot(selected, bots)
	if !ok || got.Key != "v" {
		t.Fatalf("validator ok=%v bot=%+v", ok, got)
	}
}
```

- [ ] **Step 3: Implement role filtering and validator lookup**

Modify `MatchBots`:

```go
for _, bot := range bots {
	if strings.EqualFold(strings.TrimSpace(bot.Role), "validator") {
		continue
	}
	// existing score logic
}
```

Add:

```go
func FindValidatorForBot(selected BotRef, bots []BotRef) (BotRef, bool) {
	for _, bot := range bots {
		if !sameText(bot.Role, "validator") {
			continue
		}
		if !sameText(bot.SystemID, selected.SystemID) || !sameText(bot.Target, selected.Target) {
			continue
		}
		if selected.Env != "" && !sameText(bot.Env, selected.Env) {
			continue
		}
		return bot, true
	}
	return BotRef{}, false
}
```

- [ ] **Step 4: Change investigator Start signature**

Modify `internal/bughub/codex_runner.go`:

```go
type InvestigationOptions struct {
	Validator BotRef
}

func (i *CodexInvestigator) Start(parent context.Context, bug Bug, bot BotRef, opts ...InvestigationOptions) (InvestigationRun, error) {
	options := InvestigationOptions{}
	if len(opts) > 0 {
		options = opts[0]
	}
	validator := options.Validator
	if strings.TrimSpace(validator.Key) == "" {
		validator = bot
	}
	validationPrompt := BuildCodexValidationPrompt(bug, validator)
	validationCmd, validationParser, err := i.buildCommandLocked(strings.TrimSpace(validator.Target), validator, validationPrompt)
	// investigation command remains built from bot
}
```

Keep fallback to `bot` so existing tests and CLI-like calls continue working before UI is updated.

- [ ] **Step 5: Write failing run-order test with separate binaries**

Add to `internal/bughub/investigation_test.go`:

```go
func TestCodexInvestigatorUsesValidatorBotForValidation(t *testing.T) {
	root := t.TempDir()
	validatorWorkspace := filepath.Join(root, "validator")
	troubleshooterWorkspace := filepath.Join(root, "troubleshooter")
	if err := os.MkdirAll(validatorWorkspace, 0o755); err != nil { t.Fatal(err) }
	if err := os.MkdirAll(troubleshooterWorkspace, 0o755); err != nil { t.Fatal(err) }
	promptsPath := filepath.Join(root, "prompts.txt")
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nprintf '%s\\n' \"$PWD\" >> " + shellQuote(promptsPath) + "\ncase \"$PWD\" in\n*" + shellQuote(validatorWorkspace) + "*) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"verification_status: reproduced\"}}' ;;\n*) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"rca final\"}}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil { t.Fatal(err) }
	store := NewInvestigationStore(root)
	inv := NewCodexInvestigator(store, bin)
	run, err := inv.Start(context.Background(), Bug{ID: "bug-1", Title: "Bug"},
		BotRef{Key: "t", Target: "codex", Path: troubleshooterWorkspace, Role: "troubleshooter"},
		InvestigationOptions{Validator: BotRef{Key: "v", Target: "codex", Path: validatorWorkspace, Role: "validator"}},
	)
	if err != nil { t.Fatalf("Start: %v", err) }
	waited, err := inv.Wait(run.ID)
	if err != nil { t.Fatalf("Wait: %v", err) }
	if waited.FinalMessage != "rca final" {
		t.Fatalf("final = %q", waited.FinalMessage)
	}
	data, err := os.ReadFile(promptsPath)
	if err != nil { t.Fatal(err) }
	got := string(data)
	if !strings.Contains(got, validatorWorkspace) || !strings.Contains(got, troubleshooterWorkspace) {
		t.Fatalf("expected both workspaces, got:\n%s", got)
	}
}
```

- [ ] **Step 6: Pass validator from desktop binding**

In `cmd/tshoot-desktop/bindings_bug_investigation.go`, extend input:

```go
type BugInvestigationInput struct {
	BugID     string        `json:"bug_id"`
	Bot       bughub.BotRef `json:"bot"`
	Validator bughub.BotRef `json:"validator,omitempty"`
}
```

Call:

```go
return a.codexInvestigator().Start(ctx, bug, input.Bot, bughub.InvestigationOptions{Validator: input.Validator})
```

The frontend will provide `validator`. For old callers, zero value falls back.

- [ ] **Step 7: Run bughub tests**

Run:

```bash
gofmt -w internal/bughub cmd/tshoot-desktop/bindings_bug_investigation.go
go test ./internal/bughub -run 'TestMatchBotsExcludesValidatorByDefault|TestFindValidatorForTroubleshooter|TestCodexInvestigatorUsesValidatorBotForValidation|TestCodexInvestigatorRunsValidationAgentBeforeInvestigationAgent' -count=1
go test ./cmd/tshoot-desktop -run TestStartBugInvestigation -count=1
```

Expected: PASS. If no `TestStartBugInvestigation` exists, run `go test ./cmd/tshoot-desktop -run BugInvestigation -count=1`.

- [ ] **Step 8: Commit**

```bash
git add internal/bughub cmd/tshoot-desktop/bindings_bug_investigation.go
git commit -m "feat: run validator before troubleshooting"
```

---

### Task 8: Expose Roles In Bridge And UI

**Files:**
- Modify: `web/src/lib/bridge/bugs.ts`
- Modify: `web/src/lib/bridge/bugs.test.ts`
- Modify: `web/src/pages/BugWorkbenchPage.vue`
- Modify: `web/src/pages/BugWorkbenchPage.test.ts`
- Modify: `web/src/pages/BotsPage.vue`

- [ ] **Step 1: Add frontend types**

Modify `web/src/lib/bridge/bugs.ts` BotRef type:

```ts
export type BotRef = {
  key: string
  system_id: string
  target: string
  path: string
  name?: string
  role?: 'troubleshooter' | 'validator' | string
  env?: string
  envs?: string[]
}
```

Update `startBugInvestigation` input type:

```ts
export type StartBugInvestigationInput = {
  bug_id: string
  bot: BotRef
  validator?: BotRef
}
```

- [ ] **Step 2: Write failing UI test**

Add to `web/src/pages/BugWorkbenchPage.test.ts`:

```ts
it('shows only troubleshooting bots and passes matched validator when starting', async () => {
  vi.mocked(listBugs).mockResolvedValue([
    { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常', system_id: 'base', env: 'test' },
  ])
  vi.mocked(matchBugBots).mockResolvedValue([
    { bot: { key: 'base-t|codex', system_id: 'base', target: 'codex', path: '/t', role: 'troubleshooter', env: 'test' }, score: 10, reasons: [] },
    { bot: { key: 'base-v|codex', system_id: 'base', target: 'codex', path: '/v', role: 'validator', env: 'test' }, score: 10, reasons: [] },
  ])
  vi.mocked(startBugInvestigation).mockResolvedValue({
    id: 'run-1',
    bug_id: 'zentao-577',
    bot_key: 'base-t|codex',
    status: 'running',
    events: [],
  })
  const wrapper = mount(BugWorkbenchPage)
  await flushPromises()
  await flushPromises()

  expect(wrapper.text()).toContain('base-t')
  expect(wrapper.text()).toContain('验证')
  expect(wrapper.findAll('.bot-match').some(item => item.text().includes('/v'))).toBe(false)

  await wrapper.find('.bot-actions .btn.primary').trigger('click')
  expect(startBugInvestigation).toHaveBeenCalledWith({
    bug_id: 'zentao-577',
    bot: expect.objectContaining({ key: 'base-t|codex' }),
    validator: expect.objectContaining({ key: 'base-v|codex' }),
  })
})
```

- [ ] **Step 3: Update Bug Workbench computed values**

In `BugWorkbenchPage.vue`, keep raw matches and display matches separate:

```ts
const troubleshootingMatches = computed(() => matches.value.filter(m => (m.bot.role || 'troubleshooter') !== 'validator'))
const validatorMatches = computed(() => matches.value.filter(m => m.bot.role === 'validator'))
const selectedValidatorBot = computed(() => {
  const bot = selectedBot.value
  if (!bot) return undefined
  return validatorMatches.value.find(m =>
    m.bot.system_id === bot.system_id &&
    m.bot.target === bot.target &&
    (!bot.env || m.bot.env === bot.env)
  )?.bot
})
```

Use `troubleshootingMatches` in the bot list `v-for`.

In `startInvestigation`, call:

```ts
const run = await startBugInvestigation({ bug_id: bugID, bot, validator: selectedValidatorBot.value })
```

Add display near bot actions:

```vue
<p v-if="selectedValidatorBot" class="muted direct-launch-note">
  验证: {{ selectedValidatorBot.name || selectedValidatorBot.system_id }} · {{ selectedValidatorBot.target }}<template v-if="selectedValidatorBot.env"> · {{ selectedValidatorBot.env }}</template>
</p>
<p v-else class="muted direct-launch-note">未安装对应验证 agent,可继续仅排障。</p>
```

- [ ] **Step 4: Update Bots page role display**

In `web/src/pages/BotsPage.vue`, wherever bot cards show name/target, add:

```vue
<span class="role-pill" :class="bot.meta.role === 'validator' ? 'validator' : 'troubleshooter'">
  {{ bot.meta.role === 'validator' ? '验证' : '排障' }}
</span>
```

Add compact CSS:

```css
.role-pill {
  display: inline-flex;
  align-items: center;
  height: 20px;
  padding: 0 7px;
  border: 1px solid #cbd5e1;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 700;
  color: #475569;
  background: #f8fafc;
}
.role-pill.validator {
  color: #3730a3;
  background: #eef2ff;
  border-color: #c7d2fe;
}
```

- [ ] **Step 5: Run frontend tests**

Run:

```bash
npm test -- --run src/pages/BugWorkbenchPage.test.ts src/lib/bridge/bugs.test.ts
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/src/lib/bridge/bugs.ts web/src/lib/bridge/bugs.test.ts web/src/pages/BugWorkbenchPage.vue web/src/pages/BugWorkbenchPage.test.ts web/src/pages/BotsPage.vue
git commit -m "feat: show validator agent in bug workflow"
```

---

### Task 9: End-To-End Generation And Install Tests

**Files:**
- Modify: `internal/agent/install_e2e_test.go`
- Modify: `internal/generator/generator_test.go`
- Modify: `internal/discover/scan_test.go`

- [ ] **Step 1: Add end-to-end test for generated IDE staging**

Add to `internal/agent/install_e2e_test.go`:

```go
func TestGeneratedClaudeCodeStagingInstallsBothRoles(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)
	cfg, yamlSrc := loadShopCfg(t)
	staging := buildStaging(t, cfg, yamlSrc, "claude-code")
	if err := agent.InstallNative(staging, "claude-code"); err != nil {
		t.Fatalf("InstallNative: %v", err)
	}
	for _, name := range []string{"shop-troubleshooter", "shop-validator"} {
		if _, err := os.Stat(filepath.Join(fakeHome, ".claude", "agents", name+".md")); err != nil {
			t.Fatalf("missing agent %s: %v", name, err)
		}
	}
	agents, err := discover.Scan([]string{filepath.Join(fakeHome, ".claude", "skills")})
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 {
		t.Fatalf("discover should find two roles, got %+v", agents)
	}
}
```

- [ ] **Step 2: Run focused end-to-end tests**

Run:

```bash
go test ./internal/agent -run TestGeneratedClaudeCodeStagingInstallsBothRoles -count=1
```

Expected: PASS.

- [ ] **Step 3: Run package suite**

Run:

```bash
go test ./internal/generator ./internal/agent ./internal/discover ./internal/bughub ./cmd/tshoot-desktop -timeout 90s
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/install_e2e_test.go internal/generator/generator_test.go internal/discover/scan_test.go
git commit -m "test: cover validation agent install flow"
```

---

### Task 10: Build And Embedded Web Assets

**Files:**
- Modify generated assets under: `internal/webui/dist/`
- Keep: `internal/webui/dist/.gitkeep`

- [ ] **Step 1: Run full targeted verification**

Run:

```bash
go test ./internal/generator ./internal/agent ./internal/discover ./internal/bughub ./cmd/tshoot-desktop -timeout 90s
npm test -- --run src/pages/BugWorkbenchPage.test.ts src/lib/bridge/bugs.test.ts
npm run build
```

Expected: all commands pass.

- [ ] **Step 2: Sync built assets**

Run:

```bash
rm -rf internal/webui/dist
cp -R web/dist internal/webui/dist
: > internal/webui/dist/.gitkeep
```

- [ ] **Step 3: Check status**

Run:

```bash
git status --short
```

Expected: source files and built web assets changed; `internal/webui/dist/.gitkeep` is not deleted.

- [ ] **Step 4: Commit**

```bash
git add internal/webui/dist web/src internal/generator internal/agent internal/discover internal/bughub cmd/tshoot-desktop templates/workspace/skills/bug-verifier
git status --short
git commit -m "feat: deploy validation agent with troubleshooter"
```

---

## Self-Review Notes

- Spec coverage: generator, install, discover, Bug Workbench, validator skill, tests, and build asset sync are covered.
- Compatibility: missing `role` and `agent_id` default to `troubleshooter` and `<system>-troubleshooter`.
- Main risk: existing `agent.id` custom values. The implementation preserves explicit `*-troubleshooter` for the troubleshooting role and derives validator from `system.id`.
- MCP duplication avoided: both roles use the same MCP servers and credentials; only agent definitions and installed skill namespaces are duplicated per IDE.
