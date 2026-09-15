import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import App from './App.vue'
import router from './router'
import { toast } from './lib/toast'

const runtime = vi.hoisted(() => ({ ready: true, handlers: {} as Record<string, (payload: any) => void>, unlisten: vi.fn(), EventsOn: vi.fn((name: string, handler: (payload: any) => void) => { runtime.handlers[name] = handler; return runtime.unlisten }) }))
const route = vi.hoisted(() => ({ path: '/', fullPath: '/' }))

vi.mock('../wailsjs/runtime/runtime', () => ({ EventsOn: runtime.EventsOn }))
vi.mock('vue-router', async () => {
  const actual = await vi.importActual<typeof import('vue-router')>('vue-router')
  return { ...actual, useRoute: () => route }
})
vi.mock('./lib/logStore', () => ({ hasWailsEventRuntime: () => runtime.ready, setupGlobalLogBridges: vi.fn(), useLogStore: () => ({ count: { value: 0 } }), pushLog: vi.fn() }))
vi.mock('./lib/toast', () => ({ toast: { info: vi.fn(), error: vi.fn(), success: vi.fn() } }))

const RouterLinkStub = {
  props: ['to'],
  template: '<a :href="to"><slot /></a>',
}

function mountApp() {
  return mount(App, { global: { stubs: { RouterLink: RouterLinkStub, RouterView: true, ToastContainer: true } } })
}

afterEach(() => {
  runtime.ready = true
  route.path = '/'; route.fullPath = '/'
  for (const key of Object.keys(runtime.handlers)) delete runtime.handlers[key]
  runtime.EventsOn.mockClear(); runtime.unlisten.mockClear()
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
    ['/incidents', '故障闭环', '选择 Bug，排障、修复和提交'],
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
