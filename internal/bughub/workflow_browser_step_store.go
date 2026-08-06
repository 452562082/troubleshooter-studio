package bughub

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type BrowserDecisionStepStatus string

const (
	BrowserDecisionStepPrepared  BrowserDecisionStepStatus = "prepared"
	BrowserDecisionStepExecuting BrowserDecisionStepStatus = "executing"
	BrowserDecisionStepConfirmed BrowserDecisionStepStatus = "confirmed"
	BrowserDecisionStepNoEffect  BrowserDecisionStepStatus = "no_effect"
	BrowserDecisionStepBlocked   BrowserDecisionStepStatus = "blocked"
	BrowserDecisionStepAmbiguous BrowserDecisionStepStatus = "ambiguous"
	BrowserDecisionStepUncertain BrowserDecisionStepStatus = "uncertain"
)

const (
	BrowserStepEffectConfirmedCode = "browser_step_effect_confirmed"
	BrowserStepNoEffectCode        = "browser_action_no_effect"
	BrowserStepAmbiguousCode       = "browser_step_effect_ambiguous"
	BrowserStepUncertainCode       = "browser_step_effect_uncertain"
)

// BrowserDecisionStep is the durable idempotency boundary around one future
// BrowserDecision action. It contains only digests and frozen artifact refs;
// raw Scene/Decision content remains in the attempt artifact store.
type BrowserDecisionStep struct {
	AttemptID         string
	StepNo            int
	SceneSHA256       string
	DecisionSHA256    string
	ActionFingerprint string
	Status            BrowserDecisionStepStatus
	EffectCode        string
	BeforeSceneRef    string
	AfterSceneRef     string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type BrowserDecisionStepStore interface {
	PrepareBrowserDecisionStep(context.Context, BrowserDecisionStep) (BrowserDecisionStep, bool, error)
	StartBrowserDecisionStep(context.Context, string, int, string) (BrowserDecisionStep, bool, error)
	CompleteBrowserDecisionStep(context.Context, string, int, BrowserDecisionStepStatus, string, string) (BrowserDecisionStep, bool, error)
	GetBrowserDecisionStep(context.Context, string, int) (BrowserDecisionStep, bool, error)
	ListBrowserDecisionSteps(context.Context, string) ([]BrowserDecisionStep, error)
	MarkExecutingBrowserDecisionStepsUncertain(context.Context, string) (int64, error)
}

func (status BrowserDecisionStepStatus) terminal() bool {
	return status == BrowserDecisionStepConfirmed || status == BrowserDecisionStepNoEffect || status == BrowserDecisionStepBlocked || status == BrowserDecisionStepAmbiguous || status == BrowserDecisionStepUncertain
}

func (step BrowserDecisionStep) validate() error {
	if strings.TrimSpace(step.AttemptID) == "" || step.StepNo < 1 || !validLowerSHA256(step.SceneSHA256) || !validLowerSHA256(step.DecisionSHA256) || !validLowerSHA256(step.ActionFingerprint) {
		return errors.New("browser decision step identity is invalid")
	}
	if !validBrowserStepSceneReference(step.BeforeSceneRef, true) || !validBrowserStepSceneReference(step.AfterSceneRef, step.Status == BrowserDecisionStepConfirmed || step.Status == BrowserDecisionStepNoEffect || step.Status == BrowserDecisionStepBlocked) {
		return errors.New("browser decision step scene reference is invalid")
	}
	switch step.Status {
	case BrowserDecisionStepPrepared, BrowserDecisionStepExecuting:
		if step.EffectCode != "" || step.AfterSceneRef != "" {
			return errors.New("unfinished browser decision step contains outcome fields")
		}
	case BrowserDecisionStepConfirmed:
		if step.EffectCode != BrowserStepEffectConfirmedCode {
			return errors.New("confirmed browser decision step effect code is invalid")
		}
	case BrowserDecisionStepNoEffect:
		if step.EffectCode != BrowserStepNoEffectCode {
			return errors.New("no-effect browser decision step effect code is invalid")
		}
	case BrowserDecisionStepBlocked:
		if !validBrowserStepEffectCode(step.EffectCode) {
			return errors.New("blocked browser decision step effect code is invalid")
		}
	case BrowserDecisionStepAmbiguous:
		if step.EffectCode != BrowserStepAmbiguousCode {
			return errors.New("ambiguous browser decision step effect code is invalid")
		}
	case BrowserDecisionStepUncertain:
		if step.EffectCode != BrowserStepUncertainCode {
			return errors.New("uncertain browser decision step effect code is invalid")
		}
	default:
		return errors.New("browser decision step status is invalid")
	}
	if !step.CreatedAt.IsZero() && !step.UpdatedAt.IsZero() && step.UpdatedAt.Before(step.CreatedAt) {
		return errors.New("browser decision step updated_at precedes created_at")
	}
	return nil
}

func validBrowserStepSceneReference(value string, required bool) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return !required
	}
	if len(value) > 1024 || filepath.IsAbs(value) || strings.Contains(value, "\\") || strings.ContainsRune(value, '\x00') || redactSensitiveText(value) != value || containsSensitiveData([]byte(value)) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	return clean == value && clean != "." && !strings.HasPrefix(clean, "../") && !strings.Contains(clean, "/../")
}

func validBrowserStepEffectCode(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "browser_") || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' {
			continue
		}
		return false
	}
	return value != BrowserStepEffectConfirmedCode && value != BrowserStepNoEffectCode && value != BrowserStepAmbiguousCode && value != BrowserStepUncertainCode
}

func (s *CaseStore) PrepareBrowserDecisionStep(ctx context.Context, step BrowserDecisionStep) (BrowserDecisionStep, bool, error) {
	if s == nil || s.db == nil {
		return BrowserDecisionStep{}, false, errors.New("browser decision step store is unavailable")
	}
	step.Status = BrowserDecisionStepPrepared
	step.EffectCode = ""
	step.AfterSceneRef = ""
	if err := step.validate(); err != nil {
		return BrowserDecisionStep{}, false, err
	}
	now := time.Now().UTC()
	if step.CreatedAt.IsZero() {
		step.CreatedAt = now
	} else {
		step.CreatedAt = step.CreatedAt.UTC()
	}
	if step.UpdatedAt.IsZero() {
		step.UpdatedAt = step.CreatedAt
	} else {
		step.UpdatedAt = step.UpdatedAt.UTC()
	}
	if err := step.validate(); err != nil {
		return BrowserDecisionStep{}, false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO browser_decision_steps (
		attempt_id,step_no,scene_sha256,decision_sha256,action_fingerprint,status,effect_code,
		before_scene_ref,after_scene_ref,created_at,updated_at
	) SELECT ?,?,?,?,?,'prepared','',?,'',?,? FROM phase_attempts
		WHERE id=? AND phase IN ('validation','regression') AND status='running'
		ON CONFLICT DO NOTHING`,
		step.AttemptID, step.StepNo, step.SceneSHA256, step.DecisionSHA256, step.ActionFingerprint,
		step.BeforeSceneRef, formatStoreTime(step.CreatedAt), formatStoreTime(step.UpdatedAt), step.AttemptID)
	if err != nil {
		return BrowserDecisionStep{}, false, fmt.Errorf("prepare browser decision step: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return BrowserDecisionStep{}, false, fmt.Errorf("prepare browser decision step rows: %w", err)
	}
	stored, found, err := s.GetBrowserDecisionStep(ctx, step.AttemptID, step.StepNo)
	if err != nil {
		return BrowserDecisionStep{}, false, err
	}
	if !found {
		return BrowserDecisionStep{}, false, errors.New("browser decision step requires a running validation or regression attempt")
	}
	if !samePreparedBrowserDecisionStep(stored, step) {
		return BrowserDecisionStep{}, false, errors.New("browser decision step idempotency conflict")
	}
	return stored, rows == 0, nil
}

func samePreparedBrowserDecisionStep(left, right BrowserDecisionStep) bool {
	return left.AttemptID == right.AttemptID && left.StepNo == right.StepNo && left.SceneSHA256 == right.SceneSHA256 && left.DecisionSHA256 == right.DecisionSHA256 && left.ActionFingerprint == right.ActionFingerprint && left.BeforeSceneRef == right.BeforeSceneRef
}

func (s *CaseStore) StartBrowserDecisionStep(ctx context.Context, attemptID string, stepNo int, decisionSHA256 string) (BrowserDecisionStep, bool, error) {
	if s == nil || s.db == nil || strings.TrimSpace(attemptID) == "" || stepNo < 1 || !validLowerSHA256(decisionSHA256) {
		return BrowserDecisionStep{}, false, errors.New("browser decision step start identity is invalid")
	}
	now := formatStoreTime(time.Now().UTC())
	result, err := s.db.ExecContext(ctx, `UPDATE browser_decision_steps SET status='executing',updated_at=?
		WHERE attempt_id=? AND step_no=? AND decision_sha256=? AND status='prepared'
		AND EXISTS (SELECT 1 FROM phase_attempts WHERE id=? AND phase IN ('validation','regression') AND status='running')`, now, attemptID, stepNo, decisionSHA256, attemptID)
	if err != nil {
		return BrowserDecisionStep{}, false, fmt.Errorf("start browser decision step: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return BrowserDecisionStep{}, false, err
	}
	stored, found, err := s.GetBrowserDecisionStep(ctx, attemptID, stepNo)
	if err != nil {
		return BrowserDecisionStep{}, false, err
	}
	eligible, eligibilityErr := s.browserDecisionStepAttemptEligible(ctx, attemptID)
	if eligibilityErr != nil {
		return BrowserDecisionStep{}, false, eligibilityErr
	}
	if !found || !eligible || stored.DecisionSHA256 != decisionSHA256 || (rows == 0 && stored.Status != BrowserDecisionStepExecuting) {
		return BrowserDecisionStep{}, false, errors.New("browser decision step cannot enter executing state")
	}
	return stored, rows == 0, nil
}

func (s *CaseStore) CompleteBrowserDecisionStep(ctx context.Context, attemptID string, stepNo int, status BrowserDecisionStepStatus, effectCode, afterSceneRef string) (BrowserDecisionStep, bool, error) {
	if s == nil || s.db == nil || strings.TrimSpace(attemptID) == "" || stepNo < 1 || !status.terminal() {
		return BrowserDecisionStep{}, false, errors.New("browser decision step completion identity is invalid")
	}
	probe, found, err := s.GetBrowserDecisionStep(ctx, attemptID, stepNo)
	if err != nil || !found {
		return BrowserDecisionStep{}, false, errors.Join(errors.New("browser decision step is missing"), err)
	}
	probe.Status = status
	probe.EffectCode = strings.TrimSpace(effectCode)
	probe.AfterSceneRef = strings.TrimSpace(afterSceneRef)
	probe.UpdatedAt = time.Now().UTC()
	if err := probe.validate(); err != nil {
		return BrowserDecisionStep{}, false, err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE browser_decision_steps SET status=?,effect_code=?,after_scene_ref=?,updated_at=?
		WHERE attempt_id=? AND step_no=? AND status='executing'
		AND EXISTS (SELECT 1 FROM phase_attempts WHERE id=? AND phase IN ('validation','regression') AND status='running')`, status, probe.EffectCode, probe.AfterSceneRef, formatStoreTime(probe.UpdatedAt), attemptID, stepNo, attemptID)
	if err != nil {
		return BrowserDecisionStep{}, false, fmt.Errorf("complete browser decision step: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return BrowserDecisionStep{}, false, err
	}
	stored, found, err := s.GetBrowserDecisionStep(ctx, attemptID, stepNo)
	if err != nil {
		return BrowserDecisionStep{}, false, err
	}
	if !found || stored.Status != status || stored.EffectCode != probe.EffectCode || stored.AfterSceneRef != probe.AfterSceneRef {
		return BrowserDecisionStep{}, false, errors.New("browser decision step completion conflicts with its durable state")
	}
	return stored, rows == 0, nil
}

func (s *CaseStore) browserDecisionStepAttemptEligible(ctx context.Context, attemptID string) (bool, error) {
	var exists int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM phase_attempts WHERE id=? AND phase IN ('validation','regression') AND status='running'`, attemptID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check browser decision step attempt: %w", err)
	}
	return true, nil
}

func (s *CaseStore) GetBrowserDecisionStep(ctx context.Context, attemptID string, stepNo int) (BrowserDecisionStep, bool, error) {
	if s == nil || s.db == nil || strings.TrimSpace(attemptID) == "" || stepNo < 1 {
		return BrowserDecisionStep{}, false, errors.New("browser decision step lookup identity is invalid")
	}
	row := s.db.QueryRowContext(ctx, `SELECT attempt_id,step_no,scene_sha256,decision_sha256,action_fingerprint,status,
		effect_code,before_scene_ref,after_scene_ref,created_at,updated_at
		FROM browser_decision_steps WHERE attempt_id=? AND step_no=?`, attemptID, stepNo)
	step, err := scanBrowserDecisionStep(row)
	if errors.Is(err, sql.ErrNoRows) {
		return BrowserDecisionStep{}, false, nil
	}
	if err != nil {
		return BrowserDecisionStep{}, false, fmt.Errorf("get browser decision step: %w", err)
	}
	return step, true, nil
}

func (s *CaseStore) ListBrowserDecisionSteps(ctx context.Context, attemptID string) ([]BrowserDecisionStep, error) {
	if s == nil || s.db == nil || strings.TrimSpace(attemptID) == "" {
		return nil, errors.New("browser decision step attempt is required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT attempt_id,step_no,scene_sha256,decision_sha256,action_fingerprint,status,
		effect_code,before_scene_ref,after_scene_ref,created_at,updated_at
		FROM browser_decision_steps WHERE attempt_id=? ORDER BY step_no`, attemptID)
	if err != nil {
		return nil, fmt.Errorf("list browser decision steps: %w", err)
	}
	defer rows.Close()
	steps := make([]BrowserDecisionStep, 0)
	for rows.Next() {
		step, err := scanBrowserDecisionStep(rows)
		if err != nil {
			return nil, fmt.Errorf("scan browser decision step: %w", err)
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list browser decision steps: %w", err)
	}
	return steps, nil
}

func (s *CaseStore) MarkExecutingBrowserDecisionStepsUncertain(ctx context.Context, attemptID string) (int64, error) {
	if s == nil || s.db == nil || strings.TrimSpace(attemptID) == "" {
		return 0, errors.New("browser decision step attempt is required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE browser_decision_steps SET status='uncertain',effect_code=?,updated_at=?
		WHERE attempt_id=? AND status='executing'`, BrowserStepUncertainCode, formatStoreTime(time.Now().UTC()), attemptID)
	if err != nil {
		return 0, fmt.Errorf("mark executing browser decision steps uncertain: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return rows, nil
}

type browserDecisionStepScanner interface {
	Scan(...any) error
}

func scanBrowserDecisionStep(scanner browserDecisionStepScanner) (BrowserDecisionStep, error) {
	var step BrowserDecisionStep
	var createdAt, updatedAt string
	if err := scanner.Scan(&step.AttemptID, &step.StepNo, &step.SceneSHA256, &step.DecisionSHA256, &step.ActionFingerprint,
		&step.Status, &step.EffectCode, &step.BeforeSceneRef, &step.AfterSceneRef, &createdAt, &updatedAt); err != nil {
		return BrowserDecisionStep{}, err
	}
	var err error
	if step.CreatedAt, err = parseStoreTime(createdAt); err != nil {
		return BrowserDecisionStep{}, err
	}
	if step.UpdatedAt, err = parseStoreTime(updatedAt); err != nil {
		return BrowserDecisionStep{}, err
	}
	if err := step.validate(); err != nil {
		return BrowserDecisionStep{}, fmt.Errorf("validate stored browser decision step: %w", err)
	}
	return step, nil
}
