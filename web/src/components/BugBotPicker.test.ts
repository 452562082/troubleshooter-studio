import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { BotMatch } from '../lib/bridge/bugs'
import BugBotPicker from './BugBotPicker.vue'

const matches: BotMatch[] = [
  {
    bot: { key: '/bots/storefront|codex', system_id: 'storefront', target: 'codex', path: '/bots/storefront', name: 'Storefront Bot', env: 'test' },
    score: 90,
    reasons: ['system_id 匹配'],
  },
  {
    bot: { key: '/bots/platform|claude-code', system_id: 'platform', target: 'claude-code', path: '/bots/platform', name: 'Platform Bot' },
    score: 70,
    reasons: [],
  },
]

describe('BugBotPicker', () => {
  it('renders a checked and textual selected state and emits the exact Bot key', async () => {
    const wrapper = mount(BugBotPicker, {
      props: { matches, selectedKey: matches[0].bot.key },
    })

    const inputs = wrapper.findAll('input[type="radio"]')
    expect((inputs[0].element as HTMLInputElement).checked).toBe(true)
    expect(wrapper.findAll('.bot-option')[0].text()).toContain('已选择')

    await inputs[1].setValue()

    expect(wrapper.emitted('select')).toEqual([[matches[1].bot.key]])
  })

  it('announces loading and empty states without emitting a selection', () => {
    const loading = mount(BugBotPicker, {
      props: { matches, selectedKey: '', loading: true },
    })
    expect(loading.get('[role="status"]').text()).toContain('匹配中')
    expect(loading.find('input').attributes('disabled')).toBeDefined()

    const empty = mount(BugBotPicker, {
      props: { matches: [], selectedKey: '' },
    })
    expect(empty.get('[role="status"]').text()).toContain('暂无匹配')
    expect(empty.emitted('select')).toBeUndefined()
  })

  it('uses unique accessible group labels across component instances', () => {
    const first = mount(BugBotPicker, { props: { matches, selectedKey: '' } })
    const second = mount(BugBotPicker, { props: { matches, selectedKey: '' } })

    expect(first.get('fieldset').attributes('aria-labelledby'))
      .not.toBe(second.get('fieldset').attributes('aria-labelledby'))
  })
})
