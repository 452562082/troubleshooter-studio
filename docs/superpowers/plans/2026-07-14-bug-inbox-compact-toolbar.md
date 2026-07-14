# Bug Inbox Compact Toolbar Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Eliminate the Bug inbox count/action overlap and place scheduled sync plus manual Bug fetch in one separated responsive control row.

**Architecture:** Keep synchronization behavior in `BugInboxPage.vue`, but give `BugTicketList.vue` a presentation-only `actions` slot so title, count, and action share one normal-flow toolbar. Group scheduled sync and manual fetch under one page-owned CSS grid with a decorative divider; no backend state or API changes are introduced.

**Tech Stack:** Vue 3 Composition API, TypeScript, Vue Test Utils, Vitest, scoped CSS.

## Global Constraints

- The inbox title, Bug count, and “同步我的 Bug” action must share normal document flow; do not use absolute positioning or fixed right padding to make room for the action.
- Preserve the existing `syncEnabledPlatforms`, button `data-action`, dynamic `aria-label`, loading text, spinning icon, and `syncingBugs || tickets.loading.value` disabled condition.
- Above 900px, the sync/access control row must use `auto 1px minmax(0, 1fr)` columns in the order scheduled sync, decorative divider, manual fetch.
- At 900px and below, the control row must stack in one column and the divider must become horizontal.
- At 640px and below, the manual fetch button must be full width and interactive controls must retain at least 44px touch targets.
- Hook URL must remain a separate row immediately after the combined control row.
- Preserve selected-platform scoping for manual fetch, background polling persistence, multi-platform sync behavior, and Hook URL generation.
- Preserve overflow safety at 375px, 768px, 1024px, and 1440px.
- Preserve `internal/webui/dist/.gitkeep`; do not commit generated `web/dist` or `internal/webui/dist` artifacts.

---

### Task 1: Put the inbox action in the list heading normal flow

**Files:**
- Modify: `web/src/components/BugTicketList.test.ts`
- Modify: `web/src/components/BugTicketList.vue`
- Modify: `web/src/pages/BugInboxPage.test.ts`
- Modify: `web/src/pages/BugInboxPage.vue`

**Interfaces:**
- Consumes: existing `BugTicketList` props and the page-owned `syncEnabledPlatforms(): Promise<void>` action.
- Produces: optional `#actions` slot rendered inside `.list-heading`; `.list-heading-summary` and `.list-actions` layout hooks.

- [ ] **Step 1: Write the failing list-slot test**

Add this test to `web/src/components/BugTicketList.test.ts`:

```ts
it('renders an optional action beside the title and count in normal flow', () => {
  const withAction = mount(BugTicketList, {
    props: { bugs, selectedId: '', query: '' },
    slots: { actions: '<button data-test="list-action">同步</button>' },
  })
  const heading = withAction.get('.list-heading')
  expect(heading.get('.list-heading-summary strong').text()).toBe('Bug 收件箱')
  expect(heading.get('.list-heading-summary span').text()).toBe('2 条')
  expect(heading.get('.list-actions [data-test="list-action"]').text()).toBe('同步')

  const withoutAction = mount(BugTicketList, {
    props: { bugs, selectedId: '', query: '' },
  })
  expect(withoutAction.find('.list-actions').exists()).toBe(false)
})
```

- [ ] **Step 2: Write the failing page placement contract**

Add a page test beside the existing responsive tests in `web/src/pages/BugInboxPage.test.ts`:

```ts
it('keeps the sync action in the Bug list heading without absolute overlap', async () => {
  vi.mocked(listBugs).mockResolvedValue([bug])
  const wrapper = await mountedInbox()
  const panel = wrapper.get('.ticket-list-panel')
  const action = panel.get('.list-heading .list-actions [data-action="sync-enabled-platforms"]')

  expect(action.text()).toContain('同步我的 Bug')
  expect(panel.find(':scope > [data-action="sync-enabled-platforms"]').exists()).toBe(false)

  const source = readFileSync('src/pages/BugInboxPage.vue', 'utf8')
  expect(source).not.toMatch(/\.refresh-button\s*\{[^}]*position:\s*absolute/)
  expect(source).not.toContain('.ticket-list-panel :deep(.list-heading) { padding-right: 112px; }')
})
```

- [ ] **Step 3: Run the focused tests and verify RED**

Run:

```bash
cd web && npm test -- --run src/components/BugTicketList.test.ts src/pages/BugInboxPage.test.ts -t 'optional action|without absolute overlap'
```

Expected: FAIL because `BugTicketList` has no `actions` slot and the page action is still an absolutely positioned panel child.

- [ ] **Step 4: Add the optional list action slot**

Replace the heading in `web/src/components/BugTicketList.vue` with:

```vue
<header class="list-heading">
  <div class="list-heading-summary">
    <strong :id="`${listInstanceID}-title`">Bug 收件箱</strong>
    <span>{{ bugs.length }} 条</span>
  </div>
  <div v-if="$slots.actions" class="list-actions">
    <slot name="actions" />
  </div>
</header>
```

Replace the existing heading CSS with:

```css
.list-heading { min-width: 0; display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: var(--sp-2); }
.list-heading-summary { min-width: 0; display: flex; align-items: baseline; gap: var(--sp-2); }
.list-actions { min-width: 0; margin-left: auto; display: flex; align-items: center; }
.list-heading strong { color: var(--c-ink); font-size: var(--fs-md); }
.list-heading-summary span, .search-field label { color: var(--c-muted); font-size: var(--fs-xs); }
```

- [ ] **Step 5: Inject the page action through the slot**

Remove the standalone synchronization button before `<BugTicketList>` and change the component invocation in `web/src/pages/BugInboxPage.vue` to:

```vue
<BugTicketList
  :bugs="tickets.filteredBugs.value"
  :selected-id="tickets.selectedID.value"
  :loading="tickets.loading.value"
  :query="tickets.query.value"
  @select="tickets.select"
  @update:query="tickets.query.value = $event"
>
  <template #actions>
    <button class="compact-button secondary-button refresh-button" type="button" data-action="sync-enabled-platforms" :aria-label="syncingBugs ? '正在同步我的 Bug' : '同步我的 Bug'" :disabled="syncingBugs || tickets.loading.value" @click="syncEnabledPlatforms">
      <svg aria-hidden="true" :class="{ spinning: syncingBugs }" viewBox="0 0 24 24" fill="none"><path d="M20 11a8 8 0 1 0-2.34 5.66M20 4v7h-7" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
      {{ syncingBugs ? '同步中…' : '同步我的 Bug' }}
    </button>
  </template>
</BugTicketList>
```

Delete these rules:

```css
.ticket-list-panel :deep(.list-heading) { padding-right: 112px; }
.refresh-button { position: absolute; z-index: 1; top: var(--sp-2); right: var(--sp-2); }
```

Keep the icon and animation rules:

```css
.refresh-button svg { width: 16px; height: 16px; }
.refresh-button svg.spinning { animation: refresh-spin 800ms linear infinite; }
```

- [ ] **Step 6: Run focused tests and verify GREEN**

Run:

```bash
cd web && npm test -- --run src/components/BugTicketList.test.ts src/pages/BugInboxPage.test.ts -t 'optional action|without absolute overlap'
```

Expected: both new tests PASS.

- [ ] **Step 7: Commit Task 1**

```bash
git add web/src/components/BugTicketList.vue web/src/components/BugTicketList.test.ts web/src/pages/BugInboxPage.vue web/src/pages/BugInboxPage.test.ts
git commit -m "fix: keep Bug sync action in list toolbar"
```

### Task 2: Combine scheduled sync and manual fetch with a responsive divider

**Files:**
- Modify: `web/src/pages/BugInboxPage.test.ts`
- Modify: `web/src/pages/BugInboxPage.vue`

**Interfaces:**
- Consumes: existing `.sync-settings`, `.manual-bug-row`, `.hook-row`, `platformDraft`, `manualBugID`, and `fetchManualBug` behavior.
- Produces: `.sync-control-row` and decorative `.sync-control-divider` with desktop vertical and responsive horizontal forms.

- [ ] **Step 1: Replace the old layout test with a failing grouped-row contract**

Update `keeps the sync/access layout ordered and responsive` in `web/src/pages/BugInboxPage.test.ts` to assert:

```ts
const syncAccess = wrapper.get('.sync-access-section')
const controlRow = syncAccess.get(':scope > .sync-control-row')
expect(Array.from(controlRow.element.children).map(element => element.classList[0])).toEqual([
  'sync-settings',
  'sync-control-divider',
  'manual-bug-row',
])
expect(controlRow.get('.sync-control-divider').attributes('aria-hidden')).toBe('true')
expect(controlRow.find('.manual-bug-row .manual-bug-field').exists()).toBe(true)
expect(controlRow.find('.manual-bug-row [data-action="fetch-bug"]').exists()).toBe(true)
expect(controlRow.element.nextElementSibling).toBe(syncAccess.get(':scope > .hook-row').element)

const source = readFileSync('src/pages/BugInboxPage.vue', 'utf8')
const tabletCSS = source.split('@media (max-width: 900px) {')[1]?.split('\n}')[0] || ''
const mobileCSS = source.split('@media (max-width: 640px) {')[1]?.split('\n}')[0] || ''
expect(source).toContain('.sync-control-row { min-width: 0; display: grid; grid-template-columns: auto 1px minmax(0, 1fr);')
expect(source).toContain('.sync-control-divider { align-self: stretch; width: 1px; background: var(--c-line); }')
expect(tabletCSS).toContain('.sync-control-row { grid-template-columns: minmax(0, 1fr); }')
expect(tabletCSS).toContain('.sync-control-divider { width: 100%; height: 1px; }')
expect(mobileCSS).toContain('.manual-bug-row .compact-button { width: 100%; }')
```

- [ ] **Step 2: Run the layout test and verify RED**

Run:

```bash
cd web && npm test -- --run src/pages/BugInboxPage.test.ts -t 'sync/access layout'
```

Expected: FAIL because `.sync-control-row` and `.sync-control-divider` do not exist.

- [ ] **Step 3: Group the controls without changing behavior**

Replace the two separate control divs in `web/src/pages/BugInboxPage.vue` with:

```vue
<div class="sync-control-row">
  <div class="sync-settings">
    <label class="toggle-control"><input v-model="platformDraft.poll_enabled" type="checkbox"><span class="toggle-track" aria-hidden="true"><span></span></span><span>后台定时同步</span></label>
    <label class="interval-control">每 <input v-model.number="platformDraft.poll_interval_minutes" aria-label="后台同步间隔分钟" type="number" min="1" :disabled="!platformDraft.poll_enabled"> 分钟</label>
  </div>
  <span class="sync-control-divider" aria-hidden="true"></span>
  <div class="manual-bug-row">
    <label class="field-label manual-bug-field" :for="manualBugFieldID"><span>指定 Bug</span><input :id="manualBugFieldID" v-model="manualBugID" class="form-control" placeholder="Bug ID 或飞书消息" @keyup.enter="fetchManualBug"></label>
    <button class="compact-button secondary-button" type="button" data-action="fetch-bug" :disabled="!selectedPlatform || !manualBugID.trim() || fetchingBug" @click="fetchManualBug">拉取指定 Bug</button>
  </div>
</div>
<div class="hook-row"><strong>Hook URL</strong><code>{{ hookURL || '保存平台后生成' }}</code><button class="compact-button secondary-button" type="button" data-action="copy-hook-url" :disabled="!hookURL" @click="copyHookURL">复制</button></div>
```

Add desktop rules near `.manual-bug-row`:

```css
.sync-control-row { min-width: 0; display: grid; grid-template-columns: auto 1px minmax(0, 1fr); align-items: end; gap: var(--sp-3); }
.sync-control-divider { align-self: stretch; width: 1px; background: var(--c-line); }
.manual-bug-row { min-width: 0; display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: end; gap: var(--sp-2); }
```

Add to the existing 900px media query:

```css
.sync-control-row { grid-template-columns: minmax(0, 1fr); }
.sync-control-divider { width: 100%; height: 1px; }
```

Keep the existing 640px rule:

```css
.manual-bug-row .compact-button { width: 100%; }
```

- [ ] **Step 4: Run focused layout and behavior tests**

Run:

```bash
cd web && npm test -- --run src/pages/BugInboxPage.test.ts src/components/BugTicketList.test.ts
```

Expected: both files PASS. Existing tests must continue to prove manual fetch, synchronization loading state, labelled controls, and mobile touch targets.

- [ ] **Step 5: Commit Task 2**

```bash
git add web/src/pages/BugInboxPage.vue web/src/pages/BugInboxPage.test.ts
git commit -m "fix: compact Bug synchronization controls"
```

### Task 3: Full verification and artifact guard

**Files:**
- Verify: `web/src/pages/BugInboxPage.vue`
- Verify: `web/src/pages/BugInboxPage.test.ts`
- Verify: `web/src/components/BugTicketList.vue`
- Verify: `web/src/components/BugTicketList.test.ts`
- Preserve: `internal/webui/dist/.gitkeep`

**Interfaces:**
- Consumes: Task 1’s optional list `actions` slot and Task 2’s `.sync-control-row`.
- Produces: a clean, fully verified `test` branch ready for explicit user-authorized push.

- [ ] **Step 1: Run the complete frontend suite and build**

```bash
cd web && npm test -- --run && npm run build
```

Expected: all Vitest files pass, `vue-tsc --noEmit` passes, and Vite production build exits 0.

- [ ] **Step 2: Guard the embed placeholder**

Run:

```bash
git status --short internal/webui/dist web/dist
```

If `internal/webui/dist/.gitkeep` is deleted, restore exactly this one-line file with `apply_patch`:

```text
# Keep this directory present for //go:embed all:dist in fresh checkouts.
```

Expected: no generated build output is staged or committed.

- [ ] **Step 3: Run repository checks**

```bash
go test ./...
make lint
git diff --check
```

Expected: every command exits 0.

- [ ] **Step 4: Review final scope**

```bash
git status --short
git diff --stat HEAD~2..HEAD
git diff --check HEAD~2..HEAD
git log -3 --oneline
```

Expected: implementation commits contain only the four Vue source/test files; the design and plan remain separate documentation commits; working tree is clean.

- [ ] **Step 5: Stop before push**

Report the final commit SHAs and validation results. Do not push until the user explicitly requests it.
