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
	workflowReminderPendingEvent = "deployment_reminder_pending"
	workflowReminderAttemptEvent = "deployment_reminder_delivery_attempted"
	workflowReminderAckEvent     = "deployment_reminder_delivered"
	workflowReminderFailureEvent = "deployment_reminder_delivery_failed"
	workflowReminderSnoozeEvent  = "deployment_reminder_snoozed"

	DefaultWorkflowReminderAfter = 24 * time.Hour
	DefaultWorkflowReminderLease = 5 * time.Minute
)

type WorkflowReminder struct {
	CaseID          string        `json:"case_id"`
	BugID           string        `json:"bug_id"`
	Environment     string        `json:"environment"`
	WaitingSince    time.Time     `json:"waiting_since"`
	WaitingAge      time.Duration `json:"waiting_age"`
	Sequence        int           `json:"sequence"`
	ReservationKey  string        `json:"reservation_key"`
	DeliveryAttempt int           `json:"delivery_attempt"`
}

type workflowReminderReservation struct {
	CaseID   string `json:"case_id"`
	Anchor   string `json:"anchor"`
	Sequence int    `json:"sequence"`
}

type workflowReminderAttempt struct {
	CaseID         string `json:"case_id"`
	ReservationKey string `json:"reservation_key"`
	Attempt        int    `json:"attempt"`
}

type workflowReminderAck struct {
	CaseID         string `json:"case_id"`
	ReservationKey string `json:"reservation_key"`
	Attempt        int    `json:"attempt"`
	ActorID        string `json:"actor_id"`
}

type workflowReminderFailure struct {
	CaseID         string `json:"case_id"`
	ReservationKey string `json:"reservation_key"`
	Attempt        int    `json:"attempt"`
	Error          string `json:"error"`
}

type workflowReminderSnooze struct {
	Until time.Time `json:"until"`
}

type WorkflowReminderService struct {
	store             *CaseStore
	clock             func() time.Time
	interval          time.Duration
	lease             time.Duration
	deliver           func(context.Context, WorkflowReminder) error
	resolveProduction WorkflowProductionResolver
}

// WorkflowProductionResolver reads the authoritative environment configuration
// for a Case. A missing resolver or any resolution failure is fail-closed: the
// Case is not eligible for automatic reminders.
type WorkflowProductionResolver func(context.Context, IncidentCase) (bool, error)

func NewWorkflowReminderService(store *CaseStore, clock func() time.Time, interval time.Duration, deliver func(context.Context, WorkflowReminder) error, resolver ...WorkflowProductionResolver) *WorkflowReminderService {
	return NewWorkflowReminderServiceWithLease(store, clock, interval, DefaultWorkflowReminderLease, deliver, resolver...)
}

func NewWorkflowReminderServiceWithLease(store *CaseStore, clock func() time.Time, interval, lease time.Duration, deliver func(context.Context, WorkflowReminder) error, resolver ...WorkflowProductionResolver) *WorkflowReminderService {
	if clock == nil {
		clock = func() time.Time { return time.Now().UTC() }
	}
	if interval <= 0 {
		interval = DefaultWorkflowReminderAfter
	}
	if lease <= 0 {
		lease = DefaultWorkflowReminderLease
	}
	if deliver == nil {
		deliver = func(context.Context, WorkflowReminder) error {
			return errors.New("workflow reminder receiver unavailable")
		}
	}
	var resolveProduction WorkflowProductionResolver
	if len(resolver) > 0 {
		resolveProduction = resolver[0]
	}
	return &WorkflowReminderService{store: store, clock: clock, interval: interval, lease: lease, deliver: deliver, resolveProduction: resolveProduction}
}

// Poll durably reserves one stable slot and one short delivery lease before
// notifying the desktop receiver. Only an explicit Ack starts the next 24-hour
// interval; an unacknowledged attempt is retried after the bounded lease.
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
		if !s.workflowReminderEligibleCase(ctx, incident) {
			continue
		}
		events, listErr := s.store.ListEvents(ctx, incident.ID)
		if listErr != nil {
			pollErrors = append(pollErrors, listErr)
			continue
		}
		history := foldWorkflowReminderHistory(incident, events)
		if history.waitingSince.IsZero() || now.Before(history.snoozedUntil) {
			continue
		}
		reservation := history.pending
		reservationKey := history.pendingKey
		if reservationKey == "" {
			dueSince := history.waitingSince
			if !history.lastAck.IsZero() {
				dueSince = history.lastAck
			}
			if now.Before(dueSince.Add(s.interval)) {
				continue
			}
			reservation = workflowReminderReservation{CaseID: incident.ID, Anchor: history.anchor, Sequence: history.maxSequence + 1}
			reservationKey = fmt.Sprintf("workflow-reminder:%s:%s:%d", incident.ID, history.anchor, reservation.Sequence)
			payload, _ := json.Marshal(reservation)
			replay, reserveErr := s.store.reserveWorkflowReminderEvent(ctx, incident.ID, reservationKey, TransitionEvent{ID: stableID("event", reservationKey), EventType: workflowReminderPendingEvent, ActorType: "studio", ActorID: "workflow-reminder", PayloadJSON: payload, CreatedAt: now})
			if reserveErr != nil {
				if !errors.Is(reserveErr, errWorkflowReminderCaseChanged) {
					pollErrors = append(pollErrors, reserveErr)
				}
				continue
			}
			if replay { // another poll won; refold so attempt numbering/lease is exact
				events, _ = s.store.ListEvents(ctx, incident.ID)
				history = foldWorkflowReminderHistory(incident, events)
				reservation, reservationKey = history.pending, history.pendingKey
			}
		}
		if reservationKey == "" {
			continue
		}
		attempts := history.attempts[reservationKey]
		if len(attempts) > 0 && now.Before(attempts[len(attempts)-1].CreatedAt.Add(s.lease)) {
			continue
		}
		attemptNumber := len(attempts) + 1
		attemptIdentity := workflowReminderAttempt{CaseID: incident.ID, ReservationKey: reservationKey, Attempt: attemptNumber}
		attemptPayload, _ := json.Marshal(attemptIdentity)
		attemptKey := fmt.Sprintf("%s:attempt:%d", reservationKey, attemptNumber)
		replay, attemptErr := s.store.reserveWorkflowReminderEvent(ctx, incident.ID, attemptKey, TransitionEvent{ID: stableID("event", attemptKey), EventType: workflowReminderAttemptEvent, ActorType: "studio", ActorID: "workflow-reminder", PayloadJSON: attemptPayload, CreatedAt: now})
		if attemptErr != nil {
			if !errors.Is(attemptErr, errWorkflowReminderCaseChanged) {
				pollErrors = append(pollErrors, attemptErr)
			}
			continue
		}
		if replay {
			continue
		}
		reminder := WorkflowReminder{CaseID: incident.ID, BugID: incident.BugID, Environment: incident.Environment, WaitingSince: history.waitingSince, WaitingAge: saturatingDurationBetween(history.waitingSince, now), Sequence: reservation.Sequence, ReservationKey: reservationKey, DeliveryAttempt: attemptNumber}
		if deliverErr := s.deliver(ctx, reminder); deliverErr != nil {
			failureText := redactSensitiveText(deliverErr.Error())
			if len(failureText) > 512 {
				failureText = failureText[:512]
			}
			failure := workflowReminderFailure{CaseID: incident.ID, ReservationKey: reservationKey, Attempt: attemptNumber, Error: failureText}
			failurePayload, _ := json.Marshal(failure)
			_, _ = s.store.reserveWorkflowReminderEvent(ctx, incident.ID, attemptKey+":failed", TransitionEvent{ID: stableID("event", attemptKey+":failed"), EventType: workflowReminderFailureEvent, ActorType: "studio", ActorID: "workflow-reminder", PayloadJSON: failurePayload, CreatedAt: now})
			pollErrors = append(pollErrors, deliverErr)
		}
	}
	return errors.Join(pollErrors...)
}

// Pending supports a receiver mounted after the Wails event was emitted. It
// returns attempted but unacknowledged deliveries without changing their lease.
func (s *WorkflowReminderService) Pending(ctx context.Context) ([]WorkflowReminder, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("workflow reminder store is required")
	}
	now := s.clock().UTC()
	cases, err := s.store.ListCases(ctx)
	if err != nil {
		return nil, err
	}
	result := []WorkflowReminder{}
	for _, incident := range cases {
		if !s.workflowReminderEligibleCase(ctx, incident) {
			continue
		}
		events, err := s.store.ListEvents(ctx, incident.ID)
		if err != nil {
			return nil, err
		}
		history := foldWorkflowReminderHistory(incident, events)
		if history.pendingKey == "" || now.Before(history.snoozedUntil) {
			continue
		}
		attempts := history.attempts[history.pendingKey]
		if len(attempts) == 0 {
			continue
		}
		var identity workflowReminderAttempt
		if json.Unmarshal(attempts[len(attempts)-1].PayloadJSON, &identity) != nil {
			continue
		}
		result = append(result, WorkflowReminder{CaseID: incident.ID, BugID: incident.BugID, Environment: incident.Environment, WaitingSince: history.waitingSince, WaitingAge: saturatingDurationBetween(history.waitingSince, now), Sequence: history.pending.Sequence, ReservationKey: history.pendingKey, DeliveryAttempt: identity.Attempt})
	}
	return result, nil
}

func (s *WorkflowReminderService) Ack(ctx context.Context, caseID, reservationKey string, attempt int, actorID string) error {
	caseID, reservationKey, actorID = strings.TrimSpace(caseID), strings.TrimSpace(reservationKey), strings.TrimSpace(actorID)
	if caseID == "" || reservationKey == "" || attempt < 1 || actorID == "" {
		return errors.New("reminder acknowledgement requires case, reservation, attempt, and actor")
	}
	events, err := s.store.ListEvents(ctx, caseID)
	if err != nil {
		return err
	}
	reservationFound, attemptFound := false, false
	for _, event := range events {
		if event.IdempotencyKey == reservationKey && event.EventType == workflowReminderPendingEvent {
			reservationFound = true
		}
		if event.IdempotencyKey == fmt.Sprintf("%s:attempt:%d", reservationKey, attempt) && event.EventType == workflowReminderAttemptEvent {
			attemptFound = true
		}
	}
	if !reservationFound || !attemptFound {
		return errors.New("reminder acknowledgement does not match a durable delivery attempt")
	}
	ack := workflowReminderAck{CaseID: caseID, ReservationKey: reservationKey, Attempt: attempt, ActorID: actorID}
	payload, _ := json.Marshal(ack)
	_, err = s.store.reserveWorkflowReminderEvent(ctx, caseID, reservationKey+":acked", TransitionEvent{ID: stableID("event", reservationKey+":acked"), EventType: workflowReminderAckEvent, ActorType: "desktop", ActorID: actorID, PayloadJSON: payload, CreatedAt: s.clock().UTC()})
	return err
}

func (s *WorkflowReminderService) Snooze(ctx context.Context, caseID string, until time.Time, actorID, idempotencyKey string) error {
	caseID, actorID, idempotencyKey = strings.TrimSpace(caseID), strings.TrimSpace(actorID), strings.TrimSpace(idempotencyKey)
	if caseID == "" || actorID == "" || idempotencyKey == "" || !until.After(s.clock()) {
		return errors.New("snooze requires case, actor, idempotency key, and a future deadline")
	}
	payload, _ := json.Marshal(workflowReminderSnooze{Until: until.UTC()})
	_, err := s.store.reserveWorkflowReminderEvent(ctx, caseID, idempotencyKey, TransitionEvent{ID: stableID("event", idempotencyKey), EventType: workflowReminderSnoozeEvent, ActorType: "user", ActorID: actorID, PayloadJSON: payload, CreatedAt: s.clock().UTC()})
	return err
}

type workflowReminderHistory struct {
	waitingSince time.Time
	anchor       string
	snoozedUntil time.Time
	lastAck      time.Time
	maxSequence  int
	pending      workflowReminderReservation
	pendingKey   string
	attempts     map[string][]TransitionEvent
}

func foldWorkflowReminderHistory(incident IncidentCase, events []TransitionEvent) workflowReminderHistory {
	h := workflowReminderHistory{waitingSince: incident.UpdatedAt.UTC(), anchor: fmt.Sprintf("snapshot-v%d", incident.Version), attempts: map[string][]TransitionEvent{}}
	start := -1
	for i, event := range events {
		if event.FromStatus != event.ToStatus && event.ToStatus == CaseWaitingDeployment {
			h.waitingSince, h.anchor, start = event.CreatedAt.UTC(), event.ID, i
		}
	}
	acked := map[string]time.Time{}
	reservations := map[string]workflowReminderReservation{}
	for i, event := range events {
		if i <= start {
			continue
		}
		switch event.EventType {
		case workflowReminderPendingEvent:
			var value workflowReminderReservation
			if json.Unmarshal(event.PayloadJSON, &value) == nil && value.CaseID == incident.ID && value.Anchor == h.anchor {
				reservations[event.IdempotencyKey] = value
				if value.Sequence > h.maxSequence {
					h.maxSequence = value.Sequence
				}
			}
		case workflowReminderAttemptEvent:
			var value workflowReminderAttempt
			if json.Unmarshal(event.PayloadJSON, &value) == nil && value.CaseID == incident.ID {
				h.attempts[value.ReservationKey] = append(h.attempts[value.ReservationKey], event)
			}
		case workflowReminderAckEvent:
			var value workflowReminderAck
			if json.Unmarshal(event.PayloadJSON, &value) == nil && value.CaseID == incident.ID {
				acked[value.ReservationKey] = event.CreatedAt.UTC()
				if event.CreatedAt.After(h.lastAck) {
					h.lastAck = event.CreatedAt.UTC()
				}
			}
		case workflowReminderSnoozeEvent:
			var value workflowReminderSnooze
			if json.Unmarshal(event.PayloadJSON, &value) == nil && value.Until.After(h.snoozedUntil) {
				h.snoozedUntil = value.Until.UTC()
			}
		}
	}
	for key, value := range reservations {
		if _, ok := acked[key]; !ok && (h.pendingKey == "" || value.Sequence < h.pending.Sequence) {
			h.pending, h.pendingKey = value, key
		}
	}
	return h
}

func (s *WorkflowReminderService) workflowReminderEligibleCase(ctx context.Context, incident IncidentCase) bool {
	if incident.Status != CaseWaitingDeployment || incident.ClosedAt != nil {
		return false
	}
	if s.resolveProduction == nil {
		return false
	}
	isProduction, err := s.resolveProduction(ctx, incident)
	return err == nil && !isProduction
}

func saturatingDurationBetween(from, to time.Time) time.Duration {
	if to.Before(from) {
		return 0
	}
	// time.Sub already saturates to Duration bounds, but retaining this helper
	// makes the metric/reminder contract explicit and testable.
	return to.Sub(from)
}

var errWorkflowReminderCaseChanged = errors.New("workflow reminder Case is no longer waiting for deployment")

func (s *CaseStore) reserveWorkflowReminderEvent(ctx context.Context, caseID, key string, event TransitionEvent) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var storedCaseID, storedType, storedActor, storedPayload string
	queryErr := tx.QueryRowContext(ctx, `SELECT case_id,event_type,actor_id,payload_json FROM transition_events WHERE idempotency_key=?`, key).Scan(&storedCaseID, &storedType, &storedActor, &storedPayload)
	if queryErr == nil {
		if storedCaseID != caseID || storedType != event.EventType || storedActor != event.ActorID || storedPayload != string(event.PayloadJSON) {
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
	_, err = tx.ExecContext(ctx, `INSERT INTO transition_events (id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at,request_fingerprint,result_case_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, event.ID, event.CaseID, event.FromStatus, event.ToStatus, event.EventType, event.ActorType, event.ActorID, event.IdempotencyKey, string(event.PayloadJSON), formatStoreTime(event.CreatedAt.UTC()), hex.EncodeToString(digest[:]), string(resultJSON))
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return false, nil
}
