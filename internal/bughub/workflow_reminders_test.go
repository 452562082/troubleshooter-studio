package bughub

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openWorkflowTestStore(t *testing.T) *CaseStore {
	t.Helper()
	store, err := OpenCaseStore(filepath.Join(t.TempDir(), "workflow.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestWorkflowRemindersEmitsAtMostOncePerBoundedInterval(t *testing.T) {
	store := openWorkflowTestStore(t)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	incident := reminderCase(t, store, "waiting", "test", now.Add(-25*time.Hour), CaseWaitingDeployment)
	var mu sync.Mutex
	var delivered []WorkflowReminder
	service := NewWorkflowReminderService(store, func() time.Time { return now }, 24*time.Hour, func(_ context.Context, reminder WorkflowReminder) error {
		mu.Lock()
		defer mu.Unlock()
		delivered = append(delivered, reminder)
		return nil
	})

	if err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 || delivered[0].CaseID != incident.ID {
		t.Fatalf("delivered=%+v", delivered)
	}
	now = now.Add(23 * time.Hour)
	if err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 {
		t.Fatalf("early repeat delivered=%d", len(delivered))
	}
	now = now.Add(time.Hour)
	if err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 2 {
		t.Fatalf("next bounded reminder delivered=%d", len(delivered))
	}
	got, err := store.GetCase(context.Background(), incident.ID)
	if err != nil || got.Status != CaseWaitingDeployment || got.CycleNumber != incident.CycleNumber || got.CurrentAttemptID != incident.CurrentAttemptID || got.Version != incident.Version {
		t.Fatalf("reminder changed workflow case=%+v err=%v", got, err)
	}
}

func TestWorkflowRemindersSkipsTerminalProductionAndSnoozedCases(t *testing.T) {
	store := openWorkflowTestStore(t)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	reminderCase(t, store, "prod", "production", now.Add(-30*time.Hour), CaseWaitingDeployment)
	reminderCase(t, store, "prod-region", "prod_cn", now.Add(-30*time.Hour), CaseWaitingDeployment)
	reminderCase(t, store, "closed", "test", now.Add(-30*time.Hour), CaseFixedVerified)
	reminderCase(t, store, "snoozed", "test", now.Add(-30*time.Hour), CaseWaitingDeployment)
	service := NewWorkflowReminderService(store, func() time.Time { return now }, 24*time.Hour, func(_ context.Context, reminder WorkflowReminder) error {
		t.Fatalf("unexpected reminder: %+v", reminder)
		return nil
	})
	if err := service.Snooze(context.Background(), "snoozed", now.Add(48*time.Hour), "alice", "snooze-1"); err != nil {
		t.Fatal(err)
	}
	if err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowRemindersConcurrentPollDoesNotSpam(t *testing.T) {
	store := openWorkflowTestStore(t)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	reminderCase(t, store, "race", "test", now.Add(-25*time.Hour), CaseWaitingDeployment)
	var mu sync.Mutex
	deliveries := 0
	deliver := func(_ context.Context, _ WorkflowReminder) error { mu.Lock(); deliveries++; mu.Unlock(); return nil }
	first := NewWorkflowReminderService(store, func() time.Time { return now }, 24*time.Hour, deliver)
	second := NewWorkflowReminderService(store, func() time.Time { return now }, 24*time.Hour, deliver)
	var wg sync.WaitGroup
	for _, service := range []*WorkflowReminderService{first, second} {
		wg.Add(1)
		go func(service *WorkflowReminderService) { defer wg.Done(); _ = service.Poll(context.Background()) }(service)
	}
	wg.Wait()
	if deliveries != 1 {
		t.Fatalf("deliveries=%d", deliveries)
	}
}

func reminderCase(t *testing.T, store *CaseStore, id, env string, waitingSince time.Time, status CaseStatus) IncidentCase {
	t.Helper()
	created := waitingSince.Add(-time.Hour)
	initialStatus := status
	if status == CaseWaitingDeployment {
		initialStatus = CaseMerging
	}
	incident := IncidentCase{ID: id, BugID: "bug-" + id, Environment: env, Status: initialStatus, CycleNumber: 1, CurrentAttemptID: "attempt-" + id, Version: 1, CreatedAt: created, UpdatedAt: waitingSince}
	if status == CaseFixedVerified {
		closed := waitingSince
		incident.ClosedAt = &closed
	}
	if err := store.CreateCase(context.Background(), incident); err != nil {
		t.Fatal(err)
	}
	if status == CaseWaitingDeployment {
		event := metricEvent("waiting-"+id, CaseMerging, CaseWaitingDeployment, "merge_pushed", "git", waitingSince)
		event.CaseID = id
		var err error
		incident, _, err = store.Transition(context.Background(), id, 1, CaseWaitingDeployment, event)
		if err != nil {
			t.Fatal(err)
		}
	}
	return incident
}
