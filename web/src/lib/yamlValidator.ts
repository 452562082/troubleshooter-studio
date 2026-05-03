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
  ccHubStateByEnv: Record<string, CCHubEnvState | undefined>
  dsProbeResults: Record<string, DSProbeState | undefined>
  /** 跟 InitPage 同款 helper,内部 isFieldHidden 复用 */
  isFieldHidden(t: string, envID: string, f: CredField, getSibling: (k: string) => string): boolean
  getServiceSource(svc: string): string
  /** 列出 Step 7 校验对象;InitPage 那边的 enumerateDataStoreProbeTargets() */
  enumerateDataStoreProbeTargets(): DSProbeTarget[]
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
    // 仓库本地路径硬性必填(local / remote 都要),理由:产物 repo-path-map.yaml + BotsPage 诊断
    // + 代码扫描都靠 ~/.tshoot/config.json 里这份。remote 模式下 _cloneTarget 是落盘父目录,
    // 必须显式声明,不允许 "反正默认 clone 到 ~/.tshoot/repos" 这种隐式行为。
    ctx.repos.forEach((r, i) => {
      if (!r.name.trim()) errs.add(`repo.${i}.name`)
      if (r._source === 'local') {
        if (!(r._localPath || '').trim()) errs.add(`repo.${i}.localPath`)
      } else {
        if (!r.url.trim()) errs.add(`repo.${i}.url`)
        if (!(r._cloneTarget || '').trim()) errs.add(`repo.${i}.cloneTarget`)
      }
    })
    return errs
  }

  if (step === 6) {
    // 多源:每个服务必须明确归属某源(单源默认所有服务都走唯一源,不强制)
    if (ctx.isMultiSource && ctx.allServiceNames.length > 0) {
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
      ctx.environments.forEach((e) => {
        if (!e.id.trim()) return
        const svcsOnThisSource = ctx.allServiceNames.filter(svc => ctx.getServiceSource(svc) === t)
        if (svcsOnThisSource.length === 0) return
        // 凭证字段必填(optional / kuboard 的 cluster/ns/cm 跳过 / showWhen 隐藏跳过)
        for (const f of fields) {
          if (f.optional) continue
          if (f.uiOnly) continue
          if (isKuboard && (f.key === 'cluster' || f.key === 'namespace' || f.key === 'configmap')) continue
          if (ctx.isFieldHidden(t, e.id, f, (k) => getField(e.id, k))) continue
          if (!getField(e.id, f.key)) {
            errs.add(`cc.${t}.${e.id}.${f.key}`)
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
    // 数据层:只校验页面上展示的组件连通性。yaml 可能本来就没数据层(纯网关 / 纯调度)scannedDS 全空
    // 是合法的;用户可以删掉某些识别出的组件;手填也允许。校验只看 dsProbeResults 里 status==='ok'。
    for (const t of ctx.enumerateDataStoreProbeTargets()) {
      const probeSt = ctx.dsProbeResults[probeKey(t.envID, t.svc, t.dsKey)]
      if (!probeSt || probeSt.status !== 'ok') {
        if (probeSt?.status === 'fail') {
          errs.add(`ds.${t.envID}.${t.svc}.${t.dsKey}.probefail`)
        } else {
          errs.add(`ds.${t.envID}.${t.svc}.${t.dsKey}.notested`)
        }
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
    if (f === 'url') return `仓库 #${i} URL`
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
    const envID = parts[2]
    const kind = parts[3]
    if (kind === 'scan') return `${envID} 环境(${source} 源)未预加载成功`
    if (kind === 'namespace') return `${envID} 环境(${source} 源)未选 namespace`
    if (kind === 'svc') {
      const svcName = parts[4]
      if (source === 'kuboard') return `${envID} 环境 "${svcName}" 服务未挑齐 集群/namespace/ConfigMap`
      return `${envID} 环境 "${svcName}" 服务未映射 dataId`
    }
    return `${source}.${envID}.${kind}`
  }
  if (k.startsWith('ds.')) {
    const parts = k.split('.')
    const last = parts[parts.length - 1]
    if (last === 'probefail') return `${parts[1]} 环境 "${parts[2]}" 服务的 ${parts[3]} 连通性失败`
    if (last === 'notested')  return `${parts[1]} 环境 "${parts[2]}" 服务的 ${parts[3]} 未测连通性`
    return `${parts[1]} 环境 "${parts[2]}" 服务配置未拉取 / 解析成功`
  }
  return k
}
