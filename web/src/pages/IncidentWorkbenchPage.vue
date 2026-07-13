<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import BugBotPicker from '../components/BugBotPicker.vue'
import BugCaseLifecycle, { type CasePrimaryAction } from '../components/BugCaseLifecycle.vue'
import BugTicketDetail from '../components/BugTicketDetail.vue'
import BugTicketList from '../components/BugTicketList.vue'
import {
  approveIncidentFix,
  approveIncidentMerge,
  cancelIncidentAttempt,
  continueIncidentCase,
  fetchBugByID,
  getIncidentCase,
  listBugs,
  listIncidentCases,
  matchBugBots,
  notifyIncidentDeployed,
  resetIncidentCase,
  saveBugSelectedBot,
  startIncidentCase,
  type BotMatch,
  type BotRef,
  type IncidentCase,
  type IncidentCaseDetail,
} from '../lib/bridge'
import { toast, toastError } from '../lib/toast'
import { useBugTickets } from '../lib/useBugTickets'
import { activeCaseForBug, botKeyForLegacyContinuation, casesForBug, continuationForDetail, useIncidentCase } from '../lib/useIncidentCase'

const route = useRoute()
const router = useRouter()
const tickets = useBugTickets({ listBugs, fetchBugByID })
const incidentWorkflow = useIncidentCase({ listCases: listIncidentCases, getCase: getIncidentCase })
const matches = ref<BotMatch[]>([])
const selectedBotKey = ref('')
const explicitlySelectedBots = ref<Record<string, string>>({})
const matching = ref(false)
const botError = ref('')
const starting = ref(false)
const workflowNotice = ref('')
const startCaseIDs = new Map<string, string>()
type ResetDialogSnapshot = {
  bugID: string
  caseID: string
  caseVersion: number
  botKey: string
  newCaseID: string
  idempotencyKey: string
}
const resetDialog = ref<ResetDialogSnapshot | null>(null)
const resetting = ref(false)
const resetError = ref('')
const resetDialogElement = ref<HTMLElement | null>(null)
const resetCancelButton = ref<HTMLButtonElement | null>(null)
const resetTrigger = ref<HTMLElement | null>(null)
const resetRequests = new Map<string, Pick<ResetDialogSnapshot, 'newCaseID' | 'idempotencyKey'>>()

const requestedBugID = routeBugID()
if (requestedBugID) tickets.select(requestedBugID)

const selectedBugCases = computed(() => casesForBug(incidentWorkflow.cases.value, tickets.selectedID.value))
const selectedActiveCase = computed(() => activeCaseForBug(incidentWorkflow.cases.value, tickets.selectedID.value))
const newestSelectedCase = computed(() => selectedBugCases.value[0])
const preferredCase = computed(() => selectedActiveCase.value || newestSelectedCase.value)
const displayedCase = computed(() => selectedBugCases.value.find(item => item.id === incidentWorkflow.selectedCaseID.value) || preferredCase.value)
const displayedDetail = computed(() => incidentWorkflow.detail.value?.case.id === displayedCase.value?.id ? incidentWorkflow.detail.value : null)
const allCasesTerminal = computed(() => selectedBugCases.value.length > 0 && !selectedActiveCase.value)
const invalidURLBug = computed(() => Boolean(requestedBugID && !tickets.loading.value && tickets.bugs.value.length > 0 && !tickets.selectedBug.value))
const selectedBot = computed(() => matches.value.find(match => match.bot.key === selectedBotKey.value)?.bot)
const pickerSelectedBotKey = computed(() => {
  const detail = displayedDetail.value
  const bug = tickets.selectedBug.value
  if (!detail || !bug || detail.case.status !== 'legacy_archived') return selectedBotKey.value
  return explicitlySelectedBots.value[bug.id] || botKeyForLegacyContinuation(detail, bug.id, '')
})
const selectedBotSupportsStart = computed(() => !selectedBot.value || ['codex', 'claude-code', 'openclaw'].includes(selectedBot.value.target))
const showStandaloneStart = computed(() => {
  if (!tickets.selectedBug.value || selectedActiveCase.value) return false
  if (!displayedCase.value) return true
  return displayedCase.value.status !== 'legacy_archived'
})
const standaloneStartLabel = computed(() => allCasesTerminal.value ? '开始新一轮' : '开始故障闭环')
const standaloneStartDisabled = computed(() => starting.value || incidentWorkflow.pending.value || !selectedBotSupportsStart.value || !startBotChoice(displayedDetail.value).key)

watch(() => tickets.selectedID.value, async bugID => {
  workflowNotice.value = ''
  if (resetDialog.value && resetDialog.value.bugID !== bugID) discardResetDialog()
  if (!bugID || !tickets.selectedBug.value) {
    matches.value = []
    selectedBotKey.value = ''
    return
  }
  await Promise.all([refreshMatches(bugID), openPreferredCase()])
})

watch(incidentWorkflow.cases, () => {
  void openPreferredCase()
})

onMounted(async () => {
  try {
    await tickets.load()
    if (tickets.selectedBug.value) {
      await Promise.all([refreshMatches(tickets.selectedBug.value.id), openPreferredCase()])
    }
    if (!requestedBugID && tickets.selectedID.value) {
      await router.replace({ query: { ...route.query, bug_id: tickets.selectedID.value } })
    }
  } catch (error) {
    toastError('读取 Bug 工单', error)
  }
})

function routeBugID(): string {
  return typeof route.query.bug_id === 'string' ? route.query.bug_id : ''
}

async function selectBug(id: string) {
  tickets.select(id)
  await router.replace({ query: { ...route.query, bug_id: id } })
}

async function refreshTickets() {
  try {
    await tickets.load()
  } catch (error) {
    toastError('读取 Bug 工单', error)
  }
}

async function refreshMatches(bugID: string) {
  matching.value = true
  botError.value = ''
  try {
    const next = await matchBugBots(bugID)
    if (tickets.selectedID.value !== bugID) return
    matches.value = next
    const preferred = tickets.selectedBug.value?.selected_bot_key || ''
    selectedBotKey.value = next.some(match => match.bot.key === preferred) ? preferred : next[0]?.bot.key || ''
  } catch (error) {
    if (tickets.selectedID.value !== bugID) return
    matches.value = []
    selectedBotKey.value = ''
    botError.value = error instanceof Error ? error.message : String(error)
    toastError('匹配排障机器人', error)
  } finally {
    if (tickets.selectedID.value === bugID) matching.value = false
  }
}

async function rememberSelectedBot(botKey: string) {
  selectedBotKey.value = botKey
  const bug = tickets.selectedBug.value
  if (!bug || !botKey) return
  explicitlySelectedBots.value = { ...explicitlySelectedBots.value, [bug.id]: botKey }
  tickets.bugs.value = tickets.bugs.value.map(item => item.id === bug.id ? { ...item, selected_bot_key: botKey } : item)
  try {
    await saveBugSelectedBot({ bug_id: bug.id, bot_key: botKey })
  } catch (error) {
    toastError('保存机器人选择', error)
  }
}

async function openPreferredCase() {
  const target = preferredCase.value
  if (!target || incidentWorkflow.selectedCaseID.value === target.id && incidentWorkflow.detail.value?.case.id === target.id) return
  try {
    await incidentWorkflow.selectCase(target.id)
  } catch { /* controller exposes a recoverable live error */ }
}

async function selectWorkflowCase(caseID: string) {
  try {
    await incidentWorkflow.selectCase(caseID)
  } catch (error) {
    toastError('读取故障 Case', error)
  }
}

type StartBotChoice = { key: string; bot?: BotRef }

function startBotChoice(terminalDetail: IncidentCaseDetail | null): StartBotChoice {
  const bug = tickets.selectedBug.value
  if (!bug) return { key: '' }
  let key = selectedBotKey.value.trim()
  if (terminalDetail?.case.status === 'legacy_archived') {
    key = explicitlySelectedBots.value[bug.id] || botKeyForLegacyContinuation(terminalDetail, bug.id, '')
  }
  return { key, bot: matches.value.find(match => match.bot.key === key)?.bot }
}

function freshCaseID(bugID: string): string {
  const safeBugID = bugID.replace(/[^a-zA-Z0-9_-]/g, '-')
  return `case-${safeBugID}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
}

function freshResetCaseID(caseID: string): string {
  const safeCaseID = caseID.replace(/[^a-zA-Z0-9_-]/g, '-')
  return `case-reset-${safeCaseID}-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
}

async function openResetDialog(incident: IncidentCase) {
  if (resetting.value) return
  const bugID = tickets.selectedID.value
  const identity = `${incident.id}:v${incident.version}:${incident.selected_bot_key}`
  let request = resetRequests.get(identity)
  if (!request) {
    const newCaseID = freshResetCaseID(incident.id)
    request = { newCaseID, idempotencyKey: `reset:${incident.id}:v${incident.version}:${newCaseID}` }
    resetRequests.set(identity, request)
  }
  resetTrigger.value = document.activeElement instanceof HTMLElement ? document.activeElement : null
  resetError.value = incident.selected_bot_key ? '' : '当前 Case 没有绑定排障机器人，无法创建接替 Case。'
  resetDialog.value = {
    bugID,
    caseID: incident.id,
    caseVersion: incident.version,
    botKey: incident.selected_bot_key,
    ...request,
  }
  await nextTick()
  resetCancelButton.value?.focus()
}

function discardResetDialog() {
  resetDialog.value = null
  resetError.value = ''
}

function closeResetDialog() {
  if (resetting.value) return
  const trigger = resetTrigger.value
  discardResetDialog()
  nextTick(() => {
    if (trigger?.isConnected) trigger.focus()
  })
}

function trapResetDialogFocus(event: KeyboardEvent) {
  if (event.key !== 'Tab' || !resetDialogElement.value) return
  const focusable = [...resetDialogElement.value.querySelectorAll<HTMLElement>('button:not(:disabled), [href]:not([aria-disabled="true"]), input:not(:disabled), textarea:not(:disabled)')]
  if (focusable.length === 0) {
    event.preventDefault()
    resetDialogElement.value.focus()
    return
  }
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

async function confirmReset() {
  const request = resetDialog.value
  if (!request || resetting.value || !request.botKey) return
  const isOriginalCurrent = () => {
    const current = displayedDetail.value?.case
    return isCurrentBug(request.bugID) && current?.id === request.caseID && current.version === request.caseVersion
  }
  resetting.value = true
  resetError.value = ''
  try {
    const replacement = await incidentWorkflow.runOnce(request.idempotencyKey, () => resetIncidentCase({
      case_id: request.caseID,
      new_case_id: request.newCaseID,
      expected_version: request.caseVersion,
      idempotency_key: request.idempotencyKey,
      actor_id: 'desktop-user',
      bot_key: request.botKey,
    }))
    if (!isOriginalCurrent()) return
    const snapshot = await getIncidentCase(replacement.id)
    if (!isOriginalCurrent()) return
    incidentWorkflow.applySnapshot(snapshot)
    resetting.value = false
    closeResetDialog()
    await incidentWorkflow.refreshCases()
    if (!isCurrentBug(request.bugID) || displayedDetail.value?.case.id !== replacement.id) return
    toast.success('Case 已重置，接替 Case 已创建')
  } catch (error) {
    if (!isOriginalCurrent()) return
    const message = error instanceof Error ? error.message : String(error)
    resetError.value = message
    incidentWorkflow.error.value = message
    toastError('重置故障 Case', error)
  } finally {
    resetting.value = false
  }
}

async function startNewCase(terminalDetail: IncidentCaseDetail | null = displayedDetail.value) {
  const bug = tickets.selectedBug.value
  if (!bug) return
  const initiatingBugID = bug.id
  const choice = startBotChoice(terminalDetail)
  if (!choice.key) {
    const error = new Error('该历史记录没有机器人信息。请重新选择当前 Bug 的机器人后再继续。')
    incidentWorkflow.error.value = error.message
    toastError('启动故障闭环', error)
    return
  }
  const roundIdentity = `${bug.id}:${displayedCase.value?.id || 'none'}:${displayedCase.value?.version || 0}:${choice.key}`
  const actionKey = `start-round:${roundIdentity}`
  let candidateID = startCaseIDs.get(roundIdentity)
  if (!candidateID) {
    candidateID = freshCaseID(bug.id)
    startCaseIDs.set(roundIdentity, candidateID)
  }
  starting.value = true
  workflowNotice.value = ''
  try {
    const candidate = candidateID
    const opened = await incidentWorkflow.runOnce(actionKey, () => startIncidentCase({
      case_id: candidate,
      bug_id: bug.id,
      bot_key: choice.key,
      expected_version: 0,
      idempotency_key: `start:${candidate}`,
      actor_id: 'desktop-user',
      input_json: {
        mode: 'reproduce',
        expected_behavior: bug.title || '',
        bug_steps: bug.steps || '',
        target_environment: choice.bot?.env || '',
      },
    }))
    if (!isCurrentBug(initiatingBugID)) return
    const refreshed = await refreshCaseSnapshotIfCurrent(opened.id, () => isCurrentBug(initiatingBugID))
    if (!refreshed || !isCurrentBug(initiatingBugID)) return
    if (opened.id !== candidate) {
      workflowNotice.value = '已打开现有闭环'
      toast.info('已打开现有闭环')
    } else {
      workflowNotice.value = allCasesTerminal.value ? '新一轮故障闭环已启动' : '故障闭环已启动'
      toast.success(workflowNotice.value)
    }
  } catch (error) {
    if (!isCurrentBug(initiatingBugID)) return
    incidentWorkflow.error.value = error instanceof Error ? error.message : String(error)
    toastError('启动故障闭环', error)
  } finally {
    starting.value = false
  }
}

function isCurrentBug(bugID: string): boolean {
  return tickets.selectedID.value === bugID && tickets.selectedBug.value?.id === bugID
}

async function refreshCaseSnapshotIfCurrent(caseID: string, isCurrent: () => boolean): Promise<boolean> {
  if (!isCurrent()) return false
  try {
    const snapshot = await getIncidentCase(caseID)
    if (!isCurrent()) return false
    incidentWorkflow.applySnapshot(snapshot)
    return true
  } catch (error) {
    if (!isCurrent()) return false
    throw error
  }
}

async function refreshIncidentWorkflow() {
  try {
    await incidentWorkflow.refreshCases()
    await openPreferredCase()
  } catch (error) {
    toastError('刷新故障 Case', error)
  }
}

async function handleIncidentPrimary(payload: { kind: CasePrimaryAction['kind']; input?: string; observedVersion?: string; observedCommits?: Record<string, string>; versionSource?: string; rootCauseAttemptID?: string; caseVersion?: number }) {
  const detail = displayedDetail.value
  if (!detail) return
  const incident = detail.case
  if (payload.kind === 'continue_legacy') {
    await startNewCase(detail)
    return
  }
  const context = {
    bugID: tickets.selectedID.value,
    caseID: incident.id,
    caseVersion: incident.version,
  }
  const isCurrent = () => {
    const current = displayedDetail.value?.case
    return isCurrentBug(context.bugID) && current?.id === context.caseID && current.version === context.caseVersion
  }
  const key = `${payload.kind}:${incident.id}:v${incident.version}`
  try {
    const updated = await incidentWorkflow.runOnce(key, async (): Promise<IncidentCase> => {
      const base = { case_id: incident.id, expected_version: incident.version, idempotency_key: key, actor_id: 'desktop-user' }
      if (payload.kind === 'start_validation') {
        if (!incident.selected_bot_key) throw new Error('当前 Case 没有绑定排障机器人')
        return startIncidentCase({ ...base, bug_id: incident.bug_id, bot_key: incident.selected_bot_key, input_json: { mode: 'reproduce' } })
      }
      if (payload.kind === 'supply_evidence' || payload.kind === 'continue_fix') {
        return continueIncidentCase({ ...base, ...continuationForDetail(detail, payload.input || '') })
      }
      if (payload.kind === 'supply_merge_decision') {
        return continueIncidentCase({ ...base, phase: 'fix', input_json: { decision: 'resolve_merge_conflict', evidence: payload.input || '' } })
      }
      if (payload.kind === 'supply_deployment_proof') {
        return continueIncidentCase({ ...base, phase: 'regression', input_json: { decision: 'update_deployment_proof', evidence: payload.input || '' } })
      }
      if (payload.kind === 'approve_fix') {
        if (!payload.rootCauseAttemptID || payload.caseVersion === undefined) throw new Error('修复授权缺少对话框中的根因或 Case 版本快照')
        return approveIncidentFix({
          ...base,
          expected_version: payload.caseVersion,
          idempotency_key: `start-fix:${incident.id}:${payload.rootCauseAttemptID}:${payload.caseVersion}`,
          root_cause_attempt_id: payload.rootCauseAttemptID,
        })
      }
      if (payload.kind === 'approve_merge') {
        return approveIncidentMerge({
          ...base,
          fix_commits: Object.fromEntries(detail.code_changes.map(change => [change.repo, change.fix_commit])),
          target_branches: Object.fromEntries(detail.code_changes.map(change => [change.repo, change.target_environment_branch])),
          target_heads: Object.fromEntries(detail.code_changes.map(change => [change.repo, change.merge_base_head])),
        })
      }
      if (payload.kind === 'notify_deployed') {
        return notifyIncidentDeployed({ ...base, observed_version: payload.observedVersion || '', observed_commits: payload.observedCommits || {} })
      }
      if (payload.kind === 'cancel_attempt') {
        if (!incident.current_attempt_id) throw new Error('当前没有可停止的阶段')
        return cancelIncidentAttempt({ ...base, attempt_id: incident.current_attempt_id })
      }
      throw new Error(`暂不支持操作 ${payload.kind}`)
    })
    if (!isCurrent()) return
    const refreshed = await refreshCaseSnapshotIfCurrent(updated.id, isCurrent)
    if (!refreshed) return
    toast.success('操作已提交')
  } catch (error) {
    if (!isCurrent()) return
    incidentWorkflow.error.value = error instanceof Error ? error.message : String(error)
    toastError('执行故障流程操作', error)
  }
}
</script>

<template>
  <div class="incident-workbench-page" data-responsive-viewports="375,768,1024,1440" data-overflow-safe="true">
    <header class="incident-header">
      <div>
        <h1>故障闭环</h1>
        <p>从 Bug 工单选择入口，打开已有 Case 或启动一轮可恢复的验证、排障与修复流程。</p>
      </div>
      <button class="btn" type="button" :disabled="tickets.loading.value" @click="refreshTickets">刷新 Bug</button>
    </header>

    <section class="selection-workspace" aria-label="Bug 驱动的故障闭环选择">
      <aside class="selection-panel ticket-list-panel">
        <BugTicketList
          :bugs="tickets.filteredBugs.value"
          :selected-id="tickets.selectedID.value"
          :loading="tickets.loading.value"
          :query="tickets.query.value"
          @select="selectBug"
          @update:query="tickets.query.value = $event"
        />
      </aside>

      <main class="selection-panel ticket-summary-panel">
        <p v-if="invalidURLBug" class="invalid-bug-state" role="status">
          URL 中的 Bug 不存在。请从左侧选择一条可用工单，页面会更新链接并继续。
        </p>
        <BugTicketDetail :bug="tickets.selectedBug.value" mode="summary" />

        <section v-if="showStandaloneStart" class="start-card" aria-labelledby="start-card-title">
          <div>
            <span>{{ allCasesTerminal ? '历史已闭环' : '尚未建立 Case' }}</span>
            <h2 id="start-card-title">{{ allCasesTerminal ? '可以开始新一轮验证' : '为当前 Bug 建立故障闭环' }}</h2>
            <p>{{ allCasesTerminal ? '历史 Case 保持只读，新一轮会使用新的 Case ID。' : '启动前请确认右侧机器人和目标环境。' }}</p>
          </div>
          <button class="btn primary" type="button" data-action="start-case" :disabled="standaloneStartDisabled" @click="startNewCase()">
            {{ starting ? '启动中…' : standaloneStartLabel }}
          </button>
        </section>
        <p v-if="workflowNotice" class="workflow-notice" role="status" aria-live="polite">{{ workflowNotice }}</p>
      </main>

      <aside class="selection-panel bot-panel">
        <BugBotPicker :matches="matches" :selected-key="pickerSelectedBotKey" :loading="matching" @select="rememberSelectedBot" />
        <p v-if="botError" class="live-error" role="status">{{ botError }}</p>
        <p v-else-if="selectedBot && !selectedBotSupportsStart" class="support-note">{{ selectedBot.target }} 暂不支持由 Studio 后台启动，请选择 Codex、Claude Code 或 OpenClaw。</p>
      </aside>
    </section>

    <BugCaseLifecycle
      v-if="displayedCase && displayedDetail"
      :cases="selectedBugCases"
      :detail="displayedDetail"
      :pending="incidentWorkflow.pending.value || starting"
      :error="incidentWorkflow.error.value"
      @select="selectWorkflowCase"
      @refresh="refreshIncidentWorkflow"
      @primary="handleIncidentPrimary"
      @reset="openResetDialog"
    />
    <p v-else-if="displayedCase" class="case-loading" role="status" aria-live="polite">正在加载 Case {{ displayedCase.id }}…</p>

    <div v-if="resetDialog" class="reset-dialog-backdrop" @click.self="closeResetDialog" @keydown.esc="closeResetDialog">
      <section ref="resetDialogElement" role="dialog" aria-modal="true" aria-labelledby="reset-dialog-title" aria-describedby="reset-dialog-description" class="reset-dialog" tabindex="-1" @keydown="trapResetDialogFocus">
        <header>
          <span>危险操作</span>
          <h2 id="reset-dialog-title">重置并新建 Case</h2>
        </header>
        <p id="reset-dialog-description">当前 Case 将归档为“已重置归档”，并创建一个绑定同一机器人的新 Case，从验证阶段重新开始。</p>
        <p class="reset-warning" role="note"><strong>重置不会撤销已发生的提交、推送或部署。</strong>原 Case、证据和审计记录保持不可变；外部副作用需要人工另行处理。</p>
        <dl class="reset-scope">
          <div><dt>原 Case</dt><dd>{{ resetDialog.caseID }} · v{{ resetDialog.caseVersion }}</dd></div>
          <div><dt>绑定机器人</dt><dd>{{ resetDialog.botKey || '未绑定' }}</dd></div>
          <div><dt>接替 Case</dt><dd>{{ resetDialog.newCaseID }}</dd></div>
        </dl>
        <p data-reset-error class="reset-live-error" role="status" aria-live="assertive">{{ resetError }}</p>
        <footer>
          <button ref="resetCancelButton" class="btn" data-reset-cancel type="button" :disabled="resetting" @click="closeResetDialog">取消</button>
          <button class="btn danger" data-reset-confirm type="button" :disabled="resetting || !resetDialog.botKey" @click="confirmReset">{{ resetting ? '重置中…' : '确认重置并新建' }}</button>
        </footer>
      </section>
    </div>
  </div>
</template>

<style scoped>
.incident-workbench-page { min-width: 0; display: grid; gap: var(--sp-3); color: var(--c-text); }
.incident-header { min-width: 0; display: flex; align-items: flex-start; justify-content: space-between; gap: var(--sp-3); }
.incident-header h1 { margin: 0; color: var(--c-ink); font-size: 24px; }
.incident-header p { max-width: 760px; margin: 4px 0 0; color: var(--c-muted); font-size: var(--fs-sm); line-height: 1.55; }
.btn { min-height: 44px; padding: 0 12px; border: 1px solid var(--c-line-2); border-radius: var(--r-md); background: var(--c-surf); color: var(--c-text); font: inherit; cursor: pointer; }
.btn:hover:not(:disabled) { border-color: var(--c-accent); background: var(--c-surf-2); }
.btn:focus-visible { outline: 2px solid var(--c-accent-hover); outline-offset: 2px; }
.btn:disabled { opacity: .55; cursor: not-allowed; }
.btn.primary { border-color: var(--c-accent-hover); background: var(--c-accent-hover); color: white; }
.btn.danger { border-color: #b91c1c; background: #b91c1c; color: white; }
.btn.danger:hover:not(:disabled) { border-color: #991b1b; background: #991b1b; }
.selection-workspace { min-width: 0; display: grid; grid-template-columns: minmax(220px, .8fr) minmax(300px, 1.35fr) minmax(240px, .9fr); align-items: start; gap: var(--sp-3); }
.selection-panel { min-width: 0; padding: var(--sp-3); border: 1px solid var(--c-line); border-radius: var(--r-lg); background: var(--c-surf); }
.ticket-list-panel { max-height: min(560px, 58vh); overflow: auto; }
.ticket-summary-panel { display: grid; gap: var(--sp-3); }
.bot-panel { max-height: min(560px, 58vh); overflow: auto; }
.invalid-bug-state, .case-loading, .support-note, .live-error, .workflow-notice { min-width: 0; margin: 0; padding: 10px 12px; overflow-wrap: anywhere; border-radius: var(--r-md); font-size: var(--fs-sm); line-height: 1.5; }
.invalid-bug-state { border: 1px solid #fbbf24; background: #fffbeb; color: #92400e; }
.case-loading { min-height: 64px; display: grid; place-items: center; border: 1px dashed var(--c-line-2); color: var(--c-muted); }
.support-note { margin-top: var(--sp-2); background: var(--c-surf-2); color: var(--c-muted); }
.live-error { margin-top: var(--sp-2); border: 1px solid #fecaca; background: #fef2f2; color: #b91c1c; }
.workflow-notice { border: 1px solid #bbf7d0; background: #f0fdf4; color: #166534; }
.start-card { min-width: 0; padding: var(--sp-3); display: flex; align-items: center; justify-content: space-between; gap: var(--sp-3); border: 1px solid #bfdbfe; border-radius: var(--r-lg); background: #eff6ff; }
.start-card div { min-width: 0; }
.start-card span { color: #1d4ed8; font-size: var(--fs-xs); font-weight: 700; }
.start-card h2 { margin: 3px 0 0; color: var(--c-ink); font-size: var(--fs-md); }
.start-card p { margin: 4px 0 0; color: var(--c-muted); font-size: var(--fs-sm); }
.start-card .btn { flex: 0 0 auto; }
.reset-dialog-backdrop { position: fixed; inset: 0; z-index: 60; display: grid; place-items: center; padding: var(--sp-4); background: rgba(15, 23, 42, .6); }
.reset-dialog { width: min(560px, 100%); max-height: calc(100vh - 32px); overflow: auto; box-sizing: border-box; display: grid; gap: var(--sp-3); padding: var(--sp-5); border: 1px solid #fecaca; border-radius: var(--r-lg); background: var(--c-surf); box-shadow: 0 18px 50px rgba(15, 23, 42, .28); }
.reset-dialog h2, .reset-dialog p { margin: 0; }
.reset-dialog header span { color: #b91c1c; font-size: var(--fs-xs); font-weight: 700; text-transform: uppercase; }
.reset-dialog header h2 { margin-top: 3px; color: var(--c-ink); font-size: var(--fs-lg); }
.reset-dialog > p { color: var(--c-text); font-size: var(--fs-base); line-height: 1.6; }
.reset-warning { padding: var(--sp-3); overflow-wrap: anywhere; border: 1px solid #fca5a5; border-radius: var(--r-md); background: #fef2f2; color: #991b1b !important; }
.reset-scope { min-width: 0; display: grid; gap: var(--sp-2); margin: 0; padding: var(--sp-3); border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf-2); }
.reset-scope > div { min-width: 0; display: grid; grid-template-columns: 100px minmax(0, 1fr); gap: var(--sp-2); }
.reset-scope dt { color: var(--c-muted); font-size: var(--fs-sm); }
.reset-scope dd { min-width: 0; margin: 0; overflow-wrap: anywhere; color: var(--c-ink); font-size: var(--fs-sm); }
.reset-live-error { min-height: 1.5em; overflow-wrap: anywhere; color: var(--c-danger) !important; font-size: var(--fs-sm) !important; }
.reset-live-error:empty { visibility: hidden; }
.reset-dialog footer { display: flex; justify-content: flex-end; gap: var(--sp-2); }
.reset-dialog footer .btn { min-width: 112px; min-height: 44px; }
@media (max-width: 1024px) {
  .selection-workspace { grid-template-columns: minmax(220px, .8fr) minmax(320px, 1.2fr); }
  .bot-panel { grid-column: 1 / -1; max-height: none; }
}
@media (max-width: 700px) {
  .incident-header, .start-card { align-items: stretch; flex-direction: column; }
  .selection-workspace { grid-template-columns: minmax(0, 1fr); }
  .bot-panel { grid-column: auto; }
  .ticket-list-panel, .bot-panel { max-height: none; }
  .start-card .btn { width: 100%; }
  .reset-dialog footer { flex-direction: column; }
  .reset-dialog footer .btn { width: 100%; }
}
@media (prefers-reduced-motion: reduce) { .btn { scroll-behavior: auto; } }
</style>
