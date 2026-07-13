# Bug Inbox and Incident Workbench Split Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Split Bug ticket browsing and durable incident handling into separate sidebar pages, allow any Bug to open or start its one active Case, and add an audited reset command that archives the old Case and restarts validation.

**Architecture:** Keep `CaseOrchestrator` as the only workflow writer. Extend SQLite with reset relationships and atomic create/reset operations, expose one desktop/TypeScript bridge command, then split the current Vue page into a Bug inbox and an incident workbench backed by shared ticket components. Preserve legacy `InvestigationRun` output as collapsed read-only history only.

**Tech Stack:** Go 1.24, SQLite (`modernc.org/sqlite`), Wails v2, Vue 3 Composition API, Vue Router, TypeScript, Vitest, Vue Test Utils.

## Global Constraints

- `/bugs` is the browse/configuration route; `/incidents` is the only active durable workflow route.
- One Bug has at most one non-terminal Case. Terminal statuses are `fixed_verified`, `legacy_archived`, and `reset_archived`.
- Reset never deletes artifacts, approvals, code changes, deployment observations, events, or external Git/deployment effects.
- Reset archives the old Case and creates its replacement in one SQLite transaction.
- Commands use optimistic Case versions and idempotency keys; replay never duplicates attempts or Agent scheduling.
- Studio never deploys applications; existing human deployment and version-gated regression behavior remains unchanged.
- Preserve the current uncommitted legacy-read-only page change as the execution baseline.
- Never stage generated `internal/webui/dist/`; preserve `internal/webui/dist/.gitkeep` exactly.

## File Map

- `internal/bughub/workflow_{types,transition,store_schema,store,orchestrator,recovery,metrics,reminders}.go`: reset model, atomicity, scheduling, and projections.
- `cmd/tshoot-desktop/bindings_bug_workflow.go`: desktop reset command.
- `web/src/lib/bridge/bugWorkflow.ts`: TypeScript reset API.
- `web/src/lib/useBugTickets.ts`: shared Bug loading/search/selection.
- `web/src/components/BugTicketList.vue`, `BugTicketDetail.vue`, `BugBotPicker.vue`: shared presentation.
- `web/src/pages/BugInboxPage.vue`: full browse-only ticket page.
- `web/src/pages/IncidentWorkbenchPage.vue`: Bug-driven durable workflow page.
- `web/src/router/index.ts`, `web/src/App.vue`: routes and sidebar.

---

### Task 0: Preserve the verified legacy read-only baseline

**Files:**
- Modify: `web/src/pages/BugWorkbenchPage.vue`
- Modify: `web/src/pages/BugWorkbenchPage.test.ts`

**Interfaces:**
- Produces: a clean baseline where `startIncidentCase` is the only active start path and legacy output has no stop/fix/supplement mutation.

- [ ] **Step 1: Re-run focused tests**

```bash
npm --prefix web test -- --run src/pages/BugWorkbenchPage.test.ts
```

Expected: 42 tests pass, including the collapsed read-only legacy run test and `startBugInvestigation` not-called assertion.

- [ ] **Step 2: Verify no production legacy mutations remain**

```bash
test -z "$(rg -n 'cancelBugInvestigation|continueBugInvestigation|startBugFix|supplement-fab|启动修复 Agent' web/src/pages/BugWorkbenchPage.vue || true)"
git diff --check
```

Expected: exit 0.

- [ ] **Step 3: Commit only these files**

```bash
git add web/src/pages/BugWorkbenchPage.vue web/src/pages/BugWorkbenchPage.test.ts
git commit -m "fix: make legacy bug runs read-only"
```

---

### Task 1: Add `reset_archived` and schema v6 relationships

**Files:**
- Modify: `internal/bughub/workflow_types.go`
- Modify: `internal/bughub/workflow_transition.go`
- Modify: `internal/bughub/workflow_store_schema.go`
- Modify: `internal/bughub/workflow_store.go`
- Modify: `internal/bughub/workflow_transition_test.go`
- Modify: `internal/bughub/workflow_store_test.go`
- Modify: `internal/bughub/workflow_deployment_test.go`
- Modify: `internal/bughub/workflow_metrics.go`
- Modify: `internal/bughub/workflow_metrics_test.go`

**Interfaces:**
- Produces: `CaseResetArchived`, two relationship fields, and `IsTerminalCaseStatus(CaseStatus) bool`.

- [ ] **Step 1: Write failing model/transition/reopen tests**

```go
func TestResetArchiveTransitions(t *testing.T) {
    for _, from := range []CaseStatus{CasePendingValidation, CaseValidating, CaseWaitingEvidence, CaseInvestigating, CaseWaitingFixApproval, CaseFixing, CaseWaitingMergeApproval, CaseMerging, CaseWaitingDeployment, CaseRegressionValidating} {
        if !CanTransition(from, CaseResetArchived) { t.Fatalf("%s must reset", from) }
    }
    for _, from := range []CaseStatus{CaseFixedVerified, CaseLegacyArchived, CaseResetArchived} {
        if CanTransition(from, CaseResetArchived) { t.Fatalf("terminal %s reset unexpectedly", from) }
    }
}

func TestResetRelationshipFieldsSurviveReopen(t *testing.T) {
    path := filepath.Join(t.TempDir(), "workflow.db")
    store, err := OpenCaseStore(path)
    if err != nil { t.Fatal(err) }
    closed := time.Now().UTC()
    old := IncidentCase{ID: "case-old", BugID: "bug-840", Status: CaseResetArchived, CycleNumber: 1, Version: 2, SupersededByCaseID: "case-new", ClosedAt: &closed}
    next := IncidentCase{ID: "case-new", BugID: "bug-840", Status: CasePendingValidation, CycleNumber: 1, Version: 1, ResetFromCaseID: "case-old"}
    if err := store.CreateCase(context.Background(), old); err != nil { t.Fatal(err) }
    if err := store.CreateCase(context.Background(), next); err != nil { t.Fatal(err) }
    if err := store.Close(); err != nil { t.Fatal(err) }
    reopened, err := OpenCaseStore(path)
    if err != nil { t.Fatal(err) }
    defer reopened.Close()
    gotOld, _ := reopened.GetCase(context.Background(), old.ID)
    gotNext, _ := reopened.GetCase(context.Background(), next.ID)
    if gotOld.SupersededByCaseID != next.ID || gotNext.ResetFromCaseID != old.ID { t.Fatalf("old=%+v next=%+v", gotOld, gotNext) }
}
```

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/bughub -run 'TestResetArchiveTransitions|TestResetRelationshipFieldsSurviveReopen' -count=1
```

Expected: compile failure for missing reset status/fields.

- [ ] **Step 3: Implement exact model and migration**

```go
const CaseResetArchived CaseStatus = "reset_archived"

type IncidentCase struct {
    ResetFromCaseID    string `json:"reset_from_case_id,omitempty"`
    SupersededByCaseID string `json:"superseded_by_case_id,omitempty"`
}

func IsTerminalCaseStatus(status CaseStatus) bool {
    return status == CaseFixedVerified || status == CaseLegacyArchived || status == CaseResetArchived
}
```

Set schema version 6 and apply:

```sql
ALTER TABLE incident_cases ADD COLUMN reset_from_case_id TEXT NOT NULL DEFAULT '';
ALTER TABLE incident_cases ADD COLUMN superseded_by_case_id TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_cases_bug_updated ON incident_cases(bug_id, updated_at);
```

Update every Case INSERT/SELECT/scan, clone, `legacyWorkflowTableColumns`, required indexes, and schema fingerprint. Permit every non-terminal status to transition to `CaseResetArchived`; allow no outbound transition. Exclude reset archives alongside legacy archives from completed outcome metrics.

- [ ] **Step 4: Verify GREEN**

```bash
go test ./internal/bughub -run 'TestResetArchiveTransitions|TestResetRelationshipFieldsSurviveReopen|TestWorkflowStoreSchema|TestWorkflowMetrics' -count=1
go test ./internal/bughub -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/bughub/workflow_types.go internal/bughub/workflow_transition.go internal/bughub/workflow_store_schema.go internal/bughub/workflow_store.go internal/bughub/workflow_transition_test.go internal/bughub/workflow_store_test.go internal/bughub/workflow_deployment_test.go internal/bughub/workflow_metrics.go internal/bughub/workflow_metrics_test.go
git commit -m "feat: model reset-archived incident cases"
```

---

### Task 2: Enforce one active Case per Bug during creation

**Files:**
- Modify: `internal/bughub/workflow_store.go`
- Modify: `internal/bughub/workflow_orchestrator.go`
- Modify: `internal/bughub/workflow_store_test.go`
- Modify: `internal/bughub/workflow_orchestrator_test.go`

**Interfaces:**
- Produces: `CaseCreationResult{Case IncidentCase, Replay bool, ExistingOpen bool}`.

- [ ] **Step 1: Write failing atomicity tests**

`TestCreateCaseWithIdentityReturnsExistingOpenCaseForBug` must create `case-a` for `bug-840`, request `case-b` with a different idempotency key, and assert `result.Case.ID == "case-a"`, `result.ExistingOpen`, one listed Case, and one attempt. `TestCreateAndStartCaseConcurrentDifferentIDsSchedulesOnce` must release two goroutines simultaneously, collect both returned IDs, and assert the IDs match and the synchronized runner start counter equals one.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/bughub -run 'TestCreateCaseWithIdentityReturnsExistingOpenCaseForBug|TestCreateAndStartCaseConcurrentDifferentIDsSchedulesOnce' -count=1
```

Expected: duplicate Cases or duplicate starts.

- [ ] **Step 3: Implement atomic reuse**

```go
type CaseCreationResult struct {
    Case IncidentCase
    Replay bool
    ExistingOpen bool
}

func (s *CaseStore) CreateCaseWithIdentity(ctx context.Context, creation CaseCreation) (CaseCreationResult, error)
```

Within the existing creation write transaction, after idempotency replay lookup and before INSERT, query the newest Case for the same `bug_id` excluding the three terminal statuses. Return it with `ExistingOpen=true`. Serialize `CreateAndStartCase` by `create-start-bug:<bug-id>`; when an existing Case is returned, do not call `StartCase`.

When reusing an open Case, insert an `open_case_reused` command-result event containing the request fingerprint and serialized Case result without incrementing Case version. This identity record ensures replay still returns the same Case even if that Case later reaches a terminal state.

- [ ] **Step 4: Verify GREEN under race**

```bash
go test ./internal/bughub -run 'TestCreateCaseWithIdentityReturnsExistingOpenCaseForBug|TestCreateAndStartCaseConcurrentDifferentIDsSchedulesOnce|TestCreateAndStartCase' -race -count=1
go test ./internal/bughub -count=1
```

- [ ] **Step 5: Commit**

```bash
git add internal/bughub/workflow_store.go internal/bughub/workflow_orchestrator.go internal/bughub/workflow_store_test.go internal/bughub/workflow_orchestrator_test.go
git commit -m "fix: enforce one active incident case per bug"
```

---

### Task 3: Add the atomic reset/replacement store operation

**Files:**
- Modify: `internal/bughub/workflow_store.go`
- Create: `internal/bughub/workflow_reset_test.go`

**Interfaces:**
- Produces:

```go
type CaseReset struct {
    CaseID, NewCaseID, IdempotencyKey, ActorID string
    ExpectedVersion int64
    SelectedBotKey string
    RequestJSON json.RawMessage
}
type CaseResetResult struct {
    Archived IncidentCase
    Replacement IncidentCase
    CancelledAttemptID string
    Replay bool
}
func (s *CaseStore) ResetCaseWithReplacement(context.Context, CaseReset) (CaseResetResult, error)
```

- [ ] **Step 1: Write failing store tests**

Create tests named `TestResetCaseWithReplacementIsAtomic`, `TestResetCaseWithReplacementReplaysSameReplacement`, `TestResetCaseWithReplacementRejectsTerminalAndStaleCases`, `TestResetCaseWithReplacementPreservesRelatedRecords`, `TestResetCaseWithReplacementRollsBackInjectedFailure`, `TestResetCaseWithReplacementSurvivesReopen`, and `TestResetCaseWithReplacementRedactsStoredRequest`. Assert cancelled current attempt, returned `CancelledAttemptID`, cleared run claim, deleted current fix checkpoint, old `closed_at`, both relationship IDs, two audit events, unchanged artifact/approval/change/observation records, and absence of raw token/Cookie/Authorization/password/URL-userinfo fixtures from event payload and stored command identity.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/bughub -run 'TestResetCaseWithReplacement' -count=1
```

Expected: missing store API.

- [ ] **Step 3: Implement one transaction**

Use this exact operation order:

```text
idempotency replay lookup and fingerprint comparison
load old Case and compare version
reject terminal Case and duplicate replacement ID
cancel queued/running current attempt and clear run_claim_token
delete the current fix checkpoint when present
update old Case to reset_archived, closed_at, superseded_by_case_id, version+1
insert replacement pending_validation Case with reset_from_case_id
insert case_reset event with serialized replacement result
insert case_created_from_reset event on replacement
commit
```

Fingerprint old/new IDs, version, bot, actor, key, and request JSON. Changed input with the same key returns `ErrIdempotencyConflict`.

- [ ] **Step 4: Verify GREEN**

```bash
go test ./internal/bughub -run 'TestResetCaseWithReplacement' -race -count=1
```

- [ ] **Step 5: Commit**

```bash
git add internal/bughub/workflow_store.go internal/bughub/workflow_reset_test.go
git commit -m "feat: atomically reset and replace incident cases"
```

---

### Task 4: Orchestrate reset cancellation and fresh validation

**Files:**
- Modify: `internal/bughub/workflow_orchestrator.go`
- Modify: `internal/bughub/workflow_recovery.go`
- Modify: `internal/bughub/workflow_reminders.go`
- Modify: `internal/bughub/workflow_reset_test.go`
- Modify: `internal/bughub/workflow_recovery_test.go`
- Modify: `internal/bughub/workflow_reminders_test.go`

**Interfaces:**
- Produces:

```go
type ResetCaseCommand struct {
    CaseID, NewCaseID, IdempotencyKey, ActorID string
    ExpectedVersion int64
    Bug Bug
    Bot BotRef
    InputJSON json.RawMessage
}
func (o *CaseOrchestrator) ResetCase(context.Context, ResetCaseCommand) (IncidentCase, error)
```

- [ ] **Step 1: Write failing orchestration tests**

Add `TestResetCaseCancelsOldRunnerAndStartsReplacementValidation`, `TestResetCaseReplayDoesNotCancelOrStartTwice`, `TestResetCaseRacingCompletionHasOneWinningState`, `TestRecoveryIgnoresResetArchiveAndRecoversReplacement`, and `TestReminderNeverEmitsForResetArchive`.

- [ ] **Step 2: Verify RED**

```bash
go test ./internal/bughub -run 'TestResetCase|TestRecoveryIgnoresResetArchive|TestReminderNeverEmitsForResetArchive' -count=1
```

- [ ] **Step 3: Implement orchestration**

Validate IDs/version/actor/Bug/Bot/JSON, acquire `reset-case:<old-id>`, and call the store. On a non-replay result, call `cancelPhase(result.CancelledAttemptID)` once when that ID is non-empty, then start replacement validation:

```go
return o.StartCase(ctx, StartCaseCommand{
    CaseID: result.Replacement.ID,
    ExpectedVersion: result.Replacement.Version,
    IdempotencyKey: cmd.IdempotencyKey + ":start",
    ActorID: cmd.ActorID,
    Bug: cmd.Bug, Bot: cmd.Bot, InputJSON: cmd.InputJSON,
})
```

Reuse `phaseScheduleFailure` on start failure and never unarchive the old Case. On replay, return/replay the same replacement start without cancelling again. Recovery and reminder loops skip terminal Cases before attempt or deployment handling.

- [ ] **Step 4: Verify GREEN**

```bash
go test ./internal/bughub -run 'TestResetCase|TestRecoveryIgnoresResetArchive|TestReminderNeverEmitsForResetArchive' -race -count=1
go test ./internal/bughub -race
```

- [ ] **Step 5: Commit**

```bash
git add internal/bughub/workflow_orchestrator.go internal/bughub/workflow_recovery.go internal/bughub/workflow_reminders.go internal/bughub/workflow_reset_test.go internal/bughub/workflow_recovery_test.go internal/bughub/workflow_reminders_test.go
git commit -m "feat: restart validation through audited case reset"
```

---

### Task 5: Expose reset through desktop and TypeScript bridges

**Files:**
- Modify: `cmd/tshoot-desktop/bindings_bug_workflow.go`
- Modify: `cmd/tshoot-desktop/bindings_bug_workflow_test.go`
- Modify: `web/src/lib/bridge/bugWorkflow.ts`
- Modify: `web/src/lib/bridge/bugWorkflow.test.ts`
- Regenerate: `web/wailsjs/go/main/App.js`
- Regenerate: `web/wailsjs/go/main/App.d.ts`
- Regenerate: `web/wailsjs/go/models.ts`

**Interfaces:**
- Produces: `ResetIncidentCaseInput` and `resetIncidentCase(input): Promise<IncidentCase>`.

- [ ] **Step 1: Write failing bridge tests**

Add `TestResetIncidentCaseValidatesScalarsBeforeOpeningRuntime` with an empty `new_case_id` and assert the runtime factory remains unopened. Add `TestResetIncidentCaseForwardsContextAndEmitsReplacement` using `newWorkflowBindingApp`, a running Case and `workflowBindingRunner`; assert returned ID, reset relationships, old terminal status, one cancel and replacement start. Add `TestResetIncidentCaseDuplicateCommandSchedulesOnce` by calling the same input twice and asserting the same result ID and unchanged runner counters.

```ts
it('forwards resetIncidentCase to Wails', async () => {
  const input = { case_id: 'case-1', new_case_id: 'case-2', expected_version: 7, idempotency_key: 'reset:case-1:v7', actor_id: 'desktop-user', bot_key: 'base|codex' }
  await resetIncidentCase(input)
  expect(window.go.main.App.ResetIncidentCase).toHaveBeenCalledWith(input)
})
```

- [ ] **Step 2: Verify RED**

```bash
go test ./cmd/tshoot-desktop -run TestResetIncidentCase -count=1
npm --prefix web test -- --run src/lib/bridge/bugWorkflow.test.ts
```

- [ ] **Step 3: Implement and regenerate**

```go
type ResetIncidentCaseInput struct {
    CaseID string `json:"case_id"`
    NewCaseID string `json:"new_case_id"`
    BotKey string `json:"bot_key"`
    ExpectedVersion int64 `json:"expected_version"`
    IdempotencyKey string `json:"idempotency_key"`
    ActorID string `json:"actor_id"`
    InputJSON map[string]any `json:"input_json,omitempty"`
}
```

The binding validates both IDs, loads original Bug/Bot context, calls `orchestrator.ResetCase`, and emits the replacement snapshot. TypeScript adds `reset_archived`, both relationship fields, normalization, input type, and:

```ts
export async function resetIncidentCase(input: ResetIncidentCaseInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.ResetIncidentCase(input))
}
```

Run `make wails-gen`; inspect generated changes for only this method/model.

- [ ] **Step 4: Verify GREEN**

```bash
go test ./cmd/tshoot-desktop -run 'TestResetIncidentCase|TestStartIncidentCase' -count=1
npm --prefix web test -- --run src/lib/bridge/bugWorkflow.test.ts
```

- [ ] **Step 5: Commit**

```bash
git add cmd/tshoot-desktop/bindings_bug_workflow.go cmd/tshoot-desktop/bindings_bug_workflow_test.go web/src/lib/bridge/bugWorkflow.ts web/src/lib/bridge/bugWorkflow.test.ts web/wailsjs/go/main/App.js web/wailsjs/go/main/App.d.ts web/wailsjs/go/models.ts
git commit -m "feat: expose incident reset to the desktop workbench"
```

---

### Task 6: Extract shared Bug ticket units

**Files:**
- Create: `web/src/lib/useBugTickets.ts`
- Create: `web/src/lib/useBugTickets.test.ts`
- Create: `web/src/components/BugTicketList.vue`
- Create: `web/src/components/BugTicketList.test.ts`
- Create: `web/src/components/BugTicketDetail.vue`
- Create: `web/src/components/BugTicketDetail.test.ts`
- Create: `web/src/components/BugBotPicker.vue`
- Create: `web/src/components/BugBotPicker.test.ts`

**Interfaces:**
- Produces: `useBugTickets`, reusable list/detail, and robot picker with no bridge side effects inside components.

- [ ] **Step 1: Write failing tests for the exact contracts**

```ts
const tickets = useBugTickets({ listBugs, fetchBugByID })
// refs: bugs, selectedID, selectedBug, query, filteredBugs, loading, error
// methods: load(), select(id), fetchByID(id), clearSelection()
```

Tests prove deterministic filtering across title/source/source ID/environment/system/service hints, no guessed first selection for an invalid requested ID, full versus summary detail, list selection emits, attachment emits, and picker emits the exact Bot key.

- [ ] **Step 2: Verify RED**

```bash
npm --prefix web test -- --run src/lib/useBugTickets.test.ts src/components/BugTicketList.test.ts src/components/BugTicketDetail.test.ts src/components/BugBotPicker.test.ts
```

Expected: module-not-found failures.

- [ ] **Step 3: Implement minimal shared units**

Use these public component contracts:

```ts
defineProps<{ bugs: BugRecord[]; selectedId: string; loading?: boolean; query: string }>()
defineEmits<{ select: [id: string]; 'update:query': [value: string] }>()
```

```ts
defineProps<{ bug?: BugRecord; mode: 'full' | 'summary' }>()
defineEmits<{ previewAttachment: [index: number]; openIncident: [bugId: string] }>()
```

```ts
defineProps<{ matches: BotMatch[]; selectedKey: string; loading?: boolean }>()
defineEmits<{ select: [key: string] }>()
```

Move the existing sanitized Markdown behavior into the detail component; do not add another raw HTML path.

- [ ] **Step 4: Verify GREEN and type safety**

```bash
npm --prefix web test -- --run src/lib/useBugTickets.test.ts src/components/BugTicketList.test.ts src/components/BugTicketDetail.test.ts src/components/BugBotPicker.test.ts
npm --prefix web run build
```

- [ ] **Step 5: Commit**

```bash
git add web/src/lib/useBugTickets.ts web/src/lib/useBugTickets.test.ts web/src/components/BugTicketList.vue web/src/components/BugTicketList.test.ts web/src/components/BugTicketDetail.vue web/src/components/BugTicketDetail.test.ts web/src/components/BugBotPicker.vue web/src/components/BugBotPicker.test.ts
git commit -m "refactor: extract shared bug ticket workbench units"
```

---

### Task 7: Build the browse-only Bug inbox

**Files:**
- Create: `web/src/pages/BugInboxPage.vue`
- Create: `web/src/pages/BugInboxPage.test.ts`
- Modify: `web/src/pages/BugWorkbenchPage.vue`
- Modify: `web/src/pages/BugWorkbenchPage.test.ts`

**Interfaces:**
- Consumes: Task 6 units.
- Produces: full Bug platform/ticket page without durable workflow mutations.

- [ ] **Step 1: Write failing page tests**

```ts
expect(wrapper.text()).toContain('Bug 工单')
expect(wrapper.text()).toContain('复现步骤')
expect(wrapper.text()).not.toContain('开始故障闭环')
expect(wrapper.text()).not.toContain('允许修复')
expect(wrapper.find('.legacy-history').attributes('open')).toBeUndefined()
await wrapper.get('[data-action="open-incident"]').trigger('click')
expect(router.push).toHaveBeenCalledWith({ path: '/incidents', query: { bug_id: 'zentao-840' } })
```

Retain platform login/configuration, sync, manual fetch, attachments, rich text, and legacy read-only tests from the old page.

- [ ] **Step 2: Verify RED**

```bash
npm --prefix web test -- --run src/pages/BugInboxPage.test.ts
```

- [ ] **Step 3: Move browse responsibilities**

Create `BugInboxPage` from the current page, remove IncidentCase state/actions and internal workbench tabs, use `BugTicketList` and full `BugTicketDetail`, and add:

```ts
function openIncident(bugID: string) {
  router.push({ path: '/incidents', query: { bug_id: bugID } })
}
```

Keep legacy runs collapsed and read-only. Temporarily make `BugWorkbenchPage` a thin wrapper around `BugInboxPage` so intermediate builds stay green.

- [ ] **Step 4: Verify GREEN**

```bash
npm --prefix web test -- --run src/pages/BugInboxPage.test.ts src/pages/BugWorkbenchPage.test.ts
npm --prefix web run build
```

- [ ] **Step 5: Commit**

```bash
git add web/src/pages/BugInboxPage.vue web/src/pages/BugInboxPage.test.ts web/src/pages/BugWorkbenchPage.vue web/src/pages/BugWorkbenchPage.test.ts
git commit -m "refactor: make bug inbox a browse-only page"
```

---

### Task 8: Build Bug-driven Incident workbench selection

**Files:**
- Create: `web/src/pages/IncidentWorkbenchPage.vue`
- Create: `web/src/pages/IncidentWorkbenchPage.test.ts`
- Modify: `web/src/lib/useIncidentCase.ts`
- Modify: `web/src/lib/useIncidentCase.test.ts`

**Interfaces:**
- Consumes: Task 6 units and existing `BugCaseLifecycle`.
- Produces: `terminalCaseStatuses`, `casesForBug`, `activeCaseForBug`, and URL-selected workflow page.

- [ ] **Step 1: Write failing selection/start tests**

Add tests named:

```text
restores the exact bug_id from the route without guessing
updates the query when another Bug is selected
opens the newest active Case without calling Start
shows start for a Bug with no Case
shows new round when all Cases are terminal
uses an existing Case returned by the backend
shows a recoverable empty state for an invalid URL Bug
```

- [ ] **Step 2: Verify RED**

```bash
npm --prefix web test -- --run src/pages/IncidentWorkbenchPage.test.ts src/lib/useIncidentCase.test.ts
```

- [ ] **Step 3: Implement deterministic resolution**

```ts
export const terminalCaseStatuses = new Set<CaseStatus>(['fixed_verified', 'legacy_archived', 'reset_archived'])
export function casesForBug(cases: IncidentCase[], bugID: string): IncidentCase[] {
  return cases.filter(item => item.bug_id === bugID).sort((a, b) => b.updated_at.localeCompare(a.updated_at) || a.id.localeCompare(b.id))
}
export function activeCaseForBug(cases: IncidentCase[], bugID: string): IncidentCase | undefined {
  return casesForBug(cases, bugID).find(item => !terminalCaseStatuses.has(item.status))
}
```

Use `router.replace({ query: { ...route.query, bug_id: id } })` on selection. Start with a fresh candidate Case ID; if the backend returns another existing ID, select/refresh it and display “已打开现有闭环”.

- [ ] **Step 4: Verify GREEN**

```bash
npm --prefix web test -- --run src/pages/IncidentWorkbenchPage.test.ts src/lib/useIncidentCase.test.ts
npm --prefix web run build
```

- [ ] **Step 5: Commit**

```bash
git add web/src/pages/IncidentWorkbenchPage.vue web/src/pages/IncidentWorkbenchPage.test.ts web/src/lib/useIncidentCase.ts web/src/lib/useIncidentCase.test.ts
git commit -m "feat: select any bug in the incident workbench"
```

---

### Task 9: Add accessible reset UI and reset history

**Files:**
- Modify: `web/src/pages/IncidentWorkbenchPage.vue`
- Modify: `web/src/pages/IncidentWorkbenchPage.test.ts`
- Modify: `web/src/components/BugCaseLifecycle.vue`
- Modify: `web/src/components/BugCaseLifecycle.test.ts`
- Modify: `web/src/components/BugCaseArtifacts.vue`
- Modify: `web/src/components/BugCaseArtifacts.test.ts`

**Interfaces:**
- Consumes: `resetIncidentCase` from Task 5.
- Produces: lifecycle `reset` event and linked archive rendering.

- [ ] **Step 1: Write failing dialog/lifecycle tests**

Prove reset is separate from the one-primary-action rule, appears only for non-terminal Cases, and the dialog has `role="dialog"`, cancel-first focus, Escape close, focus restoration, immutable-side-effect warning, pending disablement, and this call:

```ts
expect(resetIncidentCase).toHaveBeenCalledWith(expect.objectContaining({
  case_id: 'case-1',
  new_case_id: expect.stringMatching(/^case-reset-/),
  expected_version: 7,
  idempotency_key: expect.stringMatching(/^reset:case-1:v7:/),
  actor_id: 'desktop-user',
  bot_key: 'base|codex',
}))
```

- [ ] **Step 2: Verify RED**

```bash
npm --prefix web test -- --run src/pages/IncidentWorkbenchPage.test.ts src/components/BugCaseLifecycle.test.ts src/components/BugCaseArtifacts.test.ts
```

- [ ] **Step 3: Implement reset interaction**

Extend lifecycle emits:

```ts
defineEmits<{
  select: [caseID: string]
  refresh: []
  primary: [action: CasePrimaryAction]
  reset: [incident: IncidentCase]
}>()
```

Render `reset_archived` as “已重置归档” and both relation IDs as navigable Case references. The page owns the confirmation dialog and calls `resetIncidentCase`; success selects and refreshes the replacement.

- [ ] **Step 4: Verify GREEN**

```bash
npm --prefix web test -- --run src/pages/IncidentWorkbenchPage.test.ts src/components/BugCaseLifecycle.test.ts src/components/BugCaseArtifacts.test.ts
npm --prefix web run build
```

- [ ] **Step 5: Commit**

```bash
git add web/src/pages/IncidentWorkbenchPage.vue web/src/pages/IncidentWorkbenchPage.test.ts web/src/components/BugCaseLifecycle.vue web/src/components/BugCaseLifecycle.test.ts web/src/components/BugCaseArtifacts.vue web/src/components/BugCaseArtifacts.test.ts
git commit -m "feat: reset incident workflows without losing history"
```

---

### Task 10: Wire separate routes/sidebar and remove the combined page

**Files:**
- Modify: `web/src/router/index.ts`
- Modify: `web/src/App.vue`
- Modify: `web/src/App.test.ts`
- Modify: `web/src/pages/HomePage.vue`
- Modify: `web/src/pages/HomePage.test.ts`
- Delete: `web/src/pages/BugWorkbenchPage.vue`
- Delete: `web/src/pages/BugWorkbenchPage.test.ts`

**Interfaces:**
- Produces: final `/bugs` and `/incidents` navigation.

- [ ] **Step 1: Write failing navigation tests**

Export `navItems` if needed and assert both exact entries and both router matches:

```ts
expect(navItems).toEqual(expect.arrayContaining([
  expect.objectContaining({ path: '/bugs', label: 'Bug 工单' }),
  expect.objectContaining({ path: '/incidents', label: '故障闭环' }),
]))
```

- [ ] **Step 2: Verify RED**

```bash
npm --prefix web test -- --run src/App.test.ts src/pages/HomePage.test.ts
```

- [ ] **Step 3: Wire final routes and copy**

```ts
{ path: '/bugs', name: 'Bugs', component: () => import('../pages/BugInboxPage.vue') },
{ path: '/incidents', name: 'Incidents', component: () => import('../pages/IncidentWorkbenchPage.vue') },
```

```ts
{ path: '/bugs', icon: '🐞', label: 'Bug 工单', desc: '同步工单平台，查看完整 Bug 详情' },
{ path: '/incidents', icon: '🔁', label: '故障闭环', desc: '选择 Bug，完成验证、排障、修复和回归' },
```

Update Home navigation copy and delete the compatibility wrapper and its old combined-page test after all assertions have moved.

Add a responsive contract test that mounts both pages at 375, 768, 1024, and 1440 pixel fixtures and asserts the page root advertises those supported viewports, uses overflow-safe grid/flex rules, and keeps reset dialog controls keyboard reachable.

- [ ] **Step 4: Verify GREEN**

```bash
npm --prefix web test -- --run
npm --prefix web run build
```

- [ ] **Step 5: Commit**

```bash
git add web/src/router/index.ts web/src/App.vue web/src/App.test.ts web/src/pages/HomePage.vue web/src/pages/HomePage.test.ts
git rm web/src/pages/BugWorkbenchPage.vue web/src/pages/BugWorkbenchPage.test.ts
git commit -m "feat: split bug inbox and incident workflow navigation"
```

---

### Task 11: Add E2E coverage, ADR, and final verification

**Files:**
- Modify: `internal/bughub/workflow_e2e_test.go`
- Modify: `docs/decisions.md`

**Interfaces:**
- Consumes: all prior tasks.
- Produces: restart-safe full-loop regression and durable decision record.

- [ ] **Step 1: Write the failing E2E scenario**

Add `TestWorkflowE2E_ResetStartsFreshAuditedCase`. It starts Bug 840, repeats Start with another candidate ID, persists representative evidence/approval/change/observation records, resets, reopens SQLite, verifies the old archive and retained records, verifies the replacement validation state, replays Reset without another runner start, and completes the replacement independently.

- [ ] **Step 2: Verify RED, then add only required integration glue**

```bash
go test ./internal/bughub -run TestWorkflowE2E_ResetStartsFreshAuditedCase -race -count=1
```

Expected before glue: failure on an uncovered integration boundary; expected after glue: PASS.

- [ ] **Step 3: Append an ADR without modifying old entries**

```markdown
## 2026-07-13 · Bug 输入与故障闭环拆分，重置采用新 Case

**背景**：Bug 平台管理、工单详情与持久化闭环共用一个页面，旧运行记录和新 Case 容易被理解为两套流程。

**决策**：`/bugs` 只管理和浏览工单，`/incidents` 是唯一主动闭环入口；同一 Bug 最多一个未结束 Case。重置不回滚外部副作用、不删除历史，而是在同一 SQLite 事务中把旧 Case 标为 `reset_archived` 并创建关系明确的新 Case，从验证阶段重新开始。

**后果**：导航和职责更清晰，重置可审计且可跨重启恢复；代价是新增 schema v6、跨 Case 原子命令和更多竞态/幂等测试。
```

- [ ] **Step 4: Run final verification**

```bash
git diff --check
go test ./... -race
scripts/check-go-coverage.sh
npm --prefix web test -- --run
npm --prefix web run build
make lint
make build
test "$(git status --short internal/webui/dist/.gitkeep)" = ""
```

Expected: every command exits 0, Web has zero failed tests, and `.gitkeep` is unchanged.

- [ ] **Step 5: Review acceptance criteria and forbidden calls**

Verify every design acceptance criterion. `/bugs` must have no durable or legacy mutation buttons. `/incidents` must not import or call `startBugInvestigation`, `continueBugInvestigation`, or `startBugFix`.

- [ ] **Step 6: Commit**

```bash
git add internal/bughub/workflow_e2e_test.go docs/decisions.md
git commit -m "test: verify resettable bug incident workflow end to end"
```
