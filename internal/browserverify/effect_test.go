package browserverify

import (
	"testing"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

func effectTestScene(id, rawURL string) *bughub.BrowserScene {
	return &bughub.BrowserScene{
		Version: bughub.BrowserSceneVersion, SceneID: id, URL: rawURL,
		Capabilities: bughub.BrowserSceneCapabilities{DOM: "available"},
	}
}

func effectTestDecision(effect bughub.BrowserDecisionEffect) bughub.BrowserDecision {
	return bughub.BrowserDecision{
		Version: bughub.BrowserDecisionVersion, Decision: "act", SceneID: "scene-before", RationaleCode: "scenario_next_step",
		Action:         &bughub.BrowserDecisionAction{ActionID: "open-detail", Type: "click", ElementRef: "e-1"},
		ExpectedEffect: &bughub.BrowserDecisionExpectedEffects{AnyOf: []bughub.BrowserDecisionEffect{effect}},
	}
}

func TestEvaluateBrowserDecisionEffectsConfirmsSemanticAfterScene(t *testing.T) {
	before := effectTestScene("scene-before", "https://app.test/content")
	after := effectTestScene("scene-after", "https://app.test/content/42")
	after.ActiveSurface = &bughub.BrowserSceneSurface{Ref: "s-1", Type: "dialog", Name: "视频详情", Modal: true}
	after.Elements = []bughub.BrowserSceneElement{{
		Ref: "e-9", Role: "button", Name: "保存", States: bughub.BrowserSceneElementStates{Visible: true, InViewport: true},
	}}
	after.TextBlocks = []bughub.BrowserSceneTextBlock{{Ref: "t-1", Text: "已发布"}}
	receipt := BrowserStepReceipt{ActionID: "open-detail", ActionType: "click", TargetElementRef: "e-1"}

	for name, effect := range map[string]bughub.BrowserDecisionEffect{
		"url":     {Kind: "url_changed", SameOriginPathContains: "/content/42"},
		"surface": {Kind: "surface_opened", Role: "dialog", NameContains: "视频详情"},
		"element": {Kind: "element_visible", Role: "button", NameContains: "保存"},
		"text":    {Kind: "text_visible", Text: "已发布"},
	} {
		t.Run(name, func(t *testing.T) {
			got := EvaluateBrowserDecisionEffects(effectTestDecision(effect), before, after, receipt)
			if got.Outcome != bughub.BrowserEffectConfirmed || len(got.ConfirmedKinds) != 1 {
				t.Fatalf("evaluation = %+v", got)
			}
		})
	}
}

func TestEvaluateBrowserDecisionEffectsUsesValueFreeActionReceipt(t *testing.T) {
	persisted := true
	decision := effectTestDecision(bughub.BrowserDecisionEffect{Kind: "input_value_persisted", ElementRef: "e-1"})
	decision.Action.Type = "fill"
	receipt := BrowserStepReceipt{
		ActionID: "open-detail", ActionType: "fill", TargetElementRef: "e-1", InputPersisted: &persisted,
	}
	got := EvaluateBrowserDecisionEffects(decision, effectTestScene("scene-before", "https://app.test"), nil, receipt)
	if got.Outcome != bughub.BrowserEffectConfirmed {
		t.Fatalf("evaluation = %+v", got)
	}
}

func TestEvaluateBrowserDecisionEffectsClassifiesFailureModes(t *testing.T) {
	decision := effectTestDecision(bughub.BrowserDecisionEffect{Kind: "text_visible", Text: "保存成功"})
	before := effectTestScene("scene-before", "https://app.test")
	after := effectTestScene("scene-after", "https://app.test")
	base := BrowserStepReceipt{ActionID: "open-detail", ActionType: "click", TargetElementRef: "e-1"}

	tests := []struct {
		name    string
		after   *bughub.BrowserScene
		receipt BrowserStepReceipt
		want    string
	}{
		{name: "no effect", after: after, receipt: base, want: bughub.BrowserEffectNoEffect},
		{name: "ambiguous", after: nil, receipt: base, want: bughub.BrowserEffectAmbiguous},
		{name: "blocked", after: after, receipt: BrowserStepReceipt{ActionID: "open-detail", ActionType: "click", BlockedCode: "element_not_enabled"}, want: bughub.BrowserEffectBlocked},
		{name: "uncertain", after: nil, receipt: BrowserStepReceipt{ActionID: "open-detail", ActionType: "click", Interrupted: true}, want: bughub.BrowserEffectUncertain},
		{name: "wrong receipt", after: after, receipt: BrowserStepReceipt{ActionID: "other", ActionType: "click"}, want: bughub.BrowserEffectAmbiguous},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := EvaluateBrowserDecisionEffects(decision, before, test.after, test.receipt)
			if got.Outcome != test.want {
				t.Fatalf("outcome = %q, want %q (%+v)", got.Outcome, test.want, got)
			}
		})
	}
}

func TestEvaluateBrowserDecisionEffectsAnyOfConfirmsOneProvingEffect(t *testing.T) {
	decision := effectTestDecision(bughub.BrowserDecisionEffect{Kind: "text_visible", Text: "missing"})
	decision.ExpectedEffect.AnyOf = append(decision.ExpectedEffect.AnyOf, bughub.BrowserDecisionEffect{Kind: "network_request_observed", EvidenceRef: "request-create"})
	receipt := BrowserStepReceipt{
		ActionID: "open-detail", ActionType: "click", EvidenceRefs: map[string]struct{}{"request-create": {}},
	}
	got := EvaluateBrowserDecisionEffects(decision, nil, nil, receipt)
	if got.Outcome != bughub.BrowserEffectConfirmed || len(got.ConfirmedKinds) != 1 || got.ConfirmedKinds[0] != "network_request_observed" {
		t.Fatalf("evaluation = %+v", got)
	}
}
