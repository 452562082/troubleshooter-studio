// bridge 把 "桌面 Wails binding" 和 "tshoot serve HTTP" 两种通路封到同一个函数。
// 桌面 app 里 window.go 存在 → 直接调 Go 方法，零 HTTP 开销；
// 浏览器里 → 回退到原来的 fetch('/api/*')。
//
// 新页面写代码只调 bridge.*，不要直接摸 fetch 或 window.go，省得未来改通路到处改。

import type {
  ApplyResult,
  DiscoveredBot,
  InstallPrompt,
  OpenYAMLResult,
  RunInstallResult,
  ValidateResult,
} from '../types/wails'

function desktopApp() {
  if (typeof window === 'undefined') return null
  return window.go?.main?.App ?? null
}

/** 桌面 app 模式下为 true，浏览器 / dev 模式下为 false */
export function isDesktop(): boolean {
  return desktopApp() !== null
}

/** Validate system.yaml；失败抛 Error（message 已带解析原因） */
export async function validate(yamlText: string): Promise<ValidateResult> {
  const app = desktopApp()
  if (app) return app.Validate(yamlText)
  const resp = await fetch('/api/validate', {
    method: 'POST',
    headers: { 'Content-Type': 'text/yaml' },
    body: yamlText,
  })
  const body = await resp.json()
  if (!resp.ok) throw new Error(body?.error || `validate failed: ${resp.status}`)
  return body as ValidateResult
}

/** Gen 真落盘；outputDir 空字符串 = 用 yaml 里的 generation.output_dir（推荐） */
export interface GenSummary {
  output_dir: string
  [k: string]: unknown // 具体字段见 internal/generator.GenSummary；先 loose 接着
}
export async function gen(yamlText: string, outputDir = ''): Promise<GenSummary> {
  const app = desktopApp()
  if (app) return (await app.Gen(yamlText, outputDir)) as GenSummary
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
  const app = desktopApp()
  if (!app) return []
  // Go 端 nil slice 会被 JSON 编成 null；强制兜成数组
  const r = await app.DiscoverBots(extraRoots)
  return Array.isArray(r) ? r : []
}

/** ApplyBot 把新 yaml 应用到已装机器人的活 workspace（含 preserve 保留用户手改） */
export async function applyBot(
  agentPath: string,
  newYamlText: string,
  dryRun: boolean,
): Promise<ApplyResult> {
  const app = desktopApp()
  if (!app) throw new Error('ApplyBot 只在桌面 app 里可用')
  return app.ApplyBot(agentPath, newYamlText, dryRun)
}

/** 原生文件对话框：选一个 yaml 文件，返回 {path, content}；取消返回空对象 */
export async function openYAML(): Promise<OpenYAMLResult> {
  const app = desktopApp()
  if (!app) throw new Error('OpenYAML 只在桌面 app 里可用')
  return app.OpenYAML()
}

/** 原生目录对话框：选一个目录（用于部署目标路径 destPath），返回路径；取消返回空串 */
export async function openDir(title: string): Promise<string> {
  const app = desktopApp()
  if (!app) throw new Error('OpenDir 只在桌面 app 里可用')
  return app.OpenDir(title)
}

/** 把 yaml 直接部署成一个新机器人（agent.ImportAndApply 的 UI 封装）
 *  target: openclaw / claude-code / cursor / standalone；destPath 是部署目标路径
 */
export async function importAndDeploy(
  yamlText: string,
  target: string,
  destPath: string,
): Promise<ApplyResult> {
  const app = desktopApp()
  if (!app) throw new Error('ImportAndDeploy 只在桌面 app 里可用')
  return app.ImportAndDeploy(yamlText, target, destPath)
}

/** 扫 install.sh 里所有 read_var 调用，给 UI 渲染凭证表单 */
export async function scanInstallPrompts(outputDir: string): Promise<InstallPrompt[]> {
  const app = desktopApp()
  if (!app) throw new Error('ScanInstallPrompts 只在桌面 app 里可用')
  return app.ScanInstallPrompts(outputDir)
}

/** 读 scripts/.env 现存值（用于预填表单） */
export async function readEnv(outputDir: string): Promise<Record<string, string>> {
  const app = desktopApp()
  if (!app) return {}
  return app.ReadEnv(outputDir)
}

/** 写凭证到 scripts/.env 后 shell-out bash install.sh，返回合并日志 */
export async function runInstall(
  outputDir: string,
  creds: Record<string, string>,
): Promise<RunInstallResult> {
  const app = desktopApp()
  if (!app) throw new Error('RunInstall 只在桌面 app 里可用')
  return app.RunInstall(outputDir, creds)
}

/** 在 Finder / Explorer 里展示（不是打开）指定路径 */
export async function revealInFinder(path: string): Promise<void> {
  const app = desktopApp()
  if (!app) return
  return app.RevealInFinder(path)
}

/** exportYAML 弹原生保存对话框导出 yaml 到任意路径。
 *  桌面 app 走 Wails SaveFileDialog；浏览器走 Blob 下载。
 *  返回值：桌面 app 下为保存路径（或用户取消时空串）；浏览器下为下载文件名。
 */
export async function exportYAML(defaultFilename: string, yamlText: string): Promise<string> {
  const app = desktopApp()
  if (app) return app.SaveYAML(defaultFilename, yamlText)
  // 浏览器回退：触发 blob 下载
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
