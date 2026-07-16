<script setup lang="ts">
import { computed } from 'vue'
import type { IncidentPhaseEvent, PhaseAttempt } from '../lib/bridge/bugWorkflow'

type BrowserAction = 'login' | 'clear-session' | 'repair-runtime' | 'redeploy-validator'

const props = withDefaults(defineProps<{
  attempt?: PhaseAttempt | null
  events?: IncidentPhaseEvent[]
  systemID?: string
  environment?: string
  pending?: boolean
}>(), { attempt: null, events: () => [], systemID: '', environment: '', pending: false })

defineEmits<{ action: [action: BrowserAction] }>()

const errorCode = computed(() => {
  const persisted = props.attempt?.error_code?.trim()
  if (persisted) return persisted
  const output = props.attempt?.output_json || {}
  return typeof output.error_code === 'string' ? output.error_code.trim() : ''
})

const state = computed<'progress' | 'login' | 'runtime' | 'validator' | 'locator' | 'business' | 'system' | ''>(() => {
  if (errorCode.value === 'browser_login_required') return 'login'
  if (errorCode.value === 'browser_runtime_broken') return 'runtime'
  if (errorCode.value === 'validator_not_installed') return 'validator'
  if (errorCode.value === 'browser_locator_failed') return 'locator'
  if (['browser_url_required', 'browser_assertion_failed'].includes(errorCode.value)) return 'business'
  if (errorCode.value.startsWith('browser_')) return 'system'
  return props.events.length > 0 ? 'progress' : ''
})

const loginOrigin = computed(() => {
  const value = props.attempt?.output_json?.login_origin
  if (typeof value !== 'string') return ''
  try {
    const parsed = new URL(value)
    return ['http:', 'https:'].includes(parsed.protocol) && parsed.origin === value.replace(/\/$/, '') ? parsed.origin : ''
  } catch { return '' }
})

const stateCopy = computed(() => ({
  login: '当前验证需要登录。请在 Studio 打开的验证浏览器中完成登录，不要在 Case 中粘贴账号、密码或 Cookie。',
  runtime: '验证浏览器环境不可用。修复并通过运行时探测后，Studio 会创建一次新的验证继续。',
  validator: '验证机器人尚未部署，浏览器验证不会退回普通排障机器人。请重新部署当前机器人的 validator 角色。',
  locator: '页面元素定位失败。请补充失败步骤附近可见的控件名称或页面变化后重试。',
  business: errorCode.value === 'browser_url_required'
    ? 'Web 验证缺少可访问的页面地址。请补充当前环境的页面入口后重试。'
    : '页面结果与预期不一致。请补充最小业务预期或测试数据后重试。',
  system: '浏览器验证遇到系统错误。请刷新 Case 后按稳定错误码处理，不要用附件补充来掩盖运行时故障。',
  progress: '',
  '': '',
})[state.value])
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

    <ol v-if="events.length" class="browser-progress-events" aria-label="浏览器执行进度" aria-live="polite">
      <li v-for="(event, index) in events" :key="`${event.at || ''}-${event.type || ''}-${event.message || ''}-${String(event.meta.browser_code || '')}-${String(event.meta.action_id || '')}-${index}`">
        <span aria-hidden="true"></span><p>{{ event.message }}</p>
      </li>
    </ol>

    <div v-if="stateCopy" class="browser-recovery-copy">
      <p>{{ stateCopy }}</p>
      <small v-if="state === 'login' && loginOrigin">登录入口：{{ loginOrigin }}</small>
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
  </section>
</template>

<style scoped>
.browser-progress { display: grid; gap: var(--sp-3); padding: var(--sp-4); border: 1px solid #bfdbfe; border-left: 3px solid #2563eb; border-radius: var(--r-lg); background: #f8fbff; }
.browser-progress[data-browser-state="login"], .browser-progress[data-browser-state="locator"], .browser-progress[data-browser-state="business"] { border-color: #fed7aa; border-left-color: #ea580c; background: #fffaf5; }
.browser-progress[data-browser-state="runtime"], .browser-progress[data-browser-state="validator"], .browser-progress[data-browser-state="system"] { border-color: #fecaca; border-left-color: #dc2626; background: #fffafa; }
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
