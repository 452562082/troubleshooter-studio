package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
)

const incidentCaseEvent = "incident-case:event"

type IncidentCaseDetail struct {
	Case                   bughub.IncidentCase            `json:"case"`
	Attempts               []bughub.PhaseAttempt          `json:"attempts"`
	Artifacts              []bughub.EvidenceArtifact      `json:"artifacts"`
	Approvals              []bughub.Approval              `json:"approvals"`
	CodeChanges            []bughub.CodeChange            `json:"code_changes"`
	DeploymentObservations []bughub.DeploymentObservation `json:"deployment_observations"`
	Events                 []bughub.TransitionEvent       `json:"events"`
}

// IncidentCaseEventPayload always carries a versioned snapshot so the Web UI
// can discard stale/out-of-order events. PhaseEvent is present for live Agent
// progress and absent for state-only updates.
type IncidentCaseEventPayload struct {
	Case       bughub.IncidentCase        `json:"case"`
	Snapshot   IncidentCaseDetail         `json:"snapshot"`
	PhaseEvent *bughub.InvestigationEvent `json:"phase_event,omitempty"`
}

type StartIncidentCaseInput struct {
	CaseID          string         `json:"case_id"`
	ExpectedVersion int64          `json:"expected_version"`
	IdempotencyKey  string         `json:"idempotency_key"`
	ActorID         string         `json:"actor_id"`
	InputJSON       map[string]any `json:"input_json,omitempty"`
}

type ContinueIncidentCaseInput struct {
	CaseID          string         `json:"case_id"`
	ExpectedVersion int64          `json:"expected_version"`
	IdempotencyKey  string         `json:"idempotency_key"`
	ActorID         string         `json:"actor_id"`
	Phase           bughub.Phase   `json:"phase"`
	InputJSON       map[string]any `json:"input_json,omitempty"`
}

type ApproveIncidentFixInput struct {
	CaseID             string         `json:"case_id"`
	ExpectedVersion    int64          `json:"expected_version"`
	IdempotencyKey     string         `json:"idempotency_key"`
	ActorID            string         `json:"actor_id"`
	RootCauseAttemptID string         `json:"root_cause_attempt_id"`
	InputJSON          map[string]any `json:"input_json,omitempty"`
}

type ApproveIncidentMergeInput struct {
	CaseID          string            `json:"case_id"`
	ExpectedVersion int64             `json:"expected_version"`
	IdempotencyKey  string            `json:"idempotency_key"`
	ActorID         string            `json:"actor_id"`
	FixCommits      map[string]string `json:"fix_commits"`
	TargetBranches  map[string]string `json:"target_branches"`
}

type NotifyIncidentDeployedInput struct {
	CaseID          string            `json:"case_id"`
	ExpectedVersion int64             `json:"expected_version"`
	IdempotencyKey  string            `json:"idempotency_key"`
	ActorID         string            `json:"actor_id"`
	ObservedVersion string            `json:"observed_version"`
	ObservedCommits map[string]string `json:"observed_commits,omitempty"`
	InputJSON       map[string]any    `json:"input_json,omitempty"`
}

type CancelIncidentAttemptInput struct {
	CaseID          string `json:"case_id"`
	AttemptID       string `json:"attempt_id"`
	ExpectedVersion int64  `json:"expected_version"`
	IdempotencyKey  string `json:"idempotency_key"`
	ActorID         string `json:"actor_id"`
}

func workflowContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (a *App) workflowCommandContext() context.Context {
	return workflowContext(a.getRuntimeContext())
}

func (a *App) initializeIncidentWorkflow(ctx context.Context) error {
	a.workflowMu.Lock()
	defer a.workflowMu.Unlock()
	if a.workflowStore != nil && a.workflowOrchestrator != nil {
		return a.workflowInitErr
	}
	if a.workflowInitErr != nil {
		return a.workflowInitErr
	}
	root := strings.TrimSpace(a.workflowRoot)
	if root == "" {
		root = bughub.DefaultRoot()
	}
	store, err := bughub.OpenCaseStore(filepath.Join(root, "incident-workflow.db"))
	if err != nil {
		a.workflowInitErr = err
		return err
	}
	legacy := bughub.NewInvestigationStore(root)
	investigator := bughub.NewCodexInvestigator(legacy, "codex")
	runner := bughub.NewAgentPhaseRunner(store, investigator, legacy, filepath.Join(root, "artifacts"), nil)
	orchestrator := bughub.NewCaseOrchestrator(store, runner, nil, nil)
	runner.SetCompletionCallback(func(callbackCtx context.Context, command bughub.CompleteAttemptCommand) error {
		incident, completeErr := orchestrator.CompleteAttempt(workflowContext(callbackCtx), command)
		if completeErr == nil {
			a.emitIncidentCase(incident.ID)
		}
		return completeErr
	})
	runner.SetEventSink(func(_ bughub.InvestigationRun, event bughub.InvestigationEvent) {
		caseID, _ := event.Meta["case_id"].(string)
		a.emitIncidentPhaseEvent(caseID, event)
	})
	a.workflowStore = store
	a.workflowOrchestrator = orchestrator
	a.workflowRunner = runner
	a.bugInvestigationMu.Lock()
	a.bugInvestigator = investigator
	a.bugInvestigationMu.Unlock()
	if _, statErr := os.Stat(legacy.Path()); statErr == nil {
		if _, importErr := bughub.ImportLegacyRuns(workflowContext(ctx), store, legacy.Path()); importErr != nil {
			a.workflowInitErr = importErr
			_ = store.Close()
			a.workflowStore, a.workflowOrchestrator, a.workflowRunner = nil, nil, nil
			return importErr
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		a.workflowInitErr = statErr
		_ = store.Close()
		a.workflowStore, a.workflowOrchestrator, a.workflowRunner = nil, nil, nil
		return statErr
	}
	if recoverErr := orchestrator.RecoverInterrupted(workflowContext(ctx)); recoverErr != nil {
		a.workflowInitErr = recoverErr
		_ = store.Close()
		a.workflowStore, a.workflowOrchestrator, a.workflowRunner = nil, nil, nil
		return recoverErr
	}
	return nil
}

func (a *App) closeIncidentWorkflow() error {
	a.workflowMu.Lock()
	defer a.workflowMu.Unlock()
	if a.workflowStore == nil {
		return nil
	}
	err := a.workflowStore.Close()
	a.workflowStore, a.workflowOrchestrator, a.workflowRunner = nil, nil, nil
	return err
}

func (a *App) workflowComponents() (*bughub.CaseStore, *bughub.CaseOrchestrator, error) {
	if err := a.initializeIncidentWorkflow(a.workflowCommandContext()); err != nil {
		return nil, nil, err
	}
	a.workflowMu.Lock()
	defer a.workflowMu.Unlock()
	if a.workflowStore == nil || a.workflowOrchestrator == nil {
		return nil, nil, errors.New("incident workflow is unavailable")
	}
	return a.workflowStore, a.workflowOrchestrator, nil
}

func (a *App) ListIncidentCases() ([]bughub.IncidentCase, error) {
	store, _, err := a.workflowComponents()
	if err != nil {
		return nil, err
	}
	items, err := store.ListCases(a.workflowCommandContext())
	if items == nil {
		items = []bughub.IncidentCase{}
	}
	return items, err
}

func (a *App) GetIncidentCase(caseID string) (IncidentCaseDetail, error) {
	caseID = strings.TrimSpace(caseID)
	if caseID == "" {
		return IncidentCaseDetail{}, errors.New("case_id is required")
	}
	store, _, err := a.workflowComponents()
	if err != nil {
		return IncidentCaseDetail{}, err
	}
	ctx := a.workflowCommandContext()
	incident, err := store.GetCase(ctx, caseID)
	if err != nil {
		return IncidentCaseDetail{}, err
	}
	detail := IncidentCaseDetail{Case: incident}
	if detail.Attempts, err = store.ListAttempts(ctx, bughub.AttemptFilter{CaseID: caseID}); err != nil {
		return IncidentCaseDetail{}, err
	}
	if detail.Artifacts, err = store.ListEvidenceArtifacts(ctx, caseID); err != nil {
		return IncidentCaseDetail{}, err
	}
	if detail.Approvals, err = store.ListApprovals(ctx, caseID); err != nil {
		return IncidentCaseDetail{}, err
	}
	if detail.CodeChanges, err = store.ListCodeChanges(ctx, caseID); err != nil {
		return IncidentCaseDetail{}, err
	}
	if detail.DeploymentObservations, err = store.ListDeploymentObservations(ctx, caseID); err != nil {
		return IncidentCaseDetail{}, err
	}
	if detail.Events, err = store.ListEvents(ctx, caseID); err != nil {
		return IncidentCaseDetail{}, err
	}
	normalizeIncidentCaseDetail(&detail)
	return detail, nil
}

func normalizeIncidentCaseDetail(detail *IncidentCaseDetail) {
	if detail.Attempts == nil {
		detail.Attempts = []bughub.PhaseAttempt{}
	}
	if detail.Artifacts == nil {
		detail.Artifacts = []bughub.EvidenceArtifact{}
	}
	if detail.Approvals == nil {
		detail.Approvals = []bughub.Approval{}
	}
	if detail.CodeChanges == nil {
		detail.CodeChanges = []bughub.CodeChange{}
	}
	if detail.DeploymentObservations == nil {
		detail.DeploymentObservations = []bughub.DeploymentObservation{}
	}
	if detail.Events == nil {
		detail.Events = []bughub.TransitionEvent{}
	}
}

func (a *App) StartIncidentCase(input StartIncidentCaseInput) (bughub.IncidentCase, error) {
	if err := validateWorkflowCommandScalars(input.CaseID, input.ExpectedVersion, input.IdempotencyKey, input.ActorID); err != nil {
		return bughub.IncidentCase{}, err
	}
	_, orchestrator, err := a.workflowComponents()
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	bug, bot, err := a.loadIncidentContext(input.CaseID)
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	inputJSON, err := normalizeWorkflowJSON(input.InputJSON)
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	incident, err := orchestrator.StartCase(a.workflowCommandContext(), bughub.StartCaseCommand{CaseID: strings.TrimSpace(input.CaseID), ExpectedVersion: input.ExpectedVersion, IdempotencyKey: strings.TrimSpace(input.IdempotencyKey), ActorID: strings.TrimSpace(input.ActorID), Bug: bug, Bot: bot, InputJSON: inputJSON})
	a.emitIncidentResult(incident, err)
	return incident, err
}

func (a *App) ContinueIncidentCase(input ContinueIncidentCaseInput) (bughub.IncidentCase, error) {
	if err := validateWorkflowCommandScalars(input.CaseID, input.ExpectedVersion, input.IdempotencyKey, input.ActorID); err != nil {
		return bughub.IncidentCase{}, err
	}
	if input.Phase == "" {
		return bughub.IncidentCase{}, errors.New("phase is required")
	}
	_, orchestrator, err := a.workflowComponents()
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	bug, bot, err := a.loadIncidentContext(input.CaseID)
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	inputJSON, err := normalizeWorkflowJSON(input.InputJSON)
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	incident, err := orchestrator.ContinueWithEvidence(a.workflowCommandContext(), bughub.ContinueWithEvidenceCommand{CaseID: strings.TrimSpace(input.CaseID), ExpectedVersion: input.ExpectedVersion, IdempotencyKey: strings.TrimSpace(input.IdempotencyKey), ActorID: strings.TrimSpace(input.ActorID), Phase: input.Phase, Bug: bug, Bot: bot, InputJSON: inputJSON})
	a.emitIncidentResult(incident, err)
	return incident, err
}

func (a *App) ApproveIncidentFix(input ApproveIncidentFixInput) (bughub.IncidentCase, error) {
	if err := validateWorkflowCommandScalars(input.CaseID, input.ExpectedVersion, input.IdempotencyKey, input.ActorID); err != nil {
		return bughub.IncidentCase{}, err
	}
	if strings.TrimSpace(input.RootCauseAttemptID) == "" {
		return bughub.IncidentCase{}, errors.New("root_cause_attempt_id is required")
	}
	_, orchestrator, err := a.workflowComponents()
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	bug, bot, err := a.loadIncidentContext(input.CaseID)
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	inputJSON, err := normalizeWorkflowJSON(input.InputJSON)
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	incident, err := orchestrator.ApproveFix(a.workflowCommandContext(), bughub.ApproveFixCommand{CaseID: strings.TrimSpace(input.CaseID), ExpectedVersion: input.ExpectedVersion, IdempotencyKey: strings.TrimSpace(input.IdempotencyKey), ActorID: strings.TrimSpace(input.ActorID), RootCauseAttemptID: strings.TrimSpace(input.RootCauseAttemptID), Bug: bug, Bot: bot, InputJSON: inputJSON})
	a.emitIncidentResult(incident, err)
	return incident, err
}

func (a *App) ApproveIncidentMerge(input ApproveIncidentMergeInput) (bughub.IncidentCase, error) {
	if err := validateWorkflowCommandScalars(input.CaseID, input.ExpectedVersion, input.IdempotencyKey, input.ActorID); err != nil {
		return bughub.IncidentCase{}, err
	}
	_, orchestrator, err := a.workflowComponents()
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	incident, err := orchestrator.ApproveMerge(a.workflowCommandContext(), bughub.ApproveMergeCommand{CaseID: strings.TrimSpace(input.CaseID), ExpectedVersion: input.ExpectedVersion, IdempotencyKey: strings.TrimSpace(input.IdempotencyKey), ActorID: strings.TrimSpace(input.ActorID), FixCommits: input.FixCommits, TargetBranches: input.TargetBranches})
	a.emitIncidentResult(incident, err)
	return incident, err
}

func (a *App) NotifyIncidentDeployed(input NotifyIncidentDeployedInput) (bughub.IncidentCase, error) {
	if err := validateWorkflowCommandScalars(input.CaseID, input.ExpectedVersion, input.IdempotencyKey, input.ActorID); err != nil {
		return bughub.IncidentCase{}, err
	}
	_, orchestrator, err := a.workflowComponents()
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	bug, bot, err := a.loadIncidentContext(input.CaseID)
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	inputJSON, err := normalizeWorkflowJSON(input.InputJSON)
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	incident, err := orchestrator.NotifyDeployed(a.workflowCommandContext(), bughub.NotifyDeployedCommand{CaseID: strings.TrimSpace(input.CaseID), ExpectedVersion: input.ExpectedVersion, IdempotencyKey: strings.TrimSpace(input.IdempotencyKey), ActorID: strings.TrimSpace(input.ActorID), ObservedVersion: strings.TrimSpace(input.ObservedVersion), ObservedCommits: input.ObservedCommits, Bug: bug, Bot: bot, InputJSON: inputJSON})
	a.emitIncidentResult(incident, err)
	return incident, err
}

func (a *App) CancelIncidentAttempt(input CancelIncidentAttemptInput) (bughub.IncidentCase, error) {
	if err := validateWorkflowCommandScalars(input.CaseID, input.ExpectedVersion, input.IdempotencyKey, input.ActorID); err != nil {
		return bughub.IncidentCase{}, err
	}
	if strings.TrimSpace(input.AttemptID) == "" {
		return bughub.IncidentCase{}, errors.New("attempt_id is required")
	}
	_, orchestrator, err := a.workflowComponents()
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	incident, err := orchestrator.CancelAttempt(a.workflowCommandContext(), bughub.CancelAttemptCommand{CaseID: strings.TrimSpace(input.CaseID), AttemptID: strings.TrimSpace(input.AttemptID), ExpectedVersion: input.ExpectedVersion, IdempotencyKey: strings.TrimSpace(input.IdempotencyKey), ActorID: strings.TrimSpace(input.ActorID)})
	a.emitIncidentResult(incident, err)
	return incident, err
}

func validateWorkflowCommandScalars(caseID string, version int64, key, actor string) error {
	if strings.TrimSpace(caseID) == "" {
		return errors.New("case_id is required")
	}
	if version < 1 {
		return errors.New("expected_version must be positive")
	}
	if strings.TrimSpace(key) == "" {
		return errors.New("idempotency_key is required")
	}
	if strings.TrimSpace(actor) == "" {
		return errors.New("actor_id is required")
	}
	return nil
}

func normalizeWorkflowJSON(value map[string]any) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage(`{}`), nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode input_json: %w", err)
	}
	return encoded, nil
}

func (a *App) loadIncidentContext(caseID string) (bughub.Bug, bughub.BotRef, error) {
	store, _, err := a.workflowComponents()
	if err != nil {
		return bughub.Bug{}, bughub.BotRef{}, err
	}
	incident, err := store.GetCase(a.workflowCommandContext(), strings.TrimSpace(caseID))
	if err != nil {
		return bughub.Bug{}, bughub.BotRef{}, err
	}
	loadBug := a.workflowLoadBug
	if loadBug == nil {
		loadBug = func(id string) (bughub.Bug, error) {
			bug, found, getErr := bugStore().Get(id)
			if getErr != nil {
				return bughub.Bug{}, getErr
			}
			if !found {
				return bughub.Bug{}, os.ErrNotExist
			}
			return bug, nil
		}
	}
	bug, err := loadBug(incident.BugID)
	if err != nil {
		return bughub.Bug{}, bughub.BotRef{}, fmt.Errorf("load incident bug: %w", err)
	}
	loadBot := a.workflowLoadBot
	if loadBot == nil {
		loadBot = func(key string) (bughub.BotRef, error) {
			bots, listErr := a.bugBotRefs()
			if listErr != nil {
				return bughub.BotRef{}, listErr
			}
			for _, bot := range bots {
				if bot.Key == key {
					return bot, nil
				}
			}
			return bughub.BotRef{}, os.ErrNotExist
		}
	}
	bot, err := loadBot(incident.SelectedBotKey)
	if err != nil {
		return bughub.Bug{}, bughub.BotRef{}, fmt.Errorf("load incident bot: %w", err)
	}
	return bug, bot, nil
}

func (a *App) emitIncidentResult(incident bughub.IncidentCase, err error) {
	if err == nil && incident.ID != "" {
		a.emitIncidentCase(incident.ID)
	}
}

func (a *App) emitIncidentCase(caseID string) {
	detail, err := a.GetIncidentCase(caseID)
	if err == nil {
		a.emitWorkflowEvent(IncidentCaseEventPayload{Case: detail.Case, Snapshot: detail})
	}
}

func (a *App) emitIncidentPhaseEvent(caseID string, event bughub.InvestigationEvent) {
	if strings.TrimSpace(caseID) == "" {
		return
	}
	detail, err := a.GetIncidentCase(caseID)
	if err != nil {
		return
	}
	cloned := event
	a.emitWorkflowEvent(IncidentCaseEventPayload{Case: detail.Case, Snapshot: detail, PhaseEvent: &cloned})
}

func (a *App) emitWorkflowEvent(payload any) {
	if a.workflowEmit != nil {
		a.workflowEmit(incidentCaseEvent, payload)
		return
	}
	if ctx := a.getRuntimeContext(); ctx != nil {
		wailsruntime.EventsEmit(ctx, incidentCaseEvent, payload)
	}
}
