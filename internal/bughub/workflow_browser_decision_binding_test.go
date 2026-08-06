package bughub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func boundBrowserDecisionScene(t *testing.T, attemptID string, elements []BrowserSceneElement, surface *BrowserSceneSurface) BrowserScene {
	t.Helper()
	scene := BrowserScene{
		Version: BrowserSceneVersion, AttemptID: attemptID, CapturedAt: "2026-08-04T00:00:00Z",
		URL: "https://app.test/content", Title: "内容列表", DeviceProfile: "desktop",
		Viewport: BrowserSceneViewport{Width: 1280, Height: 720}, ActiveSurface: surface,
		Frames:   []BrowserSceneFrame{{Ref: "f-main", URL: "https://app.test/content", SameOrigin: true}},
		Elements: elements, TextBlocks: []BrowserSceneTextBlock{},
		Capabilities: BrowserSceneCapabilities{DOM: "available", Accessibility: "partial", Screenshot: "available", VisionGrounding: "disabled", FrameObservation: "main_only"},
	}
	encoded, err := json.Marshal(scene)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	scene.SceneSHA256 = hex.EncodeToString(digest[:])
	scene.SceneID = "scene-" + scene.SceneSHA256[:16]
	return scene
}

func browserDecisionBindingPlan(t *testing.T) BrowserPlan {
	t.Helper()
	plan, err := ParseBrowserPlan([]byte(`version: 2
start_url: https://app.test/content
actions:
  - id: open-row
    action: click
    locator:
      kind: role
      value: button
      name: 旧定位
      exact: true
assertions:
  - kind: visible_text
    value: 视频详情
`))
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func browserActDecisionForScene(t *testing.T, scene BrowserScene) BrowserDecision {
	t.Helper()
	decision, err := ParseBrowserDecision([]byte(`version: 1
decision: act
scene_id: `+scene.SceneID+`
rationale_code: scenario_next_step
action:
  action_id: open-row
  type: click
  element_ref: e-1
expected_effect:
  any_of:
    - kind: surface_opened
      role: dialog
      name_contains: 视频详情
passive_checks:
  - kind: screenshot
`), scene.SceneID)
	if err != nil {
		t.Fatal(err)
	}
	return decision
}

func TestBindBrowserDecisionStepUsesCurrentElementButFrozenActionSemantics(t *testing.T) {
	attemptID := "attempt-decision-bind"
	scene := boundBrowserDecisionScene(t, attemptID, []BrowserSceneElement{{
		Ref: "e-1", FrameRef: "f-main", Role: "button", Name: "查看", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{TestID: "view-button"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 100, Y: 200, Width: 80, Height: 32},
		Relations:    BrowserSceneElementRelations{RowName: "测试都市生活剧"},
	}}, nil)
	decision := browserActDecisionForScene(t, scene)

	bound, err := BindBrowserDecisionStep(browserDecisionBindingPlan(t), decision, scene, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if !validLowerSHA256(bound.DecisionSHA256) || !validLowerSHA256(bound.ActionFingerprint) || bound.BeforeSceneSHA256 != scene.SceneSHA256 || bound.ElementRef != "e-1" {
		t.Fatalf("bound identity=%+v", bound)
	}
	if bound.Action.ID != "open-row" || bound.Action.Action != "click" || bound.Action.Locator == nil || bound.Action.Locator.Kind != "test_id" || bound.Action.Locator.Value != "view-button" || bound.Action.Locator.Within == nil || bound.Action.Locator.Within.Name != "测试都市生活剧" {
		t.Fatalf("bound action=%+v", bound.Action)
	}
	second, err := BindBrowserDecisionStep(browserDecisionBindingPlan(t), decision, scene, attemptID)
	if err != nil || second.DecisionSHA256 != bound.DecisionSHA256 || second.ActionFingerprint != bound.ActionFingerprint {
		t.Fatalf("non-deterministic binding second=%+v err=%v", second, err)
	}
}

func TestBindBrowserDecisionStepKeepsDismissSurfaceHostOwned(t *testing.T) {
	plan, err := ParseBrowserPlan([]byte(`version: 2
start_url: https://app.test/content
actions:
  - id: close-author-selector
    action: dismiss_surface
assertions:
  - kind: visible_text
    value: 用户信息管理
`))
	if err != nil {
		t.Fatal(err)
	}
	attemptID := "attempt-decision-dismiss"
	surface := &BrowserSceneSurface{Ref: "s-author", Type: "dialog", Name: "作者用户选择", Modal: true}
	scene := boundBrowserDecisionScene(t, attemptID, nil, surface)
	decision, err := ParseBrowserDecision([]byte(`version: 1
decision: act
scene_id: `+scene.SceneID+`
rationale_code: scenario_next_step
action:
  action_id: close-author-selector
  type: dismiss_surface
expected_effect:
  any_of:
    - kind: surface_closed
      role: dialog
      name_contains: 作者用户选择
`), scene.SceneID)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindBrowserDecisionStep(plan, decision, scene, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if bound.ElementRef != "" || bound.Action.Action != "dismiss_surface" || bound.Action.Locator != nil || bound.Action.Key != "" {
		t.Fatalf("bound=%+v", bound)
	}

	withoutSurface := boundBrowserDecisionScene(t, attemptID, nil, nil)
	decision.SceneID = withoutSurface.SceneID
	if _, err := BindBrowserDecisionStep(plan, decision, withoutSurface, attemptID); err == nil {
		t.Fatal("dismiss_surface bound without an active surface")
	}
}

func TestBrowserScenarioDirectTargetAvailabilityUsesCurrentSafeScene(t *testing.T) {
	exact := true
	plan := browserDecisionBindingPlan(t)
	plan.Actions[0].Locator = &BrowserLocator{Kind: "role", Value: "button", Name: "查看", Exact: &exact}
	scene := boundBrowserDecisionScene(t, "attempt-direct-target", []BrowserSceneElement{{
		Ref: "e-1", FrameRef: "f-main", Role: "button", Name: "查看", Tag: "button",
		States: BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:    BrowserSceneBox{X: 1, Y: 1, Width: 10, Height: 10},
	}}, nil)
	if !browserScenarioDirectTargetAvailable(plan, 0, scene) {
		t.Fatal("safe current scenario target was not detected")
	}
	plan.Actions[0].Locator.Name = "不存在"
	if browserScenarioDirectTargetAvailable(plan, 0, scene) {
		t.Fatal("stale scenario target was treated as directly available")
	}
	plan.Actions[0] = BrowserAction{ID: "capture", Action: "screenshot"}
	if browserScenarioDirectTargetAvailable(plan, 0, scene) {
		t.Fatal("passive screenshot must not suppress safe exploration")
	}
}

func TestBindBrowserDecisionStepRejectsStaleUnsafeOrInventedTargets(t *testing.T) {
	attemptID := "attempt-decision-reject"
	surface := &BrowserSceneSurface{Ref: "s-1", Type: "dialog", Name: "编辑", Modal: true}
	baseElement := BrowserSceneElement{
		Ref: "e-1", FrameRef: "f-main", SurfaceRef: "s-1", Role: "button", Name: "保存", Tag: "button",
		LocatorHints: BrowserSceneLocatorHints{Label: "保存"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true},
		Box:          BrowserSceneBox{X: 100, Y: 200, Width: 80, Height: 32},
	}
	tests := []struct {
		name   string
		mutate func(*BrowserDecision, *BrowserScene)
	}{
		{name: "scene digest mutation", mutate: func(_ *BrowserDecision, scene *BrowserScene) { scene.Title = "被替换页面" }},
		{name: "stale ref", mutate: func(decision *BrowserDecision, _ *BrowserScene) { decision.Action.ElementRef = "e-9" }},
		{name: "wrong action type", mutate: func(decision *BrowserDecision, _ *BrowserScene) { decision.Action.Type = "press" }},
		{name: "background target", mutate: func(decision *BrowserDecision, scene *BrowserScene) {
			scene.Elements[0].SurfaceRef = ""
			rebindBrowserDecisionTestScene(t, scene)
			decision.SceneID = scene.SceneID
		}},
		{name: "obscured target", mutate: func(decision *BrowserDecision, scene *BrowserScene) {
			scene.Elements[0].States.Obscured = true
			rebindBrowserDecisionTestScene(t, scene)
			decision.SceneID = scene.SceneID
		}},
		{name: "no semantic locator", mutate: func(decision *BrowserDecision, scene *BrowserScene) {
			scene.Elements[0].LocatorHints = BrowserSceneLocatorHints{}
			scene.Elements[0].Role = ""
			scene.Elements[0].Name = ""
			rebindBrowserDecisionTestScene(t, scene)
			decision.SceneID = scene.SceneID
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scene := boundBrowserDecisionScene(t, attemptID, []BrowserSceneElement{baseElement}, surface)
			decision := browserActDecisionForScene(t, scene)
			test.mutate(&decision, &scene)
			if _, err := BindBrowserDecisionStep(browserDecisionBindingPlan(t), decision, scene, attemptID); err == nil {
				t.Fatal("unsafe browser decision binding was accepted")
			}
		})
	}
}

func TestBindBrowserDecisionStepKeepsPlanValueOutOfDurableIdentity(t *testing.T) {
	plan, err := ParseBrowserPlan([]byte(`version: 2
start_url: https://app.test/search
actions:
  - id: enter-query
    action: fill
    locator:
      kind: placeholder
      value: 搜索
    value: 汤圆-用户查询
assertions:
  - kind: visible_text
    value: 搜索结果
`))
	if err != nil {
		t.Fatal(err)
	}
	attemptID := "attempt-decision-fill"
	scene := boundBrowserDecisionScene(t, attemptID, []BrowserSceneElement{{
		Ref: "e-1", FrameRef: "f-main", Role: "searchbox", Name: "搜索", Tag: "input",
		LocatorHints: BrowserSceneLocatorHints{Placeholder: "搜索"},
		States:       BrowserSceneElementStates{Visible: true, InViewport: true, Enabled: true, Editable: true},
		Box:          BrowserSceneBox{X: 10, Y: 20, Width: 240, Height: 32},
	}}, nil)
	decision, err := ParseBrowserDecision([]byte(`version: 1
decision: act
scene_id: `+scene.SceneID+`
rationale_code: scenario_next_step
action:
  action_id: enter-query
  type: fill
  element_ref: e-1
expected_effect:
  any_of:
    - kind: input_value_persisted
      element_ref: e-1
`), scene.SceneID)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := BindBrowserDecisionStep(plan, decision, scene, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if bound.Action.Value != "汤圆-用户查询" {
		t.Fatalf("frozen action value changed: %+v", bound.Action)
	}
	identityJSON, err := json.Marshal(struct {
		DecisionSHA256    string `json:"decision_sha256"`
		ActionFingerprint string `json:"action_fingerprint"`
	}{bound.DecisionSHA256, bound.ActionFingerprint})
	if err != nil || strings.Contains(string(identityJSON), "汤圆-用户查询") {
		t.Fatalf("durable identity leaked the action value: %s err=%v", identityJSON, err)
	}
}

func rebindBrowserDecisionTestScene(t *testing.T, scene *BrowserScene) {
	t.Helper()
	scene.SceneID = ""
	scene.SceneSHA256 = ""
	encoded, err := json.Marshal(scene)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	scene.SceneSHA256 = hex.EncodeToString(digest[:])
	scene.SceneID = "scene-" + scene.SceneSHA256[:16]
}
