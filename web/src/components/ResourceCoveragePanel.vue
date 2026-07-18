<script setup lang="ts">
import type { ResourceCoverage, CoverageCell } from '../lib/resourceCoverage'

defineProps<{ coverage: ResourceCoverage }>()

const levelLabel: Record<ResourceCoverage['level'], string> = {
  blocked: '尚未就绪', basic: '基础能力', standard: '标准能力', complete: '完整能力',
}
const cellIcon = (cell: CoverageCell) => ({ ready: '✓', partial: '△', missing: '!', na: '—' })[cell.state]
</script>

<template>
  <section class="coverage-panel">
    <header class="coverage-head">
      <div>
        <h3>部署能力覆盖</h3>
        <p>按环境和服务汇总前面四步的真实可用能力；“部分”表示机器人会使用模糊匹配或降级路径。</p>
      </div>
      <span class="coverage-level" :class="coverage.level">{{ levelLabel[coverage.level] }}</span>
    </header>
    <div class="coverage-stats">✓ {{ coverage.ready }} 就绪　△ {{ coverage.partial }} 部分　! {{ coverage.missing }} 缺失</div>
    <div class="coverage-scroll">
      <table>
        <thead><tr><th>环境 / 资源</th><th>代码</th><th>配置</th><th>数据层</th><th>Workload</th><th>日志</th><th>Trace</th></tr></thead>
        <tbody>
          <tr v-for="row in coverage.rows" :key="`${row.env}:${row.resource}`">
            <th><small>{{ row.env }} · {{ row.kind === 'service' ? '服务' : '工作负载' }}</small>{{ row.resource }}</th>
            <td v-for="key in ['code','config','data','runtime','logs','trace'] as const" :key="key">
              <span class="coverage-cell" :class="row[key].state">{{ cellIcon(row[key]) }} {{ row[key].label }}</span>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </section>
</template>

<style scoped>
.coverage-panel { margin-top: 20px; padding: 16px; border: 1px solid #cbd5e1; border-radius: 12px; background: #fff; }
.coverage-head { display: flex; justify-content: space-between; gap: 16px; align-items: flex-start; }
.coverage-head h3 { margin: 0 0 4px; }
.coverage-head p { margin: 0; color: #64748b; font-size: 13px; }
.coverage-level { padding: 6px 10px; border-radius: 999px; white-space: nowrap; font-weight: 700; }
.coverage-level.blocked { background: #fee2e2; color: #b91c1c; }
.coverage-level.basic { background: #f1f5f9; color: #475569; }
.coverage-level.standard { background: #dbeafe; color: #1d4ed8; }
.coverage-level.complete { background: #dcfce7; color: #15803d; }
.coverage-stats { margin: 12px 0; color: #475569; font-size: 13px; }
.coverage-scroll { overflow-x: auto; }
table { width: 100%; min-width: 1040px; border-collapse: collapse; }
th, td { padding: 10px 8px; border-top: 1px solid #e2e8f0; text-align: left; vertical-align: top; }
thead th { color: #64748b; font-size: 12px; }
tbody th { min-width: 150px; }
tbody th small { display: block; color: #94a3b8; font-weight: 500; }
.coverage-cell { display: inline-block; max-width: 180px; font-size: 12px; line-height: 1.35; }
.coverage-cell.ready { color: #15803d; }
.coverage-cell.partial { color: #a16207; }
.coverage-cell.missing { color: #b91c1c; font-weight: 600; }
.coverage-cell.na { color: #94a3b8; }
</style>
