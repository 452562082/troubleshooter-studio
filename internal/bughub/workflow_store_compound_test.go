package bughub

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

func TestCaseStoreCompoundMutationCommitsAttemptsMultiEdgeAndAuditAtomically(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-compound")
	validation := PhaseAttempt{ID: "compound-validation", CaseID: "case-compound", CycleNumber: 1, Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusRunning, InputJSON: json.RawMessage(`{"x":1}`), OutputJSON: json.RawMessage(`{}`)}
	if err := store.CreateAttempt(ctx, validation); err != nil {
		t.Fatal(err)
	}
	current := "compound-validation"
	started, _, err := store.TransitionWithUpdate(ctx, "case-compound", 1, CaseValidating, CaseSnapshotUpdate{CurrentAttemptID: &current}, TransitionEvent{ID: "compound-start", IdempotencyKey: "compound-start", EventType: "start", ActorType: "user", ActorID: "alice", PayloadJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	next := PhaseAttempt{ID: "compound-investigation", CaseID: "case-compound", CycleNumber: 1, Phase: PhaseInvestigation, Status: AttemptStatusRunning, InputJSON: json.RawMessage(`{}`), OutputJSON: json.RawMessage(`{}`)}
	finished := validation.Clone()
	finished.Status = AttemptStatusSucceeded
	finished.OutputJSON = json.RawMessage(`{"result":"reproduced"}`)
	mutation := CaseMutation{
		CaseID: "case-compound", ExpectedVersion: started.Version, IdempotencyKey: "complete-validation", RequestJSON: json.RawMessage(`{"outcome":"reproduced"}`),
		FinishAttempts: []PhaseAttempt{finished}, CreateAttempts: []PhaseAttempt{next}, Snapshot: CaseSnapshotUpdate{CurrentAttemptID: workflowStringPointer(next.ID)},
		Steps: []CaseMutationStep{
			{To: CaseReproduced, Event: TransitionEvent{ID: "compound-reproduced", EventType: "validation_reproduced", ActorType: "agent", ActorID: "validator", PayloadJSON: []byte(`{}`)}},
			{To: CaseInvestigating, Event: TransitionEvent{ID: "compound-investigating", EventType: "investigation_started", ActorType: "studio", ActorID: "orchestrator", PayloadJSON: []byte(`{}`)}},
			{To: CaseInvestigating, AuditOnly: true, Event: TransitionEvent{ID: "compound-audit", EventType: "runner_reserved", ActorType: "studio", ActorID: "orchestrator", PayloadJSON: []byte(`{}`)}},
		},
	}
	result, err := store.ApplyCaseMutation(ctx, mutation)
	if err != nil {
		t.Fatal(err)
	}
	if result.Case.Status != CaseInvestigating || result.Case.CurrentAttemptID != next.ID || result.Case.Version != started.Version+1 {
		t.Fatalf("result=%+v", result)
	}
	storedFinished, _ := store.GetAttempt(ctx, validation.ID)
	storedNext, _ := store.GetAttempt(ctx, next.ID)
	if storedFinished.Status != AttemptStatusSucceeded || storedNext.Status != AttemptStatusRunning {
		t.Fatalf("finished=%+v next=%+v", storedFinished, storedNext)
	}
	events, _ := store.ListEvents(ctx, "case-compound")
	if len(events) != 4 {
		t.Fatalf("events=%+v", events)
	}
	terminalNext := next.Clone()
	terminalNext.Status = AttemptStatusFailed
	terminalNext.OutputJSON = []byte(`{"later":true}`)
	if err := store.FinishAttempt(ctx, terminalNext); err != nil {
		t.Fatal(err)
	}

	replay, err := store.ApplyCaseMutation(ctx, mutation)
	if err != nil || !replay.Replay || replay.Case.Status != CaseInvestigating {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	afterReplay, _ := store.GetAttempt(ctx, next.ID)
	if afterReplay.Status != AttemptStatusFailed {
		t.Fatalf("replay rewrote historical attempt: %+v", afterReplay)
	}

	changed := CaseMutation{
		CaseID: "case-compound", ExpectedVersion: started.Version, IdempotencyKey: "complete-validation",
		RequestJSON: json.RawMessage(`{"outcome":"different"}`),
		Steps: []CaseMutationStep{{
			To:    CaseReproduced,
			Event: TransitionEvent{ID: "compound-reproduced", EventType: "validation_reproduced", ActorType: "agent", ActorID: "validator", PayloadJSON: []byte(`{}`)},
		}},
	}
	if _, err := store.ApplyCaseMutation(ctx, changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("err=%v", err)
	}
}

func TestCaseStoreCompoundMutationCancelVsCompleteHasOneExactWinner(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-attempt-race")
	attempt := PhaseAttempt{ID: "attempt-race", CaseID: "case-attempt-race", CycleNumber: 1, Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusRunning, InputJSON: []byte(`{}`), OutputJSON: []byte(`{}`)}
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	id := attempt.ID
	active, _, err := store.TransitionWithUpdate(ctx, attempt.CaseID, 1, CaseValidating, CaseSnapshotUpdate{CurrentAttemptID: &id}, TransitionEvent{ID: "race-start", IdempotencyKey: "race-start", EventType: "start", ActorType: "user", ActorID: "alice", PayloadJSON: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	complete := attempt.Clone()
	complete.Status = AttemptStatusSucceeded
	complete.OutputJSON = []byte(`{"winner":"complete"}`)
	cancel := attempt.Clone()
	cancel.Status = AttemptStatusCancelled
	cancel.OutputJSON = []byte(`{"winner":"cancel"}`)
	mutations := []CaseMutation{
		{CaseID: attempt.CaseID, ExpectedVersion: active.Version, IdempotencyKey: "race-complete", RequestJSON: []byte(`{"kind":"complete"}`), FinishAttempts: []PhaseAttempt{complete}, Steps: []CaseMutationStep{{To: CaseReproduced, Event: TransitionEvent{ID: "race-complete", EventType: "complete", ActorType: "agent", ActorID: "validator", PayloadJSON: []byte(`{}`)}}}},
		{CaseID: attempt.CaseID, ExpectedVersion: active.Version, IdempotencyKey: "race-cancel", RequestJSON: []byte(`{"kind":"cancel"}`), FinishAttempts: []PhaseAttempt{cancel}, Steps: []CaseMutationStep{{To: CaseWaitingEvidence, Event: TransitionEvent{ID: "race-cancel", EventType: "cancel", ActorType: "user", ActorID: "alice", PayloadJSON: []byte(`{}`)}}}},
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, mutation := range mutations {
		wg.Add(1)
		go func(m CaseMutation) {
			defer wg.Done()
			<-start
			_, err := store.ApplyCaseMutation(ctx, m)
			results <- err
		}(mutation)
	}
	close(start)
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, ErrCaseVersionConflict) || errors.Is(err, ErrAttemptAlreadyFinished) {
			conflicts++
		} else {
			t.Fatalf("err=%v", err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatalf("wins=%d conflicts=%d", wins, conflicts)
	}
	stored, _ := store.GetAttempt(ctx, attempt.ID)
	if stored.Status == AttemptStatusSucceeded && string(stored.OutputJSON) != `{"winner":"complete"}` {
		t.Fatalf("stored=%+v", stored)
	}
	if stored.Status == AttemptStatusCancelled && string(stored.OutputJSON) != `{"winner":"cancel"}` {
		t.Fatalf("stored=%+v", stored)
	}
}

func TestCaseStoreCompoundMutationRecordsApprovalCodeAndObservationOrRollsBack(t *testing.T) {
	ctx := context.Background()
	store := openTestCaseStore(t)
	createTestCase(t, store, "case-records")
	attempt := validFixAttempt("records-attempt", "case-records")
	if err := store.CreateAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	active, _, err := store.TransitionWithUpdate(ctx, "case-records", 1, CaseValidating, CaseSnapshotUpdate{CurrentAttemptID: workflowStringPointer(attempt.ID)}, validEvent(999, "case-records"))
	if err != nil {
		t.Fatal(err)
	}
	approval := Approval{ID: "records-approval", CaseID: "case-records", Kind: ApprovalStartFix, Actor: "alice", CaseVersion: active.Version, ScopeJSON: []byte(`{"root_cause_attempt_id":"records-attempt"}`)}
	change := CodeChange{ID: "records-change", CaseID: "case-records", AttemptID: attempt.ID, Repo: "repo", BaseBranch: "main", FixBranch: "fix/bug", FixCommit: "abc", TestEvidence: []byte(`{}`), TargetEnvironmentBranch: "test", PushStatus: "pushed"}
	observation := DeploymentObservation{ID: "records-observation", CaseID: "case-records", Environment: "test", ExpectedCommits: map[string]string{"repo": "abc"}, VerificationSource: "manual", Result: DeploymentResultUnavailable}
	mutation := CaseMutation{CaseID: "case-records", ExpectedVersion: active.Version, IdempotencyKey: "records-mutation", RequestJSON: []byte(`{"records":true}`), Approvals: []Approval{approval}, CodeChanges: []CodeChange{change}, Observations: []DeploymentObservation{observation}, Steps: []CaseMutationStep{{To: CaseWaitingEvidence, Event: TransitionEvent{ID: "records-event", EventType: "records", ActorType: "studio", ActorID: "orchestrator", PayloadJSON: []byte(`{}`)}}}}
	result, err := store.ApplyCaseMutation(ctx, mutation)
	if err != nil || result.Case.Status != CaseWaitingEvidence {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	approvals, _ := store.ListApprovals(ctx, "case-records")
	changes, _ := store.ListCodeChanges(ctx, "case-records")
	observations, _ := store.ListDeploymentObservations(ctx, "case-records")
	if len(approvals) != 1 || len(changes) != 1 || len(observations) != 1 {
		t.Fatalf("approvals=%d changes=%d observations=%d", len(approvals), len(changes), len(observations))
	}
	approvals[0].FixCommits = map[string]string{"x": "mutated"}
	again, _ := store.ListApprovals(ctx, "case-records")
	if len(again[0].FixCommits) != 0 {
		t.Fatal("compound result leaked mutable approval input")
	}
}

func TestCaseStoreCompoundMutationRejectsCrossOwnershipBeforeWrite(t *testing.T) {
	for _, kind := range []string{"approval", "observation", "code-change", "attempt"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			store := openTestCaseStore(t)
			createTestCase(t, store, "owner-a")
			createTestCase(t, store, "owner-b")
			attemptA := validFixAttempt("attempt-a", "owner-a")
			attemptB := validFixAttempt("attempt-b", "owner-b")
			if err := store.CreateAttempt(ctx, attemptA); err != nil {
				t.Fatal(err)
			}
			if err := store.CreateAttempt(ctx, attemptB); err != nil {
				t.Fatal(err)
			}
			id := attemptA.ID
			active, _, err := store.TransitionWithUpdate(ctx, "owner-a", 1, CaseValidating, CaseSnapshotUpdate{CurrentAttemptID: &id}, validEvent(777, "owner-a"))
			if err != nil {
				t.Fatal(err)
			}
			mutation := CaseMutation{CaseID: "owner-a", ExpectedVersion: active.Version, IdempotencyKey: "inject-" + kind, RequestJSON: []byte(`{}`), Steps: []CaseMutationStep{{To: CaseWaitingEvidence, Event: TransitionEvent{ID: "inject-event-" + kind, EventType: "inject", ActorType: "studio", ActorID: "test", PayloadJSON: []byte(`{}`)}}}}
			switch kind {
			case "approval":
				mutation.Approvals = []Approval{{ID: "bad-approval", CaseID: "owner-b", Kind: ApprovalStartFix, Actor: "x", CaseVersion: active.Version, ScopeJSON: []byte(`{"root_cause_attempt_id":"attempt-a"}`)}}
			case "observation":
				mutation.Observations = []DeploymentObservation{{ID: "bad-observation", CaseID: "owner-b", Environment: "test", ExpectedCommits: map[string]string{"repo": "x"}, VerificationSource: "manual", Result: DeploymentResultUnavailable}}
			case "code-change":
				mutation.CodeChanges = []CodeChange{{ID: "bad-change", CaseID: "owner-b", AttemptID: attemptA.ID, Repo: "repo", BaseBranch: "main", FixBranch: "fix", FixCommit: "x", TestEvidence: []byte(`{}`)}}
			case "attempt":
				mutation.CodeChanges = []CodeChange{{ID: "bad-attempt", CaseID: "owner-a", AttemptID: attemptB.ID, Repo: "repo", BaseBranch: "main", FixBranch: "fix", FixCommit: "x", TestEvidence: []byte(`{}`)}}
			}
			if _, err := store.ApplyCaseMutation(ctx, mutation); err == nil {
				t.Fatal("cross ownership accepted")
			}
			got, _ := store.GetCase(ctx, "owner-a")
			if got.Version != active.Version || got.Status != CaseValidating {
				t.Fatalf("partial write: %+v", got)
			}
		})
	}
}
