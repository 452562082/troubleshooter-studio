package bughub

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"strings"
)

const (
	AutonomousValidationRecipeVersion = 3
	maxAutonomousValidationRecipeSize = 256 << 10
)

// AutonomousValidationRecipe augments the still-authoritative BrowserPlan v2
// with deterministic per-state grounding and effect contracts. FrozenAction
// retains business values from the validated plan but never retains a
// short-lived element ref or a single rebound locator.
type AutonomousValidationRecipe struct {
	Version                int                        `json:"version"`
	ScenarioContractSHA256 string                     `json:"scenario_contract_sha256"`
	PlanSHA256             string                     `json:"plan_sha256"`
	DeviceProfile          string                     `json:"device_profile"`
	FrontendEntryIDs       []string                   `json:"frontend_entry_ids"`
	Steps                  []AutonomousRecipeStep     `json:"steps"`
	Assertions             []BrowserAssertion         `json:"assertions"`
	RequestCaptures        []BrowserRequestCapture    `json:"request_captures,omitempty"`
	ResponseAssertions     []BrowserResponseAssertion `json:"response_assertions,omitempty"`
}

type AutonomousRecipeStep struct {
	Source          string                         `json:"source"`
	FrozenAction    BrowserAction                  `json:"frozen_action"`
	FrontendEntryID string                         `json:"frontend_entry_id"`
	Precondition    AutonomousRecipePrecondition   `json:"precondition"`
	Anchors         []AutonomousRecipeAnchor       `json:"anchors,omitempty"`
	ExpectedEffect  BrowserDecisionExpectedEffects `json:"expected_effect"`
}

type AutonomousRecipePrecondition struct {
	PathContains      string `json:"path_contains"`
	ActiveSurfaceType string `json:"active_surface_type,omitempty"`
	ActiveSurfaceName string `json:"active_surface_name,omitempty"`
	RelationText      string `json:"relation_text,omitempty"`
}

type AutonomousRecipeAnchor struct {
	Kind       string `json:"kind"`
	Value      string `json:"value,omitempty"`
	Role       string `json:"role,omitempty"`
	Name       string `json:"name,omitempty"`
	WithinRole string `json:"within_role,omitempty"`
	WithinName string `json:"within_name,omitempty"`
}

type AutonomousRecipeTraceStep struct {
	Before          BrowserScene
	Bound           BoundBrowserDecisionStep
	Outcome         string
	Exploration     bool
	FrontendEntryID string
}

func CompileAutonomousValidationRecipe(plan BrowserPlan, scenarioContractSHA256 string, trace []AutonomousRecipeTraceStep) (AutonomousValidationRecipe, error) {
	if err := validateDurableBrowserPlan(plan); err != nil {
		return AutonomousValidationRecipe{}, fmt.Errorf("compile autonomous recipe plan: %w", err)
	}
	scenarioContractSHA256 = strings.TrimSpace(scenarioContractSHA256)
	if plan.ScenarioContract == nil || !validLowerSHA256(scenarioContractSHA256) || plan.ScenarioContract.ContextSHA256 != scenarioContractSHA256 {
		return AutonomousValidationRecipe{}, errors.New("autonomous recipe scenario contract binding is invalid")
	}
	if len(trace) == 0 || len(trace) > BrowserDecisionLoopMaxDecisions {
		return AutonomousValidationRecipe{}, errors.New("autonomous recipe trace count is invalid")
	}
	planSHA256, err := durableBrowserPlanSHA256(plan)
	if err != nil {
		return AutonomousValidationRecipe{}, err
	}
	profile := strings.TrimSpace(plan.DeviceProfile)
	if profile == "" {
		profile = "desktop"
	}
	recipe := AutonomousValidationRecipe{
		Version: AutonomousValidationRecipeVersion, ScenarioContractSHA256: scenarioContractSHA256,
		PlanSHA256: planSHA256, DeviceProfile: profile,
		FrontendEntryIDs:   append([]string(nil), plan.ScenarioContract.FrontendEntryIDs...),
		Steps:              make([]AutonomousRecipeStep, 0, len(trace)),
		Assertions:         append([]BrowserAssertion(nil), plan.Assertions...),
		RequestCaptures:    append([]BrowserRequestCapture(nil), plan.RequestCaptures...),
		ResponseAssertions: append([]BrowserResponseAssertion(nil), plan.ResponseAssertions...),
	}
	for index, executed := range trace {
		step, compileErr := compileAutonomousRecipeStep(plan, executed)
		if compileErr != nil {
			return AutonomousValidationRecipe{}, fmt.Errorf("compile autonomous recipe step %d: %w", index, compileErr)
		}
		recipe.Steps = append(recipe.Steps, step)
	}
	if err := validateAutonomousValidationRecipe(recipe, plan); err != nil {
		return AutonomousValidationRecipe{}, err
	}
	return recipe, nil
}

func ParseAutonomousValidationRecipe(content []byte, plan BrowserPlan) (AutonomousValidationRecipe, error) {
	if len(content) == 0 || len(content) > maxAutonomousValidationRecipeSize || containsSensitiveData(content) {
		return AutonomousValidationRecipe{}, errors.New("autonomous validation recipe is unsafe")
	}
	var recipe AutonomousValidationRecipe
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&recipe); err != nil {
		return AutonomousValidationRecipe{}, fmt.Errorf("decode autonomous validation recipe: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return AutonomousValidationRecipe{}, errors.New("autonomous validation recipe contains trailing content")
	}
	if err := validateAutonomousValidationRecipe(recipe, plan); err != nil {
		return AutonomousValidationRecipe{}, err
	}
	return recipe, nil
}

func autonomousValidationRecipeSHA256(recipe AutonomousValidationRecipe, plan BrowserPlan) (string, error) {
	if err := validateAutonomousValidationRecipe(recipe, plan); err != nil {
		return "", err
	}
	encoded, err := json.Marshal(recipe)
	if err != nil || len(encoded) > maxAutonomousValidationRecipeSize || containsSensitiveData(encoded) {
		return "", errors.New("autonomous validation recipe is unsafe")
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func compileAutonomousRecipeStep(plan BrowserPlan, trace AutonomousRecipeTraceStep) (AutonomousRecipeStep, error) {
	if trace.Outcome != BrowserEffectConfirmed || trace.Bound.BeforeSceneSHA256 != trace.Before.SceneSHA256 ||
		!validLowerSHA256(trace.Bound.DecisionSHA256) || !validLowerSHA256(trace.Bound.ActionFingerprint) {
		return AutonomousRecipeStep{}, errors.New("trace step was not a confirmed bound action")
	}
	if err := validateBoundBrowserDecisionScene(trace.Before, trace.Before.AttemptID, trace.Before.SceneID); err != nil {
		return AutonomousRecipeStep{}, err
	}
	entryID := strings.TrimSpace(trace.FrontendEntryID)
	if !validBrowserDecisionIdentifier(entryID, 128) || !stringInSlice(entryID, plan.ScenarioContract.FrontendEntryIDs) {
		return AutonomousRecipeStep{}, errors.New("trace frontend entry is outside the scenario contract")
	}
	action := trace.Bound.Action
	if trace.Exploration {
		action.Locator = nil
		if err := validateAutonomousExplorationRecipeAction(action); err != nil {
			return AutonomousRecipeStep{}, err
		}
	} else {
		planned, found := browserPlanActionByID(plan, action.ID)
		if !found || !equalBrowserRecipeActionWithoutLocator(planned, action) {
			return AutonomousRecipeStep{}, errors.New("trace action differs from the frozen scenario action")
		}
		action = planned
	}
	action.Locator = nil
	target, anchors, relationText, err := autonomousRecipeAnchors(trace.Before, trace.Bound.ElementRef)
	if err != nil {
		return AutonomousRecipeStep{}, err
	}
	if trace.Bound.ElementRef != "" && (target == nil || len(anchors) == 0) {
		return AutonomousRecipeStep{}, errors.New("trace target has no stable recipe anchor")
	}
	parsedURL, err := url.Parse(trace.Before.URL)
	if err != nil || parsedURL.Path == "" {
		return AutonomousRecipeStep{}, errors.New("trace Scene path is invalid")
	}
	precondition := AutonomousRecipePrecondition{PathContains: parsedURL.EscapedPath(), RelationText: relationText}
	if trace.Before.ActiveSurface != nil {
		precondition.ActiveSurfaceType = trace.Before.ActiveSurface.Type
		precondition.ActiveSurfaceName = trace.Before.ActiveSurface.Name
	}
	if len(trace.Bound.ExpectedEffect.AnyOf) == 0 {
		return AutonomousRecipeStep{}, errors.New("trace expected effect is missing")
	}
	expectedEffect := BrowserDecisionExpectedEffects{AnyOf: append([]BrowserDecisionEffect(nil), trace.Bound.ExpectedEffect.AnyOf...)}
	for index := range expectedEffect.AnyOf {
		effect := &expectedEffect.AnyOf[index]
		if effect.ElementRef != "" {
			if trace.Bound.ElementRef == "" || effect.ElementRef != trace.Bound.ElementRef {
				return AutonomousRecipeStep{}, errors.New("trace expected effect references another ephemeral element")
			}
			effect.ElementRef = autonomousRecipeTargetRef
		}
		if err := validateBrowserDecisionEffect(*effect); err != nil {
			return AutonomousRecipeStep{}, err
		}
	}
	source := "scenario"
	if trace.Exploration {
		source = "safe_exploration"
	}
	return AutonomousRecipeStep{
		Source: source, FrozenAction: action, FrontendEntryID: entryID, Precondition: precondition,
		Anchors: anchors, ExpectedEffect: expectedEffect,
	}, nil
}

func autonomousRecipeAnchors(scene BrowserScene, elementRef string) (*BrowserSceneElement, []AutonomousRecipeAnchor, string, error) {
	if strings.TrimSpace(elementRef) == "" {
		return nil, nil, "", nil
	}
	target, err := browserDecisionSceneElement(scene, elementRef)
	if err != nil {
		return nil, nil, "", err
	}
	anchors := make([]AutonomousRecipeAnchor, 0, 6)
	appendAnchor := func(anchor AutonomousRecipeAnchor) {
		for _, existing := range anchors {
			if reflect.DeepEqual(existing, anchor) {
				return
			}
		}
		anchors = append(anchors, anchor)
	}
	if value := strings.TrimSpace(target.LocatorHints.TestID); value != "" {
		appendAnchor(AutonomousRecipeAnchor{Kind: "test_id", Value: value})
	}
	if value := strings.TrimSpace(target.LocatorHints.Label); value != "" {
		appendAnchor(AutonomousRecipeAnchor{Kind: "label", Value: value})
	}
	if value := strings.TrimSpace(target.LocatorHints.Placeholder); value != "" {
		appendAnchor(AutonomousRecipeAnchor{Kind: "placeholder", Value: value})
	}
	if role, name := strings.TrimSpace(target.Role), strings.TrimSpace(target.Name); role != "" && name != "" {
		appendAnchor(AutonomousRecipeAnchor{Kind: "role", Role: role, Name: name})
		if withinName := strings.TrimSpace(target.Relations.RowName); withinName != "" {
			appendAnchor(AutonomousRecipeAnchor{Kind: "role_within", Role: role, Name: name, WithinRole: "row", WithinName: withinName})
		}
		if withinName := strings.TrimSpace(target.Relations.GroupName); withinName != "" {
			appendAnchor(AutonomousRecipeAnchor{Kind: "role_within", Role: role, Name: name, WithinRole: "group", WithinName: withinName})
		}
	}
	if value := strings.TrimSpace(target.LocatorHints.SameOriginHref); value != "" {
		appendAnchor(AutonomousRecipeAnchor{Kind: "same_origin_href", Value: value})
	}
	relationText := strings.TrimSpace(target.Relations.RowName)
	if relationText == "" {
		relationText = strings.TrimSpace(target.Relations.GroupName)
	}
	return target, anchors, relationText, nil
}

func validateAutonomousValidationRecipe(recipe AutonomousValidationRecipe, plan BrowserPlan) error {
	if err := validateDurableBrowserPlan(plan); err != nil {
		return err
	}
	planSHA256, err := durableBrowserPlanSHA256(plan)
	if err != nil {
		return err
	}
	profile := strings.TrimSpace(plan.DeviceProfile)
	if profile == "" {
		profile = "desktop"
	}
	if recipe.Version != AutonomousValidationRecipeVersion || plan.ScenarioContract == nil ||
		recipe.ScenarioContractSHA256 != plan.ScenarioContract.ContextSHA256 || !validLowerSHA256(recipe.ScenarioContractSHA256) ||
		recipe.PlanSHA256 != planSHA256 || recipe.DeviceProfile != profile ||
		!reflect.DeepEqual(recipe.FrontendEntryIDs, plan.ScenarioContract.FrontendEntryIDs) ||
		!reflect.DeepEqual(recipe.Assertions, plan.Assertions) ||
		!reflect.DeepEqual(recipe.RequestCaptures, plan.RequestCaptures) ||
		!reflect.DeepEqual(recipe.ResponseAssertions, plan.ResponseAssertions) ||
		len(recipe.Steps) == 0 || len(recipe.Steps) > BrowserDecisionLoopMaxDecisions {
		return errors.New("autonomous validation recipe binding is invalid")
	}
	for index, step := range recipe.Steps {
		if err := validateAutonomousRecipeStep(step, plan); err != nil {
			return fmt.Errorf("autonomous validation recipe step %d: %w", index, err)
		}
	}
	nextCausalAction := 0
	for _, step := range recipe.Steps {
		if step.Source == "scenario" && nextCausalAction < len(plan.ScenarioContract.CausalActionIDs) &&
			step.FrozenAction.ID == plan.ScenarioContract.CausalActionIDs[nextCausalAction] {
			nextCausalAction++
		}
	}
	if nextCausalAction != len(plan.ScenarioContract.CausalActionIDs) {
		return errors.New("autonomous validation recipe does not preserve causal scenario action order")
	}
	encoded, err := json.Marshal(recipe)
	if err != nil || len(encoded) > maxAutonomousValidationRecipeSize || containsSensitiveData(encoded) {
		return errors.New("autonomous validation recipe is unsafe")
	}
	return nil
}

func validateAutonomousRecipeStep(step AutonomousRecipeStep, plan BrowserPlan) error {
	if step.Source != "scenario" && step.Source != "safe_exploration" {
		return errors.New("source is invalid")
	}
	if !validBrowserDecisionIdentifier(step.FrontendEntryID, 128) || !stringInSlice(step.FrontendEntryID, plan.ScenarioContract.FrontendEntryIDs) ||
		strings.TrimSpace(step.Precondition.PathContains) == "" || !strings.HasPrefix(step.Precondition.PathContains, "/") ||
		len(step.Precondition.PathContains) > 1024 || len(step.Precondition.ActiveSurfaceType) > 64 ||
		len(step.Precondition.ActiveSurfaceName) > 512 || len(step.Precondition.RelationText) > 512 ||
		browserStrongCredentialSemantic(step.Precondition.ActiveSurfaceName) || browserStrongCredentialSemantic(step.Precondition.RelationText) {
		return errors.New("precondition is invalid")
	}
	if step.FrozenAction.Locator != nil || !validBrowserDecisionIdentifier(step.FrozenAction.ID, 128) {
		return errors.New("frozen action identity is invalid")
	}
	if step.Source == "scenario" {
		planned, found := browserPlanActionByID(plan, step.FrozenAction.ID)
		if !found || !equalBrowserRecipeActionWithoutLocator(planned, step.FrozenAction) {
			return errors.New("frozen scenario action differs from plan")
		}
	} else if err := validateAutonomousExplorationRecipeAction(step.FrozenAction); err != nil {
		return err
	}
	if len(step.Anchors) > 6 || ((step.FrozenAction.Action == "click" || step.FrozenAction.Action == "fill" || step.FrozenAction.Action == "select" || step.FrozenAction.Action == "upload_file") && len(step.Anchors) == 0) {
		return errors.New("anchor count is invalid")
	}
	for _, anchor := range step.Anchors {
		if err := validateAutonomousRecipeAnchor(anchor); err != nil {
			return err
		}
	}
	if len(step.ExpectedEffect.AnyOf) == 0 || len(step.ExpectedEffect.AnyOf) > 4 {
		return errors.New("expected effect count is invalid")
	}
	for _, effect := range step.ExpectedEffect.AnyOf {
		if err := validateBrowserDecisionEffect(effect); err != nil {
			return err
		}
	}
	return nil
}

func validateAutonomousRecipeAnchor(anchor AutonomousRecipeAnchor) error {
	bounded := func(value string, limit int) bool {
		return strings.TrimSpace(value) != "" && len(value) <= limit && !browserStrongCredentialSemantic(value)
	}
	switch anchor.Kind {
	case "test_id", "label", "placeholder", "same_origin_href":
		if !bounded(anchor.Value, 1024) || anchor.Role != "" || anchor.Name != "" || anchor.WithinRole != "" || anchor.WithinName != "" {
			return errors.New("value anchor is invalid")
		}
	case "role":
		if !validBrowserDecisionElementRole(anchor.Role) || !bounded(anchor.Name, 512) || anchor.Value != "" || anchor.WithinRole != "" || anchor.WithinName != "" {
			return errors.New("role anchor is invalid")
		}
	case "role_within":
		if !validBrowserDecisionElementRole(anchor.Role) || !bounded(anchor.Name, 512) ||
			(anchor.WithinRole != "row" && anchor.WithinRole != "group") || !bounded(anchor.WithinName, 512) || anchor.Value != "" {
			return errors.New("scoped role anchor is invalid")
		}
	default:
		return errors.New("anchor kind is invalid")
	}
	return nil
}

func equalBrowserRecipeActionWithoutLocator(left, right BrowserAction) bool {
	left.Locator = nil
	right.Locator = nil
	return reflect.DeepEqual(left, right)
}

func validateAutonomousExplorationRecipeAction(action BrowserAction) error {
	if !validBrowserDecisionIdentifier(action.ID, 128) || action.ScreenshotAfter || action.FileRef != "" || action.Value != "" || action.Locator != nil {
		return errors.New("safe exploration frozen action is invalid")
	}
	switch action.Action {
	case "goto":
		parsed, err := url.Parse(action.URL)
		if err != nil || !parsed.IsAbs() || parsed.User != nil || action.Key != "" {
			return errors.New("safe exploration goto is invalid")
		}
	case "press":
		if !strings.EqualFold(strings.TrimSpace(action.Key), "escape") || action.URL != "" {
			return errors.New("safe exploration press is invalid")
		}
	case "dismiss_surface":
		if action.Key != "" || action.URL != "" {
			return errors.New("safe exploration dismiss surface is invalid")
		}
	case "click", "wait_for":
		if action.URL != "" || action.Key != "" {
			return errors.New("safe exploration element action is invalid")
		}
	default:
		return errors.New("safe exploration action type is invalid")
	}
	return nil
}

func stringInSlice(value string, values []string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
