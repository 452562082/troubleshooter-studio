<script setup lang="ts">
import { computed } from 'vue'
import type { IncidentCaseDetail, PhaseAttempt } from '../lib/bridge/bugWorkflow'
import BugStageAttemptOutput from './BugStageAttemptOutput.vue'

const props = defineProps<{ detail: IncidentCaseDetail }>()
const disputedIDs = computed(() => new Set(props.detail.events
  .filter(event => event.event_type === 'root_cause_disputed')
  .map(event => event.payload_json?.source_root_cause_attempt_id)))
function safeAttempt(attempt: PhaseAttempt): PhaseAttempt {
  const code = attempt.error_code || (typeof attempt.output_json?.error_code === 'string' ? attempt.output_json.error_code : '')
  return code === 'validator_not_installed' || code.startsWith('browser_')
    ? { ...attempt, error_message: '', output_json: { error_code: code } }
    : attempt
}
function testRecords(value: unknown): string[] {
  if (!Array.isArray(value)) return typeof value === 'string' && value.trim() ? [value] : []
  return value.flatMap(item => {
    if (typeof item === 'string') return item.trim() ? [item] : []
    if (!item || typeof item !== 'object') return []
    return [item.command, item.result, item.note].filter(value => typeof value === 'string' && value.trim()).join(' · ') || []
  })
}
const hasResult = (attempt: PhaseAttempt) => Boolean(attempt.error_message || Object.keys(attempt.output_json || {}).length)
// A new investigation invalidates the previous recommendation as a current result.
// Older and disputed attempts stay available in the collapsed history.
const reports = computed(() => {
  const cycle = props.detail.attempts.filter(attempt => attempt.cycle_number === props.detail.case.cycle_number)
  const root = [...cycle].reverse().find(attempt => attempt.phase === 'investigation')
  const rootIndex = root ? cycle.indexOf(root) : -1
  const fix = cycle.slice(rootIndex + 1).reverse().find(attempt => attempt.phase === 'fix')
  return [root, fix].filter((attempt): attempt is PhaseAttempt => Boolean(attempt && hasResult(attempt) && !disputedIDs.value.has(attempt.id)))
})
const history = computed(() => props.detail.attempts.filter(attempt => hasResult(attempt) && !reports.value.some(report => report.id === attempt.id)))
const changes = computed(() => props.detail.code_changes.filter(change => change.attempt_id === props.detail.case.current_attempt_id))
function approvalLabel(kind: string): string {
  return ({ start_fix: '修复授权', complete_remediation: '人工处置确认', merge_environment_branch: '提交授权' } as Record<string, string>)[kind] || '操作授权'
}
function pushStatusLabel(status: string): string {
  return ({ pushed: '已推送', failed: '推送失败', conflict: '合并冲突', merge_local: '已合并，待推送', push_unknown: '推送结果待确认', pending: '待推送' } as Record<string, string>)[status] || '待推送'
}
</script>

<template>
  <div class="case-results">
    <section v-if="reports.length" class="current-reports" aria-label="处理结果">
      <BugStageAttemptOutput v-for="report in reports" :key="report.id" :attempt="report" :latest="true" />
    </section>
    <section v-if="changes.length" class="submission-card" aria-labelledby="submission-title">
      <header><h3 id="submission-title">提交记录</h3><span>{{ changes.length }} 个仓库</span></header>
      <article v-for="change in changes" :key="change.id" class="submission-repo">
        <strong>{{ change.repo }}</strong>
        <span class="branch-path">{{ change.fix_branch }} <span aria-hidden="true">→</span> {{ change.target_environment_branch }}</span>
        <dl>
          <div><dt>修复提交</dt><dd><code>{{ change.fix_commit }}</code></dd></div>
          <div v-if="change.merge_commit"><dt>合并提交</dt><dd><code>{{ change.merge_commit }}</code></dd></div>
          <div><dt>推送状态</dt><dd>{{ pushStatusLabel(change.push_status) }}</dd></div>
        </dl>
        <details v-if="testRecords(change.test_evidence).length" class="test-record"><summary>工程测试记录</summary><p v-for="(test, index) in testRecords(change.test_evidence)" :key="index">{{ test }}</p></details>
      </article>
    </section>
    <details v-if="history.length || detail.approvals.length" class="result-history">
      <summary>历史结果与授权 <span>{{ history.length + detail.approvals.length }}</span></summary>
      <div class="history-content">
        <div v-for="attempt in history" :key="attempt.id">
          <p v-if="disputedIDs.has(attempt.id)" class="disputed-notice">此结论已被质疑，仅供历史参考。</p>
          <BugStageAttemptOutput :attempt="safeAttempt(attempt)" :latest="false" />
        </div>
        <ol v-if="detail.approvals.length" class="approval-list" aria-label="授权记录">
          <li v-for="approval in detail.approvals" :key="approval.id"><strong>{{ approvalLabel(approval.kind) }}</strong><span>{{ approval.actor }}</span><time>{{ new Date(approval.approved_at).toLocaleString('zh-CN', { hour12: false }) }}</time></li>
        </ol>
      </div>
    </details>
  </div>
</template>

<style scoped>
.case-results, .current-reports, .history-content { min-width: 0; display: grid; gap: 16px; }
.case-results:empty { display: none; }
.submission-card { min-width: 0; padding: 16px; border: 1px solid var(--c-line); border-radius: 12px; background: var(--c-surf); }
.submission-card header { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
h3 { margin: 0; font-size: 14px; color: var(--c-ink); }
header > span, .branch-path, dt, time { color: var(--c-muted); font-size: 12px; }
.submission-repo { display: grid; gap: 10px; padding-top: 16px; overflow-wrap: anywhere; }
.submission-repo + .submission-repo { margin-top: 16px; border-top: 1px solid var(--c-line); }
.submission-repo > strong { font-size: 14px; }
dl { display: grid; gap: 8px; margin: 0; }
dl > div { display: grid; grid-template-columns: 72px minmax(0, 1fr); gap: 12px; }
dd { min-width: 0; margin: 0; font-size: 13px; }
code { font-size: 12px; }
.result-history { border-top: 1px solid var(--c-line); }
summary { min-height: 44px; padding: 10px 0; box-sizing: border-box; cursor: pointer; color: var(--c-muted); font-size: 13px; }
summary span { margin-left: 8px; padding: 2px 7px; border-radius: 6px; background: var(--c-surf-2); font-size: 11px; }
summary:focus-visible { outline: 2px solid var(--c-accent); outline-offset: 2px; }
.test-record p { margin: 6px 0; font: 12px/1.6 ui-monospace, monospace; white-space: pre-wrap; overflow-wrap: anywhere; }
.disputed-notice { padding: 10px 12px; margin: 0 0 8px; border-radius: 8px; background: #fffbeb; color: #92400e; font-size: 13px; }
.approval-list { margin: 0; padding: 0; list-style: none; }
.approval-list li { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; padding: 10px 0; font-size: 12px; }
.approval-list time { margin-left: auto; }

.submission-card { background: #fbfdfc; border-color: #d8e8dd; }
.submission-card header { padding-bottom: 12px; border-bottom: 1px solid #e3eee7; }
.branch-path { padding: 7px 10px; border-radius: 6px; background: #f1f5f9; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; line-height: 1.7; }
.test-record { padding: 10px 12px; border-radius: 8px; background: #f8fafc; border: 1px solid var(--c-line); }
.result-history { border: 1px solid var(--c-line); border-radius: 8px; background: #f8fafc; }
.result-history > summary { padding: 10px 12px; font-size: 12px; }
.history-content { padding: 0 12px 12px; }
.approval-list li + li { border-top: 1px solid var(--c-line); }
</style>
