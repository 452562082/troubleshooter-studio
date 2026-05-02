// useYamlFileLoader —— 统一桌面 / 浏览器两条 yaml 加载通路。
// EditorPage / AnalyzePage 都有"原生 osascript 弹窗"+"<input type=file> 回退"两份
// 完全重复的实现,抽成 composable 后两处共用。
//
// 桌面:走 Wails openYAML(reliable on macOS WKWebView);
// 浏览器:回退 FileReader 读 yaml/yml 文本。
//
// onLoaded 回调里调用方写"set yamlContent + 清旧结果"即可。

import { isDesktop, openYAML } from './bridge'

export interface YamlFileLoader {
  /** 桌面 app 调:走原生对话框。浏览器模式下静默 no-op。 */
  loadFileNative: () => Promise<void>
  /** 浏览器调:挂在 <input type="file" @change> 上 */
  loadFileBrowser: (event: Event) => void
}

export interface YamlFileLoaderOptions {
  /** 拿到 yaml 文本后的回调;调用方负责赋值 + 清旧扫描/验证结果 */
  onLoaded: (content: string) => void
  /** 错误信息;桌面对话框抛错或文件读取异常时被调 */
  onError?: (message: string) => void
}

export function useYamlFileLoader(opts: YamlFileLoaderOptions): YamlFileLoader {
  const onErr = (msg: string) => opts.onError?.(msg)

  async function loadFileNative() {
    if (!isDesktop()) return
    try {
      const r = await openYAML()
      if (!r || !r.path) return // 用户取消
      opts.onLoaded(r.content || '')
    } catch (e) {
      const msg = e instanceof Error ? e.message : String((e as any)?.message ?? e)
      onErr(`加载文件失败: ${msg}`)
    }
  }

  function loadFileBrowser(event: Event) {
    const input = event.target as HTMLInputElement
    const file = input.files?.[0]
    if (!file) return
    const reader = new FileReader()
    reader.onload = () => {
      opts.onLoaded(reader.result as string)
    }
    reader.readAsText(file)
    input.value = '' // 重置 input,允许再次选同名文件
  }

  return { loadFileNative, loadFileBrowser }
}
