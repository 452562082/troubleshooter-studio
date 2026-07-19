import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { PhaseAttempt } from '../lib/bridge/bugWorkflow'
import BugStageAttemptOutput from './BugStageAttemptOutput.vue'

const validation: PhaseAttempt = {
  id: 'validation-1', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {},
  output_json: { verification_status: 'insufficient_info', environment: 'test', expected_behavior: '显示两名用户', observed_behavior: '<img src=x onerror=alert(1)> 只显示一名用户', gaps: ['缺少测试账号'], evidence: [] },
  parent_attempt_id: '', started_at: '2026-07-14T10:00:00Z', error_code: 'needs_evidence', error_message: '证据不足', usage: {},
}

describe('BugStageAttemptOutput', () => {
  it('renders a latest result as an expanded Chinese semantic card', () => {
    const wrapper = mount(BugStageAttemptOutput, { props: { attempt: validation, latest: true } })
    expect(wrapper.get('details').attributes()).toHaveProperty('open')
    expect(wrapper.get('summary').text()).toContain('验证')
    expect(wrapper.get('summary').text()).toContain('信息不足')
    expect(wrapper.text()).toContain('预期表现')
    expect(wrapper.text()).toContain('实际观察')
    expect(wrapper.text()).toContain('还需补充')
    expect(wrapper.text()).toContain('尚无有效证据')
    expect(wrapper.text()).not.toContain('verification_status')
    expect(wrapper.text()).not.toContain('{')
    expect(wrapper.find('img').exists()).toBe(false)
    const expectedSection = wrapper.findAll('.stage-section').find(section => section.get('h4').text() === '预期表现')
    expect(expectedSection?.get('p.stage-text').text()).toBe('显示两名用户')
    expect(expectedSection?.find('dd').exists()).toBe(false)
    for (const definitionList of wrapper.findAll('dl')) {
      expect(definitionList.findAll('dd')).toHaveLength(definitionList.findAll('dt').length)
    }
  })

  it('keeps an older result collapsed and exposes its error in readable text', () => {
    const wrapper = mount(BugStageAttemptOutput, { props: { attempt: validation, latest: false } })
    expect(wrapper.get('details').attributes('open')).toBeUndefined()
    expect(wrapper.get('[data-attempt-error]').text()).toBe('证据不足')
    expect(wrapper.get('summary').attributes('aria-label')).toContain('验证')
  })

  it('separates every validation evidence item into a labelled card', () => {
    const attempt = {
      ...validation,
      output_json: {
        verification_status: 'reproduced', environment: 'test', gaps: [],
        evidence: [
          { kind: 'screenshot', environment: 'test', path: 'browser-executions/primary/browser/action-02-wait-for-featured-card.png' },
          { kind: 'screenshot', environment: 'test', path: 'browser-executions/primary/browser/action-03-capture-featured-card.png' },
          { kind: 'screenshot', environment: 'test', path: 'browser-executions/primary/browser/failure.png' },
          { kind: 'network', environment: 'test', path: 'browser-executions/primary/browser/network.json', request_id: 'request-123', trace_id: 'trace-456' },
        ],
      },
    }
    const wrapper = mount(BugStageAttemptOutput, { props: { attempt, latest: true } })
    const cards = wrapper.findAll('[data-stage-group]')

    expect(cards).toHaveLength(4)
    expect(cards.map(card => card.get('.stage-group-heading strong').text())).toEqual(['证据 1', '证据 2', '证据 3', '证据 4'])
    expect(cards[0].get('.stage-group-heading span').text()).toBe('截图')
    expect(cards[0].text()).toContain('action-02-wait-for-featured-card.png')
    expect(cards[0].text()).not.toContain('network.json')
    expect(cards[3].get('.stage-group-heading span').text()).toBe('Network')
    expect(cards[3].text()).toContain('request-123')
    expect(cards[3].text()).toContain('trace-456')
  })

  it('renders multiple unknown fields with Chinese fallback labels and stable keyed sections', async () => {
    const unknown = {
      ...validation,
      phase: 'investigation' as const,
      output_json: { first_machine_key: '第一项可读值', second_machine_key: '第二项可读值' },
    }
    const wrapper = mount(BugStageAttemptOutput, { props: { attempt: unknown, latest: true } })

    expect(wrapper.text()).toContain('第一项可读值')
    expect(wrapper.text()).toContain('第二项可读值')
    expect(wrapper.text()).not.toContain('first_machine_key')
    expect(wrapper.text()).not.toContain('second_machine_key')
    expect(wrapper.findAll('.stage-section').map(section => section.get('h4').text())).toEqual(['补充信息', '补充信息'])

    await wrapper.setProps({ attempt: { ...unknown, output_json: { first_machine_key: '第一项更新值', second_machine_key: '第二项更新值' } } })
    expect(wrapper.text()).toContain('第一项更新值')
    expect(wrapper.text()).toContain('第二项更新值')
  })
})
