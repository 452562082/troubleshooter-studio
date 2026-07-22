package bughub

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func readyCaseForRemediationReassessment(t *testing.T, store *CaseStore, id string) (IncidentCase, PhaseAttempt) {
	t.Helper()
	incident := createWorkflowCase(t, store, id, CaseWaitingFixApproval)
	now := time.Now().UTC()
	root := PhaseAttempt{
		ID: id + "-root", CaseID: incident.ID, CycleNumber: incident.CycleNumber, Phase: PhaseInvestigation,
		Status: AttemptStatusSucceeded, AgentTarget: "codex", BotKey: "investigator", InputJSON: []byte(`{"validation_attempt_id":"validation-1","scenario_hash":"scenario-1","validation_evidence":[{"artifact_id":"artifact-1","kind":"request_facts","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","environment":"test"}]}`),
		OutputJSON: []byte(`{"investigation_status":"root_cause_ready","environment":"test","root_cause":"frontend renders nickname and text as separate titles","confidence":"high","root_cause_type":"code","remediation":{"mode":"code_change","target":"frontend card","summary":"deduplicate equal labels","verification":"run the original search"},"call_chain":[],"evidence":[],"validation_gaps":[],"gaps":[],"unchecked_scopes":[]}`),
		StartedAt:  now.Add(-time.Minute), FinishedAt: &now,
	}
	if err := store.CreateAttempt(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	bound, err := store.ApplyCaseMutation(context.Background(), CaseMutation{
		CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: id + ":bind-root", RequestJSON: []byte(`{}`),
		Snapshot: CaseSnapshotUpdate{CurrentAttemptID: workflowStringPtr(root.ID)},
		Steps:    []CaseMutationStep{{To: CaseWaitingFixApproval, AuditOnly: true, Event: TransitionEvent{ID: id + "-bind-event", EventType: "root_bound", ActorType: "studio", ActorID: "test", PayloadJSON: []byte(`{}`)}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return bound.Case, root
}

func TestReconsiderRemediationStartsReadOnlyInvestigationAndReplays(t *testing.T) {
	store := newOrchestratorStore(t)
	incident, root := readyCaseForRemediationReassessment(t, store, "case-reconsider")
	runner := &recordingPhaseRunner{}
	orchestrator := NewCaseOrchestrator(store, runner, nil, nil)
	command := ReconsiderRemediationCommand{
		CaseID: incident.ID, ExpectedVersion: incident.Version,
		IdempotencyKey: ReconsiderRemediationKey(incident.ID, root.ID, incident.Version),
		ActorID:        "alice", RootCauseAttemptID: root.ID,
		Proposal: "优先在后端兼容字段语义，前端只保留兜底去重",
		Bug:      Bug{ID: incident.BugID}, Bot: BotRef{Key: "investigator", Target: "codex"},
	}
	updated, err := orchestrator.ReconsiderRemediation(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != CaseInvestigating || updated.CurrentAttemptID == root.ID {
		t.Fatalf("unexpected reassessment Case: %+v", updated)
	}
	started, err := store.GetAttempt(context.Background(), updated.CurrentAttemptID)
	if err != nil {
		t.Fatal(err)
	}
	if started.Phase != PhaseInvestigation || started.ParentAttemptID != root.ID || started.BotKey != "investigator" {
		t.Fatalf("unexpected reassessment attempt: %+v", started)
	}
	input, ok := remediationReassessmentFromInput(started.InputJSON)
	if !ok || input.Proposal != command.Proposal || input.PreviousResult.RootCause != "frontend renders nickname and text as separate titles" {
		t.Fatalf("reassessment handoff lost context: %+v ok=%v", input, ok)
	}
	if !strings.Contains(string(started.InputJSON), `"validation_attempt_id":"validation-1"`) || !strings.Contains(string(started.InputJSON), `"artifact_id":"artifact-1"`) {
		t.Fatalf("reassessment lost frozen validation handoff: %s", started.InputJSON)
	}
	if runner.startCount() != 1 {
		t.Fatalf("runner starts=%d, want 1", runner.startCount())
	}
	event, found, err := store.GetEventByIdempotencyKey(context.Background(), command.IdempotencyKey)
	if err != nil || !found {
		t.Fatalf("load reassessment audit event: found=%v err=%v", found, err)
	}
	if strings.Contains(string(event.PayloadJSON), command.Proposal) || !strings.Contains(string(event.PayloadJSON), `"proposal_sha256"`) {
		t.Fatalf("audit event must retain only the proposal digest: %s", event.PayloadJSON)
	}

	replayed, err := orchestrator.ReconsiderRemediation(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != updated.ID || replayed.Version != updated.Version || replayed.CurrentAttemptID != updated.CurrentAttemptID || runner.startCount() != 1 {
		t.Fatalf("unsafe replay: replayed=%+v starts=%d", replayed, runner.startCount())
	}
	revisedOutput := []byte(`{"investigation_status":"root_cause_ready","environment":"test","root_cause":"frontend and backend disagree on field semantics","confidence":"high","root_cause_type":"code","remediation":{"mode":"code_change","target":"backend response adapter","summary":"normalize the compatibility field in the backend and keep a frontend guard","verification":"run the original user search and API contract tests"},"call_chain":[],"evidence":[],"validation_gaps":[],"gaps":[],"unchecked_scopes":[]}`)
	completed, err := orchestrator.CompleteAttempt(context.Background(), CompleteAttemptCommand{
		CaseID: updated.ID, AttemptID: started.ID, ExpectedVersion: updated.Version,
		IdempotencyKey: "complete-reassessment", ActorID: "investigator", Outcome: PhaseOutcomeRootCauseReady, OutputJSON: revisedOutput,
	})
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != CaseWaitingFixApproval || completed.CurrentAttemptID != started.ID {
		t.Fatalf("reassessment did not return to fix approval: %+v", completed)
	}
}

func TestReconsiderRemediationRejectsInvalidScopeAndProposal(t *testing.T) {
	store := newOrchestratorStore(t)
	incident, root := readyCaseForRemediationReassessment(t, store, "case-reconsider-invalid")
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, nil)
	base := ReconsiderRemediationCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, ActorID: "alice", RootCauseAttemptID: root.ID, Proposal: "改由后端修复", Bug: Bug{ID: incident.BugID}, Bot: BotRef{Key: "investigator", Target: "codex"}}
	base.IdempotencyKey = "wrong"
	if _, err := orchestrator.ReconsiderRemediation(context.Background(), base); !errors.Is(err, ErrApprovalScope) {
		t.Fatalf("scope err=%v", err)
	}
	base.IdempotencyKey = ReconsiderRemediationKey(base.CaseID, base.RootCauseAttemptID, base.ExpectedVersion)
	base.Proposal = "   "
	if _, err := orchestrator.ReconsiderRemediation(context.Background(), base); err == nil || !strings.Contains(err.Error(), "proposal is required") {
		t.Fatalf("empty proposal err=%v", err)
	}
	base.Proposal = "authorization: Bearer abcdefghijklmnopqrstuvwxyz"
	if _, err := orchestrator.ReconsiderRemediation(context.Background(), base); err == nil || !strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("sensitive proposal err=%v", err)
	}
}

func TestInvestigationPromptExplainsRemediationReassessmentBoundary(t *testing.T) {
	input := remediationReassessmentInput{
		Kind: "user_remediation_proposal", Proposal: "后端统一字段语义", SourceRootCauseAttemptID: "root-1",
		PreviousResult: InvestigationResult{InvestigationStatus: "root_cause_ready", Environment: "test", RootCause: "field mismatch", Confidence: "high"},
	}
	prompt, err := (&AgentPhaseRunner{}).promptForAttempt(PhaseAttempt{Phase: PhaseInvestigation, InputJSON: mustJSON(remediationReassessmentEnvelope{RemediationReassessment: input})}, Bug{ID: "bug"}, BotRef{})
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"用户修复方案重新评估", "没有授权任何修复操作", "前端、后端、配置/数据/运行处置", "root-1", "后端统一字段语义", "previous_result"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q:\n%s", required, prompt)
		}
	}
}
