package bughub

import (
	"context"
	"strings"
)

const (
	BrowserActionDispatchUnknown       = "unknown"
	BrowserActionDispatchNotDispatched = "not_dispatched"
)

// BrowserStepRecoveryEvidence contains only Host/Worker facts about whether a
// failed step reached the business action boundary. It is intentionally not
// model-authored and is not serialized into Decision prompts.
type BrowserStepRecoveryEvidence struct {
	DispatchState                  string
	WaitElementRefs                []string
	ConfirmedObstructionSurfaceRef string
}

// ConservativeBrowserDecisionRecoveryProvider is the production-safe default
// adapter for evidence already returned by the bound Worker session. Unknown
// dispatch state never authorizes a business-action retry.
type ConservativeBrowserDecisionRecoveryProvider struct{}

func (ConservativeBrowserDecisionRecoveryProvider) AssessBrowserDecisionRecovery(_ context.Context, observation BrowserDecisionRecoveryObservation) (BrowserDecisionRecoveryAssessment, error) {
	evidence := observation.Evidence
	if evidence.DispatchState == BrowserActionDispatchNotDispatched {
		return BrowserDecisionRecoveryAssessment{
			RetryScenario: true, RetryProofCode: BrowserRecoveryRetryNotDispatched,
		}, nil
	}
	signals := BrowserRecoverySignals{
		WaitElementRefs:                append([]string(nil), evidence.WaitElementRefs...),
		ConfirmedObstructionSurfaceRef: strings.TrimSpace(evidence.ConfirmedObstructionSurfaceRef),
	}
	if len(signals.WaitElementRefs) != 0 || signals.ConfirmedObstructionSurfaceRef != "" {
		return BrowserDecisionRecoveryAssessment{Signals: signals}, nil
	}
	return BrowserDecisionRecoveryAssessment{Exhausted: true}, nil
}
