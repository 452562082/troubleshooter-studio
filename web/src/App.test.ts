import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App.vue'
import { ackIncidentWorkflowReminder, listPendingIncidentWorkflowReminders } from './lib/bridge'
import { toast } from './lib/toast'

const runtime = vi.hoisted(() => ({ handlers: {} as Record<string, (payload: any) => void>, unlisten: vi.fn(), EventsOn: vi.fn((name: string, handler: (payload: any) => void) => { runtime.handlers[name] = handler; return runtime.unlisten }) }))

vi.mock('../wailsjs/runtime/runtime', () => ({ EventsOn: runtime.EventsOn }))
vi.mock('vue-router', () => ({ useRoute: () => ({ path: '/' }) }))
vi.mock('./lib/logStore', () => ({ setupGlobalLogBridges: vi.fn(), useLogStore: () => ({ count: { value: 0 } }), pushLog: vi.fn() }))
vi.mock('./lib/bridge', () => ({ ackIncidentWorkflowReminder: vi.fn(), listPendingIncidentWorkflowReminders: vi.fn().mockResolvedValue([]) }))
vi.mock('./lib/toast', () => ({ toast: { info: vi.fn(), error: vi.fn(), success: vi.fn() } }))

function reminder() {
  return { case_id: 'case-1', bug_id: 'bug-1', environment: 'test', waiting_since: '2026-07-01T00:00:00Z', waiting_age: 90_000_000_000_000, sequence: 1, reservation_key: 'slot-1', delivery_attempt: 1 }
}

async function flush() { await new Promise(resolve => setTimeout(resolve, 0)) }

afterEach(() => {
  for (const key of Object.keys(runtime.handlers)) delete runtime.handlers[key]
  runtime.EventsOn.mockClear(); runtime.unlisten.mockClear()
  vi.mocked(listPendingIncidentWorkflowReminders).mockReset().mockResolvedValue([])
  vi.mocked(ackIncidentWorkflowReminder).mockReset().mockResolvedValue(undefined)
  vi.mocked(toast.info).mockReset()
})

describe('App workflow reminder receiver', () => {
  it('acks a live reminder from the persistent root receiver', async () => {
    const wrapper = mount(App, { global: { stubs: { RouterLink: true, RouterView: true, ToastContainer: true } } })
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
    mount(App, { global: { stubs: { RouterLink: true, RouterView: true, ToastContainer: true } } })
    await flush()
    expect(toast.info).toHaveBeenCalledTimes(1)
    expect(ackIncidentWorkflowReminder).toHaveBeenCalledWith(expect.objectContaining({ reservation_key: 'slot-1', delivery_attempt: 1 }))
  })

  it('does not acknowledge when local notification display fails', async () => {
    vi.mocked(toast.info).mockImplementationOnce(() => { throw new Error('toast unavailable') })
    mount(App, { global: { stubs: { RouterLink: true, RouterView: true, ToastContainer: true } } })
    await flush()
    runtime.handlers['incident-workflow:reminder'](reminder())
    await flush()
    expect(ackIncidentWorkflowReminder).not.toHaveBeenCalled()
  })

  it('deduplicates the same live event and late-mount pending item', async () => {
    vi.mocked(listPendingIncidentWorkflowReminders).mockResolvedValue([reminder()] as any)
    mount(App, { global: { stubs: { RouterLink: true, RouterView: true, ToastContainer: true } } })
    runtime.handlers['incident-workflow:reminder'](reminder())
    await flush()
    expect(toast.info).toHaveBeenCalledTimes(1)
    expect(ackIncidentWorkflowReminder).toHaveBeenCalledTimes(1)
  })
})
