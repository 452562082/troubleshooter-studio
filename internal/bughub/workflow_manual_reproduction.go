package bughub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"
)

const (
	LegacyManualReproductionRecipeVersion = 1
	ManualReproductionRecipeVersion       = 2
	ManualReproductionBundleVersion       = 1
	ManualReproductionArtifactKind        = "manual_reproduction_recipe"
)

// BrowserManualReproductionRecipe is a host-recorded, credential-safe replay
// trace. It is evidence, not model output. The browser planner may add waits,
// screenshots and assertions, but every replayable action must remain present
// in order in the generated plan.
type BrowserManualReproductionRecipe struct {
	Version           int                               `json:"version"`
	FrontendEntryID   string                            `json:"frontend_entry_id,omitempty"`
	FrontendEntryName string                            `json:"frontend_entry_name,omitempty"`
	StartURL          string                            `json:"start_url"`
	FinalURL          string                            `json:"final_url,omitempty"`
	Title             string                            `json:"title,omitempty"`
	Actions           []BrowserManualReproductionAction `json:"actions"`
}

// BrowserManualReproductionBundle groups independently recorded application
// segments. Segments follow the Case's frozen frontend-entry order when that
// order is available, with capture time as a legacy fallback, so locators from
// different DOMs never get merged.
type BrowserManualReproductionBundle struct {
	Version  int                               `json:"version"`
	Segments []BrowserManualReproductionRecipe `json:"segments"`
}

type BrowserManualReproductionAction struct {
	ID            string          `json:"id"`
	Action        string          `json:"action"`
	Locator       *BrowserLocator `json:"locator,omitempty"`
	Role          string          `json:"role,omitempty"`
	Tag           string          `json:"tag,omitempty"`
	Label         string          `json:"label,omitempty"`
	InputType     string          `json:"input_type,omitempty"`
	Value         string          `json:"value,omitempty"`
	ValueRedacted bool            `json:"value_redacted,omitempty"`
	Key           string          `json:"key,omitempty"`
	URL           string          `json:"url,omitempty"`
	StartedAt     string          `json:"started_at,omitempty"`
	DurationMS    int             `json:"duration_ms,omitempty"`
	Result        string          `json:"result,omitempty"`
}

func ParseBrowserManualReproductionRecipe(content []byte) (BrowserManualReproductionRecipe, error) {
	if len(content) == 0 || len(content) > 256<<10 || containsSensitiveData(content) {
		return BrowserManualReproductionRecipe{}, errors.New("manual reproduction recipe is unsafe")
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var recipe BrowserManualReproductionRecipe
	if err := decoder.Decode(&recipe); err != nil {
		return BrowserManualReproductionRecipe{}, fmt.Errorf("decode manual reproduction recipe: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return BrowserManualReproductionRecipe{}, errors.New("manual reproduction recipe contains trailing data")
	}
	if err := recipe.Validate(); err != nil {
		return BrowserManualReproductionRecipe{}, err
	}
	return recipe, nil
}

func (recipe BrowserManualReproductionRecipe) Validate() error {
	if recipe.Version != LegacyManualReproductionRecipeVersion && recipe.Version != ManualReproductionRecipeVersion {
		return fmt.Errorf("manual reproduction recipe version must be %d or %d", LegacyManualReproductionRecipeVersion, ManualReproductionRecipeVersion)
	}
	if recipe.Version >= ManualReproductionRecipeVersion {
		if err := validateBrowserPlanString("manual_reproduction_recipe.frontend_entry_id", recipe.FrontendEntryID, true); err != nil {
			return err
		}
		if err := validateBrowserPlanString("manual_reproduction_recipe.frontend_entry_name", recipe.FrontendEntryName, true); err != nil {
			return err
		}
	}
	if err := validateBrowserPlanString("manual_reproduction_recipe.start_url", recipe.StartURL, true); err != nil {
		return err
	}
	if err := validateBrowserPlanString("manual_reproduction_recipe.final_url", recipe.FinalURL, false); err != nil {
		return err
	}
	if err := validateBrowserPlanString("manual_reproduction_recipe.title", recipe.Title, false); err != nil {
		return err
	}
	if len(recipe.Actions) > 40 {
		return errors.New("manual reproduction recipe actions must contain at most 40 entries")
	}
	seen := make(map[string]struct{}, len(recipe.Actions))
	for index, action := range recipe.Actions {
		prefix := fmt.Sprintf("manual_reproduction_recipe.actions[%d]", index)
		if err := validateBrowserPlanString(prefix+".id", action.ID, true); err != nil {
			return err
		}
		if _, duplicate := seen[action.ID]; duplicate {
			return fmt.Errorf("manual reproduction recipe action id %q is duplicated", action.ID)
		}
		seen[action.ID] = struct{}{}
		switch action.Action {
		case "click", "fill", "press", "select":
		default:
			return fmt.Errorf("manual reproduction recipe %s action %q is not replayable", prefix, action.Action)
		}
		if action.Locator == nil {
			// Keep the observed action in the audit trail. It cannot be enforced
			// as a replay step until a safe locator is available.
			continue
		}
		if err := validateManualReproductionLocator(prefix+".locator", action.Locator); err != nil {
			return err
		}
		if (action.Action == "fill" || action.Action == "select") && !action.ValueRedacted {
			if err := validateBrowserPlanString(prefix+".value", action.Value, true); err != nil {
				return err
			}
		}
		if action.ValueRedacted && action.Value != "" {
			return fmt.Errorf("manual reproduction recipe %s redacted value must be empty", prefix)
		}
		if action.Action == "press" {
			if err := validateBrowserPlanString(prefix+".key", action.Key, true); err != nil {
				return err
			}
		} else if action.Key != "" {
			return fmt.Errorf("manual reproduction recipe %s key is only allowed for press actions", prefix)
		}
	}
	return nil
}

func (bundle BrowserManualReproductionBundle) Validate() error {
	if bundle.Version != ManualReproductionBundleVersion {
		return fmt.Errorf("manual reproduction bundle version must be %d", ManualReproductionBundleVersion)
	}
	if len(bundle.Segments) == 0 || len(bundle.Segments) > 16 {
		return errors.New("manual reproduction bundle must contain 1 to 16 segments")
	}
	seen := make(map[string]struct{}, len(bundle.Segments))
	legacyCount := 0
	for index, segment := range bundle.Segments {
		if err := segment.Validate(); err != nil {
			return fmt.Errorf("manual reproduction bundle segment %d: %w", index, err)
		}
		key := strings.TrimSpace(segment.FrontendEntryID)
		if segment.Version == LegacyManualReproductionRecipeVersion {
			legacyCount++
			key = "__legacy__"
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("manual reproduction bundle frontend entry %q is duplicated", key)
		}
		seen[key] = struct{}{}
	}
	if legacyCount > 1 || (legacyCount == 1 && len(bundle.Segments) > 1) {
		return errors.New("legacy manual reproduction recipe cannot be combined with other segments")
	}
	return nil
}

func validateManualReproductionLocator(field string, locator *BrowserLocator) error {
	if locator == nil {
		return fmt.Errorf("%s is required", field)
	}
	if err := validateBrowserPlanString(field+".value", locator.Value, true); err != nil {
		return err
	}
	switch locator.Kind {
	case "role", "label", "text", "placeholder", "test_id", "css":
	default:
		return fmt.Errorf("%s kind %q is not supported", field, locator.Kind)
	}
	if locator.Kind != "role" && locator.Name != "" {
		return fmt.Errorf("%s name is only allowed for role locators", field)
	}
	if locator.Within != nil {
		return fmt.Errorf("%s nested scope is not supported in a recorded recipe", field)
	}
	return nil
}

func (recipe BrowserManualReproductionRecipe) ReplayableActions() []BrowserManualReproductionAction {
	replayable := make([]BrowserManualReproductionAction, 0, len(recipe.Actions))
	for _, action := range recipe.Actions {
		if action.Locator == nil || ((action.Action == "fill" || action.Action == "select") && action.ValueRedacted) {
			continue
		}
		replayable = append(replayable, action)
	}
	return replayable
}

func validateBrowserPlanManualReproductionRecipe(plan BrowserPlan, recipe *BrowserManualReproductionRecipe) error {
	if recipe == nil {
		return nil
	}
	bundle := &BrowserManualReproductionBundle{Version: ManualReproductionBundleVersion, Segments: []BrowserManualReproductionRecipe{*recipe}}
	return validateBrowserPlanManualReproductionBundle(plan, bundle)
}

func validateBrowserPlanManualReproductionBundle(plan BrowserPlan, bundle *BrowserManualReproductionBundle) error {
	if bundle == nil {
		return nil
	}
	if err := bundle.Validate(); err != nil {
		return err
	}
	cursor := 0
	for segmentIndex, segment := range bundle.Segments {
		entry := strings.TrimSpace(segment.FrontendEntryID)
		if entry == "" {
			entry = "legacy"
		}
		if name := strings.TrimSpace(segment.FrontendEntryName); name != "" {
			entry += " (" + name + ")"
		}
		activated := segmentIndex == 0 && sameManualReproductionURL(plan.StartURL, segment.StartURL)
		if !activated {
			for cursor < len(plan.Actions) {
				candidate := plan.Actions[cursor]
				cursor++
				if candidate.Action == "goto" && sameManualReproductionURL(candidate.URL, segment.StartURL) {
					activated = true
					break
				}
			}
		}
		if !activated {
			return fmt.Errorf("browser plan must activate manual reproduction frontend entry %s at %q", entry, segment.StartURL)
		}
		for _, expected := range segment.ReplayableActions() {
			matched := false
			for cursor < len(plan.Actions) {
				candidate := plan.Actions[cursor]
				cursor++
				if manualReproductionActionMatches(candidate, expected) {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("browser plan must replay recorded manual action %q (%s) for frontend entry %s in order", expected.ID, strings.TrimSpace(expected.Label), entry)
			}
		}
	}
	return nil
}

func sameManualReproductionURL(left, right string) bool {
	return strings.TrimRight(strings.TrimSpace(left), "/") == strings.TrimRight(strings.TrimSpace(right), "/")
}

func manualReproductionActionMatches(candidate BrowserAction, expected BrowserManualReproductionAction) bool {
	if candidate.Action != expected.Action || !reflect.DeepEqual(candidate.Locator, expected.Locator) {
		return false
	}
	if (expected.Action == "fill" || expected.Action == "select") && candidate.Value != expected.Value {
		return false
	}
	return expected.Action != "press" || strings.EqualFold(strings.TrimSpace(candidate.Key), strings.TrimSpace(expected.Key))
}

func (r *AgentPhaseRunner) browserManualReproductionBundle(ctx context.Context, attempt PhaseAttempt) (*BrowserManualReproductionBundle, error) {
	if r == nil || r.store == nil || strings.TrimSpace(attempt.ParentAttemptID) == "" {
		return nil, nil
	}
	ancestorIDs := make(map[string]struct{})
	parentID := strings.TrimSpace(attempt.ParentAttemptID)
	for parentID != "" {
		if _, duplicate := ancestorIDs[parentID]; duplicate {
			return nil, errors.New("manual reproduction evidence ancestry contains a cycle")
		}
		parent, err := r.store.GetAttempt(ctx, parentID)
		if err != nil {
			return nil, err
		}
		if parent.CaseID != attempt.CaseID || parent.CycleNumber != attempt.CycleNumber {
			return nil, errors.New("manual reproduction evidence ancestor does not belong to the current Case cycle")
		}
		ancestorIDs[parent.ID] = struct{}{}
		parentID = strings.TrimSpace(parent.ParentAttemptID)
	}
	artifacts, err := r.store.ListEvidenceArtifacts(ctx, attempt.CaseID)
	if err != nil {
		return nil, err
	}
	candidates := make([]EvidenceArtifact, 0)
	for _, artifact := range artifacts {
		if artifact.Kind != ManualReproductionArtifactKind || artifact.RedactionStatus == RedactionStatusPending {
			continue
		}
		if _, belongs := ancestorIDs[artifact.AttemptID]; belongs {
			candidates = append(candidates, artifact)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].CapturedAt.Equal(candidates[right].CapturedAt) {
			return candidates[left].ID < candidates[right].ID
		}
		return candidates[left].CapturedAt.Before(candidates[right].CapturedAt)
	})
	type capturedRecipe struct {
		recipe   BrowserManualReproductionRecipe
		captured EvidenceArtifact
	}
	latest := make(map[string]capturedRecipe)
	hasVersionedSegment := false
	for _, candidate := range candidates {
		stored, readErr := ReadEvidenceArtifactFromRoot(ctx, r.store, r.artifactsRoot, attempt.CaseID, candidate.ID)
		if readErr != nil {
			return nil, readErr
		}
		recipe, parseErr := ParseBrowserManualReproductionRecipe(stored.Content)
		if parseErr != nil {
			return nil, parseErr
		}
		key := strings.TrimSpace(recipe.FrontendEntryID)
		if recipe.Version == LegacyManualReproductionRecipeVersion {
			key = "__legacy__"
		} else {
			hasVersionedSegment = true
		}
		latest[key] = capturedRecipe{recipe: recipe, captured: candidate}
	}
	if hasVersionedSegment {
		delete(latest, "__legacy__")
	}
	selected := make([]capturedRecipe, 0, len(latest))
	for _, item := range latest {
		selected = append(selected, item)
	}
	entryOrder := make(map[string]int)
	for index, entry := range browserAttemptFrontendEntryBindings(attempt) {
		entryOrder[entry.ID] = index
	}
	sort.SliceStable(selected, func(left, right int) bool {
		leftOrder, leftConfigured := entryOrder[selected[left].recipe.FrontendEntryID]
		rightOrder, rightConfigured := entryOrder[selected[right].recipe.FrontendEntryID]
		if leftConfigured && rightConfigured && leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		if leftConfigured != rightConfigured {
			return leftConfigured
		}
		if selected[left].captured.CapturedAt.Equal(selected[right].captured.CapturedAt) {
			return selected[left].captured.ID < selected[right].captured.ID
		}
		return selected[left].captured.CapturedAt.Before(selected[right].captured.CapturedAt)
	})
	bundle := &BrowserManualReproductionBundle{Version: ManualReproductionBundleVersion, Segments: make([]BrowserManualReproductionRecipe, 0, len(selected))}
	for _, item := range selected {
		bundle.Segments = append(bundle.Segments, item.recipe)
	}
	if err := bundle.Validate(); err != nil {
		return nil, err
	}
	return bundle, nil
}
