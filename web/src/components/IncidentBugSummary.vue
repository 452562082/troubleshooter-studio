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
  return normalized.toUpperCase().startsWith(prefix) ? `${prefix}${normalized.slice(1)}` : `${prefix}${normalized}`
}

function bugGrade(bug?: BugRecord): string {
  if (!bug) return '-'
  return [gradePart('S', bug.severity), gradePart('P', bug.priority)].filter(Boolean).join(' · ') || '-'
}

function isValidHTMLDateTime(value: string, parsed: Date): boolean {
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{1,3}))?)?(Z|[+-]\d{2}:\d{2})?$/.exec(value)
  if (!match) return false

  const year = Number(match[1])
  const month = Number(match[2])
  const day = Number(match[3])
  const hour = Number(match[4])
  const minute = Number(match[5])
  const second = Number(match[6] || 0)
  const millisecond = Number((match[7] || '').padEnd(3, '0') || 0)
  const zone = match[8]
  const leapYear = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  const daysInMonth = [31, leapYear ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]

  if (year === 0 || month < 1 || month > 12 || day < 1 || day > daysInMonth[month - 1]) return false
  if (hour > 23 || minute > 59 || second > 59) return false

  if (!zone) {
    return parsed.getFullYear() === year
      && parsed.getMonth() === month - 1
      && parsed.getDate() === day
      && parsed.getHours() === hour
      && parsed.getMinutes() === minute
      && parsed.getSeconds() === second
      && parsed.getMilliseconds() === millisecond
  }

  let offsetMinutes = 0
  if (zone !== 'Z') {
    const offsetHour = Number(zone.slice(1, 3))
    const offsetMinute = Number(zone.slice(4, 6))
    if (offsetHour > 23 || offsetMinute > 59) return false
    offsetMinutes = (offsetHour * 60 + offsetMinute) * (zone.startsWith('+') ? 1 : -1)
  }

  const expected = new Date(0)
  expected.setUTCFullYear(year, month - 1, day)
  expected.setUTCHours(hour, minute, second, millisecond)
  return parsed.getTime() === expected.getTime() - offsetMinutes * 60_000
}

function formatTime(value?: string): DisplayTime {
  if (!value) return { text: '-' }
  const date = new Date(value)
  if (Number.isNaN(date.getTime()) || !isValidHTMLDateTime(value, date)) return { text: value }
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
