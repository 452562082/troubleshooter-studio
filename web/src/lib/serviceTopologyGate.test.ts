import { describe, expect, it } from 'vitest'
import {
  hasServiceTopologyWorkbenchRepos,
  type ServiceTopologyRepoGateInput,
} from './serviceTopologyGate'

type GateCase = [
  name: string,
  repos: ServiceTopologyRepoGateInput[],
  paths: Record<string, string>,
  expected: boolean,
]

const includedRoleCases: GateCase[] = ['frontend', 'mobile', 'admin', 'gateway', 'backend', 'middleware', ''].map(role => [
  `${role || 'empty/default'} role is a service node`,
  [{ name: 'caller', role }, { name: 'orders', role: 'backend' }],
  { caller: '/repos/caller', orders: '/repos/orders' },
  true,
])

const gateCases: GateCase[] = [
  ['zero resolved services', [], {}, false],
  ['one resolved service', [{ name: 'orders', role: 'backend' }], { orders: '/repos/orders' }, false],
  [
    'duplicate and blank names',
    [{ name: 'orders', role: 'backend' }, { name: ' orders ', role: 'gateway' }, { name: '', role: 'backend' }],
    { orders: '/repos/orders' },
    false,
  ],
  ...includedRoleCases,
  [
    'excluded roles do not count',
    [{ name: 'common', role: 'common-lib' }, { name: 'docs', role: 'docs' }, { name: 'infra', role: 'infra' }],
    { common: '/repos/common', docs: '/repos/docs', infra: '/repos/infra' },
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
]

describe('hasServiceTopologyWorkbenchRepos', () => {
  it.each(gateCases)('%s', (_name, repos, paths, expected) => {
    expect(hasServiceTopologyWorkbenchRepos(repos, paths)).toBe(expected)
  })
})
