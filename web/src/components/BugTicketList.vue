<script lang="ts">
let bugTicketListSequence = 0
</script>

<script setup lang="ts">
import type { BugRecord } from '../lib/bridge/bugs'

defineProps<{ bugs: BugRecord[]; selectedId: string; loading?: boolean; query: string }>()
const emit = defineEmits<{ select: [id: string]; 'update:query': [value: string] }>()

const listInstanceID = `bug-ticket-list-${++bugTicketListSequence}`
const searchID = `${listInstanceID}-search`

function sourceLabel(bug: BugRecord): string {
  if (bug.source === 'zentao') return `禅道 #${bug.source_id || '-'}`
  return [bug.source || '未知来源', bug.source_id].filter(Boolean).join(' #')
}

function inputValue(event: Event): string {
  return (event.target as HTMLInputElement).value
}
</script>

<template>
  <section class="ticket-list" :aria-labelledby="`${listInstanceID}-title`">
    <header class="list-heading">
      <strong :id="`${listInstanceID}-title`">Bug 收件箱</strong>
      <span>{{ bugs.length }} 条</span>
    </header>

    <div class="search-field">
      <label :for="searchID">搜索 Bug 工单</label>
      <input
        :id="searchID"
        type="search"
        :value="query"
        placeholder="搜索标题、来源、系统、环境、服务"
        @input="emit('update:query', inputValue($event))"
      >
    </div>

    <p v-if="loading" class="empty-state" role="status" aria-live="polite">加载中...</p>
    <p v-else-if="bugs.length === 0" class="empty-state" role="status">暂无同步到的 Bug</p>
    <div v-else class="ticket-rows">
      <button
        v-for="bug in bugs"
        :key="bug.id"
        type="button"
        class="ticket-row"
        :class="{ selected: selectedId === bug.id }"
        :aria-pressed="selectedId === bug.id"
        :data-ticket-id="bug.id"
        @click="emit('select', bug.id)"
      >
        <span class="ticket-title">{{ bug.title }}</span>
        <span class="ticket-meta">
          <span>{{ sourceLabel(bug) }}</span>
          <span v-if="bug.env || bug.bot_env">{{ bug.env || bug.bot_env }}</span>
          <span v-if="bug.severity">S{{ bug.severity }}</span>
        </span>
        <span v-if="selectedId === bug.id" class="selection-copy">已选择</span>
      </button>
    </div>
  </section>
</template>

<style scoped>
.ticket-list { min-width: 0; display: flex; flex-direction: column; gap: var(--sp-2); color: var(--c-text); }
.list-heading { display: flex; align-items: baseline; justify-content: space-between; gap: var(--sp-2); }
.list-heading strong { color: var(--c-ink); font-size: var(--fs-md); }
.list-heading span, .search-field label { color: var(--c-muted); font-size: var(--fs-xs); }
.search-field { display: grid; gap: var(--sp-1); min-width: 0; }
.search-field label { font-weight: 600; }
.ticket-rows { display: grid; gap: var(--sp-2); min-width: 0; }
.ticket-row {
  position: relative; width: 100%; min-width: 0; min-height: 44px; padding: 10px;
  display: flex; flex-direction: column; gap: 6px; text-align: left;
  border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf);
  color: var(--c-text); font-family: inherit; cursor: pointer;
  transition: background .16s ease, border-color .16s ease;
}
.ticket-row:hover { border-color: var(--c-line-2); background: var(--c-surf-2); }
.ticket-row:focus-visible { outline: 2px solid var(--c-accent-hover); outline-offset: 2px; }
.ticket-row.selected { border-color: var(--c-accent); background: #eff6ff; }
.ticket-title { padding-right: 54px; overflow-wrap: anywhere; color: var(--c-ink); font-size: var(--fs-base); font-weight: 600; line-height: 1.45; }
.ticket-meta { display: flex; flex-wrap: wrap; gap: 5px; color: var(--c-muted); font-size: var(--fs-xs); }
.ticket-meta span { max-width: 100%; padding: 1px 6px; overflow-wrap: anywhere; border-radius: 999px; background: var(--c-surf-3); }
.selection-copy { position: absolute; top: 10px; right: 10px; color: #1d4ed8; font-size: var(--fs-xs); font-weight: 700; }
.empty-state { min-height: 44px; margin: 0; display: grid; place-items: center; color: var(--c-muted); font-size: var(--fs-sm); text-align: center; }
@media (prefers-reduced-motion: reduce) { .ticket-row { transition: none; } }
</style>
