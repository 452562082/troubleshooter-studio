// useK8sRtWorkloads —— "(env, cluster, ns) → Deployment 列表" 三维缓存 + 加载器。
//
// 跟 useKuboardState(集群+ns+cm 三级树)平行,单独存这一份;走 bridge.kuboardListDeployments
// 返回 name + selector。持久化:跨会话保留 status='ok' 的 deployments 列表
// (switching tabs 切回时立刻有下拉,不必等 onMounted → triggerStep7Init 异步重拉)。
// loading / error 是瞬态不存。数据量每集群 namespace 通常几十条,整个 cache 几 KB 可控。
//
// autoPickK8sRtWorkloads(模糊匹配 deployment ↔ 服务名)留在 InitPage 里 —— 它要读
// allServiceNames / serviceMatchKeys / startsAtBoundary / ensureK8sRtSvcLoc 多块状态;
// loadK8sRtWorkloads 拉到列表后通过 onLoaded 回调通知调用方,由其触发 autoPick。
import { reactive } from 'vue'
import { kuboardListDeployments } from './bridge'
import { pushLog } from './logStore'

export type K8sRtWorkloadState =
  | { status: 'loading' }
  | { status: 'ok'; deployments: Array<{ name: string; selector: string }> }
  | { status: 'error'; error: string }

export interface KuboardEnvCreds {
  url?: string
  access_key?: string
  username?: string
  password?: string
}

export function useK8sRtWorkloads(deps: {
  /** 来自 saved.k8sRtWorkloadCache 的恢复值;只挂 status='ok' 的 */
  initialCache?: Record<string, K8sRtWorkloadState>
  /** Step 7 各 obs tool 输入 map;k8s_runtime 的 url/user/pass/access_key 从这取 */
  toolInputs: Record<string, string>
  /** 拼 (cat, toolKey, envID, fieldKey) → toolInputs key 的函数 */
  toolKeyFor: (cat: 'obs' | 'ds', toolKey: string, envID: string, fieldKey: string) => string
  /** Step 5 sourceCreds.kuboard?.creds[envID] 兜底;同集群 kuboard 配置源场景常见复用 */
  getKuboardCredsFor: (envID: string) => KuboardEnvCreds | undefined
  /** 拉到 deployments 后回调,InitPage 在里头跑 autoPickK8sRtWorkloads */
  onLoaded?: (envID: string, deployments: Array<{ name: string; selector: string }>) => void
}) {
  const k8sRtWorkloadCache = reactive<Record<string, K8sRtWorkloadState>>(
    (() => {
      const out: Record<string, K8sRtWorkloadState> = {}
      const src = deps.initialCache ?? {}
      for (const [k, v] of Object.entries(src)) {
        if (v && v.status === 'ok' && Array.isArray(v.deployments)) {
          out[k] = v
        }
      }
      return out
    })(),
  )

  function k8sRtWorkloadKey(envID: string, cluster: string, ns: string): string {
    return `${envID}::${cluster}::${ns}`
  }

  function k8sRtWorkloadsFor(envID: string, cluster: string, ns: string): Array<{ name: string; selector: string }> {
    const st = k8sRtWorkloadCache[k8sRtWorkloadKey(envID, cluster, ns)]
    return (st && st.status === 'ok') ? st.deployments : []
  }

  async function loadK8sRtWorkloads(envID: string, cluster: string, ns: string) {
    if (!cluster || !ns) return
    const key = k8sRtWorkloadKey(envID, cluster, ns)
    if (k8sRtWorkloadCache[key]?.status === 'loading') return
    // 凭证优先吃 obs k8s_runtime 自己填的,fallback 用 kuboard 配置源的(同集群常见复用)
    const fallback = deps.getKuboardCredsFor(envID) || {}
    const url = (deps.toolInputs[deps.toolKeyFor('obs', 'k8s_runtime', envID, 'url')] || '').trim() ||
                (fallback.url || '').trim()
    const accessKey = (deps.toolInputs[deps.toolKeyFor('obs', 'k8s_runtime', envID, 'access_key')] || '').trim() ||
                      (fallback.access_key || '').trim()
    const username = (deps.toolInputs[deps.toolKeyFor('obs', 'k8s_runtime', envID, 'username')] || '').trim() ||
                     (fallback.username || '').trim()
    const password = (deps.toolInputs[deps.toolKeyFor('obs', 'k8s_runtime', envID, 'password')] || '').trim() ||
                     (fallback.password || '').trim()
    if (!url || (!accessKey && (!username || !password))) {
      k8sRtWorkloadCache[key] = { status: 'error', error: '缺 URL 或鉴权信息' }
      return
    }
    k8sRtWorkloadCache[key] = { status: 'loading' }
    pushLog('cchub', 'info', `[${envID}] k8s_runtime 拉 deployments: cluster=${cluster}, ns=${ns}`, { envID })
    try {
      const list = await kuboardListDeployments({
        url, access_key: accessKey, username, password, cluster, namespace: ns,
      })
      const deployments = list.map(d => ({ name: d.name, selector: d.selector || '' }))
      k8sRtWorkloadCache[key] = { status: 'ok', deployments }
      if (list.length === 0) {
        pushLog('cchub', 'warn',
          `[${envID}] k8s_runtime: ns=${ns} 下 Deployment 数为 0(选错 ns?账号 RBAC 权限够 list deployments?)`,
          { envID })
      } else {
        pushLog('cchub', 'info', `[${envID}] k8s_runtime: 拉到 ${list.length} 个 Deployment`, { envID })
        // 自动给每个服务挑最匹配的 deployment(只在用户没手动选过时填,不覆盖已有选择)
        deps.onLoaded?.(envID, deployments)
      }
    } catch (e: any) {
      const msg = String(e?.message || e)
      k8sRtWorkloadCache[key] = { status: 'error', error: msg }
      pushLog('cchub', 'error', `[${envID}] k8s_runtime 列 deployments 失败: ${msg}`, { envID })
    }
  }

  return { k8sRtWorkloadCache, k8sRtWorkloadKey, k8sRtWorkloadsFor, loadK8sRtWorkloads }
}
