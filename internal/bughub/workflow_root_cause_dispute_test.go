package bughub

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDisputeRootCauseReopensInvestigationWithoutRestartingValidation(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident, root := readyCaseForRemediationReassessment(t, store, "case-dispute-root")
	runner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
	command := DisputeRootCauseCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version,
		IdempotencyKey:     DisputeRootCauseKey(incident.ID, root.ID, incident.Version),
		ActorID:            "alice",
		RootCauseAttemptID: root.ID,
		Reason:             "运行时响应已经有独立 signature，旧结论没有解释这条证据",
		Bug:                Bug{ID: incident.BugID},
		Bot:                BotRef{Key: root.BotKey, Target: root.AgentTarget},
	}

	reopened, err := orchestrator.DisputeRootCause(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Status != CaseInvestigating || reopened.CycleNumber != incident.CycleNumber || reopened.CurrentAttemptID == root.ID {
		t.Fatalf("unexpected reopened Case: %+v", reopened)
	}
	attempt, err := store.GetAttempt(ctx, reopened.CurrentAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Phase != PhaseInvestigation || attempt.ParentAttemptID != root.ID || attempt.CycleNumber != incident.CycleNumber {
		t.Fatalf("unexpected reopened attempt: %+v", attempt)
	}
	dispute, ok := rootCauseDisputeFromInput(attempt.InputJSON)
	if !ok || dispute.Reason != command.Reason || dispute.SourceRootCauseAttemptID != root.ID ||
		dispute.PreviousResult.RootCause != "frontend renders nickname and text as separate titles" {
		t.Fatalf("dispute handoff=%+v ok=%v", dispute, ok)
	}
	if !strings.Contains(string(attempt.InputJSON), `"validation_attempt_id":"validation-1"`) ||
		!strings.Contains(string(attempt.InputJSON), `"artifact_id":"artifact-1"`) {
		t.Fatalf("frozen validation handoff was not preserved: %s", attempt.InputJSON)
	}
	if runner.startCount() != 1 {
		t.Fatalf("runner starts=%d, want 1", runner.startCount())
	}
	events, err := store.ListEvents(ctx, incident.ID)
	if err != nil {
		t.Fatal(err)
	}
	var disputed, investigationReopened bool
	for _, event := range events {
		switch event.EventType {
		case "root_cause_disputed":
			disputed = true
			if strings.Contains(string(event.PayloadJSON), command.Reason) || !strings.Contains(string(event.PayloadJSON), `"reason_sha256"`) {
				t.Fatalf("dispute event leaked raw reason or lost digest: %s", event.PayloadJSON)
			}
		case "investigation_reopened":
			investigationReopened = true
		}
	}
	if !disputed || !investigationReopened {
		t.Fatalf("missing dispute audit events: %+v", events)
	}

	replayed, err := orchestrator.DisputeRootCause(ctx, command)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != reopened.ID || replayed.Version != reopened.Version || replayed.CurrentAttemptID != reopened.CurrentAttemptID || runner.startCount() != 1 {
		t.Fatalf("unsafe replay: case=%+v starts=%d", replayed, runner.startCount())
	}
}

func TestDisputeRootCauseSupportsNonCodeRemediationAndRejectsInvalidScope(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-dispute-operator", CaseWaitingRemediation)
	now := time.Now().UTC()
	root := PhaseAttempt{
		ID: incident.ID + "-root", CaseID: incident.ID, CycleNumber: incident.CycleNumber,
		Phase: PhaseInvestigation, Status: AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "investigator",
		InputJSON:  []byte(`{"validation_attempt_id":"validation-1","scenario_hash":"scenario-1","validation_evidence":[{"artifact_id":"artifact-1","kind":"request_facts","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","environment":"test"}]}`),
		OutputJSON: []byte(`{"investigation_status":"root_cause_ready","environment":"test","root_cause":"test deployment has a stale configuration","confidence":"high","root_cause_type":"configuration","remediation":{"mode":"operator_action","repositories":[],"target":"test/base-api config","summary":"restore the expected value","rollback":"restore the previous config version","verification":"rerun the original scenario"},"call_chain":[],"evidence":[],"validation_gaps":[],"gaps":[],"unchecked_scopes":[]}`),
		StartedAt:  now.Add(-time.Minute), FinishedAt: &now,
	}
	if err := store.CreateAttempt(ctx, root); err != nil {
		t.Fatal(err)
	}
	bound, err := store.ApplyCaseMutation(ctx, CaseMutation{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: incident.ID + ":bind-root", RequestJSON: []byte(`{}`),
		Snapshot: CaseSnapshotUpdate{CurrentAttemptID: workflowStringPtr(root.ID)},
		Steps: []CaseMutationStep{{To: CaseWaitingRemediation, AuditOnly: true, Event: TransitionEvent{
			ID: incident.ID + "-bind-root-event", EventType: "root_bound", ActorType: "studio", ActorID: "test", PayloadJSON: []byte(`{}`),
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	incident = bound.Case
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, nil)
	command := DisputeRootCauseCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version,
		IdempotencyKey: DisputeRootCauseKey(incident.ID, root.ID, incident.Version),
		ActorID:        "alice", RootCauseAttemptID: root.ID, Reason: "配置记录显示本轮没有发生变更",
		Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: root.BotKey, Target: root.AgentTarget},
	}
	reopened, err := orchestrator.DisputeRootCause(ctx, command)
	if err != nil || reopened.Status != CaseInvestigating {
		t.Fatalf("case=%+v err=%v", reopened, err)
	}

	invalid := command
	invalid.IdempotencyKey = "wrong"
	if _, err := orchestrator.DisputeRootCause(ctx, invalid); !errors.Is(err, ErrApprovalScope) {
		t.Fatalf("invalid key err=%v", err)
	}
	invalid = command
	invalid.ExpectedVersion = reopened.Version
	invalid.IdempotencyKey = DisputeRootCauseKey(reopened.ID, root.ID, reopened.Version)
	if _, err := orchestrator.DisputeRootCause(ctx, invalid); !errors.Is(err, ErrApprovalNotReady) {
		t.Fatalf("invalid state err=%v", err)
	}
}

func TestRootCauseDisputePromptTreatsPreviousConclusionAsHypothesis(t *testing.T) {
	input := rootCauseDisputeInput{
		Kind: "user_root_cause_dispute", Reason: "静态源码和运行响应不一致",
		SourceRootCauseAttemptID: "root-1",
		PreviousResult:           InvestigationResult{InvestigationStatus: "root_cause_ready", RootCause: "old cause"},
	}
	prompt, err := buildRootCauseDisputePrompt(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"待验证假设", "CodeGraph", "定向补证", "不得修改代码"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q: %s", required, prompt)
		}
	}
	if strings.Contains(prompt, "previous_result 中除 remediation 外的所有字段均由 Studio 锁定") {
		t.Fatalf("disputed root cause must not remain locked: %s", prompt)
	}
}

func TestValidationRefreshCarriesRootCauseDisputeContext(t *testing.T) {
	ctx := context.Background()
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "case-dispute-refresh", CaseValidating)
	now := time.Now().UTC()
	source := PhaseAttempt{
		ID: incident.ID + "-dispute", CaseID: incident.ID, CycleNumber: incident.CycleNumber,
		Phase: PhaseInvestigation, Status: AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "investigator",
		InputJSON:  []byte(`{"validation_attempt_id":"validation-old","validation_evidence":[{"artifact_id":"artifact-old"}],"root_cause_dispute":{"kind":"user_root_cause_dispute","reason":"旧结论忽略运行证据","source_root_cause_attempt_id":"root-old","previous_result":{"investigation_status":"root_cause_ready","root_cause":"old cause"}}}`),
		OutputJSON: []byte(`{}`), StartedAt: now.Add(-time.Minute), FinishedAt: &now,
	}
	if err := store.CreateAttempt(ctx, source); err != nil {
		t.Fatal(err)
	}
	validation := PhaseAttempt{
		ID: incident.ID + "-refresh", CaseID: incident.ID, CycleNumber: incident.CycleNumber,
		Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "investigator",
		InputJSON: []byte(`{"source_investigation_attempt_id":"` + source.ID + `"}`), OutputJSON: []byte(`{}`),
	}
	orchestrator := NewCaseOrchestrator(store, nil, nil, nil)
	carried, err := orchestrator.carryRootCauseDisputeAfterValidationRefresh(ctx, validation, []byte(`{"validation_attempt_id":"validation-new","validation_evidence":[{"artifact_id":"artifact-new"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	dispute, ok := rootCauseDisputeFromInput(carried)
	if !ok || dispute.Reason != "旧结论忽略运行证据" || !strings.Contains(string(carried), `"validation_attempt_id":"validation-new"`) {
		t.Fatalf("carried=%s dispute=%+v ok=%v", carried, dispute, ok)
	}
}
