package bughub

import "fmt"

var allowedCaseTransitions = map[CaseStatus]map[CaseStatus]struct{}{
	CasePendingValidation:    {CaseValidating: {}},
	CaseValidating:           {CaseReproduced: {}, CaseWaitingEvidence: {}, CaseNotReproduced: {}},
	CaseWaitingEvidence:      {CaseValidating: {}, CaseInvestigating: {}, CaseRegressionValidating: {}},
	CaseReproduced:           {CaseInvestigating: {}},
	CaseNotReproduced:        {CaseValidating: {}},
	CaseInvestigating:        {CaseRootCauseReady: {}, CaseWaitingEvidence: {}},
	CaseRootCauseReady:       {CaseWaitingFixApproval: {}},
	CaseWaitingFixApproval:   {CaseFixing: {}},
	CaseFixing:               {CaseFixPushed: {}, CaseFixFailed: {}},
	CaseFixFailed:            {CaseFixing: {}},
	CaseFixPushed:            {CaseWaitingMergeApproval: {}},
	CaseWaitingMergeApproval: {CaseMerging: {}},
	CaseMerging:              {CaseWaitingDeployment: {}, CaseMergeConflict: {}},
	CaseMergeConflict:        {CaseWaitingMergeApproval: {}},
	CaseWaitingDeployment:    {CaseDeploymentVerified: {}, CaseDeploymentUnverified: {}},
	CaseDeploymentUnverified: {CaseWaitingDeployment: {}},
	CaseDeploymentVerified:   {CaseRegressionValidating: {}},
	CaseRegressionValidating: {CaseFixedVerified: {}, CaseStillReproduces: {}, CaseWaitingEvidence: {}},
	CaseStillReproduces:      {CaseInvestigating: {}},
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
