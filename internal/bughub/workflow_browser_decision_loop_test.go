package bughub

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

type scriptedBoundBrowserStepExecutor struct {
	Calls   int
	Results []BoundBrowserStepExecutionResult
}

func (executor *scriptedBoundBrowserStepExecutor) ExecuteBoundBrowserStep(_ context.Context, _ BoundBrowserStepExecutionRequest) (BoundBrowserStepExecutionResult, error) {
	executor.Calls++
	if len(executor.Results) == 0 {
		return BoundBrowserStepExecutionResult{}, fmt.Errorf("scripted bound browser executor exhausted")
	}
	result := executor.Results[0]
	executor.Results = executor.Results[1:]
	return result, nil
}

func TestBrowserDecisionLoopCorrectsInvalidDecisionThenCommitsScenarioStep(t *testing.T) {
	store := openTestCaseStore(t)
	attempt := browserDecisionStepStoreFixture(t, store, "decision-loop-scenario")
	before := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{{
		Ref: "e-1", FrameRef: "f-main", Role: "button", Name: "查看", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{TestID: "view-button"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 100, Y: 200, Width: 80, Height: 32},
		Relations:    BrowserSceneElementRelations{RowName: "测试都市生活剧"},
	}}, nil)
	after := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{{
		Ref: "e-1", FrameRef: "f-main", SurfaceRef: "s-1", Role: "button", Name: "关闭", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{Label: "关闭"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 900, Y: 100, Width: 64, Height: 32},
	}}, &BrowserSceneSurface{Ref: "s-1", Type: "dialog", Name: "视频详情", Modal: true})
	providerCalls := 0
	provider := BrowserDecisionProviderFunc(func(_ context.Context, observation BrowserDecisionLoopObservation) ([]byte, error) {
		providerCalls++
		switch providerCalls {
		case 1:
			return []byte("not: a valid decision\n"), nil
		case 2:
			return []byte(fmt.Sprintf(`version: 1
decision: act
scene_id: %s
rationale_code: scenario_next_step
action: {action_id: open-row, type: click, element_ref: e-1}
expected_effect:
  any_of: [{kind: surface_opened, role: dialog, name_contains: 视频详情}]
`, observation.Scene.SceneID)), nil
		default:
			return []byte(fmt.Sprintf(`version: 1
decision: conclude
scene_id: %s
rationale_code: evidence_sufficient
conclusion_code: current_evidence_sufficient
`, observation.Scene.SceneID)), nil
		}
	})
	guard, err := NewBrowserExplorationGuard(DefaultBrowserExplorationBudget())
	if err != nil {
		t.Fatal(err)
	}
	executor := &fakeBoundBrowserStepExecutor{Result: BoundBrowserStepExecutionResult{
		After: after, AfterSceneRef: "browser-scenes/loop-after.json",
		Effect: BrowserStepEffectEvaluation{Outcome: BrowserEffectConfirmed, ConfirmedKinds: []string{"surface_opened"}},
	}}
	var metricEvents []InvestigationEvent
	loop := BrowserDecisionLoop{
		Provider: provider, Transactions: BrowserStepTransactionCoordinator{Store: store},
		Executor: executor, Exploration: guard, Emit: func(event InvestigationEvent) { metricEvents = append(metricEvents, event) },
	}
	result, err := loop.Run(context.Background(), BrowserDecisionLoopRequest{
		AttemptID: attempt.ID, Plan: browserDecisionBindingPlan(t), InitialScene: before,
		InitialSceneRef: "browser-scenes/loop-before.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != BrowserDecisionLoopConcluded || providerCalls != 3 || executor.Calls != 1 || len(result.Steps) != 1 || result.Steps[0].Outcome != BrowserEffectConfirmed || result.Scene.SceneID != after.SceneID {
		t.Fatalf("result=%+v provider=%d executor=%d", result, providerCalls, executor.Calls)
	}
	if result.Metrics.DecisionCalls != 3 || result.Metrics.InvalidDecisions != 1 || result.Metrics.ScenarioSteps != 1 || result.Metrics.ConfirmedSteps != 1 {
		t.Fatalf("metrics=%+v", result.Metrics)
	}
	if len(result.autonomousTrace) != 1 || result.autonomousTrace[0].Bound.Action.ID != "open-row" || result.autonomousTrace[0].Outcome != BrowserEffectConfirmed {
		t.Fatalf("autonomous trace=%+v", result.autonomousTrace)
	}
	if len(metricEvents) != 1 || metricEvents[0].Type != "browser_decision_loop_metrics" || metricEvents[0].Meta["status"] != BrowserDecisionLoopConcluded {
		t.Fatalf("metric events=%+v", metricEvents)
	}
	if snapshot := guard.Snapshot(); snapshot.DecisionCorrections != 1 || snapshot.StateActions != 1 || snapshot.ExplorationActions != 0 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	stored, found, err := store.GetBrowserDecisionStep(context.Background(), attempt.ID, 1)
	if err != nil || !found || stored.Status != BrowserDecisionStepConfirmed {
		t.Fatalf("stored=%+v found=%v err=%v", stored, found, err)
	}
}

func TestBrowserDecisionLoopExecutesHostGeneratedExplorationCandidate(t *testing.T) {
	store := openTestCaseStore(t)
	attempt := browserDecisionStepStoreFixture(t, store, "decision-loop-exploration")
	before := browserExplorationTestScene(t, attempt.ID)
	after := browserExplorationTestScene(t, attempt.ID)
	after.URL = "https://app.test/content/42?source=list"
	after.Frames[0].URL = after.URL
	after.Title = "内容详情"
	rebindBrowserDecisionTestScene(t, &after)
	base, err := ParseBrowserPlan([]byte(`version: 2
start_url: https://app.test/content
actions:
  - id: final-screenshot
    action: screenshot
assertions:
  - kind: visible_text
    value: 内容详情
`))
	if err != nil {
		t.Fatal(err)
	}
	providerCalls := 0
	provider := BrowserDecisionProviderFunc(func(_ context.Context, observation BrowserDecisionLoopObservation) ([]byte, error) {
		providerCalls++
		if providerCalls == 1 {
			var navigation BrowserExplorationCandidate
			for _, candidate := range observation.Candidates {
				if candidate.Intent == BrowserExplorationNavigateSameOrigin {
					navigation = candidate
				}
			}
			if navigation.Action.ID == "" {
				t.Fatalf("observation candidates=%+v", observation.Candidates)
			}
			return []byte(fmt.Sprintf(`version: 1
decision: act
scene_id: %s
rationale_code: scenario_next_step
action: {action_id: %s, type: goto}
expected_effect:
  any_of: [{kind: url_changed, same_origin_path_contains: /content/42}]
`, observation.Scene.SceneID, navigation.Action.ID)), nil
		}
		return []byte(fmt.Sprintf(`version: 1
decision: conclude
scene_id: %s
rationale_code: evidence_sufficient
conclusion_code: current_evidence_sufficient
`, observation.Scene.SceneID)), nil
	})
	guard, err := NewBrowserExplorationGuard(DefaultBrowserExplorationBudget())
	if err != nil {
		t.Fatal(err)
	}
	executor := &fakeBoundBrowserStepExecutor{Result: BoundBrowserStepExecutionResult{
		After: after, AfterSceneRef: "browser-scenes/explore-after.json",
		Effect: BrowserStepEffectEvaluation{Outcome: BrowserEffectConfirmed, ConfirmedKinds: []string{"url_changed"}},
	}}
	loop := BrowserDecisionLoop{
		Provider: provider, Transactions: BrowserStepTransactionCoordinator{Store: store}, Executor: executor,
		Exploration: guard,
		Readiness: func(context.Context, BrowserScene) (BrowserDecisionLoopReadiness, error) {
			return BrowserDecisionLoopReadiness{
				ScenarioContractSHA256: strings.Repeat("a", 64), FrontendEntryID: "admin",
				ScenarioReady: true, LoginReady: true, TestInputsReady: true, AuthorizationReady: true,
			}, nil
		},
	}
	result, err := loop.Run(context.Background(), BrowserDecisionLoopRequest{
		AttemptID: attempt.ID, Plan: base, InitialScene: before, InitialSceneRef: "browser-scenes/explore-before.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != BrowserDecisionLoopConcluded || len(result.Steps) != 1 || !result.Steps[0].Exploration || result.Scene.URL != after.URL {
		t.Fatalf("result=%+v", result)
	}
	if snapshot := guard.Snapshot(); snapshot.ExplorationActions != 1 || snapshot.StateActions != 1 || snapshot.UniqueStates != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if len(base.Actions) != 1 || base.Actions[0].ID != "final-screenshot" {
		t.Fatalf("frozen base plan was mutated: %+v", base.Actions)
	}
}

func TestBrowserDecisionLoopStopsAfterBoundedOutOfOrderCorrections(t *testing.T) {
	store := openTestCaseStore(t)
	attempt := browserDecisionStepStoreFixture(t, store, "decision-loop-order")
	scene := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{{
		Ref: "e-1", FrameRef: "f-main", Role: "button", Name: "下一步", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{TestID: "next"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 100, Y: 100, Width: 80, Height: 32},
	}}, nil)
	plan, err := ParseBrowserPlan([]byte(`version: 2
start_url: https://app.test/content
actions:
  - id: first
    action: click
    locator: {kind: test_id, value: next}
  - id: second
    action: click
    locator: {kind: test_id, value: next}
assertions:
  - kind: visible_text
    value: 完成
`))
	if err != nil {
		t.Fatal(err)
	}
	providerCalls := 0
	provider := BrowserDecisionProviderFunc(func(_ context.Context, observation BrowserDecisionLoopObservation) ([]byte, error) {
		providerCalls++
		return []byte(fmt.Sprintf(`version: 1
decision: act
scene_id: %s
rationale_code: scenario_next_step
action: {action_id: second, type: click, element_ref: e-1}
expected_effect:
  any_of: [{kind: text_visible, text: 完成}]
`, observation.Scene.SceneID)), nil
	})
	guard, err := NewBrowserExplorationGuard(DefaultBrowserExplorationBudget())
	if err != nil {
		t.Fatal(err)
	}
	executor := &fakeBoundBrowserStepExecutor{}
	result, err := (BrowserDecisionLoop{
		Provider: provider, Transactions: BrowserStepTransactionCoordinator{Store: store},
		Executor: executor, Exploration: guard,
	}).Run(context.Background(), BrowserDecisionLoopRequest{
		AttemptID: attempt.ID, Plan: plan, InitialScene: scene, InitialSceneRef: "browser-scenes/order-before.json",
	})
	if BrowserExplorationErrorCode(err) != BrowserValidatorDecisionInvalidCode || result.ErrorCode != BrowserDecisionLoopInvalidCode || providerCalls != 3 || executor.Calls != 0 {
		t.Fatalf("result=%+v calls=%d executor=%d err=%v", result, providerCalls, executor.Calls, err)
	}
}

func TestBrowserDecisionLoopStopsForRecoveryAfterScenarioNoEffect(t *testing.T) {
	store := openTestCaseStore(t)
	attempt := browserDecisionStepStoreFixture(t, store, "decision-loop-no-effect")
	scene := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{{
		Ref: "e-1", FrameRef: "f-main", Role: "button", Name: "查看", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{TestID: "view-button"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 100, Y: 200, Width: 80, Height: 32},
	}}, nil)
	after := scene
	after.CapturedAt = "2026-08-04T12:00:03Z"
	rebindBrowserDecisionTestScene(t, &after)
	provider := BrowserDecisionProviderFunc(func(_ context.Context, observation BrowserDecisionLoopObservation) ([]byte, error) {
		return []byte(fmt.Sprintf(`version: 1
decision: act
scene_id: %s
rationale_code: scenario_next_step
action: {action_id: open-row, type: click, element_ref: e-1}
expected_effect:
  any_of: [{kind: surface_opened, role: dialog, name_contains: 视频详情}]
`, observation.Scene.SceneID)), nil
	})
	guard, err := NewBrowserExplorationGuard(DefaultBrowserExplorationBudget())
	if err != nil {
		t.Fatal(err)
	}
	executor := &fakeBoundBrowserStepExecutor{Result: BoundBrowserStepExecutionResult{
		After: after, AfterSceneRef: "browser-scenes/no-effect-after.json",
		Effect: BrowserStepEffectEvaluation{Outcome: BrowserEffectNoEffect, UnconfirmedKinds: []string{"surface_opened"}},
	}}
	result, err := (BrowserDecisionLoop{
		Provider: provider, Transactions: BrowserStepTransactionCoordinator{Store: store},
		Executor: executor, Exploration: guard,
	}).Run(context.Background(), BrowserDecisionLoopRequest{
		AttemptID: attempt.ID, Plan: browserDecisionBindingPlan(t), InitialScene: scene,
		InitialSceneRef: "browser-scenes/no-effect-before.json",
	})
	if err != nil || result.Status != BrowserDecisionLoopRecovery || result.ErrorCode != BrowserDecisionLoopRecoveryCode || len(result.Steps) != 1 || result.Steps[0].Outcome != BrowserEffectNoEffect {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestBrowserDecisionLoopReturnsCapabilityGapWithoutStartingManualBrowser(t *testing.T) {
	store := openTestCaseStore(t)
	attempt := browserDecisionStepStoreFixture(t, store, "decision-loop-gap")
	scene := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{{
		Ref: "e-status", FrameRef: "f-main", Role: "status", Name: "加载完成", Tag: "div",
		States: BrowserSceneElementStates{Visible: true, InViewport: true},
	}}, nil)
	provider := BrowserDecisionProviderFunc(func(_ context.Context, observation BrowserDecisionLoopObservation) ([]byte, error) {
		return []byte(fmt.Sprintf(`version: 1
decision: capability_gap
scene_id: %s
rationale_code: automation_exhausted
capability_gap_code: browser_capability_gap
exhausted_channels: [semantic_grounding, structured_grounding, safe_exploration]
`, observation.Scene.SceneID)), nil
	})
	guard, err := NewBrowserExplorationGuard(DefaultBrowserExplorationBudget())
	if err != nil {
		t.Fatal(err)
	}
	executor := &fakeBoundBrowserStepExecutor{}
	result, err := (BrowserDecisionLoop{
		Provider: provider, Transactions: BrowserStepTransactionCoordinator{Store: store},
		Executor: executor, Exploration: guard, Readiness: readyBrowserDecisionRunner,
	}).Run(context.Background(), BrowserDecisionLoopRequest{
		AttemptID: attempt.ID, Plan: browserDecisionBindingPlan(t), InitialScene: scene,
		InitialSceneRef: "browser-scenes/gap-before.json",
	})
	if err != nil || result.Status != BrowserDecisionLoopCapabilityGap || executor.Calls != 0 || len(result.Steps) != 0 || result.ManualReproductionGate == nil {
		t.Fatalf("result=%+v executor=%d err=%v", result, executor.Calls, err)
	}
}

func TestBrowserDecisionLoopRejectsCapabilityGapWhileSafeCandidateRemains(t *testing.T) {
	store := openTestCaseStore(t)
	attempt := browserDecisionStepStoreFixture(t, store, "decision-loop-premature-gap")
	scene := browserExplorationTestScene(t, attempt.ID)
	provider := BrowserDecisionProviderFunc(func(_ context.Context, observation BrowserDecisionLoopObservation) ([]byte, error) {
		return []byte(fmt.Sprintf(`version: 1
decision: capability_gap
scene_id: %s
rationale_code: automation_exhausted
capability_gap_code: browser_capability_gap
exhausted_channels: [semantic_grounding, structured_grounding, safe_exploration]
`, observation.Scene.SceneID)), nil
	})
	guard, err := NewBrowserExplorationGuard(DefaultBrowserExplorationBudget())
	if err != nil {
		t.Fatal(err)
	}
	result, err := (BrowserDecisionLoop{
		Provider: provider, Transactions: BrowserStepTransactionCoordinator{Store: store},
		Executor: &fakeBoundBrowserStepExecutor{}, Exploration: guard, Readiness: readyBrowserDecisionRunner,
	}).Run(context.Background(), BrowserDecisionLoopRequest{
		AttemptID: attempt.ID, Plan: browserDecisionBindingPlan(t), InitialScene: scene,
		InitialSceneRef: "browser-scenes/premature-gap-before.json",
	})
	if err == nil || result.ErrorCode != BrowserDecisionLoopInvalidCode || result.ManualReproductionGate != nil || result.Metrics.InvalidDecisions != 3 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestBrowserDecisionLoopResumesConfirmedJournalFromFrozenSceneWithoutReplay(t *testing.T) {
	store := openTestCaseStore(t)
	attempt := browserDecisionStepStoreFixture(t, store, "decision-loop-frozen-replay")
	before := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{{
		Ref: "e-1", FrameRef: "f-main", Role: "button", Name: "查看", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{TestID: "view-button"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 100, Y: 200, Width: 80, Height: 32},
	}}, nil)
	after := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{{
		Ref: "e-1", FrameRef: "f-main", SurfaceRef: "s-1", Role: "button", Name: "关闭", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{Label: "关闭"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 900, Y: 100, Width: 64, Height: 32},
	}}, &BrowserSceneSurface{Ref: "s-1", Type: "dialog", Name: "视频详情", Modal: true})
	decision := browserActDecisionForScene(t, before)
	plan := browserDecisionBindingPlan(t)
	coordinator := BrowserStepTransactionCoordinator{Store: store}
	preparationExecutor := &fakeBoundBrowserStepExecutor{Result: BoundBrowserStepExecutionResult{
		After: after, AfterSceneRef: "browser-scenes/replay-after.json",
		Effect: BrowserStepEffectEvaluation{Outcome: BrowserEffectConfirmed},
	}}
	if _, err := coordinator.Run(context.Background(), preparationExecutor, plan, decision, before, attempt.ID, 1, "browser-scenes/replay-before.json"); err != nil {
		t.Fatal(err)
	}
	frozen, err := EncodeFrozenBrowserScene(after, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	providerCalls := 0
	provider := BrowserDecisionProviderFunc(func(_ context.Context, observation BrowserDecisionLoopObservation) ([]byte, error) {
		providerCalls++
		if providerCalls == 1 {
			return json.Marshal(decision)
		}
		return []byte(fmt.Sprintf(`version: 1
decision: conclude
scene_id: %s
rationale_code: evidence_sufficient
conclusion_code: current_evidence_sufficient
`, observation.Scene.SceneID)), nil
	})
	guard, err := NewBrowserExplorationGuard(DefaultBrowserExplorationBudget())
	if err != nil {
		t.Fatal(err)
	}
	replayExecutor := &fakeBoundBrowserStepExecutor{}
	result, err := (BrowserDecisionLoop{
		Provider: provider, Transactions: coordinator, Executor: replayExecutor, Exploration: guard,
		SceneLoader: BrowserSceneEvidenceLoaderFunc(func(_ context.Context, loadedAttemptID, reference string) ([]byte, error) {
			if loadedAttemptID != attempt.ID || reference != "browser-scenes/replay-after.json" {
				t.Fatalf("load attempt=%q reference=%q", loadedAttemptID, reference)
			}
			return frozen, nil
		}),
	}).Run(context.Background(), BrowserDecisionLoopRequest{
		AttemptID: attempt.ID, Plan: plan, InitialScene: before, InitialSceneRef: "browser-scenes/replay-before.json",
	})
	if err != nil || result.Status != BrowserDecisionLoopConcluded || replayExecutor.Calls != 0 || providerCalls != 2 ||
		len(result.Steps) != 1 || !result.Steps[0].Replay || result.Scene.SceneID != after.SceneID {
		t.Fatalf("result=%+v provider=%d executor=%d err=%v", result, providerCalls, replayExecutor.Calls, err)
	}
	if result.Metrics.ReplayedSteps != 1 || result.Metrics.ConfirmedSteps != 1 {
		t.Fatalf("metrics=%+v", result.Metrics)
	}
	eventJSON, err := json.Marshal(BrowserDecisionLoopMetricEvent(result))
	if err != nil || strings.Contains(string(eventJSON), "app.test") || strings.Contains(string(eventJSON), "browser-scenes") {
		t.Fatalf("metric event=%s err=%v", eventJSON, err)
	}
}

func TestBrowserDecisionLoopRejectsFrozenSceneFromAnotherAttemptWithoutReplay(t *testing.T) {
	store := openTestCaseStore(t)
	attempt := browserDecisionStepStoreFixture(t, store, "decision-loop-frozen-substitution")
	before := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{{
		Ref: "e-1", FrameRef: "f-main", Role: "button", Name: "查看", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{TestID: "view-button"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 100, Y: 200, Width: 80, Height: 32},
	}}, nil)
	after := boundBrowserDecisionScene(t, attempt.ID, before.Elements, nil)
	decision := browserActDecisionForScene(t, before)
	plan := browserDecisionBindingPlan(t)
	coordinator := BrowserStepTransactionCoordinator{Store: store}
	if _, err := coordinator.Run(context.Background(), &fakeBoundBrowserStepExecutor{Result: BoundBrowserStepExecutionResult{
		After: after, AfterSceneRef: "browser-scenes/substituted-after.json", Effect: BrowserStepEffectEvaluation{Outcome: BrowserEffectConfirmed},
	}}, plan, decision, before, attempt.ID, 1, "browser-scenes/substituted-before.json"); err != nil {
		t.Fatal(err)
	}
	other := boundBrowserDecisionScene(t, "another-attempt", before.Elements, nil)
	frozen, err := EncodeFrozenBrowserScene(other, other.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	decisionJSON, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	guard, err := NewBrowserExplorationGuard(DefaultBrowserExplorationBudget())
	if err != nil {
		t.Fatal(err)
	}
	replayExecutor := &fakeBoundBrowserStepExecutor{}
	result, runErr := (BrowserDecisionLoop{
		Provider:     BrowserDecisionProviderFunc(func(context.Context, BrowserDecisionLoopObservation) ([]byte, error) { return decisionJSON, nil }),
		Transactions: coordinator, Executor: replayExecutor, Exploration: guard,
		SceneLoader: BrowserSceneEvidenceLoaderFunc(func(context.Context, string, string) ([]byte, error) { return frozen, nil }),
	}).Run(context.Background(), BrowserDecisionLoopRequest{
		AttemptID: attempt.ID, Plan: plan, InitialScene: before, InitialSceneRef: "browser-scenes/substituted-before.json",
	})
	if runErr == nil || result.Status != BrowserDecisionLoopRecovery || result.ErrorCode != BrowserDecisionLoopRecoveryCode || replayExecutor.Calls != 0 {
		t.Fatalf("result=%+v executor=%d err=%v", result, replayExecutor.Calls, runErr)
	}
}

func TestBrowserDecisionLoopRunsHostProvenRecoveryBeforeRetryingScenario(t *testing.T) {
	store := openTestCaseStore(t)
	attempt := browserDecisionStepStoreFixture(t, store, "decision-loop-auto-recovery")
	actionElement := BrowserSceneElement{
		Ref: "e-action", FrameRef: "f-main", Role: "button", Name: "查看", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{TestID: "view-button"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 100, Y: 200, Width: 80, Height: 32},
	}
	before := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{actionElement}, nil)
	loadingElement := BrowserSceneElement{
		Ref: "e-status", FrameRef: "f-main", Role: "status", Name: "正在加载", Tag: "div",
		LocatorHints: BrowserSceneLocatorHints{TestID: "loading-status"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true},
		Box:          BrowserSceneBox{X: 100, Y: 250, Width: 120, Height: 24},
	}
	afterNoEffect := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{actionElement, loadingElement}, nil)
	afterWait := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{actionElement}, nil)
	afterWait.CapturedAt = "2026-08-04T00:00:03Z"
	rebindBrowserDecisionTestScene(t, &afterWait)
	afterSuccess := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{{
		Ref: "e-close", FrameRef: "f-main", SurfaceRef: "s-1", Role: "button", Name: "关闭", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{Label: "关闭"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 900, Y: 100, Width: 64, Height: 32},
	}}, &BrowserSceneSurface{Ref: "s-1", Type: "dialog", Name: "视频详情", Modal: true})
	providerCalls := 0
	provider := BrowserDecisionProviderFunc(func(_ context.Context, observation BrowserDecisionLoopObservation) ([]byte, error) {
		providerCalls++
		if providerCalls == 2 {
			if observation.Recovery == nil || observation.Recovery.ActionID != "open-row" || observation.Recovery.RecoverySteps != 0 {
				t.Fatalf("recovery decision observation=%+v", observation.Recovery)
			}
			var wait BrowserExplorationCandidate
			for _, candidate := range observation.Candidates {
				if candidate.Intent == BrowserExplorationPassiveWait {
					wait = candidate
				}
			}
			if wait.Action.ID == "" {
				t.Fatalf("recovery candidates=%+v", observation.Candidates)
			}
			return []byte(fmt.Sprintf(`version: 1
decision: act
scene_id: %s
rationale_code: effect_check
action: {action_id: %s, type: wait_for, element_ref: e-status}
expected_effect:
  any_of: [{kind: element_absent, role: status, name_contains: 正在加载}]
`, observation.Scene.SceneID, wait.Action.ID)), nil
		}
		if providerCalls == 4 {
			return []byte(fmt.Sprintf(`version: 1
decision: conclude
scene_id: %s
rationale_code: evidence_sufficient
conclusion_code: current_evidence_sufficient
`, observation.Scene.SceneID)), nil
		}
		if providerCalls == 3 && observation.Recovery != nil {
			t.Fatalf("safe retry still exposed recovery=%+v", observation.Recovery)
		}
		return []byte(fmt.Sprintf(`version: 1
decision: act
scene_id: %s
rationale_code: scenario_next_step
action: {action_id: open-row, type: click, element_ref: e-action}
expected_effect:
  any_of: [{kind: surface_opened, role: dialog, name_contains: 视频详情}]
`, observation.Scene.SceneID)), nil
	})
	recoveryCalls := 0
	recovery := BrowserDecisionRecoveryProviderFunc(func(_ context.Context, observation BrowserDecisionRecoveryObservation) (BrowserDecisionRecoveryAssessment, error) {
		recoveryCalls++
		if observation.ActionID != "open-row" || observation.Outcome != BrowserEffectNoEffect || observation.FailedStepNo != 1 {
			t.Fatalf("recovery observation=%+v", observation)
		}
		if observation.RecoverySteps == 0 {
			return BrowserDecisionRecoveryAssessment{Signals: BrowserRecoverySignals{WaitElementRefs: []string{"e-status"}}}, nil
		}
		return BrowserDecisionRecoveryAssessment{RetryScenario: true, RetryProofCode: BrowserRecoveryRetryNotDispatched}, nil
	})
	guard, err := NewBrowserExplorationGuard(DefaultBrowserExplorationBudget())
	if err != nil {
		t.Fatal(err)
	}
	executor := &scriptedBoundBrowserStepExecutor{Results: []BoundBrowserStepExecutionResult{
		{After: afterNoEffect, AfterSceneRef: "browser-scenes/recovery-no-effect.json", Effect: BrowserStepEffectEvaluation{Outcome: BrowserEffectNoEffect}},
		{After: afterWait, AfterSceneRef: "browser-scenes/recovery-wait.json", Effect: BrowserStepEffectEvaluation{Outcome: BrowserEffectConfirmed}},
		{After: afterSuccess, AfterSceneRef: "browser-scenes/recovery-success.json", Effect: BrowserStepEffectEvaluation{Outcome: BrowserEffectConfirmed}},
	}}
	result, err := (BrowserDecisionLoop{
		Provider: provider, Transactions: BrowserStepTransactionCoordinator{Store: store}, Executor: executor,
		Exploration: guard, Recovery: recovery,
		Readiness: func(context.Context, BrowserScene) (BrowserDecisionLoopReadiness, error) {
			return BrowserDecisionLoopReadiness{
				ScenarioContractSHA256: strings.Repeat("a", 64), FrontendEntryID: "admin",
				ScenarioReady: true, LoginReady: true, TestInputsReady: true, AuthorizationReady: true,
			}, nil
		},
	}).Run(context.Background(), BrowserDecisionLoopRequest{
		AttemptID: attempt.ID, Plan: browserDecisionBindingPlan(t), InitialScene: before,
		InitialSceneRef: "browser-scenes/recovery-before.json",
	})
	if err != nil || result.Status != BrowserDecisionLoopConcluded || providerCalls != 4 || recoveryCalls != 2 || executor.Calls != 3 || len(result.Steps) != 3 {
		t.Fatalf("result=%+v provider=%d recovery=%d executor=%d err=%v", result, providerCalls, recoveryCalls, executor.Calls, err)
	}
	if !result.Steps[1].Exploration || result.Steps[0].Outcome != BrowserEffectNoEffect || result.Steps[2].Outcome != BrowserEffectConfirmed ||
		result.Metrics.RecoveryAssessments != 2 || result.Metrics.RecoveryRetries != 1 {
		t.Fatalf("steps=%+v metrics=%+v", result.Steps, result.Metrics)
	}
}

func TestBrowserDecisionRecoveryAssessmentRejectsUnprovenRetry(t *testing.T) {
	for _, assessment := range []BrowserDecisionRecoveryAssessment{
		{RetryScenario: true, RetryProofCode: "model_says_retry"},
		{RetryScenario: true, RetryProofCode: BrowserRecoveryRetryNotDispatched, Signals: BrowserRecoverySignals{WaitElementRefs: []string{"e-1"}}},
		{},
		{Exhausted: true, RetryScenario: true, RetryProofCode: BrowserRecoveryRetryNotDispatched},
	} {
		if err := validateBrowserDecisionRecoveryAssessment(assessment); err == nil {
			t.Fatalf("assessment was accepted: %+v", assessment)
		}
	}
	for _, assessment := range []BrowserDecisionRecoveryAssessment{
		{RetryScenario: true, RetryProofCode: BrowserRecoveryRetryNotDispatched},
		{RetryScenario: true, RetryProofCode: BrowserRecoveryRetryObstructionRemoved},
		{Signals: BrowserRecoverySignals{WaitElementRefs: []string{"e-1"}}},
		{Exhausted: true},
	} {
		if err := validateBrowserDecisionRecoveryAssessment(assessment); err != nil {
			t.Fatalf("assessment=%+v err=%v", assessment, err)
		}
	}
}
