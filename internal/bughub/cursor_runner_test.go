package bughub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func cursorTestCLI(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cursor-agent")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func cursorResultScript(t *testing.T, result string) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": result})
	if err != nil {
		t.Fatal(err)
	}
	return "printf '%s\\n' '" + strings.ReplaceAll(string(line), "'", "'\"'\"'") + "'\n"
}

func TestFindCursorCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	if _, err := FindCursorCLI(); err == nil {
		t.Fatal("missing CLI accepted")
	}
	binary := cursorTestCLI(t, "exit 0\n")
	t.Setenv("PATH", filepath.Dir(binary))
	if got, err := FindCursorCLI(); err != nil || got != binary {
		t.Fatalf("PATH lookup = %q, %v", got, err)
	}
	t.Setenv("PATH", t.TempDir())
	homeBin := filepath.Join(os.Getenv("HOME"), ".local", "bin", "cursor-agent")
	if err := os.MkdirAll(filepath.Dir(homeBin), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(homeBin, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if got, err := FindCursorCLI(); err != nil || got != homeBin {
		t.Fatalf("GUI lookup = %q, %v", got, err)
	}
}

func TestFindCursorCLIRejectsUnrelatedAgent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	path := filepath.Join(dir, "agent")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'unrelated agent'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := FindCursorCLI(); err == nil {
		t.Fatal("unrelated agent accepted")
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'Start the Cursor Agent'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if got, err := FindCursorCLI(); err != nil || got != path {
		t.Fatalf("Cursor agent alias = %q, %v", got, err)
	}
}

func TestBuildCursorCommand(t *testing.T) {
	workspace := t.TempDir()
	prompt := "investigate\n$(echo should-not-run) --resume other"
	cmd, err := BuildCursorInvestigationCommand("/test/cursor-agent", workspace, prompt)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/test/cursor-agent", "--print", "--output-format", "stream-json", "--force", "--trust", "--approve-mcps", "--", prompt}
	if !reflect.DeepEqual(cmd.Args, want) || cmd.Dir != workspace {
		t.Fatalf("command = %#v, dir=%s", cmd.Args, cmd.Dir)
	}
	for _, path := range []string{"", filepath.Join(workspace, "missing"), cursorTestCLI(t, "exit 0")} {
		if _, err := BuildCursorInvestigationCommand("test", path, "prompt"); err == nil {
			t.Fatalf("invalid workspace accepted: %s", path)
		}
	}
}

func TestCursorStreamProtocol(t *testing.T) {
	tests := []struct {
		name, line, kind, final string
		failed                  bool
	}{
		{"init", `{"type":"system","subtype":"init"}`, "thread_started", "", false},
		{"prompt echo", `{"type":"user","message":{"content":[{"type":"text","text":"secret prompt"}]}}`, "", "", false},
		{"assistant", `{"type":"assistant","message":{"content":[{"type":"text","text":"progress"}]}}`, "agent_message", "", false},
		{"step", `{"type":"assistant","message":{"content":[{"type":"text","text":"[[TSHOOT_STEP phase=investigation index=2 key=timeline]]"}]}}`, "phase_step", "", false},
		{"read", `{"type":"tool_call","subtype":"started","tool_call":{"readToolCall":{"args":{"path":"secret-path"}}}}`, "mcp_tool_call", "", false},
		{"shell", `{"type":"tool_call","subtype":"completed","tool_call":{"shellToolCall":{"args":{"command":"secret-command"},"result":{"output":"secret-output"}}}}`, "command_execution", "", false},
		{"success", `{"type":"result","subtype":"success","is_error":false,"result":"report"}`, "result", "report", false},
		{"error flag", `{"type":"result","subtype":"success","is_error":true,"result":"failed"}`, "result", "", true},
		{"error subtype", `{"type":"result","subtype":"error","result":"failed"}`, "result", "", true},
		{"unknown terminal", `{"type":"result","result":"not confirmed"}`, "result", "", true},
		{"error", `{"type":"error","message":"failed"}`, "turn_failed", "", true},
		{"noise", "not JSON secret", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, final, failed := ParseCursorStreamJSONEvent([]byte(tt.line))
			if event.Type != tt.kind || final != tt.final || (failed != "") != tt.failed {
				t.Fatalf("event=%+v final=%q failed=%q", event, final, failed)
			}
			data, _ := json.Marshal(event)
			if strings.Contains(string(data), "secret") {
				t.Fatalf("tool/prompt leaked: %s", data)
			}
		})
	}
}

func TestCursorPhaseExecution(t *testing.T) {
	for _, phase := range []string{"investigation", "fix"} {
		t.Run(phase, func(t *testing.T) {
			inv := NewCodexInvestigator(NewInvestigationStore(t.TempDir()), "")
			inv.SetBinaryForTarget("cursor", cursorTestCLI(t, cursorResultScript(t, "phase: "+phase)))
			result, err := inv.ExecutePhase(context.Background(), "attempt-"+phase, BotRef{Target: "cursor", Path: t.TempDir()}, "authorized "+phase, nil)
			if err != nil || result.FinalYAML != "phase: "+phase {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
	for _, tt := range []struct{ name, script, want string }{
		{"missing final", "printf '%s\\n' '{\"type\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"not final\"}]}}'\n", "no final structured result"},
		{"nonzero", cursorResultScript(t, "report") + "printf 'login required' >&2\nexit 1\n", "login required"},
		{"terminal error", "printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":true,\"result\":\"permission denied\"}'\n", "permission denied"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inv := NewCodexInvestigator(nil, "")
			inv.SetBinaryForTarget("cursor", cursorTestCLI(t, tt.script))
			_, err := inv.ExecutePhase(context.Background(), tt.name, BotRef{Target: "cursor", Path: t.TempDir()}, "prompt", nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCursorPhaseCancellationAndDuplicate(t *testing.T) {
	inv := NewCodexInvestigator(nil, "")
	inv.SetBinaryForTarget("cursor", cursorTestCLI(t, "printf '%s\\n' '{\"type\":\"system\",\"subtype\":\"init\"}'\nwhile :; do sleep 1; done\n"))
	started := make(chan struct{}, 1)
	done := make(chan error, 1)
	bot := BotRef{Target: "cursor", Path: t.TempDir()}
	go func() {
		_, err := inv.ExecutePhase(context.Background(), "cancel", bot, "prompt", func(InvestigationEvent) { started <- struct{}{} })
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("did not start")
	}
	if _, err := inv.ExecutePhase(context.Background(), "cancel", bot, "prompt", nil); err == nil {
		t.Fatal("duplicate accepted")
	}
	if err := inv.CancelPhase(context.Background(), "cancel"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("cancel = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("process did not stop")
	}
}

func TestCursorPhaseRunnerProjectsValidatedResult(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-cursor", CaseInvestigating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseInvestigation, "")
	attempt.AgentTarget = "cursor"
	if _, err := store.db.Exec(`UPDATE phase_attempts SET agent_target = ? WHERE id = ?`, attempt.AgentTarget, attempt.ID); err != nil {
		t.Fatal(err)
	}
	legacy := NewInvestigationStore(t.TempDir())
	inv := NewCodexInvestigator(legacy, "")
	evidenceScript := `for arg do prompt="$arg"; done
staging=$(printf '%s\n' "$prompt" | sed -n 's/^STUDIO_EVIDENCE_STAGING_DIR=//p' | head -1)
printf '%s' '{"status":"timeout"}' > "$staging/evidence.json"
`
	inv.SetBinaryForTarget("cursor", cursorTestCLI(t, evidenceScript+cursorResultScript(t, validReproducedPhaseYAML)))
	completed := make(chan CompleteAttemptCommand, 2)
	runner := NewAgentPhaseRunner(store, inv, legacy, phaseArtifactsRoot(t), func(_ context.Context, cmd CompleteAttemptCommand) error { completed <- cmd; return nil })
	bot := installedPhaseRunnerBot(t, "bot", "cursor")
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, bot); err != nil {
		t.Fatal(err)
	}
	select {
	case cmd := <-completed:
		if cmd.Outcome != PhaseOutcomeRootCauseReady {
			t.Fatalf("completion = %+v", cmd)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no completion")
	}
	waitForAgentPhaseRunnerInactive(t, runner, attempt.ID)
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, bot); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
		t.Fatal("duplicate completion")
	case <-time.After(50 * time.Millisecond):
	}
	run, err := legacy.Get(attempt.ID)
	if err != nil || run.FinalMessage != validReproducedPhaseYAML {
		t.Fatalf("projection = %+v, %v", run, err)
	}
}

func TestCursorPhaseAttachments(t *testing.T) {
	attachment, cleanup := testPhaseScreenshotAttachment(t)
	defer func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	}()
	inv := NewCodexInvestigator(nil, "")
	bin := cursorTestCLI(t, "printf '%s\\n' \"$@\" > arguments.txt\n"+cursorResultScript(t, "report"))
	inv.SetBinaryForTarget("cursor", bin)
	workspace := t.TempDir()
	bot := BotRef{Target: "cursor", Path: workspace}
	result, err := inv.ExecutePhaseWithAttachments(context.Background(), "attachment", bot, "review supplied evidence", []PhaseAttachment{attachment}, nil)
	if err != nil || result.FinalYAML != "report" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	args, err := os.ReadFile(filepath.Join(workspace, "arguments.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), attachment.Path) {
		t.Fatal("trusted evidence path missing")
	}
	inv.SetBinaryForTarget("cursor", cursorTestCLI(t, cursorResultScript(t, attachment.Path)))
	if _, err := inv.ExecutePhaseWithAttachments(context.Background(), "echo", bot, "prompt", []PhaseAttachment{attachment}, nil); err == nil {
		t.Fatal("attachment path echo accepted")
	}
}

func TestCursorLegacyRunRequiresTerminalResult(t *testing.T) {
	for _, tt := range []struct {
		name, script string
		want         InvestigationStatus
	}{
		{"success", cursorResultScript(t, "root cause report"), InvestigationSucceeded},
		{"missing result", "exit 0\n", InvestigationFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inv := NewCodexInvestigator(NewInvestigationStore(t.TempDir()), "")
			inv.SetBinaryForTarget("cursor", cursorTestCLI(t, tt.script))
			run, err := inv.Start(context.Background(), Bug{ID: "bug", Title: "issue"}, BotRef{Target: "cursor", Path: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			_, waitErr := inv.Wait(run.ID)
			if tt.want == InvestigationSucceeded && waitErr != nil {
				t.Fatal(waitErr)
			}
			final, err := inv.store.Get(run.ID)
			if err != nil || final.Status != tt.want {
				t.Fatalf("run=%+v err=%v", final, err)
			}
		})
	}
}

func TestCursorToolEventIgnoresRuntimeMetadata(t *testing.T) {
	for _, state := range []string{"started", "completed"} {
		line, _ := json.Marshal(map[string]any{"type": "tool_call", "subtype": state, "tool_call": map[string]any{
			"readToolCall":           map[string]any{"args": map[string]any{"path": "probe.txt"}},
			"hookAdditionalContexts": []string{"private hook context"}, "toolCallId": "call-1", "startedAtMs": 123, "completedAtMs": 456,
		}})
		event, _, _ := ParseCursorStreamJSONEvent(line)
		if event.Message != "readToolCall" || event.Meta["state"] != state {
			t.Fatalf("tool event = %+v", event)
		}
	}
}

func TestCursorTerminalResultSeparatesCommentary(t *testing.T) {
	assistant := func(text string) []byte {
		line, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}})
		return line
	}
	terminal := func(text string, failed bool) []byte {
		line, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": failed, "result": text})
		return line
	}
	for _, tc := range []struct {
		name, terminal, want string
		failed               bool
	}{
		{"actual concatenated result", "I will read the evidence.status: ok\ntoken: READY", "status: ok\ntoken: READY", false},
		{"distinct result", "different terminal report", "different terminal report", false},
		{"missing result", "", "", false},
		{"terminal failure", "I will read the evidence.status: ok\ntoken: READY", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parser := newCursorStreamJSONParser()
			for _, text := range []string{"I will read the evidence.", "status: ok\ntoken: READY"} {
				_, final, err := parser(assistant(text))
				if final != "" || err != "" {
					t.Fatal("assistant text promoted before terminal result")
				}
			}
			_, got, failed := parser(terminal(tc.terminal, tc.failed))
			if got != tc.want || (failed != "") != tc.failed {
				t.Fatalf("final=%q failed=%q", got, failed)
			}
			// A second process must not inherit the last assistant message.
			_, fresh, _ := newCursorStreamJSONParser()(terminal(tc.terminal, false))
			if fresh != tc.terminal {
				t.Fatalf("state leaked to another process: %q", fresh)
			}
		})
	}
}

func TestCursorPhaseResultExcludesPreToolCommentary(t *testing.T) {
	inv := NewCodexInvestigator(nil, "")
	script := `printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"Reading evidence."}]}}' '{"type":"assistant","message":{"content":[{"type":"text","text":"investigation_status: insufficient_info\nenvironment: test\nconfidence: low\nevidence: []\ngaps: [missing runtime evidence]\n"}]}}'
`
	report := "investigation_status: insufficient_info\nenvironment: test\nconfidence: low\nevidence: []\ngaps: [missing runtime evidence]\n"
	inv.SetBinaryForTarget("cursor", cursorTestCLI(t, script+cursorResultScript(t, "Reading evidence."+report)))
	result, err := inv.ExecutePhase(context.Background(), "cursor-commentary", BotRef{Target: "cursor", Path: t.TempDir()}, "prompt", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.FinalYAML != report {
		t.Fatalf("final = %q", result.FinalYAML)
	}
	if _, err := ParseInvestigationResult([]byte(result.FinalYAML)); err != nil {
		t.Fatalf("invalid phase report: %v", err)
	}
}

// Sanitized real CLI capture: retains the observed message concatenation and
// tool metadata layout, but no account, session, local path or thinking content.
func TestCursorRecordedRuntimeStream(t *testing.T) {
	data, err := os.ReadFile("testdata/cursor-20260910-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	parser := newCursorStreamJSONParser()
	var final string
	reads := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		event, result, failed := parser([]byte(line))
		if failed != "" {
			t.Fatal(failed)
		}
		if event.Type == "mcp_tool_call" {
			if event.Message != "readToolCall" {
				t.Fatalf("metadata mistaken for tool: %+v", event)
			}
			reads++
		}
		if result != "" {
			final = result
		}
	}
	if reads != 2 || final != "status: ok\ntoken: TSHOOT_CURSOR_READY_914" {
		t.Fatalf("reads=%d final=%q", reads, final)
	}
}
