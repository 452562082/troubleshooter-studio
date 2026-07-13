import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import OneClickDeployStep from './OneClickDeployStep.vue'

const report = {
  ready: 2,
  total: 3,
  repos: [
    {
      name: 'orders',
      path: '/work/orders',
      action: 'initialized' as const,
      status: 'ready' as const,
      file_count: 42,
      node_count: 100,
      edge_count: 150,
      duration_ms: 1234,
    },
    {
      name: 'docs',
      path: '',
      action: 'skipped' as const,
      status: 'skipped' as const,
      detail: '仓库路径未配置',
      duration_ms: 0,
    },
    {
      name: 'legacy',
      path: '/work/legacy',
      action: 'failed' as const,
      status: 'warn' as const,
      detail: 'CodeGraph sync timed out',
      duration_ms: 30000,
    },
  ],
}

function mountStep(overrides: Record<string, unknown> = {}) {
  return mount(OneClickDeployStep, {
    props: {
      deploySummary: [{ target: 'openclaw', label: 'OpenClaw', path: '/tmp/agent' }],
      deployLoading: false,
      deployError: null,
      codeGraphReport: report,
      codeGraphRetrying: false,
      codeGraphRetryFeedback: '',
      ...overrides,
    },
  })
}

describe('OneClickDeployStep CodeGraph report', () => {
  it('renders the shared summary, repository state, counts, and failure reason', () => {
    const wrapper = mountStep()
    const text = wrapper.text()

    expect(text).toContain('CodeGraph 2/3 repos ready')
    expect(text).toContain('orders')
    expect(text).toContain('initialized')
    expect(text).toContain('42 files')
    expect(text).toContain('100 nodes')
    expect(text).toContain('legacy')
    expect(text).toContain('CodeGraph sync timed out')
    expect(wrapper.get('[data-status="ready"]').text()).toContain('ready')
    expect(wrapper.get('[data-status="warn"]').text()).toContain('warn')
  })

  it('emits retry without a hidden repository path argument', async () => {
    const wrapper = mountStep()

    await wrapper.get('[data-test="retry-codegraph"]').trigger('click')

    expect(wrapper.emitted('retry-codegraph')).toEqual([[]])
  })

  it('disables retry while running and reserves an announced feedback region', () => {
    const wrapper = mountStep({ codeGraphRetrying: true, codeGraphRetryFeedback: '正在重新索引…' })
    const retry = wrapper.get('[data-test="retry-codegraph"]')

    expect(retry.attributes('disabled')).toBeDefined()
    expect(retry.text()).toContain('重新索引中')
    expect(wrapper.get('[data-test="codegraph-retry-feedback"]').attributes('aria-live')).toBe('polite')
    expect(wrapper.get('[data-test="codegraph-retry-feedback"]').text()).toContain('正在重新索引')
  })

  it('disables deploy while retry is active and does not emit an overlapping action', async () => {
    const wrapper = mountStep({ codeGraphRetrying: true })
    const deploy = wrapper.get('.deploy-final-btn')

    expect(deploy.attributes('disabled')).toBeDefined()
    await deploy.trigger('click')
    expect(wrapper.emitted('run-deploy')).toBeUndefined()
  })

  it('disables retry while deploy is active and does not emit an overlapping action', async () => {
    const wrapper = mountStep({ deployLoading: true })
    const retry = wrapper.get('[data-test="retry-codegraph"]')

    expect(retry.attributes('disabled')).toBeDefined()
    await retry.trigger('click')
    expect(wrapper.emitted('retry-codegraph')).toBeUndefined()
  })
})
