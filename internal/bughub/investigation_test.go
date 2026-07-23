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

func TestInvestigationStoreUpsertValidationAndDefaults(t *testing.T) {
	store := NewInvestigationStore(t.TempDir())
	if err := store.Upsert(InvestigationRun{BugID: "b1"}); err == nil {
		t.Fatal("expected empty ID error")
	}
	if err := store.Upsert(InvestigationRun{ID: "run-1"}); err == nil {
		t.Fatal("expected empty BugID error")
	}

	if err := store.Upsert(InvestigationRun{ID: "run-1", BugID: "b1"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	runs, err := store.ListByBug("b1")
	if err != nil {
		t.Fatalf("ListByBug: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs len = %d", len(runs))
	}
	got := runs[0]
	if got.Status != InvestigationQueued {
		t.Fatalf("status = %q", got.Status)
	}
	if got.StartedAt.IsZero() {
		t.Fatal("StartedAt is zero")
	}
	if got.StartedAt.Location() != time.UTC {
		t.Fatalf("StartedAt location = %v", got.StartedAt.Location())
	}

	data, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(data), "finished_at") {
		t.Fatalf("unfinished run serialized finished_at: %s", data)
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
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"}]}}`,
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
		"请作为选定的 AI 排障机器人开始排障",
		"搜索结果错误",
		"zentao:577",
		"target: codex",
		"不要修改代码",
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

func TestBuildOpenClawInvestigationCommand(t *testing.T) {
	cmd, err := BuildOpenClawInvestigationCommand("openclaw", "base", "hello")
	if err != nil {
		t.Fatalf("BuildOpenClawInvestigationCommand: %v", err)
	}
	got := strings.Join(cmd.Args, " ")
	for _, want := range []string{"agent", "--agent base", "--message hello", "--json"} {
		if !strings.Contains(got, want) {
			t.Fatalf("args %q missing %q", got, want)
		}
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

func TestCodexInvestigatorRunsValidationAgentBeforeInvestigationAgent(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	promptsPath := filepath.Join(root, "prompts.txt")
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\n{\n  printf '%s\\n' '---PROMPT---'\n  printf '%s\\n' \"$last\"\n} >> " + shellQuote(promptsPath) + "\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"verification_status: reproduced\\\\ngaps: []\\\\nobserved_behavior: movie shows 一集全\"}}' ;;\n  *) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"rca final\"}}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	inv := NewCodexInvestigator(store, bin)
	run, err := inv.Start(context.Background(), Bug{
		ID:       "bug-1",
		Source:   "zentao",
		SourceID: "577",
		Title:    "电影展示一集全",
		Steps:    "1. 打开搜索页\n2. 搜索电影",
	}, BotRef{Key: "b|codex", Target: "codex", Path: workspace})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waited, err := inv.Wait(run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waited.Status != InvestigationSucceeded || waited.FinalMessage != "rca final" {
		t.Fatalf("waited = %+v", waited)
	}
	data, err := os.ReadFile(promptsPath)
	if err != nil {
		t.Fatalf("ReadFile prompts: %v", err)
	}
	prompts := string(data)
	if got := strings.Count(prompts, "---PROMPT---"); got != 2 {
		t.Fatalf("prompt count = %d\n%s", got, prompts)
	}
	if !strings.Contains(prompts, "你是 Bug 验证 Agent") {
		t.Fatalf("missing validation prompt:\n%s", prompts)
	}
	if strings.Contains(prompts, "复现 Agent") || strings.Contains(prompts, "repro_status") {
		t.Fatalf("prompt still uses repro naming:\n%s", prompts)
	}
	if !strings.Contains(prompts, "## 验证 Agent 报告") || !strings.Contains(prompts, "movie shows 一集全") {
		t.Fatalf("investigation prompt missing validation report:\n%s", prompts)
	}
}

func TestCodexInvestigatorPausesWhenValidationNeedsUserInput(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	promptsPath := filepath.Join(root, "prompts.txt")
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\n{\n  printf '%s\\n' '---PROMPT---'\n  printf '%s\\n' \"$last\"\n} >> " + shellQuote(promptsPath) + "\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"verification_status: insufficient_info\\\\ngaps:\\\\n- 未提供后台账号/登录态，无法确认页面业务数据。\"}}' ;;\n  *) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"rca should not run\"}}' ;;\nesac\n"
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
	if waited.Status != InvestigationSucceeded ||
		!strings.Contains(waited.FinalMessage, "### 验证报告") ||
		!strings.Contains(waited.FinalMessage, "信息不足") ||
		!strings.Contains(waited.FinalMessage, "未提供后台账号") {
		t.Fatalf("waited = %+v", waited)
	}
	data, err := os.ReadFile(promptsPath)
	if err != nil {
		t.Fatalf("ReadFile prompts: %v", err)
	}
	prompts := string(data)
	if got := strings.Count(prompts, "---PROMPT---"); got != 1 {
		t.Fatalf("prompt count = %d, want validation only\n%s", got, prompts)
	}
	var messages []string
	for _, event := range waited.Events {
		if phase, _ := event.Meta["phase"].(string); phase == "validation" {
			messages = append(messages, event.Message)
		}
		if phase, _ := event.Meta["phase"].(string); phase == "investigation" {
			t.Fatalf("unexpected investigation event: %+v", event)
		}
	}
	joined := strings.Join(messages, "\n")
	if !strings.Contains(joined, "验证 Agent 信息不足，已暂停进入排障 Agent") || !strings.Contains(joined, "未提供后台账号") {
		t.Fatalf("validation messages = %q", joined)
	}
}

func TestCodexInvestigatorPausesWhenValidationStatusMissing(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	promptsPath := filepath.Join(root, "prompts.txt")
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\n{\n  printf '%s\\n' '---PROMPT---'\n  printf '%s\\n' \"$last\"\n} >> " + shellQuote(promptsPath) + "\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"验证看起来完成了，但没有结构化状态\"}}' ;;\n  *) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"rca should not run\"}}' ;;\nesac\n"
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
	if waited.Status != InvestigationSucceeded {
		t.Fatalf("waited = %+v", waited)
	}
	data, err := os.ReadFile(promptsPath)
	if err != nil {
		t.Fatalf("ReadFile prompts: %v", err)
	}
	if got := strings.Count(string(data), "---PROMPT---"); got != 1 {
		t.Fatalf("prompt count = %d, want validation only\n%s", got, data)
	}
}

func TestValidationReportReadyForInvestigationRequiresTerminalStatusAndNoGaps(t *testing.T) {
	cases := []struct {
		name   string
		report string
		want   bool
	}{
		{
			name: "reproduced",
			report: `verification_status: reproduced
gaps: []`,
			want: true,
		},
		{
			name: "still reproduces",
			report: `verification_status: still_reproduces
gaps: []`,
			want: true,
		},
		{
			name: "not reproduced pauses before investigation",
			report: `verification_status: not_reproduced
gaps: []`,
			want: false,
		},
		{
			name: "fixed verified pauses before investigation",
			report: `verification_status: fixed_verified
gaps: []`,
			want: false,
		},
		{
			name: "tool limitation is non blocking when gaps empty",
			report: `verification_status: reproduced
handoff_to_troubleshooter:
  unchecked_scopes:
  - in-app browser unavailable: iab
gaps: []`,
			want: true,
		},
		{
			name: "insufficient info",
			report: `verification_status: insufficient_info
gaps:
- 缺少测试账号`,
			want: false,
		},
		{
			name: "missing status",
			report: `observed_behavior: 已打开页面
gaps: []`,
			want: false,
		},
		{
			name: "missing gaps",
			report: `verification_status: reproduced
observed_behavior: 已打开页面`,
			want: false,
		},
		{
			name: "non empty gaps",
			report: `verification_status: reproduced
gaps:
- 仍缺少登录态`,
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validationReportReadyForInvestigation(tc.report); got != tc.want {
				t.Fatalf("validationReportReadyForInvestigation() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFormatValidationFinalReportSummarizesStructuredResult(t *testing.T) {
	report := `verification_status: not_reproduced
environment: test
entry:
  frontend_url: https://example.test/admin
  api_url: /admin/items
observed_behavior: 当前接口返回计数正确
expected_behavior: 历史截图中计数为 0
handoff_to_troubleshooter:
  evidence_summary: GET /admin/items 返回 total=2
  unchecked_scopes:
  - 未采集实时 console
gaps: []`

	formatted := formatValidationFinalReport(report, Bug{}, BotRef{})
	for _, want := range []string{
		"### 验证报告 | test | 未复现",
		"未复现原始 Bug",
		"frontend=https://example.test/admin; api=/admin/items",
		"GET /admin/items 返回 total=2",
		"```yaml",
		"verification_status: not_reproduced",
	} {
		if !strings.Contains(formatted, want) {
			t.Fatalf("formatted report missing %q:\n%s", want, formatted)
		}
	}
}

func TestFormatValidationFinalReportNormalizesCompositeEnv(t *testing.T) {
	report := `verification_status: not_reproduced
environment: bug env: -, bot env: test
observed_behavior: 未复现
gaps: []`

	formatted := formatValidationFinalReport(report, Bug{}, BotRef{Env: "test"})
	if !strings.Contains(formatted, "### 验证报告 | test | 未复现") {
		t.Fatalf("formatted report should normalize env:\n%s", formatted)
	}
	if strings.Contains(formatted, "验证报告 | bug env") {
		t.Fatalf("formatted report leaked composite env:\n%s", formatted)
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

func TestBuildCodexValidationPromptKeepsValidatorEvidenceOnly(t *testing.T) {
	prompt := BuildCodexValidationPrompt(Bug{
		ID:    "577",
		Title: "电影展示一集全",
		Steps: "搜索电影",
	}, BotRef{Target: "codex", Env: "test"})
	for _, want := range []string{
		"只复现场景和收集证据",
		"不要读取业务源码定位函数/行号",
		"不要输出\"代码根因/最可能原因/修复建议/候选原因\"",
		"如需代码分析，交给后续排障 Agent",
		"verification_status",
		"不保证拥有 in-app browser / iab",
		"不要把它本身写入 gaps",
		"后台登录态/测试账号",
		"unchecked_scopes",
		"最终回答不得输出该结构之外的解释性段落",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("validation prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "禅道工单") {
		t.Fatalf("validation prompt should use generic bug platform wording:\n%s", prompt)
	}
}

func TestValidationSkillTemplateMatchesRuntimeOutputContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "templates", "workspace", "skills", "bug-verifier", "SKILL.md.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	templateBlock := fencedYAMLBlock(string(data))
	runtimeBlock := strings.TrimSpace(strings.SplitN(validationOutputContract(), "只有当阻塞资料", 2)[0])
	runtimeBlock = strings.TrimSpace(strings.TrimPrefix(runtimeBlock, "请只输出下面的严格 YAML，不得增加字段或解释性段落："))
	if templateBlock != runtimeBlock {
		t.Fatalf("validator template/runtime contract drift\ntemplate:\n%s\nruntime:\n%s", templateBlock, runtimeBlock)
	}
}

func TestValidatorTemplateUsesStudioBrowserVerifierAsPrimary(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "templates", "workspace", "skills", "bug-verifier", "SKILL.md.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, required := range []string{"Studio BrowserVerifier", "渲染截图", "browser_login_required", "不得直接启动 Playwright"} {
		if !strings.Contains(content, required) {
			t.Fatalf("missing %q", required)
		}
	}
	if strings.Contains(content, "后台运行时不保证有 in-app browser") {
		t.Fatal("obsolete browser downgrade guidance remains")
	}
}

func TestFrontendReproTemplateTreatsBrowserCollectAsManualCompatibilityOnly(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "templates", "workspace", "skills", "frontend-repro-investigator", "SKILL.md.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, required := range []string{"Studio BrowserVerifier", "手动兼容路径", "不属于持久化 Case 证据协议"} {
		if !strings.Contains(content, required) {
			t.Fatalf("missing %q", required)
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

func TestCodexInvestigatorUsesValidatorBotForValidationStage(t *testing.T) {
	root := t.TempDir()
	troubleshooterWorkspace := filepath.Join(root, "troubleshooter")
	validatorWorkspace := filepath.Join(root, "validator")
	for _, dir := range []string{troubleshooterWorkspace, validatorWorkspace} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	callsPath := filepath.Join(root, "calls.txt")
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\n{\n  printf '%s\\n' '---CALL---'\n  pwd\n  printf '%s\\n' \"$last\"\n} >> " + shellQuote(callsPath) + "\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"verification_status: reproduced\\ngaps: []\"}}' ;;\n  *) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"rca final\"}}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	inv := NewCodexInvestigator(store, bin)

	run, err := inv.StartWithValidator(
		context.Background(),
		Bug{ID: "bug-1", Title: "Bug"},
		BotRef{Key: "t|codex", Target: "codex", Path: troubleshooterWorkspace, Role: "troubleshooter"},
		BotRef{Key: "v|codex", Target: "codex", Path: validatorWorkspace, Role: "validator"},
	)
	if err != nil {
		t.Fatalf("StartWithValidator: %v", err)
	}
	waited, err := inv.Wait(run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waited.Status != InvestigationSucceeded || waited.FinalMessage != "rca final" {
		t.Fatalf("waited = %+v", waited)
	}
	data, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatalf("ReadFile calls: %v", err)
	}
	calls := strings.Split(string(data), "---CALL---")
	if len(calls) < 3 {
		t.Fatalf("want two calls, got:\n%s", data)
	}
	if !strings.Contains(calls[1], validatorWorkspace) || !strings.Contains(calls[1], "你是 Bug 验证 Agent") {
		t.Fatalf("validation call should use validator workspace and prompt:\n%s", calls[1])
	}
	if !strings.Contains(calls[2], troubleshooterWorkspace) || !strings.Contains(calls[2], "请作为选定的 AI 排障机器人开始排障") {
		t.Fatalf("investigation call should use troubleshooter workspace and prompt:\n%s", calls[2])
	}
}

func TestCodexInvestigatorContinuesValidationPhaseWithValidatorBot(t *testing.T) {
	root := t.TempDir()
	troubleshooterWorkspace := filepath.Join(root, "troubleshooter")
	validatorWorkspace := filepath.Join(root, "validator")
	for _, dir := range []string{troubleshooterWorkspace, validatorWorkspace} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	callsPath := filepath.Join(root, "calls.txt")
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\n{\n  printf '%s\\n' '---CALL---'\n  pwd\n  printf '%s\\n' \"$last\"\n} >> " + shellQuote(callsPath) + "\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"verification_status: reproduced\\\\ngaps: []\\\\nvalidation continued\"}}' ;;\n  *) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"rca final\"}}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	prevRun := InvestigationRun{
		ID:     "run-prev",
		BugID:  "bug-1",
		Status: InvestigationSucceeded,
		Events: []InvestigationEvent{{
			Type:    "agent_message",
			Message: "previous validation",
			Meta:    map[string]any{"phase": "validation"},
		}},
	}
	if err := store.Upsert(prevRun); err != nil {
		t.Fatalf("Upsert previous run: %v", err)
	}
	inv := NewCodexInvestigator(store, bin)
	run, err := inv.Continue(
		context.Background(),
		Bug{ID: "bug-1", Title: "Bug"},
		BotRef{
			Key:      "t|codex",
			Target:   "codex",
			Path:     troubleshooterWorkspace,
			SystemID: "base",
			Role:     "troubleshooter",
			InternalAgents: []BotInternalAgent{
				{ID: "troubleshooter", Role: "troubleshooter"},
				{ID: "validator", Role: "validator"},
			},
		},
		"补充验证账号",
		prevRun.ID,
		"validation",
	)
	if err != nil {
		t.Fatalf("Continue validation: %v", err)
	}
	waited, err := inv.Wait(run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waited.Status != InvestigationSucceeded || waited.BotKey != "t|codex" || waited.FinalMessage != "rca final" {
		t.Fatalf("waited = %+v", waited)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatalf("ReadFile calls: %v", err)
	}
	callParts := strings.Split(string(calls), "---CALL---")
	if len(callParts) < 3 {
		t.Fatalf("want validation + investigation calls, got:\n%s", calls)
	}
	if !strings.Contains(callParts[1], validatorWorkspace) || !strings.Contains(callParts[1], "你是 Bug 验证 Agent") {
		t.Fatalf("validation continuation should use validator workspace and prompt:\n%s", callParts[1])
	}
	if !strings.Contains(callParts[2], troubleshooterWorkspace) || !strings.Contains(callParts[2], "请作为选定的 AI 排障机器人开始排障") {
		t.Fatalf("validation continuation should enter investigation after evidence:\n%s", callParts[2])
	}
	phasesByMessage := map[string]string{}
	for _, event := range waited.Events {
		if event.Meta != nil {
			phase, _ := event.Meta["phase"].(string)
			phasesByMessage[event.Message] = phase
		}
	}
	if phasesByMessage["验证 Agent 继续取证（基于用户补充信息）"] != "validation" || phasesByMessage["rca final"] != "investigation" || phaseForMessageContaining(phasesByMessage, "validation continued") != "validation" {
		t.Fatalf("phasesByMessage = %+v", phasesByMessage)
	}
}

func TestCodexInvestigatorKeepsValidationContinuationPausedWithoutEvidence(t *testing.T) {
	root := t.TempDir()
	troubleshooterWorkspace := filepath.Join(root, "troubleshooter")
	validatorWorkspace := filepath.Join(root, "validator")
	for _, dir := range []string{troubleshooterWorkspace, validatorWorkspace} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	callsPath := filepath.Join(root, "calls.txt")
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\n{\n  printf '%s\\n' '---CALL---'\n  pwd\n  printf '%s\\n' \"$last\"\n} >> " + shellQuote(callsPath) + "\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"verification_status: insufficient_info\\\\ngaps:\\\\n- 仍缺少测试账号\"}}' ;;\n  *) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"rca should not run\"}}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	prevRun := InvestigationRun{ID: "run-prev", BugID: "bug-1", Status: InvestigationSucceeded}
	if err := store.Upsert(prevRun); err != nil {
		t.Fatalf("Upsert previous run: %v", err)
	}
	inv := NewCodexInvestigator(store, bin)
	run, err := inv.Continue(
		context.Background(),
		Bug{ID: "bug-1", Title: "Bug"},
		BotRef{
			Key:      "t|codex",
			Target:   "codex",
			Path:     troubleshooterWorkspace,
			SystemID: "base",
			InternalAgents: []BotInternalAgent{
				{ID: "troubleshooter", Role: "troubleshooter"},
				{ID: "validator", Role: "validator"},
			},
		},
		"继续补充",
		prevRun.ID,
		"validation",
	)
	if err != nil {
		t.Fatalf("Continue validation: %v", err)
	}
	waited, err := inv.Wait(run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waited.Status != InvestigationSucceeded ||
		!strings.Contains(waited.FinalMessage, "### 验证报告") ||
		!strings.Contains(waited.FinalMessage, "信息不足") ||
		!strings.Contains(waited.FinalMessage, "仍缺少测试账号") {
		t.Fatalf("waited = %+v", waited)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatalf("ReadFile calls: %v", err)
	}
	if got := strings.Count(string(calls), "---CALL---"); got != 1 {
		t.Fatalf("want validation only, got %d calls:\n%s", got, calls)
	}
}

func TestCodexInvestigatorPausesWhenValidationDoesNotReproduce(t *testing.T) {
	root := t.TempDir()
	troubleshooterWorkspace := filepath.Join(root, "troubleshooter")
	validatorWorkspace := filepath.Join(root, "validator")
	for _, dir := range []string{troubleshooterWorkspace, validatorWorkspace} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	callsPath := filepath.Join(root, "calls.txt")
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\n{\n  printf '%s\\n' '---CALL---'\n  pwd\n  printf '%s\\n' \"$last\"\n} >> " + shellQuote(callsPath) + "\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"verification_status: not_reproduced\\\\ngaps: []\\\\nobserved_behavior: 未复现原始问题\"}}' ;;\n  *) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"rca should not run\"}}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	inv := NewCodexInvestigator(store, bin)
	run, err := inv.StartWithValidator(
		context.Background(),
		Bug{ID: "bug-1", Title: "Bug"},
		BotRef{Key: "t|codex", Target: "codex", Path: troubleshooterWorkspace, Role: "troubleshooter"},
		BotRef{Key: "v|codex", Target: "codex", Path: validatorWorkspace, Role: "validator"},
	)
	if err != nil {
		t.Fatalf("StartWithValidator: %v", err)
	}
	waited, err := inv.Wait(run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waited.Status != InvestigationSucceeded ||
		!strings.Contains(waited.FinalMessage, "### 验证报告") ||
		!strings.Contains(waited.FinalMessage, "未复现") ||
		!strings.Contains(waited.FinalMessage, "未复现原始 Bug") {
		t.Fatalf("waited = %+v", waited)
	}
	calls, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatalf("ReadFile calls: %v", err)
	}
	if got := strings.Count(string(calls), "---CALL---"); got != 1 {
		t.Fatalf("want validation only, got %d calls:\n%s", got, calls)
	}
	if !strings.Contains(string(calls), "verification_status") || strings.Contains(string(calls), "请作为选定的 AI 排障机器人开始排障") {
		t.Fatalf("unexpected calls:\n%s", calls)
	}
	if phaseForMessageContaining(eventPhases(waited.Events), "未复现原始 Bug") != "validation" {
		t.Fatalf("missing not reproduced pause event: %+v", waited.Events)
	}
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

func TestCodexInvestigatorEmitsValidationStageEvents(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"verification_status: reproduced\\\\ngaps: []\\\\nvalidation report\"}}' ;;\n  *) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"final report\"}}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	inv := NewCodexInvestigator(store, bin)
	got := make(chan InvestigationEvent, 8)
	inv.SetEventSink(func(run InvestigationRun, event InvestigationEvent) {
		got <- event
	})
	run, err := inv.Start(context.Background(), Bug{ID: "bug-1", Title: "Bug"}, BotRef{Key: "b|codex", Target: "codex", Path: workspace})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := inv.Wait(run.ID); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	var messages []string
	deadline := time.After(time.Second)
	for {
		select {
		case event := <-got:
			if event.Type == "stage" {
				if event.Meta["phase"] != "validation" {
					t.Fatalf("stage phase = %+v", event.Meta)
				}
				messages = append(messages, event.Message)
			}
			if len(messages) >= 2 {
				if messages[0] != "验证 Agent 开始取证验证" || messages[1] != "验证 Agent 完成，已将证据交给排障 Agent" {
					t.Fatalf("stage messages = %+v", messages)
				}
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for stage events, got %+v", messages)
		}
	}
}

func TestCodexInvestigatorTagsValidationAndInvestigationEvents(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "codex")
	script := "#!/bin/sh\nlast=\"\"\nfor arg in \"$@\"; do last=\"$arg\"; done\ncase \"$last\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"verification_status: reproduced\\\\ngaps: []\\\\nvalidation evidence\"}}' ;;\n  *) printf '%s\\n' '{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"investigation evidence\"}}' ;;\nesac\n"
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
	phasesByMessage := map[string]string{}
	for _, event := range waited.Events {
		if event.Message != "" && event.Meta != nil {
			if phase, _ := event.Meta["phase"].(string); phase != "" {
				phasesByMessage[event.Message] = phase
			}
		}
	}
	if phaseForMessageContaining(phasesByMessage, "validation evidence") != "validation" {
		t.Fatalf("validation event phase map = %+v", phasesByMessage)
	}
	if phasesByMessage["investigation evidence"] != "investigation" {
		t.Fatalf("investigation event phase map = %+v", phasesByMessage)
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
	if !strings.Contains(validationJoined, "开始验证") || !strings.Contains(validationJoined, "验证完成") {
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

func TestCodexInvestigatorRunsFakeOpenClaw(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, "openclaw")
	script := "#!/bin/sh\nall=\"$*\"\ncase \"$all\" in\n  *你是\\ Bug\\ 验证\\ Agent*) printf '%s\\n' '{\"ok\":true,\"reply\":\"verification_status: reproduced\\ngaps: []\"}' ;;\n  *) printf '%s\\n' '{\"ok\":true,\"reply\":\"openclaw final\"}' ;;\nesac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	store := NewInvestigationStore(root)
	inv := NewCodexInvestigator(store, "codex")
	inv.SetBinaryForTarget("openclaw", bin)
	run, err := inv.Start(context.Background(), Bug{ID: "bug-1", Title: "Bug"}, BotRef{Key: "base|openclaw", Target: "openclaw", Path: "base", SystemID: "base"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waited, err := inv.Wait(run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if waited.Status != InvestigationSucceeded || waited.FinalMessage != "openclaw final" {
		t.Fatalf("waited = %+v", waited)
	}
}

func TestCodexInvestigatorRejectsUnsupportedBot(t *testing.T) {
	store := NewInvestigationStore(t.TempDir())
	inv := NewCodexInvestigator(store, "codex")
	_, err := inv.Start(context.Background(), Bug{ID: "bug-1", Title: "Bug"}, BotRef{Key: "b|cursor", Target: "cursor", Path: t.TempDir()})
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
