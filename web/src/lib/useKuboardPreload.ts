// useKuboardPreload —— Step 5 Kuboard 资源拉取 + 服务三联(cluster/namespace/configmap)启发式自动匹。
//
// 接 useKuboardState(state)。包含:
//   - runKuboardPreloadFromSource  从 sourceCreds[sourceType] 抽凭证调 kuboardListResources,
//                                   写 kuboardStateByEnv,自动跑 autoFillKuboardSelections
//   - runKuboardPreload            主源版,固定从 sourceCreds['kuboard'] 读
//   - autoMatchKuboardLocation     给某 (env, svc) 找最匹配的三联(serviceMatchKeys 退化 + 3-pass)
//   - autoFillKuboardSelections    遍历该 env 所有 kuboard 源服务,填空三联(用户已挑的不覆盖)
//
// runK8sRtPreload 跟可观测性 toolInputs / k8sRuntimeEnvLoc 多块状态交织,留在 InitPage 直接
// mutate 暴露的 kuboardStateByEnv + 调用 autoFillKuboardSelections。
import { kuboardListResources } from './bridge'
import { isDesktop } from './bridge/shared'
import { pushLog } from './logStore'
import { toast } from './toast'
import { svcKey } from './yamlShared'
import { serviceMatchKeys, startsAtBoundary } from './serviceMatchHelpers'
import type { KuboardResourceState } from './credFields'

interface KuboardSvcLocator {
  cluster?: string
  namespace?: string
  configmap?: string
}

export interface UseKuboardPreloadDeps {
  /** useKuboardState 暴露的 reactive */
  kuboardStateByEnv: Record<string, KuboardResourceState>
  /** persistKuboardState — 写完立即落盘,不等大 draft watch */
  persistKuboardState: () => void
  /** Step 5 各源的 creds 容器,runKuboardPreloadFromSource 按 sourceType 取 */
  sourceCreds: Record<string, { creds: Record<string, Record<string, string>>; rawExtra?: Record<string, unknown> }>
  /** Step 5 用户挑的服务 → 三联 map */
  kuboardSvcMap: Record<string, KuboardSvcLocator>
  /** 当前所有服务名(computed.value) */
  allServiceNames: { value: readonly string[] }
  /** 取服务对应的源 type;multi-source 下区分主源/副源 */
  getServiceSource: (svc: string) => string
}

export function useKuboardPreload(deps: UseKuboardPreloadDeps) {
  // 给某 (env, svc) 找最匹配的 cluster/namespace/configmap 三联。
  // serviceMatchKeys 退化候选 + startsAtBoundary 段对齐 + 3-pass(env-aware → bare → fuzzy)。
  // 返回 null 表示没找到,UI 保持空让用户手挑。
  function autoMatchKuboardLocation(envID: string, svc: string): { cluster: string, namespace: string, configmap: string } | null {
    const state = deps.kuboardStateByEnv[envID]
    if (!state || state.status !== 'ok') return null
    const candidates = serviceMatchKeys(svc)
    const envLower = envID.toLowerCase()
    // 把所有 cluster→namespace→configmap 三联拍平,方便扫描;按出现顺序保留(首个命中赢)。
    const flat: Array<{ cluster: string, namespace: string, configmap: string }> = []
    for (const c of state.clusters) {
      for (const n of c.namespaces) {
        for (const cm of n.configmaps) {
          flat.push({ cluster: c.name, namespace: n.name, configmap: cm })
        }
      }
    }
    if (flat.length === 0) return null
    // Pass 1:configmap 段对齐前缀 + (configmap 含 env 或 namespace 含 env)—— 最强信号
    for (const cand of candidates) {
      const hit = flat.find(x => {
        const cmL = x.configmap.toLowerCase()
        return startsAtBoundary(cmL, cand) && (cmL.includes(envLower) || x.namespace.toLowerCase().includes(envLower))
      })
      if (hit) return hit
    }
    // Pass 2:configmap 段对齐前缀(不要求含 env)—— 跨集群共享或 env 体现在 namespace
    for (const cand of candidates) {
      const hit = flat.find(x => startsAtBoundary(x.configmap.toLowerCase(), cand))
      if (hit) return hit
    }
    // Pass 3:fuzzy 兜底(完整服务名 substring)
    const svcLower = svc.toLowerCase()
    let hit = flat.find(x => {
      const cmL = x.configmap.toLowerCase()
      return cmL.includes(svcLower) && (cmL.includes(envLower) || x.namespace.toLowerCase().includes(envLower))
    })
    if (!hit) hit = flat.find(x => x.configmap.toLowerCase().includes(svcLower))
    return hit || null
  }

  // 给某个 env 的所有"以 kuboard 为源的服务"自动填三联映射。
  // sourceType 决定从哪条服务源筛(主源走 configCenterType,副源走传入值,如 'kuboard')。
  // 行为跟 useCCHubPreload.autoFillSelections 对齐:已有用户选择的格子不覆盖,只填空的。
  function autoFillKuboardSelections(envID: string, sourceType: string = 'kuboard') {
    const state = deps.kuboardStateByEnv[envID]
    if (!state || state.status !== 'ok') return
    for (const svc of deps.allServiceNames.value) {
      if (deps.getServiceSource(svc) !== sourceType) continue
      const k = svcKey(envID, svc)
      const cur = deps.kuboardSvcMap[k]
      if (cur && cur.cluster && cur.namespace && cur.configmap) continue // 三联齐了 → 不动
      const hit = autoMatchKuboardLocation(envID, svc)
      if (!hit) continue
      if (!cur) {
        deps.kuboardSvcMap[k] = { cluster: hit.cluster, namespace: hit.namespace, configmap: hit.configmap }
      } else {
        // 部分填:只补空字段,保留用户已挑的(如已选 cluster 想换 namespace)
        if (!cur.cluster) cur.cluster = hit.cluster
        if (!cur.namespace) cur.namespace = hit.namespace
        if (!cur.configmap) cur.configmap = hit.configmap
      }
    }
  }

  async function runKuboardPreloadFromSource(sourceType: string, envID: string) {
    if (!isDesktop()) {
      toast.error('Kuboard 拉取只在桌面 app 可用')
      return
    }
    const data = deps.sourceCreds[sourceType]
    if (!data) return
    const envCreds = data.creds[envID] || {}
    const url = (envCreds.url || '').trim()
    const accessKey = (envCreds.access_key || '').trim()
    const username = (envCreds.username || '').trim()
    const password = (envCreds.password || '').trim()
    if (!url) {
      toast.error(`${envID}: 先填 Kuboard URL`)
      return
    }
    if (!accessKey && (!username || !password)) {
      toast.error(`${envID}: 鉴权填 API 访问凭证(优先),或 用户名+密码`)
      return
    }
    deps.kuboardStateByEnv[envID] = { status: 'loading' }
    try {
      const res = await kuboardListResources(url, username, password, accessKey)
      const clusters = (res.clusters || []).map(c => ({
        name: c.name,
        namespaces: (c.namespaces || []).map(n => ({
          name: n.name,
          configmaps: n.configmaps || [],
        })),
      }))
      deps.kuboardStateByEnv[envID] = { status: 'ok', clusters, notes: res.notes }
      deps.persistKuboardState() // 立即落盘,不等大 draft watch
      if (clusters.length === 0) {
        toast.info(`${envID}: 没拉到集群,看看账号在 Kuboard 里的权限`)
      } else {
        // 顺手给本 env 下走 kuboard 源(主或副)的服务跑一次 auto-match,把 cluster/namespace/configmap
        // 三级下拉自动填上 —— 跟 nacos autoFillSelections 行为对齐,免得用户每个服务手挑 3 次。
        // 主源 vs 副源:这条入口走的是 sourceType,直接传它就行。
        autoFillKuboardSelections(envID, sourceType)
        toast.success(`${envID}: 拉到 ${clusters.length} 个集群`)
      }
    } catch (e: any) {
      const msg = String(e?.message || e)
      deps.kuboardStateByEnv[envID] = { status: 'error', error: msg }
      pushLog('cchub', 'error', `[${envID}] kuboard 拉取失败: ${msg}`, { envID })
      toast.error(`${envID} kuboard 拉取失败: ${msg.slice(0, 80)}`)
    }
  }

  // 主源版:固定从 sourceCreds['kuboard'] 读
  async function runKuboardPreload(envID: string) {
    return runKuboardPreloadFromSource('kuboard', envID)
  }

  return {
    autoMatchKuboardLocation,
    autoFillKuboardSelections,
    runKuboardPreloadFromSource,
    runKuboardPreload,
  }
}
