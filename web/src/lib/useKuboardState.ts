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
  savedKuboardState?: Record<string, KuboardResourceState> | null
  /** 大 draft blob 里的 kuboardStateByEnv 拷贝(fallback) */
  draftKuboardState?: Record<string, KuboardResourceState>
}) {
  const kuboardStateByEnv = reactive<Record<string, KuboardResourceState>>(
    (() => {
      const out: Record<string, KuboardResourceState> = {}
      const src = initial.savedKuboardState ?? initial.draftKuboardState
      if (src && typeof src === 'object') {
        for (const [k, v] of Object.entries(src)) {
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
  //
  // **增量 merge,不全量覆盖**:从 localStorage 读旧值,跟 reactive 合并 ——
  //   - reactive ok 项 → 写新值
  //   - reactive 缺失但 localStorage 有的 env → 保留旧值
  // 否则 reactive 里某 env 因"清孤儿 watcher / runK8sRtPreload 中途 loading 态 / saved init
  // 没反填(老 draft 里是 loading/error 被 init 跳过)"等原因不在 reactive 时,
  // 全量覆盖会把那个 env 从 localStorage 永久抹掉,用户上次填的 cluster/ns/cm 凭空消失。
  // (踩过这个坑:test 重新读取一次 → test2 的 ok 状态被覆盖丢失。)
  function persistKuboardState() {
    try {
      const merged: Record<string, KuboardResourceState> = {}
      // 1. 先把 localStorage 旧的 ok 项捞回来作为底
      try {
        const raw = localStorage.getItem(INIT_KUBOARD_STATE_KEY)
        if (raw) {
          const old = JSON.parse(raw) as Record<string, KuboardResourceState>
          if (old && typeof old === 'object') {
            for (const [k, v] of Object.entries(old)) {
              if (v && v.status === 'ok' && Array.isArray(v.clusters)) merged[k] = v
            }
          }
        }
      } catch { /* 旧值损坏忽略,从 reactive 重建 */ }
      // 2. reactive ok 项覆盖底(以 reactive 为准 — 用户刚拉到的最新)
      for (const [k, v] of Object.entries(kuboardStateByEnv)) {
        if (v && v.status === 'ok') merged[k] = v
      }
      if (Object.keys(merged).length > 0) {
        localStorage.setItem(INIT_KUBOARD_STATE_KEY, JSON.stringify(merged))
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
