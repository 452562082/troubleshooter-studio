package bughub

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestCanTransition(t *testing.T) {
	allowedEdges := [][2]CaseStatus{
		{CasePendingValidation, CaseValidating},
		{CaseValidating, CaseReproduced},
		{CaseValidating, CaseWaitingEvidence},
		{CaseValidating, CaseNotReproduced},
		{CaseWaitingEvidence, CaseValidating},
		{CaseWaitingEvidence, CaseInvestigating},
		{CaseWaitingEvidence, CaseRegressionValidating},
		{CaseReproduced, CaseInvestigating},
		{CaseNotReproduced, CaseValidating},
		{CaseInvestigating, CaseRootCauseReady},
		{CaseInvestigating, CaseWaitingEvidence},
		{CaseRootCauseReady, CaseWaitingFixApproval},
		{CaseWaitingFixApproval, CaseFixing},
		{CaseFixing, CaseFixPushed},
		{CaseFixing, CaseFixFailed},
		{CaseFixFailed, CaseFixing},
		{CaseFixPushed, CaseWaitingMergeApproval},
		{CaseWaitingMergeApproval, CaseMerging},
		{CaseMerging, CaseWaitingDeployment},
		{CaseMerging, CaseMergeConflict},
		{CaseMerging, CaseWaitingMergeApproval},
		{CaseMergeConflict, CaseWaitingMergeApproval},
		{CaseWaitingDeployment, CaseDeploymentVerified},
		{CaseWaitingDeployment, CaseDeploymentUnverified},
		{CaseDeploymentUnverified, CaseWaitingDeployment},
		{CaseDeploymentVerified, CaseRegressionValidating},
		{CaseDeploymentVerified, CaseWaitingEvidence},
		{CaseRegressionValidating, CaseFixedVerified},
		{CaseRegressionValidating, CaseStillReproduces},
		{CaseRegressionValidating, CaseWaitingEvidence},
		{CaseStillReproduces, CaseInvestigating},
	}
	allowed := make(map[[2]CaseStatus]struct{}, len(allowedEdges))
	for _, edge := range allowedEdges {
		allowed[edge] = struct{}{}
	}
	statuses := []CaseStatus{
		CasePendingValidation,
		CaseValidating,
		CaseWaitingEvidence,
		CaseReproduced,
		CaseNotReproduced,
		CaseInvestigating,
		CaseRootCauseReady,
		CaseWaitingFixApproval,
		CaseFixing,
		CaseFixFailed,
		CaseFixPushed,
		CaseWaitingMergeApproval,
		CaseMerging,
		CaseMergeConflict,
		CaseWaitingDeployment,
		CaseDeploymentUnverified,
		CaseDeploymentVerified,
		CaseRegressionValidating,
		CaseFixedVerified,
		CaseStillReproduces,
		CaseLegacyArchived,
		CaseResetArchived,
		CaseStatus("unknown"),
	}
	for _, from := range statuses {
		if from.valid() && !IsTerminalCaseStatus(from) {
			allowed[[2]CaseStatus{from, CaseResetArchived}] = struct{}{}
		}
	}
	for _, from := range statuses {
		for _, to := range statuses {
			_, want := allowed[[2]CaseStatus{from, to}]
			if got := CanTransition(from, to); got != want {
				t.Fatalf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
		}
	}
}

func TestResetArchiveTransitions(t *testing.T) {
	for _, from := range []CaseStatus{
		CasePendingValidation,
		CaseValidating,
		CaseWaitingEvidence,
		CaseInvestigating,
		CaseWaitingFixApproval,
		CaseFixing,
		CaseWaitingMergeApproval,
		CaseMerging,
		CaseWaitingDeployment,
		CaseRegressionValidating,
	} {
		if !CanTransition(from, CaseResetArchived) {
			t.Fatalf("%s must reset", from)
		}
	}
	for _, from := range []CaseStatus{CaseFixedVerified, CaseLegacyArchived, CaseResetArchived} {
		if CanTransition(from, CaseResetArchived) {
			t.Fatalf("terminal %s reset unexpectedly", from)
		}
		for _, to := range []CaseStatus{CasePendingValidation, CaseInvestigating, CaseResetArchived} {
			if CanTransition(from, to) {
				t.Fatalf("terminal %s transitioned to %s unexpectedly", from, to)
			}
		}
	}
}

func TestIsTerminalCaseStatus(t *testing.T) {
	for _, status := range []CaseStatus{CaseFixedVerified, CaseLegacyArchived, CaseResetArchived} {
		if !IsTerminalCaseStatus(status) {
			t.Fatalf("%s must be terminal", status)
		}
	}
	for _, status := range []CaseStatus{CasePendingValidation, CaseStillReproduces, CaseStatus("unknown")} {
		if IsTerminalCaseStatus(status) {
			t.Fatalf("%s must not be terminal", status)
		}
	}
}

func TestValidateWorkflow(t *testing.T) {
	t.Run("valid transition", func(t *testing.T) {
		incident := IncidentCase{
			ID:          "case-1",
			BugID:       "zentao-909",
			Status:      CasePendingValidation,
			CycleNumber: 1,
		}
		if err := ValidateTransition(incident, CaseValidating); err != nil {
			t.Fatalf("ValidateTransition() error = %v", err)
		}
	})

	t.Run("legacy cases are immutable", func(t *testing.T) {
		incident := IncidentCase{
			ID:          "legacy-1",
			BugID:       "zentao-808",
			Status:      CaseLegacyArchived,
			CycleNumber: 1,
		}
		err := ValidateTransition(incident, CaseInvestigating)
		var invalid *ErrInvalidTransition
		if !errors.As(err, &invalid) {
			t.Fatalf("error = %v, want *ErrInvalidTransition", err)
		}
	})

	for _, tc := range []struct {
		name     string
		incident IncidentCase
	}{
		{
			name:     "case ID is required",
			incident: IncidentCase{BugID: "zentao-909", Status: CasePendingValidation, CycleNumber: 1},
		},
		{
			name:     "case ID cannot be whitespace",
			incident: IncidentCase{ID: " ", BugID: "zentao-909", Status: CasePendingValidation, CycleNumber: 1},
		},
		{
			name:     "bug ID is required",
			incident: IncidentCase{ID: "case-1", Status: CasePendingValidation, CycleNumber: 1},
		},
		{
			name:     "positive cycle number is required",
			incident: IncidentCase{ID: "case-1", BugID: "zentao-909", Status: CasePendingValidation},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.incident.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want validation error")
			}
		})
	}

	t.Run("attempt IDs, cycle, and usage are valid", func(t *testing.T) {
		valid := PhaseAttempt{
			ID:          "attempt-1",
			CaseID:      "case-1",
			CycleNumber: 1,
			Phase:       PhaseValidation,
			Mode:        AttemptReproduce,
			Status:      AttemptStatusRunning,
			InputJSON:   json.RawMessage(`{}`),
			OutputJSON:  json.RawMessage(`{}`),
		}
		if err := valid.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		invalid := []PhaseAttempt{
			{CaseID: "case-1", CycleNumber: 1},
			{ID: " ", CaseID: "case-1", CycleNumber: 1},
			{ID: "attempt-1", CycleNumber: 1},
			{ID: "attempt-1", CaseID: "case-1"},
			{ID: "attempt-1", CaseID: "case-1", CycleNumber: 1, Usage: AgentUsage{InputTokens: -1}},
			{ID: "attempt-1", CaseID: "case-1", CycleNumber: 1, Usage: AgentUsage{OutputTokens: -1}},
			{ID: "attempt-1", CaseID: "case-1", CycleNumber: 1, Usage: AgentUsage{Duration: -1}},
		}
		for _, attempt := range invalid {
			if err := attempt.Validate(); err == nil {
				t.Fatalf("Validate(%+v) error = nil, want validation error", attempt)
			}
		}
	})

	t.Run("approval scope is required and valid JSON", func(t *testing.T) {
		valid := Approval{
			ID:          "approval-1",
			CaseID:      "case-1",
			Kind:        ApprovalStartFix,
			Actor:       "user-1",
			CaseVersion: 1,
			ScopeJSON:   json.RawMessage(`{"root_cause_attempt_id":"attempt-root"}`),
		}
		if err := valid.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		for _, approval := range []Approval{
			{CaseID: "case-1", CaseVersion: 1, ScopeJSON: json.RawMessage(`{}`)},
			{ID: "approval-1", CaseVersion: 1, ScopeJSON: json.RawMessage(`{}`)},
			{ID: "approval-1", CaseID: "case-1", ScopeJSON: json.RawMessage(`{}`)},
			{ID: "approval-1", CaseID: "case-1", CaseVersion: 1},
			{ID: "approval-1", CaseID: "case-1", CaseVersion: 1, ScopeJSON: json.RawMessage(`{`)},
		} {
			if err := approval.Validate(); err == nil {
				t.Fatalf("Validate(%+v) error = nil, want validation error", approval)
			}
		}
	})
}

func TestValidateWorkflowCaseStatusMembership(t *testing.T) {
	base := IncidentCase{ID: "case-1", BugID: "zentao-909", CycleNumber: 1}
	for _, status := range []CaseStatus{
		CasePendingValidation,
		CaseValidating,
		CaseWaitingEvidence,
		CaseReproduced,
		CaseNotReproduced,
		CaseInvestigating,
		CaseRootCauseReady,
		CaseWaitingFixApproval,
		CaseFixing,
		CaseFixFailed,
		CaseFixPushed,
		CaseWaitingMergeApproval,
		CaseMerging,
		CaseMergeConflict,
		CaseWaitingDeployment,
		CaseDeploymentUnverified,
		CaseDeploymentVerified,
		CaseRegressionValidating,
		CaseFixedVerified,
		CaseStillReproduces,
		CaseLegacyArchived,
		CaseResetArchived,
	} {
		incident := base
		incident.Status = status
		if err := incident.Validate(); err != nil {
			t.Fatalf("status %q Validate() error = %v", status, err)
		}
	}
	for _, status := range []CaseStatus{"", "unknown"} {
		incident := base
		incident.Status = status
		if err := incident.Validate(); err == nil {
			t.Fatalf("status %q Validate() error = nil", status)
		}
	}
}

func TestValidateWorkflowApprovalKindsAndScopes(t *testing.T) {
	base := Approval{
		ID:          "approval-1",
		CaseID:      "case-1",
		Actor:       "user-1",
		CaseVersion: 2,
	}

	startFix := base
	startFix.Kind = ApprovalStartFix
	startFix.ScopeJSON = json.RawMessage(`{"root_cause_attempt_id":"attempt-root"}`)
	if err := startFix.Validate(); err != nil {
		t.Fatalf("start-fix Validate() error = %v", err)
	}

	merge := base
	merge.Kind = ApprovalMergeEnvironmentBranch
	merge.ScopeJSON = json.RawMessage(`{"environment":"test"}`)
	merge.FixCommits = map[string]string{"api": "abc123"}
	merge.TargetBranches = map[string]string{"api": "env/test"}
	if err := merge.Validate(); err != nil {
		t.Fatalf("merge Validate() error = %v", err)
	}

	invalid := []Approval{
		base,
		func() Approval {
			a := base
			a.Kind = ApprovalKind("unknown")
			a.ScopeJSON = json.RawMessage(`{}`)
			return a
		}(),
		func() Approval { a := startFix; a.ScopeJSON = json.RawMessage(`{}`); return a }(),
		func() Approval { a := startFix; a.Actor = ""; return a }(),
		func() Approval { a := startFix; a.Actor = " "; return a }(),
		func() Approval {
			a := startFix
			a.ScopeJSON = json.RawMessage(`{"root_cause_attempt_id":" "}`)
			return a
		}(),
		func() Approval { a := merge; a.FixCommits = nil; return a }(),
		func() Approval { a := merge; a.TargetBranches = nil; return a }(),
		func() Approval { a := merge; a.TargetBranches = map[string]string{"worker": "env/test"}; return a }(),
	}
	for _, approval := range invalid {
		if err := approval.Validate(); err == nil {
			t.Fatalf("Validate(%+v) error = nil, want scope validation error", approval)
		}
	}
}

func TestValidateWorkflowAttemptContract(t *testing.T) {
	base := PhaseAttempt{
		ID:          "attempt-1",
		CaseID:      "case-1",
		CycleNumber: 1,
		Status:      AttemptStatusInterrupted,
		InputJSON:   json.RawMessage(`{}`),
		OutputJSON:  json.RawMessage(`{}`),
	}
	valid := []PhaseAttempt{
		func() PhaseAttempt { a := base; a.Phase = PhaseValidation; a.Mode = AttemptReproduce; return a }(),
		func() PhaseAttempt { a := base; a.Phase = PhaseRegression; a.Mode = AttemptRegression; return a }(),
		func() PhaseAttempt { a := base; a.Phase = PhaseInvestigation; return a }(),
		func() PhaseAttempt { a := base; a.Phase = PhaseFix; return a }(),
	}
	for _, attempt := range valid {
		if err := attempt.Validate(); err != nil {
			t.Fatalf("Validate(%+v) error = %v", attempt, err)
		}
	}
	for _, status := range []AttemptStatus{
		AttemptStatusQueued,
		AttemptStatusRunning,
		AttemptStatusSucceeded,
		AttemptStatusFailed,
		AttemptStatusCancelled,
		AttemptStatusInterrupted,
	} {
		attempt := base
		attempt.Phase = PhaseFix
		attempt.Status = status
		if err := attempt.Validate(); err != nil {
			t.Fatalf("status %q Validate() error = %v", status, err)
		}
	}

	legacy := base
	legacy.Phase = PhaseLegacy
	if err := legacy.Validate(); err == nil {
		t.Fatal("legacy Validate() error = nil, want migration-only error")
	}
	if err := legacy.ValidateWithOptions(AttemptValidationOptions{AllowLegacyMigration: true}); err != nil {
		t.Fatalf("legacy migration ValidateWithOptions() error = %v", err)
	}

	invalid := []PhaseAttempt{
		func() PhaseAttempt { a := base; a.Phase = Phase("unknown"); return a }(),
		func() PhaseAttempt { a := base; a.Phase = PhaseValidation; return a }(),
		func() PhaseAttempt { a := base; a.Phase = PhaseValidation; a.Mode = AttemptRegression; return a }(),
		func() PhaseAttempt { a := base; a.Phase = PhaseRegression; a.Mode = AttemptReproduce; return a }(),
		func() PhaseAttempt { a := base; a.Phase = PhaseInvestigation; a.Mode = AttemptReproduce; return a }(),
		func() PhaseAttempt { a := base; a.Phase = PhaseFix; a.Mode = AttemptRegression; return a }(),
		func() PhaseAttempt { a := base; a.Phase = PhaseFix; a.Status = AttemptStatus("unknown"); return a }(),
	}
	for _, attempt := range invalid {
		if err := attempt.Validate(); err == nil {
			t.Fatalf("Validate(%+v) error = nil, want contract validation error", attempt)
		}
	}

	for _, payload := range []json.RawMessage{
		nil,
		json.RawMessage{},
		json.RawMessage(`{`),
		json.RawMessage(`null`),
		json.RawMessage(`[]`),
		json.RawMessage(`"text"`),
		json.RawMessage(`1`),
	} {
		invalidInput := base
		invalidInput.Phase = PhaseFix
		invalidInput.InputJSON = payload
		if err := invalidInput.Validate(); err == nil {
			t.Fatalf("InputJSON %q Validate() error = nil", payload)
		}
		invalidOutput := base
		invalidOutput.Phase = PhaseFix
		invalidOutput.OutputJSON = payload
		if err := invalidOutput.Validate(); err == nil {
			t.Fatalf("OutputJSON %q Validate() error = nil", payload)
		}
		invalidLegacy := legacy
		invalidLegacy.InputJSON = payload
		if err := invalidLegacy.ValidateWithOptions(AttemptValidationOptions{AllowLegacyMigration: true}); err == nil {
			t.Fatalf("legacy InputJSON %q migration validation error = nil", payload)
		}
		invalidLegacy = legacy
		invalidLegacy.OutputJSON = payload
		if err := invalidLegacy.ValidateWithOptions(AttemptValidationOptions{AllowLegacyMigration: true}); err == nil {
			t.Fatalf("legacy OutputJSON %q migration validation error = nil", payload)
		}
	}

	for _, status := range []InvestigationStatus{
		InvestigationQueued,
		InvestigationRunning,
		InvestigationSucceeded,
		InvestigationFailed,
		InvestigationCancelled,
	} {
		got, err := AttemptStatusFromInvestigationStatus(status)
		if err != nil || InvestigationStatus(got) != status {
			t.Fatalf("AttemptStatusFromInvestigationStatus(%q) = %q, %v", status, got, err)
		}
		roundTrip, err := got.InvestigationStatus()
		if err != nil || roundTrip != status {
			t.Fatalf("AttemptStatus(%q).InvestigationStatus() = %q, %v", got, roundTrip, err)
		}
	}
	if _, err := AttemptStatusFromInvestigationStatus(InvestigationStatus("unknown")); err == nil {
		t.Fatal("unknown InvestigationStatus conversion error = nil")
	}
	if _, err := AttemptStatusInterrupted.InvestigationStatus(); err == nil {
		t.Fatal("interrupted InvestigationStatus conversion error = nil")
	}
}

func TestValidateWorkflowPersistedRecords(t *testing.T) {
	sha := strings.Repeat("a", 64)
	evidence := EvidenceArtifact{
		ID:              "evidence-1",
		CaseID:          "case-1",
		AttemptID:       "attempt-1",
		Kind:            "screenshot",
		PathOrReference: "artifacts/screenshot.png",
		SHA256:          sha,
		RedactionStatus: RedactionStatusRedacted,
	}
	if err := evidence.Validate(); err != nil {
		t.Fatalf("evidence Validate() error = %v", err)
	}
	for _, mutate := range []func(*EvidenceArtifact){
		func(v *EvidenceArtifact) { v.ID = "" },
		func(v *EvidenceArtifact) { v.ID = " " },
		func(v *EvidenceArtifact) { v.CaseID = "" },
		func(v *EvidenceArtifact) { v.AttemptID = "" },
		func(v *EvidenceArtifact) { v.Kind = "" },
		func(v *EvidenceArtifact) { v.PathOrReference = "" },
		func(v *EvidenceArtifact) { v.SHA256 = "not-a-sha" },
		func(v *EvidenceArtifact) { v.RedactionStatus = RedactionStatus("unknown") },
	} {
		invalid := evidence
		mutate(&invalid)
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid evidence %+v passed validation", invalid)
		}
	}

	change := CodeChange{
		ID:           "change-1",
		CaseID:       "case-1",
		AttemptID:    "attempt-fix",
		Repo:         "api",
		BaseBranch:   "main",
		FixBranch:    "fix/case-1",
		FixCommit:    "abc123",
		TestEvidence: json.RawMessage(`{"command":"go test ./..."}`),
	}
	if err := change.Validate(); err != nil {
		t.Fatalf("code change Validate() error = %v", err)
	}
	for _, mutate := range []func(*CodeChange){
		func(v *CodeChange) { v.ID = "" },
		func(v *CodeChange) { v.CaseID = "" },
		func(v *CodeChange) { v.AttemptID = "" },
		func(v *CodeChange) { v.Repo = "" },
		func(v *CodeChange) { v.Repo = " " },
		func(v *CodeChange) { v.BaseBranch = "" },
		func(v *CodeChange) { v.FixBranch = "" },
		func(v *CodeChange) { v.FixCommit = "" },
		func(v *CodeChange) { v.TestEvidence = json.RawMessage(`{`) },
	} {
		invalid := change
		mutate(&invalid)
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid code change %+v passed validation", invalid)
		}
	}

	observation := DeploymentObservation{
		ID:                 "observation-1",
		CaseID:             "case-1",
		Environment:        "test",
		ExpectedCommits:    map[string]string{"api": "abc123"},
		VerificationSource: "manual",
		ObservedCommits:    map[string]string{"api": "abc123"},
		ObservedAt:         time.Now().UTC(),
		VerifiedAt:         func() *time.Time { value := time.Now().UTC(); return &value }(),
		Result:             DeploymentResultMatched,
	}
	if err := observation.Validate(); err != nil {
		t.Fatalf("deployment observation Validate() error = %v", err)
	}
	for _, mutate := range []func(*DeploymentObservation){
		func(v *DeploymentObservation) { v.ID = "" },
		func(v *DeploymentObservation) { v.CaseID = "" },
		func(v *DeploymentObservation) { v.Environment = "" },
		func(v *DeploymentObservation) { v.Environment = " " },
		func(v *DeploymentObservation) { v.ExpectedCommits = nil },
		func(v *DeploymentObservation) { v.ExpectedCommits = map[string]string{"": "abc123"} },
		func(v *DeploymentObservation) { v.VerificationSource = "" },
		func(v *DeploymentObservation) { v.ObservedAt = time.Time{} },
		func(v *DeploymentObservation) { v.DiagnosticCode = strings.Repeat("x", 65) },
		func(v *DeploymentObservation) { v.DiagnosticMessage = "unsafe\nline" },
		func(v *DeploymentObservation) { v.Result = DeploymentResult("unknown") },
		func(v *DeploymentObservation) { v.ObservedImages = map[string]string{"api": ""} },
		func(v *DeploymentObservation) { v.ObservedCommits = map[string]string{"": "abc123"} },
	} {
		invalid := observation
		mutate(&invalid)
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid deployment observation %+v passed validation", invalid)
		}
	}
	for _, result := range []DeploymentResult{
		DeploymentResultMismatched,
		DeploymentResultUnavailable,
	} {
		valid := observation
		valid.Result = result
		valid.VerifiedAt = nil
		valid.ObservedCommits = nil
		valid.ObservedVersion = "build-20260711"
		valid.ObservedImages = map[string]string{"api": "registry/api:build-20260711"}
		if err := valid.Validate(); err != nil {
			t.Fatalf("result %q Validate() error = %v", result, err)
		}
	}

	matchedMultiRepo := observation
	matchedMultiRepo.ExpectedCommits = map[string]string{"api": "abc123", "worker": "def456"}
	matchedMultiRepo.ObservedCommits = map[string]string{"api": "abc123", "worker": "def456"}
	if err := matchedMultiRepo.Validate(); err != nil {
		t.Fatalf("matched multi-repo Validate() error = %v", err)
	}
	for _, mutate := range []func(*DeploymentObservation){
		func(v *DeploymentObservation) { v.VerifiedAt = nil },
		func(v *DeploymentObservation) { value := time.Time{}; v.VerifiedAt = &value },
		func(v *DeploymentObservation) { v.ObservedCommits = nil },
		func(v *DeploymentObservation) { delete(v.ObservedCommits, "worker") },
		func(v *DeploymentObservation) { v.ObservedCommits["worker"] = "wrong-commit" },
	} {
		invalid := matchedMultiRepo.Clone()
		mutate(&invalid)
		if err := invalid.Validate(); err == nil {
			t.Fatalf("matched observation without commit proof %+v passed validation", invalid)
		}
	}

	event := TransitionEvent{
		ID:             "event-1",
		CaseID:         "case-1",
		FromStatus:     CasePendingValidation,
		ToStatus:       CaseValidating,
		EventType:      "validation_started",
		ActorType:      "user",
		ActorID:        "user-1",
		IdempotencyKey: "validate:case-1:1",
		PayloadJSON:    json.RawMessage(`{}`),
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("transition event Validate() error = %v", err)
	}
	for _, mutate := range []func(*TransitionEvent){
		func(v *TransitionEvent) { v.ID = "" },
		func(v *TransitionEvent) { v.CaseID = "" },
		func(v *TransitionEvent) { v.ToStatus = CaseFixing },
		func(v *TransitionEvent) { v.EventType = "" },
		func(v *TransitionEvent) { v.ActorType = "" },
		func(v *TransitionEvent) { v.ActorID = "" },
		func(v *TransitionEvent) { v.ActorID = " " },
		func(v *TransitionEvent) { v.IdempotencyKey = "" },
		func(v *TransitionEvent) { v.PayloadJSON = json.RawMessage(`{`) },
	} {
		invalid := event
		mutate(&invalid)
		if err := invalid.Validate(); err == nil {
			t.Fatalf("invalid transition event %+v passed validation", invalid)
		}
	}
}

func TestWorkflowRecordClonesAreIsolated(t *testing.T) {
	now := time.Now().UTC()
	raw := json.RawMessage(`{"key":"value"}`)
	values := map[string]string{"api": "abc123"}
	if cloned := CloneRawMessage(raw); &cloned[0] == &raw[0] {
		t.Fatal("CloneRawMessage retained backing array")
	}
	clonedValues := CloneStringMap(values)
	values["api"] = "changed"
	if clonedValues["api"] == "changed" {
		t.Fatal("CloneStringMap retained source map")
	}
	values["api"] = "abc123"

	incident := IncidentCase{ClosedAt: &now}
	incidentClone := incident.Clone()
	*incident.ClosedAt = incident.ClosedAt.Add(time.Hour)
	if incidentClone.ClosedAt.Equal(*incident.ClosedAt) {
		t.Fatal("IncidentCase.Clone retained ClosedAt pointer")
	}

	attempt := PhaseAttempt{InputJSON: CloneRawMessage(raw), OutputJSON: CloneRawMessage(raw), FinishedAt: &now}
	attemptClone := attempt.Clone()
	attempt.InputJSON[2] = 'X'
	attemptClone.OutputJSON[2] = 'Y'
	*attempt.FinishedAt = attempt.FinishedAt.Add(time.Hour)
	if attemptClone.InputJSON[2] == 'X' || attempt.OutputJSON[2] == 'Y' || attemptClone.FinishedAt.Equal(*attempt.FinishedAt) {
		t.Fatal("PhaseAttempt.Clone retained mutable fields")
	}

	change := CodeChange{TestEvidence: CloneRawMessage(raw)}
	changeClone := change.Clone()
	change.TestEvidence[2] = 'X'
	if changeClone.TestEvidence[2] == 'X' {
		t.Fatal("CodeChange.Clone retained TestEvidence")
	}

	approval := Approval{ScopeJSON: CloneRawMessage(raw), FixCommits: CloneStringMap(values), TargetBranches: CloneStringMap(values)}
	approvalClone := approval.Clone()
	approval.ScopeJSON[2] = 'X'
	approval.FixCommits["api"] = "changed"
	approvalClone.TargetBranches["api"] = "changed-clone"
	if approvalClone.ScopeJSON[2] == 'X' || approvalClone.FixCommits["api"] == "changed" || approval.TargetBranches["api"] == "changed-clone" {
		t.Fatal("Approval.Clone retained mutable fields")
	}

	observation := DeploymentObservation{
		ExpectedCommits: CloneStringMap(values),
		ObservedImages:  CloneStringMap(values),
		ObservedCommits: CloneStringMap(values),
		UserNotifiedAt:  &now,
		VerifiedAt:      &now,
	}
	observationClone := observation.Clone()
	observation.ExpectedCommits["api"] = "changed"
	observationClone.ObservedImages["api"] = "changed-clone"
	*observation.VerifiedAt = observation.VerifiedAt.Add(time.Hour)
	if observationClone.ExpectedCommits["api"] == "changed" || observation.ObservedImages["api"] == "changed-clone" || observationClone.VerifiedAt.Equal(*observation.VerifiedAt) {
		t.Fatal("DeploymentObservation.Clone retained mutable fields")
	}

	event := TransitionEvent{PayloadJSON: CloneRawMessage(raw)}
	eventClone := event.Clone()
	event.PayloadJSON[2] = 'X'
	if eventClone.PayloadJSON[2] == 'X' {
		t.Fatal("TransitionEvent.Clone retained PayloadJSON")
	}
}
