package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

func TestInvestigationStoreCreateAppendAndList(t *testing.T) {
	store := NewInvestigationStore(t.TempDir())
	run := InvestigationRun{
		ID:            "run-1",
		BugID:         "zentao-577",
		BotKey:        "/Users/me/.codex/agents/base.toml|codex",
		Status:        InvestigationRunning,
		StartedAt:     time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC),
		PromptPreview: "Investigate bug",
	}
	if err := store.Upsert(run); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.AppendEvent("run-1", InvestigationEvent{Type: "agent_message", Message: "checking logs"}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := store.Finish("run-1", InvestigationSucceeded, "root cause", ""); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	runs, err := store.ListByBug("zentao-577")
	if err != nil {
		t.Fatalf("ListByBug: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs len = %d", len(runs))
	}
	got := runs[0]
	if got.Status != InvestigationSucceeded || got.FinalMessage != "root cause" {
		t.Fatalf("run = %+v", got)
	}
	if got.FinishedAt == nil {
		t.Fatalf("FinishedAt is nil")
	}
	if len(got.Events) != 1 || got.Events[0].Message != "checking logs" {
		t.Fatalf("events = %+v", got.Events)
	}
}

func TestInvestigationStoreActiveRunForBug(t *testing.T) {
	store := NewInvestigationStore(t.TempDir())
	if err := store.Upsert(InvestigationRun{ID: "done", BugID: "b1", Status: InvestigationSucceeded}); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(InvestigationRun{ID: "running", BugID: "b1", Status: InvestigationRunning}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.ActiveRunForBug("b1")
	if err != nil {
		t.Fatalf("ActiveRunForBug: %v", err)
	}
	if !ok || got.ID != "running" {
		t.Fatalf("active ok=%v run=%+v", ok, got)
	}
}

func TestInvestigationStoreGet(t *testing.T) {
	store := NewInvestigationStore(t.TempDir())
	if err := store.Upsert(InvestigationRun{ID: "run-1", BugID: "b1", Status: InvestigationRunning}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("run-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != "run-1" || got.BugID != "b1" {
		t.Fatalf("run = %+v", got)
	}
	if _, err := store.Get("missing"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing err = %v", err)
	}
}

func TestInvestigationStoreListByBugFiltersAndSortsNewestFirst(t *testing.T) {
	store := NewInvestigationStore(t.TempDir())
	older := time.Date(2026, 7, 6, 11, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	if err := store.Upsert(InvestigationRun{ID: "old", BugID: "b1", StartedAt: older}); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(InvestigationRun{ID: "other", BugID: "b2", StartedAt: newer}); err != nil {
		t.Fatal(err)
	}
	if err := store.Upsert(InvestigationRun{ID: "new", BugID: "b1", StartedAt: newer}); err != nil {
		t.Fatal(err)
	}

	runs, err := store.ListByBug("b1")
	if err != nil {
		t.Fatalf("ListByBug: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("runs len = %d", len(runs))
	}
	if runs[0].ID != "new" || runs[1].ID != "old" {
		t.Fatalf("runs order = %+v", runs)
	}
}

func TestInvestigationStoreMissingRunErrors(t *testing.T) {
	store := NewInvestigationStore(t.TempDir())
	if err := store.AppendEvent("missing", InvestigationEvent{Type: "agent_message"}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("AppendEvent err = %v", err)
	}
	if err := store.Finish("missing", InvestigationFailed, "", "failed"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Finish err = %v", err)
	}
}

func TestInvestigationStoreMissingAndEmptyFile(t *testing.T) {
	root := t.TempDir()
	store := NewInvestigationStore(root)
	runs, err := store.ListByBug("b1")
	if err != nil {
		t.Fatalf("ListByBug missing file: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs len = %d", len(runs))
	}
	got, ok, err := store.ActiveRunForBug("b1")
	if err != nil {
		t.Fatalf("ActiveRunForBug missing file: %v", err)
	}
	if ok || got.ID != "" {
		t.Fatalf("active ok=%v run=%+v", ok, got)
	}

	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "runs.json"), []byte(" \n\t"), 0o600); err != nil {
		t.Fatal(err)
	}
	runs, err = store.ListByBug("b1")
	if err != nil {
		t.Fatalf("ListByBug empty file: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("runs len = %d", len(runs))
	}
}

func TestInvestigationStoreWriteNewlineAndMode(t *testing.T) {
	store := NewInvestigationStore(t.TempDir())
	if err := store.Upsert(InvestigationRun{ID: "run-1", BugID: "b1"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("runs.json missing trailing newline: %q", data)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o", got)
	}
}

func TestInvestigationStoreWriteTightensExistingFileMode(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "runs.json"), []byte("[]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	if err := store.Upsert(InvestigationRun{ID: "run-1", BugID: "b1"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o", got)
	}
}

func TestParseCodexJSONLEvent(t *testing.T) {
	event, final, failed := ParseCodexJSONLEvent([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"root cause found"}}`))
	if event.Type != "agent_message" || event.Message != "root cause found" {
		t.Fatalf("event = %+v", event)
	}
	if final != "root cause found" || failed != "" {
		t.Fatalf("final=%q failed=%q", final, failed)
	}

	event, final, failed = ParseCodexJSONLEvent([]byte(`{"type":"turn.failed","error":{"message":"auth missing"}}`))
	if event.Type != "turn_failed" || failed != "auth missing" || final != "" {
		t.Fatalf("event=%+v final=%q failed=%q", event, final, failed)
	}

	event, final, failed = ParseCodexJSONLEvent([]byte(`not-json`))
	if event.Type != "raw" || event.Message != "not-json" || final != "" || failed != "" {
		t.Fatalf("malformed event=%+v final=%q failed=%q", event, final, failed)
	}

	event, _, _ = ParseCodexJSONLEvent([]byte(`{"type":"item.started","item":{"type":"command_execution","command":"go test ./...","status":"in_progress"}}`))
	if event.Type != "command_execution" || event.Message != "go test ./..." || event.Meta["state"] != "started" || event.Meta["status"] != "in_progress" {
		t.Fatalf("command started event = %+v", event)
	}
	event, _, _ = ParseCodexJSONLEvent([]byte(`{"type":"item.completed","item":{"type":"command_execution","command":"go test ./...","status":"completed","exit_code":0}}`))
	if event.Meta["state"] != "completed" || event.Meta["exit_code"] != 0 {
		t.Fatalf("command completed event = %+v", event)
	}
	event, _, _ = ParseCodexJSONLEvent([]byte(`{"type":"item.started","item":{"type":"mcp_tool_call","tool":"get_logs"}}`))
	if event.Type != "mcp_tool_call" || event.Message != "get_logs" || event.Meta["state"] != "started" {
		t.Fatalf("mcp event = %+v", event)
	}
}

func TestParseCodexJSONLEventEmitsTrustedInvestigationStep(t *testing.T) {
	handoff, _, _ := ParseCodexJSONLEvent([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"[[TSHOOT_STEP phase=investigation index=1 key=evidence_handoff]]"}}`))
	if handoff.Type != "phase_step" || handoff.Message != "接收验证证据" {
		t.Fatalf("handoff event=%+v", handoff)
	}

	event, final, failed := ParseCodexJSONLEvent([]byte(`{"type":"item.completed","item":{"type":"agent_message","text":"[[TSHOOT_STEP phase=investigation index=4 key=dependency_chain]]"}}`))
	if event.Type != "phase_step" || event.Message != "依赖与调用链" || final != "" || failed != "" {
		t.Fatalf("event=%+v final=%q failed=%q", event, final, failed)
	}
	if event.Meta["phase"] != "investigation" || event.Meta["step_key"] != "dependency_chain" || event.Meta["step_index"] != 4 || event.Meta["step_total"] != 7 {
		t.Fatalf("step meta = %+v", event.Meta)
	}

	for _, marker := range []string{
		"[[TSHOOT_STEP phase=investigation index=4 key=root_cause]]",
		"[[TSHOOT_STEP phase=investigation index=8 key=knowledge_sink]]",
		"prefix [[TSHOOT_STEP phase=investigation index=1 key=evidence_handoff]]",
	} {
		event, final, failed = ParseCodexJSONLEvent([]byte(fmt.Sprintf(`{"type":"item.completed","item":{"type":"agent_message","text":%q}}`, marker)))
		if event.Type != "agent_message" || final != marker || failed != "" {
			t.Fatalf("malformed marker %q was accepted: event=%+v final=%q failed=%q", marker, event, final, failed)
		}
	}
}

func TestParseClaudeStreamJSONEventEmitsInvestigationStep(t *testing.T) {
	event, final, failed := ParseClaudeStreamJSONEvent([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"[[TSHOOT_STEP phase=investigation index=2 key=timeline]]"}]}}`))
	if event.Type != "phase_step" || event.Message != "时间轴与最近变更" || event.Meta["step_index"] != 2 || final != "" || failed != "" {
		t.Fatalf("event=%+v final=%q failed=%q", event, final, failed)
	}
}

func TestNormalizePhaseEventUsesCurrentWorkflowPhase(t *testing.T) {
	for phase, labels := range map[string][2]string{
		"validation":    {"开始验证", "验证完成"},
		"investigation": {"开始排障", "排障完成"},
		"fix":           {"开始修复", "修复完成"},
		"regression":    {"开始回归", "回归完成"},
	} {
		started := InvestigationEvent{Type: "turn_started", Message: "开始排障"}
		completed := InvestigationEvent{Type: "turn_completed", Message: "排障完成"}
		normalizePhaseEvent(&started, phase)
		normalizePhaseEvent(&completed, phase)
		if started.Message != labels[0] || completed.Message != labels[1] {
			t.Fatalf("phase %s = %q/%q", phase, started.Message, completed.Message)
		}
	}
}

func TestParseClaudeStreamJSONEventIgnoresProtocolNoise(t *testing.T) {
	for _, raw := range []string{
		`{"type":"system","subtype":"init","session_id":"s1"}`,
		`{"type":"user","message":{"role":"user","content":"prompt echo"}}`,
	} {
		event, final, failed := ParseClaudeStreamJSONEvent([]byte(raw))
		if event.Message != "" || final != "" || failed != "" {
			t.Fatalf("raw=%s event=%+v final=%q failed=%q", raw, event, final, failed)
		}
	}
}

func TestParseClaudeStreamJSONEventKeepsAssistantText(t *testing.T) {
	event, final, failed := ParseClaudeStreamJSONEvent([]byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"checking logs"}]}}`))
	if event.Type != "agent_message" || event.Message != "checking logs" || final != "" || failed != "" {
		t.Fatalf("event=%+v final=%q failed=%q", event, final, failed)
	}
}

func TestBuildCodexInvestigationPromptIncludesBugAndBot(t *testing.T) {
	bug := Bug{ID: "zentao-577", Source: "zentao", SourceID: "577", Title: "搜索结果错误", Steps: "1. 搜索电影"}
	bot := BotRef{Key: "/tmp/base.toml|codex", SystemID: "base", Target: "codex", Path: "/tmp/base.toml"}
	prompt := BuildCodexInvestigationPrompt(bug, bot)
	for _, want := range []string{
		"请作为选定排障机器人执行只读根因分析",
		"搜索结果错误",
		"zentao:577",
		"target: codex",
		"不修改代码",
		"Read `incident-investigator/SKILL.md`",
		"7 步排障图谱",
		"最终回答必须使用下面的故障快报模板",
		"🚨 故障快报 | <环境> | <服务/模块>",
		"confidence=high",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "1. 排障过程：") {
		t.Fatalf("investigation prompt should not use the old generic output shape:\n%s", prompt)
	}
}

func TestBuildCodexContinuePromptRequiresIncidentReport(t *testing.T) {
	prompt := BuildCodexContinuePrompt(
		Bug{ID: "zentao-909", Source: "zentao", SourceID: "909", Title: "分类数量错误"},
		BotRef{Target: "codex", Env: "test"},
		"补充账号：admin",
		InvestigationRun{FinalMessage: "前一轮缺少登录态"},
	)
	for _, want := range []string{
		"用户补充信息",
		"补充账号：admin",
		"最终回答必须使用下面的故障快报模板",
		"🚨 故障快报 | <环境> | <服务/模块>",
		"7) 需补信息",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("continue prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "1. 排障过程：") {
		t.Fatalf("continue prompt should not use the old generic output shape:\n%s", prompt)
	}
}

func TestBuildCodexFixPromptUsesStructuredOutputContract(t *testing.T) {
	prompt := BuildCodexFixPrompt(
		Bug{ID: "zentao-909", Source: "zentao", SourceID: "909", Title: "分类数量错误"},
		BotRef{Target: "codex", Env: "test"},
		InvestigationRun{FinalMessage: "根因：分类接口统计字段错误"},
		"只修复最小问题",
	)
	for _, want := range []string{
		"你是 Bug 修复 Agent",
		"最终回答必须只输出下面的 YAML 结构",
		"fix_status: fixed_pushed | blocked | failed",
		"deployment_notice",
		"blocked_reason",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("fix prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "1. 修复分支") {
		t.Fatalf("fix prompt should not use the old generic output shape:\n%s", prompt)
	}
}

func TestBuildCodexExecCommandUsesSafeWorkspace(t *testing.T) {
	workspace := t.TempDir()
	cmd, err := BuildCodexExecCommand("codex", workspace, "hello")
	if err != nil {
		t.Fatalf("BuildCodexExecCommand: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	for _, want := range []string{"exec", "--json", "--enable respect_system_proxy", "-c suppress_unstable_features_warning=true", `-c approval_policy="never"`, `-c default_permissions="studio_agent"`, `-c permissions.studio_agent.network.enabled=true`, `permissions.studio_agent.filesystem={":minimal"="read"`, "--cd " + workspace, "--skip-git-repo-check"} {
		if !strings.Contains(got, want) {
			t.Fatalf("args %q missing %q", got, want)
		}
	}
	if strings.Contains(got, `permission_profile=`) {
		t.Fatalf("obsolete Codex permission selector remains enabled: %q", got)
	}
	if strings.Contains(got, "--sandbox") || strings.Contains(got, "workspace-write") {
		t.Fatalf("legacy broad-read sandbox remains enabled: %q", got)
	}
	if cmd.Dir != workspace {
		t.Fatalf("Dir = %q", cmd.Dir)
	}
}

func TestBuildCodexExecCommandAllowsOnlySSHHostVerificationFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.Mkdir(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	knownHosts := filepath.Join(sshDir, "known_hosts")
	knownHosts2 := filepath.Join(sshDir, "known_hosts2")
	sshConfig := filepath.Join(sshDir, "config")
	privateKey := filepath.Join(sshDir, "id_ed25519")
	for path, content := range map[string]string{
		knownHosts:  "git.example.test ssh-ed25519 AAAA\n",
		knownHosts2: "legacy.example.test ssh-ed25519 AAAA\n",
		sshConfig:   "Host *\n  IdentitiesOnly yes\n",
		privateKey:  "PRIVATE KEY MUST NOT BE EXPOSED\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	workspace := t.TempDir()
	cmd, err := BuildCodexExecCommand("codex", workspace, "hello")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, "\n")
	for _, path := range []string{knownHosts, knownHosts2} {
		if !strings.Contains(joined, strconv.Quote(path)+`="read"`) {
			t.Fatalf("Codex permission profile omitted SSH host verification file %s:\n%s", path, joined)
		}
	}
	for _, path := range []string{sshDir, sshConfig, privateKey} {
		if strings.Contains(joined, strconv.Quote(path)+`="read"`) {
			t.Fatalf("Codex permission profile exposed SSH credential path %s:\n%s", path, joined)
		}
	}
}

func TestBuildCodexBotExecCommandLoadsManagedAgentRuntimeHome(t *testing.T) {
	codexHome := t.TempDir()
	agentID := "base-troubleshooter"
	workspace := filepath.Join(codexHome, "skills", agentID)
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	agentsDir := filepath.Join(codexHome, "agents")
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agentTOML := "name = \"base-troubleshooter\"\n" + generator.CodexMCPRegionBegin + "\n[mcp_servers.base-mongodb-test]\ncommand = \"npx\"\n" + generator.CodexMCPRegionEnd + "\ndeveloper_instructions = \"do not copy\"\n"
	if err := os.WriteFile(filepath.Join(agentsDir, agentID+".toml"), []byte(agentTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"auth_mode":"test"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd, err := buildCodexBotExecCommand("codex", BotRef{Target: "codex", AgentID: agentID, Path: workspace}, "investigate", nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, " ")
	if strings.Contains(joined, "--profile") {
		t.Fatalf("Codex command still uses an MCP-incompatible profile layer: %s", joined)
	}
	runtimeHome := filepath.Join(codexHome, "tshoot-runtimes", agentID)
	foundHome := false
	for _, item := range cmd.Env {
		if item == "CODEX_HOME="+runtimeHome {
			foundHome = true
		}
	}
	if !foundHome {
		t.Fatalf("Codex command did not bind the isolated bot CODEX_HOME")
	}
	profilePath := filepath.Join(runtimeHome, "config.toml")
	profile, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(profile), "[mcp_servers.base-mongodb-test]") || strings.Contains(string(profile), "developer_instructions") {
		t.Fatalf("runtime config did not copy exactly the managed MCP region:\n%s", profile)
	}
	if info, err := os.Stat(profilePath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("runtime config mode is not 0600: info=%v err=%v", info, err)
	}
	for name, target := range map[string]string{"auth.json": filepath.Join(codexHome, "auth.json"), "skills": filepath.Join(codexHome, "skills")} {
		got, err := os.Readlink(filepath.Join(runtimeHome, name))
		if err != nil || got != target {
			t.Fatalf("runtime %s link = %q, %v; want %q", name, got, err, target)
		}
	}
}

func TestBuildCodexBotExecCommandRefreshesStaleManagedRuntimeConfig(t *testing.T) {
	codexHome := t.TempDir()
	agentID := "base-troubleshooter"
	workspace := filepath.Join(codexHome, "skills", agentID)
	agentsDir := filepath.Join(codexHome, "agents")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(agentsDir, agentID+".toml")
	profilePath := filepath.Join(codexHome, "tshoot-runtimes", agentID, "config.toml")
	oldAgent := generator.CodexMCPRegionBegin + "\n[mcp_servers.base-mongodb-test]\ncommand = \"mongo-old\"\n" + generator.CodexMCPRegionEnd + "\n"
	if err := os.WriteFile(agentPath, []byte(oldAgent), 0o600); err != nil {
		t.Fatal(err)
	}
	bot := BotRef{Target: "codex", AgentID: agentID, Path: workspace}
	if _, err := buildCodexBotExecCommand("codex", bot, "first", nil); err != nil {
		t.Fatal(err)
	}
	updatedAgent := generator.CodexMCPRegionBegin + "\n[mcp_servers.base-mongodb-test]\ncommand = \"mongo-new\"\n[mcp_servers.base-one2all]\nurl = \"https://one2all.example/mcp\"\n" + generator.CodexMCPRegionEnd + "\n"
	if err := os.WriteFile(agentPath, []byte(updatedAgent), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := buildCodexBotExecCommand("codex", bot, "second", nil); err != nil {
		t.Fatal(err)
	}
	profile, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(profile), "base-one2all") || !strings.Contains(string(profile), "mongo-new") || strings.Contains(string(profile), "mongo-old") {
		t.Fatalf("stale runtime config was not refreshed:\n%s", profile)
	}
}

func TestCodexCLIReadsManagedRuntimeHomeMCPServers(t *testing.T) {
	codexBin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex CLI is not installed")
	}
	codexHome := t.TempDir()
	agentID := "base-troubleshooter"
	workspace := filepath.Join(codexHome, "skills", agentID)
	agentsDir := filepath.Join(codexHome, "agents")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(agentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	agentTOML := generator.CodexMCPRegionBegin + "\n[mcp_servers.tshoot-runtime-probe]\ncommand = \"/usr/bin/true\"\n" + generator.CodexMCPRegionEnd + "\n"
	if err := os.WriteFile(filepath.Join(agentsDir, agentID+".toml"), []byte(agentTOML), 0o600); err != nil {
		t.Fatal(err)
	}
	_, runtimeHome, err := ensureCodexAgentRuntimeHome(BotRef{Target: "codex", AgentID: agentID, Path: workspace})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(codexBin, "mcp", "list", "--json")
	cmd.Env = setProcessEnv(os.Environ(), "CODEX_HOME", runtimeHome)
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("codex mcp list: %v", err)
	}
	var servers []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(output, &servers); err != nil {
		t.Fatalf("decode codex mcp list: %v\n%s", err, output)
	}
	for _, server := range servers {
		if server.Name == "tshoot-runtime-probe" {
			return
		}
	}
	t.Fatalf("Codex CLI ignored the managed runtime MCP config: %s", output)
}

func TestBuildCodexBotExecCommandPreservesStandaloneWorkspaceCompatibility(t *testing.T) {
	workspace := t.TempDir()
	cmd, err := buildCodexBotExecCommand("codex", BotRef{Target: "codex", AgentID: "base-troubleshooter", Path: workspace}, "investigate", nil)
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(cmd.Args, " "); strings.Contains(joined, "--profile") {
		t.Fatalf("standalone workspace must not infer an unrelated CODEX_HOME profile: %s", joined)
	}
	if cmd.Dir != workspace {
		t.Fatalf("Dir = %q, want %q", cmd.Dir, workspace)
	}
}

func TestBuildCodexExecCommandUsesHostRepositoryAllowlist(t *testing.T) {
	workspace := t.TempDir()
	repository := t.TempDir()
	gitMetadata := filepath.Join(repository, ".git")
	if err := os.Mkdir(gitMetadata, 0o700); err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	manifest := repositoryAccessManifest{Version: 1, Phase: PhaseFix, Roots: []repositoryAccessRoot{{Repo: "base-backend", Path: repository, Access: "write"}}}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, repositoryAccessManifestName), data, 0o400); err != nil {
		t.Fatal(err)
	}
	prompt := "investigate\nSTUDIO_EVIDENCE_STAGING_DIR=" + staging + "\n"
	cmd, err := BuildCodexExecCommand("codex", workspace, prompt)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, "\n")
	for _, want := range []string{
		strconv.Quote(workspace) + `="write"`,
		strconv.Quote(staging) + `="write"`,
		strconv.Quote(repository) + `="write"`,
		strconv.Quote(gitMetadata) + `="write"`,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Codex permission profile missing %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, strconv.Quote(filepath.Dir(repository))+`="read"`) {
		t.Fatalf("Codex profile granted a repository parent directory:\n%s", joined)
	}
	processEnv := strings.Join(cmd.Env, "\n")
	goEnvCommand := exec.Command("go", "env", "GOROOT")
	goEnvCommand.Dir = workspace
	goRootOutput, err := goEnvCommand.Output()
	if err != nil {
		t.Fatalf("resolve test GOROOT: %v", err)
	}
	goRoot := filepath.Clean(strings.TrimSpace(string(goRootOutput)))
	goSandboxRoot := filepath.Join(staging, ".tshoot-go")
	for _, want := range []string{
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"TMPDIR=" + staging,
		"TMP=" + staging,
		"TEMP=" + staging,
		"GOROOT=" + goRoot,
		"GOCACHE=" + filepath.Join(goSandboxRoot, "build-cache"),
		"GOPATH=" + filepath.Join(goSandboxRoot, "path"),
		"GOMODCACHE=" + filepath.Join(goSandboxRoot, "path", "pkg", "mod"),
		"GOTELEMETRY=off",
		"GOTELEMETRYDIR=" + filepath.Join(goSandboxRoot, "telemetry"),
		"GOENV=off",
		"GOTOOLCHAIN=auto",
		"GOFLAGS=-modcacherw",
	} {
		if !strings.Contains(processEnv, want) {
			t.Fatalf("Codex process environment missing %q", want)
		}
	}
	if !strings.Contains(joined, strconv.Quote(goRoot)+`="read"`) {
		t.Fatalf("Codex permission profile omitted the selected Go SDK %q:\n%s", goRoot, joined)
	}
	if got := strings.Split(processEnvValue(cmd.Env, "PATH"), string(os.PathListSeparator))[0]; got != filepath.Join(goRoot, "bin") {
		t.Fatalf("PATH first entry = %q, want Go SDK bin", got)
	}
	for _, directory := range []string{
		filepath.Join(goSandboxRoot, "build-cache"),
		filepath.Join(goSandboxRoot, "path", "pkg", "mod"),
		filepath.Join(goSandboxRoot, "telemetry"),
	} {
		info, statErr := os.Stat(directory)
		if statErr != nil || !info.IsDir() {
			t.Fatalf("isolated Go directory %q was not prepared: %v", directory, statErr)
		}
	}
}

func TestBuildCodexExecCommandKeepsInvestigationGitMetadataReadOnly(t *testing.T) {
	workspace := t.TempDir()
	repository := t.TempDir()
	gitMetadata := filepath.Join(repository, ".git")
	if err := os.Mkdir(gitMetadata, 0o700); err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	manifest := repositoryAccessManifest{Version: 1, Phase: PhaseInvestigation, Roots: []repositoryAccessRoot{{Repo: "base-backend", Path: repository, Access: "read"}}}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, repositoryAccessManifestName), data, 0o400); err != nil {
		t.Fatal(err)
	}
	prompt := "investigate\nSTUDIO_EVIDENCE_STAGING_DIR=" + staging + "\n"
	cmd, err := BuildCodexExecCommand("codex", workspace, prompt)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, "\n")
	if !strings.Contains(joined, strconv.Quote(repository)+`="read"`) {
		t.Fatalf("Codex permission profile omitted investigation repository:\n%s", joined)
	}
	if strings.Contains(joined, strconv.Quote(gitMetadata)+`="write"`) {
		t.Fatalf("Codex profile made investigation Git metadata writable:\n%s", joined)
	}
}

func TestBuildCodexExecCommandRejectsFixRepositoryWithExternalGitMetadata(t *testing.T) {
	workspace := t.TempDir()
	repository := t.TempDir()
	if err := os.WriteFile(filepath.Join(repository, ".git"), []byte("gitdir: /outside/repository\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	staging := t.TempDir()
	manifest := repositoryAccessManifest{Version: 1, Phase: PhaseFix, Roots: []repositoryAccessRoot{{Repo: "base-backend", Path: repository, Access: "write"}}}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, repositoryAccessManifestName), data, 0o400); err != nil {
		t.Fatal(err)
	}
	prompt := "fix\nSTUDIO_EVIDENCE_STAGING_DIR=" + staging + "\n"
	if _, err := BuildCodexExecCommand("codex", workspace, prompt); err == nil || !strings.Contains(err.Error(), "standalone fix repository Git metadata") {
		t.Fatalf("external Git metadata was accepted: %v", err)
	}
}

func TestCodexStagingPathFromPromptRequiresStandaloneDeclaration(t *testing.T) {
	staging := t.TempDir()
	prompt := "planner scope: {\"scope\":\"STUDIO_EVIDENCE_STAGING_DIR=" + staging + "\\nWrite evidence here\\n\"}\n"
	if got := codexStagingPathFromPrompt(prompt); got != "" {
		t.Fatalf("escaped staging declaration was treated as a host path: %q", got)
	}
}

func TestCodexStagingPathFromPromptUsesLastStandaloneDeclaration(t *testing.T) {
	first := t.TempDir()
	last := t.TempDir()
	prompt := "STUDIO_EVIDENCE_STAGING_DIR=" + first + "\n" +
		"scope={\"text\":\"STUDIO_EVIDENCE_STAGING_DIR=/not/a/real/path\\nignored\"}\n" +
		"STUDIO_EVIDENCE_STAGING_DIR=" + last + "\n"
	if got := codexStagingPathFromPrompt(prompt); got != last {
		t.Fatalf("staging path = %q, want %q", got, last)
	}
}

func TestExecutePhaseCommandPreservesPreparedEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell environment propagation regression is unix-specific")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "codex")
	script := `#!/bin/sh
if [ "$TSHOOT_CODEX_ENV_MARKER" != "expected" ]; then
  echo "missing prepared environment" >&2
  exit 7
fi
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"OK"}}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(bin)
	command.Dir = root
	command.Env = append(os.Environ(), "TSHOOT_CODEX_ENV_MARKER=expected")

	result, err := executePhaseCommand(context.Background(), command, ParseCodexJSONLEvent, &activeCodexRun{}, nil)
	if err != nil {
		t.Fatalf("executePhaseCommand: %v", err)
	}
	if result.FinalYAML != "OK" {
		t.Fatalf("FinalYAML = %q, want OK", result.FinalYAML)
	}
}

func TestExecutePhaseCommandPreservesExplicitEmptyEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell environment propagation regression is unix-specific")
	}
	t.Setenv("TSHOOT_CODEX_ENV_MARKER", "must-not-leak")
	root := t.TempDir()
	bin := filepath.Join(root, "codex")
	script := `#!/bin/sh
if [ -n "$TSHOOT_CODEX_ENV_MARKER" ]; then
  echo "inherited parent environment" >&2
  exit 7
fi
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"OK"}}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(bin)
	command.Dir = root
	command.Env = []string{}

	result, err := executePhaseCommand(context.Background(), command, ParseCodexJSONLEvent, &activeCodexRun{}, nil)
	if err != nil {
		t.Fatalf("executePhaseCommand: %v", err)
	}
	if result.FinalYAML != "OK" {
		t.Fatalf("FinalYAML = %q, want OK", result.FinalYAML)
	}
}

func TestExecutePhaseCommandAcceptsCompletedResultAfterRecoverableCodexError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture is unix-specific")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "codex")
	script := `#!/bin/sh
printf '%s\n' '{"type":"error","message":"Reconnecting... 5/5 (tls handshake eof)"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"version: 1"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":17,"output_tokens":9}}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	result, err := executePhaseCommand(context.Background(), exec.Command(bin), ParseCodexJSONLEvent, &activeCodexRun{}, nil)
	if err != nil {
		t.Fatalf("executePhaseCommand rejected recovered Codex stream: %v", err)
	}
	if result.FinalYAML != "version: 1" || result.Usage.InputTokens != 17 || result.Usage.OutputTokens != 9 {
		t.Fatalf("result = %+v", result)
	}
}

func TestExecutePhaseCommandRejectsUnrecoveredCodexError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture is unix-specific")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "codex")
	script := `#!/bin/sh
printf '%s\n' '{"type":"error","message":"request timed out"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"partial result"}}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := executePhaseCommand(context.Background(), exec.Command(bin), ParseCodexJSONLEvent, &activeCodexRun{}, nil); err == nil || !strings.Contains(err.Error(), "request timed out") {
		t.Fatalf("executePhaseCommand error = %v, want unrecovered transport error", err)
	}
}

func TestExecutePhaseCommandDoesNotMaskTerminalCodexFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture is unix-specific")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "codex")
	script := `#!/bin/sh
printf '%s\n' '{"type":"turn.failed","error":{"message":"auth missing"}}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"stale result"}}'
printf '%s\n' '{"type":"turn.completed"}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := executePhaseCommand(context.Background(), exec.Command(bin), ParseCodexJSONLEvent, &activeCodexRun{}, nil); err == nil || !strings.Contains(err.Error(), "auth missing") {
		t.Fatalf("executePhaseCommand error = %v, want terminal failure", err)
	}
}

func TestRunCommandStageAcceptsCompletedResultAfterRecoverableCodexError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fixture is unix-specific")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "codex")
	script := `#!/bin/sh
printf '%s\n' '{"type":"error","message":"stream disconnected before completion: tls handshake eof"}'
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"recovered result"}}'
printf '%s\n' '{"type":"turn.completed"}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(filepath.Join(root, "runs"))
	if err := store.Upsert(InvestigationRun{ID: "run-recovered", BugID: "bug-1", Status: InvestigationRunning}); err != nil {
		t.Fatal(err)
	}
	investigator := NewCodexInvestigator(store, bin)

	final, status, err := investigator.runCommandStage(context.Background(), "run-recovered", exec.Command(bin), ParseCodexJSONLEvent, &activeCodexRun{}, "validation")
	if err != nil || status != InvestigationSucceeded || final != "recovered result" {
		t.Fatalf("final=%q status=%q err=%v", final, status, err)
	}
}

func TestCodexInvestigatorExecutePhaseReusesTargetAdapterAndCapturesUsage(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "codex")
	script := `#!/bin/sh
printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"verification_status: reproduced\nenvironment: test\nevidence: []\ngaps: []"}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":17,"output_tokens":9}}'
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	investigator := NewCodexInvestigator(NewInvestigationStore(filepath.Join(root, "legacy")), bin)
	var events []InvestigationEvent
	result, err := investigator.ExecutePhase(context.Background(), "attempt-execute", BotRef{Target: "codex", Path: root}, "prompt", func(event InvestigationEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.FinalYAML, "verification_status: reproduced") || result.Usage.InputTokens != 17 || result.Usage.OutputTokens != 9 || result.Usage.Duration <= 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(events) == 0 {
		t.Fatal("phase execution did not stream events")
	}
}

func TestBuildCodexExecCommandRejectsMissingWorkspace(t *testing.T) {
	_, err := BuildCodexExecCommand("codex", filepath.Join(t.TempDir(), "missing"), "hello")
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Fatalf("err = %v", err)
	}
	file := filepath.Join(t.TempDir(), "workspace-file")
	if err := os.WriteFile(file, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = BuildCodexExecCommand("codex", file, "hello")
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildClaudeInvestigationCommand(t *testing.T) {
	workspace := t.TempDir()
	agentPath := filepath.Join(workspace, "base-troubleshooter.md")
	if err := os.WriteFile(agentPath, []byte("# agent"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd, err := BuildClaudeInvestigationCommand("claude", workspace, agentPath, "hello")
	if err != nil {
		t.Fatalf("BuildClaudeInvestigationCommand: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	for _, want := range []string{"-p", "--dangerously-skip-permissions", "--permission-mode bypassPermissions", `--settings {"sandbox":{"enabled":false}}`, "--output-format stream-json", "--verbose", "--agent base-troubleshooter", "hello"} {
		if !strings.Contains(got, want) {
			t.Fatalf("args %q missing %q", got, want)
		}
	}
	if cmd.Dir != workspace {
		t.Fatalf("Dir = %q", cmd.Dir)
	}
}

func TestCodexInvestigatorRunsFakeCodex(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"t1\"}' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"verification_status: reproduced\\ngaps: []\"}}' '{\"type\":\"turn.completed\"}' ;;\n  *) printf '%s\\n' '{\"type\":\"thread.started\",\"thread_id\":\"t1\"}' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"final answer\"}}' '{\"type\":\"turn.completed\"}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	inv := NewCodexInvestigator(store, bin)
	run, err := inv.Start(context.Background(), Bug{ID: "bug-1", Title: "Bug"}, BotRef{Key: "b|codex", Target: "codex", Path: workspace})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.Status != InvestigationRunning {
		t.Fatalf("initial run = %+v", run)
	}
	waited, err := inv.Wait(run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waited.Status != InvestigationSucceeded || waited.FinalMessage != "final answer" {
		t.Fatalf("waited = %+v", waited)
	}
}

func TestFormatFixFinalReportSummarizesStructuredResult(t *testing.T) {
	report := `fix_status: fixed_pushed
environment: "test"
branches:
  - repo: "admin"
    base_branch: "test"
    fix_branch: "fix/bug-909"
    commit: "abc123"
    pushed: true
    target_environment_branch: "test"
    push_remote: "origin"
changes:
  - repo: "admin"
    summary: "counter.ts: 按内容类型拆分计数"
tests:
  - repo: "admin"
    commit: "abc123"
    command: "npm test"
    result: passed
    note: "unit passed"
deployment_notice: "请部署 admin/fix/bug-909 到 test 后再触发验证 Agent 回归。"
risks: []
blocked_reason: ""`

	formatted := formatFixFinalReport(report)
	for _, want := range []string{
		"### 修复报告 | test | 已提交推送",
		"修复分支已生成",
		"fix/bug-909",
		"counter.ts",
		"npm test",
		"请部署 admin/fix/bug-909 到 test",
		"```yaml",
	} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("formatted report missing %q:\n%s", want, formatted)
		}
	}
}

func TestFixerSkillTemplateMatchesRuntimeOutputContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "templates", "workspace", "skills", "bug-fixer", "SKILL.md.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	if templateBlock, runtimeBlock := fencedYAMLBlock(string(data)), fencedYAMLBlock(fixOutputContract()); templateBlock != runtimeBlock {
		t.Fatalf("fixer template/runtime contract drift\ntemplate:\n%s\nruntime:\n%s", templateBlock, runtimeBlock)
	}
}

func fencedYAMLBlock(text string) string {
	start := strings.Index(text, "```yaml\n")
	if start < 0 {
		return ""
	}
	remaining := text[start+len("```yaml\n"):]
	end := strings.Index(remaining, "\n```")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(remaining[:end])
}

func TestCodexInvestigatorStartFixUsesFixerBot(t *testing.T) {
	root := t.TempDir()
	troubleshooterWorkspace := filepath.Join(root, "troubleshooter")
	fixerWorkspace := filepath.Join(root, "fixer")
	for _, dir := range []string{troubleshooterWorkspace, fixerWorkspace} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	callsPath := filepath.Join(root, "calls.txt")
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\n{\n  printf '%s\\n' '---CALL---'\n  pwd\n  printf '%s\\n' \"$last\"\n} >> " + shellQuote(callsPath) + "\nprintf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"fix branch pushed\"}}' '{\"type\":\"turn.completed\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	prevRun := InvestigationRun{
		ID:           "run-prev",
		BugID:        "bug-1",
		Status:       InvestigationSucceeded,
		FinalMessage: "root cause: counter ignores content type",
		Events: []InvestigationEvent{{
			Type:    "agent_message",
			Message: "verification_status: reproduced\ngaps: []",
			Meta:    map[string]any{"phase": "validation"},
		}},
	}
	if err := store.Upsert(prevRun); err != nil {
		t.Fatalf("Upsert previous run: %v", err)
	}
	inv := NewCodexInvestigator(store, bin)
	run, err := inv.StartFix(
		context.Background(),
		Bug{ID: "bug-1", Source: "zentao", SourceID: "909", Title: "分类计数错误"},
		BotRef{
			Key:      "t|codex",
			Target:   "codex",
			Path:     troubleshooterWorkspace,
			SystemID: "base",
			InternalAgents: []BotInternalAgent{
				{ID: "troubleshooter", Role: "troubleshooter"},
				{ID: "fixer", Role: "fixer"},
			},
		},
		prevRun.ID,
	)
	if err != nil {
		t.Fatalf("StartFix: %v", err)
	}
	waited, err := inv.Wait(run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waited.Status != InvestigationSucceeded || waited.FinalMessage != "fix branch pushed" {
		t.Fatalf("waited = %+v", waited)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatalf("ReadFile calls: %v", err)
	}
	if !strings.Contains(string(calls), fixerWorkspace) || !strings.Contains(string(calls), "你是 Bug 修复 Agent") {
		t.Fatalf("fix call should use fixer workspace and prompt:\n%s", calls)
	}
	for _, want := range []string{"创建独立修复分支", "提交", "推送修复分支", "等待 Studio 单独授权后把修复提交分别合入开发基线与环境分支", "root cause: counter ignores content type"} {
		if !strings.Contains(string(calls), want) {
			t.Fatalf("fix prompt missing %q:\n%s", want, calls)
		}
	}
	if phaseForMessageContaining(eventPhases(waited.Events), "fix branch pushed") != "fix" {
		t.Fatalf("fix event phase missing: %+v", waited.Events)
	}
}

func TestCodexInvestigatorEmitsSinkEventWithCurrentRun(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"verification_status: reproduced\\ngaps: []\"}}' '{\"type\":\"turn.completed\"}' ;;\n  *) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"checking\"}}' '{\"type\":\"turn.completed\"}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	inv := NewCodexInvestigator(store, bin)
	got := make(chan struct {
		run   InvestigationRun
		event InvestigationEvent
	}, 16)
	inv.SetEventSink(func(run InvestigationRun, event InvestigationEvent) {
		got <- struct {
			run   InvestigationRun
			event InvestigationEvent
		}{run: run, event: event}
	})

	run, err := inv.Start(context.Background(), Bug{ID: "bug-1", Title: "Bug"}, BotRef{Key: "b|codex", Target: "codex", Path: workspace})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waited, err := inv.Wait(run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waited.Status != InvestigationSucceeded {
		t.Fatalf("waited = %+v", waited)
	}

	deadline := time.After(time.Second)
	for {
		select {
		case emitted := <-got:
			if emitted.event.Type != "agent_message" {
				continue
			}
			if emitted.event.Message != "checking" {
				continue
			}
			if emitted.run.ID != run.ID || emitted.run.BugID != "bug-1" {
				t.Fatalf("sink run = %+v", emitted.run)
			}
			if emitted.event.At.IsZero() {
				t.Fatalf("sink event = %+v", emitted.event)
			}
			goto sawAgentMessage
		case <-deadline:
			t.Fatal("timed out waiting for sink event")
		}
	}
sawAgentMessage:
	deadline = time.After(time.Second)
	for {
		select {
		case emitted := <-got:
			if emitted.event.Type != "status" {
				continue
			}
			if emitted.run.ID != run.ID || emitted.run.Status != InvestigationSucceeded {
				t.Fatalf("sink finish run = %+v", emitted.run)
			}
			if emitted.event.Message != string(InvestigationSucceeded) || emitted.event.At.IsZero() {
				t.Fatalf("sink finish event = %+v", emitted.event)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for finish sink event")
		}
	}
}

func TestCodexInvestigatorUsesPhaseSpecificLifecycleMessages(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\nprintf '%s\\n' '{\"type\":\"turn.started\"}'\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"verification_status: reproduced\\\\ngaps: []\\\\nvalidation evidence\"}}' ;;\n  *) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"investigation evidence\"}}' ;;\nesac\nprintf '%s\\n' '{\"type\":\"turn.completed\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	inv := NewCodexInvestigator(store, bin)
	run, err := inv.Start(context.Background(), Bug{ID: "bug-1", Title: "Bug"}, BotRef{Key: "b|codex", Target: "codex", Path: workspace})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waited, err := inv.Wait(run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	var validationMessages []string
	var investigationMessages []string
	for _, event := range waited.Events {
		phase, _ := event.Meta["phase"].(string)
		switch phase {
		case "validation":
			validationMessages = append(validationMessages, event.Message)
		case "investigation":
			investigationMessages = append(investigationMessages, event.Message)
		}
	}
	validationJoined := strings.Join(validationMessages, "\n")
	investigationJoined := strings.Join(investigationMessages, "\n")
	if validationJoined != "" {
		t.Fatalf("validation messages = %q", validationJoined)
	}
	if strings.Contains(validationJoined, "排障完成") {
		t.Fatalf("validation messages still mention investigation = %q", validationJoined)
	}
	if !strings.Contains(investigationJoined, "开始排障") || !strings.Contains(investigationJoined, "排障完成") {
		t.Fatalf("investigation messages = %q", investigationJoined)
	}
}

func TestCodexInvestigatorStoresLongAgentMessage(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	longMessage := strings.Repeat("x", 70*1024)
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"verification_status: reproduced\\ngaps: []\"}}' ;;\n  *) printf '%s\\n' " + shellQuote(`{"type":"item.completed","item":{"type":"agent_message","text":"`+longMessage+`"}}`) + " ;;\nesac\n"
	script += "printf '%s\\n' '{\"type\":\"turn.completed\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	inv := NewCodexInvestigator(store, bin)
	run, err := inv.Start(context.Background(), Bug{ID: "bug-1", Title: "Bug"}, BotRef{Key: "b|codex", Target: "codex", Path: workspace})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waited, err := inv.Wait(run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waited.Status != InvestigationSucceeded || waited.FinalMessage != longMessage {
		t.Fatalf("status=%s final len=%d", waited.Status, len(waited.FinalMessage))
	}
}

func TestCodexInvestigatorRunsFakeClaude(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	agentPath := filepath.Join(workspace, "base-troubleshooter.md")
	if err := os.WriteFile(agentPath, []byte("# agent"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "claude")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"verification_status: reproduced\\ngaps: []\"}' ;;\n  *) printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"checking\"}]}}' '{\"type\":\"result\",\"subtype\":\"success\",\"result\":\"claude final\"}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	inv := NewCodexInvestigator(store, "codex")
	inv.SetBinaryForTarget("claude-code", bin)
	run, err := inv.Start(context.Background(), Bug{ID: "bug-1", Title: "Bug"}, BotRef{Key: agentPath + "|claude-code", Target: "claude-code", Path: agentPath})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waited, err := inv.Wait(run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waited.Status != InvestigationSucceeded || waited.FinalMessage != "claude final" {
		t.Fatalf("waited = %+v", waited)
	}
}

func TestCodexInvestigatorRejectsUnsupportedBot(t *testing.T) {
	store := NewInvestigationStore(t.TempDir())
	inv := NewCodexInvestigator(store, "codex")
	_, err := inv.Start(context.Background(), Bug{ID: "bug-1", Title: "Bug"}, BotRef{Key: "b|embedded", Target: "embedded", Path: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "不支持") {
		t.Fatalf("err = %v", err)
	}
}

func TestCodexInvestigatorCancelMarksStoredRunCancelled(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nwhile :; do :; done\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	inv := NewCodexInvestigator(store, bin)
	run, err := inv.Start(context.Background(), Bug{ID: "bug-1", Title: "Bug"}, BotRef{Key: "b|codex", Target: "codex", Path: workspace})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := inv.Cancel(run.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got, err := store.Get(run.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != InvestigationCancelled {
		t.Fatalf("run = %+v", got)
	}
}

func TestCodexInvestigatorCancelKillsDescendantHoldingStdout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell descendant stdout inheritance regression is unix-specific")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "codex")
	childPID := filepath.Join(root, "child.pid")
	script := "#!/bin/sh\n(sh -c 'trap \"\" HUP TERM; while :; do sleep 1; done') &\necho $! > " + shellQuote(childPID) + "\nwait\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		data, err := os.ReadFile(childPID)
		if err != nil {
			return
		}
		_ = exec.Command("kill", "-9", strings.TrimSpace(string(data))).Run()
	})
	store := NewInvestigationStore(root)
	inv := NewCodexInvestigator(store, bin)
	run, err := inv.Start(context.Background(), Bug{ID: "bug-1", Title: "Bug"}, BotRef{Key: "b|codex", Target: "codex", Path: workspace})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitForFile(t, childPID, 2*time.Second)

	done := make(chan error, 1)
	go func() {
		done <- inv.Cancel(run.ID)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Cancel timed out while descendant held stdout")
	}

	got, err := store.Get(run.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != InvestigationCancelled {
		t.Fatalf("run = %+v", got)
	}
}

func TestCodexInvestigatorStartsNewRunWhenStoredActiveRunIsOrphaned(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nprintf '%s\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"new final\"}}'\n"
	script += "printf '%s\\n' '{\"type\":\"turn.completed\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	if err := store.Upsert(InvestigationRun{ID: "old-run", BugID: "bug-1", Status: InvestigationRunning}); err != nil {
		t.Fatal(err)
	}
	inv := NewCodexInvestigator(store, bin)
	run, err := inv.Start(context.Background(), Bug{ID: "bug-1", Title: "Bug"}, BotRef{Key: "b|codex", Target: "codex", Path: workspace})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.ID == "old-run" || run.Status != InvestigationRunning {
		t.Fatalf("run = %+v", run)
	}
	oldRun, err := store.Get("old-run")
	if err != nil {
		t.Fatalf("Get old-run: %v", err)
	}
	if oldRun.Status != InvestigationFailed || !strings.Contains(oldRun.Error, "investigation process is not running") {
		t.Fatalf("old run = %+v", oldRun)
	}
	if _, err := inv.Wait(run.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestCodexInvestigatorWaitReturnsPersistenceError(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nsleep 0.2\nprintf '%s\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"final\"}}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	inv := NewCodexInvestigator(store, bin)
	run, err := inv.Start(context.Background(), Bug{ID: "bug-1", Title: "Bug"}, BotRef{Key: "b|codex", Target: "codex", Path: workspace})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := os.Remove(store.Path()); err != nil {
		t.Fatalf("Remove runs.json: %v", err)
	}
	if err := os.Mkdir(store.Path(), 0o700); err != nil {
		t.Fatalf("Mkdir runs.json: %v", err)
	}
	_, err = inv.Wait(run.ID)
	if err == nil {
		t.Fatal("expected persistence error")
	}
}

func phaseForMessageContaining(phasesByMessage map[string]string, needle string) string {
	for message, phase := range phasesByMessage {
		if strings.Contains(message, needle) {
			return phase
		}
	}
	return ""
}

func eventPhases(events []InvestigationEvent) map[string]string {
	out := map[string]string{}
	for _, event := range events {
		if event.Meta == nil {
			continue
		}
		phase, _ := event.Meta["phase"].(string)
		out[event.Message] = phase
	}
	return out
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
