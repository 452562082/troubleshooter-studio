// yamlImporter.ts —— InitPage::applyImport 反填 wizard reactive state 的纯函数 + helper。
//
// 包含的 helper:
//   - isPlaceholder / isLiveString:yaml 字段值占位符判定
//   - inferAuthMode:根据 yaml endpoint 实际填了哪些字段反推 auth_mode UI 选项
//   - parseEnvironment / parseRepoCore:yaml 段 → wizard reactive 形状
//   - placeholderName:多源场景的环境变量占位符命名
//   - applyParsedYAMLToWizardState:整段同步反填(parsed yaml → 15+ reactive 集合)
//
// applyParsedYAMLToWizardState 不做 async cross-check tail —— 那部分留 InitPage,
// 它依赖 runCCHubPreload / crossCheckImported* 等 closure 函数,跨边界不值得。

import type { CredField } from './credFields'
import { VIA_GRAFANA_ELIGIBLE } from './yamlShared'

/** yaml 字段值是否为模板占位符 "{{XYZ}}";占位符不应当作真值反填。 */
export function isPlaceholder(v: unknown): boolean {
  return typeof v === 'string' && v.startsWith('{{') && v.endsWith('}}')
}

/** 实际值非空且非占位符。 */
export function isLiveString(v: unknown): v is string {
  return typeof v === 'string' && v !== '' && !isPlaceholder(v)
}

/**
 * auth_mode 是 UI-only 字段(不进 yaml),反填时根据 endpoint 实际填了哪类凭证字段反推:
 *   - 有 access_key            → 'access_key'
 *   - 有 api_key               → 'api_key'
 *   - 有 username+password 或 user+pass → 'username_password'
 * 没匹配时返回空串 → 调用方保持下拉默认。
 *
 * authModeField:CredField 形状,需要 options 列表(用来检查目标 mode 是不是 spec 支持的选项)。
 * ep:yaml 里这条 endpoint 的 raw record,按字段名读真值 / 占位符。
 */
export function inferAuthMode(
  authModeField: CredField | undefined,
  ep: Record<string, unknown>,
): string {
  if (!authModeField || !authModeField.options || authModeField.options.length === 0) return ''
  const has = (k: string) => isLiveString(ep[k])
  const opts = authModeField.options
  if (opts.some(o => o.value === 'access_key') && has('access_key')) return 'access_key'
  if (opts.some(o => o.value === 'api_key') && has('api_key')) return 'api_key'
  if (opts.some(o => o.value === 'username_password')
      && (has('username') && has('password') || has('user') && has('pass'))) {
    return 'username_password'
  }
  return ''
}

/** wizard env 行的反填形状(applyImport 里 environments.splice 用的同款 shape)。 */
export interface ParsedEnv {
  id: string
  api_domain: string
  web_domain: string
  is_prod: boolean
}

/** parsed.environments[i] → ParsedEnv,字段全 fallback 空串/false。 */
export function parseEnvironment(e: unknown): ParsedEnv {
  const o = (e ?? {}) as Record<string, unknown>
  return {
    id: typeof o.id === 'string' ? o.id : '',
    api_domain: typeof o.api_domain === 'string' ? o.api_domain : '',
    web_domain: typeof o.web_domain === 'string' ? o.web_domain : '',
    is_prod: Boolean(o.is_prod),
  }
}

/** parseRepoCore 提取 yaml repo 字段;_source / _localPath / env_branches 由调用方拼。 */
export interface ParsedRepoCore {
  name: string
  url: string
  stack: string
  framework: string
  role: string
  sub_path: string
  /** service_names 在 yaml 里可能是数组或字符串,统一成 csv 串(InitPage RepoItem 形状) */
  service_names: string
  config_source: string
  /** 引用 repos[].name,标明本仓是某 umbrella 切出去的子模块 */
  parent_repo: string
  /** 在 umbrella clone 内的挂载相对路径(默认=name) */
  parent_path: string
  service_entries?: Record<string, string>
}

export function parseRepoCore(r: unknown): ParsedRepoCore {
  const o = (r ?? {}) as Record<string, unknown>
  const svcNames = Array.isArray(o.service_names)
    ? (o.service_names as unknown[]).filter(s => typeof s === 'string').join(', ')
    : (typeof o.service_names === 'string' ? o.service_names : '')
  const role = (typeof o.role === 'string' && o.role.trim()) ? (o.role as string).trim() : 'backend'
  let serviceEntries: Record<string, string> | undefined
  if (typeof o.service_entries === 'object' && o.service_entries) {
    const out: Record<string, string> = {}
    for (const [k, v] of Object.entries(o.service_entries as Record<string, unknown>)) {
      if (typeof v === 'string') out[k] = v
    }
    serviceEntries = out
  }
  return {
    name: typeof o.name === 'string' ? o.name : '',
    url: typeof o.url === 'string' ? o.url : '',
    stack: typeof o.stack === 'string' ? o.stack : 'go',
    framework: typeof o.framework === 'string' ? o.framework : '',
    role,
    sub_path: typeof o.sub_path === 'string' ? (o.sub_path as string).trim() : '',
    service_names: svcNames,
    config_source: typeof o.config_source === 'string' ? o.config_source : '',
    parent_repo: typeof o.parent_repo === 'string' ? (o.parent_repo as string).trim() : '',
    parent_path: typeof o.parent_path === 'string' ? (o.parent_path as string).trim() : '',
    service_entries: serviceEntries,
  }
}

// placeholderName 收口在 yamlShared.ts(yamlGenerator 也用同一份);re-export 给老调用方。
export { placeholderName } from './yamlShared'

// ── applyParsedYAMLToWizardState 同步反填 ──

/** 反填用 ctx,InitPage 把 reactive 引用 + helper 函数 + bridge 桥接 一次性传入。
 *  reactive 对象通过 Vue 3 proxy 跨边界仍然工作,lib 内 obj[k]=v 等价于 InitPage 写 reactive。 */
export interface ApplyImportContext {
  // 一组 reactive 引用(直传,Vue 3 proxy 跨组件边界 mutate 就反应)
  system: { id: string; name: string; description: string }
  agent: { id: string; name: string; workspace_name: string; model: string }
  targetModels: Record<string, string>
  environments: Array<{ id: string; api_domain: string; web_domain: string; is_prod: boolean }>
  repos: any[]
  enabledSourceTypes: Record<string, boolean>
  enabledSourceOrder: string[]
  sourceCreds: Record<string, { creds: Record<string, Record<string, string>>; rawExtra?: Record<string, unknown> }>
  serviceSourceMap: Record<string, string>
  ccCredInputs: Record<string, string>
  envNamespaces: Record<string, string>
  serviceConfigSel: Record<string, string>
  serviceConfigGroup: Record<string, string>
  ccHubStateByEnv: Record<string, any>
  enabledObservability: Record<string, boolean>
  toolInputs: Record<string, string>
  obsAccessModeMap: Record<string, 'via_grafana' | 'direct'>
  grafanaDsUidByObsEnv: Record<string, string>
  k8sRuntimeEnvLoc: Record<string, { cluster?: string; namespace?: string }>
  k8sRuntimeSvcMap: Record<string, { workload?: string; label_selector?: string }>
  scannedDS: Record<string, Record<string, Record<string, Record<string, string>>>>
  enabledDataStores: Record<string, boolean>
  dsAutoFilled: Record<string, boolean>
  dsScanState: Record<string, { status: string; reason?: string }>
  // 静态 / computed 数据
  ALL_SOURCE_TYPES: readonly string[]
  // VIA_GRAFANA_ELIGIBLE 不再走 ctx,直接 import 自 yamlShared(单一源)
  CC_FIELDS_BY_TYPE: Record<string, CredField[]>
  allServiceNames: string[]
  // helper 函数(InitPage closure)
  ensureKuboardLoc: (envID: string, svc: string) => { cluster?: string; namespace?: string; configmap?: string }
  getLokiMapping: (envID: string) => any
  ccKeyFor: (type: string, envID: string, field: string) => string
  svcKey: (envID: string, svc: string) => string
  scanStateKey: (envID: string, svc: string) => string
  toolKeyFor: (cat: 'obs' | 'ds', tool: string, envID: string, field: string) => string
  obsAccessKey: (obsKey: string, envID: string) => string
  obsGrafanaDsKey: (obsKey: string, envID: string) => string
  toolSpecByKey: (cat: 'obs' | 'ds', key: string) => { fields: CredField[] } | undefined
  // 用 any 接 env —— InitPage 的 EnvItem 含更多字段(api_domain / web_domain),
  // 收紧类型会让 InitPage 那边 ctx.pickBranchForEnv = pickBranchForEnv 报参数不兼容。
  pickBranchForEnv: (env: any, branches: string[]) => string
  // 异步 bridge(InitPage 直接传 import 进来的 bridge 函数)
  getRepoPathsForSystem: (systemID: string) => Promise<Record<string, string>>
  listBranchesForRepo: (path: string) => Promise<string[]>
  // 后台 fetch 完成后写入,InitPage 那边是 ref<Record<string, string[]>>
  setRepoBranches: (name: string, branches: string[]) => void
}

/**
 * 把 parsed yaml 反填到 wizard 的 reactive state(同步部分,async cross-check tail 留 InitPage)。
 * 返回主源 type(InitPage 用作 setTimeout cross-check 的 envID 输入)。
 */
export async function applyParsedYAMLToWizardState(
  parsed: any,
  ctx: ApplyImportContext,
): Promise<{ primaryConfigCenter: string }> {
  // system
  if (parsed.system && typeof parsed.system === 'object') {
    ctx.system.id = parsed.system.id ?? ''
    ctx.system.name = parsed.system.name ?? ''
    ctx.system.description = parsed.system.description ?? ''
  }

  // agent
  if (parsed.agent && typeof parsed.agent === 'object') {
    ctx.agent.id = parsed.agent.id ?? ''
    ctx.agent.name = parsed.agent.name ?? ''
    ctx.agent.workspace_name = parsed.agent.workspace_name ?? ''
    ctx.agent.model = parsed.agent.model ?? ctx.agent.model
    const tm = parsed.agent.target_models || {}
    ctx.targetModels.openclaw = tm.openclaw || ctx.agent.model
  }

  // environments
  if (Array.isArray(parsed.environments) && parsed.environments.length) {
    ctx.environments.splice(0, ctx.environments.length, ...parsed.environments.map(parseEnvironment))
  }

  // repos:同步反填 + 后台 fire-and-forget 拉真实分支
  if (Array.isArray(parsed.repos) && parsed.repos.length) {
    let savedRepoPaths: Record<string, string> = {}
    if (ctx.system.id) {
      try { savedRepoPaths = await ctx.getRepoPathsForSystem(ctx.system.id) } catch { /* 失败不阻塞 */ }
    }
    const localPathsToFetch: Array<{ name: string; path: string }> = []
    ctx.repos.splice(0, ctx.repos.length, ...parsed.repos.map((r: any) => {
      const core = parseRepoCore(r)
      const branches: Record<string, string> = {}
      for (const env of ctx.environments) {
        if (env.id) branches[env.id] = r?.env_branches?.[env.id] ?? ''
      }
      let localPath = core.name ? (savedRepoPaths[core.name] || '') : ''
      // umbrella 子模块(parent_repo 在场):代码必须由 umbrella 的 git submodule 提供,
      // 不能独立 clone。即便 savedRepoPaths 没记本子模块路径(新机器导入常态),也强制 local
      // 模式 —— 不强制的话默认 _source='remote',跟 UI 锁死 local 矛盾,用户看到混合状态。
      // _localPath 优先 savedPath;没有就用 umbrella 的 saved path + parent_path 拼算
      // (umbrella 也可能还没扫,这里返空即可,后续 umbrella 扫完会有 cascade 回填这里)。
      if (core.parent_repo && !localPath) {
        const umbrellaPath = savedRepoPaths[core.parent_repo] || ''
        if (umbrellaPath) {
          const sub = (core.parent_path || core.name).replace(/^\/+/, '')
          localPath = umbrellaPath.replace(/\/+$/, '') + '/' + sub
        }
      }
      const isUmbrellaChild = !!core.parent_repo
      if (core.name && localPath) localPathsToFetch.push({ name: core.name, path: localPath })
      return {
        ...core,
        env_branches: branches,
        _source: (isUmbrellaChild || localPath) ? 'local' : 'remote',
        _localPath: localPath,
        _serviceEntries: core.service_entries,
        _submoduleHintsDismissed: !!(core.service_entries && Object.keys(core.service_entries).length > 0),
      }
    }))
    // umbrella 行的 service_names / _serviceEntries 不在 yaml import 阶段强制清:
    // 1. 大多数 umbrella 是纯容器 — splitMonorepo 已在创建时把它的 service_names
    //    清掉,导出的 yaml 自然就是空的,re-import 仍是空,符合预期
    // 2. 少数 umbrella 自身有运行的 service(典型:truss 既是 gateway 服务又是
    //    commerce/api/... 的 umbrella),用户在 yaml 里显式写 service_names: [truss]
    //    → 必须尊重,不能 import 时就被清掉
    //
    // **但**:role 跟 service_names 必须一致(syncServiceNamesWithRole 的 yaml-side
    // 镜像)。非业务服务角色(common-lib / docs / infra / frontend / mobile)即便
    // yaml 里有 service_names 也清掉 —— allServiceNames 就不会出 "frontend 仓的
    // 名字当成服务" 这种噪音。常见场景:用户改 role=docs 后导出 yaml,但 service_names
    // 残留(比如老版本 wizard 没自动清,或手编辑 yaml 漏)。
    const NON_SERVICE_ROLES = new Set(['common-lib', 'docs', 'infra', 'frontend', 'mobile'])
    for (const r of ctx.repos as any[]) {
      if (NON_SERVICE_ROLES.has((r.role || '').trim())) {
        r.service_names = ''
      }
    }
    // 后台拉真实分支:不阻塞 applyImport 同步返回
    for (const { name, path } of localPathsToFetch) {
      ctx.listBranchesForRepo(path).then((bs) => {
        if (bs && bs.length > 0) {
          ctx.setRepoBranches(name, bs)
          const r = ctx.repos.find((x: any) => x.name === name)
          if (r) {
            for (const env of ctx.environments) {
              if (!env.id) continue
              const cur = (r.env_branches[env.id] || '').trim()
              if (cur && bs.includes(cur)) continue
              const mapped = ctx.pickBranchForEnv(env, bs)
              if (mapped) r.env_branches[env.id] = mapped
            }
          }
        }
      }).catch(() => { /* 拉不到就保持文本输入 */ })
    }
  }

  // ── 配置源 ingest:多源 schema(config_centers 数组)>  单源(config_center)──
  for (const t of ctx.ALL_SOURCE_TYPES) ctx.enabledSourceTypes[t] = false
  ctx.enabledSourceTypes['none'] = false
  for (const t of ctx.ALL_SOURCE_TYPES) ctx.sourceCreds[t] = { creds: {} }
  ctx.enabledSourceOrder.splice(0, ctx.enabledSourceOrder.length)

  const ingestSource = (s: any, sourceID: string) => {
    if (!s || typeof s.type !== 'string') return
    const t = s.type
    ctx.enabledSourceTypes[t] = true
    if (!ctx.enabledSourceOrder.includes(t)) ctx.enabledSourceOrder.push(t)
    if (!ctx.sourceCreds[t]) ctx.sourceCreds[t] = { creds: {} }
    const fields = ctx.CC_FIELDS_BY_TYPE[t] || []
    if (Array.isArray(s.endpoints)) {
      for (const ep of s.endpoints) {
        if (!ep || typeof ep.env !== 'string') continue
        const envCreds: Record<string, string> = ctx.sourceCreds[t].creds[ep.env] || {}
        for (const f of fields) {
          const v = ep[f.key]
          if (isLiveString(v)) envCreds[f.key] = v
        }
        const mode = inferAuthMode(fields.find(f => f.key === 'auth_mode'), ep)
        if (mode) envCreds['auth_mode'] = mode
        if (Object.keys(envCreds).length > 0) ctx.sourceCreds[t].creds[ep.env] = envCreds
      }
    }
    if (t === 'kuboard' && s.service_map && typeof s.service_map === 'object') {
      for (const [envID, svcs] of Object.entries(s.service_map as Record<string, unknown>)) {
        if (!svcs || typeof svcs !== 'object') continue
        for (const [svc, rec] of Object.entries(svcs as Record<string, unknown>)) {
          if (!rec || typeof rec !== 'object') continue
          const r = rec as { cluster?: string; namespace?: string; configmap?: string }
          const loc = ctx.ensureKuboardLoc(envID, svc)
          if (typeof r.cluster === 'string') loc.cluster = r.cluster
          if (typeof r.namespace === 'string') loc.namespace = r.namespace
          if (typeof r.configmap === 'string') loc.configmap = r.configmap
        }
      }
    }
    // 其它高级字段进 rawExtra round-trip(service_map 一律排除,emit 时 emitSourceBody 自己生成)
    const rawExtra: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(s)) {
      if (k === 'id' || k === 'type' || k === 'endpoints') continue
      if (k === 'per_env_credentials') continue // 已废弃
      if (k === 'service_map') continue
      rawExtra[k] = v
    }
    if (Object.keys(rawExtra).length > 0) ctx.sourceCreds[t].rawExtra = rawExtra
    void sourceID
  }

  let primarySource: any = null
  const ccArray = parsed.infrastructure?.config_centers
  if (Array.isArray(ccArray) && ccArray.length > 0) {
    for (const s of ccArray) ingestSource(s, typeof s?.id === 'string' ? s.id : '')
    const defaultEntry = ccArray.find((s: any) => s?.id === 'default')
    primarySource = defaultEntry || ccArray[0]
  } else if (parsed.infrastructure?.config_center) {
    ingestSource(parsed.infrastructure.config_center, 'default')
    primarySource = parsed.infrastructure.config_center
  }

  const cc = primarySource?.type
  if (cc && Array.isArray(parsed.repos)) {
    for (const r of parsed.repos) {
      const explicit = (typeof r?.config_source === 'string' && r.config_source.trim()) ? r.config_source.trim() : ''
      const target = explicit || cc
      const svcNames: string[] = Array.isArray(r?.service_names) && r.service_names.length > 0
        ? r.service_names.filter((s: any) => typeof s === 'string')
        : (typeof r?.name === 'string' ? [r.name] : [])
      for (const svc of svcNames) {
        if (svc) ctx.serviceSourceMap[svc] = target
      }
    }
  }

  // 主源 endpoints[] 字段值 → ccCredInputs(让老 preload / 命名空间下拉等代码继续用 ccKeyFor 拼 key)
  const endpoints = primarySource?.endpoints
  if (Array.isArray(endpoints) && typeof cc === 'string') {
    const fields = ctx.CC_FIELDS_BY_TYPE[cc] || []
    for (const ep of endpoints) {
      const envID = ep?.env
      if (!envID || typeof envID !== 'string') continue
      for (const f of fields) {
        const v = ep?.[f.key]
        if (!isLiveString(v)) continue
        ctx.ccCredInputs[ctx.ccKeyFor(cc, envID, f.key)] = v
      }
      const mode = inferAuthMode(fields.find(f => f.key === 'auth_mode'), ep)
      if (mode) ctx.ccCredInputs[ctx.ccKeyFor(cc, envID, 'auth_mode')] = mode
    }
  }

  // service_map(nacos/apollo/consul):envNamespaces + serviceConfigSel + serviceConfigGroup + 合成 ccHubStateByEnv
  const svcMap = (cc !== 'kuboard') ? primarySource?.service_map : null
  if (svcMap && typeof svcMap === 'object') {
    const synthByEnv: Record<string, { ns: Map<string, string>; entries: Map<string, { locator: string; group?: string; tenant?: string }> }> = {}
    for (const [envID, svcs] of Object.entries(svcMap)) {
      if (!svcs || typeof svcs !== 'object') continue
      for (const [svc, rec] of Object.entries(svcs as Record<string, unknown>)) {
        if (!rec || typeof rec !== 'object') continue
        const r = rec as { namespace?: string; group?: string; data_id?: string }
        if (typeof r.namespace === 'string' && r.namespace) ctx.envNamespaces[envID] = r.namespace
        if (typeof r.data_id === 'string' && r.data_id) ctx.serviceConfigSel[ctx.svcKey(envID, svc)] = r.data_id
        if (typeof r.group === 'string' && r.group) ctx.serviceConfigGroup[ctx.svcKey(envID, svc)] = r.group
        if (!synthByEnv[envID]) synthByEnv[envID] = { ns: new Map(), entries: new Map() }
        const bucket = synthByEnv[envID]
        if (typeof r.namespace === 'string' && r.namespace) bucket.ns.set(r.namespace, r.namespace)
        if (typeof r.data_id === 'string' && r.data_id) {
          const grp = (typeof r.group === 'string' && r.group) ? r.group : ''
          const key = r.data_id + '@' + grp
          bucket.entries.set(key, {
            locator: r.data_id,
            group: grp || undefined,
            tenant: (typeof r.namespace === 'string' && r.namespace) ? r.namespace : undefined,
          })
        }
      }
    }
    for (const [envID, bucket] of Object.entries(synthByEnv)) {
      if (bucket.ns.size === 0 && bucket.entries.size === 0) continue
      ctx.ccHubStateByEnv[envID] = {
        status: 'ok',
        namespaces: Array.from(bucket.ns.entries()).map(([id, show]) => ({ id, show_name: show })),
        entries: Array.from(bucket.entries.values()),
        notes: ['(基于导入的 troubleshooter.yaml service_map 合成,后续会跑一次真实 preload 校验)'],
        loadedAt: Date.now(),
        synthesized: true,
      }
    }
  }

  // observability:勾选态 + endpoints + datasource_uid + via_grafana 模式 + loki labels + k8s_runtime
  const obs = parsed.infrastructure?.observability
  if (obs && typeof obs === 'object') {
    for (const key of Object.keys(ctx.enabledObservability)) {
      ctx.enabledObservability[key] = Boolean(obs?.[key]?.enabled)
      const spec = ctx.toolSpecByKey('obs', key)
      const obsEndpoints = obs?.[key]?.endpoints
      if (spec && Array.isArray(obsEndpoints)) {
        for (const ep of obsEndpoints) {
          const envID = ep?.env
          if (!envID || typeof envID !== 'string') continue
          for (const f of spec.fields) {
            const v = ep?.[f.key]
            if (!isLiveString(v)) continue
            ctx.toolInputs[ctx.toolKeyFor('obs', key, envID, f.key)] = v
          }
          const mode = inferAuthMode(spec.fields.find(f => f.key === 'auth_mode'), ep)
          if (mode) ctx.toolInputs[ctx.toolKeyFor('obs', key, envID, 'auth_mode')] = mode
        }
      }
      const uidMap = obs?.[key]?.datasource_uid_by_env
      if (uidMap && typeof uidMap === 'object' && ['prometheus', 'jaeger', 'tempo', 'elk'].includes(key)) {
        for (const [envID, uid] of Object.entries(uidMap)) {
          if (typeof uid === 'string' && uid) {
            ctx.grafanaDsUidByObsEnv[ctx.obsGrafanaDsKey(key, envID)] = uid
          }
        }
      }
      if ((VIA_GRAFANA_ELIGIBLE as readonly string[]).includes(key)) {
        const viaGrafana = Boolean(obs?.[key]?.via_grafana)
        for (const env of ctx.environments) {
          if (!env.id) continue
          ctx.obsAccessModeMap[ctx.obsAccessKey(key, env.id)] = viaGrafana ? 'via_grafana' : 'direct'
        }
      }
    }

    // loki.label_mapping_by_env 反填到 lokiMappingByEnv[env]
    const lokiBlock = obs?.['loki']
    const lokiLM = lokiBlock?.label_mapping_by_env
    if (lokiLM && typeof lokiLM === 'object') {
      for (const [envID, m] of Object.entries(lokiLM as Record<string, any>)) {
        if (!envID || !m || typeof m !== 'object') continue
        const lm = ctx.getLokiMapping(envID)
        if (typeof m.env_label === 'string' && m.env_label) lm.envLabelKey = m.env_label
        if (typeof m.service_label === 'string' && m.service_label) lm.serviceLabelKey = m.service_label
        if (typeof m.grafana_ds_uid === 'string' && m.grafana_ds_uid) lm.dsUID = m.grafana_ds_uid
        if (lm.envLabelKey && typeof m[lm.envLabelKey] === 'string') lm.envValue = m[lm.envLabelKey]
        const sm = m.service_map
        if (sm && typeof sm === 'object') {
          for (const [svc, val] of Object.entries(sm as Record<string, any>)) {
            if (!svc) continue
            let v = ''
            if (typeof val === 'string') v = val
            else if (val && typeof val === 'object' && lm.serviceLabelKey
                     && typeof val[lm.serviceLabelKey] === 'string') {
              v = val[lm.serviceLabelKey]
            }
            if (v) lm.serviceValues[svc] = v
          }
        }
      }
    }

    // k8s_runtime.service_map 反填到 envLoc + svcMap
    const k8sSvcMap = obs?.['k8s_runtime']?.service_map
    if (Array.isArray(k8sSvcMap)) {
      for (const k of Object.keys(ctx.k8sRuntimeSvcMap)) delete ctx.k8sRuntimeSvcMap[k]
      for (const k of Object.keys(ctx.k8sRuntimeEnvLoc)) delete ctx.k8sRuntimeEnvLoc[k]
      for (const entry of k8sSvcMap) {
        const envID = entry?.env, svc = entry?.service
        if (typeof envID !== 'string' || typeof svc !== 'string' || !envID || !svc) continue
        if (!ctx.k8sRuntimeEnvLoc[envID]) {
          ctx.k8sRuntimeEnvLoc[envID] = {
            cluster: typeof entry?.cluster === 'string' ? entry.cluster : '',
            namespace: typeof entry?.namespace === 'string' ? entry.namespace : '',
          }
        }
        ctx.k8sRuntimeSvcMap[ctx.svcKey(envID, svc)] = {
          workload: typeof entry?.workload === 'string' ? entry.workload : '',
          label_selector: typeof entry?.label_selector === 'string' ? entry.label_selector : '',
        }
      }
    }
  }

  // data_stores 反填 scannedDS / enabledDataStores / dsAutoFilled / dsScanState
  const ds = parsed.infrastructure?.data_stores
  if (Array.isArray(ds)) {
    for (const key of Object.keys(ctx.scannedDS)) delete ctx.scannedDS[key]
    for (const entry of ds) {
      const t = entry?.type
      if (typeof t !== 'string' || entry?.enabled === false) continue
      const spec = ctx.toolSpecByKey('ds', t)
      const dsEndpoints = entry?.endpoints
      if (!spec || !Array.isArray(dsEndpoints)) continue
      ctx.enabledDataStores[t] = true
      for (const ep of dsEndpoints) {
        const envID = ep?.env
        if (!envID || typeof envID !== 'string') continue
        const svc = typeof ep?.service === 'string' && ep.service ? ep.service : 'legacy'
        if (!ctx.scannedDS[envID]) ctx.scannedDS[envID] = {}
        if (!ctx.scannedDS[envID][svc]) ctx.scannedDS[envID][svc] = {}
        const fields: Record<string, string> = {}
        for (const f of spec.fields) {
          const v = ep?.[f.key]
          if (!isLiveString(v)) continue
          fields[f.key] = v
        }
        if (Object.keys(fields).length > 0) {
          ctx.scannedDS[envID][svc][t] = fields
          ctx.dsAutoFilled[t] = true
        }
      }
    }
    // dsScanState:Step 7 校验门用 scanStateOf;有数据层 → ok / 没识别到 → empty
    for (const env of ctx.environments) {
      if (!env.id) continue
      for (const svc of ctx.allServiceNames) {
        const k = ctx.scanStateKey(env.id, svc)
        const hasItems = ctx.scannedDS[env.id]?.[svc] && Object.keys(ctx.scannedDS[env.id][svc]).length > 0
        ctx.dsScanState[k] = hasItems
          ? { status: 'ok' }
          : { status: 'empty', reason: 'yaml 未识别到该服务的数据层(可能是无 redis/mysql 等)' }
      }
    }
  }

  return { primaryConfigCenter: cc || '' }
}
