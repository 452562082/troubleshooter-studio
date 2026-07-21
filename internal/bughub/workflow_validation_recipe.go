package bughub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ValidationRecipe is the durable, Case-scoped browser program. Attempts keep
// their own crash journal, while this record is reused by later attempts and
// regression cycles after one execution proved the plan runnable.
type ValidationRecipe struct {
	CaseID          string
	ScenarioSHA256  string
	PlanSHA256      string
	Plan            BrowserPlan
	SourceAttemptID string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type ValidationRecipeStore interface {
	GetValidationRecipe(context.Context, string) (ValidationRecipe, bool, error)
	StoreValidationRecipe(context.Context, ValidationRecipe) (ValidationRecipe, error)
}

func validLowerSHA256(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (recipe ValidationRecipe) validate() error {
	if strings.TrimSpace(recipe.CaseID) == "" || strings.TrimSpace(recipe.SourceAttemptID) == "" {
		return errors.New("validation recipe case and source attempt are required")
	}
	if !validLowerSHA256(recipe.ScenarioSHA256) || !validLowerSHA256(recipe.PlanSHA256) {
		return errors.New("validation recipe digests are invalid")
	}
	if err := validateDurableBrowserPlan(recipe.Plan); err != nil {
		return fmt.Errorf("validation recipe plan: %w", err)
	}
	actual, err := durableBrowserPlanSHA256(recipe.Plan)
	if err != nil || actual != recipe.PlanSHA256 {
		return errors.New("validation recipe plan digest does not match")
	}
	return nil
}

func (s *CaseStore) StoreValidationRecipe(ctx context.Context, recipe ValidationRecipe) (ValidationRecipe, error) {
	if s == nil || s.db == nil {
		return ValidationRecipe{}, errors.New("validation recipe store is unavailable")
	}
	if err := recipe.validate(); err != nil {
		return ValidationRecipe{}, err
	}
	encoded, err := json.Marshal(recipe.Plan)
	if err != nil {
		return ValidationRecipe{}, fmt.Errorf("encode validation recipe plan: %w", err)
	}
	if len(encoded) > int(maxBrowserCoordinatorPlanJournalSize) || containsSensitiveData(encoded) {
		return ValidationRecipe{}, errors.New("validation recipe plan content is unsafe")
	}
	var sourceCaseID string
	var sourcePhase Phase
	if err := s.db.QueryRowContext(ctx, `SELECT case_id, phase FROM phase_attempts WHERE id=?`, recipe.SourceAttemptID).Scan(&sourceCaseID, &sourcePhase); err != nil {
		return ValidationRecipe{}, fmt.Errorf("load validation recipe source attempt: %w", err)
	}
	if sourceCaseID != recipe.CaseID || (sourcePhase != PhaseValidation && sourcePhase != PhaseRegression) {
		return ValidationRecipe{}, errors.New("validation recipe source attempt must be a validation or regression attempt for the same case")
	}
	now := time.Now().UTC()
	if recipe.CreatedAt.IsZero() {
		recipe.CreatedAt = now
	} else {
		recipe.CreatedAt = recipe.CreatedAt.UTC()
	}
	if recipe.UpdatedAt.IsZero() {
		recipe.UpdatedAt = now
	} else {
		recipe.UpdatedAt = recipe.UpdatedAt.UTC()
	}
	if recipe.UpdatedAt.Before(recipe.CreatedAt) {
		return ValidationRecipe{}, errors.New("validation recipe updated_at precedes created_at")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO validation_recipes (
		case_id, scenario_sha256, plan_sha256, plan_json, source_attempt_id, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(case_id) DO UPDATE SET
		scenario_sha256=excluded.scenario_sha256,
		plan_sha256=excluded.plan_sha256,
		plan_json=excluded.plan_json,
		source_attempt_id=excluded.source_attempt_id,
		updated_at=excluded.updated_at`,
		recipe.CaseID, recipe.ScenarioSHA256, recipe.PlanSHA256, string(encoded),
		recipe.SourceAttemptID, formatStoreTime(recipe.CreatedAt), formatStoreTime(recipe.UpdatedAt))
	if err != nil {
		return ValidationRecipe{}, fmt.Errorf("store validation recipe: %w", err)
	}
	if rows, rowsErr := result.RowsAffected(); rowsErr != nil || rows != 1 {
		return ValidationRecipe{}, errors.Join(errors.New("store validation recipe affected an unexpected row count"), rowsErr)
	}
	stored, found, err := s.GetValidationRecipe(ctx, recipe.CaseID)
	if err != nil || !found {
		return ValidationRecipe{}, errors.Join(errors.New("stored validation recipe is missing"), err)
	}
	return stored, nil
}

func (s *CaseStore) GetValidationRecipe(ctx context.Context, caseID string) (ValidationRecipe, bool, error) {
	if s == nil || s.db == nil || strings.TrimSpace(caseID) == "" {
		return ValidationRecipe{}, false, errors.New("validation recipe case is required")
	}
	var recipe ValidationRecipe
	var planJSON, createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT case_id, scenario_sha256, plan_sha256, plan_json,
		source_attempt_id, created_at, updated_at FROM validation_recipes WHERE case_id=?`, caseID).Scan(
		&recipe.CaseID, &recipe.ScenarioSHA256, &recipe.PlanSHA256, &planJSON,
		&recipe.SourceAttemptID, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ValidationRecipe{}, false, nil
	}
	if err != nil {
		return ValidationRecipe{}, false, fmt.Errorf("get validation recipe: %w", err)
	}
	decoder := json.NewDecoder(strings.NewReader(planJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&recipe.Plan); err != nil {
		return ValidationRecipe{}, false, errors.New("stored validation recipe plan is invalid")
	}
	if recipe.CreatedAt, err = parseStoreTime(createdAt); err != nil {
		return ValidationRecipe{}, false, err
	}
	if recipe.UpdatedAt, err = parseStoreTime(updatedAt); err != nil {
		return ValidationRecipe{}, false, err
	}
	if err := recipe.validate(); err != nil {
		return ValidationRecipe{}, false, err
	}
	return recipe, true, nil
}
