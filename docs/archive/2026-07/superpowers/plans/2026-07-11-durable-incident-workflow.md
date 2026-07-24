# Durable Incident Workflow Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a durable, auditable workflow that carries one Bug Case through validation, investigation, authorized repair, authorized environment-branch merge, manual deployment notification, deployed-version verification, and regression validation.

**Architecture:** Studio owns a transactional SQLite state machine and is the only writer of Case state. Existing validation, investigation, and fix agents become phase runners that consume and produce structured records; Git merge/push and deployment verification are explicit services behind authorization and idempotency gates. Wails exposes Case-oriented commands and events, while the Vue workbench renders the persisted snapshot instead of inferring lifecycle state from agent text.

**Tech Stack:** Go 1.25.11, `database/sql`, `modernc.org/sqlite v1.53.0` (pure Go), Wails v2, Vue 3, TypeScript, Vitest, Go race tests, temporary Git repositories for integration tests.

## Global Constraints

- Studio never deploys an application; deployment remains an external manual action.
- Starting code repair and merging a fix into an environment branch require two separate, persisted user approvals.
- A deployment notification never starts regression directly; the observed runtime version must first prove that every expected fix commit is deployed.
- Validation and investigation may retry once after process failure; fix, merge, and push must inspect external state before any retry.
- Every side effect uses an idempotency key and is recorded in the same transaction as its state transition.
- Existing `runs.json` is imported once as read-only `legacy_archived` Cases with `legacy` attempts; free-form historical text must not be used to resume an automatic phase.
- Evidence files remain under `~/.tshoot/bugs/artifacts/<case-id>/`; SQLite stores metadata and SHA256 only.
- Tokens, cookies, Authorization headers, passwords, and unredacted personal data must never be stored in workflow events or artifacts.
- Existing `InvestigationRun` APIs remain readable during migration; new mutations go through `CaseOrchestrator` only.
- No force-push, destructive Git reset, automatic conflict resolution, or implicit production write is allowed.

---

### Task 1: Define the workflow domain and transition contract

**Files:**
- Create: `internal/bughub/workflow_types.go`
- Create: `internal/bughub/workflow_transition.go`
- Create: `internal/bughub/workflow_transition_test.go`

**Interfaces:**
- Consumes: existing `bughub.Bug`, `bughub.BotRef`, and `InvestigationStatus` concepts.
- Produces: `CaseStatus`, `Phase`, `AttemptMode`, `IncidentCase`, `PhaseAttempt`, `EvidenceArtifact`, `CodeChange`, `Approval`, `DeploymentObservation`, `TransitionEvent`, `CanTransition(CaseStatus, CaseStatus) bool`, and `ValidateTransition(IncidentCase, CaseStatus) error`.

- [ ] **Step 1: Write a table-driven failing test for every allowed transition and representative forbidden jumps**

```go
func TestCanTransition(t *testing.T) {
	allowed := [][2]CaseStatus{
		{CasePendingValidation, CaseValidating},
		{CaseValidating, CaseReproduced},
		{CaseValidating, CaseWaitingEvidence},
		{CaseReproduced, CaseInvestigating},
		{CaseInvestigating, CaseRootCauseReady},
		{CaseRootCauseReady, CaseWaitingFixApproval},
		{CaseWaitingFixApproval, CaseFixing},
		{CaseFixing, CaseFixPushed},
		{CaseFixPushed, CaseWaitingMergeApproval},
		{CaseWaitingMergeApproval, CaseMerging},
		{CaseMerging, CaseWaitingDeployment},
		{CaseWaitingDeployment, CaseDeploymentVerified},
		{CaseDeploymentVerified, CaseRegressionValidating},
		{CaseRegressionValidating, CaseFixedVerified},
		{CaseRegressionValidating, CaseStillReproduces},
		{CaseStillReproduces, CaseInvestigating},
	}
	for _, edge := range allowed {
		if !CanTransition(edge[0], edge[1]) {
			t.Fatalf("expected %s -> %s", edge[0], edge[1])
		}
	}
	for _, edge := range [][2]CaseStatus{
		{CasePendingValidation, CaseFixing},
		{CaseFixPushed, CaseWaitingDeployment},
		{CaseWaitingDeployment, CaseRegressionValidating},
		{CaseLegacyArchived, CaseInvestigating},
	} {
		if CanTransition(edge[0], edge[1]) {
			t.Fatalf("forbidden %s -> %s", edge[0], edge[1])
		}
	}
}
```

- [ ] **Step 2: Run the focused test and verify RED**

Run: `go test ./internal/bughub -run '^TestCanTransition$'`

Expected: FAIL because `CaseStatus` and `CanTransition` do not exist.

- [ ] **Step 3: Implement explicit types and an allow-list transition table**

```go
type CaseStatus string

const (
	CasePendingValidation     CaseStatus = "pending_validation"
	CaseValidating            CaseStatus = "validating"
	CaseWaitingEvidence       CaseStatus = "waiting_evidence"
	CaseReproduced            CaseStatus = "reproduced"
	CaseNotReproduced         CaseStatus = "not_reproduced"
	CaseInvestigating         CaseStatus = "investigating"
	CaseRootCauseReady        CaseStatus = "root_cause_ready"
	CaseWaitingFixApproval    CaseStatus = "waiting_fix_approval"
	CaseFixing                CaseStatus = "fixing"
	CaseFixFailed             CaseStatus = "fix_failed"
	CaseFixPushed             CaseStatus = "fix_pushed"
	CaseWaitingMergeApproval  CaseStatus = "waiting_merge_approval"
	CaseMerging               CaseStatus = "merging"
	CaseMergeConflict         CaseStatus = "merge_conflict"
	CaseWaitingDeployment     CaseStatus = "waiting_deployment"
	CaseDeploymentUnverified CaseStatus = "deployment_unverified"
	CaseDeploymentVerified   CaseStatus = "deployment_verified"
	CaseRegressionValidating CaseStatus = "regression_validating"
	CaseFixedVerified        CaseStatus = "fixed_verified"
	CaseStillReproduces      CaseStatus = "still_reproduces"
	CaseLegacyArchived       CaseStatus = "legacy_archived"
)

type Phase string
const (
	PhaseValidation    Phase = "validation"
	PhaseInvestigation Phase = "investigation"
	PhaseFix           Phase = "fix"
	PhaseRegression    Phase = "regression"
	PhaseLegacy        Phase = "legacy"
)

type AttemptMode string
const (
	AttemptReproduce AttemptMode = "reproduce"
	AttemptRegression AttemptMode = "regression"
)
```

Define persisted records with these exact fields; JSON payload fields use `json.RawMessage` so store code does not reinterpret phase contracts:

```go
type IncidentCase struct {
	ID, BugID, Source, SystemID, Environment string
	Status CaseStatus
	CycleNumber int
	CurrentAttemptID, SelectedBotKey string
	Version int64
	CreatedAt, UpdatedAt time.Time
	ClosedAt *time.Time
}
type PhaseAttempt struct {
	ID, CaseID string
	CycleNumber int
	Phase Phase
	Mode AttemptMode
	Status InvestigationStatus
	AgentTarget, BotKey string
	InputJSON, OutputJSON json.RawMessage
	ParentAttemptID string
	StartedAt time.Time
	FinishedAt *time.Time
	ErrorCode, ErrorMessage string
	Usage AgentUsage
}
type EvidenceArtifact struct {
	ID, CaseID, AttemptID, Kind, PathOrReference, SHA256 string
	CapturedAt time.Time
	Environment, Version, RequestID, TraceID, RedactionStatus string
}
type CodeChange struct {
	ID, CaseID, AttemptID, Repo, BaseBranch, FixBranch, FixCommit string
	TestEvidence json.RawMessage
	TargetEnvironmentBranch, MergeBaseHead, MergeCommit, PushRemote, PushStatus string
}
type Approval struct {
	ID, CaseID, Kind, Actor string
	ApprovedAt time.Time
	CaseVersion int64
	ScopeJSON json.RawMessage
	FixCommits, TargetBranches map[string]string
}
type DeploymentObservation struct {
	ID, CaseID, Environment string
	ExpectedCommits map[string]string
	UserNotifiedAt, VerifiedAt *time.Time
	VerificationSource, ObservedVersion string
	ObservedImages, ObservedCommits map[string]string
	Result string
}
type TransitionEvent struct {
	ID, CaseID string
	FromStatus, ToStatus CaseStatus
	EventType, ActorType, ActorID, IdempotencyKey string
	PayloadJSON json.RawMessage
	CreatedAt time.Time
}
```

Add JSON tags matching the lowercase snake-case field names in the design. Implement `CanTransition` from a literal `map[CaseStatus]map[CaseStatus]struct{}` and return a typed `ErrInvalidTransition` from `ValidateTransition`.

Persist phase resource use with the exact shape below so Task 13 can calculate execution cost without parsing logs:

```go
type AgentUsage struct {
	InputTokens  int64         `json:"input_tokens,omitempty"`
	OutputTokens int64         `json:"output_tokens,omitempty"`
	Duration     time.Duration `json:"duration,omitempty"`
}
```

Add `Usage AgentUsage` to `PhaseAttempt`; negative values are invalid.

- [ ] **Step 4: Add validation tests for legacy immutability, non-empty IDs, cycle number, and approval scope**

Run: `go test ./internal/bughub -run 'Test(CanTransition|ValidateWorkflow)' -race`

Expected: PASS.

- [ ] **Step 5: Commit the domain contract**

```bash
git add internal/bughub/workflow_types.go internal/bughub/workflow_transition.go internal/bughub/workflow_transition_test.go
git commit -m "feat: define incident workflow states"
```

---

### Task 2: Add the transactional SQLite CaseStore

**Files:**
- Modify: `go.mod`
- Modify: `go.sum`
- Create: `internal/bughub/workflow_store.go`
- Create: `internal/bughub/workflow_store_schema.go`
- Create: `internal/bughub/workflow_store_test.go`

**Interfaces:**
- Consumes: Task 1 workflow types.
- Produces: `OpenCaseStore(path string) (*CaseStore, error)`, `(*CaseStore).Close() error`, `CreateCase`, `GetCase`, `ListCases`, `CreateAttempt`, `FinishAttempt`, `Transition`, `RecordApproval`, `RecordCodeChange`, `RecordDeploymentObservation`, and `ListEvents`.

- [ ] **Step 1: Add a failing transaction test proving snapshot and event cannot diverge**

```go
func TestCaseStoreTransitionIsTransactionalAndIdempotent(t *testing.T) {
	store := openTestCaseStore(t)
	ctx := context.Background()
	c := IncidentCase{ID: "case-1", BugID: "zentao-909", Status: CasePendingValidation, CycleNumber: 1, Version: 1}
	if err := store.CreateCase(ctx, c); err != nil { t.Fatal(err) }
	e := TransitionEvent{ID: "event-1", CaseID: c.ID, IdempotencyKey: "validate:case-1:1"}
	updated, replay, err := store.Transition(ctx, c.ID, 1, CaseValidating, e)
	if err != nil || replay || updated.Version != 2 { t.Fatalf("updated=%+v replay=%v err=%v", updated, replay, err) }
	replayed, replay, err := store.Transition(ctx, c.ID, 1, CaseValidating, e)
	if err != nil || !replay || replayed.Version != 2 { t.Fatalf("replayed=%+v replay=%v err=%v", replayed, replay, err) }
	if events, _ := store.ListEvents(ctx, c.ID); len(events) != 1 { t.Fatalf("events=%d", len(events)) }
}
```

- [ ] **Step 2: Verify RED before adding the dependency**

Run: `go test ./internal/bughub -run '^TestCaseStore'`

Expected: FAIL because `OpenCaseStore` does not exist.

- [ ] **Step 3: Add the pure-Go SQLite dependency and schema**

Run: `go get modernc.org/sqlite@v1.53.0`

Create schema statements for:

```sql
CREATE TABLE IF NOT EXISTS incident_cases (
  id TEXT PRIMARY KEY,
  bug_id TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT '',
  system_id TEXT NOT NULL DEFAULT '',
  environment TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  cycle_number INTEGER NOT NULL CHECK (cycle_number >= 1),
  current_attempt_id TEXT NOT NULL DEFAULT '',
  selected_bot_key TEXT NOT NULL DEFAULT '',
  version INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  closed_at TEXT
);
CREATE TABLE IF NOT EXISTS phase_attempts (
  id TEXT PRIMARY KEY,
  case_id TEXT NOT NULL REFERENCES incident_cases(id),
  cycle_number INTEGER NOT NULL,
  phase TEXT NOT NULL,
  mode TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  agent_target TEXT NOT NULL DEFAULT '',
  bot_key TEXT NOT NULL DEFAULT '',
  input_json TEXT NOT NULL DEFAULT '{}',
  output_json TEXT NOT NULL DEFAULT '{}',
  parent_attempt_id TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL,
  finished_at TEXT,
  error_code TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS transition_events (
  id TEXT PRIMARY KEY,
  case_id TEXT NOT NULL REFERENCES incident_cases(id),
  from_status TEXT NOT NULL,
  to_status TEXT NOT NULL,
  event_type TEXT NOT NULL,
  actor_type TEXT NOT NULL,
  actor_id TEXT NOT NULL DEFAULT '',
  idempotency_key TEXT NOT NULL UNIQUE,
  payload_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL
);
```

Add the remaining tables exactly as follows:

```sql
CREATE TABLE IF NOT EXISTS evidence_artifacts (
  id TEXT PRIMARY KEY, case_id TEXT NOT NULL REFERENCES incident_cases(id),
  attempt_id TEXT NOT NULL REFERENCES phase_attempts(id), kind TEXT NOT NULL,
  path_or_reference TEXT NOT NULL, sha256 TEXT NOT NULL, captured_at TEXT NOT NULL,
  environment TEXT NOT NULL DEFAULT '', version TEXT NOT NULL DEFAULT '',
  request_id TEXT NOT NULL DEFAULT '', trace_id TEXT NOT NULL DEFAULT '',
  redaction_status TEXT NOT NULL,
  UNIQUE(attempt_id, sha256, kind)
);
CREATE TABLE IF NOT EXISTS code_changes (
  id TEXT PRIMARY KEY, case_id TEXT NOT NULL REFERENCES incident_cases(id),
  attempt_id TEXT NOT NULL REFERENCES phase_attempts(id), repo TEXT NOT NULL,
  base_branch TEXT NOT NULL, fix_branch TEXT NOT NULL, fix_commit TEXT NOT NULL,
  test_evidence_json TEXT NOT NULL DEFAULT '[]', target_environment_branch TEXT NOT NULL,
  merge_base_head TEXT NOT NULL DEFAULT '', merge_commit TEXT NOT NULL DEFAULT '',
  push_remote TEXT NOT NULL DEFAULT '', push_status TEXT NOT NULL DEFAULT '',
  UNIQUE(case_id, repo, fix_commit, target_environment_branch)
);
CREATE TABLE IF NOT EXISTS approvals (
  id TEXT PRIMARY KEY, case_id TEXT NOT NULL REFERENCES incident_cases(id),
  kind TEXT NOT NULL, actor TEXT NOT NULL, approved_at TEXT NOT NULL,
  case_version INTEGER NOT NULL, scope_json TEXT NOT NULL,
  fix_commits_json TEXT NOT NULL, target_branches_json TEXT NOT NULL,
  idempotency_key TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS deployment_observations (
  id TEXT PRIMARY KEY, case_id TEXT NOT NULL REFERENCES incident_cases(id),
  environment TEXT NOT NULL, expected_commits_json TEXT NOT NULL,
  user_notified_at TEXT, verification_source TEXT NOT NULL,
  observed_version TEXT NOT NULL DEFAULT '', observed_images_json TEXT NOT NULL DEFAULT '{}',
  observed_commits_json TEXT NOT NULL DEFAULT '{}', verified_at TEXT,
  result TEXT NOT NULL, idempotency_key TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS schema_migrations (
  key TEXT PRIMARY KEY, applied_at TEXT NOT NULL, detail_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_cases_status_updated ON incident_cases(status, updated_at);
CREATE INDEX IF NOT EXISTS idx_attempts_case_started ON phase_attempts(case_id, started_at);
CREATE INDEX IF NOT EXISTS idx_events_case_created ON transition_events(case_id, created_at);
```

- [ ] **Step 4: Implement store initialization and transactional methods**

Open with driver name `sqlite`, set `PRAGMA foreign_keys=ON`, `PRAGMA journal_mode=WAL`, and `PRAGMA busy_timeout=5000`. `Transition` must use `BEGIN`, check an existing event by idempotency key, compare `IncidentCase.Version`, validate the status edge, update the snapshot, insert the event, and commit.

- [ ] **Step 5: Add concurrent writers, optimistic version mismatch, rollback, reopen, and file permission tests**

Run: `go test ./internal/bughub -run '^TestCaseStore' -race`

Expected: PASS; two transitions with the same expected version cannot both win, and reopening the database returns the same event history.

- [ ] **Step 6: Commit the store**

```bash
git add go.mod go.sum internal/bughub/workflow_store.go internal/bughub/workflow_store_schema.go internal/bughub/workflow_store_test.go
git commit -m "feat: persist incident workflows in sqlite"
```

---

### Task 3: Import legacy runs and register immutable artifacts

**Files:**
- Create: `internal/bughub/workflow_migrate.go`
- Create: `internal/bughub/workflow_migrate_test.go`
- Create: `internal/bughub/workflow_artifacts.go`
- Create: `internal/bughub/workflow_artifacts_test.go`

**Interfaces:**
- Consumes: `InvestigationStore.Path()`, Task 2 `CaseStore`.
- Produces: `ImportLegacyRuns(ctx context.Context, store *CaseStore, runsPath string) (LegacyImportResult, error)` and `RegisterArtifact(ctx context.Context, store *CaseStore, input ArtifactInput) (EvidenceArtifact, error)`.

- [ ] **Step 1: Write failing migration tests**

Create a `runs.json` with succeeded, running, malformed-phase, and duplicate runs. Assert:

```go
result, err := ImportLegacyRuns(ctx, store, runsPath)
if err != nil { t.Fatal(err) }
if result.Cases != 2 || result.Attempts != 4 { t.Fatalf("result=%+v", result) }
for _, c := range mustListCases(t, store) {
	if c.Status != CaseLegacyArchived { t.Fatalf("status=%s", c.Status) }
}
second, err := ImportLegacyRuns(ctx, store, runsPath)
if err != nil || second.Attempts != 0 { t.Fatalf("second=%+v err=%v", second, err) }
```

- [ ] **Step 2: Verify migration RED**

Run: `go test ./internal/bughub -run 'TestImportLegacyRuns|TestRegisterArtifact'`

Expected: FAIL because the functions do not exist.

- [ ] **Step 3: Implement one-time safe import**

Group legacy runs by BugID, create deterministic IDs from `sha256("legacy-case:"+bugID)`, insert `legacy` attempts preserving prompt/events/final/error JSON, and write migration key `runs-json-v1:<sha256(file)>`. Never infer an active workflow status from `FinalMessage`.

- [ ] **Step 4: Implement artifact registration with redaction gate and hashing**

Reject inputs whose content or metadata contains case-insensitive `authorization:`, `cookie:`, `set-cookie:`, `password=`, or bearer tokens unless `RedactionStatus == "redacted"`. Hash file bytes, copy into `artifacts/<case-id>/<sha256><ext>` with mode `0600`, and insert metadata transactionally.

- [ ] **Step 5: Test corrupted JSON, repeated import, path traversal, duplicate hashes, permissions, and secret rejection**

Run: `go test ./internal/bughub -run 'Test(ImportLegacyRuns|RegisterArtifact)' -race`

Expected: PASS; corrupt legacy input returns a visible error without modifying existing Cases.

- [ ] **Step 6: Commit migration and artifacts**

```bash
git add internal/bughub/workflow_migrate.go internal/bughub/workflow_migrate_test.go internal/bughub/workflow_artifacts.go internal/bughub/workflow_artifacts_test.go
git commit -m "feat: migrate incident runs and evidence"
```

---

### Task 4: Implement CaseOrchestrator, authorization gates, and restart recovery

**Files:**
- Create: `internal/bughub/workflow_orchestrator.go`
- Create: `internal/bughub/workflow_orchestrator_test.go`
- Create: `internal/bughub/workflow_recovery.go`
- Create: `internal/bughub/workflow_recovery_test.go`

**Interfaces:**
- Consumes: Task 2 `CaseStore`, Task 1 transitions.
- Produces: `NewCaseOrchestrator(store *CaseStore, runner PhaseRunner, git GitIntegration, deployment DeploymentVerifier) *CaseOrchestrator`; commands `StartCase`, `ContinueWithEvidence`, `ApproveFix`, `ApproveMerge`, `NotifyDeployed`, `CancelAttempt`, and `RecoverInterrupted`.

- [ ] **Step 1: Write failing tests for automatic and gated transitions**

```go
func TestOrchestratorRequiresSeparateFixAndMergeApprovals(t *testing.T) {
	o := newTestOrchestrator(t)
	c := createRootCauseReadyCase(t, o)
	if _, err := o.ApproveMerge(ctx, ApproveMergeCommand{CaseID: c.ID}); !errors.Is(err, ErrApprovalNotReady) {
		t.Fatalf("err=%v", err)
	}
	c = mustApproveFixAndFinish(t, o, c.ID)
	if c.Status != CaseWaitingMergeApproval { t.Fatalf("status=%s", c.Status) }
	c, err := o.ApproveMerge(ctx, validMergeCommand(c))
	if err != nil || c.Status != CaseMerging { t.Fatalf("case=%+v err=%v", c, err) }
}
```

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/bughub -run '^TestOrchestrator'`

Expected: FAIL because `CaseOrchestrator` is undefined.

- [ ] **Step 3: Define command interfaces and enforce state ownership**

```go
type PhaseRunner interface {
	Start(context.Context, PhaseAttempt, Bug, BotRef) error
	Cancel(context.Context, string) error
}
type GitIntegration interface {
	MergeAndPush(context.Context, MergeRequest) (MergeResult, error)
	Inspect(context.Context, MergeRequest) (MergeInspection, error)
}
type DeploymentVerifier interface {
	Verify(context.Context, DeploymentVerificationRequest) (DeploymentObservation, error)
}
```

Every public command loads the Case, validates `ExpectedVersion`, checks state and approval scope, inserts the approval/event, transitions, then schedules the runner/service after commit. If scheduling fails, transition to the explicit failure state with a new event.

- [ ] **Step 4: Implement recovery rules**

On startup, inspect attempts left `running`:

- validation/investigation: mark interrupted and create at most one retry attempt.
- fix: mark `fix_failed` unless external Git inspection proves `fix_commit` was pushed.
- merging: inspect target branch; if recorded merge commit exists remotely, move to `waiting_deployment`; otherwise retain `merge_conflict` or a retryable push failure.
- regression: mark interrupted and allow one fresh regression attempt only if the deployment observation remains matched.

- [ ] **Step 5: Test duplicate commands, stale versions, crash points, and side-effect recovery**

Run: `go test ./internal/bughub -run 'Test(Orchestrator|RecoverInterrupted)' -race`

Expected: PASS.

- [ ] **Step 6: Commit orchestration**

```bash
git add internal/bughub/workflow_orchestrator.go internal/bughub/workflow_orchestrator_test.go internal/bughub/workflow_recovery.go internal/bughub/workflow_recovery_test.go
git commit -m "feat: orchestrate durable incident cases"
```

---

### Task 5: Adapt existing agents into structured phase runners

**Files:**
- Create: `internal/bughub/workflow_phase_runner.go`
- Create: `internal/bughub/workflow_phase_runner_test.go`
- Modify: `internal/bughub/codex_runner.go`
- Modify: `internal/bughub/investigation.go`
- Modify: `internal/bughub/investigation_test.go`
- Modify: `templates/workspace/skills/bug-verifier/SKILL.md.tmpl`
- Modify: `templates/workspace/skills/bug-fixer/SKILL.md.tmpl`

**Interfaces:**
- Consumes: Task 4 `PhaseRunner`, existing `CodexInvestigator` execution adapters.
- Produces: `AgentPhaseRunner`, `ValidationResult`, `InvestigationResult`, `FixResult`, strict output parsing, and regression-mode validation prompts.

- [ ] **Step 1: Write failing parser and prompt tests**

Assert validation results map only as follows:

```go
cases := []struct{ status string; want CaseStatus }{
	{"reproduced", CaseReproduced},
	{"not_reproduced", CaseNotReproduced},
	{"insufficient_info", CaseWaitingEvidence},
	{"fixed_verified", CaseFixedVerified},
	{"still_reproduces", CaseStillReproduces},
}
```

For regression mode, assert the prompt includes original scenario hash, expected commits, observed runtime version, a fresh-evidence requirement, and forbids source-code root-cause analysis.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/bughub -run 'Test(PhaseResult|RegressionPrompt|AgentPhaseRunner)'`

Expected: FAIL because structured phase results do not exist.

- [ ] **Step 3: Implement phase-specific contracts**

`AgentPhaseRunner.Start` must select the existing CLI adapter by `BotRef.Target`, emit events tagged with `case_id`, `attempt_id`, `cycle_number`, and `phase`, parse the final YAML into typed results, register referenced evidence artifacts, finish the attempt, and call one orchestrator callback with the typed result.

- [ ] **Step 4: Preserve legacy read compatibility without dual mutation**

Continue writing `InvestigationRun` events only as a compatibility projection for the currently running attempt. Case state changes must occur only through the orchestrator callback; remove any new direct workflow inference from `FinalMessage` in Wails or Vue.

- [ ] **Step 5: Add fresh-evidence regression tests and retry tests**

Reject `fixed_verified` when deployment verification is absent, when evidence references only attempts from an earlier cycle, or when the environment differs. Verify one read-only process retry and zero blind fix retries.

Run: `go test ./internal/bughub -run 'Test(PhaseResult|RegressionPrompt|AgentPhaseRunner|CodexInvestigator)' -race`

Expected: PASS.

- [ ] **Step 6: Run skill script tests and commit**

Run: `scripts/test-skill-scripts.sh`

Expected: PASS.

```bash
git add internal/bughub/workflow_phase_runner.go internal/bughub/workflow_phase_runner_test.go internal/bughub/codex_runner.go internal/bughub/investigation.go internal/bughub/investigation_test.go templates/workspace/skills/bug-verifier/SKILL.md.tmpl templates/workspace/skills/bug-fixer/SKILL.md.tmpl
git commit -m "feat: run agents as workflow phases"
```

---

### Task 6: Expose Case workflow bindings and frontend bridge types

**Files:**
- Create: `cmd/tshoot-desktop/bindings_bug_workflow.go`
- Create: `cmd/tshoot-desktop/bindings_bug_workflow_test.go`
- Modify: `cmd/tshoot-desktop/main.go`
- Modify: `cmd/tshoot-desktop/bindings_bug_investigation.go`
- Create: `web/src/lib/bridge/bugWorkflow.ts`
- Create: `web/src/lib/bridge/bugWorkflow.test.ts`
- Modify: `web/wailsjs/go/main/App.d.ts`
- Modify: `web/wailsjs/go/main/App.js`
- Modify: `web/wailsjs/go/models.ts`

**Interfaces:**
- Consumes: Task 4 orchestrator and Task 2 store.
- Produces Wails methods `ListIncidentCases`, `GetIncidentCase`, `StartIncidentCase`, `ContinueIncidentCase`, `ApproveIncidentFix`, `ApproveIncidentMerge`, `NotifyIncidentDeployed`, and `CancelIncidentAttempt`; emits `incident-case:event`.

- [ ] **Step 1: Write failing binding tests for nil context, stale version, duplicate command, and restart reload**

```go
func TestNotifyIncidentDeployedDoesNotStartRegressionBeforeVersionMatch(t *testing.T) {
	a := newWorkflowTestApp(t)
	got, err := a.NotifyIncidentDeployed(NotifyIncidentDeployedInput{
		CaseID: "case-1", ExpectedVersion: 9, Environment: "test", ObservedVersion: "old-build",
	})
	if err != nil { t.Fatal(err) }
	if got.Status != bughub.CaseDeploymentUnverified { t.Fatalf("status=%s", got.Status) }
}
```

- [ ] **Step 2: Verify Go and TypeScript RED**

Run: `go test ./cmd/tshoot-desktop -run '^Test.*Incident'`

Run: `cd web && npm test -- --run src/lib/bridge/bugWorkflow.test.ts`

Expected: FAIL because bindings and bridge functions are absent.

- [ ] **Step 3: Implement Wails bindings as thin command adapters**

Bindings validate required scalar fields, load Bug/Bot context, then call CaseOrchestrator. They must not issue Git commands, parse Agent text, or update CaseStore directly. Initialize one store/orchestrator per `App`, close it during shutdown, run legacy migration once, and call recovery after startup.

- [ ] **Step 4: Implement exact frontend types and normalization**

```ts
export type CaseStatus = 'pending_validation' | 'validating' | 'waiting_evidence' | 'reproduced' |
  'not_reproduced' | 'investigating' | 'root_cause_ready' | 'waiting_fix_approval' |
  'fixing' | 'fix_failed' | 'fix_pushed' | 'waiting_merge_approval' | 'merging' |
  'merge_conflict' | 'waiting_deployment' | 'deployment_unverified' |
  'deployment_verified' | 'regression_validating' | 'fixed_verified' |
  'still_reproduces' | 'legacy_archived'
```

Normalize nil slices to `[]`, preserve numeric `version`, and reject browser-mode mutations with a clear desktop-only error.

- [ ] **Step 5: Verify bindings and bridge**

Run: `go test ./cmd/tshoot-desktop -run '^Test.*Incident' -race`

Run: `cd web && npm test -- --run src/lib/bridge/bugWorkflow.test.ts`

Expected: PASS.

- [ ] **Step 6: Commit the API boundary**

```bash
git add cmd/tshoot-desktop/bindings_bug_workflow.go cmd/tshoot-desktop/bindings_bug_workflow_test.go cmd/tshoot-desktop/main.go cmd/tshoot-desktop/bindings_bug_investigation.go web/src/lib/bridge/bugWorkflow.ts web/src/lib/bridge/bugWorkflow.test.ts web/wailsjs/go/main/App.d.ts web/wailsjs/go/main/App.js web/wailsjs/go/models.ts
git commit -m "feat: expose incident case workflow"
```

---

### Task 7: Build the Case lifecycle workbench

**Files:**
- Create: `web/src/components/BugCaseLifecycle.vue`
- Create: `web/src/components/BugCaseLifecycle.test.ts`
- Create: `web/src/components/BugCaseArtifacts.vue`
- Create: `web/src/components/BugCaseArtifacts.test.ts`
- Create: `web/src/lib/useIncidentCase.ts`
- Create: `web/src/lib/useIncidentCase.test.ts`
- Modify: `web/src/pages/BugWorkbenchPage.vue`
- Modify: `web/src/pages/BugWorkbenchPage.test.ts`

**Interfaces:**
- Consumes: Task 6 bridge functions and `incident-case:event`.
- Produces: three-column Case UI, six-stage progress, one primary action, timeline, evidence panel, refresh/restart recovery, and accessible approval dialogs.

- [ ] **Step 1: Write failing composable tests for event ordering and retained state**

Test that an event with Case version 7 replaces version 6, version 5 is ignored, a failed refresh retains the current snapshot, and duplicate button clicks share one pending promise.

- [ ] **Step 2: Write failing component tests for every actionable state**

Examples:

```ts
expect(primaryActionFor(caseAt('waiting_fix_approval')).label).toBe('允许修复')
expect(primaryActionFor(caseAt('waiting_merge_approval')).label).toBe('允许合并环境分支')
expect(primaryActionFor(caseAt('waiting_deployment')).label).toBe('已部署，开始验证')
expect(primaryActionFor(caseAt('fixed_verified'))).toBeUndefined()
```

- [ ] **Step 3: Verify UI RED**

Run: `cd web && npm test -- --run src/lib/useIncidentCase.test.ts src/components/BugCaseLifecycle.test.ts src/components/BugCaseArtifacts.test.ts`

Expected: FAIL because components/composable are absent.

- [ ] **Step 4: Implement the approved three-column layout**

Left: Case list and status. Center: validation/investigation/fix/merge/deploy/regression progress, current-action card, and event timeline. Right: evidence, root-cause result, code changes/tests, approvals, and deployment observations.

Use semantic buttons, visible focus, text plus color status, 44px targets, `aria-live` for failures, reduced-motion support, and a single-column layout below 900px.

- [ ] **Step 5: Integrate without removing legacy run readability**

New Cases use lifecycle UI. Imported `legacy_archived` Cases render read-only historical run output with a “从新一轮验证继续” action that creates a new current Case cycle; it must not mutate the archived attempt.

- [ ] **Step 6: Verify UI and build**

Run: `cd web && npm test -- --run src/lib/useIncidentCase.test.ts src/components/BugCaseLifecycle.test.ts src/components/BugCaseArtifacts.test.ts src/pages/BugWorkbenchPage.test.ts && npm run build`

Expected: PASS with no horizontal overflow at 375, 768, 1024, and 1440 CSS viewport fixtures.

- [ ] **Step 7: Commit the workbench**

```bash
git add web/src/components/BugCaseLifecycle.vue web/src/components/BugCaseLifecycle.test.ts web/src/components/BugCaseArtifacts.vue web/src/components/BugCaseArtifacts.test.ts web/src/lib/useIncidentCase.ts web/src/lib/useIncidentCase.test.ts web/src/pages/BugWorkbenchPage.vue web/src/pages/BugWorkbenchPage.test.ts
git commit -m "feat: show durable incident lifecycle"
```

---

### Task 8: Capture structured fix outputs and start-fix authorization

**Files:**
- Create: `internal/bughub/workflow_fix.go`
- Create: `internal/bughub/workflow_fix_test.go`
- Modify: `internal/bughub/codex_runner.go`
- Modify: `cmd/tshoot-desktop/bindings_bug_workflow.go`
- Modify: `web/src/components/BugCaseLifecycle.vue`

**Interfaces:**
- Consumes: typed `FixResult`, `Approval`, Task 4 `ApproveFix`.
- Produces: validated `CodeChange` rows and approval bound to root-cause attempt, Case version, repo, base branch, fix branch, and fix commit.

- [ ] **Step 1: Write failing tests for stale and incomplete fix authorization**

Reject approval when the root-cause attempt is not the latest investigation attempt, confidence is below high, critical evidence is unresolved, or the Case version differs from the dialog snapshot.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/bughub -run 'Test(ApproveFix|ParseFixResult)'`

Expected: FAIL before implementation.

- [ ] **Step 3: Implement strict fix result parsing**

Require `fix_status`, environment, per-repo base/fix branches, commit SHA, push status, changes, tests, deployment notice, and risks. `fixed_pushed` is accepted only when every changed repo has a non-empty commit, successful push, and at least one test result or an explicit skipped reason.

- [ ] **Step 4: Persist approval and CodeChange scope**

Approval key:

```text
start-fix:<case-id>:<root-cause-attempt-id>:<case-version>
```

After fix completion, transition to `fix_pushed`, insert CodeChange rows, then automatically transition to `waiting_merge_approval` as a separate event.

- [ ] **Step 5: Verify and commit**

Run: `go test ./internal/bughub -run 'Test(ApproveFix|ParseFixResult)' -race`

Expected: PASS.

```bash
git add internal/bughub/workflow_fix.go internal/bughub/workflow_fix_test.go internal/bughub/codex_runner.go cmd/tshoot-desktop/bindings_bug_workflow.go web/src/components/BugCaseLifecycle.vue
git commit -m "feat: authorize incident repairs"
```

---

### Task 9: Merge fix branches into environment branches safely

**Files:**
- Create: `internal/bughub/workflow_git.go`
- Create: `internal/bughub/workflow_git_test.go`
- Modify: `internal/bughub/workflow_orchestrator.go`
- Modify: `cmd/tshoot-desktop/bindings_bug_workflow.go`
- Modify: `web/src/components/BugCaseLifecycle.vue`

**Interfaces:**
- Consumes: `CodeChange`, repo local paths, env-branch mapping, merge approval.
- Produces: `GitIntegrationService`, `MergeRequest`, `MergeInspection`, `MergeResult`, and recorded per-repo environment merge commits.

- [ ] **Step 1: Write temporary-repository RED tests**

Cover fast-forward, merge commit, target HEAD changed after approval, dirty worktree, detached HEAD, conflict, already-merged idempotent replay, SSH push failure, and two repositories where the second fails.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/bughub -run '^TestGitIntegration'`

Expected: FAIL because `GitIntegrationService` is absent.

- [ ] **Step 3: Implement inspection and merge using non-destructive Git commands**

Inspection uses `git status --porcelain`, `rev-parse`, `merge-base --is-ancestor`, `merge-tree` where supported, and configured repo paths. Execution checks out the target branch only in a dedicated Studio-owned worktree, fetches without rewriting user work, merges the exact fix commit, records the resulting commit, and pushes using the configured SSH remote.

Never run `reset --hard`, `clean -fd`, force push, or auto-resolve conflicts.

- [ ] **Step 4: Implement exact approval scope and idempotency**

Approval key:

```text
merge:<case-id>:<repo>:<fix-commit>:<target-branch>:<target-head>
```

If the target HEAD changes, transition back to `waiting_merge_approval` with a new inspection summary. If the recorded merge commit is already on the remote target, return replay success.

- [ ] **Step 5: Handle multi-repo partial completion**

Persist each repository result. Do not move to `waiting_deployment` until all CodeChanges are merged and pushed. A failure exposes completed repos and the exact blocked repo; retry only the incomplete repos after a new inspection.

- [ ] **Step 6: Verify and commit**

Run: `go test ./internal/bughub -run '^TestGitIntegration' -race`

Expected: PASS.

```bash
git add internal/bughub/workflow_git.go internal/bughub/workflow_git_test.go internal/bughub/workflow_orchestrator.go cmd/tshoot-desktop/bindings_bug_workflow.go web/src/components/BugCaseLifecycle.vue
git commit -m "feat: merge authorized incident fixes"
```

---

### Task 10: Add deployment notification and manual version proof

**Files:**
- Create: `internal/bughub/workflow_deployment.go`
- Create: `internal/bughub/workflow_deployment_test.go`
- Create: `internal/bughub/workflow_deployment_intent.go`
- Create: `internal/bughub/workflow_deployment_intent_test.go`
- Modify: `internal/bughub/workflow_orchestrator.go`
- Modify: `cmd/tshoot-desktop/bindings_bug_workflow.go`
- Modify: `web/src/components/BugCaseLifecycle.vue`
- Modify: `web/src/components/BugCaseLifecycle.test.ts`

**Interfaces:**
- Consumes: expected per-repo fix/merge commits and `waiting_deployment` Case.
- Produces: `CompositeDeploymentVerifier`, `ManualVersionVerifier`, deployment observation persistence, and the “已部署，开始验证” flow.

- [ ] **Step 1: Write failing verifier and duplicate-notification tests**

Assert a manual observation only matches when its provided commit set contains every expected fix commit or a verified descendant mapping; missing one repository returns `mismatched`. Repeating the same notification returns the existing observation and does not create a regression attempt.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/bughub -run 'Test(ManualVersionVerifier|NotifyDeployed)'`

Expected: FAIL.

- [ ] **Step 3: Implement manual proof and composite selection**

```go
type DeploymentVerificationRequest struct {
	CaseID string
	Environment string
	ExpectedCommits map[string]string
	ObservedVersion string
	ObservedCommits map[string]string
	Source string
}
```

The manual verifier requires a non-empty observed version, exact environment, source metadata, and all expected repositories. Persist `matched`, `mismatched`, or `unavailable`; only `matched` transitions to `deployment_verified`.

- [ ] **Step 4: Implement UI confirmation**

The dialog displays target environment, every expected commit, version source, and an optional per-repo observed commit field. It must not label the deployment verified before the backend returns a matched observation.

- [ ] **Step 5: Recognize deployment notification language without accepting ambiguous messages**

Implement `ParseDeploymentNotificationIntent(text string) bool` for normalized exact intents such as `已部署`, `部署完成`, and `已经部署到 test`. It must return false for `准备部署`, `还没部署`, `部署失败`, quoted historical text, or messages received while the Case is not `waiting_deployment`. A recognized intent calls the same `NotifyDeployed` command as the button; it never bypasses DeploymentVerifier.

- [ ] **Step 6: Verify and commit**

Run: `go test ./internal/bughub -run 'Test(ManualVersionVerifier|NotifyDeployed|DeploymentNotificationIntent)' -race`

Run: `cd web && npm test -- --run src/components/BugCaseLifecycle.test.ts`

Expected: PASS.

```bash
git add internal/bughub/workflow_deployment.go internal/bughub/workflow_deployment_test.go internal/bughub/workflow_deployment_intent.go internal/bughub/workflow_deployment_intent_test.go internal/bughub/workflow_orchestrator.go cmd/tshoot-desktop/bindings_bug_workflow.go web/src/components/BugCaseLifecycle.vue web/src/components/BugCaseLifecycle.test.ts
git commit -m "feat: verify manual incident deployments"
```

---

### Task 11: Configure HTTP and K8s runtime version verification

**Files:**
- Create: `internal/config/types_deployment_verification.go`
- Modify: `internal/config/types.go`
- Modify: `internal/config/validate.go`
- Create: `internal/config/deployment_verification_test.go`
- Modify: `schema/troubleshooter.schema.yaml`
- Modify: `examples/three-tier-troubleshooter.yaml`
- Modify: `web/src/lib/yamlGenerator.ts`
- Modify: `web/src/lib/yamlGenerator.test.ts`
- Modify: `web/src/lib/yamlImporter.ts`
- Modify: `web/src/lib/yamlImporter.test.ts`
- Modify: `web/src/lib/useWizardDraft.ts`
- Modify: `web/src/pages/InitPage.vue`
- Modify: `web/src/components/EnvListItem.vue`
- Create: `internal/bughub/workflow_deployment_http.go`
- Create: `internal/bughub/workflow_deployment_k8s.go`
- Create: `internal/bughub/workflow_deployment_providers_test.go`

**Interfaces:**
- Consumes: Task 10 `DeploymentVerifier`, existing environment and K8s routing configuration.
- Produces: optional per-environment `deployment_verification` configuration with `manual`, `http`, and `k8s` providers.

- [ ] **Step 1: Write failing config round-trip and semantic validation tests**

Use this exact YAML contract:

```yaml
environments:
  - id: test
    deployment_verification:
      provider: http
      http:
        url: "https://admin-test.example.com/version"
        json_pointer: "/git/commit"
```

K8s contract:

```yaml
deployment_verification:
  provider: k8s
  k8s:
    cluster: "test-cluster"
    namespace: "admin-test"
    deployments_by_repo:
      admin-web: "admin-web"
    commit_annotation: "app.example.com/git-commit"
```

Reject mixed provider blocks, missing URL/pointer, missing cluster/namespace/map, unknown repo names, and automatic providers on an environment absent from the system.

In the same RED step, add Web tests that import each exact YAML block above and expect `generateYAML` to emit the same provider-specific values after draft restoration.

- [ ] **Step 2: Verify config RED**

Run: `go test ./internal/config -run '^TestDeploymentVerification'`

Run: `npm --prefix web test -- --run src/lib/yamlGenerator.test.ts src/lib/yamlImporter.test.ts`

Expected: both commands FAIL before the environment model supports deployment verification.

- [ ] **Step 3: Implement config types, schema, validation, and example**

Add `DeploymentVerification DeploymentVerificationConfig` to `Environment` with `yaml:"deployment_verification,omitempty"`. Keep provider empty equivalent to manual so old YAML remains valid.

Extend the wizard environment model, draft persistence, YAML importer, and YAML generator with the same nested shape. `EnvListItem.vue` shows provider-specific fields only after the user selects `http` or `k8s`; selecting `manual` emits no provider block. Add round-trip tests proving import → draft → generation preserves HTTP and K8s values, while legacy environments remain byte-semantically unchanged.

- [ ] **Step 4: Write failing provider tests with fake HTTP and fake K8s readers**

HTTP must use a bounded timeout, reject redirects to a different host, cap body size, follow RFC 6901 JSON Pointer, and never store response headers. K8s must use the configured cluster/namespace/deployment mapping and compare the configured annotation or image label per repository.

- [ ] **Step 5: Implement providers behind injected read-only clients**

Neither provider may mutate runtime state. Return observations with source, observed version/commits, timestamp, and sanitized diagnostics. Network/auth errors produce `unavailable`, not `matched`.

- [ ] **Step 6: Verify and commit**

Run: `go test ./internal/config ./internal/bughub -run 'TestDeploymentVerification|TestHTTPVersionVerifier|TestK8sVersionVerifier' -race`

Run: `npm --prefix web test -- --run src/lib/yamlGenerator.test.ts src/lib/yamlImporter.test.ts`

Expected: Go and Web tests PASS.

```bash
git add internal/config/types_deployment_verification.go internal/config/types.go internal/config/validate.go internal/config/deployment_verification_test.go schema/troubleshooter.schema.yaml examples/three-tier-troubleshooter.yaml web/src/lib/yamlGenerator.ts web/src/lib/yamlGenerator.test.ts web/src/lib/yamlImporter.ts web/src/lib/yamlImporter.test.ts web/src/lib/useWizardDraft.ts web/src/pages/InitPage.vue web/src/components/EnvListItem.vue internal/bughub/workflow_deployment_http.go internal/bughub/workflow_deployment_k8s.go internal/bughub/workflow_deployment_providers_test.go
git commit -m "feat: verify deployed runtime versions"
```

---

### Task 12: Reuse the validation Agent for regression and cycle failures back to investigation

**Files:**
- Create: `internal/bughub/workflow_regression.go`
- Create: `internal/bughub/workflow_regression_test.go`
- Modify: `internal/bughub/workflow_phase_runner.go`
- Modify: `internal/bughub/workflow_orchestrator.go`
- Modify: `templates/workspace/skills/bug-verifier/SKILL.md.tmpl`
- Modify: `web/src/components/BugCaseLifecycle.vue`

**Interfaces:**
- Consumes: matched `DeploymentObservation`, original validation attempt/evidence, expected fix commits.
- Produces: regression attempt input, `fixed_verified` closure, and `still_reproduces` next-cycle investigation input.

- [ ] **Step 1: Write failing full regression transition tests**

Assert:

```go
attempt, err := o.StartRegression(ctx, caseID, expectedVersion)
if err != nil { t.Fatal(err) }
if attempt.Phase != PhaseRegression || attempt.Mode != AttemptRegression { t.Fatalf("attempt=%+v", attempt) }
```

Reject start when deployment is unverified, observation environment differs, expected commits changed, or an identical scenario/version regression key already exists.

- [ ] **Step 2: Verify RED**

Run: `go test ./internal/bughub -run '^TestRegression'`

Expected: FAIL.

- [ ] **Step 3: Build deterministic regression input**

Input contains original reproduction steps, expected behavior, references to original evidence, expected fix commits, observed runtime version, target environment, and a scenario hash. It explicitly requires new timestamps/request IDs/artifacts.

- [ ] **Step 4: Implement outcomes**

- `fixed_verified`: transition to terminal state, record closure timestamp, preserve all attempts.
- `still_reproduces`: finish regression, increment cycle, create a new investigation attempt whose input includes new evidence plus a delta section explaining which original observations remain unexplained.
- `insufficient_info`: move to `waiting_evidence` without incrementing cycle.

- [ ] **Step 5: Verify fresh evidence and cycle isolation**

Run: `go test ./internal/bughub -run '^TestRegression' -race`

Expected: PASS; evidence from an earlier attempt alone cannot yield `fixed_verified`.

- [ ] **Step 6: Commit regression reuse**

```bash
git add internal/bughub/workflow_regression.go internal/bughub/workflow_regression_test.go internal/bughub/workflow_phase_runner.go internal/bughub/workflow_orchestrator.go templates/workspace/skills/bug-verifier/SKILL.md.tmpl web/src/components/BugCaseLifecycle.vue
git commit -m "feat: close incidents with regression validation"
```

---

### Task 13: Add workflow metrics, wait indicators, and bounded reminders

**Files:**
- Create: `internal/bughub/workflow_metrics.go`
- Create: `internal/bughub/workflow_metrics_test.go`
- Create: `internal/bughub/workflow_reminders.go`
- Create: `internal/bughub/workflow_reminders_test.go`
- Modify: `cmd/tshoot-desktop/main.go`
- Modify: `cmd/tshoot-desktop/bindings_bug_workflow.go`
- Create: `web/src/components/BugWorkflowMetrics.vue`
- Create: `web/src/components/BugWorkflowMetrics.test.ts`
- Modify: `web/src/pages/BugWorkbenchPage.vue`

**Interfaces:**
- Consumes: transition events and Case timestamps.
- Produces: `WorkflowMetrics`, stage-duration queries, blocker distribution, automation ratio, first-regression success rate, and local non-spamming wait reminders.

- [ ] **Step 1: Write failing deterministic metric tests**

Use a fixed clock and event fixture to assert validation, investigation, fix, human deployment wait, regression, total lead time, `still_reproduces` rate, retry count, and blocker counts. Waiting time must not be attributed to Agent execution.

- [ ] **Step 2: Write failing reminder tests**

One Case in `waiting_deployment` for 24 hours emits one local reminder; repeated polls do not emit another until the configured 24-hour interval. Terminal, prod, and user-snoozed Cases do not auto-notify.

- [ ] **Step 3: Verify RED**

Run: `go test ./internal/bughub -run 'TestWorkflowMetrics|TestWorkflowReminders'`

Expected: FAIL.

- [ ] **Step 4: Implement event-derived metrics and reminders**

Metrics are read-only SQL queries or deterministic folds over TransitionEvents. They must never modify confidence or trigger a phase. Reminder delivery uses the existing desktop notification mechanism and stores its last-sent event/idempotency key.

- [ ] **Step 5: Implement a compact metrics panel**

Show median stage duration, total open Cases, waiting-deployment age, first-regression success, and blocker distribution. Hide the panel when fewer than five completed Cases exist to avoid misleading rates.

- [ ] **Step 6: Verify and commit**

Run: `go test ./internal/bughub -run 'TestWorkflowMetrics|TestWorkflowReminders' -race`

Run: `cd web && npm test -- --run src/components/BugWorkflowMetrics.test.ts src/pages/BugWorkbenchPage.test.ts`

Expected: PASS.

```bash
git add internal/bughub/workflow_metrics.go internal/bughub/workflow_metrics_test.go internal/bughub/workflow_reminders.go internal/bughub/workflow_reminders_test.go cmd/tshoot-desktop/main.go cmd/tshoot-desktop/bindings_bug_workflow.go web/src/components/BugWorkflowMetrics.vue web/src/components/BugWorkflowMetrics.test.ts web/src/pages/BugWorkbenchPage.vue
git commit -m "feat: measure incident workflow outcomes"
```

---

### Task 14: Add full offline workflow E2E, documentation, ADR, and final verification

**Files:**
- Create: `internal/bughub/workflow_e2e_test.go`
- Create: `cmd/tshoot-desktop/bindings_bug_workflow_e2e_test.go`
- Modify: `README.md`
- Modify: `docs/troubleshooting-flow.md`
- Modify: `CONTRIBUTING.md`
- Modify: `docs/decisions.md`

**Interfaces:**
- Consumes: complete workflow.
- Produces: stable offline regression coverage, operator documentation, migration notes, and final evidence.

- [ ] **Step 1: Add a complete fixed-verified offline E2E**

Use temporary local Git remotes and fake phase runners to execute:

```text
pending_validation -> reproduced -> investigating -> root_cause_ready
-> waiting_fix_approval -> fixing -> fix_pushed -> waiting_merge_approval
-> merging -> waiting_deployment -> deployment_verified
-> regression_validating -> fixed_verified
```

Assert two distinct approvals, exact fix and merge commits, one push, one deployment observation, fresh regression evidence, one event per idempotency key, and successful recovery after reopening SQLite between merge and deployment notification.

- [ ] **Step 2: Add failure-path E2E cases**

Cover:

- missing evidence pauses before investigation.
- stale fix authorization is rejected.
- target branch changes after merge approval.
- conflict does not modify the environment branch.
- SSH push failure preserves the merge commit.
- duplicate deployment notification does not duplicate regression.
- button and natural-language deployment notifications use the same idempotent verifier path; negative language does not trigger it.
- mismatched deployed version remains unverified.
- regression `still_reproduces` increments cycle and returns to investigation.
- two repositories with only one deployed remain unverified.
- imported legacy Case stays archived until explicitly restarted.

- [ ] **Step 3: Document user and operator flow**

README documents the six phases, two approvals, manual deployment boundary, runtime version gate, and regression reuse. `docs/troubleshooting-flow.md` explains the Case state machine and evidence freshness. CONTRIBUTING requires transition-table tests, idempotency tests, crash recovery, temp Git remotes, no real deployment, and secret-redaction fixtures.

- [ ] **Step 4: Append an ADR**

Append `SQLite 状态机作为故障闭环真源` to `docs/decisions.md`. Record the rejected prompt-text and external workflow-engine approaches, one-writer orchestration, two authorization gates, manual deployment boundary, version verification, legacy archival migration, and consequences of maintaining SQLite migrations.

- [ ] **Step 5: Run complete verification in order**

```bash
go test ./... -race
scripts/check-go-coverage.sh
scripts/test-skill-scripts.sh
npm --prefix web test -- --run
npm --prefix web run build
make lint
make build
git diff --check
```

Expected: every command exits 0. The known macOS `LC_DYSYMTAB` linker warning may appear but must remain non-fatal. Any concurrency or SQLite locking flake must be investigated; a rerun-only claim is not acceptable.

- [ ] **Step 6: Inspect exact branch scope**

```bash
BASE=$(git merge-base HEAD test)
git diff --stat "$BASE"..HEAD
git diff --check "$BASE"..HEAD
git status --short
```

Expected: only workflow implementation, tests, schema/example, skills, UI, docs, and dependency changes; clean worktree.

- [ ] **Step 7: Commit final E2E and docs**

```bash
git add internal/bughub/workflow_e2e_test.go cmd/tshoot-desktop/bindings_bug_workflow_e2e_test.go README.md docs/troubleshooting-flow.md CONTRIBUTING.md docs/decisions.md
git commit -m "test: verify durable incident workflow"
```

- [ ] **Step 8: Request whole-branch review**

Review from the branch merge base must explicitly inspect transition legality, transaction boundaries, idempotency, secret redaction, legacy migration, phase output parsing, retry side effects, Git approval scope, multi-repo partial completion, deployed-version correctness, regression evidence freshness, UI event ordering, and metrics isolation. Address every blocker/high finding and rerun the complete verification sequence before completion.
