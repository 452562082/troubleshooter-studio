// bridge/one2all.ts —— one2all-remote MCP 资源预加载前端桥接。
// 跟 kuboard.ts 同款:顶层静态 import Wails binding,浏览器调用时 isDesktop() 判。
import { isDesktop } from './shared'
import { One2AllListResources, One2AllListDeployments, One2AllFetchConfigMaps } from '../../../wailsjs/go/main/App'

export interface One2AllNsEntry {
  name: string
  configmaps: string[]
}

export interface One2AllClusterEntry {
  name: string
  cluster_id: string
  namespaces: One2AllNsEntry[]
}

export interface One2AllResources {
  clusters: One2AllClusterEntry[]
  notes?: string[]
}

export interface One2AllDeploymentEntry {
  name: string
  selector?: string
}

export interface One2AllDeployments {
  deployments: One2AllDeploymentEntry[]
}

/** 拉 one2all 集群→namespace→configmap 资源树(仅桌面 app) */
export async function one2allListResources(
  mcpURL: string,
  token: string,
  includeConfigMaps: boolean,
): Promise<One2AllResources> {
  if (!isDesktop()) throw new Error('one2all 预加载仅桌面 app 支持')
  const raw = await One2AllListResources(mcpURL, token, includeConfigMaps)
  return {
    clusters: (raw.clusters || []).map((c: any) => ({
      name: c.name || '',
      cluster_id: c.cluster_id || c.clusterId || '',
      namespaces: (c.namespaces || []).map((n: any) => ({
        name: n.name || '',
        configmaps: n.configmaps || n.configMaps || [],
      })),
    })),
    notes: raw.notes || [],
  }
}

/** 拉 one2all 指定 cluster+namespace 下的 Deployment 列表(仅桌面 app) */
export async function one2allListDeployments(
  mcpURL: string,
  token: string,
  clusterID: string,
  namespace: string,
): Promise<One2AllDeployments> {
  if (!isDesktop()) throw new Error('one2all 预加载仅桌面 app 支持')
  const raw = await One2AllListDeployments(mcpURL, token, clusterID, namespace)
  return {
    deployments: (raw.deployments || []).map((d: any) => ({
      name: d.name || '',
      selector: d.selector || '',
    })),
  }
}

/** 批量读取 ConfigMap 内容,用于数据层自动识别 */
export async function one2allFetchConfigMaps(
  mcpURL: string,
  token: string,
  configs: Array<{ cluster_id: string; namespace: string; name: string }>,
): Promise<Array<{ cluster_id: string; namespace: string; name: string; content: string; error?: string }>> {
  if (!isDesktop()) throw new Error('one2all 仅桌面 app 支持')
  return One2AllFetchConfigMaps(mcpURL, token, configs) as any
}
