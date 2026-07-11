import { ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import { topology } from '../../wailsjs/go/models'
import type { ServiceTopologyOverrideState } from './yamlGenerator'
import { useServiceTopology } from './useServiceTopology'

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

function makeEdge(toService: string, status = 'candidate'): topology.CandidateEdge {
  return topology.CandidateEdge.createFrom({
    from_endpoint: `web:${toService}:out`,
    to_endpoint: `${toService}:in`,
    from_service: 'web',
    to_service: toService,
    protocol: 'http',
    method: 'GET',
    path: `/${toService}`,
    confidence: 0.76,
    status,
    reasons: ['method_path_exact'],
    conflicts: [],
  })
}

function makeSnapshot(toService: string): topology.Snapshot {
  const edge = makeEdge(toService)
  return topology.Snapshot.createFrom({
    schema_version: '1',
    services: [],
    endpoints: [],
    edges: [edge],
    repositories: [],
  })
}

function makeTopology(analyze: (yaml: string, paths: Record<string, string>) => Promise<topology.Snapshot>) {
  const overrides = ref<ServiceTopologyOverrideState[]>([])
  const topologyState = useServiceTopology({
    overrides,
    yamlText: () => 'system:\n  id: mall',
    repoPaths: () => ({ web: '/repos/web', order: '/repos/order' }),
    analyze,
  })
  return { topologyState, overrides }
}

describe('useServiceTopology', () => {
  it('rejects overlapping refreshes', async () => {
    const pending = deferred<topology.Snapshot>()
    const analyze = vi.fn(() => pending.promise)
    const { topologyState } = makeTopology(analyze)

    const first = topologyState.refresh()
    await Promise.resolve()
    await expect(topologyState.refresh()).resolves.toBe(false)
    expect(analyze).toHaveBeenCalledTimes(1)
    expect(topologyState.loading.value).toBe(true)

    pending.resolve(makeSnapshot('orders'))
    await expect(first).resolves.toBe(true)
  })

  it('ignores a stale completion after clear starts a newer generation', async () => {
    const slow = deferred<topology.Snapshot>()
    const fast = deferred<topology.Snapshot>()
    const analyze = vi.fn()
      .mockReturnValueOnce(slow.promise)
      .mockReturnValueOnce(fast.promise)
    const { topologyState } = makeTopology(analyze)

    const oldRefresh = topologyState.refresh()
    topologyState.clear()
    const newRefresh = topologyState.refresh()
    fast.resolve(makeSnapshot('profile'))
    await newRefresh
    slow.resolve(makeSnapshot('orders'))
    await oldRefresh

    expect(topologyState.snapshot.value?.edges[0]?.to_service).toBe('profile')
    expect(topologyState.loading.value).toBe(false)
  })

  it('retains the current snapshot and exposes an announced error message on failure', async () => {
    const analyze = vi.fn()
      .mockResolvedValueOnce(makeSnapshot('orders'))
      .mockRejectedValueOnce(new Error('analyzer timed out'))
    const { topologyState } = makeTopology(analyze)

    await topologyState.refresh()
    await expect(topologyState.refresh()).resolves.toBe(false)

    expect(topologyState.snapshot.value?.edges[0]?.to_service).toBe('orders')
    expect(topologyState.error.value).toContain('analyzer timed out')
    expect(topologyState.loading.value).toBe(false)
  })

  it('derives immediate statuses while mutations change only override state', async () => {
    const raw = makeSnapshot('orders')
    const { topologyState, overrides } = makeTopology(vi.fn(async () => raw))
    await topologyState.refresh()
    const edge = topologyState.snapshot.value?.edges[0]
    if (!edge) throw new Error('fixture edge missing')

    topologyState.confirm(edge)
    expect(overrides.value).toEqual([
      expect.objectContaining({ action: 'confirm', fromService: 'web', toService: 'orders' }),
    ])
    expect(raw.edges[0]?.status).toBe('candidate')
    expect(topologyState.snapshot.value?.edges[0]?.status).toBe('confirmed')

    topologyState.retarget(edge, 'orders-v2')
    expect(overrides.value).toEqual([
      expect.objectContaining({ action: 'reject', toService: 'orders' }),
      expect.objectContaining({ action: 'add', toService: 'orders-v2' }),
    ])
    expect(topologyState.snapshot.value?.edges.some(candidate => (
      candidate.to_service === 'orders-v2' && candidate.status === 'manual'
    ))).toBe(true)
  })

  it('supports reject, add, and clear without invoking analysis', () => {
    const analyze = vi.fn(async () => makeSnapshot('orders'))
    const { topologyState, overrides } = makeTopology(analyze)
    const edge = makeEdge('orders', 'automatic')

    topologyState.reject(edge)
    topologyState.add({
      action: 'add',
      fromService: 'web',
      toService: 'search',
      protocol: 'http',
      method: 'GET',
      path: '/search',
    })
    expect(overrides.value.map(item => item.action)).toEqual(['reject', 'add'])
    expect(analyze).not.toHaveBeenCalled()

    topologyState.clear()
    expect(overrides.value).toEqual([])
    expect(topologyState.snapshot.value).toBeNull()
    expect(topologyState.error.value).toBe('')
  })

  it('keeps server-reported stale confirmations stale until evidence returns', async () => {
    const snapshot = makeSnapshot('profile')
    snapshot.edges[0].status = 'stale'
    const { topologyState, overrides } = makeTopology(vi.fn(async () => snapshot))
    overrides.value = [{
      action: 'confirm',
      fromService: 'web',
      toService: 'profile',
      protocol: 'http',
      method: 'GET',
      path: '/profile',
    }]

    await topologyState.refresh()

    expect(topologyState.snapshot.value?.edges[0]?.status).toBe('stale')
  })

  it('upserts decisions using backend-equivalent route normalization', () => {
    const { topologyState, overrides } = makeTopology(vi.fn(async () => makeSnapshot('orders')))
    overrides.value = [{
      action: 'confirm',
      fromService: 'web',
      toService: 'orders',
      protocol: 'http',
      method: 'get',
      path: 'https://orders.test/orders/:id/?debug=true',
    }]
    const edge = makeEdge('orders')
    edge.path = '/orders/{orderId}'

    topologyState.reject(edge)

    expect(overrides.value).toEqual([
      expect.objectContaining({ action: 'reject', method: 'GET', path: '/orders/{orderId}' }),
    ])
  })

  it.each([
    ['encoded parameter', '/orders/%7Bid%7D', '/orders/{orderId}'],
    ['encoded wildcard', '/files/%2Apath', '/files/{*rest}'],
    ['encoded slash', '/orders%2F%7Bid%7D', '/orders/{orderId}'],
    ['absolute encoded query and trailing slash', 'https://orders.test/orders/%7Bid%7D%2F?debug=true', '/orders/{orderId}'],
    ['encoded question mark across relative and absolute URLs', '/search%3Fterm?debug=true', 'https://orders.test/search%3Fterm?trace=true'],
    ['scheme-relative URL host', '//orders.test/orders/%7Bid%7D?debug=true', '/orders/{orderId}'],
  ])('deduplicates %s paths like Go net/url', (_name, existingPath, incomingPath) => {
    const { topologyState, overrides } = makeTopology(vi.fn(async () => makeSnapshot('orders')))
    overrides.value = [{
      action: 'confirm',
      fromService: 'web',
      toService: 'orders',
      protocol: 'http',
      method: 'GET',
      path: existingPath,
    }]
    const edge = makeEdge('orders')
    edge.path = incomingPath

    topologyState.reject(edge)

    expect(overrides.value).toHaveLength(1)
    expect(overrides.value[0]).toEqual(expect.objectContaining({ action: 'reject', path: incomingPath }))
  })

  it('does not throw or over-normalize malformed percent encodings', () => {
    const { topologyState, overrides } = makeTopology(vi.fn(async () => makeSnapshot('orders')))
    overrides.value = [{
      action: 'confirm',
      fromService: 'web',
      toService: 'orders',
      protocol: 'http',
      method: 'GET',
      path: '/orders/%ZZ?debug=true',
    }]
    const edge = makeEdge('orders')
    edge.path = '/orders/%ZZ'

    expect(() => topologyState.reject(edge)).not.toThrow()
    expect(overrides.value).toHaveLength(1)
  })

  it('does not throw for a malformed scheme-relative percent encoding', () => {
    const { topologyState, overrides } = makeTopology(vi.fn(async () => makeSnapshot('orders')))
    const edge = makeEdge('orders')
    edge.path = '//orders.test/orders/%ZZ?debug=true'

    expect(() => topologyState.reject(edge)).not.toThrow()
    expect(overrides.value).toHaveLength(1)
  })
})
