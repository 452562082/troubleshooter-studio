import { describe, it, expect } from 'vitest'
import yaml from 'js-yaml'
import { generateYAML, type YAMLGenContext } from './yamlGenerator'

// 最小可工作 ctx 工厂:测试用 stub。各测试按需 spread + 覆盖具体字段。
function makeCtx(overrides: Partial<YAMLGenContext> = {}): YAMLGenContext {
  return {
    system: { id: 'shop', name: 'Shop', description: '' },
    agent: { id: '', name: '', workspace_name: '', model: 'anthropic/claude-sonnet-4-6' },
    agentNameDefault: 'Shop 排障机器人',
    targetModels: { openclaw: 'anthropic/claude-sonnet-4-6' },
    enabledTargets: { openclaw: true, 'claude-code': false, cursor: false, codex: false },
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
})
