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
	ManualReproductionRecipeVersion = 1
	ManualReproductionArtifactKind  = "manual_reproduction_recipe"
)

// BrowserManualReproductionRecipe is a host-recorded, credential-safe replay
// trace. It is evidence, not model output. The browser planner may add waits,
// screenshots and assertions, but every replayable action must remain present
// in order in the generated plan.
type BrowserManualReproductionRecipe struct {
	Version  int                               `json:"version"`
	StartURL string                            `json:"start_url"`
	FinalURL string                            `json:"final_url,omitempty"`
	Actions  []BrowserManualReproductionAction `json:"actions"`
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
	if recipe.Version != ManualReproductionRecipeVersion {
		return fmt.Errorf("manual reproduction recipe version must be %d", ManualReproductionRecipeVersion)
	}
	if err := validateBrowserPlanString("manual_reproduction_recipe.start_url", recipe.StartURL, true); err != nil {
		return err
	}
	if err := validateBrowserPlanString("manual_reproduction_recipe.final_url", recipe.FinalURL, false); err != nil {
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
	expected := recipe.ReplayableActions()
	if len(expected) == 0 {
		return nil
	}
	actual := make([]BrowserAction, 0, len(plan.Actions))
	for _, action := range plan.Actions {
		switch action.Action {
		case "click", "fill", "press", "select":
			actual = append(actual, action)
		}
	}
	next := 0
	for _, candidate := range actual {
		if next >= len(expected) {
			break
		}
		want := expected[next]
		if candidate.Action != want.Action || !reflect.DeepEqual(candidate.Locator, want.Locator) {
			continue
		}
		if (want.Action == "fill" || want.Action == "select") && candidate.Value != want.Value {
			continue
		}
		if want.Action == "press" && !strings.EqualFold(strings.TrimSpace(candidate.Key), strings.TrimSpace(want.Key)) {
			continue
		}
		next++
	}
	if next != len(expected) {
		missing := expected[next]
		return fmt.Errorf("browser plan must replay recorded manual action %q (%s) in order", missing.ID, strings.TrimSpace(missing.Label))
	}
	return nil
}

func (r *AgentPhaseRunner) browserManualReproductionRecipe(ctx context.Context, attempt PhaseAttempt) (*BrowserManualReproductionRecipe, error) {
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
			return candidates[left].ID > candidates[right].ID
		}
		return candidates[left].CapturedAt.After(candidates[right].CapturedAt)
	})
	stored, err := ReadEvidenceArtifactFromRoot(ctx, r.store, r.artifactsRoot, attempt.CaseID, candidates[0].ID)
	if err != nil {
		return nil, err
	}
	recipe, err := ParseBrowserManualReproductionRecipe(stored.Content)
	if err != nil {
		return nil, err
	}
	return &recipe, nil
}
