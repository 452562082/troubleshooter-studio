// bridge/discoverBot.ts —— 已装机器人发现 / 卸载 / 工作目录浏览编辑 / Apply 闭环。
// BotsPage 卡片所有"管理已装"操作的统一入口。仅桌面 app 可用(rootPath 鉴权来源是 discover.Scan)。
import * as App from '../../../wailsjs/go/main/App'
import { agent, discover } from '../../../wailsjs/go/models'
import { isDesktop } from './shared'

export type DiscoveredBot = discover.DiscoveredAgent
export type ApplyResult = agent.Result

/** DiscoverBots 扫描已装机器人;只在桌面 app 下有意义,浏览器下返回 [] */
export async function discoverBots(extraRoots: string[] = []): Promise<DiscoveredBot[]> {
  if (!isDesktop()) return []
  // Go 端 nil slice 会被 JSON 编成 null;强制兜成数组
  const r = await App.DiscoverBots(extraRoots)
  return Array.isArray(r) ? r : []
}

/** UninstallBot 卸载已装机器人:按 target 分派(openclaw / claude-code / cursor)。
 *  - openclaw:workspace 移 ~/.Trash + 摘 openclaw.json agents.list + 清 creds.json
 *  - claude-code / cursor:中间包移 ~/.Trash + 清 ~/.claude|cursor/{agents,skills,scripts}/<name>
 *  返回结果含日志,前端展示给用户看动了哪些资源。仅桌面 app 可用。 */
export type UninstallBotResult = {
  target: string,
  // openclaw 专属
  workspace_moved_to?: string,
  openclaw_json_clean?: boolean,
  creds_removed?: boolean,
  // claude-code / cursor 专属
  staging_moved_to?: string,
  user_agent_md?: string,
  user_skills_dir?: string,
  user_scripts_dir?: string,
  log?: string[],
}

export async function uninstallBot(dir: string, target: string): Promise<UninstallBotResult> {
  if (!isDesktop()) throw new Error('UninstallBot 只在桌面 app 里可用')
  return App.UninstallBot(dir, target) as unknown as UninstallBotResult
}

/** ForgetGhostBot:disk 已不在的 ghost 卡片"忘掉它"按钮 → 只清 ~/.tshoot/config.json
 *  里的部署记录,不动 disk(disk 上本来就没东西)。disk 还在的 bot 应走 uninstallBot,
 *  不要走这条绕过(否则 disk 残留没清)。仅桌面 app 可用。 */
export async function forgetGhostBot(systemID: string, target: string): Promise<void> {
  if (!isDesktop()) throw new Error('ForgetGhostBot 只在桌面 app 里可用')
  await (App as any).ForgetGhostBot(systemID, target)
}

// ── 已装机器人:工作目录浏览 / 编辑 ──
// BotsPage 卡片"📂 浏览工作目录"用。后端三件套(列树 / 读文件 / 写文件),
// rootPath 必须是 BotsPage 卡片里的 path(discover.Scan 出来的真实部署位置),
// 防止 binding 被滥用成"任意目录读写"。
export interface FileNode {
  name: string
  path: string         // 相对 rootPath 的路径,后端读 / 写时回传
  is_dir: boolean
  size?: number
  children?: FileNode[]
}

export interface ReadFileResult {
  content: string
  is_binary: boolean
  truncated?: boolean
  size: number
}

export async function listBotWorkspaceFiles(rootPath: string): Promise<FileNode> {
  if (!isDesktop()) throw new Error('ListBotWorkspaceFiles 只在桌面 app 里可用')
  return (App as any).ListBotWorkspaceFiles(rootPath)
}

export async function readBotWorkspaceFile(rootPath: string, relPath: string): Promise<ReadFileResult> {
  if (!isDesktop()) throw new Error('ReadBotWorkspaceFile 只在桌面 app 里可用')
  return (App as any).ReadBotWorkspaceFile(rootPath, relPath)
}

export async function writeBotWorkspaceFile(rootPath: string, relPath: string, content: string): Promise<void> {
  if (!isDesktop()) throw new Error('WriteBotWorkspaceFile 只在桌面 app 里可用')
  await (App as any).WriteBotWorkspaceFile(rootPath, relPath, content)
}

/** ApplyBot 把新 yaml 应用到已装机器人的活 workspace（含 preserve 保留用户手改） */
export async function applyBot(
  agentPath: string,
  newYamlText: string,
  dryRun: boolean,
): Promise<ApplyResult> {
  if (!isDesktop()) throw new Error('ApplyBot 只在桌面 app 里可用')
  return App.ApplyBot(agentPath, newYamlText, dryRun)
}
