package bughub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// BoundBrowserDecisionStep is a Host-owned executable step. The Agent chooses
// only a current-scene element ref; every value, key, URL, and file reference
// is copied from the already validated BrowserPlan.
type BoundBrowserDecisionStep struct {
	DecisionSHA256    string
	ActionFingerprint string
	BeforeSceneSHA256 string
	// ElementRef is the short-lived Host scene reference selected by the
	// Decision. It is carried separately from the rebound locator so a
	// persistent Worker can reject a stale target before executing the step.
	ElementRef     string
	Action         BrowserAction
	ExpectedEffect BrowserDecisionExpectedEffects
	PassiveChecks  []BrowserDecisionPassiveCheck
}

func BindBrowserDecisionStep(plan BrowserPlan, decision BrowserDecision, scene BrowserScene, attemptID string) (BoundBrowserDecisionStep, error) {
	if err := validateDurableBrowserPlan(plan); err != nil {
		return BoundBrowserDecisionStep{}, fmt.Errorf("bind browser decision plan: %w", err)
	}
	canonicalDecision, parsedDecision, err := canonicalBrowserDecision(decision)
	if err != nil {
		return BoundBrowserDecisionStep{}, err
	}
	decision = parsedDecision
	if decision.Decision != "act" || decision.Action == nil || decision.ExpectedEffect == nil {
		return BoundBrowserDecisionStep{}, errors.New("only browser act decisions can be bound to a step")
	}
	if err := validateBoundBrowserDecisionScene(scene, attemptID, decision.SceneID); err != nil {
		return BoundBrowserDecisionStep{}, err
	}

	planned, found := browserPlanActionByID(plan, decision.Action.ActionID)
	if !found || planned.Action != decision.Action.Type {
		return BoundBrowserDecisionStep{}, errors.New("browser decision action does not match the frozen plan")
	}
	boundAction := planned
	var target *BrowserSceneElement
	if decision.Action.ElementRef != "" {
		target, err = browserDecisionSceneElement(scene, decision.Action.ElementRef)
		if err != nil {
			return BoundBrowserDecisionStep{}, err
		}
		if err := validateBrowserDecisionTarget(scene, planned, *target); err != nil {
			return BoundBrowserDecisionStep{}, err
		}
		boundAction.Locator, err = browserDecisionElementLocator(*target)
		if err != nil {
			return BoundBrowserDecisionStep{}, err
		}
	} else if planned.Locator != nil {
		return BoundBrowserDecisionStep{}, errors.New("browser decision omitted the frozen action target")
	} else if planned.Action == "press" && (!strings.EqualFold(strings.TrimSpace(planned.Key), "escape") || scene.ActiveSurface == nil) {
		return BoundBrowserDecisionStep{}, errors.New("locator-free browser decision requires Escape on an active surface")
	} else if planned.Action == "dismiss_surface" && scene.ActiveSurface == nil {
		return BoundBrowserDecisionStep{}, errors.New("dismiss_surface requires an active surface")
	}
	if err := validateBoundBrowserDecisionEffects(plan, decision, target); err != nil {
		return BoundBrowserDecisionStep{}, err
	}
	executablePlan := plan
	executablePlan.Actions = append([]BrowserAction(nil), plan.Actions...)
	for index := range executablePlan.Actions {
		if executablePlan.Actions[index].ID == boundAction.ID {
			executablePlan.Actions[index] = boundAction
		}
	}
	if err := validateDurableBrowserPlan(executablePlan); err != nil {
		return BoundBrowserDecisionStep{}, fmt.Errorf("bound browser decision action: %w", err)
	}

	actionIdentity := struct {
		SceneSHA256 string        `json:"scene_sha256"`
		Action      BrowserAction `json:"action"`
	}{SceneSHA256: scene.SceneSHA256, Action: boundAction}
	actionJSON, err := json.Marshal(actionIdentity)
	if err != nil {
		return BoundBrowserDecisionStep{}, errors.New("bound browser action identity is invalid")
	}
	actionDigest := sha256.Sum256(actionJSON)
	decisionDigest := sha256.Sum256(canonicalDecision)
	return BoundBrowserDecisionStep{
		DecisionSHA256:    hex.EncodeToString(decisionDigest[:]),
		ActionFingerprint: hex.EncodeToString(actionDigest[:]),
		BeforeSceneSHA256: scene.SceneSHA256,
		ElementRef:        decision.Action.ElementRef,
		Action:            boundAction,
		ExpectedEffect:    *decision.ExpectedEffect,
		PassiveChecks:     append([]BrowserDecisionPassiveCheck(nil), decision.PassiveChecks...),
	}, nil
}

func canonicalBrowserDecision(decision BrowserDecision) ([]byte, BrowserDecision, error) {
	encoded, err := json.Marshal(decision)
	if err != nil || len(encoded) > 32<<10 || containsSensitiveData(encoded) {
		return nil, BrowserDecision{}, errors.New("browser decision is unsafe")
	}
	parsed, err := ParseBrowserDecision(encoded, strings.TrimSpace(decision.SceneID))
	if err != nil {
		return nil, BrowserDecision{}, err
	}
	if !reflect.DeepEqual(decision, parsed) {
		return nil, BrowserDecision{}, errors.New("browser decision is not canonical")
	}
	canonical, err := json.Marshal(parsed)
	if err != nil {
		return nil, BrowserDecision{}, err
	}
	return canonical, parsed, nil
}

func validateBoundBrowserDecisionScene(scene BrowserScene, attemptID, decisionSceneID string) error {
	if strings.TrimSpace(attemptID) == "" || scene.Version != BrowserSceneVersion || scene.AttemptID != attemptID || !validLowerSHA256(scene.SceneSHA256) || scene.SceneID != "scene-"+scene.SceneSHA256[:16] || scene.SceneID != decisionSceneID {
		return errors.New("browser decision scene binding is invalid")
	}
	identity := scene
	identity.SceneID = ""
	identity.SceneSHA256 = ""
	encoded, err := json.Marshal(identity)
	if err != nil {
		return errors.New("browser decision scene identity is invalid")
	}
	digest := sha256.Sum256(encoded)
	if hex.EncodeToString(digest[:]) != scene.SceneSHA256 {
		return errors.New("browser decision scene digest does not match")
	}
	if scene.Capabilities.DOM != "available" || len(scene.Frames) == 0 || scene.Frames[0].Ref != "f-main" || !scene.Frames[0].SameOrigin {
		return errors.New("browser decision scene is not executable")
	}
	return nil
}

func browserPlanActionByID(plan BrowserPlan, actionID string) (BrowserAction, bool) {
	var found BrowserAction
	count := 0
	for _, action := range plan.Actions {
		if action.ID == actionID {
			found = action
			count++
		}
	}
	return found, count == 1
}

func browserDecisionSceneElement(scene BrowserScene, ref string) (*BrowserSceneElement, error) {
	var found *BrowserSceneElement
	for index := range scene.Elements {
		if scene.Elements[index].Ref != ref {
			continue
		}
		if found != nil {
			return nil, errors.New("browser decision element ref is duplicated")
		}
		found = &scene.Elements[index]
	}
	if found == nil {
		return nil, errors.New("browser decision element ref is stale")
	}
	return found, nil
}

func validateBrowserDecisionTarget(scene BrowserScene, action BrowserAction, element BrowserSceneElement) error {
	if element.FrameRef != "f-main" || !element.States.Visible || !element.States.InViewport {
		return errors.New("browser decision target is outside the executable main-frame viewport")
	}
	if scene.ActiveSurface != nil && element.SurfaceRef != scene.ActiveSurface.Ref {
		return errors.New("browser decision target is outside the active surface")
	}
	if scene.ActiveSurface == nil && element.SurfaceRef != "" {
		return errors.New("browser decision target references an inactive surface")
	}
	switch action.Action {
	case "wait_for":
		return nil
	case "fill":
		if !element.States.Enabled || !element.States.Editable || element.States.Obscured {
			return errors.New("browser fill target is not safely editable")
		}
	case "select":
		if !element.States.Enabled || element.States.Obscured || (element.Role != "combobox" && element.Tag != "select") {
			return errors.New("browser select target is not a safe selection control")
		}
	case "upload_file":
		if !element.States.Enabled || element.States.Obscured || (element.Tag != "input" && element.Role != "button") {
			return errors.New("browser upload target is not a safe file input or chooser control")
		}
	case "click", "press":
		if !element.States.Enabled || element.States.Obscured {
			return errors.New("browser decision target is not safely actionable")
		}
	default:
		return errors.New("browser decision action does not support an element target")
	}
	return nil
}

func browserDecisionElementLocator(element BrowserSceneElement) (*BrowserLocator, error) {
	exact := true
	within := func() *BrowserLocator {
		if strings.TrimSpace(element.Relations.RowName) != "" {
			return &BrowserLocator{Kind: "role", Value: "row", Name: element.Relations.RowName, Exact: &exact}
		}
		return nil
	}
	if value := strings.TrimSpace(element.LocatorHints.TestID); value != "" {
		return &BrowserLocator{Kind: "test_id", Value: value, Within: within()}, nil
	}
	if value := strings.TrimSpace(element.LocatorHints.Label); value != "" {
		return &BrowserLocator{Kind: "label", Value: value, Exact: &exact, Within: within()}, nil
	}
	if value := strings.TrimSpace(element.LocatorHints.Placeholder); value != "" {
		return &BrowserLocator{Kind: "placeholder", Value: value, Exact: &exact, Within: within()}, nil
	}
	if role, name := strings.TrimSpace(element.Role), strings.TrimSpace(element.Name); role != "" && name != "" {
		return &BrowserLocator{Kind: "role", Value: role, Name: name, Exact: &exact, Within: within()}, nil
	}
	return nil, errors.New("browser decision target lacks a stable semantic locator")
}

// browserScenarioDirectTargetAvailable prevents the exploration budget from
// being used while the next frozen scenario action already has a safe,
// semantically grounded target in the current Scene. CSS selectors are not
// reconstructed from Scene evidence and therefore never count as proof here.
func browserScenarioDirectTargetAvailable(plan BrowserPlan, nextAction int, scene BrowserScene) bool {
	if nextAction < 0 || nextAction >= len(plan.Actions) {
		return false
	}
	action := plan.Actions[nextAction]
	if action.Locator == nil {
		return action.Action == "goto" || action.Action == "press" || action.Action == "dismiss_surface"
	}
	for _, element := range scene.Elements {
		if validateBrowserDecisionTarget(scene, action, element) != nil {
			continue
		}
		if browserSceneElementMatchesLocator(element, *action.Locator) {
			return true
		}
	}
	return false
}

func browserSceneElementMatchesLocator(element BrowserSceneElement, locator BrowserLocator) bool {
	match := func(observed, expected string) bool {
		observed = strings.TrimSpace(observed)
		expected = strings.TrimSpace(expected)
		if locator.Exact != nil && *locator.Exact {
			return observed == expected
		}
		return strings.Contains(strings.ToLower(observed), strings.ToLower(expected))
	}
	matched := false
	switch locator.Kind {
	case "test_id":
		matched = match(element.LocatorHints.TestID, locator.Value)
	case "label":
		matched = match(element.LocatorHints.Label, locator.Value)
	case "placeholder":
		matched = match(element.LocatorHints.Placeholder, locator.Value)
	case "role":
		matched = match(element.Role, locator.Value) && match(element.Name, locator.Name)
	case "text":
		matched = match(element.Name, locator.Value)
	default:
		return false
	}
	if !matched || locator.Within == nil {
		return matched
	}
	return locator.Within.Kind == "role" && locator.Within.Value == "row" &&
		browserSceneElementMatchesLocator(BrowserSceneElement{
			Role: "row", Name: element.Relations.RowName,
		}, *locator.Within)
}

func validateBoundBrowserDecisionEffects(plan BrowserPlan, decision BrowserDecision, target *BrowserSceneElement) error {
	requestCaptures := make(map[string]struct{}, len(plan.RequestCaptures))
	for _, capture := range plan.RequestCaptures {
		requestCaptures[capture.ID] = struct{}{}
	}
	responseAssertions := make(map[string]struct{}, len(plan.ResponseAssertions))
	for _, assertion := range plan.ResponseAssertions {
		responseAssertions[assertion.ID] = struct{}{}
	}
	for _, effect := range decision.ExpectedEffect.AnyOf {
		switch effect.Kind {
		case "input_value_persisted":
			if decision.Action.Type != "fill" || target == nil || effect.ElementRef != target.Ref {
				return errors.New("browser input persistence effect is not bound to the fill target")
			}
		case "selection_persisted":
			if decision.Action.Type != "select" || target == nil || effect.ElementRef != target.Ref {
				return errors.New("browser selection persistence effect is not bound to the select target")
			}
		case "network_request_observed":
			if _, found := requestCaptures[effect.EvidenceRef]; !found {
				return errors.New("browser network effect does not reference a frozen request capture")
			}
		case "response_assertion_passed":
			if _, found := responseAssertions[effect.EvidenceRef]; !found {
				return errors.New("browser response effect does not reference a frozen response assertion")
			}
		case "download_observed":
			if effect.EvidenceRef != decision.Action.ActionID {
				return errors.New("browser download effect does not reference the current frozen action")
			}
		}
	}
	return nil
}
