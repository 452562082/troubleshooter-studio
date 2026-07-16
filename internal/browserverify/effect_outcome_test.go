package browserverify

import (
	"errors"
	"fmt"
	"testing"
)

func TestRecoveryEffectOutcomeRequiresExplicitKnownFailure(t *testing.T) {
	plain := errors.New("worker failed before publishing a durable effect")
	if outcome := RecoveryEffectOutcomeOf(plain); outcome != RecoveryEffectOutcomeUnknown {
		t.Fatalf("plain outcome=%q", outcome)
	}
	typed := KnownFailedRecoveryEffect(plain)
	if outcome := RecoveryEffectOutcomeOf(typed); outcome != RecoveryEffectKnownFailedNoDurableEffect {
		t.Fatalf("typed outcome=%q", outcome)
	}
	wrapped := fmt.Errorf("fixed boundary context: %w", typed)
	if outcome := RecoveryEffectOutcomeOf(wrapped); outcome != RecoveryEffectKnownFailedNoDurableEffect {
		t.Fatalf("wrapped outcome=%q", outcome)
	}
	if !errors.Is(wrapped, plain) {
		t.Fatal("typed outcome wrapper did not preserve the cause")
	}
}

func TestKnownFailedRecoveryEffectRejectsNilAndDoesNotUpgradeUnknownJoins(t *testing.T) {
	if KnownFailedRecoveryEffect(nil) != nil {
		t.Fatal("nil error acquired an effect outcome")
	}
	ambiguous := errors.New("post-publication failure")
	joined := errors.Join(KnownFailedRecoveryEffect(errors.New("pre-publication cleanup failure")), ambiguous)
	if outcome := RecoveryEffectOutcomeOf(joined); outcome != RecoveryEffectOutcomeUnknown {
		t.Fatalf("mixed joined outcome=%q", outcome)
	}
}
