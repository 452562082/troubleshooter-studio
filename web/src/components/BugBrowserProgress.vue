<script setup lang="ts">
import { computed } from 'vue'
import { incidentBrowserProgressCodes, type IncidentBrowserProgressCode, type IncidentPhaseEvent, type PhaseAttempt } from '../lib/bridge/bugWorkflow'

type BrowserAction = 'login' | 'confirm-login' | 'clear-session' | 'repair-runtime' | 'redeploy-validator' | 'edit-bug-url'

const props = withDefaults(defineProps<{
  attempt?: PhaseAttempt | null
  events?: IncidentPhaseEvent[]
  systemID?: string
  environment?: string
  pending?: boolean
  loginReady?: boolean
}>(), { attempt: null, events: () => [], systemID: '', environment: '', pending: false, loginReady: false })

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
  if (event.code === 'browser_login_opened') return '验证浏览器已打开；完成登录后请关闭窗口'
  if (event.code === 'browser_login_completed') return '浏览器会话快照已保存（未校验登录）'
  if (event.code === 'browser_action_started' || event.code === 'action_started') return count ? `执行 ${count}：开始页面操作` : '正在执行页面操作'
  if (event.code === 'browser_action_completed' || event.code === 'action_completed') return count ? `执行 ${count}：页面操作完成` : '页面操作已完成'
  if (event.code === 'browser_plan_generating') return '验证 Agent 正在观察并决定下一步'
  if (event.code === 'browser_repair_generating') return '验证 Agent 正在根据现场调整策略'
  if (event.code === 'browser_result_evaluating') return '正在判定浏览器验证结果'
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

const userClarificationApplied = computed(() => props.attempt?.output_json?.user_clarification_applied === true)

const hasValidationQuestions = computed(() => {
  const questions = props.attempt?.output_json?.validation_questions
  const hasQuestions = Array.isArray(questions) && questions.some(value => {
    if (!value || typeof value !== 'object') return false
    const question = (value as Record<string, unknown>).question
    return typeof question === 'string' && question.trim().length > 0
  })
  return hasQuestions || stableErrorCode.value === 'browser_locator_failed' && !userClarificationApplied.value
})

const planValidationIssueCopy: Record<string, string> = {
  plan_not_canonical: '计划包含与持久化协议不一致的空字段或默认值',
  locator_positional_css_forbidden: '同名控件使用了不稳定的位置型 CSS；Studio 未能自动转换为行内结构化定位',
  frontend_scope_incomplete: '场景合同未覆盖全部已选择的应用端',
  frontend_scope_order_invalid: '场景合同中的应用端顺序与用户选择不一致',
  frontend_entry_not_visited: '计划没有访问全部已选择的应用端',
  frontend_evidence_incomplete: '至少一个已选择的应用端没有形成可验证证据',
  device_profile_mismatch: '计划设备类型与当前验证场景不一致',
  scenario_contract_invalid: '场景合同缺失、引用不完整或不属于当前用户澄清',
  search_entry_unresolved: '计划没有使用当前页面中可确认的搜索入口',
  frontend_origin_invalid: '计划入口不属于本次已配置的应用范围',
  sensitive_plan_rejected: '计划包含凭据语义或其他不允许持久化的敏感内容',
  locator_contract_invalid: '页面控件定位方式不符合浏览器协议',
  repair_strategy_repeated: '页面策略重复了当前 Case 中已经失败的因果交互链',
  plan_structure_invalid: '动作、断言或必填字段不符合浏览器计划协议',
}
const planValidationIssue = computed(() => {
  if (stableErrorCode.value !== 'browser_validator_plan_invalid' && stableErrorCode.value !== 'browser_locator_repair_plan_invalid') return ''
  const value = props.attempt?.output_json?.plan_validation_code
  if (typeof value !== 'string' || !/^[a-z0-9_]{1,128}$/.test(value)) return ''
  return planValidationIssueCopy[value] || ''
})

const failureStageCopy = computed(() => {
  const stage = props.attempt?.output_json?.failure_stage
  if (typeof stage !== 'string') return ''
  return ({
    planning: '理解场景并决定下一步',
    locator_repair: '根据现场证据决定下一步',
    evaluation: '判定验证结果',
    plan_validation: '校验内部验证策略',
  } as Record<string, string>)[stage] || ''
})

const transportRetryCount = computed(() => {
  const value = props.attempt?.output_json?.transport_retry_count
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0 && value <= 3 ? value : 0
})

const state = computed<'progress' | 'assistance' | 'login' | 'runtime' | 'validator' | 'quota' | 'locator' | 'url' | 'business' | 'plan' | 'attachment' | 'configuration' | 'process' | 'transport' | 'retry' | 'system' | ''>(() => {
  const code = stableErrorCode.value
  if (hasValidationQuestions.value) return 'assistance'
  if (code === 'browser_validation_needs_user_input') return 'assistance'
  if (code === 'browser_login_required') return 'login'
  if (code === 'browser_runtime_broken') return 'runtime'
  if (code === 'validator_not_installed') return 'validator'
  if (code === 'browser_validator_usage_limited') return 'quota'
  if (code === 'browser_locator_failed') return 'locator'
  if (code === 'browser_url_required') return 'url'
  if (code === 'browser_assertion_failed') return 'business'
  if (code === 'browser_validator_plan_invalid' || code === 'browser_locator_repair_plan_invalid') return 'plan'
  if (code === 'browser_validator_attachment_failed') return 'attachment'
  if (code === 'browser_validator_configuration_invalid') return 'configuration'
  if (code === 'browser_validator_timeout' || code === 'browser_validator_no_output' || code === 'browser_validator_process_failed') return 'process'
  if (code === 'browser_validator_transport_failed') return 'transport'
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
  if (stableErrorCode.value === 'browser_validator_timeout') {
    if (failureStageCopy.value === '理解场景并决定下一步') {
      return '验证 Agent 在理解场景和决定下一步时超时，尚未开始浏览器执行。可以在当前 Case 重试，无需重建故障闭环。'
    }
    if (failureStageCopy.value === '根据现场证据决定下一步') {
      return '验证 Agent 在根据页面现场决定下一步时超时。Studio 会改用已冻结证据进入结果判定；若判定也超时，当前 Case 仍保留全部现场证据。'
    }
    if (failureStageCopy.value === '判定验证结果') {
      return '验证 Agent 基于同一份冻结证据自动重试判定后仍超时。浏览器步骤和证据均已保留，不需要重新补充附件。'
    }
    return '等待验证 Agent 超时。当前 Case 和已采集的浏览器证据均已保留，可以直接重试，无需补充附件或重建故障闭环。'
  }
  if (stableErrorCode.value === 'browser_locator_repair_plan_invalid') {
    return '验证 Agent 根据页面现场给出的后续策略连续未通过内部协议校验。Studio 已先自动纠正一次；当前 Case 和现场证据均已保留，可以直接重试。'
  }
  if (stableErrorCode.value === 'browser_validator_transport_failed') {
    const retryCopy = transportRetryCount.value > 0 ? `自动重试 ${transportRetryCount.value} 次后` : '自动重试后'
    return `验证 Agent 与模型服务连接中断，Studio 在同一阶段保留输入并${retryCopy}仍未恢复。当前 Case 和已采集现场均已保留，可重新连接继续验证；无需补充附件或重建故障闭环。`
  }
  if (stableErrorCode.value === 'browser_locator_failed' && userClarificationApplied.value) {
    return '验证 Agent 已采用你补充的真实流程，但页面控件定位策略仍然耗尽。无需再次回答相同问题；重新观察后会从当前 Case 继续验证。'
  }
  const artifactCopy: Record<string, string> = {
    browser_artifact_staging_invalid: '验证证据暂存目录不可用，失败发生在浏览器启动前。请检查本地磁盘与目录权限后在当前 Case 重试。',
    browser_artifact_identity_changed: '验证过程中证据目录身份发生变化，Studio 已拒绝继续使用该目录。请在当前 Case 重试；若持续出现，请检查清理程序或同步工具。',
    browser_artifact_manifest_invalid: '浏览器已完成操作，但生成的证据清单或文件格式未通过校验。当前错误属于证据验收阶段，不是页面定位失败。',
    browser_artifact_digest_changed: '浏览器证据完成后内容发生变化，Studio 已拒绝发布不一致的证据。',
    browser_artifact_sensitive: '浏览器证据包含未脱敏的凭据信息，Studio 已停止发布。请检查 Network 或 Console 的脱敏配置。',
    browser_artifact_freeze_failed: '浏览器证据已生成，但注册发布失败。页面操作无需补录，请检查本地证据存储后在当前 Case 重试。',
    browser_artifact_frozen_invalid: '已发布证据未通过最终完整性校验，Studio 已停止后续判定。',
    browser_artifact_repair_evidence_invalid: '页面定位失败后，供修复计划使用的证据无法读取。原始页面操作已完成，可在当前 Case 重试。',
    browser_artifact_repair_cleanup_failed: '页面定位修复证据的临时副本清理失败，Studio 已停止后续执行。',
    browser_artifact_evaluator_evidence_invalid: '浏览器操作已完成，但供最终判定使用的证据无法读取。这不是页面定位失败。',
    browser_artifact_evaluator_cleanup_failed: '最终判定证据的临时副本清理失败，Studio 已停止后续执行。',
    browser_artifact_response_assertion_invalid: '接口响应断言证据不完整或与当前执行不一致，Studio 不会改用页面截图猜测结果。',
    browser_scenario_binding_invalid: '回归场景与首次验证冻结合同不一致，Studio 已在浏览器启动前停止执行，避免用另一个场景误判回归结果。',
  }
  if (artifactCopy[stableErrorCode.value]) return artifactCopy[stableErrorCode.value]
  return ({
    assistance: '验证 Agent 无法安全确定下一步，已暂停并提出具体问题。回答后会在当前 Case 中重建 scenario_contract 和完整验证计划。',
    login: props.loginReady
      ? '浏览器会话快照已保存，这是未校验登录的快照。若你确实完成了登录，请确认继续；下一轮验证会用真实页面结果再次检查。'
      : '当前验证需要登录。请在 Studio 打开的验证浏览器中完成登录，然后主动关闭浏览器窗口。Studio 不通过 Cookie、页面路径或按钮猜测登录状态。',
    runtime: '验证浏览器环境不可用。修复并通过运行时探测后，Studio 会创建一次新的验证继续。',
    validator: '验证机器人尚未部署，浏览器验证不会退回普通排障机器人。请重新部署当前机器人的 validator 角色。',
    quota: '验证机器人用量已达上限。恢复额度或切换到可用机器人后，请重新开始故障闭环。',
    locator: '页面定位经过有限次现场调整仍失败。当前 Case 已保留执行证据；重试后 Agent 会重新观察现场并继续判断，只有业务语义确实不明确时才会向你提问。',
    url: '来源工单缺少 frontend_url。请先在来源工单平台补充页面地址，再前往 Bug 收件箱重新同步该 Bug。',
    business: '页面结果与预期不一致。请补充最小业务预期或测试数据后重试。',
    plan: '验证 Agent 的内部执行策略连续未通过协议校验。Studio 已自动纠错并保留当前 Case，无需重建故障闭环；这属于系统执行问题，不要求你补页面控件或协议信息。',
    attachment: '验证机器人无法读取本次截图证据。Studio 会优先使用结构化页面与网络证据降级判定；仍失败时请检查 macOS 文件访问权限后在当前 Case 重试。',
    configuration: '验证机器人启动配置不兼容。请升级或重新启动已修复的 Studio 后，在当前 Case 直接重试验证；无需补充证据或重建故障闭环。',
    process: '验证机器人进程异常退出或没有返回结构化结果。当前 Case 和浏览器证据均已保留，可以直接重试。',
    transport: '',
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
      <small v-if="planValidationIssue" data-browser-plan-validation-issue>未通过规则：{{ planValidationIssue }}</small>
      <small v-if="failureStageCopy" data-browser-failure-stage>发生阶段：{{ failureStageCopy }}</small>
      <small v-if="state === 'login' && loginOrigin">登录入口：{{ loginOrigin }}</small>
      <small v-if="stableErrorCode" data-browser-error-code>错误码：{{ stableErrorCode }}</small>
    </div>

    <div v-if="state === 'login'" class="browser-recovery-actions">
      <button v-if="loginReady" class="btn primary" type="button" data-browser-action="confirm-login" :disabled="pending" @click="$emit('action', 'confirm-login')">我已完成登录，继续验证</button>
      <button v-else class="btn primary" type="button" data-browser-action="login" :disabled="pending" @click="$emit('action', 'login')">打开验证浏览器</button>
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
.browser-progress[data-browser-state="assistance"], .browser-progress[data-browser-state="login"], .browser-progress[data-browser-state="url"], .browser-progress[data-browser-state="business"] { border-color: #fed7aa; border-left-color: #ea580c; background: #fffaf5; }
.browser-progress[data-browser-state="runtime"], .browser-progress[data-browser-state="validator"], .browser-progress[data-browser-state="quota"], .browser-progress[data-browser-state="locator"], .browser-progress[data-browser-state="plan"], .browser-progress[data-browser-state="transport"], .browser-progress[data-browser-state="retry"], .browser-progress[data-browser-state="system"] { border-color: #fecaca; border-left-color: #dc2626; background: #fffafa; }
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
