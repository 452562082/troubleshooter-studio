package bughub

import (
	"context"
	"errors"
	"testing"
)

func TestDeploymentNotificationIntentAcceptsOnlyExplicitCurrentStatements(t *testing.T) {
	for _, text := range []string{"已部署", "部署完成", " 已经部署到 test ", "已经部署到 TeST"} {
		if !ParseDeploymentNotificationIntent(text) {
			t.Errorf("expected recognized intent %q", text)
		}
	}
	for _, text := range []string{"准备部署", "还没部署", "部署失败", "他说“已部署”", "之前已部署", "已部署吗", ""} {
		if ParseDeploymentNotificationIntent(text) {
			t.Errorf("unexpected recognized intent %q", text)
		}
	}
}

func TestDeploymentNotificationIntentBindsExplicitEnvironment(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "intent-env", CaseWaitingDeployment)
	incident = addPushedWorkflowChange(t, store, incident)
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, &recordingDeploymentVerifier{result: DeploymentObservation{VerificationSource: "manual", Result: DeploymentResultMismatched}})
	command := NotifyDeployedCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "intent-env", ActorID: "alice"}
	if _, err := orchestrator.NotifyDeployedFromText(context.Background(), "已经部署到 prod", command); !errors.Is(err, ErrDeploymentEnvironmentMismatch) {
		t.Fatalf("wrong environment error=%v", err)
	}
	stored, err := store.GetCase(context.Background(), incident.ID)
	if err != nil || stored.Status != CaseWaitingDeployment {
		t.Fatalf("case=%+v err=%v", stored, err)
	}
	command.IdempotencyKey = "intent-env-match"
	if _, err := orchestrator.NotifyDeployedFromText(context.Background(), " 已经部署到 TEST ", command); err != nil {
		t.Fatalf("normalized environment error=%v", err)
	}
}

func TestNotifyDeployedIntentRequiresWaitingDeployment(t *testing.T) {
	store := newOrchestratorStore(t)
	incident := createWorkflowCase(t, store, "intent-not-waiting", CaseWaitingMergeApproval)
	orchestrator := NewCaseOrchestrator(store, &recordingPhaseRunner{}, nil, &recordingDeploymentVerifier{})
	_, err := orchestrator.NotifyDeployedFromText(context.Background(), "已部署", NotifyDeployedCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "intent", ActorID: "alice"})
	if !errors.Is(err, ErrApprovalNotReady) {
		t.Fatalf("error=%v", err)
	}
	_, err = orchestrator.NotifyDeployedFromText(context.Background(), "准备部署", NotifyDeployedCommand{CaseID: incident.ID, ExpectedVersion: incident.Version, IdempotencyKey: "intent-2", ActorID: "alice"})
	if !errors.Is(err, ErrDeploymentNotificationIntent) {
		t.Fatalf("ambiguous intent error=%v", err)
	}
}
