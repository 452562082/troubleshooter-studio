// bridge 把 "桌面 Wails binding" 和 "tshoot serve HTTP" 两种通路封到同一组函数。
// 桌面 app 里 window.go 存在 → 直接调 Go 方法(wailsjs/go/main/App.ts 自动产出);
// 浏览器里 → fallback fetch('/api/*')。新页面只调 bridge.*,改通路一处搞定。
// 类型从 wailsjs/go/models 来,Go 端改 struct 跑 make wails-gen 同步。
//
// 按域分文件:
//   bridge/kuboard.ts       —— Kuboard 集群资源 / cm / Deployments
//   bridge/configCenter.ts  —— Nacos / Apollo / Consul + DSProbe / URLProbe
//   bridge/loki.ts          —— Loki + Grafana datasources
//   bridge/openclaw.ts      —— OpenClaw 模型探测
//   bridge/aitools.ts       —— Claude Code / Cursor / Codex 安装探测
// 本文件保留:isDesktop / yaml validate-plan-doctor-gen / discover-uninstall-applyBot /
//          openYAML-openDir-genPreview / importAndDeploy + install workflow / 用户配置 /
//          仓库扫描(monorepo / submodule / branches / role recommend) / Infra 凭证。

import * as App from '../../wailsjs/go/main/App'
import { agent, analyzerpipe, deploy, discover, generator, main } from '../../wailsjs/go/models'

// 各域 binding re-export(已搬到 bridge/<domain>.ts 子文件;import path 不变)
export * from './bridge/kuboard'
export * from './bridge/configCenter'
export * from './bridge/loki'
export * from './bridge/openclaw'
export * from './bridge/aitools'

export type DiscoveredBot = discover.DiscoveredAgent
export type ApplyResult = agent.Result
export type InstallPrompt = deploy.Prompt
export type OpenYAMLResult = main.OpenYAMLResult
export type RunInstallResult = main.RunInstallResult
export type ValidateResult = main.ValidateResult
export type GenSummary = generator.GenSummary
export type Plan = generator.Plan
export type AnalyzeResult = analyzerpipe.Result
export type RepoSummary = analyzerpipe.RepoSummary
export type DoctorReport = Record<string, unknown> // doctor.Report 字段较多且业务后续会扩,先 loose

/** 桌面 app 模式下为 true，浏览器 / dev 模式下为 false */
export function isDesktop(): boolean {
  return typeof window !== 'undefined' && window.go != null
}

/** Validate system.yaml；失败抛 Error（message 已带解析原因） */
export async function validate(yamlText: string): Promise<ValidateResult> {
  if (isDesktop()) return App.Validate(yamlText)
  const resp = await fetch('/api/validate', {
    method: 'POST',
    headers: { 'Content-Type': 'text/yaml' },
    body: yamlText,
  })
  const body = await resp.json()
  if (!resp.ok) throw new Error(body?.error || `validate failed: ${resp.status}`)
  return body as ValidateResult
}

/** Plan 干跑 gen,返回 skills / files / config-map 分布;不落盘 */
export async function plan(yamlText: string): Promise<Plan> {
  if (isDesktop()) return App.Plan(yamlText)
  const resp = await fetch('/api/plan', {
    method: 'POST',
    headers: { 'Content-Type': 'text/yaml' },
    body: yamlText,
  })
  const body = await resp.json()
  if (!resp.ok) throw new Error(body?.error || `plan failed: ${resp.status}`)
  return body as Plan
}

/** Analyze 扫 reposRoot 下每个仓库,抽 service_names + 配置中心线索。
 *  autoClone=true 时缺失仓库自动 shallow clone(需要 git + 凭证)。
 *  进度通过 Wails 'analyze:log' event 推流,前端订阅后展示。
 *  浏览器模式下(tshoot serve)目前没对应 handler,只能桌面用。
 */
export async function analyze(
  yamlText: string,
  reposRoot: string,
  autoClone = false,
): Promise<AnalyzeResult> {
  if (!isDesktop()) {
    throw new Error('Analyze 仅在桌面 app 可用,浏览器模式请用 CLI: tshoot analyze')
  }
  return App.Analyze(yamlText, reposRoot, autoClone)
}

/** AnalyzeV2:混合来源版,允许 per-repo 指定本地绝对路径(走 RepoPaths),
 *  没指定的仓库回落到 ReposRoot+Name 默认拼法。InitPage Step 4 的"本地 / 远程
 *  混合"模式专用。 */
export async function analyzeV2(
  yamlText: string,
  reposRoot: string,
  repoPaths: Record<string, string>,
  autoClone: boolean,
  repoName?: string,
): Promise<AnalyzeResult> {
  if (!isDesktop()) throw new Error('AnalyzeV2 仅在桌面 app 可用')
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return App.AnalyzeV2({
    yaml_text: yamlText,
    repos_root: reposRoot,
    repo_paths: repoPaths,
    auto_clone: autoClone,
    repo_name: repoName ?? '',
  } as Parameters<typeof App.AnalyzeV2>[0])
}

/** 从本地已 clone 的仓库目录里反查 origin remote URL,用于"本地模式"反填 yaml.repos[].url */
export async function getRemoteURL(repoPath: string): Promise<string> {
  if (!isDesktop()) return ''
  return App.GetRemoteURL(repoPath)
}

// ── 用户级配置(~/.tshoot/config.json) ───────────────────────────
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

/** AI 平台自定义安装根目录(target → 绝对路径)。空 target 不返。
 *  配套 ~/.tshoot/config.json 里的 custom_install_roots 字段。
 *  InitPage 启动时调一次反填,DiscoverBots 内部也读这份合并扫描列表。 */
export async function getCustomInstallRoots(): Promise<Record<string, string>> {
  if (!isDesktop()) return {}
  const m = await (App as any).GetCustomInstallRoots()
  return (m as Record<string, string>) || {}
}

/** upsert 单个 target 的自定义安装根目录;dir='' → 删除该 target 覆盖。 */
export async function setCustomInstallRoot(target: string, dir: string): Promise<void> {
  if (!isDesktop() || !target) return
  await (App as any).SetCustomInstallRoot(target, dir)
}

/** 子模块探测结果(monorepo 自动拆分用)。
 *  url 仅在 .gitmodules 路径下非空 —— 那是真"独立 git repo + 子目录共置"场景,
 *  每个 submodule 有自己的 git URL。 其它检测路径(workspaces / pom modules / cmd 多入口 /
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

// 注:曾经的 diff() bridge + DiffPage 已删 —— 功能被 BotsPage 的"编辑配置 → 预演"
// 完全覆盖(而且那个给的是 target-aware 真实 diff,带 preserve/remove 列表)。
// 后端 App.Diff binding 暂留做 CLI 调用兼容,UI 不再经过 bridge.

/** Doctor 对比声明 vs 代码实态,reposRoot 留空只校验声明一致性 */
export async function doctor(yamlText: string, reposRoot = ''): Promise<DoctorReport> {
  if (isDesktop()) return (await App.Doctor(yamlText, reposRoot)) as unknown as DoctorReport
  const qs = reposRoot ? `?repos_root=${encodeURIComponent(reposRoot)}` : ''
  const resp = await fetch(`/api/doctor${qs}`, {
    method: 'POST',
    headers: { 'Content-Type': 'text/yaml' },
    body: yamlText,
  })
  const body = await resp.json()
  if (!resp.ok) throw new Error(body?.error || `doctor failed: ${resp.status}`)
  return body
}

/** Gen 真落盘；outputDir 空字符串 = 用 yaml 里的 generation.output_dir（推荐） */
export async function gen(yamlText: string, outputDir = ''): Promise<GenSummary> {
  if (isDesktop()) return App.Gen(yamlText, outputDir)
  const resp = await fetch('/api/gen', {
    method: 'POST',
    headers: { 'Content-Type': 'text/yaml' },
    body: yamlText,
  })
  const body = await resp.json()
  if (!resp.ok) throw new Error(body?.error || `gen failed: ${resp.status}`)
  return body as GenSummary
}

/** DiscoverBots 扫描已装机器人；只在桌面 app 下有意义，浏览器下返回 [] */
export async function discoverBots(extraRoots: string[] = []): Promise<DiscoveredBot[]> {
  if (!isDesktop()) return []
  // Go 端 nil slice 会被 JSON 编成 null；强制兜成数组
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

/** 原生文件对话框：选一个 yaml 文件,返回 {path, content};取消返回空对象 */
export async function openYAML(): Promise<OpenYAMLResult> {
  if (!isDesktop()) throw new Error('OpenYAML 只在桌面 app 里可用')
  return App.OpenYAML()
}

/** 跑一次 gen 到 tmp 目录,返回所有产物文件(含内容)。
 *  比 plan() 重(真实写盘 + 读回内容),给 EditorPage 的"📂 预览产物"按钮用,
 *  让用户像文件浏览器一样点开看每个文件。仅桌面 app 可用。 */
export type GenPreviewFile = {
  path: string,
  size: number,
  binary: boolean,
  truncated?: boolean,
  content?: string,
}
export type GenPreviewResult = {
  system: string,
  config_center: string,
  targets: string[],
  skills_included: { name: string, reason?: string }[],
  skills_skipped: { name: string, reason?: string }[],
  files: GenPreviewFile[],
}
export async function genPreview(yamlText: string): Promise<GenPreviewResult> {
  if (!isDesktop()) throw new Error('GenPreview 只在桌面 app 里可用')
  return App.GenPreview(yamlText) as unknown as GenPreviewResult
}

/** 原生目录对话框：选一个目录（用于部署目标路径 destPath），返回路径；取消返回空串 */
export async function openDir(title: string): Promise<string> {
  if (!isDesktop()) throw new Error('OpenDir 只在桌面 app 里可用')
  return App.OpenDir(title)
}

/** 把 yaml 直接部署成一个新机器人（agent.ImportAndApply 的 UI 封装）
 *
 *  repoPaths: 仓库名 → 本机绝对路径,产物里会写进 repo-path-map.yaml。
 *  system.yaml 本身不含这些路径(故意的,跨机器可分享),只有通过这里把路径送进
 *  产物。想按"仓库"部署但不关心路径的场景(比如 CLI 跑 smoke test)传空 map 即可 ——
 *  产物里的 repo-path-map.yaml 就是"未配置"占位,bot 运行时会提示用户补齐。
 */
export async function importAndDeploy(
  yamlText: string,
  target: string,
  destPath: string,
  repoPaths: Record<string, string> = {},
  ideCreds: Record<string, string> = {},
  customInstallRoot: string = '',
): Promise<ApplyResult> {
  if (!isDesktop()) throw new Error('ImportAndDeploy 只在桌面 app 里可用')
  // Wails generate 跑慢一步时,新加的参数 backend 不认 —— 用 any 绕过 TS 严格签名校验
  return (App.ImportAndDeploy as any)(yamlText, target, destPath, repoPaths, ideCreds, customInstallRoot)
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

// ── Infra 凭证(配置中心 / 可观测性 / 消息平台...) ─────────────────────
// key 格式建议 "<type>:<env>:<field>",例 "nacos:dev:addr"。部署前端从钥匙串读,
// 映射成 install.sh 认的 env var(CC_ADDR_DEV 等),install.sh read_var 跳过交互。
export interface InfraCredLoadResult {
  api_key: string
  ok: boolean
  err?: string
}
export async function saveInfraCred(key: string, value: string): Promise<void> {
  if (!isDesktop()) throw new Error('SaveInfraCred 只在桌面 app 可用')
  await App.SaveInfraCred(key, value)
}
export async function loadInfraCred(key: string): Promise<InfraCredLoadResult> {
  if (!isDesktop()) return { api_key: '', ok: false }
  return App.LoadInfraCred(key)
}
export async function deleteInfraCred(key: string): Promise<void> {
  if (!isDesktop()) return
  await App.DeleteInfraCred(key)
}
/** 批量保存/删(一次 RPC),value 为空串 = 删 */
export async function saveInfraCredBatch(entries: Record<string, string>): Promise<void> {
  if (!isDesktop()) throw new Error('SaveInfraCredBatch 只在桌面 app 可用')
  await App.SaveInfraCredBatch({ entries } as { entries: Record<string, string> })
}

// 配置中心预加载 / Loki / OpenClaw / AITools 已搬到 bridge/<domain>.ts(顶部 export *)


/** 在 Finder / Explorer 里展示(不是打开)指定路径 */
export async function revealInFinder(path: string): Promise<void> {
  if (!isDesktop()) return
  return App.RevealInFinder(path)
}

/** exportYAML 弹原生保存对话框导出 yaml 到任意路径。
 *  桌面 app 走 Wails SaveFileDialog;浏览器走 Blob 下载。
 *  返回值:桌面 app 下为保存路径(或用户取消时空串);浏览器下为下载文件名。
 */
export async function exportYAML(defaultFilename: string, yamlText: string): Promise<string> {
  if (isDesktop()) return App.SaveYAML(defaultFilename, yamlText)
  // 浏览器回退:触发 blob 下载
  const blob = new Blob([yamlText], { type: 'text/yaml;charset=utf-8' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = defaultFilename
  document.body.appendChild(a)
  a.click()
  document.body.removeChild(a)
  URL.revokeObjectURL(url)
  return defaultFilename
}
