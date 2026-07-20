package bughub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

type regressionContinuationIdentity struct {
	CaseID          string          `json:"case_id"`
	ExpectedVersion int64           `json:"expected_version"`
	IdempotencyKey  string          `json:"idempotency_key"`
	ActorID         string          `json:"actor_id"`
	Phase           Phase           `json:"phase"`
	InputJSON       json.RawMessage `json:"input_json"`
	Bug             Bug             `json:"bug"`
	Bot             BotRef          `json:"bot"`
}

func regressionContinuationIdentityDigest(command ContinueWithEvidenceCommand) (string, error) {
	input, err := canonicalJSONObject(command.InputJSON)
	if err != nil {
		return "", err
	}
	identity := regressionContinuationIdentity{CaseID: command.CaseID, ExpectedVersion: command.ExpectedVersion, IdempotencyKey: command.IdempotencyKey, ActorID: command.ActorID, Phase: command.Phase, InputJSON: input, Bug: canonicalBug(command.Bug), Bot: canonicalBot(command.Bot)}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalJSONObject(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		raw = []byte(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, errors.New("continuation input must be one JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("continuation input must be one JSON object")
	}
	return json.Marshal(object)
}

func canonicalBug(value Bug) Bug {
	cloned := value
	cloned.CreatedAt = canonicalTime(value.CreatedAt)
	cloned.UpdatedAt = canonicalTime(value.UpdatedAt)
	cloned.LastContextAt = canonicalTime(value.LastContextAt)
	cloned.ServiceHints = sortedStrings(value.ServiceHints)
	cloned.APIPaths = sortedStrings(value.APIPaths)
	cloned.TraceIDs = sortedStrings(value.TraceIDs)
	cloned.RequestIDs = sortedStrings(value.RequestIDs)
	cloned.Attachments = append([]Attachment(nil), value.Attachments...)
	sort.Slice(cloned.Attachments, func(i, j int) bool {
		left, _ := json.Marshal(cloned.Attachments[i])
		right, _ := json.Marshal(cloned.Attachments[j])
		return bytes.Compare(left, right) < 0
	})
	return cloned
}

func canonicalBot(value BotRef) BotRef {
	cloned := value
	cloned.Envs = sortedStrings(value.Envs)
	cloned.InternalAgents = append([]BotInternalAgent(nil), value.InternalAgents...)
	sort.Slice(cloned.InternalAgents, func(i, j int) bool {
		if cloned.InternalAgents[i].ID != cloned.InternalAgents[j].ID {
			return cloned.InternalAgents[i].ID < cloned.InternalAgents[j].ID
		}
		return cloned.InternalAgents[i].Role < cloned.InternalAgents[j].Role
	})
	return cloned
}

func sortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	cloned := append([]string(nil), values...)
	sort.Strings(cloned)
	return cloned
}

func canonicalTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Time{}
	}
	return value.UTC()
}

var (
	ErrRegressionDuplicate        = errors.New("identical regression scenario and deployment already exists")
	ErrRegressionBinding          = errors.New("regression is not bound to the current verified deployment")
	ErrRegressionFreshEvidence    = errors.New("regression requires fresh current-attempt evidence")
	ErrRegressionOriginalScenario = errors.New("regression original validation scenario is incomplete")
	ErrRegressionOriginalEvidence = errors.New("regression original validation evidence is missing")
)

type NextCycleInvestigationInput struct {
	PreviousCycle                int                              `json:"previous_cycle"`
	RegressionAttemptID          string                           `json:"regression_attempt_id"`
	ScenarioHash                 string                           `json:"scenario_hash"`
	ObservedDeploymentVersion    string                           `json:"observed_deployment_version"`
	RegressionEvidenceReferences []InvestigationEvidenceReference `json:"regression_evidence_refs"`
	Delta                        string                           `json:"delta"`
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
	if incident.Status != CaseDeploymentVerified && incident.Status != CaseRemediationApplied {
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
	persisted, err := o.store.GetAttempt(ctx, attempt.ID)
	if err != nil {
		return PhaseAttempt{}, err
	}
	return persisted, nil
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
	if len(expected) == 0 {
		scope, scopeErr := o.currentRemediationScope(ctx, incident)
		if scopeErr != nil || reservation.RemediationBindingID != scope.BindingID || reservation.RemediationType != scope.RootCauseType || reservation.RemediationSummary != scope.Summary {
			return RegressionValidationInput{}, DeploymentReservation{}, errors.Join(ErrRegressionBinding, scopeErr)
		}
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
	if observation.ID == "" || observation.Environment != incident.Environment || !equalStringMap(observation.ExpectedCommits, expected) || !deploymentObservationAllowsRegression(observation, expected) {
		return RegressionValidationInput{}, DeploymentReservation{}, ErrRegressionBinding
	}
	original, validation, refs, err := o.originalValidation(ctx, incident.ID)
	if err != nil {
		return RegressionValidationInput{}, DeploymentReservation{}, err
	}
	reproduction, scenarioHash, err := deterministicOriginalScenario(original, validation)
	if err != nil {
		return RegressionValidationInput{}, DeploymentReservation{}, fmt.Errorf("%w: %v", ErrRegressionOriginalScenario, err)
	}
	if len(refs) == 0 {
		return RegressionValidationInput{}, DeploymentReservation{}, ErrRegressionOriginalEvidence
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
		RemediationBindingID:        reservation.RemediationBindingID,
		RemediationType:             reservation.RemediationType,
		RemediationSummary:          reservation.RemediationSummary,
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

func deploymentObservationAllowsRegression(observation DeploymentObservation, expected map[string]string) bool {
	switch observation.Result {
	case DeploymentResultMatched:
		return observation.VerifiedAt != nil && !observation.VerifiedAt.IsZero() && strings.TrimSpace(observation.ObservedVersion) != "" && observedCommitsCoverExpected(observation, expected)
	case DeploymentResultUnavailable:
		// Deployment was confirmed by the operator, but the runtime exposed no
		// trustworthy version identity. Regression is the final business proof.
		return true
	default:
		return false
	}
}

func (o *CaseOrchestrator) expectedRegressionCommits(ctx context.Context, incident IncidentCase) (map[string]string, error) {
	approvals, err := o.store.ListApprovals(ctx, incident.ID)
	if err != nil {
		return nil, err
	}
	for index := len(approvals) - 1; index >= 0; index-- {
		if approvals[index].Kind != ApprovalCompleteRemediation {
			continue
		}
		var remediation RemediationApprovalScope
		if json.Unmarshal(approvals[index].ScopeJSON, &remediation) == nil && remediation.CycleNumber == incident.CycleNumber && remediation.BindingID != "" {
			return map[string]string{}, nil
		}
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

func (o *CaseOrchestrator) currentRemediationScope(ctx context.Context, incident IncidentCase) (RemediationApprovalScope, error) {
	approvals, err := o.store.ListApprovals(ctx, incident.ID)
	if err != nil {
		return RemediationApprovalScope{}, err
	}
	for index := len(approvals) - 1; index >= 0; index-- {
		if approvals[index].Kind != ApprovalCompleteRemediation {
			continue
		}
		var scope RemediationApprovalScope
		if json.Unmarshal(approvals[index].ScopeJSON, &scope) == nil && scope.CycleNumber == incident.CycleNumber && scope.BindingID != "" {
			return scope, nil
		}
	}
	return RemediationApprovalScope{}, ErrApprovalScope
}

func (o *CaseOrchestrator) originalValidation(ctx context.Context, caseID string) (PhaseAttempt, ValidationResult, []string, error) {
	attempts, err := o.store.ListAttempts(ctx, AttemptFilter{CaseID: caseID})
	if err != nil {
		return PhaseAttempt{}, ValidationResult{}, nil, err
	}
	artifacts, err := o.store.ListEvidenceArtifacts(ctx, caseID)
	if err != nil {
		return PhaseAttempt{}, ValidationResult{}, nil, err
	}
	references := make(map[string][]string)
	for _, artifact := range artifacts {
		references[artifact.AttemptID] = append(references[artifact.AttemptID], artifact.ID+":"+artifact.SHA256)
	}
	sawCompleteScenario := false
	for _, attempt := range attempts {
		if attempt.Phase != PhaseValidation || attempt.Mode != AttemptReproduce || attempt.Status != AttemptStatusSucceeded {
			continue
		}
		var result ValidationResult
		if json.Unmarshal(attempt.OutputJSON, &result) != nil || result.VerificationStatus != "reproduced" || strings.TrimSpace(result.ExpectedBehavior) == "" || strings.TrimSpace(result.ObservedBehavior) == "" {
			continue
		}
		if _, _, scenarioErr := deterministicOriginalScenario(attempt, result); scenarioErr != nil {
			continue
		}
		sawCompleteScenario = true
		refs := references[attempt.ID]
		if len(refs) == 0 {
			continue
		}
		sort.Strings(refs)
		return attempt, result, refs, nil
	}
	if sawCompleteScenario {
		return PhaseAttempt{}, ValidationResult{}, nil, ErrRegressionOriginalEvidence
	}
	return PhaseAttempt{}, ValidationResult{}, nil, ErrRegressionOriginalScenario
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
		if cmd.Outcome != PhaseOutcomeNeedsEvidence && cmd.Outcome != PhaseOutcomeSystemFailed {
			return errors.New("failed regression completion must require evidence or report a system failure")
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
	if json.Unmarshal(reservationEvent.PayloadJSON, &reservation) != nil || validateDeploymentReservationIdentity(reservation, reservationEvent.IdempotencyKey, reservation.CallerIdempotencyKey, reservationEvent.ActorID) != nil || reservation.ReservationID != input.DeploymentReservationID || reservation.CycleNumber != input.CycleNumber || reservation.Environment != input.TargetEnvironment || !equalStringMap(reservation.ExpectedCommits, expected) || reservation.RemediationBindingID != input.RemediationBindingID || reservation.RemediationType != input.RemediationType || reservation.RemediationSummary != input.RemediationSummary {
		return ErrRegressionBinding
	}
	if len(expected) == 0 {
		scope, scopeErr := o.currentRemediationScope(ctx, incident)
		if scopeErr != nil || scope.BindingID != input.RemediationBindingID || scope.RootCauseType != input.RemediationType || scope.Summary != input.RemediationSummary {
			return errors.Join(ErrRegressionBinding, scopeErr)
		}
	}
	observations, err := o.store.ListDeploymentObservations(ctx, incident.ID)
	if err != nil {
		return err
	}
	for _, observation := range observations {
		if observation.ID == input.DeploymentObservationID && observation.ID == stableID("deployment", reservation.ReservationKey) && observation.Environment == input.TargetEnvironment && observation.ObservedVersion == input.ObservedDeploymentVersion && equalStringMap(observation.ExpectedCommits, expected) && deploymentObservationAllowsRegression(observation, expected) {
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
	historicalIDs := make(map[string]struct{})
	for _, artifact := range all {
		if artifact.AttemptID == attempt.ID {
			continue
		}
		for _, identifier := range []string{artifact.RequestID, artifact.TraceID} {
			if identifier = strings.TrimSpace(identifier); identifier != "" {
				historicalIDs[identifier] = struct{}{}
			}
		}
	}
	current := make([]EvidenceArtifact, 0)
	for _, artifact := range all {
		if artifact.AttemptID != attempt.ID || !artifact.CapturedAt.After(attempt.StartedAt) || artifact.Environment != input.TargetEnvironment || (input.ObservedDeploymentVersion != "" && artifact.Version != input.ObservedDeploymentVersion) || (strings.TrimSpace(artifact.RequestID) == "" && strings.TrimSpace(artifact.TraceID) == "") {
			continue
		}
		for _, identifier := range []string{artifact.RequestID, artifact.TraceID} {
			if identifier = strings.TrimSpace(identifier); identifier != "" {
				if _, reused := historicalIDs[identifier]; reused {
					return nil, ErrRegressionFreshEvidence
				}
			}
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
	refs := make([]InvestigationEvidenceReference, 0, len(artifacts))
	for _, artifact := range artifacts {
		refs = append(refs, InvestigationEvidenceReference{
			ArtifactID: artifact.ID, Kind: artifact.Kind, SHA256: artifact.SHA256,
			Environment: artifact.Environment, Version: artifact.Version,
			RequestID: artifact.RequestID, TraceID: artifact.TraceID,
		})
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Kind != refs[j].Kind {
			return refs[i].Kind < refs[j].Kind
		}
		return refs[i].ArtifactID < refs[j].ArtifactID
	})
	delta := fmt.Sprintf("original observation remains unexplained: %s; regression observation after deployed fix: %s", regression.OriginalObservedBehavior, result.ObservedBehavior)
	input := NextCycleInvestigationInput{PreviousCycle: attempt.CycleNumber, RegressionAttemptID: attempt.ID, ScenarioHash: regression.OriginalScenarioHash, ObservedDeploymentVersion: regression.ObservedDeploymentVersion, RegressionEvidenceReferences: refs, Delta: delta}
	return json.Marshal(input)
}
