package bughub

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExecutionBotForPhaseUsesValidatorForValidationAndRegression(t *testing.T) {
	root := t.TempDir()
	selectedPath := filepath.Join(root, "base-troubleshooter")
	validatorPath := filepath.Join(root, "base-validator")
	for _, path := range []string{selectedPath, validatorPath} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	selected := BotRef{
		Key:      "base|codex",
		Target:   "codex",
		Path:     selectedPath,
		SystemID: "base",
		Role:     "troubleshooter",
		InternalAgents: []BotInternalAgent{
			{ID: "base-validator", Role: "validator"},
		},
	}

	for _, phase := range []Phase{PhaseValidation, PhaseRegression} {
		got, err := ExecutionBotForPhase(phase, selected)
		if err != nil {
			t.Fatalf("phase %s: %v", phase, err)
		}
		if got.Role != "validator" || got.Path != validatorPath || got.Key != "base|codex#validator" {
			t.Fatalf("phase %s resolved %+v", phase, got)
		}
	}
}

func TestExecutionBotForPhaseKeepsSelectedBotForInvestigationAndFix(t *testing.T) {
	selected := BotRef{Key: "base|codex", Target: "codex", Path: t.TempDir(), SystemID: "base", Role: "troubleshooter"}
	for _, phase := range []Phase{PhaseInvestigation, PhaseFix} {
		got, err := ExecutionBotForPhase(phase, selected)
		if err != nil || got.Key != selected.Key || got.Path != selected.Path || got.Role != selected.Role {
			t.Fatalf("phase %s resolved %+v, %v", phase, got, err)
		}
	}
}

func TestExecutionBotForPhaseRejectsMissingValidator(t *testing.T) {
	selected := BotRef{Key: "base|codex", Target: "codex", Path: t.TempDir(), SystemID: "base", Role: "troubleshooter"}
	_, err := ExecutionBotForPhase(PhaseValidation, selected)
	if !errors.Is(err, ErrValidatorNotInstalled) {
		t.Fatalf("err = %v", err)
	}
}
