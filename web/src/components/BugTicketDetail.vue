<script lang="ts">
let bugTicketDetailSequence = 0
</script>

<script setup lang="ts">
import { marked } from 'marked'
import { computed } from 'vue'
import type { BugAttachment, BugRecord } from '../lib/bridge/bugs'

const props = defineProps<{ bug?: BugRecord; mode: 'full' | 'summary' }>()
const emit = defineEmits<{ previewAttachment: [index: number]; openIncident: [bugId: string] }>()
const detailInstanceID = `bug-ticket-detail-${++bugTicketDetailSequence}`

const stepsHTML = computed(() => safeMarkdown(props.bug?.steps || '-'))
const descriptionHTML = computed(() => safeMarkdown(props.bug?.description || ''))

function sourceLabel(bug: BugRecord): string {
  if (bug.source === 'zentao') return `禅道 #${bug.source_id || '-'}`
  return [bug.source || '未知来源', bug.source_id].filter(Boolean).join(' #')
}

function formatTime(value?: string): string {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, '0')}-${String(date.getDate()).padStart(2, '0')} ${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`
}

function attachmentSubtitle(attachment: BugAttachment): string {
  return attachment.type || attachment.local_path || attachment.remote_url || '附件'
}

function attachmentKey(attachment: BugAttachment, index: number): string {
  const stableIdentity = attachment.id || attachment.local_path || attachment.remote_url
  const identity = stableIdentity || `${attachment.name}:${attachment.type || 'attachment'}:${index}`
  return `${props.bug?.id || 'bug'}:${identity}`
}

const allowedTags = new Set([
  'A', 'BLOCKQUOTE', 'BR', 'CODE', 'DEL', 'EM', 'H1', 'H2', 'H3', 'H4', 'H5', 'H6',
  'HR', 'LI', 'OL', 'P', 'PRE', 'STRONG', 'TABLE', 'TBODY', 'TD', 'TH', 'THEAD', 'TR', 'UL',
])

function safeMarkdown(text: string): string {
  const parsed = marked.parse(text || '', { async: false }) as string
  const document = new DOMParser().parseFromString(parsed, 'text/html')
  for (const element of Array.from(document.body.querySelectorAll('*'))) {
    if (!allowedTags.has(element.tagName)) {
      element.replaceWith(document.createTextNode(element.textContent || ''))
      continue
    }
    for (const attribute of Array.from(element.attributes)) {
      const keepLinkAttribute = element.tagName === 'A' && ['href', 'title'].includes(attribute.name.toLowerCase())
      if (!keepLinkAttribute) element.removeAttribute(attribute.name)
    }
    if (element.tagName === 'A') {
      const href = element.getAttribute('href')
      if (!href || !safeLink(href)) {
        element.replaceWith(document.createTextNode(element.textContent || href || ''))
        continue
      }
      element.setAttribute('rel', 'noopener noreferrer')
    }
  }
  return document.body.innerHTML
}

function safeLink(href: string): boolean {
  const normalized = href.trim().replace(/[\u0000-\u001f\u007f\s]+/g, '')
  if (normalized.startsWith('#') || normalized.startsWith('/') || normalized.startsWith('./') || normalized.startsWith('../')) return true
  try {
    return ['http:', 'https:', 'mailto:'].includes(new URL(normalized, 'https://studio.invalid').protocol)
  } catch {
    return false
  }
}
</script>

<template>
  <section
    v-if="bug"
    class="ticket-detail"
    :data-mode="mode"
    data-responsive-viewports="375,768,1024,1440"
    data-overflow-safe="true"
    :aria-labelledby="`${detailInstanceID}-title`"
  >
    <header class="detail-head">
      <div class="heading-copy">
        <div class="detail-source">{{ sourceLabel(bug) }}</div>
        <h2 :id="`${detailInstanceID}-title`">{{ bug.title }}</h2>
      </div>
      <div class="detail-tags" aria-label="工单状态">
        <span v-if="bug.status">{{ bug.status }}</span>
        <span v-if="bug.priority">P{{ bug.priority }}</span>
        <span v-if="bug.env || bug.bot_env">{{ bug.env || bug.bot_env }}</span>
      </div>
    </header>

    <dl v-if="mode === 'summary'" class="summary-fields">
      <div><dt>系统</dt><dd>{{ bug.system_id || '-' }}</dd></div>
      <div><dt>环境</dt><dd>{{ bug.env || bug.bot_env || '-' }}</dd></div>
      <div><dt>服务</dt><dd>{{ (bug.service_hints || []).join('、') || '-' }}</dd></div>
    </dl>

    <template v-else>
      <dl class="ticket-fields">
        <div><dt>所属产品</dt><dd>{{ bug.product || '-' }}</dd></div>
        <div><dt>所属模块</dt><dd>{{ bug.module || '-' }}</dd></div>
        <div><dt>Bug 类型</dt><dd>{{ bug.bug_type || '-' }}</dd></div>
        <div><dt>严重程度</dt><dd>{{ bug.severity ? `S${bug.severity}` : '-' }}</dd></div>
        <div><dt>优先级</dt><dd>{{ bug.priority ? `P${bug.priority}` : '-' }}</dd></div>
        <div><dt>指派</dt><dd>{{ bug.assignee || '-' }}</dd></div>
        <div><dt>提交</dt><dd>{{ bug.reporter || '-' }}</dd></div>
        <div><dt>创建时间</dt><dd>{{ formatTime(bug.created_at) }}</dd></div>
        <div><dt>更新时间</dt><dd>{{ formatTime(bug.updated_at) }}</dd></div>
        <div><dt>操作系统</dt><dd>{{ bug.os || '-' }}</dd></div>
        <div><dt>浏览器</dt><dd>{{ bug.browser || '-' }}</dd></div>
        <div><dt>关键词</dt><dd>{{ bug.keywords || '-' }}</dd></div>
      </dl>

      <section class="text-block" :aria-labelledby="`${detailInstanceID}-steps`">
        <h3 :id="`${detailInstanceID}-steps`">复现步骤</h3>
        <article class="ticket-markdown" v-html="stepsHTML"></article>
      </section>
      <section v-if="bug.description" class="text-block" :aria-labelledby="`${detailInstanceID}-description`">
        <h3 :id="`${detailInstanceID}-description`">描述</h3>
        <article class="ticket-markdown" v-html="descriptionHTML"></article>
      </section>

      <section v-if="bug.attachments?.length" class="attachments-block" :aria-labelledby="`${detailInstanceID}-attachments`">
        <h3 :id="`${detailInstanceID}-attachments`">附件</h3>
        <div class="attachment-grid">
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
        </div>
      </section>

      <div class="detail-actions">
        <button class="btn primary" type="button" data-action="open-incident" @click="emit('openIncident', bug.id)">进入故障闭环</button>
      </div>
    </template>
  </section>
  <p v-else class="detail-empty" role="status">选择一条 Bug 查看详情</p>
</template>

<style scoped>
.ticket-detail { min-width: 0; container: ticket-detail / inline-size; color: var(--c-text); }
.detail-head { min-width: 0; margin-bottom: 14px; display: flex; align-items: flex-start; justify-content: space-between; gap: var(--sp-3); }
.heading-copy, .detail-tags, .ticket-fields, .summary-fields, .text-block, .attachments-block { min-width: 0; }
.detail-source { margin-bottom: var(--sp-1); color: var(--c-accent-hover); font-size: var(--fs-sm); font-weight: 700; overflow-wrap: anywhere; }
.detail-head h2 { margin: 0; color: var(--c-ink); font-size: 20px; line-height: 1.35; overflow-wrap: anywhere; }
.detail-tags { display: flex; flex: 0 1 auto; flex-wrap: wrap; justify-content: flex-end; gap: 6px; }
.detail-tags span { padding: 2px 8px; border: 1px solid #c7d2fe; border-radius: 999px; background: #eef2ff; color: #3730a3; font-size: var(--fs-xs); overflow-wrap: anywhere; }
.ticket-fields, .summary-fields { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--sp-2); margin: 0 0 14px; }
.summary-fields { grid-template-columns: repeat(3, minmax(0, 1fr)); }
.ticket-fields div, .summary-fields div { min-width: 0; padding: var(--sp-2); border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf-2); }
dt { margin-bottom: 3px; color: var(--c-muted); font-size: var(--fs-xs); }
dd { margin: 0; color: var(--c-ink); font-size: var(--fs-base); overflow-wrap: anywhere; }
.text-block, .attachments-block { margin-top: var(--sp-3); }
.text-block h3, .attachments-block h3 { margin: 0 0 6px; color: var(--c-ink); font-size: var(--fs-base); }
.ticket-markdown { box-sizing: border-box; width: 100%; min-width: 0; max-width: 100%; padding: 10px; overflow-wrap: anywhere; border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf-2); color: var(--c-text); font-size: var(--fs-base); line-height: 1.6; }
.ticket-markdown :deep(:first-child) { margin-top: 0; }
.ticket-markdown :deep(:last-child) { margin-bottom: 0; }
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
.ticket-markdown :deep(a) { color: #1d4ed8; overflow-wrap: anywhere; }
.attachment-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--sp-2); min-width: 0; }
.attachment-item {
  width: 100%; min-width: 0; min-height: 56px; padding: var(--sp-2) 10px;
  display: grid; grid-template-columns: 32px minmax(0, 1fr) auto; align-items: center; gap: 10px;
  border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf);
  color: var(--c-text); text-align: left; font-family: inherit; cursor: pointer;
  transition: border-color .18s ease, background .18s ease;
}
.attachment-item:hover { border-color: var(--c-accent); background: #eff6ff; }
.attachment-item:focus-visible, .detail-actions .btn:focus-visible { outline: 2px solid var(--c-accent-hover); outline-offset: 2px; }
.attachment-item svg { width: 28px; height: 28px; fill: var(--c-muted); }
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
.attachment-copy small { color: var(--c-muted); font-size: var(--fs-xs); overflow-wrap: anywhere; }
.attachment-action { width: 42px; display: grid; justify-items: end; color: #1d4ed8; font-size: var(--fs-xs); font-weight: 700; white-space: nowrap; }
.detail-actions { margin-top: var(--sp-4); display: flex; justify-content: flex-end; }
.detail-actions .btn { min-height: 44px; }
.detail-empty { min-height: 160px; margin: 0; display: grid; place-items: center; color: var(--c-muted); font-size: var(--fs-sm); }
@container ticket-detail (max-width: 720px) {
  .detail-head { flex-direction: column; }
  .detail-tags { justify-content: flex-start; }
  .attachment-grid { grid-template-columns: minmax(0, 1fr); }
}
@container ticket-detail (max-width: 520px) {
  .ticket-fields, .summary-fields { grid-template-columns: minmax(0, 1fr); }
  .detail-actions, .detail-actions .btn { width: 100%; }
}
@media (prefers-reduced-motion: reduce) { .attachment-item { transition: none; } }
</style>
