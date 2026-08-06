package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

const autonomousRecipeTargetRef = "recipe_target"

// autonomousRecipeDecisionProvider attempts deterministic Recipe v3 grounding
// before delegating drift, recovery and final conclusions to the normal
// BrowserDecision provider. It never executes actions and cannot bypass the
// loop's Scene, Plan, exploration or transaction checks.
type autonomousRecipeDecisionProvider struct {
	recipe   AutonomousValidationRecipe
	plan     BrowserPlan
	fallback BrowserDecisionProvider
}

func newAutonomousRecipeDecisionProvider(recipe AutonomousValidationRecipe, plan BrowserPlan, fallback BrowserDecisionProvider) (BrowserDecisionProvider, error) {
	if fallback == nil {
		return nil, errors.New("autonomous recipe fallback provider is unavailable")
	}
	if err := validateAutonomousValidationRecipe(recipe, plan); err != nil {
		return nil, err
	}
	return autonomousRecipeDecisionProvider{recipe: recipe, plan: plan, fallback: fallback}, nil
}

func (provider autonomousRecipeDecisionProvider) DecideBrowserStep(ctx context.Context, observation BrowserDecisionLoopObservation) ([]byte, error) {
	decision, ok := provider.replayDecision(observation)
	if !ok {
		return provider.fallback.DecideBrowserStep(ctx, observation)
	}
	encoded, err := json.Marshal(decision)
	if err != nil {
		return nil, errors.New("encode autonomous recipe decision")
	}
	return encoded, nil
}

func (provider autonomousRecipeDecisionProvider) replayDecision(observation BrowserDecisionLoopObservation) (BrowserDecision, bool) {
	if observation.Recovery != nil || validateBoundBrowserDecisionScene(observation.Scene, observation.AttemptID, observation.Scene.SceneID) != nil {
		return BrowserDecision{}, false
	}
	next, ok := autonomousRecipeNextStep(provider.recipe, observation.History)
	if !ok || !autonomousRecipePreconditionMatches(next.Precondition, observation.Scene) {
		return BrowserDecision{}, false
	}
	actionID := next.FrozenAction.ID
	actionType := next.FrozenAction.Action
	elementRef := ""
	if len(next.Anchors) != 0 {
		var grounded bool
		elementRef, grounded = autonomousRecipeGroundElement(next.Anchors, actionType, next.Precondition.RelationText, observation.Scene)
		if !grounded {
			return BrowserDecision{}, false
		}
	}
	if next.Source == "safe_exploration" {
		candidate, found := autonomousRecipeExplorationCandidate(observation.Candidates, actionType, elementRef)
		if !found {
			return BrowserDecision{}, false
		}
		actionID = candidate.Action.ID
	} else {
		planned, found := browserPlanActionByID(provider.plan, actionID)
		if !found || planned.Action != actionType {
			return BrowserDecision{}, false
		}
	}
	effects := BrowserDecisionExpectedEffects{AnyOf: append([]BrowserDecisionEffect(nil), next.ExpectedEffect.AnyOf...)}
	for index := range effects.AnyOf {
		if effects.AnyOf[index].ElementRef == autonomousRecipeTargetRef {
			if elementRef == "" {
				return BrowserDecision{}, false
			}
			effects.AnyOf[index].ElementRef = elementRef
		}
	}
	rationale := "scenario_next_step"
	if next.Source == "safe_exploration" {
		rationale = "locator_recovery"
	}
	decision := BrowserDecision{
		Version: BrowserDecisionVersion, Decision: "act", SceneID: observation.Scene.SceneID,
		RationaleCode:  rationale,
		Action:         &BrowserDecisionAction{ActionID: actionID, Type: actionType, ElementRef: elementRef},
		ExpectedEffect: &effects,
	}
	if _, err := ParseBrowserDecision(mustJSON(decision), observation.Scene.SceneID); err != nil {
		return BrowserDecision{}, false
	}
	return decision, true
}

func autonomousRecipeNextStep(recipe AutonomousValidationRecipe, history []BrowserDecisionLoopStep) (AutonomousRecipeStep, bool) {
	if len(history) >= len(recipe.Steps) {
		return AutonomousRecipeStep{}, false
	}
	for index, executed := range history {
		expected := recipe.Steps[index]
		if executed.Outcome != BrowserEffectConfirmed || executed.ActionType != expected.FrozenAction.Action ||
			executed.Exploration != (expected.Source == "safe_exploration") ||
			(!executed.Exploration && executed.ActionID != expected.FrozenAction.ID) {
			return AutonomousRecipeStep{}, false
		}
	}
	return recipe.Steps[len(history)], true
}

func autonomousRecipePreconditionMatches(precondition AutonomousRecipePrecondition, scene BrowserScene) bool {
	parsed, err := url.Parse(scene.URL)
	if err != nil || !strings.Contains(parsed.EscapedPath(), precondition.PathContains) {
		return false
	}
	if precondition.ActiveSurfaceType == "" && precondition.ActiveSurfaceName == "" {
		return scene.ActiveSurface == nil
	}
	return scene.ActiveSurface != nil && scene.ActiveSurface.Type == precondition.ActiveSurfaceType &&
		scene.ActiveSurface.Name == precondition.ActiveSurfaceName
}

func autonomousRecipeGroundElement(anchors []AutonomousRecipeAnchor, actionType, relationText string, scene BrowserScene) (string, bool) {
	bestScore := 0
	bestRef := ""
	bestCount := 0
	for _, element := range scene.Elements {
		if validateBrowserDecisionTarget(scene, BrowserAction{Action: actionType}, element) != nil {
			continue
		}
		if relationText != "" && element.Relations.RowName != relationText && element.Relations.GroupName != relationText {
			continue
		}
		score := 0
		for _, anchor := range anchors {
			if autonomousRecipeAnchorMatches(anchor, element) {
				score++
			}
		}
		if score == 0 {
			continue
		}
		if score > bestScore {
			bestScore, bestRef, bestCount = score, element.Ref, 1
		} else if score == bestScore {
			bestCount++
		}
	}
	return bestRef, bestScore > 0 && bestCount == 1
}

func autonomousRecipeAnchorMatches(anchor AutonomousRecipeAnchor, element BrowserSceneElement) bool {
	switch anchor.Kind {
	case "test_id":
		return element.LocatorHints.TestID == anchor.Value
	case "label":
		return element.LocatorHints.Label == anchor.Value
	case "placeholder":
		return element.LocatorHints.Placeholder == anchor.Value
	case "role":
		return element.Role == anchor.Role && element.Name == anchor.Name
	case "role_within":
		if element.Role != anchor.Role || element.Name != anchor.Name {
			return false
		}
		if anchor.WithinRole == "row" {
			return element.Relations.RowName == anchor.WithinName
		}
		if anchor.WithinRole == "group" {
			return element.Relations.GroupName == anchor.WithinName
		}
		return false
	case "same_origin_href":
		return element.LocatorHints.SameOriginHref == anchor.Value
	default:
		return false
	}
}

func autonomousRecipeExplorationCandidate(candidates []BrowserExplorationCandidate, actionType, elementRef string) (BrowserExplorationCandidate, bool) {
	var found BrowserExplorationCandidate
	count := 0
	for _, candidate := range candidates {
		if candidate.Action.Action == actionType && candidate.ElementRef == elementRef {
			found = candidate
			count++
		}
	}
	return found, count == 1
}
