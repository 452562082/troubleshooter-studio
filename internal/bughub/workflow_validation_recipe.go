package bughub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// ValidationRecipe is the durable, Case-scoped browser program. Attempts keep
// their own crash journal, while this record is reused by later attempts and
// regression cycles after one execution proved the plan runnable.
type ValidationRecipe struct {
	CaseID                 string
	ScenarioSHA256         string
	PlanSHA256             string
	Plan                   BrowserPlan
	AutonomousRecipeSHA256 string
	AutonomousRecipe       *AutonomousValidationRecipe
	SourceAttemptID        string
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type ValidationRecipeStore interface {
	GetValidationRecipe(context.Context, string) (ValidationRecipe, bool, error)
	StoreValidationRecipe(context.Context, ValidationRecipe) (ValidationRecipe, error)
}

func autonomousValidationRecipeForPlan(ctx context.Context, store ValidationRecipeStore, caseID string, plan BrowserPlan) (*AutonomousValidationRecipe, error) {
	if store == nil || strings.TrimSpace(caseID) == "" {
		return nil, nil
	}
	stored, found, err := store.GetValidationRecipe(ctx, caseID)
	if err != nil || !found || stored.AutonomousRecipe == nil || plan.ScenarioContract == nil {
		return nil, err
	}
	planSHA256, err := durableBrowserPlanSHA256(plan)
	if err != nil {
		return nil, err
	}
	if stored.ScenarioSHA256 != plan.ScenarioContract.ContextSHA256 || stored.PlanSHA256 != planSHA256 {
		return nil, nil
	}
	copy := *stored.AutonomousRecipe
	copy.Steps = append([]AutonomousRecipeStep(nil), stored.AutonomousRecipe.Steps...)
	return &copy, nil
}

func validationRecipeFromBrowserExecution(
	ctx context.Context,
	store ValidationRecipeStore,
	caseID, sourceAttemptID, scenarioSHA256 string,
	plan BrowserPlan,
	result BrowserVerificationResult,
) (ValidationRecipe, error) {
	planSHA256, err := durableBrowserPlanSHA256(plan)
	if err != nil {
		return ValidationRecipe{}, err
	}
	recipe := ValidationRecipe{
		CaseID: caseID, ScenarioSHA256: scenarioSHA256, PlanSHA256: planSHA256,
		Plan: plan, SourceAttemptID: sourceAttemptID,
	}
	if result.autonomousScenarioSHA256 == scenarioSHA256 && len(result.autonomousTrace) != 0 {
		if autonomous, compileErr := CompileAutonomousValidationRecipe(plan, scenarioSHA256, result.autonomousTrace); compileErr == nil {
			digest, digestErr := autonomousValidationRecipeSHA256(autonomous, plan)
			if digestErr != nil {
				return ValidationRecipe{}, digestErr
			}
			recipe.AutonomousRecipeSHA256 = digest
			recipe.AutonomousRecipe = &autonomous
		}
	}
	if recipe.AutonomousRecipe == nil && store != nil {
		stored, found, loadErr := store.GetValidationRecipe(ctx, caseID)
		if loadErr != nil {
			return ValidationRecipe{}, loadErr
		}
		if found && stored.ScenarioSHA256 == scenarioSHA256 && stored.PlanSHA256 == planSHA256 && stored.AutonomousRecipe != nil {
			recipe.AutonomousRecipeSHA256 = stored.AutonomousRecipeSHA256
			copy := *stored.AutonomousRecipe
			copy.Steps = append([]AutonomousRecipeStep(nil), stored.AutonomousRecipe.Steps...)
			recipe.AutonomousRecipe = &copy
		}
	}
	return recipe, nil
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
	if recipe.AutonomousRecipe == nil {
		if strings.TrimSpace(recipe.AutonomousRecipeSHA256) != "" {
			return errors.New("validation recipe autonomous digest has no recipe")
		}
		return nil
	}
	if recipe.AutonomousRecipe.ScenarioContractSHA256 != recipe.ScenarioSHA256 || recipe.AutonomousRecipe.PlanSHA256 != recipe.PlanSHA256 {
		return errors.New("validation recipe autonomous binding does not match")
	}
	autonomousSHA256, err := autonomousValidationRecipeSHA256(*recipe.AutonomousRecipe, recipe.Plan)
	if err != nil || autonomousSHA256 != recipe.AutonomousRecipeSHA256 {
		return errors.New("validation recipe autonomous digest does not match")
	}
	return nil
}

func validateBrowserRegressionScenarioBinding(binding BrowserRegressionScenarioBinding, originalAttemptID string) error {
	if binding.Version != 1 {
		return errors.New("browser regression scenario binding version must be 1")
	}
	if strings.TrimSpace(binding.SourceAttemptID) == "" || binding.SourceAttemptID != strings.TrimSpace(originalAttemptID) {
		return errors.New("browser regression scenario binding source attempt does not match")
	}
	if !validLowerSHA256(binding.ScenarioSHA256) || !validLowerSHA256(binding.PlanSHA256) {
		return errors.New("browser regression scenario binding digests are invalid")
	}
	if binding.DeviceProfile != "desktop" && binding.DeviceProfile != "mobile" {
		return errors.New("browser regression scenario binding device profile is invalid")
	}
	if binding.ScenarioContract.ContextSHA256 != binding.ScenarioSHA256 {
		return errors.New("browser regression scenario contract digest does not match")
	}
	if strings.TrimSpace(binding.ScenarioContract.Goal) == "" {
		return errors.New("browser regression scenario contract goal is required")
	}
	return nil
}

func validateRegressionRecipeBinding(binding BrowserRegressionScenarioBinding, recipe ValidationRecipe) error {
	if err := validateBrowserRegressionScenarioBinding(binding, binding.SourceAttemptID); err != nil {
		return err
	}
	if recipe.SourceAttemptID != binding.SourceAttemptID ||
		recipe.ScenarioSHA256 != binding.ScenarioSHA256 ||
		recipe.PlanSHA256 != binding.PlanSHA256 ||
		!reflect.DeepEqual(recipe.Plan.ScenarioContract, &binding.ScenarioContract) {
		return errors.New("frozen browser recipe differs from the regression scenario binding")
	}
	profile := strings.TrimSpace(recipe.Plan.DeviceProfile)
	if profile == "" {
		profile = "desktop"
	}
	if profile != binding.DeviceProfile {
		return errors.New("frozen browser recipe device profile differs from the regression scenario binding")
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
	autonomousJSON := ""
	if recipe.AutonomousRecipe != nil {
		autonomousEncoded, encodeErr := json.Marshal(recipe.AutonomousRecipe)
		if encodeErr != nil || len(autonomousEncoded) > maxAutonomousValidationRecipeSize || containsSensitiveData(autonomousEncoded) {
			return ValidationRecipe{}, errors.New("validation recipe autonomous content is unsafe")
		}
		autonomousJSON = string(autonomousEncoded)
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
		case_id, scenario_sha256, plan_sha256, plan_json, autonomous_recipe_sha256, autonomous_recipe_json, source_attempt_id, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(case_id) DO UPDATE SET
		scenario_sha256=excluded.scenario_sha256,
		plan_sha256=excluded.plan_sha256,
		plan_json=excluded.plan_json,
		autonomous_recipe_sha256=excluded.autonomous_recipe_sha256,
		autonomous_recipe_json=excluded.autonomous_recipe_json,
		source_attempt_id=excluded.source_attempt_id,
		updated_at=excluded.updated_at`,
		recipe.CaseID, recipe.ScenarioSHA256, recipe.PlanSHA256, string(encoded), recipe.AutonomousRecipeSHA256, autonomousJSON,
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
	var planJSON, autonomousJSON, createdAt, updatedAt string
	err := s.db.QueryRowContext(ctx, `SELECT case_id, scenario_sha256, plan_sha256, plan_json,
		autonomous_recipe_sha256, autonomous_recipe_json, source_attempt_id, created_at, updated_at FROM validation_recipes WHERE case_id=?`, caseID).Scan(
		&recipe.CaseID, &recipe.ScenarioSHA256, &recipe.PlanSHA256, &planJSON, &recipe.AutonomousRecipeSHA256, &autonomousJSON,
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
	if autonomousJSON != "" {
		autonomous, decodeErr := ParseAutonomousValidationRecipe([]byte(autonomousJSON), recipe.Plan)
		if decodeErr != nil {
			return ValidationRecipe{}, false, errors.New("stored autonomous validation recipe is invalid")
		}
		recipe.AutonomousRecipe = &autonomous
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
