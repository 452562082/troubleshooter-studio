import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { BugRecord } from '../lib/bridge/bugs'
import BugTicketDetail from './BugTicketDetail.vue'
import detailSource from './BugTicketDetail.vue?raw'

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
  it('keeps lists and wide rich content inside the detail card', () => {
    const wrapper = mount(BugTicketDetail, {
      props: {
        bug: {
          ...bug,
          steps: '1. 打开搜索页并输入超长关键字\n2. 对比后端返回结果\n\n```text\nvery-long-line\n```',
        },
        mode: 'full',
      },
    })

    expect(wrapper.get('.ticket-markdown ol').findAll('li')).toHaveLength(2)
    expect(detailSource).toContain('padding-inline-start: 1.75rem')
    expect(detailSource).toContain('.ticket-markdown :deep(table)')
    expect(detailSource).toContain('overflow-x: auto')
  })

  it('declares component-width responsive layout for supported viewports', () => {
    const wrapper = mount(BugTicketDetail, { props: { bug, mode: 'full' } })

    expect(wrapper.get('.ticket-detail').attributes('data-responsive-viewports')).toBe('375,768,1024,1440')
    expect(wrapper.get('.ticket-detail').attributes('data-overflow-safe')).toBe('true')
    expect(detailSource).toContain('container: ticket-detail / inline-size')
    expect(detailSource).toContain('@container ticket-detail')
  })

  it('keeps a long attachment name accessible without displacing the preview action', () => {
    const longName = 'app_user_search_backend_two_results_with_environment_and_timestamp.png'
    const wrapper = mount(BugTicketDetail, {
      props: { bug: { ...bug, attachments: [{ id: 'long', name: longName, type: 'image/png' }] }, mode: 'full' },
    })
    const attachment = wrapper.get('[data-attachment-index="0"]')

    expect(attachment.attributes('title')).toBe(longName)
    expect(attachment.attributes('aria-label')).toBe(`预览附件：${longName}`)
    expect(attachment.get('.attachment-copy strong').text()).toBe(longName)
    expect(attachment.get('.attachment-action').text()).toBe('预览')
  })

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
