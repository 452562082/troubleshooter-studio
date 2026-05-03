// useGrafanaDS —— Step 7 Grafana datasource 列表 + per-(obsType, env) 选中 UID 管理。
//
// 包含:
//   - obsGrafanaDsKey      "<obsKey>:<envID>" map key 拼接(yamlImporter 反填也用)
//   - lokiAuthFor          从 toolInputs 抽 grafana / loki url + auth_mode 鉴权字段
//   - obsGrafanaDsCandidates 按 obsKey 类型过滤 ds 列表(loki/prometheus/jaeger/tempo/elk 各自需要的 type)
//   - loadLokiDatasources  调 listGrafanaDatasources 拉 ds 列表 + 自动给四类填默认 UID
//   - scheduleGrafanaDsAutoload Grafana URL+鉴权填好 800ms 防抖自动 reload
//
// crossCheckImportedObservability(导入 yaml 后跨真实 grafana 校验 UID)还跟 environments /
// scheduleObsProbe / k8sRuntimeEnvLoc / sourceCreds 多块状态交织,留在 InitPage。
import { reactive } from 'vue'
import { listGrafanaDatasources, type GrafanaDatasource } from './bridge'
import { isDesktop } from './bridge/shared'
import { pushLog } from './logStore'
import type { LokiMappingPerEnv } from './useLokiMappingState'

// obsKey → grafana datasource.type 的允许值(可多个,如 elk 需要 elasticsearch)。
// yamlGenerator / useGrafanaDS 都用,集中放这里;新增 obs 工具改这一处。
export const OBS_GRAFANA_DS_TYPES: Record<string, string[]> = {
  loki: ['loki'],
  prometheus: ['prometheus'],
  jaeger: ['jaeger'],
  tempo: ['tempo'],
  elk: ['elasticsearch'],
}

/** "<obsKey>:<envID>" key 拼接;reactive map 写读两侧一定要走这函数,字符串漂移 = UI 默默坏掉。 */
export function obsGrafanaDsKey(obsKey: string, envID: string): string {
  return `${obsKey}:${envID}`
}

export interface UseGrafanaDSDeps {
  /** Step 7 各 obs tool 输入 map */
  toolInputs: Record<string, string>
  /** 拼 (cat, toolKey, envID, fieldKey) → toolInputs key 的函数 */
  toolKeyFor: (cat: 'obs' | 'ds', toolKey: string, envID: string, fieldKey: string) => string
  /** Step 7 顶部勾选 map;判 grafana 是否启用决定是否自动 reload */
  enabledObservability: Record<string, boolean>
  /** useLokiMappingState 暴露的 getter,本 composable lokiAuthFor 要用 lm.dsUID */
  getLokiMapping: (envID: string) => LokiMappingPerEnv
  /** useLokiMappingState 暴露的 reactive,obsGrafanaDsCandidates 用 lm.dsList 过滤 */
  lokiMappingByEnv: Record<string, LokiMappingPerEnv>
}

export function useGrafanaDS(deps: UseGrafanaDSDeps) {
  // 通过 Grafana 代理访问的可观测性组件(prometheus/jaeger/tempo/elk)在每个 env 下
  // 对应的 Grafana datasource UID。Loki 走 lokiMappingByEnv[env].dsUID(因为还要拉 labels);
  // 其他类型只需选 UID,所以放进这个扁平 map。key="<obsType>:<env>"。
  // dsList(候选下拉项)继续复用 lokiMappingByEnv[env].dsList,各 obsType 按 .type 字段过滤。
  // 实例化时由 InitPage 传 saved.grafanaDsUidByObsEnv 反填。
  const grafanaDsUidByObsEnv = reactive<Record<string, string>>({})

  // Grafana URL+鉴权填好后自动拉一次 datasources(800ms 防抖)。
  // 用户改 URL/Key/账号 → 等输入稳定 → 自动重新加载,不必手动点"加载"。
  const grafanaDsAutoTimers: Record<string, ReturnType<typeof setTimeout>> = {}

  function lokiAuthFor(envID: string) {
    const lm = deps.getLokiMapping(envID)
    // auth_mode 是 UI-only 选择(api_key 或 username_password);按它过滤掉对侧的残留值,
    // 避免 stale draft 把 api_key 跟 user/pass 一起发给后端,后端按 apiKey 优先走 Bearer
    // → 用错的 token 401。空 auth_mode 兜底走 options[0] = api_key(跟 CredentialField 视觉一致)。
    const authMode = (deps.toolInputs[deps.toolKeyFor('obs', 'grafana', envID, 'auth_mode')] || 'api_key').trim()
    const useApiKey = authMode === 'api_key'
    return {
      grafana_url: (deps.toolInputs[deps.toolKeyFor('obs', 'grafana', envID, 'url')] || '').trim(),
      api_key: useApiKey ? (deps.toolInputs[deps.toolKeyFor('obs', 'grafana', envID, 'api_key')] || '') : '',
      user: useApiKey ? '' : (deps.toolInputs[deps.toolKeyFor('obs', 'grafana', envID, 'user')] || '').trim(),
      pass: useApiKey ? '' : (deps.toolInputs[deps.toolKeyFor('obs', 'grafana', envID, 'pass')] || ''),
      loki_url: (deps.toolInputs[deps.toolKeyFor('obs', 'loki', envID, 'url')] || '').trim(),
      ds_uid: lm.dsUID,
    }
  }

  function obsGrafanaDsCandidates(envID: string, obsKey: string): GrafanaDatasource[] {
    const lm = deps.lokiMappingByEnv[envID]
    if (!lm || lm.dsList.length === 0) return []
    const types = OBS_GRAFANA_DS_TYPES[obsKey] || []
    return lm.dsList.filter(d => types.includes(d.type))
  }

  async function loadLokiDatasources(envID: string) {
    const lm = deps.getLokiMapping(envID)
    lm.dsListStatus = 'loading'
    lm.dsListError = undefined
    try {
      const auth = lokiAuthFor(envID)
      if (!auth.grafana_url) throw new Error('请先填本环境 Grafana URL')
      const list = await listGrafanaDatasources(auth)
      lm.dsList = list
      lm.dsListStatus = 'ok'
      if (!lm.dsUID) {
        const loki = list.find(d => d.is_loki)
        if (loki) lm.dsUID = loki.uid
      }
      // 自动给 prometheus / jaeger / tempo / elk 也填默认 datasource:每种 type 取第一个匹配的
      for (const obsKey of ['prometheus', 'jaeger', 'tempo', 'elk']) {
        const k = obsGrafanaDsKey(obsKey, envID)
        if (grafanaDsUidByObsEnv[k]) continue // 用户填过则不动
        const candidates = list.filter(d => (OBS_GRAFANA_DS_TYPES[obsKey] || []).includes(d.type))
        const def = candidates.find(d => d.default) || candidates[0]
        if (def) grafanaDsUidByObsEnv[k] = def.uid
      }
      const counts: Record<string, number> = {}
      for (const d of list) counts[d.type] = (counts[d.type] || 0) + 1
      const summary = Object.entries(counts).map(([t, n]) => `${t}=${n}`).join(', ')
      pushLog('cchub', 'info', `[${envID}] Grafana 列到 ${list.length} 个 datasource(${summary})`, { envID })
    } catch (e: any) {
      lm.dsListStatus = 'fail'
      lm.dsListError = String(e?.message || e)
      pushLog('cchub', 'error', `[${envID}] 列 Grafana datasource 失败: ${lm.dsListError}`, { envID })
    }
  }

  function scheduleGrafanaDsAutoload(envID: string) {
    if (!isDesktop()) return
    if (!deps.enabledObservability['grafana']) return
    const auth = lokiAuthFor(envID)
    if (!auth.grafana_url) return
    if (!auth.api_key && (!auth.user || !auth.pass)) return
    const k = `gads:${envID}`
    if (grafanaDsAutoTimers[k]) clearTimeout(grafanaDsAutoTimers[k])
    grafanaDsAutoTimers[k] = setTimeout(() => loadLokiDatasources(envID), 800)
  }

  return {
    grafanaDsUidByObsEnv,
    lokiAuthFor,
    obsGrafanaDsCandidates,
    loadLokiDatasources,
    scheduleGrafanaDsAutoload,
  }
}
