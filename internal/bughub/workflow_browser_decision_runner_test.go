package bughub

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type fakeBrowserDecisionHostSession struct {
	initial     BrowserScene
	initialRef  string
	final       BrowserVerificationResult
	finishCalls int
	closeCalls  int
}

func (session *fakeBrowserDecisionHostSession) InitialBrowserDecisionScene() (BrowserScene, string) {
	return session.initial, session.initialRef
}

func (session *fakeBrowserDecisionHostSession) ExecuteBoundBrowserStep(context.Context, BoundBrowserStepExecutionRequest) (BoundBrowserStepExecutionResult, error) {
	return BoundBrowserStepExecutionResult{}, fmt.Errorf("unexpected browser decision step")
}

func (session *fakeBrowserDecisionHostSession) LoadFrozenBrowserScene(context.Context, string, string) ([]byte, error) {
	return nil, fmt.Errorf("unexpected frozen Scene load")
}

func (session *fakeBrowserDecisionHostSession) FinishBrowserDecision(context.Context) (BrowserVerificationResult, error) {
	session.finishCalls++
	return session.final, nil
}

func (session *fakeBrowserDecisionHostSession) Close() error {
	session.closeCalls++
	return nil
}

type fakeBrowserDecisionSessionOpener struct {
	session *fakeBrowserDecisionHostSession
	calls   int
}

func (opener *fakeBrowserDecisionSessionOpener) OpenBrowserDecisionSession(context.Context, BrowserVerificationRequest) (BrowserDecisionHostSession, error) {
	opener.calls++
	return opener.session, nil
}

func TestHostBrowserDecisionCoordinatorRunnerConcludesAndFinalizesEvidence(t *testing.T) {
	store := openTestCaseStore(t)
	attempt := browserDecisionStepStoreFixture(t, store, "coordinator-runner-conclude")
	scene := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{{
		Ref: "e-1", FrameRef: "f-main", Role: "button", Name: "查看", Tag: "button",
		States: BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:    BrowserSceneBox{X: 1, Y: 1, Width: 10, Height: 10},
	}}, nil)
	session := &fakeBrowserDecisionHostSession{
		initial: scene, initialRef: "browser-scenes/scene-000-test.json",
		final: BrowserVerificationResult{Status: "completed", FinalScreenshotPath: "browser/final.png"},
	}
	opener := &fakeBrowserDecisionSessionOpener{session: session}
	runner := HostBrowserDecisionCoordinatorRunner{
		Opener: opener,
		Provider: BrowserDecisionProviderFunc(func(_ context.Context, observation BrowserDecisionLoopObservation) ([]byte, error) {
			return []byte(fmt.Sprintf(`version: 1
decision: conclude
scene_id: %s
rationale_code: evidence_sufficient
conclusion_code: current_evidence_sufficient
`, observation.Scene.SceneID)), nil
		}),
		Transactions: BrowserStepTransactionCoordinator{Store: store},
		Recovery:     ConservativeBrowserDecisionRecoveryProvider{},
		Readiness:    readyBrowserDecisionRunner,
	}
	result, err := runner.ExecuteBrowserDecision(context.Background(), BrowserDecisionCoordinatorRunRequest{
		Verification: BrowserVerificationRequest{AttemptID: attempt.ID, Plan: browserDecisionBindingPlan(t)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "completed" || opener.calls != 1 || session.finishCalls != 1 || session.closeCalls != 1 {
		t.Fatalf("result=%+v opener=%d finish=%d close=%d", result, opener.calls, session.finishCalls, session.closeCalls)
	}
}

func TestHostBrowserDecisionCoordinatorRunnerReturnsCapabilityGapWithFrozenFinalEvidence(t *testing.T) {
	store := openTestCaseStore(t)
	attempt := browserDecisionStepStoreFixture(t, store, "coordinator-runner-gap")
	scene := boundBrowserDecisionScene(t, attempt.ID, nil, nil)
	session := &fakeBrowserDecisionHostSession{
		initial: scene, initialRef: "browser-scenes/scene-000-test.json",
		final: BrowserVerificationResult{Status: "completed", FinalScreenshotPath: "browser/final.png"},
	}
	runner := HostBrowserDecisionCoordinatorRunner{
		Opener: &fakeBrowserDecisionSessionOpener{session: session},
		Provider: BrowserDecisionProviderFunc(func(_ context.Context, observation BrowserDecisionLoopObservation) ([]byte, error) {
			return []byte(fmt.Sprintf(`version: 1
decision: capability_gap
scene_id: %s
rationale_code: automation_exhausted
capability_gap_code: browser_capability_gap
exhausted_channels: [semantic_grounding, structured_grounding, safe_exploration]
`, observation.Scene.SceneID)), nil
		}),
		Transactions: BrowserStepTransactionCoordinator{Store: store},
		Recovery:     ConservativeBrowserDecisionRecoveryProvider{}, Readiness: readyBrowserDecisionRunner,
	}
	result, err := runner.ExecuteBrowserDecision(context.Background(), BrowserDecisionCoordinatorRunRequest{
		Verification: BrowserVerificationRequest{AttemptID: attempt.ID, Plan: browserDecisionBindingPlan(t)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != BrowserDecisionLoopCapabilityGap || result.ErrorCode != "browser_capability_gap" || session.finishCalls != 1 || session.closeCalls != 1 {
		t.Fatalf("result=%+v finish=%d close=%d", result, session.finishCalls, session.closeCalls)
	}
}

func TestHostBrowserDecisionCoordinatorRunnerMarksOnlyBoundRecipeV3Attempts(t *testing.T) {
	store := openTestCaseStore(t)
	attempt := browserDecisionStepStoreFixture(t, store, "coordinator-runner-recipe-v3")
	plan, recipe := autonomousRecipeReplayFixture(t, attempt.ID)
	// Equal anchors deliberately force Recipe grounding to fall back to the
	// normal provider. The attempt is still a Recipe v3 attempt because the
	// exact bound recipe was selected before the browser session opened.
	scene := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{
		recipeReplayElement("e-one"), recipeReplayElement("e-two"),
	}, nil)
	session := &fakeBrowserDecisionHostSession{
		initial: scene, initialRef: "browser-scenes/scene-000-test.json",
		final: BrowserVerificationResult{Status: "completed", FinalScreenshotPath: "browser/final.png"},
	}
	var events []InvestigationEvent
	runner := HostBrowserDecisionCoordinatorRunner{
		Opener: &fakeBrowserDecisionSessionOpener{session: session},
		Provider: BrowserDecisionProviderFunc(func(_ context.Context, observation BrowserDecisionLoopObservation) ([]byte, error) {
			return []byte(fmt.Sprintf(`version: 1
decision: conclude
scene_id: %s
rationale_code: evidence_sufficient
conclusion_code: current_evidence_sufficient
`, observation.Scene.SceneID)), nil
		}),
		Transactions: BrowserStepTransactionCoordinator{Store: store},
		Recovery:     ConservativeBrowserDecisionRecoveryProvider{},
	}
	result, err := runner.ExecuteBrowserDecision(context.Background(), BrowserDecisionCoordinatorRunRequest{
		Verification: BrowserVerificationRequest{AttemptID: attempt.ID, Plan: plan}, Plan: plan,
		AutonomousRecipe: &recipe,
		Readiness: func(context.Context, BrowserScene) (BrowserDecisionLoopReadiness, error) {
			return BrowserDecisionLoopReadiness{
				ScenarioContractSHA256: recipe.ScenarioContractSHA256, FrontendEntryID: "admin",
				ScenarioReady: true, LoginReady: true, TestInputsReady: true, AuthorizationReady: true,
			}, nil
		},
		Emit: func(event InvestigationEvent) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.autonomousRecipeVersion != AutonomousValidationRecipeVersion || result.autonomousScenarioSHA256 != recipe.ScenarioContractSHA256 {
		t.Fatalf("result recipe metadata=%d/%s", result.autonomousRecipeVersion, result.autonomousScenarioSHA256)
	}
	if len(events) != 1 || events[0].Meta["recipe_version"] != AutonomousValidationRecipeVersion {
		t.Fatalf("events=%+v", events)
	}
}

func TestHostBrowserDecisionCoordinatorRunnerRejectsIncompleteCompositionBeforeOpening(t *testing.T) {
	opener := &fakeBrowserDecisionSessionOpener{}
	runner := HostBrowserDecisionCoordinatorRunner{
		Opener: opener,
		Provider: BrowserDecisionProviderFunc(func(context.Context, BrowserDecisionLoopObservation) ([]byte, error) {
			return nil, nil
		}),
		Transactions: BrowserStepTransactionCoordinator{Store: openTestCaseStore(t)},
		Recovery:     ConservativeBrowserDecisionRecoveryProvider{},
	}
	if _, err := runner.ExecuteBrowserDecision(context.Background(), BrowserDecisionCoordinatorRunRequest{}); err == nil {
		t.Fatal("expected missing request readiness to fail")
	}
	if opener.calls != 0 {
		t.Fatal("incomplete runner opened a browser")
	}
}

func readyBrowserDecisionRunner(context.Context, BrowserScene) (BrowserDecisionLoopReadiness, error) {
	return BrowserDecisionLoopReadiness{
		ScenarioContractSHA256: strings.Repeat("a", 64), FrontendEntryID: "primary",
		ScenarioReady: true, LoginReady: true, TestInputsReady: true, AuthorizationReady: true,
	}, nil
}
