package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

const incidentCaseEvent = "incident-case:event"

type incidentWorkflowRuntime struct {
	orchestrator *bughub.CaseOrchestrator
	runner       *bughub.AgentPhaseRunner
	investigator *bughub.CodexInvestigator
}

type IncidentCaseDetail struct {
	Case                   bughub.IncidentCase            `json:"case"`
	Attempts               []IncidentPhaseAttempt         `json:"attempts"`
	Artifacts              []bughub.EvidenceArtifact      `json:"artifacts"`
	Approvals              []IncidentApproval             `json:"approvals"`
	CodeChanges            []IncidentCodeChange           `json:"code_changes"`
	DeploymentObservations []bughub.DeploymentObservation `json:"deployment_observations"`
	Events                 []IncidentTransitionEvent      `json:"events"`
}

type IncidentPhaseAttempt struct {
	ID              string               `json:"id"`
	CaseID          string               `json:"case_id"`
	CycleNumber     int                  `json:"cycle_number"`
	Phase           bughub.Phase         `json:"phase"`
	Mode            bughub.AttemptMode   `json:"mode"`
	Status          bughub.AttemptStatus `json:"status"`
	AgentTarget     string               `json:"agent_target"`
	BotKey          string               `json:"bot_key"`
	InputJSON       map[string]any       `json:"input_json"`
	OutputJSON      map[string]any       `json:"output_json"`
	ParentAttemptID string               `json:"parent_attempt_id"`
	StartedAt       time.Time            `json:"started_at"`
	FinishedAt      *time.Time           `json:"finished_at"`
	ErrorCode       string               `json:"error_code"`
	ErrorMessage    string               `json:"error_message"`
	Usage           bughub.AgentUsage    `json:"usage"`
}

type IncidentApproval struct {
	ID             string              `json:"id"`
	CaseID         string              `json:"case_id"`
	Kind           bughub.ApprovalKind `json:"kind"`
	Actor          string              `json:"actor"`
	ApprovedAt     time.Time           `json:"approved_at"`
	CaseVersion    int64               `json:"case_version"`
	ScopeJSON      map[string]any      `json:"scope_json"`
	FixCommits     map[string]string   `json:"fix_commits"`
	TargetBranches map[string]string   `json:"target_branches"`
}

type IncidentCodeChange struct {
	ID                      string `json:"id"`
	CaseID                  string `json:"case_id"`
	AttemptID               string `json:"attempt_id"`
	Repo                    string `json:"repo"`
	BaseBranch              string `json:"base_branch"`
	FixBranch               string `json:"fix_branch"`
	FixCommit               string `json:"fix_commit"`
	TestEvidence            any    `json:"test_evidence"`
	TargetEnvironmentBranch string `json:"target_environment_branch"`
	MergeBaseHead           string `json:"merge_base_head"`
	MergeCommit             string `json:"merge_commit"`
	PushRemote              string `json:"push_remote"`
	PushStatus              string `json:"push_status"`
}

type IncidentTransitionEvent struct {
	ID             string            `json:"id"`
	CaseID         string            `json:"case_id"`
	FromStatus     bughub.CaseStatus `json:"from_status"`
	ToStatus       bughub.CaseStatus `json:"to_status"`
	EventType      string            `json:"event_type"`
	ActorType      string            `json:"actor_type"`
	ActorID        string            `json:"actor_id"`
	IdempotencyKey string            `json:"idempotency_key"`
	PayloadJSON    map[string]any    `json:"payload_json"`
	CreatedAt      time.Time         `json:"created_at"`
}

// IncidentCaseEventPayload carries either a versioned snapshot (so the Web UI
// can discard stale/out-of-order events) or an observable startup error.
// PhaseEvent is present only for live Agent progress.
type IncidentCaseEventPayload struct {
	Kind       string                        `json:"kind"`
	Case       *bughub.IncidentCase          `json:"case,omitempty"`
	Snapshot   *IncidentCaseDetail           `json:"snapshot,omitempty"`
	PhaseEvent *bughub.InvestigationEvent    `json:"phase_event,omitempty"`
	Error      *IncidentWorkflowStartupError `json:"error,omitempty"`
}

type IncidentWorkflowStartupError struct {
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type StartIncidentCaseInput struct {
	CaseID          string         `json:"case_id"`
	BugID           string         `json:"bug_id,omitempty"`
	BotKey          string         `json:"bot_key,omitempty"`
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
	TargetHeads     map[string]string `json:"target_heads"`
}

type NotifyIncidentDeployedInput struct {
	CaseID           string            `json:"case_id"`
	ExpectedVersion  int64             `json:"expected_version"`
	IdempotencyKey   string            `json:"idempotency_key"`
	ActorID          string            `json:"actor_id"`
	ObservedVersion  string            `json:"observed_version"`
	ObservedCommits  map[string]string `json:"observed_commits,omitempty"`
	VersionSource    string            `json:"version_source,omitempty"`
	NotificationText string            `json:"notification_text,omitempty"`
	InputJSON        map[string]any    `json:"input_json,omitempty"`
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
		if a.workflowInitErr == nil {
			return nil
		}
		if recoverErr := a.workflowOrchestrator.RecoverInterrupted(workflowContext(ctx)); recoverErr != nil {
			a.workflowInitErr = recoverErr
			return recoverErr
		}
		a.workflowInitErr = nil
		return nil
	}
	// Initialization errors are observable but not sticky: a later command can
	// retry after a transient filesystem or migration issue is corrected.
	a.workflowInitErr = nil
	root := strings.TrimSpace(a.workflowRoot)
	if root == "" {
		root = bughub.DefaultRoot()
	}
	store, err := bughub.OpenCaseStore(filepath.Join(root, "workflows.db"))
	if err != nil {
		a.workflowInitErr = err
		return err
	}
	legacy := bughub.NewInvestigationStore(root)
	if _, statErr := os.Stat(legacy.Path()); statErr == nil {
		if _, importErr := bughub.ImportLegacyRuns(workflowContext(ctx), store, legacy.Path()); importErr != nil {
			a.workflowInitErr = importErr
			_ = store.Close()
			return importErr
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		a.workflowInitErr = statErr
		_ = store.Close()
		return statErr
	}
	runtime := incidentWorkflowRuntime{}
	if a.workflowRuntimeFactory != nil {
		runtime = a.workflowRuntimeFactory(store, legacy)
	} else {
		investigator := bughub.NewCodexInvestigator(legacy, "codex")
		runner := bughub.NewAgentPhaseRunner(store, investigator, legacy, filepath.Join(root, "artifacts"), nil)
		gitService := bughub.NewGitIntegrationService(filepath.Join(root, "git-worktrees"), func(ctx context.Context, caseID, repo string) (string, error) {
			incident, loadErr := store.GetCase(ctx, caseID)
			if loadErr != nil {
				return "", loadErr
			}
			path := strings.TrimSpace(userconfig.GetRepoPathsForSystem(incident.SystemID)[repo])
			if path == "" {
				return "", fmt.Errorf("repository %s has no configured local path for system %s", repo, incident.SystemID)
			}
			return filepath.Clean(path), nil
		})
		deploymentVerifier := bughub.NewCompositeDeploymentVerifier(map[string]bughub.DeploymentVerifier{
			"manual": bughub.ManualVersionVerifier{},
		})
		orchestrator := bughub.NewCaseOrchestrator(store, runner, gitService, deploymentVerifier)
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
		runtime = incidentWorkflowRuntime{orchestrator: orchestrator, runner: runner, investigator: investigator}
	}
	if runtime.orchestrator == nil {
		runtimeErr := errors.New("incident workflow runtime requires an orchestrator")
		a.workflowInitErr = runtimeErr
		_ = store.Close()
		return runtimeErr
	}
	runtime.orchestrator.SetRecoveryContextResolver(bughub.RecoveryContextResolverFunc(a.resolveIncidentRecoveryContext))
	a.workflowStore = store
	a.workflowOrchestrator = runtime.orchestrator
	a.workflowRunner = runtime.runner
	if runtime.investigator != nil {
		a.bugInvestigationMu.Lock()
		a.bugInvestigator = runtime.investigator
		a.bugInvestigationMu.Unlock()
	}
	if recoverErr := runtime.orchestrator.RecoverInterrupted(workflowContext(ctx)); recoverErr != nil {
		a.workflowInitErr = recoverErr
		return recoverErr
	}
	a.workflowInitErr = nil
	return nil
}

func (a *App) startIncidentWorkflow(ctx context.Context) error {
	err := a.initializeIncidentWorkflow(workflowContext(ctx))
	if err == nil {
		return nil
	}
	fmt.Fprintf(os.Stderr, "[warn] incident workflow startup failed: %v\n", err)
	a.emitWorkflowEvent(IncidentCaseEventPayload{Kind: "startup_error", Error: &IncidentWorkflowStartupError{Message: err.Error(), Retryable: true}})
	return err
}

func (a *App) resolveIncidentRecoveryContext(_ context.Context, incident bughub.IncidentCase, attempt bughub.PhaseAttempt) (bughub.Bug, bughub.BotRef, error) {
	return a.loadBugAndBot(incident.BugID, attempt.BotKey)
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
	if err := a.startIncidentWorkflow(a.workflowCommandContext()); err != nil {
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
	attempts, err := store.ListAttempts(ctx, bughub.AttemptFilter{CaseID: caseID})
	if err != nil {
		return IncidentCaseDetail{}, err
	}
	if detail.Attempts, err = incidentPhaseAttempts(attempts); err != nil {
		return IncidentCaseDetail{}, err
	}
	if detail.Artifacts, err = store.ListEvidenceArtifacts(ctx, caseID); err != nil {
		return IncidentCaseDetail{}, err
	}
	approvals, err := store.ListApprovals(ctx, caseID)
	if err != nil {
		return IncidentCaseDetail{}, err
	}
	if detail.Approvals, err = incidentApprovals(approvals); err != nil {
		return IncidentCaseDetail{}, err
	}
	changes, err := store.ListCodeChanges(ctx, caseID)
	if err != nil {
		return IncidentCaseDetail{}, err
	}
	if detail.CodeChanges, err = incidentCodeChanges(changes); err != nil {
		return IncidentCaseDetail{}, err
	}
	if detail.DeploymentObservations, err = store.ListDeploymentObservations(ctx, caseID); err != nil {
		return IncidentCaseDetail{}, err
	}
	events, err := store.ListEvents(ctx, caseID)
	if err != nil {
		return IncidentCaseDetail{}, err
	}
	if detail.Events, err = incidentTransitionEvents(events); err != nil {
		return IncidentCaseDetail{}, err
	}
	normalizeIncidentCaseDetail(&detail)
	return detail, nil
}

func normalizeIncidentCaseDetail(detail *IncidentCaseDetail) {
	if detail.Attempts == nil {
		detail.Attempts = []IncidentPhaseAttempt{}
	}
	if detail.Artifacts == nil {
		detail.Artifacts = []bughub.EvidenceArtifact{}
	}
	if detail.Approvals == nil {
		detail.Approvals = []IncidentApproval{}
	}
	if detail.CodeChanges == nil {
		detail.CodeChanges = []IncidentCodeChange{}
	}
	if detail.DeploymentObservations == nil {
		detail.DeploymentObservations = []bughub.DeploymentObservation{}
	}
	if detail.Events == nil {
		detail.Events = []IncidentTransitionEvent{}
	}
}

func incidentJSONObject(raw json.RawMessage) (map[string]any, error) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, errors.New("workflow JSON payload must be an object")
	}
	return value, nil
}

func incidentJSONValue(raw json.RawMessage) (any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func incidentPhaseAttempts(items []bughub.PhaseAttempt) ([]IncidentPhaseAttempt, error) {
	out := make([]IncidentPhaseAttempt, 0, len(items))
	for _, item := range items {
		input, err := incidentJSONObject(item.InputJSON)
		if err != nil {
			return nil, err
		}
		output, err := incidentJSONObject(item.OutputJSON)
		if err != nil {
			return nil, err
		}
		out = append(out, IncidentPhaseAttempt{ID: item.ID, CaseID: item.CaseID, CycleNumber: item.CycleNumber, Phase: item.Phase, Mode: item.Mode, Status: item.Status, AgentTarget: item.AgentTarget, BotKey: item.BotKey, InputJSON: input, OutputJSON: output, ParentAttemptID: item.ParentAttemptID, StartedAt: item.StartedAt, FinishedAt: item.FinishedAt, ErrorCode: item.ErrorCode, ErrorMessage: item.ErrorMessage, Usage: item.Usage})
	}
	return out, nil
}

func incidentApprovals(items []bughub.Approval) ([]IncidentApproval, error) {
	out := make([]IncidentApproval, 0, len(items))
	for _, item := range items {
		scope, err := incidentJSONObject(item.ScopeJSON)
		if err != nil {
			return nil, err
		}
		out = append(out, IncidentApproval{ID: item.ID, CaseID: item.CaseID, Kind: item.Kind, Actor: item.Actor, ApprovedAt: item.ApprovedAt, CaseVersion: item.CaseVersion, ScopeJSON: scope, FixCommits: item.FixCommits, TargetBranches: item.TargetBranches})
	}
	return out, nil
}

func incidentCodeChanges(items []bughub.CodeChange) ([]IncidentCodeChange, error) {
	out := make([]IncidentCodeChange, 0, len(items))
	for _, item := range items {
		testEvidence, err := incidentJSONValue(item.TestEvidence)
		if err != nil {
			return nil, err
		}
		out = append(out, IncidentCodeChange{ID: item.ID, CaseID: item.CaseID, AttemptID: item.AttemptID, Repo: item.Repo, BaseBranch: item.BaseBranch, FixBranch: item.FixBranch, FixCommit: item.FixCommit, TestEvidence: testEvidence, TargetEnvironmentBranch: item.TargetEnvironmentBranch, MergeBaseHead: item.MergeBaseHead, MergeCommit: item.MergeCommit, PushRemote: item.PushRemote, PushStatus: item.PushStatus})
	}
	return out, nil
}

func incidentTransitionEvents(items []bughub.TransitionEvent) ([]IncidentTransitionEvent, error) {
	out := make([]IncidentTransitionEvent, 0, len(items))
	for _, item := range items {
		payload, err := incidentJSONObject(item.PayloadJSON)
		if err != nil {
			return nil, err
		}
		out = append(out, IncidentTransitionEvent{ID: item.ID, CaseID: item.CaseID, FromStatus: item.FromStatus, ToStatus: item.ToStatus, EventType: item.EventType, ActorType: item.ActorType, ActorID: item.ActorID, IdempotencyKey: item.IdempotencyKey, PayloadJSON: payload, CreatedAt: item.CreatedAt})
	}
	return out, nil
}

func (a *App) StartIncidentCase(input StartIncidentCaseInput) (bughub.IncidentCase, error) {
	if err := validateWorkflowStartScalars(input.CaseID, input.ExpectedVersion, input.IdempotencyKey, input.ActorID); err != nil {
		return bughub.IncidentCase{}, err
	}
	_, orchestrator, err := a.workflowComponents()
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	bug, bot, err := a.loadIncidentStartContext(input)
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	inputJSON, err := normalizeWorkflowJSON(input.InputJSON)
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	incident, err := orchestrator.CreateAndStartCase(a.workflowCommandContext(), bughub.CreateAndStartCaseCommand{CaseID: strings.TrimSpace(input.CaseID), ExpectedVersion: input.ExpectedVersion, IdempotencyKey: strings.TrimSpace(input.IdempotencyKey), ActorID: strings.TrimSpace(input.ActorID), Bug: bug, Bot: bot, InputJSON: inputJSON})
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
	expectedKey := bughub.StartFixApprovalKey(strings.TrimSpace(input.CaseID), strings.TrimSpace(input.RootCauseAttemptID), input.ExpectedVersion)
	if strings.TrimSpace(input.IdempotencyKey) != expectedKey {
		return bughub.IncidentCase{}, errors.New("start-fix approval key does not match the dialog snapshot scope")
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
	incident, err := orchestrator.ApproveMerge(a.workflowCommandContext(), bughub.ApproveMergeCommand{CaseID: strings.TrimSpace(input.CaseID), ExpectedVersion: input.ExpectedVersion, IdempotencyKey: strings.TrimSpace(input.IdempotencyKey), ActorID: strings.TrimSpace(input.ActorID), FixCommits: input.FixCommits, TargetBranches: input.TargetBranches, TargetHeads: input.TargetHeads})
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
	command := bughub.NotifyDeployedCommand{CaseID: strings.TrimSpace(input.CaseID), ExpectedVersion: input.ExpectedVersion, IdempotencyKey: strings.TrimSpace(input.IdempotencyKey), ActorID: strings.TrimSpace(input.ActorID), ObservedVersion: strings.TrimSpace(input.ObservedVersion), ObservedCommits: input.ObservedCommits, Source: strings.TrimSpace(input.VersionSource), Bug: bug, Bot: bot, InputJSON: inputJSON}
	var incident bughub.IncidentCase
	if strings.TrimSpace(input.NotificationText) != "" {
		incident, err = orchestrator.NotifyDeployedFromText(a.workflowCommandContext(), input.NotificationText, command)
	} else {
		incident, err = orchestrator.NotifyDeployed(a.workflowCommandContext(), command)
	}
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
	if err := validateWorkflowStartScalars(caseID, version, key, actor); err != nil {
		return err
	}
	if version < 1 {
		return errors.New("expected_version must be positive")
	}
	return nil
}

func validateWorkflowStartScalars(caseID string, version int64, key, actor string) error {
	if strings.TrimSpace(caseID) == "" {
		return errors.New("case_id is required")
	}
	if version < 0 {
		return errors.New("expected_version must not be negative")
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
	return a.loadBugAndBot(incident.BugID, incident.SelectedBotKey)
}

func (a *App) loadIncidentStartContext(input StartIncidentCaseInput) (bughub.Bug, bughub.BotRef, error) {
	store, _, err := a.workflowComponents()
	if err != nil {
		return bughub.Bug{}, bughub.BotRef{}, err
	}
	bugID := strings.TrimSpace(input.BugID)
	botKey := strings.TrimSpace(input.BotKey)
	incident, getErr := store.GetCase(a.workflowCommandContext(), strings.TrimSpace(input.CaseID))
	if getErr == nil {
		if bugID != "" && bugID != incident.BugID {
			return bughub.Bug{}, bughub.BotRef{}, errors.New("bug_id does not match existing Case")
		}
		bugID = incident.BugID
		if incident.SelectedBotKey != "" {
			if botKey != "" && botKey != incident.SelectedBotKey {
				return bughub.Bug{}, bughub.BotRef{}, errors.New("bot_key does not match existing Case")
			}
			botKey = incident.SelectedBotKey
		}
	} else if !errors.Is(getErr, bughub.ErrCaseNotFound) {
		return bughub.Bug{}, bughub.BotRef{}, getErr
	}
	if bugID == "" || botKey == "" {
		return bughub.Bug{}, bughub.BotRef{}, errors.New("bug_id and bot_key are required when creating or continuing a Case")
	}
	return a.loadBugAndBot(bugID, botKey)
}

func (a *App) loadBugAndBot(bugID, botKey string) (bughub.Bug, bughub.BotRef, error) {
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
	bug, err := loadBug(bugID)
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
	bot, err := loadBot(botKey)
	if err != nil {
		return bughub.Bug{}, bughub.BotRef{}, fmt.Errorf("load incident bot: %w", err)
	}
	return bug, bot, nil
}

func (a *App) emitIncidentResult(incident bughub.IncidentCase, _ error) {
	// Some guarded external actions persist a new inspection snapshot while
	// returning a typed error (for example a stale merge approval). Emit that
	// durable snapshot so the dialog can request a fresh, exact authorization.
	if incident.ID != "" {
		a.emitIncidentCase(incident.ID)
	}
}

func (a *App) emitIncidentCase(caseID string) {
	detail, err := a.GetIncidentCase(caseID)
	if err == nil {
		incident := detail.Case
		a.emitWorkflowEvent(IncidentCaseEventPayload{Kind: "snapshot", Case: &incident, Snapshot: &detail})
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
	incident := detail.Case
	a.emitWorkflowEvent(IncidentCaseEventPayload{Kind: "snapshot", Case: &incident, Snapshot: &detail, PhaseEvent: &cloned})
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
