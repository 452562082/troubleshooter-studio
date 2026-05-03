// useCCHubState —— 配置中心(nacos / apollo / consul)真实 preload 出来的"per-env 命名空间 +
// 配置条目"缓存 + 跨会话恢复。
//
// 状态按 env 分开:正在扫 loading / 扫完 entries / 扫失败 error —— 同时扫多 env 互不干扰。
//
// 持久化:跨会话保留已成功扫过的 env 的 entries + namespaces + notes(恢复 UI 下拉 / 服务映射);
// loading / error 等瞬态不存 —— 重进应该从 idle 开始,loading 是"还在拉"的状态。
//
// 写侧(runCCHubPreload / autoMatchNamespace / autoMatchDataID 等)还跟 sourceCreds /
// allServiceNames / serviceMatchKeys 多块状态交织,留在 InitPage,直接 mutate 本 composable
// 暴露的 ccHubStateByEnv。
import { reactive } from 'vue'
import type { CCHubEntry, CCHubNamespace } from './bridge'

export interface CCHubEnvState {
  status: 'idle' | 'loading' | 'ok' | 'error'
  entries?: CCHubEntry[]
  namespaces?: CCHubNamespace[]   // nacos / apollo 返回的 namespace 列表,用户用它下拉挑
  notes?: string[]
  error?: string
  loadedAt?: number // 记录时间戳给 UI 显示"N 秒前拉的"
  // synthesized=true 标识本条不是从真实 HTTP preload 拉的,而是 applyImport 时
  // 用 yaml service_map 合成的"虚假 ok"。auto-preload 用它判断是否仍需调真实 nacos
  // 做交叉校验(防止 yaml 写的 namespace/dataId 在真实 nacos 不存在却看起来"已选中")。
  synthesized?: boolean
}

export function useCCHubState(initial?: Record<string, CCHubEnvState>) {
  const seed: Record<string, CCHubEnvState> = {}
  for (const [envID, raw] of Object.entries(initial || {})) {
    if (raw && raw.status === 'ok') seed[envID] = raw
  }
  const ccHubStateByEnv = reactive<Record<string, CCHubEnvState>>(seed)
  return { ccHubStateByEnv }
}
