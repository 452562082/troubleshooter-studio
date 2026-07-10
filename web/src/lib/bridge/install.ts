// bridge/install.ts —— 部署/安装 workflow:importAndDeploy / runInstall / scanInstallPrompts /
// readEnv / selfTestAgent / cancelInstall / defaultDestPath。
// InitPage Step 10 + BotsPage 都走这套。仅桌面 app 可用。
import * as App from '../../../wailsjs/go/main/App'
import { deploy, main } from '../../../wailsjs/go/models'
import { isDesktop } from './shared'
import type { ApplyResult } from './discoverBot'

export type InstallPrompt = deploy.Prompt
export type RunInstallResult = main.RunInstallResult

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

export type ImportAndDeployResult = ApplyResult & {
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
  // 用 any:wails generate 还没跑完时类型签名可能滞后(本次砍 customInstallRoot 参数,
  // 旧 wailsjs/go/main/App.d.ts 还有第 6 个参数,等 generate 后类型自动 refresh)。
  return (App.ImportAndDeploy as any)(yamlText, target, destPath, repoPaths, ideCreds)
}

/** 用户显式重试 CodeGraph 准备和索引。repoPaths 必须与部署使用同一份解析结果。 */
export async function reindexCodeGraph(
  yamlText: string,
  repoPaths: Record<string, string>,
): Promise<CodeGraphIndexReport> {
  if (!isDesktop()) throw new Error('ReindexCodeGraph 只在桌面 app 里可用')
  return App.ReindexCodeGraph(yamlText, repoPaths) as unknown as CodeGraphIndexReport
}

/** 给 target 推荐默认部署路径。embedded/openclaw 返回 ~/.tshoot/<target>/<id>/
 *  (UI 据此不让用户手填 destPath);claude-code/cursor 返回空串(UI 强制必填)。
 *  浏览器模式直接返回空,浏览器模式本来就没 home dir 概念。 */
export async function defaultDestPath(target: string, systemId: string): Promise<string> {
  if (!isDesktop()) return ''
  return App.DefaultDestPath(target, systemId)
}

/** 扫 install.sh 里所有 read_var 调用,给 UI 渲染凭证表单 */
export async function scanInstallPrompts(outputDir: string): Promise<InstallPrompt[]> {
  if (!isDesktop()) throw new Error('ScanInstallPrompts 只在桌面 app 里可用')
  const r = await App.ScanInstallPrompts(outputDir)
  return Array.isArray(r) ? r : []
}

/** 读 scripts/.env 现存值(用于预填表单) */
export async function readEnv(outputDir: string): Promise<Record<string, string>> {
  if (!isDesktop()) return {}
  return App.ReadEnv(outputDir)
}

/** 写凭证到 scripts/.env 后 shell-out bash install.sh,返回合并日志 */
export async function runInstall(
  outputDir: string,
  creds: Record<string, string>,
): Promise<RunInstallResult> {
  if (!isDesktop()) throw new Error('RunInstall 只在桌面 app 里可用')
  return App.RunInstall(outputDir, creds)
}

/** 跑一次 self-test:校验 agent 安装完整性 + ping 各 env 配置中心/可观测性端点。
 *  返回 ok 标志 + checks 明细;部署完自动触发,把 fail/warn 摘要弹给用户。 */
export type SelfTestCheck = { name: string; status: string; detail?: string }
export type SelfTestResult = { ok: boolean; checks: SelfTestCheck[] }

export async function selfTestAgent(dir: string): Promise<SelfTestResult> {
  if (!isDesktop()) throw new Error('SelfTestAgent 只在桌面 app 里可用')
  return App.SelfTestAgent(dir) as unknown as SelfTestResult
}

/** 取消正在跑的 install.sh(SIGKILL 给 bash 进程组)。返回 true=成功取消,
 *  false=当前没 install 在跑(UI 可忽略)。浏览器模式无 install,直接 false。 */
export async function cancelInstall(): Promise<boolean> {
  if (!isDesktop()) return false
  return App.CancelInstall()
}
