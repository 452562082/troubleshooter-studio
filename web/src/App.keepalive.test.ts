import { mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App.vue'
import IncidentWorkbenchPage from './pages/IncidentWorkbenchPage.vue'
import {
  getIncidentCase,
  listBugs,
  listIncidentCases,
} from './lib/bridge'

vi.mock('../wailsjs/runtime/runtime', () => ({ EventsOn: vi.fn(() => vi.fn()) }))
vi.mock('./lib/bridge', () => ({
  approveIncidentFix: vi.fn(),
  approveIncidentMerge: vi.fn(),
  ackIncidentWorkflowReminder: vi.fn(),
  cancelIncidentAttempt: vi.fn(),
  completeIncidentRemediation: vi.fn(),
  continueIncidentCase: vi.fn(),
  fetchBugByID: vi.fn(),
  getIncidentCase: vi.fn(),
  listBugs: vi.fn(),
  listIncidentCases: vi.fn(),
  listPendingIncidentWorkflowReminders: vi.fn().mockResolvedValue([]),
  matchBugBots: vi.fn().mockResolvedValue([]),
  notifyIncidentDeployed: vi.fn(),
  resetIncidentCase: vi.fn(),
  saveBugSelectedBot: vi.fn(),
  startIncidentCase: vi.fn(),
}))
vi.mock('./lib/toast', () => ({
  toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() },
  toastError: vi.fn(),
}))
vi.mock('./lib/logStore', () => ({
  setupGlobalLogBridges: vi.fn(),
  useLogStore: () => ({ count: { value: 0 } }),
  pushLog: vi.fn(),
}))

const bugA = { id: 'bug-a', source: 'zentao', source_id: '1', title: '支付页超时', env: 'test', system_id: 'base' }
const bugB = { id: 'bug-b', source: 'zentao', source_id: '2', title: '缓存命中下降', env: 'prod', system_id: 'base' }
const caseA = { id: 'case-a', bug_id: 'bug-a', source: 'zentao', system_id: 'base', environment: 'test', status: 'waiting_evidence', cycle_number: 1, current_attempt_id: 'attempt-a', selected_bot_key: 'base|codex', version: 2, created_at: '2026-07-12T00:00:00Z', updated_at: '2026-07-12T00:00:00Z' }
const caseB = { ...caseA, id: 'case-b', bug_id: 'bug-b', environment: 'prod', current_attempt_id: 'attempt-b', updated_at: '2026-07-13T00:00:00Z' }

const BugsPage = defineComponent({ template: '<div data-page="bugs">Bug inbox route</div>' })
const DummyPage = defineComponent({ template: '<div data-page="dummy" />' })

function detail(item: typeof caseA | typeof caseB) {
  return { case: item, attempts: [], artifacts: [], approvals: [], code_changes: [], deployment_observations: [], events: [] }
}

function testRouter() {
  return createRouter({
    history: createMemoryHistory(),
    routes: [
      { path: '/', component: DummyPage },
      { path: '/bugs', component: BugsPage },
      { path: '/incidents', component: IncidentWorkbenchPage },
      { path: '/bots', component: DummyPage },
      { path: '/init', component: DummyPage },
      { path: '/editor', component: DummyPage },
      { path: '/analyze', component: DummyPage },
      { path: '/logs', component: DummyPage },
    ],
  })
}

function mountTestApp(router: ReturnType<typeof testRouter>) {
  return mount(App, { attachTo: document.body, global: { plugins: [router], stubs: { ToastContainer: true } } })
}

async function flushRouteWork() {
  for (let index = 0; index < 6; index += 1) await new Promise(resolve => setTimeout(resolve, 0))
}

afterEach(() => vi.clearAllMocks())

describe('App keep-alive incident route synchronization', () => {
  it('tracks an external bug_id query change while the incident route stays active', async () => {
    vi.mocked(listBugs).mockResolvedValue([bugA, bugB] as any)
    vi.mocked(listIncidentCases).mockResolvedValue([caseA, caseB] as any)
    vi.mocked(getIncidentCase).mockImplementation(async id => detail(id === 'case-b' ? caseB : caseA) as any)
    const router = testRouter()
    await router.push('/incidents?bug_id=bug-a')
    const wrapper = mountTestApp(router)
    await router.isReady()
    await flushRouteWork()

    await router.push('/incidents?bug_id=bug-b')
    await flushRouteWork()

    expect(wrapper.get('[data-ticket-id="bug-b"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.get('.incident-bug-summary h2').text()).toBe('缓存命中下降')
    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe('case-b')
    expect(wrapper.get('.case-heading').text()).toContain('缓存命中下降')
    expect(router.currentRoute.value.query.bug_id).toBe('bug-b')
    wrapper.unmount()
  })

  it('does not loop when a ticket selection updates the query with router.replace', async () => {
    vi.mocked(listBugs).mockResolvedValue([bugA, bugB] as any)
    vi.mocked(listIncidentCases).mockResolvedValue([caseA, caseB] as any)
    vi.mocked(getIncidentCase).mockImplementation(async id => detail(id === 'case-b' ? caseB : caseA) as any)
    const router = testRouter()
    await router.push('/incidents?bug_id=bug-a')
    const wrapper = mountTestApp(router)
    await router.isReady()
    await flushRouteWork()
    const replace = vi.spyOn(router, 'replace')

    await wrapper.get('[data-ticket-id="bug-b"]').trigger('click')
    await flushRouteWork()

    expect(replace).toHaveBeenCalledTimes(1)
    expect(router.currentRoute.value.query.bug_id).toBe('bug-b')
    expect(wrapper.get('[data-ticket-id="bug-b"]').attributes('aria-pressed')).toBe('true')
    wrapper.unmount()
  })

  it('reactivates the cached incident instance with the exact new Bug and matching Case', async () => {
    vi.mocked(listBugs).mockResolvedValue([bugA, bugB] as any)
    vi.mocked(listIncidentCases).mockResolvedValue([caseA, caseB] as any)
    vi.mocked(getIncidentCase).mockImplementation(async id => detail(id === 'case-b' ? caseB : caseA) as any)
    const router = testRouter()
    await router.push('/incidents?bug_id=bug-a')
    const wrapper = mountTestApp(router)
    await router.isReady()
    await flushRouteWork()

    expect(wrapper.get('[data-ticket-id="bug-a"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe('case-a')
    expect(wrapper.get('.case-heading').text()).toContain('支付页超时')

    await router.push('/bugs')
    await flushRouteWork()
    expect(wrapper.find('[data-page="bugs"]').exists()).toBe(true)

    await router.push('/incidents?bug_id=bug-b')
    await flushRouteWork()

    expect(wrapper.get('[data-ticket-id="bug-b"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.get('[data-ticket-id="bug-a"]').attributes('aria-pressed')).toBe('false')
    expect(wrapper.get('.incident-bug-summary h2').text()).toBe('缓存命中下降')
    expect(wrapper.get('.case-heading').attributes('data-case-id')).toBe('case-b')
    expect(wrapper.get('.case-heading').text()).toContain('缓存命中下降')
    expect(router.currentRoute.value.query.bug_id).toBe('bug-b')
    wrapper.unmount()
  })

  it('reloads the cached Bug list and moves archived Bugs out of the current view', async () => {
    vi.mocked(listBugs)
      .mockResolvedValueOnce([bugA, bugB] as any)
      .mockResolvedValue([{ ...bugA, inbox_state: 'active' }, { ...bugB, inbox_state: 'history', status: 'resolved' }] as any)
    vi.mocked(listIncidentCases).mockResolvedValue([caseA, caseB] as any)
    vi.mocked(getIncidentCase).mockImplementation(async id => detail(id === 'case-b' ? caseB : caseA) as any)
    const router = testRouter()
    await router.push('/incidents?bug_id=bug-a')
    const wrapper = mountTestApp(router)
    await router.isReady()
    await flushRouteWork()

    expect(wrapper.find('[data-ticket-id="bug-b"]').exists()).toBe(true)

    await router.push('/bugs')
    await flushRouteWork()
    await router.push('/incidents?bug_id=bug-a')
    await flushRouteWork()

    expect(listBugs).toHaveBeenCalledTimes(2)
    expect(wrapper.get('[data-ticket-view="active"]').text()).toContain('1')
    expect(wrapper.get('[data-ticket-view="history"]').text()).toContain('1')
    expect(wrapper.find('[data-ticket-id="bug-b"]').exists()).toBe(false)
    expect(wrapper.get('[data-ticket-id="bug-a"]').attributes('aria-pressed')).toBe('true')
    wrapper.unmount()
  })

  it('reactivates an invalid query as a recoverable empty selection without guessing', async () => {
    vi.mocked(listBugs).mockResolvedValue([bugA, bugB] as any)
    vi.mocked(listIncidentCases).mockResolvedValue([caseA, caseB] as any)
    vi.mocked(getIncidentCase).mockImplementation(async id => detail(id === 'case-b' ? caseB : caseA) as any)
    const router = testRouter()
    await router.push('/incidents?bug_id=bug-a')
    const wrapper = mountTestApp(router)
    await router.isReady()
    await flushRouteWork()

    await router.push('/bugs')
    await router.push('/incidents?bug_id=missing-bug')
    await flushRouteWork()

    expect(wrapper.get('.invalid-bug-state').text()).toContain('URL 中的 Bug 不存在')
    expect(wrapper.findAll('[data-ticket-id][aria-pressed="true"]')).toHaveLength(0)
    expect(wrapper.get('.ticket-summary-panel').text()).toContain('选择一条 Bug 开始故障闭环')
    expect(router.currentRoute.value.query.bug_id).toBe('missing-bug')
    wrapper.unmount()
  })
})
