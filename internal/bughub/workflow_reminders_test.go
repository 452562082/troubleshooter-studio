package bughub

import (
	"context"
	"errors"
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

func testReminderProductionResolver(_ context.Context, incident IncidentCase) (bool, error) {
	return incident.ID == "prod" || incident.ID == "prod-region", nil
}

func TestWorkflowRemindersAckAdvancesTwentyFourHourLimit(t *testing.T) {
	store := openWorkflowTestStore(t)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	incident := reminderCase(t, store, "waiting", "test", now.Add(-25*time.Hour), CaseWaitingDeployment)
	var delivered []WorkflowReminder
	service := NewWorkflowReminderService(store, func() time.Time { return now }, 24*time.Hour, func(_ context.Context, reminder WorkflowReminder) error {
		delivered = append(delivered, reminder)
		return nil
	}, testReminderProductionResolver)

	if err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 || delivered[0].ReservationKey == "" || delivered[0].DeliveryAttempt != 1 {
		t.Fatalf("delivered=%+v", delivered)
	}
	if err := service.Ack(context.Background(), delivered[0].CaseID, delivered[0].ReservationKey, delivered[0].DeliveryAttempt, "desktop-root"); err != nil {
		t.Fatal(err)
	}
	if err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 {
		t.Fatalf("duplicate after ack=%d", len(delivered))
	}
	now = now.Add(23*time.Hour + 59*time.Minute)
	if err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 1 {
		t.Fatalf("early next slot=%d", len(delivered))
	}
	now = now.Add(time.Minute)
	if err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(delivered) != 2 || delivered[1].Sequence != 2 {
		t.Fatalf("next slot=%+v", delivered)
	}
	got, _ := store.GetCase(context.Background(), incident.ID)
	if got.Version != incident.Version || got.Status != incident.Status || got.CycleNumber != incident.CycleNumber {
		t.Fatalf("reminder changed Case: before=%+v after=%+v", incident, got)
	}
}

func TestWorkflowRemindersDeliveryFailureAndNoAckRetryAfterBoundedLease(t *testing.T) {
	for _, fixture := range []struct {
		name        string
		deliveryErr error
	}{
		{name: "delivery_error", deliveryErr: errors.New("receiver unavailable")},
		{name: "no_listener_no_ack"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			store := openWorkflowTestStore(t)
			now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
			reminderCase(t, store, fixture.name, "test", now.Add(-25*time.Hour), CaseWaitingDeployment)
			calls := 0
			service := NewWorkflowReminderServiceWithLease(store, func() time.Time { return now }, 24*time.Hour, 5*time.Minute, func(context.Context, WorkflowReminder) error { calls++; return fixture.deliveryErr }, testReminderProductionResolver)
			_ = service.Poll(context.Background())
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
			now = now.Add(4 * time.Minute)
			_ = service.Poll(context.Background())
			if calls != 1 {
				t.Fatalf("spammed before lease calls=%d", calls)
			}
			now = now.Add(time.Minute)
			_ = service.Poll(context.Background())
			if calls != 2 {
				t.Fatalf("did not retry after lease calls=%d", calls)
			}
		})
	}
}

func TestWorkflowRemindersLateMountPullsPendingAndAckReplayIsExact(t *testing.T) {
	store := openWorkflowTestStore(t)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	reminderCase(t, store, "late", "test", now.Add(-25*time.Hour), CaseWaitingDeployment)
	service := NewWorkflowReminderServiceWithLease(store, func() time.Time { return now }, 24*time.Hour, 5*time.Minute, func(context.Context, WorkflowReminder) error { return nil }, testReminderProductionResolver)
	if err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	} // emitted before UI listener mounted
	pending, err := service.Pending(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	item := pending[0]
	if err := service.Ack(context.Background(), item.CaseID, item.ReservationKey, item.DeliveryAttempt, "desktop-root"); err != nil {
		t.Fatal(err)
	}
	if err := service.Ack(context.Background(), item.CaseID, item.ReservationKey, item.DeliveryAttempt, "desktop-root"); err != nil {
		t.Fatalf("ack replay: %v", err)
	}
	pending, err = service.Pending(context.Background())
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after ack=%+v err=%v", pending, err)
	}
}

func TestWorkflowRemindersConcurrentDifferentClocksUseStableReservation(t *testing.T) {
	store := openWorkflowTestStore(t)
	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	reminderCase(t, store, "race", "test", base.Add(-25*time.Hour), CaseWaitingDeployment)
	var mu sync.Mutex
	var delivered []WorkflowReminder
	deliver := func(_ context.Context, reminder WorkflowReminder) error {
		mu.Lock()
		delivered = append(delivered, reminder)
		mu.Unlock()
		return nil
	}
	first := NewWorkflowReminderServiceWithLease(store, func() time.Time { return base }, 24*time.Hour, 5*time.Minute, deliver, testReminderProductionResolver)
	second := NewWorkflowReminderServiceWithLease(store, func() time.Time { return base.Add(time.Second) }, 24*time.Hour, 5*time.Minute, deliver, testReminderProductionResolver)
	var wg sync.WaitGroup
	for _, service := range []*WorkflowReminderService{first, second} {
		wg.Add(1)
		go func(s *WorkflowReminderService) { defer wg.Done(); _ = s.Poll(context.Background()) }(service)
	}
	wg.Wait()
	if len(delivered) != 1 {
		t.Fatalf("deliveries=%+v", delivered)
	}
	if err := second.Ack(context.Background(), delivered[0].CaseID, delivered[0].ReservationKey, delivered[0].DeliveryAttempt, "desktop-root"); err != nil {
		t.Fatal(err)
	}
	events, _ := store.ListEvents(context.Background(), "race")
	reservations, attempts, acks := 0, 0, 0
	for _, event := range events {
		switch event.EventType {
		case workflowReminderPendingEvent:
			reservations++
		case workflowReminderAttemptEvent:
			attempts++
		case workflowReminderAckEvent:
			acks++
		}
	}
	if reservations != 1 || attempts != 1 || acks != 1 {
		t.Fatalf("events pending=%d attempts=%d acks=%d", reservations, attempts, acks)
	}
}

func TestWorkflowRemindersRestartRecoversUnackedAttempt(t *testing.T) {
	store := openWorkflowTestStore(t)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	reminderCase(t, store, "restart", "test", now.Add(-25*time.Hour), CaseWaitingDeployment)
	firstCalls, secondCalls := 0, 0
	first := NewWorkflowReminderServiceWithLease(store, func() time.Time { return now }, 24*time.Hour, 5*time.Minute, func(context.Context, WorkflowReminder) error { firstCalls++; return nil }, testReminderProductionResolver)
	_ = first.Poll(context.Background())
	now = now.Add(5 * time.Minute)
	restarted := NewWorkflowReminderServiceWithLease(store, func() time.Time { return now }, 24*time.Hour, 5*time.Minute, func(context.Context, WorkflowReminder) error { secondCalls++; return nil }, testReminderProductionResolver)
	_ = restarted.Poll(context.Background())
	if firstCalls != 1 || secondCalls != 1 {
		t.Fatalf("calls=%d/%d", firstCalls, secondCalls)
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
	}, testReminderProductionResolver)
	if err := service.Snooze(context.Background(), "snoozed", now.Add(48*time.Hour), "alice", "snooze-1"); err != nil {
		t.Fatal(err)
	}
	if err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestReminderNeverEmitsForResetArchive(t *testing.T) {
	store := openWorkflowTestStore(t)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	old := reminderCase(t, store, "reset-reminder", "test", now.Add(-30*time.Hour), CaseWaitingDeployment)
	reset, err := store.ResetCaseWithReplacement(context.Background(), CaseReset{CaseID: old.ID, NewCaseID: "reset-reminder-next", ExpectedVersion: old.Version, IdempotencyKey: "reset-reminder-key", ActorID: "alice", SelectedBotKey: "validator", RequestJSON: []byte(`{"reason":"retry"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if reset.Archived.Status != CaseResetArchived {
		t.Fatalf("archived=%+v", reset.Archived)
	}
	deliveries := 0
	service := NewWorkflowReminderService(store, func() time.Time { return now }, 24*time.Hour, func(context.Context, WorkflowReminder) error {
		deliveries++
		return nil
	}, testReminderProductionResolver)
	if err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	pending, err := service.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if deliveries != 0 || len(pending) != 0 {
		t.Fatalf("deliveries=%d pending=%+v", deliveries, pending)
	}
}

func TestWorkflowRemindersSnoozeHidesAlreadyPendingDelivery(t *testing.T) {
	store := openWorkflowTestStore(t)
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	reminderCase(t, store, "pending-snooze", "test", now.Add(-25*time.Hour), CaseWaitingDeployment)
	service := NewWorkflowReminderServiceWithLease(store, func() time.Time { return now }, 24*time.Hour, 5*time.Minute, func(context.Context, WorkflowReminder) error { return nil }, testReminderProductionResolver)
	if err := service.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.Snooze(context.Background(), "pending-snooze", now.Add(time.Hour), "alice", "snooze-pending"); err != nil {
		t.Fatal(err)
	}
	pending, err := service.Pending(context.Background())
	if err != nil || len(pending) != 0 {
		t.Fatalf("snoozed pending=%+v err=%v", pending, err)
	}
}

func reminderCase(t *testing.T, store *CaseStore, id, env string, waitingSince time.Time, status CaseStatus) IncidentCase {
	t.Helper()
	created, initial := waitingSince.Add(-time.Hour), status
	if status == CaseWaitingDeployment {
		initial = CaseMerging
	}
	incident := IncidentCase{ID: id, BugID: "bug-" + id, Environment: env, Status: initial, CycleNumber: 1, CurrentAttemptID: "attempt-" + id, Version: 1, CreatedAt: created, UpdatedAt: waitingSince}
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
