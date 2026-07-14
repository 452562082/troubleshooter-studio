# Bug Inbox Unified Sync Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the separate single-platform sync and local refresh actions with one inbox action that sequentially synchronizes every enabled Bug platform, refreshes the local list once, and leaves a cleaner sync/access configuration layout.

**Architecture:** Keep the existing single-platform Wails binding as the primitive and coordinate the enabled-platform loop in `BugInboxPage.vue`. The page owns transient progress and aggregate feedback; `useBugTickets` remains the only local-list loader, and no backend API or persistence schema changes are introduced.

**Tech Stack:** Vue 3 Composition API, TypeScript, Vitest, Vue Test Utils, existing Wails Bug bridge, scoped CSS.

## Global Constraints

- Synchronize enabled platforms sequentially in platform-list order so concurrent calls cannot race on the local Bug JSON store.
- A failed platform must not stop later platforms from synchronizing.
- Refresh the local Bug list exactly once after all attempted platform synchronizations.
- Keep background polling and “拉取指定 Bug” scoped exactly as they are today.
- Preserve at least 44px touch targets on mobile and do not add a new backend bulk API.
- Preserve the current uncommitted Cursor incident-capability filtering changes in the same page and tests.

---

### Task 1: Replace the two manual actions with one multi-platform sync flow

**Files:**
- Modify: `web/src/pages/BugInboxPage.test.ts:405-480`
- Modify: `web/src/pages/BugInboxPage.vue:239-253`
- Modify: `web/src/pages/BugInboxPage.vue:528-534`

**Interfaces:**
- Consumes: `platforms: Ref<BugPlatform[]>`, `syncBugPlatform(platformID: string): Promise<BugSyncResult>`, `loadTickets(): Promise<void>`, `toast.info/success/error`.
- Produces: `syncEnabledPlatforms(): Promise<void>` and the sole template action `data-action="sync-enabled-platforms"`.

- [ ] **Step 1: Replace the old independence tests with failing enabled-platform coverage**

Add a test for two enabled platforms and one disabled platform:

```ts
it('synchronizes every enabled platform and refreshes the local list once', async () => {
  vi.mocked(listBugPlatforms).mockResolvedValue([
    { id: 'zentao-a', name: '禅道 A', type: 'zentao', enabled: true },
    { id: 'zentao-off', name: '停用平台', type: 'zentao', enabled: false },
    { id: 'zentao-b', name: '禅道 B', type: 'zentao', enabled: true },
  ])
  vi.mocked(syncBugPlatform)
    .mockResolvedValueOnce({ platform_id: 'zentao-a', fetched: 2, stored: 2 })
    .mockResolvedValueOnce({ platform_id: 'zentao-b', fetched: 1, stored: 1 })
  const wrapper = await mountedInbox()
  vi.mocked(listBugs).mockClear()

  await wrapper.get('[data-action="sync-enabled-platforms"]').trigger('click')
  await flushPromises()

  expect(syncBugPlatform.mock.calls).toEqual([['zentao-a'], ['zentao-b']])
  expect(listBugs).toHaveBeenCalledTimes(1)
  expect(wrapper.find('[data-action="sync-platform"]').exists()).toBe(false)
})
```

Import `toast` from `../lib/toast` so its mocked methods can be asserted. Add a partial-failure test that makes the first call reject, verifies the second still runs, verifies one final `listBugs` call, and asserts `toast.error` names the failed platform. Add a no-enabled-platform test that asserts no sync/list refresh calls and `toast.info('请先启用 Bug 平台')`. In the all-success test, hold the first platform in a deferred promise and assert the second platform has not been called until that promise resolves; this proves synchronization is sequential rather than merely checking final call order.

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
cd web && npm test -- --run src/pages/BugInboxPage.test.ts -t 'enabled platform|failed platform|先启用 Bug 平台'
```

Expected: FAIL because `sync-enabled-platforms` is absent and the current action only synchronizes `selectedPlatform`.

- [ ] **Step 3: Implement sequential aggregation in the page**

Replace `syncSelectedPlatform` with:

```ts
function syncErrorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String((error as any)?.message ?? error)
}

async function syncEnabledPlatforms() {
  const enabled = platforms.value.filter(platform => platform.enabled)
  if (!enabled.length) {
    toast.info('请先启用 Bug 平台')
    return
  }
  syncingBugs.value = true
  let stored = 0
  const failures: string[] = []
  try {
    for (const platform of enabled) {
      try {
        const result = await syncBugPlatform(platform.id)
        stored += result.stored
      } catch (error) {
        failures.push(`${platform.name || platform.id}：${syncErrorMessage(error)}`)
      }
    }
    await loadTickets()
    const succeeded = enabled.length - failures.length
    if (!failures.length) {
      toast.success(`已同步 ${succeeded} 个平台，新增/更新 ${stored} 条`)
    } else if (succeeded > 0) {
      toast.error(`已同步 ${succeeded} 个平台，${failures.length} 个平台失败；新增/更新 ${stored} 条。${failures.join('；')}`)
    } else {
      toast.error(`所有已启用平台同步失败：${failures.join('；')}`)
    }
  } finally {
    syncingBugs.value = false
  }
}
```

Change the inbox button to call this function, use `data-action="sync-enabled-platforms"`, disable on `syncingBugs || tickets.loading.value`, show “同步中…” while active and “同步我的 Bug” otherwise, and rotate its SVG only while `syncingBugs` is true. Remove the configuration-area `sync-platform` button.

- [ ] **Step 4: Update existing interaction tests to use the sole sync action**

In the login/manual-fetch integration test, click `[data-action="sync-enabled-platforms"]`. Replace the old local-refresh loading test with a pending `syncBugPlatform` promise and assert the sole button is disabled, says “同步中…”, and has a spinning icon until the promise resolves.

- [ ] **Step 5: Run the complete page test and verify GREEN**

Run:

```bash
cd web && npm test -- --run src/pages/BugInboxPage.test.ts
```

Expected: all `BugInboxPage` tests pass; no test queries `sync-platform` or `refresh-tickets`.

### Task 2: Reflow the sync/access configuration section

**Files:**
- Modify: `web/src/pages/BugInboxPage.test.ts:300-410`
- Modify: `web/src/pages/BugInboxPage.vue:508-524`
- Modify: `web/src/pages/BugInboxPage.vue:665-708`

**Interfaces:**
- Consumes: existing `sync-settings`, `manual-bug-field`, `hook-row`, `fetchManualBug`, and `copyHookURL` behaviors.
- Produces: a two-column `.manual-bug-row` with the input occupying remaining width and responsive one-column layout below 640px.

- [ ] **Step 1: Write a failing layout contract test**

Add assertions for the three ordered rows:

```ts
const syncAccess = wrapper.get('.sync-access-section')
expect(syncAccess.find('[data-action="sync-platform"]').exists()).toBe(false)
expect(syncAccess.findAll(':scope > .sync-settings')).toHaveLength(1)
expect(syncAccess.findAll(':scope > .manual-bug-row')).toHaveLength(1)
expect(syncAccess.findAll(':scope > .hook-row')).toHaveLength(1)
expect(syncAccess.get('.manual-bug-row .manual-bug-field').exists()).toBe(true)
expect(syncAccess.get('.manual-bug-row [data-action="fetch-bug"]').exists()).toBe(true)
```

Extend the source-level responsive contract to require `.manual-bug-row { grid-template-columns: minmax(0, 1fr) auto; }` and the one-column rule inside the 640px media query.

- [ ] **Step 2: Run the layout test and verify RED**

Run:

```bash
cd web && npm test -- --run src/pages/BugInboxPage.test.ts -t 'sync/access layout'
```

Expected: FAIL because the page still uses the three-column `.trigger-row` containing the old sync button.

- [ ] **Step 3: Implement the revised markup and CSS**

Rename `.trigger-row` to `.manual-bug-row`, leave only the labelled input and fetch button in it, and add:

```css
.manual-bug-row {
  min-width: 0;
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  align-items: end;
  gap: var(--sp-2);
}

@media (max-width: 640px) {
  .manual-bug-row, .bot-config-row { grid-template-columns: minmax(0, 1fr); }
  .manual-bug-row .compact-button { width: 100%; }
}
```

Keep `sync-settings` first and `hook-row` last. Do not change selected-platform scoping for `fetchManualBug` or Hook URL generation.

- [ ] **Step 4: Run focused UI tests and verify GREEN**

Run:

```bash
cd web && npm test -- --run src/pages/BugInboxPage.test.ts src/components/BugTicketList.test.ts
```

Expected: both test files pass with no stale selector for `.trigger-row`, `sync-platform`, or `refresh-tickets`.

### Task 3: Full verification and intentional commit

**Files:**
- Verify: `web/src/pages/BugInboxPage.vue`
- Verify: `web/src/pages/BugInboxPage.test.ts`
- Preserve: `internal/webui/dist/.gitkeep`

**Interfaces:**
- Consumes: Task 1’s `syncEnabledPlatforms` and Task 2’s `.manual-bug-row`.
- Produces: a verified working tree ready for the user’s later push request.

- [ ] **Step 1: Run full frontend tests and production build**

Run:

```bash
cd web && npm test -- --run && npm run build
```

Expected: all Vitest files pass, `vue-tsc --noEmit` passes, and Vite completes successfully.

- [ ] **Step 2: Restore the embed placeholder if the Web build removed it**

If `git status --short` shows `D internal/webui/dist/.gitkeep`, restore exactly:

```text
# Keep this directory present for //go:embed all:dist in fresh checkouts.
```

- [ ] **Step 3: Run repository verification**

Run:

```bash
go test ./... -race
make lint
make build
git diff --check
```

Expected: every command exits 0. The known macOS malformed `LC_DYSYMTAB` linker warning may appear, but tests must still exit 0.

- [ ] **Step 4: Review the final scope**

Run:

```bash
git status --short
git diff --stat
git diff -- web/src/pages/BugInboxPage.vue web/src/pages/BugInboxPage.test.ts
```

Expected: the diff contains the previously approved Cursor filtering plus the unified-sync behavior and layout; no generated `web/dist` files or `.gitkeep` deletion is staged.

- [ ] **Step 5: Commit only after verification**

Stage the exact source and test files involved in both approved Bug workflow corrections:

```bash
git add cmd/tshoot-desktop/bindings_bug.go internal/bughub/match.go internal/bughub/match_test.go internal/bughub/platform.go internal/bughub/platform_test.go web/src/pages/BugInboxPage.vue web/src/pages/BugInboxPage.test.ts
git commit -m "fix: align Bug workflow candidates and synchronization"
```

Expected: the commit excludes `internal/webui/dist` build output and documentation commits remain separate.
