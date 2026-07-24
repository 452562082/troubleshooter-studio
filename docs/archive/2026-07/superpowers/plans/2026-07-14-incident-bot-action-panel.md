# Incident Bot Action Panel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Put open, enter, and restart actions directly below the incident Bot picker, and make restart create its replacement Case with the currently selected Bot and mapped environment.

**Architecture:** Resolve each candidate Bot's platform-mapped environment before matching, then carry that BotRef through Start and Reset. Extend the atomic Reset identity with the replacement Bot target and environment so the archived Case stays immutable while the replacement gets the new binding. Replace the middle standalone start card and lifecycle reset button with one Bot-panel action block; entering is read-only scroll/focus, while restart retains confirmation and the idempotent backend command.

**Tech Stack:** Go, modernc SQLite, Vue 3 Composition API, TypeScript, Vitest, Vue Test Utils, Wails bridge.

## Global Constraints

- The Bot panel is the single location for open, enter, and restart actions.
- An active Case shows both “进入故障闭环” and “重新开始故障闭环”.
- Restart uses the currently selected Bot and its platform-mapped environment; it never rewrites the archived Case binding.
- Restart always requires the existing accessible confirmation dialog.
- Start and restart remain disabled without a supported Bot and non-empty resolved environment; entering an existing Case remains available.
- Only codex, claude-code, and openclaw can be started by Studio.
- Existing version, idempotency, transaction, cancellation, late-callback, redaction, and recovery guards remain in force.
- No real remote, deployment, browser, or Agent process may run in tests.
- Responsive contracts cover 375, 768, 1024, and 1440px, with 44px minimum action height.

---

## File Structure

- Modify cmd/tshoot-desktop/bindings_bug.go and its test to resolve mapped Bot environments.
- Modify internal/bughub/workflow_store.go and workflow_reset_test.go to persist replacement identity atomically.
- Modify internal/bughub/workflow_orchestrator.go plus desktop workflow binding/tests to switch Bot safely.
- Modify web/src/components/BugCaseLifecycle.vue and its test to remove the duplicate reset control and expose a focus target.
- Modify web/src/pages/IncidentWorkbenchPage.vue and its test to centralize actions, scroll/focus, and snapshot restart context.

---

### Task 1: Resolve the selected Bot's platform environment

**Files:**
- Modify: cmd/tshoot-desktop/bindings_bug.go
- Test: cmd/tshoot-desktop/bindings_bug_test.go

**Interfaces:**
- Consumes: Bug.PlatformID, Bug.BotEnv, Bug.Env, PlatformConfig.BotMappings, []BotRef.
- Produces: applyBugBotEnvironments(Bug, []BotRef, *PlatformConfig) []BotRef.
- Precedence: matching bot_mappings.env, then Bug.BotEnv, then Bug.Env.

- [ ] **Step 1: Write the failing per-Bot environment test**

Create two discovered Bots and save:

    platform, err := bugPlatformStore().Upsert(bughub.PlatformConfig{
        ID: "zentao-main", Name: "Zentao", Type: "zentao", Enabled: true,
        BotEnv: "legacy-test",
        BotMappings: []bughub.PlatformBotMapping{
            {BotKey: codexKey, Env: "test"},
            {BotKey: claudeKey, Env: "prod"},
        },
    })
    if err != nil { t.Fatal(err) }
    if err := bugStore().Upsert(bughub.Bug{
        ID: "zentao-1842", PlatformID: platform.ID, Title: "支付页 500", Env: "stage",
    }); err != nil { t.Fatal(err) }

    matches, err := (&App{}).MatchBugBots("zentao-1842")
    if err != nil { t.Fatal(err) }
    got := map[string]string{}
    for _, match := range matches { got[match.Bot.Key] = match.Bot.Env }
    if got[codexKey] != "test" || got[claudeKey] != "prod" {
        t.Fatalf("mapped environments = %+v", got)
    }

Add table cases proving an empty/missing mapping falls back first to Bug.BotEnv, then Bug.Env, and that a missing platform record does not fail matching.

- [ ] **Step 2: Run RED**

    go test ./cmd/tshoot-desktop -run 'TestMatchBugBots.*Env' -count=1

Expected: FAIL because returned BotRef.Env is empty or global rather than per-Bot.

- [ ] **Step 3: Implement the overlay before scoring**

Add near MatchBugBots:

    func applyBugBotEnvironments(bug bughub.Bug, bots []bughub.BotRef, platform *bughub.PlatformConfig) []bughub.BotRef {
        mapped := map[string]string{}
        if platform != nil {
            for _, item := range platform.BotMappings {
                mapped[strings.TrimSpace(item.BotKey)] = strings.TrimSpace(item.Env)
            }
        }
        fallback := strings.TrimSpace(bug.BotEnv)
        if fallback == "" { fallback = strings.TrimSpace(bug.Env) }
        out := make([]bughub.BotRef, len(bots))
        copy(out, bots)
        for i := range out {
            env, found := mapped[out[i].Key]
            if !found || env == "" { env = fallback }
            out[i].Env = env
        }
        return out
    }

MatchBugBots loads selected.PlatformID with bugPlatformStore().Get, treats not-found as no overlay, propagates actual read errors, applies the helper, then calls bughub.MatchBots.

- [ ] **Step 4: Run GREEN**

    go test ./cmd/tshoot-desktop -run 'TestMatchBugBots|TestSaveBugPlatform' -count=1

Expected: PASS.

- [ ] **Step 5: Commit**

    git add cmd/tshoot-desktop/bindings_bug.go cmd/tshoot-desktop/bindings_bug_test.go
    git commit -m "fix: resolve incident bot mapping environments"

---

### Task 2: Persist the replacement binding in atomic Reset

**Files:**
- Modify: internal/bughub/workflow_store.go
- Test: internal/bughub/workflow_reset_test.go

**Interfaces:**
- Extends CaseReset with ReplacementBotTarget and ReplacementEnvironment.
- ResetCaseWithReplacement preserves archived SelectedBotKey/Environment and writes requested values to the replacement.
- Both new fields enter the fingerprint and redacted audit payload.

- [ ] **Step 1: Write failing atomic switch tests**

Change resetCommand to provide:

    CaseReset{
        CaseID: incident.ID,
        NewCaseID: newCaseID,
        ExpectedVersion: incident.Version,
        IdempotencyKey: key,
        ActorID: "alice",
        SelectedBotKey: "replacement|claude-code",
        ReplacementBotTarget: "claude-code",
        ReplacementEnvironment: "prod",
        RequestJSON: json.RawMessage("{\"reason\":\"retry\"}"),
    }

Seed the original Case with original|codex and test. Assert:

    if result.Archived.SelectedBotKey != "original|codex" || result.Archived.Environment != "test" {
        t.Fatalf("archived binding changed: %+v", result.Archived)
    }
    if result.Replacement.SelectedBotKey != "replacement|claude-code" || result.Replacement.Environment != "prod" {
        t.Fatalf("replacement binding = %+v", result.Replacement)
    }

Add fingerprint conflict mutations for ReplacementBotTarget and ReplacementEnvironment. Add blank-field validation cases that prove no Case, attempt, event, or cancellation operation changed. Extend a reopen test to reload both bindings and reset relationships.

- [ ] **Step 2: Run RED**

    go test ./internal/bughub -run 'TestResetCaseWithReplacement' -count=1

Expected: compile failure for missing fields, then behavioral failure because replacement copies the old environment.

- [ ] **Step 3: Extend CaseReset and the transaction**

Use:

    type CaseReset struct {
        CaseID                  string
        NewCaseID               string
        IdempotencyKey          string
        ActorID                 string
        ExpectedVersion         int64
        SelectedBotKey          string
        ReplacementBotTarget    string
        ReplacementEnvironment string
        RequestJSON             json.RawMessage
    }

Require all replacement identity fields. Create the replacement with reset.SelectedBotKey and reset.ReplacementEnvironment. Include target/environment in caseResetFingerprint and caseResetEventPayload. Do not modify archived binding fields.

- [ ] **Step 4: Run GREEN plus redaction/reopen coverage**

    go test ./internal/bughub -run 'TestResetCaseWithReplacement|Test.*Reset.*Redact|Test.*Reset.*Reopen' -count=1

Expected: PASS.

- [ ] **Step 5: Commit**

    git add internal/bughub/workflow_store.go internal/bughub/workflow_reset_test.go
    git commit -m "feat: persist replacement incident bot binding"

---

### Task 3: Reset with the selected Bot and schedule its environment

**Files:**
- Modify: internal/bughub/workflow_orchestrator.go
- Modify: cmd/tshoot-desktop/bindings_bug_workflow.go
- Test: internal/bughub/workflow_reset_test.go
- Test: cmd/tshoot-desktop/bindings_bug_workflow_test.go

**Interfaces:**
- ResetCaseCommand.Bot becomes the replacement Bot.
- The old attempt remains authoritative only for cancellation.
- Adds resolveIncidentEnvironment(Bug, BotRef) string with Bot.Env → Bug.BotEnv → Bug.Env precedence.

- [ ] **Step 1: Write failing orchestrator and binding tests**

Add a test with an old running original|codex/test attempt and replacement command Bot replacement|claude-code/prod. Assert the archived Case remains original/test, replacement is replacement/prod, cancellation count is one, start count is one, and the started Bot is claude-code/prod.

Add rejection tests for cursor and blank resolved environment; compare before/after store snapshots to prove no partial transaction. Change TestResetIncidentCaseForwardsContextAndEmitsReplacement so the desktop loader returns base|claude-code/prod and the emitted snapshot contains that binding.

- [ ] **Step 2: Run RED**

    go test ./internal/bughub ./cmd/tshoot-desktop -run 'Test.*Reset.*(SwitchesBot|ForwardsContext|Rejects)' -count=1

Expected: FAIL with the old Bot-equality guards.

- [ ] **Step 3: Implement replacement validation**

Add:

    func resolveIncidentEnvironment(bug Bug, bot BotRef) string {
        if env := strings.TrimSpace(bot.Env); env != "" { return env }
        if env := strings.TrimSpace(bug.BotEnv); env != "" { return env }
        return strings.TrimSpace(bug.Env)
    }

Use it in CreateAndStartCase and ResetCaseWithOutcome. In Reset:
- keep Bug ID/source/system checks;
- remove equality checks between cmd.Bot and the old Case/attempt;
- reject unsupported target and empty environment before the transaction;
- pass target/environment into CaseReset;
- cancel incident.CurrentAttemptID as before;
- start the replacement with cmd.Bot.

In resetIncidentCaseWithWarnings, remove the old-key equality guard and call loadBugAndBot(original.BugID, input.BotKey). Do not change loadIncidentContext; continuing an existing Case must use its persisted binding.

- [ ] **Step 4: Run recovery and idempotency tests**

    go test ./internal/bughub -run 'Test.*Reset|TestRecoverInterrupted' -count=1
    go test ./cmd/tshoot-desktop -run 'TestResetIncidentCase' -count=1

Expected: PASS, with duplicate Reset scheduling and cancellation still occurring once.

- [ ] **Step 5: Commit**

    git add internal/bughub/workflow_orchestrator.go internal/bughub/workflow_reset_test.go cmd/tshoot-desktop/bindings_bug_workflow.go cmd/tshoot-desktop/bindings_bug_workflow_test.go
    git commit -m "feat: restart incidents with selected bot"

---

### Task 4: Centralize actions below the Bot picker

**Files:**
- Modify: web/src/components/BugCaseLifecycle.vue
- Modify: web/src/components/BugCaseLifecycle.test.ts
- Modify: web/src/pages/IncidentWorkbenchPage.vue
- Modify: web/src/pages/IncidentWorkbenchPage.test.ts

**Interfaces:**
- Produces enterIncidentCase(), restartIncidentCase(), .bot-action-panel, and data actions start-case, enter-case, restart-case.
- ResetDialogSnapshot carries old and new binding snapshots.

- [ ] **Step 1: Write failing state-table tests**

For no Case, assert .bot-action-panel contains only “开启故障闭环” and .start-card is absent.

For waiting_evidence and fixed_verified, assert .bot-action-panel contains both “进入故障闭环” and “重新开始故障闭环”.

Add a lifecycle component test asserting .reset-action is absent while its current-stage .primary-action remains.

- [ ] **Step 2: Write failing scroll/focus tests**

Stub HTMLElement.prototype.scrollIntoView and window.matchMedia. Click enter-case and assert:

    expect(scrollIntoView).toHaveBeenCalledWith({ behavior: 'smooth', block: 'start' })
    expect(document.activeElement).toBe(wrapper.get('.primary-action').element)

Repeat with reduced motion and a terminal Case: behavior is auto and .case-heading with tabindex -1 receives focus.

- [ ] **Step 3: Write failing restart snapshot tests**

Use an active Case bound to base|codex/test. Select base-prod|claude-code/prod, click top restart, and assert the dialog shows both bindings. Confirm and assert:

    expect(resetIncidentCaseWithWarnings).toHaveBeenCalledWith(expect.objectContaining({
      case_id: 'case-1',
      bot_key: 'base-prod|claude-code',
    }))

Change the radio after opening the dialog and prove submission keeps the snapshotted Bot. For a terminal Case, prove restart calls startIncidentCase with a fresh ID/current Bot and never calls Reset.

- [ ] **Step 4: Run RED**

    cd web
    npm test -- --run src/pages/IncidentWorkbenchPage.test.ts src/components/BugCaseLifecycle.test.ts

Expected: FAIL because the top action block and enter behavior do not exist.

- [ ] **Step 5: Implement the unified action block**

In IncidentWorkbenchPage.vue:
- remove showStandaloneStart, standaloneStartLabel, the middle .start-card, and its CSS;
- render “开启故障闭环” only when no Case exists;
- render “进入故障闭环” and “重新开始故障闭环” whenever displayedCase exists;
- disable Start/Restart for pending, no selected Bot, unsupported target, or empty selectedBot.env;
- keep Enter enabled for a displayed Case;
- active restart opens Reset with selected Bot snapshot;
- terminal restart calls startNewCase with selected Bot;
- successful Start/Reset calls enterIncidentCase.

Use a native wrapper ref around lifecycle/loading state:

    async function enterIncidentCase() {
      const target = preferredCase.value
      if (!target) return
      if (incidentWorkflow.selectedCaseID.value !== target.id) await selectWorkflowCase(target.id)
      await nextTick()
      const region = lifecycleRegion.value
      if (!region) return
      const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
      region.scrollIntoView({ behavior: reduced ? 'auto' : 'smooth', block: 'start' })
      ;(region.querySelector<HTMLElement>('.primary-action')
        || region.querySelector<HTMLElement>('.case-heading'))?.focus()
    }

In BugCaseLifecycle.vue, give .case-heading tabindex="-1", remove its reset emit/control, and preserve phase actions.

ResetDialogSnapshot gets oldBotKey, oldEnvironment, newBotKey, newBotTarget, and newEnvironment. resetRequests identity includes Case/version/new Bot/environment. Confirm submits newBotKey.

- [ ] **Step 6: Add accessible responsive styles**

    .bot-action-panel {
      margin-top: var(--sp-3);
      padding-top: var(--sp-3);
      display: grid;
      gap: var(--sp-2);
      border-top: 1px solid var(--c-line);
    }
    .bot-action-controls { display: flex; flex-wrap: wrap; gap: var(--sp-2); }
    .bot-action-controls .btn { flex: 1 1 160px; min-height: 44px; }
    .danger-secondary { border-color: #fca5a5; background: #fff; color: #b91c1c; }
    @media (max-width: 700px) {
      .bot-action-controls { flex-direction: column; }
      .bot-action-controls .btn { width: 100%; flex-basis: auto; }
    }

Keep visible focus, nearby disabled reasons, pending labels, and primary-before-danger DOM order.

- [ ] **Step 7: Run GREEN and type-check**

    cd web
    npm test -- --run src/pages/IncidentWorkbenchPage.test.ts src/components/BugCaseLifecycle.test.ts
    npm run type-check

Expected: focused tests and Vue TypeScript pass.

- [ ] **Step 8: Commit**

    git add web/src/pages/IncidentWorkbenchPage.vue web/src/pages/IncidentWorkbenchPage.test.ts web/src/components/BugCaseLifecycle.vue web/src/components/BugCaseLifecycle.test.ts
    git commit -m "feat: centralize incident actions under bot selection"

---

### Task 5: Full verification and requirement audit

**Files:**
- Verify only; modify earlier in-scope files only if a check exposes an in-scope defect.

- [ ] **Step 1: Run all Web tests**

    cd web && npm test -- --run

Expected: all Vitest files pass with zero unhandled errors.

- [ ] **Step 2: Run frontend type-check and production build**

    cd web && npm run type-check && npm run build

Expected: both commands exit 0.

- [ ] **Step 3: Run focused Go race tests**

    go test ./internal/bughub ./cmd/tshoot-desktop -race -count=1

Expected: both packages pass with no race report.

- [ ] **Step 4: Run repository test floor**

    go test ./...

Expected: all Go packages pass.

- [ ] **Step 5: Audit requirements**

Confirm with code/test evidence:

    [ ] No Case: only 开启故障闭环 in Bot panel.
    [ ] Active Case: 进入故障闭环 + 重新开始故障闭环.
    [ ] Terminal history: 进入故障闭环 + 重新开始故障闭环.
    [ ] Enter is read-only and scrolls/focuses correct Case.
    [ ] Restart snapshots selected new Bot/environment.
    [ ] Archived Case retains old Bot/environment.
    [ ] Replacement Case and Agent use new Bot/environment.
    [ ] Duplicate Reset does not duplicate side effects.
    [ ] UI environment equals persisted Case environment.
    [ ] Four viewport contracts and 44px actions are covered.

- [ ] **Step 6: Inspect final diff and ownership**

    git diff --check
    git status --short
    git log --oneline -6

Expected: no whitespace errors; preserve the pre-existing internal/webui/dist/.gitkeep deletion without staging or modifying it.

