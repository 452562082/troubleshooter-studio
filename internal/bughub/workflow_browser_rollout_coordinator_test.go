package bughub

import (
	"context"
	"errors"
	"testing"
)

func TestBrowserCoordinatorDecisionRolloutUsesExplicitEnabledRunner(t *testing.T) {
	request := browserCoordinatorRequest(t)
	var events []InvestigationEvent
	request.Emit = func(event InvestigationEvent) { events = append(events, event) }
	legacy := &fakeBrowserVerifier{Results: []BrowserVerificationResult{completedBrowserResult("browser/legacy.png")}}
	runner := &fakeBrowserDecisionCoordinatorRunner{
		Capabilities: completeBrowserDecisionRolloutCapabilities(),
		Result:       completedBrowserResult("browser/decision.png"),
	}
	coordinator := BrowserCoordinator{
		Verifier: legacy, BrowserDecisionRunner: runner,
		BrowserDecisionPolicy: BrowserDecisionRolloutPolicy{Version: BrowserDecisionRolloutVersion, Enabled: true, Percentage: 100},
	}

	result, _, err := coordinator.executeBrowser(context.Background(), request, rolloutCoordinatorTestPlan(), browserPrimaryExecution)
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Calls != 0 || len(runner.Requests) != 1 || result.Status != "completed" {
		t.Fatalf("legacy calls=%d runner calls=%d result=%#v", legacy.Calls, len(runner.Requests), result)
	}
	if len(events) != 1 || events[0].Type != "browser_decision_rollout" || events[0].Meta["enabled"] != true || events[0].Meta["reason"] != BrowserDecisionRolloutEnabled {
		t.Fatalf("unexpected rollout event: %#v", events)
	}
}

func TestBrowserCoordinatorDecisionRolloutLoadsOnlyExactRecipeV3(t *testing.T) {
	request := browserCoordinatorRequest(t)
	request.Attempt.OutputJSON = []byte(`{}`)
	store := openTestCaseStore(t)
	createTestCase(t, store, request.Attempt.CaseID)
	if err := store.CreateAttempt(context.Background(), request.Attempt); err != nil {
		t.Fatal(err)
	}
	plan, autonomous := autonomousRecipeReplayFixture(t, request.Attempt.ID)
	planSHA, err := durableBrowserPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	autonomousSHA, err := autonomousValidationRecipeSHA256(autonomous, plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.StoreValidationRecipe(context.Background(), ValidationRecipe{
		CaseID: request.Attempt.CaseID, ScenarioSHA256: autonomous.ScenarioContractSHA256, PlanSHA256: planSHA,
		Plan: plan, AutonomousRecipeSHA256: autonomousSHA, AutonomousRecipe: &autonomous, SourceAttemptID: request.Attempt.ID,
	}); err != nil {
		t.Fatal(err)
	}
	runner := &fakeBrowserDecisionCoordinatorRunner{
		Capabilities: completeBrowserDecisionRolloutCapabilities(),
		Result:       completedBrowserResult("browser/decision.png"),
	}
	coordinator := BrowserCoordinator{
		Verifier: &fakeBrowserVerifier{}, Recipes: store, BrowserDecisionRunner: runner,
		BrowserDecisionPolicy: BrowserDecisionRolloutPolicy{Version: BrowserDecisionRolloutVersion, Enabled: true, Percentage: 100},
	}
	if _, _, err := coordinator.executeBrowser(context.Background(), request, plan, browserPrimaryExecution); err != nil {
		t.Fatal(err)
	}
	if len(runner.Requests) != 1 || runner.Requests[0].AutonomousRecipe == nil || runner.Requests[0].AutonomousRecipe.PlanSHA256 != planSHA {
		t.Fatalf("runner requests=%+v", runner.Requests)
	}
}

func TestBrowserCoordinatorDecisionRolloutDefaultsToLegacy(t *testing.T) {
	request := browserCoordinatorRequest(t)
	var events []InvestigationEvent
	request.Emit = func(event InvestigationEvent) { events = append(events, event) }
	legacy := &fakeBrowserVerifier{Results: []BrowserVerificationResult{completedBrowserResult("browser/legacy.png")}}
	runner := &fakeBrowserDecisionCoordinatorRunner{
		Capabilities: completeBrowserDecisionRolloutCapabilities(),
		Result:       completedBrowserResult("browser/decision.png"),
	}
	coordinator := BrowserCoordinator{Verifier: legacy, BrowserDecisionRunner: runner}

	if _, _, err := coordinator.executeBrowser(context.Background(), request, rolloutCoordinatorTestPlan(), browserPrimaryExecution); err != nil {
		t.Fatal(err)
	}
	if legacy.Calls != 1 || len(runner.Requests) != 0 {
		t.Fatalf("default must remain on legacy path: legacy=%d runner=%d", legacy.Calls, len(runner.Requests))
	}
	if len(events) != 0 {
		t.Fatalf("default-off registration changed observable events: %#v", events)
	}
}

func TestBrowserCoordinatorDecisionRolloutFailsClosed(t *testing.T) {
	tests := []struct {
		name         string
		policy       BrowserDecisionRolloutPolicy
		production   bool
		capabilities BrowserDecisionRolloutCapabilities
		wantReason   string
	}{
		{
			name: "production", policy: BrowserDecisionRolloutPolicy{Version: 1, Enabled: true, Percentage: 100}, production: true,
			capabilities: completeBrowserDecisionRolloutCapabilities(), wantReason: BrowserDecisionRolloutProduction,
		},
		{
			name: "missing capability", policy: BrowserDecisionRolloutPolicy{Version: 1, Enabled: true, Percentage: 100},
			capabilities: BrowserDecisionRolloutCapabilities{PersistentSession: true}, wantReason: BrowserDecisionRolloutCapabilityMissing,
		},
		{
			name: "invalid config", policy: BrowserDecisionRolloutPolicy{Version: 1, Enabled: true, Percentage: 101},
			capabilities: completeBrowserDecisionRolloutCapabilities(), wantReason: BrowserDecisionRolloutConfigInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := browserCoordinatorRequest(t)
			request.Policy.IsProd = test.production
			var events []InvestigationEvent
			request.Emit = func(event InvestigationEvent) { events = append(events, event) }
			legacy := &fakeBrowserVerifier{Results: []BrowserVerificationResult{completedBrowserResult("browser/legacy.png")}}
			runner := &fakeBrowserDecisionCoordinatorRunner{
				Capabilities: test.capabilities, Result: completedBrowserResult("browser/decision.png"),
			}
			coordinator := BrowserCoordinator{Verifier: legacy, BrowserDecisionRunner: runner, BrowserDecisionPolicy: test.policy}
			if _, _, err := coordinator.executeBrowser(context.Background(), request, rolloutCoordinatorTestPlan(), browserPrimaryExecution); err != nil {
				t.Fatal(err)
			}
			if legacy.Calls != 1 || len(runner.Requests) != 0 {
				t.Fatalf("fail-closed path mismatch: legacy=%d runner=%d", legacy.Calls, len(runner.Requests))
			}
			if len(events) != 1 || events[0].Meta["reason"] != test.wantReason || events[0].Meta["enabled"] != false {
				t.Fatalf("unexpected rollout event: %#v", events)
			}
		})
	}
}

func TestBrowserCoordinatorDecisionRunnerErrorNeverReplaysLegacyPlan(t *testing.T) {
	request := browserCoordinatorRequest(t)
	legacy := &fakeBrowserVerifier{Results: []BrowserVerificationResult{completedBrowserResult("browser/legacy.png")}}
	runner := &fakeBrowserDecisionCoordinatorRunner{
		Capabilities: completeBrowserDecisionRolloutCapabilities(), Err: errors.New("decision session interrupted"),
	}
	coordinator := BrowserCoordinator{
		Verifier: legacy, BrowserDecisionRunner: runner,
		BrowserDecisionPolicy: BrowserDecisionRolloutPolicy{Version: 1, Enabled: true, Percentage: 100},
	}

	if _, _, err := coordinator.executeBrowser(context.Background(), request, rolloutCoordinatorTestPlan(), browserPrimaryExecution); err == nil {
		t.Fatal("expected autonomous runner error")
	}
	if legacy.Calls != 0 || len(runner.Requests) != 1 {
		t.Fatalf("runner error must not replay legacy plan: legacy=%d runner=%d", legacy.Calls, len(runner.Requests))
	}
}

func rolloutCoordinatorTestPlan() BrowserPlan {
	return BrowserPlan{
		Version: 2, DeviceProfile: "desktop", StartURL: "https://app.example.com/users",
		Actions: []BrowserAction{}, Assertions: []BrowserAssertion{},
	}
}
