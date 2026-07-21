<script lang="ts">
import type { IncidentCase, IncidentCaseDetail as ActionDetail, IncidentEvidenceImageInput } from '../lib/bridge/bugWorkflow'

export type CasePrimaryAction = {
  kind: 'start_validation' | 'retry_validation' | 'supply_evidence' | 'approve_fix' | 'complete_remediation' | 'continue_fix' | 'approve_merge' | 'supply_merge_decision' | 'notify_deployed' | 'supply_deployment_proof' | 'cancel_attempt' | 'continue_legacy'
  label: string
  approval?: boolean
}

export function primaryActionFor(subject: IncidentCase | ActionDetail): CasePrimaryAction | undefined {
  const detail = 'case' in subject ? subject : undefined
  const incident = detail?.case || subject as IncidentCase
  const actions: Partial<Record<IncidentCase['status'], CasePrimaryAction>> = {
    pending_validation: { kind: 'start_validation', label: '开始验证' },
    validating: { kind: 'cancel_attempt', label: '停止当前验证' },
    waiting_evidence: { kind: 'supply_evidence', label: '补充证据并继续' },
    not_reproduced: { kind: 'supply_evidence', label: '补充证据并重试' },
    investigating: { kind: 'cancel_attempt', label: '停止当前排障' },
    waiting_fix_approval: { kind: 'approve_fix', label: '允许修复', approval: true },
    waiting_remediation: { kind: 'complete_remediation', label: '确认处置完成并回归', approval: true },
    fixing: { kind: 'cancel_attempt', label: '停止当前修复' },
    fix_failed: { kind: 'continue_fix', label: '补充信息并继续修复' },
    waiting_merge_approval: { kind: 'approve_merge', label: '允许合并基线和环境分支', approval: true },
    merge_conflict: { kind: 'supply_merge_decision', label: '提交合并处理决定' },
    waiting_deployment: { kind: 'notify_deployed', label: '已部署，开始验证', approval: true },
    deployment_unverified: { kind: 'supply_deployment_proof', label: '重新部署后再检查' },
    regression_validating: { kind: 'cancel_attempt', label: '停止回归验证' },
    legacy_archived: { kind: 'continue_legacy', label: '从新一轮验证继续' },
  }
  if (incident.status === 'waiting_evidence' && detail) {
    const attempt = detail.attempts.find(item => item.id === incident.current_attempt_id)
    const outputCode = typeof attempt?.output_json?.error_code === 'string' ? attempt.output_json.error_code.trim() : ''
    const code = attempt?.error_code?.trim() || outputCode
    if (code === 'browser_validator_plan_invalid' || code === 'browser_locator_repair_plan_invalid') return { kind: 'retry_validation', label: '重新生成验证计划并重试' }
    if (code === 'browser_locator_failed') return { kind: 'retry_validation', label: '重新观察页面并生成验证计划' }
    if (['browser_validator_failed', 'browser_validator_attachment_failed', 'browser_validator_no_output', 'browser_validator_process_failed', 'browser_worker_protocol_invalid'].includes(code)) {
      return { kind: 'retry_validation', label: '重试当前验证' }
    }
    const browserGapLabels: Record<string, string> = {
      browser_assertion_failed: '补充业务预期并重试',
    }
    if (browserGapLabels[code]) return { kind: 'supply_evidence', label: browserGapLabels[code] }
    if (code === 'validator_not_installed' || code.startsWith('browser_')) return undefined
    if (attempt?.phase === 'investigation' && attempt.output_json?.investigation_status === 'insufficient_info') {
      return { kind: 'supply_evidence', label: '补充权限或外部资料并继续' }
    }
  }
  return actions[incident.status]
}
</script>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import type { CaseStatus, IncidentCaseDetail, IncidentPhaseEvent } from '../lib/bridge/bugWorkflow'
import BugAgentProgress from './BugAgentProgress.vue'
import BugCaseArtifacts from './BugCaseArtifacts.vue'
import BugBrowserProgress from './BugBrowserProgress.vue'

const props = defineProps<{
  detail: IncidentCaseDetail | null
  bugTitle?: string
  pending?: boolean
  error?: string
  phaseEvents?: IncidentPhaseEvent[]
}>()
const emit = defineEmits<{
  refresh: []
  primary: [payload: { kind: CasePrimaryAction['kind']; input?: string; evidence?: string; images?: IncidentEvidenceImageInput[]; rootCauseAttemptID?: string; caseVersion?: number; sourceBaselines?: Record<string, string> }]
  browser: [action: 'login' | 'clear-session' | 'repair-runtime' | 'redeploy-validator' | 'edit-bug-url']
}>()

const dialogOpen = ref(false)
const dialogInput = ref('')
const dialogEvidence = ref('')
type PendingEvidenceImage = IncidentEvidenceImageInput & { size: number; preview: string }
const dialogImages = ref<PendingEvidenceImage[]>([])
const dialogImageError = ref('')
const confirmButton = ref<HTMLButtonElement | null>(null)
const dialogElement = ref<HTMLElement | null>(null)
const actionTrigger = ref<HTMLElement | null>(null)
const dialogCaseVersion = ref<number>()
const dialogRootCauseAttemptID = ref('')
const dialogSourceBaselines = ref<Array<{ repo: string; branch: string }>>([])
const currentCase = computed(() => props.detail?.case)
const TIMELINE_PREVIEW_COUNT = 3
const timelineExpanded = ref(false)
const timelineEvents = computed(() => [...(props.detail?.events ?? [])].reverse())
const timelineCanExpand = computed(() => timelineEvents.value.length > TIMELINE_PREVIEW_COUNT)
const visibleTimelineEvents = computed(() => {
  if (timelineExpanded.value && timelineCanExpand.value) return timelineEvents.value
  return timelineEvents.value.slice(0, TIMELINE_PREVIEW_COUNT)
})

watch(() => props.detail?.case.id, () => {
  timelineExpanded.value = false
})

watch(() => props.detail?.events.length ?? 0, count => {
  if (count <= TIMELINE_PREVIEW_COUNT) timelineExpanded.value = false
})
const action = computed(() => props.detail ? primaryActionFor(props.detail) : undefined)
const currentAttempt = computed(() => props.detail?.attempts.find(item => item.id === props.detail?.case.current_attempt_id) || null)
const validationEvidenceRefresh = computed(() => currentAttempt.value?.phase === 'validation' && typeof currentAttempt.value.input_json?.source_investigation_attempt_id === 'string')
const expectedDeploymentCommits = computed(() => {
  const currentAttemptID = props.detail?.case.current_attempt_id || ''
  const changes = (props.detail?.code_changes || []).filter(change => change.attempt_id === currentAttemptID && change.push_status === 'pushed')
  return Object.fromEntries(changes.map(change => [change.repo, change.merge_commit || change.fix_commit]))
})
const latestDeployment = computed(() => {
  const expected = expectedDeploymentCommits.value
  const entries = Object.entries(expected)
  const items = props.detail?.deployment_observations || []
  return [...items].reverse().find(observation => {
    const observed = observation.expected_commits || {}
    return Object.keys(observed).length === entries.length && entries.every(([repo, commit]) => observed[repo] === commit)
  })
})
const deploymentVersionSource = computed(() => props.detail?.deployment_verification?.provider || latestDeployment.value?.verification_source || 'manual')
const automaticDeploymentVerification = computed(() => ['http', 'k8s'].includes(deploymentVersionSource.value))
const continuedAfterFailedRegression = computed(() => currentCase.value?.status === 'investigating' && currentCase.value.cycle_number > 1 && (props.detail?.events || []).some(event => event.event_type === 'regression_failed'))
const mergeApprovalScopes = computed(() => (props.detail?.code_changes || []).map(change => ({
  repo: change.repo,
  fixCommit: change.fix_commit,
  baseBranch: change.base_branch,
  targetBranch: change.target_environment_branch,
  targetHead: change.merge_base_head,
})))

const latestRootCauseAttempt = computed(() => [...(props.detail?.attempts || [])].reverse().find(attempt =>
  attempt.cycle_number === props.detail?.case.cycle_number && attempt.phase === 'investigation' && attempt.status === 'succeeded',
) || null)
const rootCauseType = computed(() => {
  const value = latestRootCauseAttempt.value?.output_json?.root_cause_type
  return typeof value === 'string' && value.trim() ? value.trim() : 'code'
})
const remediationPlan = computed(() => {
  const raw = latestRootCauseAttempt.value?.output_json?.remediation
  return raw && typeof raw === 'object' ? raw as Record<string, unknown> : {}
})
const usesNonCodeRemediation = computed(() => Boolean(
  ['waiting_remediation', 'remediation_applied'].includes(currentCase.value?.status || '') ||
  rootCauseType.value !== 'code' ||
  (remediationPlan.value.mode && remediationPlan.value.mode !== 'code_change'),
))

const codeStages = [
  { key: 'validation', label: '验证' },
  { key: 'investigation', label: '排障' },
  { key: 'fix', label: '修复' },
  { key: 'merge', label: '合并' },
  { key: 'deploy', label: '部署' },
  { key: 'regression', label: '回归' },
] as const

const remediationStages = [
  { key: 'validation', label: '验证' },
  { key: 'investigation', label: '排障' },
  { key: 'remediation', label: '处置' },
  { key: 'regression', label: '回归' },
] as const
const stages = computed(() => usesNonCodeRemediation.value ? remediationStages : codeStages)

const statusPosition: Record<CaseStatus, number> = {
  pending_validation: 0, validating: 0, waiting_evidence: 0, reproduced: 1, not_reproduced: 0,
  investigating: 1, root_cause_ready: 2, waiting_fix_approval: 2, fixing: 2, fix_failed: 2,
  waiting_remediation: 2, remediation_applied: 3,
  fix_pushed: 3, waiting_merge_approval: 3, merging: 3, merge_conflict: 3,
  waiting_deployment: 4, deployment_unverified: 4, deployment_verified: 5,
  regression_validating: 5, fixed_verified: 6, still_reproduces: 1, legacy_archived: -1, reset_archived: -1,
}

function statusStagePosition(status: CaseStatus): number {
  if (validationEvidenceRefresh.value) return 1
  if (status === 'waiting_evidence') {
    const phase = currentAttempt.value?.phase
    if (phase === 'investigation') return 1
    if (phase === 'fix') return 2
    if (phase === 'regression') return stages.value.length - 1
    return 0
  }
  if (!usesNonCodeRemediation.value) return statusPosition[status]
  const positions: Partial<Record<CaseStatus, number>> = {
    pending_validation: 0, validating: 0, waiting_evidence: 0, not_reproduced: 0,
    reproduced: 1, investigating: 1, root_cause_ready: 2,
    waiting_remediation: 2, remediation_applied: 3,
    regression_validating: 3, fixed_verified: 4, still_reproduces: 1,
    legacy_archived: -1, reset_archived: -1,
  }
  return positions[status] ?? statusPosition[status]
}

const activeStatuses = new Set<CaseStatus>(['validating', 'investigating', 'fixing', 'merging', 'regression_validating'])
const blockedStatuses = new Set<CaseStatus>(['waiting_evidence', 'not_reproduced', 'fix_failed', 'merge_conflict', 'deployment_unverified', 'still_reproduces'])

function stageState(index: number): 'complete' | 'current' | 'blocked' | 'pending' | 'archived' {
  const status = currentCase.value?.status
  if (!status || status === 'legacy_archived' || status === 'reset_archived') return status ? 'archived' : 'pending'
  const position = statusStagePosition(status)
  if (index < position || position === stages.value.length) return 'complete'
  if (index > position) return 'pending'
  if (blockedStatuses.has(status)) return 'blocked'
  return 'current'
}

function stageStateLabel(index: number): string {
  if (validationEvidenceRefresh.value && index === 1) return '自动补采中'
  return { complete: '已完成', current: activeStatuses.has(currentCase.value?.status as CaseStatus) ? '进行中' : '等待操作', blocked: '需处理', pending: '未开始', archived: '历史' }[stageState(index)]
}

function statusLabel(status: CaseStatus): string {
  const labels: Partial<Record<CaseStatus, string>> = {
    pending_validation: '等待验证', validating: '验证中', waiting_evidence: '等待证据', reproduced: '已复现', not_reproduced: '未复现',
    investigating: '排障中', root_cause_ready: '根因已确认', waiting_fix_approval: '等待修复授权', fixing: '修复中', fix_failed: '修复失败',
    waiting_remediation: '等待处置确认', remediation_applied: '处置已确认',
    fix_pushed: '修复已推送', waiting_merge_approval: '等待合并授权', merging: '合并中', merge_conflict: '合并冲突',
    waiting_deployment: '等待人工部署', deployment_unverified: '检测到版本不一致', deployment_verified: '部署已确认', regression_validating: '回归中',
    fixed_verified: '修复已验证', still_reproduces: '回归仍复现', legacy_archived: '历史归档', reset_archived: '已重置归档',
  }
  if (status === 'validating' && validationEvidenceRefresh.value) return '排障中 · 自动补采'
  return labels[status] || status
}

function fmtTime(value?: string): string {
  if (!value) return '-'
  const time = new Date(value)
  return Number.isNaN(time.getTime()) ? value : time.toLocaleString('zh-CN', { hour12: false })
}

async function openAction(event: MouseEvent) {
  if (!action.value || props.pending) return
  if (!action.value.approval && !['supply_evidence', 'continue_fix', 'supply_merge_decision'].includes(action.value.kind)) {
    emit('primary', { kind: action.value.kind })
    return
  }
  actionTrigger.value = event.currentTarget as HTMLElement
  if (action.value.kind === 'approve_fix') {
    dialogCaseVersion.value = props.detail?.case.version
    const currentAttemptID = props.detail?.case.current_attempt_id || ''
    const rootCause = props.detail?.attempts.find(attempt => attempt.id === currentAttemptID && attempt.phase === 'investigation' && attempt.status === 'succeeded')
    dialogRootCauseAttemptID.value = rootCause?.id || ''
    const callChain = Array.isArray(rootCause?.output_json?.call_chain) ? rootCause.output_json.call_chain : []
    const repos = [...new Set(callChain.map(item => {
      if (!item || typeof item !== 'object') return ''
      const repo = (item as Record<string, unknown>).repo
      return typeof repo === 'string' ? repo.trim() : ''
    }).filter(Boolean))]
    dialogSourceBaselines.value = (repos.length > 0 ? repos : ['']).map(repo => ({ repo, branch: '' }))
  } else if (action.value.kind === 'complete_remediation') {
    dialogCaseVersion.value = props.detail?.case.version
    dialogRootCauseAttemptID.value = latestRootCauseAttempt.value?.id || ''
    dialogSourceBaselines.value = []
  } else {
    dialogCaseVersion.value = undefined
    dialogRootCauseAttemptID.value = ''
    dialogSourceBaselines.value = []
  }
  dialogInput.value = ''
  dialogEvidence.value = ''
  dialogImages.value = []
  dialogImageError.value = ''
  dialogOpen.value = true
  await nextTick()
  if (action.value.kind === 'approve_fix' && !sourceBaselinesValid.value) {
    const firstEmpty = [...(dialogElement.value?.querySelectorAll<HTMLInputElement>('.source-baseline-row input') || [])].find(input => !input.value.trim())
    setTimeout(() => firstEmpty?.focus(), 0)
  } else {
    confirmButton.value?.focus()
  }
}

function closeDialog() {
  if (props.pending) return
  dialogOpen.value = false
  nextTick(() => actionTrigger.value?.focus())
}

function confirmAction() {
  if (!action.value) return
  const payload: { kind: CasePrimaryAction['kind']; input?: string; evidence?: string; images?: IncidentEvidenceImageInput[]; rootCauseAttemptID?: string; caseVersion?: number; sourceBaselines?: Record<string, string> } = { kind: action.value.kind }
  if (action.value.kind === 'approve_fix') {
    payload.rootCauseAttemptID = dialogRootCauseAttemptID.value
    payload.caseVersion = dialogCaseVersion.value
    payload.sourceBaselines = Object.fromEntries(dialogSourceBaselines.value.map(item => [item.repo.trim(), item.branch.trim()]).filter(([repo]) => repo))
  }
  if (action.value.kind === 'complete_remediation') {
    payload.rootCauseAttemptID = dialogRootCauseAttemptID.value
    payload.caseVersion = dialogCaseVersion.value
    payload.input = dialogInput.value.trim()
    payload.evidence = dialogEvidence.value.trim()
  }
  if (['supply_evidence', 'continue_fix', 'supply_merge_decision'].includes(action.value.kind)) payload.input = dialogInput.value.trim()
  if (action.value.kind === 'supply_evidence' && dialogImages.value.length > 0) {
    payload.images = dialogImages.value.map(({ name, mime_type, base64_data }) => ({ name, mime_type, base64_data }))
  }
  emit('primary', payload)
  dialogOpen.value = false
  nextTick(() => actionTrigger.value?.focus())
}

const evidenceSupplementMissing = computed(() => {
  if (!action.value || !['supply_evidence', 'continue_fix', 'supply_merge_decision'].includes(action.value.kind)) return false
  if (action.value.kind === 'supply_evidence') return !dialogInput.value.trim() && dialogImages.value.length === 0
  return !dialogInput.value.trim()
})

function readEvidenceImage(file: File): Promise<PendingEvidenceImage> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(new Error('读取图片失败'))
    reader.onload = () => {
      const preview = typeof reader.result === 'string' ? reader.result : ''
      const separator = preview.indexOf(',')
      const base64Data = separator >= 0 ? preview.slice(separator + 1) : ''
      if (!base64Data) {
        reject(new Error('图片内容为空'))
        return
      }
      resolve({ name: file.name, mime_type: file.type as 'image/png' | 'image/jpeg', base64_data: base64Data, size: file.size, preview })
    }
    reader.readAsDataURL(file)
  })
}

async function selectEvidenceImages(event: Event) {
  const input = event.currentTarget as HTMLInputElement
  const files = Array.from(input.files || [])
  input.value = ''
  dialogImageError.value = ''
  if (dialogImages.value.length + files.length > 4) {
    dialogImageError.value = '最多上传 4 张图片。'
    return
  }
  for (const file of files) {
    if (!['image/png', 'image/jpeg'].includes(file.type)) {
      dialogImageError.value = '仅支持 PNG 或 JPEG 图片。'
      return
    }
    if (file.size <= 0 || file.size > 16 * 1024 * 1024) {
      dialogImageError.value = '每张图片必须小于 16 MB。'
      return
    }
  }
  try {
    dialogImages.value.push(...await Promise.all(files.map(readEvidenceImage)))
  } catch (error) {
    dialogImageError.value = error instanceof Error ? error.message : '读取图片失败。'
  }
}

function removeEvidenceImage(index: number) {
  dialogImages.value.splice(index, 1)
  dialogImageError.value = ''
}

function formatEvidenceImageSize(size: number): string {
  if (size < 1024 * 1024) return `${Math.max(1, Math.round(size / 1024))} KB`
  return `${(size / 1024 / 1024).toFixed(1)} MB`
}

function addSourceBaseline() {
  dialogSourceBaselines.value.push({ repo: '', branch: '' })
}

function removeSourceBaseline(index: number) {
  if (dialogSourceBaselines.value.length <= 1) return
  dialogSourceBaselines.value.splice(index, 1)
}

const sourceBaselinesValid = computed(() => dialogSourceBaselines.value.length > 0 && dialogSourceBaselines.value.every(item => item.repo.trim()) && new Set(dialogSourceBaselines.value.map(item => item.repo.trim())).size === dialogSourceBaselines.value.length)

function trapDialogFocus(event: KeyboardEvent) {
  if (event.key !== 'Tab' || !dialogElement.value) return
  const focusable = [...dialogElement.value.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled), textarea:not(:disabled)')]
  if (focusable.length === 0) return
  const first = focusable[0]
  const last = focusable[focusable.length - 1]
  if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}

function dialogTitle(): string {
  if (action.value?.kind === 'approve_fix') return '确认允许修复'
  if (action.value?.kind === 'complete_remediation') return '确认非代码处置已完成'
  if (action.value?.kind === 'approve_merge') return '确认合并基线和环境分支'
  if (action.value?.kind === 'supply_merge_decision') return '提交合并冲突处理决定'
  if (action.value?.kind === 'notify_deployed') return '确认业务版本已部署'
  return action.value?.label || '继续处理'
}
</script>

<template>
  <section class="case-lifecycle" data-responsive-viewports="375,768,1024,1440" data-overflow-safe="true">
    <main class="case-column case-main-column">
      <div v-if="!detail" class="empty-state">选择一个 Case 查看生命周期</div>
      <template v-else>
        <header class="case-heading" :data-case-id="detail.case.id" tabindex="-1">
          <div class="case-heading-copy">
            <span>故障闭环进度</span>
            <h2>{{ bugTitle?.trim() || '当前 Bug' }}</h2>
            <p>第 {{ detail.case.cycle_number }} 轮 · {{ detail.case.environment || '环境未知' }}</p>
          </div>
          <div class="case-heading-actions">
            <span class="status-pill" :data-status="detail.case.status">{{ statusLabel(detail.case.status) }}</span>
            <button class="icon-button" type="button" aria-label="刷新故障闭环" :disabled="pending" @click="emit('refresh')">
              <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 11a8 8 0 1 0-2.34 5.66M20 5v6h-6" /></svg>
            </button>
          </div>
        </header>

        <ol class="stage-progress" :class="{ 'is-remediation': usesNonCodeRemediation }" aria-label="故障处理阶段">
          <li v-for="(stage, index) in stages" :key="stage.key" class="lifecycle-stage" :data-state="stageState(index)">
            <span class="stage-marker" aria-hidden="true">{{ index + 1 }}</span>
            <span><strong>{{ stage.label }}</strong><small>{{ stageStateLabel(index) }}</small></span>
          </li>
        </ol>

        <div class="workflow-loop-hint" :data-loop-state="detail.case.status === 'fixed_verified' ? 'complete' : continuedAfterFailedRegression ? 'restarted' : 'active'">
          <span class="workflow-loop-icon" aria-hidden="true">↺</span>
          <p v-if="detail.case.status === 'fixed_verified' && detail.bug_ticket_resolution?.state === 'resolved'"><strong>回归通过</strong> → Bug 工单已转为已解决，故障闭环完成。</p>
          <p v-else-if="detail.case.status === 'fixed_verified'"><strong>回归通过</strong> → 故障闭环已完成，正在将 Bug 工单同步为已解决。</p>
          <p v-else-if="continuedAfterFailedRegression"><strong>回归仍复现</strong> → 已自动进入下一轮（第 {{ detail.case.cycle_number }} 轮）排障，随后继续修复、部署和回归。</p>
          <p v-else><strong>循环规则</strong>：回归仍复现会自动进入下一轮排障；只有回归通过才会结束闭环并解决 Bug 工单。</p>
        </div>

        <section class="current-action-card" aria-labelledby="current-action-title">
          <div>
            <span>当前状态</span>
            <h3 id="current-action-title">{{ statusLabel(detail.case.status) }}</h3>
            <p v-if="detail.case.status === 'legacy_archived'">历史记录只读；继续时会通过 CreateAndStart 创建新的 Case，不修改归档 attempt。</p>
            <p v-else-if="detail.case.status === 'reset_archived'">历史记录只读；重置后的新 Case 已保留原闭环的证据和审计关系。</p>
            <p v-else-if="detail.case.status === 'waiting_deployment'">环境分支已推送。人工部署后，Studio 会尝试自动采集运行版本；采集不到也会直接启动回归验证。</p>
            <p v-else-if="detail.case.status === 'waiting_remediation'">根因不需要修改代码。完成数据、配置、运行环境、网络或外部依赖处置后，提交实际结果与证据即可开始回归。</p>
            <p v-else-if="continuedAfterFailedRegression">第 {{ detail.case.cycle_number }} 轮 · 回归仍复现，Studio 已把本轮新证据和差分带入排障。</p>
            <p v-else>第 {{ detail.case.cycle_number }} 轮 · {{ detail.case.environment || '环境未知' }}</p>
          </div>
          <div class="current-action-controls">
            <button v-if="action" class="btn primary primary-action" type="button" :disabled="pending" @click="openAction">
              {{ pending ? '处理中…' : action.label }}
            </button>
            <span v-else class="terminal-copy">{{ detail.case.status === 'fixed_verified' ? '闭环完成' : detail.case.status === 'reset_archived' ? '已归档，由新 Case 接替' : '当前阶段自动推进' }}</span>
          </div>
        </section>

        <BugAgentProgress
          :attempt="currentAttempt"
          :events="phaseEvents || []"
        />

        <BugBrowserProgress
          :attempt="currentAttempt"
          :events="phaseEvents || []"
          :system-i-d="detail.case.system_id"
          :environment="detail.case.environment"
          :pending="pending"
          @action="emit('browser', $event)"
        />

        <p class="live-error" role="status" aria-live="assertive">{{ error }}</p>

        <section class="timeline" aria-labelledby="timeline-title">
          <header class="timeline-heading">
            <div>
              <h3 id="timeline-title">过程时间线</h3>
              <span class="timeline-count">· 共 {{ detail.events.length }} 条</span>
            </div>
            <button
              v-if="timelineCanExpand"
              class="timeline-toggle"
              type="button"
              :aria-expanded="timelineExpanded"
              aria-controls="case-timeline-events"
              @click="timelineExpanded = !timelineExpanded"
            >
              <span>{{ timelineExpanded ? '收起' : '展开全部' }}</span>
              <svg class="timeline-toggle-icon" :class="{ 'is-expanded': timelineExpanded }" viewBox="0 0 20 20" aria-hidden="true">
                <path d="m5 7.5 5 5 5-5" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="1.8" />
              </svg>
            </button>
          </header>
          <ol
            v-if="detail.events.length > 0"
            id="case-timeline-events"
            class="timeline-events"
            :class="{ 'is-expanded': timelineExpanded && timelineCanExpand }"
            aria-label="Case 时间线"
          >
            <li v-for="event in visibleTimelineEvents" :key="event.id">
              <span class="timeline-dot" aria-hidden="true"></span>
              <div><strong>{{ event.event_type }}</strong><span>{{ statusLabel(event.from_status) }} → {{ statusLabel(event.to_status) }}</span><small>{{ fmtTime(event.created_at) }} · {{ event.actor_type }}</small></div>
            </li>
          </ol>
          <p v-else class="empty-state">暂无状态事件</p>
        </section>
      </template>
    </main>

    <aside class="case-column case-detail-column" aria-label="Case 证据与详情">
      <BugCaseArtifacts v-if="detail" :detail="detail" />
      <p v-else class="empty-state">证据与变更将在这里显示</p>
    </aside>

    <div v-if="dialogOpen && action" class="dialog-backdrop" @click.self="closeDialog" @keydown.esc="closeDialog">
      <section ref="dialogElement" role="dialog" aria-modal="true" aria-labelledby="case-action-dialog-title" class="approval-dialog" @keydown="trapDialogFocus">
        <header><h2 id="case-action-dialog-title">{{ dialogTitle() }}</h2></header>
        <template v-if="action.kind === 'approve_fix'">
          <p>将授权修复 Agent 基于当前根因和证据创建最小修复。请为每个受影响仓库确认开发基线；留空时默认使用当前环境对应的分支。修复分支会从确认后的基线创建，后续分别合并并推送到开发基线和环境分支。</p>
          <p>授权范围：Case v{{ dialogCaseVersion }} / {{ dialogRootCauseAttemptID || '未找到根因 attempt' }}。</p>
          <div class="source-baseline-editor">
            <div v-for="(item, index) in dialogSourceBaselines" :key="index" class="source-baseline-row">
              <label :for="`fix-repo-${index}`">代码仓库</label>
              <input :id="`fix-repo-${index}`" v-model="item.repo" autocomplete="off" placeholder="例如 admin-web" />
              <label :for="`fix-baseline-${index}`">开发基线分支</label>
              <input :id="`fix-baseline-${index}`" v-model="item.branch" autocomplete="off" placeholder="留空则使用当前环境分支" />
              <button class="btn" type="button" :disabled="dialogSourceBaselines.length <= 1" @click="removeSourceBaseline(index)">移除</button>
            </div>
            <button class="btn" type="button" @click="addSourceBaseline">+ 添加仓库</button>
          </div>
        </template>
        <template v-else-if="action.kind === 'approve_merge'">
          <p>将把修复提交分别合并并通过 SSH 推送到已确认的开发基线和环境分支；两者相同时只执行一次。Studio 会重新检查目标 HEAD，任何变化都会使本次授权失效。</p>
          <dl class="deployment-preview">
            <div v-for="scope in mergeApprovalScopes" :key="scope.repo"><dt>{{ scope.repo }}</dt><dd><code>{{ scope.fixCommit }} → 基线 {{ scope.baseBranch }}；环境 {{ scope.targetBranch }} @ {{ scope.targetHead || '待重新检查' }}</code></dd></div>
          </dl>
        </template>
        <template v-else-if="action.kind === 'complete_remediation'">
          <p>Studio 不会自动修改业务数据、配置或运行资源。请在对应平台完成处置，再记录实际操作和可审计证据；提交后将直接启动全新的业务回归。</p>
          <dl class="deployment-preview remediation-preview">
            <div><dt>根因类型</dt><dd>{{ rootCauseType }}</dd></div>
            <div><dt>处置目标</dt><dd>{{ remediationPlan.target || '未指定' }}</dd></div>
            <div><dt>建议动作</dt><dd>{{ remediationPlan.summary || '按根因结论处置' }}</dd></div>
            <div><dt>回滚方案</dt><dd>{{ remediationPlan.rollback || '按变更平台的既有回滚流程执行' }}</dd></div>
            <div><dt>验证方式</dt><dd>{{ remediationPlan.verification || '重新执行业务回归' }}</dd></div>
          </dl>
          <label for="remediation-summary">实际处置结果</label>
          <textarea id="remediation-summary" v-model="dialogInput" rows="4" placeholder="例如：已回滚配置 dataId xxx 至版本 42，服务已恢复"></textarea>
          <label for="remediation-evidence">处置证据</label>
          <textarea id="remediation-evidence" v-model="dialogEvidence" rows="3" placeholder="填写变更单号、配置版本、监控/工单链接或其他可核验信息"></textarea>
        </template>
        <p v-else-if="action.kind === 'supply_merge_decision'">记录冲突处理结果并返回合并授权门，不会在这一步直接重新合并。</p>
        <p v-else-if="action.kind === 'notify_deployed'">Studio 不执行部署。确认后会尝试从运行环境自动采集版本；如果没有可靠版本信息，将留空并直接开始回归。</p>
        <dl v-if="action.kind === 'notify_deployed'" class="deployment-preview">
          <div><dt>目标环境</dt><dd>{{ detail?.case.environment || '未知' }}</dd></div>
          <div><dt>期望 commits</dt><dd><code v-for="(commit, repo) in expectedDeploymentCommits" :key="repo">{{ repo }}: {{ commit }}</code><span v-if="Object.keys(expectedDeploymentCommits).length === 0">尚未记录</span></dd></div>
          <div><dt>采集方式</dt><dd>{{ deploymentVersionSource }}<small v-if="detail?.deployment_verification?.hint"> · {{ detail.deployment_verification.hint }}</small></dd></div>
        </dl>
        <p v-if="action.kind === 'notify_deployed' && automaticDeploymentVerification">无需手工填写版本号或 commit。只有明确检测到运行版本与本次修复不一致时，流程才会停下。</p>
        <p v-else-if="action.kind === 'notify_deployed'">无需填写版本号或 commit；本次只记录部署确认，最终以回归结果为准。</p>
        <label v-if="['supply_evidence', 'continue_fix', 'supply_merge_decision'].includes(action.kind)" for="case-supplement">补充信息</label>
        <textarea v-if="['supply_evidence', 'continue_fix', 'supply_merge_decision'].includes(action.kind)" id="case-supplement" v-model="dialogInput" rows="5" :placeholder="action.kind === 'supply_evidence' ? '描述图片中的页面状态、操作位置或业务预期（可选）' : '输入新证据、处理决定或测试信息'"></textarea>
        <section v-if="action.kind === 'supply_evidence'" class="evidence-image-upload">
          <div class="evidence-image-heading">
            <div>
              <strong>补充截图</strong>
              <small>PNG / JPEG，单张不超过 16 MB，最多 4 张；重试时会直接交给验证 Agent。</small>
            </div>
            <input id="case-evidence-images" type="file" accept="image/png,image/jpeg" multiple @change="selectEvidenceImages">
            <label class="btn evidence-image-picker" for="case-evidence-images">选择图片</label>
          </div>
          <p v-if="dialogImageError" class="evidence-image-error" role="alert">{{ dialogImageError }}</p>
          <ul v-if="dialogImages.length" class="evidence-image-list" aria-label="待上传图片">
            <li v-for="(image, index) in dialogImages" :key="`${image.name}-${index}`">
              <img :src="image.preview" alt="">
              <span><strong>{{ image.name }}</strong><small>{{ formatEvidenceImageSize(image.size) }}</small></span>
              <button type="button" class="evidence-image-remove" :aria-label="`移除 ${image.name}`" @click="removeEvidenceImage(index)">×</button>
            </li>
          </ul>
        </section>
        <footer>
          <button class="btn" type="button" :disabled="pending" @click="closeDialog">取消</button>
          <button ref="confirmButton" class="btn primary" data-confirm type="button" :disabled="pending || (action.kind === 'approve_fix' && (!dialogRootCauseAttemptID || dialogCaseVersion === undefined || !sourceBaselinesValid)) || (action.kind === 'complete_remediation' && (!dialogRootCauseAttemptID || dialogCaseVersion === undefined || !dialogInput.trim() || !dialogEvidence.trim())) || evidenceSupplementMissing" @click="confirmAction">{{ action.kind === 'complete_remediation' ? '确认并开始回归' : action.kind === 'supply_evidence' ? '保存证据并重试' : '确认' }}</button>
        </footer>
      </section>
    </div>
  </section>
</template>

<style scoped>
.case-lifecycle { width: 100%; min-width: 0; display: grid; grid-template-columns: minmax(0, 1fr); align-items: start; gap: var(--sp-3); color: var(--c-text); }
.case-column { min-width: 0; border: 1px solid var(--c-line); border-radius: var(--r-lg); background: var(--c-surf-2); padding: var(--sp-3); overflow-wrap: anywhere; }
.case-heading, .current-action-card { display: flex; align-items: center; justify-content: space-between; gap: var(--sp-2); }
.case-heading-copy { min-width: 0; }
.case-heading-copy > span { display: block; margin-bottom: 2px; }
.case-heading-copy p { margin-top: 3px; color: var(--c-muted); font-size: var(--fs-xs); }
.case-heading-actions { min-width: 0; display: flex; align-items: center; justify-content: flex-end; gap: var(--sp-2); }
h2, h3, p { margin: 0; }
.case-heading h2 { color: var(--c-ink); font-size: var(--fs-lg); }
.case-heading span, .current-action-card span { color: var(--c-muted); font-size: var(--fs-sm); }
.icon-button { width: 44px; height: 44px; display: grid; place-items: center; border: 1px solid var(--c-line-2); border-radius: var(--r-md); background: var(--c-surf); color: var(--c-text); cursor: pointer; }
.icon-button svg { width: 19px; fill: none; stroke: currentColor; stroke-width: 1.8; stroke-linecap: round; stroke-linejoin: round; }
.status-pill { display: inline-flex; align-items: center; gap: 5px; color: var(--c-muted); font-size: var(--fs-xs); }
.status-pill::before { content: ''; width: 7px; height: 7px; border-radius: 50%; background: #94a3b8; }
[data-status="fixed_verified"]::before, [data-status="deployment_verified"]::before { background: #15803d; }
[data-status="waiting_evidence"]::before, [data-status="fix_failed"]::before, [data-status="merge_conflict"]::before, [data-status="deployment_unverified"]::before { background: #c2410c; }
[data-status="validating"]::before, [data-status="investigating"]::before, [data-status="fixing"]::before, [data-status="merging"]::before, [data-status="regression_validating"]::before { background: #2563eb; }
.case-main-column { display: flex; flex-direction: column; gap: var(--sp-4); background: var(--c-surf); }
.status-pill { flex: 0 0 auto; padding: 6px 9px; border: 1px solid var(--c-line); border-radius: 999px; background: var(--c-surf-2); }
.stage-progress { display: grid; grid-template-columns: repeat(6, minmax(0, 1fr)); gap: 5px; margin: 0; padding: 0; list-style: none; }
.stage-progress.is-remediation { grid-template-columns: repeat(4, minmax(0, 1fr)); }
.lifecycle-stage { min-width: 0; display: flex; align-items: center; gap: 6px; padding: 8px 5px; border-top: 3px solid var(--c-line-2); }
.stage-marker { flex: 0 0 25px; width: 25px; height: 25px; display: grid; place-items: center; border-radius: 50%; background: var(--c-surf-3); color: var(--c-muted); font-size: var(--fs-xs); font-weight: 700; }
.lifecycle-stage > span:last-child { min-width: 0; display: grid; gap: 1px; }
.lifecycle-stage strong { color: var(--c-text); font-size: var(--fs-sm); }
.lifecycle-stage small { color: var(--c-muted); font-size: 10px; }
.lifecycle-stage[data-state="complete"] { border-color: #16a34a; }
.lifecycle-stage[data-state="complete"] .stage-marker { background: var(--c-success-bg); color: var(--c-success); }
.lifecycle-stage[data-state="current"] { border-color: var(--c-accent); }
.lifecycle-stage[data-state="current"] .stage-marker { background: #eff6ff; color: #1d4ed8; }
.lifecycle-stage[data-state="blocked"] { border-color: #ea580c; }
.lifecycle-stage[data-state="blocked"] .stage-marker { background: #fff7ed; color: #c2410c; }
.workflow-loop-hint { min-width: 0; display: flex; align-items: center; gap: var(--sp-2); margin-top: calc(var(--sp-3) * -1); padding: 9px 12px; border: 1px dashed #93c5fd; border-radius: var(--r-md); background: #f8fbff; color: var(--c-muted); }
.workflow-loop-hint p { min-width: 0; font-size: var(--fs-xs); line-height: 1.5; }
.workflow-loop-hint strong { color: var(--c-text); }
.workflow-loop-icon { flex: 0 0 auto; display: grid; place-items: center; width: 24px; height: 24px; border-radius: 50%; background: #dbeafe; color: #1d4ed8; font-size: 16px; font-weight: 800; }
.workflow-loop-hint[data-loop-state="restarted"] { border-color: #fdba74; background: #fff7ed; }
.workflow-loop-hint[data-loop-state="restarted"] .workflow-loop-icon { background: #ffedd5; color: #c2410c; }
.workflow-loop-hint[data-loop-state="complete"] { border-style: solid; border-color: #86efac; background: #f0fdf4; }
.workflow-loop-hint[data-loop-state="complete"] .workflow-loop-icon { background: #dcfce7; color: #15803d; }
.current-action-card { align-items: flex-end; padding: var(--sp-4); border: 1px solid var(--c-line); border-left: 3px solid var(--c-accent); border-radius: var(--r-lg); background: var(--c-surf-2); }
.current-action-card > div { min-width: 0; }
.current-action-card h3 { margin: 3px 0; color: var(--c-ink); font-size: var(--fs-lg); }
.current-action-card p { max-width: 62ch; color: var(--c-muted); font-size: var(--fs-sm); line-height: 1.55; }
.primary-action { min-height: 44px; flex: 0 0 auto; }
.current-action-controls { min-width: 0; display: flex; align-items: stretch; justify-content: flex-end; gap: var(--sp-2); flex: 0 0 auto; }
.terminal-copy { padding: 8px 0; font-weight: 600; }
.live-error { min-height: 1.5em; color: var(--c-danger); font-size: var(--fs-sm); }
.live-error:empty { display: none; }
.source-baseline-editor { display: grid; gap: var(--sp-2); margin-top: var(--sp-2); }
.source-baseline-row { display: grid; grid-template-columns: minmax(120px, .8fr) minmax(180px, 1fr) auto; gap: 6px var(--sp-2); align-items: end; padding: var(--sp-2); border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf-2); }
.source-baseline-row label { grid-row: 1; color: var(--c-muted); font-size: var(--fs-xs); }
.source-baseline-row input { min-width: 0; min-height: 40px; padding: 8px 10px; border: 1px solid var(--c-line-2); border-radius: var(--r-sm); background: var(--c-surf); color: var(--c-text); font: inherit; }
.source-baseline-row .btn { grid-column: 3; grid-row: 1 / span 2; }
.timeline-heading { min-width: 0; display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: var(--sp-2); margin-bottom: var(--sp-3); }
.timeline-heading > div { min-width: 0; display: flex; align-items: baseline; flex-wrap: wrap; gap: 4px; }
.timeline-heading h3 { margin: 0; color: var(--c-ink); font-size: var(--fs-base); }
.timeline-count { color: var(--c-muted); font-size: var(--fs-xs); }
.timeline-toggle { min-width: 44px; min-height: 44px; display: inline-flex; align-items: center; justify-content: center; gap: 6px; padding: 8px 10px; border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf); color: var(--c-text); font: inherit; font-size: var(--fs-sm); font-weight: 600; cursor: pointer; }
.timeline-toggle:hover { border-color: #93c5fd; background: #eff6ff; color: #1d4ed8; }
.timeline-toggle:focus-visible { outline: 3px solid rgba(37, 99, 235, .55); outline-offset: 2px; }
.timeline-toggle-icon { width: 16px; height: 16px; flex: 0 0 auto; transition: transform 180ms ease; }
.timeline-toggle-icon.is-expanded { transform: rotate(180deg); }
.timeline-events { margin: 0; padding: 0; list-style: none; }
.timeline-events.is-expanded { max-height: clamp(280px, 38vh, 520px); padding-right: var(--sp-1); overflow-x: hidden; overflow-y: auto; overscroll-behavior: contain; scrollbar-gutter: stable; }
.timeline li { display: grid; grid-template-columns: 14px minmax(0, 1fr); gap: var(--sp-2); padding-bottom: var(--sp-3); }
.timeline-dot { width: 9px; height: 9px; margin-top: 4px; border: 2px solid #93c5fd; border-radius: 50%; background: var(--c-surf); box-shadow: 0 0 0 3px #eff6ff; }
.timeline li > div { min-width: 0; display: grid; gap: 2px; overflow-wrap: anywhere; }
.timeline strong { color: var(--c-ink); font-size: var(--fs-sm); }
.timeline li span, .timeline li small { color: var(--c-muted); font-size: var(--fs-xs); }
.empty-state { padding: var(--sp-4); border: 1px dashed var(--c-line-2); border-radius: var(--r-md); color: var(--c-muted); text-align: center; font-size: var(--fs-sm); }
.dialog-backdrop { position: fixed; inset: 0; z-index: 50; display: grid; place-items: center; padding: var(--sp-4); background: rgba(15, 23, 42, .56); }
.approval-dialog { width: min(520px, 100%); max-height: calc(100vh - 32px); overflow: auto; box-sizing: border-box; display: grid; gap: var(--sp-3); padding: var(--sp-5); border: 1px solid var(--c-line-2); border-radius: var(--r-lg); background: var(--c-surf); box-shadow: 0 18px 50px rgba(15, 23, 42, .24); }
.approval-dialog h2 { color: var(--c-ink); font-size: var(--fs-lg); }
.approval-dialog p, .approval-dialog label { color: var(--c-text); font-size: var(--fs-base); line-height: 1.6; }
.approval-dialog label { font-weight: 600; }
.deployment-preview { display: grid; gap: var(--sp-2); margin: 0; padding: var(--sp-3); border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf-2); }
.deployment-preview > div { display: grid; grid-template-columns: 110px minmax(0, 1fr); gap: var(--sp-2); }
.deployment-preview dt { color: var(--c-muted); font-size: var(--fs-sm); }
.deployment-preview dd { min-width: 0; margin: 0; color: var(--c-ink); font-size: var(--fs-sm); overflow-wrap: anywhere; }
.deployment-preview code { display: block; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
.approval-dialog input, .approval-dialog textarea { min-height: 44px; }
.evidence-image-upload { display: grid; gap: var(--sp-2); padding: var(--sp-3); border: 1px dashed #93c5fd; border-radius: var(--r-md); background: #f8fbff; }
.evidence-image-heading { display: flex; align-items: center; justify-content: space-between; gap: var(--sp-2); }
.evidence-image-heading > div { min-width: 0; display: grid; gap: 2px; }
.evidence-image-heading strong { color: var(--c-ink); font-size: var(--fs-sm); }
.evidence-image-heading small { color: var(--c-muted); font-size: var(--fs-xs); line-height: 1.45; }
.evidence-image-picker { flex: 0 0 auto; min-height: 40px; cursor: pointer; }
#case-evidence-images { position: absolute; width: 1px; height: 1px; margin: -1px; padding: 0; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; border: 0; }
#case-evidence-images:focus-visible + .evidence-image-picker { outline: 3px solid rgba(37, 99, 235, .55); outline-offset: 2px; }
.evidence-image-error { color: var(--c-danger) !important; font-size: var(--fs-xs) !important; }
.evidence-image-list { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--sp-2); margin: 0; padding: 0; list-style: none; }
.evidence-image-list li { min-width: 0; display: grid; grid-template-columns: 52px minmax(0, 1fr) 32px; align-items: center; gap: var(--sp-2); padding: 6px; border: 1px solid var(--c-line); border-radius: var(--r-sm); background: var(--c-surf); }
.evidence-image-list img { width: 52px; height: 42px; border-radius: 5px; object-fit: cover; background: var(--c-surf-3); }
.evidence-image-list span { min-width: 0; display: grid; gap: 2px; }
.evidence-image-list span strong { overflow: hidden; color: var(--c-text); font-size: var(--fs-xs); text-overflow: ellipsis; white-space: nowrap; }
.evidence-image-list span small { color: var(--c-muted); font-size: 10px; }
.evidence-image-remove { width: 30px; height: 30px; display: grid; place-items: center; border: 0; border-radius: 50%; background: #fee2e2; color: #b91c1c; font-size: 19px; cursor: pointer; }
.approval-dialog footer { display: flex; justify-content: flex-end; gap: var(--sp-2); }
.approval-dialog footer .btn { min-height: 44px; min-width: 88px; justify-content: center; }
button:focus-visible, input:focus-visible, textarea:focus-visible { outline: 3px solid rgba(37, 99, 235, .55); outline-offset: 2px; }
@media (max-width: 899px) {
  .current-action-card { align-items: stretch; flex-direction: column; }
  .current-action-controls { width: 100%; flex-direction: column; }
  .primary-action { width: 100%; justify-content: center; }
}
@media (max-width: 560px) {
  .stage-progress { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .case-heading { align-items: flex-start; flex-direction: column; }
  .approval-dialog footer { flex-direction: column-reverse; }
  .approval-dialog footer .btn { width: 100%; }
  .evidence-image-heading { align-items: stretch; flex-direction: column; }
  .evidence-image-list { grid-template-columns: minmax(0, 1fr); }
}
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after { scroll-behavior: auto !important; transition-duration: .01ms !important; animation-duration: .01ms !important; }
}
</style>
