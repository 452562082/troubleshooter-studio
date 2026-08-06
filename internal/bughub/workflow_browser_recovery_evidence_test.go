package bughub

import (
	"context"
	"reflect"
	"testing"
)

func TestConservativeBrowserDecisionRecoveryProviderUsesOnlyHostEvidence(t *testing.T) {
	provider := ConservativeBrowserDecisionRecoveryProvider{}
	retry, err := provider.AssessBrowserDecisionRecovery(context.Background(), BrowserDecisionRecoveryObservation{
		Evidence: BrowserStepRecoveryEvidence{DispatchState: BrowserActionDispatchNotDispatched},
	})
	if err != nil || !retry.RetryScenario || retry.RetryProofCode != BrowserRecoveryRetryNotDispatched {
		t.Fatalf("retry=%+v err=%v", retry, err)
	}
	signals, err := provider.AssessBrowserDecisionRecovery(context.Background(), BrowserDecisionRecoveryObservation{
		Evidence: BrowserStepRecoveryEvidence{
			DispatchState: BrowserActionDispatchUnknown, WaitElementRefs: []string{"e-status"},
			ConfirmedObstructionSurfaceRef: "s-1",
		},
	})
	if err != nil || signals.RetryScenario || !reflect.DeepEqual(signals.Signals.WaitElementRefs, []string{"e-status"}) ||
		signals.Signals.ConfirmedObstructionSurfaceRef != "s-1" {
		t.Fatalf("signals=%+v err=%v", signals, err)
	}
	exhausted, err := provider.AssessBrowserDecisionRecovery(context.Background(), BrowserDecisionRecoveryObservation{
		Evidence: BrowserStepRecoveryEvidence{DispatchState: BrowserActionDispatchUnknown},
	})
	if err != nil || !exhausted.Exhausted || exhausted.RetryScenario {
		t.Fatalf("exhausted=%+v err=%v", exhausted, err)
	}
}
