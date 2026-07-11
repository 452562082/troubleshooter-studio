package bughub

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestCanTransition(t *testing.T) {
	allowed := [][2]CaseStatus{
		{CasePendingValidation, CaseValidating},
		{CaseValidating, CaseReproduced},
		{CaseValidating, CaseWaitingEvidence},
		{CaseReproduced, CaseInvestigating},
		{CaseInvestigating, CaseRootCauseReady},
		{CaseRootCauseReady, CaseWaitingFixApproval},
		{CaseWaitingFixApproval, CaseFixing},
		{CaseFixing, CaseFixPushed},
		{CaseFixPushed, CaseWaitingMergeApproval},
		{CaseWaitingMergeApproval, CaseMerging},
		{CaseMerging, CaseWaitingDeployment},
		{CaseWaitingDeployment, CaseDeploymentVerified},
		{CaseDeploymentVerified, CaseRegressionValidating},
		{CaseRegressionValidating, CaseFixedVerified},
		{CaseRegressionValidating, CaseStillReproduces},
		{CaseStillReproduces, CaseInvestigating},
	}
	for _, edge := range allowed {
		if !CanTransition(edge[0], edge[1]) {
			t.Fatalf("expected %s -> %s", edge[0], edge[1])
		}
	}
	for _, edge := range [][2]CaseStatus{
		{CasePendingValidation, CaseFixing},
		{CaseFixPushed, CaseWaitingDeployment},
		{CaseWaitingDeployment, CaseRegressionValidating},
		{CaseLegacyArchived, CaseInvestigating},
	} {
		if CanTransition(edge[0], edge[1]) {
			t.Fatalf("forbidden %s -> %s", edge[0], edge[1])
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
		valid := PhaseAttempt{ID: "attempt-1", CaseID: "case-1", CycleNumber: 1}
		if err := valid.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
		invalid := []PhaseAttempt{
			{CaseID: "case-1", CycleNumber: 1},
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
			CaseVersion: 1,
			ScopeJSON:   json.RawMessage(`{"phase":"fix"}`),
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
