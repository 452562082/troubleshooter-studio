package bughub

import "testing"

func TestExecutionBotForPhaseKeepsSelectedBotForInvestigationAndFix(t *testing.T) {
	selected := BotRef{Key: "base|codex", Target: "codex", Path: t.TempDir(), SystemID: "base", Role: "troubleshooter"}
	for _, phase := range []Phase{PhaseInvestigation, PhaseFix} {
		got, err := ExecutionBotForPhase(phase, selected)
		if err != nil || got.Key != selected.Key || got.Path != selected.Path || got.Role != selected.Role {
			t.Fatalf("phase %s resolved %+v, %v", phase, got, err)
		}
	}
}

func TestExecutionBotRejectsRetiredPhases(t *testing.T) {
	for _, phase := range []Phase{PhaseValidation, PhaseRegression, PhaseLegacy} {
		if _, err := ExecutionBotForPhase(phase, BotRef{Key: "base|codex"}); err == nil {
			t.Fatalf("accepted retired phase %s", phase)
		}
	}
}
