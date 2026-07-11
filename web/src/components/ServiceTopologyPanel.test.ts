import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
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
    services: [],
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

function mountPanel(overrides: Partial<{
  snapshot: topology.Snapshot | null
  overrides: ServiceTopologyOverrideState[]
  loading: boolean
  disabled: boolean
  error: string
}> = {}) {
  return mount(ServiceTopologyPanel, {
    props: {
      snapshot: makeTopologySnapshot({ edges }),
      overrides: [],
      loading: false,
      disabled: false,
      error: '',
      ...overrides,
    },
  })
}

describe('ServiceTopologyPanel', () => {
  it('shows text and color statuses and exposes selected endpoint evidence', async () => {
    const wrapper = mountPanel()

    expect(wrapper.get('[data-edge="web-bff"] [data-status]').text()).toContain('自动采纳')
    expect(wrapper.get('[data-edge="bff-order"] [data-status]').classes()).toContain('status--candidate')
    expect(wrapper.get('[data-edge="bff-order"] [data-status]').text()).toContain('待确认')
    expect(wrapper.get('[data-edge="web-legacy"] [data-status]').text()).toContain('已拒绝')
    expect(wrapper.get('[data-edge="bff-profile"] [data-status]').text()).toContain('已失效')

    const candidate = wrapper.get('[data-edge="bff-order"]')
    expect(candidate.element.tagName).toBe('BUTTON')
    await candidate.trigger('click')

    expect(wrapper.text()).toContain('app/Clients/OrderClient.php:31')
    expect(wrapper.text()).toContain('internal/http/order.go:48')
    expect(wrapper.text()).toContain('method_path_exact')
    expect(wrapper.get('[data-action="confirm"]').element.tagName).toBe('BUTTON')
  })

  it('confirms a candidate by emitting only the updated override array', async () => {
    const wrapper = mountPanel()

    await wrapper.get('[data-edge="bff-order"]').trigger('click')
    await wrapper.get('[data-action="confirm"]').trigger('click')

    expect(wrapper.emitted('update:overrides')?.[0]?.[0]).toEqual([
      expect.objectContaining({
        action: 'confirm',
        fromService: 'mall-bff',
        toService: 'mall-order',
        protocol: 'http',
      }),
    ])
  })

  it('retargets by rejecting the old edge and adding the replacement', async () => {
    const wrapper = mountPanel()

    await wrapper.get('[data-edge="bff-order"]').trigger('click')
    await wrapper.get('[data-retarget-service]').setValue('mall-orders-v2')
    await wrapper.get('[data-action="retarget"]').trigger('click')

    expect(wrapper.emitted('update:overrides')?.[0]?.[0]).toEqual([
      expect.objectContaining({ action: 'reject', fromService: 'mall-bff', toService: 'mall-order' }),
      expect.objectContaining({ action: 'add', fromService: 'mall-bff', toService: 'mall-orders-v2' }),
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

    expect(wrapper.find('[data-edge="bff-profile"]').exists()).toBe(true)
    await wrapper.get('[data-edge="web-bff"]').trigger('click')
    await wrapper.get('[data-action="reject"]').trigger('click')

    expect(wrapper.emitted('update:overrides')?.[0]?.[0]).toEqual([
      staleConfirm,
      expect.objectContaining({ action: 'reject', fromService: 'mall-web', toService: 'mall-bff' }),
    ])
  })

  it('emits a manual add and disables every mutation during refresh', async () => {
    const wrapper = mountPanel()

    await wrapper.get('[data-add-from]').setValue('mall-web')
    await wrapper.get('[data-add-to]').setValue('mall-search')
    await wrapper.get('[data-add-path]').setValue('/internal/search')
    await wrapper.get('[data-action="add"]').trigger('click')
    expect(wrapper.emitted('update:overrides')?.[0]?.[0]).toEqual([
      expect.objectContaining({
        action: 'add',
        fromService: 'mall-web',
        toService: 'mall-search',
        method: 'GET',
        path: '/internal/search',
      }),
    ])

    await wrapper.setProps({ loading: true })
    for (const button of wrapper.findAll('[data-mutation]')) {
      expect(button.attributes('disabled')).toBeDefined()
    }
    expect(wrapper.get('[data-action="refresh"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-feedback]').attributes('aria-live')).toBe('polite')
  })

  it('announces errors and disables refresh when an external operation is active', async () => {
    const wrapper = mountPanel({ disabled: true, error: '拓扑分析失败: timeout' })

    expect(wrapper.get('[role="alert"]').text()).toContain('timeout')
    expect(wrapper.get('[data-action="refresh"]').attributes('disabled')).toBeDefined()
    await wrapper.get('[data-action="refresh"]').trigger('click')
    expect(wrapper.emitted('refresh')).toBeUndefined()
  })
})
