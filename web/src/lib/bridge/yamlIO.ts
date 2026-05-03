// bridge/yamlIO.ts —— 文件对话框 + 产物预览 + 本机文件操作(reveal / 导出)。
// 全部桌面 app 专属(浏览器模式 fallback 到 Blob 下载或抛错)。
import * as App from '../../../wailsjs/go/main/App'
import { main } from '../../../wailsjs/go/models'
import { isDesktop } from './shared'

export type OpenYAMLResult = main.OpenYAMLResult

/** 原生文件对话框:选一个 yaml 文件,返回 {path, content};取消返回空对象 */
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

/** 原生目录对话框:选一个目录(用于部署目标路径 destPath),返回路径;取消返回空串 */
export async function openDir(title: string): Promise<string> {
  if (!isDesktop()) throw new Error('OpenDir 只在桌面 app 里可用')
  return App.OpenDir(title)
}

/** 在 Finder / Explorer 里展示(不是打开)指定路径 */
export async function revealInFinder(path: string): Promise<void> {
  if (!isDesktop()) return
  return App.RevealInFinder(path)
}

/** exportYAML 弹原生保存对话框导出 yaml 到任意路径。
 *  桌面 app 走 Wails SaveFileDialog;浏览器走 Blob 下载。
 *  返回值:桌面 app 下为保存路径(或用户取消时空串);浏览器下为下载文件名。 */
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
