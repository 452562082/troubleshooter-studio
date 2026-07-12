package bughub

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	workflowReminderSentEvent    = "deployment_reminder_sent"
	workflowReminderSnoozeEvent  = "deployment_reminder_snoozed"
	DefaultWorkflowReminderAfter = 24 * time.Hour
)

type WorkflowReminder struct {
	CaseID       string        `json:"case_id"`
	BugID        string        `json:"bug_id"`
	Environment  string        `json:"environment"`
	WaitingSince time.Time     `json:"waiting_since"`
	WaitingAge   time.Duration `json:"waiting_age"`
	Sequence     int           `json:"sequence"`
}

type workflowReminderSnooze struct {
	Until time.Time `json:"until"`
}

type WorkflowReminderService struct {
	store    *CaseStore
	clock    func() time.Time
	interval time.Duration
	deliver  func(context.Context, WorkflowReminder) error
}

func NewWorkflowReminderService(store *CaseStore, clock func() time.Time, interval time.Duration, deliver func(context.Context, WorkflowReminder) error) *WorkflowReminderService {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	if interval <= 0 {
		interval = DefaultWorkflowReminderAfter
	}
	if deliver == nil {
		deliver = func(context.Context, WorkflowReminder) error { return nil }
	}
	return &WorkflowReminderService{store: store, clock: clock, interval: interval, deliver: deliver}
}

// Poll reserves each reminder in the append-only event log before delivery.
// This gives process restarts and concurrent pollers the same non-spamming key.
func (s *WorkflowReminderService) Poll(ctx context.Context) error {
	if s == nil || s.store == nil {
		return errors.New("workflow reminder store is required")
	}
	now := s.clock().UTC()
	cases, err := s.store.ListCases(ctx)
	if err != nil {
		return err
	}
	var pollErrors []error
	for _, incident := range cases {
		if !workflowReminderEligibleCase(incident) {
			continue
		}
		events, listErr := s.store.ListEvents(ctx, incident.ID)
		if listErr != nil {
			pollErrors = append(pollErrors, listErr)
			continue
		}
		waitingSince, anchor, sent, lastSent, snoozedUntil := workflowReminderHistory(incident, events)
		if waitingSince.IsZero() || now.Before(waitingSince.Add(s.interval)) || now.Before(snoozedUntil) {
			continue
		}
		if !lastSent.IsZero() && now.Before(lastSent.Add(s.interval)) {
			continue
		}
		reminder := WorkflowReminder{CaseID: incident.ID, BugID: incident.BugID, Environment: incident.Environment, WaitingSince: waitingSince, WaitingAge: now.Sub(waitingSince), Sequence: sent + 1}
		key := fmt.Sprintf("workflow-reminder:%s:%s:%d", incident.ID, anchor, reminder.Sequence)
		payload, _ := json.Marshal(reminder)
		replay, reserveErr := s.store.reserveWorkflowReminderEvent(ctx, incident.ID, key, TransitionEvent{
			ID: stableID("event", key), EventType: workflowReminderSentEvent, ActorType: "studio", ActorID: "workflow-reminder", PayloadJSON: payload, CreatedAt: now,
		})
		if reserveErr != nil {
			if !errors.Is(reserveErr, errWorkflowReminderCaseChanged) {
				pollErrors = append(pollErrors, reserveErr)
			}
			continue
		}
		if replay {
			continue
		}
		if deliverErr := s.deliver(ctx, reminder); deliverErr != nil {
			pollErrors = append(pollErrors, deliverErr)
		}
	}
	return errors.Join(pollErrors...)
}

// Snooze persists a local reminder preference as an audit event. It does not
// transition the Case or invoke any phase runner.
func (s *WorkflowReminderService) Snooze(ctx context.Context, caseID string, until time.Time, actorID, idempotencyKey string) error {
	if s == nil || s.store == nil {
		return errors.New("workflow reminder store is required")
	}
	caseID, actorID, idempotencyKey = strings.TrimSpace(caseID), strings.TrimSpace(actorID), strings.TrimSpace(idempotencyKey)
	if caseID == "" || actorID == "" || idempotencyKey == "" || !until.After(s.clock()) {
		return errors.New("snooze requires case, actor, idempotency key, and a future deadline")
	}
	payload, _ := json.Marshal(workflowReminderSnooze{Until: until.UTC()})
	_, err := s.store.reserveWorkflowReminderEvent(ctx, caseID, idempotencyKey, TransitionEvent{
		ID: stableID("event", idempotencyKey), EventType: workflowReminderSnoozeEvent, ActorType: "user", ActorID: actorID, PayloadJSON: payload, CreatedAt: s.clock().UTC(),
	})
	return err
}

var errWorkflowReminderCaseChanged = errors.New("workflow reminder Case is no longer waiting for deployment")

// reserveWorkflowReminderEvent appends reminder metadata without touching the
// mutable Case snapshot or version. A reminder must never invalidate a user's
// already-rendered deployment command scope.
func (s *CaseStore) reserveWorkflowReminderEvent(ctx context.Context, caseID, key string, event TransitionEvent) (replay bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var storedCaseID, storedType, storedPayload string
	queryErr := tx.QueryRowContext(ctx, `SELECT case_id,event_type,payload_json FROM transition_events WHERE idempotency_key=?`, key).Scan(&storedCaseID, &storedType, &storedPayload)
	if queryErr == nil {
		if storedCaseID != caseID || storedType != event.EventType || storedPayload != string(event.PayloadJSON) {
			return false, fmt.Errorf("%w: workflow reminder key %q", ErrIdempotencyConflict, key)
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return true, nil
	}
	if !errors.Is(queryErr, sql.ErrNoRows) {
		return false, queryErr
	}
	incident, err := getCase(ctx, tx, caseID)
	if err != nil {
		return false, err
	}
	if incident.Status != CaseWaitingDeployment || incident.ClosedAt != nil {
		return false, errWorkflowReminderCaseChanged
	}
	event.CaseID, event.FromStatus, event.ToStatus, event.IdempotencyKey = incident.ID, incident.Status, incident.Status, key
	if event.CreatedAt.IsZero() {
		return false, errors.New("workflow reminder event requires a timestamp")
	}
	if err := validateAuditEvent(event); err != nil {
		return false, err
	}
	resultJSON, _ := json.Marshal(incident)
	digest := sha256.Sum256(append([]byte(key+"\x00"), event.PayloadJSON...))
	_, err = tx.ExecContext(ctx, `INSERT INTO transition_events (id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at,request_fingerprint,result_case_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		event.ID, event.CaseID, event.FromStatus, event.ToStatus, event.EventType, event.ActorType, event.ActorID, event.IdempotencyKey, string(event.PayloadJSON), formatStoreTime(event.CreatedAt.UTC()), hex.EncodeToString(digest[:]), string(resultJSON))
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return false, nil
}

func workflowReminderEligibleCase(incident IncidentCase) bool {
	if incident.Status != CaseWaitingDeployment || incident.ClosedAt != nil {
		return false
	}
	env := strings.ToLower(strings.TrimSpace(incident.Environment))
	return !strings.HasPrefix(env, "prod")
}

func workflowReminderHistory(incident IncidentCase, events []TransitionEvent) (waitingSince time.Time, anchor string, sent int, lastSent, snoozedUntil time.Time) {
	waitingSince = incident.UpdatedAt.UTC()
	anchor = fmt.Sprintf("snapshot-v%d", incident.Version)
	startIndex := -1
	for index, event := range events {
		if event.FromStatus != event.ToStatus && event.ToStatus == CaseWaitingDeployment {
			waitingSince, anchor, startIndex = event.CreatedAt.UTC(), event.ID, index
		}
	}
	for index, event := range events {
		if index <= startIndex {
			continue
		}
		switch event.EventType {
		case workflowReminderSentEvent:
			sent++
			if event.CreatedAt.After(lastSent) {
				lastSent = event.CreatedAt.UTC()
			}
		case workflowReminderSnoozeEvent:
			var snooze workflowReminderSnooze
			if json.Unmarshal(event.PayloadJSON, &snooze) == nil && snooze.Until.After(snoozedUntil) {
				snoozedUntil = snooze.Until.UTC()
			}
		}
	}
	return waitingSince, anchor, sent, lastSent, snoozedUntil
}
