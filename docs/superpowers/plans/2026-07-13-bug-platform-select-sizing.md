# Bug Platform Select Sizing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 统一 Bug 平台配置区桌面控件尺寸，扩大平台类型和机器人环境下拉框，并在 1024px 等中等宽度下保持清晰、无横向溢出的响应式布局。

**Architecture:** 保留现有 Vue 模板、原生 `select`、状态和事件流，仅调整 `BugInboxPage.vue` 的局部 CSS。使用源码契约测试锁定桌面 40px 控件高度、下拉框最小宽度、1200px/900px 两级降列规则和现有 640px 移动端 44px 触控尺寸。

**Tech Stack:** Vue 3 SFC、TypeScript、Vitest、Vue Test Utils、CSS Grid

## Global Constraints

- 不修改 Bug 平台配置 API、保存逻辑、登录状态或同步行为。
- 不替换原生 `select`，保持键盘操作、焦点和浏览器无障碍语义。
- 桌面平台配置控件统一为 40px；移动端继续使用 44px 触控尺寸。
- 大于 1200px 使用三列基本信息布局；901–1200px 使用两列且平台地址独占一行；900px 及以下使用单列。
- 平台类型列最小宽度为 200px；机器人环境列保持在 240–280px；删除按钮维持独立 40px 列。
- 只提交本任务文件，保留工作区中任何无关改动。

---

## Task 1: Lock the sizing and responsive contracts with a failing test

**Files:**
- Modify: `web/src/pages/BugInboxPage.test.ts`
- Reference: `web/src/pages/BugInboxPage.vue:576-683`

- [ ] **Step 1: Add a source-contract test for desktop control sizing**

在现有移动端 CSS 契约测试旁新增测试 `declares consistent desktop control sizing and responsive select widths`，读取 `BugInboxPage.vue` 源码并断言：

```ts
it('declares consistent desktop control sizing and responsive select widths', () => {
  const source = readFileSync('src/pages/BugInboxPage.vue', 'utf8')
  const mediumCSS = source.split('@media (max-width: 1200px) {')[1]?.split('\n}')[0] || ''

  expect(source).toContain('.platform-config { --config-control-height: 40px;')
  expect(source).toContain('.platform-config .form-control { min-height: var(--config-control-height); }')
  expect(source).toContain('.platform-config select.form-control { padding-right: 36px; }')
  expect(source).toContain('.basic-row { grid-template-columns: minmax(220px, 1fr) minmax(200px, .6fr) minmax(320px, 1.35fr); }')
  expect(source).toContain('grid-template-columns: minmax(0, 1fr) minmax(240px, 280px) 40px;')
  expect(mediumCSS).toContain('.basic-row { grid-template-columns: minmax(0, 1fr) minmax(200px, .65fr); }')
  expect(mediumCSS).toContain('.basic-row .field-label:last-child { grid-column: 1 / -1; }')
})
```

如最终选择用组合选择器统一高度，测试应断言等价的明确 CSS 契约，而不是依赖浏览器计算样式。

- [ ] **Step 2: Run the focused test and confirm it fails for the intended missing contracts**

Run:

```bash
npm --prefix web test -- --run src/pages/BugInboxPage.test.ts
```

Expected: FAIL；错误应指向缺少 40px 变量、200px/240–280px 宽度或 1200px 断点，不应出现挂载、API mock 等无关错误。

- [ ] **Step 3: Keep the red test uncommitted until the implementation is green**

默认不单独提交失败测试；在同一任务完成绿色实现后统一提交。

## Task 2: Implement the consistent sizing and responsive grid

**Files:**
- Modify: `web/src/pages/BugInboxPage.vue:576-683`
- Test: `web/src/pages/BugInboxPage.test.ts`

- [ ] **Step 1: Introduce the scoped desktop control-height token**

在 `.platform-config` 上增加局部变量，并将配置区表单、紧凑按钮、开关和间隔输入的桌面高度统一到 40px：

```css
.platform-config { --config-control-height: 40px; min-width: 0; ... }
.platform-config .form-control { min-height: var(--config-control-height); }
.compact-button { min-height: var(--config-control-height); ... }
.toggle-control { min-height: var(--config-control-height); ... }
.interval-control { min-height: var(--config-control-height); ... }
.interval-control input { width: 72px; min-height: var(--config-control-height); ... }
```

删除或替换这些组件原有的 36px 声明，避免同一区域继续混用高度。若组件可能脱离 `.platform-config` 使用，则给变量增加安全回退值，例如 `var(--config-control-height, 40px)`。

- [ ] **Step 2: Give native selects readable internal spacing and desktop widths**

增加原生下拉箭头右侧空间，并调整两类网格：

```css
.platform-config select.form-control { padding-right: 36px; }
.basic-row { grid-template-columns: minmax(220px, 1fr) minmax(200px, .6fr) minmax(320px, 1.35fr); }
.bot-config-row { grid-template-columns: minmax(0, 1fr) minmax(240px, 280px) 40px; ... }
```

保留 `.form-control { width: 100%; min-width: 0; }` 和机器人主信息列的 `minmax(0, 1fr)`，确保长路径可以换行而不是撑宽页面。

- [ ] **Step 3: Add a medium-width two-column breakpoint before the existing 900px rule**

```css
@media (max-width: 1200px) {
  .basic-row { grid-template-columns: minmax(0, 1fr) minmax(200px, .65fr); }
  .basic-row .field-label:last-child { grid-column: 1 / -1; }
}
```

保留现有 `@media (max-width: 900px)` 单列规则；其后声明会覆盖 1200px 的两列规则。确认地址字段在 901–1200px 独占整行，在 900px 以下自然回到单列。

- [ ] **Step 4: Preserve the existing mobile 44px contracts**

不要删除或弱化 `@media (max-width: 640px)` 中以下规则：

```css
.platform-config .form-control, .compact-button, .danger-link, .toggle-control { min-height: 44px; }
.bot-config-row .icon-button { justify-self: end; width: 44px; height: 44px; }
```

如桌面变量选择器提高了优先级，需保证移动端规则仍能覆盖到 44px。

- [ ] **Step 5: Run the focused test until green**

Run:

```bash
npm --prefix web test -- --run src/pages/BugInboxPage.test.ts
```

Expected: PASS，包含新增桌面契约测试和原有移动端 44px 测试。

- [ ] **Step 6: Run the full frontend verification**

Run:

```bash
npm --prefix web test -- --run
npm --prefix web run build
```

Expected: 全部前端测试通过，生产构建成功。若构建清理了 `internal/webui/dist/.gitkeep`，用 `apply_patch` 恢复其原内容，不提交构建产物。

- [ ] **Step 7: Run repository lint and whitespace checks**

Run:

```bash
make lint
git diff --check
```

Expected: 两条命令均成功，无格式和尾随空白问题。

- [ ] **Step 8: Review the scoped diff**

Run:

```bash
git diff -- web/src/pages/BugInboxPage.vue web/src/pages/BugInboxPage.test.ts
git status --short
```

确认：

- 只有目标 Vue 样式和对应测试被修改。
- 没有模板、请求、保存和同步逻辑变化。
- 1200px 规则位于 900px 规则之前。
- 不存在遗留的配置区 36px 高度声明。
- 移动端 44px 规则仍在。

- [ ] **Step 9: Commit the implementation**

```bash
git add web/src/pages/BugInboxPage.vue web/src/pages/BugInboxPage.test.ts
git commit -m "fix: unify Bug platform control sizing"
```

不要推送，除非用户随后明确要求。
