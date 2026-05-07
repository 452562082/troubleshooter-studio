// bridge/repoScan.ts —— 单仓库快速扫描(分支列表 / monorepo 子模块 / 角色推荐 / 本地 origin)。
// 比完整 analyze 轻得多,仅给 wizard Step 4 单行 UI 行内用。
import * as App from '../../../wailsjs/go/main/App'
import { isDesktop } from './shared'

/** 子模块探测结果(monorepo 自动拆分用)。
 *  url 仅在 .gitmodules 路径下非空 —— 那是真"独立 git repo + 子目录共置"场景,
 *  每个 submodule 有自己的 git URL。其它检测路径(workspaces / pom modules / cmd 多入口 /
 *  顶层平铺多服务)是"同一仓库子目录",共用父仓 URL,本字段空。 */
export interface SubmoduleHint {
  name: string
  sub_path: string
  stack: string
  role: string
  reason: string
  url?: string
}

/** 列分支(只列,不跑 stack 检测 / 依赖扫描)。
 *  monorepo .gitmodules 拆分后给每个子模块行喂下拉用 —— 比完整 analyze 轻得多。
 *  空路径或非 git 仓库返回空数组。 */
export async function listBranchesForRepo(repoPath: string): Promise<string[]> {
  if (!isDesktop() || !repoPath) return []
  const r = await App.ListBranchesForRepo(repoPath)
  return r || []
}

/** 检测仓库是不是 monorepo + 列出每个子模块。
 *  返回空数组 = 不是 monorepo,UI 静默;返回 N>1 → "一键拆成 N 行"按钮。
 *  支持的 monorepo 模式见 internal/analyzer/monorepo_scan.go。 */
export async function detectSubmodulesForRepo(repoPath: string): Promise<SubmoduleHint[]> {
  if (!isDesktop() || !repoPath) return []
  const r = await App.DetectSubmodulesForRepo(repoPath)
  return (r || []) as SubmoduleHint[]
}

/** 给 (stack, name, optionalLocalPath) 推荐一个 role + 理由说明。
 *  wizard Step 4 在"扫描完成"或"用户改名/改 stack"时调一次,把推荐结果展示在 role 下拉旁边,
 *  让用户能一眼看出"为啥推这个角色"。空路径时只看名字 + stack 兜底,有路径时进一步读
 *  package.json / pom.xml / go.mod / composer.json 等。 */
export async function recommendRoleForRepo(stack: string, name: string, path = ''): Promise<{ role: string, reason: string }> {
  if (!isDesktop()) return { role: 'backend', reason: '默认' }
  const r = await App.RecommendRoleForRepo(stack, name, path)
  return { role: r?.role || 'backend', reason: r?.reason || '' }
}

/** 从本地已 clone 的仓库目录里反查 origin remote URL,用于"本地模式"反填 yaml.repos[].url */
export async function getRemoteURL(repoPath: string): Promise<string> {
  if (!isDesktop()) return ''
  return App.GetRemoteURL(repoPath)
}

/** 检查路径是否存在(给 wizard 扫描前预检用 —— umbrella 子模块代码可能被用户 rm 掉,
 *  扫描前 check 一下能给出明确指引,比让 backend 跑下去拿模糊的 path-missing 错误好。
 *  浏览器模式 / 路径空 → false。 */
export async function pathExists(p: string): Promise<boolean> {
  if (!isDesktop() || !p) return false
  return App.PathExists(p)
}
