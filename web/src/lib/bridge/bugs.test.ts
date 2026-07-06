import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  cancelBugInvestigation,
  clearBugPlatformLogin,
  deleteBugPlatform,
  fetchBugByID,
  listBugInvestigationRuns,
  listBugs,
  loginBugPlatform,
  matchBugBots,
  saveBugPlatform,
  startBugInvestigation,
  syncBugPlatform,
} from './bugs'

afterEach(() => {
  vi.restoreAllMocks()
  delete (window as any).go
})

describe('bug bridge', () => {
  it('returns [] in browser mode for listBugs', async () => {
    delete (window as any).go
    expect(await listBugs()).toEqual([])
  })

  it('forwards saveBugPlatform to Wails in desktop mode', async () => {
    const spy = vi.fn().mockResolvedValue({ id: 'zentao-main', name: '禅道' })
    ;(window as any).go = { main: { App: { SaveBugPlatform: spy } } }

    const result = await saveBugPlatform({
      name: '禅道',
      type: 'zentao',
      account: 'xiaolong',
      auth_mode: 'session_header',
      session_header: 'Cookie: sid=1',
      enabled: true,
      poll_enabled: true,
      poll_interval_minutes: 15,
    })

    expect(spy).toHaveBeenCalledWith({
      name: '禅道',
      type: 'zentao',
      account: 'xiaolong',
      auth_mode: 'session_header',
      session_header: 'Cookie: sid=1',
      enabled: true,
      poll_enabled: true,
      poll_interval_minutes: 15,
    })
    expect(result.id).toBe('zentao-main')
  })

  it('forwards syncBugPlatform to Wails in desktop mode', async () => {
    const spy = vi.fn().mockResolvedValue({ platform_id: 'zentao-main', stored: 1 })
    ;(window as any).go = { main: { App: { SyncBugPlatform: spy } } }

    const result = await syncBugPlatform('zentao-main')

    expect(spy).toHaveBeenCalledWith('zentao-main')
    expect(result.stored).toBe(1)
  })

  it('forwards fetchBugByID to Wails in desktop mode', async () => {
    const spy = vi.fn().mockResolvedValue({ platform_id: 'zentao-main', selected_bug_id: 'zentao-1842' })
    ;(window as any).go = { main: { App: { FetchBugByID: spy } } }

    const result = await fetchBugByID({ platform_id: 'zentao-main', bug_id: '#1842' })

    expect(spy).toHaveBeenCalledWith({ platform_id: 'zentao-main', bug_id: '#1842' })
    expect(result.selected_bug_id).toBe('zentao-1842')
  })

  it('forwards loginBugPlatform to Wails in desktop mode', async () => {
    const spy = vi.fn().mockResolvedValue({ platform_id: 'zentao-main', session_saved: true, cookie_count: 2 })
    ;(window as any).go = { main: { App: { LoginBugPlatform: spy } } }

    const result = await loginBugPlatform({ platform_id: 'zentao-main' })

    expect(spy).toHaveBeenCalledWith({ platform_id: 'zentao-main' })
    expect(result.session_saved).toBe(true)
  })

  it('forwards clearBugPlatformLogin to Wails in desktop mode', async () => {
    const spy = vi.fn().mockResolvedValue({ platform_id: 'zentao-main', session_saved: false })
    ;(window as any).go = { main: { App: { ClearBugPlatformLogin: spy } } }

    const result = await clearBugPlatformLogin({ platform_id: 'zentao-main' })

    expect(spy).toHaveBeenCalledWith({ platform_id: 'zentao-main' })
    expect(result.session_saved).toBe(false)
  })

  it('forwards deleteBugPlatform to Wails in desktop mode', async () => {
    const spy = vi.fn().mockResolvedValue(undefined)
    ;(window as any).go = { main: { App: { DeleteBugPlatform: spy } } }

    await deleteBugPlatform({ platform_id: 'zentao-main' })

    expect(spy).toHaveBeenCalledWith({ platform_id: 'zentao-main' })
  })

  it('normalizes null bot match reasons from Wails', async () => {
    const spy = vi.fn().mockResolvedValue([
      { bot: { key: 'shop-prod', system_id: 'shop', target: 'prod', path: '/bots/shop' }, score: 10, reasons: null },
    ])
    ;(window as any).go = { main: { App: { MatchBugBots: spy } } }

    const result = await matchBugBots('zentao-1842')

    expect(spy).toHaveBeenCalledWith('zentao-1842')
    expect(result[0].reasons).toEqual([])
  })

  it('forwards startBugInvestigation to Wails in desktop mode', async () => {
    const spy = vi.fn().mockResolvedValue({ id: 'run-1', status: 'running' })
    ;(window as any).go = { main: { App: { StartBugInvestigation: spy } } }
    const input = { bug_id: 'zentao-577', bot: { key: 'base|codex', target: 'codex', path: '/repo', system_id: 'base' } }

    const result = await startBugInvestigation(input)

    expect(spy).toHaveBeenCalledWith(input)
    expect(result.status).toBe('running')
  })

  it('forwards cancelBugInvestigation to Wails in desktop mode', async () => {
    const spy = vi.fn().mockResolvedValue(undefined)
    ;(window as any).go = { main: { App: { CancelBugInvestigation: spy } } }

    await cancelBugInvestigation({ run_id: 'run-1' })

    expect(spy).toHaveBeenCalledWith({ run_id: 'run-1' })
  })

  it('returns [] for listBugInvestigationRuns in browser mode', async () => {
    delete (window as any).go
    await expect(listBugInvestigationRuns('zentao-577')).resolves.toEqual([])
  })

  it('normalizes null investigation events from Wails', async () => {
    const spy = vi.fn().mockResolvedValue([
      { id: 'run-1', bug_id: 'zentao-577', bot_key: 'base|codex', status: 'running', events: null },
    ])
    ;(window as any).go = { main: { App: { ListBugInvestigationRuns: spy } } }

    const result = await listBugInvestigationRuns('zentao-577')

    expect(spy).toHaveBeenCalledWith('zentao-577')
    expect(result[0].events).toEqual([])
  })
})
