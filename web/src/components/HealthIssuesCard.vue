<script setup lang="ts">
// HealthIssuesCard —— EditorPage 验证后的"配置健康检查"面板。
// 自带:按 category 分组、组内按 severity 排序、可折叠展开、severity icon/label 翻译。
//
// 父端只需把 /api/validate 返回的 issues[] 透过来,本组件管展示和交互。

import { computed, ref } from 'vue'

export interface HealthIssue {
  severity: string  // 'error' | 'warn' | 'info'
  category: string  // 'repo' | 'observability' | 'generation' | ...
  field?: string
  message: string
  hint?: string
}

const props = defineProps<{
  issues: HealthIssue[]
}>()

const collapsed = ref<Set<string>>(new Set())

// 按 category 分组,组内 error → warn → info 排,组间也按"组内最严重的优先"排
const issuesByCategory = computed(() => {
  const map: Record<string, HealthIssue[]> = {}
  for (const it of props.issues) {
    if (!map[it.category]) map[it.category] = []
    map[it.category].push(it)
  }
  const order: Record<string, number> = { error: 0, warn: 1, info: 2 }
  for (const k of Object.keys(map)) {
    map[k].sort((a, b) => (order[a.severity] ?? 9) - (order[b.severity] ?? 9))
  }
  const ordered = Object.keys(map).sort((a, b) => {
    const ma = Math.min(...map[a].map(i => order[i.severity] ?? 9))
    const mb = Math.min(...map[b].map(i => order[i.severity] ?? 9))
    if (ma !== mb) return ma - mb
    return a.localeCompare(b)
  })
  return ordered.map(cat => ({ cat, issues: map[cat] }))
})

const categoryLabels: Record<string, string> = {
  repo: '仓库 / 环境关系',
  observability: '可观测性',
  generation: '生成 / 技能',
  config_center: '配置中心',
  data_stores: '数据层',
  messaging: '消息通知',
  env: '环境',
}
function categoryLabel(cat: string): string { return categoryLabels[cat] || cat }

function severityLabel(sev: string): string {
  if (sev === 'error') return '矛盾'
  if (sev === 'warn') return '缺口'
  if (sev === 'info') return '提示'
  return sev
}
function severityIcon(sev: string): string {
  if (sev === 'error') return '✗'
  if (sev === 'warn') return '!'
  return 'ⓘ'
}

function toggleCategory(cat: string) {
  const next = new Set(collapsed.value)
  if (next.has(cat)) next.delete(cat)
  else next.add(cat)
  collapsed.value = next
}
</script>

<template>
  <div v-if="issues.length" class="health-card">
    <div class="health-card-head">
      <span class="health-card-title">📋 配置健康检查 ({{ issues.length }})</span>
      <span class="sub-text">分类展示:✗ 矛盾必修 / ! 缺口建议补 / ⓘ 仅提示</span>
    </div>
    <div
      v-for="g in issuesByCategory"
      :key="g.cat"
      class="health-group"
      :class="'health-group-worst-' + g.issues[0].severity"
    >
      <button
        type="button"
        class="health-group-head"
        @click="toggleCategory(g.cat)"
      >
        <span class="health-group-toggle">{{ collapsed.has(g.cat) ? '▶' : '▼' }}</span>
        <span class="health-group-cat">{{ categoryLabel(g.cat) }}</span>
        <span class="health-group-counts">
          <template v-for="sev in ['error', 'warn', 'info']" :key="sev">
            <span
              v-if="g.issues.some(i => i.severity === sev)"
              class="badge"
              :class="'badge-sev-' + sev"
            >
              {{ severityIcon(sev) }} {{ g.issues.filter(i => i.severity === sev).length }}
            </span>
          </template>
        </span>
      </button>
      <div v-if="!collapsed.has(g.cat)" class="health-group-body">
        <div
          v-for="(it, i) in g.issues"
          :key="g.cat + '-' + i"
          class="health-issue"
          :class="'health-issue-' + it.severity"
        >
          <span class="health-issue-icon">{{ severityIcon(it.severity) }}</span>
          <div class="health-issue-body">
            <div class="health-issue-msg">
              <span class="health-issue-sev-tag">{{ severityLabel(it.severity) }}</span>
              {{ it.message }}
            </div>
            <div v-if="it.field" class="health-issue-field">
              <code>{{ it.field }}</code>
            </div>
            <div v-if="it.hint" class="health-issue-hint">建议:{{ it.hint }}</div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.health-card {
  background: #fff;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  padding: 14px 16px;
  margin-top: 12px;
  margin-bottom: 16px;
}
.health-card-head {
  display: flex; align-items: center; justify-content: space-between;
  margin-bottom: 10px;
  padding-bottom: 8px;
  border-bottom: 1px solid #e2e8f0;
}
.health-card-title {
  font-size: 14px; font-weight: 600; color: #1e293b;
}
.health-group { margin-top: 8px; border-radius: 6px; overflow: hidden; }
.health-group-worst-error { border-left: 3px solid #dc2626; }
.health-group-worst-warn  { border-left: 3px solid #d97706; }
.health-group-worst-info  { border-left: 3px solid #2563eb; }
.health-group-head {
  display: flex; align-items: center; gap: 8px; width: 100%;
  padding: 8px 12px;
  background: #f8fafc;
  border: none; cursor: pointer; text-align: left;
  font-family: inherit; font-size: 13px; color: #1e293b;
}
.health-group-head:hover { background: #f1f5f9; }
.health-group-toggle {
  flex-shrink: 0; width: 12px; text-align: center;
  font-size: 9px; color: #64748b;
}
.health-group-cat { flex: 1 1 auto; font-weight: 600; }
.health-group-counts { display: flex; gap: 4px; flex-shrink: 0; }
.health-group-body { padding: 4px 0; background: #fff; }
.health-issue {
  display: flex; gap: 10px;
  padding: 8px 12px 8px 28px;
  border-top: 1px solid #f1f5f9;
}
.health-issue:first-child { border-top: none; }
.health-issue-icon {
  flex-shrink: 0;
  width: 18px; height: 18px;
  display: inline-flex; align-items: center; justify-content: center;
  border-radius: 50%;
  font-size: 11px; font-weight: 700;
}
.health-issue-error .health-issue-icon { background: #fee2e2; color: #991b1b; }
.health-issue-warn  .health-issue-icon { background: #fef3c7; color: #92400e; }
.health-issue-info  .health-issue-icon { background: #dbeafe; color: #1e40af; }
.health-issue-body { flex: 1 1 auto; min-width: 0; }
.health-issue-msg { font-size: 13px; color: #1e293b; line-height: 1.5; }
.health-issue-sev-tag {
  display: inline-block;
  padding: 1px 6px; margin-right: 6px;
  border-radius: 3px;
  font-size: 11px; font-weight: 600;
}
.health-issue-error .health-issue-sev-tag { background: #fee2e2; color: #991b1b; }
.health-issue-warn  .health-issue-sev-tag { background: #fef3c7; color: #92400e; }
.health-issue-info  .health-issue-sev-tag { background: #dbeafe; color: #1e40af; }
.health-issue-field { margin-top: 3px; font-size: 11.5px; }
.health-issue-field code {
  background: #f1f5f9; color: #334155;
  padding: 1px 5px; border-radius: 3px;
  font-family: 'SF Mono', Menlo, monospace; font-size: 11px;
}
.health-issue-hint { margin-top: 4px; font-size: 12px; color: #64748b; font-style: italic; }
</style>
