// bridge/configCenter.ts —— 配置中心(Nacos / Apollo / Consul)真实预加载 + 数据层 / URL 探测
// Nacos → /nacos/v1/auth/login + /nacos/v1/cs/configs
// Apollo → /openapi/v1/apps + /envs/<env>/apps/<appId>/clusters/...
// Consul → /v1/kv/<prefix>?recurse=true&keys=true

import * as App from '../../../wailsjs/go/main/App'
import { isDesktop } from '../bridge'

export interface CCHubEntry {
  locator: string    // dataId(nacos) / namespace name(apollo) / full kv key(consul)
  group?: string     // nacos 独有;apollo 复用此字段表 cluster
  tenant?: string    // namespace
  type?: string      // yaml / properties / json / ...
  app_id?: string    // apollo 独有
}
export interface CCHubNamespace {
  id: string         // namespace UUID(public 为空串)
  show_name: string  // 友好名,UI 下拉选项用
}
export interface CCHubResult {
  type: string
  entries: CCHubEntry[]
  namespaces?: CCHubNamespace[]  // nacos / apollo:给 UI 下拉用
  notes?: string[]
}
export interface CCHubPreloadInput {
  type: 'nacos' | 'apollo' | 'consul'
  addr: string
  username?: string
  password?: string
  token?: string
  namespace?: string
  app_id?: string
  namespaces_only?: boolean  // true = 轻量模式,只列 namespaces 不拉 configs
}
/** 连目标配置中心拉清单。失败 reject 带人话错误(网络 / 鉴权 / 参数等)。 */
export async function preloadConfigCenter(input: CCHubPreloadInput): Promise<CCHubResult> {
  if (!isDesktop()) throw new Error('PreloadConfigCenter 只在桌面 app 可用')
  return App.PreloadConfigCenter(input as Parameters<typeof App.PreloadConfigCenter>[0]) as Promise<CCHubResult>
}

// 拉单条配置内容(给"数据层自动识别"用:Step 7 从已挑的 dataId 拉原文,js-yaml 解析出数据层)
export interface CCHubFetchContentInput {
  type: 'nacos' | 'apollo' | 'consul'
  addr: string
  username?: string
  password?: string
  token?: string
  namespace?: string
  group?: string
  data_id: string
  app_id?: string
}
export interface CCHubFetchContentResult {
  content: string
  format?: string  // yaml / json / properties
  notes?: string[]
}
export async function fetchConfigContent(input: CCHubFetchContentInput): Promise<CCHubFetchContentResult> {
  if (!isDesktop()) throw new Error('FetchConfigContent 只在桌面 app 可用')
  return App.FetchConfigContent(input as Parameters<typeof App.FetchConfigContent>[0]) as Promise<CCHubFetchContentResult>
}

// 批量拉取:nacos 会复用一次 probe+login,省 N-1 次登录开销。单条失败不中断整批。
export interface CCHubFetchBatchItem {
  key: string          // 前端自定义的映射 key(如 "dev::user-service")
  namespace?: string
  group?: string
  data_id: string
  app_id?: string
}
export interface CCHubFetchBatchInput {
  type: 'nacos' | 'apollo' | 'consul'
  addr: string
  username?: string
  password?: string
  token?: string
  items: CCHubFetchBatchItem[]
}
export interface CCHubFetchBatchItemResult {
  key: string
  ok: boolean
  result?: CCHubFetchContentResult
  error?: string
}
export interface CCHubFetchBatchResult {
  items: CCHubFetchBatchItemResult[]
  notes?: string[]
}
export async function fetchConfigContentBatch(input: CCHubFetchBatchInput): Promise<CCHubFetchBatchResult> {
  if (!isDesktop()) throw new Error('FetchConfigContentBatch 只在桌面 app 可用')
  return App.FetchConfigContentBatch(input as Parameters<typeof App.FetchConfigContentBatch>[0]) as Promise<CCHubFetchBatchResult>
}

// 数据层连通性探测:轻量 TCP dial + 最小协议握手,5s 超时,不读不写数据
export interface DSProbeInput {
  type: string                          // redis / mysql / doris / mongodb / ...
  fields: Record<string, string>        // url / dsn / host / port / brokers / user / pass ...
}
export interface DSProbeResult {
  ok: boolean
  latency?: string                      // "120ms"
  detail?: string                       // 成功时的服务版本 / 握手 banner
  error?: string                        // 失败时的人话原因
}
export async function probeDataStore(input: DSProbeInput): Promise<DSProbeResult> {
  if (!isDesktop()) throw new Error('ProbeDataStore 只在桌面 app 可用')
  return App.ProbeDataStore(input as Parameters<typeof App.ProbeDataStore>[0]) as Promise<DSProbeResult>
}

// 给 Step 3 环境列表的 api_domain / web_domain 自动测试用。GET 一下 URL,
// < 500 都算可达(404/401/403 也算通);DNS 失败 / 拒连 / 超时 / 5xx 算不通。
export async function probeURL(url: string): Promise<DSProbeResult> {
  if (!isDesktop()) throw new Error('ProbeURL 只在桌面 app 可用')
  return App.ProbeURL(url) as Promise<DSProbeResult>
}

// 给 Step 7 可观测性工具用:可选 basic auth + 可选 Bearer / API Key。
export async function probeURLAuth(url: string, user: string, pass: string, apiKey: string): Promise<DSProbeResult> {
  if (!isDesktop()) throw new Error('ProbeURLAuth 只在桌面 app 可用')
  return App.ProbeURLAuth(url, user, pass, apiKey) as Promise<DSProbeResult>
}
