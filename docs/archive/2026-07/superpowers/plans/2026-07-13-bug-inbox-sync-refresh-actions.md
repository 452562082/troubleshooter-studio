# Bug Inbox Sync and Refresh Actions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make external Bug-platform synchronization and local inbox refresh visibly and behaviorally distinct.

**Architecture:** Keep the existing `syncSelectedPlatform()` and `loadTickets()` data paths unchanged. Express their different scopes through exact button copy, inline SVG icons, independent `data-action` hooks, and loading states in `BugInboxPage.vue`; lock the call boundary with component tests.

**Tech Stack:** Vue 3 Composition API, TypeScript, Vue Test Utils, Vitest, scoped CSS.

## Global Constraints

- “从平台同步” calls `syncBugPlatform`, then refreshes the local list through the existing `loadTickets()` flow.
- “刷新列表” calls only the local `listBugs` flow and must not call `syncBugPlatform`.
- Use inline SVG icons; do not use emoji or character icons.
- Loading actions are disabled and show “同步中…” or “刷新中…”.
- Keep visible keyboard focus, a minimum 44px mobile touch target, and `prefers-reduced-motion` behavior.
- Do not change backend APIs, persistence models, polling behavior, or synchronization strategy.
- Preserve the existing uncommitted platform-save visibility fix in `BugInboxPage.vue` and its regression test.

---

## File Structure

- Modify `web/src/pages/BugInboxPage.vue`: button copy, SVGs, loading presentation, refresh-button styling, and reduced-motion rule.
- Modify `web/src/pages/BugInboxPage.test.ts`: behavioral boundary, icon/copy, and loading-state regressions.

### Task 1: Distinguish platform synchronization from local list refresh

**Files:**
- Modify: `web/src/pages/BugInboxPage.vue:235-248,501-513,627-675`
- Test: `web/src/pages/BugInboxPage.test.ts`

**Interfaces:**
- Consumes: existing `syncSelectedPlatform(): Promise<void>`, `loadTickets(): Promise<void>`, `syncingBugs: Ref<boolean>`, and `tickets.loading: Ref<boolean>`.
- Produces: stable DOM hooks `data-action="sync-platform"` and `data-action="refresh-tickets"`; exact loading copy “同步中…” and “刷新中…”.

- [ ] **Step 1: Write the failing data-boundary test**

Add this component test. It proves local refresh never calls the external platform and platform sync still refreshes the local list:

```ts
it('keeps platform sync separate from local list refresh', async () => {
  const platform = {
    id: 'zentao-main', name: '测试环境', type: 'zentao',
    auth_mode: 'feishu_sso', enabled: true,
  }
  vi.mocked(listBugPlatforms).mockResolvedValue([platform])
  vi.mocked(listBugs).mockResolvedValue([bug])
  vi.mocked(syncBugPlatform).mockResolvedValue({
    platform_id: 'zentao-main', fetched: 1, stored: 1,
  })
  const wrapper = await mountedInbox()
  await wrapper.get('[data-action="toggle-platform-config"]').trigger('click')

  vi.mocked(listBugs).mockClear()
  vi.mocked(syncBugPlatform).mockClear()
  const refresh = wrapper.get('[data-action="refresh-tickets"]')
  expect(refresh.text()).toContain('刷新列表')
  expect(refresh.find('svg[aria-hidden="true"]').exists()).toBe(true)
  await refresh.trigger('click')
  await flushPromises()
  expect(listBugs).toHaveBeenCalledTimes(1)
  expect(syncBugPlatform).not.toHaveBeenCalled()

  vi.mocked(listBugs).mockClear()
  const sync = wrapper.get('[data-action="sync-platform"]')
  expect(sync.text()).toContain('从平台同步')
  expect(sync.find('svg[aria-hidden="true"]').exists()).toBe(true)
  await sync.trigger('click')
  await flushPromises()
  expect(syncBugPlatform).toHaveBeenCalledWith('zentao-main')
  expect(listBugs).toHaveBeenCalledTimes(1)
})
```

- [ ] **Step 2: Write the failing refresh loading-state test**

Use a deferred local list request so the in-flight state can be inspected:

```ts
it('shows a disabled loading state while refreshing the local list', async () => {
  const wrapper = await mountedInbox()
  let resolveRefresh!: (bugs: (typeof bug)[]) => void
  vi.mocked(listBugs).mockImplementationOnce(() => new Promise(resolve => {
    resolveRefresh = resolve
  }))

  const refresh = wrapper.get('[data-action="refresh-tickets"]')
  await refresh.trigger('click')
  await wrapper.vm.$nextTick()
  expect(refresh.attributes('disabled')).toBeDefined()
  expect(refresh.text()).toContain('刷新中…')
  expect(refresh.get('svg').classes()).toContain('spinning')

  resolveRefresh([bug])
  await flushPromises()
  expect(refresh.attributes('disabled')).toBeUndefined()
  expect(refresh.text()).toContain('刷新列表')
})
```

- [ ] **Step 3: Write the failing platform-sync loading-state test**

Use a deferred platform request to prove the external action has its own loading copy and disabled state:

```ts
it('shows a disabled loading state while synchronizing the platform', async () => {
  const platform = {
    id: 'zentao-main', name: '测试环境', type: 'zentao',
    auth_mode: 'feishu_sso', enabled: true,
  }
  vi.mocked(listBugPlatforms).mockResolvedValue([platform])
  const wrapper = await mountedInbox()
  await wrapper.get('[data-action="toggle-platform-config"]').trigger('click')
  let resolveSync!: (result: { platform_id: string; fetched: number; stored: number }) => void
  vi.mocked(syncBugPlatform).mockImplementationOnce(() => new Promise(resolve => {
    resolveSync = resolve
  }))

  const sync = wrapper.get('[data-action="sync-platform"]')
  await sync.trigger('click')
  await wrapper.vm.$nextTick()
  expect(sync.attributes('disabled')).toBeDefined()
  expect(sync.text()).toContain('同步中…')

  resolveSync({ platform_id: 'zentao-main', fetched: 1, stored: 1 })
  await flushPromises()
  expect(sync.attributes('disabled')).toBeUndefined()
  expect(sync.text()).toContain('从平台同步')
})
```

- [ ] **Step 4: Run the focused test and verify RED**

Run `npm --prefix web test -- --run src/pages/BugInboxPage.test.ts`.

Expected: FAIL because `refresh-tickets`, the new copy, inline refresh SVG, and loading presentation do not exist yet.

- [ ] **Step 5: Implement the platform sync presentation**

Keep the current handler and disabled expression, replacing only the button content:

```vue
<button class="compact-button secondary-button" type="button" data-action="sync-platform" :disabled="!selectedPlatform || syncingBugs" @click="syncSelectedPlatform">
  <svg aria-hidden="true" viewBox="0 0 24 24" fill="none"><path d="M20 7h-5V2M4 17h5v5M18.5 11a7 7 0 0 0-11.9-4.9L4 8M5.5 13a7 7 0 0 0 11.9 4.9L20 16" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
  {{ syncingBugs ? '同步中…' : '从平台同步' }}
</button>
```

- [ ] **Step 6: Implement the local refresh presentation**

Keep `loadTickets` as the only click handler:

```vue
<button class="compact-button secondary-button refresh-button" type="button" data-action="refresh-tickets" :aria-label="tickets.loading.value ? '正在刷新本地 Bug 列表' : '刷新本地 Bug 列表'" :disabled="tickets.loading.value" @click="loadTickets">
  <svg aria-hidden="true" :class="{ spinning: tickets.loading.value }" viewBox="0 0 24 24" fill="none"><path d="M20 11a8 8 0 1 0-2.34 5.66M20 4v7h-7" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
  {{ tickets.loading.value ? '刷新中…' : '刷新列表' }}
</button>
```

- [ ] **Step 7: Add compact refresh styling and reduced motion**

Use the existing absolute heading tool placement, reserve space for the longer label, and animate only the loading icon:

```css
.ticket-list-panel :deep(.list-heading) { padding-right: 112px; }
.refresh-button { position: absolute; z-index: 1; top: var(--sp-2); right: var(--sp-2); }
.refresh-button svg { width: 16px; height: 16px; }
.refresh-button svg.spinning { animation: refresh-spin 800ms linear infinite; }
@keyframes refresh-spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) {
  .refresh-button svg.spinning { animation: none; }
}
```

Do not add a new loading ref; use `tickets.loading.value`.

- [ ] **Step 8: Run the focused test and verify GREEN**

Run `npm --prefix web test -- --run src/pages/BugInboxPage.test.ts`.

Expected: all `BugInboxPage` tests PASS, including the new boundary and loading-state tests.

- [ ] **Step 9: Run full verification**

Run these commands:

```bash
npm --prefix web test -- --run
npm --prefix web run build
make lint
git diff --check
```

Expected: all Web tests PASS; Vue type-check and production build PASS; lint PASS; `git diff --check` has no output. Restore `internal/webui/dist/.gitkeep` if the build removes it.

- [ ] **Step 10: Commit the implementation explicitly**

```bash
git add web/src/pages/BugInboxPage.vue web/src/pages/BugInboxPage.test.ts
git status --short
git commit -m "fix: distinguish Bug platform sync from list refresh"
```

Expected: the commit contains the previously verified save-visibility fix plus this sync/refresh clarification, and no generated `web/dist` files or `internal/webui/dist/.gitkeep` deletion.
