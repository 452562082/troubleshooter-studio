// useImportFlow —— "导入已有 system.yaml" 入口闭环。
//
// 暴露:
//   - 状态        showImportDialog / importText / importError
//   - 打开 / 关闭  openImportDialog / closeImportDialog
//   - 选文件     handleImportFile(浏览器 fallback)/ pickImportYAMLNative(桌面 osascript)
//   - 应用       applyImport():yaml.load → applyParsedYAMLToWizardState → 跳 Step 2 →
//                              setTimeout 调 runImportCrossChecks
//
// applyImport 的真正反填主体在 lib/yamlImporter.ts(applyParsedYAMLToWizardState),InitPage 把
// 闭包里的 30+ reactive + helper + bridge 函数打包成一个 ApplyImportContext 传进去。本 composable
// 通过 buildContext callback 委托上层(避免再把 30+ 字段重复列一遍)。
//
// importInProgress 是 useImportCrossCheck 已经持有的 ref(InitPage 在两个 composable 之间共享):
// applyImport 开头置 true、runImportCrossChecks 完成后置 false。期间 configCenterType watcher
// 不破坏性清空 envNamespaces / serviceConfigSel / ccHubStateByEnv,否则刚反填的 state 全没了。
import { ref, type Ref } from 'vue'
import yaml from 'js-yaml'
import { openYAML } from './bridge'
import { isDesktop } from './bridge/shared'
import { applyParsedYAMLToWizardState, type ApplyImportContext } from './yamlImporter'

export interface UseImportFlowDeps {
  /** applyImport 开头置 true → cross-check 完成后置 false;watcher 见 true 期间不清 state */
  importInProgress: Ref<boolean>
  /** wizard 当前 step,反填完直接跳 Step 2 让用户看到反填的字段 */
  currentStep: Ref<number>
  /** 闭包构造 ApplyImportContext —— 把 InitPage 的 30+ reactive / helper 打包给反填主体 */
  buildContext: () => ApplyImportContext
  /** 反填完成后异步触发的真实端校验,由 useImportCrossCheck 暴露 */
  runImportCrossChecks: (cc: string) => Promise<void> | void
}

export function useImportFlow(deps: UseImportFlowDeps) {
  const showImportDialog = ref(false)
  const importText = ref('')
  const importError = ref('')

  // 单调递增的 import 序号,防 setTimeout(0) 排程的 cross-check 跟下一次 applyImport 撞:
  // 用户快速点两次"导入"或在 cross-check 跑完前手动重 retry,旧那次 cross-check 起飞后会
  // 写到刚被新 import 反填的 ccHubStateByEnv / kuboardStateByEnv 上,把新值覆盖掉。
  // 每次 applyImport bump 一次 token;cross-check 起飞前对比,token 不一致就直接 return。
  let importToken = 0

  function openImportDialog() {
    importText.value = ''
    importError.value = ''
    showImportDialog.value = true
  }

  function closeImportDialog() {
    showImportDialog.value = false
  }

  // 浏览器模式 fallback:HTML5 file input 走 webview 原生 panel
  function handleImportFile(e: Event) {
    const input = e.target as HTMLInputElement
    const file = input.files?.[0]
    if (!file) return
    const reader = new FileReader()
    reader.onload = () => {
      importText.value = String(reader.result || '')
    }
    reader.readAsText(file)
  }

  // 桌面 app 模式:用 openYAML() 走 osascript 弹原生选择器,**不能**用 <input type="file">,
  // 因为 macOS 26 上 Wails v2.12 的 WebKit2 NSOpenPanel 出现即崩(整个 app 闪退)。
  // 跟 EditorPage / AnalyzePage 的 loadFileNative 一致,统一走 osascript 绕过这个 bug。
  async function pickImportYAMLNative() {
    if (!isDesktop()) return
    try {
      const r = await openYAML()
      if (r && r.path) {
        importText.value = r.content || ''
      }
    } catch (err: any) {
      importError.value = `加载文件失败:${String(err?.message || err)}`
    }
  }

  async function applyImport() {
    importError.value = ''
    let parsed: any
    try {
      parsed = yaml.load(importText.value)
    } catch (err: any) {
      importError.value = `YAML 解析失败：${err.message}`
      return
    }
    if (!parsed || typeof parsed !== 'object') {
      importError.value = '内容为空或不是合法的 system.yaml'
      return
    }
    // bump import token —— 上一次 applyImport 排程但还没起飞的 setTimeout cross-check 会因
    // myToken !== importToken 直接 return,不会动新反填的 state。
    importToken++
    const myToken = importToken
    // 期间禁用 configCenterType watcher 的破坏性清空(它会在 ingest 多源 type 期间触发,把刚
    // 反填的 envNamespaces / serviceConfigSel / ccHubStateByEnv 全删)。
    deps.importInProgress.value = true
    const ctx = deps.buildContext()
    const { primaryConfigCenter } = await applyParsedYAMLToWizardState(parsed, ctx)
    const cc = primaryConfigCenter

    // 导入完直接跳到 Step 2(系统基本信息)— 反填的字段从这里展开,用户能逐步检查 / 改。
    // 留在欢迎页(Step 1)没意义,反填的内容在那看不见。
    deps.currentStep.value = 2
    showImportDialog.value = false

    // 反填完成后异步触发交叉校验。setTimeout(0) 推到宏任务,确保 configCenterType
    // watcher 跑完 + reactive flush settle,避免跟同步反填竞争。
    // myToken 闭包捕获本次 import 的序号;若期间用户又点了一次"导入" → importToken 已 bump,
    // 这次的 cross-check 直接放弃,让新 import 自己触发它的那次 cross-check。
    setTimeout(() => {
      if (myToken !== importToken) return
      deps.runImportCrossChecks(cc)
    }, 0)
  }

  return {
    showImportDialog, importText, importError,
    openImportDialog, closeImportDialog,
    handleImportFile, pickImportYAMLNative,
    applyImport,
  }
}
