package bughub

import "fmt"

var allowedCaseTransitions = map[CaseStatus]map[CaseStatus]struct{}{
	CasePendingValidation:    {CaseValidating: {}, CaseResetArchived: {}},
	CaseValidating:           {CaseReproduced: {}, CaseWaitingEvidence: {}, CaseNotReproduced: {}, CaseResetArchived: {}},
	CaseWaitingEvidence:      {CaseValidating: {}, CaseInvestigating: {}, CaseRegressionValidating: {}, CaseResetArchived: {}},
	CaseReproduced:           {CaseInvestigating: {}, CaseResetArchived: {}},
	CaseNotReproduced:        {CaseValidating: {}, CaseResetArchived: {}},
	CaseInvestigating:        {CaseValidating: {}, CaseRootCauseReady: {}, CaseWaitingEvidence: {}, CaseResetArchived: {}},
	CaseRootCauseReady:       {CaseWaitingFixApproval: {}, CaseWaitingRemediation: {}, CaseResetArchived: {}},
	CaseWaitingFixApproval:   {CaseInvestigating: {}, CaseFixing: {}, CaseResetArchived: {}},
	CaseWaitingRemediation:   {CaseRemediationApplied: {}, CaseResetArchived: {}},
	CaseRemediationApplied:   {CaseRegressionValidating: {}, CaseWaitingEvidence: {}, CaseResetArchived: {}},
	CaseFixing:               {CaseFixPushed: {}, CaseFixFailed: {}, CaseResetArchived: {}},
	CaseFixFailed:            {CaseFixing: {}, CaseResetArchived: {}},
	CaseFixPushed:            {CaseWaitingMergeApproval: {}, CaseResetArchived: {}},
	CaseWaitingMergeApproval: {CaseInvestigating: {}, CaseMerging: {}, CaseResetArchived: {}},
	CaseMerging:              {CaseWaitingDeployment: {}, CaseMergeConflict: {}, CaseWaitingMergeApproval: {}, CaseResetArchived: {}},
	CaseMergeConflict:        {CaseWaitingMergeApproval: {}, CaseResetArchived: {}},
	CaseWaitingDeployment:    {CaseDeploymentVerified: {}, CaseDeploymentUnverified: {}, CaseResetArchived: {}},
	CaseDeploymentUnverified: {CaseWaitingDeployment: {}, CaseResetArchived: {}},
	CaseDeploymentVerified:   {CaseRegressionValidating: {}, CaseWaitingEvidence: {}, CaseResetArchived: {}},
	CaseRegressionValidating: {CaseFixedVerified: {}, CaseStillReproduces: {}, CaseWaitingEvidence: {}, CaseResetArchived: {}},
	CaseStillReproduces:      {CaseInvestigating: {}, CaseResetArchived: {}},
}

func CanTransition(from, to CaseStatus) bool {
	_, ok := allowedCaseTransitions[from][to]
	return ok
}

type ErrInvalidTransition struct {
	From   CaseStatus
	To     CaseStatus
	Reason string
}

func (e *ErrInvalidTransition) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("invalid incident case transition %s -> %s: %s", e.From, e.To, e.Reason)
	}
	return fmt.Sprintf("invalid incident case transition %s -> %s", e.From, e.To)
}

func ValidateTransition(incident IncidentCase, to CaseStatus) error {
	if err := incident.Validate(); err != nil {
		return &ErrInvalidTransition{From: incident.Status, To: to, Reason: err.Error()}
	}
	if !CanTransition(incident.Status, to) {
		return &ErrInvalidTransition{From: incident.Status, To: to}
	}
	return nil
}
