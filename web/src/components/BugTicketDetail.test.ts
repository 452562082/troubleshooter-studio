import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { BugRecord } from '../lib/bridge/bugs'
import BugTicketDetail from './BugTicketDetail.vue'

const bug: BugRecord = {
  id: 'zentao-840',
  source: 'zentao',
  source_id: '840',
  title: 'Checkout timeout',
  status: 'active',
  priority: '1',
  severity: '2',
  env: 'test',
  system_id: 'storefront',
  product: 'Shop',
  module: 'Checkout',
  bug_type: 'codeerror',
  assignee: 'alice',
  reporter: 'bob',
  created_at: '2026-07-13T01:02:00Z',
  updated_at: '2026-07-13T03:04:00Z',
  os: 'Linux',
  browser: 'Chrome',
  keywords: 'timeout',
  steps: '**Click pay**\n\n- wait 30 seconds',
  description: 'Request returns `504`.',
  service_hints: ['order-api'],
  attachments: [
    { id: 'shot-1', name: 'timeout.png', type: 'image/png' },
    { id: 'log-1', name: 'gateway.log', type: 'text/plain' },
  ],
}

describe('BugTicketDetail', () => {
  it('renders full ticket fields, sanitized Markdown, attachments, and open action', async () => {
    const wrapper = mount(BugTicketDetail, { props: { bug, mode: 'full' } })

    expect(wrapper.text()).toContain('所属产品')
    expect(wrapper.text()).toContain('Shop')
    expect(wrapper.get('.ticket-markdown strong').text()).toBe('Click pay')
    expect(wrapper.text()).toContain('timeout.png')

    await wrapper.findAll('[data-attachment-index]')[1].trigger('click')
    await wrapper.get('[data-action="open-incident"]').trigger('click')

    expect(wrapper.emitted('previewAttachment')).toEqual([[1]])
    expect(wrapper.emitted('openIncident')).toEqual([['zentao-840']])
  })

  it('renders a compact summary without full-only fields or actions', () => {
    const wrapper = mount(BugTicketDetail, { props: { bug, mode: 'summary' } })

    expect(wrapper.text()).toContain('Checkout timeout')
    expect(wrapper.text()).toContain('storefront')
    expect(wrapper.text()).toContain('order-api')
    expect(wrapper.text()).not.toContain('所属产品')
    expect(wrapper.find('[data-attachment-index]').exists()).toBe(false)
    expect(wrapper.find('[data-action="open-incident"]').exists()).toBe(false)
  })

  it('keeps hostile Markdown inert while preserving readable Markdown', () => {
    const hostile = {
      ...bug,
      steps: [
        '**safe conclusion**',
        '<img src=x onerror=alert(1)>',
        '<script>alert(2)</script>',
        '[bad](jav&#x61;script:alert(3))',
        '<svg/onload=alert(4)>',
      ].join('\n'),
    }
    const wrapper = mount(BugTicketDetail, { props: { bug: hostile, mode: 'full' } })

    expect(wrapper.get('.ticket-markdown strong').text()).toBe('safe conclusion')
    expect(wrapper.findAll('.ticket-markdown img, .ticket-markdown script, .ticket-markdown svg')).toHaveLength(0)
    for (const element of wrapper.findAll('.ticket-markdown *')) {
      expect(Object.keys(element.attributes()).some(name => name.toLowerCase().startsWith('on'))).toBe(false)
      expect(`${element.attributes('href') || ''}${element.attributes('src') || ''}`.toLowerCase()).not.toContain('javascript:')
    }
  })

  it('shows a recoverable empty state when no ticket is selected', () => {
    const wrapper = mount(BugTicketDetail, { props: { mode: 'full' } })

    expect(wrapper.get('[role="status"]').text()).toContain('选择一条 Bug')
  })

  it('uses unique accessible title IDs across component instances', () => {
    const first = mount(BugTicketDetail, { props: { bug, mode: 'summary' } })
    const second = mount(BugTicketDetail, { props: { bug, mode: 'summary' } })

    expect(first.get('.ticket-detail').attributes('aria-labelledby'))
      .not.toBe(second.get('.ticket-detail').attributes('aria-labelledby'))
  })
})
