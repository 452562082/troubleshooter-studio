import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { reactive } from 'vue'
import FrontendRepoBindings from './FrontendRepoBindings.vue'

describe('FrontendRepoBindings', () => {
  it('binds configured frontend entries to repositories after repository setup', async () => {
    const environments = reactive([{
      id: 'test',
      frontend_entries: [{ name: '管理端', url: 'https://admin.test', repo: '' }],
    }])
    const repos = reactive([
      { name: 'api', role: 'backend' },
      { name: 'admin-web', role: 'admin' },
    ])
    const wrapper = mount(FrontendRepoBindings, { props: { environments, repos } })

    expect(wrapper.text()).toContain('前端入口对应哪个代码仓库')
    expect(wrapper.text()).toContain('管理端')
    const select = wrapper.get('select')
    expect(select.findAll('option').map(option => option.text())).toEqual([
      '暂不绑定 / 后续识别',
      'admin-web（管理后台）',
      'api（后端）',
    ])
    await select.setValue('admin-web')
    expect(environments[0].frontend_entries[0].repo).toBe('admin-web')
  })

  it('stays hidden when no frontend entry is configured', () => {
    const wrapper = mount(FrontendRepoBindings, {
      props: { environments: [{ id: 'test', frontend_entries: [] }], repos: [] },
    })
    expect(wrapper.find('[data-test="frontend-repo-bindings"]').exists()).toBe(false)
  })
})
