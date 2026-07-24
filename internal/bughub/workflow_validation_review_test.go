package bughub

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func completeReviewableValidation(t *testing.T, store *CaseStore, orchestrator *CaseOrchestrator, runner *recordingPhaseRunner, caseID string) (IncidentCase, PhaseAttempt, Bug, BotRef) {
	t.Helper()
	ctx := context.Background()
	incident := createWorkflowCase(t, store, caseID, CasePendingValidation)
	bug := Bug{ID: incident.BugID, Source: incident.Source, SystemID: incident.SystemID, Env: "test"}
	bot := BotRef{Key: "base|codex", Target: "codex", Env: "test"}
	var err error
	incident, err = orchestrator.StartCase(ctx, StartCaseCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: caseID + ":start",
		ActorID: "alice", Bug: bug, Bot: bot,
		InputJSON: []byte(`{"mode":"reproduce","target_environment":"test","reproduction_steps":["submit once"]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	validation, err := store.GetAttempt(ctx, incident.CurrentAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	artifact := EvidenceArtifact{
		ID: validation.ID + "-screenshot", CaseID: incident.ID, AttemptID: validation.ID,
		Kind: "screenshot", PathOrReference: "/artifacts/" + validation.ID,
		SHA256: strings.Repeat("a", 64), CapturedAt: validation.StartedAt.Add(time.Second),
		Environment: "test", RedactionStatus: RedactionStatusNotRequired,
	}
	if _, _, err := store.recordEvidenceArtifact(ctx, artifact, nil); err != nil {
		t.Fatal(err)
	}
	output := []byte(`{"verification_status":"reproduced","environment":"test","observed_behavior":"submitted twice","expected_behavior":"submit once","scenario_hash":"scenario-old","evidence":[{"kind":"screenshot","path":"browser/final.png","environment":"test","redaction_status":"not_required"}],"gaps":[]}`)
	incident, err = orchestrator.CompleteAttempt(ctx, CompleteAttemptCommand{
		CaseID: incident.ID, AttemptID: validation.ID, ExpectedVersion: incident.Version,
		IdempotencyKey: caseID + ":complete", ActorID: "validator",
		Outcome: PhaseOutcomeReproduced, OutputJSON: output,
	})
	if err != nil {
		t.Fatal(err)
	}
	if incident.Status != CaseReproduced || incident.CurrentAttemptID != validation.ID || runner.startCount() != 1 {
		t.Fatalf("review gate case=%+v starts=%d", incident, runner.startCount())
	}
	return incident, validation, bug, bot
}

func TestValidationConfirmationSurvivesSQLiteReopenAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workflow.db")
	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	firstRunner := &recordingPhaseRunner{}
	first := NewCaseOrchestrator(store, firstRunner, nil, nil)
	incident, validation, bug, bot := completeReviewableValidation(t, store, first, firstRunner, "case-validation-confirm")
	command := ConfirmValidationCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version,
		IdempotencyKey: ConfirmValidationKey(incident.ID, validation.ID, incident.Version),
		ActorID:        "alice", ValidationAttemptID: validation.ID, Bug: bug, Bot: bot,
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	runner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(reopened, runner, nil, nil)
	confirmed, err := orchestrator.ConfirmValidation(ctx, command)
	if err != nil || confirmed.Status != CaseInvestigating || runner.startCount() != 1 {
		t.Fatalf("confirmed=%+v starts=%d err=%v", confirmed, runner.startCount(), err)
	}
	investigation, err := reopened.GetAttempt(ctx, confirmed.CurrentAttemptID)
	if err != nil || investigation.Phase != PhaseInvestigation || investigation.ParentAttemptID != validation.ID {
		t.Fatalf("investigation=%+v err=%v", investigation, err)
	}
	replayed, err := orchestrator.ConfirmValidation(ctx, command)
	if err != nil || replayed.Version != confirmed.Version || runner.startCount() != 1 {
		t.Fatalf("replay=%+v starts=%d err=%v", replayed, runner.startCount(), err)
	}
}

func TestValidationFeedbackCreatesFreshAttemptAndForcesScenarioContractRevision(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	runner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
	incident, validation, bug, bot := completeReviewableValidation(t, store, orchestrator, runner, "case-validation-feedback")
	feedback := "这个流程只有一次提交；选择文件后会自动上传，不存在第二次提交按钮。"
	revised, err := orchestrator.ContinueWithEvidence(ctx, ContinueWithEvidenceCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version,
		IdempotencyKey: ReviseValidationKey(incident.ID, validation.ID, incident.Version),
		ActorID:        "alice", Phase: PhaseValidation, Bug: bug, Bot: bot,
		InputJSON: mustJSON(map[string]any{
			"mode": "reproduce", "target_environment": "test", "user_input": feedback,
			"scenario_contract_revision": map[string]any{"reason": "user_feedback", "source_attempt_id": validation.ID},
		}),
	})
	if err != nil || revised.Status != CaseValidating || revised.CurrentAttemptID == validation.ID || runner.startCount() != 2 {
		t.Fatalf("revised=%+v starts=%d err=%v", revised, runner.startCount(), err)
	}
	attempt, err := store.GetAttempt(ctx, revised.CurrentAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	if err := json.Unmarshal(attempt.InputJSON, &input); err != nil {
		t.Fatal(err)
	}
	structured, _ := input["validation_feedback"].(map[string]any)
	if input["force_browser_replan"] != true || structured["kind"] != "user_validation_feedback" ||
		structured["reason"] != feedback || structured["source_validation_attempt_id"] != validation.ID {
		t.Fatalf("revision input=%+v", input)
	}
	events, err := store.ListEvents(ctx, incident.ID)
	if err != nil || events[len(events)-1].EventType != "validation_feedback_submitted" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	replayed, err := orchestrator.ContinueWithEvidence(ctx, ContinueWithEvidenceCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version,
		IdempotencyKey: ReviseValidationKey(incident.ID, validation.ID, incident.Version),
		ActorID:        "alice", Phase: PhaseValidation, Bug: bug, Bot: bot,
		InputJSON: mustJSON(map[string]any{
			"mode": "reproduce", "target_environment": "test", "user_input": feedback,
			"scenario_contract_revision": map[string]any{"reason": "user_feedback", "source_attempt_id": validation.ID},
		}),
	})
	if err != nil || replayed.Version != revised.Version || runner.startCount() != 2 {
		t.Fatalf("feedback replay=%+v starts=%d err=%v", replayed, runner.startCount(), err)
	}
}
