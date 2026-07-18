import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { IncidentPhaseEvent, PhaseAttempt } from '../lib/bridge/bugWorkflow'
import BugBrowserProgress from './BugBrowserProgress.vue'

function attempt(errorCode = '', output: Record<string, unknown> = {}): PhaseAttempt {
  return {
    id: 'attempt-1', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: errorCode ? 'failed' : 'running',
    agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: output, parent_attempt_id: '', started_at: '',
    error_code: errorCode, error_message: 'Playwright 未安装，请提供 HAR /Users/alice/private/trace.zip', usage: {},
  }
}

const progress: IncidentPhaseEvent[] = [
  { type: 'browser_progress', message: 'Cookie: sid=secret /Users/alice/private/trace.zip', raw: { Authorization: 'Bearer secret', storageState: 'secret' }, meta: { attempt_id: 'attempt-1', browser_code: 'browser_launching' } },
  { type: 'browser_progress', meta: { attempt_id: 'attempt-1', browser_code: 'browser_context_preparing' } },
  { type: 'browser_progress', meta: { attempt_id: 'attempt-1', browser_code: 'browser_evidence_preparing' } },
  { type: 'browser_progress', meta: { attempt_id: 'attempt-1', browser_code: 'browser_starting' } },
  { type: 'browser_progress', message: 'password=hunter2', meta: { attempt_id: 'attempt-1', browser_code: 'browser_action_started', action_id: '/private/open-users', current: 2, total: 4 } },
]

describe('BugBrowserProgress', () => {
  it('renders structured browser progress without exposing raw attempt errors', () => {
    const wrapper = mount(BugBrowserProgress, { props: { attempt: attempt(), events: progress, systemID: 'base', environment: 'test' } })

    expect(wrapper.text()).toContain('正在启动验证浏览器')
    expect(wrapper.text()).toContain('正在准备隔离浏览器环境')
    expect(wrapper.text()).toContain('正在接入页面与网络证据采集')
    expect(wrapper.text()).toContain('正在打开待验证页面')
    expect(wrapper.text()).toContain('执行 2/4：开始页面操作')
    expect(wrapper.text()).not.toContain('Playwright 未安装，请提供 HAR')
    expect(wrapper.text()).not.toContain('/Users/alice/private/trace.zip')
    expect(wrapper.text()).not.toMatch(/Cookie|Authorization|password|storageState|hunter2|open-users/)
    expect(wrapper.html()).not.toMatch(/Cookie|Authorization|password|storageState|hunter2|private/)
  })

  it('shows the latest Chromium download percentage without rendering every prior update', () => {
    const wrapper = mount(BugBrowserProgress, {
      props: {
        attempt: attempt(),
        events: [
          { type: 'browser_progress', meta: { browser_code: 'browser_runtime_dependencies_installing' } },
          { type: 'browser_progress', meta: { browser_code: 'browser_runtime_downloading', current: 10, total: 100 } },
          { type: 'browser_progress', meta: { browser_code: 'browser_runtime_downloading', current: 30, total: 100 } },
          { type: 'browser_progress', meta: { browser_code: 'browser_runtime_probing' } },
        ],
      },
    })

    expect(wrapper.text()).toContain('正在安装 Playwright 依赖')
    expect(wrapper.text()).toContain('正在下载 Chromium：30%')
    expect(wrapper.text()).not.toContain('正在下载 Chromium：10%')
    expect(wrapper.text()).toContain('正在启动 Chromium 自检')
  })

  it('drops unknown progress codes and never uses invalid numeric metadata as copy', () => {
    const wrapper = mount(BugBrowserProgress, {
      props: {
        attempt: attempt(),
        events: [
          { type: 'agent_text', message: 'Cookie: secret', meta: { browser_code: 'browser_starting' } },
          { type: 'browser_progress', message: 'Authorization: secret', meta: { browser_code: 'browser_password_hunter2', current: 1, total: 2 } },
          { type: 'browser_progress', message: 'event type is not a progress code', meta: { browser_code: 'browser_progress' } },
          { type: 'browser_progress', message: 'storageState secret', meta: { browser_code: 'browser_action_started', current: '/private/2', total: 4 } },
        ],
      },
    })

    expect(wrapper.text()).toBe('')
    expect(wrapper.html()).not.toMatch(/Cookie|Authorization|password|storageState|private/)
  })

  it('offers visible login and session clearing without a credentials field', async () => {
    const wrapper = mount(BugBrowserProgress, {
      props: {
        attempt: attempt('browser_login_required', { error_code: 'browser_login_required', application_origin: 'https://app.test', login_origin: 'https://login.test' }),
        events: [], systemID: 'base', environment: 'test',
      },
    })

    expect(wrapper.text()).toContain('base · test')
    expect(wrapper.text()).toContain('https://login.test')
    expect(wrapper.get('[data-browser-action="login"]').text()).toBe('打开验证浏览器完成登录')
    expect(wrapper.get('[data-browser-action="clear-session"]').text()).toBe('清除此环境登录态')
    expect(wrapper.find('input[type="password"]').exists()).toBe(false)
    expect(wrapper.find('textarea').exists()).toBe(false)
    await wrapper.get('[data-browser-action="login"]').trigger('click')
    await wrapper.get('[data-browser-action="clear-session"]').trigger('click')
    expect(wrapper.emitted('action')).toEqual([['login'], ['clear-session']])
  })

  it('separates runtime repair, validator deployment, locator and business gaps', async () => {
    const runtime = mount(BugBrowserProgress, { props: { attempt: attempt('browser_runtime_broken'), events: [], systemID: 'base', environment: 'test' } })
    expect(runtime.text()).toContain('验证浏览器环境不可用')
    expect(runtime.get('[data-browser-error-code]').text()).toBe('错误码：browser_runtime_broken')
    expect(runtime.get('[data-browser-action="repair-runtime"]').text()).toBe('修复浏览器环境并重试')
    expect(runtime.text()).not.toContain('Playwright 未安装，请提供 HAR')

    const validator = mount(BugBrowserProgress, { props: { attempt: attempt('validator_not_installed'), events: [], systemID: 'base', environment: 'test' } })
    expect(validator.text()).toContain('验证机器人尚未部署')
    expect(validator.get('[data-browser-action="redeploy-validator"]').text()).toBe('重新部署验证机器人')

    const quota = mount(BugBrowserProgress, { props: { attempt: attempt('browser_validator_usage_limited'), events: [], systemID: 'base', environment: 'test' } })
    expect(quota.text()).toContain('验证机器人用量已达上限')
    expect(quota.text()).toContain('恢复额度或切换到可用机器人')
    expect(quota.find('[data-browser-action]').exists()).toBe(false)

    const locator = mount(BugBrowserProgress, { props: { attempt: attempt('browser_locator_failed'), events: [], systemID: 'base', environment: 'test' } })
    expect(locator.text()).toContain('页面元素定位失败')
    expect(locator.find('[data-browser-action="repair-runtime"]').exists()).toBe(false)

    const business = mount(BugBrowserProgress, { props: { attempt: attempt('browser_url_required'), events: [], systemID: 'base', environment: 'test' } })
    expect(business.text()).toContain('来源工单')
    expect(business.text()).toContain('frontend_url')
    expect(business.text()).toContain('重新同步')
    expect(business.get('[data-browser-action="edit-bug-url"]').text()).toBe('前往 Bug 收件箱重新同步')
    expect(business.find('[data-browser-action="repair-runtime"]').exists()).toBe(false)

    const system = mount(BugBrowserProgress, { props: { attempt: attempt('browser_worker_failed'), events: [], systemID: 'base', environment: 'test' } })
    expect(system.text()).toContain('浏览器验证遇到系统错误')
    expect(system.get('[data-browser-error-code]').text()).toBe('错误码：browser_worker_failed')
  })

  it('never renders an untrusted error code as recovery copy', () => {
    const wrapper = mount(BugBrowserProgress, { props: { attempt: attempt('browser_worker_failed Cookie_secret'), events: [] } })
    expect(wrapper.text()).toBe('')
    expect(wrapper.html()).not.toMatch(/Cookie|secret/)
  })
})
