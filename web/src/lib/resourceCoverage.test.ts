import { describe, expect, it } from 'vitest'
import { buildResourceCoverage } from './resourceCoverage'

const base = {
  environments: [{ id: 'test' }],
  repos: [{ name: 'api', role: 'backend', stack: 'go', service_names: 'user-api', env_branches: { test: 'test' } }],
  businessServices: ['user-api'],
  runtimeWorkloads: ['user-api'],
  activeSourceTypes: ['nacos'],
  getServiceSource: () => 'nacos',
  scannedDS: {},
  dsProbeResults: {},
  enabledObservability: {},
  obsProbeResults: {},
  k8sRuntimeEnvLoc: {},
  k8sRuntimeSvcMap: {},
  svcKey: (env: string, svc: string) => `${env}::${svc}`,
  lokiMappingByEnv: {},
}

describe('buildResourceCoverage', () => {
  it('reports a code/config-only setup as basic and keeps unknown data explicit', () => {
    const result = buildResourceCoverage(base)
    expect(result.level).toBe('basic')
    expect(result.rows[0].data).toMatchObject({ state: 'partial' })
  })

  it('includes frontend workloads without requiring a config source', () => {
    const result = buildResourceCoverage({
      ...base,
      repos: [...base.repos, { name: 'admin-web', role: 'frontend', stack: 'node', service_names: '', env_branches: { test: 'test' } }],
      runtimeWorkloads: ['user-api', 'admin-web'],
    })
    const frontend = result.rows.find(row => row.resource === 'admin-web')!
    expect(frontend.kind).toBe('workload')
    expect(frontend.config).toEqual({ state: 'na', label: '无需配置源' })
  })

  it('reports exact workload and Loki labels as ready', () => {
    const result = buildResourceCoverage({
      ...base,
      enabledObservability: { k8s_runtime: true, loki: true },
      k8sRuntimeEnvLoc: { test: { cluster: 'test', namespace: 'app-test' } },
      k8sRuntimeSvcMap: { 'test::user-api': { workload: 'user-api' } },
      lokiMappingByEnv: {
        test: { dsUID: 'loki', envLabelKey: 'namespace', serviceLabelKey: 'app', envValue: 'app-test', serviceValues: { 'user-api': 'user-api' } },
      },
      scannedDS: {
        test: {
          'user-api': {
            redis: { url: 'redis://redis.test:6379' },
          },
        },
      },
      dsProbeResults: {
        'test::user-api::redis': { status: 'ok' },
      },
    })
    expect(result.rows[0].runtime.state).toBe('ready')
    expect(result.rows[0].logs.state).toBe('ready')
    expect(result.level).toBe('complete')
  })
})
