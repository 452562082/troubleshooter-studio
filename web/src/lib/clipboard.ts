// clipboard.ts —— 复制到剪贴板,带 execCommand 兜底。
// navigator.clipboard 在 file:// 或非 https / 旧 Wails WKWebView 下可能 reject;
// 用 textarea + document.execCommand('copy') 老路径兜一道,提高可达性。

/** 复制 text 到剪贴板;成功返回 true,失败返回 false(execCommand 也失败时)。 */
export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    // fallback for non-secure context / older webview
    try {
      const ta = document.createElement('textarea')
      ta.value = text
      ta.style.position = 'fixed'
      ta.style.opacity = '0'
      document.body.appendChild(ta)
      ta.select()
      const ok = document.execCommand('copy')
      document.body.removeChild(ta)
      return ok
    } catch {
      return false
    }
  }
}
