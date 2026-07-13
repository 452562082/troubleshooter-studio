import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import App from './App.vue'
import router from './router'
import { ackIncidentWorkflowReminder, listPendingIncidentWorkflowReminders } from './lib/bridge'
import { toast } from './lib/toast'

const runtime = vi.hoisted(() => ({ handlers: {} as Record<string, (payload: any) => void>, unlisten: vi.fn(), EventsOn: vi.fn((name: string, handler: (payload: any) => void) => { runtime.handlers[name] = handler; return runtime.unlisten }) }))
const route = vi.hoisted(() => ({ path: '/', fullPath: '/' }))

vi.mock('../wailsjs/runtime/runtime', () => ({ EventsOn: runtime.EventsOn }))
vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return { ...actual, useRoute: () => route }
})
vi.mock('./lib/logStore', () => ({ setupGlobalLogBridges: vi.fn(), useLogStore: () => ({ count: { value: 0 } }), pushLog: vi.fn() }))
vi.mock('./lib/bridge', () => ({ ackIncidentWorkflowReminder: vi.fn(), listPendingIncidentWorkflowReminders: vi.fn().mockResolvedValue([]) }))
vi.mock('./lib/toast', () => ({ toast: { info: vi.fn(), error: vi.fn(), success: vi.fn() } }))

function reminder() {
  return { case_id: 'case-1', bug_id: 'bug-1', environment: 'test', waiting_since: '2026-07-01T00:00:00Z', waiting_age: 90_000_000_000_000, sequence: 1, reservation_key: 'slot-1', delivery_attempt: 1 }
}

async function flush() { await new Promise(resolve => setTimeout(resolve, 0)) }

const RouterLinkStub = {
  props: ['to'],
  template: '<a :href="to"><slot /></a>',
}

function mountApp() {
  return mount(App, { global: { stubs: { RouterLink: RouterLinkStub, RouterView: true, ToastContainer: true } } })
}

afterEach(() => {
  route.path = '/'; route.fullPath = '/'
  for (const key of Object.keys(runtime.handlers)) delete runtime.handlers[key]
  runtime.EventsOn.mockClear(); runtime.unlisten.mockClear()
  vi.mocked(listPendingIncidentWorkflowReminders).mockReset().mockResolvedValue([])
  vi.mocked(ackIncidentWorkflowReminder).mockReset().mockResolvedValue(undefined)
  vi.mocked(toast.info).mockReset()
})

describe('App navigation', () => {
  it('registers the inbox and incident workbench as distinct route components', async () => {
    const bugMatches = router.resolve('/bugs').matched
    const incidentMatches = router.resolve('/incidents').matched
    const bugs = bugMatches[bugMatches.length - 1]
    const incidents = incidentMatches[incidentMatches.length - 1]

    expect(bugs?.name).toBe('Bugs')
    expect(incidents?.name).toBe('Incidents')

    const loadBugs = bugs?.components?.default as (() => Promise<{ default: { __name?: string } }>) | undefined
    const loadIncidents = incidents?.components?.default as (() => Promise<{ default: { __name?: string } }>) | undefined
    expect((await loadBugs?.())?.default.__name).toBe('BugInboxPage')
    expect((await loadIncidents?.())?.default.__name).toBe('IncidentWorkbenchPage')
  })

  it.each([
    ['/bugs', 'Bug 工单', '同步工单平台，查看完整 Bug 详情'],
    ['/incidents', '故障闭环', '选择 Bug，完成验证、排障、修复和回归'],
  ])('shows and exclusively highlights the %s sidebar entry', (path, label, desc) => {
    route.path = path; route.fullPath = path
    const wrapper = mountApp()
    const link = wrapper.get(`a[href="${path}"]`)

    expect(link.text()).toContain(label)
    expect(link.text()).toContain(desc)
    expect(link.classes()).toContain('active')
    expect(wrapper.findAll('.nav-link.active')).toHaveLength(1)
  })

  it('uses path-scoped keep-alive keys so the two workspaces cannot share an instance', () => {
    const source = readFileSync('src/App.vue', 'utf8')
    expect(source).toContain(`route.path === '/init' ? route.fullPath : route.path`)
  })
})

describe('App workflow reminder receiver', () => {
  it('acks a live reminder from the persistent root receiver', async () => {
    const wrapper = mountApp()
    await flush()
    runtime.handlers['incident-workflow:reminder'](reminder())
    await flush()
    expect(toast.info).toHaveBeenCalledWith('Bug bug-1 已等待人工部署超过 24 小时')
    expect(ackIncidentWorkflowReminder).toHaveBeenCalledWith({ case_id: 'case-1', reservation_key: 'slot-1', delivery_attempt: 1, actor_id: 'desktop-root' })
    wrapper.unmount()
    expect(runtime.unlisten).toHaveBeenCalled()
  })

  it('pulls and acknowledges an attempted reminder emitted before root mount', async () => {
    vi.mocked(listPendingIncidentWorkflowReminders).mockResolvedValue([reminder()] as any)
    mountApp()
    await flush()
    expect(toast.info).toHaveBeenCalledTimes(1)
    expect(ackIncidentWorkflowReminder).toHaveBeenCalledWith(expect.objectContaining({ reservation_key: 'slot-1', delivery_attempt: 1 }))
  })

  it('does not acknowledge when local notification display fails', async () => {
    vi.mocked(toast.info).mockImplementationOnce(() => { throw new Error('toast unavailable') })
    mountApp()
    await flush()
    runtime.handlers['incident-workflow:reminder'](reminder())
    await flush()
    expect(ackIncidentWorkflowReminder).not.toHaveBeenCalled()
  })

  it('deduplicates the same live event and late-mount pending item', async () => {
    vi.mocked(listPendingIncidentWorkflowReminders).mockResolvedValue([reminder()] as any)
    mountApp()
    runtime.handlers['incident-workflow:reminder'](reminder())
    await flush()
    expect(toast.info).toHaveBeenCalledTimes(1)
    expect(ackIncidentWorkflowReminder).toHaveBeenCalledTimes(1)
  })
})
