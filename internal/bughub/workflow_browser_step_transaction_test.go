package bughub

import (
	"context"
	"errors"
	"testing"
)

type fakeBoundBrowserStepExecutor struct {
	Calls  int
	Result BoundBrowserStepExecutionResult
	Err    error
}

func (f *fakeBoundBrowserStepExecutor) ExecuteBoundBrowserStep(_ context.Context, _ BoundBrowserStepExecutionRequest) (BoundBrowserStepExecutionResult, error) {
	f.Calls++
	return f.Result, f.Err
}

func TestBrowserStepTransactionCoordinatorPersistsBoundDecisionBeforeCompletion(t *testing.T) {
	store := openTestCaseStore(t)
	attempt := browserDecisionStepStoreFixture(t, store, "coordinator")
	scene := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{{
		Ref: "e-1", FrameRef: "f-main", Role: "button", Name: "查看", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{TestID: "view-button"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 100, Y: 200, Width: 80, Height: 32},
		Relations:    BrowserSceneElementRelations{RowName: "测试都市生活剧"},
	}}, nil)
	decision := browserActDecisionForScene(t, scene)
	coordinator := BrowserStepTransactionCoordinator{Store: store}

	bound, prepared, replay, err := coordinator.Prepare(
		context.Background(), browserDecisionBindingPlan(t), decision, scene, attempt.ID, 1,
		"browser-scenes/step-1-before.json",
	)
	if err != nil || replay || prepared.Status != BrowserDecisionStepPrepared || prepared.DecisionSHA256 != bound.DecisionSHA256 {
		t.Fatalf("bound=%+v prepared=%+v replay=%v err=%v", bound, prepared, replay, err)
	}
	executing, replay, err := coordinator.Start(context.Background(), bound, attempt.ID, 1)
	if err != nil || replay || executing.Status != BrowserDecisionStepExecuting {
		t.Fatalf("executing=%+v replay=%v err=%v", executing, replay, err)
	}
	completed, replay, err := coordinator.Complete(context.Background(), attempt.ID, 1, BrowserStepEffectEvaluation{
		Outcome: BrowserEffectAmbiguous, UnconfirmedKinds: []string{"surface_opened"},
	}, "browser-scenes/step-1-after.json")
	if err != nil || replay || completed.Status != BrowserDecisionStepAmbiguous || completed.EffectCode != BrowserStepAmbiguousCode {
		t.Fatalf("completed=%+v replay=%v err=%v", completed, replay, err)
	}
}

func TestBrowserDecisionStepTerminalStateBoundsWorkerBlockCodes(t *testing.T) {
	tests := []struct {
		evaluation BrowserStepEffectEvaluation
		status     BrowserDecisionStepStatus
		code       string
	}{
		{evaluation: BrowserStepEffectEvaluation{Outcome: BrowserEffectConfirmed}, status: BrowserDecisionStepConfirmed, code: BrowserStepEffectConfirmedCode},
		{evaluation: BrowserStepEffectEvaluation{Outcome: BrowserEffectNoEffect}, status: BrowserDecisionStepNoEffect, code: BrowserStepNoEffectCode},
		{evaluation: BrowserStepEffectEvaluation{Outcome: BrowserEffectBlocked, BlockCode: "locator_ambiguous"}, status: BrowserDecisionStepBlocked, code: "browser_locator_ambiguous"},
		{evaluation: BrowserStepEffectEvaluation{Outcome: BrowserEffectBlocked, BlockCode: "../../unsafe"}, status: BrowserDecisionStepBlocked, code: "browser_action_blocked"},
		{evaluation: BrowserStepEffectEvaluation{Outcome: BrowserEffectUncertain}, status: BrowserDecisionStepUncertain, code: BrowserStepUncertainCode},
	}
	for _, test := range tests {
		status, code, err := browserDecisionStepTerminalState(test.evaluation)
		if err != nil || status != test.status || code != test.code {
			t.Fatalf("evaluation=%+v status=%q code=%q err=%v", test.evaluation, status, code, err)
		}
	}
	if _, _, err := browserDecisionStepTerminalState(BrowserStepEffectEvaluation{Outcome: "invented"}); err == nil {
		t.Fatal("invented browser effect outcome was accepted")
	}
}

func TestBrowserStepTransactionRunNeverReplaysAnExecutingAction(t *testing.T) {
	store := openTestCaseStore(t)
	attempt := browserDecisionStepStoreFixture(t, store, "run-replay")
	scene := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{{
		Ref: "e-1", FrameRef: "f-main", Role: "button", Name: "查看", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{TestID: "view-button"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 100, Y: 200, Width: 80, Height: 32},
	}}, nil)
	decision := browserActDecisionForScene(t, scene)
	coordinator := BrowserStepTransactionCoordinator{Store: store}
	bound, _, _, err := coordinator.Prepare(context.Background(), browserDecisionBindingPlan(t), decision, scene, attempt.ID, 1, "browser-scenes/before.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := coordinator.Start(context.Background(), bound, attempt.ID, 1); err != nil {
		t.Fatal(err)
	}
	executor := &fakeBoundBrowserStepExecutor{}
	result, err := coordinator.Run(context.Background(), executor, browserDecisionBindingPlan(t), decision, scene, attempt.ID, 1, "browser-scenes/before.json")
	if err != nil || executor.Calls != 0 || !result.Interrupted || result.Journal.Status != BrowserDecisionStepUncertain {
		t.Fatalf("result=%+v calls=%d err=%v", result, executor.Calls, err)
	}
}

func TestBrowserStepTransactionRunPersistsSuccessAndExecutorFailure(t *testing.T) {
	t.Run("confirmed", func(t *testing.T) {
		store := openTestCaseStore(t)
		attempt := browserDecisionStepStoreFixture(t, store, "run-confirmed")
		scene := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{{
			Ref: "e-1", FrameRef: "f-main", Role: "button", Name: "查看", Tag: "button",
			LocatorHints: BrowserSceneLocatorHints{TestID: "view-button"},
			States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
			Box:          BrowserSceneBox{X: 100, Y: 200, Width: 80, Height: 32},
		}}, nil)
		after := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{{
			Ref: "e-1", FrameRef: "f-main", Role: "button", Name: "关闭", Tag: "button",
			LocatorHints: BrowserSceneLocatorHints{Label: "关闭"},
			States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
			Box:          BrowserSceneBox{X: 900, Y: 100, Width: 64, Height: 32},
		}}, &BrowserSceneSurface{Ref: "s-1", Type: "dialog", Name: "视频详情", Modal: true})
		executor := &fakeBoundBrowserStepExecutor{Result: BoundBrowserStepExecutionResult{
			After: after, AfterSceneRef: "browser-scenes/after.json",
			Effect: BrowserStepEffectEvaluation{Outcome: BrowserEffectConfirmed, ConfirmedKinds: []string{"surface_opened"}},
		}}
		coordinator := BrowserStepTransactionCoordinator{Store: store}
		result, err := coordinator.Run(context.Background(), executor, browserDecisionBindingPlan(t), browserActDecisionForScene(t, scene), scene, attempt.ID, 1, "browser-scenes/before.json")
		if err != nil || executor.Calls != 1 || !result.DidExecute || result.Journal.Status != BrowserDecisionStepConfirmed {
			t.Fatalf("result=%+v calls=%d err=%v", result, executor.Calls, err)
		}
		replayed, err := coordinator.Run(context.Background(), executor, browserDecisionBindingPlan(t), browserActDecisionForScene(t, scene), scene, attempt.ID, 1, "browser-scenes/before.json")
		if err != nil || executor.Calls != 1 || !replayed.Replay || replayed.DidExecute {
			t.Fatalf("replayed=%+v calls=%d err=%v", replayed, executor.Calls, err)
		}
	})

	t.Run("executor failure", func(t *testing.T) {
		store := openTestCaseStore(t)
		attempt := browserDecisionStepStoreFixture(t, store, "run-failed")
		scene := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{{
			Ref: "e-1", FrameRef: "f-main", Role: "button", Name: "查看", Tag: "button",
			LocatorHints: BrowserSceneLocatorHints{TestID: "view-button"},
			States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
			Box:          BrowserSceneBox{X: 100, Y: 200, Width: 80, Height: 32},
		}}, nil)
		executor := &fakeBoundBrowserStepExecutor{Err: context.Canceled}
		coordinator := BrowserStepTransactionCoordinator{Store: store}
		result, err := coordinator.Run(context.Background(), executor, browserDecisionBindingPlan(t), browserActDecisionForScene(t, scene), scene, attempt.ID, 1, "browser-scenes/before.json")
		if !errors.Is(err, context.Canceled) || executor.Calls != 1 || !result.Interrupted || result.Journal.Status != BrowserDecisionStepUncertain {
			t.Fatalf("result=%+v calls=%d err=%v", result, executor.Calls, err)
		}
	})
}
