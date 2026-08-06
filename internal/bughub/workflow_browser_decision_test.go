package bughub

import (
	"strings"
	"testing"
)

func TestParseBrowserDecisionAcceptsBoundAct(t *testing.T) {
	raw := `version: 1
decision: act
scene_id: scene-1234
rationale_code: scenario_next_step
action:
  action_id: open-target-row
  type: click
  element_ref: e-17
expected_effect:
  any_of:
    - kind: surface_opened
      role: dialog
      name_contains: 视频详情
    - kind: url_changed
      same_origin_path_contains: /content/detail
passive_checks:
  - kind: screenshot
`
	decision, err := ParseBrowserDecision([]byte(raw), "scene-1234")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action == nil || decision.Action.ElementRef != "e-17" || len(decision.ExpectedEffect.AnyOf) != 2 {
		t.Fatalf("decision = %+v", decision)
	}
}

func TestParseBrowserDecisionAcceptsHostOwnedDismissSurfaceWithoutElementRef(t *testing.T) {
	raw := `version: 1
decision: act
scene_id: scene-dismiss
rationale_code: scenario_next_step
action:
  action_id: close-author-selector
  type: dismiss_surface
expected_effect:
  any_of:
    - kind: surface_closed
      role: dialog
      name_contains: 作者用户选择
`
	decision, err := ParseBrowserDecision([]byte(raw), "scene-dismiss")
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action == nil || decision.Action.Type != "dismiss_surface" || decision.Action.ElementRef != "" {
		t.Fatalf("decision=%+v", decision)
	}

	withElement := strings.Replace(raw, "  type: dismiss_surface", "  type: dismiss_surface\n  element_ref: e-close", 1)
	if _, err := ParseBrowserDecision([]byte(withElement), "scene-dismiss"); err == nil {
		t.Fatal("dismiss_surface decision accepted an Agent-selected element")
	}
}

func TestParseBrowserDecisionAcceptsNonActionBranches(t *testing.T) {
	for name, raw := range map[string]string{
		"conclude": `version: 1
decision: conclude
scene_id: scene-1
rationale_code: evidence_sufficient
conclusion_code: current_evidence_sufficient
`,
		"assist": `version: 1
decision: assist
scene_id: scene-1
rationale_code: user_fact_required
questions:
  - id: intended_state
    question: 当前记录应处于已发布还是草稿状态？
    answer_hint: 请确认该测试数据的预期业务状态
`,
		"capability_gap": `version: 1
decision: capability_gap
scene_id: scene-1
rationale_code: automation_exhausted
capability_gap_code: browser_capability_gap
exhausted_channels: [semantic_grounding, structured_grounding, safe_exploration]
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBrowserDecision([]byte(raw), "scene-1"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestParseBrowserDecisionRejectsUnsafeOrUnboundOutput(t *testing.T) {
	valid := `version: 1
decision: act
scene_id: scene-1
rationale_code: locator_recovery
action: {action_id: submit, type: click, element_ref: e-1}
expected_effect:
  any_of: [{kind: text_visible, text: 保存成功}]
`
	tests := map[string]string{
		"stale scene":          strings.Replace(valid, "scene-1", "scene-old", 1),
		"unknown field":        valid + "script: document.body.click()\n",
		"forged locator":       strings.Replace(valid, "element_ref: e-1", "element_ref: '#submit'", 1),
		"scene only effect":    strings.Replace(valid, "{kind: text_visible, text: 保存成功}", "{kind: scene_changed}", 1),
		"credential page text": strings.Replace(valid, "保存成功", "token=top-secret", 1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseBrowserDecision([]byte(raw), "scene-1"); err == nil {
				t.Fatal("unsafe decision was accepted")
			}
		})
	}
}
