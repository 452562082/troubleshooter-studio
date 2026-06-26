import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import K8sRuntimeBlock from './K8sRuntimeBlock.vue'

describe('K8sRuntimeBlock', () => {
  const baseProps = {
    envID: 'dev',
    services: ['base-backend-base'],
    kuboardState: { status: 'idle' as const },
    envLoc: { cluster: '', cluster_id: '', namespace: '' },
    svcMap: {},
    workloadCache: {},
    svcKey: (envID: string, svc: string) => `${envID}::${svc}`,
    workloadKey: (envID: string, cluster: string, ns: string) => `${envID}::${cluster}::${ns}`,
    workloadsFor: () => [],
    namespacesFor: () => [],
  }

  it('renders one2all preload and cluster namespace selectors', async () => {
    const wrapper = mount(K8sRuntimeBlock, {
      props: {
        ...baseProps,
        provider: 'one2all',
        envLoc: { cluster: '', cluster_id: '1', namespace: '' },
        one2allState: {
          status: 'ok',
          clusters: [
            {
              name: 'dev-cluster',
              cluster_id: '1',
              namespaces: [
                { name: 'default', configmaps: [] },
                { name: 'truss', configmaps: [] },
              ],
            },
          ],
        },
      },
      global: {
        stubs: { RouterLink: true },
      },
    })

    expect(wrapper.text()).toContain('重新加载集群资源')
    expect(wrapper.text()).toContain('1 个集群')

    const selects = wrapper.findAll('select')
    expect(selects.length).toBeGreaterThanOrEqual(2)
    expect(selects[0].text()).toContain('dev-cluster(1)')
    expect(selects[1].text()).toContain('default')
    expect(selects[1].text()).toContain('truss')

    await selects[0].setValue('1')
    await selects[1].setValue('truss')
    expect(wrapper.emitted('setEnvLoc')).toContainEqual(['dev', 'cluster_id', '1'])
    expect(wrapper.emitted('setEnvLoc')).toContainEqual(['dev', 'namespace', 'truss'])
  })

  it('does not allow manual cluster namespace input before one2all resources are loaded', () => {
    const wrapper = mount(K8sRuntimeBlock, {
      props: {
        ...baseProps,
        provider: 'one2all',
        one2allState: { status: 'idle' },
      },
      global: {
        stubs: { RouterLink: true },
      },
    })

    expect(wrapper.text()).toContain('加载集群资源')
    expect(wrapper.find('input[placeholder="如 1"]').exists()).toBe(false)
    expect(wrapper.find('input[placeholder="如 default"]').exists()).toBe(false)
    expect(wrapper.findAll('select')).toHaveLength(0)
  })
})
