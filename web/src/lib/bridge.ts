// bridge 把 "桌面 Wails binding" 和 "tshoot serve HTTP" 两种通路封到同一组函数。
// 桌面 app 里 window.go 存在 → 直接调 Go 方法（由 wails generate module 自动产出
// wailsjs/go/main/App.ts 绑定）；浏览器里 → 回退到原来的 fetch('/api/*')。
//
// 新页面写代码只调 bridge.*，不要直接 import wailsjs 或摸 window.go，省得未来改
// 通路到处改。类型从 wailsjs/go/models 来，Go 端改了 struct 跑 make wails-gen 同步。

import * as App from '../../wailsjs/go/main/App'
import { agent, analyzerpipe, deploy, discover, generator, main } from '../../wailsjs/go/models'

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

/** Diff 预览新 yaml vs existingDir 现有产物的文件级 create/modify/remove 变化。
 *  浏览器模式没有对应 API 端点(api/handler.go 没 HandleDiff),只能桌面用。
 */
export async function diff(yamlText: string, existingDir: string): Promise<Plan> {
  if (!isDesktop()) {
    throw new Error('Diff 在浏览器模式下不可用(tshoot serve 未实现 /api/diff),请在桌面 app 里使用')
  }
  return App.Diff(yamlText, existingDir)
}

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

/** 原生目录对话框：选一个目录（用于部署目标路径 destPath），返回路径；取消返回空串 */
export async function openDir(title: string): Promise<string> {
  if (!isDesktop()) throw new Error('OpenDir 只在桌面 app 里可用')
  return App.OpenDir(title)
}

/** 把 yaml 直接部署成一个新机器人（agent.ImportAndApply 的 UI 封装） */
export async function importAndDeploy(
  yamlText: string,
  target: string,
  destPath: string,
): Promise<ApplyResult> {
  if (!isDesktop()) throw new Error('ImportAndDeploy 只在桌面 app 里可用')
  return App.ImportAndDeploy(yamlText, target, destPath)
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

/** 取消正在跑的 install.sh(SIGKILL 给 bash 进程组)。返回 true=成功取消,
 *  false=当前没 install 在跑(UI 可忽略)。浏览器模式无 install,直接 false。 */
export async function cancelInstall(): Promise<boolean> {
  if (!isDesktop()) return false
  return App.CancelInstall()
}

// ── Standalone 嵌入桌面端:启动 / 停止 / 状态查询 ─────────────────────────
// 把 standalone target 机器人的 server.py 托管在 Studio 进程里,
// 前端 iframe 指 localhost:<port>,用户不用开浏览器。

export interface StandaloneStartResult {
  port: number
  pid: number
}
export interface StandaloneStatus {
  running: boolean
  port?: number
  pid?: number
  last_err?: string
}

/** 启动 standalone 机器人的 server.py,返回实际绑定的端口(UI 用来 iframe src)。
 *  apiKey 空串时 fallback 到 Studio 启动时的 LLM_API_KEY env;两者都空会 reject,
 *  UI 要引导用户填。同一 path 已在跑的会幂等返回现有 port。 */
export async function startStandalone(path: string, apiKey = ''): Promise<StandaloneStartResult> {
  if (!isDesktop()) throw new Error('StartStandalone 只在桌面 app 里可用')
  return App.StartStandalone(path, apiKey)
}

/** 停掉 path 对应的 runner。没在跑时返回 false,UI 可忽略。 */
export async function stopStandalone(path: string): Promise<boolean> {
  if (!isDesktop()) return false
  return App.StopStandalone(path)
}

/** 查状态:画"运行中 / 已停止"徽章,进 chat 页时先探活。 */
export async function standaloneStatus(path: string): Promise<StandaloneStatus> {
  if (!isDesktop()) return { running: false }
  return App.StandaloneStatus(path)
}

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
