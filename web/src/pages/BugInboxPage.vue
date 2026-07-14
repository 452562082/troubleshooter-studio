<script setup lang="ts">
import { computed, onMounted, ref, useId, watch } from 'vue'
import { useRouter } from 'vue-router'
import BugTicketDetail from '../components/BugTicketDetail.vue'
import BugTicketList from '../components/BugTicketList.vue'
import {
  type BotRef,
  type BugAttachmentPreviewResult,
  type BugPlatform,
  type DiscoveredBot,
  bugHookBaseURL,
  clearBugPlatformLogin,
  deleteBugPlatform,
  discoverBots,
  fetchBugByID,
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
const platformConfigInstanceID = useId()
const botSearchID = `${platformConfigInstanceID}-bot-search`
const manualBugFieldID = `${platformConfigInstanceID}-manual-bug`
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
const platformDraft = ref(emptyPlatformDraft())
const selectedPlatform = computed(() => platforms.value.find(platform => platform.id === selectedPlatformID.value))
const selectedPlatformHasSession = computed(() => Boolean(selectedPlatform.value?.session_header))
const allBotRefs = computed(() => installedBots.value
  .filter(bot => !bot.ghost && supportsIncidentWorkflowTarget(bot.meta.target))
  .map(discoveredBotToRef))
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
    bot_mappings: (platform.bot_mappings || [])
      .filter(mapping => supportsIncidentWorkflowTarget(botTargetFromKey(mapping.bot_key)))
      .map(mapping => ({ bot_key: mapping.bot_key, env: mapping.env || '' })),
    enabled: platform.enabled,
    poll_enabled: Boolean(platform.poll_enabled),
    poll_interval_minutes: platform.poll_interval_minutes || 5,
  }
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

function syncErrorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String((error as any)?.message ?? error)
}

async function syncEnabledPlatforms() {
  const enabled = platforms.value.filter(platform => platform.enabled)
  if (!enabled.length) {
    toast.info('请先启用 Bug 平台')
    return
  }
  syncingBugs.value = true
  let stored = 0
  const failures: string[] = []
  try {
    for (const platform of enabled) {
      try {
        const result = await syncBugPlatform(platform.id)
        stored += result.stored
      } catch (error) {
        failures.push(`${platform.name || platform.id}：${syncErrorMessage(error)}`)
      }
    }
    await loadTickets()
    const succeeded = enabled.length - failures.length
    if (!failures.length) {
      toast.success(`已同步 ${succeeded} 个平台，新增/更新 ${stored} 条`)
    } else if (succeeded > 0) {
      toast.error(`已同步 ${succeeded} 个平台，${failures.length} 个平台失败；新增/更新 ${stored} 条。${failures.join('；')}`)
    } else {
      toast.error(`所有已启用平台同步失败：${failures.join('；')}`)
    }
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

function supportsIncidentWorkflowTarget(target: string): boolean {
  return ['codex', 'claude-code', 'openclaw'].includes(target.trim().toLowerCase())
}

function botTargetFromKey(key: string): string {
  const separator = key.lastIndexOf('|')
  return separator >= 0 ? key.slice(separator + 1) : ''
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

function eventValue(event: Event): string {
  return (event.target as HTMLInputElement | HTMLSelectElement).value
}
</script>

<template>
  <div class="bug-inbox-page" data-responsive-viewports="375,768,1024,1440" data-overflow-safe="true">
    <header class="bug-header">
      <div>
        <h1>Bug 工单</h1>
        <p>配置 Bug 平台登录方式，后台可按间隔同步，也可以主动拉取指定 Bug。</p>
      </div>
      <button
        class="config-disclosure"
        :class="{ expanded: configOpen }"
        type="button"
        data-action="toggle-platform-config"
        :aria-expanded="configOpen"
        aria-controls="bug-platform-config"
        @click="configOpen = !configOpen"
      >
        <svg aria-hidden="true" viewBox="0 0 24 24" fill="none">
          <path d="M4 7h10M18 7h2M4 17h2M10 17h10M14 4v6M6 14v6" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
        </svg>
        <span>{{ configOpen ? '收起配置' : '平台配置' }}</span>
        <svg aria-hidden="true" viewBox="0 0 24 24" fill="none">
          <path :d="configOpen ? 'm6 15 6-6 6 6' : 'm6 9 6 6 6-6'" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </button>
    </header>

    <section
      id="bug-platform-config"
      class="platform-config"
      :class="{ open: configOpen }"
      aria-label="Bug 平台配置"
      data-density="compact"
      data-responsive-viewports="375,768,1024,1440"
      data-overflow-safe="true"
    >
      <section class="platform-config-section platform-details-section" aria-labelledby="platform-details-title">
        <header class="section-heading">
          <div><h2 id="platform-details-title">平台信息</h2><p>管理平台连接、授权方式和启用状态。</p></div>
        </header>
        <div class="platform-list">
          <div class="platform-tabs">
            <button
              v-for="platform in platforms"
              :key="platform.id"
              type="button"
              class="platform-chip"
              :class="{ active: selectedPlatformID === platform.id }"
              :aria-pressed="selectedPlatformID === platform.id"
              @click="selectedPlatformID = platform.id"
            >
              {{ platform.name }}<span>{{ platform.enabled ? '启用' : '停用' }}</span>
            </button>
          </div>
          <button class="compact-button secondary-button add-platform" type="button" data-action="new-platform" @click="newPlatform">
            <svg aria-hidden="true" viewBox="0 0 24 24" fill="none"><path d="M12 5v14M5 12h14" stroke="currentColor" stroke-width="2" stroke-linecap="round" /></svg>
            新建平台
          </button>
        </div>
        <div class="config-grid">
          <div class="config-row basic-row">
            <label class="field-label"><span>平台名称</span><input v-model="platformDraft.name" class="form-control" placeholder="如：测试环境"></label>
            <label class="field-label"><span>平台类型</span><select v-model="platformDraft.type" class="form-control"><option value="zentao">禅道</option><option value="generic">通用 Webhook</option></select></label>
            <label class="field-label"><span>平台地址</span><input v-model="platformDraft.base_url" class="form-control" placeholder="https://bug-platform.example.com"></label>
          </div>
          <div class="config-row auth-row">
            <label class="field-label"><span>登录方式</span><select v-model="platformDraft.auth_mode" class="form-control"><option value="feishu_sso">飞书授权登录</option><option value="api_token">API Token</option><option value="password">账号密码</option></select></label>
            <label v-if="platformDraft.auth_mode === 'password'" class="field-label"><span>密码</span><input v-model="platformDraft.password" class="form-control" type="password" placeholder="留空沿用已保存值"></label>
            <label v-if="platformDraft.auth_mode === 'api_token'" class="field-label"><span>API Token</span><input v-model="platformDraft.token" class="form-control" type="password" placeholder="留空沿用已保存值"></label>
            <div v-if="platformDraft.auth_mode === 'feishu_sso'" class="login-field">
              <span class="login-status-badge" :class="{ ok: selectedPlatformHasSession }">{{ selectedPlatformHasSession ? '已登录' : '未登录' }}</span>
              <button class="compact-button secondary-button" type="button" data-action="login-platform" :disabled="platformSaving || platformLoggingIn" @click="loginSelectedPlatform">{{ platformLoggingIn ? '等待授权' : '登录平台' }}</button>
              <button class="compact-button ghost-button" type="button" data-action="clear-platform-login" :disabled="loginClearing || platformLoggingIn || !selectedPlatformHasSession" @click="clearSelectedPlatformLogin">清除登录态</button>
            </div>
            <label class="toggle-control"><input v-model="platformDraft.enabled" type="checkbox"><span class="toggle-track" aria-hidden="true"><span></span></span><span>启用平台</span></label>
          </div>
        </div>
      </section>

      <footer class="config-footer">
        <button class="danger-link" type="button" data-action="delete-platform" :disabled="platformDeleting || !platformDraft.id" @click="deleteSelectedPlatform">
          <svg aria-hidden="true" viewBox="0 0 24 24" fill="none"><path d="M4 7h16M9 7V4h6v3m-8 0 1 13h8l1-13" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
          删除平台
        </button>
        <span class="config-save-hint">修改后需保存才会生效</span>
        <button class="compact-button primary-button" type="button" data-action="save-platform" :disabled="platformSaving || platformLoggingIn" @click="savePlatform">{{ platformSaving ? '保存中…' : '保存配置' }}</button>
      </footer>

      <section class="platform-config-section bot-mapping-section" aria-labelledby="bot-mapping-title">
        <header class="section-heading bot-config-title">
          <div><h2 id="bot-mapping-title">排障机器人</h2><p>平台映射只用于后续故障闭环选人。</p></div>
          <button class="compact-button secondary-button" type="button" data-action="toggle-bot-picker" :disabled="allBotRefs.length === 0" @click="botPickerOpen = !botPickerOpen">
            <svg aria-hidden="true" viewBox="0 0 24 24" fill="none"><path d="M12 5v14M5 12h14" stroke="currentColor" stroke-width="2" stroke-linecap="round" /></svg>
            {{ botPickerOpen ? '收起' : '添加机器人' }}
          </button>
        </header>
        <p v-if="configuredPlatformBots.length === 0" class="empty compact">{{ allBotRefs.length ? '还未添加排障机器人' : '暂无已安装机器人' }}</p>
        <div v-else class="bot-config-list">
          <div v-for="(item, botIndex) in configuredPlatformBots" :key="item.mapping.bot_key" class="bot-config-row">
            <span class="bot-config-main"><strong :id="`${platformConfigInstanceID}-bot-${botIndex}-name`">{{ botDisplayName(item.bot) }}</strong><small>{{ item.bot.target || '未知类型' }} · {{ item.bot.path }}</small></span>
            <label class="field-label bot-env-field" :for="`${platformConfigInstanceID}-bot-${botIndex}-env`">
              <span :id="`${platformConfigInstanceID}-bot-${botIndex}-env-label`">机器人环境</span>
              <select
                v-if="item.bot.envs?.length"
                :id="`${platformConfigInstanceID}-bot-${botIndex}-env`"
                class="form-control"
                :aria-labelledby="`${platformConfigInstanceID}-bot-${botIndex}-name ${platformConfigInstanceID}-bot-${botIndex}-env-label`"
                :value="item.mapping.env"
                @change="setPlatformBotEnv(item.mapping.bot_key, eventValue($event))"
              ><option v-for="env in item.bot.envs" :key="env" :value="env">{{ env }}</option></select>
              <input
                v-else
                :id="`${platformConfigInstanceID}-bot-${botIndex}-env`"
                class="form-control"
                :aria-labelledby="`${platformConfigInstanceID}-bot-${botIndex}-name ${platformConfigInstanceID}-bot-${botIndex}-env-label`"
                :value="item.mapping.env"
                placeholder="机器人环境"
                @input="setPlatformBotEnv(item.mapping.bot_key, eventValue($event))"
              >
            </label>
            <button class="icon-button danger-icon-button" type="button" aria-label="移除机器人" @click="removePlatformBot(item.mapping.bot_key)">
              <svg aria-hidden="true" viewBox="0 0 24 24" fill="none"><path d="M4 7h16M9 7V4h6v3m-8 0 1 13h8l1-13M10 11v5m4-5v5" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
            </button>
          </div>
        </div>
        <div v-if="botPickerOpen" class="bot-picker">
          <label class="field-label bot-search-field" :for="botSearchID"><span>搜索机器人</span><input :id="botSearchID" v-model="botPickerQuery" class="form-control" placeholder="名称、类型或路径"></label>
          <p v-if="addableBotRefs.length === 0" class="empty compact">没有可添加的机器人</p>
          <button v-for="bot in addableBotRefs" :key="bot.key" type="button" class="bot-picker-row" :data-bot-key="bot.key" @click="addPlatformBot(bot)"><span class="bot-config-main"><strong>{{ botDisplayName(bot) }}</strong><small>{{ bot.target }} · {{ bot.path }}</small></span><span>添加</span></button>
        </div>
      </section>

      <section class="platform-config-section sync-access-section" aria-labelledby="sync-access-title">
        <header class="section-heading"><div><h2 id="sync-access-title">同步与接入</h2><p>同步指派给我的 Bug，或按 ID 主动拉取。</p></div></header>
        <div class="sync-settings">
          <label class="toggle-control"><input v-model="platformDraft.poll_enabled" type="checkbox"><span class="toggle-track" aria-hidden="true"><span></span></span><span>后台定时同步</span></label>
          <label class="interval-control">每 <input v-model.number="platformDraft.poll_interval_minutes" aria-label="后台同步间隔分钟" type="number" min="1" :disabled="!platformDraft.poll_enabled"> 分钟</label>
        </div>
        <div class="manual-bug-row">
          <label class="field-label manual-bug-field" :for="manualBugFieldID"><span>指定 Bug</span><input :id="manualBugFieldID" v-model="manualBugID" class="form-control" placeholder="Bug ID 或飞书消息" @keyup.enter="fetchManualBug"></label>
          <button class="compact-button secondary-button" type="button" data-action="fetch-bug" :disabled="!selectedPlatform || !manualBugID.trim() || fetchingBug" @click="fetchManualBug">拉取指定 Bug</button>
        </div>
        <div class="hook-row"><strong>Hook URL</strong><code>{{ hookURL || '保存平台后生成' }}</code><button class="compact-button secondary-button" type="button" data-action="copy-hook-url" :disabled="!hookURL" @click="copyHookURL">复制</button></div>
      </section>

    </section>

    <section class="inbox-workspace" data-overflow-safe="true">
      <aside class="ticket-list-panel" data-overflow-safe="true">
        <button class="compact-button secondary-button refresh-button" type="button" data-action="sync-enabled-platforms" :aria-label="syncingBugs ? '正在同步我的 Bug' : '同步我的 Bug'" :disabled="syncingBugs || tickets.loading.value" @click="syncEnabledPlatforms">
          <svg aria-hidden="true" :class="{ spinning: syncingBugs }" viewBox="0 0 24 24" fill="none"><path d="M20 11a8 8 0 1 0-2.34 5.66M20 4v7h-7" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" /></svg>
          {{ syncingBugs ? '同步中…' : '同步我的 Bug' }}
        </button>
        <BugTicketList
          :bugs="tickets.filteredBugs.value"
          :selected-id="tickets.selectedID.value"
          :loading="tickets.loading.value"
          :query="tickets.query.value"
          @select="tickets.select"
          @update:query="tickets.query.value = $event"
        />
      </aside>
      <main class="ticket-detail-panel" data-overflow-safe="true">
        <BugTicketDetail
          :bug="tickets.selectedBug.value"
          mode="full"
          @preview-attachment="previewAttachment"
          @open-incident="openIncident"
        />
      </main>
    </section>

    <div v-if="attachmentPreview" class="attachment-preview-backdrop" @click.self="attachmentPreview = null">
      <section class="attachment-preview-modal" role="dialog" aria-modal="true" aria-label="附件预览">
        <header>
          <strong>{{ attachmentPreview.name }}</strong>
          <button class="attachment-preview-close" type="button" aria-label="关闭附件预览" @click="attachmentPreview = null">
            <svg aria-hidden="true" viewBox="0 0 24 24" fill="none">
              <path d="M6 6l12 12M18 6 6 18" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
            </svg>
          </button>
        </header>
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
.btn:focus-visible, input:focus-visible, select:focus-visible { outline: 2px solid var(--c-accent-hover); outline-offset: 2px; }
.btn:disabled { opacity: .55; cursor: not-allowed; }
.btn.primary, .btn.accent { border-color: var(--c-accent-hover); background: var(--c-accent-hover); color: white; }
.btn.danger { border-color: #fecaca; color: #b91c1c; }
.btn.small { min-height: 44px; font-size: var(--fs-sm); }
.btn.icon { width: 44px; padding: 0; font-size: 20px; }
.form-control, input, select { max-width: 100%; min-width: 0; min-height: 44px; box-sizing: border-box; border: 1px solid var(--c-line-2); border-radius: var(--r-md); background: var(--c-surf); color: var(--c-text); font: inherit; }
.form-control { width: 100%; padding: 0 10px; }
.config-disclosure { min-height: 38px; padding: 0 12px; display: inline-flex; align-items: center; gap: 7px; border: 1px solid var(--c-accent-hover); border-radius: var(--r-md); background: var(--c-accent-hover); color: #fff; font: inherit; font-weight: 600; cursor: pointer; transition: background-color 180ms ease, border-color 180ms ease, color 180ms ease; }
.config-disclosure svg { width: 17px; height: 17px; flex: 0 0 auto; }
.config-disclosure:hover { background: #1d4ed8; border-color: #1d4ed8; }
.config-disclosure.expanded { background: #eff6ff; border-color: #93c5fd; color: #1d4ed8; }
.config-disclosure.expanded:hover { background: #dbeafe; border-color: #60a5fa; color: #1e40af; }
.config-disclosure:focus-visible { outline: 2px solid var(--c-accent-hover); outline-offset: 2px; }
.platform-config { --config-control-height: 40px; min-width: 0; display: none; gap: var(--sp-2); padding: var(--sp-2); border: 1px solid var(--c-line); border-radius: var(--r-lg); background: var(--c-surf-2); }
.platform-config.open { display: grid; }
.platform-config-section { min-width: 0; display: grid; gap: var(--sp-2); padding: var(--sp-3); border: 1px solid var(--c-line); border-radius: var(--r-lg); background: var(--c-surf); }
.section-heading, .platform-list, .platform-tabs, .config-footer, .hook-row { min-width: 0; display: flex; align-items: center; gap: var(--sp-2); }
.section-heading, .platform-list, .config-footer { justify-content: space-between; }
.config-footer { position: sticky; top: var(--sp-2); z-index: 5; padding: var(--sp-2) var(--sp-3); border: 1px solid #bfdbfe; border-radius: var(--r-lg); background: rgba(255, 255, 255, .96); box-shadow: 0 6px 18px rgba(15, 23, 42, .1); backdrop-filter: blur(8px); }
.config-save-hint { min-width: 0; flex: 1; color: var(--c-muted); font-size: var(--fs-sm); }
.section-heading h2 { margin: 0; color: var(--c-ink); font-size: var(--fs-md); }
.section-heading p { margin: 2px 0 0; color: var(--c-muted); font-size: var(--fs-sm); }
.platform-tabs { flex-wrap: wrap; }
.platform-chip { min-height: 36px; padding: 4px 10px; border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf); color: var(--c-text); cursor: pointer; }
.platform-chip.active { border-color: #93c5fd; background: #eff6ff; color: #1d4ed8; box-shadow: inset 0 0 0 1px #bfdbfe; }
.platform-chip span { display: block; color: var(--c-muted); font-size: var(--fs-xs); }
.config-grid { min-width: 0; display: grid; gap: var(--sp-2); }
.config-row { min-width: 0; display: grid; gap: var(--sp-2); }
.basic-row { grid-template-columns: minmax(220px, 1fr) minmax(200px, .6fr) minmax(320px, 1.35fr); }
.auth-row { grid-template-columns: minmax(160px, .8fr) minmax(280px, 1.4fr) auto; align-items: end; }
.field-label { min-width: 0; display: grid; gap: 4px; color: var(--c-muted); font-size: var(--fs-sm); }
.platform-config .form-control { min-height: var(--config-control-height); }
.platform-config select.form-control {
  appearance: none;
  -webkit-appearance: none;
  padding: 0 40px 0 12px;
  background-image: url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='24' height='24' viewBox='0 0 24 24' fill='none'%3E%3Cpath d='m7 9 5 5 5-5' stroke='%2364758b' stroke-width='2' stroke-linecap='round' stroke-linejoin='round'/%3E%3C/svg%3E");
  background-repeat: no-repeat;
  background-position: right 12px center;
  background-size: 16px 16px;
  cursor: pointer;
  transition: border-color 180ms ease, box-shadow 180ms ease, background-color 180ms ease;
}
.platform-config select.form-control:hover:not(:disabled) { border-color: #93c5fd; }
.platform-config select.form-control:focus-visible { border-color: var(--c-accent-hover); }
.login-field, .sync-settings { min-width: 0; display: flex; align-items: center; gap: var(--sp-2); flex-wrap: wrap; }
.login-status-badge { padding: 3px 8px; border: 1px solid #fed7aa; border-radius: 999px; background: #fff7ed; color: #9a3412; font-size: var(--fs-xs); font-weight: 600; }
.login-status-badge.ok { border-color: #bbf7d0; background: #f0fdf4; color: #166534; }
.compact-button { min-height: 36px; padding: 0 11px; display: inline-flex; align-items: center; justify-content: center; gap: 6px; border: 1px solid transparent; border-radius: var(--r-md); font: inherit; font-size: var(--fs-sm); font-weight: 600; cursor: pointer; transition: background-color 180ms ease, border-color 180ms ease, color 180ms ease; }
.platform-config .compact-button { min-height: var(--config-control-height); }
.compact-button svg, .danger-link svg { width: 16px; height: 16px; flex: 0 0 auto; }
.secondary-button { border-color: var(--c-line-2); background: var(--c-surf); color: var(--c-text); }
.secondary-button:hover:not(:disabled) { border-color: #93c5fd; background: #eff6ff; color: #1d4ed8; }
.ghost-button { background: transparent; color: var(--c-muted); }
.ghost-button:hover:not(:disabled) { background: var(--c-surf-3); color: var(--c-text); }
.primary-button { border-color: var(--c-accent-hover); background: var(--c-accent-hover); color: #fff; }
.primary-button:hover:not(:disabled) { border-color: #1d4ed8; background: #1d4ed8; }
.compact-button:disabled, .danger-link:disabled, .icon-button:disabled { border-color: var(--c-line); background: var(--c-surf-3); color: #64748b; cursor: not-allowed; }
.compact-button:disabled svg, .danger-link:disabled svg, .icon-button:disabled svg { color: #64748b; }
.platform-config input:disabled, .platform-config select:disabled { border-color: var(--c-line); background-color: var(--c-surf-3); color: #64748b; cursor: not-allowed; }
.compact-button:focus-visible, .danger-link:focus-visible, .icon-button:focus-visible { outline: 2px solid var(--c-accent-hover); outline-offset: 2px; }
.toggle-control { min-height: 36px; display: inline-flex; align-items: center; gap: 7px; color: var(--c-text); white-space: nowrap; cursor: pointer; }
.platform-config .toggle-control { min-height: var(--config-control-height); }
.toggle-control input { position: absolute; opacity: 0; pointer-events: none; }
.toggle-track { width: 34px; height: 20px; padding: 2px; display: inline-flex; align-items: center; border-radius: 999px; background: var(--c-line-2); transition: background-color 180ms ease; }
.toggle-track > span { width: 16px; height: 16px; border-radius: 50%; background: #fff; box-shadow: 0 1px 2px rgba(15, 23, 42, .2); transition: transform 180ms ease; }
.toggle-control input:checked + .toggle-track { background: var(--c-accent-hover); }
.toggle-control input:checked + .toggle-track > span { transform: translateX(14px); }
.toggle-control input:focus-visible + .toggle-track { outline: 2px solid var(--c-accent-hover); outline-offset: 2px; }
.bot-config-list, .bot-picker { min-width: 0; display: grid; gap: var(--sp-2); }
.bot-config-row { min-width: 0; display: grid; grid-template-columns: minmax(0, 1fr) minmax(240px, 280px) 40px; align-items: center; gap: var(--sp-2); padding: 7px 8px; border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf-2); }
.bot-config-main { min-width: 0; display: grid; gap: 2px; }
.bot-config-main small { color: var(--c-muted); font-size: var(--fs-xs); overflow-wrap: anywhere; }
.icon-button { width: 40px; height: 40px; padding: 0; display: inline-grid; place-items: center; border: 0; border-radius: 999px; background: transparent; color: var(--c-muted); cursor: pointer; transition: background-color 180ms ease, color 180ms ease; }
.icon-button svg { width: 18px; height: 18px; }
.danger-icon-button:hover, .danger-icon-button:focus-visible { background: var(--c-danger-bg); color: var(--c-danger); }
.bot-picker-row { width: 100%; min-width: 0; min-height: 40px; padding: 8px 10px; display: flex; justify-content: space-between; gap: var(--sp-2); border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf); color: var(--c-text); text-align: left; cursor: pointer; }
.interval-control { min-height: 36px; display: inline-flex; align-items: center; gap: 6px; color: var(--c-muted); }
.platform-config .interval-control { min-height: var(--config-control-height); }
.interval-control input { width: 72px; min-height: 36px; padding: 0 8px; }
.platform-config .interval-control input { min-height: var(--config-control-height); }
.manual-bug-row { min-width: 0; display: grid; grid-template-columns: minmax(0, 1fr) auto; align-items: end; gap: var(--sp-2); }
.hook-row { flex-wrap: wrap; }
.hook-row code { min-width: 0; flex: 1; padding: 7px 9px; overflow-wrap: anywhere; border-radius: var(--r-sm); background: var(--c-surf-2); color: var(--c-muted); }
.danger-link { min-height: 36px; padding: 0 6px; display: inline-flex; align-items: center; gap: 6px; border: 0; background: transparent; color: var(--c-danger); font: inherit; font-size: var(--fs-sm); font-weight: 600; cursor: pointer; }
.danger-link:hover:not(:disabled) { color: #7f1d1d; text-decoration: underline; text-underline-offset: 3px; }
.inbox-workspace { min-width: 0; display: grid; grid-template-columns: minmax(250px, 330px) minmax(0, 1fr); gap: var(--sp-3); }
.ticket-list-panel, .ticket-detail-panel { min-width: 0; border: 1px solid var(--c-line); border-radius: var(--r-lg); background: var(--c-surf); }
.ticket-list-panel { position: relative; padding: var(--sp-3); overflow: auto; }
.ticket-list-panel :deep(.list-heading) { padding-right: 112px; }
.refresh-button { position: absolute; z-index: 1; top: var(--sp-2); right: var(--sp-2); }
.refresh-button svg { width: 16px; height: 16px; }
.refresh-button svg.spinning { animation: refresh-spin 800ms linear infinite; }
@keyframes refresh-spin { to { transform: rotate(360deg); } }
.ticket-detail-panel { padding: var(--sp-3); overflow: auto; }
.empty { min-height: 44px; margin: 0; display: grid; place-items: center; color: var(--c-muted); font-size: var(--fs-sm); text-align: center; }
.empty.compact { min-height: 44px; }
.attachment-preview-backdrop { position: fixed; inset: 0; z-index: 60; padding: 24px; display: grid; place-items: center; background: rgba(15, 23, 42, .58); }
.attachment-preview-modal { width: min(1080px, 92vw); max-height: 88vh; min-width: 0; overflow: hidden; border-radius: var(--r-lg); background: var(--c-surf); box-shadow: 0 20px 60px rgba(15, 23, 42, .28); }
.attachment-preview-modal header { min-height: 44px; padding: 6px 8px 6px 12px; display: flex; align-items: center; justify-content: space-between; gap: var(--sp-2); }
.attachment-preview-modal header strong { min-width: 0; overflow-wrap: anywhere; }
.attachment-preview-close {
  flex: 0 0 44px;
  width: 44px;
  height: 44px;
  padding: 0;
  display: inline-grid;
  place-items: center;
  border: 0;
  border-radius: 999px;
  background: transparent;
  color: var(--c-muted);
  cursor: pointer;
  transition: background-color 160ms ease, color 160ms ease;
}
.attachment-preview-close svg { width: 20px; height: 20px; }
.attachment-preview-close:hover { background: var(--c-surf-2); color: var(--c-text); }
.attachment-preview-close:active { background: var(--c-line); }
.attachment-preview-close:focus-visible { outline: 2px solid var(--c-accent-hover); outline-offset: 2px; }
.attachment-preview-image { display: block; max-width: 100%; max-height: calc(88vh - 56px); margin: auto; object-fit: contain; background: #0f172a; }
.attachment-preview-fallback { min-height: 220px; padding: var(--sp-4); display: grid; place-items: center; color: var(--c-muted); }
@media (prefers-reduced-motion: reduce) {
  .config-disclosure, .compact-button, .toggle-track, .toggle-track > span, .icon-button, .attachment-preview-close, .platform-config select.form-control { transition: none; }
  .refresh-button svg.spinning { animation: none; }
}
@media (max-width: 1200px) {
  .basic-row { grid-template-columns: minmax(0, 1fr) minmax(200px, .65fr); }
  .basic-row .field-label:last-child { grid-column: 1 / -1; }
}
@media (max-width: 900px) {
  .basic-row, .auth-row { grid-template-columns: minmax(0, 1fr); }
  .inbox-workspace { grid-template-columns: minmax(0, 1fr); }
  .ticket-list-panel { max-height: 360px; }
}
@media (max-width: 640px) {
  .bug-header, .section-heading, .platform-list, .hook-row { align-items: stretch; flex-direction: column; }
  .config-disclosure, .platform-chip, .bot-picker-row, .platform-config .interval-control input { min-height: 44px; }
  .compact-button, .danger-link, .toggle-control { min-height: 44px; }
  .platform-config .form-control, .platform-config .compact-button, .platform-config .toggle-control { min-height: 44px; }
  .manual-bug-row, .bot-config-row { grid-template-columns: minmax(0, 1fr); }
  .manual-bug-row .compact-button { width: 100%; }
  .bot-config-row .icon-button { justify-self: end; width: 44px; height: 44px; }
  .config-footer { align-items: stretch; flex-direction: column; }
  .config-footer .danger-link { align-self: flex-start; }
  .config-save-hint { text-align: left; }
  .config-footer .primary-button { width: 100%; min-width: 0; }
}
</style>
