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

	"github.com/xiaolong/troubleshooter-studio/internal/analyzer"
	"github.com/xiaolong/troubleshooter-studio/internal/bughub"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

const (
	incidentCaseEvent              = "incident-case:event"
	bugTicketResolutionEventPrefix = "bug-ticket-resolution:"
)

var incidentBugResolutionPollInterval = time.Minute

type incidentWorkflowRuntime struct {
	orchestrator *bughub.CaseOrchestrator
	runner       *bughub.AgentPhaseRunner
	investigator *bughub.CodexInvestigator
}

type IncidentCaseDetail struct {
	Case                   bughub.IncidentCase            `json:"case"`
	Attempts               []IncidentPhaseAttempt         `json:"attempts"`
	PhaseEvents            []bughub.InvestigationEvent    `json:"phase_events"`
	Artifacts              []IncidentArtifact             `json:"artifacts"`
	Approvals              []IncidentApproval             `json:"approvals"`
	CodeChanges            []IncidentCodeChange           `json:"code_changes"`
	DeploymentObservations []bughub.DeploymentObservation `json:"deployment_observations"`
	Events                 []IncidentTransitionEvent      `json:"events"`
	BugTicketResolution    IncidentBugTicketResolution    `json:"bug_ticket_resolution"`
}

type IncidentBugTicketResolution struct {
	State        string `json:"state"`
	SourceStatus string `json:"source_status,omitempty"`
}

type IncidentArtifact struct {
	ID          string    `json:"id"`
	CaseID      string    `json:"case_id"`
	AttemptID   string    `json:"attempt_id"`
	Kind        string    `json:"kind"`
	SHA256      string    `json:"sha256"`
	Size        int64     `json:"size"`
	CapturedAt  time.Time `json:"captured_at"`
	Environment string    `json:"environment"`
	Version     string    `json:"version"`
	RequestID   string    `json:"request_id"`
	TraceID     string    `json:"trace_id"`
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
	BotEnvironment  string         `json:"bot_environment,omitempty"`
	ExpectedVersion int64          `json:"expected_version"`
	IdempotencyKey  string         `json:"idempotency_key"`
	ActorID         string         `json:"actor_id"`
	InputJSON       map[string]any `json:"input_json,omitempty"`
}

type ResetIncidentCaseInput struct {
	CaseID          string         `json:"case_id"`
	NewCaseID       string         `json:"new_case_id"`
	BotKey          string         `json:"bot_key"`
	BotEnvironment  string         `json:"bot_environment,omitempty"`
	ExpectedVersion int64          `json:"expected_version"`
	IdempotencyKey  string         `json:"idempotency_key"`
	ActorID         string         `json:"actor_id"`
	InputJSON       map[string]any `json:"input_json,omitempty"`
}

type DeleteIncidentHistoryInput struct {
	CaseID string `json:"case_id"`
	BugID  string `json:"bug_id"`
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

type ReconsiderIncidentRemediationInput struct {
	CaseID             string `json:"case_id"`
	ExpectedVersion    int64  `json:"expected_version"`
	IdempotencyKey     string `json:"idempotency_key"`
	ActorID            string `json:"actor_id"`
	RootCauseAttemptID string `json:"root_cause_attempt_id"`
	Proposal           string `json:"proposal"`
}

type DisputeIncidentRootCauseInput struct {
	CaseID              string   `json:"case_id"`
	ExpectedVersion     int64    `json:"expected_version"`
	IdempotencyKey      string   `json:"idempotency_key"`
	ActorID             string   `json:"actor_id"`
	RootCauseAttemptID  string   `json:"root_cause_attempt_id"`
	Reason              string   `json:"reason"`
	EvidenceArtifactIDs []string `json:"evidence_artifact_ids,omitempty"`
}

type CompleteIncidentRemediationInput struct {
	CaseID             string `json:"case_id"`
	ExpectedVersion    int64  `json:"expected_version"`
	IdempotencyKey     string `json:"idempotency_key"`
	ActorID            string `json:"actor_id"`
	RootCauseAttemptID string `json:"root_cause_attempt_id"`
	Summary            string `json:"summary"`
	Evidence           string `json:"evidence"`
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
		if a.workflowInitErr == nil && !a.workflowRecoveryPending {
			return nil
		}

		if recoverErr := a.workflowOrchestrator.RecoverInterrupted(workflowContext(ctx)); recoverErr != nil {
			a.workflowInitErr = recoverErr
			return recoverErr
		}
		a.workflowInitErr = nil
		a.workflowRecoveryPending = false
		return nil
	}
	// Initialization errors are observable but not sticky: a later command can
	// retry after a transient filesystem or migration issue is corrected.
	a.workflowInitErr = nil
	root := strings.TrimSpace(a.workflowRoot)
	if root == "" {
		root = bughub.DefaultRoot()
	}
	a.workflowRoot = root
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
		resolveRepositoryPath := func(ctx context.Context, caseID, repo string) (string, error) {
			incident, loadErr := store.GetCase(ctx, caseID)
			if loadErr != nil {
				return "", loadErr
			}
			path := strings.TrimSpace(userconfig.GetRepoPathsForSystem(incident.SystemID)[repo])
			if path == "" {
				return "", fmt.Errorf("repository %s has no configured local path for system %s", repo, incident.SystemID)
			}
			return filepath.Clean(path), nil
		}
		gitService := bughub.NewGitIntegrationService(filepath.Join(root, "git-worktrees"), resolveRepositoryPath)
		runner.SetFixWorkspaceManager(bughub.NewFixWorkspaceManager(filepath.Join(root, "fix-worktrees"), resolveRepositoryPath))
		runner.SetRepositoryAccessResolver(bughub.RepositoryAccessResolverFunc(func(_ context.Context, incident bughub.IncidentCase) (map[string]string, error) {
			paths := userconfig.GetRepoPathsForSystem(incident.SystemID)
			result := make(map[string]string, len(paths))
			for repo, path := range paths {
				if path = strings.TrimSpace(path); path != "" {
					result[repo] = filepath.Clean(path)
				}
			}
			return result, nil
		}))
		orchestrator := bughub.NewCaseOrchestrator(store, runner, gitService)
		runner.SetCompletionCallback(func(callbackCtx context.Context, command bughub.CompleteAttemptCommand) error {
			incident, completeErr := orchestrator.CompleteAttempt(workflowContext(callbackCtx), command)
			if incident.ID != "" {
				a.emitIncidentCase(incident.ID)
			}

			if errors.Is(completeErr, bughub.ErrFixInspectionUnavailable) {
				// Make the next workflow command run the same bounded durable recovery
				// pass. This retries remote inspection without rerunning the fixer and
				// does not require restarting Studio.
				a.workflowMu.Lock()
				a.workflowInitErr = completeErr
				a.workflowMu.Unlock()
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
	if runtime.runner != nil {
		runtime.runner.SetFrontendRuntimeResolver(caseFrontendRuntimeResolver{app: a})
		runtime.runner.SetCodeIntelligenceResolver(caseCodeIntelligenceResolver{app: a})
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
	a.workflowRecoveryPending = false
	return nil
}

func (a *App) startIncidentWorkflow(ctx context.Context) error {
	err := a.initializeIncidentWorkflow(workflowContext(ctx))
	if err == nil {
		if runtimeCtx := a.getRuntimeContext(); runtimeCtx != nil {
		}
		return nil
	}
	fmt.Fprintf(os.Stderr, "[warn] incident workflow startup failed: %v\n", err)
	a.emitWorkflowEvent(IncidentCaseEventPayload{Kind: "startup_error", Error: &IncidentWorkflowStartupError{Message: err.Error(), Retryable: true}})
	return err
}

func fetchZentaoBugWithSessionRecovery(platform bughub.PlatformConfig, sourceID string) (bughub.Bug, error) {
	run := func(current bughub.PlatformConfig) (bughub.Bug, error) {
		return (bughub.ZentaoClient{
			BaseURL: current.BaseURL, Account: current.Account, AuthMode: current.AuthMode,
			SessionHeader: current.SessionHeader, Password: current.Password, Token: current.Token,
		}).FetchByID(sourceID)
	}
	current, err := run(platform)
	if err != nil && shouldRecoverZentaoSession(platform, err) {
		if refreshed, ok := refreshZentaoSession(platform); ok {
			current, err = run(refreshed)
		}
	}
	if err != nil && clearExpiredZentaoSession(platform, err) {
		return bughub.Bug{}, zentaoSessionExpiredError(err)
	}
	return current, err
}

func zentaoTicketResolutionComplete(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "resolved", "closed":
		return true
	default:
		return false
	}
}

func (a *App) resolveIncidentRecoveryContext(_ context.Context, incident bughub.IncidentCase, attempt bughub.PhaseAttempt) (bughub.Bug, bughub.BotRef, error) {
	bug, bot, err := a.loadBugAndBot(incident.BugID, attempt.BotKey)
	if err != nil {
		return bughub.Bug{}, bughub.BotRef{}, err
	}
	bot.Env = strings.TrimSpace(incident.Environment)
	if incident.FrontendEntry.IsZero() {
	} else {
	}
	return bug, bot, nil
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

// DeleteIncidentHistory removes all terminal Case history for one Bug while
// preserving the archived Bug snapshot. Supplying both identities makes a
// repeated request idempotent without allowing a stale Case ID to delete a
// different Bug's history.
func (a *App) DeleteIncidentHistory(input DeleteIncidentHistoryInput) (bughub.CaseHistoryDeleteResult, error) {
	caseID := strings.TrimSpace(input.CaseID)
	bugID := strings.TrimSpace(input.BugID)
	if caseID == "" || bugID == "" {
		return bughub.CaseHistoryDeleteResult{}, errors.New("case_id and bug_id are required")
	}
	store, _, err := a.workflowComponents()
	if err != nil {
		return bughub.CaseHistoryDeleteResult{}, err
	}
	incident, err := store.GetCase(a.workflowCommandContext(), caseID)
	if err == nil {
		if incident.BugID != bugID {
			return bughub.CaseHistoryDeleteResult{}, errors.New("Case does not belong to the requested Bug")
		}
		if !bughub.IsTerminalCaseStatus(incident.Status) {
			return bughub.CaseHistoryDeleteResult{}, bughub.ErrActiveCaseHistory
		}
	} else if !errors.Is(err, bughub.ErrCaseNotFound) {
		return bughub.CaseHistoryDeleteResult{}, err
	} else {
		// A replay after a successful deletion is a no-op because no Cases for
		// this Bug remain. If other Cases still exist, the stale/unknown Case ID
		// must not authorize deleting their history.
		cases, listErr := store.ListCases(a.workflowCommandContext())
		if listErr != nil {
			return bughub.CaseHistoryDeleteResult{}, listErr
		}
		for _, current := range cases {
			if current.BugID == bugID {
				return bughub.CaseHistoryDeleteResult{}, errors.New("Case was not found for the requested Bug")
			}
		}
	}
	return bughub.DeleteTerminalCaseHistoryForBug(
		a.workflowCommandContext(),
		store,
		filepath.Join(a.workflowRoot, "artifacts"),
		bugID,
	)
}

func (a *App) GetIncidentWorkflowMetrics() (bughub.WorkflowMetrics, error) {
	store, _, err := a.workflowComponents()
	if err != nil {
		return bughub.WorkflowMetrics{}, err
	}
	return store.WorkflowMetrics(a.workflowCommandContext(), time.Now().UTC())
}

func (a *App) resolveIncidentProductionEnvironment(ctx context.Context, incident bughub.IncidentCase) (bool, error) {
	loader := a.workflowLoadDeploymentConfig
	if loader == nil {
		loader = a.loadInstalledIncidentConfig
	}
	cfg, err := loader(ctx, incident)
	if err != nil || cfg == nil {
		return false, errors.New("incident environment configuration unavailable")
	}
	if strings.TrimSpace(incident.SystemID) == "" || cfg.System.ID != incident.SystemID {
		return false, errors.New("incident environment system does not match configuration")
	}
	for _, environment := range cfg.Environments {
		if environment.ID == incident.Environment {
			return environment.IsProd, nil
		}
	}
	return false, errors.New("incident environment is absent from configuration")
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
	detail.BugTicketResolution = a.incidentBugTicketResolution(incident)
	attempts, err := store.ListAttempts(ctx, bughub.AttemptFilter{CaseID: caseID})
	if err != nil {
		return IncidentCaseDetail{}, err
	}
	if detail.Attempts, err = incidentPhaseAttempts(attempts, bughub.NewInvestigationStore(a.workflowRoot)); err != nil {
		return IncidentCaseDetail{}, err
	}
	detail.PhaseEvents = incidentPhaseEventsForDetail(a.workflowRoot, incident)
	artifacts, err := store.ListEvidenceArtifacts(ctx, caseID)
	if err != nil {
		return IncidentCaseDetail{}, err
	}
	if detail.Artifacts, err = incidentArtifacts(ctx, store, filepath.Join(a.workflowRoot, "artifacts"), caseID, artifacts); err != nil {
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

func (a *App) incidentBugTicketResolution(incident bughub.IncidentCase) IncidentBugTicketResolution {
	if incident.Status != bughub.CaseFixedVerified {
		return IncidentBugTicketResolution{State: "not_ready"}
	}
	var bug bughub.Bug
	if a.workflowLoadBug != nil {
		loaded, err := a.workflowLoadBug(incident.BugID)
		if err != nil {
			return IncidentBugTicketResolution{State: "unknown"}
		}
		bug = loaded
	} else {
		loaded, found, err := bugStore().Get(incident.BugID)
		if err != nil || !found {
			return IncidentBugTicketResolution{State: "unknown"}
		}
		bug = loaded
	}
	state := "pending"
	if zentaoTicketResolutionComplete(bug.Status) {
		state = "resolved"
	}
	return IncidentBugTicketResolution{State: state, SourceStatus: strings.TrimSpace(bug.Status)}
}

func normalizeIncidentCaseDetail(detail *IncidentCaseDetail) {
	if detail.Attempts == nil {
		detail.Attempts = []IncidentPhaseAttempt{}
	}
	if detail.PhaseEvents == nil {
		detail.PhaseEvents = []bughub.InvestigationEvent{}
	}
	if detail.Artifacts == nil {
		detail.Artifacts = []IncidentArtifact{}
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

func incidentArtifacts(ctx context.Context, store *bughub.CaseStore, artifactsRoot, caseID string, items []bughub.EvidenceArtifact) ([]IncidentArtifact, error) {
	out := make([]IncidentArtifact, 0, len(items))
	for _, item := range items {
		verified, err := bughub.ReadEvidenceArtifactFromRoot(ctx, store, artifactsRoot, caseID, item.ID)
		if err != nil {
			return nil, err
		}
		artifact := verified.Artifact
		out = append(out, IncidentArtifact{
			ID: artifact.ID, CaseID: artifact.CaseID, AttemptID: artifact.AttemptID,
			Kind: artifact.Kind, SHA256: artifact.SHA256, Size: int64(len(verified.Content)),
			CapturedAt: artifact.CapturedAt, Environment: artifact.Environment, Version: artifact.Version,
			RequestID: artifact.RequestID, TraceID: artifact.TraceID,
		})
	}
	return out, nil
}

func incidentPublicJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, nested := range typed {
			if key == "application_url" || key == "path_or_reference" {
				continue
			}
			out[key] = incidentPublicJSONValue(nested)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for index, nested := range typed {
			out[index] = incidentPublicJSONValue(nested)
		}
		return out
	default:
		return value
	}
}

func incidentPublicJSONObject(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	return incidentPublicJSONValue(value).(map[string]any)
}

func incidentPhaseAttempts(items []bughub.PhaseAttempt, legacy *bughub.InvestigationStore) ([]IncidentPhaseAttempt, error) {
	out := make([]IncidentPhaseAttempt, 0, len(items))
	for _, item := range items {
		input, err := incidentJSONObject(item.InputJSON)
		if err != nil {
			return nil, err
		}
		outputJSON := incidentLegacyInvestigationOutput(item, legacy)
		output, err := incidentJSONObject(outputJSON)
		if err != nil {
			return nil, err
		}
		out = append(out, IncidentPhaseAttempt{ID: item.ID, CaseID: item.CaseID, CycleNumber: item.CycleNumber, Phase: item.Phase, Mode: item.Mode, Status: item.Status, AgentTarget: item.AgentTarget, BotKey: item.BotKey, InputJSON: input, OutputJSON: incidentPublicJSONObject(output), ParentAttemptID: item.ParentAttemptID, StartedAt: item.StartedAt, FinishedAt: item.FinishedAt, ErrorCode: item.ErrorCode, ErrorMessage: item.ErrorMessage, Usage: item.Usage})
	}
	return out, nil
}

func incidentLegacyInvestigationOutput(item bughub.PhaseAttempt, legacy *bughub.InvestigationStore) json.RawMessage {
	if legacy == nil || item.Phase != bughub.PhaseInvestigation || item.Status != bughub.AttemptStatusFailed ||
		strings.TrimSpace(item.ErrorCode) != "invalid_phase_result" {
		return item.OutputJSON
	}
	run, err := legacy.Get(item.ID)
	if err != nil {
		return item.OutputJSON
	}
	projection, ok := bughub.SafeLegacyInvestigationProjection([]byte(run.FinalMessage))
	if !ok {
		return item.OutputJSON
	}
	return projection
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
		out = append(out, IncidentTransitionEvent{ID: item.ID, CaseID: item.CaseID, FromStatus: item.FromStatus, ToStatus: item.ToStatus, EventType: item.EventType, ActorType: item.ActorType, ActorID: item.ActorID, IdempotencyKey: item.IdempotencyKey, PayloadJSON: incidentPublicJSONObject(payload), CreatedAt: item.CreatedAt})
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
	inputJSON, err := normalizeWorkflowInputEnvironment(input.InputJSON, bot.Env, strings.TrimSpace(input.BotEnvironment) != "")
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	incident, err := orchestrator.CreateAndStartCase(a.workflowCommandContext(), bughub.CreateAndStartCaseCommand{CaseID: strings.TrimSpace(input.CaseID), ExpectedVersion: input.ExpectedVersion, IdempotencyKey: strings.TrimSpace(input.IdempotencyKey), ActorID: strings.TrimSpace(input.ActorID), Bug: bug, Bot: bot, InputJSON: inputJSON})
	a.emitIncidentResult(incident, err)
	return incident, err
}

func (a *App) ResetIncidentCase(input ResetIncidentCaseInput) (bughub.IncidentCase, error) {
	result, err := a.resetIncidentCaseWithWarnings(input)
	if err == nil && workflowWarningCodePresent(result.Warnings, "reset_replacement_start_failed") {
		err = errors.New("replacement Case phase start failed; retry investigation from the preserved Case")
	}
	return result.Case, err
}

func workflowWarningCodePresent(warnings []bughub.WorkflowWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}

// ResetIncidentCaseWithWarnings is the structured desktop binding used by the
// incident workbench. ResetIncidentCase remains available for older clients.
func (a *App) ResetIncidentCaseWithWarnings(input ResetIncidentCaseInput) (bughub.ResetCaseOutcome, error) {
	return a.resetIncidentCaseWithWarnings(input)
}

func (a *App) resetIncidentCaseWithWarnings(input ResetIncidentCaseInput) (bughub.ResetCaseOutcome, error) {
	if err := validateWorkflowCommandScalars(input.CaseID, input.ExpectedVersion, input.IdempotencyKey, input.ActorID); err != nil {
		return bughub.ResetCaseOutcome{}, err
	}
	if strings.TrimSpace(input.NewCaseID) == "" {
		return bughub.ResetCaseOutcome{}, errors.New("new_case_id is required")
	}
	if strings.TrimSpace(input.NewCaseID) == strings.TrimSpace(input.CaseID) {
		return bughub.ResetCaseOutcome{}, errors.New("new_case_id must differ from case_id")
	}
	if strings.TrimSpace(input.BotKey) == "" {
		return bughub.ResetCaseOutcome{}, errors.New("bot_key is required")
	}
	store, orchestrator, err := a.workflowComponents()
	if err != nil {
		return bughub.ResetCaseOutcome{}, err
	}
	original, err := store.GetCase(a.workflowCommandContext(), strings.TrimSpace(input.CaseID))
	if err != nil {
		return bughub.ResetCaseOutcome{}, err
	}
	bug, bot, err := a.loadFreshBugAndBot(original.BugID, strings.TrimSpace(input.BotKey), input.BotEnvironment)
	if err != nil {
		return bughub.ResetCaseOutcome{}, err
	}
	inputJSON, err := normalizeWorkflowInputEnvironment(input.InputJSON, bot.Env, strings.TrimSpace(input.BotEnvironment) != "")
	if err != nil {
		return bughub.ResetCaseOutcome{}, err
	}
	result, err := orchestrator.ResetCaseWithOutcome(a.workflowCommandContext(), bughub.ResetCaseCommand{
		CaseID: strings.TrimSpace(input.CaseID), NewCaseID: strings.TrimSpace(input.NewCaseID),
		ExpectedVersion: input.ExpectedVersion, IdempotencyKey: strings.TrimSpace(input.IdempotencyKey), ActorID: strings.TrimSpace(input.ActorID),
		Bug: bug, Bot: bot, InputJSON: inputJSON,
	})
	if errors.Is(err, bughub.ErrCaseVersionConflict) {
		err = fmt.Errorf("workflow_conflict:case_version_conflict: %w", err)
	} else if errors.Is(err, bughub.ErrIdempotencyConflict) {
		err = fmt.Errorf("workflow_conflict:idempotency_conflict: %w", err)
	}
	a.emitIncidentResult(result.Case, err)
	return result, err
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

// ListIncidentFixBranches returns only branch names for the repositories in
// the approved remediation scope. Repository paths stay host-private.
func (a *App) ListIncidentFixBranches(caseID, rootCauseAttemptID string) (map[string][]string, error) {
	caseID, rootCauseAttemptID = strings.TrimSpace(caseID), strings.TrimSpace(rootCauseAttemptID)
	if caseID == "" || rootCauseAttemptID == "" {
		return nil, errors.New("case_id and root_cause_attempt_id are required")
	}
	store, _, err := a.workflowComponents()
	if err != nil {
		return nil, err
	}
	ctx := a.workflowCommandContext()
	incident, err := store.GetCase(ctx, caseID)
	if err != nil {
		return nil, err
	}
	if incident.Status != bughub.CaseWaitingFixApproval || incident.CurrentAttemptID != rootCauseAttemptID {
		return nil, bughub.ErrApprovalScope
	}
	attempt, err := store.GetAttempt(ctx, rootCauseAttemptID)
	if err != nil || attempt.CaseID != caseID || attempt.Phase != bughub.PhaseInvestigation || attempt.Status != bughub.AttemptStatusSucceeded {
		return nil, bughub.ErrApprovalScope
	}
	result, err := bughub.ParseInvestigationResult(attempt.OutputJSON)
	if err != nil || result.InvestigationStatus != "root_cause_ready" || !result.UsesCodeFixWorkflow() {
		return nil, bughub.ErrApprovalScope
	}
	repositories := bughub.RemediationFixRepositories(result)
	paths := userconfig.GetRepoPathsForSystem(incident.SystemID)
	branches := make(map[string][]string, len(repositories))
	for _, repo := range repositories {
		path := strings.TrimSpace(paths[repo])
		if path == "" {
			return nil, fmt.Errorf("修复建议中的仓库 %q 未绑定本地代码仓库；请提出其他修复方案，让 Agent 从已配置仓库中重新选择", repo)
		}
		items := analyzer.ListBranches(filepath.Clean(path))
		if len(items) == 0 {
			return nil, fmt.Errorf("无法从修复建议仓库 %q 的本地代码仓库读取 Git 分支；请检查仓库路径和 Git 状态后重试", repo)
		}
		branches[repo] = items
	}
	return branches, nil
}

func (a *App) ReconsiderIncidentRemediation(input ReconsiderIncidentRemediationInput) (bughub.IncidentCase, error) {
	if err := validateWorkflowCommandScalars(input.CaseID, input.ExpectedVersion, input.IdempotencyKey, input.ActorID); err != nil {
		return bughub.IncidentCase{}, err
	}
	caseID := strings.TrimSpace(input.CaseID)
	actorID := strings.TrimSpace(input.ActorID)
	rootCauseAttemptID := strings.TrimSpace(input.RootCauseAttemptID)
	proposal := strings.TrimSpace(input.Proposal)
	if rootCauseAttemptID == "" {
		return bughub.IncidentCase{}, errors.New("root_cause_attempt_id is required")
	}
	if proposal == "" {
		return bughub.IncidentCase{}, errors.New("proposal is required")
	}
	expectedKey := bughub.ReconsiderRemediationKey(caseID, rootCauseAttemptID, input.ExpectedVersion)
	if strings.TrimSpace(input.IdempotencyKey) != expectedKey {
		return bughub.IncidentCase{}, errors.New("remediation reassessment key does not match the dialog snapshot scope")
	}
	_, orchestrator, err := a.workflowComponents()
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	bug, bot, err := a.loadIncidentContext(caseID)
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	incident, err := orchestrator.ReconsiderRemediation(a.workflowCommandContext(), bughub.ReconsiderRemediationCommand{
		CaseID: caseID, ExpectedVersion: input.ExpectedVersion, IdempotencyKey: expectedKey, ActorID: actorID,
		RootCauseAttemptID: rootCauseAttemptID, Proposal: proposal, Bug: bug, Bot: bot,
	})
	a.emitIncidentResult(incident, err)
	return incident, err
}

func (a *App) DisputeIncidentRootCause(input DisputeIncidentRootCauseInput) (bughub.IncidentCase, error) {
	if err := validateWorkflowCommandScalars(input.CaseID, input.ExpectedVersion, input.IdempotencyKey, input.ActorID); err != nil {
		return bughub.IncidentCase{}, err
	}
	caseID := strings.TrimSpace(input.CaseID)
	actorID := strings.TrimSpace(input.ActorID)
	rootCauseAttemptID := strings.TrimSpace(input.RootCauseAttemptID)
	reason := strings.TrimSpace(input.Reason)
	if rootCauseAttemptID == "" {
		return bughub.IncidentCase{}, errors.New("root_cause_attempt_id is required")
	}
	if reason == "" {
		return bughub.IncidentCase{}, errors.New("reason is required")
	}
	expectedKey := bughub.DisputeRootCauseKey(caseID, rootCauseAttemptID, input.ExpectedVersion)
	if strings.TrimSpace(input.IdempotencyKey) != expectedKey {
		return bughub.IncidentCase{}, errors.New("root cause dispute key does not match the dialog snapshot scope")
	}
	_, orchestrator, err := a.workflowComponents()
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	bug, bot, err := a.loadIncidentContext(caseID)
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	incident, err := orchestrator.DisputeRootCause(a.workflowCommandContext(), bughub.DisputeRootCauseCommand{
		CaseID: caseID, ExpectedVersion: input.ExpectedVersion, IdempotencyKey: expectedKey, ActorID: actorID,
		RootCauseAttemptID: rootCauseAttemptID, Reason: reason,
		EvidenceArtifactIDs: append([]string(nil), input.EvidenceArtifactIDs...), Bug: bug, Bot: bot,
	})
	a.emitIncidentResult(incident, err)
	return incident, err
}

func (a *App) CompleteIncidentRemediation(input CompleteIncidentRemediationInput) (bughub.IncidentCase, error) {
	if err := validateWorkflowCommandScalars(input.CaseID, input.ExpectedVersion, input.IdempotencyKey, input.ActorID); err != nil {
		return bughub.IncidentCase{}, err
	}
	if strings.TrimSpace(input.RootCauseAttemptID) == "" {
		return bughub.IncidentCase{}, errors.New("root_cause_attempt_id is required")
	}
	expectedKey := bughub.CompleteRemediationKey(strings.TrimSpace(input.CaseID), strings.TrimSpace(input.RootCauseAttemptID), input.ExpectedVersion)
	if strings.TrimSpace(input.IdempotencyKey) != expectedKey {
		return bughub.IncidentCase{}, errors.New("remediation confirmation key does not match the dialog snapshot scope")
	}
	_, orchestrator, err := a.workflowComponents()
	if err != nil {
		return bughub.IncidentCase{}, err
	}
	bug, bot, err := a.loadIncidentContext(input.CaseID)
	if err != nil {
		return bughub.IncidentCase{}, err
	}

	incident, err := orchestrator.CompleteRemediation(a.workflowCommandContext(), bughub.CompleteRemediationCommand{
		CaseID: strings.TrimSpace(input.CaseID), ExpectedVersion: input.ExpectedVersion, IdempotencyKey: strings.TrimSpace(input.IdempotencyKey), ActorID: strings.TrimSpace(input.ActorID),
		RootCauseAttemptID: strings.TrimSpace(input.RootCauseAttemptID), Summary: strings.TrimSpace(input.Summary), Evidence: strings.TrimSpace(input.Evidence), Bug: bug, Bot: bot,
	})
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

func normalizeWorkflowInputEnvironment(value map[string]any, environment string, snapshotProvided bool) (json.RawMessage, error) {
	environment = strings.TrimSpace(environment)
	cloned := make(map[string]any, len(value)+1)
	for key, item := range value {
		cloned[key] = item
	}
	if target, ok := cloned["target_environment"]; ok {
		text, textOK := target.(string)
		if !textOK {
			return nil, errors.New("input_json target_environment must be a string")
		}
		if targetEnvironment := strings.TrimSpace(text); targetEnvironment != "" && targetEnvironment != environment {
			return nil, errors.New("input_json target_environment does not match bot_environment")
		}
	}
	if snapshotProvided && environment != "" {
		cloned["target_environment"] = environment
	}
	return normalizeWorkflowJSON(cloned)
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
	bug, bot, err := a.loadBugAndBot(incident.BugID, incident.SelectedBotKey)
	if err != nil {
		return bughub.Bug{}, bughub.BotRef{}, err
	}
	bot.Env = strings.TrimSpace(incident.Environment)
	if incident.FrontendEntry.IsZero() {
	} else {
	}
	return bug, bot, nil
}

func (a *App) loadIncidentStartContext(input StartIncidentCaseInput) (bughub.Bug, bughub.BotRef, error) {
	store, _, err := a.workflowComponents()
	if err != nil {
		return bughub.Bug{}, bughub.BotRef{}, err
	}
	bugID := strings.TrimSpace(input.BugID)
	botKey := strings.TrimSpace(input.BotKey)
	persistedEnvironment := ""
	usePersistedEnvironment := false
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
		if incident.Status != bughub.CaseLegacyArchived {
			persistedEnvironment = strings.TrimSpace(incident.Environment)
			usePersistedEnvironment = true
		}
	} else if !errors.Is(getErr, bughub.ErrCaseNotFound) {
		return bughub.Bug{}, bughub.BotRef{}, getErr
	}
	if bugID == "" || botKey == "" {
		return bughub.Bug{}, bughub.BotRef{}, errors.New("bug_id and bot_key are required when creating or continuing a Case")
	}
	bug, bot, err := a.loadBugAndBot(bugID, botKey)
	if err != nil {
		return bughub.Bug{}, bughub.BotRef{}, err
	}
	if usePersistedEnvironment {
		bot.Env = persistedEnvironment
		return bug, bot, nil
	}
	return a.applyFreshIncidentBotEnvironment(bug, bot, input.BotEnvironment)
}

func (a *App) loadBugAndBot(bugID, botKey string) (bughub.Bug, bughub.BotRef, error) {
	loadBug := a.workflowLoadBug
	materializeAttachments := loadBug == nil
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
	if materializeAttachments {
		materialized := materializeBugAttachmentsForAgent(bug)
		if bugAttachmentLocalPathsChanged(bug.Attachments, materialized.Attachments) {
			bug = materialized
			_ = bugStore().Upsert(bug)
		}
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

func bugAttachmentLocalPathsChanged(before, after []bughub.Attachment) bool {
	if len(before) != len(after) {
		return true
	}
	for index := range before {
		if before[index].LocalPath != after[index].LocalPath || before[index].Type != after[index].Type {
			return true
		}
	}
	return false
}

func (a *App) loadFreshBugAndBot(bugID, botKey, environment string) (bughub.Bug, bughub.BotRef, error) {
	bug, bot, err := a.loadBugAndBot(bugID, botKey)
	if err != nil {
		return bughub.Bug{}, bughub.BotRef{}, err
	}
	return a.applyFreshIncidentBotEnvironment(bug, bot, environment)
}

func (a *App) applyFreshIncidentBotEnvironment(bug bughub.Bug, bot bughub.BotRef, environment string) (bughub.Bug, bughub.BotRef, error) {
	environment = strings.TrimSpace(environment)
	if environment != "" {
		if err := validateIncidentBotEnvironment(bot, environment); err != nil {
			return bughub.Bug{}, bughub.BotRef{}, err
		}
		bot.Env = environment
	} else if strings.TrimSpace(bot.Env) != "" {
		bot.Env = strings.TrimSpace(bot.Env)
	} else {
		resolved, err := a.applyStoredBugBotEnvironments(bug, []bughub.BotRef{bot})
		if err != nil {
			return bughub.Bug{}, bughub.BotRef{}, err
		}
		bot = resolved[0]
	}
	return bug, bot, nil
}

func validateIncidentBotEnvironment(bot bughub.BotRef, environment string) error {
	if len(bot.Envs) == 0 {
		return nil
	}
	for _, candidate := range bot.Envs {
		if strings.TrimSpace(candidate) == environment {
			return nil
		}
	}
	return fmt.Errorf("bot environment %q is not allowed by Bot %q", environment, bot.Key)
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
	cloned, ok := incidentPublicPhaseEvent(event)
	if !ok {
		return
	}
	incident := detail.Case
	a.emitWorkflowEvent(IncidentCaseEventPayload{Kind: "snapshot", Case: &incident, Snapshot: &detail, PhaseEvent: &cloned})
}

var incidentPublicPhaseEventTypes = map[string]bool{
	"thread_started": true, "turn_started": true,
	"turn_completed": true, "command_execution": true, "mcp_tool_call": true,
	"agent_message": true, "phase_step": true, "code_intelligence": true, "retry": true, "error": true,
	"turn_failed": true, "result": true,
}

func incidentPublicPhaseEvent(event bughub.InvestigationEvent) (bughub.InvestigationEvent, bool) {
	if !incidentPublicPhaseEventTypes[event.Type] {
		return bughub.InvestigationEvent{}, false
	}
	cloned := event
	// Raw Agent protocol payloads may contain command output, tool arguments, or
	// environment details. The workbench only needs the stable progress fields.
	cloned.Raw = nil
	cloned.Message = strings.TrimSpace(cloned.Message)
	if runes := []rune(cloned.Message); len(runes) > 4000 {
		cloned.Message = string(runes[:4000]) + "…"
	}
	cloned.Meta = incidentPublicPhaseEventMeta(event.Meta)
	return cloned, true
}

func incidentPhaseEventsForDetail(root string, incident bughub.IncidentCase) []bughub.InvestigationEvent {
	attemptID := strings.TrimSpace(incident.CurrentAttemptID)
	if attemptID == "" || strings.TrimSpace(root) == "" {
		return nil
	}
	run, err := bughub.NewInvestigationStore(root).Get(attemptID)
	if err != nil {
		// runs.json is a compatibility/progress projection. A missing or damaged
		// projection must not make the durable Case detail unavailable.
		return nil
	}
	const maxEvents = 100
	out := make([]bughub.InvestigationEvent, 0, min(len(run.Events), maxEvents))
	for _, event := range run.Events {
		if safe, ok := incidentPublicPhaseEvent(event); ok {
			out = append(out, safe)
		}
	}
	return capIncidentPhaseEvents(out, maxEvents)
}

func capIncidentPhaseEvents(events []bughub.InvestigationEvent, limit int) []bughub.InvestigationEvent {
	if limit <= 0 || len(events) <= limit {
		return events
	}
	recentStart := len(events) - limit
	latestStep := -1
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type == "phase_step" {
			latestStep = index
			break
		}
	}
	if latestStep < 0 || latestStep >= recentStart {
		return events[recentStart:]
	}
	out := make([]bughub.InvestigationEvent, 0, limit)
	out = append(out, events[latestStep])
	out = append(out, events[len(events)-(limit-1):]...)
	return out
}

func incidentPublicPhaseEventMeta(meta map[string]any) map[string]any {
	if meta == nil {
		return nil
	}
	allowed := map[string]struct{}{
		"case_id": {}, "attempt_id": {}, "cycle_number": {}, "phase": {},

		"state": {}, "status": {}, "exit_code": {}, "ready": {},
		"step_key": {}, "step_index": {}, "step_total": {},
	}
	out := make(map[string]any, len(allowed))
	for key := range allowed {
		if value, ok := meta[key]; ok {
			out[key] = value
		}
	}
	return out
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
