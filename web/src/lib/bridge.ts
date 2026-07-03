// bridge 把 "桌面 Wails binding" 和 "tshoot serve HTTP" 两种通路封到同一组函数。
// 桌面 app 里 window.go 存在 → 直接调 Go 方法(wailsjs/go/main/App.ts 自动产出);
// 浏览器里 → fallback fetch('/api/*')。新页面只调 bridge.*,改通路一处搞定。
// 类型从 wailsjs/go/models 来,Go 端改 struct 跑 make wails-gen 同步。
//
// 按域分子文件(全部从 ./bridge/<domain>.ts re-export,调用方 import path 不变):
//   bridge/shared.ts        —— isDesktop 探测
//   bridge/userConfig.ts    —— ~/.tshoot/config.json 读写(default repos / repo paths / install roots)
//   bridge/repoScan.ts      —— 单仓库快速扫描(branches / submodules / role / origin)
//   bridge/discoverBot.ts   —— 已装机器人发现 / 卸载 / 工作目录浏览编辑 / Apply
//   bridge/install.ts       —— 部署/安装 workflow(importAndDeploy / runInstall / selfTest 等)
//   bridge/infraCred.ts     —— 钥匙串 Infra 凭证读写
//   bridge/yamlIO.ts        —— 文件对话框 + 产物预览 + reveal/导出
//   bridge/kuboard.ts       —— Kuboard 集群资源 / cm / Deployments
//   bridge/configCenter.ts  —— Nacos / Apollo / Consul + DSProbe / URLProbe
//   bridge/loki.ts          —— Loki + Grafana datasources
//   bridge/openclaw.ts      —— OpenClaw 模型探测
//   bridge/aitools.ts       —— Claude Code / Cursor / Codex 安装探测
// 本文件只保留:isDesktop re-export + YAML core(validate / plan / analyze / doctor / gen)
//          —— 这一组共用 yaml 文本入参 + JSON 出参的 fetch 模板,集中放着。

import * as App from '../../wailsjs/go/main/App'
import { analyzerpipe, generator, main } from '../../wailsjs/go/models'

// 各域 binding re-export(import path 兼容,新代码也可直接 from './bridge/<domain>')
export { isDesktop } from './bridge/shared'
export * from './bridge/userConfig'
export * from './bridge/repoScan'
export * from './bridge/discoverBot'
export * from './bridge/install'
export * from './bridge/infraCred'
export * from './bridge/yamlIO'
export * from './bridge/kuboard'
export * from './bridge/configCenter'
export * from './bridge/loki'
export * from './bridge/openclaw'
export * from './bridge/aitools'

import { isDesktop } from './bridge/shared'

export type ValidateResult = main.ValidateResult
export type GenSummary = generator.GenSummary
export type Plan = generator.Plan
export type AnalyzeResult = analyzerpipe.Result
export type RepoSummary = analyzerpipe.RepoSummary
export type DoctorReport = Record<string, unknown> // doctor.Report 字段较多且业务后续会扩,先 loose

/** Validate troubleshooter.yaml；失败抛 Error（message 已带解析原因） */
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

export async function cancelAnalyze(): Promise<boolean> {
  if (!isDesktop()) return false
  return App.CancelAnalyze()
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
