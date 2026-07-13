# Bug Platform Native Select Visuals Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 Bug 平台配置区的原生下拉框具有统一、明显的自定义箭头和交互状态，同时保留原生键盘、选项弹层与无障碍语义。

**Architecture:** 不修改 Vue 模板和数据流，仅在 `BugInboxPage.vue` 的 scoped CSS 中为 `.platform-config select.form-control` 增加视觉壳层。通过源码 CSS 契约测试锁定作用域、原生外观重置、SVG 箭头、间距、悬停/聚焦/禁用状态及移动端尺寸。

**Tech Stack:** Vue 3 SFC、TypeScript、Vitest、Vue Test Utils、CSS、内联 SVG data URI

## Global Constraints

- 保留原生 `select`、`option`、现有 `label` 关联和浏览器键盘行为。
- 不增加 JavaScript 展开状态、事件处理、自定义浮层、搜索、多选或模板装饰节点。
- 样式必须限定为 `.platform-config select.form-control`，不得影响其他页面或配置区外控件。
- 桌面高度保持 40px；640px 及以下保持 44px。
- 平台类型列保持最小 200px；机器人环境列保持 240–280px。
- 箭头使用 CSS 内联 SVG，文本左内边距 12px、右内边距 40px，箭头尺寸 16px、右侧间距 12px。
- 悬停、聚焦、禁用状态必须可区分，且状态变化不能引发布局位移。
- 不修改 API、状态管理、保存流程、平台数据绑定或其他页面样式。
- 只提交目标 Vue 文件和对应测试，保留工作区中的无关改动。

---

### Task 1: Style the scoped native selects and lock the visual contract

**Files:**
- Modify: `web/src/pages/BugInboxPage.vue:595-612`
- Modify: `web/src/pages/BugInboxPage.vue:668-692`
- Test: `web/src/pages/BugInboxPage.test.ts:135-175`

**Interfaces:**
- Consumes: existing `.platform-config`, `.form-control`, disabled-state and responsive CSS contracts
- Produces: scoped native-select visual contract with no template or runtime interface changes

- [ ] **Step 1: Add failing source-contract assertions for the native select visual shell**

扩展现有 `declares consistent desktop control sizing and responsive select widths` 测试，提取目标选择器并断言完整视觉规则。测试代码应包含以下契约：

```ts
const selectRule = source.match(/\.platform-config select\.form-control \{([^}]*)\}/)?.[1] || ''

expect(selectRule).toContain('appearance: none;')
expect(selectRule).toContain('-webkit-appearance: none;')
expect(selectRule).toContain('padding: 0 40px 0 12px;')
expect(selectRule).toContain('background-image: url("data:image/svg+xml,')
expect(selectRule).toContain('background-repeat: no-repeat;')
expect(selectRule).toContain('background-position: right 12px center;')
expect(selectRule).toContain('background-size: 16px 16px;')
expect(selectRule).toContain('cursor: pointer;')
expect(selectRule).toContain('transition: border-color 180ms ease, box-shadow 180ms ease, background-color 180ms ease;')
expect(source).toContain('.platform-config select.form-control:hover:not(:disabled) { border-color: #93c5fd; }')
expect(source).toContain('.platform-config select.form-control:focus-visible { border-color: var(--c-accent-hover); }')
expect(source).toContain('.platform-config input:disabled, .platform-config select:disabled {')
```

同时增强 reduced-motion 契约，要求目标 select 出现在对应媒体查询的 `transition: none` 规则中：

```ts
const reducedMotionCSS = source.split('@media (prefers-reduced-motion: reduce) {')[1]?.split('\n}')[0] || ''
expect(reducedMotionCSS).toContain('.platform-config select.form-control')
expect(reducedMotionCSS).toContain('transition: none;')
```

- [ ] **Step 2: Run the focused test and capture the expected RED result**

Run:

```bash
npm --prefix web test -- --run src/pages/BugInboxPage.test.ts
```

Expected: FAIL only because the current select rule lacks `appearance`, SVG background, exact padding, interaction states, cursor and transition/reduced-motion contracts。现有挂载、API mock 和其他页面行为测试应继续通过。

- [ ] **Step 3: Implement the scoped native select visual shell**

将当前规则：

```css
.platform-config select.form-control { padding-right: 36px; }
```

替换为等价于以下内容的单一 scoped 规则：

```css
.platform-config select.form-control {
  appearance: none;
  -webkit-appearance: none;
  padding: 0 40px 0 12px;
  background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='24' height='24' viewBox='0 0 24 24' fill='none'%3E%3Cpath d='m7 9 5 5 5-5' stroke='%2364758b' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");
  background-repeat: no-repeat;
  background-position: right 12px center;
  background-size: 16px 16px;
  cursor: pointer;
  transition: border-color 180ms ease, box-shadow 180ms ease, background-color 180ms ease;
}
.platform-config select.form-control:hover:not(:disabled) { border-color: #93c5fd; }
.platform-config select.form-control:focus-visible { border-color: var(--c-accent-hover); }
```

保持现有 `.platform-config input:disabled, .platform-config select:disabled` 规则在其后生效，使禁用下拉框继续使用灰底、灰字和 `not-allowed` 光标。不得增加模板 wrapper 或 SVG 节点。

- [ ] **Step 4: Disable the cosmetic transition for reduced-motion users**

在现有 reduced-motion 媒体查询中加入 scoped select：

```css
@media (prefers-reduced-motion: reduce) {
  .config-disclosure, .compact-button, .toggle-track, .toggle-track > span, .icon-button, .attachment-preview-close, .platform-config select.form-control { transition: none; }
  .refresh-button svg.spinning { animation: none; }
}
```

只关闭过渡，不移除悬停、聚焦、禁用或焦点轮廓状态。

- [ ] **Step 5: Run the focused test and confirm GREEN**

Run:

```bash
npm --prefix web test -- --run src/pages/BugInboxPage.test.ts
```

Expected: `BugInboxPage.test.ts` 全部测试通过，包括新的 native-select 视觉契约和现有桌面 40px、移动端 44px、列宽、断点顺序契约。

- [ ] **Step 6: Run full frontend verification and production build**

Run:

```bash
npm --prefix web test -- --run
npm --prefix web run build
```

Expected: 全部前端测试通过，`vue-tsc --noEmit` 和 Vite production build 成功。若构建清理 `internal/webui/dist/.gitkeep`，使用 `apply_patch` 恢复：

```text
# Keep this directory present for //go:embed all:dist in fresh checkouts.
```

不得提交 `web/dist` 或 `internal/webui/dist` 构建产物。

- [ ] **Step 7: Run lint and whitespace verification**

Run:

```bash
make lint
git diff --check
```

Expected: `go vet`、`gofmt`、`vue-tsc` 和空白检查全部成功。

- [ ] **Step 8: Review scope and responsive safety**

Run:

```bash
git diff -- web/src/pages/BugInboxPage.vue web/src/pages/BugInboxPage.test.ts
git status --short
```

逐项确认：

- 只修改目标 Vue 样式和对应测试。
- 模板、API、保存、登录、同步和机器人环境数据绑定没有变化。
- 所有新增视觉选择器均以 `.platform-config` 开头。
- 原生 `select` 和 `option` 仍存在，未增加自定义浮层。
- 40px/44px、200px、240–280px 和三个响应式断点规则保持不变。
- 自定义 SVG data URI 不包含外部资源请求。

- [ ] **Step 9: Commit the implementation**

```bash
git add web/src/pages/BugInboxPage.vue web/src/pages/BugInboxPage.test.ts
git commit -m "fix: style Bug platform native selects"
```

不要推送，除非用户随后明确要求。
