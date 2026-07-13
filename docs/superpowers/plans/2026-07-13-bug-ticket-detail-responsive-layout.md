# Bug 工单详情响应式布局优化实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修复 Bug 详情中列表序号越界、长内容撑宽和附件操作被文件名挤压的问题，并让标题、字段、附件及底部操作按组件宽度自适应重排。

**Architecture:** 只修改共享展示组件 `BugTicketDetail`，通过语义化附件属性、局部富文本溢出规则和 CSS container query 建立组件级响应式布局。页面数据流、附件预览事件和故障闭环跳转接口保持不变。

**Tech Stack:** Vue 3、TypeScript、scoped CSS、CSS Container Queries、Vitest、Vue Test Utils

## Global Constraints

- 只优化 `BugTicketDetail`，不调整左侧工单列表、刷新按钮、平台配置区或故障闭环业务行为。
- 附件卡片仍是唯一可点击按钮，触控高度至少 44px，并保留文本“预览”。
- 附件完整名称必须同时存在于 `title` 和包含完整文件名的 `aria-label` 中。
- 目标视口固定为 375、768、1024、1440 像素，不得产生页面级横向溢出。
- 保留 Markdown 消毒、附件点击和 `openIncident` 事件的现有接口与行为。

---

### Task 1: 建立 Bug 详情内容与响应式布局契约

**Files:**
- Modify: `web/src/components/BugTicketDetail.test.ts`
- Modify: `web/src/components/BugTicketDetail.vue`

**Interfaces:**
- Consumes: `bug?: BugRecord`、`mode: 'full' | 'summary'` props。
- Produces: 现有 `previewAttachment(index: number)` 与 `openIncident(bugId: string)` emits；不新增公共 TypeScript API。

- [ ] **Step 1: 写富文本溢出与响应式契约的失败测试**

在 `BugTicketDetail.test.ts` 顶部增加源码导入：

```ts
import detailSource from './BugTicketDetail.vue?raw'
```

在现有 describe 中加入：

```ts
it('keeps lists and wide rich content inside the detail card', () => {
  const wrapper = mount(BugTicketDetail, {
    props: {
      bug: {
        ...bug,
        steps: '1. 打开搜索页并输入超长关键字\n2. 对比后端返回结果\n\n```text\nvery-long-line\n```',
      },
      mode: 'full',
    },
  })

  expect(wrapper.get('.ticket-markdown ol').findAll('li')).toHaveLength(2)
  expect(detailSource).toContain('padding-inline-start: 1.75rem')
  expect(detailSource).toContain('.ticket-markdown :deep(table)')
  expect(detailSource).toContain('overflow-x: auto')
})

it('declares component-width responsive layout for supported viewports', () => {
  const wrapper = mount(BugTicketDetail, { props: { bug, mode: 'full' } })

  expect(wrapper.get('.ticket-detail').attributes('data-responsive-viewports')).toBe('375,768,1024,1440')
  expect(wrapper.get('.ticket-detail').attributes('data-overflow-safe')).toBe('true')
  expect(detailSource).toContain('container: ticket-detail / inline-size')
  expect(detailSource).toContain('@container ticket-detail')
})
```

- [ ] **Step 2: 运行测试并确认上述契约失败**

Run: `npm --prefix web test -- --run src/components/BugTicketDetail.test.ts`

Expected: FAIL，因为当前组件没有响应式契约属性、container query、列表缩进和表格局部滚动规则。

- [ ] **Step 3: 写长附件名称的失败测试**

在同一测试文件加入：

```ts
it('keeps a long attachment name accessible without displacing the preview action', () => {
  const longName = 'app_user_search_backend_two_results_with_environment_and_timestamp.png'
  const wrapper = mount(BugTicketDetail, {
    props: { bug: { ...bug, attachments: [{ id: 'long', name: longName, type: 'image/png' }] }, mode: 'full' },
  })
  const attachment = wrapper.get('[data-attachment-index="0"]')

  expect(attachment.attributes('title')).toBe(longName)
  expect(attachment.attributes('aria-label')).toBe(`预览附件：${longName}`)
  expect(attachment.get('.attachment-copy strong').text()).toBe(longName)
  expect(attachment.get('.attachment-action').text()).toBe('预览')
})
```

- [ ] **Step 4: 再次运行测试并确认附件契约失败**

Run: `npm --prefix web test -- --run src/components/BugTicketDetail.test.ts`

Expected: FAIL，因为当前附件按钮没有 `title`、完整 `aria-label` 或 `.attachment-copy` 结构。

- [ ] **Step 5: 实现语义化模板结构**

将根节点和附件按钮调整为：

```vue
<section
  v-if="bug"
  class="ticket-detail"
  :data-mode="mode"
  data-responsive-viewports="375,768,1024,1440"
  data-overflow-safe="true"
  :aria-labelledby="`${detailInstanceID}-title`"
>
```

```vue
<button
  v-for="(attachment, index) in bug.attachments"
  :key="attachmentKey(attachment, index)"
  type="button"
  class="attachment-item"
  :data-attachment-index="index"
  :title="attachment.name"
  :aria-label="`预览附件：${attachment.name}`"
  @click="emit('previewAttachment', index)"
>
  <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 3h8.2L19 7.8V19a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2Zm7.3 1.8V8.7h3.9L13.3 4.8ZM6 4.7a.3.3 0 0 0-.3.3v14c0 .17.13.3.3.3h11a.3.3 0 0 0 .3-.3V10.4h-4.8a.9.9 0 0 1-.9-.9V4.7H6Z" /></svg>
  <span class="attachment-copy"><strong>{{ attachment.name }}</strong><small>{{ attachmentSubtitle(attachment) }}</small></span>
  <span class="attachment-action" aria-hidden="true">预览</span>
</button>
```

- [ ] **Step 6: 实现富文本、附件和 container query 样式**

用以下规则替换或扩展现有 scoped CSS：

```css
.ticket-detail { min-width: 0; container: ticket-detail / inline-size; color: var(--c-text); }
.heading-copy, .detail-tags, .ticket-fields, .summary-fields, .text-block, .attachments-block { min-width: 0; }
.ticket-markdown { box-sizing: border-box; width: 100%; min-width: 0; max-width: 100%; }
.ticket-markdown :deep(ol), .ticket-markdown :deep(ul) {
  margin: .5rem 0;
  padding-inline-start: 1.75rem;
  list-style-position: outside;
}
.ticket-markdown :deep(li) { padding-inline-start: .15rem; overflow-wrap: anywhere; }
.ticket-markdown :deep(p), .ticket-markdown :deep(h1), .ticket-markdown :deep(h2),
.ticket-markdown :deep(h3), .ticket-markdown :deep(h4), .ticket-markdown :deep(h5),
.ticket-markdown :deep(h6), .ticket-markdown :deep(code) { overflow-wrap: anywhere; word-break: break-word; }
.ticket-markdown :deep(pre), .ticket-markdown :deep(table) { display: block; max-width: 100%; overflow-x: auto; }
.ticket-markdown :deep(pre) { white-space: pre; }

.attachment-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
.attachment-copy { min-width: 0; display: grid; gap: 3px; }
.attachment-copy strong {
  display: -webkit-box;
  overflow: hidden;
  color: var(--c-ink);
  font-size: var(--fs-base);
  line-height: 1.35;
  overflow-wrap: anywhere;
  -webkit-box-orient: vertical;
  -webkit-line-clamp: 2;
}
.attachment-action { width: 42px; justify-items: end; white-space: nowrap; }

@container ticket-detail (max-width: 720px) {
  .detail-head { flex-direction: column; }
  .detail-tags { justify-content: flex-start; }
  .attachment-grid { grid-template-columns: minmax(0, 1fr); }
}
@container ticket-detail (max-width: 520px) {
  .ticket-fields, .summary-fields { grid-template-columns: minmax(0, 1fr); }
  .detail-actions, .detail-actions .btn { width: 100%; }
}
```

保留已有的焦点态、hover、44px 按钮高度和 `prefers-reduced-motion` 规则；移除由 container query 取代的 `@media (max-width: 640px)` 详情组件规则。

- [ ] **Step 7: 运行组件测试并确认通过**

Run: `npm --prefix web test -- --run src/components/BugTicketDetail.test.ts`

Expected: PASS，现有 Markdown 消毒、附件点击、空状态和唯一标题 ID 测试也保持通过。

- [ ] **Step 8: 运行页面回归测试**

Run: `npm --prefix web test -- --run src/pages/BugInboxPage.test.ts src/pages/IncidentWorkbenchPage.test.ts`

Expected: PASS，完整模式与摘要模式的页面集成没有回归。

- [ ] **Step 9: 运行完整前端验证**

Run: `npm --prefix web test -- --run`

Expected: 全部 Web 测试通过。

Run: `npm --prefix web run build`

Expected: `vue-tsc --noEmit` 与 Vite 生产构建成功。

- [ ] **Step 10: 提交**

```bash
git add web/src/components/BugTicketDetail.vue web/src/components/BugTicketDetail.test.ts
git commit -m "fix: keep Bug detail content within responsive layout"
```

