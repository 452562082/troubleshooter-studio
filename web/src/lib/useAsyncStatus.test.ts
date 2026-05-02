import { describe, it, expect } from 'vitest'
import { useAsyncStatus } from './useAsyncStatus'

describe('useAsyncStatus', () => {
  it('resets all three refs initially', () => {
    const s = useAsyncStatus()
    expect(s.loading.value).toBe('')
    expect(s.errorMsg.value).toBe('')
    expect(s.successMsg.value).toBe('')
  })

  it('run sets loading during fn, clears after', async () => {
    const s = useAsyncStatus()
    let snapshot = ''
    const promise = s.run('查询', async () => {
      snapshot = s.loading.value
      return 42
    })
    const v = await promise
    expect(v).toBe(42)
    expect(snapshot).toBe('查询')
    expect(s.loading.value).toBe('')
  })

  it('run catches errors → errorMsg set, returns undefined', async () => {
    const s = useAsyncStatus()
    const v = await s.run('查询', async () => {
      throw new Error('boom')
    })
    expect(v).toBeUndefined()
    expect(s.errorMsg.value).toBe('boom')
    expect(s.loading.value).toBe('')
  })

  it('run preserves successMsg set inside fn (does not auto-clear after success)', async () => {
    const s = useAsyncStatus()
    await s.run('验证', async () => {
      s.successMsg.value = '通过'
    })
    expect(s.successMsg.value).toBe('通过')
    expect(s.errorMsg.value).toBe('')
  })

  it('run clears prior success/error before fn runs', async () => {
    const s = useAsyncStatus()
    s.errorMsg.value = '旧错'
    s.successMsg.value = '旧成功'
    await s.run('再跑', async () => {
      // mid-run:msg 已清
      expect(s.errorMsg.value).toBe('')
      expect(s.successMsg.value).toBe('')
    })
  })

  it('run extra callback fires before loading flips on', async () => {
    const s = useAsyncStatus()
    let order = ''
    await s.run('x', async () => { order += 'fn' }, () => { order += 'extra-' })
    expect(order).toBe('extra-fn')
  })

  it('reset() with extra also clears local state', () => {
    const s = useAsyncStatus()
    s.errorMsg.value = 'x'
    s.successMsg.value = 'y'
    s.loading.value = 'z'
    let extraRan = false
    s.reset(() => { extraRan = true })
    expect(s.errorMsg.value).toBe('')
    expect(s.successMsg.value).toBe('')
    expect(s.loading.value).toBe('')
    expect(extraRan).toBe(true)
  })

  it('non-Error throw is stringified', async () => {
    const s = useAsyncStatus()
    await s.run('x', async () => { throw { message: 'custom' } })
    expect(s.errorMsg.value).toBe('custom')
  })
})
