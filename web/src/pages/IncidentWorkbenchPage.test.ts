import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import {
  approveIncidentFix,
  approveIncidentMerge,
  clearIncidentBrowserSession,
  completeIncidentRemediation,
  continueIncidentCase,
  getIncidentBrowserRuntimeStatus,
  getIncidentCase,
  listBugs,
  listIncidentFixBranches,
  listIncidentCases,
  matchBugBots,
  notifyIncidentDeployed,
  openIncidentBrowserLogin,
  prepareIncidentBrowserRuntime,
  reconsiderIncidentRemediation,
  repairIncidentBrowserRuntime,
  resolveIncidentFrontendEntry,
  IncidentWorkflowCommandError,
  resetIncidentCaseWithWarnings,
  saveBugSelectedBot,
  startIncidentCase,
  uploadIncidentEvidenceImages,
  type CaseStatus,
  type IncidentCase,
  type IncidentCaseDetail,
} from '../lib/bridge'
import BugCaseLifecycle from '../components/BugCaseLifecycle.vue'
import IncidentWorkbenchPage from './IncidentWorkbenchPage.vue'

const route = vi.hoisted(() => ({ path: '/incidents', query: {} as Record<string, string> }))
const router = vi.hoisted(() => ({ replace: vi.fn(), push: vi.fn() }))
const runtime = vi.hoisted(() => ({ EventsOn: vi.fn((_name: string, _handler: (payload: unknown) => void) => vi.fn()) }))
const notifications = vi.hoisted(() => ({
  error: vi.fn(),
  success: vi.fn(),
  info: vi.fn(),
  toastError: vi.fn(),
}))
const originalScrollIntoView = HTMLElement.prototype.scrollIntoView

vi.mock('vue-router', () => ({ useRoute: () => route, useRouter: () => router }))
vi.mock('../../wailsjs/runtime/runtime', () => ({ EventsOn: runtime.EventsOn }))
vi.mock('../lib/bridge', async importOriginal => ({
  ...(await importOriginal<typeof import('../lib/bridge')>()),
  approveIncidentFix: vi.fn(),
  approveIncidentMerge: vi.fn(),
  cancelIncidentAttempt: vi.fn(),
  clearIncidentBrowserSession: vi.fn(),
  completeIncidentRemediation: vi.fn(),
  continueIncidentCase: vi.fn(),
  fetchBugByID: vi.fn(),
  getIncidentBrowserRuntimeStatus: vi.fn().mockResolvedValue({ state: 'ready', version: '1.61.1', error_code: '', message: '' }),
  getIncidentCase: vi.fn(),
  listBugs: vi.fn().mockResolvedValue([]),
  listIncidentFixBranches: vi.fn().mockResolvedValue({ 'admin-web': ['feature/new-navigation'], api: ['feature/work'] }),
  listIncidentCases: vi.fn().mockResolvedValue([]),
  matchBugBots: vi.fn().mockResolvedValue([]),
  notifyIncidentDeployed: vi.fn(),
  openIncidentBrowserLogin: vi.fn(),
  prepareIncidentBrowserRuntime: vi.fn(),
  reconsiderIncidentRemediation: vi.fn(),
  repairIncidentBrowserRuntime: vi.fn(),
  resolveIncidentFrontendEntry: vi.fn().mockResolvedValue({ status: 'selected', selected: { id: 'default-web', name: '默认 Web 入口', url: 'https://app.test/', resolution_source: 'only_candidate' } }),
  resetIncidentCaseWithWarnings: vi.fn(),
  saveBugSelectedBot: vi.fn(),
  startIncidentCase: vi.fn(),
  uploadIncidentEvidenceImages: vi.fn(),
}))
vi.mock('../lib/toast', () => ({
  toast: { error: notifications.error, success: notifications.success, info: notifications.info },
  toastError: notifications.toastError,
}))

const bugA = {
  id: 'bug-a',
  source: 'zentao',
  source_id: '840',
  title: '支付页超时',
  status: 'active',
  severity: '3',
  priority: '2',
  created_at: '2026-07-08T11:12:00',
  updated_at: '2026-07-10T12:24:00',
  description: '支付接口返回超时',
  steps: '打开支付页',
  env: 'test',
  system_id: 'base',
  service_hints: ['checkout'],
}
const bugB = {
  id: 'bug-b', source: 'lark', source_id: 'TASK-17', title: '缓存命中下降', steps: '查看缓存指标', env: 'prod', system_id: 'base', service_hints: ['cache'],
}
const botMatch = {
  bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo/base', name: 'Base', env: 'test' }, score: 10, reasons: ['系统匹配'],
}
const replacementBotMatch = {
  bot: { key: 'base-prod|claude-code', system_id: 'base', target: 'claude-code', path: '/repo/base-prod', name: 'Base Prod', env: 'prod' }, score: 9, reasons: ['系统匹配'],
}

function incident(id: string, status: CaseStatus, updatedAt: string, overrides: Partial<IncidentCase> = {}): IncidentCase {
  return {
    id,
    bug_id: 'bug-a',
    source: 'zentao',
    system_id: 'base',
    environment: 'test',
    status,
    cycle_number: 1,
    current_attempt_id: 'attempt-1',
    selected_bot_key: 'base|codex',
    version: 7,
    created_at: '2026-07-10T00:00:00Z',
    updated_at: updatedAt,
    ...overrides,
  }
}

function detail(item: IncidentCase, overrides: Partial<IncidentCaseDetail> = {}): IncidentCaseDetail {
  return {
    case: item,
    attempts: [],
    artifacts: [],
    approvals: [],
    code_changes: [],
    deployment_observations: [],
    events: [],
    ...overrides,
  }
}

function mockCaseDetails(...snapshots: IncidentCaseDetail[]) {
  const byID = new Map(snapshots.map(snapshot => [snapshot.case.id, snapshot]))
  vi.mocked(getIncidentCase).mockImplementation(async caseID => {
    const snapshot = byID.get(caseID)
    if (!snapshot) throw new Error(`missing Case ${caseID}`)
    return snapshot
  })
}

function flushPromises() {
  return new Promise(resolve => setTimeout(resolve, 0))
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((done, fail) => {
    resolve = done
    reject = fail
  })
  return { promise, resolve, reject }
}

function stubIncidentEntry(reducedMotion = false) {
  const scrollIntoView = vi.fn()
  Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: scrollIntoView })
  vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: reducedMotion }))
  return scrollIntoView
}

async function mountedPage() {
  const wrapper = mount(IncidentWorkbenchPage)
  await flushPromises()
  await flushPromises()
  await flushPromises()
  return wrapper
}

afterEach(() => {
  if (originalScrollIntoView) Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: originalScrollIntoView })
  else delete (HTMLElement.prototype as Partial<HTMLElement>).scrollIntoView
  vi.unstubAllGlobals()
  route.path = '/incidents'
  route.query = {}
  router.replace.mockReset()
  router.push.mockReset()
  runtime.EventsOn.mockClear()
  vi.mocked(listBugs).mockReset().mockResolvedValue([])
  vi.mocked(listIncidentFixBranches).mockReset().mockResolvedValue({ 'admin-web': ['feature/new-navigation'], api: ['feature/work'] })
  vi.mocked(listIncidentCases).mockReset().mockResolvedValue([])
  vi.mocked(getIncidentCase).mockReset()
  vi.mocked(resolveIncidentFrontendEntry).mockReset().mockResolvedValue({ status: 'selected', selected: { id: 'default-web', name: '默认 Web 入口', url: 'https://app.test/', resolution_source: 'only_candidate' } })
  vi.mocked(getIncidentBrowserRuntimeStatus).mockReset().mockResolvedValue({ state: 'ready', version: '1.61.1', error_code: '', message: '' })
  vi.mocked(matchBugBots).mockReset().mockResolvedValue([botMatch])
  vi.mocked(saveBugSelectedBot).mockReset().mockResolvedValue(bugA as any)
  vi.mocked(startIncidentCase).mockReset()
  vi.mocked(uploadIncidentEvidenceImages).mockReset()
  vi.mocked(continueIncidentCase).mockReset()
  vi.mocked(approveIncidentFix).mockReset()
  vi.mocked(reconsiderIncidentRemediation).mockReset()
  vi.mocked(approveIncidentMerge).mockReset()
  vi.mocked(notifyIncidentDeployed).mockReset()
  vi.mocked(openIncidentBrowserLogin).mockReset()
  vi.mocked(prepareIncidentBrowserRuntime).mockReset().mockResolvedValue()
  vi.mocked(repairIncidentBrowserRuntime).mockReset()
  vi.mocked(clearIncidentBrowserSession).mockReset()
  vi.mocked(completeIncidentRemediation).mockReset()
  vi.mocked(resetIncidentCaseWithWarnings).mockReset()
  notifications.error.mockReset()
  notifications.success.mockReset()
  notifications.info.mockReset()
  notifications.toastError.mockReset()
})

describe('IncidentWorkbenchPage', () => {
  it('prepares Chromium outside the Case, reports download progress, and blocks only Web starts until ready', async () => {
    route.query = { bug_id: 'bug-a' }
    const webBug = { ...bugA, frontend_url: 'https://test.example.com/search' }
    vi.mocked(listBugs).mockResolvedValue([webBug])
    vi.mocked(matchBugBots).mockResolvedValue([botMatch])
    vi.mocked(getIncidentBrowserRuntimeStatus).mockResolvedValue({
      state: 'installing', version: '1.61.1', error_code: 'browser_runtime_install_in_progress', message: '',
    })

    const wrapper = await mountedPage()

    const start = wrapper.get<HTMLButtonElement>('[data-action="start-case"]')
    expect(start.element.disabled).toBe(true)
    expect(wrapper.get('[data-browser-runtime-summary]').text()).toContain('初始化验证浏览器基础工具')
    expect(wrapper.text()).toContain('完成后才能启动 Web 验证')

    const registration = runtime.EventsOn.mock.calls.find(call => call[0] === 'browser-runtime:status')
    expect(registration).toBeTruthy()
    registration?.[1]({
      status: { state: 'installing', version: '1.61.1', error_code: 'browser_runtime_install_in_progress' },
      code: 'browser_runtime_downloading', current: 40, total: 100,
    })
    await flushPromises()

    expect(wrapper.get('[data-browser-runtime-summary]').text()).toContain('40%')
    expect(wrapper.get('progress').attributes('value')).toBe('40')
    await start.trigger('click')
    expect(startIncidentCase).not.toHaveBeenCalled()
  })

  it('reports bundled Chromium import as a local first-launch step', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([{ ...bugA, frontend_url: 'https://test.example.com/search' }])
    vi.mocked(matchBugBots).mockResolvedValue([botMatch])
    vi.mocked(getIncidentBrowserRuntimeStatus).mockResolvedValue({
      state: 'installing', version: '1.61.1', error_code: '', message: '',
    })
    const wrapper = await mountedPage()
    const registration = runtime.EventsOn.mock.calls.find(call => call[0] === 'browser-runtime:status')
    registration?.[1]({
      status: { state: 'installing', version: '1.61.1' },
      code: 'browser_runtime_importing', current: 0, total: 0,
    })
    await flushPromises()
    expect(wrapper.get('[data-browser-runtime-summary]').text()).toContain('App 内置 Chromium')
    expect(wrapper.get('[data-browser-runtime-summary]').text()).toContain('无需联网下载')
  })

  it('retries a broken Studio browser runtime without creating a Case', async () => {
    vi.mocked(getIncidentBrowserRuntimeStatus)
      .mockResolvedValueOnce({ state: 'broken', version: '1.61.1', error_code: 'browser_runtime_install_failed', message: '' })
      .mockResolvedValue({ state: 'ready', version: '1.61.1', error_code: '', message: '' })

    const wrapper = await mountedPage()
    await wrapper.get('[data-action="prepare-browser-runtime"]').trigger('click')
    await flushPromises()
    await flushPromises()

    expect(prepareIncidentBrowserRuntime).toHaveBeenCalledTimes(1)
    expect(startIncidentCase).not.toHaveBeenCalled()
    expect(wrapper.get('[data-browser-runtime-summary]').text()).toContain('Web 验证可直接执行')
  })

  it('loads locally stored Bugs on mount without exposing a duplicate refresh action', async () => {
    vi.mocked(listBugs).mockResolvedValue([bugA])

    const wrapper = await mountedPage()

    expect(listBugs).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).not.toContain('刷新 Bug')
    expect(wrapper.get('.incident-header p').text()).toContain('Bug 工单')
  })

  it('separates current unresolved Bugs from history', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { ...bugA, inbox_state: 'active' },
      { ...bugB, inbox_state: 'history', status: 'resolved' },
    ] as any)

    const wrapper = await mountedPage()

    expect(wrapper.get('[data-ticket-view="active"]').attributes('aria-selected')).toBe('true')
    expect(wrapper.get('[data-ticket-view="active"]').text()).toContain('1')
    expect(wrapper.get('[data-ticket-view="history"]').text()).toContain('1')
    expect(wrapper.find('[data-ticket-id="bug-a"]').exists()).toBe(true)
    expect(wrapper.find('[data-ticket-id="bug-b"]').exists()).toBe(false)
    expect(wrapper.get('.ticket-list-panel').text()).toContain('当前未修复')

    await wrapper.get('[data-ticket-view="history"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-ticket-view="history"]').attributes('aria-selected')).toBe('true')
    expect(wrapper.find('[data-ticket-id="bug-a"]').exists()).toBe(false)
    expect(wrapper.get('[data-ticket-id="bug-b"]').text()).toContain('已解决')
    expect(router.replace).toHaveBeenCalledWith({ query: { bug_id: 'bug-b', view: 'history' } })
  })

  it('renders the selected Bug as a compact incident summary', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])

    const wrapper = await mountedPage()
    const summary = wrapper.get('.incident-bug-summary')

    expect(summary.get('.incident-bug-source').text()).toBe('禅道 #840')
    expect(summary.get('h2').text()).toBe('支付页超时')
    expect(summary.get('.incident-bug-grade').text()).toBe('S3 · P2')
    expect(summary.text()).toContain('2026-07-08 11:12')
    expect(summary.text()).toContain('2026-07-10 12:24')
    expect(summary.get('.incident-bug-status').text()).toBe('active')
    expect(summary.text()).not.toContain('base')
    expect(summary.text()).not.toContain('test')
    expect(summary.text()).not.toContain('checkout')
    expect(summary.text()).not.toContain('支付接口返回超时')
    expect(summary.text()).not.toContain('打开支付页')
  })

  it.each([375, 768, 1024, 1440])('keeps the selected Case and restart focus contained at %dpx', async width => {
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: width })
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-responsive', 'waiting_evidence', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(detail(item))
    const wrapper = mount(IncidentWorkbenchPage, { attachTo: document.body })
    await flushPromises()
    await flushPromises()
    await flushPromises()
    const root = wrapper.get('.incident-workbench-page')
    const workspace = wrapper.get('.selection-workspace')

    expect(root.attributes('data-responsive-viewports')).toBe('375,768,1024,1440')
    expect(root.attributes('data-overflow-safe')).toBe('true')
    expect(workspace.attributes('data-overflow-safe')).toBe('true')
    expect(wrapper.findAll('.selection-panel').every(panel => panel.attributes('data-overflow-safe') === 'true')).toBe(true)

    const trigger = wrapper.get<HTMLButtonElement>('[data-action="restart-case"]')
    trigger.element.focus()
    await trigger.trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    const cancel = wrapper.get<HTMLButtonElement>('[data-reset-cancel]')
    const confirm = wrapper.get<HTMLButtonElement>('[data-reset-confirm]')
    expect(dialog.attributes('data-overflow-safe')).toBe('true')
    expect(document.activeElement).toBe(cancel.element)

    await cancel.trigger('keydown', { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(confirm.element)
    await confirm.trigger('keydown', { key: 'Tab' })
    expect(document.activeElement).toBe(cancel.element)

    wrapper.unmount()
  })

  it('restores the exact bug_id from the route without guessing', async () => {
    route.query = { bug_id: 'bug-b' }
    vi.mocked(listBugs).mockResolvedValue([bugA, bugB])

    const wrapper = await mountedPage()

    expect(wrapper.get('[data-ticket-id="bug-b"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.get('.incident-bug-summary h2').text()).toBe('缓存命中下降')
    expect(wrapper.get('[data-ticket-id="bug-a"]').attributes('aria-pressed')).toBe('false')
  })

  it('updates the query when another Bug is selected', async () => {
    route.query = { bug_id: 'bug-a', view: 'audit' }
    vi.mocked(listBugs).mockResolvedValue([bugA, bugB])
    const wrapper = await mountedPage()

    await wrapper.get('[data-ticket-id="bug-b"]').trigger('click')
    await flushPromises()

    expect(router.replace).toHaveBeenCalledWith({ query: { bug_id: 'bug-b', view: 'audit' } })
    expect(wrapper.get('.incident-bug-summary h2').text()).toBe('缓存命中下降')
  })

  it('switches the displayed workflow directly when another Bug is selected', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA, bugB])
    const caseA = incident('case-a', 'waiting_evidence', '2026-07-13T00:00:00Z')
    const caseB = incident('case-b', 'waiting_fix_approval', '2026-07-13T00:01:00Z', { bug_id: 'bug-b' })
    vi.mocked(listIncidentCases).mockResolvedValue([caseA, caseB])
    mockCaseDetails(detail(caseA), detail(caseB))
    const wrapper = await mountedPage()

    await wrapper.get('[data-ticket-id="bug-b"]').trigger('click')
    await flushPromises()
    await flushPromises()

    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe(caseB.id)
    expect(wrapper.get('.case-heading').text()).toContain(bugB.title)
    expect(wrapper.get('.case-heading').text()).not.toContain(caseB.id)
    expect(wrapper.find('[data-action="enter-case"]').exists()).toBe(false)
    expect(wrapper.get('[data-action="restart-case"]').text()).toContain('重新开始故障闭环')
  })

  it('opens the newest active Case without calling Start', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const oldActive = incident('case-active-old', 'waiting_evidence', '2026-07-11T00:00:00Z')
    const newActive = incident('case-active-new', 'waiting_fix_approval', '2026-07-12T00:00:00Z', { current_attempt_id: 'root-1' })
    const terminal = incident('case-terminal', 'fixed_verified', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([terminal, oldActive, newActive])
    mockCaseDetails(detail(terminal), detail(oldActive), detail(newActive))

    const wrapper = await mountedPage()

    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe('case-active-new')
    expect(wrapper.get('.case-heading').text()).toContain(bugA.title)
    expect(wrapper.get('.case-heading').text()).not.toContain('case-active-new')
    expect(wrapper.text()).toContain('允许修复')
    expect(startIncidentCase).not.toHaveBeenCalled()
    expect(wrapper.find('[data-action="start-case"]').exists()).toBe(false)
    expect(wrapper.find('.case-list-column').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('case-terminal')
    expect(wrapper.text()).not.toContain('case-active-old')
  })

  it('shows only open in the Bot action panel for a Bug with no Case', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])

    const wrapper = await mountedPage()

    const panel = wrapper.get('.bot-action-panel')
    const start = panel.get('[data-action="start-case"]')
    expect(start.text()).toContain('开启故障闭环')
    expect(start.attributes('disabled')).toBeUndefined()
    expect(panel.find('[data-action="enter-case"]').exists()).toBe(false)
    expect(panel.find('[data-action="restart-case"]').exists()).toBe(false)
    expect(wrapper.find('.start-card').exists()).toBe(false)
  })

  it('shows the active workflow directly and keeps only restart in the Bot action panel', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-waiting_evidence', 'waiting_evidence', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(detail(item))

    const wrapper = await mountedPage()
    const panel = wrapper.get('.bot-action-panel')

    expect(panel.get('.bot-action-status').text()).toBe('故障闭环进行中')
    expect(panel.find('[data-action="enter-case"]').exists()).toBe(false)
    expect(panel.get('[data-action="restart-case"]').text()).toContain('重新开始故障闭环')
    expect(panel.find('[data-action="start-case"]').exists()).toBe(false)
    expect(wrapper.find('.lifecycle-region').exists()).toBe(true)
    expect(wrapper.get('.case-heading').text()).toContain(bugA.title)
    expect(wrapper.get('.case-heading').text()).not.toContain(item.id)
  })

  it.each(['fixed_verified', 'legacy_archived', 'reset_archived'] as const)('hides a %s Case and offers only a fresh start', async status => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const terminal = incident(`case-${status}`, status, '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([terminal])
    mockCaseDetails(detail(terminal))

    const wrapper = await mountedPage()
    const panel = wrapper.get('.bot-action-panel')

    expect(panel.get('.bot-action-status').text()).toBe('尚未开启故障闭环')
    expect(panel.get('[data-action="start-case"]').text()).toContain('开启故障闭环')
    expect(panel.find('[data-action="enter-case"]').exists()).toBe(false)
    expect(panel.find('[data-action="restart-case"]').exists()).toBe(false)
    expect(wrapper.find('.lifecycle-region').exists()).toBe(false)
    expect(wrapper.find('.case-heading').exists()).toBe(false)
    expect(wrapper.text()).not.toContain(terminal.id)
  })

  it('shows the latest terminal Case when opened from Bug history', async () => {
    route.query = { bug_id: 'bug-a', view: 'history' }
    vi.mocked(listBugs).mockResolvedValue([{ ...bugA, inbox_state: 'history', status: 'resolved' }])
    const terminal = incident('case-history-fixed', 'fixed_verified', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([terminal])
    mockCaseDetails(detail(terminal, { bug_ticket_resolution: { state: 'resolved', source_status: 'resolved' } }))

    const wrapper = await mountedPage()

    expect(wrapper.get('.bot-action-status').text()).toBe('历史故障闭环')
    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe(terminal.id)
    expect(wrapper.get('.workflow-loop-hint').text()).toContain('Bug 工单已转为已解决')
    expect(wrapper.get('[data-action="restart-case"]').text()).toContain('重新开始故障闭环')
  })

  it('hides the lifecycle immediately when the active Case becomes terminal', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const active = incident('case-live-terminal', 'waiting_evidence', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([active])
    mockCaseDetails(detail(active))
    const wrapper = await mountedPage()

    expect(wrapper.find('.lifecycle-region').exists()).toBe(true)
    const terminal = { ...active, status: 'fixed_verified' as const, version: active.version + 1, updated_at: '2026-07-13T00:01:00Z' }
    const eventHandler = runtime.EventsOn.mock.calls.find(call => call[0] === 'incident-case:event')?.[1]
    expect(eventHandler).toBeTypeOf('function')
    eventHandler?.({ kind: 'snapshot', case: terminal, snapshot: detail(terminal) })
    await flushPromises()

    expect(wrapper.find('.lifecycle-region').exists()).toBe(false)
    expect(wrapper.get('.bot-action-status').text()).toBe('尚未开启故障闭环')
    expect(wrapper.find('[data-action="start-case"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain(terminal.id)
  })

  it('automatically closes restart confirmation when the active Case becomes terminal', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    vi.mocked(matchBugBots).mockResolvedValue([botMatch])
    const active = incident('case-watch-restart-terminal', 'waiting_evidence', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([active])
    mockCaseDetails(detail(active))
    const wrapper = await mountedPage()

    await wrapper.get('[data-action="restart-case"]').trigger('click')
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)
    const terminal = { ...active, status: 'fixed_verified' as const, version: active.version + 1, updated_at: '2026-07-13T00:01:00Z' }
    const eventHandler = runtime.EventsOn.mock.calls.find(call => call[0] === 'incident-case:event')?.[1]
    expect(eventHandler).toBeTypeOf('function')

    eventHandler?.({ kind: 'snapshot', case: terminal, snapshot: detail(terminal) })
    await flushPromises()

    expect(wrapper.find('.lifecycle-region').exists()).toBe(false)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.get('.bot-action-status').text()).toBe('尚未开启故障闭环')
    expect(wrapper.find('[data-action="start-case"]').exists()).toBe(true)
    expect(wrapper.find('[data-action="enter-case"]').exists()).toBe(false)
    expect(wrapper.find('[data-action="restart-case"]').exists()).toBe(false)
    expect(resetIncidentCaseWithWarnings).not.toHaveBeenCalled()
    expect(startIncidentCase).not.toHaveBeenCalled()
  })

  it('rejects a captured restart confirmation after the active Case becomes terminal', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    vi.mocked(matchBugBots).mockResolvedValue([botMatch])
    const active = incident('case-guard-restart-terminal', 'waiting_evidence', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([active])
    mockCaseDetails(detail(active))
    const wrapper = await mountedPage()

    await wrapper.get('[data-action="restart-case"]').trigger('click')
    const staleConfirm = wrapper.get<HTMLButtonElement>('[data-reset-confirm]')
    const terminal = { ...active, status: 'fixed_verified' as const, version: active.version + 1, updated_at: '2026-07-13T00:01:00Z' }
    const eventHandler = runtime.EventsOn.mock.calls.find(call => call[0] === 'incident-case:event')?.[1]
    expect(eventHandler).toBeTypeOf('function')

    eventHandler?.({ kind: 'snapshot', case: terminal, snapshot: detail(terminal) })
    staleConfirm.element.click()

    expect(resetIncidentCaseWithWarnings).not.toHaveBeenCalled()
    expect(startIncidentCase).not.toHaveBeenCalled()
    await flushPromises()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
  })

  it('invalidates a submitted reset after its active Case becomes terminal and the request fails', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    vi.mocked(matchBugBots).mockResolvedValue([botMatch])
    const active = incident('case-pending-reset-terminal', 'waiting_evidence', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([active])
    mockCaseDetails(detail(active))
    const pendingReset = deferred<{ case: IncidentCase; warnings: [] }>()
    vi.mocked(resetIncidentCaseWithWarnings).mockReturnValue(pendingReset.promise)
    const wrapper = await mountedPage()

    await wrapper.get('[data-action="restart-case"]').trigger('click')
    const submittedConfirm = wrapper.get<HTMLButtonElement>('[data-reset-confirm]')
    await submittedConfirm.trigger('click')
    expect(resetIncidentCaseWithWarnings).toHaveBeenCalledTimes(1)

    const terminal = { ...active, status: 'fixed_verified' as const, version: active.version + 1, updated_at: '2026-07-13T00:01:00Z' }
    const eventHandler = runtime.EventsOn.mock.calls.find(call => call[0] === 'incident-case:event')?.[1]
    expect(eventHandler).toBeTypeOf('function')
    eventHandler?.({ kind: 'snapshot', case: terminal, snapshot: detail(terminal) })
    await flushPromises()

    pendingReset.reject(new Error('reset bridge unavailable'))
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('.lifecycle-region').exists()).toBe(false)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.get('.bot-action-status').text()).toBe('尚未开启故障闭环')
    expect(wrapper.find('[data-action="start-case"]').exists()).toBe(true)
    expect(wrapper.find('[data-action="enter-case"]').exists()).toBe(false)
    expect(wrapper.find('[data-action="restart-case"]').exists()).toBe(false)

    submittedConfirm.element.click()
    await flushPromises()
    expect(resetIncidentCaseWithWarnings).toHaveBeenCalledTimes(1)
    expect(startIncidentCase).not.toHaveBeenCalled()
  })

  it('starts directly when only terminal records exist', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const terminal = incident('case-old-terminal', 'fixed_verified', '2026-07-13T00:00:00Z')
    const opened = incident('case-new-active', 'validating', '2026-07-13T00:01:00Z', { version: 1 })
    vi.mocked(listIncidentCases).mockResolvedValue([terminal])
    vi.mocked(startIncidentCase).mockResolvedValue(opened)
    mockCaseDetails(detail(terminal), detail(opened))
    const wrapper = await mountedPage()

    await wrapper.get('[data-action="start-case"]').trigger('click')
    await flushPromises()
    await flushPromises()

    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({
      bug_id: 'bug-a', expected_version: 0, bot_key: 'base|codex',
    }))
    expect(resetIncidentCaseWithWarnings).not.toHaveBeenCalled()
    expect(wrapper.find('[data-reset-confirm]').exists()).toBe(false)
    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe(opened.id)
  })

  it('retries an initial active Case detail failure without revealing terminal history', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const active = incident('case-retry-active', 'waiting_evidence', '2026-07-13T00:00:00Z')
    const terminal = incident('case-hidden-terminal', 'fixed_verified', '2026-07-12T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([active, terminal])
    vi.mocked(getIncidentCase).mockRejectedValue(new Error('active detail unavailable'))
    const wrapper = await mountedPage()

    expect(wrapper.get('.case-loading').text()).toContain('active detail unavailable')
    expect(wrapper.text()).not.toContain(terminal.id)
    vi.mocked(getIncidentCase).mockResolvedValue(detail(active))
    await wrapper.get('[data-action="retry-active-case"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe(active.id)
    expect(wrapper.find('[data-action="retry-active-case"]').exists()).toBe(false)
  })

  it('refreshes the displayed active Case from its heading', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const active = incident('case-refresh-active', 'waiting_evidence', '2026-07-13T00:00:00Z')
    const refreshed = { ...active, status: 'waiting_fix_approval' as const, version: active.version + 1, current_attempt_id: 'root-refresh' }
    vi.mocked(listIncidentCases).mockResolvedValue([active])
    mockCaseDetails(detail(active))
    const wrapper = await mountedPage()

    vi.mocked(getIncidentCase).mockClear()
    vi.mocked(getIncidentCase).mockResolvedValue(detail(refreshed))
    await wrapper.get('[aria-label="刷新故障闭环"]').trigger('click')
    await flushPromises()

    expect(getIncidentCase).toHaveBeenCalledWith(active.id)
    expect(wrapper.get('.current-action-card').text()).toContain('允许修复')
  })

  it('keeps Bot action primary buttons readable across interaction states', () => {
    const source = readFileSync('src/pages/IncidentWorkbenchPage.vue', 'utf8')

    expect(source).toContain('.bot-action-controls .btn { flex: 1 1 160px; min-height: 44px; transition: background-color 180ms ease, border-color 180ms ease, color 180ms ease; }')
    expect(source).toContain('.btn.primary { border-color: #2563eb; background: #2563eb; color: #fff; }')
    expect(source).toContain('.bot-action-controls .btn.primary:hover:not(:disabled) { border-color: #1d4ed8; background: #1d4ed8; color: #fff; }')
    expect(source).toContain('.bot-action-controls .btn.primary:focus-visible { border-color: #2563eb; background: #2563eb; color: #fff; outline: 2px solid #1e40af; outline-offset: 2px; }')
    expect(source).toContain('.bot-action-controls .btn.primary:disabled { opacity: 1; border-color: #cbd5e1; background: #e2e8f0; color: #475569; cursor: not-allowed; }')
  })

  it.each([
    ['no selected Bot', [], '请选择排障机器人'],
    ['unsupported target', [{ bot: { ...botMatch.bot, key: 'base|cursor', target: 'cursor' }, score: 8, reasons: [] }], '暂不支持由 Studio 后台启动'],
    ['empty environment', [{ bot: { ...botMatch.bot, env: '' }, score: 8, reasons: [] }], '缺少目标环境'],
  ] as const)('keeps the active workflow visible but disables restart for %s', async (_label, availableMatches, reason) => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    vi.mocked(matchBugBots).mockResolvedValue(availableMatches as any)
    const item = incident('case-disabled-restart', 'waiting_evidence', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(detail(item))

    const wrapper = await mountedPage()

    expect(wrapper.find('[data-action="enter-case"]').exists()).toBe(false)
    expect(wrapper.find('.lifecycle-region').exists()).toBe(true)
    expect(wrapper.get<HTMLButtonElement>('[data-action="restart-case"]').element.disabled).toBe(true)
    expect(wrapper.get('.bot-action-disabled-reason').text()).toContain(reason)
  })

  it.each([
    ['no selected Bot', [], '请选择排障机器人'],
    ['unsupported target', [{ bot: { ...botMatch.bot, key: 'base|cursor', target: 'cursor' }, score: 8, reasons: [] }], '暂不支持由 Studio 后台启动'],
    ['empty environment', [{ bot: { ...botMatch.bot, env: '' }, score: 8, reasons: [] }], '缺少目标环境'],
  ] as const)('disables open for a no-Case Bug with %s', async (_label, availableMatches, reason) => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    vi.mocked(matchBugBots).mockResolvedValue(availableMatches as any)

    const wrapper = await mountedPage()

    expect(wrapper.get<HTMLButtonElement>('[data-action="start-case"]').element.disabled).toBe(true)
    expect(wrapper.get('.bot-action-disabled-reason').text()).toContain(reason)
  })

  it('disables open while Bot matching is pending', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const pendingMatches = deferred<(typeof botMatch)[]>()
    vi.mocked(matchBugBots).mockReturnValue(pendingMatches.promise)

    const wrapper = await mountedPage()

    expect(wrapper.get<HTMLButtonElement>('[data-action="start-case"]').element.disabled).toBe(true)
    expect(wrapper.get('.bot-action-disabled-reason').text()).toContain('正在匹配')

    pendingMatches.resolve([botMatch])
    await flushPromises()
  })

  it('requires an explicit frontend choice when ticket evidence is ambiguous and freezes it into Start', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([{ ...bugA, frontend_url: 'https://portal.test/search' }])
    vi.mocked(resolveIncidentFrontendEntry).mockResolvedValue({
      status: 'ambiguous',
      message: '工单证据无法唯一确定前端入口，请选择本次验证对应的应用',
      candidates: [
        { binding: { id: 'consumer', name: 'C 端 H5', url: 'https://m.test/', resolution_source: '' }, score: 0, reasons: [] },
        { binding: { id: 'admin', name: '管理端', url: 'https://admin.test/', resolution_source: '' }, score: 0, reasons: [] },
      ],
    })
    const opened = incident('case-admin-frontend', 'validating', '2026-07-13T00:01:00Z', {
      version: 1,
      frontend_entry: { id: 'admin', name: '管理端', url: 'https://admin.test/', resolution_source: 'user' },
    })
    vi.mocked(startIncidentCase).mockResolvedValue(opened)
    mockCaseDetails(detail(opened))

    const wrapper = await mountedPage()

    const start = wrapper.get<HTMLButtonElement>('[data-action="start-case"]')
    expect(start.element.disabled).toBe(true)
    expect(wrapper.get('.frontend-entry-resolution').text()).toContain('请选择本次验证对应的应用')

    const admin = wrapper.findAll<HTMLInputElement>('.frontend-entry-option input').find(input => input.element.value === 'admin')
    expect(admin).toBeTruthy()
    await admin!.setValue(true)
    expect(start.element.disabled).toBe(false)

    await start.trigger('click')
    await flushPromises()

    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({ frontend_entry_id: 'admin' }))
  })

  it('clears Start pending before scrolling and focusing the opened Case', async () => {
    const scrollIntoView = stubIncidentEntry()
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const pendingStart = deferred<IncidentCase>()
    const opened = incident('case-start-focused', 'waiting_evidence', '2026-07-13T00:01:00Z', { version: 1 })
    vi.mocked(startIncidentCase).mockReturnValue(pendingStart.promise)
    mockCaseDetails(detail(opened))
    const wrapper = mount(IncidentWorkbenchPage, { attachTo: document.body })
    await flushPromises()
    await flushPromises()
    await flushPromises()

    await wrapper.get('[data-action="start-case"]').trigger('click')
    expect(wrapper.get<HTMLButtonElement>('[data-action="start-case"]').element.disabled).toBe(true)
    expect(wrapper.get('.bot-action-disabled-reason').text()).toContain('处理中')
    expect(scrollIntoView).not.toHaveBeenCalled()

    pendingStart.resolve(opened)
    await flushPromises()
    await flushPromises()
    await flushPromises()

    expect(scrollIntoView).toHaveBeenLastCalledWith({ behavior: 'smooth', block: 'start' })
    const primary = wrapper.get<HTMLButtonElement>('.primary-action')
    expect(primary.element.disabled).toBe(false)
    expect(document.activeElement).toBe(primary.element)
    wrapper.unmount()
  })

  it('loads and displays an active workflow without a separate entry action', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-loading-active', 'waiting_evidence', '2026-07-13T00:00:00Z')
    const pendingDetail = deferred<IncidentCaseDetail>()
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    vi.mocked(getIncidentCase).mockReturnValue(pendingDetail.promise)
    const wrapper = mount(IncidentWorkbenchPage)
    await flushPromises()
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('.case-loading').exists()).toBe(true)
    expect(wrapper.find('[data-action="enter-case"]').exists()).toBe(false)
    expect(wrapper.find('.primary-action').exists()).toBe(false)

    pendingDetail.resolve(detail(item))
    await flushPromises()
    await flushPromises()
    await flushPromises()

    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe(item.id)
    expect(wrapper.get('.case-heading').text()).toContain(bugA.title)
    expect(wrapper.find('[data-action="enter-case"]').exists()).toBe(false)
    expect(wrapper.find('.primary-action').exists()).toBe(true)
  })

  it('uses an existing Case returned by the backend', async () => {
    const scrollIntoView = stubIncidentEntry()
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const existing = incident('case-existing', 'validating', '2026-07-13T00:00:00Z', { version: 4 })
    vi.mocked(startIncidentCase).mockResolvedValue(existing)
    mockCaseDetails(detail(existing))
    const wrapper = mount(IncidentWorkbenchPage, { attachTo: document.body })
    await flushPromises()
    await flushPromises()
    await flushPromises()

    await wrapper.get('[data-action="start-case"]').trigger('click')
    await flushPromises()
    await flushPromises()

    const input = vi.mocked(startIncidentCase).mock.calls[0][0]
    expect(input.case_id).not.toBe('case-existing')
    expect(input).toMatchObject({ bug_id: 'bug-a', bot_key: 'base|codex', expected_version: 0, actor_id: 'desktop-user' })
    expect(getIncidentCase).toHaveBeenCalledWith('case-existing')
    expect(wrapper.text()).toContain('已打开现有闭环')
    expect(scrollIntoView).toHaveBeenLastCalledWith({ behavior: 'smooth', block: 'start' })
    expect(wrapper.get<HTMLButtonElement>('.primary-action').element.disabled).toBe(false)
    expect(document.activeElement).toBe(wrapper.get('.primary-action').element)
    wrapper.unmount()
  })

  it('does not apply a delayed Start completion after another Bug is selected', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA, bugB])
    const pendingStart = deferred<IncidentCase>()
    vi.mocked(startIncidentCase).mockReturnValue(pendingStart.promise)
    const staleOpened = incident('case-a-new', 'validating', '2026-07-13T00:01:00Z', { version: 1 })
    mockCaseDetails(detail(staleOpened))
    const wrapper = await mountedPage()

    await wrapper.get('[data-action="start-case"]').trigger('click')
    await wrapper.get('[data-ticket-id="bug-b"]').trigger('click')
    await flushPromises()
    vi.mocked(getIncidentCase).mockClear()
    notifications.info.mockClear()
    notifications.success.mockClear()

    pendingStart.resolve(staleOpened)
    await flushPromises()
    await flushPromises()

    expect(wrapper.get('.incident-bug-summary h2').text()).toBe('缓存命中下降')
    expect(getIncidentCase).not.toHaveBeenCalled()
    expect(wrapper.text()).not.toContain('已打开现有闭环')
    expect(notifications.info).not.toHaveBeenCalled()
    expect(notifications.success).not.toHaveBeenCalled()
  })

  it('shows a recoverable empty state for an invalid URL Bug', async () => {
    route.query = { bug_id: 'missing-bug' }
    vi.mocked(listBugs).mockResolvedValue([bugA])

    const wrapper = await mountedPage()

    expect(wrapper.text()).toContain('URL 中的 Bug 不存在')
    expect(wrapper.get('[data-ticket-id="bug-a"]').attributes('aria-pressed')).toBe('false')
    expect(matchBugBots).not.toHaveBeenCalled()

    await wrapper.get('[data-ticket-id="bug-a"]').trigger('click')
    await flushPromises()
    expect(router.replace).toHaveBeenCalledWith({ query: { bug_id: 'bug-a' } })
    expect(wrapper.get('.incident-bug-summary h2').text()).toBe('支付页超时')
  })

  it('submits fix approval with the dialog-captured root cause and exact Case version key', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-1', 'waiting_fix_approval', '2026-07-13T00:00:00Z')
    const snapshot = detail(item, {
      attempts: [{ id: 'attempt-1', case_id: 'case-1', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { investigation_status: 'root_cause_ready', call_chain: [{ repo: 'admin-web' }] }, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(snapshot)
    vi.mocked(approveIncidentFix).mockResolvedValue({ ...item, status: 'fixing', version: 8 })
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    await wrapper.get('#fix-baseline-0').setValue('feature/new-navigation')
    await wrapper.get('[data-confirm]').trigger('click')
    await flushPromises()

    expect(approveIncidentFix).toHaveBeenCalledWith(expect.objectContaining({
      case_id: 'case-1', expected_version: 7, root_cause_attempt_id: 'attempt-1', idempotency_key: 'start-fix:case-1:attempt-1:7', actor_id: 'desktop-user',
      input_json: { source_baselines: { 'admin-web': 'feature/new-navigation' } },
    }))
  })

  it('submits an alternative remediation for investigation reassessment without fix approval', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-1', 'waiting_fix_approval', '2026-07-13T00:00:00Z')
    const snapshot = detail(item, {
      attempts: [{ id: 'attempt-1', case_id: 'case-1', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { investigation_status: 'root_cause_ready', root_cause: '字段语义错配', call_chain: [{ repo: 'admin-web' }] }, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(snapshot)
    vi.mocked(reconsiderIncidentRemediation).mockResolvedValue({ ...item, status: 'investigating', current_attempt_id: 'reassessment-1', version: 8 })
    const wrapper = await mountedPage()

    await wrapper.get('.reconsider-action').trigger('click')
    await wrapper.get('#remediation-proposal').setValue('优先由后端统一字段语义，前端只保留兼容兜底')
    await wrapper.get('[data-confirm]').trigger('click')
    await flushPromises()

    expect(reconsiderIncidentRemediation).toHaveBeenCalledWith({
      case_id: 'case-1', expected_version: 7, root_cause_attempt_id: 'attempt-1',
      idempotency_key: 'reconsider-remediation:case-1:attempt-1:7', actor_id: 'desktop-user',
      proposal: '优先由后端统一字段语义，前端只保留兼容兜底',
    })
    expect(approveIncidentFix).not.toHaveBeenCalled()
  })

  it('submits a non-code remediation confirmation and immediately enters regression', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-remediation', 'waiting_remediation', '2026-07-13T00:00:00Z')
    const snapshot = detail(item, {
      attempts: [{ id: 'attempt-1', case_id: item.id, cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { investigation_status: 'root_cause_ready', root_cause_type: 'infrastructure', remediation: { mode: 'operator_action', target: 'K8s test/base-api', summary: 'restart unhealthy workload', rollback: 'restore replica set', verification: 'rerun original scenario' } }, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(snapshot)
    vi.mocked(completeIncidentRemediation).mockResolvedValue({ ...item, status: 'regression_validating', version: 9 })
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    await wrapper.get('#remediation-summary').setValue('已重建异常 Pod，副本全部 Ready')
    await wrapper.get('#remediation-evidence').setValue('K8s event UID 42')
    await wrapper.get('[data-confirm]').trigger('click')
    await flushPromises()

    expect(completeIncidentRemediation).toHaveBeenCalledWith({
      case_id: item.id, expected_version: 7, idempotency_key: `complete-remediation:${item.id}:attempt-1:7`, actor_id: 'desktop-user',
      root_cause_attempt_id: 'attempt-1', summary: '已重建异常 Pod，副本全部 Ready', evidence: 'K8s event UID 42',
    })
  })

  it('snapshots the old and selected replacement bindings before confirming reset', async () => {
    const scrollIntoView = stubIncidentEntry()
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    vi.mocked(matchBugBots).mockResolvedValue([botMatch, replacementBotMatch])
    const item = incident('case-1', 'waiting_evidence', '2026-07-13T00:00:00Z')
    const archived = { ...item, status: 'reset_archived' as const, version: 8, superseded_by_case_id: 'case-reset-replacement' }
    const replacement = incident('case-reset-replacement', 'pending_validation', '2026-07-13T00:01:00Z', {
      version: 1,
      current_attempt_id: '',
      reset_from_case_id: 'case-1',
      selected_bot_key: 'base-prod|claude-code',
      environment: 'prod',
    })
    vi.mocked(listIncidentCases).mockResolvedValueOnce([item]).mockResolvedValueOnce([archived, replacement])
    mockCaseDetails(detail(item), detail(replacement))
    vi.mocked(resetIncidentCaseWithWarnings).mockResolvedValue({ case: replacement, warnings: [] })
    const wrapper = mount(IncidentWorkbenchPage, { attachTo: document.body })
    await flushPromises()
    await flushPromises()
    await flushPromises()

    await wrapper.findAll('.bot-picker input[type="radio"]')[1].setValue(true)
    await wrapper.get('[data-action="restart-case"]').trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    expect(dialog.attributes('aria-modal')).toBe('true')
    expect(dialog.attributes('aria-labelledby')).toBeTruthy()
    expect(dialog.attributes('aria-describedby')).toBeTruthy()
    expect(dialog.text()).toContain('已发生的提交、推送或部署不会自动撤销')
    expect(dialog.text()).toContain('已有证据和审计记录会继续保留')
    expect(dialog.text()).toContain('开始阶段验证')
    expect(dialog.text()).toContain('排障机器人Base Prod · Claude Code')
    expect(dialog.text()).toContain('prod')
    expect(dialog.text()).not.toContain('base|codex')
    expect(dialog.text()).not.toContain('base-prod|claude-code')
    expect(dialog.text()).not.toContain('/repo/')
    await wrapper.findAll('.bot-picker input[type="radio"]')[0].setValue(true)
    await dialog.get('[data-reset-confirm]').trigger('click')
    await flushPromises()
    await flushPromises()

    expect(resetIncidentCaseWithWarnings).toHaveBeenCalledWith(expect.objectContaining({
      case_id: 'case-1',
      new_case_id: expect.stringMatching(/^case-reset-/),
      expected_version: 7,
      idempotency_key: expect.stringMatching(/^reset:case-1:v7:/),
      actor_id: 'desktop-user',
      bot_key: 'base-prod|claude-code',
      bot_environment: 'prod',
      input_json: expect.objectContaining({ target_environment: 'prod' }),
    }))
    const generatedReplacementID = vi.mocked(resetIncidentCaseWithWarnings).mock.calls[0][0].new_case_id
    expect(generatedReplacementID.length).toBeLessThanOrEqual(64)
    expect(generatedReplacementID).not.toContain(item.id)
    expect(startIncidentCase).not.toHaveBeenCalled()
    expect(continueIncidentCase).not.toHaveBeenCalled()
    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe('case-reset-replacement')
    expect(getIncidentCase).toHaveBeenLastCalledWith('case-reset-replacement')
    expect(scrollIntoView).toHaveBeenLastCalledWith({ behavior: 'smooth', block: 'start' })
    const primary = wrapper.get<HTMLButtonElement>('.primary-action')
    expect(primary.element.disabled).toBe(false)
    expect(document.activeElement).toBe(primary.element)
    wrapper.unmount()
  })

  it('keeps internal Case, attempt, status and binding details out of the reset confirmation', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-reset-context', 'waiting_evidence', '2026-07-13T00:00:00Z', { current_attempt_id: 'attempt-investigation' })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(detail(item, {
      attempts: [{ id: 'attempt-investigation', case_id: item.id, cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }],
    }))
    const wrapper = await mountedPage()

    await wrapper.get('[data-action="restart-case"]').trigger('click')
    const dialog = wrapper.get('[role="dialog"]')

    expect(dialog.text()).toContain('重新开始故障闭环')
    expect(dialog.text()).toContain('将停止当前 Agent')
    expect(dialog.text()).toContain('排障机器人Base · Codex')
    expect(dialog.text()).toContain('目标环境test')
    expect(dialog.text()).not.toContain('waiting_evidence')
    expect(dialog.text()).not.toContain('investigation')
    expect(dialog.text()).not.toContain('attempt-investigation')
    expect(dialog.text()).not.toContain('base|codex')
    expect(dialog.text()).not.toContain('bug-a')
  })

  it('refreshes after a v8 realtime snapshot arrives before the v7 reset conflict', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const stale = incident('case-reset-realtime-conflict', 'waiting_evidence', '2026-07-13T00:00:00Z', { version: 7 })
    const fresh = incident('case-reset-realtime-conflict', 'waiting_evidence', '2026-07-13T00:01:00Z', { version: 8 })
    vi.mocked(listIncidentCases).mockResolvedValueOnce([stale]).mockResolvedValue([fresh])
    vi.mocked(getIncidentCase).mockResolvedValueOnce(detail(stale)).mockResolvedValue(detail(fresh))
    const pendingReset = deferred<{ case: IncidentCase; warnings: [] }>()
    vi.mocked(resetIncidentCaseWithWarnings).mockReturnValue(pendingReset.promise)
    const wrapper = await mountedPage()

    await wrapper.get('[data-action="restart-case"]').trigger('click')
    await wrapper.get('[data-reset-confirm]').trigger('click')
    const eventHandler = runtime.EventsOn.mock.calls.find(call => call[0] === 'incident-case:event')?.[1]
    eventHandler?.({ kind: 'snapshot', case: fresh, snapshot: detail(fresh) })
    await flushPromises()
    pendingReset.reject(new IncidentWorkflowCommandError('case_version_conflict', 'Case 已更新'))
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(listIncidentCases).toHaveBeenCalledTimes(2)
    expect(getIncidentCase).toHaveBeenLastCalledWith(fresh.id)
    expect(notifications.info).toHaveBeenCalledWith(expect.stringContaining('已刷新'))
  })

  it('reports the actual refresh failure after a reset conflict instead of claiming refresh succeeded', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const stale = incident('case-reset-refresh-failure', 'waiting_evidence', '2026-07-13T00:00:00Z', { version: 7 })
    vi.mocked(listIncidentCases).mockResolvedValueOnce([stale]).mockRejectedValueOnce(new Error('Case 列表暂时不可用'))
    vi.mocked(getIncidentCase).mockResolvedValue(detail(stale))
    vi.mocked(resetIncidentCaseWithWarnings).mockRejectedValue(new IncidentWorkflowCommandError('idempotency_conflict', '请求身份冲突'))
    const wrapper = await mountedPage()

    await wrapper.get('[data-action="restart-case"]').trigger('click')
    await wrapper.get('[data-reset-confirm]').trigger('click')
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.get('.live-error').text()).toContain('Case 列表暂时不可用')
    expect(wrapper.get('.live-error').text()).toContain('请手动刷新')
    expect(notifications.info).not.toHaveBeenCalledWith(expect.stringContaining('已刷新'))
  })

  it('refreshes the Case and discards the stale reset identity after a version conflict', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const stale = incident('case-reset-conflict', 'waiting_evidence', '2026-07-13T00:00:00Z', { version: 7 })
    const fresh = incident('case-reset-conflict', 'waiting_evidence', '2026-07-13T00:01:00Z', { version: 8 })
    vi.mocked(listIncidentCases).mockResolvedValueOnce([stale]).mockResolvedValue([fresh])
    vi.mocked(getIncidentCase).mockResolvedValueOnce(detail(stale)).mockResolvedValue(detail(fresh))
    vi.mocked(resetIncidentCaseWithWarnings).mockRejectedValue(new IncidentWorkflowCommandError('case_version_conflict', 'Case 已被其他操作更新'))
    const wrapper = await mountedPage()

    await wrapper.get('[data-action="restart-case"]').trigger('click')
    await wrapper.get('[data-reset-confirm]').trigger('click')
    const firstReplacementID = vi.mocked(resetIncidentCaseWithWarnings).mock.calls[0][0].new_case_id
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(listIncidentCases).toHaveBeenCalledTimes(2)
    expect(getIncidentCase).toHaveBeenLastCalledWith(stale.id)
    expect(notifications.info).toHaveBeenCalledWith(expect.stringContaining('已刷新'))
    expect(notifications.toastError).not.toHaveBeenCalled()

    await wrapper.get('[data-action="restart-case"]').trigger('click')
    const secondDialog = wrapper.get('[role="dialog"]')
    expect(secondDialog.text()).toContain('从“验证”重新开始')
    expect(secondDialog.text()).not.toContain('v8')
    expect(secondDialog.text()).not.toContain('case-reset-conflict')
    expect(secondDialog.text()).not.toContain(firstReplacementID)
  })

  it('surfaces cancellation and replacement start warnings together after the replacement is selected', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-reset-warning', 'waiting_evidence', '2026-07-13T00:00:00Z')
    const replacement = incident('case-reset-warning-next', 'waiting_evidence', '2026-07-13T00:01:00Z', { version: 3, reset_from_case_id: item.id })
    vi.mocked(listIncidentCases).mockResolvedValueOnce([item]).mockResolvedValue([replacement])
    mockCaseDetails(detail(item), detail(replacement))
    vi.mocked(resetIncidentCaseWithWarnings).mockResolvedValue({
      case: replacement,
      warnings: [
        { code: 'reset_runner_cancel_failed', message: '旧阶段 Agent 未能确认停止，请人工检查其运行状态。' },
        { code: 'reset_replacement_start_failed', message: '接替 Case 的新阶段未能启动，已保留为可恢复状态；请刷新 Case 或重试开始验证。' },
      ],
    })
    const wrapper = await mountedPage()

    await wrapper.get('[data-action="restart-case"]').trigger('click')
    await wrapper.get('[data-reset-confirm]').trigger('click')
    await flushPromises()
    await flushPromises()

    expect(wrapper.get('.live-error').text()).toContain('旧阶段 Agent 未能确认停止')
		expect(wrapper.get('.live-error').text()).toContain('接替 Case 的新阶段未能启动')
    expect(notifications.error).toHaveBeenCalledWith(expect.stringContaining('旧阶段 Agent 未能确认停止'))
		expect(notifications.error).toHaveBeenCalledWith(expect.stringContaining('接替 Case 的新阶段未能启动'))
		expect(notifications.success).not.toHaveBeenCalled()
  })

  it('finishes reset when the exact replacement event is selected before the bridge Promise resolves', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-1', 'waiting_evidence', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    const pendingReset = deferred<{ case: IncidentCase; warnings: [] }>()
    vi.mocked(resetIncidentCaseWithWarnings).mockReturnValue(pendingReset.promise)
    vi.mocked(getIncidentCase).mockImplementation(async caseID => {
      if (caseID === item.id) return detail(item)
      const replacement = incident(caseID, 'pending_validation', '2026-07-13T00:01:00Z', {
        version: 1,
        current_attempt_id: '',
        reset_from_case_id: item.id,
      })
      return detail(replacement)
    })
    const wrapper = await mountedPage()

    await wrapper.get('[data-action="restart-case"]').trigger('click')
    await wrapper.get('[data-reset-confirm]').trigger('click')
    const input = vi.mocked(resetIncidentCaseWithWarnings).mock.calls[0][0]
    const replacement = incident(input.new_case_id, 'pending_validation', '2026-07-13T00:01:00Z', {
      version: 1,
      current_attempt_id: '',
      reset_from_case_id: item.id,
    })
    const archived = { ...item, status: 'reset_archived' as const, version: 8, superseded_by_case_id: replacement.id }
    vi.mocked(listIncidentCases).mockResolvedValueOnce([archived, replacement])
    const eventHandler = runtime.EventsOn.mock.calls.find(call => call[0] === 'incident-case:event')?.[1]
    expect(eventHandler).toBeTypeOf('function')

    eventHandler?.({ kind: 'snapshot', case: replacement, snapshot: detail(replacement) })
    await flushPromises()
    await flushPromises()
    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe(replacement.id)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)

    pendingReset.resolve({ case: replacement, warnings: [] })
    await flushPromises()
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe(replacement.id)
    expect(listIncidentCases).toHaveBeenCalledTimes(2)
    expect(getIncidentCase).toHaveBeenLastCalledWith(replacement.id)
    expect(notifications.success).toHaveBeenCalledWith('Case 已重置，接替 Case 已创建')
  })

  it('keeps the durable replacement and reports a recoverable scheduling failure when reset rejects after its event', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-1', 'waiting_evidence', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    const pendingReset = deferred<{ case: IncidentCase; warnings: [] }>()
    vi.mocked(resetIncidentCaseWithWarnings).mockReturnValue(pendingReset.promise)
    vi.mocked(getIncidentCase).mockImplementation(async caseID => {
      if (caseID === item.id) return detail(item)
      return detail(incident(caseID, 'pending_validation', '2026-07-13T00:01:00Z', {
        version: 1,
        current_attempt_id: '',
        reset_from_case_id: item.id,
      }))
    })
    const wrapper = await mountedPage()

    await wrapper.get('[data-action="restart-case"]').trigger('click')
    await wrapper.get('[data-reset-confirm]').trigger('click')
    const input = vi.mocked(resetIncidentCaseWithWarnings).mock.calls[0][0]
    const replacement = incident(input.new_case_id, 'pending_validation', '2026-07-13T00:01:00Z', {
      version: 1,
      current_attempt_id: '',
      reset_from_case_id: item.id,
    })
    const archived = { ...item, status: 'reset_archived' as const, version: 8, superseded_by_case_id: replacement.id }
    vi.mocked(listIncidentCases).mockResolvedValueOnce([archived, replacement])
    const eventHandler = runtime.EventsOn.mock.calls.find(call => call[0] === 'incident-case:event')?.[1]
    eventHandler?.({ kind: 'snapshot', case: replacement, snapshot: detail(replacement) })
    await flushPromises()
    await flushPromises()
    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe(replacement.id)

    pendingReset.reject(new Error('validation phase schedule failed'))
    await flushPromises()
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe(replacement.id)
    expect(listIncidentCases).toHaveBeenCalledTimes(2)
    expect(vi.mocked(getIncidentCase).mock.calls.filter(([caseID]) => caseID === replacement.id)).toHaveLength(2)
    expect(wrapper.get('.live-error').text()).toContain('接替 Case 已创建，但新阶段启动失败')
    expect(wrapper.get('.live-error').text()).toContain('validation phase schedule failed')
    expect(wrapper.get('.live-error').text()).toContain('请刷新 Case 或重试开始验证')
    expect(notifications.error).toHaveBeenCalledWith(expect.stringContaining('接替 Case 已创建，但新阶段启动失败'))
    expect(notifications.toastError).not.toHaveBeenCalledWith('重置故障 Case', expect.anything())
    expect(notifications.success).not.toHaveBeenCalled()
  })

  it('keeps the reset dialog cancellation-first, focus-trapped, dismissible, and focus-restoring', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-1', 'waiting_evidence', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(detail(item))
    const wrapper = mount(IncidentWorkbenchPage, { attachTo: document.body })
    await flushPromises()
    await flushPromises()
    await flushPromises()

    const trigger = wrapper.get<HTMLButtonElement>('[data-action="restart-case"]')
    trigger.element.focus()
    await trigger.trigger('click')
    const cancel = wrapper.get<HTMLButtonElement>('[data-reset-cancel]')
    const confirm = wrapper.get<HTMLButtonElement>('[data-reset-confirm]')
    expect(document.activeElement).toBe(cancel.element)

    await cancel.trigger('keydown', { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(confirm.element)
    await confirm.trigger('keydown', { key: 'Tab' })
    expect(document.activeElement).toBe(cancel.element)

    await wrapper.get('.reset-dialog-backdrop').trigger('keydown', { key: 'Escape' })
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(document.activeElement).toBe(trigger.element)

    await trigger.trigger('click')
    await wrapper.get('.reset-dialog-backdrop').trigger('click')
    await wrapper.vm.$nextTick()
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(document.activeElement).toBe(trigger.element)

    const source = readFileSync('src/pages/IncidentWorkbenchPage.vue', 'utf8')
    expect(source).toMatch(/\.reset-dialog[^}]*width: min\([^;]+, 100%\)/)
    expect(source).toMatch(/\.reset-dialog[\s\S]*?max-height: calc\(100vh - 32px\)/)
    expect(source).toMatch(/\.reset-dialog[\s\S]*?\.btn[^}]*min-height: 44px/)
    wrapper.unmount()
  })

  it('keeps reset controls disabled while pending and reports a retryable live error', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-1', 'waiting_evidence', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(detail(item))
    const pendingReset = deferred<{ case: IncidentCase; warnings: [] }>()
    vi.mocked(resetIncidentCaseWithWarnings).mockReturnValue(pendingReset.promise)
    const wrapper = mount(IncidentWorkbenchPage, { attachTo: document.body })
    await flushPromises()
    await flushPromises()
    await flushPromises()

    await wrapper.get('[data-action="restart-case"]').trigger('click')
    await wrapper.get('[data-reset-confirm]').trigger('click')
    await wrapper.vm.$nextTick()
    expect(wrapper.get<HTMLButtonElement>('[data-reset-cancel]').element.disabled).toBe(true)
    expect(wrapper.get<HTMLButtonElement>('[data-reset-confirm]').element.disabled).toBe(true)
    expect(wrapper.get<HTMLButtonElement>('[data-action="restart-case"]').element.disabled).toBe(true)
    expect(wrapper.find('[data-action="enter-case"]').exists()).toBe(false)
    expect(wrapper.get('.bot-action-disabled-reason').text()).toContain('处理中')
    const dialog = wrapper.get<HTMLElement>('[role="dialog"]')
    expect(wrapper.get('[data-reset-error]').attributes('aria-live')).toBe('assertive')
    await dialog.trigger('keydown', { key: 'Tab' })
    expect(document.activeElement).toBe(dialog.element)

    pendingReset.reject(new Error('reset conflict; refresh and retry'))
    await flushPromises()
    await flushPromises()

    expect(wrapper.get('[role="dialog"]').text()).toContain('reset conflict; refresh and retry')
    const cancel = wrapper.get<HTMLButtonElement>('[data-reset-cancel]')
    const confirm = wrapper.get<HTMLButtonElement>('[data-reset-confirm]')
    expect(cancel.element.disabled).toBe(false)
    expect(confirm.element.disabled).toBe(false)
    expect(document.activeElement).toBe(dialog.element)

    await dialog.trigger('keydown', { key: 'Tab', shiftKey: true })
    expect(document.activeElement).toBe(confirm.element)
    dialog.element.focus()
    await dialog.trigger('keydown', { key: 'Tab' })
    expect(document.activeElement).toBe(cancel.element)
    wrapper.unmount()
  })

  it('ignores a delayed reset completion after another Bug and Case are selected', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA, bugB])
    const caseA = incident('case-a', 'waiting_evidence', '2026-07-13T00:00:00Z')
    const caseB = incident('case-b', 'waiting_evidence', '2026-07-13T00:00:00Z', { bug_id: 'bug-b', version: 3 })
    vi.mocked(listIncidentCases).mockResolvedValue([caseA, caseB])
    mockCaseDetails(detail(caseA), detail(caseB))
    const pendingReset = deferred<{ case: IncidentCase; warnings: [] }>()
    vi.mocked(resetIncidentCaseWithWarnings).mockReturnValue(pendingReset.promise)
    const wrapper = await mountedPage()

    await wrapper.get('[data-action="restart-case"]').trigger('click')
    await wrapper.get('[data-reset-confirm]').trigger('click')
    await wrapper.get('[data-ticket-id="bug-b"]').trigger('click')
    await flushPromises()
    await flushPromises()
    vi.mocked(getIncidentCase).mockClear()
    notifications.success.mockClear()
    pendingReset.resolve({ case: incident('case-reset-stale', 'pending_validation', '2026-07-13T00:01:00Z', { version: 1, reset_from_case_id: 'case-a' }), warnings: [] })
    await flushPromises()
    await flushPromises()

    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe('case-b')
    expect(getIncidentCase).not.toHaveBeenCalled()
    expect(notifications.success).not.toHaveBeenCalled()
  })

  it('does not expose a delayed reset error after another Bug and Case are selected', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA, bugB])
    const caseA = incident('case-a', 'waiting_evidence', '2026-07-13T00:00:00Z')
    const caseB = incident('case-b', 'waiting_evidence', '2026-07-13T00:00:00Z', { bug_id: 'bug-b', version: 3 })
    vi.mocked(listIncidentCases).mockResolvedValue([caseA, caseB])
    mockCaseDetails(detail(caseA), detail(caseB))
    const pendingReset = deferred<{ case: IncidentCase; warnings: [] }>()
    vi.mocked(resetIncidentCaseWithWarnings).mockReturnValue(pendingReset.promise)
    const wrapper = await mountedPage()

    await wrapper.get('[data-action="restart-case"]').trigger('click')
    await wrapper.get('[data-reset-confirm]').trigger('click')
    await wrapper.get('[data-ticket-id="bug-b"]').trigger('click')
    await flushPromises()
    await flushPromises()
    notifications.toastError.mockClear()
    pendingReset.reject(new Error('stale reset failed'))
    await flushPromises()
    await flushPromises()

    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe('case-b')
    expect(wrapper.text()).not.toContain('stale reset failed')
    expect(notifications.toastError).not.toHaveBeenCalled()
  })

  it('does not apply a delayed primary-action completion after another Bug and Case are selected', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA, bugB])
    const caseA = incident('case-a', 'waiting_fix_approval', '2026-07-13T00:00:00Z')
    const caseB = incident('case-b', 'waiting_evidence', '2026-07-13T00:00:00Z', { bug_id: 'bug-b', version: 3 })
    const snapshotA = detail(caseA, {
      attempts: [{ id: 'attempt-1', case_id: 'case-a', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { call_chain: [{ repo: 'api' }] }, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }],
    })
    const snapshotB = detail(caseB)
    vi.mocked(listIncidentCases).mockResolvedValue([caseA, caseB])
    mockCaseDetails(snapshotA, snapshotB)
    const pendingApproval = deferred<IncidentCase>()
    vi.mocked(approveIncidentFix).mockReturnValue(pendingApproval.promise)
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    await wrapper.get('#fix-baseline-0').setValue('feature/work')
    await wrapper.get('[data-confirm]').trigger('click')
    await wrapper.get('[data-ticket-id="bug-b"]').trigger('click')
    await flushPromises()
    await flushPromises()
    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe('case-b')
    vi.mocked(getIncidentCase).mockClear()
    notifications.success.mockClear()

    pendingApproval.resolve({ ...caseA, status: 'fixing', version: 8 })
    await flushPromises()
    await flushPromises()

    expect(getIncidentCase).not.toHaveBeenCalled()
    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe('case-b')
    expect(notifications.success).not.toHaveBeenCalled()
  })

  it('does not expose a delayed primary-action error after another Bug and Case are selected', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA, bugB])
    const caseA = incident('case-a', 'waiting_fix_approval', '2026-07-13T00:00:00Z')
    const caseB = incident('case-b', 'waiting_evidence', '2026-07-13T00:00:00Z', { bug_id: 'bug-b', version: 3 })
    const snapshotA = detail(caseA, {
      attempts: [{ id: 'attempt-1', case_id: 'case-a', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { call_chain: [{ repo: 'api' }] }, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([caseA, caseB])
    mockCaseDetails(snapshotA, detail(caseB))
    const pendingApproval = deferred<IncidentCase>()
    vi.mocked(approveIncidentFix).mockReturnValue(pendingApproval.promise)
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    await wrapper.get('#fix-baseline-0').setValue('feature/work')
    await wrapper.get('[data-confirm]').trigger('click')
    await wrapper.get('[data-ticket-id="bug-b"]').trigger('click')
    await flushPromises()
    await flushPromises()
    notifications.toastError.mockClear()

    pendingApproval.reject(new Error('stale approval failed'))
    await flushPromises()
    await flushPromises()

    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe('case-b')
    expect(wrapper.text()).not.toContain('stale approval failed')
    expect(notifications.toastError).not.toHaveBeenCalled()
  })

  it('forwards the exact persisted target heads with merge approval', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-1', 'waiting_merge_approval', '2026-07-13T00:00:00Z')
    const snapshot = detail(item, {
      code_changes: [
        { id: 'a', case_id: 'case-1', attempt_id: 'attempt-1', repo: 'api', base_branch: 'main', fix_branch: 'fix/api', fix_commit: 'fix-api', test_evidence: {}, target_environment_branch: 'test', merge_base_head: 'head-api', merge_commit: '', push_remote: 'origin', push_status: 'pushed' },
        { id: 'w', case_id: 'case-1', attempt_id: 'attempt-1', repo: 'web', base_branch: 'main', fix_branch: 'fix/web', fix_commit: 'fix-web', test_evidence: {}, target_environment_branch: 'test', merge_base_head: 'head-web', merge_commit: '', push_remote: 'origin', push_status: 'pushed' },
      ],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(snapshot)
    vi.mocked(approveIncidentMerge).mockResolvedValue({ ...item, status: 'merging', version: 8 })
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    await wrapper.get('[data-confirm]').trigger('click')
    await flushPromises()

    expect(approveIncidentMerge).toHaveBeenCalledWith(expect.objectContaining({
      target_heads: { api: 'head-api', web: 'head-web' },
      fix_commits: { api: 'fix-api', web: 'fix-web' },
      target_branches: { api: 'test', web: 'test' },
    }))
  })

  it.each([
    ['merge_conflict', approveIncidentMerge, 'resolve_merge_conflict', 'fix'],
    ['deployment_unverified', notifyIncidentDeployed, 'update_deployment_proof', 'regression'],
  ] as const)('uses ContinueIncidentCase for %s recovery before the gated action', async (status, forbidden, decision, phase) => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-1', status, '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(detail(item))
    vi.mocked(continueIncidentCase).mockResolvedValue({ ...item, status: status === 'merge_conflict' ? 'waiting_merge_approval' : 'waiting_deployment', version: 8 })
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    if (status === 'merge_conflict') {
      await wrapper.get('[role="dialog"] textarea').setValue('人工确认已处理')
      await wrapper.get('[data-confirm]').trigger('click')
    }
    await flushPromises()

    const inputJSON = status === 'merge_conflict'
      ? { decision, evidence: '人工确认已处理' }
      : { decision: 'retry_deployment_check' }
    expect(continueIncidentCase).toHaveBeenCalledWith(expect.objectContaining({ phase, input_json: inputJSON }))
    expect(forbidden).not.toHaveBeenCalled()
  })

  it('renders same-version browser progress events without waiting for a Case version change', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-browser-progress', 'validating', '2026-07-15T10:00:00Z', { version: 3, current_attempt_id: 'attempt-browser' })
    const snapshot = detail(item, {
      attempts: [{ id: 'attempt-browser', case_id: item.id, cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'running', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(snapshot)
    const wrapper = await mountedPage()
    const eventHandler = runtime.EventsOn.mock.calls.find(call => call[0] === 'incident-case:event')?.[1]

    eventHandler?.({
      kind: 'snapshot', case: item, snapshot,
      phase_event: { type: 'browser_progress', message: 'Cookie: sid=secret /Users/alice/private/trace.zip', raw: { Authorization: 'Bearer secret', storageState: 'secret' }, meta: { case_id: item.id, attempt_id: 'attempt-browser', browser_code: 'runtime_preparing' } },
    })
    eventHandler?.({
      kind: 'snapshot', case: item, snapshot,
      phase_event: { type: 'browser_progress', message: 'password=hunter2', meta: { case_id: item.id, attempt_id: 'attempt-browser', browser_code: 'action_started', action_id: '/private/open-users', current: 2, total: 4 } },
    })
    await flushPromises()

    expect(wrapper.get('[data-browser-state="progress"]').text()).toContain('准备验证浏览器')
    expect(wrapper.get('[data-browser-state="progress"]').text()).toContain('执行 2/4：开始页面操作')
    expect(wrapper.html()).not.toMatch(/Cookie|Authorization|password|storageState|hunter2|private/)
    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe(item.id)
  })

  it('runs login once with the exact browser identity, then refreshes the continued snapshot', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-browser-login', 'waiting_evidence', '2026-07-15T10:00:00Z', { version: 7, current_attempt_id: 'attempt-login' })
    const blocked = detail(item, {
      attempts: [{ id: 'attempt-login', case_id: item.id, cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { error_code: 'browser_login_required', application_origin: 'https://app.test', login_origin: 'https://login.test' }, parent_attempt_id: '', started_at: '', error_code: 'browser_login_required', error_message: '', usage: {} }],
    })
    const continued = { ...item, status: 'validating' as const, version: 8, current_attempt_id: 'attempt-login-next' }
    const refreshed = detail(continued, {
      attempts: [{ ...blocked.attempts[0], id: 'attempt-login-next', status: 'running', error_code: '', output_json: {}, parent_attempt_id: 'attempt-login' }],
      artifacts: [{ id: 'recovery-evidence', case_id: item.id, attempt_id: 'attempt-login-next', kind: 'log', sha256: 'a', size: 1, captured_at: '', environment: 'test', version: '8', request_id: '', trace_id: '' }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    let recoveryCompleted = false
    vi.mocked(getIncidentCase).mockImplementation(async () => recoveryCompleted ? refreshed : blocked)
    const pending = deferred<IncidentCase>()
    vi.mocked(openIncidentBrowserLogin).mockReturnValue(pending.promise)
    const wrapper = await mountedPage()

    const login = wrapper.get<HTMLButtonElement>('[data-browser-action="login"]')
    await login.trigger('click')
    await login.trigger('click')
    expect(openIncidentBrowserLogin).toHaveBeenCalledTimes(1)
    expect(openIncidentBrowserLogin).toHaveBeenCalledWith({
      case_id: item.id,
      attempt_id: 'attempt-login',
      expected_version: 7,
      idempotency_key: 'login:case-browser-login:attempt-login:v7',
      actor_id: 'desktop-user',
    })

    recoveryCompleted = true
    pending.resolve(continued)
    await flushPromises()
    await flushPromises()

    expect(getIncidentCase).toHaveBeenLastCalledWith(item.id)
    expect(wrapper.get('.status-pill').text()).toBe('验证中')
    expect(wrapper.find('[data-artifact-id="recovery-evidence"]').exists()).toBe(true)
  })

  it('repairs the browser runtime with the exact key and refreshes only the current Case', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-browser-runtime', 'waiting_evidence', '2026-07-15T10:00:00Z', { version: 11, current_attempt_id: 'attempt-runtime' })
    const blocked = detail(item, {
      attempts: [{ id: 'attempt-runtime', case_id: item.id, cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { error_code: 'browser_runtime_broken' }, parent_attempt_id: '', started_at: '', error_code: 'browser_runtime_broken', error_message: '', usage: {} }],
    })
    const continued = { ...item, status: 'validating' as const, version: 12, current_attempt_id: 'attempt-runtime-next' }
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    let recoveryCompleted = false
    vi.mocked(getIncidentCase).mockImplementation(async () => recoveryCompleted ? detail(continued) : blocked)
    vi.mocked(repairIncidentBrowserRuntime).mockImplementation(async () => {
      recoveryCompleted = true
      return continued
    })
    const wrapper = await mountedPage()

    await wrapper.get('[data-browser-action="repair-runtime"]').trigger('click')
    await flushPromises()
    await flushPromises()

    expect(repairIncidentBrowserRuntime).toHaveBeenCalledWith({
      case_id: item.id,
      attempt_id: 'attempt-runtime',
      expected_version: 11,
      idempotency_key: 'repair-runtime:case-browser-runtime:attempt-runtime:v11',
      actor_id: 'desktop-user',
    })
    expect(getIncidentCase).toHaveBeenLastCalledWith(item.id)
  })

  it('applies a successful recovery before refresh and reports refresh failure only as a local warning', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-browser-refresh-warning', 'waiting_evidence', '2026-07-15T10:00:00Z', { version: 7, current_attempt_id: 'attempt-login' })
    const blocked = detail(item, {
      attempts: [{ id: 'attempt-login', case_id: item.id, cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { error_code: 'browser_login_required', application_origin: 'https://app.test', login_origin: 'https://login.test' }, parent_attempt_id: '', started_at: '', error_code: 'browser_login_required', error_message: '', usage: {} }],
    })
    const continued = { ...item, status: 'validating' as const, version: 8, current_attempt_id: 'attempt-login-next' }
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    let recoveryCompleted = false
    vi.mocked(getIncidentCase).mockImplementation(async () => {
      if (recoveryCompleted) throw new Error('Cookie: secret /private/detail')
      return blocked
    })
    vi.mocked(openIncidentBrowserLogin).mockImplementation(async () => {
      recoveryCompleted = true
      return continued
    })
    const wrapper = await mountedPage()

    await wrapper.get('[data-browser-action="login"]').trigger('click')
    await flushPromises()
    await flushPromises()

    expect(getIncidentCase).toHaveBeenLastCalledWith(item.id)
    expect(wrapper.get('.status-pill').text()).toBe('验证中')
    expect(wrapper.text()).toContain('浏览器操作已完成，但 Case 详情刷新失败')
    expect(wrapper.text()).not.toMatch(/Cookie|private\/detail|无法完成验证浏览器登录/)
    expect(notifications.error).not.toHaveBeenCalled()
  })

  it('rejects a cross-Case recovery result without refreshing or changing the captured Case', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-browser-cross-result', 'waiting_evidence', '2026-07-15T10:00:00Z', { version: 7, current_attempt_id: 'attempt-login' })
    const blocked = detail(item, {
      attempts: [{ id: 'attempt-login', case_id: item.id, cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { error_code: 'browser_login_required', application_origin: 'https://app.test', login_origin: 'https://login.test' }, parent_attempt_id: '', started_at: '', error_code: 'browser_login_required', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(blocked)
    vi.mocked(openIncidentBrowserLogin).mockResolvedValue({ ...item, id: 'case-other', status: 'validating', version: 8 })
    const wrapper = await mountedPage()
    const initialReads = vi.mocked(getIncidentCase).mock.calls.length

    await wrapper.get('[data-browser-action="login"]').trigger('click')
    await flushPromises()

    expect(getIncidentCase).toHaveBeenCalledTimes(initialReads)
    expect(getIncidentCase).not.toHaveBeenCalledWith('case-other')
    expect(wrapper.get('.status-pill').text()).toBe('等待证据')
    expect(wrapper.text()).toContain('无法完成验证浏览器登录')
  })

  it('does not surface a recovery error after the captured attempt or version changes', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-browser-stale-error', 'waiting_evidence', '2026-07-15T10:00:00Z', { version: 7, current_attempt_id: 'attempt-login' })
    const blocked = detail(item, {
      attempts: [{ id: 'attempt-login', case_id: item.id, cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { error_code: 'browser_login_required', application_origin: 'https://app.test', login_origin: 'https://login.test' }, parent_attempt_id: '', started_at: '', error_code: 'browser_login_required', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(blocked)
    const pending = deferred<IncidentCase>()
    vi.mocked(openIncidentBrowserLogin).mockReturnValue(pending.promise)
    const wrapper = await mountedPage()
    const eventHandler = runtime.EventsOn.mock.calls.find(call => call[0] === 'incident-case:event')?.[1]

    await wrapper.get('[data-browser-action="login"]').trigger('click')
    const advanced = { ...item, version: 8, current_attempt_id: 'attempt-next' }
    eventHandler?.({ kind: 'snapshot', case: advanced, snapshot: detail(advanced, { attempts: [{ ...blocked.attempts[0], id: 'attempt-next' }] }) })
    await flushPromises()
    pending.reject(new Error('Authorization: Bearer secret /private/login'))
    await flushPromises()

    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe(item.id)
    expect(wrapper.text()).not.toMatch(/无法完成验证浏览器登录|Authorization|private\/login/)
    expect(notifications.error).not.toHaveBeenCalled()
  })

  it('clears the exact blocked session idempotently and refreshes only after success', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-browser-clear', 'waiting_evidence', '2026-07-15T10:00:00Z', { version: 5, current_attempt_id: 'attempt-login' })
    const blocked = detail(item, {
      attempts: [{ id: 'attempt-login', case_id: item.id, cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { error_code: 'browser_login_required', application_origin: 'https://app.test', login_origin: 'https://login.test' }, parent_attempt_id: '', started_at: '', error_code: 'browser_login_required', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    vi.mocked(getIncidentCase).mockResolvedValue(blocked)
    const pending = deferred<void>()
    vi.mocked(clearIncidentBrowserSession).mockReturnValue(pending.promise)
    const wrapper = await mountedPage()
    const initialReads = vi.mocked(getIncidentCase).mock.calls.length

    const clear = wrapper.get<HTMLButtonElement>('[data-browser-action="clear-session"]')
    await clear.trigger('click')
    await clear.trigger('click')
    expect(clearIncidentBrowserSession).toHaveBeenCalledTimes(1)
    expect(getIncidentCase).toHaveBeenCalledTimes(initialReads)
    expect(clearIncidentBrowserSession).toHaveBeenCalledWith({
      case_id: item.id,
      attempt_id: 'attempt-login',
      expected_version: 5,
      idempotency_key: 'clear-session:case-browser-clear:attempt-login:v5',
      actor_id: 'desktop-user',
    })

    pending.resolve()
    await flushPromises()
    await flushPromises()
    expect(getIncidentCase).toHaveBeenCalledTimes(initialReads + 1)
  })

  it('keeps clear-session success separate from a captured-Case refresh failure', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-browser-clear-warning', 'waiting_evidence', '2026-07-15T10:00:00Z', { version: 5, current_attempt_id: 'attempt-login' })
    const blocked = detail(item, {
      attempts: [{ id: 'attempt-login', case_id: item.id, cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { error_code: 'browser_login_required', application_origin: 'https://app.test', login_origin: 'https://login.test' }, parent_attempt_id: '', started_at: '', error_code: 'browser_login_required', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    let clearCompleted = false
    vi.mocked(getIncidentCase).mockImplementation(async () => {
      if (clearCompleted) throw new Error('storageState /private/session')
      return blocked
    })
    vi.mocked(clearIncidentBrowserSession).mockImplementation(async () => { clearCompleted = true })
    const wrapper = await mountedPage()

    await wrapper.get('[data-browser-action="clear-session"]').trigger('click')
    await flushPromises()
    await flushPromises()

    expect(getIncidentCase).toHaveBeenLastCalledWith(item.id)
    expect(wrapper.text()).toContain('浏览器操作已完成，但 Case 详情刷新失败')
    expect(wrapper.text()).not.toMatch(/清除浏览器登录态失败|storageState|private\/session/)
    expect(notifications.error).not.toHaveBeenCalled()
  })

  it('routes validator recovery to deployed robot management without exposing backend errors', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-validator-missing', 'waiting_evidence', '2026-07-15T10:00:00Z', { current_attempt_id: 'attempt-validator' })
    const snapshot = detail(item, {
      attempts: [{ id: 'attempt-validator', case_id: item.id, cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { error_code: 'validator_not_installed' }, parent_attempt_id: '', started_at: '', error_code: 'validator_not_installed', error_message: '/Users/alice/private/validator workspace', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(snapshot)
    const wrapper = await mountedPage()

    await wrapper.get('[data-browser-action="redeploy-validator"]').trigger('click')

    expect(router.push).toHaveBeenCalledWith('/bots')
    expect(wrapper.text()).not.toContain('/Users/alice/private/validator workspace')
  })

  it('routes a missing frontend URL to the selected Bug sync flow without evidence continuation', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-url-required', 'waiting_evidence', '2026-07-15T10:00:00Z', { current_attempt_id: 'attempt-url' })
    const snapshot = detail(item, {
      attempts: [{ id: 'attempt-url', case_id: item.id, cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { error_code: 'browser_url_required' }, parent_attempt_id: '', started_at: '', error_code: 'browser_url_required', error_message: '/private/raw URL error', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(snapshot)
    const wrapper = await mountedPage()

    expect(wrapper.find('.primary-action').exists()).toBe(false)
    expect(wrapper.find('#case-supplement').exists()).toBe(false)
    await wrapper.get('[data-browser-action="edit-bug-url"]').trigger('click')

    expect(router.push).toHaveBeenCalledWith({ path: '/bugs', query: { bug_id: 'bug-a' } })
    expect(continueIncidentCase).not.toHaveBeenCalled()
    expect(wrapper.text()).not.toContain('/private/raw URL error')
  })

  it('uploads supplemental screenshots before retrying the current validation Attempt', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-image-evidence', 'not_reproduced', '2026-07-15T10:00:00Z', { current_attempt_id: 'attempt-validation', version: 7 })
    const snapshot = detail(item, {
      attempts: [{ id: 'attempt-validation', case_id: item.id, cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: { mode: 'reproduce', target_environment: 'test' }, output_json: {}, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(snapshot)
    vi.mocked(uploadIncidentEvidenceImages).mockResolvedValue([{ artifact_id: 'artifact-shot-1', name: 'search-result.png', mime_type: 'image/png', size: 128 }])
    vi.mocked(continueIncidentCase).mockResolvedValue({ ...item, status: 'validating', version: 8 })
    const wrapper = await mountedPage()

    wrapper.getComponent(BugCaseLifecycle).vm.$emit('primary', {
      kind: 'supply_evidence',
      input: '',
      images: [{ name: 'search-result.png', mime_type: 'image/png', data_base64: 'aW1hZ2U=' }],
    })
    await flushPromises()
    await flushPromises()

    expect(uploadIncidentEvidenceImages).toHaveBeenCalledWith({
      case_id: item.id,
      attempt_id: 'attempt-validation',
      expected_version: 7,
      images: [{ name: 'search-result.png', mime_type: 'image/png', data_base64: 'aW1hZ2U=' }],
    })
    expect(continueIncidentCase).toHaveBeenCalledWith(expect.objectContaining({
      case_id: item.id,
      expected_version: 7,
      phase: 'validation',
      input_json: expect.objectContaining({
        user_input: expect.stringContaining('artifact-shot-1'),
      }),
    }))
    expect(vi.mocked(uploadIncidentEvidenceImages).mock.invocationCallOrder[0]).toBeLessThan(vi.mocked(continueIncidentCase).mock.invocationCallOrder[0])
  })

  it('regenerates an invalid browser plan in the current Case instead of resetting the workflow', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-plan-invalid', 'waiting_evidence', '2026-07-15T10:00:00Z', { current_attempt_id: 'attempt-plan', version: 7 })
    const snapshot = detail(item, {
      attempts: [{ id: 'attempt-plan', case_id: item.id, cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: { mode: 'reproduce', target_environment: 'test' }, output_json: { error_code: 'browser_validator_plan_invalid' }, parent_attempt_id: '', started_at: '', error_code: 'browser_validator_plan_invalid', error_message: 'rejected raw plan', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(snapshot)
    vi.mocked(continueIncidentCase).mockResolvedValue({ ...item, status: 'validating', version: 8 })
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    await flushPromises()

    expect(continueIncidentCase).toHaveBeenCalledWith(expect.objectContaining({
      case_id: item.id,
      expected_version: 7,
      phase: 'validation',
      input_json: { mode: 'reproduce', target_environment: 'test', user_input: '' },
    }))
    expect(resetIncidentCaseWithWarnings).not.toHaveBeenCalled()
    expect(wrapper.text()).not.toContain('rejected raw plan')
  })

})
