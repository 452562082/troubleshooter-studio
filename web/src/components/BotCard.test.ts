import { mount } from '@vue/test-utils'
import { describe, it, expect } from 'vitest'
import BotCard from './BotCard.vue'
import type { DiscoveredBot } from '../lib/bridge'

describe('BotCard active agents', () => {
  it.each(['claude-code', 'cursor', 'codex'])('hides legacy validators and counts remaining entries for %s', target => {
    const bot = {
      path: '/preview/base', ide_available: true,
      meta: { system_id: 'base', target, internal_agents: [
        { id: 'base-troubleshooter', role: 'troubleshooter' },
        { id: 'base-validator', role: 'validator' },
        { id: 'base-old-validator', role: '' },
        { id: 'base-fixer', role: 'fixer' },
      ] },
    } as DiscoveredBot
    const wrapper = mount(BotCard, { props: { bot, editing: false, menuOpen: false, editorDraft: '', targetLabel: value => value } })
    expect(wrapper.text()).toContain('2 个执行入口')
    expect(wrapper.text()).toContain('排障 Agent')
    expect(wrapper.text()).toContain('修复 Agent')
    expect(wrapper.text()).not.toContain('validator')
    expect(wrapper.text()).not.toContain('验证 Agent')
  })
})
