import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import RepoServiceChips from './RepoServiceChips.vue'

function mountChips(role: string, serviceNames: string[] = []) {
  return mount(RepoServiceChips, {
    props: {
      repo: { role },
      index: 0,
      serviceNames,
      svcAddInputs: {},
    },
  })
}

describe('RepoServiceChips', () => {
  it('explains frontend identity as runtime-only mapping', () => {
    const wrapper = mountChips('frontend', ['base-frontend'])
    expect(wrapper.text()).toContain('前端运行时服务名')
    expect(wrapper.text()).toContain('base-frontend')
    expect(wrapper.get('.help-icon').attributes('title')).toContain('不会进入配置中心或数据层扫描')
  })

  it('keeps the existing backend config-service wording', () => {
    const wrapper = mountChips('backend', ['base-backend'])
    expect(wrapper.text()).toContain('服务名')
    expect(wrapper.text()).not.toContain('前端运行时服务名')
    expect(wrapper.get('.help-icon').attributes('title')).toContain('config-map')
  })
})
