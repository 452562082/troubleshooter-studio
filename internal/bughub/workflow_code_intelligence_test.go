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

func TestMaterializeCodeIntelligenceUsesHostPreparedManifest(t *testing.T) {
	root := phaseArtifactsRoot(t)
	staging, err := openAttemptEvidenceStaging(root, "attempt-codegraph-manifest")
	if err != nil {
		t.Fatal(err)
	}
	defer staging.Close()
	defer staging.Cleanup()

	runner := NewAgentPhaseRunner(nil, nil, nil, root, nil)
	manifest, prompt, err := runner.materializeCodeIntelligence(context.Background(), PhaseAttempt{Phase: PhaseInvestigation}, IncidentCase{Environment: "test"}, staging, CodeIntelligenceResolverFunc(func(context.Context, IncidentCase) (CodeIntelligenceManifest, error) {
		return CodeIntelligenceManifest{
			Enabled: true, Provider: "codegraph", Ready: 1, Total: 1,
			Repositories: []CodeIntelligenceRepository{{Repo: "api", ProjectPath: "/tmp/codegraph/api", Status: "ready", CurrentBranch: "feature/local", TargetBranch: "test", Head: "abc123", FileCount: 12, NodeCount: 34}},
		}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.HasReadyRepository() || !strings.Contains(prompt, "codegraph_explore") || !strings.Contains(prompt, "不得执行 CodeGraph CLI") {
		t.Fatalf("manifest=%+v prompt=%q", manifest, prompt)
	}
	if strings.Contains(prompt, "$HOME/.tshoot/bin/codegraph") || strings.Contains(prompt, "codegraph status") || strings.Contains(prompt, "codegraph sync") {
		t.Fatalf("prompt still asks the sandboxed Agent to run CodeGraph CLI: %q", prompt)
	}

	data, err := os.ReadFile(filepath.Join(staging.Path(), codeIntelligenceManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var stored CodeIntelligenceManifest
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Version != 1 || stored.PreparedAt.IsZero() || stored.Repositories[0].ProjectPath != "/tmp/codegraph/api" {
		t.Fatalf("stored manifest=%+v", stored)
	}
}

func TestCodeGraphReceiptRequiresRealMCPToolCall(t *testing.T) {
	if !eventProvesCodeGraphQuery(InvestigationEvent{Type: "mcp_tool_call", Message: "codegraph_explore", Meta: map[string]any{"state": "completed"}}) {
		t.Fatal("completed codegraph_explore MCP call was not recognized")
	}
	event, _, _ := ParseCodexJSONLEvent([]byte(`{"type":"item.completed","item":{"type":"mcp_tool_call","name":"mcp__shop-codegraph__codegraph_explore"}}`))
	if !eventProvesCodeGraphQuery(event) {
		t.Fatalf("real Codex MCP completion was not recognized: %+v", event)
	}
	if eventProvesCodeGraphQuery(InvestigationEvent{Type: "command_execution", Message: "$HOME/.tshoot/bin/codegraph status /repo --json"}) {
		t.Fatal("CodeGraph CLI status must not count as an actual graph query")
	}
	if eventProvesCodeGraphQuery(InvestigationEvent{Type: "mcp_tool_call", Message: "query_loki_logs", Meta: map[string]any{"state": "completed"}}) {
		t.Fatal("unrelated MCP call counted as CodeGraph usage")
	}
}

func TestAgentPhaseRunnerRetriesCodeRootCauseUntilCodeGraphIsQueried(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-codegraph-receipt", CaseInvestigating)
	attempt := createPhaseRunnerAttempt(t, store, incident, PhaseInvestigation, "")
	const result = `investigation_status: root_cause_ready
environment: test
root_cause: API maps the wrong user field
confidence: high
root_cause_type: code
remediation:
  mode: code_change
  repositories: [api]
  target: user response mapper
  summary: map the signature field according to the API contract
  verification: rerun the original request
call_chain:
  - kind: service
    name: user mapper
    repo: api
    precision: static_candidate
    evidence: source and runtime response agree
evidence: []
validation_gaps: []
gaps: []
unchecked_scopes: []
`

	var calls int
	executor := phaseExecutorFunc(func(_ context.Context, _ string, _ BotRef, prompt string, emit func(InvestigationEvent)) (PhaseExecutionResult, error) {
		calls++
		if calls == 2 {
			if !strings.Contains(prompt, "Mandatory CodeGraph evidence retry") {
				t.Fatalf("retry prompt=%q", prompt)
			}
			emit(InvestigationEvent{Type: "mcp_tool_call", Message: "codegraph_explore", Meta: map[string]any{"state": "completed"}})
		}
		return PhaseExecutionResult{FinalYAML: result}, nil
	})
	completed := make(chan CompleteAttemptCommand, 1)
	runner := NewAgentPhaseRunner(store, executor, nil, phaseArtifactsRoot(t), func(_ context.Context, command CompleteAttemptCommand) error {
		completed <- command
		return nil
	})
	runner.SetCodeIntelligenceResolver(CodeIntelligenceResolverFunc(func(context.Context, IncidentCase) (CodeIntelligenceManifest, error) {
		return CodeIntelligenceManifest{
			Enabled: true, Provider: "codegraph", Ready: 1, Total: 1,
			Repositories: []CodeIntelligenceRepository{{Repo: "api", ProjectPath: "/tmp/codegraph/api", Status: "ready", CurrentBranch: "feature/local", TargetBranch: "test", Head: "abc123"}},
			PreparedAt:   time.Now().UTC(),
		}, nil
	}))
	if err := runner.Start(context.Background(), attempt, Bug{ID: incident.BugID}, installedPhaseRunnerBot(t, "bot", "codex")); err != nil {
		t.Fatal(err)
	}
	command := <-completed
	if calls != 2 || command.Outcome != PhaseOutcomeRootCauseReady || command.ErrorCode != "" {
		t.Fatalf("calls=%d completion=%+v", calls, command)
	}
}
