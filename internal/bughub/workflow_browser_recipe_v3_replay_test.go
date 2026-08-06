package bughub

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestAutonomousRecipeDecisionProviderRebindsCurrentUniqueAnchor(t *testing.T) {
	plan, recipe := autonomousRecipeReplayFixture(t, "attempt-recipe-replay")
	current := boundBrowserDecisionScene(t, "attempt-recipe-replay", []BrowserSceneElement{recipeReplayElement("e-current")}, nil)
	fallbackCalls := 0
	provider, err := newAutonomousRecipeDecisionProvider(recipe, plan, BrowserDecisionProviderFunc(func(context.Context, BrowserDecisionLoopObservation) ([]byte, error) {
		fallbackCalls++
		return nil, fmt.Errorf("unexpected fallback")
	}))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := provider.DecideBrowserStep(context.Background(), BrowserDecisionLoopObservation{
		AttemptID: current.AttemptID, StepNo: 1, Scene: current, Plan: plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := ParseBrowserDecision(raw, current.SceneID)
	if err != nil {
		t.Fatal(err)
	}
	if fallbackCalls != 0 || decision.Action == nil || decision.Action.ActionID != "open-users" || decision.Action.ElementRef != "e-current" || decision.RationaleCode != "scenario_next_step" {
		t.Fatalf("decision=%+v fallback=%d", decision, fallbackCalls)
	}
	if _, err := BindBrowserDecisionStep(plan, decision, current, current.AttemptID); err != nil {
		t.Fatalf("rebound recipe decision did not pass Host binding: %v", err)
	}
}

func TestAutonomousRecipeDecisionProviderFallsBackOnAmbiguousGrounding(t *testing.T) {
	plan, recipe := autonomousRecipeReplayFixture(t, "attempt-recipe-ambiguous")
	current := boundBrowserDecisionScene(t, "attempt-recipe-ambiguous", []BrowserSceneElement{
		recipeReplayElement("e-one"), recipeReplayElement("e-two"),
	}, nil)
	fallbackCalls := 0
	provider, err := newAutonomousRecipeDecisionProvider(recipe, plan, BrowserDecisionProviderFunc(func(_ context.Context, observation BrowserDecisionLoopObservation) ([]byte, error) {
		fallbackCalls++
		return []byte(fmt.Sprintf(`{"version":1,"decision":"conclude","scene_id":"%s","rationale_code":"evidence_sufficient","conclusion_code":"current_evidence_sufficient"}`, observation.Scene.SceneID)), nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := provider.DecideBrowserStep(context.Background(), BrowserDecisionLoopObservation{
		AttemptID: current.AttemptID, StepNo: 1, Scene: current, Plan: plan,
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := ParseBrowserDecision(raw, current.SceneID)
	if err != nil {
		t.Fatal(err)
	}
	if fallbackCalls != 1 || decision.Decision != "conclude" {
		t.Fatalf("decision=%+v fallback=%d", decision, fallbackCalls)
	}
}

func TestAutonomousRecipeRebindsTargetEffectWithoutPersistingEphemeralRef(t *testing.T) {
	plan, err := ParseBrowserPlan([]byte(`version: 2
scenario_contract:
  version: 1
  goal: 输入搜索条件
  basis: bug
  causal_action_ids: [enter-name]
  frontend_entry_ids: [admin]
  evidence: [{kind: ui_assertions}]
  context_sha256: ` + strings.Repeat("f", 64) + `
start_url: https://app.test/content
actions:
  - id: enter-name
    action: fill
    locator: {kind: placeholder, value: 请输入名称, exact: true}
    value: 汤圆
assertions: [{kind: visible_text, value: 搜索结果}]
`))
	if err != nil {
		t.Fatal(err)
	}
	before := boundBrowserDecisionScene(t, "attempt-recipe-effect", []BrowserSceneElement{{
		Ref: "e-old", FrameRef: "f-main", Role: "textbox", Name: "名称", Tag: "input",
		LocatorHints: BrowserSceneLocatorHints{Placeholder: "请输入名称"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true, Editable: true},
		Box:          BrowserSceneBox{X: 10, Y: 10, Width: 100, Height: 20},
	}}, nil)
	decision, err := ParseBrowserDecision([]byte(fmt.Sprintf(`{"version":1,"decision":"act","scene_id":"%s","rationale_code":"scenario_next_step","action":{"action_id":"enter-name","type":"fill","element_ref":"e-old"},"expected_effect":{"any_of":[{"kind":"input_value_persisted","element_ref":"e-old"}]}}`, before.SceneID)), before.SceneID)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindBrowserDecisionStep(plan, decision, before, before.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := CompileAutonomousValidationRecipe(plan, plan.ScenarioContract.ContextSHA256, []AutonomousRecipeTraceStep{{
		Before: before, Bound: bound, Outcome: BrowserEffectConfirmed, FrontendEntryID: "admin",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := recipe.Steps[0].ExpectedEffect.AnyOf[0].ElementRef; got != autonomousRecipeTargetRef || strings.Contains(fmt.Sprintf("%+v", recipe), "e-old") {
		t.Fatalf("recipe retained ephemeral effect ref: %+v", recipe)
	}
	current := before
	current.Elements[0].Ref = "e-new"
	rebindBrowserDecisionTestScene(t, &current)
	provider, err := newAutonomousRecipeDecisionProvider(recipe, plan, BrowserDecisionProviderFunc(func(context.Context, BrowserDecisionLoopObservation) ([]byte, error) {
		return nil, fmt.Errorf("unexpected fallback")
	}))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := provider.DecideBrowserStep(context.Background(), BrowserDecisionLoopObservation{AttemptID: current.AttemptID, StepNo: 1, Scene: current, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := ParseBrowserDecision(raw, current.SceneID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Action.ElementRef != "e-new" || replayed.ExpectedEffect.AnyOf[0].ElementRef != "e-new" {
		t.Fatalf("replayed=%+v", replayed)
	}
}

func autonomousRecipeReplayFixture(t *testing.T, attemptID string) (BrowserPlan, AutonomousValidationRecipe) {
	t.Helper()
	plan, err := ParseBrowserPlan([]byte(validBrowserPlanYAML()))
	if err != nil {
		t.Fatal(err)
	}
	scenarioSHA := strings.Repeat("a", 64)
	plan.ScenarioContract.ContextSHA256 = scenarioSHA
	plan.ScenarioContract.FrontendEntryIDs = []string{"admin"}
	before := boundBrowserDecisionScene(t, attemptID, []BrowserSceneElement{recipeReplayElement("e-old")}, nil)
	decision, err := ParseBrowserDecision([]byte(fmt.Sprintf(`{"version":1,"decision":"act","scene_id":"%s","rationale_code":"scenario_next_step","action":{"action_id":"open-users","type":"click","element_ref":"e-old"},"expected_effect":{"any_of":[{"kind":"text_visible","text":"汤圆"}]}}`, before.SceneID)), before.SceneID)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindBrowserDecisionStep(plan, decision, before, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	recipe, err := CompileAutonomousValidationRecipe(plan, scenarioSHA, []AutonomousRecipeTraceStep{{
		Before: before, Bound: bound, Outcome: BrowserEffectConfirmed, FrontendEntryID: "admin",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return plan, recipe
}

func recipeReplayElement(ref string) BrowserSceneElement {
	return BrowserSceneElement{
		Ref: ref, FrameRef: "f-main", Role: "tab", Name: "用户", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{TestID: "users-tab", Label: "用户"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 10, Y: 10, Width: 100, Height: 20},
	}
}
