import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// 重要:import 里 App 模块会 eagerly 读 window.go —— 但 Wails 生成的 App.js 只在
// 函数调用时访问,所以 import 本身不 throw。
import { analyzeServiceTopology, discoverBots, isDesktop, validate } from './bridge'

describe('bridge.isDesktop', () => {
  afterEach(() => {
    // 清理 window.go 状态
    delete (window as any).go
  })

  it('no window.go → false(浏览器模式)', () => {
    delete (window as any).go
    expect(isDesktop()).toBe(false)
  })

  it('有 window.go → true(桌面 app 模式)', () => {
    ;(window as any).go = { main: { App: {} } }
    expect(isDesktop()).toBe(true)
  })
})

describe('bridge.discoverBots', () => {
  beforeEach(() => {
    delete (window as any).go
  })
  afterEach(() => {
    delete (window as any).go
    vi.restoreAllMocks()
  })

  it('浏览器模式直接返回 []', async () => {
    const r = await discoverBots([])
    expect(r).toEqual([])
  })

  it('桌面模式转发到 window.go.main.App.DiscoverBots', async () => {
    const mockBot = { meta: { system_id: 'x', target: 'openclaw' }, path: '/a', mod_time: '' }
    const spy = vi.fn().mockResolvedValue([mockBot])
    ;(window as any).go = { main: { App: { DiscoverBots: spy } } }
    const r = await discoverBots(['/extra'])
    expect(spy).toHaveBeenCalledWith(['/extra'])
    expect(r).toHaveLength(1)
    expect(r[0].meta.system_id).toBe('x')
  })

  it('Go 返回 null(nil slice)时兜成 [],防 .length 崩(regression guard)', async () => {
    // 之前真实发生过的 bug:discover.Scan 没匹配到就 return nil slice,
    // 经 JSON 编码变 null,JS 这边 .length 崩页。bridge 加了 Array.isArray 兜底。
    ;(window as any).go = {
      main: { App: { DiscoverBots: vi.fn().mockResolvedValue(null) } },
    }
    const r = await discoverBots([])
    expect(Array.isArray(r)).toBe(true)
    expect(r).toHaveLength(0)
  })
})

describe('bridge.validate', () => {
  const origFetch = globalThis.fetch

  beforeEach(() => {
    delete (window as any).go
  })
  afterEach(() => {
    globalThis.fetch = origFetch
    delete (window as any).go
    vi.restoreAllMocks()
  })

  it('浏览器模式走 fetch POST /api/validate', async () => {
    const mockResp = {
      ok: true,
      json: vi.fn().mockResolvedValue({ valid: true, system: 'x', name: 'X', envs: 2, repos: 3 }),
    }
    globalThis.fetch = vi.fn().mockResolvedValue(mockResp as any) as any
    const r = await validate('system:\n  id: x')
    expect(globalThis.fetch).toHaveBeenCalledWith(
      '/api/validate',
      expect.objectContaining({ method: 'POST', body: 'system:\n  id: x' }),
    )
    expect(r.valid).toBe(true)
    expect(r.system).toBe('x')
  })

  it('桌面模式直接调 App.Validate,跳过 fetch', async () => {
    const spy = vi.fn().mockResolvedValue({ valid: true, system: 'y', name: 'Y', envs: 1, repos: 1 })
    ;(window as any).go = { main: { App: { Validate: spy } } }
    const fetchSpy = vi.fn()
    globalThis.fetch = fetchSpy as any
    const r = await validate('yaml-text')
    expect(spy).toHaveBeenCalledWith('yaml-text')
    expect(fetchSpy).not.toHaveBeenCalled() // 没走 HTTP
    expect(r.system).toBe('y')
  })

  it('浏览器模式 fetch 非 200 抛 Error', async () => {
    globalThis.fetch = vi.fn().mockResolvedValue({
      ok: false,
      status: 400,
      json: vi.fn().mockResolvedValue({ error: 'bad yaml' }),
    }) as any
    await expect(validate('broken')).rejects.toThrow('bad yaml')
  })
})

describe('bridge.analyzeServiceTopology', () => {
  afterEach(() => {
    delete (window as any).go
    vi.restoreAllMocks()
  })

  it('桌面模式转发 yaml 和 repo paths', async () => {
    const snapshot = { schema_version: '1', services: [], endpoints: [], edges: [], repositories: [] }
    const spy = vi.fn().mockResolvedValue(snapshot)
    ;(window as any).go = { main: { App: { AnalyzeServiceTopology: spy } } }

    await expect(analyzeServiceTopology('system:\n  id: mall', { web: '/repos/web' })).resolves.toEqual(snapshot)
    expect(spy).toHaveBeenCalledWith('system:\n  id: mall', { web: '/repos/web' })
  })

  it('浏览器模式明确拒绝', async () => {
    delete (window as any).go
    await expect(analyzeServiceTopology('yaml', {})).rejects.toThrow('仅在桌面 app 可用')
  })
})
