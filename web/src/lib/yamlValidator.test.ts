import { describe, it, expect } from 'vitest'
import {
  computeStepErrors, labelForErrorKey, ccKeyFor, svcKey, probeKey,
  type ValidatorContext,
} from './yamlValidator'

function makeCtx(overrides: Partial<ValidatorContext> = {}): ValidatorContext {
  return {
    step: 2,
    system: { id: 'shop', name: 'Shop' },
    agent: { name: 'Shop bot' },
    enabledTargets: { openclaw: true },
    targetModels: { openclaw: 'anthropic/claude-sonnet-4-6' },
    anyTargetSelected: true,
    environments: [{ id: 'dev', api_domain: 'api-dev.shop' }],
    repos: [{ name: 'order', url: 'git@github.com:shop/order.git', stack: 'go', _source: 'remote', _cloneTarget: '/tmp/repos' }],
    isMultiSource: false,
    allServiceNames: ['order-service'],
    activeSourceTypes: ['nacos'],
    CC_FIELDS_BY_TYPE: {
      nacos: [
        { key: 'addr', label: 'Nacos addr', secret: false, envVar: (e) => `CC_ADDR_${e.toUpperCase()}` },
      ],
    },
    ccCredInputs: { 'cc:nacos:dev:addr': 'nacos:8848' },
    sourceCreds: {},
    envNamespaces: { dev: 'DEV_NS' },
    serviceConfigSel: { 'dev::order-service': 'order.yaml' },
    kuboardStateByEnv: {},
    kuboardSvcMap: {},
    ccHubStateByEnv: { dev: { status: 'ok' } },
    dsProbeResults: {},
    isFieldHidden: () => false,
    getServiceSource: () => 'nacos',
    enumerateDataStoreProbeTargets: () => [],
    enabledObservability: {},
    ...overrides,
  }
}

describe('computeStepErrors', () => {
  it('step 1 (welcome) has no errors regardless', () => {
    expect(computeStepErrors(makeCtx({ step: 1, system: { id: '', name: '' } })).size).toBe(0)
  })

  it('step 2 flags missing system fields', () => {
    const errs = computeStepErrors(makeCtx({ step: 2, system: { id: '', name: '' } }))
    expect(errs.has('system.id')).toBe(true)
    expect(errs.has('system.name')).toBe(true)
  })

  it('step 2 flags non-kebab system.id', () => {
    expect(computeStepErrors(makeCtx({ step: 2, system: { id: 'Shop_X', name: 'X' } })).has('system.id')).toBe(true)
    expect(computeStepErrors(makeCtx({ step: 2, system: { id: 'shop-x', name: 'X' } })).has('system.id')).toBe(false)
  })

  it('step 3 flags missing agent.name + targets + openclaw model', () => {
    const errs = computeStepErrors(makeCtx({
      step: 3, agent: { name: '' }, anyTargetSelected: false,
      targetModels: { openclaw: '' },
    }))
    expect(errs.has('agent.name')).toBe(true)
    expect(errs.has('targets.none')).toBe(true)
    expect(errs.has('model.openclaw')).toBe(true)
  })

  it('step 3 skips model check when openclaw not selected', () => {
    const errs = computeStepErrors(makeCtx({
      step: 3, enabledTargets: { 'claude-code': true }, targetModels: {},
    }))
    expect(errs.has('model.openclaw')).toBe(false)
  })

  it('step 4 flags env id + api_domain', () => {
    const errs = computeStepErrors(makeCtx({
      step: 4,
      environments: [{ id: '', api_domain: '' }, { id: 'prod', api_domain: '' }],
    }))
    expect(errs.has('env.0.id')).toBe(true)
    expect(errs.has('env.0.api_domain')).toBe(true)
    expect(errs.has('env.1.api_domain')).toBe(true)
    expect(errs.has('env.1.id')).toBe(false)
  })

  it('step 5 remote repo requires url(_cloneTarget 改可选,走全局默认 reposRoot)', () => {
    const errs = computeStepErrors(makeCtx({
      step: 5,
      repos: [{ name: 'x', url: '', _source: 'remote' }],
    }))
    expect(errs.has('repo.0.url')).toBe(true)
    // _cloneTarget 不再硬性必填:空时走上方"全局默认 clone 父目录",
    // useRepoScan 三层兜底已统一处理
    expect(errs.has('repo.0.cloneTarget')).toBe(false)
  })

  it('step 5 local repo requires _localPath', () => {
    const errs = computeStepErrors(makeCtx({
      step: 5,
      repos: [{ name: 'x', url: '', stack: 'go', _source: 'local' }],
    }))
    expect(errs.has('repo.0.localPath')).toBe(true)
    expect(errs.has('repo.0.url')).toBe(false)
  })

  it('step 5 requires a scanned or manually confirmed stack before advancing', () => {
    const errs = computeStepErrors(makeCtx({
      step: 5,
      repos: [{ name: 'x', url: 'git@example.com:x.git', stack: '', _source: 'remote' }],
    }))
    expect(errs.has('repo.0.stack')).toBe(true)
  })

  it('step 6 multi-source flags unassigned services', () => {
    const errs = computeStepErrors(makeCtx({
      step: 6, isMultiSource: true,
      activeSourceTypes: ['nacos', 'apollo'],
      getServiceSource: (svc) => svc === 'order-service' ? '' : 'nacos',
      CC_FIELDS_BY_TYPE: { nacos: [], apollo: [] },
    }))
    expect(errs.has('cc.unassigned.dev.order-service')).toBe(true)
  })

  it('step 6 happy path no errors', () => {
    expect(computeStepErrors(makeCtx({ step: 6 })).size).toBe(0)
  })

  it('step 6 requires an explicit none selection instead of silently treating empty as none', () => {
    const errs = computeStepErrors(makeCtx({ step: 6, activeSourceTypes: [] }))
    expect(errs.has('cc.none.explicit')).toBe(true)
  })

  it('step 6 supports multiple remote config hub instances', () => {
    const errs = computeStepErrors(makeCtx({
      step: 6,
      activeSourceTypes: ['nacos', 'apollo'],
      CC_FIELDS_BY_TYPE: { nacos: [], apollo: [] },
    }))
    expect(errs.has('cc.multi_remote.unsupported')).toBe(false)
  })

  it('step 6 rejects duplicate one2all instances that share one global endpoint contract', () => {
    const errs = computeStepErrors(makeCtx({
      step: 6,
      activeSourceTypes: ['one2all'],
      sourceInstances: [{ id: 'one2all', type: 'one2all' }, { id: 'one2all-2', type: 'one2all' }],
      CC_FIELDS_BY_TYPE: { one2all: [] },
    }))
    expect(errs.has('cc.instance.unsupported.one2all')).toBe(true)
  })

  it('step 6 allows a multi-service repo split across sources through the resource catalog', () => {
    const errs = computeStepErrors(makeCtx({
      step: 6,
      repos: [{ name: 'mono', url: 'git@example.com:mono.git', stack: 'go', service_names: 'user,order' }],
      allServiceNames: ['user', 'order'],
      activeSourceTypes: ['nacos', 'kuboard'],
      getServiceSource: svc => svc === 'user' ? 'nacos' : 'kuboard',
      CC_FIELDS_BY_TYPE: { nacos: [], kuboard: [] },
    }))
    expect(errs.has('cc.repo_mixed.0')).toBe(false)
  })

  it('step 6 flags missing namespace when ccHub ok but no namespace selected', () => {
    const errs = computeStepErrors(makeCtx({
      step: 6, envNamespaces: {}, // no namespace yet
    }))
    expect(errs.has('cc.nacos.dev.namespace')).toBe(true)
  })

  it('step 6 flags scan not run for nacos', () => {
    const errs = computeStepErrors(makeCtx({
      step: 6, ccHubStateByEnv: {}, // nothing fetched
    }))
    expect(errs.has('cc.nacos.dev.scan')).toBe(true)
  })

  it('step 7 blocks untested probe targets', () => {
    const errs = computeStepErrors(makeCtx({
      step: 7,
      enumerateDataStoreProbeTargets: () => [{ envID: 'dev', svc: 'order', dsKey: 'redis' }],
      // 不给 dsProbeResults → 该 target 处于"未测试"态
    }))
    expect(errs.has('ds.dev.order.redis.notested')).toBe(true)
    expect(errs.has('ds.dev.order.redis.probefail')).toBe(false)
  })

  it('step 7 flags probefail when status=fail', () => {
    const errs = computeStepErrors(makeCtx({
      step: 7,
      enumerateDataStoreProbeTargets: () => [{ envID: 'dev', svc: 'order', dsKey: 'redis' }],
      dsProbeResults: { 'dev::order::redis': { status: 'fail' } },
    }))
    expect(errs.has('ds.dev.order.redis.probefail')).toBe(true)
  })

  it('step 7 ok status passes', () => {
    expect(computeStepErrors(makeCtx({
      step: 7,
      enumerateDataStoreProbeTargets: () => [{ envID: 'dev', svc: 'order', dsKey: 'redis' }],
      dsProbeResults: { 'dev::order::redis': { status: 'ok' } },
    })).size).toBe(0)
  })

  // ── step 8:可观测性 ────────────────────────────────────────────────
  // Loki/Prometheus/Tempo 启用必须 Grafana 启用(本系统通过 mcp-grafana-npx 内置工具查,无独立 MCP 包)。
  it('step 8 flags loki/prom/tempo enabled but grafana off', () => {
    const errs = computeStepErrors(makeCtx({
      step: 8,
      enabledObservability: { loki: true, prometheus: true, tempo: true, grafana: false },
    }))
    expect(errs.has('obs.loki.needs_grafana')).toBe(true)
    expect(errs.has('obs.prometheus.needs_grafana')).toBe(true)
    expect(errs.has('obs.tempo.needs_grafana')).toBe(true)
  })

  it('step 8 grafana on => loki/prom/tempo OK', () => {
    expect(computeStepErrors(makeCtx({
      step: 8,
      enabledObservability: { loki: true, prometheus: true, tempo: true, grafana: true },
    })).size).toBe(0)
  })

  it('step 8 jaeger/elk standalone (no grafana) does not require grafana', () => {
    expect(computeStepErrors(makeCtx({
      step: 8,
      enabledObservability: { jaeger: true, elk: true, grafana: false },
    })).size).toBe(0)
  })

  it('step 8 requires direct tool fields and a successful connection probe', () => {
    const common = {
      step: 8,
      enabledObservability: { jaeger: true },
      OBS_TOOL_SPECS: [{
        key: 'jaeger',
        fields: [{ key: 'url', label: 'URL', secret: false, envVar: () => 'JAEGER_URL' }],
      }],
      toolInputs: {},
      toolKeyFor: (cat: 'obs' | 'ds', tool: string, env: string, field: string) => `${cat}:${tool}:${env}:${field}`,
      obsProbeKey: (tool: string, env: string) => `${tool}::${env}`,
      getObsAccessMode: () => 'direct' as const,
      requireObsProbe: true,
    }
    const missing = computeStepErrors(makeCtx(common))
    expect(missing.has('obs.jaeger.dev.url')).toBe(true)
    expect(missing.has('obs.jaeger.dev.probe')).toBe(true)

    expect(computeStepErrors(makeCtx({
      ...common,
      toolInputs: { 'obs:jaeger:dev:url': 'http://jaeger:16686' },
      obsProbeResults: { 'jaeger::dev': { status: 'ok' } },
    })).size).toBe(0)
  })

  it('step 8 accepts a K8s runtime connection reused from the Kuboard config source', () => {
    const errs = computeStepErrors(makeCtx({
      step: 8,
      enabledObservability: { k8s_runtime: true },
      OBS_TOOL_SPECS: [{
        key: 'k8s_runtime',
        fields: [
          { key: 'provider', label: 'Provider', secret: false, envVar: () => '', uiOnly: true },
          { key: 'url', label: 'URL', secret: false, envVar: () => 'KUBOARD_URL' },
          { key: 'access_key', label: 'Key', secret: true, envVar: () => 'KUBOARD_KEY' },
        ],
      }],
      toolInputs: {},
      toolKeyFor: (cat, tool, env, field) => `${cat}:${tool}:${env}:${field}`,
      getObsAccessMode: () => 'direct',
      sourceCreds: { kuboard: { creds: { dev: { url: 'http://kuboard', access_key: 'secret' } } } },
      kuboardStateByEnv: { dev: { status: 'ok', clusters: [] } },
      k8sRuntimeEnvLoc: { dev: { cluster: 'dev', namespace: 'default' } },
      requireObsProbe: true,
    }))
    expect(errs.size).toBe(0)
  })

  it('step 8 blocks mixed K8s runtime providers because YAML provider is global', () => {
    const errs = computeStepErrors(makeCtx({
      step: 8,
      environments: [{ id: 'dev', api_domain: 'dev' }, { id: 'prod', api_domain: 'prod' }],
      enabledObservability: { k8s_runtime: true },
      OBS_TOOL_SPECS: [{ key: 'k8s_runtime', fields: [] }],
      toolInputs: {
        'obs:k8s_runtime:dev:provider': 'kuboard',
        'obs:k8s_runtime:prod:provider': 'one2all',
      },
      toolKeyFor: (cat, tool, env, field) => `${cat}:${tool}:${env}:${field}`,
      k8sRuntimeEnvLoc: {
        dev: { cluster: 'dev', namespace: 'default' },
        prod: { cluster_id: 'prod', namespace: 'default' },
      },
    }))
    expect(errs.has('obs.k8s_runtime.provider_mismatch')).toBe(true)
  })
})

describe('labelForErrorKey', () => {
  const repos = [{ name: 'order', url: '' }]

  it('static labels round-trip', () => {
    expect(labelForErrorKey('system.id', repos)).toBe('系统 ID')
    expect(labelForErrorKey('targets.none', repos)).toBe('至少勾一个部署平台')
  })
  it('env.<i>.<field> formatted', () => {
    expect(labelForErrorKey('env.0.id', repos)).toContain('环境 #1')
    expect(labelForErrorKey('env.2.api_domain', repos)).toContain('环境 #3')
  })
  it('repo.<i>.cloneTarget mentions repo name', () => {
    expect(labelForErrorKey('repo.0.cloneTarget', repos)).toContain('order')
  })
  it('cc.unassigned.<svc> formatted', () => {
    expect(labelForErrorKey('cc.unassigned.order', repos)).toContain('order')
  })
  it('cc kuboard svc msg differs from nacos', () => {
    expect(labelForErrorKey('cc.kuboard.dev.svc.order', repos)).toContain('集群')
    expect(labelForErrorKey('cc.nacos.dev.svc.order', repos)).toContain('dataId')
  })
  it('unknown key falls through', () => {
    expect(labelForErrorKey('weird.key.xx', repos)).toBe('weird.key.xx')
  })
})

describe('key helpers', () => {
  it('ccKeyFor / svcKey / probeKey shapes are stable', () => {
    expect(ccKeyFor('nacos', 'dev', 'addr')).toBe('cc:nacos:dev:addr')
    expect(svcKey('dev', 'order')).toBe('dev::order')
    expect(probeKey('dev', 'order', 'redis')).toBe('dev::order::redis')
  })
})
