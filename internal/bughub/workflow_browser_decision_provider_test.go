package bughub

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestPhaseAgentBrowserDecisionProviderUsesBoundedDecisionOnlyPrompt(t *testing.T) {
	attemptID := "attempt-decision-provider"
	scene := boundBrowserDecisionScene(t, attemptID, []BrowserSceneElement{{
		Ref: "e-1", FrameRef: "f-main", Role: "textbox", Name: "标题", Tag: "input",
		LocatorHints: BrowserSceneLocatorHints{Label: "标题"},
		States: BrowserSceneElementStates{
			Visible: true, InViewport: true, Enabled: true, Editable: true,
		},
		Box: BrowserSceneBox{X: 50, Y: 80, Width: 300, Height: 32},
	}}, nil)
	plan, err := ParseBrowserPlan([]byte(`version: 2
start_url: https://app.test/content
actions:
  - id: fill-title
    action: fill
    locator: {kind: label, value: 标题, exact: true}
    value: 内部测试值-7391
assertions:
  - kind: visible_text
    value: 保存成功
`))
	if err != nil {
		t.Fatal(err)
	}
	var prompt string
	executor := phaseExecutorFunc(func(_ context.Context, id string, _ BotRef, value string, _ func(InvestigationEvent)) (PhaseExecutionResult, error) {
		if id != attemptID {
			t.Fatalf("attempt=%q", id)
		}
		prompt = value
		return PhaseExecutionResult{
			FinalYAML: fmt.Sprintf(`version: 1
decision: conclude
scene_id: %s
rationale_code: evidence_sufficient
conclusion_code: current_evidence_sufficient
`, scene.SceneID),
			Usage: AgentUsage{InputTokens: 7, OutputTokens: 3},
		}, nil
	})
	var usage AgentUsage
	provider := PhaseAgentBrowserDecisionProvider{
		Executor: executor, AttemptID: attemptID, Bot: BotRef{Target: "codex"},
		BasePrompt: "验证当前问题", UsageSink: func(value AgentUsage) { usage = value },
	}
	raw, err := provider.DecideBrowserStep(context.Background(), BrowserDecisionLoopObservation{
		AttemptID: attemptID, StepNo: 1, Scene: scene, Plan: plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := ParseBrowserDecision(raw, scene.SceneID)
	if err != nil || decision.Decision != "conclude" {
		t.Fatalf("decision=%+v err=%v", decision, err)
	}
	if !strings.Contains(prompt, "Studio BrowserDecision step") || !strings.Contains(prompt, scene.SceneID) ||
		strings.Contains(prompt, "内部测试值-7391") || strings.Contains(prompt, `"locator"`) {
		t.Fatalf("unexpected decision prompt: %s", prompt)
	}
	if usage.InputTokens != 7 || usage.OutputTokens != 3 {
		t.Fatalf("usage=%+v", usage)
	}
}

func TestPhaseAgentBrowserDecisionProviderRejectsAttemptMismatchBeforeCall(t *testing.T) {
	calls := 0
	provider := PhaseAgentBrowserDecisionProvider{
		Executor: phaseExecutorFunc(func(context.Context, string, BotRef, string, func(InvestigationEvent)) (PhaseExecutionResult, error) {
			calls++
			return PhaseExecutionResult{}, nil
		}),
		AttemptID: "attempt-a",
	}
	if _, err := provider.DecideBrowserStep(context.Background(), BrowserDecisionLoopObservation{AttemptID: "attempt-b"}); err == nil || calls != 0 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
