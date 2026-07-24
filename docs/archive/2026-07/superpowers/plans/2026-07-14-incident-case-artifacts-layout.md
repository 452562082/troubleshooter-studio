# 故障 Case 详情下置与阶段输出滚动 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将故障 Cases 和当前 Case 保持在上方主工作区，把证据详情移动到下方全宽区域，并让阶段输出在随窗口高度伸缩的独立视口内滚动。

**Architecture:** `BugCaseLifecycle.vue` 只调整既有三块语义区域的 CSS Grid 排列，不改变 props、emits 或流程行为；`BugCaseArtifacts.vue` 继续消费同一个 `IncidentCaseDetail`，负责详情卡片两列网格和阶段输出滚动容器。所有输出内容保持完整，滚动视口只改变展示边界，不增加折叠、展开、拖拽或持久化状态。

**Tech Stack:** Vue 3 Composition API、TypeScript、scoped CSS、Vue Test Utils、Vitest、Vite、Go 1.x 回归测试。

## Global Constraints

- 桌面端“故障 Cases”和当前 Case 必须并排位于上方，“Case 证据与详情”必须位于下一行并设置 `grid-column: 1 / -1`。
- 899px 以下生命周期改为单列；560px 以下现有阶段进度、Case 标题和对话框规则保持不变。
- 详情内部桌面为两列，阶段输出独占整行；899px 以下详情为单列。
- 阶段输出标题位于滚动区域之外，输出内容完整保留且仅在内部纵向滚动。
- 桌面滚动高度必须是 `clamp(320px, 45vh, 640px)`；899px 以下必须是 `clamp(280px, 42vh, 480px)`。
- 滚动容器必须包含 `overflow-y: auto`、`overflow-x: hidden`、`scrollbar-gutter: stable`、`overscroll-behavior: contain`。
- 滚动容器必须提供 `role="region"`、`aria-label="阶段输出内容"`、`tabindex="0"` 和可见 `:focus-visible` 样式。
- 不改变 Case 状态机、props、emits、选择、刷新、主操作、重置、关系跳转或任何输出数据。
- 不增加折叠、展开全部、拖拽高度或高度持久化。
- 375px、768px、1024px、1440px 不得出现页面级横向滚动。
- 使用 TDD；精确暂存任务文件，不使用 `git add -A`。
- 生产构建后必须保留 `internal/webui/dist/.gitkeep`，内容为 `# Keep this directory present for //go:embed all:dist in fresh checkouts.`。

---

## File Map

- Modify: `web/src/components/BugCaseLifecycle.vue` — 上方双列、下方详情跨列和窄屏堆叠。
- Modify: `web/src/components/BugCaseLifecycle.test.ts` — 语义区域顺序、两列/跨列/单列响应式契约。
- Modify: `web/src/components/BugCaseArtifacts.vue` — 详情两列网格、阶段输出跨行及独立滚动容器。
- Modify: `web/src/components/BugCaseArtifacts.test.ts` — 输出容器结构、普通/历史内容、可访问性和响应式滚动契约。
- Preserve: `web/src/pages/IncidentWorkbenchPage.vue` — 页面数据流和 Case 编排不变。
- Preserve: `web/src/lib/bridge/bugWorkflow.ts` — 类型和 bridge 契约不变。

### Task 1: 将 Case 生命周期调整为上方双列、下方详情

**Files:**
- Modify: `web/src/components/BugCaseLifecycle.vue`
- Modify: `web/src/components/BugCaseLifecycle.test.ts`

**Interfaces:**
- Consumes unchanged props: `cases: IncidentCase[]`, `detail: IncidentCaseDetail | null`, `pending?: boolean`, `error?: string`.
- Preserves unchanged emits: `select`, `refresh`, `primary`, `reset`.
- Produces DOM order `.case-list-column`, `.case-main-column`, `.case-detail-column`; the detail column spans the full second Grid row.

- [ ] **Step 1: Replace the three-column layout assertion with failing top/bottom layout tests**

In `web/src/components/BugCaseLifecycle.test.ts`, replace the existing test named `renders three semantic columns, six stages, timeline and one primary button` with:

```ts
it('renders Cases and the current Case above a full-width detail region', () => {
  const wrapper = mount(BugCaseLifecycle, { props: { cases: [incident('waiting_fix_approval')], detail: detail('waiting_fix_approval') } })
  const columns = wrapper.findAll('.case-column')
  const source = readFileSync('src/components/BugCaseLifecycle.vue', 'utf8')

  expect(columns).toHaveLength(3)
  expect(columns[0].classes()).toContain('case-list-column')
  expect(columns[1].classes()).toContain('case-main-column')
  expect(columns[2].classes()).toContain('case-detail-column')
  expect(source).toMatch(/\.case-lifecycle \{[^}]*grid-template-columns: minmax\(210px, \.72fr\) minmax\(0, 1\.65fr\);/)
  expect(source).toMatch(/\.case-detail-column \{[^}]*grid-column: 1 \/ -1;/)
  expect(wrapper.findAll('.lifecycle-stage')).toHaveLength(6)
  expect(wrapper.find('[aria-label="故障处理阶段"]').exists()).toBe(true)
  expect(wrapper.find('[aria-label="Case 时间线"]').text()).toContain('transition')
  expect(wrapper.findAll('.current-action-card .primary-action')).toHaveLength(1)
})
```

Extend `contains responsive no-overflow contracts for all supported viewport fixtures` with these assertions after loading `source`:

```ts
expect(source).not.toContain('@media (max-width: 1180px)')
expect(source).toMatch(/@media \(max-width: 899px\)[\s\S]*?\.case-lifecycle \{ grid-template-columns: minmax\(0, 1fr\); \}/)
expect(source).toMatch(/@media \(max-width: 899px\)[\s\S]*?\.case-detail-column \{ grid-column: auto; \}/)
```

Keep the existing assertions for `data-responsive-viewports`, `data-overflow-safe`, `min-width: 0` and the 560px breakpoint.

- [ ] **Step 2: Run the lifecycle test and verify RED**

Run:

```bash
npm --prefix web test -- src/components/BugCaseLifecycle.test.ts
```

Expected: FAIL because the root still declares three columns, `.case-detail-column` does not span the full row, and the 1180px transition block still exists.

- [ ] **Step 3: Implement the two-column primary workspace**

In `web/src/components/BugCaseLifecycle.vue`, replace the root layout rule with:

```css
.case-lifecycle { width: 100%; min-width: 0; display: grid; grid-template-columns: minmax(210px, .72fr) minmax(0, 1.65fr); align-items: start; gap: var(--sp-3); color: var(--c-text); }
```

Add the full-width detail placement immediately after the existing `.case-main-column` rule:

```css
.case-detail-column { grid-column: 1 / -1; }
```

Delete the complete `@media (max-width: 1180px)` block. Replace the beginning of the 899px block with:

```css
@media (max-width: 899px) {
  .case-lifecycle { grid-template-columns: minmax(0, 1fr); }
  .case-detail-column { grid-column: auto; }
  .case-list-column { max-height: 310px; overflow-y: auto; }
```

Keep the existing current-action, button and dialog rules in that breakpoint. Remove the two `:deep(.artifact-sections)` rules from `BugCaseLifecycle.vue`; Task 2 moves detail-grid ownership into `BugCaseArtifacts.vue`.

- [ ] **Step 4: Run the lifecycle test and verify GREEN**

Run:

```bash
npm --prefix web test -- src/components/BugCaseLifecycle.test.ts
```

Expected: the suite PASS, including existing primary-action, approval, reset, deployment and responsive behavior.

- [ ] **Step 5: Commit the lifecycle layout**

Run:

```bash
git add -- web/src/components/BugCaseLifecycle.vue web/src/components/BugCaseLifecycle.test.ts
git diff --cached --check
git commit -m "fix: move incident Case details below workflow"
```

Expected: one commit containing only the lifecycle component and its test.

### Task 2: 给阶段输出增加响应式独立滚动视口

**Files:**
- Modify: `web/src/components/BugCaseArtifacts.vue`
- Modify: `web/src/components/BugCaseArtifacts.test.ts`
- Preserve after build: `internal/webui/dist/.gitkeep`

**Interfaces:**
- Consumes unchanged prop: `detail: IncidentCaseDetail`.
- Preserves unchanged emit: `'select-case': [caseID: string]`.
- Produces `.attempt-output-card` spanning the detail grid and `.attempt-output-scroll` containing every normal and legacy attempt output.

- [ ] **Step 1: Write failing structure, accessibility and CSS-contract tests**

Add this import to `web/src/components/BugCaseArtifacts.test.ts`:

```ts
import artifactSource from './BugCaseArtifacts.vue?raw'
```

Add this test after the main rendering test:

```ts
it('keeps the stage title outside a responsive keyboard-scrollable output region', () => {
  const wrapper = mount(BugCaseArtifacts, { props: { detail } })
  const card = wrapper.get('.attempt-output-card')
  const scroll = card.get('.attempt-output-scroll')

  expect(card.get(':scope > h3').text()).toBe('阶段输出')
  expect(scroll.attributes('role')).toBe('region')
  expect(scroll.attributes('aria-label')).toBe('阶段输出内容')
  expect(scroll.attributes('tabindex')).toBe('0')
  expect(scroll.findAll('.artifact-item')).toHaveLength(detail.attempts.length)
  expect(artifactSource).toMatch(/\.artifact-sections \{[^}]*grid-template-columns: repeat\(2, minmax\(0, 1fr\)\);/)
  expect(artifactSource).toMatch(/\.attempt-output-card \{[^}]*grid-column: 1 \/ -1;/)
  expect(artifactSource).toMatch(/\.attempt-output-scroll \{[^}]*height: clamp\(320px, 45vh, 640px\);/)
  expect(artifactSource).toMatch(/\.attempt-output-scroll \{[^}]*overflow-y: auto;/)
  expect(artifactSource).toMatch(/\.attempt-output-scroll \{[^}]*overflow-x: hidden;/)
  expect(artifactSource).toMatch(/\.attempt-output-scroll \{[^}]*scrollbar-gutter: stable;/)
  expect(artifactSource).toMatch(/\.attempt-output-scroll \{[^}]*overscroll-behavior: contain;/)
  expect(artifactSource).toContain('.attempt-output-scroll:focus-visible')
})
```

Extend `keeps imported legacy attempt output readable` with:

```ts
expect(wrapper.get('.attempt-output-scroll').find('.legacy-attempt').exists()).toBe(true)
expect(wrapper.get('.attempt-output-card > h3').text()).toBe('阶段输出')
```

Add a responsive test:

```ts
it('stacks artifact cards and scales the output viewport on narrow screens', () => {
  expect(artifactSource).toMatch(/@media \(max-width: 899px\)[\s\S]*?\.artifact-sections \{ grid-template-columns: minmax\(0, 1fr\); \}/)
  expect(artifactSource).toMatch(/@media \(max-width: 899px\)[\s\S]*?\.attempt-output-card \{ grid-column: auto; \}/)
  expect(artifactSource).toMatch(/@media \(max-width: 899px\)[\s\S]*?\.attempt-output-scroll \{ height: clamp\(280px, 42vh, 480px\); \}/)
})
```

- [ ] **Step 2: Run the artifact test and verify RED**

Run:

```bash
npm --prefix web test -- src/components/BugCaseArtifacts.test.ts
```

Expected: FAIL because the stage-output card has no stable class or scroll region and artifact layout is still a single column.

- [ ] **Step 3: Wrap all stage outputs in the accessible scroll region**

In `web/src/components/BugCaseArtifacts.vue`, replace only the stage-output section with this structure, preserving the existing ordinary and legacy article bodies verbatim:

```vue
<section class="artifact-card attempt-output-card" aria-labelledby="attempt-output-title">
  <h3 id="attempt-output-title">阶段输出</h3>
  <div class="attempt-output-scroll" role="region" aria-label="阶段输出内容" tabindex="0">
    <p v-if="detail.attempts.length === 0" class="empty-copy">尚无阶段输出</p>
    <article v-for="attempt in detail.attempts.filter(item => item.phase !== 'legacy')" :key="attempt.id" class="artifact-item">
      <strong>{{ attempt.phase }} · {{ attempt.status }}</strong>
      <span v-if="attempt.error_message">{{ attempt.error_message }}</span>
      <pre>{{ displayJSON(attempt.output_json) }}</pre>
    </article>
    <article v-for="projection in legacyProjection" :key="projection.attempt.id" class="artifact-item legacy-attempt">
      <strong>历史运行 · {{ projection.attempt.status }}</strong>
      <div v-if="projection.events.length" class="legacy-events" role="log" aria-label="历史运行事件">
        <p v-for="(event, index) in projection.events" :key="`${event.type}-${index}`"><span>{{ event.type }}</span>{{ event.message }}</p>
      </div>
      <article v-if="projection.finalBlocks.length" class="legacy-final">
        <template v-for="(block, blockIndex) in projection.finalBlocks" :key="blockIndex">
          <h4 v-if="block.kind === 'heading'">
            <template v-for="(token, tokenIndex) in block.tokens" :key="tokenIndex"><strong v-if="token.kind === 'strong'">{{ token.text }}</strong><code v-else-if="token.kind === 'code'">{{ token.text }}</code><template v-else>{{ token.text }}</template></template>
          </h4>
          <ul v-else-if="block.kind === 'list'">
            <li v-for="(item, itemIndex) in block.items" :key="itemIndex"><template v-for="(token, tokenIndex) in item" :key="tokenIndex"><strong v-if="token.kind === 'strong'">{{ token.text }}</strong><code v-else-if="token.kind === 'code'">{{ token.text }}</code><template v-else>{{ token.text }}</template></template></li>
          </ul>
          <pre v-else-if="block.kind === 'code'"><code>{{ block.text }}</code></pre>
          <p v-else><template v-for="(token, tokenIndex) in block.tokens" :key="tokenIndex"><strong v-if="token.kind === 'strong'">{{ token.text }}</strong><code v-else-if="token.kind === 'code'">{{ token.text }}</code><template v-else>{{ token.text }}</template></template></p>
        </template>
      </article>
      <p v-if="projection.attempt.error_message" class="legacy-error">{{ projection.attempt.error_message }}</p>
    </article>
  </div>
</section>
```

- [ ] **Step 4: Add the responsive detail grid and scroll styles**

Replace the `.artifact-sections` rule and add these rules in `web/src/components/BugCaseArtifacts.vue`:

```css
.artifact-sections { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); align-items: start; gap: var(--sp-3); min-width: 0; }
.attempt-output-card { grid-column: 1 / -1; }
.attempt-output-scroll {
  height: clamp(320px, 45vh, 640px);
  min-width: 0;
  padding-right: var(--sp-1);
  overflow-y: auto;
  overflow-x: hidden;
  overscroll-behavior: contain;
  scrollbar-gutter: stable;
}
.attempt-output-scroll:focus-visible { outline: 3px solid rgba(37, 99, 235, .55); outline-offset: 2px; border-radius: var(--r-md); }
@media (max-width: 899px) {
  .artifact-sections { grid-template-columns: minmax(0, 1fr); }
  .attempt-output-card { grid-column: auto; }
  .attempt-output-scroll { height: clamp(280px, 42vh, 480px); }
}
```

Keep all existing card, safe wrapping, legacy Markdown and reset-relation styles unchanged.

- [ ] **Step 5: Run both focused component suites and verify GREEN**

Run:

```bash
npm --prefix web test -- src/components/BugCaseArtifacts.test.ts src/components/BugCaseLifecycle.test.ts
```

Expected: both suites PASS; ordinary/legacy outputs, hostile Markdown, reset links, lifecycle actions and responsive contracts remain covered.

- [ ] **Step 6: Run page and full regression verification**

Run in order:

```bash
npm --prefix web test -- src/pages/IncidentWorkbenchPage.test.ts
npm --prefix web test
npm --prefix web run build
go test ./... -race
make lint
```

Expected:

- IncidentWorkbench integration tests PASS;
- all Vitest suites PASS;
- `vue-tsc --noEmit` and Vite build succeed;
- all Go packages pass with race detection;
- lint reports no Go format/vet or Vue TypeScript errors.

If the Vite build deletes `internal/webui/dist/.gitkeep`, restore it using `apply_patch` with exactly:

```text
# Keep this directory present for //go:embed all:dist in fresh checkouts.
```

Then run:

```bash
git diff --check
git status --short
```

Expected: only the two Task 2 component/test files are uncommitted; `.gitkeep` is present and not shown as deleted.

- [ ] **Step 7: Commit the artifact grid and output viewport**

Run:

```bash
git add -- web/src/components/BugCaseArtifacts.vue web/src/components/BugCaseArtifacts.test.ts
git diff --cached --check
git diff --cached --stat
git commit -m "fix: contain incident stage output scrolling"
git status --short --branch
```

Expected: a second implementation commit containing only the artifact component and its test; the working tree is clean.

## Final Acceptance Check

- [ ] Desktop lifecycle root has two columns, with Cases first and current Case second.
- [ ] Detail region is third in DOM order but spans `grid-column: 1 / -1` below the primary workspace.
- [ ] At 899px and below, lifecycle and artifact grids both stack to one column.
- [ ] Stage-output card spans both desktop artifact columns.
- [ ] Stage title remains outside `.attempt-output-scroll`; all ordinary and legacy output stays inside it.
- [ ] Desktop scroll height is exactly `clamp(320px, 45vh, 640px)`; narrow-screen height is exactly `clamp(280px, 42vh, 480px)`.
- [ ] Mouse, touch and keyboard users can scroll the output region; focus is visible.
- [ ] No Case workflow, bridge, page orchestration, state or content semantics changed.
- [ ] Focused tests, page tests, full Web tests, build, Go race tests and lint all pass.
- [ ] `internal/webui/dist/.gitkeep` is preserved and the working tree is clean.
