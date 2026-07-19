import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import YamlPreviewStep from './YamlPreviewStep.vue'

const coverage = {
  level: 'standard' as const,
  ready: 5,
  partial: 1,
  missing: 0,
  rows: [{
    env: 'test', resource: 'base-backend', kind: 'service' as const,
    code: { state: 'ready' as const, label: 'go · base-test' },
    config: { state: 'ready' as const, label: 'one2all' },
    data: { state: 'partial' as const, label: 'mysql · 待测试' },
    runtime: { state: 'ready' as const, label: 'truss-base-test' },
    logs: { state: 'ready' as const, label: 'service=base' },
    trace: { state: 'na' as const, label: '未启用' },
  }],
}

describe('YamlPreviewStep', () => {
  it('shows deployment capability coverage during generation preview instead of observability configuration', () => {
    const wrapper = mount(YamlPreviewStep, { props: {
      yamlOutput: 'system:\n  id: base', validateLoading: false, validateResult: null, copySuccess: false,
      targetOptions: ['codex'], enabledTargets: { codex: true }, targetLabels: { codex: 'Codex CLI' },
      anyTargetSelected: true, resourceCoverage: coverage,
    } })

    expect(wrapper.get('.coverage-panel').text()).toContain('部署能力覆盖')
    expect(wrapper.get('.coverage-panel').text()).toContain('生成前汇总')
    expect(wrapper.get('.coverage-panel').text()).toContain('base-backend')
    expect(wrapper.get('.coverage-panel').element.compareDocumentPosition(wrapper.get('.yaml-preview').element) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(readFileSync('src/components/ObservabilityStep.vue', 'utf8')).not.toContain('ResourceCoveragePanel')
  })
})
