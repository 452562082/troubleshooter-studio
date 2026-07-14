import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import {
  approveIncidentFix,
  approveIncidentMerge,
  continueIncidentCase,
  getIncidentCase,
  listBugs,
  listIncidentCases,
  matchBugBots,
  notifyIncidentDeployed,
  IncidentWorkflowCommandError,
  resetIncidentCaseWithWarnings,
  saveBugSelectedBot,
  startIncidentCase,
  type CaseStatus,
  type IncidentCase,
  type IncidentCaseDetail,
} from '../lib/bridge'
import IncidentWorkbenchPage from './IncidentWorkbenchPage.vue'

const route = vi.hoisted(() => ({ path: '/incidents', query: {} as Record<string, string> }))
const router = vi.hoisted(() => ({ replace: vi.fn() }))
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
  continueIncidentCase: vi.fn(),
  fetchBugByID: vi.fn(),
  getIncidentCase: vi.fn(),
  listBugs: vi.fn().mockResolvedValue([]),
  listIncidentCases: vi.fn().mockResolvedValue([]),
  matchBugBots: vi.fn().mockResolvedValue([]),
  notifyIncidentDeployed: vi.fn(),
  resetIncidentCaseWithWarnings: vi.fn(),
  saveBugSelectedBot: vi.fn(),
  startIncidentCase: vi.fn(),
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
const prodBotMatch = {
  bot: { key: 'base-prod|codex', system_id: 'base', target: 'codex', path: '/repo/base-prod', name: 'Base Prod', env: 'prod' }, score: 9, reasons: ['系统匹配'],
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
  runtime.EventsOn.mockClear()
  vi.mocked(listBugs).mockReset().mockResolvedValue([])
  vi.mocked(listIncidentCases).mockReset().mockResolvedValue([])
  vi.mocked(getIncidentCase).mockReset()
  vi.mocked(matchBugBots).mockReset().mockResolvedValue([botMatch])
  vi.mocked(saveBugSelectedBot).mockReset().mockResolvedValue(bugA as any)
  vi.mocked(startIncidentCase).mockReset()
  vi.mocked(continueIncidentCase).mockReset()
  vi.mocked(approveIncidentFix).mockReset()
  vi.mocked(approveIncidentMerge).mockReset()
  vi.mocked(notifyIncidentDeployed).mockReset()
  vi.mocked(resetIncidentCaseWithWarnings).mockReset()
  notifications.error.mockReset()
  notifications.success.mockReset()
  notifications.info.mockReset()
  notifications.toastError.mockReset()
})

describe('IncidentWorkbenchPage', () => {
  it('loads locally stored Bugs on mount without exposing a duplicate refresh action', async () => {
    vi.mocked(listBugs).mockResolvedValue([bugA])

    const wrapper = await mountedPage()

    expect(listBugs).toHaveBeenCalledTimes(1)
    expect(wrapper.text()).not.toContain('刷新 Bug')
    expect(wrapper.get('.incident-header p').text()).toContain('Bug 工单')
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

  it('opens the newest active Case without calling Start', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const oldActive = incident('case-active-old', 'waiting_evidence', '2026-07-11T00:00:00Z')
    const newActive = incident('case-active-new', 'waiting_fix_approval', '2026-07-12T00:00:00Z', { current_attempt_id: 'root-1' })
    const terminal = incident('case-terminal', 'fixed_verified', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([terminal, oldActive, newActive])
    mockCaseDetails(detail(terminal), detail(oldActive), detail(newActive))

    const wrapper = await mountedPage()

    expect(wrapper.get('.case-heading').text()).toContain('case-active-new')
    expect(wrapper.text()).toContain('允许修复')
    expect(startIncidentCase).not.toHaveBeenCalled()
    expect(wrapper.find('[data-action="start-case"]').exists()).toBe(false)
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

  it.each(['waiting_evidence', 'fixed_verified'] as const)('shows enter and restart in the Bot action panel for %s', async status => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident(`case-${status}`, status, '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(detail(item))

    const wrapper = await mountedPage()
    const panel = wrapper.get('.bot-action-panel')

    expect(panel.get('[data-action="enter-case"]').text()).toContain('进入故障闭环')
    expect(panel.get('[data-action="restart-case"]').text()).toContain('重新开始故障闭环')
    expect(panel.find('[data-action="start-case"]').exists()).toBe(false)
    expect(wrapper.find('.start-card').exists()).toBe(false)
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
  ] as const)('keeps enter enabled but disables restart for %s', async (_label, availableMatches, reason) => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    vi.mocked(matchBugBots).mockResolvedValue(availableMatches as any)
    const item = incident('case-disabled-restart', 'waiting_evidence', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(detail(item))

    const wrapper = await mountedPage()

    expect(wrapper.get<HTMLButtonElement>('[data-action="enter-case"]').element.disabled).toBe(false)
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

  it('scrolls smoothly to an active Case and focuses its current-stage action', async () => {
    const scrollIntoView = vi.fn()
    const originalScrollIntoView = HTMLElement.prototype.scrollIntoView
    Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: scrollIntoView })
    vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: false }))
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-enter-active', 'waiting_evidence', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(detail(item))
    const wrapper = mount(IncidentWorkbenchPage, { attachTo: document.body })
    await flushPromises()
    await flushPromises()
    await flushPromises()

    await wrapper.get('[data-action="enter-case"]').trigger('click')
    await wrapper.vm.$nextTick()

    expect(scrollIntoView).toHaveBeenCalledWith({ behavior: 'smooth', block: 'start' })
    expect(document.activeElement).toBe(wrapper.get('.primary-action').element)

    wrapper.unmount()
    if (originalScrollIntoView) Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: originalScrollIntoView })
    else delete (HTMLElement.prototype as Partial<HTMLElement>).scrollIntoView
    vi.unstubAllGlobals()
  })

  it('uses immediate scrolling and focuses the heading for a terminal Case', async () => {
    const scrollIntoView = vi.fn()
    const originalScrollIntoView = HTMLElement.prototype.scrollIntoView
    Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: scrollIntoView })
    vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: true }))
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-enter-terminal', 'fixed_verified', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(detail(item))
    const wrapper = mount(IncidentWorkbenchPage, { attachTo: document.body })
    await flushPromises()
    await flushPromises()
    await flushPromises()

    await wrapper.get('[data-action="enter-case"]').trigger('click')
    await wrapper.vm.$nextTick()

    expect(scrollIntoView).toHaveBeenCalledWith({ behavior: 'auto', block: 'start' })
    const heading = wrapper.get<HTMLElement>('.case-heading')
    expect(heading.attributes('tabindex')).toBe('-1')
    expect(document.activeElement).toBe(heading.element)

    wrapper.unmount()
    if (originalScrollIntoView) Object.defineProperty(HTMLElement.prototype, 'scrollIntoView', { configurable: true, value: originalScrollIntoView })
    else delete (HTMLElement.prototype as Partial<HTMLElement>).scrollIntoView
    vi.unstubAllGlobals()
  })

  it('finishes a loading Enter by focusing after the requested Case detail resolves', async () => {
    const scrollIntoView = stubIncidentEntry()
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-loading-enter', 'waiting_evidence', '2026-07-13T00:00:00Z')
    const pendingDetail = deferred<IncidentCaseDetail>()
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    vi.mocked(getIncidentCase).mockReturnValue(pendingDetail.promise)
    const wrapper = mount(IncidentWorkbenchPage, { attachTo: document.body })
    await flushPromises()
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('.case-loading').exists()).toBe(true)
    await wrapper.get('[data-action="enter-case"]').trigger('click')
    expect(scrollIntoView).toHaveBeenCalledTimes(1)
    expect(wrapper.find('.primary-action').exists()).toBe(false)

    pendingDetail.resolve(detail(item))
    await flushPromises()
    await flushPromises()
    await flushPromises()

    expect(scrollIntoView).toHaveBeenCalledTimes(2)
    expect(scrollIntoView).toHaveBeenLastCalledWith({ behavior: 'smooth', block: 'start' })
    expect(document.activeElement).toBe(wrapper.get('.primary-action').element)
    wrapper.unmount()
  })

  it.each(['fixed_verified', 'legacy_archived', 'reset_archived'] as const)('confirms a %s terminal round using the snapshotted Bot key and environment', async status => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    vi.mocked(matchBugBots).mockResolvedValue([botMatch, prodBotMatch])
    const terminal = incident('case-terminal', status, '2026-07-13T00:00:00Z', { selected_bot_key: 'base|codex' })
    const opened = incident('case-new', 'validating', '2026-07-13T00:01:00Z', { selected_bot_key: 'base-prod|codex', environment: 'prod', version: 1 })
    vi.mocked(listIncidentCases).mockResolvedValue([terminal])
    mockCaseDetails(detail(terminal), detail(opened))
    vi.mocked(startIncidentCase).mockResolvedValue(opened)
    const wrapper = await mountedPage()

    await wrapper.findAll('.bot-picker input[type="radio"]')[1].setValue(true)
    await wrapper.get('[data-action="restart-case"]').trigger('click')

    const dialog = wrapper.get('[role="dialog"]')
    expect(dialog.text()).toContain('开启新一轮')
    expect(dialog.text()).toContain(terminal.id)
    expect(dialog.text()).toContain('base-prod|codex')
    expect(dialog.text()).toContain('prod')
    expect(startIncidentCase).not.toHaveBeenCalled()
    expect(resetIncidentCaseWithWarnings).not.toHaveBeenCalled()

    await wrapper.findAll('.bot-picker input[type="radio"]')[0].setValue(true)
    expect(dialog.text()).toContain('base-prod|codex')
    await dialog.get('[data-reset-confirm]').trigger('click')
    await flushPromises()

    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({
      case_id: expect.stringMatching(/^case-reset-case-terminal-/),
      bot_key: 'base-prod|codex',
      bot_environment: 'prod',
      input_json: expect.objectContaining({ target_environment: 'prod' }),
    }))
    expect(resetIncidentCaseWithWarnings).not.toHaveBeenCalled()
  })

  it.each(['cancel', 'escape'] as const)('does not write a terminal new round after %s', async dismissal => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const terminal = incident('case-terminal-cancel', 'fixed_verified', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([terminal])
    mockCaseDetails(detail(terminal))
    const wrapper = await mountedPage()

    await wrapper.get('[data-action="restart-case"]').trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    if (dismissal === 'cancel') await dialog.get('[data-reset-cancel]').trigger('click')
    else await wrapper.get('.reset-dialog-backdrop').trigger('keydown', { key: 'Escape' })
    await wrapper.vm.$nextTick()

    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(startIncidentCase).not.toHaveBeenCalled()
    expect(resetIncidentCaseWithWarnings).not.toHaveBeenCalled()
  })

  it('uses the recovered legacy Bot object for both key and environment without an explicit selection', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    vi.mocked(matchBugBots).mockResolvedValue([prodBotMatch, botMatch])
    const archived = incident('legacy-1', 'legacy_archived', '2026-07-13T00:00:00Z', { selected_bot_key: '' })
    const opened = incident('case-new', 'validating', '2026-07-13T00:01:00Z', { selected_bot_key: 'base|codex', environment: 'test', version: 1 })
    const snapshot = detail(archived, {
      attempts: [{ id: 'legacy-attempt', case_id: 'legacy-1', cycle_number: 1, phase: 'legacy', mode: '', status: 'succeeded', agent_target: '', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '2026-07-11T00:00:00Z', error_code: '', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([archived])
    mockCaseDetails(snapshot, detail(opened))
    vi.mocked(startIncidentCase).mockResolvedValue(opened)
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    expect(startIncidentCase).not.toHaveBeenCalled()
    await wrapper.get('[data-reset-confirm]').trigger('click')
    await flushPromises()

    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({
      bot_key: 'base|codex',
      bot_environment: 'test',
      input_json: expect.objectContaining({ target_environment: 'test' }),
    }))
  })

  it('validates restart against the Bot visibly recovered for a legacy archive', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    vi.mocked(matchBugBots).mockResolvedValue([prodBotMatch, { ...botMatch, bot: { ...botMatch.bot, env: '' } }])
    const archived = incident('legacy-visible', 'legacy_archived', '2026-07-13T00:00:00Z', { selected_bot_key: '' })
    vi.mocked(listIncidentCases).mockResolvedValue([archived])
    mockCaseDetails(detail(archived, {
      attempts: [{ id: 'legacy-attempt', case_id: archived.id, cycle_number: 1, phase: 'legacy', mode: '', status: 'succeeded', agent_target: '', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '2026-07-11T00:00:00Z', error_code: '', error_message: '', usage: {} }],
    }))

    const wrapper = await mountedPage()

    expect(wrapper.findAll<HTMLInputElement>('.bot-picker input[type="radio"]')[1].element.checked).toBe(true)
    expect(wrapper.get<HTMLButtonElement>('[data-action="restart-case"]').element.disabled).toBe(true)
    expect(wrapper.get('.bot-action-disabled-reason').text()).toContain('缺少目标环境')
  })

  it('keeps one canonical Bot when an active Case and displayed legacy history coexist', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([{ ...bugA, selected_bot_key: 'base-prod|claude-code' }])
    vi.mocked(matchBugBots).mockResolvedValue([botMatch, replacementBotMatch])
    const active = incident('case-active-canonical', 'waiting_evidence', '2026-07-12T00:00:00Z', { current_attempt_id: 'active-attempt' })
    const archived = incident('case-legacy-canonical', 'legacy_archived', '2026-07-13T00:00:00Z', { selected_bot_key: 'base|codex' })
    vi.mocked(listIncidentCases).mockResolvedValue([archived, active])
    const activeSnapshot = detail(active, {
      attempts: [{ id: 'active-attempt', case_id: active.id, cycle_number: 1, phase: 'investigation', mode: 'reproduce', status: 'running', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '2026-07-12T00:00:00Z', error_code: '', error_message: '', usage: {} }],
    })
    mockCaseDetails(activeSnapshot, detail(archived, {
      attempts: [{ id: 'legacy-attempt', case_id: archived.id, cycle_number: 1, phase: 'legacy', mode: '', status: 'succeeded', agent_target: '', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '2026-07-11T00:00:00Z', error_code: '', error_message: '', usage: {} }],
    }))
    const pendingReset = deferred<{ case: IncidentCase; warnings: [] }>()
    vi.mocked(resetIncidentCaseWithWarnings).mockReturnValue(pendingReset.promise)
    const wrapper = await mountedPage()

    await wrapper.findAll('.case-row')[0].trigger('click')
    await flushPromises()
    expect(wrapper.get('.case-heading').text()).toContain(archived.id)
    const radios = wrapper.findAll<HTMLInputElement>('.bot-picker input[type="radio"]')
    expect(radios[0].element.checked).toBe(false)
    expect(radios[1].element.checked).toBe(true)
    expect(wrapper.get<HTMLButtonElement>('[data-action="restart-case"]').element.disabled).toBe(false)

    vi.mocked(getIncidentCase).mockClear()
    await wrapper.get('[data-action="restart-case"]').trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    expect(getIncidentCase).toHaveBeenCalledWith(active.id)
    expect(wrapper.get('.case-heading').text()).toContain(archived.id)
    expect(dialog.text()).toContain('investigation')
    expect(dialog.text()).toContain('active-attempt')
    expect(dialog.text()).not.toContain('无活动阶段')
    expect(dialog.text()).toContain('base-prod|claude-code')
    expect(dialog.text()).toContain('prod')
    await dialog.get('[data-reset-confirm]').trigger('click')

    expect(resetIncidentCaseWithWarnings).toHaveBeenCalledWith(expect.objectContaining({
      case_id: active.id,
      bot_key: 'base-prod|claude-code',
    }))
  })

  it('keeps historical detail visible and reports a recoverable error when the restart target detail cannot load', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const active = incident('case-active-load-failure', 'waiting_evidence', '2026-07-12T00:00:00Z', { current_attempt_id: 'active-attempt' })
    const archived = incident('case-history-load-failure', 'fixed_verified', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([archived, active])
    mockCaseDetails(detail(active), detail(archived))
    const wrapper = await mountedPage()

    await wrapper.findAll('.case-row')[0].trigger('click')
    await flushPromises()
    expect(wrapper.get('.case-heading').text()).toContain(archived.id)
    vi.mocked(getIncidentCase).mockClear()
    vi.mocked(getIncidentCase).mockImplementation(async caseID => {
      if (caseID === active.id) throw new Error('active detail unavailable')
      return detail(archived)
    })

    await wrapper.get('[data-action="restart-case"]').trigger('click')
    await flushPromises()

    expect(getIncidentCase).toHaveBeenCalledWith(active.id)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.get('.case-heading').text()).toContain(archived.id)
    expect(wrapper.get('.live-error').text()).toContain('active detail unavailable')
    expect(notifications.toastError).toHaveBeenCalledWith('读取重启目标 Case', expect.any(Error))
    expect(startIncidentCase).not.toHaveBeenCalled()
    expect(resetIncidentCaseWithWarnings).not.toHaveBeenCalled()
  })

  it('lets an explicit Bot override legacy recovery for both key and environment', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    vi.mocked(matchBugBots).mockResolvedValue([botMatch, prodBotMatch])
    const archived = incident('legacy-1', 'legacy_archived', '2026-07-13T00:00:00Z', { selected_bot_key: 'base|codex' })
    const opened = incident('case-new', 'validating', '2026-07-13T00:01:00Z', { selected_bot_key: 'base-prod|codex', environment: 'prod', version: 1 })
    vi.mocked(listIncidentCases).mockResolvedValue([archived])
    mockCaseDetails(detail(archived), detail(opened))
    vi.mocked(startIncidentCase).mockResolvedValue(opened)
    const wrapper = await mountedPage()

    await wrapper.findAll('.bot-picker input[type="radio"]')[1].setValue(true)
    await wrapper.get('.primary-action').trigger('click')
    expect(startIncidentCase).not.toHaveBeenCalled()
    await wrapper.get('[data-reset-confirm]').trigger('click')
    await flushPromises()

    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({
      bot_key: 'base-prod|codex',
      input_json: expect.objectContaining({ target_environment: 'prod' }),
    }))
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

  it('confirms a legacy lifecycle continuation with the snapshotted Bot before starting a fresh Case round', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    vi.mocked(matchBugBots).mockResolvedValue([botMatch, prodBotMatch])
    const archived = incident('legacy-1', 'legacy_archived', '2026-07-13T00:00:00Z', { source: 'legacy-runs-json', version: 3 })
    const opened = incident('case-new', 'validating', '2026-07-13T00:01:00Z', { version: 1, selected_bot_key: 'base-prod|codex', environment: 'prod' })
    vi.mocked(listIncidentCases).mockResolvedValue([archived])
    mockCaseDetails(detail(archived), detail(opened))
    vi.mocked(startIncidentCase).mockResolvedValue(opened)
    const wrapper = await mountedPage()

    await wrapper.findAll('.bot-picker input[type="radio"]')[1].setValue(true)
    await wrapper.get('.primary-action').trigger('click')

    const dialog = wrapper.get('[role="dialog"]')
    expect(dialog.text()).toContain('开启新一轮')
    expect(dialog.text()).toContain(archived.id)
    expect(dialog.text()).toContain('base-prod|codex')
    expect(dialog.text()).toContain('prod')
    expect(startIncidentCase).not.toHaveBeenCalled()
    expect(resetIncidentCaseWithWarnings).not.toHaveBeenCalled()

    await wrapper.findAll('.bot-picker input[type="radio"]')[0].setValue(true)
    expect(dialog.text()).toContain('base-prod|codex')
    await dialog.get('[data-reset-confirm]').trigger('click')
    await flushPromises()

    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({
      bug_id: 'bug-a', expected_version: 0, bot_key: 'base-prod|codex', bot_environment: 'prod', actor_id: 'desktop-user',
    }))
    expect(vi.mocked(startIncidentCase).mock.calls[0][0].case_id).not.toBe('legacy-1')
    expect(resetIncidentCaseWithWarnings).not.toHaveBeenCalled()
  })

  it.each(['cancel', 'escape'] as const)('does not write a legacy lifecycle continuation after %s', async dismissal => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const archived = incident('legacy-lifecycle-cancel', 'legacy_archived', '2026-07-13T00:00:00Z', { source: 'legacy-runs-json', version: 3 })
    vi.mocked(listIncidentCases).mockResolvedValue([archived])
    mockCaseDetails(detail(archived))
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    if (dismissal === 'cancel') await dialog.get('[data-reset-cancel]').trigger('click')
    else await wrapper.get('.reset-dialog-backdrop').trigger('keydown', { key: 'Escape' })
    await wrapper.vm.$nextTick()

    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(startIncidentCase).not.toHaveBeenCalled()
    expect(resetIncidentCaseWithWarnings).not.toHaveBeenCalled()
  })

  it('keeps a displayed legacy lifecycle continuation in terminal-new-round mode when an active Case also exists', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    vi.mocked(matchBugBots).mockResolvedValue([botMatch])
    const active = incident('active-alongside-legacy', 'waiting_evidence', '2026-07-12T00:00:00Z')
    const archived = incident('displayed-legacy', 'legacy_archived', '2026-07-13T00:00:00Z', { source: 'legacy-runs-json', version: 3 })
    const opened = incident('case-new-from-legacy', 'validating', '2026-07-13T00:01:00Z', { version: 1 })
    vi.mocked(listIncidentCases).mockResolvedValue([archived, active])
    mockCaseDetails(detail(active), detail(archived), detail(opened))
    vi.mocked(startIncidentCase).mockResolvedValue(opened)
    const wrapper = await mountedPage()

    await wrapper.findAll('.case-row')[0].trigger('click')
    await flushPromises()
    expect(wrapper.get('.case-heading').text()).toContain(archived.id)
    await wrapper.get('.primary-action').trigger('click')

    const dialog = wrapper.get('[role="dialog"]')
    expect(dialog.text()).toContain('开启新一轮')
    expect(dialog.text()).toContain(archived.id)
    expect(dialog.text()).not.toContain(active.id)
    expect(startIncidentCase).not.toHaveBeenCalled()
    expect(resetIncidentCaseWithWarnings).not.toHaveBeenCalled()

    await dialog.get('[data-reset-confirm]').trigger('click')
    await flushPromises()

    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({ bug_id: 'bug-a', expected_version: 0 }))
    expect(resetIncidentCaseWithWarnings).not.toHaveBeenCalled()
  })

  it('submits fix approval with the dialog-captured root cause and exact Case version key', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-1', 'waiting_fix_approval', '2026-07-13T00:00:00Z')
    const snapshot = detail(item, {
      attempts: [{ id: 'attempt-1', case_id: 'case-1', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { investigation_status: 'root_cause_ready' }, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(snapshot)
    vi.mocked(approveIncidentFix).mockResolvedValue({ ...item, status: 'fixing', version: 8 })
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    await wrapper.get('[data-confirm]').trigger('click')
    await flushPromises()

    expect(approveIncidentFix).toHaveBeenCalledWith(expect.objectContaining({
      case_id: 'case-1', expected_version: 7, root_cause_attempt_id: 'attempt-1', idempotency_key: 'start-fix:case-1:attempt-1:7', actor_id: 'desktop-user',
    }))
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
    expect(dialog.text()).toContain('不会撤销已发生的提交、推送或部署')
    expect(dialog.text()).toContain('原 Case、证据和审计记录保持不可变')
    expect(dialog.text()).toContain('base|codex')
    expect(dialog.text()).toContain('test')
    expect(dialog.text()).toContain('base-prod|claude-code')
    expect(dialog.text()).toContain('prod')
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
    expect(startIncidentCase).not.toHaveBeenCalled()
    expect(continueIncidentCase).not.toHaveBeenCalled()
    expect(wrapper.get('.case-heading').text()).toContain('case-reset-replacement')
    expect(getIncidentCase).toHaveBeenLastCalledWith('case-reset-replacement')
    expect(scrollIntoView).toHaveBeenLastCalledWith({ behavior: 'smooth', block: 'start' })
    const primary = wrapper.get<HTMLButtonElement>('.primary-action')
    expect(primary.element.disabled).toBe(false)
    expect(document.activeElement).toBe(primary.element)
    wrapper.unmount()
  })

  it('snapshots phase, status, current attempt and bound Agent before confirming reset', async () => {
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

    expect(dialog.text()).toContain('waiting_evidence')
    expect(dialog.text()).toContain('investigation')
    expect(dialog.text()).toContain('attempt-investigation')
    expect(dialog.text()).toContain('base|codex')
    expect(dialog.text()).toContain('bug-a')
    expect(dialog.text()).toContain('当前 Agent 将被停止')
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
    expect(secondDialog.text()).toContain('v8')
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
    expect(wrapper.get('.case-heading').text()).toContain(replacement.id)
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true)

    pendingReset.resolve({ case: replacement, warnings: [] })
    await flushPromises()
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.get('.case-heading').text()).toContain(replacement.id)
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
    expect(wrapper.get('.case-heading').text()).toContain(replacement.id)

    pendingReset.reject(new Error('validation phase schedule failed'))
    await flushPromises()
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('[role="dialog"]').exists()).toBe(false)
    expect(wrapper.get('.case-heading').text()).toContain(replacement.id)
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
    expect(wrapper.get<HTMLButtonElement>('[data-action="enter-case"]').element.disabled).toBe(false)
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

    expect(wrapper.get('.case-heading').text()).toContain('case-b')
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

    expect(wrapper.get('.case-heading').text()).toContain('case-b')
    expect(wrapper.text()).not.toContain('stale reset failed')
    expect(notifications.toastError).not.toHaveBeenCalled()
  })

  it('does not apply a delayed primary-action completion after another Bug and Case are selected', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA, bugB])
    const caseA = incident('case-a', 'waiting_fix_approval', '2026-07-13T00:00:00Z')
    const caseB = incident('case-b', 'waiting_evidence', '2026-07-13T00:00:00Z', { bug_id: 'bug-b', version: 3 })
    const snapshotA = detail(caseA, {
      attempts: [{ id: 'attempt-1', case_id: 'case-a', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }],
    })
    const snapshotB = detail(caseB)
    vi.mocked(listIncidentCases).mockResolvedValue([caseA, caseB])
    mockCaseDetails(snapshotA, snapshotB)
    const pendingApproval = deferred<IncidentCase>()
    vi.mocked(approveIncidentFix).mockReturnValue(pendingApproval.promise)
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    await wrapper.get('[data-confirm]').trigger('click')
    await wrapper.get('[data-ticket-id="bug-b"]').trigger('click')
    await flushPromises()
    await flushPromises()
    expect(wrapper.get('.case-heading').text()).toContain('case-b')
    vi.mocked(getIncidentCase).mockClear()
    notifications.success.mockClear()

    pendingApproval.resolve({ ...caseA, status: 'fixing', version: 8 })
    await flushPromises()
    await flushPromises()

    expect(getIncidentCase).not.toHaveBeenCalled()
    expect(wrapper.get('.case-heading').text()).toContain('case-b')
    expect(notifications.success).not.toHaveBeenCalled()
  })

  it('does not expose a delayed primary-action error after another Bug and Case are selected', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA, bugB])
    const caseA = incident('case-a', 'waiting_fix_approval', '2026-07-13T00:00:00Z')
    const caseB = incident('case-b', 'waiting_evidence', '2026-07-13T00:00:00Z', { bug_id: 'bug-b', version: 3 })
    const snapshotA = detail(caseA, {
      attempts: [{ id: 'attempt-1', case_id: 'case-a', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([caseA, caseB])
    mockCaseDetails(snapshotA, detail(caseB))
    const pendingApproval = deferred<IncidentCase>()
    vi.mocked(approveIncidentFix).mockReturnValue(pendingApproval.promise)
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    await wrapper.get('[data-confirm]').trigger('click')
    await wrapper.get('[data-ticket-id="bug-b"]').trigger('click')
    await flushPromises()
    await flushPromises()
    notifications.toastError.mockClear()

    pendingApproval.reject(new Error('stale approval failed'))
    await flushPromises()
    await flushPromises()

    expect(wrapper.get('.case-heading').text()).toContain('case-b')
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
    await wrapper.get('[role="dialog"] textarea').setValue('人工确认已处理')
    await wrapper.get('[data-confirm]').trigger('click')
    await flushPromises()

    expect(continueIncidentCase).toHaveBeenCalledWith(expect.objectContaining({ phase, input_json: expect.objectContaining({ decision, evidence: '人工确认已处理' }) }))
    expect(forbidden).not.toHaveBeenCalled()
  })

  it('recovers an empty migrated selected_bot_key from the latest legacy attempt', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-1', 'legacy_archived', '2026-07-13T00:00:00Z', { selected_bot_key: '' })
    const snapshot = detail(item, {
      attempts: [{ id: 'legacy-attempt', case_id: 'case-1', cycle_number: 1, phase: 'legacy', mode: '', status: 'succeeded', agent_target: '', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '2026-07-11T00:00:00Z', error_code: '', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    const opened = incident('new-case', 'validating', '2026-07-13T00:01:00Z', { version: 1 })
    mockCaseDetails(snapshot, detail(opened))
    vi.mocked(startIncidentCase).mockResolvedValue(opened)
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    expect(startIncidentCase).not.toHaveBeenCalled()
    await wrapper.get('[data-reset-confirm]').trigger('click')
    await flushPromises()

    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({ bot_key: 'base|codex' }))
  })

  it('requires explicit bot reselection for an unbound archive before starting a new round', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-1', 'legacy_archived', '2026-07-13T00:00:00Z', { selected_bot_key: '' })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    const opened = incident('new-case', 'validating', '2026-07-13T00:01:00Z', { version: 1 })
    mockCaseDetails(detail(item), detail(opened))
    vi.mocked(startIncidentCase).mockResolvedValue(opened)
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    await flushPromises()
    expect(startIncidentCase).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('重新选择当前 Bug 的机器人')

    await wrapper.get('.bot-picker input[type="radio"]').setValue(true)
    await wrapper.get('.primary-action').trigger('click')
    expect(startIncidentCase).not.toHaveBeenCalled()
    await wrapper.get('[data-reset-confirm]').trigger('click')
    await flushPromises()
    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({ bot_key: 'base|codex' }))
  })
})
