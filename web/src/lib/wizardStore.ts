// wizardStore.ts —— InitPage 共享给各 Step 子组件(ConfigSourceStep / ObservabilityStep /
// DataStoreStep 等)的"通用上下文"。用 provide/inject 把高频共用的 reactive 引用 + helper
// 一次性透下去,Step 子组件不再每个 prop 单独透传,InitPage 模板调用从"30+ 行 props"
// 缩到 step 专属那几个。
//
// 没放进来的:Step 专属字段(如 ccFieldsByType / configTypeOptions / enabledObservability)
// 还是各自做 prop —— 只把 2+ Step 都用的进来,避免把 store 撑成"InitPage v2"。

import type { InjectionKey } from 'vue'
import type { KuboardResourceState } from './credFields'

interface Environment { id: string; is_prod: boolean }

export interface One2AllClusterEntry {
  name: string
  cluster_id: string
  namespaces: { name: string; configmaps: string[] }[]
}

export type One2AllResourceState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'ok'; clusters: One2AllClusterEntry[]; notes?: string[] }
  | { status: 'error'; error: string }

export interface WizardStore {
  // 高频 reactive 数据(2+ Step 共用)
  environments: Environment[]
  allServiceNames: string[]
  kuboardStateByEnv: Record<string, KuboardResourceState | undefined>
  one2allStateByEnv: Record<string, One2AllResourceState | undefined>

  // 通用 helper
  hasError: (key: string) => boolean
  svcKey: (envID: string, svc: string) => string
  isRevealed: (k: string) => boolean
  toggleReveal: (k: string) => void

  // kuboard 状态窄化(消除 (state as any).clusters.X 的散点)
  kuboardClustersOf: (envID: string) => Array<{ name: string; namespaces: Array<{ name: string; configmaps: string[] }> }>
  kuboardClusterCountOf: (envID: string) => number
  kuboardErrorOf: (envID: string) => string
  kuboardNamespacesFor: (envID: string, clusterName: string) => string[]
  kuboardConfigMapsFor: (envID: string, clusterName: string, nsName: string) => string[]

  // one2all 状态窄化
  one2allClustersOf: (envID: string) => One2AllClusterEntry[]
  one2allClusterCountOf: (envID: string) => number
  one2allErrorOf: (envID: string) => string
  one2allNamespacesFor: (envID: string, clusterID: string) => string[]
  one2allConfigMapsFor: (envID: string, clusterID: string, ns: string) => string[]
}

export const WizardStoreKey: InjectionKey<WizardStore> = Symbol('WizardStore')
