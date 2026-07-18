import { computed, ref } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const bridgeMocks = vi.hoisted(() => ({
  defaultDestPath: vi.fn(async () => '/tmp/agent'),
  detectAITools: vi.fn(async () => ({ claude_code: { installed: true }, cursor: { installed: true } })),
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

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function makeFlow(targets: Array<'claude-code' | 'cursor'> = ['claude-code']) {
  const router = { push: vi.fn() }
  const repos = [
    { name: 'platform', _localPath: '/work/platform' },
    { name: 'orders', parent_repo: 'platform', parent_path: 'services/orders' },
  ]
  const flow = useDeployFlow({
    agent: { workspace_name: 'test-agent', model: '' },
    system: { id: 'test-system' },
    targetModels: {},
    enabledTargets: Object.fromEntries(targets.map(target => [target, true])),
    targetOptions: targets,
    targetLabels: { 'claude-code': 'Claude Code', cursor: 'Cursor' },
    homeDir: ref('/home/test'),
    activeSourceTypes: computed(() => []),
    sourceInstances: computed(() => []),
    sourceCreds: {},
    environments: [],
    enabledDataStores: {},
    dataStoreTypes: {},
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

  it('retains the first shared report across multiple deployment targets', async () => {
    const first = { ...partialReport, repos: [...partialReport.repos] }
    const later = { ...partialReport, ready: 0, repos: [] }
    bridgeMocks.importAndDeploy
      .mockResolvedValueOnce({ codegraph: first })
      .mockResolvedValueOnce({ codegraph: later })
    const { flow } = makeFlow(['claude-code', 'cursor'])

    await flow.runOneClickDeploy()

    expect(bridgeMocks.importAndDeploy).toHaveBeenCalledTimes(2)
    expect(flow.codeGraphReport.value).toEqual(first)
  })

  it('resets a stale report before a later deployment without a report', async () => {
    const { flow } = makeFlow()
    await flow.runOneClickDeploy()
    expect(flow.codeGraphReport.value).toEqual(partialReport)
    const second = deferred<Record<string, never>>()
    bridgeMocks.importAndDeploy.mockReturnValueOnce(second.promise)

    const nextDeploy = flow.runOneClickDeploy()

    expect(flow.codeGraphReport.value).toBeNull()
    second.resolve({})
    await nextDeploy
    expect(flow.codeGraphReport.value).toBeNull()
  })

  it('claims deploy state before async preflight so retry cannot overlap', async () => {
    const validation = deferred<{ name: string }>()
    bridgeMocks.validate.mockReturnValueOnce(validation.promise)
    const { flow } = makeFlow()

    const deploy = flow.runOneClickDeploy()
    await Promise.resolve()
    await flow.retryCodeGraph()

    expect(flow.deployLoading.value).toBe(true)
    expect(bridgeMocks.reindexCodeGraph).not.toHaveBeenCalled()
    validation.resolve({ name: 'test' })
    await deploy
  })

  it('does not start deploy while retry is active', async () => {
    const retryResult = deferred<typeof partialReport>()
    bridgeMocks.reindexCodeGraph.mockReturnValueOnce(retryResult.promise)
    const { flow } = makeFlow()

    const retry = flow.retryCodeGraph()
    await Promise.resolve()
    await flow.runOneClickDeploy()

    expect(flow.codeGraphRetrying.value).toBe(true)
    expect(bridgeMocks.validate).not.toHaveBeenCalled()
    expect(bridgeMocks.importAndDeploy).not.toHaveBeenCalled()
    retryResult.resolve(partialReport)
    await retry
  })

  it('cleans up retry state and reports an error when event subscription fails', async () => {
    runtimeMocks.EventsOn.mockImplementationOnce(() => {
      throw new Error('event bridge unavailable')
    })
    const { flow } = makeFlow()

    await expect(flow.retryCodeGraph()).resolves.toBeUndefined()

    expect(bridgeMocks.reindexCodeGraph).not.toHaveBeenCalled()
    expect(flow.codeGraphRetrying.value).toBe(false)
    expect(flow.codeGraphRetryState.value).toBe('error')
    expect(flow.codeGraphRetryFeedback.value).toContain('event bridge unavailable')
  })

  it('ignores an older completion after a newer deploy resets and replaces the report', async () => {
    const staleRetry = deferred<typeof partialReport>()
    const newerReport = { ...partialReport, ready: 0, repos: [] }
    bridgeMocks.reindexCodeGraph.mockReturnValueOnce(staleRetry.promise)
    bridgeMocks.importAndDeploy.mockResolvedValueOnce({ codegraph: newerReport })
    const { flow } = makeFlow()

    const older = flow.retryCodeGraph()
    await Promise.resolve()
    // Simulate a controller reset/cancel boundary before starting the next operation.
    flow.codeGraphRetrying.value = false
    await flow.runOneClickDeploy()
    expect(flow.codeGraphReport.value).toEqual(newerReport)

    staleRetry.resolve(partialReport)
    await older

    expect(flow.codeGraphReport.value).toEqual(newerReport)
  })
})
