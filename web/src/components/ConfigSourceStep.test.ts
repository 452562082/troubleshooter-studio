import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { WizardStoreKey, type WizardStore } from '../lib/wizardStore'
import ConfigSourceStep from './ConfigSourceStep.vue'

describe('ConfigSourceStep', () => {
  const wizard: WizardStore = {
    environments: [{ id: 'dev', is_prod: false }],
    allServiceNames: ['base-backend-base'],
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

  function mountStep() {
    return mount(ConfigSourceStep, {
      props: {
        configTypeOptions: ['one2all'],
        configTypeDescriptions: { one2all: 'one2all remote MCP' },
        enabledSourceTypes: { one2all: true },
        activeSourceTypes: ['one2all'],
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
        // Simulates stale nacos/apollo/consul preload state left in draft.
        ccHubStateByEnv: {
          dev: { status: 'ok', entries: [{ locator: 'stale.yaml' }], namespaces: [{ id: 'base-dev', show_name: 'base-dev' }] },
        },
        envNamespaces: { dev: 'base-dev' },
        serviceConfigSel: { 'dev::base-backend-base': 'stale.yaml' },
        serviceConfigGroup: {},
        kuboardSvcMap: {},
        one2allSvcMap: {
          'dev::base-backend-base': { cluster_id: '1', namespace: 'base-dev', configmap: 'base-config' },
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
          PreloadStatusRow: { template: '<div><slot name="ok" /></div>' },
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
})
