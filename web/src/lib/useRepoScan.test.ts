import { reactive, ref } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const bridgeMocks = vi.hoisted(() => ({
  analyzeV2: vi.fn(),
  detectSubmodulesForRepo: vi.fn(async () => []),
  getRemoteURL: vi.fn(async () => 'git@example.com:team/base-backend.git'),
  isDesktop: vi.fn(() => true),
  openDir: vi.fn(),
  pathExists: vi.fn(async () => true),
  recommendRoleForRepo: vi.fn(async () => ({ role: 'backend', reason: 'service project' })),
}))

vi.mock('./bridge', () => bridgeMocks)
vi.mock('./toast', () => ({
  toast: { success: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

import { useRepoScan, type RepoScanItem } from './useRepoScan'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function makeRepo(): RepoScanItem {
  return reactive({
    name: 'base-backend',
    url: 'git@example.com:team/base-backend.git',
    stack: 'go',
    framework: 'gin',
    role: 'backend',
    service_names: 'base-backend-base',
    env_branches: { dev: 'dev', test: 'test' },
    _source: 'local',
    _localPath: '/repos/base-backend',
    _scanned: true,
    _scannedSource: '/repos/base-backend',
    _serviceEntries: { 'base-backend-base': 'base' },
  })
}

function makeScanner(repo: RepoScanItem, generateYAML: (options?: { omitServiceTopology?: boolean }) => string) {
  const repoBranchesMap = ref<Record<string, string[]>>({
    'base-backend': ['dev', 'test'],
  })
  const scanner = useRepoScan({
    repoBranchesMap,
    environments: reactive([{ id: 'dev' }, { id: 'test' }]),
    repos: reactive([repo]),
    reposRootInput: ref(''),
    resolvedReposRoot: ref('/repos'),
    pickBranchForEnv: (env, branches) => branches.find(branch => branch === env.id) || branches[0] || '',
    deriveRepoName: () => 'base-backend',
    generateYAML,
  })
  return { scanner, repoBranchesMap }
}

describe('useRepoScan atomic scan state', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    bridgeMocks.pathExists.mockResolvedValue(true)
    bridgeMocks.detectSubmodulesForRepo.mockResolvedValue([])
    bridgeMocks.recommendRoleForRepo.mockResolvedValue({ role: 'backend', reason: 'service project' })
  })

  it('keeps the previous scan result when validation fails before analysis', async () => {
    const repo = makeRepo()
    const generateYAML = vi.fn((options?: { omitServiceTopology?: boolean }) => {
      expect(options).toEqual({ omitServiceTopology: true })
      expect(repo.stack).toBe('go')
      expect(repo.service_names).toBe('base-backend-base')
      expect(repo.env_branches).toEqual({ dev: 'dev', test: 'test' })
      return 'scan-yaml-without-topology-overrides'
    })
    bridgeMocks.analyzeV2.mockRejectedValue(new Error(
      'validate: service_topology.overrides[0].to_service="base-backend-base" is not an effective service name',
    ))
    const { scanner, repoBranchesMap } = makeScanner(repo, generateYAML)

    await scanner.scanSingleRepo(repo)

    expect(generateYAML).toHaveBeenCalledOnce()
    expect(repo.stack).toBe('go')
    expect(repo.framework).toBe('gin')
    expect(repo.service_names).toBe('base-backend-base')
    expect(repo.env_branches).toEqual({ dev: 'dev', test: 'test' })
    expect(repoBranchesMap.value['base-backend']).toEqual(['dev', 'test'])
    expect(repo._scanned).toBe(true)
    expect(repo._scanError).toContain('service_topology.overrides')
  })

  it('replaces scan-derived fields only after the analyzer succeeds', async () => {
    const repo = makeRepo()
    const pending = deferred<any>()
    bridgeMocks.analyzeV2.mockReturnValue(pending.promise)
    const generateYAML = vi.fn(() => 'scan-yaml-without-topology-overrides')
    const { scanner, repoBranchesMap } = makeScanner(repo, generateYAML)

    const scan = scanner.scanSingleRepo(repo)
    await Promise.resolve()

    expect(repo.stack).toBe('go')
    expect(repo.service_names).toBe('base-backend-base')
    expect(repo.env_branches).toEqual({ dev: 'dev', test: 'test' })

    pending.resolve({
      per_repo: [{
        name: 'base-backend',
        status: 'analyzed',
        detected_stack: 'node',
        detected_framework: 'nestjs',
        branches: ['main', 'test'],
      }],
      report: {
        repos: [{ name: 'base-backend', service_names: ['base-backend-v2'] }],
      },
    })
    await scan

    expect(generateYAML).toHaveBeenCalledWith({ omitServiceTopology: true })
    expect(repo.stack).toBe('node')
    expect(repo.framework).toBe('nestjs')
    expect(repo.service_names).toBe('base-backend-v2')
    expect(repo.env_branches).toEqual({ dev: 'main', test: 'test' })
    expect(repoBranchesMap.value['base-backend']).toEqual(['main', 'test'])
    expect(repo._scanError).toBeUndefined()
    expect(repo._scanned).toBe(true)
  })
})
