package bughub

import "fmt"

// ExecutionBotForPhase preserves the selected robot identity for diagnosis and repair.
func ExecutionBotForPhase(phase Phase, selected BotRef) (BotRef, error) {
	if phase != PhaseInvestigation && phase != PhaseFix {
		return BotRef{}, fmt.Errorf("unsupported execution phase %q", phase)
	}
	return selected, nil
}
