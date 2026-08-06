package browserverify

import (
	"net/url"
	"reflect"
	"strings"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

// BrowserStepReceipt contains only Host/Worker-observed facts. It must never
// contain an input value, selected value, response body, or credential.
type BrowserStepReceipt struct {
	ActionID           string
	ActionType         string
	TargetElementRef   string
	InputPersisted     *bool
	SelectionPersisted *bool
	EvidenceRefs       map[string]struct{}
	BlockedCode        string
	Interrupted        bool
}

type browserEffectMatch struct {
	confirmed bool
	known     bool
}

// EvaluateBrowserDecisionEffects evaluates any_of semantics. A scene_changed
// signal is deliberately auxiliary: ParseBrowserDecision requires at least
// one business-proving effect in every act decision.
func EvaluateBrowserDecisionEffects(
	decision bughub.BrowserDecision,
	before, after *bughub.BrowserScene,
	receipt BrowserStepReceipt,
) bughub.BrowserStepEffectEvaluation {
	evaluation := bughub.BrowserStepEffectEvaluation{
		ConfirmedKinds:   []string{},
		UnconfirmedKinds: []string{},
	}
	if decision.Action == nil || decision.ExpectedEffect == nil || receipt.ActionID != decision.Action.ActionID || receipt.ActionType != decision.Action.Type {
		evaluation.Outcome = bughub.BrowserEffectAmbiguous
		return evaluation
	}
	unknown := false
	for _, effect := range decision.ExpectedEffect.AnyOf {
		matched := matchBrowserDecisionEffect(effect, decision.Action.ElementRef, before, after, receipt)
		if matched.confirmed {
			evaluation.ConfirmedKinds = append(evaluation.ConfirmedKinds, effect.Kind)
		} else {
			evaluation.UnconfirmedKinds = append(evaluation.UnconfirmedKinds, effect.Kind)
			unknown = unknown || !matched.known
		}
	}
	if len(evaluation.ConfirmedKinds) != 0 {
		evaluation.Outcome = bughub.BrowserEffectConfirmed
		return evaluation
	}
	if receipt.Interrupted {
		evaluation.Outcome = bughub.BrowserEffectUncertain
		return evaluation
	}
	if strings.TrimSpace(receipt.BlockedCode) != "" {
		evaluation.Outcome = bughub.BrowserEffectBlocked
		evaluation.BlockCode = safeVerifierIdentifier(receipt.BlockedCode, 128)
		return evaluation
	}
	if unknown {
		evaluation.Outcome = bughub.BrowserEffectAmbiguous
		return evaluation
	}
	evaluation.Outcome = bughub.BrowserEffectNoEffect
	return evaluation
}

func matchBrowserDecisionEffect(
	effect bughub.BrowserDecisionEffect,
	actionElementRef string,
	before, after *bughub.BrowserScene,
	receipt BrowserStepReceipt,
) browserEffectMatch {
	switch effect.Kind {
	case "url_changed":
		if before == nil || after == nil {
			return browserEffectMatch{}
		}
		changed := before.URL != after.URL
		return browserEffectMatch{confirmed: changed && browserScenePathContains(after.URL, effect.SameOriginPathContains), known: true}
	case "url_contains":
		if after == nil {
			return browserEffectMatch{}
		}
		return browserEffectMatch{confirmed: browserScenePathContains(after.URL, effect.SameOriginPathContains), known: true}
	case "surface_opened":
		if before == nil || after == nil {
			return browserEffectMatch{}
		}
		return browserEffectMatch{
			confirmed: !browserSceneSurfaceMatches(before.ActiveSurface, effect) && browserSceneSurfaceMatches(after.ActiveSurface, effect),
			known:     true,
		}
	case "surface_closed":
		if before == nil || after == nil {
			return browserEffectMatch{}
		}
		return browserEffectMatch{
			confirmed: browserSceneSurfaceMatches(before.ActiveSurface, effect) && !browserSceneSurfaceMatches(after.ActiveSurface, effect),
			known:     true,
		}
	case "element_visible", "element_absent":
		if after == nil || after.Capabilities.DOM != "available" {
			return browserEffectMatch{}
		}
		visible := browserSceneElementMatches(after, effect)
		if effect.Kind == "element_absent" {
			visible = !visible
		}
		return browserEffectMatch{confirmed: visible, known: true}
	case "text_visible", "text_absent":
		if after == nil || after.Capabilities.DOM != "available" {
			return browserEffectMatch{}
		}
		visible := browserSceneContainsText(after, effect.Text)
		if effect.Kind == "text_absent" {
			visible = !visible
		}
		return browserEffectMatch{confirmed: visible, known: true}
	case "input_value_persisted":
		if receipt.TargetElementRef != actionElementRef || receipt.InputPersisted == nil {
			return browserEffectMatch{}
		}
		return browserEffectMatch{confirmed: *receipt.InputPersisted, known: true}
	case "selection_persisted":
		if receipt.TargetElementRef != actionElementRef || receipt.SelectionPersisted == nil {
			return browserEffectMatch{}
		}
		return browserEffectMatch{confirmed: *receipt.SelectionPersisted, known: true}
	case "network_request_observed", "response_assertion_passed", "download_observed":
		if receipt.EvidenceRefs == nil {
			return browserEffectMatch{}
		}
		_, found := receipt.EvidenceRefs[effect.EvidenceRef]
		return browserEffectMatch{confirmed: found, known: true}
	case "scene_changed":
		if before == nil || after == nil {
			return browserEffectMatch{}
		}
		return browserEffectMatch{confirmed: browserScenesDiffer(before, after), known: true}
	default:
		return browserEffectMatch{}
	}
}

func browserScenePathContains(rawURL, expected string) bool {
	expected = strings.TrimSpace(expected)
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	if expected == "" {
		return true
	}
	return strings.Contains(parsed.EscapedPath(), expected) || strings.Contains(parsed.Path, expected)
}

func browserSceneSurfaceMatches(surface *bughub.BrowserSceneSurface, effect bughub.BrowserDecisionEffect) bool {
	if surface == nil {
		return false
	}
	if effect.Role != "" && surface.Type != effect.Role {
		return false
	}
	return normalizedBrowserEffectContains(surface.Name, effect.NameContains)
}

func browserSceneElementMatches(scene *bughub.BrowserScene, effect bughub.BrowserDecisionEffect) bool {
	for _, element := range scene.Elements {
		if !element.States.Visible || !element.States.InViewport {
			continue
		}
		if effect.Role != "" && element.Role != effect.Role {
			continue
		}
		if normalizedBrowserEffectContains(element.Name, effect.NameContains) {
			return true
		}
	}
	return false
}

func browserSceneContainsText(scene *bughub.BrowserScene, expected string) bool {
	for _, value := range []string{scene.Title, browserSceneSurfaceName(scene.ActiveSurface)} {
		if normalizedBrowserEffectContains(value, expected) {
			return true
		}
	}
	for _, element := range scene.Elements {
		if element.States.Visible && normalizedBrowserEffectContains(element.Name, expected) {
			return true
		}
	}
	for _, block := range scene.TextBlocks {
		if normalizedBrowserEffectContains(block.Text, expected) {
			return true
		}
	}
	return false
}

func browserSceneSurfaceName(surface *bughub.BrowserSceneSurface) string {
	if surface == nil {
		return ""
	}
	return surface.Name
}

func normalizedBrowserEffectContains(value, expected string) bool {
	expected = strings.ToLower(strings.Join(strings.Fields(expected), " "))
	if expected == "" {
		return true
	}
	value = strings.ToLower(strings.Join(strings.Fields(value), " "))
	return strings.Contains(value, expected)
}

func browserScenesDiffer(before, after *bughub.BrowserScene) bool {
	if before.SceneSHA256 != "" && after.SceneSHA256 != "" {
		return before.SceneSHA256 != after.SceneSHA256
	}
	beforeCopy := *before
	afterCopy := *after
	beforeCopy.SceneID, beforeCopy.SceneSHA256, beforeCopy.CapturedAt = "", "", ""
	afterCopy.SceneID, afterCopy.SceneSHA256, afterCopy.CapturedAt = "", "", ""
	return !reflect.DeepEqual(beforeCopy, afterCopy)
}
