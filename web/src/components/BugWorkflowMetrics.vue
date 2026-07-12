<script setup lang="ts">
import { computed } from 'vue'
import type { WorkflowMetrics } from '../lib/bridge'

const props = defineProps<{ metrics: WorkflowMetrics | null }>()

const visible = computed(() => (props.metrics?.completed_cases || 0) >= 5)
const stages = computed(() => [
  ['验证', 'validation'],
  ['排障', 'investigation'],
  ['修复', 'fix'],
  ['人工部署等待', 'deployment_wait'],
  ['回归', 'regression'],
] as const)

function duration(value?: number): string {
  const seconds = Math.max(0, Number(value || 0) / 1_000_000_000)
  if (seconds >= 86400) return `${compact(seconds / 86400)}天`
  if (seconds >= 3600) return `${compact(seconds / 3600)}小时`
  if (seconds >= 60) return `${compact(seconds / 60)}分钟`
  return `${Math.round(seconds)}秒`
}

function compact(value: number): string {
  return Number.isInteger(value) ? String(value) : value.toFixed(1)
}

function percent(value?: number): string {
  return `${Math.round(Math.max(0, Math.min(1, Number(value || 0))) * 100)}%`
}

const blockers = computed(() => [
  ['待补证', props.metrics?.blocker_distribution?.waiting_evidence || 0],
  ['合并冲突', props.metrics?.blocker_distribution?.merge_conflict || 0],
  ['部署未确认', props.metrics?.blocker_distribution?.deployment_unverified || 0],
] as const)
</script>

<template>
  <section v-if="visible" class="workflow-metrics" aria-label="故障闭环指标">
    <div class="metric-summary">
      <strong>闭环概览</strong>
      <span>进行中 {{ metrics?.open_cases || 0 }}</span>
      <span>最长待部署 {{ duration(metrics?.oldest_waiting_deployment_age) }}</span>
      <span>首次回归成功 {{ percent(metrics?.first_regression_success_rate) }}</span>
    </div>
    <dl class="metric-grid">
      <div v-for="([label, key]) in stages" :key="key">
        <dt>{{ label }}</dt>
        <dd>{{ duration(metrics?.median_stage_duration?.[key]) }}</dd>
      </div>
    </dl>
    <div class="metric-blockers" aria-label="阻塞分布">
      <span v-for="([label, count]) in blockers" :key="label">{{ label }} {{ count }}</span>
    </div>
  </section>
</template>

<style scoped>
.workflow-metrics {
  display: grid;
  grid-template-columns: minmax(210px, .8fr) minmax(420px, 2fr) minmax(250px, 1fr);
  gap: 12px;
  align-items: center;
  margin: 0 0 14px;
  padding: 12px 14px;
  border: 1px solid #dbe4ef;
  border-radius: 10px;
  background: #f8fafc;
  color: #334155;
  font-size: 12px;
}
.metric-summary, .metric-blockers { display: flex; flex-wrap: wrap; gap: 5px 12px; }
.metric-summary strong { width: 100%; color: #0f172a; font-size: 13px; }
.metric-grid { display: grid; grid-template-columns: repeat(5, minmax(62px, 1fr)); gap: 8px; margin: 0; }
.metric-grid div { min-width: 0; }
.metric-grid dt { color: #64748b; }
.metric-grid dd { margin: 2px 0 0; color: #0f172a; font-weight: 700; }
.metric-blockers { justify-content: flex-end; }
.metric-blockers span { padding: 3px 7px; border-radius: 999px; background: #fff; border: 1px solid #e2e8f0; white-space: nowrap; }
@media (max-width: 980px) {
  .workflow-metrics { grid-template-columns: 1fr; }
  .metric-blockers { justify-content: flex-start; }
}
@media (max-width: 640px) {
  .metric-grid { grid-template-columns: repeat(2, minmax(100px, 1fr)); }
}
</style>
