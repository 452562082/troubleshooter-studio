import { describe, it, expect } from 'vitest'
import yaml from 'js-yaml'
import { generateYAML, type ServiceTopologyState, type YAMLGenContext } from './yamlGenerator'
import { importServiceTopologyOverrides, parseEnvironment } from './yamlImporter'

// 最小可工作 ctx 工厂:测试用 stub。各测试按需 spread + 覆盖具体字段。
function makeCtx(overrides: Partial<YAMLGenContext> = {}): YAMLGenContext {
  return {
    system: { id: 'shop', name: 'Shop', description: '' },
    agent: { id: '', name: '', workspace_name: '', model: 'anthropic/claude-sonnet-4-6' },
    agentNameDefault: 'Shop 排障机器人',
    targetModels: { openclaw: 'anthropic/claude-sonnet-4-6' },
    enabledTargets: { openclaw: true, 'claude-code': false, cursor: false, codex: false },
    codeIntelligence: { enabled: false, provider: 'codegraph' },
    serviceTopology: { overrides: [] },
    enabledObservability: {},
    environments: [{ id: 'dev', api_domain: 'api-dev.shop', web_domain: '', is_prod: false }],
    repos: [{
      name: 'order-service', url: 'git@github.com:shop/order.git', stack: 'go', framework: '',
      service_names: 'order-service', env_branches: { dev: 'develop' },
    }],
    sourceCreds: { nacos: { creds: { dev: { addr: 'nacos:8848' } } } },
    serviceConfigSel: { 'dev::order-service': 'order.yaml' },
    serviceConfigGroup: {},
    envNamespaces: { dev: 'DEV_GROUP' },
    kuboardSvcMap: {},
    one2allSvcMap: {},
    lokiMappingByEnv: {},
    toolInputs: {},
    grafanaDsUidByObsEnv: {},
    k8sRuntimeEnvLoc: {},
    k8sRuntimeSvcMap: {},
    scannedDS: {},
    activeSourceTypes: ['nacos'],
    allServiceNames: ['order-service'],
    isMultiSource: false,
    targetOptions: ['openclaw', 'claude-code', 'cursor', 'codex'],
    modelConsumingTargets: ['openclaw'],
    OBS_TOOL_SPECS: [],
    CC_FIELDS_BY_TYPE: {
      nacos: [
        { key: 'addr', label: 'Nacos 地址', secret: false, envVar: (e) => `CC_ADDR_${e.toUpperCase()}` },
      ],
    },
    normalizeDomain: (s) => s,
    getServiceSource: () => 'nacos',
    isFieldHidden: () => false,
    isObsFieldHidden: () => false,
    getObsAccessMode: () => 'direct',
    obsGrafanaDsKey: (k, e) => `${k}::${e}`,
    svcKey: (envID, svc) => `${envID}::${svc}`,
    toolKeyFor: (cat, tool, envID, field) => `${cat}:${tool}:${envID}:${field}`,
    toolSpecByKey: () => undefined,
    deriveSkillsWhitelist: () => ['routing', 'config-executor'],
    recomputeEnabledDataStoresFromScanned: () => {},
    ...overrides,
  }
}

describe('generateYAML', () => {
  it('round-trips exact HTTP deployment verification values after import restoration', () => {
    const env = parseEnvironment({
      id: 'test', api_domain: '', web_domain: '', is_prod: false,
      deployment_verification: {
        provider: 'http',
        http: { url: 'https://admin-test.example.com/version', json_pointer: '/git/commit' },
      },
    })
    const parsed = yaml.load(generateYAML(makeCtx({ environments: [env] }))) as any
    expect(parsed.environments[0].deployment_verification).toEqual({
      provider: 'http',
      http: { url: 'https://admin-test.example.com/version', json_pointer: '/git/commit' },
    })
  })

  it('round-trips exact K8s deployment verification values after import restoration', () => {
    const env = parseEnvironment({
      id: 'test', api_domain: '', web_domain: '', is_prod: false,
      deployment_verification: {
        provider: 'k8s',
        k8s: {
          cluster: 'test-cluster', namespace: 'admin-test',
          deployments_by_repo: { 'admin-web': 'admin-web' },
          commit_annotation: 'app.example.com/git-commit',
        },
      },
    })
    const parsed = yaml.load(generateYAML(makeCtx({ environments: [env] }))) as any
    expect(parsed.environments[0].deployment_verification).toEqual({
      provider: 'k8s',
      k8s: {
        cluster: 'test-cluster', namespace: 'admin-test',
        deployments_by_repo: { 'admin-web': 'admin-web' },
        commit_annotation: 'app.example.com/git-commit',
      },
    })
  })

  it('keeps legacy and explicit manual environment YAML byte-semantically identical', () => {
    const legacy = makeCtx({ environments: [{ id: 'test', api_domain: '', web_domain: '', is_prod: false }] })
    const manual = makeCtx({ environments: [{
      id: 'test', api_domain: '', web_domain: '', is_prod: false,
      deployment_verification: { provider: 'manual', http: { url: '', json_pointer: '' }, k8s: { cluster: '', namespace: '', deployments_by_repo: {}, commit_annotation: '', image_label: '' } },
    } as any] })
    expect(generateYAML(manual)).toBe(generateYAML(legacy))
  })

  it('omits code_intelligence by default', () => {
    expect(generateYAML(makeCtx())).not.toContain('code_intelligence:')
  })

  it('emits enabled codegraph and its skill', () => {
    const ctx = makeCtx({ codeIntelligence: { enabled: true, provider: 'codegraph' } })
    ctx.deriveSkillsWhitelist = () => ['routing', 'incident-investigator', 'code-intelligence-query']
    expect(generateYAML(ctx)).toContain('code_intelligence:\n  enabled: true\n  provider: codegraph')
  })

  it('emits only service topology overrides and excludes scan candidates', () => {
    const serviceTopology: ServiceTopologyState = {
      overrides: [
        { action: 'confirm', fromService: 'web', toService: 'bff', protocol: 'http', method: 'GET', path: '/api/orders' },
        { action: 'reject', fromService: 'bff', toService: 'legacy', protocol: 'grpc', rpcMethod: 'legacy.Order/Get' },
        { action: 'add', fromService: 'bff', toService: 'order', protocol: 'http', method: 'POST', path: '/internal/orders' },
      ],
    }
    Object.assign(serviceTopology, {
      endpoints: [{ id: 'runtime-only-endpoint' }],
      edges: [{ status: 'candidate', confidence: 0.76 }],
    })

    const parsed = yaml.load(generateYAML(makeCtx({ serviceTopology }))) as Record<string, any>
    expect(parsed.service_topology).toEqual({
      overrides: [
        { action: 'confirm', from_service: 'web', to_service: 'bff', protocol: 'http', method: 'GET', path: '/api/orders' },
        { action: 'reject', from_service: 'bff', to_service: 'legacy', protocol: 'grpc', rpc_method: 'legacy.Order/Get' },
        { action: 'add', from_service: 'bff', to_service: 'order', protocol: 'http', method: 'POST', path: '/internal/orders' },
      ],
    })
    expect(JSON.stringify(parsed.service_topology)).not.toContain('runtime-only-endpoint')
    expect(JSON.stringify(parsed.service_topology)).not.toContain('candidate')
  })

  it('round-trips uppercase HTTP and gRPC override semantics through imported state', () => {
    const serviceTopology: ServiceTopologyState = {
      overrides: importServiceTopologyOverrides([
        {
          action: 'confirm', from_service: 'web', to_service: 'files',
          protocol: 'HTTP', method: 'get', path: '/files/:path*',
        },
        {
          action: 'reject', from_service: 'web', to_service: 'orders',
          protocol: 'GRPC', rpc_method: 'orders.v1.OrderService/GetOrder',
        },
      ]),
    }

    const parsed = yaml.load(generateYAML(makeCtx({ serviceTopology }))) as Record<string, any>
    expect(parsed.service_topology.overrides).toEqual([
      {
        action: 'confirm', from_service: 'web', to_service: 'files',
        protocol: 'http', method: 'GET', path: '/files/:path*',
      },
      {
        action: 'reject', from_service: 'web', to_service: 'orders',
        protocol: 'grpc', rpc_method: 'orders.v1.OrderService/GetOrder',
      },
    ])
  })

  it('emits parseable yaml for minimal context', () => {
    const out = generateYAML(makeCtx())
    const parsed = yaml.load(out) as any
    expect(parsed.system.id).toBe('shop')
    expect(parsed.agent.id).toBe('shop-troubleshooter') // 派生
    expect(parsed.environments[0].id).toBe('dev')
    expect(parsed.repos[0].name).toBe('order-service')
    expect(parsed.infrastructure.config_center.type).toBe('nacos')
    expect(parsed.generation.targets).toEqual(['openclaw'])
    // preserve_on_regenerate 已删除;SOUL/USER/CHECKLIST 是模板派生、必须跟模板走
    expect(parsed.generation.preserve_on_regenerate).toBeUndefined()
  })

  it('emits config_centers (plural) for multi-source', () => {
    const out = generateYAML(makeCtx({
      activeSourceTypes: ['nacos', 'apollo'],
      isMultiSource: true,
      sourceCreds: {
        nacos: { creds: { dev: { addr: 'nacos:8848' } } },
        apollo: { creds: { dev: { meta: 'http://apollo' } } },
      },
      CC_FIELDS_BY_TYPE: {
        nacos: [{ key: 'addr', label: 'Nacos', secret: false, envVar: (e) => `CC_ADDR_${e.toUpperCase()}` }],
        apollo: [{ key: 'meta', label: 'Apollo', secret: false, envVar: (e) => `APOLLO_META_${e.toUpperCase()}` }],
      },
    }))
    const parsed = yaml.load(out) as any
    expect(parsed.infrastructure.config_centers).toHaveLength(2)
    expect(parsed.infrastructure.config_center).toBeUndefined()
    const ids = parsed.infrastructure.config_centers.map((c: any) => c.id)
    expect(ids).toEqual(['nacos', 'apollo'])
  })

  it('never emits preserve_on_regenerate (field deleted; template-derived files always follow template)', () => {
    // preserve_on_regenerate 已彻底删除。SOUL/USER/CHECKLIST 是模板渲染产物,
    // 整文件 preserve 反而让模板更新被静默吞掉,改成始终按模板覆盖。
    const out = generateYAML(makeCtx({
      enabledTargets: { openclaw: false, 'claude-code': true, cursor: false, codex: false },
    }))
    const parsed = yaml.load(out) as any
    expect(parsed.generation.targets).toEqual(['claude-code'])
    expect(parsed.generation.preserve_on_regenerate).toBeUndefined()
  })

  it('writes config_center: none when no source active', () => {
    const out = generateYAML(makeCtx({ activeSourceTypes: [] }))
    const parsed = yaml.load(out) as any
    expect(parsed.infrastructure.config_center.type).toBe('none')
  })

  it('emits target_models only when openclaw value differs from agent.model', () => {
    const ctxSame = makeCtx({
      agent: { id: '', name: '', workspace_name: '', model: 'anthropic/claude-opus-4' },
      targetModels: { openclaw: 'anthropic/claude-opus-4' },
    })
    const sameYaml = yaml.load(generateYAML(ctxSame)) as any
    expect(sameYaml.agent.target_models).toBeUndefined()

    const ctxDiff = makeCtx({
      agent: { id: '', name: '', workspace_name: '', model: 'anthropic/claude-opus-4' },
      targetModels: { openclaw: 'anthropic/claude-sonnet-4' },
    })
    const diffYaml = yaml.load(generateYAML(ctxDiff)) as any
    expect(diffYaml.agent.target_models.openclaw).toBe('anthropic/claude-sonnet-4')
  })

  it('emits one2all k8s_runtime provider from tool inputs', () => {
    const out = generateYAML(makeCtx({
      enabledObservability: { k8s_runtime: true },
      deriveSkillsWhitelist: () => ['routing', 'k8s-runtime-query'],
      OBS_TOOL_SPECS: [{
        key: 'k8s_runtime',
        fields: [
          { key: 'provider', label: 'Provider', secret: false, envVar: () => '', uiOnly: true },
          { key: 'url', label: 'MCP URL', secret: false, envVar: () => 'ONE2ALL_MCP_URL' },
          { key: 'api_key', label: 'Token', secret: true, envVar: () => 'ONE2ALL_TOKEN' },
        ],
      }],
      toolInputs: {
        'obs:k8s_runtime:dev:provider': 'one2all',
        'obs:k8s_runtime:dev:url': 'http://one2all/mcp/hash',
        'obs:k8s_runtime:dev:api_key': 'o2a_secret',
      },
      k8sRuntimeEnvLoc: {
        dev: { cluster_id: '1', namespace: 'default' },
      },
      k8sRuntimeSvcMap: {
        'dev::order-service': { workload: 'order-service' },
      },
    }))
    const parsed = yaml.load(out) as any
    const rt = parsed.infrastructure.observability.k8s_runtime
    expect(rt.provider).toBe('one2all')
    expect(rt.endpoints[0].url).toBe('http://one2all/mcp/hash')
    expect(rt.endpoints[0].api_key).toBe('o2a_secret')
    expect(rt.service_map[0].cluster_id).toBe('1')
    expect(rt.service_map[0].cluster).toBeUndefined()
  })

  it('emits Doris data store endpoints from scannedDS', () => {
    const out = generateYAML(makeCtx({
      scannedDS: {
        dev: {
          'order-service': {
            doris: { dsn: 'user:pass@tcp(doris-fe:9030)/warehouse' },
          },
        },
      },
      toolSpecByKey: (_cat, key) => key === 'doris'
        ? {
            key: 'doris',
            fields: [
              { key: 'dsn', label: 'DSN', secret: true, envVar: (e) => `DORIS_DSN_${e.toUpperCase()}` },
            ],
          }
        : undefined,
      deriveSkillsWhitelist: () => ['routing', 'config-executor', 'doris-runtime-query'],
    }))
    const parsed = yaml.load(out) as any
    const doris = parsed.infrastructure.data_stores.find((ds: any) => ds.type === 'doris')
    expect(doris.enabled).toBe(true)
    expect(doris.endpoints[0].service).toBe('order-service')
    expect(doris.endpoints[0].dsn).toBe('user:pass@tcp(doris-fe:9030)/warehouse')
    expect(parsed.generation.skills_whitelist).toContain('doris-runtime-query')
  })
})
