// useAITools —— 检测本机装的 Claude Code / Cursor / Codex,并管理"未检测到时用户强制启用"
// 的 customInstallRoots / forceEnableMissingTarget 双开关。
//
//   - aitoolsResult            detectAITools 返回值,UI 卡片读它显徽标
//   - refreshAITools           初次 mount 跑;失败静默(浏览器模式)
//   - forceEnableMissingTarget per-target bool;"我自己装了" / "我会装" → checkbox 解锁
//   - customInstallRoots       per-target 用户手选的安装根目录(覆盖 ~/.<target> 默认)
//   - pickCustomInstallRoot    弹目录选择 + 持久化到 ~/.tshoot/config.json + 自动设 forceEnable
//   - clearCustomInstallRoot   清掉本地文件里的覆盖,否则下次启动又被反填回来
//
// 持久化:customInstallRoots 同时进 InitPage draft(传 initial 反填)+ ~/.tshoot/config.json
// (跨向导会话权威源)。onMounted 用 config.json 覆盖 saved.draft —— 文件版优先。
import { onMounted, reactive, ref } from 'vue'
import {
  detectAITools,
  getCustomInstallRoots,
  openDir,
  setCustomInstallRoot,
  type AIToolResult,
} from './bridge'
import { pushLog } from './logStore'

export function useAITools(initial: {
  forceEnableMissingTarget?: Record<string, boolean>
  customInstallRoots?: Record<string, string>
}) {
  const aitoolsResult = ref<{ claude_code: AIToolResult; cursor: AIToolResult; codex: AIToolResult } | null>(null)
  const forceEnableMissingTarget = reactive<Record<string, boolean>>({
    ...(initial.forceEnableMissingTarget ?? {}),
  })
  const customInstallRoots = reactive<Record<string, string>>({
    ...(initial.customInstallRoots ?? {}),
  })

  async function refreshAITools() {
    try {
      aitoolsResult.value = await detectAITools()
    } catch {
      // 探测失败静默处理,UI 回落到"不显示徽标"
    }
  }

  async function pickCustomInstallRoot(t: string) {
    try {
      const dir = await openDir(`选 ${t} 安装根目录(目录下应有 agents/ 子目录)`)
      if (dir) {
        customInstallRoots[t] = dir
        forceEnableMissingTarget[t] = true
        // 持久化到 ~/.tshoot/config.json,跨 wizard 会话和 BotsPage 扫描共用同一份
        await setCustomInstallRoot(t, dir).catch((e: any) => {
          pushLog('install', 'warn', `setCustomInstallRoot(${t}) 持久化失败: ${String(e?.message || e)}`)
        })
      }
    } catch (e: any) {
      pushLog('install', 'warn', `pickCustomInstallRoot(${t}) 失败: ${String(e?.message || e)}`)
    }
  }

  async function clearCustomInstallRoot(t: string) {
    delete customInstallRoots[t]
    // 同步清掉本地文件里的覆盖,否则下次启动又被反填回来
    await setCustomInstallRoot(t, '').catch((e: any) => {
      pushLog('install', 'warn', `setCustomInstallRoot(${t}, '') 清除失败: ${String(e?.message || e)}`)
    })
  }

  // 启动时从 ~/.tshoot/config.json 反填一次 customInstallRoots —— 优先于 saved draft,
  // 因为本地文件是"跨向导会话的权威";draft 里的值只是这次会话的快照,持久化口径以文件为准。
  onMounted(async () => {
    try {
      const m = await getCustomInstallRoots()
      for (const [t, dir] of Object.entries(m || {})) {
        if (dir) {
          customInstallRoots[t] = dir
          forceEnableMissingTarget[t] = true
        }
      }
    } catch {
      // 静默兜底:浏览器模式 / binding 还没跑 generate 都返空,不影响 UI
    }
    refreshAITools()
  })

  return {
    aitoolsResult,
    forceEnableMissingTarget,
    customInstallRoots,
    refreshAITools,
    pickCustomInstallRoot,
    clearCustomInstallRoot,
  }
}
