import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { dismiss, showToast, toast, useToasts } from './toast'

describe('toast', () => {
  const { list } = useToasts()

  beforeEach(() => {
    // 清空上一个测试留的
    list.value = []
    vi.useFakeTimers()
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  it('showToast 返回 id,消息进 list', () => {
    const id = showToast('hello')
    expect(typeof id).toBe('number')
    expect(list.value).toHaveLength(1)
    expect(list.value[0].message).toBe('hello')
    expect(list.value[0].kind).toBe('info') // 默认
  })

  it('ttl 到期自动 dismiss', () => {
    showToast('auto-gone', { ttl: 1000 })
    expect(list.value).toHaveLength(1)
    vi.advanceTimersByTime(999)
    expect(list.value).toHaveLength(1) // 未到时间
    vi.advanceTimersByTime(1)
    expect(list.value).toHaveLength(0) // 刚好到
  })

  it('ttl=0 不自动消', () => {
    showToast('sticky', { ttl: 0 })
    vi.advanceTimersByTime(60_000)
    expect(list.value).toHaveLength(1)
  })

  it('dismiss 按 id 精确移除', () => {
    const id1 = showToast('a')
    const id2 = showToast('b')
    expect(list.value).toHaveLength(2)
    dismiss(id1)
    expect(list.value).toHaveLength(1)
    expect(list.value[0].id).toBe(id2)
  })

  it('toast.error 默认 8s(比 info 长,让用户有时间读)', () => {
    toast.error('bad')
    vi.advanceTimersByTime(4001) // info 的 4s 之后
    expect(list.value).toHaveLength(1) // 还在
    vi.advanceTimersByTime(4000)
    expect(list.value).toHaveLength(0) // 8s 后消
  })

  it('同时多条 toast 不互相干扰', () => {
    toast.info('a', 1000)
    toast.success('b', 2000)
    toast.error('c', 3000)
    expect(list.value).toHaveLength(3)
    vi.advanceTimersByTime(1000)
    expect(list.value).toHaveLength(2) // a 消
    vi.advanceTimersByTime(1000)
    expect(list.value).toHaveLength(1) // b 消
    vi.advanceTimersByTime(1000)
    expect(list.value).toHaveLength(0) // c 消
  })
})
