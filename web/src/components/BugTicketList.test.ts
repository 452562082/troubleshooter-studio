import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { BugRecord } from '../lib/bridge/bugs'
import BugTicketList from './BugTicketList.vue'

const bugs: BugRecord[] = [
  { id: 'bug-1', source: 'zentao', source_id: '101', title: 'API timeout', env: 'test', severity: '2' },
  { id: 'bug-2', source: 'lark', source_id: 'TASK-9', title: 'Login fails', env: 'prod' },
]

describe('BugTicketList', () => {
  it('renders selection without relying on color and emits the exact ticket ID', async () => {
    const wrapper = mount(BugTicketList, {
      props: { bugs, selectedId: 'bug-1', query: '' },
    })

    const rows = wrapper.findAll('[data-ticket-id]')
    expect(rows).toHaveLength(2)
    expect(rows[0].attributes('aria-pressed')).toBe('true')
    expect(rows[0].text()).toContain('已选择')

    await rows[1].trigger('click')

    expect(wrapper.emitted('select')).toEqual([['bug-2']])
  })

  it('uses a labelled search input and emits query updates', async () => {
    const wrapper = mount(BugTicketList, {
      props: { bugs, selectedId: '', query: 'api' },
    })

    const input = wrapper.get('input[type="search"]')
    expect(wrapper.get(`label[for="${input.attributes('id')}"]`).text()).toContain('搜索')
    await input.setValue('login')

    expect(wrapper.emitted('update:query')).toEqual([['login']])
  })

  it('announces loading and empty states', () => {
    const loading = mount(BugTicketList, {
      props: { bugs: [], selectedId: '', query: '', loading: true },
    })
    expect(loading.get('[role="status"]').text()).toContain('加载中')

    const empty = mount(BugTicketList, {
      props: { bugs: [], selectedId: '', query: '' },
    })
    expect(empty.get('[role="status"]').text()).toContain('暂无')
  })

  it('uses unique accessible list labels across component instances', () => {
    const props = { bugs, selectedId: '', query: '' }
    const first = mount(BugTicketList, { props })
    const second = mount(BugTicketList, { props })

    expect(first.get('.ticket-list').attributes('aria-labelledby'))
      .not.toBe(second.get('.ticket-list').attributes('aria-labelledby'))
  })

  it('renders an optional action beside the title and count in normal flow', () => {
    const withAction = mount(BugTicketList, {
      props: { bugs, selectedId: '', query: '' },
      slots: { actions: '<button data-test="list-action">同步</button>' },
    })
    const heading = withAction.get('.list-heading')
    expect(heading.get('.list-heading-summary strong').text()).toBe('Bug 收件箱')
    expect(heading.get('.list-heading-summary span').text()).toBe('2 条')
    expect(heading.get('.list-actions [data-test="list-action"]').text()).toBe('同步')

    const withoutAction = mount(BugTicketList, {
      props: { bugs, selectedId: '', query: '' },
    })
    expect(withoutAction.find('.list-actions').exists()).toBe(false)
  })
})
