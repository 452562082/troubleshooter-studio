<script setup lang="ts">
import { computed } from 'vue'
import type { IncidentCaseDetail, PhaseAttempt } from '../lib/bridge/bugWorkflow'

const props = defineProps<{ detail: IncidentCaseDetail }>()

const investigation = computed(() => [...props.detail.attempts].reverse().find(item => item.phase === 'investigation'))

function displayJSON(value: unknown): string {
  if (value === null || value === undefined || value === '') return '-'
  if (typeof value === 'string') return value
  try { return JSON.stringify(value, null, 2) } catch { return String(value) }
}

function rootCause(attempt?: PhaseAttempt): string {
  if (!attempt) return '尚无根因结论'
  const output = attempt.output_json || {}
  for (const key of ['root_cause', 'summary', 'conclusion', 'report']) {
    const value = output[key]
    if (typeof value === 'string' && value.trim()) return value
  }
  return displayJSON(output)
}
</script>

<template>
  <div class="artifact-sections">
    <section class="artifact-card" aria-labelledby="evidence-title">
      <h3 id="evidence-title">验证证据</h3>
      <p v-if="detail.artifacts.length === 0" class="empty-copy">尚无证据</p>
      <article v-for="artifact in detail.artifacts" :key="artifact.id" class="artifact-item">
        <strong>{{ artifact.kind || '证据' }}</strong>
        <span>{{ artifact.environment || '-' }} · {{ artifact.version || '版本未知' }}</span>
        <code>{{ artifact.path_or_reference }}</code>
        <small v-if="artifact.request_id">request {{ artifact.request_id }}</small>
        <small v-if="artifact.trace_id">trace {{ artifact.trace_id }}</small>
      </article>
    </section>

    <section class="artifact-card" aria-labelledby="cause-title">
      <h3 id="cause-title">根因结论</h3>
      <pre>{{ rootCause(investigation) }}</pre>
    </section>

    <section class="artifact-card" aria-labelledby="attempt-output-title">
      <h3 id="attempt-output-title">阶段输出</h3>
      <p v-if="detail.attempts.length === 0" class="empty-copy">尚无阶段输出</p>
      <article v-for="attempt in detail.attempts" :key="attempt.id" class="artifact-item">
        <strong>{{ attempt.phase }} · {{ attempt.status }}</strong>
        <span v-if="attempt.error_message">{{ attempt.error_message }}</span>
        <pre>{{ displayJSON(attempt.output_json) }}</pre>
      </article>
    </section>

    <section class="artifact-card" aria-labelledby="changes-title">
      <h3 id="changes-title">代码变更与测试</h3>
      <p v-if="detail.code_changes.length === 0" class="empty-copy">尚无代码变更</p>
      <article v-for="change in detail.code_changes" :key="change.id" class="artifact-item">
        <strong>{{ change.repo }}</strong>
        <span>{{ change.fix_branch }} → {{ change.target_environment_branch }}</span>
        <code>{{ change.fix_commit }}</code>
        <pre>{{ displayJSON(change.test_evidence) }}</pre>
      </article>
    </section>

    <section class="artifact-card" aria-labelledby="approval-title">
      <h3 id="approval-title">授权记录</h3>
      <p v-if="detail.approvals.length === 0" class="empty-copy">尚无授权</p>
      <article v-for="approval in detail.approvals" :key="approval.id" class="artifact-item">
        <strong>{{ approval.kind }}</strong>
        <span>{{ approval.actor }} · {{ approval.approved_at }}</span>
      </article>
    </section>

    <section class="artifact-card" aria-labelledby="deployment-title">
      <h3 id="deployment-title">部署观察</h3>
      <p v-if="detail.deployment_observations.length === 0" class="empty-copy">尚无部署观察</p>
      <article v-for="observation in detail.deployment_observations" :key="observation.id" class="artifact-item">
        <strong>{{ observation.result }} · {{ observation.environment }}</strong>
        <span>{{ observation.verification_source || '验证来源未知' }}</span>
        <code>{{ observation.observed_version || '未识别版本' }}</code>
      </article>
    </section>
  </div>
</template>

<style scoped>
.artifact-sections { display: grid; gap: var(--sp-3); min-width: 0; }
.artifact-card { min-width: 0; border: 1px solid var(--c-line); border-radius: var(--r-lg); background: var(--c-surf); padding: var(--sp-3); }
.artifact-card h3 { margin: 0 0 var(--sp-2); color: var(--c-ink); font-size: var(--fs-base); }
.artifact-item { display: grid; gap: 4px; min-width: 0; padding: 9px 0; border-top: 1px solid var(--c-line); color: var(--c-text); font-size: var(--fs-sm); }
.artifact-item:first-of-type { border-top: 0; }
.artifact-item strong { color: var(--c-ink); }
.artifact-item span, .artifact-item small, .empty-copy { color: var(--c-muted); }
code, pre { max-width: 100%; margin: 0; overflow-wrap: anywhere; white-space: pre-wrap; color: var(--c-text); font: inherit; font-size: var(--fs-sm); line-height: 1.55; }
code { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
.empty-copy { margin: 0; font-size: var(--fs-sm); }
</style>
