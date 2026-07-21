import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { topology } from '../../wailsjs/go/models'
import type { ServiceTopologyOverrideState } from '../lib/yamlGenerator'
import ServiceTopologyPanel from './ServiceTopologyPanel.vue'

function makeTopologyEdge(
  id: string,
  from: string,
  to: string,
  status: string,
  confidence: number,
): topology.CandidateEdge {
  return topology.CandidateEdge.createFrom({
    from_endpoint: `${id}:out`,
    to_endpoint: `${id}:in`,
    from_service: from,
    to_service: to,
    protocol: 'http',
    method: 'POST',
    path: '/internal/orders',
    confidence,
    status,
    reasons: ['method_path_exact'],
    conflicts: [],
  })
}

function makeTopologySnapshot(input: { edges: topology.CandidateEdge[] }): topology.Snapshot {
  const endpoints = input.edges.flatMap((edge) => [
    topology.Endpoint.createFrom({
      id: edge.from_endpoint,
      repo: edge.from_service,
      service: edge.from_service,
      direction: 'outbound',
      protocol: 'http',
      method: 'POST',
      path: '/internal/orders',
      location: 'app/Clients/OrderClient.php:31',
      source: 'fixture',
      transforms: [],
    }),
    topology.Endpoint.createFrom({
      id: edge.to_endpoint,
      repo: edge.to_service,
      service: edge.to_service,
      direction: 'inbound',
      protocol: 'http',
      method: 'POST',
      path: '/internal/orders',
      location: 'internal/http/order.go:48',
      source: 'fixture',
      transforms: [],
    }),
  ])
  return topology.Snapshot.createFrom({
    schema_version: '1',
    services: [
      'mall-bff',
      'mall-order',
      'mall-search',
      'mall-web',
      'profile',
      '',
      'mall-web',
    ].map(service => topology.ServiceDescriptor.createFrom({ repo: service, service })),
    endpoints,
    edges: input.edges,
    repositories: [],
  })
}

const edges = [
  makeTopologyEdge('web-bff', 'mall-web', 'mall-bff', 'automatic', 0.98),
  makeTopologyEdge('bff-order', 'mall-bff', 'mall-order', 'candidate', 0.76),
  makeTopologyEdge('web-legacy', 'mall-web', 'legacy-bff', 'rejected', 0.98),
  makeTopologyEdge('bff-profile', 'mall-bff', 'profile', 'stale', 1),
]

function relationSelector(from: string, to: string): string {
  return `[data-from-service="${from}"][data-to-service="${to}"]`
}

function mountPanel(overrides: Partial<{
  snapshot: topology.Snapshot | null
  overrides: ServiceTopologyOverrideState[]
  loading: boolean
  disabled: boolean
  error: string
  configuredServices: string[]
}> = {}) {
  return mount(ServiceTopologyPanel, {
    props: {
      snapshot: makeTopologySnapshot({ edges }),
      overrides: [],
      loading: false,
      disabled: false,
      error: '',
      configuredServices: [],
      ...overrides,
    },
  })
}

describe('ServiceTopologyPanel', () => {
  afterEach(() => vi.restoreAllMocks())

  it('shows text and color statuses and exposes selected endpoint evidence', async () => {
    const wrapper = mountPanel()

    expect(wrapper.get(`${relationSelector('mall-web', 'mall-bff')} [data-status]`).text()).toContain('自动采纳')
    expect(wrapper.get(`${relationSelector('mall-bff', 'mall-order')} [data-status]`).classes()).toContain('status--candidate')
    expect(wrapper.get(`${relationSelector('mall-bff', 'mall-order')} [data-status]`).text()).toContain('待确认')
    expect(wrapper.get(`${relationSelector('mall-web', 'legacy-bff')} [data-status]`).text()).toContain('已拒绝')
    expect(wrapper.get(`${relationSelector('mall-bff', 'profile')} [data-status]`).text()).toContain('已失效')

    const candidate = wrapper.get(relationSelector('mall-bff', 'mall-order'))
    expect(candidate.element.tagName).toBe('BUTTON')
    await candidate.trigger('click')

    expect(wrapper.text()).toContain('app/Clients/OrderClient.php:31')
    expect(wrapper.text()).toContain('internal/http/order.go:48')
    expect(wrapper.text()).toContain('method_path_exact')
    expect(wrapper.get('[data-action="confirm"]').element.tagName).toBe('BUTTON')
  })

  it('confirms a candidate by emitting only the updated override array', async () => {
    const wrapper = mountPanel()

    await wrapper.get(relationSelector('mall-bff', 'mall-order')).trigger('click')
    await wrapper.get('[data-action="confirm"]').trigger('click')

    expect(wrapper.emitted('update:overrides')?.[0]?.[0]).toEqual([
      expect.objectContaining({
        action: 'confirm',
        scope: 'service',
        fromService: 'mall-bff',
        toService: 'mall-order',
      }),
    ])
  })

  it('renders an already confirmed relation as completed instead of a no-op action', async () => {
    const confirmed = makeTopologyEdge('confirmed', 'mall-web', 'mall-bff', 'confirmed', 0.76)
    const wrapper = mountPanel({ snapshot: makeTopologySnapshot({ edges: [confirmed] }) })

    const confirm = wrapper.get('[data-action="confirm"]')
    expect(confirm.text()).toBe('已确认')
    expect(confirm.attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-decision-feedback]').text()).toContain('写入 YAML')

    await confirm.trigger('click')
    expect(wrapper.emitted('update:overrides')).toBeUndefined()
  })

  it('allows a rejected relation to be confirmed again', async () => {
    const rejected = makeTopologyEdge('rejected', 'mall-web', 'mall-bff', 'rejected', 0.76)
    const wrapper = mountPanel({ snapshot: makeTopologySnapshot({ edges: [rejected] }) })

    expect(wrapper.get('[data-action="reject"]').text()).toBe('已拒绝')
    expect(wrapper.get('[data-action="reject"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-action="confirm"]').attributes('disabled')).toBeUndefined()
    await wrapper.get('[data-action="confirm"]').trigger('click')

    expect(wrapper.emitted('update:overrides')?.[0]?.[0]).toEqual([
      expect.objectContaining({ action: 'confirm', scope: 'service', fromService: 'mall-web', toService: 'mall-bff' }),
    ])
  })

  it('rejects an arbitrary retarget and accepts a configured service from a labeled select', async () => {
    const wrapper = mountPanel()

    await wrapper.get(relationSelector('mall-bff', 'mall-order')).trigger('click')
    const retarget = wrapper.get('[data-retarget-service]')
    expect(retarget.element.tagName).toBe('SELECT')
    expect(wrapper.get('label[for="topology-retarget"]').text()).toContain('目标服务')

    await wrapper.get('[data-retarget-service]').setValue('mall-orders-v2')
    await wrapper.get('[data-action="retarget"]').trigger('click')
    expect(wrapper.emitted('update:overrides')).toBeUndefined()
    expect(wrapper.get('[data-validation]').attributes('role')).toBe('alert')
    expect(wrapper.get('[data-validation]').attributes('aria-live')).toBe('polite')
    expect(wrapper.get('[data-validation]').text()).toContain('有效服务')
    expect(wrapper.get('[data-action="retarget"]').attributes('disabled')).toBeDefined()

    await wrapper.get('[data-retarget-service]').setValue('mall-search')
    await wrapper.get('[data-action="retarget"]').trigger('click')

    expect(wrapper.emitted('update:overrides')?.[0]?.[0]).toEqual([
      expect.objectContaining({ action: 'reject', scope: 'service', fromService: 'mall-bff', toService: 'mall-order' }),
      expect.objectContaining({ action: 'add', scope: 'service', fromService: 'mall-bff', toService: 'mall-search' }),
    ])
  })

  it('allows automatic edges to be rejected and keeps stale confirmed edges visible', async () => {
    const staleConfirm: ServiceTopologyOverrideState = {
      action: 'confirm',
      fromService: 'mall-bff',
      toService: 'profile',
      protocol: 'http',
      method: 'POST',
      path: '/internal/orders',
    }
    const wrapper = mountPanel({ overrides: [staleConfirm] })

    expect(wrapper.find(relationSelector('mall-bff', 'profile')).exists()).toBe(true)
    await wrapper.get(relationSelector('mall-web', 'mall-bff')).trigger('click')
    await wrapper.get('[data-action="reject"]').trigger('click')

    expect(wrapper.emitted('update:overrides')?.[0]?.[0]).toEqual([
      staleConfirm,
      expect.objectContaining({ action: 'reject', scope: 'service', fromService: 'mall-web', toService: 'mall-bff' }),
    ])
  })

  it('only adds distinct configured services through semantic labeled selects', async () => {
    const wrapper = mountPanel()

    expect(wrapper.get('[data-add-from]').element.tagName).toBe('SELECT')
    expect(wrapper.get('[data-add-to]').element.tagName).toBe('SELECT')
    expect(wrapper.get('label[for="topology-add-from"]').text()).toContain('来源服务')
    expect(wrapper.get('label[for="topology-add-to"]').text()).toContain('目标服务')
    expect(wrapper.findAll('[data-add-from] option').map(option => option.attributes('value'))).toEqual([
      '', 'mall-bff', 'mall-order', 'mall-search', 'mall-web', 'profile',
    ])

    await wrapper.get('[data-add-from]').setValue('mall-web')
    await wrapper.get('[data-add-to]').setValue('mall-orders-v2')
    await wrapper.get('[data-action="add"]').trigger('click')
    expect(wrapper.emitted('update:overrides')).toBeUndefined()
    expect(wrapper.get('[data-validation]').text()).toContain('有效服务')

    await wrapper.get('[data-add-to]').setValue('mall-web')
    expect(wrapper.get('[data-action="add"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-validation]').text()).toContain('不能相同')

    await wrapper.get('[data-add-to]').setValue('mall-search')
    await wrapper.get('[data-action="add"]').trigger('click')
    expect(wrapper.emitted('update:overrides')?.[0]?.[0]).toEqual([
      expect.objectContaining({
        action: 'add',
        scope: 'service',
        fromService: 'mall-web',
        toService: 'mall-search',
      }),
    ])
    expect(wrapper.find('[data-add-protocol]').exists()).toBe(false)
    expect(wrapper.find('[data-add-method]').exists()).toBe(false)
    expect(wrapper.find('[data-add-path]').exists()).toBe(false)
  })

  it('keeps repository-configured services selectable before the first topology snapshot exists', () => {
    const wrapper = mountPanel({
      snapshot: null,
      loading: true,
      configuredServices: ['base-frontend', 'base-admin-frontend', 'base-frontend', ''],
    })

    expect(wrapper.findAll('[data-add-from] option').map(option => option.attributes('value'))).toEqual([
      '', 'base-admin-frontend', 'base-frontend',
    ])
    expect(wrapper.findAll('[data-add-to] option').map(option => option.attributes('value'))).toEqual([
      '', 'base-admin-frontend', 'base-frontend',
    ])
  })

  it('aggregates repeated endpoint evidence into one service relation', () => {
    const duplicatePair = [
      makeTopologyEdge('web-bff-orders', 'mall-web', 'mall-bff', 'candidate', 0.35),
      makeTopologyEdge('web-bff-profile', 'mall-web', 'mall-bff', 'candidate', 0.76),
    ]
    const wrapper = mountPanel({ snapshot: makeTopologySnapshot({ edges: duplicatePair }) })

    expect(wrapper.findAll('.edge-button')).toHaveLength(1)
    expect(wrapper.get(relationSelector('mall-web', 'mall-bff')).text()).toContain('2 条端点证据')
    expect(wrapper.findAll('.relation-evidence-card')).toHaveLength(2)
    expect(wrapper.get('[data-feedback]').text()).toContain('聚合为 1 条服务关系')
  })

  it('disables every mutation during refresh', async () => {
    const wrapper = mountPanel({ loading: true })

    for (const button of wrapper.findAll('[data-mutation]')) {
      expect(button.attributes('disabled')).toBeDefined()
    }
    expect(wrapper.get('[data-action="refresh"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-feedback]').attributes('aria-live')).toBe('polite')
  })

  it('prevents and clears refresh-button text selection across the loading label change', async () => {
    const removeAllRanges = vi.fn()
    vi.spyOn(window, 'getSelection').mockReturnValue({ removeAllRanges } as unknown as Selection)
    const wrapper = mountPanel()
    const refresh = wrapper.get('[data-action="refresh"]')
    const selectStart = new Event('selectstart', { bubbles: true, cancelable: true })

    refresh.element.dispatchEvent(selectStart)
    expect(selectStart.defaultPrevented).toBe(true)
    await refresh.trigger('click')
    expect(removeAllRanges).toHaveBeenCalledTimes(1)
    expect(wrapper.emitted('refresh')).toHaveLength(1)

    await wrapper.setProps({ loading: true })
    expect(removeAllRanges).toHaveBeenCalledTimes(2)
    expect(refresh.text()).toContain('扫描调用关系中')
  })

  it('announces errors and disables refresh when an external operation is active', async () => {
    const wrapper = mountPanel({ disabled: true, error: '拓扑分析失败: timeout' })

    expect(wrapper.get('[role="alert"]').text()).toContain('timeout')
    expect(wrapper.get('[data-action="refresh"]').attributes('disabled')).toBeDefined()
    for (const button of wrapper.findAll('[data-mutation]')) {
      expect(button.attributes('disabled')).toBeDefined()
    }
    await wrapper.get('[data-action="refresh"]').trigger('click')
    expect(wrapper.emitted('refresh')).toBeUndefined()
  })

  it('explains zero edges with endpoint and per-repository scan diagnostics', () => {
    const snapshot = topology.Snapshot.createFrom({
      schema_version: '1',
      services: ['mall-web', 'mall-api'].map(service => topology.ServiceDescriptor.createFrom({ repo: service, service })),
      endpoints: [
        topology.Endpoint.createFrom({
          id: 'web-out', repo: 'mall-web', service: 'mall-web', direction: 'outbound',
          protocol: 'http', method: 'GET', path: '/api/orders', location: 'src/api.ts:1', source: 'api-fetch', transforms: [],
        }),
        topology.Endpoint.createFrom({
          id: 'api-in', repo: 'mall-api', service: 'mall-api', direction: 'inbound',
          protocol: 'http', method: 'POST', path: '/api/orders', location: 'routes.go:1', source: 'go-route', transforms: [],
        }),
      ],
      edges: [],
      repositories: [
        topology.RepositoryStatus.createFrom({ repo: 'mall-web', state: 'scanned', endpoint_count: 1 }),
        topology.RepositoryStatus.createFrom({ repo: 'mall-api', state: 'scanned', endpoint_count: 1 }),
      ],
    })
    const wrapper = mountPanel({ snapshot })

    expect(wrapper.get('[data-feedback]').text()).toContain('已扫描 2/2 个仓库')
    expect(wrapper.get('[data-feedback]').text()).toContain('1 个调用出口')
    expect(wrapper.get('[data-topology-empty]').text()).toContain('方法、路径或服务别名未能唯一对应')
    expect(wrapper.get('[data-topology-empty]').text()).toContain('mall-web')
    expect(wrapper.get('[data-action="refresh"]').text()).toContain('重新扫描调用关系')
  })
})
