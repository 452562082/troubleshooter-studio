package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResetCaseCancelsOldRunnerAndStartsReplacementValidation(t *testing.T) {
	store := newOrchestratorStore(t)
	old, oldAttempt := prepareResetCase(t, store, "case-reset-orchestrated")
	runner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
	cmd := resetOrchestratorCommand(old, "case-reset-orchestrated-next", "reset-orchestrated")

	replacement, err := orchestrator.ResetCase(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID != cmd.NewCaseID || replacement.Status != CaseValidating || replacement.ResetFromCaseID != old.ID {
		t.Fatalf("replacement=%+v", replacement)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if !reflect.DeepEqual(runner.cancels, []string{oldAttempt.ID}) {
		t.Fatalf("cancels=%v", runner.cancels)
	}
	if len(runner.starts) != 1 || runner.starts[0].CaseID != cmd.NewCaseID || runner.starts[0].Phase != PhaseValidation {
		t.Fatalf("starts=%+v", runner.starts)
	}
	archived, err := store.GetCase(context.Background(), old.ID)
	if err != nil || archived.Status != CaseResetArchived || archived.SupersededByCaseID != cmd.NewCaseID {
		t.Fatalf("archived=%+v err=%v", archived, err)
	}
}

func TestResetCaseReplayDoesNotCancelOrStartTwice(t *testing.T) {
	store := newOrchestratorStore(t)
	old, _ := prepareResetCase(t, store, "case-reset-orchestrated-replay")
	runner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
	cmd := resetOrchestratorCommand(old, "case-reset-orchestrated-replay-next", "reset-orchestrated-replay")

	first, err := orchestrator.ResetCase(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	second, err := orchestrator.ResetCase(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.Status != second.Status || first.CurrentAttemptID != second.CurrentAttemptID {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.cancels) != 1 || len(runner.starts) != 1 {
		t.Fatalf("cancels=%v starts=%+v", runner.cancels, runner.starts)
	}
}

func TestResetCaseStartFailureArchivesOldAndRecoversReplacementState(t *testing.T) {
	store := newOrchestratorStore(t)
	old, _ := prepareResetCase(t, store, "case-reset-schedule-failure")
	runner := &recordingPhaseRunner{startErr: errors.New("runner unavailable")}
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
	cmd := resetOrchestratorCommand(old, "case-reset-schedule-failure-next", "reset-schedule-failure")

	if _, err := orchestrator.ResetCase(context.Background(), cmd); err == nil {
		t.Fatal("reset start failure returned nil error")
	}
	archived, err := store.GetCase(context.Background(), old.ID)
	if err != nil || archived.Status != CaseResetArchived {
		t.Fatalf("archived=%+v err=%v", archived, err)
	}
	replacement, err := store.GetCase(context.Background(), cmd.NewCaseID)
	if err != nil || replacement.Status != CaseWaitingEvidence {
		t.Fatalf("replacement=%+v err=%v", replacement, err)
	}
	attempt, err := store.GetAttempt(context.Background(), replacement.CurrentAttemptID)
	if err != nil || attempt.Status != AttemptStatusFailed || attempt.ErrorCode != "schedule_failed" {
		t.Fatalf("attempt=%+v err=%v", attempt, err)
	}
}

type resetCancelFailureRunner struct {
	recordingPhaseRunner
	cancelErr error
}

func (r *resetCancelFailureRunner) Cancel(_ context.Context, attemptID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels = append(r.cancels, attemptID)
	return r.cancelErr
}

func TestResetCaseCancelFailureStillReturnsStartedReplacement(t *testing.T) {
	store := newOrchestratorStore(t)
	old, oldAttempt := prepareResetCase(t, store, "case-reset-cancel-failure")
	runner := &resetCancelFailureRunner{cancelErr: errors.New("old runner unavailable")}
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
	cmd := resetOrchestratorCommand(old, "case-reset-cancel-failure-next", "reset-cancel-failure")

	replacement, err := orchestrator.ResetCase(context.Background(), cmd)
	if err != nil {
		t.Fatalf("durable reset and replacement start must succeed despite cancel failure: %v", err)
	}
	if replacement.Status != CaseValidating {
		t.Fatalf("replacement=%+v", replacement)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if !reflect.DeepEqual(runner.cancels, []string{oldAttempt.ID}) || len(runner.starts) != 1 {
		t.Fatalf("cancels=%v starts=%+v", runner.cancels, runner.starts)
	}
}

func TestResetCaseRacingCompletionHasOneWinningState(t *testing.T) {
	store := newOrchestratorStore(t)
	old, attempt := createRunningPhase(t, store, "case-reset-completion-race", CasePendingValidation, CaseValidating, PhaseValidation, AttemptReproduce, []byte(`{}`))
	runner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
	reset := resetOrchestratorCommand(old, "case-reset-completion-race-next", "reset-completion-race")
	complete := CompleteAttemptCommand{CaseID: old.ID, AttemptID: attempt.ID, ExpectedVersion: old.Version, IdempotencyKey: "complete-reset-race", ActorID: "agent", Outcome: PhaseOutcomeNeedsEvidence, OutputJSON: []byte(`{"gaps":["proof"]}`)}

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	var resetResult, completeResult IncidentCase
	var resetErr, completeErr error
	go func() {
		defer wg.Done()
		<-start
		resetResult, resetErr = orchestrator.ResetCase(context.Background(), reset)
	}()
	go func() {
		defer wg.Done()
		<-start
		completeResult, completeErr = orchestrator.CompleteAttempt(context.Background(), complete)
	}()
	close(start)
	wg.Wait()

	if (resetErr == nil) == (completeErr == nil) {
		t.Fatalf("exactly one command must win: reset=%+v err=%v complete=%+v err=%v", resetResult, resetErr, completeResult, completeErr)
	}
	storedOld, err := store.GetCase(context.Background(), old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resetErr == nil {
		if storedOld.Status != CaseResetArchived || resetResult.Status != CaseValidating {
			t.Fatalf("reset winner old=%+v replacement=%+v", storedOld, resetResult)
		}
		if _, err := store.GetCase(context.Background(), reset.NewCaseID); err != nil {
			t.Fatal(err)
		}
	} else {
		if storedOld.Status != CaseWaitingEvidence || completeResult.Status != CaseWaitingEvidence {
			t.Fatalf("completion winner old=%+v result=%+v", storedOld, completeResult)
		}
		if _, err := store.GetCase(context.Background(), reset.NewCaseID); !errors.Is(err, ErrCaseNotFound) {
			t.Fatalf("losing reset created replacement: %v", err)
		}
	}
}

func TestResetCaseLateCompletionCannotRewriteArchivedCase(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	old, attempt := prepareResetCase(t, store, "case-reset-late-completion")
	runner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
	cmd := resetOrchestratorCommand(old, "case-reset-late-completion-next", "reset-late-completion")

	replacement, err := orchestrator.ResetCase(ctx, cmd)
	if err != nil {
		t.Fatal(err)
	}
	archivedBeforeCallback, err := store.GetCase(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	replacementBeforeCallback, err := store.GetCase(ctx, replacement.ID)
	if err != nil {
		t.Fatal(err)
	}

	_, completionErr := orchestrator.CompleteAttempt(ctx, CompleteAttemptCommand{
		CaseID:          old.ID,
		AttemptID:       attempt.ID,
		ExpectedVersion: old.Version,
		IdempotencyKey:  "complete-after-reset",
		ActorID:         "agent",
		Outcome:         PhaseOutcomeNeedsEvidence,
		OutputJSON:      []byte(`{"gaps":["late callback"]}`),
	})
	if completionErr == nil {
		t.Fatal("late completion succeeded after durable reset")
	}

	archivedAfterCallback, err := store.GetCase(ctx, old.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(archivedBeforeCallback, archivedAfterCallback) {
		t.Fatalf("late completion changed archived Case:\nbefore=%+v\nafter=%+v", archivedBeforeCallback, archivedAfterCallback)
	}
	if archivedAfterCallback.Status != CaseResetArchived || archivedAfterCallback.Version != old.Version+1 || archivedAfterCallback.SupersededByCaseID != replacement.ID || archivedAfterCallback.ClosedAt == nil || archivedAfterCallback.CurrentAttemptID != "" {
		t.Fatalf("archived Case lost reset state: %+v", archivedAfterCallback)
	}
	replacementAfterCallback, err := store.GetCase(ctx, replacement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(replacementBeforeCallback, replacementAfterCallback) || replacementAfterCallback.Status != CaseValidating || replacementAfterCallback.ResetFromCaseID != old.ID || replacementAfterCallback.CurrentAttemptID == "" {
		t.Fatalf("late completion changed replacement:\nbefore=%+v\nafter=%+v", replacementBeforeCallback, replacementAfterCallback)
	}
}

func TestResetCaseValidatesCommand(t *testing.T) {
	store := newOrchestratorStore(t)
	old, _ := prepareResetCase(t, store, "case-reset-validation")
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, nil)
	valid := resetOrchestratorCommand(old, "case-reset-validation-next", "reset-validation")
	tests := []struct {
		name string
		edit func(*ResetCaseCommand)
	}{
		{name: "new Case ID", edit: func(cmd *ResetCaseCommand) { cmd.NewCaseID = "" }},
		{name: "different Case IDs", edit: func(cmd *ResetCaseCommand) { cmd.NewCaseID = cmd.CaseID }},
		{name: "Bug", edit: func(cmd *ResetCaseCommand) { cmd.Bug.ID = "" }},
		{name: "matching Bug", edit: func(cmd *ResetCaseCommand) { cmd.Bug.ID = "different-bug" }},
		{name: "Bot", edit: func(cmd *ResetCaseCommand) { cmd.Bot.Target = "" }},
		{name: "JSON object", edit: func(cmd *ResetCaseCommand) { cmd.InputJSON = []byte(`[]`) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cmd := valid
			test.edit(&cmd)
			if _, err := orchestrator.ResetCase(context.Background(), cmd); err == nil {
				t.Fatal("invalid reset command succeeded")
			}
		})
	}
}

func TestResetCaseValidatesDurableExecutionBindingBeforeMutation(t *testing.T) {
	tests := []struct {
		name       string
		mutateCase func(*testing.T, *CaseStore, IncidentCase, PhaseAttempt)
		mutateCmd  func(*ResetCaseCommand)
		wantError  bool
	}{
		{name: "valid binding"},
		{name: "Bot key mismatch", mutateCmd: func(cmd *ResetCaseCommand) { cmd.Bot.Key = "other-bot" }, wantError: true},
		{name: "attempt Bot key mismatch", mutateCase: func(t *testing.T, store *CaseStore, _ IncidentCase, attempt PhaseAttempt) {
			if _, err := store.db.Exec(`UPDATE phase_attempts SET bot_key=? WHERE id=?`, "other-bot", attempt.ID); err != nil {
				t.Fatal(err)
			}
		}, wantError: true},
		{name: "target mismatch", mutateCmd: func(cmd *ResetCaseCommand) { cmd.Bot.Target = "claude" }, wantError: true},
		{name: "source mismatch", mutateCmd: func(cmd *ResetCaseCommand) { cmd.Bug.Source = "other-source" }, wantError: true},
		{name: "system mismatch", mutateCmd: func(cmd *ResetCaseCommand) { cmd.Bug.SystemID = "other-system" }, wantError: true},
		{name: "Bug environment mismatch", mutateCmd: func(cmd *ResetCaseCommand) { cmd.Bug.Env = "prod" }, wantError: true},
		{name: "Bot fallback environment mismatch", mutateCmd: func(cmd *ResetCaseCommand) { cmd.Bug.Env = ""; cmd.Bot.Env = "prod" }, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := newOrchestratorStore(t)
			old, attempt := prepareResetCase(t, store, "case-reset-binding-"+strings.ReplaceAll(test.name, " ", "-"))
			if test.mutateCase != nil {
				test.mutateCase(t, store, old, attempt)
			}
			runner := &recordingPhaseRunner{}
			orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
			cmd := resetOrchestratorCommand(old, old.ID+"-next", "reset-binding-"+test.name)
			cmd.Bug.Source = old.Source
			cmd.Bug.SystemID = old.SystemID
			if test.mutateCmd != nil {
				test.mutateCmd(&cmd)
			}

			beforeCase, err := store.GetCase(ctx, old.ID)
			if err != nil {
				t.Fatal(err)
			}
			beforeAttempt, err := store.GetAttempt(ctx, attempt.ID)
			if err != nil {
				t.Fatal(err)
			}
			_, resetErr := orchestrator.ResetCase(ctx, cmd)
			if test.wantError {
				if resetErr == nil {
					t.Fatal("durable binding mismatch succeeded")
				}
				afterCase, loadErr := store.GetCase(ctx, old.ID)
				if loadErr != nil || !reflect.DeepEqual(beforeCase, afterCase) {
					t.Fatalf("rejected reset changed old Case: before=%+v after=%+v err=%v", beforeCase, afterCase, loadErr)
				}
				afterAttempt, loadErr := store.GetAttempt(ctx, attempt.ID)
				if loadErr != nil || !reflect.DeepEqual(beforeAttempt, afterAttempt) {
					t.Fatalf("rejected reset changed attempt: before=%+v after=%+v err=%v", beforeAttempt, afterAttempt, loadErr)
				}
				if _, loadErr := store.GetCase(ctx, cmd.NewCaseID); !errors.Is(loadErr, ErrCaseNotFound) {
					t.Fatalf("rejected reset created replacement: %v", loadErr)
				}
				runner.mu.Lock()
				defer runner.mu.Unlock()
				if len(runner.cancels) != 0 || len(runner.starts) != 0 {
					t.Fatalf("rejected reset invoked runner: cancels=%v starts=%+v", runner.cancels, runner.starts)
				}
				return
			}
			if resetErr != nil {
				t.Fatalf("valid durable binding rejected: %v", resetErr)
			}
		})
	}
}

func resetOrchestratorCommand(incident IncidentCase, newCaseID, key string) ResetCaseCommand {
	return ResetCaseCommand{CaseID: incident.ID, NewCaseID: newCaseID, ExpectedVersion: incident.Version, IdempotencyKey: key, ActorID: "alice", Bug: Bug{ID: incident.BugID, Source: incident.Source, SystemID: incident.SystemID, Env: incident.Environment}, Bot: BotRef{Key: "validator", Target: "codex"}, InputJSON: []byte(`{"reason":"retry"}`)}
}

func TestResetCaseWithReplacementIsAtomic(t *testing.T) {
	store := openTestCaseStore(t)
	incident, attempt := prepareResetCase(t, store, "case-reset-atomic")

	result, err := store.ResetCaseWithReplacement(context.Background(), resetCommand(incident, "case-reset-atomic-next", "reset-atomic"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Replay || result.CancelledAttemptID != attempt.ID {
		t.Fatalf("result=%+v", result)
	}
	if result.Archived.Status != CaseResetArchived || result.Archived.Version != incident.Version+1 || result.Archived.ClosedAt == nil || result.Archived.CurrentAttemptID != "" || result.Archived.SupersededByCaseID != result.Replacement.ID {
		t.Fatalf("archived=%+v", result.Archived)
	}
	if result.Replacement.Status != CasePendingValidation || result.Replacement.Version != 1 || result.Replacement.ResetFromCaseID != incident.ID || result.Replacement.SelectedBotKey != "validator" || result.Replacement.CurrentAttemptID != "" || result.Replacement.ClosedAt != nil {
		t.Fatalf("replacement=%+v", result.Replacement)
	}
	storedOld, err := store.GetCase(context.Background(), incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedOld.Status != CaseResetArchived || storedOld.CycleNumber != incident.CycleNumber || storedOld.Version != incident.Version+1 || storedOld.CurrentAttemptID != "" || storedOld.ClosedAt == nil || storedOld.SupersededByCaseID != result.Replacement.ID || storedOld.ResetFromCaseID != incident.ResetFromCaseID {
		t.Fatalf("persisted archived Case=%+v", storedOld)
	}
	storedNew, err := store.GetCase(context.Background(), result.Replacement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedNew.Status != CasePendingValidation || storedNew.CycleNumber != 1 || storedNew.Version != 1 || storedNew.CurrentAttemptID != "" || storedNew.ClosedAt != nil || storedNew.ResetFromCaseID != incident.ID || storedNew.SupersededByCaseID != "" {
		t.Fatalf("persisted replacement Case=%+v", storedNew)
	}
	if !reflect.DeepEqual(result.Archived, storedOld) || !reflect.DeepEqual(result.Replacement, storedNew) {
		t.Fatalf("returned result differs from persisted Cases: result=%+v old=%+v new=%+v", result, storedOld, storedNew)
	}
	storedAttempt, err := store.GetAttempt(context.Background(), attempt.ID)
	if err != nil || storedAttempt.Status != AttemptStatusCancelled || storedAttempt.FinishedAt == nil {
		t.Fatalf("attempt=%+v err=%v", storedAttempt, err)
	}
	var claim string
	if err := store.db.QueryRow(`SELECT run_claim_token FROM phase_attempts WHERE id=?`, attempt.ID).Scan(&claim); err != nil || claim != "" {
		t.Fatalf("claim=%q err=%v", claim, err)
	}
	if _, found, err := store.GetFixCheckpoint(context.Background(), attempt.ID); err != nil || found {
		t.Fatalf("checkpoint found=%v err=%v", found, err)
	}
	oldEvents, _ := store.ListEvents(context.Background(), incident.ID)
	newEvents, _ := store.ListEvents(context.Background(), result.Replacement.ID)
	if len(oldEvents) != 1 || oldEvents[0].EventType != "case_reset" || len(newEvents) != 1 || newEvents[0].EventType != "case_created_from_reset" {
		t.Fatalf("old events=%+v new events=%+v", oldEvents, newEvents)
	}
}

func TestResetCaseWithReplacementReplaysSameReplacement(t *testing.T) {
	store := openTestCaseStore(t)
	incident, attempt := prepareResetCase(t, store, "case-reset-replay")
	seedResetRelatedRecords(t, store, incident, attempt, "replay")
	command := resetCommand(incident, "case-reset-replay-next", "reset-replay")
	first, err := store.ResetCaseWithReplacement(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	persistedReplacement, err := store.GetCase(context.Background(), first.Replacement.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeReplay := resetDatabaseEffectsSnapshot(t, store)
	second, err := store.ResetCaseWithReplacement(context.Background(), command)
	if err != nil || !second.Replay {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	afterReplay := resetDatabaseEffectsSnapshot(t, store)
	if !reflect.DeepEqual(beforeReplay, afterReplay) {
		t.Fatalf("replay changed database effects:\nbefore=%+v\nafter=%+v", beforeReplay, afterReplay)
	}
	resetEvents := snapshotRows(t, store, `SELECT event_type,idempotency_key FROM transition_events WHERE idempotency_key IN (?,?) ORDER BY event_type`, command.IdempotencyKey, command.IdempotencyKey+":replacement")
	if len(resetEvents) != 2 {
		t.Fatalf("reset-created events=%d rows=%v", len(resetEvents), resetEvents)
	}
	if resetEvents[0][0] != "case_created_from_reset" || resetEvents[1][0] != "case_reset" {
		t.Fatalf("unexpected reset-created events=%v", resetEvents)
	}
	reloadedReplacement, err := store.GetCase(context.Background(), first.Replacement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persistedReplacement, reloadedReplacement) || !reflect.DeepEqual(first.Replacement, reloadedReplacement) {
		t.Fatalf("replacement changed on replay: first=%+v before=%+v after=%+v", first.Replacement, persistedReplacement, reloadedReplacement)
	}
	second.Replay = false
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("first=%+v second=%+v", first, second)
	}

	conflicts := []struct {
		name   string
		mutate func(*CaseReset)
	}{
		{"new Case ID", func(reset *CaseReset) { reset.NewCaseID += "-different" }},
		{"expected version", func(reset *CaseReset) { reset.ExpectedVersion++ }},
		{"actor", func(reset *CaseReset) { reset.ActorID = "bob" }},
		{"request", func(reset *CaseReset) { reset.RequestJSON = json.RawMessage(`{"reason":"different"}`) }},
		{"Bot", func(reset *CaseReset) { reset.SelectedBotKey = "different-bot" }},
	}
	for _, conflict := range conflicts {
		t.Run("fingerprint conflict "+conflict.name, func(t *testing.T) {
			changed := command
			changed.RequestJSON = CloneRawMessage(command.RequestJSON)
			conflict.mutate(&changed)
			if _, err := store.ResetCaseWithReplacement(context.Background(), changed); !errors.Is(err, ErrIdempotencyConflict) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestResetCaseWithReplacementRejectsTerminalAndStaleCases(t *testing.T) {
	for _, status := range []CaseStatus{CaseFixedVerified, CaseLegacyArchived, CaseResetArchived} {
		t.Run(string(status), func(t *testing.T) {
			store := openTestCaseStore(t)
			closed := time.Now().UTC()
			incident := IncidentCase{ID: "terminal-" + string(status), BugID: "bug-terminal-" + string(status), Status: status, CycleNumber: 1, Version: 1, ClosedAt: &closed}
			if status == CaseLegacyArchived {
				incident.ClosedAt = nil
			}
			if err := store.CreateCase(context.Background(), incident); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ResetCaseWithReplacement(context.Background(), resetCommand(incident, incident.ID+"-next", "reset-terminal-"+string(status))); err == nil {
				t.Fatal("terminal Case reset succeeded")
			}
		})
	}
	t.Run("stale version", func(t *testing.T) {
		store := openTestCaseStore(t)
		incident := createWorkflowCase(t, store, "case-reset-stale", CaseWaitingEvidence)
		command := resetCommand(incident, "case-reset-stale-next", "reset-stale")
		command.ExpectedVersion++
		if _, err := store.ResetCaseWithReplacement(context.Background(), command); !errors.Is(err, ErrCaseVersionConflict) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("duplicate replacement", func(t *testing.T) {
		store := openTestCaseStore(t)
		incident := createWorkflowCase(t, store, "case-reset-duplicate", CaseWaitingEvidence)
		if err := store.CreateCase(context.Background(), IncidentCase{ID: "replacement-exists", BugID: "other-bug", Status: CasePendingValidation, CycleNumber: 1, Version: 1}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ResetCaseWithReplacement(context.Background(), resetCommand(incident, "replacement-exists", "reset-duplicate")); err == nil {
			t.Fatal("duplicate replacement accepted")
		}
	})
}

func TestResetCaseWithReplacementPreservesRelatedRecords(t *testing.T) {
	store := openTestCaseStore(t)
	incident, attempt := prepareResetCase(t, store, "case-reset-records")
	seedResetRelatedRecords(t, store, incident, attempt, "preserve")
	seedResetTransitionEvent(t, store, incident)
	before := relatedRecordsSnapshot(t, store, incident.ID)
	beforeEvents := snapshotRows(t, store, `SELECT * FROM transition_events WHERE case_id=? ORDER BY id`, incident.ID)
	if _, err := store.ResetCaseWithReplacement(context.Background(), resetCommand(incident, "case-reset-records-next", "reset-records")); err != nil {
		t.Fatal(err)
	}
	after := relatedRecordsSnapshot(t, store, incident.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("related records changed:\nbefore=%+v\nafter=%+v", before, after)
	}
	afterPreexistingEvents := snapshotRows(t, store, `SELECT * FROM transition_events WHERE case_id=? AND idempotency_key NOT IN (?,?) ORDER BY id`, incident.ID, "reset-records", "reset-records:replacement")
	if !reflect.DeepEqual(beforeEvents, afterPreexistingEvents) {
		t.Fatalf("preexisting events changed:\nbefore=%+v\nafter=%+v", beforeEvents, afterPreexistingEvents)
	}
	allEvents := snapshotRows(t, store, `SELECT * FROM transition_events WHERE case_id IN (?,?) ORDER BY id`, incident.ID, "case-reset-records-next")
	if len(allEvents) != len(beforeEvents)+2 {
		t.Fatalf("events before=%d after=%d rows=%v", len(beforeEvents), len(allEvents), allEvents)
	}
}

func TestResetCaseWithReplacementRollsBackInjectedFailure(t *testing.T) {
	store := openTestCaseStore(t)
	incident, attempt := prepareResetCase(t, store, "case-reset-rollback")
	var initialClaim string
	if err := store.db.QueryRow(`SELECT run_claim_token FROM phase_attempts WHERE id=?`, attempt.ID).Scan(&initialClaim); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TRIGGER fail_reset_replacement_event BEFORE INSERT ON transition_events WHEN NEW.event_type='case_created_from_reset' BEGIN SELECT RAISE(ABORT, 'injected reset failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResetCaseWithReplacement(context.Background(), resetCommand(incident, "case-reset-rollback-next", "reset-rollback")); err == nil {
		t.Fatal("injected failure was ignored")
	}
	old, err := store.GetCase(context.Background(), incident.ID)
	if err != nil || old.Status != incident.Status || old.Version != incident.Version || old.ClosedAt != nil || old.SupersededByCaseID != "" {
		t.Fatalf("old=%+v err=%v", old, err)
	}
	if _, err := store.GetCase(context.Background(), "case-reset-rollback-next"); !errors.Is(err, ErrCaseNotFound) {
		t.Fatalf("replacement err=%v", err)
	}
	storedAttempt, err := store.GetAttempt(context.Background(), attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedAttempt.Status != AttemptStatusRunning || storedAttempt.FinishedAt != nil {
		t.Fatalf("attempt=%+v", storedAttempt)
	}
	var claim string
	if err := store.db.QueryRow(`SELECT run_claim_token FROM phase_attempts WHERE id=?`, attempt.ID).Scan(&claim); err != nil {
		t.Fatal(err)
	}
	if claim != initialClaim {
		t.Fatalf("run claim after rollback=%q, before=%q", claim, initialClaim)
	}
	if _, found, err := store.GetFixCheckpoint(context.Background(), attempt.ID); err != nil || !found {
		t.Fatalf("checkpoint found=%v err=%v", found, err)
	}
	var events int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transition_events WHERE idempotency_key LIKE 'reset-rollback%'`).Scan(&events); err != nil || events != 0 {
		t.Fatalf("events=%d err=%v", events, err)
	}
}

func TestResetCaseWithReplacementSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workflow.db")
	store, err := OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	incident, _ := prepareResetCase(t, store, "case-reset-reopen")
	command := resetCommand(incident, "case-reset-reopen-next", "reset-reopen")
	first, err := store.ResetCaseWithReplacement(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenCaseStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	replay, err := store.ResetCaseWithReplacement(context.Background(), command)
	if err != nil || !replay.Replay || replay.Replacement.ID != first.Replacement.ID || replay.Archived.ID != first.Archived.ID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
}

func TestResetCaseWithReplacementRedactsStoredRequest(t *testing.T) {
	store := openTestCaseStore(t)
	incident := createWorkflowCase(t, store, "case-reset-redact", CaseWaitingEvidence)
	secrets := []string{
		"github_pat_1234567890abcdefghijklmnop",
		"session_cookie_value_123456",
		"Bearer abcdefghijklmnop1234567890",
		"raw-password-value",
		"https://reset-user:reset-pass@example.test/private",
	}
	command := resetCommand(incident, "case-reset-redact-next", "reset-redact")
	command.RequestJSON = mustJSON(map[string]any{
		"token": secrets[0], "Cookie": secrets[1], "Authorization": secrets[2],
		"password": secrets[3], "command": "curl " + secrets[4] + " --fail",
	})
	if _, err := store.ResetCaseWithReplacement(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	rows, err := store.db.Query(`SELECT payload_json,request_fingerprint,result_case_json FROM transition_events WHERE idempotency_key IN (?,?)`, command.IdempotencyKey, command.IdempotencyKey+":replacement")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var payload, fingerprint, result string
		if err := rows.Scan(&payload, &fingerprint, &result); err != nil {
			t.Fatal(err)
		}
		stored := payload + fingerprint + result
		for _, secret := range secrets {
			if strings.Contains(stored, secret) {
				t.Fatalf("stored reset identity contains secret %q: %s", secret, stored)
			}
		}
	}
	if err := rows.Err(); err != nil || count != 2 {
		t.Fatalf("rows=%d err=%v", count, err)
	}
}

func prepareResetCase(t *testing.T, store *CaseStore, caseID string) (IncidentCase, PhaseAttempt) {
	t.Helper()
	incident := createWorkflowCase(t, store, caseID, CaseFixing)
	attempt := PhaseAttempt{ID: caseID + "-attempt", CaseID: caseID, CycleNumber: incident.CycleNumber, Phase: PhaseFix, Status: AttemptStatusRunning, AgentTarget: "codex", BotKey: "validator", InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`), StartedAt: time.Now().UTC()}
	if err := store.CreateAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE incident_cases SET current_attempt_id=?,selected_bot_key=? WHERE id=?`, attempt.ID, attempt.BotKey, incident.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE phase_attempts SET run_claim_token=? WHERE id=?`, "claim-token", attempt.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveFixCheckpoint(context.Background(), FixCheckpoint{AttemptID: attempt.ID, CaseID: incident.ID, StagingLocator: attempt.ID + "-staging"}); err != nil {
		t.Fatal(err)
	}
	incident, err := store.GetCase(context.Background(), incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	return incident, attempt
}

func resetCommand(incident IncidentCase, newCaseID, key string) CaseReset {
	return CaseReset{CaseID: incident.ID, NewCaseID: newCaseID, ExpectedVersion: incident.Version, IdempotencyKey: key, ActorID: "alice", SelectedBotKey: "validator", RequestJSON: json.RawMessage(`{"reason":"retry from validation"}`)}
}

type resetRelatedRecordsSnapshot struct {
	Artifacts              [][]string
	Approvals              [][]string
	CodeChanges            [][]string
	DeploymentObservations [][]string
}

type resetDatabaseSnapshot struct {
	Cases       [][]string
	Attempts    [][]string
	Checkpoints [][]string
	Events      [][]string
	Related     resetRelatedRecordsSnapshot
}

func seedResetRelatedRecords(t *testing.T, store *CaseStore, incident IncidentCase, attempt PhaseAttempt, suffix string) {
	t.Helper()
	now := formatStoreTime(time.Now().UTC())
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO evidence_artifacts (id,case_id,attempt_id,kind,path_or_reference,sha256,captured_at,environment,version,request_id,trace_id,redaction_status) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, []any{"artifact-reset-" + suffix, incident.ID, attempt.ID, "api", "/artifact/" + suffix, strings.Repeat("a", 63) + suffix[:1], now, "test", "v1", "request-" + suffix, "trace-" + suffix, RedactionStatusNotRequired}},
		{`INSERT INTO approvals (id,case_id,kind,actor,approved_at,case_version,scope_json,fix_commits_json,target_branches_json,idempotency_key) VALUES (?,?,?,?,?,?,?,?,?,?)`, []any{"approval-reset-" + suffix, incident.ID, ApprovalStartFix, "alice", now, incident.Version, `{"scope":"` + suffix + `"}`, `{"repo":"abc"}`, `{"repo":"main"}`, "approval-reset-key-" + suffix}},
		{`INSERT INTO code_changes (id,case_id,attempt_id,repo,base_branch,fix_branch,fix_commit,test_evidence_json,target_environment_branch,merge_base_head,merge_commit,push_remote,push_status) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, []any{"change-reset-" + suffix, incident.ID, attempt.ID, "repo-" + suffix, "main", "fix/reset-" + suffix, "abc-" + suffix, `{"passed":true}`, "test", "base-head", "merge-commit", "origin", "pushed"}},
		{`INSERT INTO deployment_observations (id,case_id,environment,expected_commits_json,user_notified_at,verification_source,observed_version,observed_images_json,observed_commits_json,verified_commit_ancestors_json,observed_at,diagnostic_code,diagnostic_message,verified_at,result,idempotency_key) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, []any{"observation-reset-" + suffix, incident.ID, "test", `{"repo":"abc"}`, now, "manual", "v1", `{"app":"image:v1"}`, `{"repo":"abc"}`, `{}`, now, "diagnostic", "message", nil, DeploymentResultUnavailable, "observation-reset-key-" + suffix}},
	}
	for _, statement := range statements {
		if _, err := store.db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func seedResetTransitionEvent(t *testing.T, store *CaseStore, incident IncidentCase) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO transition_events (id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at,request_fingerprint,result_case_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, "event-before-reset", incident.ID, incident.Status, incident.Status, "case_note", "user", "historian", "event-before-reset-key", `{"note":"keep exactly"}`, formatStoreTime(time.Now().UTC().Add(-time.Hour)), "preexisting-fingerprint", `{"preexisting":true}`); err != nil {
		t.Fatal(err)
	}
}

func relatedRecordsSnapshot(t *testing.T, store *CaseStore, caseID string) resetRelatedRecordsSnapshot {
	t.Helper()
	return resetRelatedRecordsSnapshot{
		Artifacts:              snapshotRows(t, store, `SELECT * FROM evidence_artifacts WHERE case_id=? ORDER BY id`, caseID),
		Approvals:              snapshotRows(t, store, `SELECT * FROM approvals WHERE case_id=? ORDER BY id`, caseID),
		CodeChanges:            snapshotRows(t, store, `SELECT * FROM code_changes WHERE case_id=? ORDER BY id`, caseID),
		DeploymentObservations: snapshotRows(t, store, `SELECT * FROM deployment_observations WHERE case_id=? ORDER BY id`, caseID),
	}
}

func resetDatabaseEffectsSnapshot(t *testing.T, store *CaseStore) resetDatabaseSnapshot {
	t.Helper()
	return resetDatabaseSnapshot{
		Cases:       snapshotRows(t, store, `SELECT * FROM incident_cases ORDER BY id`),
		Attempts:    snapshotRows(t, store, `SELECT * FROM phase_attempts ORDER BY id`),
		Checkpoints: snapshotRows(t, store, `SELECT * FROM fix_checkpoints ORDER BY attempt_id`),
		Events:      snapshotRows(t, store, `SELECT * FROM transition_events ORDER BY id`),
		Related: resetRelatedRecordsSnapshot{
			Artifacts:              snapshotRows(t, store, `SELECT * FROM evidence_artifacts ORDER BY id`),
			Approvals:              snapshotRows(t, store, `SELECT * FROM approvals ORDER BY id`),
			CodeChanges:            snapshotRows(t, store, `SELECT * FROM code_changes ORDER BY id`),
			DeploymentObservations: snapshotRows(t, store, `SELECT * FROM deployment_observations ORDER BY id`),
		},
	}
}

func snapshotRows(t *testing.T, store *CaseStore, query string, args ...any) [][]string {
	t.Helper()
	rows, err := store.db.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	var result [][]string
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			t.Fatal(err)
		}
		record := make([]string, len(values))
		for index, value := range values {
			switch typed := value.(type) {
			case nil:
				record[index] = "<NULL>"
			case []byte:
				record[index] = string(typed)
			default:
				record[index] = fmt.Sprint(typed)
			}
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}
