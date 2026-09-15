// 三平台原生部署与 CodeGraph 索引。
import * as App from '../../../wailsjs/go/main/App'
import { isDesktop } from './shared'
import type { ApplyResult } from './discoverBot'

export type CodeGraphRepoResult = {
  name: string
  path: string
  action: 'initialized' | 'synced' | 'skipped' | 'failed'
  status: 'ready' | 'skipped' | 'warn'
  detail?: string
  file_count?: number
  node_count?: number
  edge_count?: number
  duration_ms: number
}

export type CodeGraphIndexReport = {
  ready: number
  total: number
  repos: CodeGraphRepoResult[]
}

export type ImportAndDeployResult = Omit<ApplyResult, 'codegraph'> & {
  codegraph?: CodeGraphIndexReport
}

/** 把 yaml 直接部署成一个新机器人(agent.ImportAndApply 的 UI 封装)
 *
 *  repoPaths: 仓库名 → 本机绝对路径,产物里会写进 repo-path-map.yaml。
 *  troubleshooter.yaml 本身不含这些路径(故意的,跨机器可分享),只有通过这里把路径送进
 *  产物。想按"仓库"部署但不关心路径的场景(比如 CLI 跑 smoke test)传空 map 即可 ——
 *  产物里的 repo-path-map.yaml 就是"未配置"占位,bot 运行时会提示用户补齐。
 */
export async function importAndDeploy(
  yamlText: string,
  target: string,
  destPath: string,
  repoPaths: Record<string, string> = {},
  ideCreds: Record<string, string> = {},
): Promise<ImportAndDeployResult> {
  if (!isDesktop()) throw new Error('ImportAndDeploy 只在桌面 app 里可用')
  // Wails 将 Go 的 action/status 生成为宽泛 string；桥接层按后端 JSON 契约收窄成 UI 联合类型。
  return App.ImportAndDeploy(yamlText, target, destPath, repoPaths, ideCreds) as unknown as ImportAndDeployResult
}

/** 用户显式重试 CodeGraph 准备和索引。repoPaths 必须与部署使用同一份解析结果。 */
export async function reindexCodeGraph(
  yamlText: string,
  repoPaths: Record<string, string>,
): Promise<CodeGraphIndexReport> {
  if (!isDesktop()) throw new Error('ReindexCodeGraph 只在桌面 app 里可用')
  return App.ReindexCodeGraph(yamlText, repoPaths) as unknown as CodeGraphIndexReport
}


export async function defaultDestPath(target: string, systemId: string): Promise<string> {
  if (!isDesktop()) return ''
  return App.DefaultDestPath(target, systemId)
}
