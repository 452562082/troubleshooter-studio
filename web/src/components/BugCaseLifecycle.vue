<script lang="ts">
import type { IncidentCase } from '../lib/bridge/bugWorkflow'

export type CasePrimaryAction = {
  kind: 'start_validation' | 'supply_evidence' | 'approve_fix' | 'continue_fix' | 'approve_merge' | 'supply_merge_decision' | 'notify_deployed' | 'supply_deployment_proof' | 'cancel_attempt' | 'continue_legacy'
  label: string
  approval?: boolean
}

export function primaryActionFor(incident: IncidentCase): CasePrimaryAction | undefined {
  const actions: Partial<Record<IncidentCase['status'], CasePrimaryAction>> = {
    pending_validation: { kind: 'start_validation', label: '开始验证' },
    validating: { kind: 'cancel_attempt', label: '停止当前验证' },
    waiting_evidence: { kind: 'supply_evidence', label: '补充证据并继续' },
    not_reproduced: { kind: 'supply_evidence', label: '补充证据并重试' },
    investigating: { kind: 'cancel_attempt', label: '停止当前排障' },
    waiting_fix_approval: { kind: 'approve_fix', label: '允许修复', approval: true },
    fixing: { kind: 'cancel_attempt', label: '停止当前修复' },
    fix_failed: { kind: 'continue_fix', label: '补充信息并继续修复' },
    waiting_merge_approval: { kind: 'approve_merge', label: '允许合并环境分支', approval: true },
    merge_conflict: { kind: 'supply_merge_decision', label: '提交合并处理决定' },
    waiting_deployment: { kind: 'notify_deployed', label: '已部署，开始验证', approval: true },
    deployment_unverified: { kind: 'supply_deployment_proof', label: '补充部署证明' },
    regression_validating: { kind: 'cancel_attempt', label: '停止回归验证' },
    legacy_archived: { kind: 'continue_legacy', label: '从新一轮验证继续' },
  }
  return actions[incident.status]
}
</script>

<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import type { CaseStatus, IncidentCaseDetail } from '../lib/bridge/bugWorkflow'
import BugCaseArtifacts from './BugCaseArtifacts.vue'

const props = defineProps<{
  cases: IncidentCase[]
  detail: IncidentCaseDetail | null
  pending?: boolean
  error?: string
}>()
const emit = defineEmits<{
  select: [caseID: string]
  refresh: []
  primary: [payload: { kind: CasePrimaryAction['kind']; input?: string; observedVersion?: string; observedCommits?: Record<string, string>; versionSource?: string; rootCauseAttemptID?: string; caseVersion?: number }]
}>()

const dialogOpen = ref(false)
const dialogInput = ref('')
const dialogObservedCommits = ref<Record<string, string>>({})
const confirmButton = ref<HTMLButtonElement | null>(null)
const dialogElement = ref<HTMLElement | null>(null)
const actionTrigger = ref<HTMLElement | null>(null)
const dialogCaseVersion = ref<number>()
const dialogRootCauseAttemptID = ref('')
const currentCase = computed(() => props.detail?.case)
const action = computed(() => currentCase.value ? primaryActionFor(currentCase.value) : undefined)
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
  targetBranch: change.target_environment_branch,
  targetHead: change.merge_base_head,
})))

const stages = [
  { key: 'validation', label: '验证' },
  { key: 'investigation', label: '排障' },
  { key: 'fix', label: '修复' },
  { key: 'merge', label: '合并' },
  { key: 'deploy', label: '部署' },
  { key: 'regression', label: '回归' },
] as const

const statusPosition: Record<CaseStatus, number> = {
  pending_validation: 0, validating: 0, waiting_evidence: 0, reproduced: 1, not_reproduced: 0,
  investigating: 1, root_cause_ready: 2, waiting_fix_approval: 2, fixing: 2, fix_failed: 2,
  fix_pushed: 3, waiting_merge_approval: 3, merging: 3, merge_conflict: 3,
  waiting_deployment: 4, deployment_unverified: 4, deployment_verified: 5,
  regression_validating: 5, fixed_verified: 6, still_reproduces: 1, legacy_archived: -1, reset_archived: -1,
}

const activeStatuses = new Set<CaseStatus>(['validating', 'investigating', 'fixing', 'merging', 'regression_validating'])
const blockedStatuses = new Set<CaseStatus>(['waiting_evidence', 'not_reproduced', 'fix_failed', 'merge_conflict', 'deployment_unverified', 'still_reproduces'])

function stageState(index: number): 'complete' | 'current' | 'blocked' | 'pending' | 'archived' {
  const status = currentCase.value?.status
  if (!status || status === 'legacy_archived' || status === 'reset_archived') return status ? 'archived' : 'pending'
  const position = statusPosition[status]
  if (index < position || position === 6) return 'complete'
  if (index > position) return 'pending'
  if (blockedStatuses.has(status)) return 'blocked'
  return 'current'
}

function stageStateLabel(index: number): string {
  return { complete: '已完成', current: activeStatuses.has(currentCase.value?.status as CaseStatus) ? '进行中' : '等待操作', blocked: '需处理', pending: '未开始', archived: '历史' }[stageState(index)]
}

function statusLabel(status: CaseStatus): string {
  const labels: Partial<Record<CaseStatus, string>> = {
    pending_validation: '等待验证', validating: '验证中', waiting_evidence: '等待证据', reproduced: '已复现', not_reproduced: '未复现',
    investigating: '排障中', root_cause_ready: '根因已确认', waiting_fix_approval: '等待修复授权', fixing: '修复中', fix_failed: '修复失败',
    fix_pushed: '修复已推送', waiting_merge_approval: '等待合并授权', merging: '合并中', merge_conflict: '合并冲突',
    waiting_deployment: '等待人工部署', deployment_unverified: '部署版本未确认', deployment_verified: '部署已确认', regression_validating: '回归中',
    fixed_verified: '修复已验证', still_reproduces: '回归仍复现', legacy_archived: '历史归档', reset_archived: '已重置归档',
  }
  return labels[status] || status
}

function fmtTime(value?: string): string {
  if (!value) return '-'
  const time = new Date(value)
  return Number.isNaN(time.getTime()) ? value : time.toLocaleString('zh-CN', { hour12: false })
}

async function openAction(event: MouseEvent) {
  if (!action.value || props.pending) return
  if (!action.value.approval && !['supply_evidence', 'continue_fix', 'supply_merge_decision', 'supply_deployment_proof'].includes(action.value.kind)) {
    emit('primary', { kind: action.value.kind })
    return
  }
  actionTrigger.value = event.currentTarget as HTMLElement
  if (action.value.kind === 'approve_fix') {
    dialogCaseVersion.value = props.detail?.case.version
    const currentAttemptID = props.detail?.case.current_attempt_id || ''
    const rootCause = props.detail?.attempts.find(attempt => attempt.id === currentAttemptID && attempt.phase === 'investigation' && attempt.status === 'succeeded')
    dialogRootCauseAttemptID.value = rootCause?.id || ''
  } else {
    dialogCaseVersion.value = undefined
    dialogRootCauseAttemptID.value = ''
  }
  dialogInput.value = ''
  dialogObservedCommits.value = Object.fromEntries(Object.keys(expectedDeploymentCommits.value).map(repo => [repo, '']))
  dialogOpen.value = true
  await nextTick()
  confirmButton.value?.focus()
}

function closeDialog() {
  if (props.pending) return
  dialogOpen.value = false
  nextTick(() => actionTrigger.value?.focus())
}

function confirmAction() {
  if (!action.value) return
  const payload: { kind: CasePrimaryAction['kind']; input?: string; observedVersion?: string; observedCommits?: Record<string, string>; versionSource?: string; rootCauseAttemptID?: string; caseVersion?: number } = { kind: action.value.kind }
  if (action.value.kind === 'approve_fix') {
    payload.rootCauseAttemptID = dialogRootCauseAttemptID.value
    payload.caseVersion = dialogCaseVersion.value
  }
  if (['supply_evidence', 'continue_fix', 'supply_merge_decision', 'supply_deployment_proof'].includes(action.value.kind)) payload.input = dialogInput.value.trim()
  if (action.value.kind === 'notify_deployed') {
    if (!automaticDeploymentVerification.value) {
      payload.observedVersion = dialogInput.value.trim()
      payload.observedCommits = Object.fromEntries(Object.entries(dialogObservedCommits.value).filter(([, commit]) => commit.trim()).map(([repo, commit]) => [repo, commit.trim()]))
    }
  }
  emit('primary', payload)
  dialogOpen.value = false
  nextTick(() => actionTrigger.value?.focus())
}

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
  if (action.value?.kind === 'approve_merge') return '确认合并环境分支'
  if (action.value?.kind === 'supply_merge_decision') return '提交合并冲突处理决定'
  if (action.value?.kind === 'notify_deployed') return '确认部署并校验版本'
  if (action.value?.kind === 'supply_deployment_proof') return '补充部署版本证明'
  return action.value?.label || '继续处理'
}
</script>

<template>
  <section class="case-lifecycle" data-responsive-viewports="375,768,1024,1440" data-overflow-safe="true">
    <aside class="case-column case-list-column" aria-labelledby="case-list-title">
      <div class="column-head">
        <div><h2 id="case-list-title">故障 Cases</h2><span>{{ cases.length }} 条</span></div>
        <button class="icon-button" type="button" aria-label="刷新 Case 列表" :disabled="pending" @click="emit('refresh')">
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 11a8 8 0 1 0-2.34 5.66M20 5v6h-6" /></svg>
        </button>
      </div>
      <p v-if="cases.length === 0" class="empty-state">暂无持久化 Case</p>
      <button v-for="incident in cases" :key="incident.id" type="button" class="case-row" :class="{ active: currentCase?.id === incident.id }" @click="emit('select', incident.id)">
        <strong>{{ incident.bug_id || incident.id }}</strong>
        <span class="status-text" :data-status="incident.status">{{ statusLabel(incident.status) }}</span>
        <small>{{ incident.environment || '环境未知' }} · 第 {{ incident.cycle_number }} 轮</small>
      </button>
    </aside>

    <main class="case-column case-main-column">
      <div v-if="!detail" class="empty-state">选择一个 Case 查看生命周期</div>
      <template v-else>
        <header class="case-heading">
          <div><span>Case {{ detail.case.id }}</span><h2>{{ detail.case.bug_id }}</h2></div>
          <span class="status-pill" :data-status="detail.case.status">{{ statusLabel(detail.case.status) }}</span>
        </header>

        <ol class="stage-progress" aria-label="故障处理阶段">
          <li v-for="(stage, index) in stages" :key="stage.key" class="lifecycle-stage" :data-state="stageState(index)">
            <span class="stage-marker" aria-hidden="true">{{ index + 1 }}</span>
            <span><strong>{{ stage.label }}</strong><small>{{ stageStateLabel(index) }}</small></span>
          </li>
        </ol>

        <section class="current-action-card" aria-labelledby="current-action-title">
          <div>
            <span>当前状态</span>
            <h3 id="current-action-title">{{ statusLabel(detail.case.status) }}</h3>
            <p v-if="detail.case.status === 'legacy_archived'">历史记录只读；继续时会通过 CreateAndStart 创建新的 Case，不修改归档 attempt。</p>
            <p v-else-if="detail.case.status === 'reset_archived'">历史记录只读；重置后的新 Case 已保留原闭环的证据和审计关系。</p>
            <p v-else-if="detail.case.status === 'waiting_deployment'">环境分支已推送。人工部署后，Studio 将先核对运行版本，再启动回归验证。</p>
            <p v-else-if="continuedAfterFailedRegression">第 {{ detail.case.cycle_number }} 轮 · 回归仍复现，Studio 已把本轮新证据和差分带入排障。</p>
            <p v-else>第 {{ detail.case.cycle_number }} 轮 · {{ detail.case.environment || '环境未知' }}</p>
          </div>
          <button v-if="action" class="btn primary primary-action" type="button" :disabled="pending" @click="openAction">
            {{ pending ? '处理中…' : action.label }}
          </button>
          <span v-else class="terminal-copy">{{ detail.case.status === 'fixed_verified' ? '闭环完成' : '当前阶段自动推进' }}</span>
        </section>

        <p class="live-error" role="status" aria-live="assertive">{{ error }}</p>

        <section class="timeline" aria-labelledby="timeline-title">
          <h3 id="timeline-title">过程时间线</h3>
          <ol aria-label="Case 时间线">
            <li v-for="event in [...detail.events].reverse()" :key="event.id">
              <span class="timeline-dot" aria-hidden="true"></span>
              <div><strong>{{ event.event_type }}</strong><span>{{ statusLabel(event.from_status) }} → {{ statusLabel(event.to_status) }}</span><small>{{ fmtTime(event.created_at) }} · {{ event.actor_type }}</small></div>
            </li>
          </ol>
          <p v-if="detail.events.length === 0" class="empty-state">暂无状态事件</p>
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
        <p v-if="action.kind === 'approve_fix'">将授权修复 Agent 基于当前根因和证据创建最小修复。授权范围：Case v{{ dialogCaseVersion }} / {{ dialogRootCauseAttemptID || '未找到根因 attempt' }}。</p>
        <template v-else-if="action.kind === 'approve_merge'">
          <p>将按以下精确 scope 合并并通过 SSH 推送；Studio 会重新检查目标 HEAD，任何变化都会使本次授权失效。</p>
          <dl class="deployment-preview">
            <div v-for="scope in mergeApprovalScopes" :key="scope.repo"><dt>{{ scope.repo }}</dt><dd><code>{{ scope.fixCommit }} → {{ scope.targetBranch }} @ {{ scope.targetHead || '待重新检查' }}</code></dd></div>
          </dl>
        </template>
        <p v-else-if="action.kind === 'supply_merge_decision'">记录冲突处理结果并返回合并授权门，不会在这一步直接重新合并。</p>
        <p v-else-if="action.kind === 'notify_deployed'">Studio 不执行部署；这里只记录人工部署通知，并核验运行版本是否包含目标 commit。</p>
        <p v-else-if="action.kind === 'supply_deployment_proof'">补充可核验的部署证明并返回人工部署确认门，不会在这一步直接启动回归。</p>
        <dl v-if="['notify_deployed', 'supply_deployment_proof'].includes(action.kind)" class="deployment-preview">
          <div><dt>目标环境</dt><dd>{{ detail?.case.environment || '未知' }}</dd></div>
          <div><dt>期望 commits</dt><dd><code v-for="(commit, repo) in expectedDeploymentCommits" :key="repo">{{ repo }}: {{ commit }}</code><span v-if="Object.keys(expectedDeploymentCommits).length === 0">尚未记录</span></dd></div>
          <div><dt>版本来源</dt><dd>{{ deploymentVersionSource }}<small v-if="detail?.deployment_verification?.hint"> · {{ detail.deployment_verification.hint }}</small></dd></div>
        </dl>
        <p v-if="action.kind === 'notify_deployed' && automaticDeploymentVerification">确认后将按上述服务端配置自动读取运行版本，无需手工填写版本或 commit。</p>
        <label v-if="action.kind === 'notify_deployed' && !automaticDeploymentVerification" for="observed-version">已部署版本</label>
        <input v-if="action.kind === 'notify_deployed' && !automaticDeploymentVerification" id="observed-version" v-model="dialogInput" type="text" placeholder="例如 build-20260711 或 commit SHA">
        <fieldset v-if="action.kind === 'notify_deployed' && !automaticDeploymentVerification" class="observed-commits">
          <legend>各仓库观测 commit（可选；留空将无法确认该仓库）</legend>
          <label v-for="(_, repo) in expectedDeploymentCommits" :key="repo" :for="`observed-commit-${repo}`">
            <span>{{ repo }}</span>
            <input :id="`observed-commit-${repo}`" v-model="dialogObservedCommits[repo]" type="text" :placeholder="`期望 ${expectedDeploymentCommits[repo]}`">
          </label>
        </fieldset>
        <label v-if="['supply_evidence', 'continue_fix', 'supply_merge_decision', 'supply_deployment_proof'].includes(action.kind)" for="case-supplement">补充信息</label>
        <textarea v-if="['supply_evidence', 'continue_fix', 'supply_merge_decision', 'supply_deployment_proof'].includes(action.kind)" id="case-supplement" v-model="dialogInput" rows="5" placeholder="输入新证据、处理决定、版本证明或测试信息"></textarea>
        <footer>
          <button class="btn" type="button" :disabled="pending" @click="closeDialog">取消</button>
          <button ref="confirmButton" class="btn primary" data-confirm type="button" :disabled="pending || (action.kind === 'approve_fix' && (!dialogRootCauseAttemptID || dialogCaseVersion === undefined)) || (action.kind === 'notify_deployed' && !automaticDeploymentVerification && !dialogInput.trim()) || (['supply_evidence', 'continue_fix', 'supply_merge_decision', 'supply_deployment_proof'].includes(action.kind) && !dialogInput.trim())" @click="confirmAction">确认</button>
        </footer>
      </section>
    </div>
  </section>
</template>

<style scoped>
.case-lifecycle { width: 100%; min-width: 0; display: grid; grid-template-columns: minmax(210px, .72fr) minmax(420px, 1.65fr) minmax(270px, 1fr); gap: var(--sp-3); color: var(--c-text); }
.case-column { min-width: 0; border: 1px solid var(--c-line); border-radius: var(--r-lg); background: var(--c-surf-2); padding: var(--sp-3); overflow-wrap: anywhere; }
.case-list-column { display: flex; flex-direction: column; gap: var(--sp-2); }
.column-head, .column-head > div, .case-heading, .current-action-card { display: flex; align-items: center; justify-content: space-between; gap: var(--sp-2); }
.column-head > div { align-items: baseline; justify-content: flex-start; }
h2, h3, p { margin: 0; }
.column-head h2, .case-heading h2 { color: var(--c-ink); font-size: var(--fs-lg); }
.column-head span, .case-heading span, .current-action-card span { color: var(--c-muted); font-size: var(--fs-sm); }
.icon-button { width: 44px; height: 44px; display: grid; place-items: center; border: 1px solid var(--c-line-2); border-radius: var(--r-md); background: var(--c-surf); color: var(--c-text); cursor: pointer; }
.icon-button svg { width: 19px; fill: none; stroke: currentColor; stroke-width: 1.8; stroke-linecap: round; stroke-linejoin: round; }
.case-row { width: 100%; min-height: 62px; display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 4px 8px; padding: 10px; text-align: left; border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf); color: var(--c-text); font: inherit; cursor: pointer; }
.case-row:hover { border-color: #93c5fd; background: #eff6ff; }
.case-row.active { border-color: var(--c-accent); box-shadow: inset 3px 0 var(--c-accent); }
.case-row strong { min-width: 0; color: var(--c-ink); font-size: var(--fs-base); }
.case-row small { grid-column: 1 / -1; color: var(--c-muted); font-size: var(--fs-xs); }
.status-text, .status-pill { display: inline-flex; align-items: center; gap: 5px; color: var(--c-muted); font-size: var(--fs-xs); }
.status-text::before, .status-pill::before { content: ''; width: 7px; height: 7px; border-radius: 50%; background: #94a3b8; }
[data-status="fixed_verified"]::before, [data-status="deployment_verified"]::before { background: #15803d; }
[data-status="waiting_evidence"]::before, [data-status="fix_failed"]::before, [data-status="merge_conflict"]::before, [data-status="deployment_unverified"]::before { background: #c2410c; }
[data-status="validating"]::before, [data-status="investigating"]::before, [data-status="fixing"]::before, [data-status="merging"]::before, [data-status="regression_validating"]::before { background: #2563eb; }
.case-main-column { display: flex; flex-direction: column; gap: var(--sp-4); background: var(--c-surf); }
.case-heading > div { min-width: 0; }
.case-heading > div > span { display: block; margin-bottom: 2px; }
.status-pill { flex: 0 0 auto; padding: 6px 9px; border: 1px solid var(--c-line); border-radius: 999px; background: var(--c-surf-2); }
.stage-progress { display: grid; grid-template-columns: repeat(6, minmax(60px, 1fr)); gap: 5px; margin: 0; padding: 0; list-style: none; }
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
.current-action-card { align-items: flex-end; padding: var(--sp-4); border: 1px solid var(--c-line); border-left: 3px solid var(--c-accent); border-radius: var(--r-lg); background: var(--c-surf-2); }
.current-action-card > div { min-width: 0; }
.current-action-card h3 { margin: 3px 0; color: var(--c-ink); font-size: var(--fs-lg); }
.current-action-card p { max-width: 62ch; color: var(--c-muted); font-size: var(--fs-sm); line-height: 1.55; }
.primary-action { min-height: 44px; flex: 0 0 auto; }
.terminal-copy { padding: 8px 0; font-weight: 600; }
.live-error { min-height: 1.5em; color: var(--c-danger); font-size: var(--fs-sm); }
.live-error:empty { display: none; }
.timeline h3 { margin-bottom: var(--sp-3); color: var(--c-ink); font-size: var(--fs-base); }
.timeline ol { margin: 0; padding: 0; list-style: none; }
.timeline li { display: grid; grid-template-columns: 14px minmax(0, 1fr); gap: var(--sp-2); padding-bottom: var(--sp-3); }
.timeline-dot { width: 9px; height: 9px; margin-top: 4px; border: 2px solid #93c5fd; border-radius: 50%; background: var(--c-surf); box-shadow: 0 0 0 3px #eff6ff; }
.timeline li > div { min-width: 0; display: grid; gap: 2px; }
.timeline strong { color: var(--c-ink); font-size: var(--fs-sm); }
.timeline span, .timeline small { color: var(--c-muted); font-size: var(--fs-xs); }
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
.observed-commits { display: grid; gap: var(--sp-2); margin: 0; padding: var(--sp-3); border: 1px solid var(--c-line); border-radius: var(--r-md); }
.observed-commits legend { padding: 0 4px; color: var(--c-muted); font-size: var(--fs-sm); }
.observed-commits label { display: grid; grid-template-columns: minmax(90px, .35fr) minmax(0, 1fr); align-items: center; gap: var(--sp-2); }
.approval-dialog input, .approval-dialog textarea { min-height: 44px; }
.approval-dialog footer { display: flex; justify-content: flex-end; gap: var(--sp-2); }
.approval-dialog footer .btn { min-height: 44px; min-width: 88px; justify-content: center; }
button:focus-visible, input:focus-visible, textarea:focus-visible { outline: 3px solid rgba(37, 99, 235, .55); outline-offset: 2px; }
@media (max-width: 1180px) {
  .case-lifecycle { grid-template-columns: minmax(190px, .7fr) minmax(0, 1.6fr); }
  .case-detail-column { grid-column: 1 / -1; }
  .case-detail-column :deep(.artifact-sections) { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
@media (max-width: 899px) {
  .case-lifecycle { grid-template-columns: minmax(0, 1fr); }
  .case-detail-column { grid-column: auto; }
  .case-detail-column :deep(.artifact-sections) { grid-template-columns: minmax(0, 1fr); }
  .case-list-column { max-height: 310px; overflow-y: auto; }
  .current-action-card { align-items: stretch; flex-direction: column; }
  .primary-action { width: 100%; justify-content: center; }
}
@media (max-width: 560px) {
  .stage-progress { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .case-heading { align-items: flex-start; flex-direction: column; }
  .approval-dialog footer { flex-direction: column-reverse; }
  .approval-dialog footer .btn { width: 100%; }
}
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after { scroll-behavior: auto !important; transition-duration: .01ms !important; animation-duration: .01ms !important; }
}
</style>
