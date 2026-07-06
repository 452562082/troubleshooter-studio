<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import {
  type BotMatch,
  type BugPlatform,
  type BugRecord,
  cancelBugInvestigation,
  bugHookBaseURL,
  clearBugPlatformLogin,
  deleteBugPlatform,
  fetchBugByID,
  type InvestigationEvent,
  type InvestigationRun,
  loginBugPlatform,
  listBugInvestigationRuns,
  listBugPlatforms,
  listBugs,
  matchBugBots,
  saveBugPlatform,
  startBugInvestigation,
  syncBugPlatform,
} from '../lib/bridge'
import { copyToClipboard } from '../lib/clipboard'
import { confirmDialog } from '../lib/confirm'
import { toast, toastError } from '../lib/toast'

const bugs = ref<BugRecord[]>([])
const platforms = ref<BugPlatform[]>([])
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
const matching = ref(false)
const investigationRuns = ref<InvestigationRun[]>([])
const investigationStarting = ref(false)
const investigationCancelling = ref(false)
const query = ref('')
const manualBugID = ref('')
const configOpen = ref(false)
const platformDraft = ref({
  id: '',
  name: '禅道',
  type: 'zentao',
  base_url: '',
  account: '',
  auth_mode: 'feishu_sso',
  session_header: '',
  password: '',
  token: '',
  hook_secret: '',
  enabled: true,
  poll_enabled: false,
  poll_interval_minutes: 5,
})
let unlistenInvestigationEvents: (() => void) | undefined

const selectedBug = computed(() => bugs.value.find(b => b.id === selectedID.value) || bugs.value[0])
const selectedPlatform = computed(() => platforms.value.find(p => p.id === selectedPlatformID.value))
const selectedPlatformHasSession = computed(() => Boolean(selectedPlatform.value?.session_header))
const selectedBot = computed(() => matches.value.find(m => m.bot.key === selectedBotKey.value)?.bot)
const selectedRun = computed(() => investigationRuns.value[0])
const selectedBotIsCodex = computed(() => selectedBot.value?.target === 'codex')
const investigationOutput = computed(() => {
  const run = selectedRun.value
  if (!run) return contextText.value
  if (run.final_message) return run.final_message
  const lines = (run.events || []).map(e => e.message).filter(Boolean)
  return lines.join('\n') || contextText.value
})
const filteredBugs = computed(() => {
  const kw = query.value.trim().toLowerCase()
  if (!kw) return bugs.value
  return bugs.value.filter(b => [
    b.title, b.source, b.source_id, b.env, b.system_id, b.assignee, ...(b.service_hints || []),
  ].filter(Boolean).join(' ').toLowerCase().includes(kw))
})
const hookURL = computed(() => {
  const p = selectedPlatform.value
  if (!p || !hookBaseURL.value) return ''
  const secret = p.hook_secret ? `?secret=${encodeURIComponent(p.hook_secret)}` : ''
  return `${hookBaseURL.value}/api/bug-hooks/${encodeURIComponent(p.id)}${secret}`
})

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
    enabled: p.enabled,
    poll_enabled: Boolean(p.poll_enabled),
    poll_interval_minutes: p.poll_interval_minutes || 5,
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

onMounted(async () => {
  setupInvestigationEventBridge()
  await Promise.all([loadPlatforms(), loadBugs(), loadHookBase()])
})

onUnmounted(() => {
  if (unlistenInvestigationEvents) {
    unlistenInvestigationEvents()
    unlistenInvestigationEvents = undefined
  }
})

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
    toast.error('请先填写禅道平台地址')
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
    toast.success(`禅道登录态已保存,读取到 ${result.cookie_count} 个 Cookie`)
  } catch (e) {
    await loadPlatforms()
    selectedPlatformID.value = saved.id
    toastError('飞书授权登录禅道', e)
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
    toastError('拉取 Bug', e)
  } finally {
    fetchingBug.value = false
  }
}

async function refreshMatches(bugID: string, preferredKey = '') {
  if (!bugID) return
  matching.value = true
  try {
    const nextMatches = await matchBugBots(bugID)
    if (selectedBug.value?.id !== bugID) return
    matches.value = nextMatches
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
  if (bot.target !== 'codex') {
    toast.error('当前只支持 Codex 机器人直接排障。')
    return
  }
  investigationStarting.value = true
  const bugID = bug.id
  try {
    const run = await startBugInvestigation({ bug_id: bugID, bot })
    if (selectedBug.value?.id === bugID && run.bug_id === bugID) mergeInvestigationRun(run)
    toast.success('排障已启动')
  } catch (e) {
    toastError('启动排障', e)
  } finally {
    investigationStarting.value = false
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
  const events = Array.isArray(run.events)
    ? run.events
    : event
      ? [...(existing?.events || []), event]
      : existing?.events || []
  const merged = { ...(existing || {}), ...run, events }
  investigationRuns.value = idx >= 0
    ? investigationRuns.value.map(item => item.id === run.id ? merged : item)
    : [merged, ...investigationRuns.value]
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

async function copyHookURL() {
  if (!hookURL.value) {
    toast.error('请先保存平台配置')
    return
  }
  ;(await copyToClipboard(hookURL.value)) ? toast.success('Hook URL 已复制') : toast.error('复制失败')
}

async function copyInvestigationOutput() {
  const text = investigationOutput.value
  if (!text.trim()) {
    toast.error('没有可复制的排障输出')
    return
  }
  ;(await copyToClipboard(text)) ? toast.success('已复制') : toast.error('复制失败')
}

function newPlatform() {
  selectedPlatformID.value = ''
  platformDraft.value = {
    id: '',
    name: '禅道',
    type: 'zentao',
    base_url: '',
    account: '',
    auth_mode: 'feishu_sso',
    session_header: '',
    password: '',
    token: '',
    hook_secret: '',
    enabled: true,
    poll_enabled: false,
    poll_interval_minutes: 5,
  }
  configOpen.value = true
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
            <input v-model="platformDraft.name" class="form-control" placeholder="平台名称,如 禅道" />
          </div>
          <div class="field platform-type-field">
            <select v-model="platformDraft.type" class="form-control">
              <option value="zentao">禅道</option>
              <option value="generic">通用 Webhook</option>
            </select>
          </div>
          <div class="field">
            <input v-model="platformDraft.base_url" class="form-control" placeholder="平台地址 https://zentao.example.com" />
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
              {{ platformLoggingIn ? '等待授权' : '登录禅道' }}
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
            <div><dt>系统</dt><dd>{{ selectedBug.system_id || '-' }}</dd></div>
            <div><dt>指派</dt><dd>{{ selectedBug.assignee || '-' }}</dd></div>
            <div><dt>提交</dt><dd>{{ selectedBug.reporter || '-' }}</dd></div>
            <div><dt>更新时间</dt><dd>{{ fmtTime(selectedBug.updated_at) }}</dd></div>
            <div><dt>前端仓库</dt><dd>{{ selectedBug.frontend_repo || '-' }}</dd></div>
            <div><dt>前端 URL</dt><dd>{{ selectedBug.frontend_url || '-' }}</dd></div>
          </dl>

          <section class="text-block">
            <h3>复现步骤</h3>
            <pre>{{ selectedBug.steps || '-' }}</pre>
          </section>
          <section v-if="selectedBug.description" class="text-block">
            <h3>描述</h3>
            <pre>{{ selectedBug.description }}</pre>
          </section>
          <section class="evidence-grid">
            <div>
              <h3>API 路径</h3>
              <span v-for="p in selectedBug.api_paths || []" :key="p" class="pill">{{ p }}</span>
              <span v-if="!selectedBug.api_paths?.length" class="muted">-</span>
            </div>
            <div>
              <h3>Trace / Request</h3>
              <span v-for="p in [...(selectedBug.trace_ids || []), ...(selectedBug.request_ids || [])]" :key="p" class="pill">{{ p }}</span>
              <span v-if="!selectedBug.trace_ids?.length && !selectedBug.request_ids?.length" class="muted">-</span>
            </div>
          </section>
        </template>
      </main>

      <aside class="bot-panel">
        <div class="panel-title">选择排障机器人</div>
        <div v-if="matching" class="empty">匹配中...</div>
        <div v-else-if="matches.length === 0" class="empty">暂无已安装机器人</div>
        <label
          v-for="m in matches"
          :key="m.bot.key"
          class="bot-match"
          :class="{ active: selectedBotKey === m.bot.key }"
        >
          <input v-model="selectedBotKey" type="radio" :value="m.bot.key" />
          <span>
            <strong>{{ m.bot.name || m.bot.system_id || m.bot.path }}</strong>
            <em>{{ m.bot.target }} · score {{ m.score }}</em>
            <small>{{ (m.reasons || []).length ? (m.reasons || []).join(', ') : '无显式匹配,可手动选择' }}</small>
          </span>
        </label>
        <div class="bot-actions">
          <button class="btn primary" type="button" :disabled="!selectedBug || !selectedBotKey || !selectedBotIsCodex || investigationStarting" @click="startInvestigation">
            {{ investigationStarting ? '启动中...' : '开始排障' }}
          </button>
          <button class="btn" type="button" :disabled="!selectedRun || selectedRun.status !== 'running' || investigationCancelling" @click="cancelInvestigation">
            停止
          </button>
          <button class="btn" type="button" :disabled="!investigationOutput.trim()" @click="copyInvestigationOutput">复制</button>
        </div>
        <p v-if="selectedBot && !selectedBotIsCodex" class="muted direct-launch-note">当前只支持 Codex 机器人直接排障。</p>
        <textarea :value="investigationOutput" class="context-preview" readonly placeholder="开始排障后在这里显示过程和结论"></textarea>
      </aside>
    </section>
  </div>
</template>

<style scoped>
.bug-page { max-width: 1440px; height: 100%; display: flex; flex-direction: column; gap: 14px; }
.bug-header { display: flex; justify-content: space-between; gap: 16px; align-items: flex-start; }
.bug-header h1 { font-size: 24px; color: #0f172a; margin-bottom: 4px; }
.bug-header p { color: #64748b; font-size: 13px; line-height: 1.5; }
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
  grid-template-columns: minmax(220px, 1fr) minmax(150px, 180px) minmax(360px, 2fr);
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
  flex: 1; min-height: 0; display: grid;
  grid-template-columns: minmax(240px, 300px) minmax(420px, 1fr) minmax(300px, 360px);
  gap: 14px;
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
.text-block pre {
  background: #f8fafc; border: 1px solid #e2e8f0; border-radius: 6px;
  padding: 10px; color: #334155; white-space: pre-wrap; word-break: break-word;
  font-family: inherit; font-size: 13px; line-height: 1.55;
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
.bot-actions { display: flex; gap: 8px; margin-top: 2px; }
.direct-launch-note { margin: 0; }
.context-preview {
  flex: 1; min-height: 260px; resize: none; font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 12px; line-height: 1.5; color: #0f172a;
}
.empty {
  color: #94a3b8; background: #f8fafc; border: 1px dashed #cbd5e1;
  border-radius: 6px; padding: 18px 12px; text-align: center; font-size: 13px;
}
.detail-empty { height: 100%; display: flex; align-items: center; justify-content: center; }
@media (max-width: 1180px) {
  .basic-row { grid-template-columns: minmax(180px, 1fr) minmax(140px, 170px); }
  .basic-row .field:last-child { grid-column: 1 / -1; }
  .auth-row { grid-template-columns: minmax(180px, 1fr) minmax(360px, 1.4fr); }
  .auth-row .toggle-cell { grid-column: 1 / -1; }
  .ops-row { grid-template-columns: 1fr; }
  .config-actions { justify-content: flex-start; }
  .bug-workbench { grid-template-columns: 280px 1fr; }
  .bot-panel { grid-column: 1 / -1; min-height: 420px; }
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
  .toggle-cell,
  .sync-settings { height: auto; min-height: 38px; flex-wrap: wrap; padding: 8px 11px; }
  .login-field { height: auto; min-height: 38px; flex-wrap: wrap; }
  .bot-panel { grid-column: auto; }
}
</style>
