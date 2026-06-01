// bridge/kuboard.ts —— Kuboard 集群资源 / configmap 批拉 / Deployment 列举
// 仅桌面 app 可用(Wails binding)。
//
// Wails 生成的 input 类型用 snake_case 字段(对齐 Go 端 json tag),跟前端 PascalCase
// 同名 type 不通,所以 App.X(input as Wails.X) 这层 as 不可避免;集中在本文件管理。

import * as App from '../../../wailsjs/go/main/App'
import { main } from '../../../wailsjs/go/models'

export type KuboardResources = main.KuboardResources

/** Kuboard 资源拉取:登录 → 列 cluster/ns/cm 三层。
 *  鉴权:accessKey(Kuboard 后台个人中心→API 访问凭证创建,免账密)优先;
 *  否则用 username+password 走 /login。
 *  clusterHint:Kuboard **v3** 必填 —— v3 无法用 access-key 枚举集群,需指定集群名
 *  (access-key 形态为 <密钥ID>.<密钥> + 必须配 username)。v4 忽略此参(tree 列全部)。 */
export async function kuboardListResources(
  url: string, username: string, password: string, accessKey = '', clusterHint = '',
): Promise<KuboardResources> {
  if (!isDesktop()) throw new Error('Kuboard 拉取只在桌面 app 里可用')
  return App.KuboardListResources(url, username, password, accessKey, clusterHint)
}

/** 批量拉 N 个 (cluster, namespace, configmap) 的 data 字段;
 *  Step 6 数据层自动识别用,挂在 kuboard 源的服务通过这个把 cm 内容拉回来,
 *  跟 nacos 一样跑 DS_MATCHERS 匹 redis/mysql/...。 */
export type KuboardFetchBatchInput = {
  url: string,
  access_key?: string,
  username?: string,
  password?: string,
  items: { key: string, cluster: string, namespace: string, configmap: string }[],
}
export type KuboardFetchBatchItemResult = {
  key: string,
  ok: boolean,
  content?: string,
  format?: string, // 固定 "yaml-multi"
  error?: string,
}
export type KuboardFetchBatchResult = {
  items: KuboardFetchBatchItemResult[],
  notes?: string[],
}
export async function kuboardFetchConfigMaps(input: KuboardFetchBatchInput): Promise<KuboardFetchBatchResult> {
  if (!isDesktop()) throw new Error('Kuboard 拉取只在桌面 app 里可用')
  return App.KuboardFetchConfigMaps(input as Parameters<typeof App.KuboardFetchConfigMaps>[0]) as Promise<KuboardFetchBatchResult>
}

/** 列指定 (cluster, namespace) 下的 Deployments;返回 name + selector(matchLabels)等。
 *  向导 Step 7 给 k8s 运行时配置服务 → workload 用,选完后从 selector 自动取 label_selector。 */
export type KuboardListDeploymentsInput = {
  url: string,
  username?: string,
  password?: string,
  access_key?: string,
  cluster: string,
  namespace: string,
}
export type KuboardDeploymentInfo = {
  name: string,
  namespace: string,
  replicas?: number,
  updated_replicas?: number,
  ready_replicas?: number,
  available_replicas?: number,
  strategy?: string,
  conditions?: string[],
  selector?: string,
}
export async function kuboardListDeployments(input: KuboardListDeploymentsInput): Promise<KuboardDeploymentInfo[]> {
  if (!isDesktop()) throw new Error('Kuboard 拉取只在桌面 app 里可用')
  // Wails 生成的 KuboardListPodsInput 用 snake_case(json tag),不是 Go 的 PascalCase。
  // 传错 key Go 端会拿到空 url/access_key,直接报"鉴权:填 accessKey 或 用户名+密码"。
  return App.KuboardListDeployments({
    url: input.url,
    username: input.username || '',
    password: input.password || '',
    access_key: input.access_key || '',
    cluster: input.cluster,
    namespace: input.namespace,
  } as Parameters<typeof App.KuboardListDeployments>[0]) as Promise<KuboardDeploymentInfo[]>
}

// 循环 import 兜底:isDesktop 在 bridge.ts 主文件 export
import { isDesktop } from '../bridge'
