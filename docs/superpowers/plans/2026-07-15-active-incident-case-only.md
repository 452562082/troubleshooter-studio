# Active Incident Case Only Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show only the current non-terminal incident Case, remove all archive navigation from the workbench, and hide the lifecycle region when no active Case exists while preserving archived data and backend behavior.

**Architecture:** Keep the controller's complete Case collection unchanged, but make `IncidentWorkbenchPage` derive every visible lifecycle and action decision from `activeCaseForBug`. Narrow `BugCaseLifecycle` to one full-width Case and remove reset-relation navigation from `BugCaseArtifacts`; terminal records remain persisted but have no visible entry point.

**Tech Stack:** Vue 3 Composition API, TypeScript, Vue Test Utils, Vitest, scoped CSS

## Global Constraints

- Only non-terminal Cases are visible; `fixed_verified`, `legacy_archived`, and `reset_archived` must not render anywhere in the workbench lifecycle UI.
- With an active Case, show `进入故障闭环` and `重新开始故障闭环`; without one, show only `开启故障闭环`.
- Hide the entire lifecycle region when no active Case exists; do not render a lifecycle empty-state card.
- Remove the left `故障 Cases` list and render the current Case lifecycle and artifacts as a single full-width column.
- Preserve manual refresh by moving the 44×44px refresh button into the current Case heading.
- Remove the `重置关系` card, archive links, and `select-case` event.
- Keep SQLite rows, Attempts, evidence, approvals, code changes, deployment observations, events, recovery, metrics, reminders, state transitions, idempotency, and conflict handling unchanged.
- Do not add retention, scheduled deletion, manual cleanup, archive counts, history search, or hidden history navigation.
- Preserve existing timeline collapse, long-token wrapping, stage-output scrolling, keyboard focus, and dialog behavior.
- Keep the layout free of horizontal overflow at 375px, 768px, 1024px, and 1440px.
- Preserve the user-owned deletion of `internal/webui/dist/.gitkeep`; never stage or restore it.

---

## File Map

- Modify `web/src/components/BugCaseArtifacts.vue`: remove reset-relation rendering, archive links, and the archive-selection event.
- Modify `web/src/components/BugCaseArtifacts.test.ts`: replace navigation coverage with a negative archive-visibility regression.
- Modify `web/src/components/BugCaseLifecycle.vue`: remove multi-Case navigation, move refresh into the current heading, and use one full-width column.
- Modify `web/src/components/BugCaseLifecycle.test.ts`: update the component contract and every mount call for the single-Case API.
- Modify `web/src/pages/IncidentWorkbenchPage.vue`: derive visible state and all top-level actions exclusively from the active Case.
- Modify `web/src/pages/IncidentWorkbenchPage.test.ts`: remove obsolete terminal/legacy navigation tests and add active-only, terminal-only, realtime-terminal, and direct-new-round regressions.
- Do not modify `web/src/lib/useIncidentCase.ts` or backend files; `activeCaseForBug` and the complete controller collection remain the data boundary.

### Task 1: Remove reset archive relations from Case artifacts

**Files:**
- Modify: `web/src/components/BugCaseArtifacts.vue`
- Test: `web/src/components/BugCaseArtifacts.test.ts`

**Interfaces:**
- Consumes: `IncidentCaseDetail`, including optional `reset_from_case_id` and `superseded_by_case_id` fields that remain persisted.
- Produces: `BugCaseArtifacts` with only the `detail` prop and no emitted events.

- [ ] **Step 1: Replace archive-navigation coverage with a failing negative regression**

Replace the existing test named `renders both reset relations as read-only navigable Case references` in `web/src/components/BugCaseArtifacts.test.ts` with:

```ts
it('does not expose reset archives even when persisted relations are present', () => {
  const resetDetail = {
    ...detail,
    case: {
      ...detail.case,
      reset_from_case_id: 'case-before-reset',
      superseded_by_case_id: 'case-after-reset',
    },
  }
  const wrapper = mount(BugCaseArtifacts, { props: { detail: resetDetail } })

  expect(wrapper.find('[aria-labelledby="reset-relations-title"]').exists()).toBe(false)
  expect(wrapper.find('[data-case-reference]').exists()).toBe(false)
  expect(wrapper.text()).not.toContain('重置关系')
  expect(wrapper.text()).not.toContain('case-before-reset')
  expect(wrapper.text()).not.toContain('case-after-reset')
  expect(wrapper.emitted('select-case')).toBeUndefined()
})
```

- [ ] **Step 2: Run the focused artifact test and verify RED**

Run:

```bash
cd web && npx vitest run src/components/BugCaseArtifacts.test.ts
```

Expected: FAIL because the reset-relations section and archive links still render.

- [ ] **Step 3: Remove the archive relation interface, template, and styles**

In `web/src/components/BugCaseArtifacts.vue`, keep the prop and remove the emit declaration:

```ts
const props = defineProps<{ detail: IncidentCaseDetail }>()
```

Delete the complete template section whose heading is `重置关系`, including both `data-case-reference` anchors. Delete these scoped CSS rules:

```css
.reset-relations dl
.reset-relations dl > div
.reset-relations dt
.reset-relations dd
.reset-relations a
.reset-relations a:focus-visible
.reset-relations p
```

The first child of `.artifact-sections` must now be the existing `验证证据` card. Do not alter evidence, root-cause, output, code-change, approval, or deployment cards.

- [ ] **Step 4: Run the focused artifact test and verify GREEN**

Run:

```bash
cd web && npx vitest run src/components/BugCaseArtifacts.test.ts
```

Expected: PASS with the new negative regression and all existing artifact/output tests.

- [ ] **Step 5: Commit Task 1**

```bash
git add web/src/components/BugCaseArtifacts.vue web/src/components/BugCaseArtifacts.test.ts
git diff --cached --check
git commit -m "feat: hide incident archive relations"
```

### Task 2: Convert the lifecycle component to one full-width Case

**Files:**
- Modify: `web/src/components/BugCaseLifecycle.vue`
- Test: `web/src/components/BugCaseLifecycle.test.ts`

**Interfaces:**
- Consumes: `detail: IncidentCaseDetail | null`, `pending?: boolean`, and `error?: string`.
- Produces: `refresh: []` and the existing `primary` payload event; removes `cases: IncidentCase[]` and `select: [caseID: string]`.
- Consumes from Task 1: `BugCaseArtifacts` accepts only `:detail` and emits no archive selection.

- [ ] **Step 1: Write the failing single-Case layout and refresh test**

Replace the existing test named `renders Cases and the current Case above a full-width detail region` with:

```ts
it('renders one full-width current Case and refreshes from its heading', async () => {
  const snapshot = detail('waiting_fix_approval')
  const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })
  const columns = wrapper.findAll('.case-column')
  const source = readFileSync('src/components/BugCaseLifecycle.vue', 'utf8')

  expect(columns).toHaveLength(2)
  expect(columns[0].classes()).toContain('case-main-column')
  expect(columns[1].classes()).toContain('case-detail-column')
  expect(wrapper.find('.case-list-column').exists()).toBe(false)
  expect(wrapper.text()).not.toContain('故障 Cases')
  expect('cases' in wrapper.props()).toBe(false)
  expect(wrapper.find('.case-heading-copy').exists()).toBe(true)
  expect(wrapper.find('.case-heading-actions').exists()).toBe(true)
  expect(source).toMatch(/\.case-lifecycle \{[^}]*grid-template-columns: minmax\(0, 1fr\);/)
  expect(source).not.toContain('.case-row')
  expect(wrapper.findAll('.lifecycle-stage')).toHaveLength(6)
  expect(wrapper.find('[aria-label="故障处理阶段"]').exists()).toBe(true)
  expect(wrapper.find('[aria-label="Case 时间线"]').text()).toContain('transition')
  expect(wrapper.findAll('.current-action-card .primary-action')).toHaveLength(1)

  const refresh = wrapper.get<HTMLButtonElement>('[aria-label="刷新当前 Case"]')
  expect(refresh.classes()).toContain('icon-button')
  await refresh.trigger('click')
  expect(wrapper.emitted('refresh')).toEqual([[]])
})
```

Update the responsive contract test to assert the permanent single-column layout rather than the removed 899px Case-list collapse:

```ts
it('contains responsive no-overflow contracts for all supported viewport fixtures', () => {
  const wrapper = mount(BugCaseLifecycle, { props: { detail: detail('validating') } })
  expect(wrapper.find('.case-lifecycle').attributes('data-responsive-viewports')).toBe('375,768,1024,1440')
  expect(wrapper.find('.case-lifecycle').attributes('data-overflow-safe')).toBe('true')
  const source = readFileSync('src/components/BugCaseLifecycle.vue', 'utf8')
  expect(source).toMatch(/\.case-lifecycle \{[^}]*min-width: 0;[^}]*grid-template-columns: minmax\(0, 1fr\);/)
  expect(source).toMatch(/\.case-column \{[^}]*min-width: 0;/)
  expect(source).not.toContain('.case-list-column')
  expect(source).not.toContain('.case-row')
  expect(source).toContain('@media (max-width: 560px)')
})
```

Change the heading focus test to use an active Case:

```ts
it('makes the current Case heading a programmatic focus target', () => {
  const wrapper = mount(BugCaseLifecycle, { props: { detail: detail('validating') } })
  expect(wrapper.get('.case-heading').attributes('tabindex')).toBe('-1')
})
```

Delete the direct component tests named `shows archived Cases as read-only and continues through a new Case action` and `shows reset archives as terminal read-only history`; the page will no longer pass terminal details to this component.

- [ ] **Step 2: Remove the obsolete `cases` prop from every lifecycle test mount and verify RED**

In `web/src/components/BugCaseLifecycle.test.ts`, transform every mount from the old form:

```ts
mount(BugCaseLifecycle, { props: { cases: [snapshot.case], detail: snapshot } })
```

to the single-Case form:

```ts
mount(BugCaseLifecycle, { props: { detail: snapshot } })
```

Apply the same removal to fixtures using `incident(status)`, `caseA`, `caseB`, and `attachTo`. When the Case-switch timeline test calls `setProps`, change:

```ts
await wrapper.setProps({ cases: [caseB.case], detail: caseB })
```

to:

```ts
await wrapper.setProps({ detail: caseB })
```

Verify the old API is absent from the test file:

```bash
rg -n "cases:" web/src/components/BugCaseLifecycle.test.ts
```

Expected: no matches.

Run:

```bash
cd web && npx vitest run src/components/BugCaseLifecycle.test.ts
```

Expected: FAIL because production still requires `cases`, still renders three columns, and does not have `刷新当前 Case` in the heading.

- [ ] **Step 3: Remove multi-Case props/events and move refresh into the heading**

In `web/src/components/BugCaseLifecycle.vue`, change the setup contract to:

```ts
const props = defineProps<{
  detail: IncidentCaseDetail | null
  pending?: boolean
  error?: string
}>()
const emit = defineEmits<{
  refresh: []
  primary: [payload: { kind: CasePrimaryAction['kind']; input?: string; observedVersion?: string; observedCommits?: Record<string, string>; versionSource?: string; rootCauseAttemptID?: string; caseVersion?: number }]
}>()
```

Delete the complete `<aside class="case-column case-list-column">` block. Replace the Case heading with:

```vue
<header class="case-heading" tabindex="-1">
  <div class="case-heading-copy"><span>Case {{ detail.case.id }}</span><h2>{{ detail.case.bug_id }}</h2></div>
  <div class="case-heading-actions">
    <span class="status-pill" :data-status="detail.case.status">{{ statusLabel(detail.case.status) }}</span>
    <button class="icon-button" type="button" aria-label="刷新当前 Case" :disabled="pending" @click="emit('refresh')">
      <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 11a8 8 0 1 0-2.34 5.66M20 5v6h-6" /></svg>
    </button>
  </div>
</header>
```

Change the artifacts region to remove archive selection forwarding:

```vue
<aside class="case-column case-detail-column" aria-label="Case 证据与详情">
  <BugCaseArtifacts v-if="detail" :detail="detail" />
  <p v-else class="empty-state">证据与变更将在这里显示</p>
</aside>
```

- [ ] **Step 4: Replace multi-column/list CSS with the permanent single-column contract**

Set the root and heading rules to:

```css
.case-lifecycle { width: 100%; min-width: 0; display: grid; grid-template-columns: minmax(0, 1fr); align-items: start; gap: var(--sp-3); color: var(--c-text); }
.case-column { min-width: 0; border: 1px solid var(--c-line); border-radius: var(--r-lg); background: var(--c-surf-2); padding: var(--sp-3); overflow-wrap: anywhere; }
.case-heading, .current-action-card { display: flex; align-items: center; justify-content: space-between; gap: var(--sp-2); }
.case-heading-copy { min-width: 0; }
.case-heading-copy > span { display: block; margin-bottom: 2px; }
.case-heading-actions { min-width: 0; display: flex; align-items: center; justify-content: flex-end; gap: var(--sp-2); }
h2, h3, p { margin: 0; }
.case-heading h2 { color: var(--c-ink); font-size: var(--fs-lg); }
.case-heading span, .current-action-card span { color: var(--c-muted); font-size: var(--fs-sm); }
```

Keep `.icon-button` and `.icon-button svg`. Delete `.case-list-column`, `.column-head`, `.case-row`, `.status-text`, and their descendant/hover/active/pseudo-element rules. Narrow status-dot selectors to `.status-pill`:

```css
.status-pill { display: inline-flex; align-items: center; gap: 5px; color: var(--c-muted); font-size: var(--fs-xs); }
.status-pill::before { content: ''; width: 7px; height: 7px; border-radius: 50%; background: #94a3b8; }
```

Remove the former broad `.case-heading > div` and `.case-heading > div > span` rules so the status/actions container cannot inherit copy-column spacing. Remove `.case-detail-column { grid-column: 1 / -1; }`. In the `@media (max-width: 899px)` block, remove only the obsolete `.case-lifecycle`, `.case-detail-column`, and `.case-list-column` declarations; keep current-action responsive behavior. Preserve the 560px stage/dialog rules and reduced-motion rule.

- [ ] **Step 5: Run the focused lifecycle test and verify GREEN**

Run:

```bash
cd web && npx vitest run src/components/BugCaseLifecycle.test.ts
```

Expected: PASS with single-column layout, heading refresh, existing dialogs/actions, timeline collapse, long-token wrapping, and responsive contracts.

- [ ] **Step 6: Commit Task 2**

```bash
git add web/src/components/BugCaseLifecycle.vue web/src/components/BugCaseLifecycle.test.ts
git diff --cached --check
git commit -m "refactor: simplify incident lifecycle layout"
```

### Task 3: Make the workbench display and operate on only the active Case

**Files:**
- Modify: `web/src/pages/IncidentWorkbenchPage.vue`
- Test: `web/src/pages/IncidentWorkbenchPage.test.ts`

**Interfaces:**
- Consumes: `activeCaseForBug(cases: IncidentCase[], bugID: string): IncidentCase | undefined` from the unchanged controller module.
- Consumes from Task 2: `BugCaseLifecycle` accepts `detail`, `pending`, and `error`, emits `refresh` and `primary`, and has no `cases` or `select` interface.
- Produces: `displayedCase` and `displayedDetail` that can only reference a non-terminal Case for the selected Bug.

- [ ] **Step 1: Add active-only and terminal-only page regressions**

Keep the existing test `opens the newest active Case without calling Start`, then add explicit archive non-visibility assertions:

```ts
expect(wrapper.find('.case-list-column').exists()).toBe(false)
expect(wrapper.text()).not.toContain('case-terminal')
expect(wrapper.text()).not.toContain('case-active-old')
```

Replace the parameterized test that currently treats both `waiting_evidence` and `fixed_verified` as enterable with these two tests:

```ts
it('shows enter and restart only for an active Case', async () => {
  route.query = { bug_id: 'bug-a' }
  vi.mocked(listBugs).mockResolvedValue([bugA])
  const item = incident('case-waiting_evidence', 'waiting_evidence', '2026-07-13T00:00:00Z')
  vi.mocked(listIncidentCases).mockResolvedValue([item])
  mockCaseDetails(detail(item))

  const wrapper = await mountedPage()
  const panel = wrapper.get('.bot-action-panel')

  expect(panel.get('.bot-action-status').text()).toContain('已有进行中的 Case')
  expect(panel.get('[data-action="enter-case"]').text()).toContain('进入故障闭环')
  expect(panel.get('[data-action="restart-case"]').text()).toContain('重新开始故障闭环')
  expect(panel.find('[data-action="start-case"]').exists()).toBe(false)
  expect(wrapper.find('.lifecycle-region').exists()).toBe(true)
})

it.each(['fixed_verified', 'legacy_archived', 'reset_archived'] as const)('hides a %s Case and offers only a fresh start', async status => {
  route.query = { bug_id: 'bug-a' }
  vi.mocked(listBugs).mockResolvedValue([bugA])
  const terminal = incident(`case-${status}`, status, '2026-07-13T00:00:00Z')
  vi.mocked(listIncidentCases).mockResolvedValue([terminal])
  mockCaseDetails(detail(terminal))

  const wrapper = await mountedPage()
  const panel = wrapper.get('.bot-action-panel')

  expect(panel.get('.bot-action-status').text()).toBe('尚无进行中的 Case')
  expect(panel.get('[data-action="start-case"]').text()).toContain('开启故障闭环')
  expect(panel.find('[data-action="enter-case"]').exists()).toBe(false)
  expect(panel.find('[data-action="restart-case"]').exists()).toBe(false)
  expect(wrapper.find('.lifecycle-region').exists()).toBe(false)
  expect(wrapper.find('.case-heading').exists()).toBe(false)
  expect(wrapper.text()).not.toContain(terminal.id)
})
```

- [ ] **Step 2: Add realtime terminal and direct fresh-round regressions**

Add:

```ts
it('hides the lifecycle immediately when the active Case becomes terminal', async () => {
  route.query = { bug_id: 'bug-a' }
  vi.mocked(listBugs).mockResolvedValue([bugA])
  const active = incident('case-live-terminal', 'waiting_evidence', '2026-07-13T00:00:00Z')
  vi.mocked(listIncidentCases).mockResolvedValue([active])
  mockCaseDetails(detail(active))
  const wrapper = await mountedPage()

  expect(wrapper.find('.lifecycle-region').exists()).toBe(true)
  const terminal = { ...active, status: 'fixed_verified' as const, version: active.version + 1, updated_at: '2026-07-13T00:01:00Z' }
  const eventHandler = runtime.EventsOn.mock.calls.find(call => call[0] === 'incident-case:event')?.[1]
  expect(eventHandler).toBeTypeOf('function')
  eventHandler?.({ kind: 'snapshot', case: terminal, snapshot: detail(terminal) })
  await flushPromises()

  expect(wrapper.find('.lifecycle-region').exists()).toBe(false)
  expect(wrapper.get('.bot-action-status').text()).toBe('尚无进行中的 Case')
  expect(wrapper.find('[data-action="start-case"]').exists()).toBe(true)
  expect(wrapper.text()).not.toContain(terminal.id)
})

it('starts directly when only terminal records exist', async () => {
  route.query = { bug_id: 'bug-a' }
  vi.mocked(listBugs).mockResolvedValue([bugA])
  const terminal = incident('case-old-terminal', 'fixed_verified', '2026-07-13T00:00:00Z')
  const opened = incident('case-new-active', 'validating', '2026-07-13T00:01:00Z', { version: 1 })
  vi.mocked(listIncidentCases).mockResolvedValue([terminal])
  vi.mocked(startIncidentCase).mockResolvedValue(opened)
  mockCaseDetails(detail(terminal), detail(opened))
  const wrapper = await mountedPage()

  await wrapper.get('[data-action="start-case"]').trigger('click')
  await flushPromises()
  await flushPromises()

  expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({
    bug_id: 'bug-a', expected_version: 0, bot_key: 'base|codex',
  }))
  expect(resetIncidentCaseWithWarnings).not.toHaveBeenCalled()
  expect(wrapper.find('[data-reset-confirm]').exists()).toBe(false)
  expect(wrapper.get('.case-heading').text()).toContain(opened.id)
})

it('retries an initial active Case detail failure without revealing terminal history', async () => {
  route.query = { bug_id: 'bug-a' }
  vi.mocked(listBugs).mockResolvedValue([bugA])
  const active = incident('case-retry-active', 'waiting_evidence', '2026-07-13T00:00:00Z')
  const terminal = incident('case-hidden-terminal', 'fixed_verified', '2026-07-12T00:00:00Z')
  vi.mocked(listIncidentCases).mockResolvedValue([active, terminal])
  vi.mocked(getIncidentCase).mockRejectedValue(new Error('active detail unavailable'))
  const wrapper = await mountedPage()

  expect(wrapper.get('.case-loading').text()).toContain('active detail unavailable')
  expect(wrapper.text()).not.toContain(terminal.id)
  vi.mocked(getIncidentCase).mockResolvedValue(detail(active))
  await wrapper.get('[data-action="retry-active-case"]').trigger('click')
  await flushPromises()

  expect(wrapper.get('.case-heading').text()).toContain(active.id)
  expect(wrapper.find('[data-action="retry-active-case"]').exists()).toBe(false)
})

it('refreshes the displayed active Case from its heading', async () => {
  route.query = { bug_id: 'bug-a' }
  vi.mocked(listBugs).mockResolvedValue([bugA])
  const active = incident('case-refresh-active', 'waiting_evidence', '2026-07-13T00:00:00Z')
  const refreshed = { ...active, status: 'waiting_fix_approval' as const, version: active.version + 1, current_attempt_id: 'root-refresh' }
  vi.mocked(listIncidentCases).mockResolvedValue([active])
  mockCaseDetails(detail(active))
  const wrapper = await mountedPage()

  vi.mocked(getIncidentCase).mockClear()
  vi.mocked(getIncidentCase).mockResolvedValue(detail(refreshed))
  await wrapper.get('[aria-label="刷新当前 Case"]').trigger('click')
  await flushPromises()

  expect(getIncidentCase).toHaveBeenCalledWith(active.id)
  expect(wrapper.get('.current-action-card').text()).toContain('允许修复')
})
```

- [ ] **Step 3: Delete obsolete archive-navigation tests and verify RED**

Delete these tests from `web/src/pages/IncidentWorkbenchPage.test.ts` because their UI entry points are intentionally removed:

```text
uses immediate scrolling and focuses the heading for a terminal Case
confirms a fixed_verified, legacy_archived, or reset_archived terminal round using the snapshotted Bot key and environment
does not write a terminal new round after cancel or escape
uses the recovered legacy Bot object for both key and environment without an explicit selection
validates restart against the Bot visibly recovered for a legacy archive
keeps one canonical Bot when an active Case and displayed legacy history coexist
keeps historical detail visible and reports a recoverable error when the restart target detail cannot load
lets an explicit Bot override legacy recovery for both key and environment
confirms a legacy lifecycle continuation with the snapshotted Bot before starting a fresh Case round
does not write a legacy lifecycle continuation after cancel or escape
keeps a displayed legacy lifecycle continuation in terminal-new-round mode when an active Case also exists
recovers an empty migrated selected_bot_key from the latest legacy attempt
requires explicit bot reselection for an unbound archive before starting a new round
```

Do not remove active-Case reset, confirmation, stale-response, focus-trap, version-conflict, cancellation, or replacement-Case tests.

Run:

```bash
cd web && npx vitest run src/pages/IncidentWorkbenchPage.test.ts
```

Expected: FAIL because terminal Cases still become `preferredCase`, still render lifecycle content, and still expose enter/restart instead of start.

- [ ] **Step 4: Derive all visible workbench state from the active Case**

In `web/src/pages/IncidentWorkbenchPage.vue`, remove `botKeyForLegacyContinuation` and `casesForBug` from the `useIncidentCase` import. Replace the current Case-selection computed block with:

```ts
const selectedActiveCase = computed(() => activeCaseForBug(incidentWorkflow.cases.value, tickets.selectedID.value))
const displayedCase = computed(() => selectedActiveCase.value)
const displayedDetail = computed(() => incidentWorkflow.detail.value?.case.id === displayedCase.value?.id ? incidentWorkflow.detail.value : null)
const pickerSelectedBotKey = computed(() => selectedBotKey.value)
```

Delete `selectedBugCases`, `newestSelectedCase`, `preferredCase`, and `allCasesTerminal`. Change the status computed to:

```ts
const botActionStatus = computed(() => {
  const current = displayedCase.value
  return current ? `已有进行中的 Case · ${current.status}` : '尚无进行中的 Case'
})
```

Use `displayedCase.value` instead of `preferredCase.value` in `openPreferredCase`, `enterIncidentCase`, `restartIncidentCase`'s default target and current-target guard. Keep `targetOverride` support and terminal reset mode code unchanged because they are internal compatibility paths, but no visible archive can invoke them.

Make the moved heading refresh actually reload the current detail by changing `refreshIncidentWorkflow` to:

```ts
async function refreshIncidentWorkflow() {
  try {
    await incidentWorkflow.refreshCases()
    await openPreferredCase(true)
  } catch (error) {
    toastError('刷新故障 Case', error)
  }
}
```

In `startNewCase`, keep the existing round identity structure with `displayedCase.value`, and replace the two-way success notice with:

```ts
workflowNotice.value = '故障闭环已启动'
toast.success(workflowNotice.value)
```

- [ ] **Step 5: Update the lifecycle template to the single-Case interface**

Keep the existing active-only visibility guard:

```vue
<div v-if="displayedCase" ref="lifecycleRegion" class="lifecycle-region">
```

Update `BugCaseLifecycle` usage to:

```vue
<BugCaseLifecycle
  v-if="displayedDetail"
  :detail="displayedDetail"
  :pending="incidentWorkflow.pending.value || starting"
  :error="incidentWorkflow.error.value"
  @refresh="refreshIncidentWorkflow"
  @primary="handleIncidentPrimary"
/>
```

There must be no `:cases` or `@select` binding. Replace the loading paragraph beneath the component with a recoverable loading/error section:

```vue
<section v-else class="case-loading" aria-live="polite">
  <p role="status">{{ incidentWorkflow.error.value ? `加载 Case 失败：${incidentWorkflow.error.value}` : `正在加载 Case ${displayedCase.id}…` }}</p>
  <button v-if="incidentWorkflow.error.value" class="btn" type="button" data-action="retry-active-case" :disabled="incidentWorkflow.loading.value" @click="refreshIncidentWorkflow">
    {{ incidentWorkflow.loading.value ? '重试中…' : '重试加载' }}
  </button>
</section>
```

Add the touch-target rule beside the existing `.case-loading` CSS:

```css
.case-loading .btn { min-height: 44px; }
```

When `displayedCase` is undefined, neither this wrapper nor the loading/error section renders.

- [ ] **Step 6: Run focused page and component regression suites**

Run:

```bash
cd web
npx vitest run src/pages/IncidentWorkbenchPage.test.ts
npx vitest run src/components/BugCaseLifecycle.test.ts src/components/BugCaseArtifacts.test.ts
```

Expected: PASS. Terminal-only fixtures expose only start, active fixtures preserve enter/restart, realtime terminal snapshots hide the lifecycle, direct fresh starts work without a reset dialog, and archive UI is absent.

- [ ] **Step 7: Run full verification**

Run:

```bash
cd web
npm test
npx vue-tsc --noEmit
npm run build
```

Expected: all Vitest files pass, TypeScript reports no diagnostics, and Vite completes the production build. Existing expected `useAsyncStatus` stderr and Node localStorage experimental warnings may remain; no new task-specific warning is allowed.

Then run:

```bash
git diff --check
git status --short
```

Expected before the Task 3 commit: only `web/src/pages/IncidentWorkbenchPage.vue`, `web/src/pages/IncidentWorkbenchPage.test.ts`, and the unrelated unstaged deletion of `internal/webui/dist/.gitkeep` appear. Across all three Task commits, the implementation scope is limited to the six Web files in the File Map.

- [ ] **Step 8: Commit Task 3**

```bash
git add web/src/pages/IncidentWorkbenchPage.vue web/src/pages/IncidentWorkbenchPage.test.ts
git diff --cached --check
git commit -m "feat: show only active incident case"
```
