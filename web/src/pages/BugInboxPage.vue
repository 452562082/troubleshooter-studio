<script setup lang="ts">
import { marked } from 'marked'
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import BugTicketDetail from '../components/BugTicketDetail.vue'
import BugTicketList from '../components/BugTicketList.vue'
import {
  type BotRef,
  type BugAttachmentPreviewResult,
  type BugPlatform,
  type DiscoveredBot,
  type InvestigationRun,
  bugHookBaseURL,
  clearBugPlatformLogin,
  deleteBugPlatform,
  discoverBots,
  fetchBugByID,
  listBugInvestigationRuns,
  listBugPlatforms,
  listBugs,
  loginBugPlatform,
  previewBugAttachment,
  saveBugPlatform,
  syncBugPlatform,
} from '../lib/bridge'
import { copyToClipboard } from '../lib/clipboard'
import { confirmDialog } from '../lib/confirm'
import { toast, toastError } from '../lib/toast'
import { useBugTickets } from '../lib/useBugTickets'

const router = useRouter()
const tickets = useBugTickets({ listBugs, fetchBugByID })
const platforms = ref<BugPlatform[]>([])
const installedBots = ref<DiscoveredBot[]>([])
const hookBaseURL = ref('')
const selectedPlatformID = ref('')
const manualBugID = ref('')
const configOpen = ref(false)
const botPickerOpen = ref(false)
const botPickerQuery = ref('')
const platformSaving = ref(false)
const platformLoggingIn = ref(false)
const loginClearing = ref(false)
const platformDeleting = ref(false)
const syncingBugs = ref(false)
const fetchingBug = ref(false)
const attachmentPreviewing = ref(false)
const attachmentPreview = ref<BugAttachmentPreviewResult | null>(null)
const investigationRuns = ref<InvestigationRun[]>([])

const platformDraft = ref(emptyPlatformDraft())
const selectedPlatform = computed(() => platforms.value.find(platform => platform.id === selectedPlatformID.value))
const selectedPlatformHasSession = computed(() => Boolean(selectedPlatform.value?.session_header))
const allBotRefs = computed(() => installedBots.value.filter(bot => !bot.ghost).map(discoveredBotToRef))
const configuredPlatformBots = computed(() => platformDraft.value.bot_mappings.map(mapping => ({
  mapping,
  bot: botRefByKey(mapping.bot_key),
})))
const addableBotRefs = computed(() => {
  const configured = new Set(platformDraft.value.bot_mappings.map(mapping => mapping.bot_key))
  const keyword = botPickerQuery.value.trim().toLowerCase()
  return allBotRefs.value
    .filter(bot => !configured.has(bot.key))
    .filter(bot => !keyword || [botDisplayName(bot), bot.target, bot.path, bot.system_id].join(' ').toLowerCase().includes(keyword))
})
const selectedRun = computed(() => investigationRuns.value[0])
const legacyEventLines = computed(() => (selectedRun.value?.events || [])
  .map(event => event.message?.trim())
  .filter((message): message is string => Boolean(message) && message !== selectedRun.value?.final_message?.trim()))
const legacyFinalText = computed(() => selectedRun.value?.final_message || selectedRun.value?.error || '')
const renderedLegacyMarkdown = computed(() => safeMarkdown(legacyFinalText.value))
const hookURL = computed(() => {
  const platform = selectedPlatform.value
  if (!platform || !hookBaseURL.value) return ''
  const secret = platform.hook_secret ? `?secret=${encodeURIComponent(platform.hook_secret)}` : ''
  return `${hookBaseURL.value}/api/bug-hooks/${encodeURIComponent(platform.id)}${secret}`
})

watch(selectedPlatform, platform => {
  if (!platform) return
  platformDraft.value = {
    id: platform.id,
    name: platform.name || '',
    type: platform.type || 'zentao',
    base_url: platform.base_url || '',
    account: platform.account || '',
    auth_mode: platform.auth_mode || 'feishu_sso',
    session_header: '',
    password: '',
    token: '',
    hook_secret: platform.hook_secret || '',
    bot_mappings: (platform.bot_mappings || []).map(mapping => ({ bot_key: mapping.bot_key, env: mapping.env || '' })),
    enabled: platform.enabled,
    poll_enabled: Boolean(platform.poll_enabled),
    poll_interval_minutes: platform.poll_interval_minutes || 5,
  }
})

watch(tickets.selectedID, bugID => {
  void loadInvestigationRuns(bugID)
})

onMounted(async () => {
  await Promise.all([loadPlatforms(), loadInstalledBots(), loadHookBase(), loadTickets()])
})

async function loadTickets() {
  try {
    await tickets.load()
  } catch (error) {
    toastError('读取 Bug 工单', error)
  }
}

async function loadPlatforms() {
  try {
    platforms.value = await listBugPlatforms()
    if (!platforms.value.length) {
      selectedPlatformID.value = ''
      return
    }
    if (!platforms.value.some(platform => platform.id === selectedPlatformID.value)) {
      selectedPlatformID.value = platforms.value[0].id
    }
  } catch (error) {
    toastError('读取 Bug 平台配置', error)
  }
}

async function loadInstalledBots() {
  try {
    installedBots.value = await discoverBots([])
  } catch (error) {
    installedBots.value = []
    toastError('读取已安装机器人', error)
  }
}

async function loadHookBase() {
  try {
    hookBaseURL.value = await bugHookBaseURL()
  } catch (error) {
    toastError('读取 Hook 地址', error)
  }
}

async function loadInvestigationRuns(bugID: string) {
  if (!bugID) {
    investigationRuns.value = []
    return
  }
  try {
    const runs = await listBugInvestigationRuns(bugID)
    if (tickets.selectedID.value === bugID) investigationRuns.value = runs
  } catch (error) {
    if (tickets.selectedID.value === bugID) investigationRuns.value = []
    toastError('读取历史运行记录', error)
  }
}

async function savePlatform() {
  if (!platformDraft.value.name.trim()) {
    toast.error('请填写平台名称')
    return undefined
  }
  platformSaving.value = true
  try {
    const saved = await saveBugPlatform({
      id: platformDraft.value.id.trim(),
      name: platformDraft.value.name.trim(),
      type: platformDraft.value.type.trim() || 'zentao',
      base_url: platformDraft.value.base_url.trim(),
      account: platformDraft.value.account.trim(),
      auth_mode: platformDraft.value.auth_mode.trim() || 'feishu_sso',
      session_header: platformDraft.value.session_header.trim(),
      password: platformDraft.value.password.trim(),
      token: platformDraft.value.token.trim(),
      hook_secret: platformDraft.value.hook_secret.trim(),
      bot_mappings: platformDraft.value.bot_mappings
        .map(mapping => ({ bot_key: mapping.bot_key.trim(), env: mapping.env.trim() }))
        .filter(mapping => mapping.bot_key),
      enabled: platformDraft.value.enabled,
      poll_enabled: platformDraft.value.poll_enabled,
      poll_interval_minutes: Math.max(1, Math.floor(Number(platformDraft.value.poll_interval_minutes) || 5)),
    })
    await loadPlatforms()
    selectedPlatformID.value = saved.id
    configOpen.value = true
    toast.success('平台配置已保存')
    return saved
  } catch (error) {
    toastError('保存平台配置', error)
    return undefined
  } finally {
    platformSaving.value = false
  }
}

async function loginSelectedPlatform() {
  if (!platformDraft.value.base_url.trim()) {
    toast.error('请先填写平台地址')
    return
  }
  const saved = await savePlatform()
  if (!saved) return
  platformLoggingIn.value = true
  try {
    const result = await loginBugPlatform({ platform_id: saved.id })
    await loadPlatforms()
    selectedPlatformID.value = saved.id
    toast.success(`平台登录态已保存,读取到 ${result.cookie_count} 个 Cookie`)
  } catch (error) {
    toastError('授权登录平台', error)
  } finally {
    platformLoggingIn.value = false
  }
}

async function clearSelectedPlatformLogin() {
  const platform = selectedPlatform.value
  if (!platform) return toast.error('请先选择平台')
  loginClearing.value = true
  try {
    await clearBugPlatformLogin({ platform_id: platform.id })
    await loadPlatforms()
    selectedPlatformID.value = platform.id
    toast.success('登录态已清除')
  } catch (error) {
    toastError('清除登录态', error)
  } finally {
    loginClearing.value = false
  }
}

async function deleteSelectedPlatform() {
  const id = platformDraft.value.id.trim()
  if (!id) return toast.error('当前平台还未保存')
  const confirmed = await confirmDialog({
    title: '删除平台',
    message: `确定删除「${platformDraft.value.name || id}」吗? 已接收的 Bug 工单不会被删除。`,
    confirmText: '删除',
    cancelText: '取消',
    danger: true,
    defaultAction: 'cancel',
  })
  if (!confirmed) return
  platformDeleting.value = true
  try {
    await deleteBugPlatform({ platform_id: id })
    selectedPlatformID.value = ''
    await loadPlatforms()
    if (!selectedPlatformID.value) newPlatform()
    toast.success('平台已删除')
  } catch (error) {
    toastError('删除平台', error)
  } finally {
    platformDeleting.value = false
  }
}

async function syncSelectedPlatform() {
  const platform = selectedPlatform.value
  if (!platform) return toast.error('请先选择平台')
  syncingBugs.value = true
  try {
    const result = await syncBugPlatform(platform.id)
    if (result.selected_bug_id) tickets.select(result.selected_bug_id)
    await loadTickets()
    toast.success(`已同步指派给我的 Bug,新增/更新 ${result.stored} 条`)
  } catch (error) {
    toastError('同步 Bug', error)
  } finally {
    syncingBugs.value = false
  }
}

async function fetchManualBug() {
  const platform = selectedPlatform.value
  const bugID = manualBugID.value.trim()
  if (!platform) return toast.error('请先选择平台')
  if (!bugID) return toast.error('请输入 Bug ID')
  fetchingBug.value = true
  try {
    await tickets.fetchByID({ platform_id: platform.id, bug_id: bugID })
    toast.success('Bug 已拉取')
  } catch (error) {
    toastError('拉取 Bug', error)
  } finally {
    fetchingBug.value = false
  }
}

async function previewAttachment(index: number) {
  const bug = tickets.selectedBug.value
  const platform = selectedPlatform.value
  if (!bug || !platform) return toast.error('请先选择 Bug 和平台')
  attachmentPreviewing.value = true
  try {
    attachmentPreview.value = await previewBugAttachment({
      platform_id: platform.id,
      bug_id: bug.id,
      attachment_index: index,
    })
  } catch (error) {
    toastError('预览附件', error)
  } finally {
    attachmentPreviewing.value = false
  }
}

function openIncident(bugID: string) {
  void router.push({ path: '/incidents', query: { bug_id: bugID } })
}

function newPlatform() {
  selectedPlatformID.value = ''
  botPickerOpen.value = false
  botPickerQuery.value = ''
  platformDraft.value = emptyPlatformDraft()
  configOpen.value = true
}

function emptyPlatformDraft() {
  return {
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
  }
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

function botRefByKey(key: string): BotRef {
  return allBotRefs.value.find(bot => bot.key === key) || { key, system_id: '', target: '', path: key }
}

function botDisplayName(bot: BotRef): string {
  return bot.name || bot.system_id || bot.path
}

function addPlatformBot(bot: BotRef) {
  if (platformDraft.value.bot_mappings.some(mapping => mapping.bot_key === bot.key)) return
  platformDraft.value.bot_mappings.push({ bot_key: bot.key, env: bot.envs?.[0] || '' })
  botPickerQuery.value = ''
  if (addableBotRefs.value.length === 0) botPickerOpen.value = false
}

function removePlatformBot(botKey: string) {
  platformDraft.value.bot_mappings = platformDraft.value.bot_mappings.filter(mapping => mapping.bot_key !== botKey)
}

function setPlatformBotEnv(botKey: string, env: string) {
  const mapping = platformDraft.value.bot_mappings.find(item => item.bot_key === botKey)
  if (mapping) mapping.env = env
}

async function copyHookURL() {
  if (!hookURL.value) return toast.error('请先保存平台配置')
  ;(await copyToClipboard(hookURL.value)) ? toast.success('Hook URL 已复制') : toast.error('复制失败')
}

async function copyLegacyResult() {
  if (!legacyFinalText.value.trim()) return
  ;(await copyToClipboard(legacyFinalText.value)) ? toast.success('结果已复制') : toast.error('复制失败')
}

function eventValue(event: Event): string {
  return (event.target as HTMLInputElement | HTMLSelectElement).value
}

function safeMarkdown(text: string): string {
  const parsed = marked.parse(text || '', { async: false }) as string
  const document = new DOMParser().parseFromString(parsed, 'text/html')
  const allowed = new Set(['BLOCKQUOTE', 'BR', 'CODE', 'DEL', 'EM', 'H1', 'H2', 'H3', 'H4', 'H5', 'H6', 'HR', 'LI', 'OL', 'P', 'PRE', 'STRONG', 'TABLE', 'TBODY', 'TD', 'TH', 'THEAD', 'TR', 'UL'])
  for (const element of Array.from(document.body.querySelectorAll('*'))) {
    if (!allowed.has(element.tagName)) {
      element.replaceWith(document.createTextNode(element.textContent || ''))
      continue
    }
    for (const attribute of Array.from(element.attributes)) element.removeAttribute(attribute.name)
  }
  return document.body.innerHTML
}
</script>

<template>
  <div class="bug-inbox-page">
    <header class="bug-header">
      <div>
        <h1>Bug 工单</h1>
        <p>配置 Bug 平台登录方式，后台可按间隔同步，也可以主动拉取指定 Bug。</p>
      </div>
      <button class="btn accent" type="button" data-action="toggle-platform-config" @click="configOpen = !configOpen">
        {{ configOpen ? '收起配置' : '平台配置' }}
      </button>
    </header>

    <section class="platform-config" :class="{ open: configOpen }" aria-label="Bug 平台配置">
      <div class="platform-list">
        <div class="platform-tabs">
          <button
            v-for="platform in platforms"
            :key="platform.id"
            type="button"
            class="platform-chip"
            :class="{ active: selectedPlatformID === platform.id }"
            @click="selectedPlatformID = platform.id"
          >
            {{ platform.name }}<span>{{ platform.enabled ? '启用' : '停用' }}</span>
          </button>
        </div>
        <button class="btn icon add-platform" type="button" aria-label="新增平台" @click="newPlatform">+</button>
      </div>

      <div class="config-grid">
        <div class="config-row basic-row">
          <input v-model="platformDraft.name" class="form-control" placeholder="平台名称,如 测试环境 Bug 平台">
          <select v-model="platformDraft.type" class="form-control">
            <option value="zentao">禅道</option>
            <option value="generic">通用 Webhook</option>
          </select>
          <input v-model="platformDraft.base_url" class="form-control" placeholder="平台地址 https://bug-platform.example.com">
        </div>

        <div class="config-row auth-row">
          <select v-model="platformDraft.auth_mode" class="form-control">
            <option value="feishu_sso">飞书授权登录</option>
            <option value="api_token">API Token</option>
            <option value="password">账号密码</option>
          </select>
          <input v-if="platformDraft.auth_mode === 'password'" v-model="platformDraft.password" class="form-control" type="password" placeholder="密码,留空沿用已保存值">
          <input v-if="platformDraft.auth_mode === 'api_token'" v-model="platformDraft.token" class="form-control" type="password" placeholder="Token,可选,留空沿用已保存值">
          <div v-if="platformDraft.auth_mode === 'feishu_sso'" class="login-field">
            <span class="login-state">登录状态 <strong :class="{ ok: selectedPlatformHasSession }">{{ selectedPlatformHasSession ? '已保存' : '未登录' }}</strong></span>
            <button class="btn" type="button" data-action="login-platform" :disabled="platformSaving || platformLoggingIn" @click="loginSelectedPlatform">
              {{ platformLoggingIn ? '等待授权' : '登录平台' }}
            </button>
            <button class="btn" type="button" :disabled="loginClearing || platformLoggingIn || !selectedPlatformHasSession" @click="clearSelectedPlatformLogin">清除登录态</button>
          </div>
          <label class="enabled-toggle"><input v-model="platformDraft.enabled" type="checkbox"> 启用平台</label>
        </div>

        <div class="bot-config-block">
          <div class="bot-config-title">
            <div><strong>可用于该平台的排障机器人</strong><span>平台映射只用于后续故障闭环选人。</span></div>
            <button class="btn small" type="button" data-action="toggle-bot-picker" :disabled="allBotRefs.length === 0" @click="botPickerOpen = !botPickerOpen">
              {{ botPickerOpen ? '收起' : '+ 添加' }}
            </button>
          </div>
          <p v-if="configuredPlatformBots.length === 0" class="empty compact">{{ allBotRefs.length ? '还未添加排障机器人' : '暂无已安装机器人' }}</p>
          <div v-else class="bot-config-list">
            <div v-for="item in configuredPlatformBots" :key="item.mapping.bot_key" class="bot-config-row">
              <span class="bot-config-main"><strong>{{ botDisplayName(item.bot) }}</strong><small>{{ item.bot.target || '未知类型' }} · {{ item.bot.path }}</small></span>
              <select
                v-if="item.bot.envs?.length"
                class="form-control"
                :value="item.mapping.env"
                @change="setPlatformBotEnv(item.mapping.bot_key, eventValue($event))"
              >
                <option v-for="env in item.bot.envs" :key="env" :value="env">{{ env }}</option>
              </select>
              <input v-else class="form-control" :value="item.mapping.env" placeholder="机器人环境" @input="setPlatformBotEnv(item.mapping.bot_key, eventValue($event))">
              <button class="btn icon" type="button" aria-label="移除机器人" @click="removePlatformBot(item.mapping.bot_key)">×</button>
            </div>
          </div>
          <div v-if="botPickerOpen" class="bot-picker">
            <input v-model="botPickerQuery" class="form-control" placeholder="搜索机器人名称、类型、路径">
            <p v-if="addableBotRefs.length === 0" class="empty compact">没有可添加的机器人</p>
            <button
              v-for="bot in addableBotRefs"
              :key="bot.key"
              type="button"
              class="bot-picker-row"
              :data-bot-key="bot.key"
              @click="addPlatformBot(bot)"
            >
              <span class="bot-config-main"><strong>{{ botDisplayName(bot) }}</strong><small>{{ bot.target }} · {{ bot.path }}</small></span>
              <span>添加</span>
            </button>
          </div>
        </div>

        <div class="config-row ops-row">
          <div class="sync-settings">
            <label class="enabled-toggle"><input v-model="platformDraft.poll_enabled" type="checkbox"> 后台定时同步</label>
            <label class="interval-control">每 <input v-model.number="platformDraft.poll_interval_minutes" aria-label="后台同步间隔分钟" type="number" min="1" :disabled="!platformDraft.poll_enabled"> 分钟</label>
          </div>
          <div class="config-actions">
            <button class="btn danger" type="button" :disabled="platformDeleting || !platformDraft.id" @click="deleteSelectedPlatform">删除平台</button>
            <button class="btn primary" type="button" data-action="save-platform" :disabled="platformSaving || platformLoggingIn" @click="savePlatform">保存配置</button>
          </div>
        </div>
      </div>

      <div class="trigger-row">
        <button class="btn primary" type="button" data-action="sync-platform" :disabled="!selectedPlatform || syncingBugs" @click="syncSelectedPlatform">同步我的 Bug</button>
        <input v-model="manualBugID" class="form-control" placeholder="Bug ID 或飞书消息" @keyup.enter="fetchManualBug">
        <button class="btn" type="button" data-action="fetch-bug" :disabled="!selectedPlatform || !manualBugID.trim() || fetchingBug" @click="fetchManualBug">拉取指定 Bug</button>
      </div>
      <div class="hook-row"><strong>Hook URL 可选</strong><code>{{ hookURL || '保存平台后生成' }}</code><button class="btn" type="button" :disabled="!hookURL" @click="copyHookURL">复制 Hook URL</button></div>
    </section>

    <section class="inbox-workspace">
      <aside class="ticket-list-panel">
        <button class="btn small refresh-button" type="button" :disabled="tickets.loading.value" @click="loadTickets">刷新</button>
        <BugTicketList
          :bugs="tickets.filteredBugs.value"
          :selected-id="tickets.selectedID.value"
          :loading="tickets.loading.value"
          :query="tickets.query.value"
          @select="tickets.select"
          @update:query="tickets.query.value = $event"
        />
      </aside>
      <main class="ticket-detail-panel">
        <BugTicketDetail
          :bug="tickets.selectedBug.value"
          mode="full"
          @preview-attachment="previewAttachment"
          @open-incident="openIncident"
        />
      </main>
    </section>

    <details v-if="selectedRun" class="legacy-history">
      <summary>
        <span class="legacy-history-title"><strong>历史运行记录（只读）</strong><small>旧版验证 / 排障结果，新操作请进入故障闭环。</small></span>
        <span class="legacy-history-status">{{ selectedRun.status }}</span>
      </summary>
      <div class="legacy-history-body">
        <div v-if="legacyFinalText" class="legacy-history-actions">
          <button class="btn" type="button" data-action="copy-legacy-result" @click="copyLegacyResult">复制结果</button>
        </div>
        <div v-if="legacyEventLines.length" class="process-log">
          <div v-for="(line, index) in legacyEventLines" :key="index" class="process-line">{{ line }}</div>
        </div>
        <article v-if="legacyFinalText" class="markdown-result" v-html="renderedLegacyMarkdown"></article>
        <p v-if="!legacyEventLines.length && !legacyFinalText" class="empty">该历史运行没有可展示的输出。</p>
      </div>
    </details>

    <div v-if="attachmentPreview" class="attachment-preview-backdrop" @click.self="attachmentPreview = null">
      <section class="attachment-preview-modal" role="dialog" aria-modal="true" aria-label="附件预览">
        <header><strong>{{ attachmentPreview.name }}</strong><button class="btn icon" type="button" aria-label="关闭附件预览" @click="attachmentPreview = null">×</button></header>
        <img v-if="attachmentPreview.content_type.startsWith('image/')" class="attachment-preview-image" :src="attachmentPreview.data_url" :alt="attachmentPreview.name">
        <div v-else class="attachment-preview-fallback"><span>当前附件类型不支持内嵌预览</span><a class="btn" :href="attachmentPreview.data_url" :download="attachmentPreview.name">下载附件</a></div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.bug-inbox-page { min-width: 0; display: grid; gap: var(--sp-3); color: var(--c-text); }
.bug-header { display: flex; align-items: flex-start; justify-content: space-between; gap: var(--sp-3); }
.bug-header h1 { margin: 0; color: var(--c-ink); font-size: 24px; }
.bug-header p { margin: 4px 0 0; color: var(--c-muted); font-size: var(--fs-sm); }
.btn { min-height: 44px; padding: 0 12px; border: 1px solid var(--c-line-2); border-radius: var(--r-md); background: var(--c-surf); color: var(--c-text); font: inherit; cursor: pointer; }
.btn:hover:not(:disabled) { border-color: var(--c-accent); background: var(--c-surf-2); }
.btn:focus-visible, input:focus-visible, select:focus-visible, .legacy-history > summary:focus-visible { outline: 2px solid var(--c-accent-hover); outline-offset: 2px; }
.btn:disabled { opacity: .55; cursor: not-allowed; }
.btn.primary, .btn.accent { border-color: var(--c-accent-hover); background: var(--c-accent-hover); color: white; }
.btn.danger { border-color: #fecaca; color: #b91c1c; }
.btn.small { min-height: 44px; font-size: var(--fs-sm); }
.btn.icon { width: 44px; padding: 0; font-size: 20px; }
.form-control, input, select { max-width: 100%; min-width: 0; min-height: 44px; box-sizing: border-box; border: 1px solid var(--c-line-2); border-radius: var(--r-md); background: var(--c-surf); color: var(--c-text); font: inherit; }
.form-control { width: 100%; padding: 0 10px; }
.platform-config { min-width: 0; display: none; gap: var(--sp-3); padding: var(--sp-3); border: 1px solid var(--c-line); border-radius: var(--r-lg); background: var(--c-surf); }
.platform-config.open { display: grid; }
.platform-list, .platform-tabs, .config-actions, .hook-row { min-width: 0; display: flex; align-items: center; gap: var(--sp-2); }
.platform-list { justify-content: space-between; }
.platform-tabs { flex-wrap: wrap; }
.platform-chip { min-height: 44px; padding: 6px 10px; border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf); color: var(--c-text); cursor: pointer; }
.platform-chip.active { border-color: var(--c-accent); background: #eff6ff; }
.platform-chip span { display: block; color: var(--c-muted); font-size: var(--fs-xs); }
.config-grid { min-width: 0; display: grid; gap: var(--sp-2); }
.config-row { min-width: 0; display: grid; gap: var(--sp-2); }
.basic-row { grid-template-columns: minmax(160px, 1fr) 150px minmax(220px, 1.4fr); }
.auth-row { grid-template-columns: minmax(150px, .7fr) minmax(300px, 1.4fr) auto; align-items: center; }
.login-field, .sync-settings { min-width: 0; display: flex; align-items: center; gap: var(--sp-2); flex-wrap: wrap; }
.login-state { color: var(--c-muted); font-size: var(--fs-xs); }
.login-state strong { margin-left: 4px; color: #b45309; }
.login-state strong.ok { color: #047857; }
.enabled-toggle { min-height: 44px; display: inline-flex; align-items: center; gap: 6px; white-space: nowrap; }
.enabled-toggle input { min-height: auto; }
.bot-config-block { min-width: 0; display: grid; gap: var(--sp-2); padding: var(--sp-2); border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf-2); }
.bot-config-title { min-width: 0; display: flex; justify-content: space-between; align-items: center; gap: var(--sp-2); }
.bot-config-title div, .bot-config-main { min-width: 0; display: grid; gap: 2px; }
.bot-config-title span, .bot-config-main small { color: var(--c-muted); font-size: var(--fs-xs); overflow-wrap: anywhere; }
.bot-config-list, .bot-picker { min-width: 0; display: grid; gap: var(--sp-2); }
.bot-config-row { min-width: 0; display: grid; grid-template-columns: minmax(0, 1fr) minmax(120px, 180px) 44px; align-items: center; gap: var(--sp-2); }
.bot-picker-row { width: 100%; min-width: 0; min-height: 44px; padding: 8px 10px; display: flex; justify-content: space-between; gap: var(--sp-2); border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf); color: var(--c-text); text-align: left; cursor: pointer; }
.ops-row { grid-template-columns: minmax(0, 1fr) auto; align-items: center; }
.interval-control { min-height: 44px; display: inline-flex; align-items: center; gap: 6px; color: var(--c-muted); }
.interval-control input { width: 80px; padding: 0 8px; }
.trigger-row { min-width: 0; display: grid; grid-template-columns: auto minmax(180px, 1fr) auto; gap: var(--sp-2); }
.hook-row { flex-wrap: wrap; }
.hook-row code { min-width: 0; flex: 1; overflow-wrap: anywhere; color: var(--c-muted); }
.inbox-workspace { min-width: 0; display: grid; grid-template-columns: minmax(250px, 330px) minmax(0, 1fr); gap: var(--sp-3); }
.ticket-list-panel, .ticket-detail-panel, .legacy-history { min-width: 0; border: 1px solid var(--c-line); border-radius: var(--r-lg); background: var(--c-surf); }
.ticket-list-panel { position: relative; padding: var(--sp-3); overflow: auto; }
.ticket-list-panel :deep(.list-heading) { padding-right: 66px; }
.refresh-button { position: absolute; z-index: 1; top: var(--sp-2); right: var(--sp-2); }
.ticket-detail-panel { padding: var(--sp-3); overflow: auto; }
.legacy-history { overflow: hidden; }
.legacy-history > summary { min-height: 44px; padding: 10px 44px 10px 12px; position: relative; display: flex; justify-content: space-between; align-items: center; gap: var(--sp-2); cursor: pointer; list-style: none; }
.legacy-history > summary::-webkit-details-marker { display: none; }
.legacy-history > summary::after { content: '展开'; position: absolute; right: 12px; color: var(--c-accent-hover); font-size: var(--fs-xs); font-weight: 700; }
.legacy-history[open] > summary::after { content: '收起'; }
.legacy-history-title { min-width: 0; padding-right: 40px; display: grid; gap: 2px; }
.legacy-history-title small { color: var(--c-muted); font-size: var(--fs-xs); }
.legacy-history-status { flex: 0 0 auto; margin-right: 40px; padding: 2px 7px; border: 1px solid var(--c-line-2); border-radius: 999px; color: var(--c-muted); font-size: var(--fs-xs); }
.legacy-history-body { min-width: 0; padding: var(--sp-3); border-top: 1px solid var(--c-line); }
.legacy-history-actions { margin-bottom: var(--sp-2); display: flex; justify-content: flex-end; }
.process-log { margin-bottom: var(--sp-2); padding: var(--sp-2); overflow-wrap: anywhere; border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf-2); color: var(--c-muted); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: var(--fs-xs); }
.process-line + .process-line { margin-top: 4px; }
.markdown-result { max-width: 100%; overflow-wrap: anywhere; color: var(--c-text); }
.markdown-result :deep(pre), .markdown-result :deep(table) { max-width: 100%; overflow: auto; }
.empty { min-height: 44px; margin: 0; display: grid; place-items: center; color: var(--c-muted); font-size: var(--fs-sm); text-align: center; }
.empty.compact { min-height: 44px; }
.attachment-preview-backdrop { position: fixed; inset: 0; z-index: 60; padding: 24px; display: grid; place-items: center; background: rgba(15, 23, 42, .58); }
.attachment-preview-modal { width: min(1080px, 92vw); max-height: 88vh; min-width: 0; overflow: hidden; border-radius: var(--r-lg); background: var(--c-surf); box-shadow: 0 20px 60px rgba(15, 23, 42, .28); }
.attachment-preview-modal header { min-height: 44px; padding: 6px 8px 6px 12px; display: flex; align-items: center; justify-content: space-between; gap: var(--sp-2); }
.attachment-preview-modal header strong { min-width: 0; overflow-wrap: anywhere; }
.attachment-preview-image { display: block; max-width: 100%; max-height: calc(88vh - 56px); margin: auto; object-fit: contain; background: #0f172a; }
.attachment-preview-fallback { min-height: 220px; padding: var(--sp-4); display: grid; place-items: center; color: var(--c-muted); }
@media (max-width: 900px) {
  .basic-row, .auth-row, .ops-row { grid-template-columns: minmax(0, 1fr); }
  .inbox-workspace { grid-template-columns: minmax(0, 1fr); }
  .ticket-list-panel { max-height: 360px; }
}
@media (max-width: 640px) {
  .bug-header, .bot-config-title, .hook-row { align-items: stretch; flex-direction: column; }
  .trigger-row, .bot-config-row { grid-template-columns: minmax(0, 1fr); }
  .config-actions { flex-direction: column-reverse; align-items: stretch; }
  .legacy-history > summary { align-items: flex-start; flex-direction: column; }
  .legacy-history-status { margin-right: 40px; }
}
</style>
