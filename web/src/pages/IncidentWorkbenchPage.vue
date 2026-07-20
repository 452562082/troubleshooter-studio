<script setup lang="ts">
import { computed, nextTick, onActivated, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter, type LocationQueryRaw } from 'vue-router'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import BugBotPicker from '../components/BugBotPicker.vue'
import BugCaseLifecycle, { type CasePrimaryAction } from '../components/BugCaseLifecycle.vue'
import IncidentBugSummary from '../components/IncidentBugSummary.vue'
import BugTicketList from '../components/BugTicketList.vue'
import {
  approveIncidentFix,
  approveIncidentMerge,
  cancelIncidentAttempt,
  clearIncidentBrowserSession,
  completeIncidentRemediation,
  continueIncidentCase,
  fetchBugByID,
  getIncidentBrowserRuntimeStatus,
  getIncidentCase,
  listBugs,
  listIncidentCases,
  matchBugBots,
  notifyIncidentDeployed,
  openIncidentBrowserLogin,
  prepareIncidentBrowserRuntime,
  repairIncidentBrowserRuntime,
  isIncidentWorkflowConflict,
  resetIncidentCaseWithWarnings,
  saveBugSelectedBot,
  startIncidentCase,
  uploadIncidentEvidenceImages,
  type BotMatch,
  type BotRef,
  type BugRecord,
  type IncidentBrowserRuntimeStatus,
  type IncidentCase,
  type IncidentEvidenceImageInput,
} from '../lib/bridge'
import { toast, toastError } from '../lib/toast'
import { useBugTickets } from '../lib/useBugTickets'
import { activeCaseForBug, casesForBug, continuationForDetail, terminalCaseStatuses, useIncidentCase } from '../lib/useIncidentCase'

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
const browserRuntimeStatus = ref<IncidentBrowserRuntimeStatus>({
  state: 'installing',
  version: '',
  error_code: 'browser_runtime_install_in_progress',
  message: '',
})
const browserRuntimeProgress = ref({ code: '', current: 0, total: 0 })
const retryingBrowserRuntime = ref(false)
const startCaseIDs = new Map<string, string>()
type RestartMode = 'active_reset' | 'terminal_new_round'
type ResetDialogSnapshot = {
  mode: RestartMode
  generation: number
  bugID: string
  caseID: string
  caseVersion: number
  newBotKey: string
  newBotName: string
  newBotTarget: string
  newEnvironment: string
  newCaseID: string
  idempotencyKey: string
}
const resetDialog = ref<ResetDialogSnapshot | null>(null)
const resetting = ref(false)
const restartPreparing = ref(false)
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

const selectedActiveCase = computed(() => activeCaseForBug(incidentWorkflow.cases.value, tickets.selectedID.value))
const selectedLatestCase = computed(() => casesForBug(incidentWorkflow.cases.value, tickets.selectedID.value)[0])
const historyViewRequested = computed(() => route.query.view === 'history')
const displayedCase = computed(() => selectedActiveCase.value || (historyViewRequested.value ? selectedLatestCase.value : undefined))
const displayedDetail = computed(() => incidentWorkflow.detail.value?.case.id === displayedCase.value?.id ? incidentWorkflow.detail.value : null)
const invalidURLBug = computed(() => Boolean(routeBugID() && !tickets.loading.value && tickets.bugs.value.length > 0 && !tickets.selectedBug.value))
const pickerSelectedBotKey = computed(() => selectedBotKey.value)
const selectedBot = computed(() => matches.value.find(match => match.bot.key === pickerSelectedBotKey.value)?.bot)
const selectedBotSupportsStart = computed(() => Boolean(selectedBot.value && ['codex', 'claude-code', 'openclaw'].includes(selectedBot.value.target)))
const selectedBugRequiresBrowser = computed(() => suggestsBrowserValidation(tickets.selectedBug.value))
const browserRuntimeBlocksSelectedBug = computed(() => selectedBugRequiresBrowser.value && browserRuntimeStatus.value.state !== 'ready')
const browserRuntimePercent = computed(() => {
  const { current, total } = browserRuntimeProgress.value
  if (total <= 0) return 0
  return Math.max(0, Math.min(100, Math.round(current / total * 100)))
})
const browserRuntimeSummary = computed(() => {
  if (browserRuntimeStatus.value.state === 'ready') return 'Chromium 已安装并通过启动探测，Web 验证可直接执行。'
  if (browserRuntimeStatus.value.state === 'broken') return '基础工具准备失败。故障 Case 尚未启动，请先重新准备 Chromium。'
  switch (browserRuntimeProgress.value.code) {
    case 'browser_runtime_importing': return '正在初始化 App 内置 Chromium，无需联网下载…'
    case 'browser_runtime_dependencies_installing': return '正在安装 Playwright 运行依赖…'
    case 'browser_runtime_downloading': return browserRuntimePercent.value > 0 ? `正在下载 Chromium（${browserRuntimePercent.value}%）…` : '正在下载 Chromium…'
    case 'browser_runtime_probing': return 'Chromium 已下载，正在执行启动探测…'
    default: return 'Studio 正在初始化验证浏览器基础工具…'
  }
})
const writeActionPending = computed(() => matching.value || starting.value || resetting.value || restartPreparing.value || incidentWorkflow.pending.value)
const writeActionDisabled = computed(() => writeActionPending.value || browserRuntimeBlocksSelectedBug.value || !tickets.selectedBug.value || !selectedBot.value || !selectedBotSupportsStart.value || !selectedBot.value.env?.trim())
const writeActionDisabledReason = computed(() => {
  if (matching.value) return '正在匹配排障机器人…'
  if (starting.value || resetting.value || restartPreparing.value || incidentWorkflow.pending.value) return '故障闭环操作正在处理中…'
  if (!tickets.selectedBug.value) return '请先选择一条 Bug。'
  if (browserRuntimeBlocksSelectedBug.value) {
    return browserRuntimeStatus.value.state === 'installing'
      ? 'Studio 正在初始化验证浏览器基础工具；完成后才能启动 Web 验证。'
      : '验证浏览器基础工具未就绪，请先重新准备 Chromium。'
  }
  if (!selectedBot.value) return '请选择排障机器人后继续。'
  if (!selectedBotSupportsStart.value) return `${selectedBot.value.target} 暂不支持由 Studio 后台启动，请选择 Codex、Claude Code 或 OpenClaw。`
  if (!selectedBot.value.env?.trim()) return '所选机器人缺少目标环境，请先完善平台机器人映射。'
  return ''
})
const botActionStatus = computed(() => {
  const current = displayedCase.value
  if (!current) return '尚未开启故障闭环'
  return terminalCaseStatuses.has(current.status) ? '历史故障闭环' : '故障闭环进行中'
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

watch([displayedCase, resetting], ([current, isResetting]) => {
  const request = resetDialog.value
  if (!request || request.mode !== 'active_reset' || isResetting || current?.id === request.caseID) return
  discardResetDialog()
})

watch(displayedDetail, detail => {
  if (detail?.case.id === pendingEnterCaseID.value) void focusIncidentCase(detail.case.id)
})

watch(() => [route.path, route.query.bug_id, route.query.view], () => {
  if (route.path === '/incidents') void syncRouteBugSelection(false)
})

let hasActivatedOnce = false
let unlistenBrowserRuntime: (() => void) | undefined
onActivated(() => {
  if (!hasActivatedOnce) {
    hasActivatedOnce = true
    return
  }
  void syncRouteBugSelection(true)
})

onMounted(async () => {
  unlistenBrowserRuntime = EventsOn('browser-runtime:status', applyBrowserRuntimeEvent)
  await refreshBrowserRuntimeStatus()
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

onUnmounted(() => unlistenBrowserRuntime?.())

function suggestsBrowserValidation(bug?: BugRecord): boolean {
  if (!bug) return false
  if (bug.frontend_url?.trim() || bug.frontend_repo?.trim() || bug.browser?.trim()) return true
  const text = [bug.title, bug.description, bug.steps, bug.expected, bug.actual].filter(Boolean).join(' ').toLocaleLowerCase()
  if (['页面', '浏览器', '网页', '前端', '小程序'].some(marker => text.includes(marker))) return true
  return text.split(/[^a-z0-9\u3400-\u9fff]+/).some(token => ['app', 'web', 'h5', 'ui', 'frontend'].includes(token))
}

function applyBrowserRuntimeEvent(raw: unknown) {
  const payload = raw !== null && typeof raw === 'object' ? raw as Record<string, unknown> : {}
  const status = payload.status !== null && typeof payload.status === 'object' ? payload.status as Record<string, unknown> : {}
  const state = status.state === 'ready' || status.state === 'installing' || status.state === 'broken' ? status.state : 'broken'
  browserRuntimeStatus.value = {
    state,
    version: typeof status.version === 'string' ? status.version : '',
    error_code: typeof status.error_code === 'string' ? status.error_code : '',
    message: '',
  }
  browserRuntimeProgress.value = {
    code: typeof payload.code === 'string' ? payload.code : '',
    current: typeof payload.current === 'number' && Number.isFinite(payload.current) && payload.current >= 0 ? payload.current : 0,
    total: typeof payload.total === 'number' && Number.isFinite(payload.total) && payload.total >= 0 ? payload.total : 0,
  }
}

async function refreshBrowserRuntimeStatus() {
  try {
    browserRuntimeStatus.value = await getIncidentBrowserRuntimeStatus()
  } catch {
    browserRuntimeStatus.value = { state: 'broken', version: '', error_code: 'browser_runtime_status_unavailable', message: '' }
  }
}

async function retryBrowserRuntimePreparation() {
  if (retryingBrowserRuntime.value || browserRuntimeStatus.value.state === 'installing') return
  retryingBrowserRuntime.value = true
  browserRuntimeStatus.value = { ...browserRuntimeStatus.value, state: 'installing', error_code: 'browser_runtime_install_in_progress', message: '' }
  browserRuntimeProgress.value = { code: 'browser_runtime_dependencies_installing', current: 0, total: 0 }
  try {
    await prepareIncidentBrowserRuntime()
    await refreshBrowserRuntimeStatus()
    if (browserRuntimeStatus.value.state === 'ready') toast.success('验证浏览器基础工具已就绪')
  } catch (error) {
    await refreshBrowserRuntimeStatus()
    toastError('准备验证浏览器基础工具', error)
  } finally {
    retryingBrowserRuntime.value = false
  }
}

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
  const history = tickets.selectedBug.value?.inbox_state === 'history'
  const query: LocationQueryRaw = { ...route.query, bug_id: id }
  if (history) query.view = 'history'
  else if (query.view === 'history') delete query.view
  await router.replace({ query })
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
  const target = displayedCase.value
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
  const target = displayedCase.value
  if (!target) return
  pendingEnterCaseID.value = target.id
  if (incidentWorkflow.selectedCaseID.value !== target.id) await selectWorkflowCase(target.id)
  await focusIncidentCase(target.id)
}

async function restartIncidentCase(targetOverride?: IncidentCase) {
  const target = targetOverride || displayedCase.value
  if (!target) return
  const choice = startBotChoice()
  if (writeActionDisabled.value) {
    if (targetOverride && !choice.key) {
      const error = new Error('该历史记录没有机器人信息。请重新选择当前 Bug 的机器人后再继续。')
      incidentWorkflow.error.value = error.message
      toastError('启动故障闭环', error)
    }
    return
  }
  const initiatingBugID = tickets.selectedID.value
  const targetIsCurrent = () => isCurrentBug(initiatingBugID) && (
    targetOverride
      ? displayedDetail.value?.case.id === target.id
      : displayedCase.value?.id === target.id
  )
  restartPreparing.value = true
  incidentWorkflow.error.value = ''
  try {
    const cached = incidentWorkflow.detail.value?.case.id === target.id ? incidentWorkflow.detail.value : null
    const snapshot = cached || await getIncidentCase(target.id)
    if (snapshot.case.id !== target.id) throw new Error(`读取到错误的重启目标 Case：期望 ${target.id}，实际 ${snapshot.case.id}`)
    if (!targetIsCurrent()) return
    await openResetDialog(snapshot.case, choice)
  } catch (error) {
    if (!targetIsCurrent()) return
    const message = error instanceof Error ? error.message : String(error)
    incidentWorkflow.error.value = message
    toastError('读取重启目标 Case', error)
  } finally {
    restartPreparing.value = false
  }
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

async function openResetDialog(incident: IncidentCase, choice: StartBotChoice) {
  if (resetting.value) return
  const bugID = tickets.selectedID.value
  const newBot = choice.bot
  const newEnvironment = newBot?.env?.trim() || ''
  if (!choice.key || !newBot || !['codex', 'claude-code', 'openclaw'].includes(newBot.target) || !newEnvironment) return
  const mode: RestartMode = terminalCaseStatuses.has(incident.status) ? 'terminal_new_round' : 'active_reset'
  const identity = resetRequestIdentity(mode, incident.id, incident.version, choice.key, newBot.target, newEnvironment)
  let request = resetRequests.get(identity)
  if (!request) {
    const newCaseID = freshResetCaseID(incident.id)
    request = {
      newCaseID,
      idempotencyKey: mode === 'active_reset'
        ? `reset:${incident.id}:v${incident.version}:${newCaseID}`
        : `start:${newCaseID}`,
    }
    resetRequests.set(identity, request)
  }
  resetTrigger.value = document.activeElement instanceof HTMLElement ? document.activeElement : null
  resetError.value = ''
  resetDialog.value = {
    mode,
    generation: ++resetGeneration,
    bugID,
    caseID: incident.id,
    caseVersion: incident.version,
    newBotKey: choice.key,
    newBotName: newBot.name?.trim() || newBot.system_id?.trim() || '排障机器人',
    newBotTarget: newBot.target,
    newEnvironment,
    ...request,
  }
  await nextTick()
  resetCancelButton.value?.focus()
}

function resetRequestIdentity(mode: RestartMode, caseID: string, caseVersion: number, botKey: string, botTarget: string, environment: string): string {
  return `${mode}:${caseID}:v${caseVersion}:${botKey}:${botTarget}:${environment}`
}

function botTargetLabel(target: string): string {
  switch (target) {
    case 'claude-code': return 'Claude Code'
    case 'openclaw': return 'OpenClaw'
    case 'codex': return 'Codex'
    default: return target
  }
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
      const identity = resetRequestIdentity(request.mode, request.caseID, request.caseVersion, request.newBotKey, request.newBotTarget, request.newEnvironment)
      resetRequests.delete(identity)
      resetting.value = false
      closeResetDialog()
      try {
        await incidentWorkflow.refreshCases()
        if (!isCurrentResetRequest()) return
        const refreshed = activeCaseForBug(incidentWorkflow.cases.value, request.bugID)
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

async function confirmRestart() {
  const request = resetDialog.value
  if (!request || resetting.value || !request.newBotKey) return
  if (request.mode === 'active_reset' && (displayedCase.value?.id !== request.caseID || displayedCase.value.bug_id !== request.bugID)) {
    discardResetDialog()
    return
  }
  if (request.mode === 'active_reset') {
    await confirmReset()
    return
  }
  await confirmTerminalNewRound(request)
}

async function confirmTerminalNewRound(request: ResetDialogSnapshot) {
  const bug = tickets.selectedBug.value
  if (!bug || bug.id !== request.bugID) return
  const isCurrentRequest = () => isCurrentBug(request.bugID) && resetGeneration === request.generation
  resetting.value = true
  resetError.value = ''
  workflowNotice.value = ''
  try {
    const opened = await incidentWorkflow.runOnce(request.idempotencyKey, () => startIncidentCase({
      case_id: request.newCaseID,
      bug_id: request.bugID,
      bot_key: request.newBotKey,
      bot_environment: request.newEnvironment,
      expected_version: 0,
      idempotency_key: request.idempotencyKey,
      actor_id: 'desktop-user',
      input_json: {
        mode: 'reproduce',
        expected_behavior: bug.title || '',
        bug_steps: bug.steps || '',
        target_environment: request.newEnvironment,
      },
    }))
    if (!isCurrentRequest()) return
    const refreshed = await refreshCaseSnapshotIfCurrent(opened.id, isCurrentRequest)
    if (!refreshed || !isCurrentRequest()) return
    resetting.value = false
    await nextTick()
    closeResetDialog()
    await enterIncidentCase()
    if (opened.id !== request.newCaseID) {
      workflowNotice.value = '已打开现有闭环'
      toast.info('已打开现有闭环')
    } else {
      workflowNotice.value = '新一轮故障闭环已启动'
      toast.success(workflowNotice.value)
    }
  } catch (error) {
    if (!isCurrentRequest()) return
    const message = error instanceof Error ? error.message : String(error)
    resetError.value = message
    incidentWorkflow.error.value = message
    toastError('启动新一轮故障闭环', error)
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
      workflowNotice.value = '故障闭环已启动'
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
    if (snapshot.case.id !== caseID) return false
    incidentWorkflow.selectedCaseID.value = caseID
    return incidentWorkflow.applyAuthoritativeDetail(snapshot)
  } catch (error) {
    if (!isCurrent()) return false
    throw error
  }
}

async function refreshIncidentWorkflow() {
  try {
    await incidentWorkflow.refreshCases()
    await openPreferredCase(true)
  } catch (error) {
    toastError('刷新故障 Case', error)
  }
}

type IncidentBrowserAction = 'login' | 'clear-session' | 'repair-runtime' | 'redeploy-validator' | 'edit-bug-url'

const browserKey = (kind: string, detail: NonNullable<typeof displayedDetail.value>) =>
  `${kind}:${detail.case.id}:${detail.case.current_attempt_id}:v${detail.case.version}`

type IncidentBrowserContext = { bugID: string; caseID: string; attemptID: string; version: number }

function isSameBrowserCase(context: IncidentBrowserContext): boolean {
  return isCurrentBug(context.bugID) && displayedDetail.value?.case.id === context.caseID
}

function isSameBlockedBrowserAttempt(context: IncidentBrowserContext): boolean {
  const current = displayedDetail.value?.case
  return isSameBrowserCase(context) && current?.current_attempt_id === context.attemptID && current.version === context.version
}

async function refreshBrowserCaseBestEffort(context: IncidentBrowserContext): Promise<boolean> {
  try {
    return await refreshCaseSnapshotIfCurrent(context.caseID, () => isSameBrowserCase(context))
  } catch {
    if (!isSameBrowserCase(context)) return false
    const warning = '浏览器操作已完成，但 Case 详情刷新失败；请手动刷新。'
    workflowNotice.value = warning
    toast.info(warning)
    return false
  }
}

async function handleIncidentBrowser(action: IncidentBrowserAction) {
  if (action === 'redeploy-validator') {
    await router.push('/bots')
    return
  }
  if (action === 'edit-bug-url') {
    const bugID = tickets.selectedID.value
    if (bugID) await router.push({ path: '/bugs', query: { bug_id: bugID } })
    return
  }
  const detail = displayedDetail.value
  if (!detail?.case.current_attempt_id) return
  const incident = detail.case
  const context: IncidentBrowserContext = { bugID: tickets.selectedID.value, caseID: incident.id, attemptID: incident.current_attempt_id, version: incident.version }
  const key = browserKey(action, detail)
  const input = {
    case_id: incident.id,
    attempt_id: incident.current_attempt_id,
    expected_version: incident.version,
    idempotency_key: key,
    actor_id: 'desktop-user',
  }
  incidentWorkflow.error.value = ''
  workflowNotice.value = ''
  try {
    if (action === 'clear-session') {
      await incidentWorkflow.runOnce(key, () => clearIncidentBrowserSession(input))
      if (!isSameBlockedBrowserAttempt(context)) return
      const refreshed = await refreshBrowserCaseBestEffort(context)
      if (refreshed && isSameBrowserCase(context)) toast.success('已清除此环境登录态')
      return
    }
    const updated = await incidentWorkflow.runOnce(key, () => action === 'login'
      ? openIncidentBrowserLogin(input)
      : repairIncidentBrowserRuntime(input))
    if (updated.id !== context.caseID) throw new Error('browser recovery returned another Case')
    if (!isSameBlockedBrowserAttempt(context)) return
    if (!incidentWorkflow.applyCase(updated)) throw new Error('browser recovery returned stale Case state')
    const refreshed = await refreshBrowserCaseBestEffort(context)
    if (refreshed && isSameBrowserCase(context)) toast.success(action === 'login' ? '登录完成，验证已继续' : '浏览器环境已修复，验证已继续')
  } catch {
    if (!isSameBlockedBrowserAttempt(context)) return
    const message = action === 'login'
      ? '无法完成验证浏览器登录，请刷新 Case 后重试。'
      : action === 'repair-runtime'
        ? '浏览器环境修复失败，请稍后重试。'
        : '清除浏览器登录态失败，请稍后重试。'
    incidentWorkflow.error.value = message
    toast.error(message)
  }
}

async function handleIncidentPrimary(payload: { kind: CasePrimaryAction['kind']; input?: string; evidence?: string; images?: IncidentEvidenceImageInput[]; rootCauseAttemptID?: string; caseVersion?: number; sourceBaselines?: Record<string, string> }) {
  const detail = displayedDetail.value
  if (!detail) return
  const incident = detail.case
  if (payload.kind === 'continue_legacy') {
    await restartIncidentCase(incident)
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
      if (payload.kind === 'retry_validation') {
        return continueIncidentCase({ ...base, ...continuationForDetail(detail, '') })
      }
      if (payload.kind === 'supply_evidence' || payload.kind === 'continue_fix') {
        let supplemental = payload.input?.trim() || ''
        if (payload.kind === 'supply_evidence' && payload.images?.length) {
          const attemptID = incident.current_attempt_id
          if (!attemptID) throw new Error('当前 Case 没有可绑定补充证据的验证 Attempt')
          const uploaded = await uploadIncidentEvidenceImages({
            case_id: incident.id,
            attempt_id: attemptID,
            expected_version: incident.version,
            images: payload.images,
          })
          const imageEvidence = `用户补充了 ${uploaded.length} 张页面截图（EvidenceArtifact: ${uploaded.map(item => item.artifact_id).join(', ')}），重试时必须结合图片核对复现步骤和页面状态。`
          supplemental = [supplemental, imageEvidence].filter(Boolean).join('\n')
        }
        return continueIncidentCase({ ...base, ...continuationForDetail(detail, supplemental) })
      }
      if (payload.kind === 'supply_merge_decision') {
        return continueIncidentCase({ ...base, phase: 'fix', input_json: { decision: 'resolve_merge_conflict', evidence: payload.input || '' } })
      }
      if (payload.kind === 'supply_deployment_proof') {
        return continueIncidentCase({ ...base, phase: 'regression', input_json: { decision: 'retry_deployment_check' } })
      }
      if (payload.kind === 'approve_fix') {
        if (!payload.rootCauseAttemptID || payload.caseVersion === undefined) throw new Error('修复授权缺少对话框中的根因或 Case 版本快照')
        return approveIncidentFix({
          ...base,
          expected_version: payload.caseVersion,
          idempotency_key: `start-fix:${incident.id}:${payload.rootCauseAttemptID}:${payload.caseVersion}`,
          root_cause_attempt_id: payload.rootCauseAttemptID,
          input_json: { source_baselines: payload.sourceBaselines || {} },
        })
      }
      if (payload.kind === 'complete_remediation') {
        if (!payload.rootCauseAttemptID || payload.caseVersion === undefined) throw new Error('处置确认缺少根因或 Case 版本快照')
        return completeIncidentRemediation({
          ...base,
          expected_version: payload.caseVersion,
          idempotency_key: `complete-remediation:${incident.id}:${payload.rootCauseAttemptID}:${payload.caseVersion}`,
          root_cause_attempt_id: payload.rootCauseAttemptID,
          summary: payload.input || '',
          evidence: payload.evidence || '',
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
        return notifyIncidentDeployed({ ...base })
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

    <section
      class="browser-runtime-status"
      :class="`is-${browserRuntimeStatus.state}`"
      aria-labelledby="browser-runtime-title"
      aria-live="polite"
    >
      <div>
        <p class="browser-runtime-eyebrow">Studio 基础工具</p>
        <h2 id="browser-runtime-title">验证浏览器</h2>
        <p data-browser-runtime-summary>{{ browserRuntimeSummary }}</p>
        <progress
          v-if="browserRuntimeStatus.state === 'installing' && browserRuntimeProgress.total > 0"
          :value="browserRuntimeProgress.current"
          :max="browserRuntimeProgress.total"
        >{{ browserRuntimePercent }}%</progress>
      </div>
      <span v-if="browserRuntimeStatus.state === 'ready'" class="browser-runtime-badge">已就绪</span>
      <button
        v-else-if="browserRuntimeStatus.state === 'broken'"
        class="btn"
        type="button"
        data-action="prepare-browser-runtime"
        :disabled="retryingBrowserRuntime"
        @click="retryBrowserRuntimePreparation"
      >{{ retryingBrowserRuntime ? '准备中…' : '重新准备' }}</button>
      <span v-else class="browser-runtime-badge">准备中</span>
    </section>

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
            <button v-else class="btn danger-secondary" type="button" data-action="restart-case" :disabled="writeActionDisabled" @click="restartIncidentCase()">
              {{ starting || resetting || restartPreparing ? '处理中…' : '重新开始故障闭环' }}
            </button>
          </div>
          <p v-if="writeActionDisabledReason" class="bot-action-disabled-reason" role="status">{{ writeActionDisabledReason }}</p>
          <p v-if="workflowNotice" class="workflow-notice" role="status" aria-live="polite">{{ workflowNotice }}</p>
        </section>
      </aside>
    </section>

    <div v-if="displayedCase" ref="lifecycleRegion" class="lifecycle-region">
      <BugCaseLifecycle
        v-if="displayedDetail"
        :detail="displayedDetail"
        :bug-title="tickets.selectedBug.value?.title || ''"
        :pending="incidentWorkflow.pending.value || starting"
        :error="incidentWorkflow.error.value"
        :phase-events="incidentWorkflow.phaseEvents.value[displayedDetail.case.current_attempt_id] || []"
        @refresh="refreshIncidentWorkflow"
        @primary="handleIncidentPrimary"
        @browser="handleIncidentBrowser"
      />
      <section v-else class="case-loading" aria-live="polite">
        <p role="status">{{ incidentWorkflow.error.value ? `加载故障闭环失败：${incidentWorkflow.error.value}` : '正在加载故障闭环…' }}</p>
        <button v-if="incidentWorkflow.error.value" class="btn" type="button" data-action="retry-active-case" :disabled="incidentWorkflow.loading.value" @click="refreshIncidentWorkflow">
          {{ incidentWorkflow.loading.value ? '重试中…' : '重试加载' }}
        </button>
      </section>
    </div>

    <div v-if="resetDialog" class="reset-dialog-backdrop" @click.self="closeResetDialog" @keydown.esc="closeResetDialog">
      <section ref="resetDialogElement" role="dialog" aria-modal="true" aria-labelledby="reset-dialog-title" aria-describedby="reset-dialog-description" class="reset-dialog" data-overflow-safe="true" tabindex="-1" @keydown="trapResetDialogFocus">
        <header>
          <span>危险操作</span>
          <h2 id="reset-dialog-title">{{ resetDialog.mode === 'active_reset' ? '重新开始故障闭环' : '开启新一轮故障闭环' }}</h2>
        </header>
        <p v-if="resetDialog.mode === 'active_reset'" id="reset-dialog-description">将停止当前 Agent，保留本轮记录，并使用以下设置从“验证”重新开始。</p>
        <p v-else id="reset-dialog-description">原记录保持不变，并使用以下设置从“验证”开启新一轮。</p>
        <p class="reset-warning" role="note">已发生的提交、推送或部署不会自动撤销；已有证据和审计记录会继续保留。</p>
        <dl class="reset-scope">
          <div><dt>开始阶段</dt><dd>验证</dd></div>
          <div><dt>排障机器人</dt><dd>{{ resetDialog.newBotName }} · {{ botTargetLabel(resetDialog.newBotTarget) }}</dd></div>
          <div><dt>目标环境</dt><dd>{{ resetDialog.newEnvironment }}</dd></div>
        </dl>
        <p data-reset-error class="reset-live-error" role="status" aria-live="assertive">{{ resetError }}</p>
        <footer>
          <button ref="resetCancelButton" class="btn" data-reset-cancel type="button" :disabled="resetting" @click="closeResetDialog">取消</button>
          <button class="btn danger" data-reset-confirm type="button" :disabled="resetting || !resetDialog.newBotKey" @click="confirmRestart">
            {{ resetting ? (resetDialog.mode === 'active_reset' ? '重新开始中…' : '开启中…') : (resetDialog.mode === 'active_reset' ? '确认重新开始' : '确认开启新一轮') }}
          </button>
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
.browser-runtime-status { min-width: 0; display: flex; align-items: center; justify-content: space-between; gap: var(--sp-3); padding: var(--sp-3); border: 1px solid var(--c-line); border-radius: var(--r-lg); background: var(--c-surf); }
.browser-runtime-status > div { min-width: 0; display: grid; gap: 3px; }
.browser-runtime-status h2, .browser-runtime-status p { margin: 0; }
.browser-runtime-status h2 { color: var(--c-ink); font-size: var(--fs-base); }
.browser-runtime-status p { overflow-wrap: anywhere; color: var(--c-muted); font-size: var(--fs-sm); line-height: 1.5; }
.browser-runtime-status .browser-runtime-eyebrow { color: var(--c-muted); font-size: var(--fs-xs); font-weight: 700; text-transform: uppercase; }
.browser-runtime-status progress { width: min(420px, 100%); height: 8px; margin-top: var(--sp-1); accent-color: #2563eb; }
.browser-runtime-status.is-installing { border-color: #bfdbfe; background: #eff6ff; }
.browser-runtime-status.is-broken { border-color: #fecaca; background: #fef2f2; }
.browser-runtime-badge { flex: 0 0 auto; padding: 4px 9px; border-radius: 999px; background: var(--c-surf-2); color: var(--c-muted); font-size: var(--fs-xs); font-weight: 700; }
.is-ready .browser-runtime-badge { background: #dcfce7; color: #166534; }
.is-installing .browser-runtime-badge { background: #dbeafe; color: #1d4ed8; }
.btn { min-height: 44px; padding: 0 12px; border: 1px solid var(--c-line-2); border-radius: var(--r-md); background: var(--c-surf); color: var(--c-text); font: inherit; cursor: pointer; }
.btn:hover:not(:disabled) { border-color: var(--c-accent); background: var(--c-surf-2); }
.btn:focus-visible { outline: 2px solid var(--c-accent-hover); outline-offset: 2px; }
.btn:disabled { opacity: .55; cursor: not-allowed; }
.btn.primary { border-color: #2563eb; background: #2563eb; color: #fff; }
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
.case-loading .btn { min-height: 44px; }
.support-note { margin-top: var(--sp-2); background: var(--c-surf-2); color: var(--c-muted); }
.live-error { margin-top: var(--sp-2); border: 1px solid #fecaca; background: #fef2f2; color: #b91c1c; }
.workflow-notice { border: 1px solid #bbf7d0; background: #f0fdf4; color: #166534; }
.bot-action-panel { margin-top: var(--sp-3); padding-top: var(--sp-3); display: grid; gap: var(--sp-2); border-top: 1px solid var(--c-line); }
.bot-action-status, .bot-action-disabled-reason { min-width: 0; margin: 0; overflow-wrap: anywhere; color: var(--c-muted); font-size: var(--fs-sm); line-height: 1.5; }
.bot-action-disabled-reason { color: #92400e; }
.bot-action-controls { display: flex; flex-wrap: wrap; gap: var(--sp-2); }
.bot-action-controls .btn { flex: 1 1 160px; min-height: 44px; transition: background-color 180ms ease, border-color 180ms ease, color 180ms ease; }
.bot-action-controls .btn.primary:hover:not(:disabled) { border-color: #1d4ed8; background: #1d4ed8; color: #fff; }
.bot-action-controls .btn.primary:focus-visible { border-color: #2563eb; background: #2563eb; color: #fff; outline: 2px solid #1e40af; outline-offset: 2px; }
.bot-action-controls .btn.primary:disabled { opacity: 1; border-color: #cbd5e1; background: #e2e8f0; color: #475569; cursor: not-allowed; }
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
  .browser-runtime-status { align-items: stretch; flex-direction: column; }
  .browser-runtime-status > .btn { width: 100%; }
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
