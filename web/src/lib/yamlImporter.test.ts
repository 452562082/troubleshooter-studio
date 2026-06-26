import { describe, it, expect } from 'vitest'
import {
  applyParsedYAMLToWizardState,
  isPlaceholder, isLiveString, inferAuthMode, parseEnvironment, parseRepoCore, placeholderName,
} from './yamlImporter'
import type { CredField } from './credFields'

describe('isPlaceholder / isLiveString', () => {
  it('isPlaceholder identifies {{XXX}}', () => {
    expect(isPlaceholder('{{CC_ADDR_DEV}}')).toBe(true)
    expect(isPlaceholder('{{}}')).toBe(true)
  })
  it('isPlaceholder rejects regular strings', () => {
    expect(isPlaceholder('nacos:8848')).toBe(false)
    expect(isPlaceholder('')).toBe(false)
    expect(isPlaceholder(null)).toBe(false)
    expect(isPlaceholder(undefined)).toBe(false)
    expect(isPlaceholder(42)).toBe(false)
  })
  it('isLiveString = non-empty + non-placeholder', () => {
    expect(isLiveString('hello')).toBe(true)
    expect(isLiveString('')).toBe(false)
    expect(isLiveString('{{X}}')).toBe(false)
    expect(isLiveString(null)).toBe(false)
  })
})

describe('inferAuthMode', () => {
  const authField: CredField = {
    key: 'auth_mode', label: '鉴权方式', secret: false, envVar: () => '',
    options: [
      { value: 'access_key', label: 'Access Key' },
      { value: 'api_key', label: 'API Key' },
      { value: 'username_password', label: 'Username + Password' },
    ],
  }

  it('returns empty when authField missing or no options', () => {
    expect(inferAuthMode(undefined, { access_key: 'ak' })).toBe('')
    expect(inferAuthMode({ ...authField, options: [] }, { access_key: 'ak' })).toBe('')
  })
  it('detects access_key first', () => {
    expect(inferAuthMode(authField, { access_key: 'ak', api_key: 'also' })).toBe('access_key')
  })
  it('detects api_key when access_key absent', () => {
    expect(inferAuthMode(authField, { api_key: 'glsa_xxx' })).toBe('api_key')
  })
  it('detects username_password (username/password keys)', () => {
    expect(inferAuthMode(authField, { username: 'admin', password: 'p' })).toBe('username_password')
  })
  it('detects username_password (user/pass keys)', () => {
    expect(inferAuthMode(authField, { user: 'admin', pass: 'p' })).toBe('username_password')
  })
  it('partial username_password missing one side returns empty', () => {
    expect(inferAuthMode(authField, { username: 'admin' })).toBe('')
    expect(inferAuthMode(authField, { user: 'admin' })).toBe('')
  })
  it('placeholder values are ignored', () => {
    expect(inferAuthMode(authField, { access_key: '{{ACCESS_KEY}}' })).toBe('')
  })
  it('returns empty when none match', () => {
    expect(inferAuthMode(authField, { unrelated: 'x' })).toBe('')
  })
  it('skips options that are not in authField.options', () => {
    const limited: CredField = { ...authField, options: [{ value: 'api_key', label: 'API Key' }] }
    expect(inferAuthMode(limited, { access_key: 'ak' })).toBe('')
    expect(inferAuthMode(limited, { api_key: 'k' })).toBe('api_key')
  })
})

describe('parseEnvironment', () => {
  it('extracts all fields with sensible fallbacks', () => {
    expect(parseEnvironment({ id: 'dev', api_domain: 'a', web_domain: 'w', is_prod: true }))
      .toEqual({ id: 'dev', api_domain: 'a', web_domain: 'w', is_prod: true })
  })
  it('handles missing fields', () => {
    expect(parseEnvironment({})).toEqual({ id: '', api_domain: '', web_domain: '', is_prod: false })
  })
  it('coerces is_prod with Boolean', () => {
    expect(parseEnvironment({ is_prod: 'truthy' }).is_prod).toBe(true)
    expect(parseEnvironment({ is_prod: 0 }).is_prod).toBe(false)
  })
  it('handles null/undefined input', () => {
    expect(parseEnvironment(null)).toEqual({ id: '', api_domain: '', web_domain: '', is_prod: false })
    expect(parseEnvironment(undefined)).toEqual({ id: '', api_domain: '', web_domain: '', is_prod: false })
  })
})

describe('parseRepoCore', () => {
  it('joins service_names array to csv', () => {
    const r = parseRepoCore({
      name: 'order', url: 'git@x:y.git', stack: 'go',
      service_names: ['order-svc', 'order-worker'],
    })
    expect(r.service_names).toBe('order-svc, order-worker')
  })
  it('preserves service_names string', () => {
    expect(parseRepoCore({ service_names: 'a, b' }).service_names).toBe('a, b')
  })
  it('defaults role=backend when missing or blank', () => {
    expect(parseRepoCore({}).role).toBe('backend')
    expect(parseRepoCore({ role: '   ' }).role).toBe('backend')
    expect(parseRepoCore({ role: 'frontend' }).role).toBe('frontend')
  })
  it('defaults stack=go when missing', () => {
    expect(parseRepoCore({}).stack).toBe('go')
  })
  it('trims sub_path', () => {
    expect(parseRepoCore({ sub_path: '  services/x  ' }).sub_path).toBe('services/x')
  })
  it('extracts service_entries record (string values only)', () => {
    expect(parseRepoCore({ service_entries: { foo: 'cmd/foo', bar: 42 } }).service_entries)
      .toEqual({ foo: 'cmd/foo' })
  })
  it('omits service_entries when input absent', () => {
    expect(parseRepoCore({}).service_entries).toBeUndefined()
  })
})

describe('placeholderName', () => {
  it('returns base unchanged for default source', () => {
    expect(placeholderName('CC_ADDR_DEV', 'default')).toBe('CC_ADDR_DEV')
    expect(placeholderName('CC_ADDR_DEV', '')).toBe('CC_ADDR_DEV')
  })
  it('appends uppercased sourceID with - → _ for non-default', () => {
    expect(placeholderName('CC_ADDR_DEV', 'legacy-nacos')).toBe('CC_ADDR_DEV_LEGACY_NACOS')
  })
})

describe('applyParsedYAMLToWizardState observability import', () => {
  it('restores k8s_runtime one2all cluster_id', async () => {
    const ctx: any = {
      system: { id: '', name: '', description: '' },
      agent: { id: '', name: '', workspace_name: '', model: '' },
      targetModels: {},
      environments: [],
      repos: [],
      enabledSourceTypes: {},
      enabledSourceOrder: [],
      sourceCreds: {},
      serviceSourceMap: {},
      ccCredInputs: {},
      envNamespaces: {},
      serviceConfigSel: {},
      serviceConfigGroup: {},
      ccHubStateByEnv: {},
      enabledObservability: { k8s_runtime: false },
      toolInputs: {},
      obsAccessModeMap: {},
      grafanaDsUidByObsEnv: {},
      k8sRuntimeEnvLoc: {},
      k8sRuntimeSvcMap: {},
      scannedDS: {},
      enabledDataStores: {},
      dsAutoFilled: {},
      dsScanState: {},
      ALL_SOURCE_TYPES: ['nacos', 'one2all'],
      CC_FIELDS_BY_TYPE: {},
      allServiceNames: ['order-service'],
      ensureKuboardLoc: () => ({}),
      ensureOne2AllLoc: () => ({}),
      getLokiMapping: () => ({ serviceValues: {} }),
      ccKeyFor: (type: string, envID: string, field: string) => `cc:${type}:${envID}:${field}`,
      svcKey: (envID: string, svc: string) => `${envID}::${svc}`,
      scanStateKey: (envID: string, svc: string) => `${envID}::${svc}`,
      toolKeyFor: (cat: 'obs' | 'ds', tool: string, envID: string, field: string) => `${cat}:${tool}:${envID}:${field}`,
      obsAccessKey: (obsKey: string, envID: string) => `${obsKey}:${envID}`,
      obsGrafanaDsKey: (obsKey: string, envID: string) => `${obsKey}::${envID}`,
      toolSpecByKey: () => undefined,
      pickBranchForEnv: () => '',
      getRepoPathsForSystem: async () => ({}),
      listBranchesForRepo: async () => [],
      setRepoBranches: () => {},
    }

    await applyParsedYAMLToWizardState({
      environments: [{ id: 'dev' }],
      infrastructure: {
        config_center: { type: 'nacos' },
        observability: {
          k8s_runtime: {
            enabled: true,
            provider: 'one2all',
            service_map: [{
              env: 'dev',
              service: 'order-service',
              cluster_id: '1',
              namespace: 'default',
              workload: 'order-service',
            }],
          },
        },
      },
    }, ctx)

    expect(ctx.enabledObservability.k8s_runtime).toBe(true)
    expect(ctx.toolInputs['obs:k8s_runtime:dev:provider']).toBe('one2all')
    expect(ctx.k8sRuntimeEnvLoc.dev.cluster_id).toBe('1')
    expect(ctx.k8sRuntimeEnvLoc.dev.namespace).toBe('default')
    expect(ctx.k8sRuntimeSvcMap['dev::order-service'].workload).toBe('order-service')
  })
})
