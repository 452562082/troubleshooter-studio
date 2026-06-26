// useWizardDraft —— InitPage 草稿持久化的 localStorage 读写助手 + WizardDraft 类型。
//
// 两个独立 key:
//   - INIT_WIZARD_KEY   主 draft blob(system / agent / repos / cred 表都在里头)
//   - KUBOARD_STATE_KEY Kuboard 资源树缓存独立存,大 draft 经常因 quota 静默失败时,
//                       这层 fallback 让 kuboard 数据不会被波及;即使主 draft 没存上,
//                       只要这个 key 存了,下次进来下拉 options 仍可用。
//
// WizardDraft 是 InitPage saved.* 字段的"反序列化后形态"权威类型 —— 集中一处,所有字段
// 读取的 typo 在 tsc 阶段就能拦下来。所有字段都标 optional,因为:
//   1. 老版本 draft 可能少字段(向下兼容)
//   2. localStorage.getItem 失败 / JSON.parse 失败时整个返回 null,字段访问要先判 saved
//   3. 用户清掉草稿后字段全空
//
// 写侧(auto-save watch)还跟 InitPage 里 30+ 个 reactive 字段交织,留在原地。

import type { CCHubEnvState } from './useCCHubState'
import type { KuboardResourceState } from './credFields'
import type { LokiMappingPerEnv } from './useLokiMappingState'
import type { DSByService, DSScanState } from './useDataStoreState'
import type { K8sRtWorkloadState } from './useK8sRtWorkloads'
import type { RepoScanItem } from './useRepoScan'
import type { ObsAccessMode } from './useObsAccessMode'

export const INIT_WIZARD_KEY = 'tsf-init-wizard-v1'
export const INIT_KUBOARD_STATE_KEY = 'tsf-init-wizard-kuboard-state-v1'

// ── 反序列化后保存的子结构,跟 InitPage 内联类型保持结构兼容 ──

/** Step 3 一条环境记录(EnvItem 子集,反填后 Step 3 自己保留可选字段) */
export interface DraftEnvItem {
  id: string
  api_domain: string
  web_domain: string
  is_prod: boolean
}

/** Step 1 系统基本信息 */
export interface DraftSystem {
  id: string
  name: string
  description: string
}

/** Step 2 机器人身份(target_models 是 wizard schema v2 起的字段) */
export interface DraftAgent {
  id?: string
  name?: string
  workspace_name?: string
  model?: string
  target_models?: Record<string, string>
}

/** Step 5 kuboard 副源:每 (env, svc) 选的 cluster/namespace/configmap 三联 */
export type DraftKuboardSvcLocator = { cluster: string; namespace: string; configmap: string }

/** one2all 配置源 per-service 映射:cluster_id + namespace + configmap */
export type DraftOne2AllSvcLocator = { cluster_id: string; namespace: string; configmap: string }

/** Step 7 k8s_runtime per-env cluster/cluster_id + namespace */
export type DraftK8sRuntimeEnvLocator = { cluster: string; cluster_id: string; namespace: string }

/** Step 7 k8s_runtime per-(env,svc) workload + label_selector */
export type DraftK8sRuntimeSvcLocator = { workload: string; label_selector: string }

/** Step 5 sourceCreds 单源 schema:env → field → value + 可选 rawExtra(yaml 反填残留) */
export type DraftSourceCredsEntry = {
  creds: Record<string, Record<string, string>>
  rawExtra?: Record<string, unknown>
}

/**
 * WizardDraft —— InitPage saved.* 字段的权威反序列化形态。
 *
 * 所有字段 optional;读时按 `saved?.field ?? default` 兜底。新增字段时:
 *   1. 这里加类型(强制 InitPage 同步用上,否则 tsc 报)
 *   2. InitPage 在 auto-save watch 的 tracked 对象里也加进去(不加则不持久化)
 *   3. 老 draft 没此字段 → fallback 到默认值,新加字段不阻塞老用户
 */
export interface WizardDraft {
  // ── 元字段 ──
  /** wizardSchema 版本:v2 起 step 1 是欢迎页,老 saved(无字段)的 currentStep 需 +1 迁移 */
  wizardSchema?: number
  /** 当前 step;启动时按 wizardSchema 决定要不要 +1 */
  currentStep?: number
  /** Step 1 用户是否手动改过 system.id(改过后 system.name 变化不再覆盖 id) */
  idManualOverride?: boolean

  // ── Step 1 / 2 / 3 ──
  system?: DraftSystem
  agent?: DraftAgent
  /** target_models 写到这里也合法(老 schema);新走 agent.target_models */
  targetModels?: Record<string, string>
  environments?: DraftEnvItem[]

  // ── Step 4 仓库 ──
  /** RepoScanItem 包含所有 _xxx UI 态字段;反填回来仍要继续用 */
  repos?: RepoScanItem[]
  /** repoName → 真实分支列表(saved 持久化让重开向导时 select 仍有 options) */
  repoBranchesMap?: Record<string, string[]>

  // ── Step 5 配置源(多源 schema)──
  /** 老 single-source 字段;新 schema 走 enabledSourceTypes,但保留兼容老 draft */
  configCenterType?: string
  /** 多源:顶部勾哪些源(nacos / apollo / consul / kuboard / env-vars / none) */
  enabledSourceTypes?: Record<string, boolean>
  /** 多源排序(主源在 [0]) */
  enabledSourceOrder?: string[]
  /** 多源:每源每 env 的字段值 + rawExtra */
  sourceCreds?: Record<string, DraftSourceCredsEntry>
  /** 服务 → 源 type 映射(显式 '' 表示用户取消,getServiceSource 不再 fallback 主源) */
  serviceSourceMap?: Record<string, string>
  /**
   * 老 single-source 凭证表;新 schema 走 sourceCreds 但保留迁移路径:
   * `if (saved?.ccCredInputs && !saved?.sourceCreds)` 时把老 draft 灌进新 sourceCreds。
   */
  ccCredInputs?: Record<string, string>
  /** Step 5 用户挑的 env → namespace */
  envNamespaces?: Record<string, string>
  /** Step 5 用户挑的 service → dataId */
  serviceConfigSel?: Record<string, string>
  /** Step 5 dataId 对应的 group(yaml 生成时一起写) */
  serviceConfigGroup?: Record<string, string>
  /** Step 5 kuboard 副源:(env, svc) → cluster/namespace/configmap 三联 */
  kuboardSvcMap?: Record<string, DraftKuboardSvcLocator>
  /** Step 5 one2all 配置:(env, svc) → cluster_id/namespace/configmap */
  one2allSvcMap?: Record<string, DraftOne2AllSvcLocator>
  /** Step 5 配置中心预加载结果(entries + namespaces + notes),跨会话恢复下拉 options */
  ccHubStateByEnv?: Record<string, CCHubEnvState>
  /** Step 5 kuboard 资源树缓存;独立 key 兜底,但主 draft 也保留(双写让 quota 失败时仍有兜底) */
  kuboardStateByEnv?: Record<string, KuboardResourceState>

  // ── Step 7 可观测性 + 数据层 ──
  /** Step 7 k8s_runtime:env → cluster/namespace */
  k8sRuntimeEnvLoc?: Record<string, DraftK8sRuntimeEnvLocator>
  /** Step 7 k8s_runtime:(env,svc) → workload/label_selector */
  k8sRuntimeSvcMap?: Record<string, DraftK8sRuntimeSvcLocator>
  /** Step 7 k8s_runtime per-(env, cluster, namespace) deployments 列表缓存 */
  k8sRtWorkloadCache?: Record<string, K8sRtWorkloadState>
  /** Step 7 各 obs 工具(prometheus/jaeger/tempo/elk)在每 env 选中的 Grafana datasource UID */
  grafanaDsUidByObsEnv?: Record<string, string>
  /** Step 7 (obs, env) 显式选择的访问方式 via_grafana / direct */
  obsAccessModeMap?: Record<string, ObsAccessMode>
  /** Step 7 Loki 标签映射(per-env:datasource UID + labels + values + 选中) */
  lokiMappingByEnv?: Record<string, LokiMappingPerEnv>
  /** Step 7 obs/ds 工具字段输入值(含 secret) */
  toolInputs?: Record<string, string>
  /** Step 7 obs 工具勾选状态 */
  enabledObservability?: Record<string, boolean>
  /** Step 7 数据层勾选状态(scannedDS 反推) */
  enabledDataStores?: Record<string, boolean>
  /** Step 7 "从配置中心读取" 标记(重进后徽章仍显示) */
  dsAutoFilled?: Record<string, boolean>
  /** Step 7 每个服务识别出的数据层配置(env → service → dsKey → fields) */
  scannedDS?: Record<string, DSByService>
  /** Step 7 每个 (env, service) 的扫描状态(ok/empty/skipped/error) */
  dsScanState?: Record<string, DSScanState>

  // ── Step 2 / 9 / 10 部署目标 + 安装根目录 ──
  /** Step 2 勾哪些部署 target */
  enabledTargets?: Record<string, boolean>
  /** Step 2 OpenClaw 自定义安装目录(覆盖 ~/.openclaw 探测路径) */
  openclawInstallDir?: string

  // ── Step 10 部署后回写 ──
  /** runOneClickDeploy 部署成功时间戳;HomePage 用来判定"已部署"vs"继续部署" */
  lastDeployAt?: number
  /** 上次部署成功的 target 列表 */
  lastDeployedTargets?: string[]
}

export function loadInitWizardDraft(): WizardDraft | null {
  try {
    const raw = localStorage.getItem(INIT_WIZARD_KEY)
    return raw ? (JSON.parse(raw) as WizardDraft) : null
  } catch {
    return null
  }
}

export function loadInitKuboardState(): Record<string, KuboardResourceState> | null {
  try {
    const raw = localStorage.getItem(INIT_KUBOARD_STATE_KEY)
    return raw ? (JSON.parse(raw) as Record<string, KuboardResourceState>) : null
  } catch {
    return null
  }
}
