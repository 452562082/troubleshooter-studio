// bridge/loki.ts —— Loki 标签映射(Step 7 可观测性下的 grafana/loki 子区)。
// 通过 Grafana datasource 代理或直连 Loki HTTP API 拉 labels / values。

import * as App from '../../../wailsjs/go/main/App'
import { isDesktop } from '../bridge'

export interface LokiAuthInput {
  grafana_url?: string
  loki_url?: string
  ds_uid?: string
  api_key?: string
  user?: string
  pass?: string
}
export interface GrafanaDatasource {
  uid: string
  name: string
  type: string
  url?: string
  is_loki: boolean
  default?: boolean
}
export interface LokiLabelsResult {
  labels: string[]
  notes?: string[]
}
export interface LokiLabelValuesResult {
  key: string
  values: string[]
  notes?: string[]
}

export async function listGrafanaDatasources(input: LokiAuthInput): Promise<GrafanaDatasource[]> {
  if (!isDesktop()) throw new Error('ListGrafanaDatasources 只在桌面 app 可用')
  const r = await App.ListGrafanaDatasources(input as Parameters<typeof App.ListGrafanaDatasources>[0])
  return Array.isArray(r) ? (r as GrafanaDatasource[]) : []
}
export async function listLokiLabels(input: LokiAuthInput): Promise<LokiLabelsResult> {
  if (!isDesktop()) throw new Error('ListLokiLabels 只在桌面 app 可用')
  return App.ListLokiLabels(input as Parameters<typeof App.ListLokiLabels>[0]) as Promise<LokiLabelsResult>
}
// query 可选(LogQL 选择器);用于"已选 namespace 后只拉该 namespace 下的 app"等场景
export async function listLokiLabelValues(input: LokiAuthInput, labelKey: string, query = ''): Promise<LokiLabelValuesResult> {
  if (!isDesktop()) throw new Error('ListLokiLabelValues 只在桌面 app 可用')
  return App.ListLokiLabelValues(input as Parameters<typeof App.ListLokiLabelValues>[0], labelKey, query) as Promise<LokiLabelValuesResult>
}
