package browserverify

// RecoveryEffectOutcome describes only outcomes that are safe for the durable
// desktop recovery journal to act on. Unknown is deliberately the default:
// callers must opt in to retry only when they can prove that this invocation
// published no durable recovery effect.
type RecoveryEffectOutcome string

const (
	RecoveryEffectOutcomeUnknown             RecoveryEffectOutcome = "unknown"
	RecoveryEffectKnownFailedNoDurableEffect RecoveryEffectOutcome = "known_failed_no_durable_effect"
)

// RecoveryEffectError is the narrow typed contract shared by the host browser
// implementation and the desktop recovery binding.
type RecoveryEffectError interface {
	error
	RecoveryEffectOutcome() RecoveryEffectOutcome
}

type classifiedRecoveryEffectError struct {
	cause error
}

func (e *classifiedRecoveryEffectError) Error() string { return e.cause.Error() }
func (e *classifiedRecoveryEffectError) Unwrap() error { return e.cause }
func (*classifiedRecoveryEffectError) RecoveryEffectOutcome() RecoveryEffectOutcome {
	return RecoveryEffectKnownFailedNoDurableEffect
}

// KnownFailedRecoveryEffect marks an error as safe to retry because the
// current Login or Repair invocation is known to have published no durable
// effect. It must never wrap an error returned after session/runtime publish.
func KnownFailedRecoveryEffect(err error) error {
	if err == nil {
		return nil
	}
	if RecoveryEffectOutcomeOf(err) == RecoveryEffectKnownFailedNoDurableEffect {
		return err
	}
	return &classifiedRecoveryEffectError{cause: err}
}

// RecoveryEffectOutcomeOf returns known-failed only when the entire error is
// explicitly classified. A join containing any unclassified error is unknown.
func RecoveryEffectOutcomeOf(err error) RecoveryEffectOutcome {
	if err == nil {
		return RecoveryEffectOutcomeUnknown
	}
	if classified, ok := err.(RecoveryEffectError); ok {
		return classified.RecoveryEffectOutcome()
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return RecoveryEffectOutcomeUnknown
		}
		for _, child := range children {
			if RecoveryEffectOutcomeOf(child) != RecoveryEffectKnownFailedNoDurableEffect {
				return RecoveryEffectOutcomeUnknown
			}
		}
		return RecoveryEffectKnownFailedNoDurableEffect
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return RecoveryEffectOutcomeOf(wrapped.Unwrap())
	}
	return RecoveryEffectOutcomeUnknown
}
