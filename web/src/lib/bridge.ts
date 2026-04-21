// bridge 把 "桌面 Wails binding" 和 "tshoot serve HTTP" 两种通路封到同一个函数。
// 桌面 app 里 window.go 存在 → 直接调 Go 方法，零 HTTP 开销；
// 浏览器里 → 回退到原来的 fetch('/api/*')。
//
// 新页面写代码只调 bridge.*，不要直接摸 fetch 或 window.go，省得未来改通路到处改。

import type { ApplyResult, DiscoveredBot, ValidateResult } from '../types/wails'

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
  return app.DiscoverBots(extraRoots)
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
