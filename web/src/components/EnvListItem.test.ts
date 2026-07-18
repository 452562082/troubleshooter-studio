import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { reactive } from 'vue'
import EnvListItem from './EnvListItem.vue'

function mountItem() {
  const env = reactive({
    id: 'test',
    api_domain: 'https://api-test.example.com',
    web_domain: 'https://web-test.example.com',
    is_prod: false,
  })
  const wrapper = mount(EnvListItem, {
    props: {
      env,
      apiProbe: undefined,
      webProbe: undefined,
      hasIdError: false,
      hasApiError: false,
      disableRemove: false,
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
})
