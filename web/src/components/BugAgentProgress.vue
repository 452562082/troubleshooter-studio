<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { IncidentPhaseEvent, PhaseAttempt } from '../lib/bridge/bugWorkflow'

const props = withDefaults(defineProps<{
  attempt?: PhaseAttempt | null
  events?: IncidentPhaseEvent[]
}>(), { attempt: null, events: () => [] })

const visiblePhases = new Set(['investigation', 'fix'])
const visibleTypes = new Set(['thread_started', 'turn_started', 'turn_completed', 'command_execution', 'mcp_tool_call', 'agent_message', 'code_intelligence', 'retry', 'error', 'turn_failed', 'result'])
const investigationSteps = [
  { key: 'evidence_handoff', label: '工单分析' },
  { key: 'timeline', label: '时间轴' },
  { key: 'runtime_scope', label: '运行时' },
  { key: 'dependency_chain', label: '调用链' },
  { key: 'evidence_correlation', label: '证据交叉' },
  { key: 'root_cause', label: '根因收敛' },
  { key: 'knowledge_sink', label: '结果沉淀' },
] as const

const remediationReassessment = computed(() => {
  const value = props.attempt?.input_json?.remediation_reassessment
  return props.attempt?.phase === 'investigation' && Boolean(
    value && typeof value === 'object' && (value as Record<string, unknown>).kind === 'user_remediation_proposal',
  )
})
const active = computed(() => Boolean(props.attempt?.status === 'running' && visiblePhases.has(props.attempt.phase)))
const phaseLabel = computed(() => {
  if (props.attempt?.phase === 'fix') return '修复'
  return remediationReassessment.value ? '修复方案评估' : '排障'
})
const targetLabel = computed(() => props.attempt?.agent_target?.trim() || 'Agent')
const currentInvestigationStep = computed(() => {
  if (props.attempt?.phase !== 'investigation') return 0
  return props.events.reduce((latest, event) => {
    if (event.type !== 'phase_step' || event.meta?.phase !== 'investigation') return latest
    const index = event.meta.step_index
    const total = event.meta.step_total
    const key = event.meta.step_key
    if (typeof index !== 'number' || !Number.isSafeInteger(index) || index < 1 || index > investigationSteps.length || total !== investigationSteps.length || key !== investigationSteps[index - 1].key) return latest
    return Math.max(latest, index)
  }, 0)
})
const progressSummary = computed(() => currentInvestigationStep.value > 0
  ? `第 ${currentInvestigationStep.value}/${investigationSteps.length} 步 · ${investigationSteps[currentInvestigationStep.value - 1].label}`
  : `等待进入第 1/${investigationSteps.length} 步`)
const safeEvents = computed(() => props.events.reduce<Array<{
  key: string
  type: string
  message: string
  state: string
  exitCode?: number
  at: string
}>>((result, event, index) => {
  const type = typeof event.type === 'string' ? event.type : ''
  if (!visibleTypes.has(type)) return result
  const message = typeof event.message === 'string' ? event.message.trim().slice(0, 2000) : ''
  const state = typeof event.meta?.state === 'string' ? event.meta.state : ''
  const exitCode = typeof event.meta?.exit_code === 'number' && Number.isSafeInteger(event.meta.exit_code) ? event.meta.exit_code : undefined
  result.push({ key: `${event.at || ''}:${type}:${state}:${index}`, type, message, state, exitCode, at: typeof event.at === 'string' ? event.at : '' })
  return result
}, []).slice(-30))

const detailsOpen = ref(false)
watch(() => props.attempt?.id, () => { detailsOpen.value = false })
const latestEvent = computed(() => safeEvents.value[safeEvents.value.length - 1])
const latestAnalysis = computed(() => [...safeEvents.value].reverse().find(event => ['agent_message', 'result', 'error', 'turn_failed'].includes(event.type)))

function eventTitle(event: { type: string; state: string; exitCode?: number }): string {
  if (event.type === 'thread_started') return 'Agent 会话已启动'
  if (event.type === 'turn_started') return `${phaseLabel.value}任务已开始`
  if (event.type === 'turn_completed') return `${phaseLabel.value}任务执行完成`
  if (event.type === 'command_execution') {
    if (event.state === 'completed') return event.exitCode === undefined ? '命令执行完成' : `命令执行完成 · exit ${event.exitCode}`
    return '正在执行命令'
  }
  if (event.type === 'mcp_tool_call') return event.state === 'completed' ? '工具调用完成' : '正在调用工具'
  if (event.type === 'code_intelligence') return 'CodeGraph 代码智能'
  if (event.type === 'agent_message' || event.type === 'result') return 'Agent 分析输出'
  if (event.type === 'retry') return 'Agent 正在重试'
  if (event.type === 'turn_failed' || event.type === 'error') return 'Agent 执行异常'
  return 'Agent 进度'
}

function fmtTime(value: string): string {
  if (!value) return ''
  const time = new Date(value)
  if (Number.isNaN(time.getTime())) return ''
  return time.toLocaleTimeString('zh-CN', { hour12: false })
}
</script>

<template>
  <section v-if="active" class="agent-progress" :data-agent-phase="attempt?.phase" aria-labelledby="agent-progress-title">
    <header>
      <div>
        <span>Agent 实时进度</span>
        <h3 id="agent-progress-title">{{ phaseLabel }} Agent 正在执行</h3>
      </div>
      <div class="agent-running-state"><i aria-hidden="true"></i><span>{{ targetLabel }} · 实时更新</span></div>
    </header>

    <div v-if="remediationReassessment" class="remediation-reassessment-progress" role="status" aria-live="polite">
      <strong>复用已确认根因</strong>
      <span>只重新评估修复路径，不会重新执行七步排障或修改系统。</span>
    </div>

    <div v-else-if="attempt?.phase === 'investigation'" class="investigation-step-progress" role="status" aria-live="polite">
      <div class="step-progress-heading">
        <strong>七步排障进度</strong>
        <span>{{ progressSummary }}</span>
      </div>
      <ol aria-label="七步排障进度">
        <li
          v-for="(step, index) in investigationSteps"
          :key="step.key"
          :class="{
            'is-complete': index + 1 < currentInvestigationStep,
            'is-current': index + 1 === currentInvestigationStep,
          }"
          :aria-current="index + 1 === currentInvestigationStep ? 'step' : undefined"
        >
          <span class="step-index">{{ index + 1 < currentInvestigationStep ? '✓' : index + 1 }}</span>
          <span>{{ step.label }}</span>
        </li>
      </ol>
    </div>

    <template v-if="safeEvents.length">
      <div class="latest-activity" role="status" aria-live="polite">
        <div class="event-heading"><strong>{{ latestEvent ? eventTitle(latestEvent) : '' }}</strong><time v-if="latestEvent">{{ fmtTime(latestEvent.at) }}</time></div>
        <p v-if="latestAnalysis?.message" class="analysis-preview">{{ latestAnalysis.message }}</p>
      </div>
      <details class="execution-details" :open="detailsOpen" @toggle="detailsOpen = ($event.target as HTMLDetailsElement).open">
        <summary>执行详情 <span>最近 {{ safeEvents.length }} 条</span></summary>
        <ol v-if="detailsOpen" class="agent-progress-events" aria-label="Agent 执行进度">
          <li v-for="event in [...safeEvents].reverse()" :key="event.key" :data-event-type="event.type">
            <span class="event-marker" aria-hidden="true"></span>
            <div>
              <div class="event-heading"><strong>{{ eventTitle(event) }}</strong><time v-if="fmtTime(event.at)">{{ fmtTime(event.at) }}</time></div>
              <pre v-if="event.message">{{ event.message }}</pre>
            </div>
          </li>
        </ol>
      </details>
    </template>
    <div v-else class="agent-progress-empty" role="status" aria-live="polite">
      <span aria-hidden="true"></span>
      <p>Agent 已启动，正在等待第一条执行事件…</p>
    </div>
  </section>
</template>

<style scoped>
.agent-progress { container: agent-progress / inline-size; min-width: 0; display: grid; gap: var(--sp-3); padding: var(--sp-4); border: 1px solid #dbe5f4; border-radius: var(--r-lg); background: #fbfdff; }
.latest-activity { min-width: 0; padding: 4px 0; }
.analysis-preview { margin: 10px 0 0; color: var(--c-text); font-size: 13px; line-height: 1.7; white-space: pre-wrap; overflow-wrap: anywhere; display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
.execution-details { border-top: 1px solid #dbeafe; }
.execution-details summary { padding: 12px 0 0; color: #475569; font-size: 12px; cursor: pointer; }
.execution-details summary span { margin-left: 8px; color: var(--c-muted); }
.execution-details summary:focus-visible { outline: 2px solid #2563eb; outline-offset: 3px; }
.execution-details[open] summary { margin-bottom: 12px; }
.agent-progress header { min-width: 0; display: flex; align-items: flex-start; justify-content: space-between; flex-wrap: wrap; gap: var(--sp-2); }
.agent-progress header > div:first-child { min-width: 0; }
.agent-progress header span, .agent-progress footer { color: var(--c-muted); font-size: var(--fs-xs); }
.agent-progress h3 { margin: 2px 0 0; color: var(--c-ink); font-size: var(--fs-base); }
.agent-running-state { display: inline-flex; align-items: center; gap: 7px; padding: 5px 9px; border-radius: 999px; background: #dbeafe; }
.agent-running-state i { width: 8px; height: 8px; border-radius: 50%; background: #2563eb; box-shadow: 0 0 0 4px rgba(37, 99, 235, .12); animation: agent-pulse 1.6s ease-in-out infinite; }
.remediation-reassessment-progress { display: grid; gap: 4px; padding: 12px; border: 1px solid #e2e8f0; border-radius: var(--r-md); background: #fff; }
.remediation-reassessment-progress strong { color: var(--c-ink); font-size: var(--fs-sm); }
.remediation-reassessment-progress span { color: var(--c-muted); font-size: var(--fs-xs); line-height: 1.5; }
.investigation-step-progress { display: grid; gap: 10px; padding: 12px; border: 1px solid #e2e8f0; border-radius: var(--r-md); background: #fff; }
.step-progress-heading { display: flex; align-items: baseline; justify-content: space-between; flex-wrap: wrap; gap: var(--sp-2); }
.step-progress-heading strong { color: var(--c-ink); font-size: var(--fs-sm); }
.step-progress-heading span { color: #2563eb; font-size: var(--fs-xs); font-weight: 600; }
.investigation-step-progress ol { display: grid; grid-template-columns: repeat(7, minmax(0, 1fr)); gap: 6px; margin: 0; padding: 0; list-style: none; }
.investigation-step-progress li { min-width: 0; display: grid; justify-items: center; gap: 5px; padding: 7px 4px; border-radius: 8px; color: #64748b; font-size: 11px; text-align: center; }
.step-index { width: 24px; height: 24px; display: grid; place-items: center; border: 1px solid #cbd5e1; border-radius: 50%; background: #f8fafc; color: #64748b; font-weight: 700; }
.investigation-step-progress li.is-complete { color: #15803d; }
.investigation-step-progress li.is-complete .step-index { border-color: #86efac; background: #dcfce7; color: #15803d; }
.investigation-step-progress li.is-current { background: #eff6ff; color: #1d4ed8; font-weight: 600; }
.investigation-step-progress li.is-current .step-index { border-color: #3b82f6; background: #2563eb; color: #fff; box-shadow: 0 0 0 4px rgba(37, 99, 235, .1); }
.agent-progress-events { max-height: 260px; display: grid; gap: 0; margin: 0; padding: 0 var(--sp-1) 0 0; overflow-x: hidden; overflow-y: auto; overscroll-behavior: contain; scrollbar-gutter: stable; list-style: none; }
.agent-progress-events li { min-width: 0; display: grid; grid-template-columns: 12px minmax(0, 1fr); gap: var(--sp-2); padding: 9px 0; border-bottom: 1px solid #dbeafe; }
.agent-progress-events li:last-child { border-bottom: 0; }
.event-marker { width: 8px; height: 8px; margin-top: 5px; border: 2px solid #60a5fa; border-radius: 50%; background: #fff; }
.agent-progress-events li[data-event-type="error"] .event-marker, .agent-progress-events li[data-event-type="turn_failed"] .event-marker { border-color: #dc2626; background: #fee2e2; }
.agent-progress-events li > div { min-width: 0; display: grid; gap: 5px; }
.event-heading { min-width: 0; display: flex; align-items: baseline; justify-content: space-between; gap: var(--sp-2); }
.event-heading strong { color: var(--c-ink); font-size: var(--fs-sm); }
.event-heading time { flex: 0 0 auto; color: var(--c-muted); font-size: 11px; }
.agent-progress pre { max-height: 150px; margin: 0; padding: 8px 10px; overflow: auto; border: 1px solid #e2e8f0; border-radius: var(--r-md); background: #fff; color: var(--c-text); font: 12px/1.55 ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; white-space: pre-wrap; overflow-wrap: anywhere; }
.agent-progress-empty { display: flex; align-items: center; gap: var(--sp-2); min-height: 46px; padding: 10px 12px; border: 1px dashed #93c5fd; border-radius: var(--r-md); color: var(--c-muted); }
.agent-progress-empty span { width: 9px; height: 9px; flex: 0 0 auto; border-radius: 50%; background: #2563eb; }
.agent-progress-empty p { margin: 0; font-size: var(--fs-sm); }
.agent-progress footer { line-height: 1.5; }
@keyframes agent-pulse { 50% { opacity: .35; transform: scale(.82); } }
@container agent-progress (max-width: 540px) { .investigation-step-progress ol { grid-template-columns: repeat(4, minmax(0, 1fr)); } }
@media (prefers-reduced-motion: reduce) { .agent-running-state i { animation: none; } }

.latest-activity { padding: 12px; border-radius: 8px; background: #f1f5fb; }
.agent-progress h3 { font-size: 14px; line-height: 1.5; }
.agent-progress-events { padding: 0 10px; border-radius: 8px; background: white; }
.agent-progress pre { background: #f8fafc; border-color: #e2e8f0; }
@container agent-progress (max-width: 340px) { .investigation-step-progress ol { grid-template-columns: repeat(2, minmax(0, 1fr)); } }
</style>
