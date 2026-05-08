// useAITools —— 检测本机装的 Claude Code / Cursor / Codex,UI 卡片显安装状态徽章。
//
//   - aitoolsResult     detectAITools 返回值,UI 卡片读它显徽标
//   - refreshAITools    初次 mount 跑;失败静默(浏览器模式)
//
// 设计:detector 只做"提示"角色 — 扫到给绿勾,没扫到 badge 警告但 checkbox 仍可勾
// (信任用户)。BotsPage 的 broken/ghost 状态兜底装坏的场景。
//
// 历史:曾有 forceEnableMissingTarget("我已自行安装"按钮)+ customInstallRoots
// (自定义安装目录),都被砍。前者因为新模型直接信任用户(checkbox 无 disabled),
// 后者因为 IDE 扩展目录都是 hardcoded ~/.<target>(装别处 IDE 也看不到)。
import { onMounted, ref } from 'vue'
import { detectAITools, type AIToolResult } from './bridge'

export function useAITools() {
  const aitoolsResult = ref<{ claude_code: AIToolResult; cursor: AIToolResult; codex: AIToolResult } | null>(null)

  async function refreshAITools() {
    try {
      aitoolsResult.value = await detectAITools()
    } catch {
      // 探测失败静默处理,UI 回落到"不显示徽标"
    }
  }

  onMounted(() => {
    refreshAITools()
  })

  return {
    aitoolsResult,
    refreshAITools,
  }
}
