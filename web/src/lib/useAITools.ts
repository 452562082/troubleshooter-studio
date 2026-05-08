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
   * 跑 detector 探测三家 IDE(一次性扫所有家,不分单独 IDE)。
   * @param manual 用户主动点"重新扫描"触发(true) vs onMounted 自动跑(false)。
   *   manual=true 时弹 toast 反馈各家结果,让用户对应自己点的那家。
   */
  async function refreshAITools(manual = false): Promise<void> {
    if (aitoolsRefreshing.value) return
    aitoolsRefreshing.value = true
    try {
      aitoolsResult.value = await detectAITools()
      if (manual) {
        // 列各家逐项结果,而不是"共 N 家"含糊总数 —— 用户点的是某家的"重新扫描",
        // 应该一眼看到自己关心的那家是 ✓ 还是 ✗。
        const r = aitoolsResult.value
        const lines = [
          `Claude Code ${r?.claude_code?.installed ? '✓' : '✗'}`,
          `Cursor ${r?.cursor?.installed ? '✓' : '✗'}`,
          `Codex ${r?.codex?.installed ? '✓' : '✗'}`,
        ]
        const allMissing = !r?.claude_code?.installed && !r?.cursor?.installed && !r?.codex?.installed
        if (allMissing) {
          toast.info('重新扫描完成 — 三家 AI 平台都未检测到。确认 IDE 装在 PATH 里(或 ~/.<target>/)')
        } else {
          toast.info(`重新扫描完成 — ${lines.join(' / ')}(✗ 的需要先装好)`)
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

