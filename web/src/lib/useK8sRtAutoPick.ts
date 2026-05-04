// useK8sRtAutoPick —— Step 7 k8s_runtime:loadK8sRtWorkloads 拉到 deployment 列表后,
// 给本 env 下所有服务自动挑最匹配的 Deployment(不覆盖用户已经手动选过的)。
//
// 匹配策略(由强到弱):
//   1) deployment 名 == 服务名 / selector app=<服务名> 精确命中
//   2) 候选退化(serviceMatchKeys)+ 段对齐前缀 + env 信号双约束 —— 适配 base-svc-dev / svc-dev 这类
//   3) 候选退化 + 段对齐前缀(不含 env)
//   4) 模糊兜底:归一化后双向 substring(老行为,接非典型命名)
//
// 跟 useLokiLabels.boundaryHasAnywhere 行为对齐(允许 base-/app- 前缀),用 serviceMatchHelpers
// 共享。loadK8sRtWorkloads(在 useK8sRtWorkloads)的 onLoaded 回调直接传入本函数。
import { boundaryHasAnywhere, serviceMatchKeys } from './serviceMatchHelpers'

interface K8sRtSvcLocator {
  workload: string
  label_selector: string
}

export interface UseK8sRtAutoPickDeps {
  /** 当前所有服务名(computed.value) */
  allServiceNames: { value: readonly string[] }
  /** 取/初始化 (env, svc) 的 k8s_rt service locator */
  ensureK8sRtSvcLoc: (envID: string, svc: string) => K8sRtSvcLocator
}

export function useK8sRtAutoPick(deps: UseK8sRtAutoPickDeps) {
  function autoPickK8sRtWorkloads(envID: string, deployments: Array<{ name: string; selector: string }>) {
    if (deployments.length === 0) return
    const norm = (s: string) => s.toLowerCase().replace(/[-_]/g, '')
    const envLower = envID.toLowerCase()
    for (const svc of deps.allServiceNames.value) {
      const sloc = deps.ensureK8sRtSvcLoc(envID, svc)
      if (sloc.workload) continue // 用户已经手动选过,不动
      const svcLower = svc.toLowerCase()
      const svcNorm = norm(svc)
      const candidates = serviceMatchKeys(svc)
      let pick: { name: string; selector: string } | undefined
      // 1a) 精确同名
      pick = deployments.find(d => d.name === svc)
      // 1b) selector 标签命中(app= / app.kubernetes.io/name=)
      if (!pick) {
        pick = deployments.find(d => {
          const sel = d.selector
          if (!sel) return false
          const kvs = sel.split(',')
          for (const kv of kvs) {
            const [k, v] = kv.split('=')
            if (!k || !v) continue
            if ((k === 'app' || k === 'app.kubernetes.io/name') && v.toLowerCase() === svcLower) return true
          }
          return false
        })
      }
      // 2) 候选退化 + 边界对齐 + 含 env —— 同 nacos / loki 套路。覆盖 community-grpc-server →
      //    `community-dev` / `base-community-dev`(env 信号兜底,避免误中跨 env 同名 deployment)。
      if (!pick) {
        for (const cand of candidates) {
          const m = deployments.find(d => {
            const dl = d.name.toLowerCase()
            return boundaryHasAnywhere(dl, cand) && dl.includes(envLower)
          })
          if (m) { pick = m; break }
        }
      }
      // 3) 候选退化 + 边界对齐(不含 env)—— 命名空间已经按 env 分时(base-dev / base-prod),
      //    deployment 名常省略 env 后缀,此 pass 兜底。
      if (!pick) {
        for (const cand of candidates) {
          const m = deployments.find(d => boundaryHasAnywhere(d.name.toLowerCase(), cand))
          if (m) { pick = m; break }
        }
      }
      // 4) 模糊兜底:归一化后双向 substring(老行为,接非典型命名)
      if (!pick) pick = deployments.find(d => norm(d.name).includes(svcNorm))
      if (!pick) pick = deployments.find(d => svcNorm.includes(norm(d.name)))
      if (pick) {
        sloc.workload = pick.name
        sloc.label_selector = pick.selector || ''
      }
    }
  }

  return { autoPickK8sRtWorkloads }
}
