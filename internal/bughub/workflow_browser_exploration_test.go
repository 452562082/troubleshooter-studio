package bughub

import (
	"strings"
	"testing"
)

func browserExplorationTestScene(t *testing.T, attemptID string) BrowserScene {
	t.Helper()
	return boundBrowserDecisionScene(t, attemptID, []BrowserSceneElement{
		{
			Ref: "e-1", FrameRef: "f-main", Role: "link", Name: "查看详情", Tag: "a",
			LocatorHints: BrowserSceneLocatorHints{SameOriginHref: "https://app.test/content/42?source=list"},
			States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
			Box:          BrowserSceneBox{X: 20, Y: 40, Width: 100, Height: 24},
			Relations:    BrowserSceneElementRelations{RowName: "测试内容"},
		},
		{
			Ref: "e-2", FrameRef: "f-main", Role: "tab", Name: "详情", Tag: "button",
			LocatorHints: BrowserSceneLocatorHints{TestID: "details-tab"},
			States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
			Box:          BrowserSceneBox{X: 140, Y: 40, Width: 80, Height: 24},
		},
		{
			Ref: "e-3", FrameRef: "f-main", Role: "status", Name: "加载完成", Tag: "div",
			States: BrowserSceneElementStates{Visible: true, InViewport: true},
			Box:    BrowserSceneBox{X: 20, Y: 100, Width: 120, Height: 24},
		},
	}, nil)
}

func browserExplorationTestContext(t *testing.T, attemptID string) BrowserExplorationContext {
	t.Helper()
	return BrowserExplorationContext{
		AttemptID: attemptID, ScenarioContractSHA256: strings.Repeat("a", 64), FrontendEntryID: "admin",
		Scene: browserExplorationTestScene(t, attemptID), ScenarioReady: true, LoginReady: true,
		TestInputsReady: true, AuthorizationReady: true,
	}
}

func browserExplorationNavigationCandidate(scene BrowserScene) BrowserExplorationCandidate {
	return BrowserExplorationCandidate{
		Intent: BrowserExplorationNavigateSameOrigin, RiskProofCode: BrowserExplorationProofObservedSameOriginHref,
		ElementRef: "e-1",
		Action:     BrowserAction{ID: "explore-detail", Action: "goto", URL: scene.Elements[0].LocatorHints.SameOriginHref},
	}
}

func TestBuildBrowserExplorationCandidatesUsesOnlyUniqueSceneProvenSemantics(t *testing.T) {
	attemptID := "attempt-exploration-candidates"
	scene := browserExplorationTestScene(t, attemptID)
	candidates, err := BuildBrowserExplorationCandidates(scene, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidates=%+v", candidates)
	}
	byIntent := make(map[string]BrowserExplorationCandidate, len(candidates))
	for _, candidate := range candidates {
		byIntent[candidate.Intent] = candidate
		if !validBrowserDecisionIdentifier(candidate.Action.ID, 128) || candidate.Action.Value != "" || candidate.Action.FileRef != "" {
			t.Fatalf("unsafe candidate=%+v", candidate)
		}
	}
	if navigation := byIntent[BrowserExplorationNavigateSameOrigin]; navigation.ElementRef != "e-1" || navigation.Action.Action != "goto" || navigation.Action.URL != scene.Elements[0].LocatorHints.SameOriginHref {
		t.Fatalf("navigation=%+v", navigation)
	}
	if tab := byIntent[BrowserExplorationOpenTab]; tab.ElementRef != "e-2" || tab.Action.Action != "click" || tab.Action.Locator == nil || tab.Action.Locator.Kind != "test_id" {
		t.Fatalf("tab=%+v", tab)
	}

	duplicate := scene.Elements[1]
	duplicate.Ref = "e-4"
	duplicate.Box.Y = 160
	scene.Elements = append(scene.Elements, duplicate)
	rebindBrowserDecisionTestScene(t, &scene)
	candidates, err = BuildBrowserExplorationCandidates(scene, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range candidates {
		if candidate.Intent == BrowserExplorationOpenTab {
			t.Fatalf("ambiguous tab candidate was retained: %+v", candidate)
		}
	}
}

func TestBuildBrowserRecoveryCandidatesRequiresHostProvenWaitAndObstruction(t *testing.T) {
	attemptID := "attempt-recovery-candidates"
	surface := &BrowserSceneSurface{Ref: "s-1", Type: "dialog", Name: "提示", Modal: true}
	scene := boundBrowserDecisionScene(t, attemptID, []BrowserSceneElement{
		{
			Ref: "e-status", FrameRef: "f-main", SurfaceRef: "s-1", Role: "status", Name: "正在加载", Tag: "div",
			LocatorHints: BrowserSceneLocatorHints{TestID: "loading-status"},
			States:       BrowserSceneElementStates{Visible: true, InViewport: true},
			Box:          BrowserSceneBox{X: 20, Y: 100, Width: 120, Height: 24},
		},
	}, surface)
	candidates, err := BuildBrowserRecoveryCandidates(scene, attemptID, BrowserRecoverySignals{
		WaitElementRefs: []string{"e-status"}, ConfirmedObstructionSurfaceRef: "s-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	foundWait, foundDismiss := false, false
	for _, candidate := range candidates {
		switch candidate.Intent {
		case BrowserExplorationPassiveWait:
			foundWait = candidate.ElementRef == "e-status" && candidate.Action.Action == "wait_for"
		case BrowserExplorationDismissObstruction:
			foundDismiss = candidate.ElementRef == "" && candidate.Action.Action == "dismiss_surface" && candidate.Action.Key == "" && candidate.Action.Locator == nil
		}
		if candidate.Action.Value != "" || candidate.Action.FileRef != "" {
			t.Fatalf("unsafe recovery candidate=%+v", candidate)
		}
	}
	if !foundWait || !foundDismiss {
		t.Fatalf("candidates=%+v", candidates)
	}
	if _, err := BuildBrowserRecoveryCandidates(scene, attemptID, BrowserRecoverySignals{ConfirmedObstructionSurfaceRef: "stale-surface"}); err == nil {
		t.Fatal("stale obstruction proof produced a dismiss_surface candidate")
	}
	if _, err := BuildBrowserRecoveryCandidates(scene, attemptID, BrowserRecoverySignals{WaitElementRefs: []string{"e-status", "e-status"}}); err == nil {
		t.Fatal("duplicated wait proof produced duplicate candidates")
	}
}

func TestBuildBrowserPassiveWaitAndEphemeralDecisionPlan(t *testing.T) {
	attemptID := "attempt-exploration-plan"
	scene := browserExplorationTestScene(t, attemptID)
	wait, err := BuildBrowserPassiveWaitCandidate(scene, attemptID, "e-3")
	if err != nil || wait.Action.Action != "wait_for" || wait.Action.Locator == nil || wait.Action.Value != "" {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	candidates, err := BuildBrowserExplorationCandidates(scene, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	candidates = append(candidates, wait)
	base := browserDecisionBindingPlan(t)
	originalActions := len(base.Actions)
	augmented, err := BuildBrowserDecisionPlanWithExploration(base, scene, attemptID, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(base.Actions) != originalActions || len(augmented.Actions) != originalActions+len(candidates) {
		t.Fatalf("base=%d augmented=%d candidates=%d", len(base.Actions), len(augmented.Actions), len(candidates))
	}
	forged := candidates[0]
	forged.Action.Value = "model-authored-value"
	if _, err := BuildBrowserDecisionPlanWithExploration(base, scene, attemptID, []BrowserExplorationCandidate{forged}); err == nil {
		t.Fatal("forged exploration candidate entered the binding plan")
	}
}

func TestBrowserExplorationStateFingerprintIgnoresEphemeralIdentityAndURLQuery(t *testing.T) {
	attemptID := "attempt-exploration-fingerprint"
	scene := browserExplorationTestScene(t, attemptID)
	first, err := browserExplorationStateFingerprint(scene, attemptID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	scene.CapturedAt = "2026-08-04T12:00:09Z"
	scene.URL = "https://app.test/content?token=must-not-affect-state"
	scene.Frames[0].URL = scene.URL
	scene.Elements[0].Ref = "e-99"
	scene.Elements[0].Box.X = 900
	scene.Elements[1], scene.Elements[2] = scene.Elements[2], scene.Elements[1]
	rebindBrowserDecisionTestScene(t, &scene)
	second, err := browserExplorationStateFingerprint(scene, attemptID, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !validLowerSHA256(first) {
		t.Fatalf("fingerprints first=%s second=%s", first, second)
	}
	scene.Title = "different title is deliberately not a state edge"
	scene.Elements[0].Name = "不同交互语义"
	rebindBrowserDecisionTestScene(t, &scene)
	third, err := browserExplorationStateFingerprint(scene, attemptID, "admin")
	if err != nil || third == first {
		t.Fatalf("semantic fingerprint third=%s err=%v", third, err)
	}
	otherEntry, err := browserExplorationStateFingerprint(scene, attemptID, "h5")
	if err != nil || otherEntry == third {
		t.Fatalf("frontend fingerprint=%s err=%v", otherEntry, err)
	}
}

func TestBrowserExplorationGuardAdmitsOnlySceneProvenLowRiskEdges(t *testing.T) {
	guard, err := NewBrowserExplorationGuard(DefaultBrowserExplorationBudget())
	if err != nil {
		t.Fatal(err)
	}
	context := browserExplorationTestContext(t, "attempt-exploration-safe")
	navigation := browserExplorationNavigationCandidate(context.Scene)
	admission, err := guard.ReserveExploration(context, navigation)
	if err != nil || !validLowerSHA256(admission.StateFingerprint) || !validLowerSHA256(admission.ActionFingerprint) {
		t.Fatalf("navigation admission=%+v err=%v", admission, err)
	}
	if err := guard.RecordOutcome(admission, BrowserEffectConfirmed); err != nil {
		t.Fatal(err)
	}
	sameEdgeNewID := navigation
	sameEdgeNewID.Action.ID = "renamed-explore-detail"
	if _, err := guard.ReserveExploration(context, sameEdgeNewID); BrowserExplorationErrorCode(err) != BrowserExplorationLoopCode {
		t.Fatalf("renamed semantic edge err=%v", err)
	}

	tabLocator, err := browserDecisionElementLocator(context.Scene.Elements[1])
	if err != nil {
		t.Fatal(err)
	}
	_, err = guard.ReserveExploration(context, BrowserExplorationCandidate{
		Intent: BrowserExplorationOpenTab, RiskProofCode: BrowserExplorationProofARIATab, ElementRef: "e-2",
		Action: BrowserAction{ID: "explore-tab", Action: "click", Locator: tabLocator},
	})
	if err != nil {
		t.Fatal(err)
	}

	waitLocator, err := browserDecisionElementLocator(context.Scene.Elements[2])
	if err != nil {
		t.Fatal(err)
	}
	_, err = guard.ReserveExploration(context, BrowserExplorationCandidate{
		Intent: BrowserExplorationPassiveWait, RiskProofCode: BrowserExplorationProofPassiveWait, ElementRef: "e-3",
		Action: BrowserAction{ID: "explore-wait", Action: "wait_for", Locator: waitLocator},
	})
	if err != nil {
		t.Fatal(err)
	}

	surface := &BrowserSceneSurface{Ref: "s-1", Type: "dialog", Name: "广告", Modal: true}
	dismissContext := context
	dismissContext.Scene = boundBrowserDecisionScene(t, context.AttemptID, []BrowserSceneElement{}, surface)
	_, err = guard.ReserveExploration(dismissContext, BrowserExplorationCandidate{
		Intent: BrowserExplorationDismissObstruction, RiskProofCode: BrowserExplorationProofConfirmedObstruction,
		Action: BrowserAction{ID: "dismiss-obstruction", Action: "dismiss_surface"},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBrowserExplorationGuardBoundsUniqueStateGraph(t *testing.T) {
	guard, err := NewBrowserExplorationGuard(BrowserExplorationBudget{
		MaxStateActions: 4, MaxExplorationActions: 4, MaxStates: 1, MaxDecisionCorrections: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	context := browserExplorationTestContext(t, "attempt-exploration-states")
	first, err := guard.ReserveExploration(context, browserExplorationNavigationCandidate(context.Scene))
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.RecordOutcome(first, BrowserEffectConfirmed); err != nil {
		t.Fatal(err)
	}
	context.Scene.URL = "https://app.test/another-state"
	context.Scene.Frames[0].URL = context.Scene.URL
	rebindBrowserDecisionTestScene(t, &context.Scene)
	candidate := browserExplorationNavigationCandidate(context.Scene)
	candidate.Action.ID = "another-state-navigation"
	if _, err := guard.ReserveExploration(context, candidate); BrowserExplorationErrorCode(err) != BrowserExplorationExhaustedCode {
		t.Fatalf("state budget err=%v", err)
	}
}

func TestBrowserExplorationGuardRejectsUnsafeOrUnnecessaryExploration(t *testing.T) {
	base := browserExplorationTestContext(t, "attempt-exploration-reject")
	navigation := browserExplorationNavigationCandidate(base.Scene)
	tests := []struct {
		name      string
		mutateCtx func(*BrowserExplorationContext)
		mutate    func(*BrowserExplorationCandidate)
		wantCode  string
	}{
		{name: "production", mutateCtx: func(value *BrowserExplorationContext) { value.IsProduction = true }, wantCode: BrowserExplorationDisabledCode},
		{name: "login missing", mutateCtx: func(value *BrowserExplorationContext) { value.LoginReady = false }, wantCode: BrowserExplorationNotReadyCode},
		{name: "direct target", mutateCtx: func(value *BrowserExplorationContext) { value.DirectTargetAvailable = true }, wantCode: BrowserExplorationNotNeededCode},
		{name: "invented proof", mutate: func(value *BrowserExplorationCandidate) { value.RiskProofCode = BrowserExplorationProofARIATab }, wantCode: BrowserExplorationUnsafeCode},
		{name: "cross origin", mutate: func(value *BrowserExplorationCandidate) { value.Action.URL = "https://evil.test/content/42" }, wantCode: BrowserExplorationUnsafeCode},
		{name: "state-changing fill", mutate: func(value *BrowserExplorationCandidate) { value.Action.Action = "fill"; value.Action.Value = "unsafe" }, wantCode: BrowserExplorationUnsafeCode},
		{name: "stale ref", mutate: func(value *BrowserExplorationCandidate) { value.ElementRef = "e-99" }, wantCode: BrowserExplorationUnsafeCode},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			guard, err := NewBrowserExplorationGuard(DefaultBrowserExplorationBudget())
			if err != nil {
				t.Fatal(err)
			}
			context := base
			candidate := navigation
			if test.mutateCtx != nil {
				test.mutateCtx(&context)
			}
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			_, err = guard.ReserveExploration(context, candidate)
			if BrowserExplorationErrorCode(err) != test.wantCode {
				t.Fatalf("err=%v code=%q want=%q", err, BrowserExplorationErrorCode(err), test.wantCode)
			}
		})
	}
}

func TestBrowserExplorationGuardPreventsFailedEdgeReplayAndEnforcesBudgets(t *testing.T) {
	guard, err := NewBrowserExplorationGuard(BrowserExplorationBudget{
		MaxStateActions: 3, MaxExplorationActions: 2, MaxStates: 2, MaxDecisionCorrections: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	context := browserExplorationTestContext(t, "attempt-exploration-budget")
	candidate := browserExplorationNavigationCandidate(context.Scene)
	admission, err := guard.ReserveExploration(context, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.RecordOutcome(admission, BrowserEffectNoEffect); err != nil {
		t.Fatal(err)
	}
	if _, err := guard.ReserveExploration(context, candidate); BrowserExplorationErrorCode(err) != BrowserExplorationLoopCode {
		t.Fatalf("repeat err=%v", err)
	}
	tabLocator, err := browserDecisionElementLocator(context.Scene.Elements[1])
	if err != nil {
		t.Fatal(err)
	}
	_, err = guard.ReserveExploration(context, BrowserExplorationCandidate{
		Intent: BrowserExplorationOpenTab, RiskProofCode: BrowserExplorationProofARIATab, ElementRef: "e-2",
		Action: BrowserAction{ID: "tab", Action: "click", Locator: tabLocator},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitLocator, err := browserDecisionElementLocator(context.Scene.Elements[2])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.ReserveExploration(context, BrowserExplorationCandidate{
		Intent: BrowserExplorationPassiveWait, RiskProofCode: BrowserExplorationProofPassiveWait, ElementRef: "e-3",
		Action: BrowserAction{ID: "wait", Action: "wait_for", Locator: waitLocator},
	}); BrowserExplorationErrorCode(err) != BrowserExplorationExhaustedCode {
		t.Fatalf("budget err=%v", err)
	}
	if err := guard.ReserveScenarioAction(BrowserAction{Action: "click"}); err != nil {
		t.Fatal(err)
	}
	if err := guard.ReserveScenarioAction(BrowserAction{Action: "click"}); BrowserExplorationErrorCode(err) != BrowserExplorationExhaustedCode {
		t.Fatalf("state action budget err=%v", err)
	}
	snapshot := guard.Snapshot()
	if snapshot.StateActions != 3 || snapshot.ExplorationActions != 2 || snapshot.UniqueStates != 1 || snapshot.FailedEdges != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestBrowserExplorationGuardDoesNotChargePassiveWaitAsStateMutation(t *testing.T) {
	guard, err := NewBrowserExplorationGuard(BrowserExplorationBudget{
		MaxStateActions: 1, MaxExplorationActions: 2, MaxStates: 1, MaxDecisionCorrections: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	context := browserExplorationTestContext(t, "attempt-exploration-passive-budget")
	wait, err := BuildBrowserPassiveWaitCandidate(context.Scene, context.AttemptID, "e-3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := guard.ReserveExploration(context, wait); err != nil {
		t.Fatal(err)
	}
	if err := guard.ReserveScenarioAction(BrowserAction{Action: "screenshot"}); err != nil {
		t.Fatal(err)
	}
	if err := guard.ReserveScenarioAction(BrowserAction{Action: "click"}); err != nil {
		t.Fatal(err)
	}
	if snapshot := guard.Snapshot(); snapshot.StateActions != 1 || snapshot.ExplorationActions != 1 {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestBrowserExplorationGuardBoundsDecisionCorrectionsPerScene(t *testing.T) {
	guard, err := NewBrowserExplorationGuard(BrowserExplorationBudget{
		MaxStateActions: 3, MaxExplorationActions: 2, MaxStates: 2, MaxDecisionCorrections: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	attemptID := "attempt-exploration-correction"
	scene := browserExplorationTestScene(t, attemptID)
	if err := guard.ReserveDecisionCorrection(scene, attemptID); err != nil {
		t.Fatal(err)
	}
	if err := guard.ReserveDecisionCorrection(scene, attemptID); err != nil {
		t.Fatal(err)
	}
	if err := guard.ReserveDecisionCorrection(scene, attemptID); BrowserExplorationErrorCode(err) != BrowserValidatorDecisionInvalidCode {
		t.Fatalf("correction err=%v", err)
	}
	if guard.Snapshot().DecisionCorrections != 2 {
		t.Fatalf("snapshot=%+v", guard.Snapshot())
	}
}
