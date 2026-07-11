import { describe, expect, it } from 'vitest'
import { hasServiceTopologyWorkbenchRepos } from './serviceTopologyGate'

describe('hasServiceTopologyWorkbenchRepos', () => {
  it.each([
    ['zero resolved services', [], {}, false],
    ['one resolved service', [{ name: 'orders', role: 'backend' }], { orders: '/repos/orders' }, false],
    [
      'duplicate and blank names',
      [{ name: 'orders', role: 'backend' }, { name: ' orders ', role: 'gateway' }, { name: '', role: 'backend' }],
      { orders: '/repos/orders' },
      false,
    ],
    [
      'non-service roles',
      [{ name: 'web', role: 'frontend' }, { name: 'docs', role: 'docs' }, { name: 'infra', role: 'infra' }],
      { web: '/repos/web', docs: '/repos/docs', infra: '/repos/infra' },
      false,
    ],
    [
      'two service keys sharing one resolved path',
      [{ name: 'orders', role: 'backend' }, { name: 'gateway', role: 'gateway' }],
      { orders: '/repos/shared', gateway: '/repos/shared' },
      false,
    ],
    [
      'two distinct resolved service paths',
      [{ name: 'orders', role: 'backend' }, { name: 'gateway', role: 'gateway' }],
      { orders: '/repos/orders', gateway: '/repos/gateway' },
      true,
    ],
  ])('%s', (_name, repos, paths, expected) => {
    expect(hasServiceTopologyWorkbenchRepos(repos, paths)).toBe(expected)
  })
})
