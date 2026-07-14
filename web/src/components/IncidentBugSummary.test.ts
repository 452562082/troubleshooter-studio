import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { BugRecord } from '../lib/bridge/bugs'
import IncidentBugSummary from './IncidentBugSummary.vue'
import summarySource from './IncidentBugSummary.vue?raw'

const bug: BugRecord = {
  id: 'zentao-840',
  source: 'zentao',
  source_id: '840',
  title: '用户昵称模糊搜索结果不完整',
  status: 'active',
  severity: '3',
  priority: '3',
  created_at: '2026-07-08T11:12:00',
  updated_at: '2026-07-10T12:24:00',
  system_id: 'base',
  env: 'test',
  service_hints: ['user-api'],
  description: '搜索结果只显示一个用户',
  steps: '输入昵称后搜索',
  attachments: [{ id: 'shot-1', name: 'result.png', type: 'image/png' }],
}

describe('IncidentBugSummary', () => {
  it('renders only the incident decision fields with semantic time values', () => {
    const wrapper = mount(IncidentBugSummary, { props: { bug } })
    const metadata = wrapper.get('.incident-bug-metadata')

    expect(wrapper.get('.incident-bug-source').text()).toBe('禅道 #840')
    expect(wrapper.get('h2').text()).toBe('用户昵称模糊搜索结果不完整')
    expect(wrapper.get('.incident-bug-grade').text()).toBe('S3 · P3')
    expect(metadata.text()).toContain('创建时间')
    expect(metadata.text()).toContain('2026-07-08 11:12')
    expect(metadata.text()).toContain('更新时间')
    expect(metadata.text()).toContain('2026-07-10 12:24')
    expect(metadata.text()).toContain('Bug 状态')
    expect(wrapper.get('.incident-bug-status').text()).toBe('active')
    expect(wrapper.find('time[datetime="2026-07-08T11:12:00"]').exists()).toBe(true)
    expect(wrapper.find('time[datetime="2026-07-10T12:24:00"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('系统')
    expect(wrapper.text()).not.toContain('环境')
    expect(wrapper.text()).not.toContain('服务')
    expect(wrapper.text()).not.toContain('base')
    expect(wrapper.text()).not.toContain('test')
    expect(wrapper.text()).not.toContain('user-api')
    expect(wrapper.text()).not.toContain('搜索结果只显示一个用户')
    expect(wrapper.text()).not.toContain('输入昵称后搜索')
    expect(wrapper.text()).not.toContain('result.png')
  })

  it.each([
    [{ severity: '2', priority: undefined }, 'S2'],
    [{ severity: undefined, priority: '1' }, 'P1'],
    [{ severity: undefined, priority: undefined }, '-'],
    [{ severity: 'S4', priority: 'P2' }, 'S4 · P2'],
    [{ severity: 's4', priority: 'p2' }, 'S4 · P2'],
  ])('formats partial and prefixed grades without duplicate prefixes', (grade, expected) => {
    const wrapper = mount(IncidentBugSummary, { props: { bug: { ...bug, ...grade } } })

    expect(wrapper.get('.incident-bug-grade').text()).toBe(expected)
  })

  it('keeps invalid source times readable without emitting an invalid datetime attribute', () => {
    const wrapper = mount(IncidentBugSummary, {
      props: { bug: { ...bug, created_at: 'unknown time', updated_at: undefined } },
    })
    const values = wrapper.findAll('.incident-bug-time')

    expect(values[0].text()).toBe('unknown time')
    expect(values[0].attributes('datetime')).toBeUndefined()
    expect(values[1].text()).toBe('-')
    expect(values[1].element.tagName).toBe('SPAN')
  })

  it.each([
    'July 8, 2026 11:12',
    '2026-02-30T11:12:00',
  ])('keeps parseable non-HTML or impossible source time %s out of datetime', (sourceTime) => {
    const wrapper = mount(IncidentBugSummary, {
      props: { bug: { ...bug, created_at: sourceTime } },
    })
    const value = wrapper.findAll('.incident-bug-time')[0]

    expect(value.text()).toBe(sourceTime)
    expect(value.attributes('datetime')).toBeUndefined()
    expect(value.element.tagName).toBe('SPAN')
  })

  it('renders an accessible empty state and responsive overflow contract', () => {
    const empty = mount(IncidentBugSummary)
    const selected = mount(IncidentBugSummary, { props: { bug } })

    expect(empty.get('.incident-bug-summary-empty[role="status"]').text()).toContain('选择一条 Bug')
    expect(selected.get('.incident-bug-summary').attributes('data-responsive-viewports')).toBe('375,768,1024,1440')
    expect(selected.get('.incident-bug-summary').attributes('data-overflow-safe')).toBe('true')
    expect(summarySource).toContain('grid-template-columns: repeat(2, minmax(0, 1fr))')
    expect(summarySource).toContain('@container incident-bug-summary (max-width: 520px)')
    expect(summarySource).toContain('grid-template-columns: minmax(0, 1fr)')
    expect(summarySource).toContain('overflow-wrap: anywhere')
  })
})
