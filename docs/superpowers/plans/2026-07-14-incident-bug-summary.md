# 故障闭环 Bug 摘要精简 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 移除故障闭环页面重复的“刷新 Bug”入口，并将所选 Bug 精简为来源、标题、等级、创建时间、更新时间和状态摘要。

**Architecture:** 新建纯展示组件 `IncidentBugSummary.vue`，只消费 `BugRecord` 并负责故障闭环场景的格式化与响应式展示；`IncidentWorkbenchPage.vue` 继续拥有本地 Bug 初始化、路由选择、机器人匹配与 Case 生命周期。保留挂载时的 `tickets.load()`，删除仅供手动按钮调用的 `refreshTickets()`，不改 Bug 工单同步、后端 API 或通用 `BugTicketDetail.vue`。

**Tech Stack:** Vue 3 Composition API、TypeScript、Vue Test Utils、Vitest、Vite、Go 1.x 回归测试。

## Global Constraints

- 外部 Bug 同步入口只保留在“Bug 工单”页面；故障闭环不提供同步或手动刷新动作。
- 故障闭环挂载时必须继续调用本地 `tickets.load()`，并保持路由选中和已有 Case 恢复能力。
- Bug 等级格式为 `S<severity> · P<priority>`；任一字段缺失时只展示存在值，两者均缺失时展示 `-`。
- 摘要只展示来源/ID、标题、Bug 等级、创建时间、更新时间和 Bug 状态；不得展示系统、环境、服务、描述、复现步骤或附件。
- `BugTicketDetail.vue` 的 full/summary 模式和“Bug 工单”完整详情保持不变。
- Case 创建、重置、恢复、机器人选择和所有阶段流转行为保持不变。
- 状态必须有可读文字，时间使用有效的 `<time datetime>`，元数据使用 `<dl>/<dt>/<dd>`。
- 支持 375px、768px、1024px、1440px，不产生内容越界或横向滚动。
- 使用 TDD；只精确暂存本任务文件，不使用 `git add -A`。
- `internal/webui/dist/.gitkeep` 必须保留，生产构建清理后使用 `apply_patch` 恢复。

---

## File Map

- Create: `web/src/components/IncidentBugSummary.vue` — 故障闭环专用 Bug 摘要、格式化和响应式样式。
- Create: `web/src/components/IncidentBugSummary.test.ts` — 核心字段、等级降级、时间语义、空状态和详情隔离测试。
- Modify: `web/src/pages/IncidentWorkbenchPage.vue` — 接入专用摘要、移除重复刷新入口、更新页面说明和标题布局。
- Modify: `web/src/pages/IncidentWorkbenchPage.test.ts` — 固化初始化加载、无刷新按钮、摘要字段和现有选择行为。
- Preserve: `web/src/components/BugTicketDetail.vue` and `web/src/components/BugTicketDetail.test.ts` — 不修改，作为 Bug 工单详情兼容边界。

### Task 1: 新增故障闭环专用 Bug 摘要组件

**Files:**
- Create: `web/src/components/IncidentBugSummary.vue`
- Create: `web/src/components/IncidentBugSummary.test.ts`

**Interfaces:**
- Consumes: `BugRecord` from `web/src/lib/bridge/bugs.ts` through optional prop `bug?: BugRecord`.
- Produces: Vue component `IncidentBugSummary`; root selector `.incident-bug-summary`, metadata selector `.incident-bug-metadata`, grade selector `.incident-bug-grade`, and empty status `.incident-bug-summary-empty`.
- Produces no events and performs no data loading or Case mutation.

- [ ] **Step 1: Write failing component tests**

Create `web/src/components/IncidentBugSummary.test.ts`:

```ts
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { BugRecord } from '../lib/bridge/bugs'
import IncidentBugSummary from './IncidentBugSummary.vue'
import summarySource from './IncidentBugSummary.vue?raw'

const bug: BugRecord = {
  id: 'zentao-840',
  source: 'zentao',
  source_id: '840',
  title: '用户昵称模糊搜索结果不完整',
  status: 'active',
  severity: '3',
  priority: '3',
  created_at: '2026-07-08T11:12:00',
  updated_at: '2026-07-10T12:24:00',
  system_id: 'base',
  env: 'test',
  service_hints: ['user-api'],
  description: '搜索结果只显示一个用户',
  steps: '输入昵称后搜索',
  attachments: [{ id: 'shot-1', name: 'result.png', type: 'image/png' }],
}

describe('IncidentBugSummary', () => {
  it('renders only the incident decision fields with semantic time values', () => {
    const wrapper = mount(IncidentBugSummary, { props: { bug } })
    const metadata = wrapper.get('.incident-bug-metadata')

    expect(wrapper.get('.incident-bug-source').text()).toBe('禅道 #840')
    expect(wrapper.get('h2').text()).toBe('用户昵称模糊搜索结果不完整')
    expect(wrapper.get('.incident-bug-grade').text()).toBe('S3 · P3')
    expect(metadata.text()).toContain('创建时间')
    expect(metadata.text()).toContain('2026-07-08 11:12')
    expect(metadata.text()).toContain('更新时间')
    expect(metadata.text()).toContain('2026-07-10 12:24')
    expect(metadata.text()).toContain('Bug 状态')
    expect(wrapper.get('.incident-bug-status').text()).toBe('active')
    expect(wrapper.get('time[datetime="2026-07-08T11:12:00"]').exists()).toBe(true)
    expect(wrapper.get('time[datetime="2026-07-10T12:24:00"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('系统')
    expect(wrapper.text()).not.toContain('环境')
    expect(wrapper.text()).not.toContain('服务')
    expect(wrapper.text()).not.toContain('搜索结果只显示一个用户')
    expect(wrapper.text()).not.toContain('输入昵称后搜索')
    expect(wrapper.text()).not.toContain('result.png')
  })

  it.each([
    [{ severity: '2', priority: undefined }, 'S2'],
    [{ severity: undefined, priority: '1' }, 'P1'],
    [{ severity: undefined, priority: undefined }, '-'],
    [{ severity: 'S4', priority: 'P2' }, 'S4 · P2'],
  ])('formats partial and prefixed grades without duplicate prefixes', (grade, expected) => {
    const wrapper = mount(IncidentBugSummary, { props: { bug: { ...bug, ...grade } } })

    expect(wrapper.get('.incident-bug-grade').text()).toBe(expected)
  })

  it('keeps invalid source times readable without emitting an invalid datetime attribute', () => {
    const wrapper = mount(IncidentBugSummary, {
      props: { bug: { ...bug, created_at: 'unknown time', updated_at: undefined } },
    })
    const values = wrapper.findAll('.incident-bug-time')

    expect(values[0].text()).toBe('unknown time')
    expect(values[0].attributes('datetime')).toBeUndefined()
    expect(values[1].text()).toBe('-')
    expect(values[1].element.tagName).toBe('SPAN')
  })

  it('renders an accessible empty state and responsive overflow contract', () => {
    const empty = mount(IncidentBugSummary)
    const selected = mount(IncidentBugSummary, { props: { bug } })

    expect(empty.get('.incident-bug-summary-empty[role="status"]').text()).toContain('选择一条 Bug')
    expect(selected.get('.incident-bug-summary').attributes('data-responsive-viewports')).toBe('375,768,1024,1440')
    expect(selected.get('.incident-bug-summary').attributes('data-overflow-safe')).toBe('true')
    expect(summarySource).toContain('grid-template-columns: repeat(2, minmax(0, 1fr))')
    expect(summarySource).toContain('@container incident-bug-summary (max-width: 520px)')
    expect(summarySource).toContain('grid-template-columns: minmax(0, 1fr)')
    expect(summarySource).toContain('overflow-wrap: anywhere')
  })
})
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
npm --prefix web test -- src/components/IncidentBugSummary.test.ts
```

Expected: FAIL because `IncidentBugSummary.vue` does not exist.

- [ ] **Step 3: Implement the minimal standalone summary component**

Create `web/src/components/IncidentBugSummary.vue`:

```vue
<script lang="ts">
let incidentBugSummarySequence = 0
</script>

<script setup lang="ts">
import { computed } from 'vue'
import type { BugRecord } from '../lib/bridge/bugs'

interface DisplayTime {
  text: string
  datetime?: string
}

const props = defineProps<{ bug?: BugRecord }>()
const summaryInstanceID = `incident-bug-summary-${++incidentBugSummarySequence}`
const grade = computed(() => bugGrade(props.bug))
const createdTime = computed(() => formatTime(props.bug?.created_at))
const updatedTime = computed(() => formatTime(props.bug?.updated_at))

function sourceLabel(bug: BugRecord): string {
  if (bug.source === 'zentao') return `禅道 #${bug.source_id || '-'}`
  return [bug.source || '未知来源', bug.source_id].filter(Boolean).join(' #')
}

function gradePart(prefix: 'S' | 'P', value?: string): string {
  const normalized = value?.trim()
  if (!normalized) return ''
  return normalized.toUpperCase().startsWith(prefix) ? normalized : `${prefix}${normalized}`
}

function bugGrade(bug?: BugRecord): string {
  if (!bug) return '-'
  return [gradePart('S', bug.severity), gradePart('P', bug.priority)].filter(Boolean).join(' · ') || '-'
}

function formatTime(value?: string): DisplayTime {
  if (!value) return { text: '-' }
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return { text: value }
  return {
    text: `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')} ${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`,
    datetime: value,
  }
}
</script>

<template>
  <section
    v-if="bug"
    class="incident-bug-summary"
    data-responsive-viewports="375,768,1024,1440"
    data-overflow-safe="true"
    :aria-labelledby="`${summaryInstanceID}-title`"
  >
    <header class="incident-bug-heading">
      <p class="incident-bug-source">{{ sourceLabel(bug) }}</p>
      <h2 :id="`${summaryInstanceID}-title`">{{ bug.title }}</h2>
    </header>

    <dl class="incident-bug-metadata">
      <div>
        <dt>Bug 等级</dt>
        <dd class="incident-bug-grade">{{ grade }}</dd>
      </div>
      <div>
        <dt>Bug 状态</dt>
        <dd><span class="incident-bug-status">{{ bug.status || '-' }}</span></dd>
      </div>
      <div>
        <dt>创建时间</dt>
        <dd>
          <time v-if="createdTime.datetime" class="incident-bug-time" :datetime="createdTime.datetime">{{ createdTime.text }}</time>
          <span v-else class="incident-bug-time">{{ createdTime.text }}</span>
        </dd>
      </div>
      <div>
        <dt>更新时间</dt>
        <dd>
          <time v-if="updatedTime.datetime" class="incident-bug-time" :datetime="updatedTime.datetime">{{ updatedTime.text }}</time>
          <span v-else class="incident-bug-time">{{ updatedTime.text }}</span>
        </dd>
      </div>
    </dl>
  </section>
  <p v-else class="incident-bug-summary-empty" role="status">选择一条 Bug 开始故障闭环</p>
</template>

<style scoped>
.incident-bug-summary {
  min-width: 0;
  container: incident-bug-summary / inline-size;
  color: var(--c-text);
}
.incident-bug-heading { min-width: 0; margin-bottom: var(--sp-3); }
.incident-bug-source { margin: 0 0 var(--sp-1); color: var(--c-accent-hover); font-size: var(--fs-sm); font-weight: 700; overflow-wrap: anywhere; }
.incident-bug-heading h2 { margin: 0; color: var(--c-ink); font-size: 20px; line-height: 1.35; overflow-wrap: anywhere; }
.incident-bug-metadata { min-width: 0; display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--sp-2); margin: 0; }
.incident-bug-metadata > div { min-width: 0; padding: var(--sp-2); border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf-2); }
.incident-bug-metadata dt { margin-bottom: 4px; color: var(--c-muted); font-size: var(--fs-xs); }
.incident-bug-metadata dd { min-width: 0; margin: 0; color: var(--c-ink); font-size: var(--fs-base); overflow-wrap: anywhere; }
.incident-bug-status { display: inline-flex; max-width: 100%; padding: 2px 8px; overflow-wrap: anywhere; border: 1px solid #c7d2fe; border-radius: 999px; background: #eef2ff; color: #3730a3; font-size: var(--fs-xs); }
.incident-bug-summary-empty { min-height: 160px; margin: 0; display: grid; place-items: center; color: var(--c-muted); font-size: var(--fs-sm); }
@container incident-bug-summary (max-width: 520px) {
  .incident-bug-metadata { grid-template-columns: minmax(0, 1fr); }
}
</style>
```

- [ ] **Step 4: Run the component test and verify GREEN**

Run:

```bash
npm --prefix web test -- src/components/IncidentBugSummary.test.ts
```

Expected: the suite PASS, including all four parameterized grade cases.

- [ ] **Step 5: Commit the standalone component**

Run:

```bash
git add -- web/src/components/IncidentBugSummary.vue web/src/components/IncidentBugSummary.test.ts
git diff --cached --check
git commit -m "feat: add compact incident Bug summary"
```

Expected: one commit containing only the new component and its test.

### Task 2: 接入故障闭环并移除重复刷新入口

**Files:**
- Modify: `web/src/pages/IncidentWorkbenchPage.vue`
- Modify: `web/src/pages/IncidentWorkbenchPage.test.ts`
- Preserve after build: `internal/webui/dist/.gitkeep`

**Interfaces:**
- Consumes: `IncidentBugSummary` from Task 1 with prop `bug?: BugRecord`.
- Preserves: `useBugTickets({ listBugs, fetchBugByID })`, initial `tickets.load()`, `selectBug(id: string)`, route query behavior, bot matching and Case lifecycle.
- Removes: page-local `refreshTickets(): Promise<void>` and the visible “刷新 Bug” button.

- [ ] **Step 1: Write failing page integration tests**

Extend `bugA` in `web/src/pages/IncidentWorkbenchPage.test.ts` so the page fixture covers every summary field while retaining fields that must not render in the summary:

```ts
const bugA = {
  id: 'bug-a',
  source: 'zentao',
  source_id: '840',
  title: '支付页超时',
  status: 'active',
  severity: '3',
  priority: '2',
  created_at: '2026-07-08T11:12:00',
  updated_at: '2026-07-10T12:24:00',
  description: '支付接口返回超时',
  steps: '打开支付页',
  env: 'test',
  system_id: 'base',
  service_hints: ['checkout'],
}
```

Add these tests near the beginning of `describe('IncidentWorkbenchPage', ...)`:

```ts
it('loads locally stored Bugs on mount without exposing a duplicate refresh action', async () => {
  vi.mocked(listBugs).mockResolvedValue([bugA])

  const wrapper = await mountedPage()

  expect(listBugs).toHaveBeenCalledTimes(1)
  expect(wrapper.text()).not.toContain('刷新 Bug')
  expect(wrapper.get('.incident-header p').text()).toContain('Bug 工单')
})

it('renders the selected Bug as a compact incident summary', async () => {
  route.query = { bug_id: 'bug-a' }
  vi.mocked(listBugs).mockResolvedValue([bugA])

  const wrapper = await mountedPage()
  const summary = wrapper.get('.incident-bug-summary')

  expect(summary.get('.incident-bug-source').text()).toBe('禅道 #840')
  expect(summary.get('h2').text()).toBe('支付页超时')
  expect(summary.get('.incident-bug-grade').text()).toBe('S3 · P2')
  expect(summary.text()).toContain('2026-07-08 11:12')
  expect(summary.text()).toContain('2026-07-10 12:24')
  expect(summary.get('.incident-bug-status').text()).toBe('active')
  expect(summary.text()).not.toContain('base')
  expect(summary.text()).not.toContain('test')
  expect(summary.text()).not.toContain('checkout')
  expect(summary.text()).not.toContain('支付接口返回超时')
  expect(summary.text()).not.toContain('打开支付页')
})
```

Replace all existing page-test selectors that expect `.ticket-detail h2` with the new stable selector:

```ts
expect(wrapper.get('.incident-bug-summary h2').text()).toBe('缓存命中下降')
```

and, where the selected Bug is `bugA`:

```ts
expect(wrapper.get('.incident-bug-summary h2').text()).toBe('支付页超时')
```

- [ ] **Step 2: Run the focused page test and verify RED**

Run:

```bash
npm --prefix web test -- src/pages/IncidentWorkbenchPage.test.ts
```

Expected: FAIL because “刷新 Bug” still renders and `.incident-bug-summary` does not yet exist on the page.

- [ ] **Step 3: Replace the generic detail with the incident summary**

In `web/src/pages/IncidentWorkbenchPage.vue`, replace the component import:

```ts
import IncidentBugSummary from '../components/IncidentBugSummary.vue'
```

Delete this entire page-local function while leaving the `onMounted` block unchanged:

```ts
async function refreshTickets() {
  try {
    await tickets.load()
  } catch (error) {
    toastError('读取 Bug 工单', error)
  }
}
```

Replace the page header with:

```vue
<header class="incident-header">
  <div>
    <h1>故障闭环</h1>
    <p>Bug 数据由 Bug 工单统一同步；本页用于选择工单并推进可恢复的验证、排障与修复流程。</p>
  </div>
</header>
```

Replace the middle-panel detail usage with:

```vue
<IncidentBugSummary :bug="tickets.selectedBug.value" />
```

Keep the invalid-route status, `showStandaloneStart` card and `workflowNotice` immediately around the new summary exactly as they are.

- [ ] **Step 4: Remove obsolete header action layout without changing workflow controls**

Replace the header rule in `web/src/pages/IncidentWorkbenchPage.vue` with:

```css
.incident-header { min-width: 0; }
```

Keep the existing heading and paragraph rules. In the 700px media query, stop treating the header as a flex action row:

```css
@media (max-width: 700px) {
  .start-card { align-items: stretch; flex-direction: column; }
  .selection-workspace { grid-template-columns: minmax(0, 1fr); }
  .bot-panel { grid-column: auto; }
  .ticket-list-panel, .bot-panel { max-height: none; }
  .start-card .btn { width: 100%; }
  .reset-dialog footer { flex-direction: column; }
  .reset-dialog footer .btn { width: 100%; }
}
```

Do not remove the shared `.btn` rules because Case start, reset and lifecycle actions still use them.

- [ ] **Step 5: Run focused tests and verify GREEN**

Run:

```bash
npm --prefix web test -- src/components/IncidentBugSummary.test.ts src/pages/IncidentWorkbenchPage.test.ts
```

Expected: both suites PASS; route selection, existing Case recovery, reset behavior and the new compact summary tests all pass.

- [ ] **Step 6: Run full verification**

Run in order:

```bash
npm --prefix web test
npm --prefix web run build
go test ./... -race
make lint
```

Expected:

- all Vitest suites PASS;
- `vue-tsc --noEmit` and Vite production build succeed;
- all Go packages pass with the race detector;
- lint reports no Go formatting, vet or Vue TypeScript errors.

If the Vite build deletes `internal/webui/dist/.gitkeep`, restore it with `apply_patch` using exactly:

```text
# Keep this directory present for //go:embed all:dist in fresh checkouts.
```

Then run:

```bash
git diff --check
git status --short
```

Expected: only `IncidentWorkbenchPage.vue` and `IncidentWorkbenchPage.test.ts` are modified; `.gitkeep` is present and not staged as deleted.

- [ ] **Step 7: Commit the page integration**

Run:

```bash
git add -- web/src/pages/IncidentWorkbenchPage.vue web/src/pages/IncidentWorkbenchPage.test.ts
git diff --cached --check
git diff --cached --stat
git commit -m "fix: simplify incident Bug selection summary"
git status --short --branch
```

Expected: a second implementation commit containing only the page and page test; the working tree is clean on `test`.

## Final Acceptance Check

- [ ] “故障闭环”页面没有“刷新 Bug”按钮或 `refreshTickets` 方法。
- [ ] 页面初始化仍只读取一次本地 Bug 列表，路由选中和已有 Case 恢复测试通过。
- [ ] 摘要显示来源/ID、标题、`S · P` 等级、创建时间、更新时间和状态。
- [ ] 摘要不显示系统、环境、服务、描述、步骤或附件。
- [ ] `BugTicketDetail.vue` 与其测试没有改动。
- [ ] Case、机器人和重置流程相关测试全部保持通过。
- [ ] 前端全量测试、生产构建、Go race 测试和 lint 全部通过。
- [ ] `internal/webui/dist/.gitkeep` 被保留，提交范围精确且工作树干净。
