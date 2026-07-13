# Bug 工单页移除旧版运行记录实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 从 Bug 工单页移除旧版运行记录与机器人上下文，并停止对应旧 runs 请求。

**Architecture:** 只收缩 `BugInboxPage` 的展示和页面状态，不变更 bridge、存储或故障闭环。通过页面测试锁定“无旧版区域、无旧 runs 请求”，其余工单浏览行为保持不变。

**Tech Stack:** Vue 3、TypeScript、Vitest、Vue Test Utils

## Global Constraints

- 后端旧数据、迁移逻辑、bridge 兼容接口保持不变。
- 故障闭环中的 `legacy_archived` Case 展示保持不变。
- 先观察测试因现有旧版区域和请求而失败，再改生产代码。

---

### Task 1: 收缩 Bug 工单页

**Files:**
- Modify: `web/src/pages/BugInboxPage.test.ts`
- Modify: `web/src/pages/BugInboxPage.vue`

**Interfaces:**
- Consumes: `useBugTickets` 提供的工单列表和选择状态。
- Produces: `/bugs` 的纯工单浏览页面；不产生新的公共接口。

- [ ] **Step 1: 写失败测试**

把旧版展示测试替换为以下行为断言：

```ts
it('does not load or render legacy runs and saved bot context', async () => {
  vi.mocked(listBugs).mockResolvedValue([{ ...bug, last_context: '旧机器人上下文' }])
  vi.mocked(listBugInvestigationRuns).mockResolvedValue([{ id: 'run-1', bug_id: bug.id, status: 'succeeded' } as any])

  const wrapper = await mountedInbox()

  expect(listBugInvestigationRuns).not.toHaveBeenCalled()
  expect(wrapper.find('.legacy-history').exists()).toBe(false)
  expect(wrapper.find('.generated-context-panel').exists()).toBe(false)
  expect(wrapper.text()).not.toContain('历史运行记录（只读）')
  expect(wrapper.text()).not.toContain('旧机器人上下文')
})
```

- [ ] **Step 2: 运行测试并确认失败**

Run: `npm --prefix web test -- --run src/pages/BugInboxPage.test.ts`

Expected: FAIL，因为页面仍调用 `listBugInvestigationRuns` 并渲染旧版区域。

- [ ] **Step 3: 写最小实现**

在 `BugInboxPage.vue` 中删除：

- `marked`、`InvestigationEvent`、`InvestigationRun`、`listBugInvestigationRuns` 导入。
- runs 状态、选择监听、旧输出 computed、格式化 helper 和复制函数。
- `.legacy-history`、`.generated-context-panel` 模板与专属样式。

保留平台 Hook URL 的 `copyToClipboard` 和其它工单浏览逻辑。

- [ ] **Step 4: 运行聚焦测试**

Run: `npm --prefix web test -- --run src/pages/BugInboxPage.test.ts`

Expected: PASS，且无 Vue warning 或未处理异常。

- [ ] **Step 5: 运行完整前端验证**

Run: `npm --prefix web test -- --run`

Expected: 全部测试通过。

Run: `npm --prefix web run build`

Expected: Vue 类型检查与 Vite 生产构建成功。

