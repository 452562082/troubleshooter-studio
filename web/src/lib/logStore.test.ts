import { afterEach, describe, expect, it, vi } from 'vitest'

afterEach(() => {
  delete (window as any).runtime
  globalThis.localStorage?.clear()
  vi.resetModules()
  vi.restoreAllMocks()
})

describe('setupGlobalLogBridges', () => {
  it('does not throw in browser preview without Wails runtime', async () => {
    delete (window as any).runtime
    const { setupGlobalLogBridges } = await import('./logStore')

    expect(() => setupGlobalLogBridges()).not.toThrow()
  })

  it('registers Wails events when runtime is available', async () => {
    const onMultiple = vi.fn(() => () => {})
    ;(window as any).runtime = { EventsOnMultiple: onMultiple }
    const { setupGlobalLogBridges } = await import('./logStore')

    setupGlobalLogBridges()

    expect(onMultiple).toHaveBeenCalledWith('install:log', expect.any(Function), -1)
    expect(onMultiple).toHaveBeenCalledWith('analyze:log', expect.any(Function), -1)
  })
})
