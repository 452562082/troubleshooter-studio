package bughub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var workflowAgentTargets = []string{"codex", "claude-code", "cursor", "opencode"}

func targetResultStream(t *testing.T, target, report string) string {
	t.Helper()
	var payload any
	switch target {
	case "codex":
		data, _ := json.Marshal(map[string]any{"type": "item.completed", "item": map[string]any{"type": "agent_message", "text": report}})
		return string(data) + "\n" + `{"type":"turn.completed"}` + "\n"
	case "opencode":
		data, _ := json.Marshal(map[string]any{"type": "text", "part": map[string]any{"text": report}})
		return string(data) + "\n" + `{"type":"step_finish","part":{"reason":"stop"}}` + "\n"
	case "claude-code", "cursor":
		payload = map[string]any{"type": "result", "subtype": "success", "is_error": false, "result": report}
	default:
		t.Fatal("unknown test target")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}

func targetCLI(t *testing.T, stream, extra string) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "agent")
	script := "#!/bin/sh\nprintf '%s' '" + strings.ReplaceAll(stream, "'", "'\"'\"'") + "'\n" + extra
	if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return binary
}

func TestAllAgentTargetsPhaseExecution(t *testing.T) {
	for _, target := range workflowAgentTargets {
		t.Run(target, func(t *testing.T) {
			inv := NewCodexInvestigator(nil, "")
			bot := BotRef{Key: "bot", Target: target, Path: t.TempDir(), AgentID: "probe"}
			for _, phase := range []string{"investigation", "fix"} {
				t.Run(phase, func(t *testing.T) {
					report := "phase: " + phase + "\nenvironment: test\n"
					inv.SetBinaryForTarget(target, targetCLI(t, targetResultStream(t, target, report), ""))
					result, err := inv.ExecutePhase(context.Background(), phase, bot, "phase prompt", nil)
					if err != nil || strings.TrimSpace(result.FinalYAML) != strings.TrimSpace(report) {
						t.Fatalf("result=%q err=%v", result.FinalYAML, err)
					}
				})
			}
			for _, tc := range []struct{ name, stream, extra string }{
				{"empty output", "", ""},
				{"nonzero after result", targetResultStream(t, target, "report"), "printf 'provider unavailable' >&2\nexit 2\n"},
				{"malformed", "{ broken JSON\n", ""},
			} {
				t.Run(tc.name, func(t *testing.T) {
					inv.SetBinaryForTarget(target, targetCLI(t, tc.stream, tc.extra))
					if _, err := inv.ExecutePhase(context.Background(), tc.name, bot, "prompt", nil); err == nil {
						t.Fatal("invalid execution accepted")
					}
				})
			}
		})
	}
}

func TestAllAgentTargetsCancellation(t *testing.T) {
	for _, target := range workflowAgentTargets {
		t.Run(target, func(t *testing.T) {
			inv := NewCodexInvestigator(nil, "")
			ready := filepath.Join(t.TempDir(), "ready")
			inv.SetBinaryForTarget(target, targetCLI(t, "", "touch '"+ready+"'\nwhile :; do sleep 1; done\n"))
			bot := BotRef{Target: target, Path: t.TempDir(), AgentID: "probe"}
			done := make(chan error, 1)
			go func() { _, err := inv.ExecutePhase(context.Background(), "run", bot, "prompt", nil); done <- err }()
			deadline := time.Now().Add(3 * time.Second)
			for {
				if _, err := os.Stat(ready); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("process did not start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if _, err := inv.ExecutePhase(context.Background(), "run", bot, "prompt", nil); err == nil {
				t.Fatal("duplicate started")
			}
			if err := inv.CancelPhase(context.Background(), "run"); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != context.Canceled {
					t.Fatalf("cancel=%v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("process not stopped")
			}
		})
	}
}

func TestAllAgentTargetsTerminalFailure(t *testing.T) {
	cases := []struct{ target, stream string }{
		{"codex", `{"type":"item.completed","item":{"type":"agent_message","text":"not final"}}` + "\n"},
		{"claude-code", `{"type":"result","subtype":"success","is_error":true,"result":"provider error"}` + "\n"},
		{"cursor", `{"type":"result","subtype":"success","is_error":true,"result":"provider error"}` + "\n"},
	}
	for _, tc := range cases {
		t.Run(tc.target, func(t *testing.T) {
			inv := NewCodexInvestigator(nil, "")
			inv.SetBinaryForTarget(tc.target, targetCLI(t, tc.stream, ""))
			if _, err := inv.ExecutePhase(context.Background(), "failure", BotRef{Target: tc.target, Path: t.TempDir(), AgentID: "probe"}, "prompt", nil); err == nil {
				t.Fatal("incomplete/failed response accepted")
			}
		})
	}
}

func TestAllAgentTargetsParentDeadlineKillsChildren(t *testing.T) {
	for _, target := range workflowAgentTargets {
		t.Run(target, func(t *testing.T) {
			inv := NewCodexInvestigator(nil, "")
			inv.SetBinaryForTarget(target, targetCLI(t, "", "/bin/sleep 60 &\nwait\n"))
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			started := time.Now()
			_, err := inv.ExecutePhase(ctx, "deadline", BotRef{Target: target, Path: t.TempDir(), AgentID: "probe"}, "prompt", nil)
			if err != context.DeadlineExceeded || time.Since(started) > 3*time.Second {
				t.Fatalf("deadline err=%v duration=%s", err, time.Since(started))
			}
		})
	}

}

func TestAllAgentTargetsPhaseProjection(t *testing.T) {
	for _, target := range workflowAgentTargets {
		t.Run(target, func(t *testing.T) {
			store := newOrchestratorStore(t)
			incident := createWorkflowCase(t, store, "case-projection", CaseInvestigating)
			attempt := createPhaseRunnerAttempt(t, store, incident, PhaseInvestigation, "")
			attempt.AgentTarget = target
			if _, err := store.db.Exec(`UPDATE phase_attempts SET agent_target = ? WHERE id = ?`, target, attempt.ID); err != nil {
				t.Fatal(err)
			}
			legacy := NewInvestigationStore(t.TempDir())
			inv := NewCodexInvestigator(legacy, "")
			// Emit a real temporary evidence artifact, as the production runner expects.
			script := `#!/bin/sh
if [ "$1" = "run" ]; then prompt=$(cat); fi
for arg do case "$arg" in *STUDIO_EVIDENCE_STAGING_DIR=*) prompt="$arg";; esac; done
staging=$(printf '%s\n' "$prompt" | sed -n 's/^STUDIO_EVIDENCE_STAGING_DIR=//p' | head -1)
printf '%s' '{"status":"timeout"}' > "$staging/evidence.json"
`
			stream := targetResultStream(t, target, validReproducedPhaseYAML)
			script += "printf '%s' '" + strings.ReplaceAll(stream, "'", "'\"'\"'") + "'\n"
			binary := filepath.Join(t.TempDir(), "agent")
			if err := os.WriteFile(binary, []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			inv.SetBinaryForTarget(target, binary)
			done := make(chan CompleteAttemptCommand, 2)
			runner := NewAgentPhaseRunner(store, inv, legacy, phaseArtifactsRoot(t), func(_ context.Context, cmd CompleteAttemptCommand) error { done <- cmd; return nil })
			bot := installedPhaseRunnerBot(t, "bot", target)
			bot.AgentID = "probe"
			if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, bot); err != nil {
				t.Fatal(err)
			}
			select {
			case cmd := <-done:
				if cmd.Outcome != PhaseOutcomeRootCauseReady {
					t.Fatalf("completion=%+v", cmd)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no completion")
			}
			waitForAgentPhaseRunnerInactive(t, runner, attempt.ID)
			if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, bot); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
				t.Fatal("duplicate completion")
			case <-time.After(30 * time.Millisecond):
			}
			result, err := legacy.Get(attempt.ID)
			if err != nil || strings.TrimSpace(result.FinalMessage) != strings.TrimSpace(validReproducedPhaseYAML) {
				t.Fatalf("projection err=%v", err)
			}
		})
	}
}

func TestClaudeToolProgressDoesNotExposePayloads(t *testing.T) {
	for _, tc := range []struct{ line, kind, state string }{
		{`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"path":"secret-file"}}]}}`, "mcp_tool_call", "started"},
		{`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"secret-command"}}]}}`, "command_execution", "started"},
		{`{"type":"user","message":{"content":[{"type":"tool_result","content":"secret-output"}]}}`, "mcp_tool_call", "completed"},
	} {
		event, final, failed := ParseClaudeStreamJSONEvent([]byte(tc.line))
		if event.Type != tc.kind || event.Meta["state"] != tc.state || final != "" || failed != "" {
			t.Fatalf("event=%+v", event)
		}
		data, _ := json.Marshal(event)
		if strings.Contains(string(data), "secret") {
			t.Fatalf("tool payload leaked: %s", data)
		}
	}
}

func TestMissingAgentResultErrorRedactsStderr(t *testing.T) {
	for _, secret := range []string{"Authorization: Bearer abcdefghijklmnopqrstuvwxyz", "Cookie: session=secret-session-value", "password: correct-horse-battery-staple", "https://user:secret-password@example.com"} {
		got := missingAgentResultError(secret).Error()
		if got != "agent returned no final structured result" {
			t.Fatalf("sensitive stderr exposed: %q", got)
		}
	}
	if got := missingAgentResultError("unknown model").Error(); !strings.Contains(got, "unknown model") {
		t.Fatalf("lost useful diagnostic: %s", got)
	}
}

func TestRetiredTargetCannotRunOrMatch(t *testing.T) {
	bot := BotRef{Target: "openclaw", SystemID: "shop", Path: t.TempDir()}
	if SupportsIncidentWorkflowTarget(bot.Target) {
		t.Fatal("retired target enabled")
	}
	if len(MatchBots(Bug{SystemID: "shop"}, []BotRef{bot})) != 0 {
		t.Fatal("retired bot matched")
	}
	inv := NewCodexInvestigator(nil, "")
	if _, _, err := inv.buildCommand(bot.Target, bot, "probe"); err == nil {
		t.Fatal("retired runner accepted")
	}
}
