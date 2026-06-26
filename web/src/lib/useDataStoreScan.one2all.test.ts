import { computed, reactive, ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  one2allFetchConfigMaps: vi.fn(),
  probeDataStore: vi.fn(),
}))

vi.mock('./bridge/shared', () => ({
  isDesktop: () => true,
}))

vi.mock('./bridge/one2all', () => ({
  one2allFetchConfigMaps: mocks.one2allFetchConfigMaps,
}))

vi.mock('./bridge', () => ({
  fetchConfigContentBatch: vi.fn(),
  kuboardFetchConfigMaps: vi.fn(),
  probeDataStore: mocks.probeDataStore,
}))

vi.mock('./logStore', () => ({
  pushLog: vi.fn(),
}))

vi.mock('./toast', () => ({
  toast: {
    info: vi.fn(),
    success: vi.fn(),
    error: vi.fn(),
  },
}))

import { useDataStoreScan } from './useDataStoreScan'

describe('useDataStoreScan one2all import', () => {
  it('keeps datastore hits when another ConfigMap for the same service has no hit', async () => {
    mocks.one2allFetchConfigMaps.mockResolvedValue([
      {
        cluster_id: '1',
        namespace: 'default',
        name: 'data-cm',
        content: JSON.stringify({
          'datasource.yaml': `
redis:
  url: redis://cache:6379/0
mongodb:
  uri: mongodb://mongo:27017/app
`,
        }),
      },
      {
        cluster_id: '1',
        namespace: 'default',
        name: 'biz-cm',
        content: JSON.stringify({
          'business.yaml': `
distributed_lock:
  ttl: 30
tasks:
  cleanup: true
`,
        }),
      },
    ])
    mocks.probeDataStore.mockResolvedValue({ ok: true, latency: '1ms', detail: 'ok' })

    const scannedDS = reactive<Record<string, any>>({})
    const dsScanState = reactive<Record<string, any>>({})
    const dsProbeResults = reactive<Record<string, any>>({})
    const enabledDataStores = reactive<Record<string, boolean>>({})
    const dsAutoFilled = reactive<Record<string, boolean>>({})

    const scan = useDataStoreScan({
      scannedDS,
      dsScanState,
      dsProbeResults,
      dsImportStatus: ref('idle') as any,
      dsImportStats: { scanned: 0, matched: 0 },
      dsAutoFilled,
      enabledDataStores,
      scanStateKey: (envID, svc) => `${envID}::${svc}`,
      environments: [{ id: 'dev' }],
      allServiceNames: ref(['base-backend-base']),
      getServiceSource: () => 'one2all',
      svcKey: (envID, svc) => `${envID}::${svc}`,
      buildPreloadPayload: () => ({ type: 'none', addr: '', username: '', password: '', token: '', namespace: '', app_id: '', valid: false, missing: [] }),
      envNamespaces: {},
      serviceConfigSel: {},
      serviceConfigGroup: {},
      enabledSourceTypes: { one2all: true, kuboard: false },
      activeSourceTypes: computed(() => ['one2all']),
      ccCredInputs: {
        'cc:one2all:_shared_:mcp_url': 'http://one2all/mcp',
        'cc:one2all:_shared_:token': 'token',
      },
      ccKeyFor: (sourceType, envID, field) => `cc:${sourceType}:${envID}:${field}`,
      sourceCreds: {},
      kuboardSvcMap: {},
      one2allSvcMap: {
        'dev::base-backend-base': {
          cluster_id: '1',
          namespace: 'default',
          configmap: 'data-cm,biz-cm',
        },
      },
    })

    await scan.autoImportDataStores()

    expect(dsScanState['dev::base-backend-base']).toEqual({ status: 'ok' })
    expect(scannedDS.dev['base-backend-base'].redis.url).toBe('redis://cache:6379/0')
    expect(scannedDS.dev['base-backend-base'].mongodb.uri).toBe('mongodb://mongo:27017/app')
    expect(enabledDataStores.redis).toBe(true)
    expect(enabledDataStores.mongodb).toBe(true)
  })
})
