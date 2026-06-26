// yamlValidator.ts —— InitPage step-by-step 校验逻辑。
// 跟 yamlGenerator 同款打包 ctx 入参,InitPage 那边 call site 缩成 computeStepErrors(ctx)。
//
// 所有 helper(ccKeyFor / svcKey / probeKey)直接搬过来 —— 它们是纯字符串拼接,
// 不依赖 reactive,InitPage 那边可以删掉同名实现统一从这里 import。
//
// 校验范围分步规则:
//   Step 1 欢迎页(导入 yaml / 从零开始):无校验
//   Step 2:system.id 必填且 [a-z0-9-];system.name 必填
//   Step 3:agent.name 必填、≥1 个 target、勾 openclaw 要 model
//   Step 4:每个 env 的 id + api_domain 必填
//   Step 5:每个 repo:name + (remote 必填 url+_cloneTarget,local 必填 _localPath)
//   Step 6:所选源每个 (env, svc) 组合的 non-optional 字段必填(showWhen 隐藏跳过)
//          多源必须每个服务都明确归属;kuboard 还要扫过 + 每服务挑齐 cluster/ns/cm
//   Step 7:dsProbeResults 里展示的每个组件 status='ok'

import { Target } from './constants'
import { canonicalizeGitURL } from './canonicalGitURL'
import type { CredField, KuboardResourceState } from './credFields'

export interface ValidatorEnvironment {
  id: string
  api_domain: string
  is_prod?: boolean
}

export interface ValidatorRepo {
  name: string
  url: string
  _source?: 'local' | 'remote'
  _localPath?: string
  _cloneTarget?: string
  // _fromYAML / _yamlOriginalURL: yaml import 的 repo 身份锚定校验用
  // (URL 改成不同仓 = 标红阻塞下一步)
  _fromYAML?: boolean
  _yamlOriginalURL?: string
}

// KuboardSvcLocator 跨 emit/generator/validator/importer 共用,统一从 yamlShared 取。
export type { KuboardSvcLocator } from './yamlShared'
import type { KuboardSvcLocator } from './yamlShared'

export type { ProbeStatus } from './probeTypes'
import type { ProbeStatus } from './probeTypes'

export interface CCHubEnvState {
  status: 'idle' | 'loading' | 'ok' | 'error'
}

export interface DSProbeState {
  status: ProbeStatus
}

export interface DSProbeTarget {
  envID: string
  svc: string
  dsKey: string
}

export interface ValidatorContext {
  step: number
  system: { id: string; name: string }
  agent: { name: string }
  enabledTargets: Record<string, boolean>
  targetModels: Record<string, string>
  anyTargetSelected: boolean
  environments: ValidatorEnvironment[]
  repos: ValidatorRepo[]
  isMultiSource: boolean
  allServiceNames: string[]
  activeSourceTypes: string[]
  CC_FIELDS_BY_TYPE: Record<string, CredField[]>
  ccCredInputs: Record<string, string>
  sourceCreds: Record<string, { creds: Record<string, Record<string, string>> }>
  envNamespaces: Record<string, string>
  serviceConfigSel: Record<string, string>
  kuboardStateByEnv: Record<string, KuboardResourceState | undefined>
  kuboardSvcMap: Record<string, KuboardSvcLocator>
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  one2allStateByEnv?: Record<string, any>
  one2allSvcMap?: Record<string, { cluster_id: string; namespace: string; configmap: string }>
  ccHubStateByEnv: Record<string, CCHubEnvState | undefined>
  dsProbeResults: Record<string, DSProbeState | undefined>
  dsScanState?: Record<string, { status: string; reason?: string } | undefined>
  /** 跟 InitPage 同款 helper,内部 isFieldHidden 复用 */
  isFieldHidden(t: string, envID: string, f: CredField, getSibling: (k: string) => string): boolean
  getServiceSource(svc: string): string
  /** 列出 Step 7 校验对象;InitPage 那边的 enumerateDataStoreProbeTargets() */
  enumerateDataStoreProbeTargets(): DSProbeTarget[]
  /** Step 8 用:可观测组件勾选 map(grafana/loki/prometheus/jaeger/elk/...) */
  enabledObservability: Record<string, boolean>
}

// 共享 key 拼接收口在 yamlShared.ts;这里 re-export 让老调用方(只 import 本文件的)
// 仍能用,InitPage / 新代码请直接 import yamlShared。
export { ccKeyFor, svcKey, probeKey } from './yamlShared'
import { ccKeyFor, svcKey, probeKey } from './yamlShared'

// ── 主校验函数 ────────────────────────────────────────────────────

export function computeStepErrors(ctx: ValidatorContext): Set<string> {
  const errs = new Set<string>()
  const step = ctx.step

  if (step === 1) {
    // 欢迎页:无强制校验
    return errs
  }

  if (step === 2) {
    if (!ctx.system.id.trim()) errs.add('system.id')
    else if (!/^[a-z0-9][a-z0-9-]*$/.test(ctx.system.id)) errs.add('system.id')
    if (!ctx.system.name.trim()) errs.add('system.name')
    return errs
  }

  if (step === 3) {
    if (!ctx.agent.name.trim()) errs.add('agent.name')
    if (!ctx.anyTargetSelected) errs.add('targets.none')
    if (ctx.enabledTargets[Target.Openclaw]) {
      if (!(ctx.targetModels[Target.Openclaw] || '').trim()) errs.add('model.openclaw')
    }
    return errs
  }

  if (step === 4) {
    ctx.environments.forEach((e, i) => {
      if (!e.id.trim()) errs.add(`env.${i}.id`)
      if (!e.api_domain.trim()) errs.add(`env.${i}.api_domain`)
    })
    return errs
  }

  if (step === 5) {
    // 仓库本地路径策略:
    //   - local 模式:_localPath 必填(用户已 clone 好,必须告诉我们在哪)
    //   - remote 模式:url 必填,_cloneTarget **可选** —— 不填走上方"全局默认 clone 父目录",
    //     useRepoScan 的 refreshRoleHint / refreshSubmoduleHints / scanSingleRepo 都已统一
    //     "_cloneTarget / reposRootInput / resolvedReposRoot"三层兜底。强制每个 repo 都填
    //     一遍同样的 ~/go/src/ 是冗余 UX,撤掉。用户想给单个 repo 配特殊目录(比如
    //     commerce 放 ~/code/ 不放 ~/go/src/)再填 _cloneTarget 即可。
    //   - umbrella 子模块(parent_repo 在场):部署时 clone 到 <umbrella>/<parent_path>,
    //     _cloneTarget 也无需填,让 analyzerpipe 走 umbrella 继承编排。
    ctx.repos.forEach((r, i) => {
      if (!r.name.trim()) errs.add(`repo.${i}.name`)
      if (r._source === 'local') {
        if (!(r._localPath || '').trim()) errs.add(`repo.${i}.localPath`)
      } else {
        if (!r.url.trim()) errs.add(`repo.${i}.url`)
      }
      // _fromYAML 身份锚定:URL canonicalize 后必须跟 yaml 原 URL 一致,允许 ssh ↔
      // https 等同仓换协议,拒绝换项目。url 空 / _yamlOriginalURL 空都跳过(由上面
      // url 必填规则兜底)。
      if (r._fromYAML && r.url.trim() && r._yamlOriginalURL && r._yamlOriginalURL.trim()) {
        if (canonicalizeGitURL(r.url) !== canonicalizeGitURL(r._yamlOriginalURL)) {
          errs.add(`repo.${i}.url.identity`)
        }
      }
    })
    return errs
  }

  if (step === 6) {
    // 每个服务必须明确归属某源(多源强制;单源检查是否有显式取消)
    if (ctx.allServiceNames.length > 0) {
      for (const svc of ctx.allServiceNames) {
        if (!ctx.getServiceSource(svc)) {
          errs.add(`cc.unassigned.${svc}`)
        }
      }
    }
    const primary = ctx.activeSourceTypes[0] || ''
    for (const t of ctx.activeSourceTypes) {
      const fields = ctx.CC_FIELDS_BY_TYPE[t] || []
      if (fields.length === 0) continue
      // 主源走 ccCredInputs,副源走 sourceCreds[t].creds
      const getField = (envID: string, fieldKey: string): string => {
        if (t === primary) return (ctx.ccCredInputs[ccKeyFor(t, envID, fieldKey)] || '').trim()
        return ((ctx.sourceCreds[t]?.creds?.[envID]?.[fieldKey]) || '').trim()
      }
      const isKuboard = t === 'kuboard'
      const isOne2All = t === 'one2all'
      ctx.environments.forEach((e) => {
        if (!e.id.trim()) return
        const svcsOnThisSource = ctx.allServiceNames.filter(svc => ctx.getServiceSource(svc) === t)
        if (svcsOnThisSource.length === 0) return
        // 凭证字段必填:one2all 只查 _shared_ 一次,kuboard 跳过 cluster/ns/cm,其它 per-env
        if (isOne2All) {
          // one2all:全局凭证,只检查一次
          for (const f of fields) {
            if (f.optional) continue
            if (f.uiOnly) continue
            if (!(ctx.ccCredInputs[ccKeyFor(t, '_shared_', f.key)] || '').trim()) {
              errs.add(`cc.${t}._shared_.${f.key}`)
            }
          }
        } else {
          for (const f of fields) {
            if (f.optional) continue
            if (f.uiOnly) continue
            if (isKuboard && (f.key === 'cluster' || f.key === 'namespace' || f.key === 'configmap')) continue
            if (ctx.isFieldHidden(t, e.id, f, (k) => getField(e.id, k))) continue
            if (!getField(e.id, f.key)) {
              errs.add(`cc.${t}.${e.id}.${f.key}`)
            }
          }
        }
        if (isKuboard) {
          const kbSt = ctx.kuboardStateByEnv[e.id]
          if (!kbSt || kbSt.status !== 'ok') {
            errs.add(`cc.${t}.${e.id}.scan`)
            return
          }
          for (const svc of svcsOnThisSource) {
            const loc = ctx.kuboardSvcMap[svcKey(e.id, svc)]
            if (!loc || !loc.cluster || !loc.namespace || !loc.configmap) {
              errs.add(`cc.${t}.${e.id}.svc.${svc}`)
            }
          }
        } else if (isOne2All) {
          // one2all:预加载状态 + per-service cluster_id/namespace/configmap
          const o2aSt = ctx.one2allStateByEnv?.[e.id]
          if (!o2aSt || o2aSt.status !== 'ok') {
            errs.add(`cc.${t}.${e.id}.scan`)
            return
          }
          for (const svc of svcsOnThisSource) {
            const loc = ctx.one2allSvcMap?.[svcKey(e.id, svc)]
            if (!loc || !loc.cluster_id || !loc.namespace || !loc.configmap) {
              errs.add(`cc.${t}.${e.id}.svc.${svc}`)
            }
          }
        } else {
          // nacos / apollo / consul:走 ccHub 预加载 + namespace + per-svc dataId
          const st = ctx.ccHubStateByEnv[e.id]
          if (!st || st.status !== 'ok') {
            errs.add(`cc.${t}.${e.id}.scan`)
            return
          }
          if (!(e.id in ctx.envNamespaces)) {
            errs.add(`cc.${t}.${e.id}.namespace`)
            return
          }
          for (const svc of svcsOnThisSource) {
            if (!(ctx.serviceConfigSel[svcKey(e.id, svc)] || '').trim()) {
              errs.add(`cc.${t}.${e.id}.svc.${svc}`)
            }
          }
        }
      })
    }
    return errs
  }

  if (step === 7) {
    // 扫描本身失败(网络/鉴权) → 阻止下一步;空/跳过 → 不阻止
    if (ctx.dsScanState) {
      for (const [k, v] of Object.entries(ctx.dsScanState)) {
        if (v?.status === 'error') errs.add(`ds.scanerror.${k}`)
      }
    }
    // 连通性:测试失败 → 阻止;未测试 → 不阻止
    for (const t of ctx.enumerateDataStoreProbeTargets()) {
      const probeSt = ctx.dsProbeResults[probeKey(t.envID, t.svc, t.dsKey)]
      if (probeSt?.status === 'fail') {
        errs.add(`ds.${t.envID}.${t.svc}.${t.dsKey}.probefail`)
      }
    }
    return errs
  }

  if (step === 8) {
    // 可观测性:Loki/Prometheus/Tempo 启用必须 Grafana 启用(它们在本系统通过
    // mcp-grafana-npx 内置工具查询,无独立 MCP 包)— 跟 health_observability.go 同款规则。
    const grafanaOn = !!ctx.enabledObservability['grafana']
    for (const k of ['loki', 'prometheus', 'tempo']) {
      if (ctx.enabledObservability[k] && !grafanaOn) {
        errs.add(`obs.${k}.needs_grafana`)
      }
    }
    return errs
  }

  return errs
}

// ── 错误 key → 中文标签翻译,给"还差 N 项"按钮 title 用 ──

const STATIC_LABELS: Record<string, string> = {
  'system.id': '系统 ID',
  'system.name': '系统显示名',
  'agent.name': '机器人名称',
  'agent.workspace_name': 'OpenClaw 工作区名',
  'targets.none': '至少勾一个部署平台',
  'model.openclaw': 'OpenClaw 模型',
}

export function labelForErrorKey(k: string, repos: ValidatorRepo[]): string {
  if (STATIC_LABELS[k]) return STATIC_LABELS[k]
  if (k.startsWith('env.')) {
    const [, i, field] = k.split('.')
    return `环境 #${Number(i) + 1} ${field === 'id' ? 'ID' : 'API 域名'}`
  }
  if (k.startsWith('repo.')) {
    const parts = k.split('.')
    const i = Number(parts[1]) + 1
    const f = parts[2]
    if (f === 'localPath') return `仓库 #${i} 本地目录`
    if (f === 'url') {
      // 第 4 段是 'identity' = 身份锚定校验失败(URL 改成了不同仓库)
      if (parts[3] === 'identity') {
        const orig = repos[i - 1]?._yamlOriginalURL || '<yaml 原 URL>'
        return `仓库 #${i} URL 跟 yaml 锚定 URL 不是同一个仓库(yaml: ${orig});允许 ssh ↔ https 等同仓换协议,不允许换项目`
      }
      return `仓库 #${i} URL`
    }
    if (f === 'cloneTarget') return `仓库 #${i} clone 落盘父目录(必填,远程仓库部署时 clone 到 <父目录>/${repos[i - 1]?.name || '<repo.name>'})`
    return `仓库 #${i} ${f}`
  }
  if (k.startsWith('cc.')) {
    if (k.startsWith('cc.unassigned.')) {
      const svcName = k.substring('cc.unassigned.'.length)
      return `服务 "${svcName}" 未分配源 —— 在某个源面板的"本环境包含的服务"里勾上`
    }
    const parts = k.split('.')
    const source = parts[1]
    let envID = parts[2]
    const kind = parts[3]
    // one2all 全局凭证用 _shared_ 作 key,不是真正 env
    if (envID === '_shared_') {
      const field = kind
      const labels: Record<string, string> = { mcp_url: 'MCP Server URL', token: 'Bearer Token' }
      return `one2all 全局连接缺少 ${labels[field] || field}`
    }
    if (kind === 'scan') return `${envID} 环境(${source} 源)未预加载成功`
    if (kind === 'namespace') return `${envID} 环境(${source} 源)未选 namespace`
    if (kind === 'svc') {
      const svcName = parts[4]
      if (source === 'kuboard') return `${envID} 环境 "${svcName}" 服务未挑齐 集群/namespace/ConfigMap`
      return `${envID} 环境 "${svcName}" 服务未映射 dataId`
    }
    return `${source}.${envID}.${kind}`
  }
  if (k.startsWith('ds.scanerror.')) {
    const key = k.substring('ds.scanerror.'.length)
    const parts = key.split('::')
    return `${parts[0] || '?'} / ${parts[1] || '?'} 配置扫描失败,请检查凭证或网络`
  }
  if (k.startsWith('ds.')) {
    const parts = k.split('.')
    const last = parts[parts.length - 1]
    if (last === 'probefail') return `${parts[1]} 环境 "${parts[2]}" 服务的 ${parts[3]} 连通性失败`
    if (last === 'notested')  return `${parts[1]} 环境 "${parts[2]}" 服务的 ${parts[3]} 未测连通性`
    return `${parts[1]} 环境 "${parts[2]}" 服务配置未拉取 / 解析成功`
  }
  if (k.startsWith('obs.')) {
    const parts = k.split('.')
    if (parts[2] === 'needs_grafana') {
      const tool = { loki: 'Loki', prometheus: 'Prometheus', tempo: 'Tempo' }[parts[1]] || parts[1]
      return `${tool} 启用但 Grafana 未启用(${tool} 通过 grafana MCP 内置工具查询,需先勾 Grafana)`
    }
  }
  return k
}
