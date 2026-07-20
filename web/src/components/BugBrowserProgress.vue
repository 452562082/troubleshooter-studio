<script setup lang="ts">
import { computed } from 'vue'
import { incidentBrowserProgressCodes, type IncidentBrowserProgressCode, type IncidentPhaseEvent, type PhaseAttempt } from '../lib/bridge/bugWorkflow'

type BrowserAction = 'login' | 'clear-session' | 'repair-runtime' | 'redeploy-validator' | 'edit-bug-url'

const props = withDefaults(defineProps<{
  attempt?: PhaseAttempt | null
  events?: IncidentPhaseEvent[]
  systemID?: string
  environment?: string
  pending?: boolean
}>(), { attempt: null, events: () => [], systemID: '', environment: '', pending: false })

defineEmits<{ action: [action: BrowserAction] }>()

const progressCodeAllowlist = new Set<string>(incidentBrowserProgressCodes)
const maxBrowserProgressStep = 100
const safeEvents = computed(() => props.events.reduce<Array<{ code: IncidentBrowserProgressCode; current?: number; total?: number; key: string }>>((result, event, index) => {
  if (event.type !== 'browser_progress') return result
  const code = typeof event.meta.browser_code === 'string' ? event.meta.browser_code : ''
  if (!progressCodeAllowlist.has(code)) return result
  const hasCurrent = event.meta.current !== undefined
  const hasTotal = event.meta.total !== undefined
  const validStep = (value: unknown) => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 && value <= maxBrowserProgressStep
  if (hasCurrent !== hasTotal || hasCurrent && (!validStep(event.meta.current) || !validStep(event.meta.total) || Number(event.meta.current) > Number(event.meta.total))) return result
  const parsed = { code: code as IncidentBrowserProgressCode, current: hasCurrent ? event.meta.current as number : undefined, total: hasTotal ? event.meta.total as number : undefined, key: `${code}:${String(event.meta.current ?? '')}:${String(event.meta.total ?? '')}:${index}` }
  if (code === 'browser_runtime_downloading' && result[result.length - 1]?.code === code) result[result.length - 1] = parsed
  else result.push(parsed)
  return result
}, []))

function progressCopy(event: { code: IncidentBrowserProgressCode; current?: number; total?: number }): string {
  const count = event.current !== undefined && event.total !== undefined ? `${event.current}/${event.total}` : ''
  if (event.code === 'browser_launching') return '正在启动验证浏览器'
  if (event.code === 'browser_context_preparing') return '正在准备隔离浏览器环境'
  if (event.code === 'browser_evidence_preparing') return '正在接入页面与网络证据采集'
  if (event.code === 'browser_starting') return '正在打开待验证页面'
  if (event.code === 'runtime_preparing') return '准备验证浏览器'
  if (event.code === 'browser_runtime_installing') return '正在准备验证浏览器运行时'
  if (event.code === 'browser_runtime_importing') return '正在初始化 App 内置 Chromium'
  if (event.code === 'browser_runtime_dependencies_installing') return '正在安装 Playwright 依赖'
  if (event.code === 'browser_runtime_downloading') return `正在下载 Chromium：${event.current ?? 0}%`
  if (event.code === 'browser_runtime_probing') return '正在启动 Chromium 自检'
  if (event.code === 'browser_runtime_ready') return '验证浏览器运行时已就绪'
  if (event.code === 'browser_login_opened') return '验证浏览器已打开，请完成登录'
  if (event.code === 'browser_login_completed') return '浏览器登录会话已保存'
  if (event.code === 'browser_action_started' || event.code === 'action_started') return count ? `执行 ${count}：开始页面操作` : '正在执行页面操作'
  if (event.code === 'browser_action_completed' || event.code === 'action_completed') return count ? `执行 ${count}：页面操作完成` : '页面操作已完成'
  return '浏览器操作进行中'
}

const errorCode = computed(() => {
  const persisted = props.attempt?.error_code?.trim()
  if (persisted) return persisted
  const output = props.attempt?.output_json || {}
  return typeof output.error_code === 'string' ? output.error_code.trim() : ''
})

const stableErrorCode = computed(() => {
  const value = errorCode.value
  if (!/^[a-z0-9_]{1,128}$/.test(value)) return ''
  return value.startsWith('browser_') || value === 'validator_not_installed' ? value : ''
})

const state = computed<'progress' | 'login' | 'runtime' | 'validator' | 'quota' | 'locator' | 'url' | 'business' | 'plan' | 'attachment' | 'process' | 'retry' | 'system' | ''>(() => {
  const code = stableErrorCode.value
  if (code === 'browser_login_required') return 'login'
  if (code === 'browser_runtime_broken') return 'runtime'
  if (code === 'validator_not_installed') return 'validator'
  if (code === 'browser_validator_usage_limited') return 'quota'
  if (code === 'browser_locator_failed') return 'locator'
  if (code === 'browser_url_required') return 'url'
  if (code === 'browser_assertion_failed') return 'business'
  if (code === 'browser_validator_plan_invalid' || code === 'browser_locator_repair_plan_invalid') return 'plan'
  if (code === 'browser_validator_attachment_failed') return 'attachment'
  if (code === 'browser_validator_no_output' || code === 'browser_validator_process_failed') return 'process'
  if (code === 'browser_validator_failed') return 'retry'
  if (code.startsWith('browser_')) return 'system'
  return safeEvents.value.length > 0 ? 'progress' : ''
})

const loginOrigin = computed(() => {
  const value = props.attempt?.output_json?.login_origin
  if (typeof value !== 'string') return ''
  try {
    const parsed = new URL(value)
    return ['http:', 'https:'].includes(parsed.protocol) && parsed.origin === value.replace(/\/$/, '') ? parsed.origin : ''
  } catch { return '' }
})

const stateCopy = computed(() => {
  if (stableErrorCode.value === 'browser_locator_repair_plan_invalid') {
    return '页面定位修复计划未通过协议校验。当前 Case、原计划与现场证据均已保留，可以直接重新生成计划并重试。'
  }
  return ({
    login: '当前验证需要登录。请在 Studio 打开的验证浏览器中完成登录，不要在 Case 中粘贴账号、密码或 Cookie。',
    runtime: '验证浏览器环境不可用。修复并通过运行时探测后，Studio 会创建一次新的验证继续。',
    validator: '验证机器人尚未部署，浏览器验证不会退回普通排障机器人。请重新部署当前机器人的 validator 角色。',
    quota: '验证机器人用量已达上限。恢复额度或切换到可用机器人后，请重新开始故障闭环。',
    locator: '页面元素定位失败。请补充失败步骤附近可见的控件名称或页面变化后重试。',
    url: '来源工单缺少 frontend_url。请先在来源工单平台补充页面地址，再前往 Bug 收件箱重新同步该 Bug。',
    business: '页面结果与预期不一致。请补充最小业务预期或测试数据后重试。',
    plan: '验证机器人生成的浏览器计划未通过结构校验。可以在当前 Case 内重新生成计划，无需重建故障闭环。',
    attachment: '验证机器人无法读取本次截图证据。Studio 会优先使用结构化页面与网络证据降级判定；仍失败时请检查 macOS 文件访问权限后在当前 Case 重试。',
    process: '验证机器人进程异常退出或没有返回结构化结果。当前 Case 和浏览器证据均已保留，可以直接重试。',
    retry: '验证机器人本次执行异常。可以在当前 Case 内重新运行验证，无需补附件或重建故障闭环。',
    system: '浏览器验证遇到系统错误。请刷新 Case 后按稳定错误码处理，不要用附件补充来掩盖运行时故障。',
    progress: '',
    '': '',
  })[state.value]
})
</script>

<template>
  <section v-if="state" class="browser-progress" :data-browser-state="state" aria-labelledby="browser-progress-title">
    <header>
      <div>
        <span>渲染浏览器验证</span>
        <h3 id="browser-progress-title">{{ state === 'progress' ? '浏览器正在执行' : '浏览器验证需要处理' }}</h3>
      </div>
      <small v-if="systemID || environment">{{ systemID || '系统未知' }} · {{ environment || '环境未知' }}</small>
    </header>

    <ol v-if="safeEvents.length" class="browser-progress-events" aria-label="浏览器执行进度" aria-live="polite">
      <li v-for="event in safeEvents" :key="event.key">
        <span aria-hidden="true"></span><p>{{ progressCopy(event) }}</p>
      </li>
    </ol>

    <div v-if="stateCopy" class="browser-recovery-copy">
      <p>{{ stateCopy }}</p>
      <small v-if="state === 'login' && loginOrigin">登录入口：{{ loginOrigin }}</small>
      <small v-if="stableErrorCode" data-browser-error-code>错误码：{{ stableErrorCode }}</small>
    </div>

    <div v-if="state === 'login'" class="browser-recovery-actions">
      <button class="btn primary" type="button" data-browser-action="login" :disabled="pending" @click="$emit('action', 'login')">打开验证浏览器完成登录</button>
      <button class="btn" type="button" data-browser-action="clear-session" :disabled="pending" @click="$emit('action', 'clear-session')">清除此环境登录态</button>
    </div>
    <div v-else-if="state === 'runtime'" class="browser-recovery-actions">
      <button class="btn primary" type="button" data-browser-action="repair-runtime" :disabled="pending" @click="$emit('action', 'repair-runtime')">修复浏览器环境并重试</button>
    </div>
    <div v-else-if="state === 'validator'" class="browser-recovery-actions">
      <button class="btn primary" type="button" data-browser-action="redeploy-validator" :disabled="pending" @click="$emit('action', 'redeploy-validator')">重新部署验证机器人</button>
    </div>
    <div v-else-if="state === 'url'" class="browser-recovery-actions">
      <button class="btn primary" type="button" data-browser-action="edit-bug-url" :disabled="pending" @click="$emit('action', 'edit-bug-url')">前往 Bug 收件箱重新同步</button>
    </div>
  </section>
</template>

<style scoped>
.browser-progress { display: grid; gap: var(--sp-3); padding: var(--sp-4); border: 1px solid #bfdbfe; border-left: 3px solid #2563eb; border-radius: var(--r-lg); background: #f8fbff; }
.browser-progress[data-browser-state="login"], .browser-progress[data-browser-state="locator"], .browser-progress[data-browser-state="url"], .browser-progress[data-browser-state="business"] { border-color: #fed7aa; border-left-color: #ea580c; background: #fffaf5; }
.browser-progress[data-browser-state="runtime"], .browser-progress[data-browser-state="validator"], .browser-progress[data-browser-state="quota"], .browser-progress[data-browser-state="plan"], .browser-progress[data-browser-state="retry"], .browser-progress[data-browser-state="system"] { border-color: #fecaca; border-left-color: #dc2626; background: #fffafa; }
.browser-progress header { min-width: 0; display: flex; align-items: flex-start; justify-content: space-between; flex-wrap: wrap; gap: var(--sp-2); }
.browser-progress header span, .browser-progress header small, .browser-recovery-copy small { color: var(--c-muted); font-size: var(--fs-xs); }
.browser-progress h3, .browser-progress p { margin: 0; }
.browser-progress h3 { margin-top: 2px; color: var(--c-ink); font-size: var(--fs-base); }
.browser-progress-events { display: grid; gap: var(--sp-2); margin: 0; padding: 0; list-style: none; }
.browser-progress-events li { min-width: 0; display: grid; grid-template-columns: 10px minmax(0, 1fr); align-items: start; gap: var(--sp-2); color: var(--c-text); font-size: var(--fs-sm); }
.browser-progress-events li > span { width: 8px; height: 8px; margin-top: 5px; border-radius: 50%; background: #2563eb; }
.browser-progress-events p, .browser-recovery-copy { overflow-wrap: anywhere; line-height: 1.55; }
.browser-recovery-copy { display: grid; gap: 4px; color: var(--c-text); font-size: var(--fs-sm); }
.browser-recovery-actions { display: flex; flex-wrap: wrap; gap: var(--sp-2); }
.browser-recovery-actions .btn { min-height: 44px; }
@media (max-width: 560px) { .browser-recovery-actions { flex-direction: column; } .browser-recovery-actions .btn { width: 100%; justify-content: center; } }
</style>
