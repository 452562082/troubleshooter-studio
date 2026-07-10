import { computed, ref } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const bridgeMocks = vi.hoisted(() => ({
  defaultDestPath: vi.fn(async () => '/tmp/agent'),
  detectAITools: vi.fn(async () => ({ claude_code: { installed: true } })),
  importAndDeploy: vi.fn(),
  reindexCodeGraph: vi.fn(),
  runInstall: vi.fn(),
  selfTestAgent: vi.fn(),
  validate: vi.fn(async () => ({ name: 'test' })),
  isDesktop: vi.fn(() => true),
}))

const runtimeMocks = vi.hoisted(() => ({
  EventsOn: vi.fn(() => vi.fn()),
}))

vi.mock('./bridge', () => bridgeMocks)
vi.mock('../../wailsjs/runtime/runtime', () => runtimeMocks)
vi.mock('./confirm', () => ({ confirmDialog: vi.fn(async () => true) }))
vi.mock('./logStore', () => ({ pushLog: vi.fn() }))
vi.mock('./toast', () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

import { useDeployFlow } from './useDeployFlow'

const partialReport = {
  ready: 1,
  total: 2,
  repos: [
    { name: 'platform', path: '/work/platform', action: 'initialized', status: 'ready', duration_ms: 10 },
    { name: 'orders', path: '/work/platform/services/orders', action: 'failed', status: 'warn', detail: 'timeout', duration_ms: 30 },
  ],
}

function makeFlow() {
  const router = { push: vi.fn() }
  const repos = [
    { name: 'platform', _localPath: '/work/platform' },
    { name: 'orders', parent_repo: 'platform', parent_path: 'services/orders' },
  ]
  const flow = useDeployFlow({
    agent: { workspace_name: 'test-agent', model: '' },
    system: { id: 'test-system' },
    targetModels: {},
    enabledTargets: { 'claude-code': true },
    targetOptions: ['claude-code'],
    targetLabels: { 'claude-code': 'Claude Code' },
    homeDir: ref('/home/test'),
    activeSourceTypes: computed(() => []),
    sourceCreds: {},
    environments: [],
    enabledDataStores: {},
    enabledObservability: {},
    toolInputs: {},
    OBS_TOOL_SPECS: [],
    DS_TOOL_SPECS: [],
    toolKeyFor: () => '',
    isObsFieldHidden: () => false,
    yamlOutput: ref('system:\n  id: test-system'),
    reposRootInput: ref(''),
    resolvedReposRoot: ref('/fallback'),
    repos: repos as any,
    resolveCloneDest: () => '',
    storageKey: 'test-draft',
    router: router as any,
  })
  return { flow, router }
}

describe('useDeployFlow CodeGraph report and retry', () => {
  beforeEach(() => {
    const storage = new Map<string, string>()
    vi.stubGlobal('localStorage', {
      getItem: (key: string) => storage.get(key) ?? null,
      setItem: (key: string, value: string) => storage.set(key, value),
      removeItem: (key: string) => storage.delete(key),
      clear: () => storage.clear(),
    })
    vi.clearAllMocks()
    runtimeMocks.EventsOn.mockImplementation(() => vi.fn())
    bridgeMocks.importAndDeploy.mockResolvedValue({ codegraph: partialReport })
    bridgeMocks.reindexCodeGraph.mockResolvedValue({
      ready: 2,
      total: 2,
      repos: partialReport.repos.map(repo => ({ ...repo, action: 'synced', status: 'ready', detail: undefined })),
    })
  })

  it('captures the first shared deploy report and keeps a partial report visible', async () => {
    const { flow, router } = makeFlow()

    await flow.runOneClickDeploy()

    expect(flow.codeGraphReport.value).toEqual(partialReport)
    expect(bridgeMocks.importAndDeploy).toHaveBeenCalledWith(
      expect.any(String),
      'claude-code',
      '/tmp/agent',
      {
        platform: '/work/platform',
        orders: '/work/platform/services/orders',
      },
      {},
    )
    expect(router.push).not.toHaveBeenCalled()
  })

  it('reuses the deploy path builder and replaces the same report on retry', async () => {
    const { flow } = makeFlow()
    await flow.runOneClickDeploy()
    runtimeMocks.EventsOn.mockClear()

    await flow.retryCodeGraph()

    expect(bridgeMocks.reindexCodeGraph).toHaveBeenCalledWith(
      expect.any(String),
      {
        platform: '/work/platform',
        orders: '/work/platform/services/orders',
      },
    )
    expect(flow.codeGraphReport.value?.ready).toBe(2)
    expect(flow.codeGraphRetryState.value).toBe('success')
    expect(flow.codeGraphRetryFeedback.value).toContain('2/2')
    expect(runtimeMocks.EventsOn).toHaveBeenCalledWith('install:log', expect.any(Function))
  })
})
