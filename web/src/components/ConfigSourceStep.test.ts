import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { WizardStoreKey, type WizardStore } from '../lib/wizardStore'
import ConfigSourceStep from './ConfigSourceStep.vue'

describe('ConfigSourceStep', () => {
  const wizard: WizardStore = {
    environments: [{ id: 'dev', is_prod: false }],
    allServiceNames: ['base-backend-base'],
    runtimeWorkloadNames: ['base-backend-base'],
    kuboardStateByEnv: {},
    one2allStateByEnv: {
      dev: {
        status: 'ok',
        clusters: [
          {
            name: 'ZH',
            cluster_id: '1',
            namespaces: [{ name: 'base-dev', configmaps: ['base-config'] }],
          },
        ],
      },
    },
    hasError: () => false,
    svcKey: (envID, svc) => `${envID}::${svc}`,
    isRevealed: () => false,
    toggleReveal: () => {},
    kuboardClustersOf: () => [],
    kuboardClusterCountOf: () => 0,
    kuboardErrorOf: () => '',
    kuboardNamespacesFor: () => [],
    kuboardConfigMapsFor: () => [],
    one2allClustersOf: (envID) => wizard.one2allStateByEnv[envID]?.status === 'ok' ? wizard.one2allStateByEnv[envID].clusters : [],
    one2allClusterCountOf: (envID) => wizard.one2allStateByEnv[envID]?.status === 'ok' ? wizard.one2allStateByEnv[envID].clusters.length : 0,
    one2allErrorOf: () => '',
    one2allNamespacesFor: (envID, clusterID) => {
      const clusters = wizard.one2allStateByEnv[envID]?.status === 'ok' ? wizard.one2allStateByEnv[envID].clusters : []
      return clusters.find(c => c.cluster_id === clusterID)?.namespaces.map(n => n.name) || []
    },
    one2allConfigMapsFor: (envID, clusterID, ns) => {
      const clusters = wizard.one2allStateByEnv[envID]?.status === 'ok' ? wizard.one2allStateByEnv[envID].clusters : []
      return clusters.find(c => c.cluster_id === clusterID)?.namespaces.find(n => n.name === ns)?.configmaps || []
    },
  }

  function mountStep(options: { configmaps?: string[]; selectedConfigMaps?: string } = {}) {
    const configmaps = options.configmaps ?? ['base-config']
    const selectedConfigMaps = options.selectedConfigMaps ?? 'base-config'
    wizard.one2allStateByEnv.dev = {
      status: 'ok',
      clusters: [{
        name: 'ZH',
        cluster_id: '1',
        namespaces: [{ name: 'base-dev', configmaps }],
      }],
    }
    return mount(ConfigSourceStep, {
      props: {
        configTypeOptions: ['one2all'],
        configTypeDescriptions: { one2all: 'one2all remote MCP' },
        enabledSourceTypes: { one2all: true },
        activeSourceTypes: ['one2all'],
        sourceInstances: [{ id: 'one2all', type: 'one2all' }],
        isMultiSource: false,
        configCenterType: 'one2all',
        ccFieldsByType: {
          one2all: [
            { key: 'url', label: 'MCP Server URL', secret: false, envVar: () => 'ONE2ALL_MCP_URL', placeholder: '' },
            { key: 'token', label: 'Bearer Token', secret: true, envVar: () => 'ONE2ALL_TOKEN', placeholder: '' },
          ],
        },
        ccCredInputs: {
          'cc:one2all:_shared_:url': 'http://one2all/mcp',
          'cc:one2all:_shared_:token': 'token',
        },
        sourceCreds: {},
        sourceEnvNamespaces: {},
        // Simulates stale nacos/apollo/consul preload state left in draft.
        ccHubStateByEnv: {
          dev: { status: 'ok', entries: [{ locator: 'stale.yaml' }], namespaces: [{ id: 'base-dev', show_name: 'base-dev' }] },
        },
        envNamespaces: { dev: 'base-dev' },
        serviceConfigSel: { 'dev::base-backend-base': 'stale.yaml' },
        serviceConfigGroup: {},
        kuboardSvcMap: {},
        one2allSvcMap: {
          'dev::base-backend-base': { cluster_id: '1', namespace: 'base-dev', configmap: selectedConfigMaps },
        },
        ccKeyFor: (type: string, envID: string, field: string) => `cc:${type}:${envID}:${field}`,
        isFieldHidden: () => false,
        envScanned: (envID: string) => envID === 'dev',
        namespacesFor: () => [{ id: 'base-dev', show_name: 'base-dev' }],
        entriesForNamespace: () => [{ locator: 'stale.yaml' }],
        getServiceSource: () => 'one2all',
      },
      global: {
        provide: { [WizardStoreKey as symbol]: wizard },
        stubs: {
          CredentialField: { template: '<div />' },
          PreloadStatusRow: { name: 'PreloadStatusRow', template: '<button type="button"><slot name="ok" /></button>' },
          ServiceChecklist: { template: '<div />' },
          NamespaceServiceMap: { template: '<div data-test="namespace-service-map">namespace/dataId map</div>' },
          KuboardServiceMap: { template: '<div />' },
          SecondarySourcePanel: { template: '<div />' },
          CredsShareWarning: { template: '<div><slot /></div>' },
        },
      },
    })
  }

  it('does not render namespace/dataId mapping for one2all even if stale ccHub state exists', () => {
    const wrapper = mountStep()

    expect(wrapper.find('[data-test="namespace-service-map"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('服务 → K8s 定位(one2all)')
  })

  it('emits config_source purpose when reloading one2all resources', async () => {
    const wrapper = mountStep()

    await wrapper.findComponent({ name: 'PreloadStatusRow' }).trigger('click')

    expect(wrapper.emitted('runOne2AllPreload')?.[0]).toEqual(['dev', 'config_source'])
  })

  it('keeps ConfigMap as a dropdown when the current preload has no candidates', async () => {
    const wrapper = mountStep({ configmaps: [], selectedConfigMaps: 'legacy-config' })

    expect(wrapper.find('input.cc-input').exists()).toBe(false)
    const toggle = wrapper.find('button.cm-toggle')
    expect(toggle.text()).toContain('1 个候选已失效')

    await toggle.trigger('click')
    expect(wrapper.find('.cm-panel').text()).toContain('legacy-config')
    expect(wrapper.find('.cm-panel').text()).toContain('本次未读取到')
  })

  it('disables the ConfigMap dropdown when there are no candidates or saved values', () => {
    const wrapper = mountStep({ configmaps: [], selectedConfigMaps: '' })

    const toggle = wrapper.find('button.cm-toggle')
    expect(toggle.attributes('disabled')).toBeDefined()
    expect(toggle.text()).toContain('暂无 ConfigMap 候选')
    expect(wrapper.find('input.cc-input').exists()).toBe(false)
  })
})
