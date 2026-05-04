// useLokiLabels —— Step 7 Loki label 拉取 + 启发式自动匹(env / service 标签值)。
//
// 接 useLokiMappingState(state)+ useGrafanaDS(lokiAuthFor 凭证抽取)。
// 包含:
//   - loadLokiLabels         拉本环境的 label key 列表 + 启发式 envLabelKey/serviceLabelKey + 拉对应 values + autoMatch
//   - loadEnvLabelValues     单独拉 envLabelKey 的所有取值
//   - loadServiceLabelValues 单独拉 serviceLabelKey 的取值;envValue 已选时带 LogQL selector 过滤,只列该 namespace 下的 app
//   - autoMatchLokiMapping   启发式匹 (env.id → envLabelValues) + (service → serviceLabelValues),仅填空不覆盖
//   - onEnvLabelKeyChanged   改 envLabelKey → 清 envValue + serviceMatchTried + 重拉 + 重匹
//   - onServiceLabelKeyChanged 改 serviceLabelKey → 清 serviceValues + 重拉 + 重匹
//   - onEnvValueChanged      改 envValue → 清 serviceValues + 重拉 serviceLabelValues(限定到新 namespace)+ 重匹
import { listLokiLabels, listLokiLabelValues } from './bridge'
import { pushLog } from './logStore'
import { boundaryHasAnywhere, serviceMatchKeys } from './serviceMatchHelpers'
import type { LokiMappingPerEnv } from './useLokiMappingState'

export interface UseLokiLabelsDeps {
  /** useLokiMappingState 暴露的 getter */
  getLokiMapping: (envID: string) => LokiMappingPerEnv
  /** useGrafanaDS 暴露的 lokiAuthFor */
  lokiAuthFor: (envID: string) => {
    grafana_url: string
    api_key: string
    user: string
    pass: string
    loki_url: string
    ds_uid: string
  }
  /** 用于 autoMatch service 启发匹配 */
  allServiceNames: { value: readonly string[] }
}

export function useLokiLabels(deps: UseLokiLabelsDeps) {
  async function loadLokiLabels(envID: string) {
    const lm = deps.getLokiMapping(envID)
    lm.labelStatus = 'loading'
    lm.labelError = undefined
    try {
      const auth = deps.lokiAuthFor(envID)
      if (!auth.grafana_url && !auth.loki_url) {
        throw new Error('请先填本环境 Grafana URL 或 Loki URL')
      }
      if (auth.grafana_url && !auth.ds_uid) {
        throw new Error('请先选中本环境的 Loki datasource')
      }
      const r = await listLokiLabels(auth)
      lm.labels = r.labels || []
      lm.labelStatus = 'ok'
      pushLog('cchub', 'info', `[${envID}] Loki 拉到 ${lm.labels.length} 个 label key`, { envID })
      if (!lm.envLabelKey) {
        const guess = lm.labels.find(l => l === 'namespace')
          || lm.labels.find(l => l.includes('namespace'))
          || lm.labels.find(l => l.includes('env'))
        if (guess) lm.envLabelKey = guess
      }
      if (!lm.serviceLabelKey) {
        const guess = lm.labels.find(l => l === 'app')
          || lm.labels.find(l => l === 'service')
          || lm.labels.find(l => l === 'job')
          || lm.labels.find(l => l.includes('container'))
        if (guess) lm.serviceLabelKey = guess
      }
      if (lm.envLabelKey) await loadEnvLabelValues(envID)
      autoMatchLokiMapping(envID)
      // envValue 自动匹完之后再拉 serviceLabelValues —— 这次会带 selector 过滤,
      // 只列该 namespace 下出现过的 app,避免列出来一堆别 namespace 的 app
      if (lm.serviceLabelKey) await loadServiceLabelValues(envID)
      autoMatchLokiMapping(envID)
    } catch (e: any) {
      lm.labelStatus = 'fail'
      lm.labelError = String(e?.message || e)
      pushLog('cchub', 'error', `[${envID}] 列 Loki labels 失败: ${lm.labelError}`, { envID })
    }
  }

  async function loadEnvLabelValues(envID: string) {
    const lm = deps.getLokiMapping(envID)
    if (!lm.envLabelKey) return
    try {
      const auth = deps.lokiAuthFor(envID)
      const r = await listLokiLabelValues(auth, lm.envLabelKey)
      lm.envLabelValues = r.values || []
    } catch (e: any) {
      pushLog('cchub', 'error', `[${envID}] 列 ${lm.envLabelKey} 值失败: ${e?.message || e}`, { envID })
    }
  }

  // loadServiceLabelValues:如果已选了 envValue,会带 LogQL selector 过滤,
  // 只拉该 namespace 下确实出现过的 app 值,避免列出来一堆别的 namespace 的 app。
  async function loadServiceLabelValues(envID: string) {
    const lm = deps.getLokiMapping(envID)
    if (!lm.serviceLabelKey) return
    let query = ''
    if (lm.envLabelKey && lm.envValue) {
      // 转义 envValue 里的双引号(防止破坏 LogQL 语法)
      const safeVal = lm.envValue.replace(/"/g, '\\"')
      query = `{${lm.envLabelKey}="${safeVal}"}`
    }
    try {
      const auth = deps.lokiAuthFor(envID)
      const r = await listLokiLabelValues(auth, lm.serviceLabelKey, query)
      lm.serviceLabelValues = r.values || []
      pushLog('cchub', 'info',
        `[${envID}] ${lm.serviceLabelKey} ${query ? '(限定 ' + query + ')' : ''} 拉到 ${lm.serviceLabelValues.length} 个值`,
        { envID })
    } catch (e: any) {
      pushLog('cchub', 'error', `[${envID}] 列 ${lm.serviceLabelKey} 值失败: ${e?.message || e}`, { envID })
    }
  }

  // envValue 改变 → 旧 service 选择全清(可能新 namespace 下根本没那些 app),
  // 重拉 serviceLabelValues(限定到新 namespace 内),再启发式自动匹一遍。
  async function onEnvValueChanged(envID: string) {
    const lm = deps.getLokiMapping(envID)
    lm.serviceValues = {}
    await loadServiceLabelValues(envID)
    autoMatchLokiMapping(envID)
  }

  // 启发式自动匹:env.id="dev" → 在 envLabelValues 里找含 "dev" 的;
  // service 名 → serviceLabelValues 里找含服务名的。仅填空,不覆盖。
  function autoMatchLokiMapping(envID: string) {
    const lm = deps.getLokiMapping(envID)
    if (!lm.envValue) {
      const lower = envID.toLowerCase()
      const hit = lm.envLabelValues.find(v => v.toLowerCase().includes(lower))
      if (hit) lm.envValue = hit
    }
    // 服务 label 值匹配:跟 nacos / kuboard 同套退化策略 ——
    // serviceMatchKeys 生成 [community-grpc-server, community-grpc, community] 候选,
    // 段对齐前缀 + env 信号优先,逐级 fallback。loki app 标签常见命名:
    //   <service>-<env>             如 community-scheduler-dev
    //   base-<service>-<env>        如 base-admin-truss-dev
    //   <repo>-<env>                如 community-dev(没区分 cmd entry 时)
    // env 信号比 nacos 更强(loki 标签几乎一定带 env 后缀),所以 Pass 1 require env match。
    const envLower = envID.toLowerCase()
    const lmValuesLower = lm.serviceLabelValues.map(v => ({ raw: v, low: v.toLowerCase() }))
    // boundaryHasAnywhere(共享 helper)允许前缀(base-/app-),适配 base-admin-truss-dev → admin-truss
    for (const svc of deps.allServiceNames.value) {
      if (lm.serviceValues[svc]) continue // 已选(真实标签值)→ 不覆盖
      const candidates = serviceMatchKeys(svc)
      let hit: string | undefined
      // Pass 1:候选 boundary + 含 env
      for (const cand of candidates) {
        const m = lmValuesLower.find(v => boundaryHasAnywhere(v.low, cand) && v.low.includes(envLower))
        if (m) { hit = m.raw; break }
      }
      // Pass 2:候选 boundary(不含 env)
      if (!hit) {
        for (const cand of candidates) {
          const m = lmValuesLower.find(v => boundaryHasAnywhere(v.low, cand))
          if (m) { hit = m.raw; break }
        }
      }
      // Pass 3:fuzzy 完整服务名 substring
      if (!hit) {
        const sLower = svc.toLowerCase()
        const m = lmValuesLower.find(v => v.low.includes(sLower) && v.low.includes(envLower))
          || lmValuesLower.find(v => v.low.includes(sLower))
        if (m) hit = m.raw
      }
      if (hit) {
        lm.serviceValues[svc] = hit
      } else {
        // 跑过没找到 → 标记给 UI 显示"未匹配"提示,跟"还没跑"区分开
        if (!lm.serviceMatchTried) lm.serviceMatchTried = {}
        lm.serviceMatchTried[svc] = true
      }
    }
  }

  async function onEnvLabelKeyChanged(envID: string, newKey: string) {
    const lm = deps.getLokiMapping(envID)
    lm.envLabelKey = newKey
    lm.envValue = ''
    lm.serviceMatchTried = {} // 切 envLabelKey 后重新匹配,清掉老 tried 标记
    await loadEnvLabelValues(envID)
    autoMatchLokiMapping(envID)
  }

  async function onServiceLabelKeyChanged(envID: string, newKey: string) {
    const lm = deps.getLokiMapping(envID)
    lm.serviceLabelKey = newKey
    lm.serviceValues = {}
    lm.serviceMatchTried = {}
    await loadServiceLabelValues(envID)
    autoMatchLokiMapping(envID)
  }

  return {
    loadLokiLabels,
    loadEnvLabelValues,
    loadServiceLabelValues,
    onEnvValueChanged,
    autoMatchLokiMapping,
    onEnvLabelKeyChanged,
    onServiceLabelKeyChanged,
  }
}
