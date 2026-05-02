// probeTypes.ts —— URL / 配置中心 / 数据源 等通用"连通性探测"状态形状。
// InitPage 与各子组件(EnvListItem / 后续 SourceCredCard 等)共享同一种四态枚举。

export interface URLProbeState {
  status: 'idle' | 'loading' | 'ok' | 'fail'
  latency?: string
  detail?: string
  error?: string
}
