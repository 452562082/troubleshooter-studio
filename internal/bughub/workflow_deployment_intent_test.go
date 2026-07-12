package bughub

import (
	"context"
	"errors"
	"testing"
)

func TestDeploymentNotificationIntentAcceptsOnlyExplicitCurrentStatements(t *testing.T) {
	for _, text := range []string{"已部署", "部署完成", " 已经部署到 test "} {
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
