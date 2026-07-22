import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { reactive } from 'vue'
import EnvListItem from './EnvListItem.vue'

interface FrontendEntryFixture {
  id: string
  name: string
  url: string
  repo: string
  device_profile: string
  aliases: string
  product_hints: string
  module_hints: string
  path_prefixes: string
}

function mountItem() {
  const env = reactive<{
    id: string
    api_domain: string
    web_domain: string
    frontend_entries: FrontendEntryFixture[]
    is_prod: boolean
  }>({
    id: 'test',
    api_domain: 'https://api-test.example.com',
    web_domain: 'https://web-test.example.com',
    frontend_entries: [],
    is_prod: false,
  })
  const wrapper = mount(EnvListItem, {
    props: {
      env,
      apiProbe: undefined,
      hasIdError: false,
      hasApiError: false,
      disableRemove: false,
      hasEntryError: () => false,
    },
  })
  return { wrapper, env }
}

describe('EnvListItem', () => {
  it('does not expose deployment-version settings in the robot creation wizard', () => {
    const { wrapper } = mountItem()

    expect(wrapper.find('[data-test="deployment-verification-toggle"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="deployment-verification-fields"]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('故障闭环高级配置')
    expect(wrapper.text()).not.toContain('版本接口')
  })

  it('groups each environment as a card with an accessible remove action', () => {
    const { wrapper } = mountItem()

    expect(wrapper.get('[data-test="environment-card"]').classes()).toContain('environment-card')
    expect(wrapper.get('.environment-fields').classes()).toContain('environment-fields')
    expect(wrapper.get('.environment-remove').attributes('aria-label')).toBe('删除 test 环境')
    expect(wrapper.get('.environment-remove svg').attributes('aria-hidden')).toBe('true')
  })

  it('only asks for the frontend kind and URL', async () => {
    const { wrapper, env } = mountItem()
    await wrapper.get('.add-entry').trigger('click')
    expect(env.frontend_entries).toHaveLength(1)
    const select = wrapper.get('.frontend-entry-card select')
    const input = wrapper.get('.frontend-entry-card input')
    expect(select.findAll('option').map(option => option.text())).toContain('管理端')
    await select.setValue('管理端')
    await input.setValue('https://admin-test.example.com')
    expect(env.frontend_entries[0]).toMatchObject({ id: '', name: '管理端', url: 'https://admin-test.example.com', repo: '' })
    expect(wrapper.text()).not.toContain('Web 域名')
    expect(wrapper.text()).not.toContain('入口 ID')
    expect(wrapper.text()).not.toContain('路径前缀')
  })

  it('preserves an imported custom frontend kind in the styled select', async () => {
    const { wrapper, env } = mountItem()
    env.frontend_entries.push({
      id: 'partner', name: '合作方工作台', url: 'https://partner.example.com', repo: '',
      device_profile: '', aliases: '', product_hints: '', module_hints: '', path_prefixes: '',
    })
    await wrapper.vm.$nextTick()

    const select = wrapper.get('.frontend-entry-card select')
    expect((select.element as HTMLSelectElement).value).toBe('合作方工作台')
    expect(select.text()).toContain('合作方工作台（已导入）')
  })
})
