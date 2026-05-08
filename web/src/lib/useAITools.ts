// useAITools —— 检测本机装的 Claude Code / Cursor / Codex,UI 卡片显安装状态徽章。
//
//   - aitoolsResult     detectAITools 返回值,UI 卡片读它显徽标
//   - aitoolsRefreshing 重新扫描进行中(给"重新扫描"按钮显 loading 用)
//   - refreshAITools    初次 mount 自动跑 + 用户点"重新扫描"按钮跑;失败静默
//
// 设计:detector 没扫到的 IDE = checkbox disabled,真没装的不让勾。漏扫场景靠
// "重新扫描"按钮兜底重试(用户改 PATH / 装位置后点)。
//
// 历史:曾有 forceEnableMissingTarget("我已自行安装"按钮)+ customInstallRoots
// (自定义安装目录),都被砍。前者改用 disabled checkbox,后者因为 IDE 扩展目录
// 都是 hardcoded ~/.<target>(装别处 IDE 也看不到)。
import { onMounted, ref } from 'vue'
import { detectAITools, type AIToolResult } from './bridge'
import { toast } from './toast'

export function useAITools() {
  const aitoolsResult = ref<{ claude_code: AIToolResult; cursor: AIToolResult; codex: AIToolResult } | null>(null)
  const aitoolsRefreshing = ref(false)

  /**
   * 跑 detector 探测三家 IDE。
   * @param manual 用户主动点"重新扫描"触发(true) vs onMounted 自动跑(false)。
   *   manual=true 时弹 toast 反馈结果,让用户感知按钮有响应(防"点了没反应"困惑)。
   */
  async function refreshAITools(manual = false): Promise<void> {
    if (aitoolsRefreshing.value) return
    aitoolsRefreshing.value = true
    try {
      const before = countInstalled(aitoolsResult.value)
      aitoolsResult.value = await detectAITools()
      if (manual) {
        const after = countInstalled(aitoolsResult.value)
        if (after > before) {
          toast.success(`重新扫描完成 — 新检测到 ${after - before} 个 AI 平台`)
        } else if (after === 0) {
          toast.info('重新扫描完成 — 仍未检测到任何 AI 平台,确认 IDE 装在 PATH 里(或 ~/.<target>/)')
        } else {
          toast.info(`重新扫描完成 — 检测到 ${after} 个 AI 平台,无变化`)
        }
      }
    } catch (e: any) {
      if (manual) toast.error(`重新扫描失败:${String(e?.message || e)}`)
      // 自动跑(onMounted)失败静默,UI 回落到"不显示徽标"
    } finally {
      aitoolsRefreshing.value = false
    }
  }

  onMounted(() => {
    refreshAITools(false)
  })

  return {
    aitoolsResult,
    aitoolsRefreshing,
    refreshAITools,
  }
}

function countInstalled(r: { claude_code?: AIToolResult; cursor?: AIToolResult; codex?: AIToolResult } | null): number {
  if (!r) return 0
  let n = 0
  if (r.claude_code?.installed) n++
  if (r.cursor?.installed) n++
  if (r.codex?.installed) n++
  return n
}
