<script setup lang="ts">
import { computed, nextTick, onActivated, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import BugBotPicker from '../components/BugBotPicker.vue'
import BugCaseLifecycle, { type CasePrimaryAction } from '../components/BugCaseLifecycle.vue'
import IncidentBugSummary from '../components/IncidentBugSummary.vue'
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
  isIncidentWorkflowConflict,
  resetIncidentCaseWithWarnings,
  saveBugSelectedBot,
  startIncidentCase,
  type BotMatch,
  type BotRef,
  type IncidentCase,
} from '../lib/bridge'
import { toast, toastError } from '../lib/toast'
import { useBugTickets } from '../lib/useBugTickets'
import { activeCaseForBug, botKeyForLegacyContinuation, casesForBug, continuationForDetail, terminalCaseStatuses, useIncidentCase } from '../lib/useIncidentCase'

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
  generation: number
  bugID: string
  caseID: string
  caseVersion: number
  caseStatus: string
  phase: string
  attemptID: string
  oldBotKey: string
  oldEnvironment: string
  newBotKey: string
  newBotTarget: string
  newEnvironment: string
  newCaseID: string
  idempotencyKey: string
}
const resetDialog = ref<ResetDialogSnapshot | null>(null)
const resetting = ref(false)
const resetError = ref('')
const resetDialogElement = ref<HTMLElement | null>(null)
const resetCancelButton = ref<HTMLButtonElement | null>(null)
const resetTrigger = ref<HTMLElement | null>(null)
const lifecycleRegion = ref<HTMLElement | null>(null)
const pendingEnterCaseID = ref('')
const resetRequests = new Map<string, Pick<ResetDialogSnapshot, 'newCaseID' | 'idempotencyKey'>>()
let resetGeneration = 0

const initialRequestedBugID = routeBugID()
if (initialRequestedBugID) tickets.select(initialRequestedBugID)

const selectedBugCases = computed(() => casesForBug(incidentWorkflow.cases.value, tickets.selectedID.value))
const selectedActiveCase = computed(() => activeCaseForBug(incidentWorkflow.cases.value, tickets.selectedID.value))
const newestSelectedCase = computed(() => selectedBugCases.value[0])
const preferredCase = computed(() => selectedActiveCase.value || newestSelectedCase.value)
const displayedCase = computed(() => selectedBugCases.value.find(item => item.id === incidentWorkflow.selectedCaseID.value) || preferredCase.value)
const displayedDetail = computed(() => incidentWorkflow.detail.value?.case.id === displayedCase.value?.id ? incidentWorkflow.detail.value : null)
const allCasesTerminal = computed(() => selectedBugCases.value.length > 0 && !selectedActiveCase.value)
const invalidURLBug = computed(() => Boolean(routeBugID() && !tickets.loading.value && tickets.bugs.value.length > 0 && !tickets.selectedBug.value))
const pickerSelectedBotKey = computed(() => {
  const detail = incidentWorkflow.detail.value?.case.id === preferredCase.value?.id ? incidentWorkflow.detail.value : null
  const bug = tickets.selectedBug.value
  if (!detail || !bug || detail.case.status !== 'legacy_archived') return selectedBotKey.value
  return explicitlySelectedBots.value[bug.id] || botKeyForLegacyContinuation(detail, bug.id, '')
})
const selectedBot = computed(() => matches.value.find(match => match.bot.key === pickerSelectedBotKey.value)?.bot)
const selectedBotSupportsStart = computed(() => Boolean(selectedBot.value && ['codex', 'claude-code', 'openclaw'].includes(selectedBot.value.target)))
const writeActionPending = computed(() => matching.value || starting.value || resetting.value || incidentWorkflow.pending.value)
const writeActionDisabled = computed(() => writeActionPending.value || !tickets.selectedBug.value || !selectedBot.value || !selectedBotSupportsStart.value || !selectedBot.value.env?.trim())
const writeActionDisabledReason = computed(() => {
  if (matching.value) return '正在匹配排障机器人…'
  if (starting.value || resetting.value || incidentWorkflow.pending.value) return '故障闭环操作正在处理中…'
  if (!tickets.selectedBug.value) return '请先选择一条 Bug。'
  if (!selectedBot.value) return '请选择排障机器人后继续。'
  if (!selectedBotSupportsStart.value) return `${selectedBot.value.target} 暂不支持由 Studio 后台启动，请选择 Codex、Claude Code 或 OpenClaw。`
  if (!selectedBot.value.env?.trim()) return '所选机器人缺少目标环境，请先完善平台机器人映射。'
  return ''
})
const botActionStatus = computed(() => {
  const current = preferredCase.value
  if (!current) return '尚未建立 Case'
  return terminalCaseStatuses.has(current.status) ? `已有历史 Case · ${current.status}` : `已有进行中的 Case · ${current.status}`
})

watch(() => tickets.selectedID.value, async bugID => {
  workflowNotice.value = ''
  pendingEnterCaseID.value = ''
  if (resetDialog.value && resetDialog.value.bugID !== bugID) {
    resetGeneration++
    discardResetDialog()
  }
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

watch(displayedDetail, detail => {
  if (detail?.case.id === pendingEnterCaseID.value) void focusIncidentCase(detail.case.id)
})

watch(() => [route.path, route.query.bug_id], () => {
  if (route.path === '/incidents') void syncRouteBugSelection(false)
})

let hasActivatedOnce = false
onActivated(() => {
  if (!hasActivatedOnce) {
    hasActivatedOnce = true
    return
  }
  void syncRouteBugSelection(true)
})

onMounted(async () => {
  try {
    await tickets.load()
    if (tickets.selectedBug.value) {
      await Promise.all([refreshMatches(tickets.selectedBug.value.id), openPreferredCase()])
    }
    if (!routeBugID() && tickets.selectedID.value) {
      await router.replace({ query: { ...route.query, bug_id: tickets.selectedID.value } })
    }
  } catch (error) {
    toastError('读取 Bug 工单', error)
  }
})

function routeBugID(): string {
  return typeof route.query.bug_id === 'string' ? route.query.bug_id : ''
}

async function syncRouteBugSelection(refreshCase: boolean) {
  if (route.path !== '/incidents') return
  const bugID = routeBugID()
  if (!bugID) return
  const valid = tickets.bugs.value.some(bug => bug.id === bugID)
  if (!valid) {
    if (!tickets.selectedID.value) return
    tickets.clearSelection()
    matches.value = []
    selectedBotKey.value = ''
    return
  }
  const selectionChanged = tickets.selectedID.value !== bugID
  if (selectionChanged) tickets.select(bugID)
  if (!refreshCase) return
  try {
    await incidentWorkflow.refreshCases()
    if (routeBugID() !== bugID || tickets.selectedID.value !== bugID) return
    await Promise.all([refreshMatches(bugID), openPreferredCase(true)])
  } catch (error) {
    if (routeBugID() === bugID) toastError('刷新故障 Case', error)
  }
}

async function selectBug(id: string) {
  tickets.select(id)
  await router.replace({ query: { ...route.query, bug_id: id } })
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

async function openPreferredCase(refreshCurrent = false) {
  const target = preferredCase.value
  if (!target) return
  if (incidentWorkflow.selectedCaseID.value === target.id && incidentWorkflow.detail.value?.case.id === target.id) {
    if (refreshCurrent) {
      try { await incidentWorkflow.refreshDetail(target.id) } catch { /* controller exposes a recoverable live error */ }
    }
    return
  }
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

async function focusIncidentCase(caseID: string) {
  await nextTick()
  if (pendingEnterCaseID.value !== caseID) return
  const region = lifecycleRegion.value
  if (!region) return
  const reduced = window.matchMedia?.('(prefers-reduced-motion: reduce)').matches
  region.scrollIntoView({ behavior: reduced ? 'auto' : 'smooth', block: 'start' })
  const focusTarget = region.querySelector<HTMLElement>('.primary-action:not(:disabled)')
    || region.querySelector<HTMLElement>('.case-heading')
  if (!focusTarget || displayedDetail.value?.case.id !== caseID) return
  focusTarget.focus()
  pendingEnterCaseID.value = ''
}

async function enterIncidentCase() {
  const target = preferredCase.value
  if (!target) return
  pendingEnterCaseID.value = target.id
  if (incidentWorkflow.selectedCaseID.value !== target.id) await selectWorkflowCase(target.id)
  await focusIncidentCase(target.id)
}

async function restartIncidentCase() {
  const target = preferredCase.value
  if (!target || writeActionDisabled.value) return
  if (terminalCaseStatuses.has(target.status)) {
    await startNewCase()
    return
  }
  await openResetDialog(target)
}

type StartBotChoice = { key: string; bot?: BotRef }

function startBotChoice(): StartBotChoice {
  const bug = tickets.selectedBug.value
  if (!bug) return { key: '' }
  const key = pickerSelectedBotKey.value.trim()
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
  const detail = displayedDetail.value?.case.id === incident.id ? displayedDetail.value : null
  const choice = startBotChoice()
  const newBot = choice.bot
  const newEnvironment = newBot?.env?.trim() || ''
  if (!choice.key || !newBot || !['codex', 'claude-code', 'openclaw'].includes(newBot.target) || !newEnvironment) return
  const identity = `${incident.id}:v${incident.version}:${choice.key}:${newBot.target}:${newEnvironment}`
  let request = resetRequests.get(identity)
  if (!request) {
    const newCaseID = freshResetCaseID(incident.id)
    request = { newCaseID, idempotencyKey: `reset:${incident.id}:v${incident.version}:${newCaseID}` }
    resetRequests.set(identity, request)
  }
  resetTrigger.value = document.activeElement instanceof HTMLElement ? document.activeElement : null
  resetError.value = ''
  const attempt = detail?.attempts.find(item => item.id === incident.current_attempt_id)
  resetDialog.value = {
    generation: ++resetGeneration,
    bugID,
    caseID: incident.id,
    caseVersion: incident.version,
    caseStatus: incident.status,
    phase: attempt?.phase || '无活动阶段',
    attemptID: incident.current_attempt_id || '无',
    oldBotKey: incident.selected_bot_key,
    oldEnvironment: incident.environment,
    newBotKey: choice.key,
    newBotTarget: newBot.target,
    newEnvironment,
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
  if (document.activeElement === resetDialogElement.value) {
    event.preventDefault()
    const target = event.shiftKey ? last : first
    target.focus()
  } else if (event.shiftKey && document.activeElement === first) {
    event.preventDefault()
    last.focus()
  } else if (!event.shiftKey && document.activeElement === last) {
    event.preventDefault()
    first.focus()
  }
}

async function confirmReset() {
  const request = resetDialog.value
  if (!request || resetting.value || !request.newBotKey) return
  const isCurrentResetRequest = () => isCurrentBug(request.bugID) && resetGeneration === request.generation
  const isCurrentLinkedReplacement = (replacementID: string) => {
    const current = displayedDetail.value?.case
    return Boolean(
      isCurrentBug(request.bugID) &&
      current?.bug_id === request.bugID &&
      current.id === replacementID &&
      current.reset_from_case_id === request.caseID,
    )
  }
  const isExpectedResetContext = (replacement?: IncidentCase) => {
    const current = displayedDetail.value?.case
    if (!isCurrentBug(request.bugID) || current?.bug_id !== request.bugID) return false
    if (current.id === request.caseID && current.version === request.caseVersion) return true
    return Boolean(
      replacement &&
      replacement.bug_id === request.bugID &&
      replacement.reset_from_case_id === request.caseID &&
      isCurrentLinkedReplacement(replacement.id),
    )
  }
  resetting.value = true
  resetError.value = ''
  try {
    const result = await incidentWorkflow.runOnce(request.idempotencyKey, () => resetIncidentCaseWithWarnings({
      case_id: request.caseID,
      new_case_id: request.newCaseID,
      expected_version: request.caseVersion,
      idempotency_key: request.idempotencyKey,
      actor_id: 'desktop-user',
      bot_key: request.newBotKey,
      bot_environment: request.newEnvironment,
      input_json: { target_environment: request.newEnvironment },
    }))
    const replacement = result.case
    if (!isExpectedResetContext(replacement)) return
    const snapshot = await getIncidentCase(replacement.id)
    if (!isExpectedResetContext(replacement)) return
    incidentWorkflow.applySnapshot(snapshot)
    resetting.value = false
    await nextTick()
    closeResetDialog()
    await incidentWorkflow.refreshCases()
    if (!isExpectedResetContext(replacement) || displayedDetail.value?.case.id !== replacement.id) return
    await enterIncidentCase()
    if (result.warnings.length > 0) {
      const message = `Case 已重置，但${result.warnings.map(warning => warning.message).join('；')}`
      incidentWorkflow.error.value = message
      toast.error(message)
    } else {
      toast.success('Case 已重置，接替 Case 已创建')
    }
  } catch (error) {
    if (isIncidentWorkflowConflict(error)) {
      if (!isCurrentResetRequest()) return
      const identity = `${request.caseID}:v${request.caseVersion}:${request.newBotKey}:${request.newBotTarget}:${request.newEnvironment}`
      resetRequests.delete(identity)
      resetting.value = false
      closeResetDialog()
      try {
        await incidentWorkflow.refreshCases()
        if (!isCurrentResetRequest()) return
        const refreshed = activeCaseForBug(incidentWorkflow.cases.value, request.bugID) || casesForBug(incidentWorkflow.cases.value, request.bugID)[0]
        if (refreshed) await incidentWorkflow.refreshDetail(refreshed.id)
      } catch (refreshError) {
        if (!isCurrentResetRequest()) return
        const cause = refreshError instanceof Error ? refreshError.message : String(refreshError)
        const message = `Case 已被其他操作更新，但刷新最新状态失败：${cause}。请手动刷新后重新确认。`
        incidentWorkflow.error.value = message
        toast.error(message)
        return
      }
      if (!isCurrentResetRequest()) return
      const message = 'Case 已被其他操作更新，已刷新到最新状态。请重新确认后再重置。'
      incidentWorkflow.error.value = message
      toast.info(message)
      return
    }
    if (isCurrentLinkedReplacement(request.newCaseID)) {
      resetting.value = false
      closeResetDialog()
      try { await incidentWorkflow.refreshCases() } catch { /* retain the durable event snapshot and surface the scheduling failure below */ }
      if (!isCurrentLinkedReplacement(request.newCaseID)) return
      try { await incidentWorkflow.refreshDetail(request.newCaseID) } catch { /* the selected event snapshot remains usable and recoverable */ }
      if (!isCurrentLinkedReplacement(request.newCaseID)) return
      const cause = error instanceof Error ? error.message : String(error)
      const message = `接替 Case 已创建，但新阶段启动失败：${cause}。请刷新 Case 或重试开始验证。`
      incidentWorkflow.error.value = message
      toast.error(message)
      return
    }
    if (!isExpectedResetContext()) return
    const message = error instanceof Error ? error.message : String(error)
    resetError.value = message
    incidentWorkflow.error.value = message
    toastError('重置故障 Case', error)
  } finally {
    resetting.value = false
  }
}

async function startNewCase() {
  const bug = tickets.selectedBug.value
  if (!bug) return
  const initiatingBugID = bug.id
  const choice = startBotChoice()
  if (!choice.key) {
    const error = new Error('该历史记录没有机器人信息。请重新选择当前 Bug 的机器人后再继续。')
    incidentWorkflow.error.value = error.message
    toastError('启动故障闭环', error)
    return
  }
  const selectedEnvironment = choice.bot?.env?.trim() || ''
  const roundIdentity = `${bug.id}:${displayedCase.value?.id || 'none'}:${displayedCase.value?.version || 0}:${choice.key}:${selectedEnvironment}`
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
      bot_environment: selectedEnvironment,
      expected_version: 0,
      idempotency_key: `start:${candidate}`,
      actor_id: 'desktop-user',
      input_json: {
        mode: 'reproduce',
        expected_behavior: bug.title || '',
        bug_steps: bug.steps || '',
        target_environment: selectedEnvironment,
      },
    }))
    if (!isCurrentBug(initiatingBugID)) return
    const refreshed = await refreshCaseSnapshotIfCurrent(opened.id, () => isCurrentBug(initiatingBugID))
    if (!refreshed || !isCurrentBug(initiatingBugID)) return
    starting.value = false
    await nextTick()
    await enterIncidentCase()
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
    await startNewCase()
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
        return startIncidentCase({ ...base, bug_id: incident.bug_id, bot_key: incident.selected_bot_key, bot_environment: incident.environment, input_json: { mode: 'reproduce', target_environment: incident.environment } })
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
        <p>Bug 数据由 Bug 工单统一同步；本页用于选择工单并推进可恢复的验证、排障与修复流程。</p>
      </div>
    </header>

    <section class="selection-workspace" data-overflow-safe="true" aria-label="Bug 驱动的故障闭环选择">
      <aside class="selection-panel ticket-list-panel" data-overflow-safe="true">
        <BugTicketList
          :bugs="tickets.filteredBugs.value"
          :selected-id="tickets.selectedID.value"
          :loading="tickets.loading.value"
          :query="tickets.query.value"
          @select="selectBug"
          @update:query="tickets.query.value = $event"
        />
      </aside>

      <main class="selection-panel ticket-summary-panel" data-overflow-safe="true">
        <p v-if="invalidURLBug" class="invalid-bug-state" role="status">
          URL 中的 Bug 不存在。请从左侧选择一条可用工单，页面会更新链接并继续。
        </p>
        <IncidentBugSummary :bug="tickets.selectedBug.value" />
      </main>

      <aside class="selection-panel bot-panel" data-overflow-safe="true">
        <BugBotPicker :matches="matches" :selected-key="pickerSelectedBotKey" :loading="matching" @select="rememberSelectedBot" />
        <p v-if="botError" class="live-error" role="status">{{ botError }}</p>
        <p v-else-if="selectedBot && !selectedBotSupportsStart" class="support-note">{{ selectedBot.target }} 暂不支持由 Studio 后台启动，请选择 Codex、Claude Code 或 OpenClaw。</p>
        <section v-if="tickets.selectedBug.value" class="bot-action-panel" aria-label="故障闭环操作">
          <p class="bot-action-status" role="status">{{ botActionStatus }}</p>
          <div class="bot-action-controls">
            <button v-if="!displayedCase" class="btn primary" type="button" data-action="start-case" :disabled="writeActionDisabled" @click="startNewCase()">
              {{ starting ? '开启中…' : '开启故障闭环' }}
            </button>
            <template v-else>
              <button class="btn primary" type="button" data-action="enter-case" @click="enterIncidentCase">进入故障闭环</button>
              <button class="btn danger-secondary" type="button" data-action="restart-case" :disabled="writeActionDisabled" @click="restartIncidentCase">
                {{ starting || resetting ? '处理中…' : '重新开始故障闭环' }}
              </button>
            </template>
          </div>
          <p v-if="writeActionDisabledReason" class="bot-action-disabled-reason" role="status">{{ writeActionDisabledReason }}</p>
          <p v-if="workflowNotice" class="workflow-notice" role="status" aria-live="polite">{{ workflowNotice }}</p>
        </section>
      </aside>
    </section>

    <div v-if="displayedCase" ref="lifecycleRegion" class="lifecycle-region">
      <BugCaseLifecycle
        v-if="displayedDetail"
        :cases="selectedBugCases"
        :detail="displayedDetail"
        :pending="incidentWorkflow.pending.value || starting"
        :error="incidentWorkflow.error.value"
        @select="selectWorkflowCase"
        @refresh="refreshIncidentWorkflow"
        @primary="handleIncidentPrimary"
      />
      <p v-else class="case-loading" role="status" aria-live="polite">正在加载 Case {{ displayedCase.id }}…</p>
    </div>

    <div v-if="resetDialog" class="reset-dialog-backdrop" @click.self="closeResetDialog" @keydown.esc="closeResetDialog">
      <section ref="resetDialogElement" role="dialog" aria-modal="true" aria-labelledby="reset-dialog-title" aria-describedby="reset-dialog-description" class="reset-dialog" data-overflow-safe="true" tabindex="-1" @keydown="trapResetDialogFocus">
        <header>
          <span>危险操作</span>
          <h2 id="reset-dialog-title">重置并新建 Case</h2>
        </header>
        <p id="reset-dialog-description">当前 Case 将归档为“已重置归档”，并用当前选择的机器人和环境创建新 Case，从验证阶段重新开始。<strong>当前 Agent 将被停止。</strong></p>
        <p class="reset-warning" role="note"><strong>重置不会撤销已发生的提交、推送或部署。</strong>原 Case、证据和审计记录保持不可变；外部副作用需要人工另行处理。</p>
        <dl class="reset-scope">
          <div><dt>Bug ID</dt><dd>{{ resetDialog.bugID }}</dd></div>
          <div><dt>原 Case</dt><dd>{{ resetDialog.caseID }} · v{{ resetDialog.caseVersion }}</dd></div>
          <div><dt>状态</dt><dd>{{ resetDialog.caseStatus }}</dd></div>
          <div><dt>阶段</dt><dd>{{ resetDialog.phase }}</dd></div>
          <div><dt>当前 Attempt</dt><dd>{{ resetDialog.attemptID }}</dd></div>
          <div><dt>旧绑定</dt><dd>{{ resetDialog.oldBotKey || '未绑定' }} · {{ resetDialog.oldEnvironment || '环境未知' }}</dd></div>
          <div><dt>新绑定</dt><dd>{{ resetDialog.newBotTarget }} · {{ resetDialog.newBotKey }} · {{ resetDialog.newEnvironment }}</dd></div>
          <div><dt>接替 Case</dt><dd>{{ resetDialog.newCaseID }}</dd></div>
        </dl>
        <p data-reset-error class="reset-live-error" role="status" aria-live="assertive">{{ resetError }}</p>
        <footer>
          <button ref="resetCancelButton" class="btn" data-reset-cancel type="button" :disabled="resetting" @click="closeResetDialog">取消</button>
          <button class="btn danger" data-reset-confirm type="button" :disabled="resetting || !resetDialog.newBotKey" @click="confirmReset">{{ resetting ? '重置中…' : '确认重置并新建' }}</button>
        </footer>
      </section>
    </div>
  </div>
</template>

<style scoped>
.incident-workbench-page { min-width: 0; display: grid; gap: var(--sp-3); color: var(--c-text); }
.incident-header { min-width: 0; }
.incident-header h1 { margin: 0; color: var(--c-ink); font-size: 24px; }
.incident-header p { max-width: 760px; margin: 4px 0 0; color: var(--c-muted); font-size: var(--fs-sm); line-height: 1.55; }
.btn { min-height: 44px; padding: 0 12px; border: 1px solid var(--c-line-2); border-radius: var(--r-md); background: var(--c-surf); color: var(--c-text); font: inherit; cursor: pointer; }
.btn:hover:not(:disabled) { border-color: var(--c-accent); background: var(--c-surf-2); }
.btn:focus-visible { outline: 2px solid var(--c-accent-hover); outline-offset: 2px; }
.btn:disabled { opacity: .55; cursor: not-allowed; }
.btn.primary { border-color: var(--c-accent-hover); background: var(--c-accent-hover); color: white; }
.btn.danger { border-color: #b91c1c; background: #b91c1c; color: white; }
.btn.danger:hover:not(:disabled) { border-color: #991b1b; background: #991b1b; }
.danger-secondary { border-color: #fca5a5; background: #fff; color: #b91c1c; }
.danger-secondary:hover:not(:disabled) { border-color: #dc2626; background: #fef2f2; }
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
.bot-action-panel { margin-top: var(--sp-3); padding-top: var(--sp-3); display: grid; gap: var(--sp-2); border-top: 1px solid var(--c-line); }
.bot-action-status, .bot-action-disabled-reason { min-width: 0; margin: 0; overflow-wrap: anywhere; color: var(--c-muted); font-size: var(--fs-sm); line-height: 1.5; }
.bot-action-disabled-reason { color: #92400e; }
.bot-action-controls { display: flex; flex-wrap: wrap; gap: var(--sp-2); }
.bot-action-controls .btn { flex: 1 1 160px; min-height: 44px; }
.lifecycle-region { min-width: 0; scroll-margin-top: var(--sp-3); }
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
  .selection-workspace { grid-template-columns: minmax(0, 1fr); }
  .bot-panel { grid-column: auto; }
  .ticket-list-panel, .bot-panel { max-height: none; }
  .bot-action-controls { flex-direction: column; }
  .bot-action-controls .btn { width: 100%; flex-basis: auto; }
  .reset-dialog footer { flex-direction: column; }
  .reset-dialog footer .btn { width: 100%; }
}
@media (prefers-reduced-motion: reduce) { .btn { scroll-behavior: auto; } }
</style>
