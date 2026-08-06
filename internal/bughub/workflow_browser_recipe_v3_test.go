package bughub

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCompileAutonomousValidationRecipeFreezesMultipleAnchorsWithoutElementRef(t *testing.T) {
	plan, err := ParseBrowserPlan([]byte(validBrowserPlanYAML()))
	if err != nil {
		t.Fatal(err)
	}
	scenarioSHA := strings.Repeat("a", 64)
	plan.ScenarioContract.ContextSHA256 = scenarioSHA
	plan.ScenarioContract.FrontendEntryIDs = []string{"admin"}
	scene := boundBrowserDecisionScene(t, "attempt-recipe-v3", []BrowserSceneElement{{
		Ref: "ephemeral-e-17", FrameRef: "f-main", Role: "tab", Name: "用户", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{TestID: "users-tab", Label: "用户"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 100, Y: 50, Width: 80, Height: 32},
		Relations:    BrowserSceneElementRelations{GroupName: "主导航"},
	}}, nil)
	decision, err := ParseBrowserDecision([]byte(`version: 1
decision: act
scene_id: `+scene.SceneID+`
rationale_code: scenario_next_step
action: {action_id: open-users, type: click, element_ref: ephemeral-e-17}
expected_effect:
  any_of: [{kind: text_visible, text: 汤圆}]
`), scene.SceneID)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindBrowserDecisionStep(plan, decision, scene, scene.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := CompileAutonomousValidationRecipe(plan, scenarioSHA, []AutonomousRecipeTraceStep{{
		Before: scene, Bound: bound, Outcome: BrowserEffectConfirmed, FrontendEntryID: "admin",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if recipe.Version != AutonomousValidationRecipeVersion || len(recipe.Steps) != 1 || len(recipe.Steps[0].Anchors) < 3 ||
		recipe.Steps[0].FrozenAction.Locator != nil || recipe.Steps[0].Precondition.RelationText != "主导航" {
		t.Fatalf("recipe=%+v", recipe)
	}
	encoded, err := json.Marshal(recipe)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("ephemeral-e-17")) || bytes.Contains(encoded, []byte(`"locator"`)) {
		t.Fatalf("recipe retained ephemeral grounding: %s", encoded)
	}
	parsed, err := ParseAutonomousValidationRecipe(encoded, plan)
	if err != nil || parsed.PlanSHA256 != recipe.PlanSHA256 {
		t.Fatalf("parsed=%+v err=%v", parsed, err)
	}
	if digest, err := autonomousValidationRecipeSHA256(parsed, plan); err != nil || !validLowerSHA256(digest) {
		t.Fatalf("digest=%q err=%v", digest, err)
	}
}

func TestAutonomousValidationRecipeRejectsPlanDriftAndUnknownFields(t *testing.T) {
	plan, err := ParseBrowserPlan([]byte(validBrowserPlanYAML()))
	if err != nil {
		t.Fatal(err)
	}
	scenarioSHA := strings.Repeat("b", 64)
	plan.ScenarioContract.ContextSHA256 = scenarioSHA
	plan.ScenarioContract.FrontendEntryIDs = []string{"admin"}
	scene := boundBrowserDecisionScene(t, "attempt-recipe-v3-drift", []BrowserSceneElement{{
		Ref: "e-1", FrameRef: "f-main", Role: "tab", Name: "用户", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{TestID: "users-tab"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 100, Y: 50, Width: 80, Height: 32},
	}}, nil)
	decision, err := ParseBrowserDecision([]byte(`version: 1
decision: act
scene_id: `+scene.SceneID+`
rationale_code: scenario_next_step
action: {action_id: open-users, type: click, element_ref: e-1}
expected_effect:
  any_of: [{kind: text_visible, text: 汤圆}]
`), scene.SceneID)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindBrowserDecisionStep(plan, decision, scene, scene.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := CompileAutonomousValidationRecipe(plan, scenarioSHA, []AutonomousRecipeTraceStep{{
		Before: scene, Bound: bound, Outcome: BrowserEffectConfirmed, FrontendEntryID: "admin",
	}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(recipe)
	if err != nil {
		t.Fatal(err)
	}
	driftedPlan := plan
	driftedPlan.Actions = append([]BrowserAction(nil), plan.Actions...)
	driftedPlan.Actions[0].ScreenshotAfter = false
	if _, err := ParseAutonomousValidationRecipe(encoded, driftedPlan); err == nil {
		t.Fatal("recipe was accepted against a drifted BrowserPlan")
	}
	unknown := bytes.Replace(encoded, []byte(`"version":3`), []byte(`"version":3,"unknown":true`), 1)
	if _, err := ParseAutonomousValidationRecipe(unknown, plan); err == nil {
		t.Fatal("recipe with an unknown field was accepted")
	}
	bound.Action.Value = "invented-business-value"
	if _, err := CompileAutonomousValidationRecipe(plan, scenarioSHA, []AutonomousRecipeTraceStep{{
		Before: scene, Bound: bound, Outcome: BrowserEffectConfirmed, FrontendEntryID: "admin",
	}}); err == nil {
		t.Fatal("compiler accepted business-value drift")
	}
}

func TestCompileAutonomousValidationRecipePreservesSafeExplorationBeforeCausalAction(t *testing.T) {
	plan, err := ParseBrowserPlan([]byte(validBrowserPlanYAML()))
	if err != nil {
		t.Fatal(err)
	}
	scenarioSHA := strings.Repeat("d", 64)
	plan.ScenarioContract.ContextSHA256 = scenarioSHA
	plan.ScenarioContract.FrontendEntryIDs = []string{"admin"}
	scene := boundBrowserDecisionScene(t, "attempt-recipe-v3-exploration", []BrowserSceneElement{{
		Ref: "e-tab", FrameRef: "f-main", Role: "tab", Name: "用户", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{TestID: "users-tab"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 100, Y: 50, Width: 80, Height: 32},
	}}, nil)
	candidate := BrowserExplorationCandidate{
		Intent: BrowserExplorationOpenTab, RiskProofCode: BrowserExplorationProofARIATab, ElementRef: "e-tab",
		Action: BrowserAction{Action: "click", Locator: &BrowserLocator{Kind: "test_id", Value: "users-tab"}},
	}
	candidate.Action.ID = browserExplorationCandidateActionID(candidate)
	bindingPlan, err := BuildBrowserDecisionPlanWithExploration(plan, scene, scene.AttemptID, []BrowserExplorationCandidate{candidate})
	if err != nil {
		t.Fatal(err)
	}
	explorationDecision, err := ParseBrowserDecision([]byte(`version: 1
decision: act
scene_id: `+scene.SceneID+`
rationale_code: locator_recovery
action: {action_id: `+candidate.Action.ID+`, type: click, element_ref: e-tab}
expected_effect:
  any_of: [{kind: text_visible, text: 汤圆}]
`), scene.SceneID)
	if err != nil {
		t.Fatal(err)
	}
	explorationBound, err := BindBrowserDecisionStep(bindingPlan, explorationDecision, scene, scene.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	scenarioDecision, err := ParseBrowserDecision([]byte(`version: 1
decision: act
scene_id: `+scene.SceneID+`
rationale_code: scenario_next_step
action: {action_id: open-users, type: click, element_ref: e-tab}
expected_effect:
  any_of: [{kind: text_visible, text: 汤圆}]
`), scene.SceneID)
	if err != nil {
		t.Fatal(err)
	}
	scenarioBound, err := BindBrowserDecisionStep(plan, scenarioDecision, scene, scene.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := CompileAutonomousValidationRecipe(plan, scenarioSHA, []AutonomousRecipeTraceStep{
		{Before: scene, Bound: explorationBound, Outcome: BrowserEffectConfirmed, Exploration: true, FrontendEntryID: "admin"},
		{Before: scene, Bound: scenarioBound, Outcome: BrowserEffectConfirmed, FrontendEntryID: "admin"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recipe.Steps) != 2 || recipe.Steps[0].Source != "safe_exploration" || recipe.Steps[0].FrozenAction.Locator != nil || recipe.Steps[1].FrozenAction.ID != "open-users" {
		t.Fatalf("recipe=%+v", recipe)
	}
}

func TestCaseStoreRoundTripsOptionalAutonomousValidationRecipe(t *testing.T) {
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-recipe-v3")
	attempt := PhaseAttempt{
		ID: "attempt-store-recipe-v3", CaseID: "case-recipe-v3", CycleNumber: 1,
		Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusRunning,
		AgentTarget: "codex", BotKey: "validator", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), StartedAt: time.Now().UTC(),
	}
	if err := store.CreateAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	plan, err := ParseBrowserPlan([]byte(validBrowserPlanYAML()))
	if err != nil {
		t.Fatal(err)
	}
	scenarioSHA := strings.Repeat("c", 64)
	plan.ScenarioContract.ContextSHA256 = scenarioSHA
	plan.ScenarioContract.FrontendEntryIDs = []string{"admin"}
	scene := boundBrowserDecisionScene(t, attempt.ID, []BrowserSceneElement{{
		Ref: "e-1", FrameRef: "f-main", Role: "tab", Name: "用户", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{TestID: "users-tab"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 100, Y: 50, Width: 80, Height: 32},
	}}, nil)
	decision, err := ParseBrowserDecision([]byte(`version: 1
decision: act
scene_id: `+scene.SceneID+`
rationale_code: scenario_next_step
action: {action_id: open-users, type: click, element_ref: e-1}
expected_effect:
  any_of: [{kind: text_visible, text: 汤圆}]
`), scene.SceneID)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindBrowserDecisionStep(plan, decision, scene, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	autonomous, err := CompileAutonomousValidationRecipe(plan, scenarioSHA, []AutonomousRecipeTraceStep{{
		Before: scene, Bound: bound, Outcome: BrowserEffectConfirmed, FrontendEntryID: "admin",
	}})
	if err != nil {
		t.Fatal(err)
	}
	planSHA, err := durableBrowserPlanSHA256(plan)
	if err != nil {
		t.Fatal(err)
	}
	autonomousSHA, err := autonomousValidationRecipeSHA256(autonomous, plan)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := store.StoreValidationRecipe(context.Background(), ValidationRecipe{
		CaseID: attempt.CaseID, ScenarioSHA256: scenarioSHA, PlanSHA256: planSHA, Plan: plan,
		AutonomousRecipeSHA256: autonomousSHA, AutonomousRecipe: &autonomous, SourceAttemptID: attempt.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.AutonomousRecipe == nil || stored.AutonomousRecipeSHA256 != autonomousSHA || len(stored.AutonomousRecipe.Steps) != 1 {
		t.Fatalf("stored=%+v", stored)
	}
	loaded, found, err := store.GetValidationRecipe(context.Background(), attempt.CaseID)
	if err != nil || !found || loaded.AutonomousRecipe == nil || loaded.AutonomousRecipe.PlanSHA256 != planSHA {
		t.Fatalf("loaded=%+v found=%v err=%v", loaded, found, err)
	}

	trace := []AutonomousRecipeTraceStep{{
		Before: scene, Bound: bound, Outcome: BrowserEffectConfirmed, FrontendEntryID: "admin",
	}}
	compiled, err := validationRecipeFromBrowserExecution(context.Background(), nil, attempt.CaseID, attempt.ID, scenarioSHA, plan, BrowserVerificationResult{
		autonomousTrace: trace, autonomousScenarioSHA256: scenarioSHA,
	})
	if err != nil || compiled.AutonomousRecipe == nil || compiled.AutonomousRecipeSHA256 == "" {
		t.Fatalf("compiled=%+v err=%v", compiled, err)
	}
	preserved, err := validationRecipeFromBrowserExecution(
		context.Background(), store, attempt.CaseID, attempt.ID, scenarioSHA, plan, BrowserVerificationResult{},
	)
	if err != nil || preserved.AutonomousRecipe == nil || preserved.AutonomousRecipeSHA256 != autonomousSHA {
		t.Fatalf("preserved=%+v err=%v", preserved, err)
	}
	drifted := plan
	drifted.Actions = append([]BrowserAction(nil), plan.Actions...)
	drifted.Actions[0].ScreenshotAfter = !drifted.Actions[0].ScreenshotAfter
	notPreserved, err := validationRecipeFromBrowserExecution(
		context.Background(), store, attempt.CaseID, attempt.ID, scenarioSHA, drifted, BrowserVerificationResult{},
	)
	if err != nil || notPreserved.AutonomousRecipe != nil {
		t.Fatalf("drifted recipe was preserved: %+v err=%v", notPreserved, err)
	}
}
