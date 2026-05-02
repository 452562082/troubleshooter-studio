// probeTypes.ts —— URL / 配置中心 / 数据源 等通用"连通性探测"状态形状。
// InitPage 与各子组件(EnvListItem / 后续 SourceCredCard 等)共享同一种五态枚举。

/** 通用探测状态。'fail' 通常代表"探测完成但服务不通"(如 HTTP 200 失败),
 *  'error' 代表"探测过程异常"(如网络超时 / 解析错)。yamlValidator 同名 alias。 */
export type ProbeStatus = 'idle' | 'loading' | 'ok' | 'fail' | 'error'

export interface URLProbeState {
  status: 'idle' | 'loading' | 'ok' | 'fail'
  latency?: string
  detail?: string
  error?: string
}
