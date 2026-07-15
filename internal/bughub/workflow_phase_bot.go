package bughub

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrValidatorNotInstalled = errors.New("validator role is not installed")

// ExecutionBotForPhase resolves the installed role used to execute a durable
// phase while leaving the selected base bot identity unchanged in persistence.
func ExecutionBotForPhase(phase Phase, selected BotRef) (BotRef, error) {
	switch phase {
	case PhaseInvestigation, PhaseFix:
		return selected, nil
	case PhaseValidation, PhaseRegression:
	default:
		return BotRef{}, fmt.Errorf("unsupported execution phase %q", phase)
	}

	if strings.EqualFold(strings.TrimSpace(selected.Role), "validator") {
		return selected, nil
	}
	validatorID := internalAgentIDForRole(selected, "validator")
	if validatorID == "" {
		return BotRef{}, fmt.Errorf("%w: selected bot %q has no validator agent metadata", ErrValidatorNotInstalled, selected.Key)
	}
	derived := ValidatorBotFor(selected)
	if strings.EqualFold(strings.TrimSpace(selected.Target), "openclaw") {
		if strings.TrimSpace(derived.AgentID) == "" {
			return BotRef{}, ErrValidatorNotInstalled
		}
		return derived, nil
	}
	if strings.TrimSpace(derived.Path) == "" || filepath.Clean(derived.Path) == filepath.Clean(selected.Path) {
		return BotRef{}, fmt.Errorf("%w: validator workspace %q is unavailable", ErrValidatorNotInstalled, validatorID)
	}
	info, err := os.Stat(derived.Path)
	if err != nil || !info.IsDir() {
		return BotRef{}, fmt.Errorf("%w: validator workspace %q is unavailable", ErrValidatorNotInstalled, derived.Path)
	}
	return derived, nil
}
