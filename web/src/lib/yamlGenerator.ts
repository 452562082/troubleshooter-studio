// yamlGenerator.ts —— wizard 表单 → system.yaml 文本。
// 设计:YAMLGenContext 打包 InitPage 里需要的所有 reactive / computed / helper,
//       InitPage call site 缩成 generateYAML(ctx)。lib 文件可被 vitest 直接 import,
//       不必 mount Vue 组件。

import yaml from 'js-yaml'
import { Target } from './constants'
import type { CredField } from './credFields'
import { yamlStr, hasAnyLokiMapping, emitLokiLabelMapping, type LokiEnvMapping } from './yamlEmit'
// VIA_GRAFANA_ELIGIBLE 跨 generator/useObsAccessMode/importer 共用,在 yamlShared 集中。
// re-export 给老 import("from './yamlGenerator'")兼容。
export { VIA_GRAFANA_ELIGIBLE } from './yamlShared'
import { VIA_GRAFANA_ELIGIBLE, placeholderName } from './yamlShared'

// ── 类型(跟 InitPage 现有 reactive 形状对齐) ────────────────────────

export interface YAMLGenSystem {
  id: string
  name: string
  description: string
}

export interface YAMLGenAgent {
  id: string
  name: string
  workspace_name: string
  model: string
}

export interface YAMLGenEnvironment {
  id: string
  api_domain: string
  web_domain: string
  is_prod: boolean
}

export interface YAMLGenRepo {
  name: string
  url: string
  stack: string
  framework: string
  role?: string
  sub_path?: string
  service_names: string
  env_branches: Record<string, string>
  _serviceEntries?: Record<string, string>
}

export interface YAMLGenSourceData {
  creds: Record<string, Record<string, string>>
  rawExtra?: Record<string, unknown>
}

export interface YAMLGenToolSpec {
  key: string
  fields: CredField[]
}

// KuboardSvcLocator 跟 yamlValidator/InitPage 共用,统一从 yamlShared 取。
export type { KuboardSvcLocator } from './yamlShared'
import type { KuboardSvcLocator } from './yamlShared'

export interface K8sRuntimeEnvLocator {
  cluster?: string
  namespace?: string
}

export interface K8sRuntimeSvcLocator {
  workload?: string
  label_selector?: string
}

/** 全部入参打包;InitPage call site 一次性把 25+ closure deps 通过此 object 传入。 */
export interface YAMLGenContext {
  system: YAMLGenSystem
  agent: YAMLGenAgent
  agentNameDefault: string
  targetModels: Record<string, string>
  enabledTargets: Record<string, boolean>
  enabledObservability: Record<string, boolean>
  environments: YAMLGenEnvironment[]
  repos: YAMLGenRepo[]
  sourceCreds: Record<string, YAMLGenSourceData>
  serviceConfigSel: Record<string, string>
  serviceConfigGroup: Record<string, string>
  envNamespaces: Record<string, string>
  kuboardSvcMap: Record<string, KuboardSvcLocator>
  lokiMappingByEnv: Record<string, LokiEnvMapping | undefined>
  toolInputs: Record<string, string>
  grafanaDsUidByObsEnv: Record<string, string>
  k8sRuntimeEnvLoc: Record<string, K8sRuntimeEnvLocator | undefined>
  k8sRuntimeSvcMap: Record<string, K8sRuntimeSvcLocator>
  scannedDS: Record<string, Record<string, Record<string, Record<string, string>>>>
  activeSourceTypes: string[]
  allServiceNames: string[]
  isMultiSource: boolean
  targetOptions: readonly string[]
  modelConsumingTargets: readonly string[]
  OBS_TOOL_SPECS: YAMLGenToolSpec[]
  CC_FIELDS_BY_TYPE: Record<string, CredField[]>
  // 帮助函数:行为对齐 InitPage 同名实现,内部直接 closure-read InitPage 状态
  normalizeDomain(s: string): string
  getServiceSource(svc: string): string
  isFieldHidden(t: string, envID: string, f: CredField, getSibling: (k: string) => string): boolean
  isObsFieldHidden(toolKey: string, envID: string, f: CredField): boolean
  getObsAccessMode(obsKey: string, envID: string): 'via_grafana' | 'direct'
  obsGrafanaDsKey(obsKey: string, envID: string): string
  svcKey(envID: string, svc: string): string
  toolKeyFor(cat: 'obs' | 'ds', tool: string, envID: string, field: string): string
  toolSpecByKey(cat: 'obs' | 'ds', key: string): YAMLGenToolSpec | undefined
  deriveSkillsWhitelist(): string[]
  /** emit 前 InitPage 会先 recompute 一次 enabledDataStores;无副作用调用方可传 noop */
  recomputeEnabledDataStoresFromScanned(): void
}

// ── 主函数(对齐原 InitPage::generateYAML) ────────────────────────

export function generateYAML(ctx: YAMLGenContext): string {
  // 出 yaml 之前先按 scannedDS 实时刷一次 enabledDataStores —— 这是 skills_whitelist
  // 派生 + Step 5 env-vars 字段集 + 校验逻辑共同的"启用清单",必须跟用户 Step 6 实际看到的
  // 数据层组件一致。
  ctx.recomputeEnabledDataStoresFromScanned()
  const lines: string[] = []

  // 顶部导言注释(解析时被忽略,只给用户看)
  lines.push('# 由初始化向导生成，可手工调整。字段说明：schema/system.schema.yaml')
  lines.push('# 以下行尾 # 注释仅为提示，YAML 解析时会被忽略。')

  // system
  lines.push('system:')
  lines.push(`  id: ${ctx.system.id || 'my-system'}                    # 机器可读标识，仅 [a-z0-9-]；用作 output_dir / agent id 前缀`)
  lines.push(`  name: ${yamlStr(ctx.system.name || 'My System')}          # 用户可见名称（中/英均可）`)
  if (ctx.system.description) lines.push(`  description: ${yamlStr(ctx.system.description)}`)

  // agent
  lines.push('')
  lines.push('agent:')
  // agent.id 空时推导 "<system.id>-troubleshooter",跟历史命名兼容。
  const agentID = (ctx.agent.id || '').trim() || `${ctx.system.id || 'my-system'}-troubleshooter`
  lines.push(`  id: ${agentID}            # AI 平台里的稳定标识(OpenClaw agents.list / Claude Code / Cursor subagent 名)`)
  lines.push(`  name: ${yamlStr(ctx.agent.name || ctx.agentNameDefault)}`)
  // model 是 openclaw 专属;workspace_name 不再单独 emit(Go 端 ResolveWorkspaceName 用 agent.id 当目录名)
  if (ctx.enabledTargets[Target.Openclaw]) {
    lines.push(`  model: ${ctx.agent.model}    # OpenClaw gateway 路由用的 LLM model id`)
    const tmEntries: [string, string][] = []
    for (const t of ctx.modelConsumingTargets) {
      if (!ctx.enabledTargets[t]) continue
      const v = (ctx.targetModels[t] || '').trim()
      if (v && v !== ctx.agent.model) tmEntries.push([t, v])
    }
    if (tmEntries.length > 0) {
      lines.push('  target_models:     # per-target 模型覆盖;key 只认 openclaw(其它 target 不消费)')
      for (const [t, m] of tmEntries) {
        lines.push(`    ${t}: ${m}`)
      }
    }
  }

  // environments
  lines.push('')
  lines.push('# environments：声明系统的所有环境。每个 env 会注册一套独立的 MCP 实例')
  lines.push('# （如 nacos-mcp-server-dev / -prod），机器人按 is_prod 调整谨慎度。')
  lines.push('environments:')
  for (const env of ctx.environments) {
    lines.push(`  - id: ${env.id || 'env'}`)
    const apiD = ctx.normalizeDomain(env.api_domain)
    const webD = ctx.normalizeDomain(env.web_domain)
    if (apiD) lines.push(`    api_domain: ${yamlStr(apiD)}     # 后端接口(带 http/https 前缀更明确;不带视为 https)`)
    if (webD) lines.push(`    web_domain: ${yamlStr(webD)}     # 前端入口(同上)`)
    lines.push(`    is_prod: ${env.is_prod}         # 生产环境标记:true 时机器人默认更保守、查询前二次确认`)
  }

  // repos
  lines.push('')
  lines.push('# repos：所有纳入排障范围的代码仓库。stack 决定 analyzer 与 skill 策略。')
  lines.push('repos:')
  for (const repo of ctx.repos) {
    lines.push(`  - name: ${repo.name || 'my-service'}`)
    lines.push(`    url: ${repo.url || 'git@github.com:org/repo.git'}`)
    lines.push(`    stack: ${repo.stack || 'go'}             # go/java/node/php/python，决定用哪种配置扫描器`)
    if (repo.role && repo.role !== 'backend') {
      lines.push(`    role: ${repo.role}             # 仓库角色:决定排障时是否进服务依赖图`)
    }
    if (repo.sub_path && repo.sub_path.trim()) {
      lines.push(`    sub_path: ${repo.sub_path.trim()}             # monorepo 子目录(本服务在仓库内的相对路径)`)
    }
    if (repo.framework) lines.push(`    framework: ${repo.framework}`)
    if (repo.service_names.trim()) {
      lines.push('    service_names:       # 本 repo 实际部署出来的 service 名（config-map 以此为 key）')
      for (const sn of repo.service_names.split(',').map(s => s.trim()).filter(Boolean)) {
        lines.push(`      - ${sn}`)
      }
    }
    if (repo._serviceEntries && Object.keys(repo._serviceEntries).length > 0) {
      lines.push('    service_entries:     # 同仓多服务时,每个服务在仓库内的入口子目录')
      for (const [name, entry] of Object.entries(repo._serviceEntries)) {
        if (!name || !entry) continue
        lines.push(`      ${name}: ${entry}`)
      }
    }
    const branchEntries = Object.entries(repo.env_branches).filter(([, v]) => v)
    if (branchEntries.length) {
      lines.push('    env_branches:        # 每个 env 对应的长期分支；routing skill 据此切换代码')
      for (const [eid, branch] of branchEntries) {
        lines.push(`      ${eid}: ${branch}`)
      }
    }
    // 多源场景声明本仓库走哪个源(取本仓服务列表里第一个的 source)。
    if (ctx.isMultiSource) {
      const svcs = repo.service_names.split(',').map(s => s.trim()).filter(Boolean)
      const firstSvc = svcs[0] || repo.name
      const src = ctx.getServiceSource(firstSvc)
      if (src && src !== ctx.activeSourceTypes[0]) {
        lines.push(`    config_source: ${src}    # 引用 infrastructure.config_centers[].id`)
      }
    }
  }

  // infrastructure
  lines.push('')
  lines.push('infrastructure:')

  // config_center / config_centers:用户填过的所有字段(含密码)都进 yaml,空字段给占位符
  // ⚠ 代价:yaml 带明文密码,分享范围必须可控
  const emitSourceBody = (out: string[], baseIndent: string, type: string, sourceID: string, includeServiceMap: boolean) => {
    const data = ctx.sourceCreds[type] || { creds: {} }
    const fields = ctx.CC_FIELDS_BY_TYPE[type] || []
    const isKuboard = type === 'kuboard'
    // kuboard:cluster/namespace/configmap 走 service_map,不进 endpoints
    const endpointFields = isKuboard
      ? fields.filter(f => f.key !== 'cluster' && f.key !== 'namespace' && f.key !== 'configmap')
      : fields
    if (endpointFields.length > 0) {
      out.push(`${baseIndent}endpoints:     # ⚠ 含明文凭证,仅团队私密范围分享,别 commit 公开 git`)
      for (const env of ctx.environments) {
        if (!env.id) continue
        out.push(`${baseIndent}  - env: ${env.id}`)
        const envCreds = data.creds[env.id] || {}
        for (const f of endpointFields) {
          if (f.uiOnly) continue
          if (ctx.isFieldHidden(type, env.id, f, (k) => (envCreds[k] || ''))) continue
          const v = (envCreds[f.key] || '').trim()
          if (v) {
            const comment = f.secret ? '      # ⚠ secret,yaml 分享注意范围' : ''
            out.push(`${baseIndent}    ${f.key}: ${yamlStr(v)}${comment}`)
          } else {
            const ph = placeholderName(f.envVar(env.id), sourceID)
            out.push(`${baseIndent}    ${f.key}: "{{${ph}}}"      # 没填,部署时交互收集`)
          }
        }
      }
    }
    if (includeServiceMap) {
      const svcMapLines: string[] = []
      for (const env of ctx.environments) {
        if (!env.id) continue
        const perEnv: string[] = []
        for (const svc of ctx.allServiceNames) {
          if (ctx.getServiceSource(svc) !== type) continue
          if (isKuboard) {
            const loc = ctx.kuboardSvcMap[ctx.svcKey(env.id, svc)]
            if (!loc) continue
            const cluster = (loc.cluster || '').trim()
            const ns = (loc.namespace || '').trim()
            const cm = (loc.configmap || '').trim()
            if (!cluster || !ns || !cm) continue
            perEnv.push(`${baseIndent}      ${yamlStr(svc)}:`)
            perEnv.push(`${baseIndent}        cluster: ${yamlStr(cluster)}`)
            perEnv.push(`${baseIndent}        namespace: ${yamlStr(ns)}`)
            perEnv.push(`${baseIndent}        configmap: ${yamlStr(cm)}`)
          } else {
            const dataId = (ctx.serviceConfigSel[ctx.svcKey(env.id, svc)] || '').trim()
            if (!dataId) continue
            const ns = (ctx.envNamespaces[env.id] || '').trim()
            const group = (ctx.serviceConfigGroup[ctx.svcKey(env.id, svc)] || '').trim()
            perEnv.push(`${baseIndent}      ${yamlStr(svc)}:`)
            if (ns) perEnv.push(`${baseIndent}        namespace: ${yamlStr(ns)}`)
            if (group) perEnv.push(`${baseIndent}        group: ${yamlStr(group)}`)
            perEnv.push(`${baseIndent}        data_id: ${yamlStr(dataId)}`)
          }
        }
        if (perEnv.length > 0) {
          svcMapLines.push(`${baseIndent}    ${env.id}:`)
          svcMapLines.push(...perEnv)
        }
      }
      if (svcMapLines.length > 0) {
        const fieldList = isKuboard ? 'cluster / namespace / configmap' : 'namespace / group / data_id'
        out.push(`${baseIndent}service_map:   # 每个环境每个服务对应哪条配置(${fieldList})`)
        out.push(...svcMapLines)
      }
    }
    // rawExtra(yaml 高级字段透传):防御老 saved 残留 service_map 把当前生成的覆盖
    if (data.rawExtra) {
      const safeExtra: Record<string, unknown> = {}
      for (const [k, v] of Object.entries(data.rawExtra)) {
        if (k === 'service_map') continue
        safeExtra[k] = v
      }
      if (Object.keys(safeExtra).length > 0) {
        const dump = yaml.dump(safeExtra, { indent: 2, lineWidth: -1 })
        for (const line of dump.split('\n')) {
          if (line.trim() === '') continue
          out.push(`${baseIndent}${line}`)
        }
      }
    }
  }

  const active = ctx.activeSourceTypes
  if (active.length === 0) {
    lines.push('  config_center:        # 没勾配置源,写 none 占位')
    lines.push('    type: none')
  } else if (active.length === 1) {
    const t = active[0]
    lines.push('  config_center:        # 配置中心:nacos/apollo/consul/kubernetes/env-vars/none')
    lines.push(`    type: ${t}`)
    emitSourceBody(lines, '    ', t, 'default', true)
  } else {
    lines.push('  config_centers:        # 多源配置:每个源独立 type/凭证;repos[].config_source 引用 id')
    for (const t of active) {
      lines.push(`    - id: ${t}        # 源 id 跟 type 同名(简单模式;同 type 多源需手编辑)`)
      lines.push(`      type: ${t}`)
      emitSourceBody(lines, '      ', t, t, true)
    }
  }

  // observability:对每个勾选的工具按 env 列连接字段。loki 标签映射即使没勾 loki 也输出。
  const lokiDeps = {
    environments: ctx.environments.map(e => ({ id: e.id })),
    lokiMappingByEnv: ctx.lokiMappingByEnv,
    allServiceNames: ctx.allServiceNames,
  }
  const anyObs = Object.values(ctx.enabledObservability).some(Boolean) || hasAnyLokiMapping(lokiDeps)
  if (anyObs) {
    lines.push('')
    lines.push('  observability:        # ⚠ 含明文凭证,仅团队私密范围分享')
    for (const spec of ctx.OBS_TOOL_SPECS) {
      if (!ctx.enabledObservability[spec.key]) continue
      lines.push(`    ${spec.key}:`)
      lines.push('      enabled: true')
      if (spec.key === 'elk') {
        lines.push(`      default_index: "${ctx.system.id || 'my-system'}-logs-*"`)
      }
      const isViaGrafanaEligible = (VIA_GRAFANA_ELIGIBLE as readonly string[]).includes(spec.key)
      const anyViaGrafana = isViaGrafanaEligible && ctx.environments.some(env =>
        env.id && ctx.getObsAccessMode(spec.key, env.id) === 'via_grafana')
      if (spec.key === 'loki' || spec.key === 'prometheus' || spec.key === 'jaeger' || spec.key === 'tempo' || spec.key === 'elk') {
        lines.push(`      via_grafana: ${anyViaGrafana}`)
      }
      if (spec.key === 'loki') {
        emitLokiLabelMapping(lines, '      ', lokiDeps)
      }
      const envRows: string[] = []
      for (const env of ctx.environments) {
        if (!env.id) continue
        if (isViaGrafanaEligible && ctx.getObsAccessMode(spec.key, env.id) === 'via_grafana') continue
        const fieldLines: string[] = []
        for (const f of spec.fields) {
          if (f.uiOnly) continue
          if (ctx.isObsFieldHidden(spec.key, env.id, f)) continue
          const k = ctx.toolKeyFor('obs', spec.key, env.id, f.key)
          const v = (ctx.toolInputs[k] || '').trim()
          if (v) {
            const note = f.secret ? '      # ⚠ secret' : ''
            fieldLines.push(`          ${f.key}: ${yamlStr(v)}${note}`)
          }
        }
        if (fieldLines.length > 0) {
          envRows.push(`        - env: ${env.id}`)
          envRows.push(...fieldLines)
        }
      }
      if (envRows.length > 0) {
        lines.push('      endpoints:')
        lines.push(...envRows)
      }
      if (['prometheus', 'jaeger', 'tempo', 'elk'].includes(spec.key)) {
        const uidRows: string[] = []
        for (const env of ctx.environments) {
          if (!env.id) continue
          if (ctx.getObsAccessMode(spec.key, env.id) !== 'via_grafana') continue
          const uid = (ctx.grafanaDsUidByObsEnv[ctx.obsGrafanaDsKey(spec.key, env.id)] || '').trim()
          if (uid) uidRows.push(`        ${env.id}: ${yamlStr(uid)}`)
        }
        if (uidRows.length > 0) {
          lines.push('      datasource_uid_by_env:        # 走 Grafana 代理时用的 datasource UID')
          lines.push(...uidRows)
        }
      }
      // k8s_runtime:env 级 cluster+ns + 服务级 workload+selector,routing skill 拼 KuboardListPods
      if (spec.key === 'k8s_runtime') {
        const svcRows: string[] = []
        for (const env of ctx.environments) {
          if (!env.id) continue
          const eloc = ctx.k8sRuntimeEnvLoc[env.id]
          if (!eloc?.cluster || !eloc?.namespace) continue
          for (const svc of ctx.allServiceNames) {
            const sloc = ctx.k8sRuntimeSvcMap[ctx.svcKey(env.id, svc)]
            // 没挑 workload 也照样落一行 cluster+ns,routing skill 至少能定位到 ns 级,
            // 落到具体 pod 时再 fallback 到 svc 名做 label 模糊匹配。
            svcRows.push(`        - env: ${env.id}`)
            svcRows.push(`          service: ${yamlStr(svc)}`)
            svcRows.push(`          cluster: ${yamlStr(eloc.cluster)}`)
            svcRows.push(`          namespace: ${yamlStr(eloc.namespace)}`)
            if (sloc?.workload) svcRows.push(`          workload: ${yamlStr(sloc.workload)}`)
            if (sloc?.label_selector) svcRows.push(`          label_selector: ${yamlStr(sloc.label_selector)}`)
          }
        }
        if (svcRows.length > 0) {
          lines.push('      service_map:        # routing skill 解析 env+服务名时用')
          lines.push(...svcRows)
        }
      }
    }
    // 兜底:用户只勾了 grafana(没勾 loki)但配过 Loki 标签映射 → 写一个 loki 节点承载映射
    if (!ctx.enabledObservability.loki && hasAnyLokiMapping(lokiDeps)) {
      lines.push('    loki:')
      lines.push('      enabled: false      # 仅承载标签映射,实际通过 Grafana 代理查询')
      lines.push('      via_grafana: true')
      emitLokiLabelMapping(lines, '      ', lokiDeps)
    }
  }

  // data_stores:从 scannedDS(env → service → dsKey → fields)推导
  const dsTypesUsed = new Set<string>()
  for (const envID of Object.keys(ctx.scannedDS)) {
    for (const svc of Object.keys(ctx.scannedDS[envID])) {
      for (const dsKey of Object.keys(ctx.scannedDS[envID][svc])) {
        dsTypesUsed.add(dsKey)
      }
    }
  }
  if (dsTypesUsed.size > 0) {
    lines.push('')
    lines.push('  data_stores:          # 从各服务配置自动识别的数据层;⚠ 含明文凭证,分享注意范围')
    for (const dsType of Array.from(dsTypesUsed).sort()) {
      const spec = ctx.toolSpecByKey('ds', dsType)
      lines.push(`    - type: ${dsType}`)
      lines.push('      enabled: true')
      lines.push('      readonly_enforced: true    # 强制只读;generator 拒绝写操作')
      const epRows: string[] = []
      for (const env of ctx.environments) {
        if (!env.id) continue
        const svcs = ctx.scannedDS[env.id]
        if (!svcs) continue
        for (const svc of Object.keys(svcs).sort()) {
          const fields = svcs[svc]?.[dsType]
          if (!fields) continue
          const fieldLines: string[] = []
          for (const [fKey, val] of Object.entries(fields)) {
            if (!val) continue
            const note = spec?.fields.find(f => f.key === fKey)?.secret ? '          # ⚠ secret' : ''
            fieldLines.push(`          ${fKey}: ${yamlStr(val)}${note}`)
          }
          if (fieldLines.length > 0) {
            epRows.push(`        - env: ${env.id}`)
            epRows.push(`          service: ${yamlStr(svc)}`)
            epRows.push(...fieldLines)
          }
        }
      }
      if (epRows.length > 0) {
        lines.push('      endpoints:')
        lines.push(...epRows)
      }
    }
  }

  // generation
  const skills = ctx.deriveSkillsWhitelist()
  lines.push('')
  lines.push('generation:')
  // output_dir 故意不写:CLI `tshoot gen` 才会读它,桌面 ImportAndDeploy 走 ~/.tshoot/...,
  // wizard 用户不需要;CLI 用户可以手动加这一行覆盖默认 ./dist。
  const selectedTargets = ctx.targetOptions.filter(t => ctx.enabledTargets[t])
  const targetList = selectedTargets.length ? selectedTargets : ['openclaw']
  lines.push('  targets:                             # 每个 target 产出一份机器人产物（同一份 system.yaml）')
  for (const t of targetList) {
    lines.push(`    - ${t}`)
  }
  lines.push('  skills_whitelist:                    # 只列出的 skill 会进工作区(留空 = 全开)')
  for (const s of skills) {
    lines.push(`    - ${s}`)
  }
  // preserve_on_regenerate 只对 openclaw target 生效;claude-code/cursor 产物没有 snapshot-restore 路径
  if (ctx.enabledTargets[Target.Openclaw]) {
    lines.push('  preserve_on_regenerate:              # 二次 gen 时整体保留这些文件,让用户手改不丢(仅 openclaw)')
    lines.push('    - SOUL.md')
    lines.push('    - USER.md')
    lines.push('    - CHECKLIST.md')
  }

  // meta
  lines.push('')
  lines.push('meta:')
  lines.push('  schema_version: "0.1"')
  lines.push('  tshoot_template_ref:')
  lines.push('    repo: troubleshooter-studio')
  lines.push('    ref: main')

  return lines.join('\n') + '\n'
}
