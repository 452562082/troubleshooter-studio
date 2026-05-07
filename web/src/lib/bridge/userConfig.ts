// bridge/userConfig.ts —— 用户级配置 ~/.tshoot/config.json 的读写。
// 跨 wizard 会话持久,不进 troubleshooter.yaml(那份要分享,本机偏好不能塞进去)。
//
// 字段:default_repos_root / repo_paths_by_system
import * as App from '../../../wailsjs/go/main/App'
import { isDesktop } from './shared'

export interface UserConfigResult {
  default_repos_root: string    // 用户显式设过的;空串 = 没设
  resolved_repos_root: string   // 空时 fallback 到 ~/.tshoot/repos;永远非空,UI 展示用
  home_dir: string              // 当前用户 $HOME;前端据此把绝对路径折成 ~/... 展示
}

/** 读用户级配置(默认 clone 目录等)。没设过也不会 reject,返空串 + fallback。 */
export async function getUserConfig(): Promise<UserConfigResult> {
  if (!isDesktop()) return { default_repos_root: '', resolved_repos_root: '', home_dir: '' }
  return App.GetUserConfig()
}

/** 保存默认 clone 父目录。空串清除用户设置,回落到内置 fallback。 */
export async function setDefaultReposRoot(path: string): Promise<void> {
  if (!isDesktop()) return
  await App.SetDefaultReposRoot(path)
}

/** 读某 system.id 下的"仓库名 → 本地路径"映射。
 *  yaml 不含本机路径,这份从 ~/.tshoot/config.json 来,wizard 部署时会 upsert。
 *  没存过返回 {}。仅桌面 app 可用。 */
export async function getRepoPathsForSystem(systemID: string): Promise<Record<string, string>> {
  if (!isDesktop() || !systemID) return {}
  const r = await App.GetRepoPathsForSystem(systemID)
  return r || {}
}

/** 主动持久化"仓库名 → 本地路径"映射(空 map 清掉该 system 的所有路径)。
 *  ImportAndDeploy 内部会自动调,这里给 wizard"改完不立刻部署也能存"用。 */
export async function saveRepoPathsForSystem(systemID: string, paths: Record<string, string>): Promise<void> {
  if (!isDesktop() || !systemID) return
  await App.SaveRepoPathsForSystem(systemID, paths)
}

