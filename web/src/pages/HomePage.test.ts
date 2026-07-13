import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import HomePage from './HomePage.vue'

const router = vi.hoisted(() => ({ push: vi.fn() }))

vi.mock('vue-router', () => ({ useRouter: () => router }))
vi.mock('../lib/confirm', () => ({ confirmDialog: vi.fn().mockResolvedValue(false) }))

afterEach(() => {
  router.push.mockReset()
})

describe('HomePage workflow navigation', () => {
  it.each([
    ['/bugs', 'Bug 工单', '同步工单平台，查看完整 Bug 详情'],
    ['/incidents', '故障闭环', '选择 Bug，完成验证、排障、修复和回归'],
  ])('offers a keyboard-reachable %s card with the final copy', async (path, label, desc) => {
    const wrapper = mount(HomePage)
    const card = wrapper.get(`[data-path="${path}"]`)

    expect(card.element.tagName).toBe('BUTTON')
    expect(card.attributes('type')).toBe('button')
    expect(card.text()).toContain(label)
    expect(card.text()).toContain(desc)

    await card.trigger('click')
    expect(router.push).toHaveBeenCalledWith(path)
  })
})
