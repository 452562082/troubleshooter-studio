// useAITools —— 检测本机装的 Claude Code / Cursor / Codex,管理"未检测到时用户强制启用"开关。
//
//   - aitoolsResult            detectAITools 返回值,UI 卡片读它显徽标
//   - refreshAITools           初次 mount 跑;失败静默(浏览器模式)
//   - forceEnableMissingTarget per-target bool;"我自己装了" / "我会装" → checkbox 解锁
//
// 历史:曾有 customInstallRoots / pickCustomInstallRoot / clearCustomInstallRoot —
// 让用户挑非默认安装目录。后来发现 Claude Code / Cursor / Codex 三家的扩展目录
// 都是 hardcoded(~/.claude / ~/.cursor / ~/.codex),装到别处 IDE 看不到 → 功能
// 没意义已砍。强制启用入口保留(detector 没扫到但用户确实装了的场景)。
import { onMounted, reactive, ref } from 'vue'
import { detectAITools, type AIToolResult } from './bridge'

export function useAITools(initial: {
  forceEnableMissingTarget?: Record<string, boolean>
}) {
  const aitoolsResult = ref<{ claude_code: AIToolResult; cursor: AIToolResult; codex: AIToolResult } | null>(null)
  const forceEnableMissingTarget = reactive<Record<string, boolean>>({
    ...(initial.forceEnableMissingTarget ?? {}),
  })

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
    forceEnableMissingTarget,
    refreshAITools,
  }
}
