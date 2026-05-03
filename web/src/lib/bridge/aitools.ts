// bridge/aitools.ts —— Claude Code / Cursor / Codex 安装探测。
// 给 wizard Step 2 的 claude-code / cursor 卡片显示"✓ 已装 vX.Y / ⚠ 未装"徽标用。
// 跟 openclaw 不一样:这俩 target 不从本地读模型,只做"装了没 + 版本"信息展示。

import * as App from '../../../wailsjs/go/main/App'
import { isDesktop } from '../bridge'

export interface AIToolResult {
  installed: boolean
  version?: string
  path?: string
  note?: string
}
export interface AIToolsDetectResult {
  claude_code: AIToolResult
  cursor: AIToolResult
  codex: AIToolResult
}
export async function detectAITools(): Promise<AIToolsDetectResult> {
  if (!isDesktop()) {
    return {
      claude_code: { installed: false, note: '浏览器模式不支持' },
      cursor: { installed: false, note: '浏览器模式不支持' },
      codex: { installed: false, note: '浏览器模式不支持' },
    }
  }
  // Wails 生成的类型把 nested 字段标成 optional,但 Go 侧永远返回非 nil;
  // 做一次防御性兜底,前端收到 undefined 时降级为 not-installed。
  // codex 字段在 Wails models.ts 里没声明(版本滞后),用索引访问绕过。
  const r = await App.DetectAITools()
  return {
    claude_code: (r.claude_code as AIToolResult) || { installed: false },
    cursor:      (r.cursor      as AIToolResult) || { installed: false },
    codex:       ((r as unknown as Record<string, AIToolResult | undefined>).codex) || { installed: false },
  }
}
