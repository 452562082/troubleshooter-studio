package bughub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

var (
	ErrRegressionDuplicate     = errors.New("identical regression scenario and deployment already exists")
	ErrRegressionBinding       = errors.New("regression is not bound to the current verified deployment")
	ErrRegressionFreshEvidence = errors.New("regression requires fresh current-attempt evidence")
)

type NextCycleInvestigationInput struct {
	PreviousCycle                int      `json:"previous_cycle"`
	RegressionAttemptID          string   `json:"regression_attempt_id"`
	ScenarioHash                 string   `json:"scenario_hash"`
	ObservedDeploymentVersion    string   `json:"observed_deployment_version"`
	RegressionEvidenceReferences []string `json:"regression_evidence_refs"`
	Delta                        string   `json:"delta"`
}

// StartRegression is the single entrypoint for scheduling a regression phase.
// It derives every input from durable records and never trusts caller supplied
// scenarios, commits, environments, versions, or evidence references.
func (o *CaseOrchestrator) StartRegression(ctx context.Context, caseID string, expectedVersion int64) (PhaseAttempt, error) {
	if o == nil || o.store == nil {
		return PhaseAttempt{}, errors.New("case orchestrator store is required")
	}
	release := workflowCommandLocks.acquire("start-regression:" + caseID)
	defer release()
	incident, err := o.store.GetCase(ctx, caseID)
	if err != nil {
		return PhaseAttempt{}, err
	}
	if incident.Version != expectedVersion {
		return PhaseAttempt{}, ErrCaseVersionConflict
	}
	if incident.Status != CaseDeploymentVerified {
		if existing, found := o.currentRegressionAttempt(ctx, incident); found {
			return existing, ErrRegressionDuplicate
		}
		return PhaseAttempt{}, ErrRegressionBinding
	}
	input, reservation, err := o.buildRegressionInput(ctx, incident)
	if err != nil {
		return PhaseAttempt{}, err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return PhaseAttempt{}, err
	}
	key := regressionIdempotencyKey(incident, input)
	attempt := newAttempt(incident, PhaseRegression, AttemptRegression, key, reservation.Bot, encoded, incident.CurrentAttemptID)
	existing, err := o.store.ListAttempts(ctx, AttemptFilter{CaseID: incident.ID})
	if err != nil {
		return PhaseAttempt{}, err
	}
	for _, candidate := range existing {
		if candidate.ID == attempt.ID {
			return candidate, ErrRegressionDuplicate
		}
	}
	_, err = o.beginPhase(ctx, incident, CaseRegressionValidating, attempt, reservation.Bug, reservation.Bot, key, "studio", "regression_started")
	if err != nil {
		return attempt, err
	}
	return attempt, nil
}

func (o *CaseOrchestrator) currentRegressionAttempt(ctx context.Context, incident IncidentCase) (PhaseAttempt, bool) {
	if incident.CurrentAttemptID == "" {
		return PhaseAttempt{}, false
	}
	attempt, err := o.store.GetAttempt(ctx, incident.CurrentAttemptID)
	return attempt, err == nil && attempt.Phase == PhaseRegression && attempt.Mode == AttemptRegression && attempt.CycleNumber == incident.CycleNumber
}

func regressionIdempotencyKey(incident IncidentCase, input RegressionValidationInput) string {
	material := strings.Join([]string{incident.ID, fmt.Sprint(incident.CycleNumber), input.OriginalScenarioHash, input.DeploymentObservationID, input.ObservedDeploymentVersion}, "\x1f")
	digest := sha256.Sum256([]byte(material))
	return "regression:" + incident.ID + ":" + hex.EncodeToString(digest[:])
}

func (o *CaseOrchestrator) buildRegressionInput(ctx context.Context, incident IncidentCase) (RegressionValidationInput, DeploymentReservation, error) {
	reservationEvent, found, err := o.store.latestDeploymentReservationEvent(ctx, incident.ID)
	if err != nil || !found {
		return RegressionValidationInput{}, DeploymentReservation{}, errors.Join(ErrRegressionBinding, err)
	}
	var reservation DeploymentReservation
	if err := json.Unmarshal(reservationEvent.PayloadJSON, &reservation); err != nil {
		return RegressionValidationInput{}, DeploymentReservation{}, errors.Join(ErrRegressionBinding, err)
	}
	if err := validateDeploymentReservationIdentity(reservation, reservationEvent.IdempotencyKey, reservation.CallerIdempotencyKey, reservationEvent.ActorID); err != nil {
		return RegressionValidationInput{}, DeploymentReservation{}, errors.Join(ErrRegressionBinding, err)
	}
	expected, err := o.expectedRegressionCommits(ctx, incident)
	if err != nil {
		return RegressionValidationInput{}, DeploymentReservation{}, errors.Join(ErrRegressionBinding, err)
	}
	if reservation.CycleNumber != incident.CycleNumber || reservation.Environment != incident.Environment || !equalStringMap(reservation.ExpectedCommits, expected) {
		return RegressionValidationInput{}, DeploymentReservation{}, ErrRegressionBinding
	}
	observations, err := o.store.ListDeploymentObservations(ctx, incident.ID)
	if err != nil || len(observations) == 0 {
		return RegressionValidationInput{}, DeploymentReservation{}, errors.Join(ErrRegressionBinding, err)
	}
	expectedObservationID := stableID("deployment", reservation.ReservationKey)
	var observation DeploymentObservation
	for _, candidate := range observations {
		if candidate.ID == expectedObservationID {
			observation = candidate
			break
		}
	}
	if observation.ID == "" || observation.Result != DeploymentResultMatched || observation.VerifiedAt == nil || observation.Environment != incident.Environment || strings.TrimSpace(observation.ObservedVersion) == "" || !equalStringMap(observation.ExpectedCommits, expected) || !observedCommitsCoverExpected(observation, expected) {
		return RegressionValidationInput{}, DeploymentReservation{}, ErrRegressionBinding
	}
	original, validation, err := o.originalValidation(ctx, incident.ID)
	if err != nil {
		return RegressionValidationInput{}, DeploymentReservation{}, err
	}
	artifacts, err := o.store.ListEvidenceArtifacts(ctx, incident.ID)
	if err != nil {
		return RegressionValidationInput{}, DeploymentReservation{}, err
	}
	refs := make([]string, 0)
	for _, artifact := range artifacts {
		if artifact.AttemptID == original.ID {
			refs = append(refs, artifact.ID+":"+artifact.SHA256)
		}
	}
	sort.Strings(refs)
	reproduction, scenarioHash, err := deterministicOriginalScenario(original, validation)
	if err != nil {
		return RegressionValidationInput{}, DeploymentReservation{}, err
	}
	if len(refs) == 0 {
		return RegressionValidationInput{}, DeploymentReservation{}, errors.New("original reproduced validation evidence is required")
	}
	return RegressionValidationInput{
		OriginalValidationAttemptID: original.ID,
		OriginalReproduction:        reproduction,
		ExpectedBehavior:            validation.ExpectedBehavior,
		OriginalObservedBehavior:    validation.ObservedBehavior,
		OriginalEvidenceReferences:  refs,
		OriginalScenarioHash:        scenarioHash,
		CycleNumber:                 incident.CycleNumber,
		ExpectedFixCommits:          CloneStringMap(expected),
		DeploymentObservationID:     observation.ID,
		DeploymentReservationID:     reservation.ReservationID,
		ObservedDeploymentVersion:   observation.ObservedVersion,
		TargetEnvironment:           incident.Environment,
	}, reservation, nil
}

func observedCommitsCoverExpected(observation DeploymentObservation, expected map[string]string) bool {
	for repo, commit := range expected {
		if observation.ObservedCommits[repo] == commit || observation.VerifiedCommitAncestors[repo] == commit {
			continue
		}
		return false
	}
	return true
}

func (o *CaseOrchestrator) expectedRegressionCommits(ctx context.Context, incident IncidentCase) (map[string]string, error) {
	approvals, err := o.store.ListApprovals(ctx, incident.ID)
	if err != nil {
		return nil, err
	}
	var scope MergeApprovalScope
	found := false
	for index := len(approvals) - 1; index >= 0; index-- {
		if approvals[index].Kind != ApprovalMergeEnvironmentBranch {
			continue
		}
		var candidate MergeApprovalScope
		if json.Unmarshal(approvals[index].ScopeJSON, &candidate) == nil && candidate.CycleNumber == incident.CycleNumber {
			scope, found = candidate, true
			break
		}
	}
	if !found {
		return nil, ErrApprovalScope
	}
	changes, err := o.store.ListCodeChanges(ctx, incident.ID)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]CodeChange, len(changes))
	for _, change := range changes {
		byID[change.ID] = change
	}
	expected := make(map[string]string, len(scope.CodeChanges))
	for _, approved := range scope.CodeChanges {
		change, ok := byID[approved.ID]
		if !ok || change.AttemptID != scope.FixAttemptID || change.Repo != approved.Repo || change.FixCommit != approved.FixCommit || change.TargetEnvironmentBranch != approved.TargetBranch || change.PushStatus != "pushed" || change.MergeCommit == "" {
			return nil, ErrApprovalScope
		}
		expected[change.Repo] = change.MergeCommit
	}
	if len(expected) == 0 || len(expected) != len(scope.CodeChanges) {
		return nil, ErrApprovalScope
	}
	return expected, nil
}

func (o *CaseOrchestrator) originalValidation(ctx context.Context, caseID string) (PhaseAttempt, ValidationResult, error) {
	attempts, err := o.store.ListAttempts(ctx, AttemptFilter{CaseID: caseID})
	if err != nil {
		return PhaseAttempt{}, ValidationResult{}, err
	}
	for _, attempt := range attempts {
		if attempt.Phase != PhaseValidation || attempt.Mode != AttemptReproduce || attempt.Status != AttemptStatusSucceeded {
			continue
		}
		var result ValidationResult
		if json.Unmarshal(attempt.OutputJSON, &result) != nil || result.VerificationStatus != "reproduced" || strings.TrimSpace(result.ExpectedBehavior) == "" || strings.TrimSpace(result.ObservedBehavior) == "" {
			continue
		}
		return attempt, result, nil
	}
	return PhaseAttempt{}, ValidationResult{}, errors.New("a successful original reproduction with expected behavior is required")
}

func deterministicOriginalScenario(attempt PhaseAttempt, result ValidationResult) (string, string, error) {
	var scenario any
	if err := json.Unmarshal(attempt.InputJSON, &scenario); err != nil {
		return "", "", err
	}
	canonical, err := json.Marshal(scenario)
	if err != nil {
		return "", "", err
	}
	if containsSensitiveData(canonical) {
		return "", "", errors.New("original reproduction contains sensitive data and must be redacted before regression")
	}
	reproduction := string(canonical)
	material := strings.Join([]string{reproduction, result.ExpectedBehavior, result.ObservedBehavior}, "\x1f")
	digest := sha256.Sum256([]byte(material))
	return reproduction, hex.EncodeToString(digest[:]), nil
}

func (o *CaseOrchestrator) validateRegressionCompletion(ctx context.Context, incident IncidentCase, attempt PhaseAttempt, cmd CompleteAttemptCommand) error {
	if attempt.Phase != PhaseRegression {
		return nil
	}
	var input RegressionValidationInput
	if err := json.Unmarshal(attempt.InputJSON, &input); err != nil {
		return errors.Join(ErrRegressionBinding, err)
	}
	if err := o.validatePersistedRegressionBinding(ctx, incident, input); err != nil {
		return err
	}
	if cmd.ErrorCode != "" {
		if cmd.Outcome != PhaseOutcomeNeedsEvidence {
			return errors.New("failed regression completion must require evidence")
		}
		return nil
	}
	var result ValidationResult
	if err := json.Unmarshal(cmd.OutputJSON, &result); err != nil {
		return err
	}
	expectedStatus := map[PhaseOutcome]string{PhaseOutcomeFixedVerified: "fixed_verified", PhaseOutcomeStillReproduces: "still_reproduces", PhaseOutcomeNeedsEvidence: "insufficient_info"}[cmd.Outcome]
	if expectedStatus == "" || result.VerificationStatus != expectedStatus || result.Environment != input.TargetEnvironment || result.ScenarioHash != input.OriginalScenarioHash {
		return ErrRegressionBinding
	}
	if cmd.Outcome == PhaseOutcomeNeedsEvidence {
		return nil
	}
	artifacts, err := o.currentRegressionArtifacts(ctx, attempt, input)
	if err != nil {
		return err
	}
	if len(artifacts) == 0 {
		return ErrRegressionFreshEvidence
	}
	if cmd.Outcome == PhaseOutcomeStillReproduces && strings.TrimSpace(result.ObservedBehavior) == "" {
		return errors.New("still_reproduces requires a delta observation")
	}
	return nil
}

func (o *CaseOrchestrator) validatePersistedRegressionBinding(ctx context.Context, incident IncidentCase, input RegressionValidationInput) error {
	if input.CycleNumber != incident.CycleNumber || input.TargetEnvironment != incident.Environment {
		return ErrRegressionBinding
	}
	expected, err := o.expectedRegressionCommits(ctx, incident)
	if err != nil || !equalStringMap(expected, input.ExpectedFixCommits) {
		return errors.Join(ErrRegressionBinding, err)
	}
	reservationEvent, found, err := o.store.latestDeploymentReservationEvent(ctx, incident.ID)
	if err != nil || !found {
		return errors.Join(ErrRegressionBinding, err)
	}
	var reservation DeploymentReservation
	if json.Unmarshal(reservationEvent.PayloadJSON, &reservation) != nil || validateDeploymentReservationIdentity(reservation, reservationEvent.IdempotencyKey, reservation.CallerIdempotencyKey, reservationEvent.ActorID) != nil || reservation.ReservationID != input.DeploymentReservationID || reservation.CycleNumber != input.CycleNumber || reservation.Environment != input.TargetEnvironment || !equalStringMap(reservation.ExpectedCommits, expected) {
		return ErrRegressionBinding
	}
	observations, err := o.store.ListDeploymentObservations(ctx, incident.ID)
	if err != nil {
		return err
	}
	for _, observation := range observations {
		if observation.ID == input.DeploymentObservationID && observation.ID == stableID("deployment", reservation.ReservationKey) && observation.Result == DeploymentResultMatched && observation.VerifiedAt != nil && observation.Environment == input.TargetEnvironment && observation.ObservedVersion == input.ObservedDeploymentVersion && equalStringMap(observation.ExpectedCommits, expected) && observedCommitsCoverExpected(observation, expected) {
			return nil
		}
	}
	return ErrRegressionBinding
}

func (o *CaseOrchestrator) currentRegressionArtifacts(ctx context.Context, attempt PhaseAttempt, input RegressionValidationInput) ([]EvidenceArtifact, error) {
	all, err := o.store.ListEvidenceArtifacts(ctx, attempt.CaseID)
	if err != nil {
		return nil, err
	}
	current := make([]EvidenceArtifact, 0)
	for _, artifact := range all {
		if artifact.AttemptID != attempt.ID || !artifact.CapturedAt.After(attempt.StartedAt) || artifact.Environment != input.TargetEnvironment || artifact.Version != input.ObservedDeploymentVersion || (strings.TrimSpace(artifact.RequestID) == "" && strings.TrimSpace(artifact.TraceID) == "") {
			continue
		}
		current = append(current, artifact)
	}
	return current, nil
}

func (o *CaseOrchestrator) buildNextCycleInvestigationInput(ctx context.Context, attempt PhaseAttempt, output json.RawMessage) (json.RawMessage, error) {
	var regression RegressionValidationInput
	var result ValidationResult
	if err := json.Unmarshal(attempt.InputJSON, &regression); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(output, &result); err != nil {
		return nil, err
	}
	artifacts, err := o.currentRegressionArtifacts(ctx, attempt, regression)
	if err != nil {
		return nil, err
	}
	refs := make([]string, 0, len(artifacts))
	for _, artifact := range artifacts {
		refs = append(refs, artifact.ID+":"+artifact.SHA256)
	}
	sort.Strings(refs)
	delta := fmt.Sprintf("original observation remains unexplained: %s; regression observation after deployed fix: %s", regression.OriginalObservedBehavior, result.ObservedBehavior)
	input := NextCycleInvestigationInput{PreviousCycle: attempt.CycleNumber, RegressionAttemptID: attempt.ID, ScenarioHash: regression.OriginalScenarioHash, ObservedDeploymentVersion: regression.ObservedDeploymentVersion, RegressionEvidenceReferences: refs, Delta: delta}
	return json.Marshal(input)
}
