<script setup lang="ts">
import { marked } from 'marked'
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import {
  type BotRef,
  type BotMatch,
  type BugAttachment,
  type BugAttachmentPreviewResult,
  type BugPlatform,
  type BugRecord,
  cancelBugInvestigation,
  continueBugInvestigation,
  type InvestigationContinueInput,
  bugHookBaseURL,
  clearBugPlatformLogin,
  deleteBugPlatform,
  type DiscoveredBot,
  discoverBots,
  fetchBugByID,
  generateBugContext,
  type InvestigationEvent,
  type InvestigationRun,
  loginBugPlatform,
  listBugInvestigationRuns,
  listBugPlatforms,
  listBugs,
  matchBugBots,
  previewBugAttachment,
  saveBugPlatform,
  saveBugSelectedBot,
  listIncidentCases,
  getIncidentCase,
  getIncidentWorkflowMetrics,
  startIncidentCase,
  continueIncidentCase,
  approveIncidentFix,
  approveIncidentMerge,
  notifyIncidentDeployed,
  cancelIncidentAttempt,
  type IncidentCase,
  type WorkflowMetrics,
  startBugFix,
  syncBugPlatform,
} from '../lib/bridge'
import BugCaseLifecycle, { type CasePrimaryAction } from '../components/BugCaseLifecycle.vue'
import BugWorkflowMetrics from '../components/BugWorkflowMetrics.vue'
import { copyToClipboard } from '../lib/clipboard'
import { confirmDialog } from '../lib/confirm'
import { toast, toastError } from '../lib/toast'
import { botKeyForLegacyContinuation, continuationForDetail, useIncidentCase } from '../lib/useIncidentCase'

const bugs = ref<BugRecord[]>([])
const platforms = ref<BugPlatform[]>([])
const installedBots = ref<DiscoveredBot[]>([])
const hookBaseURL = ref('')
const selectedID = ref('')
const selectedPlatformID = ref('')
const matches = ref<BotMatch[]>([])
const selectedBotKey = ref('')
const contextText = ref('')
const bugsLoading = ref(false)
const platformSaving = ref(false)
const platformLoggingIn = ref(false)
const loginClearing = ref(false)
const platformDeleting = ref(false)
const syncingBugs = ref(false)
const fetchingBug = ref(false)
const contextGenerating = ref(false)
const matching = ref(false)
const attachmentPreviewing = ref(false)
const attachmentPreview = ref<BugAttachmentPreviewResult | null>(null)
const attachmentThumbnails = ref<Record<string, BugAttachmentPreviewResult | 'loading' | 'failed'>>({})
const investigationRuns = ref<InvestigationRun[]>([])
const investigationStarting = ref(false)
const fixStarting = ref(false)
const investigationCancelling = ref(false)
const continuingInvestigation = ref(false)
const supplementDialogOpen = ref(false)
const userSupplementInput = ref('')
const supplementPhase = ref<'validation' | 'investigation' | 'fix'>('investigation')
const outputScrollRef = ref<HTMLElement | null>(null)
const query = ref('')
const manualBugID = ref('')
const configOpen = ref(false)
const botPickerOpen = ref(false)
const botPickerQuery = ref('')
const platformDraft = ref({
  id: '',
  name: 'Bug 平台',
  type: 'zentao',
  base_url: '',
  account: '',
  auth_mode: 'feishu_sso',
  session_header: '',
  password: '',
  token: '',
  hook_secret: '',
  bot_mappings: [] as Array<{ bot_key: string; env: string }>,
  enabled: true,
  poll_enabled: false,
  poll_interval_minutes: 5,
})
let unlistenInvestigationEvents: (() => void) | undefined
let unlistenWorkflowReminders: (() => void) | undefined

const incidentWorkflow = useIncidentCase({ listCases: listIncidentCases, getCase: getIncidentCase })
const incidentCases = incidentWorkflow.cases
const incidentDetail = incidentWorkflow.detail
const incidentPending = incidentWorkflow.pending
const incidentError = incidentWorkflow.error
const incidentStartIDs = new Map<string, string>()
const workbenchView = ref<'inbox' | 'cases'>('inbox')
let workbenchViewChosen = false
const explicitlySelectedBots = ref<Record<string, string>>({})
const workflowMetrics = ref<WorkflowMetrics | null>(null)

const selectedBug = computed(() => bugs.value.find(b => b.id === selectedID.value) || bugs.value[0])
const selectedPlatform = computed(() => platforms.value.find(p => p.id === selectedPlatformID.value))
const selectedPlatformHasSession = computed(() => Boolean(selectedPlatform.value?.session_header))
const selectedBot = computed(() => matches.value.find(m => m.bot.key === selectedBotKey.value)?.bot)
const allBotRefs = computed(() => installedBots.value.filter(b => !b.ghost).map(discoveredBotToRef))
const availableBotRefs = computed(() => allBotRefs.value)
const configuredPlatformBots = computed(() => platformDraft.value.bot_mappings.map(mapping => ({
  mapping,
  bot: botRefByKey(mapping.bot_key),
})))
const addableBotRefs = computed(() => {
  const selected = new Set(platformDraft.value.bot_mappings.map(m => m.bot_key))
  const kw = botPickerQuery.value.trim().toLowerCase()
  return availableBotRefs.value
    .filter(bot => !selected.has(bot.key))
    .filter(bot => {
      if (!kw) return true
      return [botDisplayName(bot), bot.target, bot.path, bot.system_id].join(' ').toLowerCase().includes(kw)
    })
})
const platformBotMappings = computed(() => selectedPlatform.value?.bot_mappings || [])
const platformBotEmptyText = computed(() => {
  if (!selectedPlatform.value) return '请先保存并选择 Bug 平台'
  if (platformBotMappings.value.length === 0) return '当前平台未勾选排障机器人'
  return '暂无已安装机器人'
})
const selectedRun = computed(() => investigationRuns.value[0])
const outputTab = ref<'validation' | 'investigation' | 'fix'>('investigation')
const selectedBotSupportsDirectLaunch = computed(() => ['codex', 'claude-code', 'openclaw'].includes(selectedBot.value?.target || ''))
const selectedBotUnsupportedReason = computed(() => {
  if (!selectedBot.value || selectedBotSupportsDirectLaunch.value) return ''
  if (selectedBot.value.target === 'cursor') return 'Cursor 暂不支持后台直启,请复制上下文后在 Cursor Custom Agent 中发起。'
  return '该机器人暂不支持后台直启。'
})

const latestRunNeedsUserInput = computed(() => {
	const run = selectedRun.value
	if (!run) return false
	const phase = outputTab.value
	const finalText = phase === currentRunPhase(run) ? (run.final_message || run.error || '') : ''
	const text = finalText + (run.events || []).filter(e => eventPhase(e) === phase).map(e => e.message).join('\n')
	return /insufficient_info|需要用户补充|需要补充信息|请提供|缺少.*信息|未提供|无法确认|无法采集|登录态|测试账号|无法.*请.*输入/i.test(text)
})
const outputPhaseLabel = computed(() => phaseLabel(outputTab.value))
const supplementPhaseLabel = computed(() => phaseLabel(supplementPhase.value))
const supplementActionLabel = computed(() => supplementPhase.value === 'validation' ? '继续验证' : supplementPhase.value === 'fix' ? '继续修复' : '继续排障')

const investigationEventLines = computed(() => {
  const run = selectedRun.value
  if (!run) return []
  const finalText = investigationFinalText.value.trim()
  return (run.events || [])
    .filter(e => eventPhase(e) === 'investigation' || !eventPhase(e))
    .filter(e => !isFinalInvestigationEvent(e))
    .filter(e => !(isTerminalInvestigationRun(run) && isLikelyInvestigationReport(e.message || '')))
    .map(e => e.message)
    .filter(message => {
      const text = (message || '').trim()
      return Boolean(text) && (!finalText || (text !== finalText && !(text.length > 120 && finalText.includes(text))))
    })
})
const validationEventLines = computed(() => {
  const run = selectedRun.value
  if (!run) return []
  const finalText = validationFinalText.value.trim()
  return (run.events || [])
    .filter(e => eventPhase(e) === 'validation')
    .filter(e => !isFinalInvestigationEvent(e))
    .map(e => e.message)
    .filter(message => {
      const text = (message || '').trim()
      return Boolean(text) && (!finalText || (text !== finalText && !isLikelyValidationStructuredResult(text)))
    })
})
const fixEventLines = computed(() => {
  const run = selectedRun.value
  if (!run) return []
  const finalText = fixFinalText.value.trim()
  return (run.events || [])
    .filter(e => eventPhase(e) === 'fix')
    .filter(e => !isFinalInvestigationEvent(e))
    .map(e => e.message)
    .filter(message => {
      const text = (message || '').trim()
      return Boolean(text) && (!finalText || (text !== finalText && !(text.length > 120 && finalText.includes(text))))
    })
})
const investigationFinalText = computed(() => {
  const run = selectedRun.value
  if (!run) return ''
  if (currentRunPhase(run) === 'fix') return ''
  return run.final_message || run.error || fallbackInvestigationFinalMessage(run)
})
const validationFinalText = computed(() => {
  const run = selectedRun.value
  if (!run) return ''
  if (currentRunPhase(run) === 'validation' && (run.final_message || run.error)) {
    return normalizeValidationReportForDisplay(run.final_message || run.error || '')
  }
  const events = [...(run.events || [])].reverse()
  const reportEvent = events.find(e => eventPhase(e) === 'validation' && isLikelyValidationStructuredResult(e.message || ''))
  return reportEvent?.message ? formatValidationReportForDisplay(reportEvent.message) : ''
})
const fixFinalText = computed(() => {
  const run = selectedRun.value
  if (!run || currentRunPhase(run) !== 'fix') return ''
  return run.final_message || run.error || fallbackInvestigationFinalMessage(run)
})
const investigationOutput = computed(() => {
  const parts = []
  if (investigationEventLines.value.length > 0) parts.push(investigationEventLines.value.join('\n'))
  if (investigationFinalText.value.trim()) parts.push(investigationFinalText.value.trim())
  if (parts.length > 0) return parts.join('\n\n')
  return contextText.value
})
const fixOutput = computed(() => {
  const parts = []
  if (fixEventLines.value.length > 0) parts.push(fixEventLines.value.join('\n'))
  if (fixFinalText.value.trim()) parts.push(fixFinalText.value.trim())
  return parts.join('\n\n')
})
const validationOutput = computed(() => {
  const parts = []
  if (validationEventLines.value.length > 0) parts.push(validationEventLines.value.join('\n'))
  if (validationFinalText.value.trim()) parts.push(validationFinalText.value.trim())
  return parts.join('\n\n')
})
const activeOutputText = computed(() => outputTab.value === 'validation'
  ? validationOutput.value
  : outputTab.value === 'fix'
    ? fixOutput.value
    : investigationOutput.value
)
const copyableInvestigationText = computed(() => {
  const fixResult = fixFinalText.value.trim()
  if (fixResult) return fixResult
  const result = investigationFinalText.value.trim()
  if (result) return result
  if (!selectedRun.value) return contextText.value.trim()
  return ''
})
const copyInvestigationLabel = computed(() => fixFinalText.value.trim() || investigationFinalText.value.trim() ? '复制结果' : '复制上下文')
const canCancelInvestigation = computed(() => selectedRun.value?.status === 'running')
const renderedInvestigationMarkdown = computed(() => safeMarkdown(investigationFinalText.value || contextText.value))
const renderedValidationMarkdown = computed(() => safeMarkdown(validationFinalText.value))
const renderedFixMarkdown = computed(() => safeMarkdown(fixFinalText.value))
const hasFixOutput = computed(() => fixEventLines.value.length > 0 || Boolean(fixFinalText.value.trim()) || currentRunPhase(selectedRun.value || ({} as InvestigationRun)) === 'fix')
const canStartFix = computed(() => {
  const run = selectedRun.value
  return Boolean(run && run.status === 'succeeded' && selectedBotSupportsDirectLaunch.value && investigationFinalText.value.trim() && !hasFixOutput.value)
})
const selectedBugStepsHTML = computed(() => selectedBug.value?.steps ? safeMarkdown(selectedBug.value.steps) : '-')
const selectedBugDescriptionHTML = computed(() => selectedBug.value?.description ? safeMarkdown(selectedBug.value.description) : '')
const selectedBugAttachments = computed(() => selectedBug.value?.attachments || [])
const filteredBugs = computed(() => {
  const kw = query.value.trim().toLowerCase()
  if (!kw) return bugs.value
  return bugs.value.filter(b => [
    b.title, b.source, b.source_id, b.env, b.bot_env, b.system_id, b.assignee, ...(b.service_hints || []),
  ].filter(Boolean).join(' ').toLowerCase().includes(kw))
})
const hookURL = computed(() => {
  const p = selectedPlatform.value
  if (!p || !hookBaseURL.value) return ''
  const secret = p.hook_secret ? `?secret=${encodeURIComponent(p.hook_secret)}` : ''
  return `${hookBaseURL.value}/api/bug-hooks/${encodeURIComponent(p.id)}${secret}`
})

watch(incidentCases, items => {
  if (items.length > 0 && !workbenchViewChosen) workbenchView.value = 'cases'
  void loadWorkflowMetrics()
}, { immediate: true })

watch(selectedPlatform, (p) => {
  if (!p) return
  platformDraft.value = {
    id: p.id,
    name: p.name || '',
    type: p.type || 'zentao',
    base_url: p.base_url || '',
    account: p.account || '',
    auth_mode: p.auth_mode || 'feishu_sso',
    session_header: '',
    password: '',
    token: '',
    hook_secret: p.hook_secret || '',
    bot_mappings: (p.bot_mappings || []).map(m => ({ bot_key: m.bot_key, env: m.env || '' })),
    enabled: p.enabled,
    poll_enabled: Boolean(p.poll_enabled),
    poll_interval_minutes: p.poll_interval_minutes || 5,
  }
  if (selectedBug.value?.id) {
    void refreshMatches(selectedBug.value.id, selectedBug.value.selected_bot_key)
  }
}, { immediate: false })

watch(selectedBug, async (bug) => {
  if (!bug) {
    matches.value = []
    selectedBotKey.value = ''
    contextText.value = ''
    investigationRuns.value = []
    return
  }
  selectedID.value = bug.id
  contextText.value = bug.last_context || ''
  await Promise.all([refreshMatches(bug.id, bug.selected_bot_key), loadInvestigationRuns(bug.id)])
}, { immediate: false })

watch([selectedBug, selectedPlatformID], () => {
  void preloadAttachmentThumbnails()
}, { flush: 'post' })

watch(selectedRun, (run, previousRun) => {
  if (!run) {
    outputTab.value = 'investigation'
    return
  }
  if (run.id !== previousRun?.id) {
    followRunPhase(run)
  }
})

watch(activeOutputText, () => {
  scrollOutputToBottom()
}, { flush: 'post' })

watch(outputTab, () => {
  scrollOutputToBottom()
}, { flush: 'post' })

onMounted(async () => {
  setupInvestigationEventBridge()
  unlistenWorkflowReminders = EventsOn('incident-workflow:reminder', (reminder: { bug_id?: string; waiting_age?: number }) => {
    toast.info(`Bug ${reminder?.bug_id || ''} 已等待人工部署超过 24 小时`)
    void loadWorkflowMetrics()
  })
  await Promise.all([loadInstalledBots(), loadPlatforms(), loadBugs(), loadHookBase(), loadWorkflowMetrics()])
})

onUnmounted(() => {
  if (unlistenInvestigationEvents) {
    unlistenInvestigationEvents()
    unlistenInvestigationEvents = undefined
  }
  if (unlistenWorkflowReminders) {
    unlistenWorkflowReminders()
    unlistenWorkflowReminders = undefined
  }
})

async function loadWorkflowMetrics() {
  try {
    workflowMetrics.value = await getIncidentWorkflowMetrics()
  } catch {
    workflowMetrics.value = null
  }
}

async function loadHookBase() {
  try {
    hookBaseURL.value = await bugHookBaseURL()
  } catch (e) {
    toastError('读取 Hook 地址', e)
  }
}

async function loadPlatforms() {
  try {
    platforms.value = await listBugPlatforms()
    if (platforms.value.length === 0) {
      selectedPlatformID.value = ''
      return
    }
    if (!selectedPlatformID.value || !platforms.value.some(p => p.id === selectedPlatformID.value)) {
      selectedPlatformID.value = platforms.value[0].id
    }
  } catch (e) {
    toastError('读取 Bug 平台配置', e)
  }
}

async function loadInstalledBots() {
  try {
    installedBots.value = await discoverBots([])
  } catch (e) {
    installedBots.value = []
    toastError('读取已安装机器人', e)
  }
}

async function savePlatform() {
  if (!platformDraft.value.name.trim()) {
    toast.error('请填写平台名称')
    return
  }
  platformSaving.value = true
  try {
    const saved = await saveBugPlatform({
      id: platformDraft.value.id.trim(),
      name: platformDraft.value.name.trim(),
      type: platformDraft.value.type.trim() || 'zentao',
      base_url: platformDraft.value.base_url.trim(),
      account: platformDraft.value.account.trim(),
      auth_mode: platformDraft.value.auth_mode.trim() || 'session_header',
      session_header: platformDraft.value.session_header.trim(),
      password: platformDraft.value.password.trim(),
      token: platformDraft.value.token.trim(),
      hook_secret: platformDraft.value.hook_secret.trim(),
      bot_mappings: platformDraft.value.bot_mappings
        .map(m => ({ bot_key: m.bot_key.trim(), env: m.env.trim() }))
        .filter(m => m.bot_key),
      enabled: platformDraft.value.enabled,
      poll_enabled: platformDraft.value.poll_enabled,
      poll_interval_minutes: Math.max(1, Math.floor(Number(platformDraft.value.poll_interval_minutes) || 5)),
    })
    await loadPlatforms()
    selectedPlatformID.value = saved.id
    configOpen.value = true
    toast.success('平台配置已保存')
    return saved
  } catch (e) {
    toastError('保存平台配置', e)
    return undefined
  } finally {
    platformSaving.value = false
  }
}

async function savePlatformForLogin() {
  if (!platformDraft.value.base_url.trim()) {
    toast.error('请先填写平台地址')
    return undefined
  }
  return savePlatform()
}

async function loginSelectedPlatform() {
  const saved = await savePlatformForLogin()
  if (!saved) return
  platformLoggingIn.value = true
  try {
    const result = await loginBugPlatform({ platform_id: saved.id })
    await loadPlatforms()
    selectedPlatformID.value = saved.id
    toast.success(`平台登录态已保存,读取到 ${result.cookie_count} 个 Cookie`)
  } catch (e) {
    await loadPlatforms()
    selectedPlatformID.value = saved.id
    toastError('授权登录平台', e)
  } finally {
    platformLoggingIn.value = false
  }
}

async function clearSelectedPlatformLogin() {
  const platform = selectedPlatform.value
  if (!platform) {
    toast.error('请先选择平台')
    return
  }
  loginClearing.value = true
  try {
    await clearBugPlatformLogin({ platform_id: platform.id })
    await loadPlatforms()
    selectedPlatformID.value = platform.id
    toast.success('登录态已清除')
  } catch (e) {
    toastError('清除登录态', e)
  } finally {
    loginClearing.value = false
  }
}

async function deleteSelectedPlatform() {
  const id = platformDraft.value.id.trim()
  if (!id) {
    toast.error('当前平台还未保存')
    return
  }
  const ok = await confirmDialog({
    title: '删除平台',
    message: `确定删除「${platformDraft.value.name || id}」吗? 已接收的 Bug 工单不会被删除。`,
    confirmText: '删除',
    cancelText: '取消',
    danger: true,
    defaultAction: 'cancel',
  })
  if (!ok) return
  platformDeleting.value = true
  try {
    await deleteBugPlatform({ platform_id: id })
    selectedPlatformID.value = ''
    await loadPlatforms()
    if (!selectedPlatformID.value) newPlatform()
    toast.success('平台已删除')
  } catch (e) {
    toastError('删除平台', e)
  } finally {
    platformDeleting.value = false
  }
}

async function loadBugs() {
  bugsLoading.value = true
  try {
    bugs.value = await listBugs()
    if (!selectedID.value && bugs.value.length > 0) selectedID.value = bugs.value[0].id
    if (selectedID.value) {
      await Promise.all([
        refreshMatches(selectedID.value, selectedBug.value?.selected_bot_key),
        loadInvestigationRuns(selectedID.value),
      ])
    }
  } catch (e) {
    toastError('读取 Bug 工单', e)
  } finally {
    bugsLoading.value = false
  }
}

async function syncSelectedPlatform() {
  const platform = selectedPlatform.value
  if (!platform) {
    toast.error('请先选择平台')
    return
  }
  syncingBugs.value = true
  try {
    const result = await syncBugPlatform(platform.id)
    if (result.selected_bug_id) selectedID.value = result.selected_bug_id
    await loadBugs()
    toast.success(`已同步指派给我的 Bug,新增/更新 ${result.stored} 条`)
  } catch (e) {
    await loadPlatforms()
    selectedPlatformID.value = platform.id
    toastError('同步 Bug', e)
  } finally {
    syncingBugs.value = false
  }
}

async function fetchManualBug() {
  const platform = selectedPlatform.value
  const bugID = manualBugID.value.trim()
  if (!platform) {
    toast.error('请先选择平台')
    return
  }
  if (!bugID) {
    toast.error('请输入 Bug ID')
    return
  }
  fetchingBug.value = true
  try {
    const result = await fetchBugByID({ platform_id: platform.id, bug_id: bugID })
    if (result.selected_bug_id) selectedID.value = result.selected_bug_id
    await loadBugs()
    toast.success('Bug 已拉取')
  } catch (e) {
    await loadPlatforms()
    selectedPlatformID.value = platform.id
    toastError('拉取 Bug', e)
  } finally {
    fetchingBug.value = false
  }
}

async function previewAttachment(index: number) {
  const bug = selectedBug.value
  const platform = selectedPlatform.value
  if (!bug || !platform) {
    toast.error('请先选择 Bug 和平台')
    return
  }
  attachmentPreviewing.value = true
  try {
    const preview = await previewBugAttachment({
      platform_id: platform.id,
      bug_id: bug.id,
      attachment_index: index,
    })
    attachmentPreview.value = preview
    if (preview.content_type.startsWith('image/')) {
      attachmentThumbnails.value[attachmentThumbnailKey(bug.id, index)] = preview
    }
  } catch (e) {
    toastError('预览附件', e)
  } finally {
    attachmentPreviewing.value = false
  }
}

async function preloadAttachmentThumbnails() {
  const bug = selectedBug.value
  const platform = selectedPlatform.value
  if (!bug || !platform) return
  const attachments = bug.attachments || []
  for (const [index, att] of attachments.entries()) {
    if (!isImageAttachment(att)) continue
    const key = attachmentThumbnailKey(bug.id, index)
    if (attachmentThumbnails.value[key]) continue
    attachmentThumbnails.value[key] = 'loading'
    try {
      const preview = await previewBugAttachment({
        platform_id: platform.id,
        bug_id: bug.id,
        attachment_index: index,
      })
      attachmentThumbnails.value[key] = preview.content_type.startsWith('image/') ? preview : 'failed'
    } catch {
      attachmentThumbnails.value[key] = 'failed'
    }
  }
}

function attachmentThumbnailKey(bugID: string, index: number): string {
  return `${bugID}:${index}`
}

function attachmentThumbnailSrc(index: number): string {
  const bug = selectedBug.value
  if (!bug) return ''
  const thumb = attachmentThumbnails.value[attachmentThumbnailKey(bug.id, index)]
  return typeof thumb === 'object' ? thumb.data_url : ''
}

function isImageAttachment(att: Pick<BugAttachment, 'type' | 'name'>): boolean {
  const type = (att.type || '').toLowerCase()
  const name = (att.name || '').toLowerCase()
  return type.startsWith('image/') || /\.(png|jpe?g|gif|webp|bmp)$/.test(name)
}

function attachmentSubtitle(att: Pick<BugAttachment, 'type' | 'local_path' | 'remote_url' | 'id'>): string {
  return att.type || (att.id ? `平台文件 #${att.id}` : att.remote_url || att.local_path || '附件')
}

async function refreshMatches(bugID: string, preferredKey = '') {
  if (!bugID) return
  matching.value = true
  try {
    const nextMatches = await matchBugBots(bugID)
    if (selectedBug.value?.id !== bugID) return
    matches.value = applyPlatformBotMappings(nextMatches)
    selectedBotKey.value = preferredKey && matches.value.some(m => m.bot.key === preferredKey)
      ? preferredKey
      : matches.value[0]?.bot.key || ''
  } catch (e) {
    if (selectedBug.value?.id !== bugID) return
    matches.value = []
    selectedBotKey.value = ''
    toastError('匹配机器人', e)
  } finally {
    if (selectedBug.value?.id === bugID) matching.value = false
  }
}

function applyPlatformBotMappings(items: BotMatch[]): BotMatch[] {
  const mappings = platformBotMappings.value
  if (!selectedPlatform.value) return items
  if (mappings.length === 0) return []
  const byKey = new Map(mappings.map(m => [m.bot_key, m.env || '']))
  return items
    .filter(item => byKey.has(item.bot.key))
    .map(item => {
      const env = byKey.get(item.bot.key) || ''
      return {
        ...item,
        bot: { ...item.bot, env },
      }
    })
}

async function loadInvestigationRuns(bugID: string) {
  if (!bugID) {
    investigationRuns.value = []
    return
  }
  try {
    const runs = await listBugInvestigationRuns(bugID)
    if (selectedBug.value?.id !== bugID) return
    investigationRuns.value = runs
  } catch (e) {
    if (selectedBug.value?.id !== bugID) return
    investigationRuns.value = []
    toastError('读取排障记录', e)
  }
}

async function startInvestigation() {
  const bug = selectedBug.value
  const bot = selectedBot.value
  if (!bug || !bot) {
    toast.error('请先选择 Bug 和机器人')
    return
  }
  if (!selectedBotSupportsDirectLaunch.value) {
    toast.error(selectedBotUnsupportedReason.value || '该机器人暂不支持后台直启。')
    return
  }
  investigationStarting.value = true
  const bugID = bug.id
  outputTab.value = 'validation'
  try {
    const startKey = `start:${bugID}:${bot.key}`
    let caseID = incidentStartIDs.get(startKey)
    if (!caseID) {
      const suffix = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
      caseID = `case-${bugID.replace(/[^a-zA-Z0-9_-]/g, '-')}-${suffix}`
      incidentStartIDs.set(startKey, caseID)
    }
    const incident = await incidentWorkflow.runOnce(startKey, () => startIncidentCase({
      case_id: caseID,
      bug_id: bugID,
      bot_key: bot.key,
      expected_version: 0,
      idempotency_key: `start:${caseID}`,
      actor_id: 'desktop-user',
      input_json: { mode: 'reproduce', expected_behavior: bug.title || '', bug_steps: bug.steps || '', target_environment: bot.env || bug.env || '' },
    }))
    await incidentWorkflow.refreshDetail(incident.id)
    workbenchView.value = 'cases'
    workbenchViewChosen = true
    toast.success('故障闭环已启动')
  } catch (e) {
    toastError('启动故障闭环', e)
  } finally {
    investigationStarting.value = false
  }
}

async function refreshIncidentWorkflow() {
  try {
    await incidentWorkflow.refreshCases()
    if (incidentWorkflow.selectedCaseID.value) await incidentWorkflow.refreshDetail()
  } catch (error) {
    toastError('刷新故障 Case', error)
  }
}

function selectWorkbenchView(view: 'inbox' | 'cases') {
  workbenchView.value = view
  workbenchViewChosen = true
}

async function handleIncidentPrimary(payload: { kind: CasePrimaryAction['kind']; input?: string; observedVersion?: string; observedCommits?: Record<string, string>; versionSource?: string; rootCauseAttemptID?: string; caseVersion?: number }) {
  const detail = incidentDetail.value
  if (!detail) return
  const incident = detail.case
  const key = `${payload.kind}:${incident.id}:v${incident.version}`
  try {
    const updated = await incidentWorkflow.runOnce(key, async (): Promise<IncidentCase> => {
      const base = { case_id: incident.id, expected_version: incident.version, idempotency_key: key, actor_id: 'desktop-user' }
      if (payload.kind === 'continue_legacy' || payload.kind === 'start_validation') {
        const botKey = payload.kind === 'continue_legacy'
          ? botKeyForLegacyContinuation(detail, selectedBug.value?.id || '', explicitlySelectedBots.value[incident.bug_id] || '')
          : incident.selected_bot_key
        if (!botKey) {
          if (selectedBug.value?.id === incident.bug_id) selectedBotKey.value = ''
          throw new Error('该历史记录没有机器人信息。请切换到 Bug 收件箱，重新选择当前 Bug 的机器人后再继续。')
        }
        return startIncidentCase({ ...base, bug_id: incident.bug_id, bot_key: botKey, input_json: { mode: 'reproduce' } })
      }
      if (payload.kind === 'supply_evidence' || payload.kind === 'continue_fix') {
        const continuation = continuationForDetail(detail, payload.input || '')
        return continueIncidentCase({ ...base, ...continuation })
      }
      if (payload.kind === 'supply_merge_decision') {
        return continueIncidentCase({ ...base, phase: 'fix', input_json: { decision: 'resolve_merge_conflict', evidence: payload.input || '' } })
      }
      if (payload.kind === 'supply_deployment_proof') {
        return continueIncidentCase({ ...base, phase: 'regression', input_json: { decision: 'update_deployment_proof', evidence: payload.input || '' } })
      }
      if (payload.kind === 'approve_fix') {
        const rootCauseAttemptID = payload.rootCauseAttemptID || ''
        const caseVersion = payload.caseVersion
        if (!rootCauseAttemptID || caseVersion === undefined) throw new Error('修复授权缺少对话框中的根因或 Case 版本快照')
        return approveIncidentFix({ ...base, expected_version: caseVersion, idempotency_key: `start-fix:${incident.id}:${rootCauseAttemptID}:${caseVersion}`, root_cause_attempt_id: rootCauseAttemptID })
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
    await incidentWorkflow.refreshDetail(updated.id)
    toast.success(payload.kind === 'continue_legacy' ? '已创建新一轮验证 Case' : '操作已提交')
  } catch (error) {
    incidentWorkflow.error.value = error instanceof Error ? error.message : String(error)
    toastError('执行故障流程操作', error)
  }
}

async function startFix() {
  const bug = selectedBug.value
  const bot = selectedBot.value
  const run = selectedRun.value
  if (!bug || !bot || !run) {
    toast.error('请先完成排障并选择机器人')
    return
  }
  if (!canStartFix.value) {
    toast.error('需要排障 Agent 完成结论后才能启动修复')
    return
  }
  fixStarting.value = true
  const bugID = bug.id
  outputTab.value = 'fix'
  try {
    const nextRun = await startBugFix({ bug_id: bugID, bot, previous_run_id: run.id })
    if (selectedBug.value?.id === bugID && nextRun.bug_id === bugID) mergeInvestigationRun(nextRun)
    toast.success('修复 Agent 已启动')
  } catch (e) {
    toastError('启动修复', e)
  } finally {
    fixStarting.value = false
  }
}

async function rememberSelectedBot(botKey: string) {
  selectedBotKey.value = botKey
  const bug = selectedBug.value
  if (!bug || !botKey) return
  explicitlySelectedBots.value = { ...explicitlySelectedBots.value, [bug.id]: botKey }
  bugs.value = bugs.value.map(b => b.id === bug.id ? { ...b, selected_bot_key: botKey } : b)
  try {
    await saveBugSelectedBot({ bug_id: bug.id, bot_key: botKey })
  } catch (e) {
    toastError('保存机器人选择', e)
  }
}

async function generateContext() {
  const bug = selectedBug.value
  const bot = selectedBot.value
  if (!bug || !bot) {
    toast.error('请先选择 Bug 和机器人')
    return
  }
  contextGenerating.value = true
  try {
    contextText.value = await generateBugContext({ bug_id: bug.id, bot })
    bugs.value = bugs.value.map(b => b.id === bug.id ? { ...b, selected_bot_key: bot.key, last_context: contextText.value } : b)
    toast.success('已生成排障上下文')
  } catch (e) {
    toastError('生成排障上下文', e)
  } finally {
    contextGenerating.value = false
  }
}

function setupInvestigationEventBridge() {
  if (unlistenInvestigationEvents || !hasWailsEventRuntime()) return
  unlistenInvestigationEvents = EventsOn('bug-investigation:event', (payload: { run?: InvestigationRun; event?: InvestigationEvent }) => {
    const run = payload?.run
    if (!run || run.bug_id !== selectedBug.value?.id) return
    mergeInvestigationRun(run, payload?.event)
  })
}

function hasWailsEventRuntime(): boolean {
  if (typeof window === 'undefined') return false
  const rt = (window as any).runtime
  return !!rt && typeof rt.EventsOnMultiple === 'function'
}

function mergeInvestigationRun(run: InvestigationRun, event?: InvestigationEvent) {
  const idx = investigationRuns.value.findIndex(item => item.id === run.id)
  const existing = idx >= 0 ? investigationRuns.value[idx] : undefined
  const incomingEvents = Array.isArray(run.events) ? run.events : undefined
  const events = incomingEvents && incomingEvents.length > 0
    ? incomingEvents
    : event && event.type !== 'status'
      ? [...(existing?.events || []), event]
      : existing?.events || incomingEvents || []
  const merged = { ...(existing || {}), ...run, events }
  investigationRuns.value = idx >= 0
    ? investigationRuns.value.map(item => item.id === run.id ? merged : item)
    : [merged, ...investigationRuns.value]
  if (selectedRun.value?.id === merged.id) {
    followRunPhase(merged, event)
  }
}

function scrollOutputToBottom() {
  nextTick(() => {
    const el = outputScrollRef.value
    if (el) el.scrollTop = el.scrollHeight
  })
}

function followRunPhase(run: InvestigationRun, event?: InvestigationEvent) {
  const phase = eventPhase(event || ({} as InvestigationEvent)) || currentRunPhase(run)
  if (phase === 'validation' || phase === 'investigation' || phase === 'fix') {
    outputTab.value = phase
  }
}

function currentRunPhase(run: InvestigationRun): 'validation' | 'investigation' | 'fix' {
  if ((run.prompt_preview || '').includes('修复 Agent')) return 'fix'
  if ((run.status === 'failed' || run.status === 'cancelled') && (run.error || run.final_message)) return 'investigation'
  const events = run.events || []
  for (let i = events.length - 1; i >= 0; i--) {
    const phase = eventPhase(events[i])
    if (phase === 'validation' || phase === 'investigation' || phase === 'fix') return phase
  }
  if ((run.prompt_preview || '').includes('验证 Agent')) return 'validation'
  if (run.status === 'running') return 'investigation'
  return (run.final_message || run.error || fallbackInvestigationFinalMessage(run)).trim() ? 'investigation' : 'validation'
}

async function cancelInvestigation() {
  const run = selectedRun.value
  const bug = selectedBug.value
  if (!run || run.status !== 'running') return
  investigationCancelling.value = true
  try {
    await cancelBugInvestigation({ run_id: run.id })
    if (bug) await loadInvestigationRuns(bug.id)
    toast.success('已停止排障')
  } catch (e) {
    toastError('停止排障', e)
  } finally {
    investigationCancelling.value = false
  }
}

function openSupplementDialog() {
  supplementPhase.value = outputTab.value
  supplementDialogOpen.value = true
}

function closeSupplementDialog() {
  if (continuingInvestigation.value) return
  supplementDialogOpen.value = false
}

async function submitSupplement() {
  const bug = selectedBug.value
  const bot = selectedBot.value
  const inputText = userSupplementInput.value.trim()
  if (!bug || !bot) {
    toast.error('请先选择 Bug 和机器人')
    return
  }
  if (!selectedBotSupportsDirectLaunch.value) {
    toast.error(selectedBotUnsupportedReason.value || '该机器人暂不支持后台直启。')
    return
  }
  if (!inputText) {
    toast.error('请输入补充信息')
    return
  }
  continuingInvestigation.value = true
  const bugID = bug.id
  const prevRunID = selectedRun.value?.id || ''
  outputTab.value = supplementPhase.value
  try {
    const run = await continueBugInvestigation({
      bug_id: bugID,
      bot,
      user_input: inputText,
      previous_run_id: prevRunID,
      phase: supplementPhase.value,
    } as InvestigationContinueInput)
    if (selectedBug.value?.id === bugID && run.bug_id === bugID) mergeInvestigationRun(run)
    userSupplementInput.value = ''
    supplementDialogOpen.value = false
    toast.success(`${supplementActionLabel.value}已启动（基于用户补充信息）`)
  } catch (e) {
    toastError('继续排障', e)
  } finally {
    continuingInvestigation.value = false
  }
}

async function copyHookURL() {
  if (!hookURL.value) {
    toast.error('请先保存平台配置')
    return
  }
  ;(await copyToClipboard(hookURL.value)) ? toast.success('Hook URL 已复制') : toast.error('复制失败')
}

async function copyInvestigationOutput() {
  const text = copyableInvestigationText.value
  if (!text.trim()) {
    toast.error('没有可复制的内容')
    return
  }
  ;(await copyToClipboard(text)) ? toast.success('已复制') : toast.error('复制失败')
}

function newPlatform() {
  selectedPlatformID.value = ''
  botPickerOpen.value = false
  botPickerQuery.value = ''
  platformDraft.value = {
    id: '',
    name: 'Bug 平台',
    type: 'zentao',
    base_url: '',
    account: '',
    auth_mode: 'feishu_sso',
    session_header: '',
    password: '',
    token: '',
    hook_secret: '',
    bot_mappings: [],
    enabled: true,
    poll_enabled: false,
    poll_interval_minutes: 5,
  }
  configOpen.value = true
}

function discoveredBotToRef(bot: DiscoveredBot): BotRef {
  return {
    key: `${bot.path}|${bot.meta.target}`,
    system_id: bot.meta.system_id,
    target: bot.meta.target,
    path: bot.path,
    name: bot.meta.system_name,
    agent_id: bot.meta.agent_id,
    role: bot.meta.role || 'troubleshooter',
    internal_agents: bot.meta.internal_agents || [],
    envs: bot.environments || [],
  }
}

function botRefByKey(botKey: string): BotRef {
  return allBotRefs.value.find(bot => bot.key === botKey) || {
    key: botKey,
    system_id: '',
    target: '',
    path: botKey,
  }
}

function botDisplayName(bot: BotRef): string {
  return bot.name || bot.system_id || bot.path
}

function platformBotEnv(botKey: string): string {
  return platformDraft.value.bot_mappings.find(m => m.bot_key === botKey)?.env || ''
}

function addPlatformBot(bot: BotRef) {
  const existing = platformDraft.value.bot_mappings.find(m => m.bot_key === bot.key)
  if (existing) return
  platformDraft.value.bot_mappings.push({ bot_key: bot.key, env: bot.envs?.[0] || '' })
  botPickerQuery.value = ''
  if (addableBotRefs.value.length === 0) botPickerOpen.value = false
}

function removePlatformBot(botKey: string) {
  platformDraft.value.bot_mappings = platformDraft.value.bot_mappings.filter(m => m.bot_key !== botKey)
}

function setPlatformBotEnv(botKey: string, env: string) {
  const existing = platformDraft.value.bot_mappings.find(m => m.bot_key === botKey)
  if (existing) existing.env = env
}

function eventValue(event: Event): string {
  return (event.target as HTMLInputElement | HTMLSelectElement | null)?.value || ''
}

function selectBug(id: string) {
  selectedID.value = id
}

function sourceLabel(b: BugRecord): string {
  if (b.source === 'zentao') return `禅道 #${b.source_id || '-'}`
  return b.source || '未知来源'
}

function fmtTime(s?: string): string {
  if (!s) return '-'
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`
}

function safeMarkdown(text: string): string {
  const html = marked.parse(text || '', { async: false }) as string
  return html
    .replace(/<script\b[^<]*(?:(?!<\/script>)<[^<]*)*<\/script>/gi, '')
    .replace(/\son\w+="[^"]*"/gi, '')
    .replace(/\son\w+='[^']*'/gi, '')
    .replace(/\s(href|src)="javascript:[^"]*"/gi, ' $1="#"')
    .replace(/\s(href|src)='javascript:[^']*'/gi, " $1='#'")
}

function fallbackInvestigationFinalMessage(run: InvestigationRun): string {
  const events = [...(run.events || [])].reverse()
  const finalEvent = events.find(isFinalInvestigationEvent)
  if (finalEvent?.message?.trim()) return finalEvent.message
  if (isTerminalInvestigationRun(run)) {
    const reportEvent = events.find(e => isLikelyInvestigationReport(e.message || ''))
    if (reportEvent?.message?.trim()) return reportEvent.message
    return events.find(e => !isCompletionMarkerEvent(e) && e.message?.trim())?.message || ''
  }
  return ''
}

function isFinalInvestigationEvent(event: InvestigationEvent): boolean {
  return ['final', 'result'].includes(event.type)
}

function eventPhase(event: InvestigationEvent): string {
  const phase = event?.meta?.phase
  return typeof phase === 'string' ? phase : ''
}

function phaseLabel(phase: 'validation' | 'investigation' | 'fix'): string {
  if (phase === 'validation') return '验证 Agent'
  if (phase === 'fix') return '修复 Agent'
  return '排障 Agent'
}

function isTerminalInvestigationRun(run: InvestigationRun): boolean {
  return ['succeeded', 'failed', 'cancelled'].includes(run.status)
}

function isCompletionMarkerEvent(event: InvestigationEvent): boolean {
  const message = (event.message || '').trim()
  return event.type === 'status' ||
    event.type === 'turn_completed' ||
    message === '排障完成' ||
    message === 'succeeded' ||
    message === 'failed' ||
    message === 'cancelled'
}

function isLikelyInvestigationReport(message: string): boolean {
  const text = message.trim()
  if (!text) return false
  if (/^#{1,6}\s+/m.test(text) && /\n\|.+\|\n\|[-:\s|]+\|/m.test(text)) return true
  if (/^#{1,6}\s+/.test(text) && /(故障快报|现象复述|已验证事实|根因|结论|建议)/.test(text)) return true
  if (/\*\*(结论|根因|现象|时间)\*\*/.test(text) && text.length > 120) return true
  return false
}

function isLikelyValidationStructuredResult(message: string): boolean {
  const text = message.trim()
  return Boolean(text) && (/^verification_status\s*:/mi.test(text) || /^#{1,6}\s*验证报告\s*\|/m.test(text))
}

function formatValidationReportForDisplay(message: string): string {
  const raw = message.trim().split('\\n').join('\n')
  if (!raw) return ''
  if (/^#{1,6}\s*验证报告\s*\|/m.test(raw)) return normalizeValidationReportForDisplay(raw)
  const status = yamlScalar(raw, 'verification_status')
  const env = yamlScalar(raw, 'environment') || '-'
  const observed = yamlScalar(raw, 'observed_behavior') || '-'
  const expected = yamlScalar(raw, 'expected_behavior') || '-'
  const evidence = yamlNestedScalar(raw, 'handoff_to_troubleshooter', 'evidence_summary') || '-'
  const gaps = yamlBlockSummary(raw, 'gaps') || '[]'
  return [
    `### 验证报告 | ${env} | ${validationStatusLabel(status)}`,
    '',
    `- 结论: ${validationConclusion(status)}`,
    `- 实际现象: ${observed}`,
    `- 期望表现: ${expected}`,
    `- 关键证据: ${evidence}`,
    `- 需补信息: ${gaps}`,
    '',
    '#### 原始结构化结果',
    '',
    '```yaml',
    raw,
    '```',
  ].join('\n')
}

function normalizeValidationReportForDisplay(message: string): string {
  const text = message.trim()
  if (!text) return ''
  return text.replace(/^(#{1,6}\s*验证报告\s*\|\s*)([^|\n]+?)(\s*\|\s*[^\n]+)$/m, (_match, prefix, env, suffix) => {
    return `${prefix}${normalizeValidationEnvLabel(env)}${suffix}`
  })
}

function normalizeValidationEnvLabel(env: string): string {
  const text = env.trim()
  if (!/bug env|bot env/i.test(text)) return text || '-'
  const bugEnv = looseEnvField(text, 'bug env')
  const botEnv = looseEnvField(text, 'bot env')
  return firstDisplayEnv(bugEnv, botEnv, text)
}

function looseEnvField(text: string, key: string): string {
  const pattern = new RegExp(`${escapeRegExp(key)}\\s*[:=：]?\\s*([^,，|;；]+)`, 'i')
  const match = text.match(pattern)
  return stripYamlQuotes(match?.[1] || '')
}

function firstDisplayEnv(...values: string[]): string {
  for (const value of values) {
    const trimmed = value.trim()
    if (trimmed && trimmed !== '-') return trimmed
  }
  return '-'
}

function validationStatusLabel(status: string): string {
  switch (status) {
    case 'reproduced': return '已复现'
    case 'not_reproduced': return '未复现'
    case 'insufficient_info': return '信息不足'
    case 'fixed_verified': return '修复已验证'
    case 'still_reproduces': return '修复后仍复现'
    default: return '结论不完整'
  }
}

function validationConclusion(status: string): string {
  switch (status) {
    case 'reproduced': return '已复现原始 Bug，可以进入排障 Agent。'
    case 'not_reproduced': return '未复现原始 Bug，已暂停进入排障 Agent。'
    case 'insufficient_info': return '验证所需信息不足，用户补充后应继续验证。'
    case 'fixed_verified': return '修复验证通过，已暂停进入排障 Agent。'
    case 'still_reproduces': return '修复后仍可复现，需要进入排障 Agent。'
    default: return '验证 Agent 未输出可进入排障的完整结构化结论。'
  }
}

function yamlScalar(text: string, key: string): string {
  const pattern = new RegExp(`^\\s*${escapeRegExp(key)}\\s*:\\s*(.*)$`, 'mi')
  const match = text.match(pattern)
  return stripYamlQuotes(match?.[1] || '')
}

function yamlNestedScalar(text: string, parent: string, key: string): string {
  return yamlScalar(yamlRawBlock(text, parent), key)
}

function yamlBlockSummary(text: string, key: string): string {
  const direct = yamlScalar(text, key)
  if (direct) return direct
  const block = yamlRawBlock(text, key).replace(/\s+/g, ' ').trim()
  if (!block) return ''
  return block.length > 420 ? `${block.slice(0, 420)}...` : block
}

function yamlRawBlock(text: string, key: string): string {
  const lines = text.split('\\n').join('\n').split('\n')
  const keyPattern = new RegExp(`^\\s*${escapeRegExp(key)}\\s*:\\s*(.*)$`, 'i')
  for (let i = 0; i < lines.length; i += 1) {
    const match = lines[i].match(keyPattern)
    if (!match) continue
    const direct = stripYamlQuotes(match[1] || '')
    if (direct) return direct
    const block: string[] = []
    for (const next of lines.slice(i + 1)) {
      if (!next.trim()) continue
      if (/^[A-Za-z_][A-Za-z0-9_-]*\s*:/.test(next)) break
      block.push(next.trim())
    }
    return block.join('\n')
  }
  return ''
}

function stripYamlQuotes(value: string): string {
  return value.trim().replace(/^["'`]+|["'`]+$/g, '')
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}
</script>

<template>
  <div class="bug-page">
    <header class="bug-header">
      <div>
        <h1>Bug 工单</h1>
        <p>配置 Bug 平台登录方式,后台可按间隔同步,也可以主动拉取指定 Bug。</p>
      </div>
      <button class="btn accent" type="button" @click="configOpen = !configOpen">
        {{ configOpen ? '收起配置' : '平台配置' }}
      </button>
    </header>

    <nav class="workbench-view-tabs" aria-label="Bug 工作台视图">
      <button type="button" :class="{ active: workbenchView === 'inbox' }" :aria-current="workbenchView === 'inbox' ? 'page' : undefined" @click="selectWorkbenchView('inbox')">Bug 收件箱</button>
      <button type="button" :class="{ active: workbenchView === 'cases' }" :aria-current="workbenchView === 'cases' ? 'page' : undefined" :disabled="incidentCases.length === 0 && !incidentDetail" @click="selectWorkbenchView('cases')">故障闭环 <span>{{ incidentCases.length }}</span></button>
    </nav>

    <section class="platform-config" :class="{ open: configOpen }">
      <div class="platform-list">
        <div class="platform-tabs">
          <button
            v-for="p in platforms"
            :key="p.id"
            type="button"
            class="platform-chip"
            :class="{ active: selectedPlatformID === p.id }"
            @click="selectedPlatformID = p.id"
          >
            {{ p.name }}<span>{{ p.enabled ? '启用' : '停用' }}</span>
          </button>
        </div>
        <button class="btn icon add-platform" type="button" title="新增平台" aria-label="新增平台" @click="newPlatform">+</button>
      </div>
      <div class="config-grid">
        <div class="config-row basic-row">
          <div class="field">
            <input v-model="platformDraft.name" class="form-control" placeholder="平台名称,如 测试环境 Bug 平台" />
          </div>
          <div class="field platform-type-field">
            <select v-model="platformDraft.type" class="form-control">
              <option value="zentao">禅道</option>
              <option value="generic">通用 Webhook</option>
            </select>
          </div>
          <div class="field">
            <input v-model="platformDraft.base_url" class="form-control" placeholder="平台地址 https://bug-platform.example.com" />
          </div>
        </div>
        <div class="config-row auth-row">
          <div class="field">
            <select v-model="platformDraft.auth_mode" class="form-control">
              <option value="feishu_sso">飞书授权登录</option>
              <option value="api_token">API Token</option>
              <option value="password">账号密码</option>
            </select>
          </div>
          <div v-if="platformDraft.auth_mode === 'password'" class="field auth-secret-field">
            <input v-model="platformDraft.password" class="form-control" type="password" placeholder="密码,留空沿用已保存值" />
          </div>
          <div v-if="platformDraft.auth_mode === 'api_token'" class="field auth-secret-field">
            <input v-model="platformDraft.token" class="form-control" type="password" placeholder="Token,可选,留空沿用已保存值" />
          </div>
          <div v-if="platformDraft.auth_mode === 'feishu_sso'" class="field login-field">
            <span class="login-state">
              <span>登录状态</span>
              <strong :class="{ ok: selectedPlatformHasSession }">
                {{ selectedPlatformHasSession ? '已保存' : '未登录' }}
              </strong>
            </span>
            <button class="btn" type="button" :disabled="platformSaving || platformLoggingIn" @click="loginSelectedPlatform">
              {{ platformLoggingIn ? '等待授权' : '登录平台' }}
            </button>
            <button class="btn" type="button" :disabled="loginClearing || platformLoggingIn || !selectedPlatformHasSession" @click="clearSelectedPlatformLogin">清除登录态</button>
          </div>
          <div class="field toggle-cell">
            <label class="enabled-toggle">
              <input v-model="platformDraft.enabled" type="checkbox" />
              启用平台
            </label>
          </div>
        </div>
        <div class="bot-config-block">
          <div class="bot-config-title">
            <div>
              <strong>可用于该平台的排障机器人</strong>
              <span>只展示已添加机器人,开始排障时只能从这里选择。</span>
            </div>
            <button
              class="btn small add-bot-btn"
              type="button"
              :disabled="availableBotRefs.length === 0"
              @click="botPickerOpen = !botPickerOpen"
            >
              {{ botPickerOpen ? '收起' : '+ 添加' }}
            </button>
          </div>
          <div v-if="configuredPlatformBots.length === 0" class="empty compact-empty">
            {{ availableBotRefs.length === 0 ? '暂无已安装机器人' : '还未添加排障机器人' }}
          </div>
          <div v-else class="bot-config-list">
            <div
              v-for="item in configuredPlatformBots"
              :key="item.mapping.bot_key"
              class="bot-config-row"
              :class="{ active: true }"
            >
              <span class="bot-config-main">
                <strong>{{ botDisplayName(item.bot) }}</strong>
                <small>{{ item.bot.target || '未知类型' }} · {{ item.bot.path }}</small>
              </span>
              <select
                v-if="item.bot.envs?.length"
                class="form-control bot-env-select"
                :value="platformBotEnv(item.mapping.bot_key)"
                @change="setPlatformBotEnv(item.mapping.bot_key, eventValue($event))"
              >
                <option v-for="env in item.bot.envs" :key="env" :value="env">{{ env }}</option>
              </select>
              <input
                v-else
                class="form-control bot-env-select"
                :value="platformBotEnv(item.mapping.bot_key)"
                placeholder="机器人环境"
                @input="setPlatformBotEnv(item.mapping.bot_key, eventValue($event))"
              />
              <button class="btn icon remove-bot-btn" type="button" title="移除机器人" aria-label="移除机器人" @click="removePlatformBot(item.mapping.bot_key)">×</button>
            </div>
          </div>
          <div v-if="botPickerOpen" class="bot-picker">
            <input v-model="botPickerQuery" class="form-control bot-picker-search" placeholder="搜索机器人名称、类型、路径" />
            <div v-if="addableBotRefs.length === 0" class="empty compact-empty">
              {{ availableBotRefs.length === platformDraft.bot_mappings.length ? '已添加全部机器人' : '没有匹配的机器人' }}
            </div>
            <div v-else class="bot-picker-list">
              <button
                v-for="bot in addableBotRefs"
                :key="bot.key"
                class="bot-picker-row"
                type="button"
                @click="addPlatformBot(bot)"
              >
                <span class="bot-config-main">
                  <strong>{{ botDisplayName(bot) }}</strong>
                  <small>{{ bot.target }} · {{ bot.path }}</small>
                </span>
                <span class="add-inline">添加</span>
              </button>
            </div>
          </div>
        </div>
        <div class="config-row ops-row">
          <div class="field sync-settings">
            <label class="enabled-toggle">
              <input v-model="platformDraft.poll_enabled" type="checkbox" />
              后台定时同步
            </label>
            <div class="interval-control" :class="{ disabled: !platformDraft.poll_enabled }">
              <span>每</span>
              <input
                v-model.number="platformDraft.poll_interval_minutes"
                aria-label="后台同步间隔分钟"
                class="interval-input"
                type="number"
                min="1"
                step="1"
                :disabled="!platformDraft.poll_enabled"
              />
              <span>分钟</span>
            </div>
          </div>
          <div class="field config-actions">
            <button class="btn danger" type="button" :disabled="platformDeleting || platformSaving || platformLoggingIn || !platformDraft.id" @click="deleteSelectedPlatform">删除平台</button>
            <button class="btn primary config-save" type="button" :disabled="platformSaving || platformLoggingIn" @click="savePlatform">保存配置</button>
          </div>
        </div>
      </div>
      <div class="trigger-row">
        <button class="btn primary" type="button" :disabled="!selectedPlatform || syncingBugs" @click="syncSelectedPlatform">
          同步我的 Bug
        </button>
        <input v-model="manualBugID" class="form-control" placeholder="Bug ID 或飞书消息" @keyup.enter="fetchManualBug" />
        <button class="btn" type="button" :disabled="!selectedPlatform || !manualBugID.trim() || fetchingBug" @click="fetchManualBug">
          拉取指定 Bug
        </button>
      </div>
      <div class="hook-row">
        <div>
          <strong>Hook URL 可选</strong>
          <code>{{ hookURL || '保存平台后生成' }}</code>
        </div>
        <button class="btn" type="button" :disabled="!hookURL" @click="copyHookURL">复制 Hook URL</button>
      </div>
    </section>

    <BugWorkflowMetrics v-if="workbenchView === 'cases'" :metrics="workflowMetrics" />

    <BugCaseLifecycle
      v-if="workbenchView === 'cases' && (incidentCases.length > 0 || incidentDetail)"
      :cases="incidentCases"
      :detail="incidentDetail"
      :pending="incidentPending"
      :error="incidentError"
      @select="incidentWorkflow.selectCase"
      @refresh="refreshIncidentWorkflow"
      @primary="handleIncidentPrimary"
    />

    <template v-else>
    <section class="bug-workbench">
      <aside class="bug-list">
        <div class="list-head">
          <div>
            <strong>Bug 收件箱</strong>
            <span>{{ filteredBugs.length }} / {{ bugs.length }}</span>
          </div>
          <button class="btn small" type="button" :disabled="bugsLoading" @click="loadBugs">刷新</button>
        </div>
        <input v-model="query" class="search form-control" placeholder="搜索标题、系统、环境、服务" />
        <div v-if="filteredBugs.length === 0" class="empty">
          {{ bugsLoading ? '加载中...' : '暂无同步到的 Bug' }}
        </div>
        <button
          v-for="bug in filteredBugs"
          :key="bug.id"
          type="button"
          class="bug-row"
          :class="{ active: selectedBug?.id === bug.id }"
          @click="selectBug(bug.id)"
        >
          <span class="bug-row-title">{{ bug.title }}</span>
          <span class="bug-row-meta">
            <span>{{ sourceLabel(bug) }}</span>
            <span v-if="bug.env">{{ bug.env }}</span>
            <span v-if="bug.severity">S{{ bug.severity }}</span>
          </span>
        </button>
      </aside>

      <main class="bug-detail">
        <div v-if="!selectedBug" class="empty detail-empty">选择一条 Bug 查看详情</div>
        <template v-else>
          <div class="detail-head">
            <div>
              <div class="detail-source">{{ sourceLabel(selectedBug) }}</div>
              <h2>{{ selectedBug.title }}</h2>
            </div>
            <div class="detail-tags">
              <span v-if="selectedBug.status">{{ selectedBug.status }}</span>
              <span v-if="selectedBug.priority">P{{ selectedBug.priority }}</span>
              <span v-if="selectedBug.env">{{ selectedBug.env }}</span>
            </div>
          </div>

          <dl class="bug-fields">
            <div><dt>所属产品</dt><dd>{{ selectedBug.product || '-' }}</dd></div>
            <div><dt>所属模块</dt><dd>{{ selectedBug.module || '-' }}</dd></div>
            <div><dt>Bug 类型</dt><dd>{{ selectedBug.bug_type || '-' }}</dd></div>
            <div><dt>严重程度</dt><dd>{{ selectedBug.severity ? `S${selectedBug.severity}` : '-' }}</dd></div>
            <div><dt>优先级</dt><dd>{{ selectedBug.priority ? `P${selectedBug.priority}` : '-' }}</dd></div>
            <div><dt>指派</dt><dd>{{ selectedBug.assignee || '-' }}</dd></div>
            <div><dt>提交</dt><dd>{{ selectedBug.reporter || '-' }}</dd></div>
            <div><dt>创建时间</dt><dd>{{ fmtTime(selectedBug.created_at) }}</dd></div>
            <div><dt>更新时间</dt><dd>{{ fmtTime(selectedBug.updated_at) }}</dd></div>
            <div><dt>操作系统</dt><dd>{{ selectedBug.os || '-' }}</dd></div>
            <div><dt>浏览器</dt><dd>{{ selectedBug.browser || '-' }}</dd></div>
            <div><dt>关键词</dt><dd>{{ selectedBug.keywords || '-' }}</dd></div>
          </dl>

          <section class="text-block">
            <h3>复现步骤</h3>
            <article class="rich-text markdown-result" v-html="selectedBugStepsHTML"></article>
          </section>
          <section v-if="selectedBug.description" class="text-block">
            <h3>描述</h3>
            <article class="rich-text markdown-result" v-html="selectedBugDescriptionHTML"></article>
          </section>
          <section v-if="selectedBugAttachments.length" class="attachments-block">
            <h3>附件</h3>
            <div class="attachment-grid">
              <button
                v-for="(att, idx) in selectedBugAttachments"
                :key="`${att.name}-${idx}`"
                class="attachment-item"
                type="button"
                :disabled="attachmentPreviewing"
                @click="previewAttachment(idx)"
              >
                <span class="attachment-thumb" :class="{ loading: isImageAttachment(att) && !attachmentThumbnailSrc(idx) }">
                  <img
                    v-if="attachmentThumbnailSrc(idx)"
                    class="attachment-thumb-img"
                    :src="attachmentThumbnailSrc(idx)"
                    :alt="att.name"
                    loading="lazy"
                  >
                  <svg v-else-if="isImageAttachment(att)" class="attachment-thumb-icon" viewBox="0 0 24 24" aria-hidden="true">
                    <path d="M4 5.5A2.5 2.5 0 0 1 6.5 3h11A2.5 2.5 0 0 1 20 5.5v13a2.5 2.5 0 0 1-2.5 2.5h-11A2.5 2.5 0 0 1 4 18.5v-13Zm2.5-.8a.8.8 0 0 0-.8.8v9.67l3.18-3.18a1.5 1.5 0 0 1 2.12 0l1.58 1.58 3.08-3.58a1.5 1.5 0 0 1 2.25-.04l.39.42V5.5a.8.8 0 0 0-.8-.8h-11Zm11.8 8.18-1.47-1.58-3.08 3.58a1.5 1.5 0 0 1-2.2.09L9.94 13.4 5.7 17.65v.85c0 .44.36.8.8.8h11c.44 0 .8-.36.8-.8v-5.62ZM8.6 8.5a1.4 1.4 0 1 1 2.8 0 1.4 1.4 0 0 1-2.8 0Z" />
                  </svg>
                  <svg v-else class="attachment-thumb-icon" viewBox="0 0 24 24" aria-hidden="true">
                    <path d="M6 3h8.2L19 7.8V19a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2Zm7.3 1.8V8.7h3.9L13.3 4.8ZM6 4.7a.3.3 0 0 0-.3.3v14c0 .17.13.3.3.3h11a.3.3 0 0 0 .3-.3V10.4h-4.8a.9.9 0 0 1-.9-.9V4.7H6Zm1.7 8.1h7.6v1.6H7.7v-1.6Zm0 3h5.6v1.6H7.7v-1.6Z" />
                  </svg>
                </span>
                <span class="attachment-main">
                  <strong>{{ att.name }}</strong>
                  <small>{{ attachmentSubtitle(att) }}</small>
                </span>
                <span class="attachment-action">预览</span>
              </button>
            </div>
          </section>
        </template>
      </main>

      <aside class="bot-panel">
        <div class="panel-title">选择排障机器人</div>
        <div v-if="matching" class="empty">匹配中...</div>
        <div v-else-if="matches.length === 0" class="empty">{{ platformBotEmptyText }}</div>
        <label
          v-for="m in matches"
          :key="m.bot.key"
          class="bot-match"
          :class="{ active: selectedBotKey === m.bot.key }"
        >
          <input v-model="selectedBotKey" type="radio" :value="m.bot.key" @change="rememberSelectedBot(m.bot.key)" />
          <span>
            <strong>{{ m.bot.name || m.bot.system_id || m.bot.path }}</strong>
            <em>{{ m.bot.target }}</em>
            <small v-if="m.bot.env" class="bot-env-pill">环境 {{ m.bot.env }}</small>
          </span>
        </label>
        <div class="bot-actions">
          <button class="btn primary" type="button" :disabled="!selectedBug || !selectedBotKey || !selectedBotSupportsDirectLaunch || investigationStarting" @click="startInvestigation">
            {{ investigationStarting ? '启动中...' : '开始排障' }}
          </button>
          <button v-if="selectedBot && !selectedBotSupportsDirectLaunch" class="btn" type="button" :disabled="contextGenerating" @click="generateContext">
            {{ contextGenerating ? '生成中...' : '生成上下文' }}
          </button>
          <button v-if="canCancelInvestigation" class="btn" type="button" :disabled="investigationCancelling" @click="cancelInvestigation">
            停止
          </button>
          <button v-if="canStartFix" class="btn accent" type="button" :disabled="fixStarting" @click="startFix">
            {{ fixStarting ? '启动中...' : '启动修复 Agent' }}
          </button>
          <button v-if="copyableInvestigationText" class="btn" type="button" @click="copyInvestigationOutput">{{ copyInvestigationLabel }}</button>
        </div>
        <p v-if="selectedBotUnsupportedReason" class="muted direct-launch-note">{{ selectedBotUnsupportedReason }}</p>
      </aside>
    </section>

    <section class="bug-output-panel">
      <div class="output-head">
        <strong>验证 / 排障</strong>
        <span>{{ selectedRun ? selectedRun.status : '未开始' }}</span>
      </div>
      <div v-if="selectedRun" class="output-tabs" role="tablist" aria-label="验证与排障输出">
        <button
          class="output-tab"
          :class="{ active: outputTab === 'validation' }"
          type="button"
          role="tab"
          :aria-selected="outputTab === 'validation'"
          @click="outputTab = 'validation'"
        >
          验证证据
        </button>
        <button
          class="output-tab"
          :class="{ active: outputTab === 'investigation' }"
          type="button"
          role="tab"
          :aria-selected="outputTab === 'investigation'"
          @click="outputTab = 'investigation'"
        >
          排障分析
        </button>
        <button
          v-if="hasFixOutput"
          class="output-tab"
          :class="{ active: outputTab === 'fix' }"
          type="button"
          role="tab"
          :aria-selected="outputTab === 'fix'"
          @click="outputTab = 'fix'"
        >
          修复提交
        </button>
      </div>
      <div ref="outputScrollRef" class="context-preview" role="log" aria-live="polite">
        <template v-if="selectedRun">
          <template v-if="outputTab === 'validation'">
            <div v-if="validationEventLines.length" class="process-log">
              <div v-for="(line, idx) in validationEventLines" :key="idx" class="process-line">{{ line }}</div>
            </div>
            <article v-if="validationFinalText" class="markdown-result validation-result" v-html="renderedValidationMarkdown"></article>
            <div v-if="!validationEventLines.length && !validationFinalText" class="preview-placeholder">验证 Agent 完成后在这里显示取证过程和证据</div>
          </template>
          <template v-else>
            <template v-if="outputTab === 'fix'">
              <div v-if="fixEventLines.length" class="process-log">
                <div v-for="(line, idx) in fixEventLines" :key="idx" class="process-line">{{ line }}</div>
              </div>
              <article v-if="fixFinalText" class="markdown-result" v-html="renderedFixMarkdown"></article>
              <div v-if="!fixEventLines.length && !fixFinalText" class="preview-placeholder">修复 Agent 启动后在这里显示修复、提交和推送过程</div>
            </template>
            <template v-else>
            <div v-if="investigationEventLines.length" class="process-log">
              <div v-for="(line, idx) in investigationEventLines" :key="idx" class="process-line">{{ line }}</div>
            </div>
            <article v-if="investigationFinalText" class="markdown-result" v-html="renderedInvestigationMarkdown"></article>
            <div v-if="!investigationEventLines.length && !investigationFinalText" class="preview-placeholder">验证完成后在这里显示排障过程和结论</div>
            </template>
          </template>
        </template>
        <article v-else-if="contextText" class="markdown-result" v-html="renderedInvestigationMarkdown"></article>
        <div v-else class="preview-placeholder">开始排障后在这里显示过程和结论</div>
      </div>
      <button
        v-if="selectedRun"
        class="supplement-fab"
        :class="{ needsInput: latestRunNeedsUserInput }"
        type="button"
        :title="`补充信息给${outputPhaseLabel}`"
        :aria-label="`补充信息给${outputPhaseLabel}`"
        :disabled="canCancelInvestigation"
        @click="openSupplementDialog"
      >
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <line x1="22" y1="2" x2="11" y2="13"></line>
          <polygon points="22 2 15 22 11 13 2 9 22 2"></polygon>
        </svg>
      </button>
    </section>
    </template>

    <!-- 补充信息弹窗 -->
    <div v-if="supplementDialogOpen" class="supplement-backdrop" @click.self="closeSupplementDialog">
      <div class="supplement-modal">
        <div class="supplement-modal-head">
          <strong>补充信息给{{ supplementPhaseLabel }}</strong>
          <span v-if="latestRunNeedsUserInput" class="supplement-modal-hint">{{ supplementPhaseLabel }} 需要更多信息才能继续</span>
          <button class="btn icon" type="button" aria-label="关闭" @click="closeSupplementDialog">×</button>
        </div>
        <textarea
          v-model="userSupplementInput"
          class="supplement-textarea"
          rows="6"
          :placeholder="supplementPhase === 'validation' ? '输入验证 Agent 缺失的信息，如入口 URL、测试账号、复现条件、截图说明等' : supplementPhase === 'fix' ? '输入修复 Agent 需要补充的要求，如期望分支名、测试命令、修复范围或提交说明等' : '输入排障 Agent 缺失的信息，如日志片段、trace id、服务线索、配置变更等'"
          :disabled="continuingInvestigation"
        ></textarea>
        <div class="supplement-modal-actions">
          <button class="btn" type="button" @click="closeSupplementDialog">取消</button>
          <button
            class="btn primary"
            type="button"
            :disabled="!userSupplementInput.trim() || continuingInvestigation"
            @click="submitSupplement"
          >
            {{ continuingInvestigation ? '启动中...' : `提交并${supplementActionLabel}` }}
          </button>
        </div>
      </div>
    </div>

    <div v-if="attachmentPreview" class="attachment-preview-backdrop" @click.self="attachmentPreview = null">
      <div class="attachment-preview-modal">
        <div class="attachment-preview-head">
          <strong>{{ attachmentPreview.name }}</strong>
          <button class="btn icon" type="button" aria-label="关闭附件预览" @click="attachmentPreview = null">×</button>
        </div>
        <img
          v-if="attachmentPreview.content_type.startsWith('image/')"
          class="attachment-preview-image"
          :src="attachmentPreview.data_url"
          :alt="attachmentPreview.name"
        />
        <div v-else class="attachment-preview-fallback">
          <p>该附件类型暂不支持内嵌预览。</p>
          <a class="btn" :href="attachmentPreview.data_url" :download="attachmentPreview.name">下载附件</a>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.bug-page { max-width: 1440px; height: 100%; display: flex; flex-direction: column; gap: 14px; }
.bug-header { display: flex; justify-content: space-between; gap: 16px; align-items: flex-start; }
.bug-header h1 { font-size: 24px; color: #0f172a; margin-bottom: 4px; }
.bug-header p { color: #64748b; font-size: 13px; line-height: 1.5; }
.workbench-view-tabs { display: flex; gap: 6px; min-width: 0; border-bottom: 1px solid var(--c-line); }
.workbench-view-tabs button { min-height: 44px; padding: 8px 14px; border: 0; border-bottom: 3px solid transparent; background: transparent; color: var(--c-muted); font: inherit; font-weight: 600; cursor: pointer; }
.workbench-view-tabs button:hover:not(:disabled) { color: var(--c-ink); background: var(--c-surf-2); }
.workbench-view-tabs button.active { border-bottom-color: var(--c-accent); color: var(--c-ink); }
.workbench-view-tabs button:focus-visible { outline: 3px solid rgba(37, 99, 235, .45); outline-offset: -3px; }
.workbench-view-tabs button:disabled { opacity: .45; cursor: not-allowed; }
.workbench-view-tabs span { margin-left: 4px; color: var(--c-muted); font-size: var(--fs-xs); }
.platform-config {
  display: none; width: 100%; min-width: 0; box-sizing: border-box;
  border: 1px solid #e2e8f0; border-radius: 8px; background: #f8fafc; padding: 12px; gap: 10px;
}
.platform-config.open { display: grid; }
.platform-list {
  display: flex;
  gap: 8px;
  align-items: center;
  justify-content: space-between;
  min-width: 0;
}
.platform-tabs { display: flex; flex: 1; flex-wrap: wrap; gap: 8px; min-width: 0; }
.platform-chip {
  border: 1px solid #cbd5e1; background: #fff; color: #334155; border-radius: 6px; padding: 5px 9px;
  display: inline-flex; align-items: center; gap: 8px; cursor: pointer; font-family: inherit; font-size: 12px;
}
.platform-chip.active { border-color: #3b82f6; background: #eff6ff; color: #1e40af; }
.platform-chip span { color: #64748b; font-size: 10px; }
.add-platform {
  width: 34px;
  min-width: 34px;
  height: 34px;
  padding: 0;
  justify-content: center;
  font-size: 18px;
  line-height: 1;
}
.config-grid {
  display: flex;
  flex-direction: column;
  gap: 10px;
  min-width: 0;
}
.config-row {
  display: grid;
  gap: 10px;
  align-items: stretch;
  min-width: 0;
}
.basic-row {
  grid-template-columns: minmax(220px, 1fr) minmax(130px, 160px) minmax(360px, 2fr);
}
.auth-row {
  grid-template-columns: minmax(220px, 1fr) minmax(420px, 1.45fr) minmax(170px, 220px);
}
.ops-row {
  grid-template-columns: minmax(360px, 1fr) minmax(250px, auto);
}
.field { min-width: 0; }
.form-control {
  width: 100%;
  height: 38px;
  min-width: 0;
  box-sizing: border-box;
  border: 1px solid #cbd5e1;
  border-radius: 6px;
  background: #fff;
  color: #0f172a;
  padding: 0 11px;
  font-family: inherit;
  font-size: 13px;
  line-height: 38px;
}
select.form-control { padding-right: 28px; }
.form-control::placeholder { color: #94a3b8; opacity: 1; }
.form-control:focus {
  outline: none;
  border-color: #3b82f6;
  box-shadow: 0 0 0 3px rgba(59, 130, 246, 0.16);
}
.form-control:disabled { background: #f1f5f9; color: #94a3b8; cursor: not-allowed; }
.login-field {
  height: 38px;
  box-sizing: border-box;
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  overflow: hidden;
}
.login-field .btn {
  height: 38px;
  min-width: 112px;
  justify-content: center;
}
.login-state {
  height: 38px;
  min-width: 118px;
  border: 1px solid #cbd5e1;
  border-radius: 6px;
  background: #f8fafc;
  color: #64748b;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 0 11px;
  font-size: 13px;
  white-space: nowrap;
  cursor: default;
}
.login-state strong {
  color: #64748b;
  font-size: 13px;
  font-weight: 700;
}
.login-state strong.ok {
  border-color: #bbf7d0;
  color: #166534;
}
.toggle-cell,
.sync-settings {
  height: 38px;
  box-sizing: border-box;
  display: flex;
  align-items: center;
  border: 1px solid #cbd5e1;
  border-radius: 6px;
  background: #fff;
  padding: 0 11px;
  min-width: 0;
}
.toggle-cell { gap: 8px; }
.sync-settings {
  justify-content: space-between;
  gap: 12px;
}
.enabled-toggle { display: inline-flex; gap: 7px; align-items: center; color: #334155; font-size: 13px; white-space: nowrap; }
.enabled-toggle input { width: 16px; height: 16px; margin: 0; }
.interval-control {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  color: #475569;
  font-size: 12px;
  white-space: nowrap;
}
.interval-control.disabled { color: #94a3b8; }
.interval-input {
  width: 58px;
  height: 28px;
  box-sizing: border-box;
  border: 1px solid #cbd5e1;
  border-radius: 6px;
  background: #fff;
  color: #0f172a;
  padding: 0 6px;
  font: inherit;
  font-size: 12px;
  line-height: 28px;
}
.interval-input:disabled { background: #f1f5f9; color: #94a3b8; cursor: not-allowed; }
.config-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  min-width: 0;
}
.config-actions .btn {
  height: 38px;
  min-width: 120px;
  justify-content: center;
}
.bot-config-block {
  border: 1px solid #e2e8f0;
  border-radius: 6px;
  background: #fff;
  padding: 10px;
  min-width: 0;
}
.bot-config-title {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  align-items: center;
  margin-bottom: 8px;
}
.bot-config-title > div {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.bot-config-title strong {
  color: #0f172a;
  font-size: 13px;
}
.bot-config-title span {
  color: #64748b;
  font-size: 12px;
}
.add-bot-btn {
  min-width: 72px;
  height: 32px;
  justify-content: center;
}
.bot-config-list {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
  gap: 8px;
}
.bot-config-row {
  min-width: 0;
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(110px, 150px) 32px;
  gap: 8px;
  align-items: center;
  border: 1px solid #e2e8f0;
  border-radius: 6px;
  background: #fff;
  padding: 8px;
}
.bot-config-row.active {
  border-color: #3b82f6;
  background: #eff6ff;
}
.bot-config-main {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.bot-config-main strong {
  color: #0f172a;
  font-size: 13px;
  word-break: break-word;
}
.bot-config-main small {
  color: #64748b;
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.bot-env-select {
  height: 34px;
  line-height: 34px;
  font-size: 12px;
  padding: 0 8px;
}
.remove-bot-btn {
  width: 32px;
  min-width: 32px;
  height: 32px;
  padding: 0;
  justify-content: center;
  color: #64748b;
}
.remove-bot-btn:hover:not(:disabled) {
  border-color: #fca5a5;
  color: #b91c1c;
  background: #fef2f2;
}
.bot-picker {
  margin-top: 8px;
  border-top: 1px solid #e2e8f0;
  padding-top: 8px;
  display: grid;
  gap: 8px;
}
.bot-picker-search {
  height: 34px;
  line-height: 34px;
  font-size: 12px;
}
.bot-picker-list {
  max-height: 220px;
  overflow: auto;
  display: grid;
  gap: 6px;
}
.bot-picker-row {
  width: 100%;
  min-width: 0;
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 8px;
  align-items: center;
  text-align: left;
  border: 1px solid #e2e8f0;
  border-radius: 6px;
  background: #fff;
  padding: 8px;
  font-family: inherit;
  cursor: pointer;
}
.bot-picker-row:hover {
  border-color: #3b82f6;
  background: #eff6ff;
}
.add-inline {
  color: #2563eb;
  font-size: 12px;
  font-weight: 700;
}
.compact-empty {
  padding: 12px;
}
.btn.danger {
  border-color: #fecaca;
  background: #fff;
  color: #b91c1c;
}
.btn.danger:hover:not(:disabled) {
  background: #fef2f2;
  border-color: #fca5a5;
}
.hook-row {
  display: flex; justify-content: space-between; gap: 12px; align-items: center;
  background: #fff; border: 1px solid #e2e8f0; border-radius: 6px; padding: 10px;
}
.trigger-row {
  display: grid; grid-template-columns: minmax(110px, auto) minmax(180px, 1fr) minmax(120px, auto); gap: 8px; align-items: center;
  background: #fff; border: 1px solid #e2e8f0; border-radius: 6px; padding: 10px;
}
.trigger-row input { width: 100%; min-width: 0; }
.trigger-row .btn {
  height: 38px;
  justify-content: center;
}
.hook-row strong { display: block; color: #0f172a; font-size: 12px; margin-bottom: 4px; }
.hook-row code { color: #334155; font-size: 12px; word-break: break-all; }
.hook-row .btn {
  height: 38px;
  min-width: 125px;
  justify-content: center;
}
.bug-workbench {
  flex: 1 1 0; min-height: 320px; display: grid;
  grid-template-columns: minmax(240px, 300px) minmax(420px, 1fr) minmax(300px, 360px);
  gap: 14px;
}
.bug-output-panel {
  flex: 1 1 clamp(380px, 45vh, 700px);
  min-height: 320px;
  min-width: 0;
  position: relative;
  display: flex;
  flex-direction: column;
  gap: 8px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  background: #fff;
  padding: 12px;
}
.output-head {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  align-items: center;
}
.output-head strong {
  color: #0f172a;
  font-size: 13px;
}
.output-head span {
  color: #64748b;
  font-size: 12px;
}
.output-tabs {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 6px;
}
.output-tab {
  min-height: 34px;
  border: 1px solid #cbd5e1;
  border-radius: 7px;
  background: #fff;
  color: #475569;
  font-size: 12px;
  font-weight: 700;
  cursor: pointer;
  transition: border-color .16s ease, background .16s ease, color .16s ease;
}
.output-tab:hover {
  border-color: #93c5fd;
  color: #1d4ed8;
}
.output-tab:focus-visible {
  outline: 2px solid #2563eb;
  outline-offset: 2px;
}
.output-tab.active {
  border-color: #2563eb;
  background: #eff6ff;
  color: #1d4ed8;
}
.bug-list,
.bug-detail,
.bot-panel {
  min-height: 0; overflow: auto;
  border: 1px solid #e2e8f0; border-radius: 8px; background: #fff;
}
.bug-list { padding: 10px; display: flex; flex-direction: column; gap: 8px; }
.list-head { display: flex; align-items: center; justify-content: space-between; gap: 8px; }
.list-head div { display: flex; flex-direction: column; gap: 2px; }
.list-head strong { color: #0f172a; font-size: 14px; }
.list-head span { color: #64748b; font-size: 11px; }
.search { height: 34px; line-height: 34px; padding: 0 9px; font-size: 12px; }
.bug-row {
  text-align: left; border: 1px solid #e2e8f0; border-radius: 6px; background: #fff;
  padding: 10px; cursor: pointer; display: flex; flex-direction: column; gap: 6px;
  font-family: inherit;
}
.bug-row:hover { background: #f8fafc; border-color: #cbd5e1; }
.bug-row.active { border-color: #3b82f6; background: #eff6ff; }
.bug-row-title {
  color: #0f172a; font-size: 13px; font-weight: 600;
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
}
.bug-row-meta { display: flex; flex-wrap: wrap; gap: 5px; color: #64748b; font-size: 11px; }
.bug-row-meta span { background: #f1f5f9; border-radius: 999px; padding: 1px 6px; }
.bug-detail { padding: 16px; }
.detail-head { display: flex; justify-content: space-between; gap: 12px; align-items: flex-start; margin-bottom: 14px; }
.detail-source { color: #2563eb; font-size: 12px; font-weight: 700; margin-bottom: 4px; }
.detail-head h2 { font-size: 20px; line-height: 1.35; color: #0f172a; }
.detail-tags { display: flex; gap: 6px; flex-wrap: wrap; justify-content: flex-end; }
.detail-tags span,
.pill {
  display: inline-flex; align-items: center;
  background: #eef2ff; color: #3730a3; border: 1px solid #c7d2fe;
  border-radius: 999px; padding: 2px 8px; font-size: 11px; margin: 0 4px 4px 0;
}
.bug-fields { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; margin-bottom: 14px; }
.bug-fields div { background: #f8fafc; border: 1px solid #e2e8f0; border-radius: 6px; padding: 8px; }
.bug-fields dt { color: #64748b; font-size: 11px; margin-bottom: 3px; }
.bug-fields dd { color: #0f172a; font-size: 13px; word-break: break-all; }
.text-block { margin-top: 12px; }
.text-block h3,
.evidence-grid h3 { font-size: 13px; color: #0f172a; margin-bottom: 6px; }
.text-block pre,
.text-block .rich-text {
  background: #f8fafc; border: 1px solid #e2e8f0; border-radius: 6px;
  padding: 10px; color: #334155; white-space: pre-wrap; word-break: break-word;
  font-family: inherit; font-size: 13px; line-height: 1.55;
}
.attachments-block { margin-top: 12px; }
.attachments-block h3 { font-size: 13px; color: #0f172a; margin-bottom: 6px; }
.attachment-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 8px;
}
.attachment-item {
  width: 100%;
  min-width: 0;
  display: grid;
  grid-template-columns: 56px minmax(0, 1fr) auto;
  gap: 10px;
  align-items: center;
  text-align: left;
  border: 1px solid #e2e8f0;
  border-radius: 6px;
  background: #fff;
  padding: 8px 10px 8px 8px;
  font-family: inherit;
  cursor: pointer;
  transition: border-color 0.18s ease, background 0.18s ease, box-shadow 0.18s ease;
}
.attachment-item:hover:not(:disabled) {
  border-color: #3b82f6;
  background: #eff6ff;
  box-shadow: 0 1px 3px rgba(15, 23, 42, 0.08);
}
.attachment-item:disabled {
  cursor: wait;
  opacity: 0.7;
}
.attachment-thumb {
  width: 56px;
  height: 44px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  color: #475569;
  overflow: hidden;
  flex-shrink: 0;
}
.attachment-thumb.loading {
  background:
    linear-gradient(90deg, rgba(241, 245, 249, 0.6), rgba(226, 232, 240, 0.95), rgba(241, 245, 249, 0.6));
  background-size: 180% 100%;
  animation: attachment-thumb-pulse 1.2s ease-in-out infinite;
}
.attachment-thumb-img {
  width: 100%;
  height: 100%;
  object-fit: cover;
  display: block;
}
.attachment-thumb-icon {
  width: 22px;
  height: 22px;
  fill: currentColor;
}
@keyframes attachment-thumb-pulse {
  0% { background-position: 100% 0; }
  100% { background-position: -100% 0; }
}
.attachment-main {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.attachment-main strong {
  color: #0f172a;
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.attachment-main small {
  color: #64748b;
  font-size: 11px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.attachment-action {
  color: #2563eb;
  font-size: 12px;
  font-weight: 700;
}
.evidence-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px; margin-top: 12px; }
.muted { color: #94a3b8; font-size: 12px; }
.bot-panel { padding: 12px; display: flex; flex-direction: column; gap: 8px; }
.panel-title { font-size: 13px; font-weight: 700; color: #0f172a; margin-bottom: 2px; }
.bot-match {
  display: grid; grid-template-columns: 16px 1fr; gap: 8px; align-items: flex-start;
  border: 1px solid #e2e8f0; border-radius: 6px; padding: 9px; cursor: pointer;
}
.bot-match.active { border-color: #3b82f6; background: #eff6ff; }
.bot-match input { margin-top: 2px; }
.bot-match strong { display: block; color: #0f172a; font-size: 13px; word-break: break-all; }
.bot-match em { display: block; color: #475569; font-style: normal; font-size: 11px; margin-top: 2px; }
.bot-match small { display: block; color: #64748b; font-size: 11px; line-height: 1.4; margin-top: 4px; }
.bot-match .bot-env-pill {
  display: inline-flex;
  align-items: center;
  width: fit-content;
  margin-top: 6px;
  padding: 1px 7px;
  border-radius: 999px;
  background: #eef2ff;
  color: #3730a3;
  border: 1px solid #c7d2fe;
  font-weight: 700;
}
.bot-actions { display: flex; gap: 8px; margin-top: 2px; }
.direct-launch-note { margin: 0; }
.context-preview {
  flex: 1; min-height: 0; overflow: auto; box-sizing: border-box;
  border: 1px solid #cbd5e1; border-radius: 6px; background: #fff;
  padding: 10px; color: #0f172a; font-size: 13px; line-height: 1.55;
}
.process-log {
  margin-bottom: 12px; padding: 9px; border: 1px solid #e2e8f0; border-radius: 6px;
  background: #f8fafc; color: #475569; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px; line-height: 1.5;
}
.process-line { white-space: pre-wrap; word-break: break-word; }
.process-line + .process-line { margin-top: 4px; }
.markdown-result { color: #0f172a; word-break: break-word; }
.markdown-result :deep(h1),
.markdown-result :deep(h2),
.markdown-result :deep(h3) {
  color: #0f172a; font-weight: 800; line-height: 1.3; margin: 14px 0 8px;
}
.markdown-result :deep(h1) { font-size: 18px; }
.markdown-result :deep(h2) { font-size: 16px; }
.markdown-result :deep(h3) { font-size: 14px; }
.markdown-result :deep(p) { margin: 7px 0; }
.markdown-result :deep(ol),
.markdown-result :deep(ul) { margin: 8px 0 8px 20px; padding: 0; }
.markdown-result :deep(li) { margin: 4px 0; }
.markdown-result :deep(pre) {
  overflow: auto; margin: 8px 0; padding: 9px; border: 1px solid #e2e8f0; border-radius: 6px;
  background: #f8fafc; color: #334155; font-size: 12px; line-height: 1.5;
}
.markdown-result :deep(code) {
  padding: 1px 4px; border-radius: 4px; background: #f1f5f9;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12px;
}
.markdown-result :deep(pre code) { padding: 0; background: transparent; }
.markdown-result :deep(blockquote) {
  margin: 8px 0; padding-left: 10px; border-left: 3px solid #cbd5e1; color: #475569;
}
.markdown-result :deep(table) { width: 100%; border-collapse: collapse; margin: 8px 0; font-size: 12px; }
.markdown-result :deep(th),
.markdown-result :deep(td) { border: 1px solid #e2e8f0; padding: 6px; text-align: left; vertical-align: top; }
.markdown-result :deep(a) { color: #2563eb; text-decoration: none; }
.preview-placeholder {
  min-height: 220px; display: flex; align-items: center; justify-content: center;
  color: #94a3b8; font-size: 13px;
}
/* 补充信息 FAB 按钮 */
.supplement-fab {
  position: absolute;
  bottom: 12px;
  right: 12px;
  width: 40px;
  height: 40px;
  border-radius: 50%;
  border: 1px solid #cbd5e1;
  background: #fff;
  color: #64748b;
  display: flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
  box-shadow: 0 2px 8px rgba(15, 23, 42, 0.12);
  transition: all 0.18s ease;
  z-index: 10;
}
.supplement-fab:hover:not(:disabled) {
  border-color: #3b82f6;
  background: #eff6ff;
  color: #2563eb;
  transform: scale(1.08);
  box-shadow: 0 4px 14px rgba(59, 130, 246, 0.22);
}
.supplement-fab.needsInput {
  border-color: #f59e0b;
  background: #fffbeb;
  color: #d97706;
  animation: fab-pulse 2s ease-in-out infinite;
}
.supplement-fab.needsInput:hover:not(:disabled) {
  border-color: #d97706;
  background: #fef3c7;
}
.supplement-fab:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
@keyframes fab-pulse {
  0%, 100% { box-shadow: 0 2px 8px rgba(15, 23, 42, 0.12); }
  50% { box-shadow: 0 2px 18px rgba(245, 158, 11, 0.45); }
}

/* 补充信息弹窗 */
.supplement-backdrop {
  position: fixed;
  inset: 0;
  z-index: 70;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 28px;
  background: rgba(15, 23, 42, 0.5);
}
.supplement-modal {
  width: min(560px, 92vw);
  max-height: 88vh;
  display: flex;
  flex-direction: column;
  border-radius: 10px;
  border: 1px solid #cbd5e1;
  background: #fff;
  box-shadow: 0 20px 60px rgba(15, 23, 42, 0.28);
  overflow: hidden;
}
.supplement-modal-head {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px 14px;
  border-bottom: 1px solid #e2e8f0;
}
.supplement-modal-head strong {
  color: #0f172a;
  font-size: 14px;
  white-space: nowrap;
}
.supplement-modal-hint {
  color: #d97706;
  font-size: 12px;
  font-weight: 600;
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.supplement-textarea {
  flex: 1;
  min-height: 140px;
  border: none;
  border-radius: 0;
  background: #fff;
  color: #0f172a;
  padding: 12px 14px;
  font-family: inherit;
  font-size: 13px;
  line-height: 1.55;
  resize: vertical;
  outline: none;
}
.supplement-textarea::placeholder {
  color: #94a3b8;
  opacity: 1;
}
.supplement-textarea:focus {
  outline: none;
  box-shadow: inset 0 0 0 2px #3b82f6;
}
.supplement-textarea:disabled {
  background: #f8fafc;
  color: #94a3b8;
}
.supplement-modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 10px 14px;
  border-top: 1px solid #e2e8f0;
}
.supplement-modal-actions .btn {
  height: 38px;
  min-width: 90px;
  justify-content: center;
}

.empty {
  color: #94a3b8; background: #f8fafc; border: 1px dashed #cbd5e1;
  border-radius: 6px; padding: 18px 12px; text-align: center; font-size: 13px;
}
.detail-empty { height: 100%; display: flex; align-items: center; justify-content: center; }
.attachment-preview-backdrop {
  position: fixed;
  inset: 0;
  z-index: 60;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 28px;
  background: rgba(15, 23, 42, 0.58);
}
.attachment-preview-modal {
  width: min(1080px, 92vw);
  max-height: 88vh;
  min-height: 240px;
  display: flex;
  flex-direction: column;
  border-radius: 8px;
  border: 1px solid #cbd5e1;
  background: #fff;
  box-shadow: 0 20px 60px rgba(15, 23, 42, 0.28);
  overflow: hidden;
}
.attachment-preview-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 10px 12px;
  border-bottom: 1px solid #e2e8f0;
}
.attachment-preview-head strong {
  color: #0f172a;
  font-size: 13px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.attachment-preview-image {
  max-width: 100%;
  max-height: calc(88vh - 54px);
  object-fit: contain;
  background: #0f172a;
}
.attachment-preview-fallback {
  min-height: 220px;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  gap: 12px;
  color: #64748b;
  padding: 24px;
}
@media (max-width: 1180px) {
  .basic-row { grid-template-columns: minmax(180px, 1fr) minmax(140px, 170px); }
  .basic-row .field:last-child { grid-column: 1 / -1; }
  .auth-row { grid-template-columns: minmax(180px, 1fr) minmax(360px, 1.4fr); }
  .auth-row .toggle-cell { grid-column: 1 / -1; }
  .ops-row { grid-template-columns: 1fr; }
  .config-actions { justify-content: flex-start; }
  .bug-workbench { grid-template-columns: 280px 1fr; }
  .bot-panel { grid-column: 1 / -1; }
  .bug-output-panel { flex: 1 1 clamp(340px, 40vh, 520px); min-height: 320px; }
}
@media (max-width: 760px) {
  .bug-header,
  .hook-row { flex-direction: column; align-items: stretch; }
  .trigger-row { grid-template-columns: 1fr; }
  .config-row,
  .bug-workbench,
  .evidence-grid,
  .bug-fields { grid-template-columns: 1fr; }
  .basic-row .field:last-child,
  .auth-row .toggle-cell { grid-column: auto; }
  .config-actions { flex-direction: column-reverse; align-items: stretch; }
  .bot-config-title { flex-direction: column; gap: 3px; align-items: flex-start; }
  .bot-config-list { grid-template-columns: 1fr; }
  .bot-config-row { grid-template-columns: minmax(0, 1fr) 32px; }
  .bot-env-select { grid-column: 1 / -1; grid-row: 2; }
  .remove-bot-btn { grid-column: 2; grid-row: 1; }
  .toggle-cell,
  .sync-settings { height: auto; min-height: 38px; flex-wrap: wrap; padding: 8px 11px; }
  .login-field { height: auto; min-height: 38px; flex-wrap: wrap; }
  .bot-panel { grid-column: auto; }
  .bug-output-panel { flex: 1 1 clamp(300px, 38vh, 460px); min-height: 300px; }
}
</style>
