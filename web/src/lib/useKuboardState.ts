// useKuboardState —— Kuboard 集群+namespace+configmap 三级树的 per-env 缓存 + 5 个读 helper。
//
// 写侧(runKuboardPreload / runK8sRtPreload 两个 runner)还跟 sourceCreds / toolInputs /
// k8sRuntimeEnvLoc / autoPickK8sRtWorkloads 多块状态交织,留在 InitPage —— 它们直接
// mutate 本 composable 暴露的 kuboardStateByEnv 并显式调用 persistKuboardState。
//
// 跨会话恢复:优先吃独立的 KUBOARD_STATE_KEY(savedKuboardState),fallback 到大 draft
// blob 里的 kuboardStateByEnv 拷贝。只恢复 status==='ok' 的;loading/error 状态对历史无意义。
// status 改变时 runner 立即调 persistKuboardState 落盘,不依赖大 draft watch
// (它可能因 quota 或排程而错过这次写入)。

import { reactive } from 'vue'
import type { KuboardResourceState } from './credFields'
import { INIT_KUBOARD_STATE_KEY } from './useWizardDraft'

export function useKuboardState(initial: {
  /** 来自 INIT_KUBOARD_STATE_KEY 的独立缓存(优先) */
  savedKuboardState?: any
  /** 大 draft blob 里的 kuboardStateByEnv 拷贝(fallback) */
  draftKuboardState?: any
}) {
  const kuboardStateByEnv = reactive<Record<string, KuboardResourceState>>(
    (() => {
      const out: Record<string, KuboardResourceState> = {}
      const src = initial.savedKuboardState ?? initial.draftKuboardState
      if (src && typeof src === 'object') {
        for (const [k, v] of Object.entries(src as Record<string, any>)) {
          if (v && v.status === 'ok' && Array.isArray(v.clusters)) {
            out[k] = { status: 'ok', clusters: v.clusters, notes: v.notes }
          }
        }
      }
      return out
    })(),
  )

  // 只保存 ok 状态;loading/error 不持久化。每次 status 改变时立即同步写入,
  // 不依赖大 draft watch(它可能因 quota 或排程而错过这次写入)。
  function persistKuboardState() {
    try {
      const out: Record<string, KuboardResourceState> = {}
      for (const [k, v] of Object.entries(kuboardStateByEnv)) {
        if (v && v.status === 'ok') out[k] = v
      }
      if (Object.keys(out).length > 0) {
        localStorage.setItem(INIT_KUBOARD_STATE_KEY, JSON.stringify(out))
      } else {
        localStorage.removeItem(INIT_KUBOARD_STATE_KEY)
      }
    } catch {
      // quota 失败 silent skip
    }
  }

  // 模板用的窄化 helper:跳过 status union narrowing 的 (state as any) 强转,统一一个出口
  function kuboardClustersOf(envID: string) {
    const st = kuboardStateByEnv[envID]
    return (st && st.status === 'ok') ? st.clusters : []
  }
  function kuboardClusterCountOf(envID: string): number {
    return kuboardClustersOf(envID).length
  }
  function kuboardErrorOf(envID: string): string {
    const st = kuboardStateByEnv[envID]
    return (st && st.status === 'error') ? st.error.slice(0, 60) : ''
  }

  // 取当前 env 下,某 cluster 的 namespace 列表(级联下拉用)。
  // clusterName 由调用方从所在 form 的 state 读出来传入(主源走 ccCredInputs / 副源走 sourceCreds)。
  function kuboardNamespacesFor(envID: string, clusterName: string): string[] {
    const st = kuboardStateByEnv[envID]
    if (!st || st.status !== 'ok') return []
    const c = st.clusters.find(c => c.name === clusterName)
    return c ? c.namespaces.map(n => n.name) : []
  }

  // 取当前 env 下,某 (cluster, namespace) 的 configmap 列表
  function kuboardConfigMapsFor(envID: string, clusterName: string, nsName: string): string[] {
    const st = kuboardStateByEnv[envID]
    if (!st || st.status !== 'ok') return []
    const cluster = st.clusters.find(cl => cl.name === clusterName)
    if (!cluster) return []
    const ns = cluster.namespaces.find(n => n.name === nsName)
    return ns ? ns.configmaps : []
  }

  return {
    kuboardStateByEnv,
    persistKuboardState,
    kuboardClustersOf,
    kuboardClusterCountOf,
    kuboardErrorOf,
    kuboardNamespacesFor,
    kuboardConfigMapsFor,
  }
}
