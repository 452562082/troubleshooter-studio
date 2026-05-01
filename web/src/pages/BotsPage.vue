<script setup lang="ts">
import { ref, computed, nextTick, onActivated, onMounted, onUnmounted, reactive, watch } from 'vue'
// Wails 运行时事件 API:Go 端 EventsEmit 推过来,这里 EventsOn 订阅。
// 注意 runtime.js 是 Wails 打进 app 的全局脚本,浏览器里 import 的效果是
// 引用 window.runtime.*;`tshoot serve` 模式下这些函数不会真实推事件(无源)。
import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime'
import {
  applyBot,
  cancelInstall,
  discoverBots,
  doctor as bridgeDoctor,
  exportYAML,
  getRepoPathsForSystem,
  importAndDeploy,
  isDesktop as bridgeIsDesktop,
  openDir,
  readEnv,
  revealInFinder,
  runInstall,
  scanInstallPrompts,
  uninstallBot,
} from '../lib/bridge'
import yaml from 'js-yaml'
import { useDeployPath } from '../lib/useDeployPath'
import type { ApplyResult, DiscoveredBot, InstallPrompt } from '../lib/bridge'
import { toast } from '../lib/toast'
import { confirmDialog } from '../lib/confirm'
import WorkspaceBrowser from '../components/WorkspaceBrowser.vue'

const bots = ref<DiscoveredBot[]>([])
const loading = ref(false)
const error = ref<string | null>(null)

// 工作目录浏览器:点击卡片"📂 浏览工作目录"打开,modal 包一层 WorkspaceBrowser。
// 一次只开一个,重选机器人时换 rootPath 即可,没必要多开。
const browserBot = ref<DiscoveredBot | null>(null)
function openBrowser(b: DiscoveredBot) {
  browserBot.value = b
}
function closeBrowser() {
  browserBot.value = null
}
// extraRoots 仍然保留(空 array),让 discoverBots(extraRoots.value) 调用签名不变;
// UI 已下线,所以永远是 [] —— 等同于"只扫 3 条默认路径"。需要传自定义路径走 CLI。
const extraRoots = ref<string[]>([])

// 每张卡片的"重 gen"状态：key = path|target
// 只留 loading 让按钮禁用;结果反馈走 toast,不留 inline 文案
const regenState = reactive<Record<string, { loading: boolean }>>({})
// 卸载状态:跟 regenState 同结构,确保按钮在卸载过程中禁用 + 反复点击不重入
const uninstallState = reactive<Record<string, { loading: boolean }>>({})

// 编辑器状态（展开哪张卡片、草稿 yaml、应用结果）：
const editingKey = ref<string | null>(null)
const editorDraft = ref('')
const applyState = reactive<
  Record<string, { loading: boolean; result?: ApplyResult; err?: string; mode?: 'dry' | 'real' }>
>({})

// Doctor 诊断状态。key = regenKey(b)。
// 两档:
//   1. 声明级(reposRoot=空):只校验 yaml 内部一致性,秒级返回,默认入口
//   2. 深度扫(reposRoot=某目录):对比 yaml 声明 vs 代码仓库实态,8 种漂移检测,
//      可能跑几秒(要 walk 目录)。用户点"加 reposRoot 深度扫"展开填路径 + 重跑。
// 深度扫合并到这里后,独立 DoctorPage 就没存在必要了(之前保留它就是为了带 reposRoot 的场景)。
interface DoctorIssue {
  severity: string
  category: string
  target: string
  message: string
  suggest?: string
}
// 诊断面板 state。
//
// BotsPage 卡片代表"已部署"的机器人 —— 部署流程必经 GetMissingRepoPaths 校验 +
// SaveRepoPathsForSystem 落盘,所以这里 saved per-repo paths 一定存在。诊断后端会
// 自动从 ~/.tshoot/config.json 拉这份路径表跑深度扫,UI 不再暴露任何"覆盖路径"入口
// (代码扫描页另说,那里可能没部署过)。
//
// scannedRepoPaths:后端实际扫了哪几个仓库(repoName → absPath),banner 显示用。
const doctorState = reactive<
  Record<string, {
    loading: boolean
    issues?: DoctorIssue[]
    err?: string
    open?: boolean
    scannedRepoPaths?: Record<string, string>
  }>
>({})

async function runDoctor(b: DiscoveredBot) {
  const k = regenKey(b)
  if (!b.meta.system_yaml) {
    toast.error(`${b.meta.system_id}: tshoot.json 缺 system_yaml,无法诊断`)
    return
  }
  doctorState[k] = { loading: true, open: true }
  try {
    // reposRoot 永远传空 —— 后端走 saved paths,UI 不允许覆盖
    const data = (await bridgeDoctor(b.meta.system_yaml, '')) as {
      issues?: DoctorIssue[]
      scanned_repo_paths?: Record<string, string>
    }
    doctorState[k] = {
      loading: false,
      open: true,
      issues: data.issues || [],
      scannedRepoPaths: data.scanned_repo_paths || undefined,
    }
  } catch (e: any) {
    doctorState[k] = {
      loading: false,
      open: true,
      err: String(e?.message || e),
    }
  }
}

function doctorSeverityIcon(s: string): string {
  if (s === 'error') return '✖'
  if (s === 'warning') return '⚠'
  return 'ℹ'
}

function doctorClassForSeverity(s: string): string {
  if (s === 'error') return 'doctor-err'
  if (s === 'warning') return 'doctor-warn'
  return 'doctor-info'
}

// ── 卡片"更多"菜单 ──
// 一张卡之前外露 5 个按钮挤一行(打开对话 / 重生成 / 编辑 / 诊断 / 导出),
// 小屏溢出,视觉也乱。保留"打开对话"+ "诊断"两个高频操作外露,其余(重生成
// /编辑/导出)塞进 ⋯ 下拉。menuOpenKey = 当前打开菜单的 card key;null = 都收起。
const menuOpenKey = ref<string | null>(null)
function toggleMenu(key: string) {
  menuOpenKey.value = menuOpenKey.value === key ? null : key
}
function closeMenu() { menuOpenKey.value = null }
// 点菜单外空白区关闭。mount 时挂全局 click 监听,unmount 时摘,避免内存泄漏。
onMounted(() => {
  document.addEventListener('click', onDocClickForMenu)
})
onUnmounted(() => {
  document.removeEventListener('click', onDocClickForMenu)
})
function onDocClickForMenu(e: MouseEvent) {
  if (menuOpenKey.value === null) return
  // 点击发生在菜单或触发按钮内不关(靠 stopPropagation 实现不现实,这里用 closest)
  const t = e.target as HTMLElement | null
  if (t && t.closest('.bot-more-wrap')) return
  menuOpenKey.value = null
}

const isDesktop = bridgeIsDesktop

async function scan() {
  if (!isDesktop()) {
    error.value = '需要在桌面 app 里打开此页面(window.go 不可用)'
    return
  }
  loading.value = true
  error.value = null
  try {
    bots.value = await discoverBots(extraRoots.value)
  } catch (e: any) {
    error.value = String(e?.message || e)
    bots.value = []
  } finally {
    loading.value = false
  }
}

function regenKey(b: DiscoveredBot) {
  return `${b.path}|${b.meta.target}`
}

// 重新生成:用 tshoot.json 里现存的 system_yaml 重新渲染产物 + 刷到这张卡的真实部署目录,
// 等同"用当前 yaml 跑一次 Apply"。preserve_on_regenerate 列表里的文件保留用户手改不覆盖。
//
// 跟"应用到活 workspace"的区别:那个走编辑器草稿(用户改完先 dry-run 确认再 apply),
// 这个用存盘的 system_yaml 一键刷新,不需要进编辑器 —— 适合"模板更新了 / 想用最新版
// generator 重出一遍产物"的场景。
async function regen(b: DiscoveredBot) {
  const k = regenKey(b)
  // 不弹 confirm:操作非破坏性(preserve_on_regenerate 文件保留),Wails WebView 里
  // 弹 native confirm 偶发被遮挡 / 不响应。loading 态足够防抖,失败走 toast.error。
  regenState[k] = { loading: true }
  toast.info(`${b.meta.system_id}: 开始刷新 ${b.path}…`)
  try {
    const yamlText = b.meta.system_yaml
    if (!yamlText) throw new Error('tshoot.json 里没 system_yaml 字段,无法重新生成')
    // dryRun=false → 真写盘到 b.path(claude-code/cursor/codex 走 Apply,openclaw 同样)
    const res = await applyBot(b.path, yamlText, false) as any
    const written = res?.files_written ?? 0
    const preserved = (res?.files_preserved || []).length
    const removed = (res?.files_removed || []).length
    toast.success(`${b.meta.system_id} 已刷新到 ${b.path}: 写 ${written} / 保留 ${preserved} / 清理 ${removed}`)
  } catch (e: any) {
    toast.error(`${b.meta.system_id} 重新生成失败: ${String(e?.message || e)}`)
  } finally {
    regenState[k] = { loading: false }
  }
}

// 卸载机器人:按 target 分派,清两端(中间包 + AI 平台真实位置)。
// 用 confirmDialog 而不是 window.confirm —— Wails WKWebView 里 native confirm
// 经常静默失败 / 自动 cancel,用户点了看着没反应。confirmDialog 走 Vue 模态稳定。
async function uninstall(b: DiscoveredBot) {
  const k = regenKey(b)
  const target = b.meta.target
  const message = target === 'openclaw'
    ? `workspace 移到 ~/.Trash;摘掉 ~/.openclaw/openclaw.json 里的 agents.list 条目;清 creds.json。MCP servers(可能被多 agent 共享)保留。`
    : `已部署目录 ${b.path} 移到 ~/.Trash;同时清掉同根下的 agents/<name>.md 与 scripts/<name>/(自定义安装目录也会一并清);staging 中间包 ~/.tshoot/${target}/<id>/ 一并删除。`
  const ok = await confirmDialog({
    title: `卸载 "${b.meta.system_id}" (${target})?`,
    message,
    confirmText: '确认卸载',
    cancelText: '取消',
    danger: true,
    defaultAction: 'cancel',
  })
  if (!ok) return
  uninstallState[k] = { loading: true }
  try {
    const r = await uninstallBot(b.path, target)
    // 从 bots 列表把这条摘掉(避免点完还残留 → 用户以为卸载没生效)
    bots.value = bots.value.filter(x => regenKey(x) !== k)
    toast.success(`${b.meta.system_id} (${target}) 已卸载;${(r.log || []).length} 项操作详见日志`)
  } catch (e: any) {
    toast.error(`卸载失败: ${String(e?.message || e)}`)
  } finally {
    uninstallState[k] = { loading: false }
  }
}

function toggleEditor(b: DiscoveredBot) {
  const k = regenKey(b)
  if (editingKey.value === k) {
    editingKey.value = null
    return
  }
  if (!b.meta.system_yaml) {
    toast.error(`${b.meta.system_id}: tshoot.json 缺 system_yaml 字段,无法编辑`)
    return
  }
  editingKey.value = k
  editorDraft.value = b.meta.system_yaml
  delete applyState[k]
}

async function doExport(b: DiscoveredBot) {
  const k = regenKey(b)
  try {
    const yamlText = b.meta.system_yaml
    if (!yamlText) throw new Error('tshoot.json 里没 system_yaml 字段')
    // 用编辑器里的草稿（如果当前在编辑）优先导，否则导存盘版本
    const payload = editingKey.value === k ? editorDraft.value : yamlText
    const filename = `${b.meta.system_id || 'system'}.yaml`
    const savedTo = await exportYAML(filename, payload)
    if (!savedTo) return // 用户取消,不弹 toast
    toast.success(`已导出 ${b.meta.system_id} 到 ${savedTo}`)
  } catch (e: any) {
    toast.error(`导出 ${b.meta.system_id} 失败: ${String(e?.message || e)}`)
  }
}

async function runApply(b: DiscoveredBot, dryRun: boolean) {
  const k = regenKey(b)
  applyState[k] = { loading: true, mode: dryRun ? 'dry' : 'real' }
  try {
    const res = await applyBot(b.path, editorDraft.value, dryRun)
    applyState[k] = { loading: false, result: res, mode: dryRun ? 'dry' : 'real' }
    if (!dryRun) {
      // 真应用后刷新列表：时间戳 / preserved 等可能变
      await scan()
      editingKey.value = null
    }
  } catch (e: any) {
    applyState[k] = { loading: false, err: String(e?.message || e), mode: dryRun ? 'dry' : 'real' }
  }
}

// addRoot / pickExtraRoot / removeRoot / newRootInput 已下线 —— 扫描路径 UI panel 整个删了。
// 真要加自定义路径走 CLI(extraRoots 参数)。

function targetLabel(t: string): string {
  const map: Record<string, string> = {
    openclaw: 'OpenClaw',
    'claude-code': 'Claude Code',
    cursor: 'Cursor',
    codex: 'Codex CLI',
  }
  return map[t] ?? t
}

// ── 导入 yaml → 部署 流程状态机 ──────────────────────────────────
// idle → picked → deploying → deployed → installing → done
type ImportStage = 'idle' | 'picked' | 'deploying' | 'deployed' | 'installing' | 'done'
const importStage = ref<ImportStage>('idle')
const importYAMLText = ref('')
const importYAMLPath = ref('')
const importTarget = ref<'openclaw' | 'claude-code' | 'cursor' | 'codex'>('openclaw')
const importDestPath = ref('')
const importError = ref<string | null>(null)

// 跟 InitPage / EditorPage 一致,拿 yaml 里的 system.id 当默认路径基准。
// importYAMLText 每次选文件都会变,computed 实时跟着算。
const importSystemId = computed<string>(() => {
  try {
    const parsed: any = yaml.load(importYAMLText.value)
    return parsed?.system?.id || ''
  } catch { return '' }
})

// 从 import yaml 里抽仓库列表,给下面"本地路径配置"UI 用。
// system.yaml 里只有 name/url,没有本地路径 —— 用户必须在这里手动补上才能部署,
// 因为 bot 运行时要靠本地路径做代码分析。
interface ImportRepoLite { name: string; url: string }
const importRepoList = computed<ImportRepoLite[]>(() => {
  try {
    const parsed: any = yaml.load(importYAMLText.value)
    if (!Array.isArray(parsed?.repos)) return []
    return parsed.repos
      .filter((r: any) => typeof r?.name === 'string' && r.name.trim())
      .map((r: any) => ({ name: String(r.name).trim(), url: String(r.url || '').trim() }))
  } catch { return [] }
})
// 每个仓库的本机绝对路径;部署时传进 importAndDeploy,后端烤进 repo-path-map.yaml +
// 触发 auto-analyze(扫码生成 service-dependency-map / data-schema-map)。
// key = repo.name。用户不填就不让部署。
const importRepoPaths = reactive<Record<string, string>>({})
// yaml 变动时:重置 → 用 system.id 反查上次部署存的路径,prefill 表单。
// 这样"BotsPage 改完 yaml 重新部署"不必再选一遍目录,跟"wizard 一次,后续静默"对齐。
watch(() => importYAMLText.value, async () => {
  for (const k of Object.keys(importRepoPaths)) delete importRepoPaths[k]
  try {
    const sysID = importSystemId.value
    if (!sysID) return
    const saved = await getRepoPathsForSystem(sysID)
    for (const [name, p] of Object.entries(saved || {})) {
      if (p) importRepoPaths[name] = p
    }
  } catch { /* 反查失败也无所谓,用户手填 */ }
})
async function pickRepoPath(name: string) {
  try {
    const p = await openDir(`选择 ${name} 的本地目录`)
    if (p) importRepoPaths[name] = p
  } catch (e: any) {
    importError.value = String(e?.message || e)
  }
}
// 所有仓库路径都填了才能部署;没仓库(repos: [])时也允许(纯基础设施 yaml)
const importRepoPathsReady = computed(() => {
  if (importRepoList.value.length === 0) return true
  return importRepoList.value.every(r => (importRepoPaths[r.name] || '').trim() !== '')
})
const importMissingRepoCount = computed(() => {
  return importRepoList.value.filter(r => !(importRepoPaths[r.name] || '').trim()).length
})
const {
  isManagedTarget: importIsManagedTarget,
  customPathExpanded: importCustomPathExpanded,
  autoDefaultPath: importAutoDefaultPath,
  resetCustomPath: importResetCustomPath,
} = useDeployPath(importTarget, importSystemId, importDestPath)
const importedOutputDir = ref('') // agent.Result.agent_path 返回的实际落盘位置
const installPrompts = ref<InstallPrompt[]>([])
const installCreds = ref<Record<string, string>>({})
const installLog = ref('')
const installOK = ref<boolean | null>(null)
const liveLogRef = ref<HTMLPreElement | null>(null)

// 新行推进来时把 <pre> 滚到底,模拟 tail -f 体验
watch(installLog, () => {
  nextTick(() => {
    if (liveLogRef.value) {
      liveLogRef.value.scrollTop = liveLogRef.value.scrollHeight
    }
  })
})

// 按变量名前缀分组,让 25 字段不至于一次铺平眼花。每组顺序固定,
// 未命中的"其他"垫底。分组 key 直接作为折叠面板标题。
const GROUP_ORDER = [
  '配置中心',
  'Grafana',
  'Jaeger',
  'Prometheus',
  'Loki',
  'ELK',
  '消息平台',
  'MCP',
  '其他',
] as const

function classifyPrompt(name: string): string {
  if (name.startsWith('CC_') || name.startsWith('CONFIG_CENTER_')) return '配置中心'
  if (name.startsWith('GRAFANA_')) return 'Grafana'
  if (name.startsWith('JAEGER_')) return 'Jaeger'
  if (name.startsWith('PROMETHEUS_')) return 'Prometheus'
  if (name.startsWith('LOKI_')) return 'Loki'
  if (name.startsWith('KIBANA_') || name.startsWith('ELK_') || name.startsWith('ES_')) return 'ELK'
  if (name.startsWith('MESSAGING_')) return '消息平台'
  if (name.startsWith('MCP_')) return 'MCP'
  return '其他'
}

const groupedPrompts = computed<{ group: string; prompts: InstallPrompt[] }[]>(() => {
  const buckets: Record<string, InstallPrompt[]> = {}
  for (const p of installPrompts.value) {
    const g = classifyPrompt(p.name)
    ;(buckets[g] ||= []).push(p)
  }
  return GROUP_ORDER.filter((g) => buckets[g]?.length).map((g) => ({
    group: g,
    prompts: buckets[g]!,
  }))
})

function filledCount(prompts: InstallPrompt[]): number {
  return prompts.filter((p) => (installCreds.value[p.name] ?? '').trim() !== '').length
}

// pickYAML / 导入 yaml 一键部署面板已下线 —— 走「创建向导」Step 1 的"导入已有 system.yaml"路径,
// 那条路完整覆盖校验 / 健康检查 / 仓库路径补全。本页 BotsPage 只做"扫已装 + 管理"职责。
// 整个 importStage / importYAMLText / importTarget / pickDestDir / runDeployFromYAML 等
// 一连串状态量保留(template 还有 v-if 的死分支引用),不影响 BotsPage 主路径。

async function pickDestDir() {
  try {
    const p = await openDir('选择部署目标路径')
    if (p) importDestPath.value = p
  } catch (e: any) {
    importError.value = String(e?.message || e)
  }
}

async function runImportDeploy() {
  if (!importDestPath.value.trim()) {
    importError.value = '需要 destPath'
    return
  }
  if (!importRepoPathsReady.value) {
    importError.value = `还有 ${importMissingRepoCount.value} 个仓库没指定本地路径;不填本地路径 bot 运行时无法分析代码`
    return
  }
  importError.value = null
  importStage.value = 'deploying'
  try {
    // 每个 repo 的本机绝对路径,作为 repo-path-map.yaml 的原料
    const paths: Record<string, string> = {}
    for (const r of importRepoList.value) {
      const p = (importRepoPaths[r.name] || '').trim()
      if (p) paths[r.name] = p
    }
    const res = await importAndDeploy(importYAMLText.value, importTarget.value, importDestPath.value, paths)
    importedOutputDir.value = res.agent_path
    // 各 target 是否需要 install.sh 步骤:
    //   openclaw:有交互凭证(scripts/install.sh),必须跑,UI 展示字段表单
    //   claude-code / cursor:ImportAndApply 内部已 native install 到 ~/.claude|cursor/,
    //                        无 install.sh 也无凭证收集
    const needsInstall = importTarget.value === 'openclaw'
    if (needsInstall) {
      const [prompts, existing] = await Promise.all([
        scanInstallPrompts(res.agent_path),
        readEnv(res.agent_path),
      ])
      installPrompts.value = prompts
      installCreds.value = { ...existing } // 预填已有
      for (const p of prompts) {
        if (!(p.name in installCreds.value)) installCreds.value[p.name] = ''
      }
      importStage.value = 'deployed'
    } else {
      // claude-code / cursor:ImportAndApply 已 rsync 产物到目标 project,完事
      installOK.value = true
      importStage.value = 'done'
      await scan()
    }
  } catch (e: any) {
    importError.value = String(e?.message || e)
    importStage.value = 'picked' // 让用户改了重来
  }
}

// installStartTime / installElapsed:把"已运行 X 秒 · Y 行日志"摆到按钮旁,
// 非程序员看到计时在动就知道"app 没卡,只是 install.sh 在跑"。
// setInterval 每秒更新;installing 结束清掉。
const installStartTime = ref<number | null>(null)
const installElapsed = ref(0)
let installTimer: number | null = null
// installCanceling:用户点了取消但 SIGKILL 还没 Go 端返回的短暂窗口,
// 禁用按钮避免重复点击。
const installCanceling = ref(false)

async function runDeployInstall() {
  importError.value = null
  importStage.value = 'installing'
  installLog.value = ''
  installCanceling.value = false
  // 开始计时(秒表)
  installStartTime.value = Date.now()
  installElapsed.value = 0
  if (installTimer) clearInterval(installTimer)
  installTimer = window.setInterval(() => {
    if (installStartTime.value) {
      installElapsed.value = Math.floor((Date.now() - installStartTime.value) / 1000)
    }
  }, 1000)
  try {
    // installLog 从 install:log 事件流已经累积;这里 r.log 是 Go 端兜底的完整文本,
    // 如果事件丢行(理论上不会)或用户刷新页面后 Go 已跑完才回来,用 r.log 补齐
    const r = await runInstall(importedOutputDir.value, installCreds.value)
    if (installLog.value === '' && r.log) installLog.value = r.log
    // exit_code === -2:Go 端约定"用户取消",UI 显示"已取消"而不是"失败"
    if (r.exit_code === -2) {
      installOK.value = false
      installLog.value += '\n[已取消] 由用户触发,install.sh 进程组已被 SIGKILL。\n'
    } else {
      installOK.value = r.ok
    }
    importStage.value = 'done'
    await scan()
  } catch (e: any) {
    importError.value = String(e?.message || e)
    importStage.value = 'deployed'
  } finally {
    // 停秒表
    if (installTimer) { clearInterval(installTimer); installTimer = null }
    installStartTime.value = null
    installCanceling.value = false
  }
}

// abortInstall:用户点"取消安装"触发。后端 SIGKILL 进程组后,runInstall promise
// 会 resolve 一个 exit_code=-2 的结果,由 runDeployInstall 的 try 块正常处理收尾。
async function abortInstall() {
  if (installCanceling.value) return // 防重复
  installCanceling.value = true
  try {
    await cancelInstall()
  } catch (e: any) {
    toast.error(`取消失败: ${String(e?.message || e)}`)
    installCanceling.value = false
  }
}

function resetImport() {
  importStage.value = 'idle'
  importYAMLText.value = ''
  importYAMLPath.value = ''
  importDestPath.value = ''
  importedOutputDir.value = ''
  installPrompts.value = []
  installCreds.value = {}
  installLog.value = ''
  installOK.value = null
  importError.value = null
}

// install:log 事件流:Go 端 RunInstallStreaming 每行推一次,这里追加到 installLog。
// 仅在"installing"阶段有意义,但订阅从 mount 持续到 unmount,Go 端静默时不会推。
// 多次安装之间 installLog 在 runDeployInstall 开头清空,所以跨会话不会脏。
onMounted(() => {
  scan()
  EventsOn('install:log', (line: string) => {
    installLog.value += line + '\n'
  })
})
// keep-alive 缓存了 BotsPage,InitPage 末步部署 → 切回 BotsPage 时组件不会重 mount,
// onMounted 里的 scan() 不跑,卸载/重装的状态变更看不到。onActivated 是 keep-alive
// 专属钩子,每次组件被激活(切回页面)都会触发 → 自动 rescan,卡片永远跟磁盘真态一致。
onActivated(() => {
  scan()
})
onUnmounted(() => {
  EventsOff('install:log')
})
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <h1>已装机器人</h1>
        <p class="subtitle">扫描本机各 AI 平台目录的 tshoot.json 锚点,列出已部署的排障机器人,并提供诊断 / 编辑 / 浏览工作目录 / 卸载等管理操作。</p>
      </div>
      <div class="page-actions">
        <!-- 导入 yaml 一键部署的入口已下线 —— 这条路径跟「创建向导」的"导入 yaml → 反填 → 一键部署"
             功能重叠且后者更完整(走过校验 / 健康检查 / 仓库路径补全)。本页只做"扫已装 + 管理"。
             需要导入新 yaml 部署?去 <创建向导> Step 1 选"导入已有 system.yaml"。 -->
        <button class="btn primary" :disabled="loading" @click="scan">
          {{ loading ? '扫描中…' : '刷新' }}
        </button>
      </div>
    </header>

    <!-- ── 导入 yaml → 部署 向导面板 ───────────────────────────── -->
    <section v-if="importStage !== 'idle'" class="deploy-panel">
      <div class="deploy-head">
        <strong>导入并部署</strong>
        <span class="deploy-path">{{ importYAMLPath }}</span>
        <button class="btn btn-regen" @click="resetImport">关闭</button>
      </div>

      <!-- Step 1: 选 target + destPath。openclaw 自动路径,其它要选 -->
      <div v-if="importStage === 'picked' || importStage === 'deploying'" class="deploy-step">
        <div class="deploy-field">
          <label>目标平台</label>
          <select v-model="importTarget" :disabled="importStage === 'deploying'">
            <option value="openclaw">OpenClaw(Studio 托管,需填凭证)</option>
            <option value="claude-code">Claude Code(CLI 用 @&lt;name&gt; 调)</option>
            <option value="cursor">Cursor(AI 侧栏选 Custom Agent)</option>
            <option value="codex">Codex CLI(用 @&lt;name&gt; 调)</option>
          </select>
        </div>
        <!-- openclaw 自动管理路径,折叠;用户点"自定义"才露 input -->
        <div v-if="importIsManagedTarget && !importCustomPathExpanded" class="deploy-field">
          <label>部署位置 <span class="auto-tag">自动管理</span></label>
          <div class="auto-path-display">
            <code>{{ importAutoDefaultPath || '…' }}</code>
            <button type="button" class="btn-link" @click="importCustomPathExpanded = true">自定义 →</button>
          </div>
        </div>
        <div v-else class="deploy-field">
          <label>
            部署目标路径
            <button v-if="importIsManagedTarget" type="button" class="btn-link" @click="importResetCustomPath">
              恢复默认
            </button>
          </label>
          <!-- readonly input,只能通过按钮选 —— 跟 wizard 里所有路径字段统一约束 -->
          <div class="path-row">
            <input
              :value="importDestPath"
              :placeholder="importIsManagedTarget ? importAutoDefaultPath : '点右侧按钮选择项目根路径'"
              :disabled="importStage === 'deploying'"
              readonly
              class="path-readonly"
              :title="importDestPath"
            />
            <button class="btn" :disabled="importStage === 'deploying'" @click="pickDestDir">
              {{ importDestPath ? '重新选…' : '选目录…' }}
            </button>
          </div>
        </div>
        <!-- 仓库本地路径配置:system.yaml 里没有这个信息(故意的 —— 跨机器可分享),
             部署到这台机器上的 bot 要靠每个仓库的本机绝对路径做代码分析,所以必须
             用户在这里手动补全。跑过 init 向导的流程那边路径是 wizard 自动攒的,
             这里 import yaml 没经过向导,用户显式指路径 —— 不然 bot 跑起来啥代码
             也找不到。 -->
        <div v-if="importRepoList.length > 0" class="deploy-field import-repo-paths">
          <label>
            仓库本地路径 <span class="required">*</span>
            <span class="field-hint">
              — system.yaml 不含本地路径,请为每个仓库指定本机绝对路径(bot 要读代码)
              <span v-if="importMissingRepoCount > 0" class="path-pending">
                · 还差 {{ importMissingRepoCount }} 个
              </span>
              <span v-else class="path-ready">· ✓ 全部配好</span>
            </span>
          </label>
          <div
            v-for="repo in importRepoList"
            :key="repo.name"
            class="import-repo-row"
          >
            <span class="import-repo-name">{{ repo.name }}</span>
            <input
              :value="importRepoPaths[repo.name]"
              type="text"
              :placeholder="repo.url ? `${repo.url} 对应的本机目录(点右侧选)` : '点右侧按钮选目录'"
              :disabled="importStage === 'deploying'"
              readonly
              class="path-readonly"
              :title="importRepoPaths[repo.name] || ''"
            />
            <button
              type="button"
              class="btn"
              :disabled="importStage === 'deploying'"
              @click="pickRepoPath(repo.name)"
            >{{ importRepoPaths[repo.name] ? '重新选…' : '选目录…' }}</button>
          </div>
        </div>
        <div class="deploy-actions">
          <button
            class="btn primary"
            :disabled="importStage === 'deploying' || !importDestPath.trim() || !importRepoPathsReady"
            @click="runImportDeploy"
          >
            {{ importStage === 'deploying' ? '部署中…' : '部署' }}
          </button>
          <span v-if="!importRepoPathsReady" class="deploy-block-hint">
            先把 {{ importMissingRepoCount }} 个仓库的本地路径补齐才能部署
          </span>
        </div>
      </div>

      <!-- Step 2: install.sh 阶段 -->
      <div v-if="importStage === 'deployed' || importStage === 'installing'" class="deploy-step">
        <p v-if="installPrompts.length > 0" class="deploy-tip">
          产物已写到 <code>{{ importedOutputDir }}</code>。
          install.sh 需要下面 {{ installPrompts.length }} 个凭证字段;已存在的
          <code>.env</code> 值自动预填。<strong>带 * 的是密码型字段</strong>。
        </p>
        <p v-else class="deploy-tip">
          产物已写到 <code>{{ importedOutputDir }}</code>。
          install.sh 不需要凭证(纯文件安装),点下面按钮直接安装。
        </p>
        <div v-if="installPrompts.length > 0" class="cred-groups">
          <details
            v-for="grp in groupedPrompts"
            :key="grp.group"
            class="cred-group"
            open
          >
            <summary>
              <span class="cred-group-name">{{ grp.group }}</span>
              <span
                class="cred-group-count"
                :class="{ complete: filledCount(grp.prompts) === grp.prompts.length }"
              >{{ filledCount(grp.prompts) }} / {{ grp.prompts.length }} 已填</span>
            </summary>
            <div class="creds-grid">
              <div v-for="p in grp.prompts" :key="p.name" class="cred-field">
                <label :for="'cred-' + p.name">
                  {{ p.name }}
                  <span v-if="p.secret" class="secret-mark">*</span>
                </label>
                <input
                  :id="'cred-' + p.name"
                  v-model="installCreds[p.name]"
                  :type="p.secret ? 'password' : 'text'"
                  :placeholder="p.prompt"
                  :disabled="importStage === 'installing'"
                />
              </div>
            </div>
          </details>
        </div>
        <div class="deploy-actions">
          <button class="btn" @click="revealInFinder(importedOutputDir)">在 Finder 中显示</button>
          <button
            class="btn primary"
            :disabled="importStage === 'installing'"
            @click="runDeployInstall"
          >
            {{ importStage === 'installing' ? '运行 install.sh 中…' : (installPrompts.length > 0 ? '写 .env 并运行 install.sh' : '运行 install.sh') }}
          </button>
          <!-- 取消按钮只在 installing 时出现。SIGKILL 给 bash 进程组,brew/npm 子进程也连带杀 -->
          <button
            v-if="importStage === 'installing'"
            class="btn btn-cancel"
            :disabled="installCanceling"
            @click="abortInstall"
          >
            {{ installCanceling ? '取消中…' : '取消安装' }}
          </button>
        </div>
        <!-- 运行时秒表 + 日志行数:让用户知道 app 没卡,install.sh 还在跑 -->
        <div v-if="importStage === 'installing'" class="install-progress">
          <span class="install-spinner" aria-hidden="true"></span>
          <span class="install-elapsed">已运行 {{ installElapsed }}s</span>
          <span class="install-loglines">· {{ installLog.split('\n').length - 1 }} 行日志</span>
        </div>
        <!-- installing 阶段实时滚出 install.sh 的 stdout+stderr,避免用户盯静默黑屏 -->
        <pre v-if="importStage === 'installing' && installLog" ref="liveLogRef" class="deploy-log live">{{ installLog }}</pre>
      </div>

      <!-- Step 3: 日志 / 完成 -->
      <div v-if="importStage === 'done'" class="deploy-step">
        <div class="deploy-result" :class="installOK ? 'ok' : 'err'">
          {{ installOK ? '✓ 部署完成' : '⚠ install.sh 非零退出' }}
        </div>
        <pre v-if="installLog" class="deploy-log">{{ installLog }}</pre>
        <div class="deploy-actions">
          <button class="btn" @click="revealInFinder(importedOutputDir)">在 Finder 中显示</button>
          <button class="btn primary" @click="resetImport">完成</button>
        </div>
      </div>

      <div v-if="importError" class="alert error">⚠ {{ importError }}</div>
    </section>

    <!-- 扫描路径已下线 —— 默认 3 条路径(`~/.openclaw/workspace` / `~/.claude/skills` /
         `~/.cursor/skills`)覆盖 99% 用户级部署。极少数"装项目根"场景走 CLI 传 extraRoots
         参数,不污染主 UI。详见 internal/discover/scan.go::DefaultRoots(). -->


    <div v-if="error" class="alert error">⚠️ {{ error }}</div>
    <div v-else-if="!isDesktop()" class="alert info">
      这个页面需要在桌面 app 里打开。浏览器模式暂不可用。
    </div>
    <div v-else-if="loading" class="empty">扫描中…</div>
    <div v-else-if="bots.length === 0" class="empty">
      还没部署过机器人。去「<strong><router-link to="/init">创建向导</router-link></strong>」从头建一份;
      已有 yaml 文件,在向导第 1 步选"导入已有 system.yaml"反填后一键部署。
    </div>

    <div v-else class="bot-grid">
      <article v-for="b in bots" :key="b.path + b.meta.target" class="bot-card">
        <header class="bot-head">
          <span class="bot-target" :data-target="b.meta.target">{{ targetLabel(b.meta.target) }}</span>
          <!-- "tshoot dev" 是 build 没打 git tag 时的兜底字面量,信息量为零(本地构建都长这样),
               显示反而成噪音。只在版本号是真版本号(非 dev / 空)时才渲染徽章。 -->
          <span
            v-if="b.meta.tshoot_version && b.meta.tshoot_version !== 'dev'"
            class="bot-ver"
          >tshoot {{ b.meta.tshoot_version }}</span>
        </header>
        <h3 class="bot-name">{{ b.meta.system_name || b.meta.system_id }}</h3>
        <p class="bot-id">ID: <code>{{ b.meta.system_id }}</code></p>
        <p class="bot-path" :title="b.path">📁 {{ b.path }}</p>
        <ul class="bot-stats">
          <li><strong>{{ b.env_count }}</strong> 环境</li>
          <li><strong>{{ b.repo_count }}</strong> 仓库</li>
          <li><strong>{{ b.skill_count }}</strong> skills</li>
        </ul>
        <footer class="bot-foot">
          <span class="bot-time">最近更新: {{ b.mod_time }}</span>
          <div class="bot-actions">
            <button
              class="btn btn-regen"
              :disabled="doctorState[regenKey(b)]?.loading"
              :title="'用部署时记下的本地仓库路径自动深度扫描:对比 yaml 声明 vs 代码实态,挑 8 类漂移给修复建议'"
              @click="runDoctor(b)"
            >
              {{ doctorState[regenKey(b)]?.loading ? '诊断中…' : '🩺 诊断' }}
            </button>
            <button
              class="btn btn-regen"
              :title="'打开机器人工作目录,树形浏览 + 文件编辑(改 SKILL.md / 脚本 / 变量做调试,不动 system.yaml)'"
              @click="openBrowser(b)"
            >
              📂 浏览工作目录
            </button>
            <!-- ⋯ 更多:低频/管理类操作折进下拉,省卡片版面 + 降视觉噪声 -->
            <div class="bot-more-wrap">
              <button class="btn btn-regen btn-more" :title="'更多操作'" @click.stop="toggleMenu(regenKey(b))">⋯</button>
              <div v-if="menuOpenKey === regenKey(b)" class="bot-menu" role="menu">
                <button
                  class="menu-item"
                  :disabled="regenState[regenKey(b)]?.loading"
                  :title="b.meta.system_yaml ? '用 tshoot.json 里保存的 yaml 重渲产物并直接刷到本机器人的活 workspace(preserve_on_regenerate 文件保留)' : 'tshoot.json 里没保存 system_yaml,无法重新生成'"
                  @click="closeMenu(); regen(b)"
                >
                  {{ regenState[regenKey(b)]?.loading ? '刷新中…' : '♻ 重新生成并刷新' }}
                </button>
                <button class="menu-item" @click="closeMenu(); toggleEditor(b)">
                  {{ editingKey === regenKey(b) ? '收起编辑器' : '✎ 编辑配置' }}
                </button>
                <button class="menu-item" @click="closeMenu(); doExport(b)">
                  {{ editingKey === regenKey(b) ? '⇩ 导出草稿' : '⇩ 导出 yaml' }}
                </button>
                <div class="menu-sep"></div>
                <button
                  class="menu-item menu-item-danger"
                  :disabled="uninstallState[regenKey(b)]?.loading"
                  :title="'卸载已部署的机器人:claude-code/cursor/codex 清 ~/.<target>/{agents,skills,scripts}/<name> 移到 ~/.Trash;openclaw 摘 openclaw.json agents.list + 清 creds.json'"
                  @click="closeMenu(); uninstall(b)"
                >
                  {{ uninstallState[regenKey(b)]?.loading ? '卸载中…' : '🗑 卸载机器人' }}
                </button>
              </div>
            </div>
          </div>
        </footer>

        <!-- Doctor 诊断结果:已部署机器人的 saved per-repo paths 由部署流程保证存在,
             后端自动用这份路径跑深度扫描,UI 不暴露"覆盖路径"入口(代码扫描页才需要)。 -->
        <section v-if="doctorState[regenKey(b)]?.open" class="doctor-panel">
          <div class="doctor-head">
            <strong>🩺 诊断结果</strong>
            <span
              v-if="doctorState[regenKey(b)]?.scannedRepoPaths && Object.keys(doctorState[regenKey(b)]!.scannedRepoPaths!).length"
              class="doctor-mode deep"
              :title="Object.entries(doctorState[regenKey(b)]!.scannedRepoPaths!).map(([n,p]) => `${n} → ${p}`).join('\n')"
            >
              深度扫 · {{ Object.keys(doctorState[regenKey(b)]!.scannedRepoPaths!).length }} 个仓库
            </span>
            <span v-else class="doctor-mode">仅静态检查 · 没找到本地仓库路径</span>
            <div class="doctor-head-actions">
              <button class="btn btn-regen" @click="doctorState[regenKey(b)].open = false">收起</button>
            </div>
          </div>

          <div v-if="doctorState[regenKey(b)]?.err" class="alert error">
            {{ doctorState[regenKey(b)]!.err }}
          </div>
          <div v-else-if="doctorState[regenKey(b)]?.issues?.length === 0" class="alert success">
            ✓ {{ doctorState[regenKey(b)]?.scannedRepoPaths && Object.keys(doctorState[regenKey(b)]!.scannedRepoPaths!).length
                ? '深度扫描未发现漂移'
                : '静态检查未发现问题(本系统暂无本地仓库路径记录)' }}
          </div>
          <ul v-else-if="doctorState[regenKey(b)]?.issues" class="doctor-list">
            <li
              v-for="(iss, i) in doctorState[regenKey(b)]!.issues"
              :key="i"
              :class="doctorClassForSeverity(iss.severity)"
            >
              <span class="doctor-icon">{{ doctorSeverityIcon(iss.severity) }}</span>
              <div class="doctor-body">
                <div class="doctor-line">
                  <span class="doctor-cat">{{ iss.category }}</span>
                  <span v-if="iss.target" class="doctor-target">→ {{ iss.target }}</span>
                </div>
                <div class="doctor-msg">{{ iss.message }}</div>
                <div v-if="iss.suggest" class="doctor-sug">建议:{{ iss.suggest }}</div>
              </div>
            </li>
          </ul>
        </section>

        <section v-if="editingKey === regenKey(b)" class="editor">
          <label class="editor-label">system.yaml(改完先「预演」看改动列表,再「应用到活 workspace」写盘 —— 仅刷新这一张卡所属平台)</label>
          <textarea v-model="editorDraft" class="editor-textarea" spellcheck="false" />
          <div class="editor-actions">
            <button
              class="btn"
              :disabled="applyState[regenKey(b)]?.loading"
              :title="'干跑:渲染产物 + 算 diff,告诉你哪些会写 / 保留 / 删除,不实际写盘'"
              @click="runApply(b, true)"
            >
              {{ applyState[regenKey(b)]?.loading && applyState[regenKey(b)]?.mode === 'dry' ? '预演中…' : '预演' }}
            </button>
            <button
              class="btn primary"
              :disabled="applyState[regenKey(b)]?.loading"
              :title="'真写盘到本机器人部署目录;preserve_on_regenerate 列表里的文件保留用户手改不覆盖'"
              @click="runApply(b, false)"
            >
              {{ applyState[regenKey(b)]?.loading && applyState[regenKey(b)]?.mode === 'real' ? '应用中…' : '应用到活 workspace' }}
            </button>
          </div>
          <div v-if="applyState[regenKey(b)]?.result" class="apply-result">
            <div class="apply-row"><strong>写入文件:</strong>{{ applyState[regenKey(b)]!.result!.files_written }}</div>
            <div v-if="applyState[regenKey(b)]!.result!.files_preserved?.length" class="apply-row">
              <strong>保留(用户手改):</strong>
              <code v-for="f in applyState[regenKey(b)]!.result!.files_preserved" :key="f">{{ f }}</code>
            </div>
            <div v-if="applyState[regenKey(b)]!.result!.files_removed?.length" class="apply-row removed">
              <strong>移除(陈旧产物):</strong>
              <code v-for="f in applyState[regenKey(b)]!.result!.files_removed" :key="f">{{ f }}</code>
            </div>
            <div v-if="applyState[regenKey(b)]!.result!.needs_restart_hint" class="apply-hint">
              💡 {{ applyState[regenKey(b)]!.result!.needs_restart_hint }}
            </div>
          </div>
          <div v-if="applyState[regenKey(b)]?.err" class="apply-err">⚠ {{ applyState[regenKey(b)]?.err }}</div>
        </section>
      </article>
    </div>
    <!-- 工作目录浏览器:点击卡片"📂 浏览工作目录"打开。 -->
    <WorkspaceBrowser
      v-if="browserBot"
      :root-path="browserBot.path"
      :bot="browserBot"
      @close="closeBrowser"
    />
  </div>
</template>

<style scoped>
/* .page / .page-header h1 / .subtitle / .btn / .btn.primary / .alert 来自 design.css */
.page-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 20px; }

.roots { background: #f8fafc; border: 1px solid #e2e8f0; border-radius: 8px; padding: 14px 16px; margin-bottom: 20px; }
.roots.collapsed { padding: 6px 14px; }
/* 折叠态:整行 ghost 按钮,跟普通文字一样轻量,鼠标 hover 才提示可点 */
.roots-toggle {
  width: 100%; padding: 4px 0;
  background: transparent; border: none; cursor: pointer;
  font-family: inherit; font-size: 12px; color: #64748b;
  text-align: left;
}
.roots-toggle:hover { color: #1e293b; }
.roots-toggle strong { color: #2563eb; font-weight: 500; }
.roots-collapse-btn {
  margin-left: auto;
  background: transparent; border: 1px solid #cbd5e1;
  padding: 2px 10px; border-radius: 4px;
  font-size: 11px; color: #64748b; cursor: pointer;
}
.roots-collapse-btn:hover { background: #fff; }
.roots-head { display: flex; align-items: baseline; gap: 12px; margin-bottom: 10px; flex-wrap: wrap; }
.roots-label { font-weight: 600; font-size: 13px; color: #334155; }
.hint { font-size: 12px; color: #64748b; }
.hint code { background: #e2e8f0; padding: 1px 4px; border-radius: 3px; font-size: 11px; }

.root-list { display: flex; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; }
.root-item {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 4px 10px; background: #fff; border: 1px solid #cbd5e1; border-radius: 4px;
  font-family: monospace; font-size: 12px; color: #334155;
}
.root-item.builtin { background: #ecfeff; border-color: #a5f3fc; color: #155e75; }
.root-item .tag { font-size: 10px; background: #0891b2; color: #fff; padding: 1px 5px; border-radius: 3px; font-family: inherit; }
.root-remove { background: none; border: none; color: #94a3b8; cursor: pointer; font-size: 16px; line-height: 1; padding: 0 2px; }
.root-remove:hover { color: #ef4444; }

.root-add { display: flex; gap: 8px; }
.root-add input { flex: 1; padding: 6px 10px; border: 1px solid #cbd5e1; border-radius: 4px; font-size: 13px; }

/* .alert.error / .alert.info 来自 design.css */

.empty { text-align: center; padding: 48px 24px; color: #94a3b8; font-size: 14px; line-height: 1.8; }
.empty code { background: #f1f5f9; padding: 1px 4px; border-radius: 3px; }

.bot-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 14px; }
.bot-card {
  background: #fff; border: 1px solid #e2e8f0; border-radius: 8px; padding: 14px 16px;
  transition: box-shadow 0.15s, border-color 0.15s;
}
.bot-card:hover { border-color: #94a3b8; box-shadow: 0 2px 8px rgba(15, 23, 42, 0.06); }

.bot-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.bot-target {
  font-size: 11px; padding: 2px 8px; border-radius: 3px; font-weight: 600;
  background: #e0e7ff; color: #3730a3;
}
.bot-target[data-target="openclaw"] { background: #fce7f3; color: #9f1239; }
.bot-target[data-target="claude-code"] { background: #fef3c7; color: #92400e; }
.bot-target[data-target="cursor"] { background: #dbeafe; color: #1e40af; }
.bot-target[data-target="codex"] { background: #d1fae5; color: #065f46; }
.bot-ver { font-size: 11px; color: #94a3b8; font-family: monospace; }

.bot-name { font-size: 16px; font-weight: 600; color: #0f172a; margin-bottom: 4px; }
.bot-id { font-size: 12px; color: #64748b; margin-bottom: 8px; }
.bot-id code { font-family: monospace; background: #f1f5f9; padding: 1px 4px; border-radius: 3px; }
.bot-path { font-size: 11px; color: #94a3b8; font-family: monospace; margin-bottom: 10px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.bot-stats { list-style: none; display: flex; gap: 14px; padding: 10px 0; border-top: 1px solid #f1f5f9; font-size: 12px; color: #64748b; }
.bot-stats strong { color: #0f172a; font-weight: 600; margin-right: 2px; }

.bot-foot {
  border-top: 1px solid #f1f5f9; padding-top: 8px; font-size: 11px; color: #94a3b8;
  display: flex; justify-content: space-between; align-items: center; gap: 10px;
}
.btn-regen {
  font-size: 11px; padding: 4px 10px; border-radius: 4px;
  background: #f1f5f9; border: 1px solid #cbd5e1; color: #334155;
}
.btn-regen:hover:not(:disabled) { background: #e2e8f0; }
/* ⋯ 更多菜单:外露只留高频,管理类折进来让卡片不拥挤 */
.bot-more-wrap { position: relative; }
.btn-more {
  font-size: 14px; line-height: 1; padding: 4px 10px;
}
.bot-menu {
  position: absolute; top: calc(100% + 4px); right: 0; z-index: 10;
  min-width: 140px; padding: 4px 0;
  background: #fff; border: 1px solid var(--c-line-2); border-radius: 6px;
  box-shadow: 0 6px 16px rgba(15, 23, 42, 0.12);
  display: flex; flex-direction: column;
}
.bot-menu .menu-item {
  text-align: left; padding: 7px 14px; font-size: 12px;
  border: none; background: transparent; color: var(--c-text); cursor: pointer;
  font-family: inherit;
}
.bot-menu .menu-item:hover:not(:disabled) { background: var(--c-surf-3); }
.bot-menu .menu-item:disabled { opacity: 0.5; cursor: not-allowed; }
/* 危险操作(卸载)用红色文字 + 顶上加分隔线,跟普通 menu item 视觉拉开,降低误点风险 */
.bot-menu .menu-sep { height: 1px; background: var(--c-line-2); margin: 4px 0; }
.bot-menu .menu-item-danger { color: #dc2626; }
.bot-menu .menu-item-danger:hover:not(:disabled) { background: #fef2f2; color: #b91c1c; }

/* Doctor 诊断结果面板 */
.doctor-panel {
  margin-top: 10px; padding-top: 10px; border-top: 1px dashed var(--c-line-2);
}
.doctor-head {
  display: flex; align-items: center; gap: 10px;
  margin-bottom: 8px; font-size: var(--fs-sm); color: var(--c-ink);
  flex-wrap: wrap;
}
.doctor-mode {
  font-size: var(--fs-xs); color: var(--c-muted);
  background: var(--c-surf-3); padding: 2px 8px; border-radius: 10px;
}
.doctor-mode.deep { background: #e0e7ff; color: #3730a3; font-family: monospace; }
.doctor-head-actions { margin-left: auto; display: flex; gap: 6px; }

.doctor-list {
  list-style: none; padding: 0; margin: 0;
  display: flex; flex-direction: column; gap: 6px;
}
.doctor-list li {
  display: flex; gap: 10px; padding: 8px 10px;
  border-radius: var(--r-sm); font-size: var(--fs-xs);
}
.doctor-err  { background: var(--c-danger-bg);  border: 1px solid var(--c-danger-border); color: var(--c-danger); }
.doctor-warn { background: #fffbeb;            border: 1px solid #fde68a;             color: var(--c-warn); }
.doctor-info { background: #eff6ff;            border: 1px solid #bfdbfe;             color: #1e40af; }
.doctor-icon { font-size: 14px; flex-shrink: 0; line-height: 1.4; }
.doctor-body { flex: 1; line-height: 1.5; }
.doctor-line { font-weight: 600; margin-bottom: 2px; }
.doctor-cat { font-family: monospace; }
.doctor-target { margin-left: 4px; opacity: 0.85; }
.doctor-msg { opacity: 0.92; }
.doctor-sug { margin-top: 4px; opacity: 0.8; font-style: italic; }


.bot-actions { display: flex; gap: 6px; }

.editor {
  margin-top: 12px; padding-top: 12px; border-top: 1px dashed #cbd5e1;
}
.editor-label { display: block; font-size: 11px; color: #64748b; margin-bottom: 6px; }
.editor-textarea {
  width: 100%; min-height: 240px; font-family: 'SFMono-Regular', 'Menlo', monospace;
  font-size: 11px; padding: 8px 10px; border: 1px solid #cbd5e1; border-radius: 4px;
  resize: vertical; line-height: 1.5; background: #f8fafc; color: #0f172a;
}
.editor-actions { display: flex; gap: 8px; margin-top: 8px; }

.apply-result {
  margin-top: 10px; padding: 10px 12px; background: #f0fdf4; border: 1px solid #bbf7d0;
  border-radius: 4px; font-size: 11px; color: #166534;
}
.apply-result .apply-row { margin-bottom: 4px; line-height: 1.6; }
.apply-result .apply-row.removed { color: #9a3412; }
.apply-result code { background: rgba(15, 23, 42, 0.05); padding: 1px 4px; border-radius: 2px; margin-right: 4px; font-family: inherit; }
.apply-hint { margin-top: 6px; padding-top: 6px; border-top: 1px dashed #bbf7d0; color: #166534; }
.apply-err { margin-top: 10px; padding: 8px 12px; background: #fef2f2; border: 1px solid #fecaca; border-radius: 4px; font-size: 11px; color: #991b1b; }

.page-actions { display: flex; gap: 8px; }

.deploy-panel {
  margin-bottom: 20px; padding: 16px 18px; background: #fff;
  border: 2px solid #0f172a; border-radius: 10px;
}
.deploy-head { display: flex; align-items: center; gap: 10px; margin-bottom: 12px; }
.deploy-head strong { font-size: 14px; color: #0f172a; }
.deploy-path { flex: 1; font-family: monospace; font-size: 11px; color: #64748b; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }

.deploy-step { display: flex; flex-direction: column; gap: 12px; }
.deploy-field { display: flex; flex-direction: column; gap: 6px; }
.deploy-field label { font-size: 12px; font-weight: 600; color: #334155; }
.deploy-field select, .deploy-field input {
  padding: 7px 10px; border: 1px solid #cbd5e1; border-radius: 4px; font-size: 13px;
}
.path-row { display: flex; gap: 8px; }
.path-row input { flex: 1; }
/* 统一的只读路径样式:跟 InitPage 保持一致 */
.path-readonly {
  background: #f8fafc; color: #475569; cursor: default;
  text-overflow: ellipsis;
}
.import-repo-row input.path-readonly {
  background: #f8fafc; color: #475569; cursor: default;
}
/* embedded/openclaw 的自动路径展示 */
.deploy-field label { display: flex; align-items: center; gap: 6px; }
.auto-tag {
  font-size: 10px; font-weight: 500; color: #065f46;
  background: #d1fae5; padding: 1px 6px; border-radius: 8px; letter-spacing: 0.2px;
}
.auto-path-display {
  display: flex; align-items: center; gap: 10px;
  padding: 7px 10px; background: #f1f5f9; border-radius: 4px;
  border: 1px dashed #cbd5e1;
}
.auto-path-display code {
  flex: 1; font-size: 12px; color: #1e40af; background: transparent; padding: 0;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.btn-link {
  padding: 0; border: none; background: transparent; color: #1e40af;
  font-size: 11px; font-weight: 500; cursor: pointer; font-family: inherit;
  text-decoration: underline; text-decoration-style: dotted; text-underline-offset: 3px;
}
.btn-link:hover { color: #1e3a8a; }

.deploy-tip { font-size: 12px; color: #64748b; line-height: 1.6; }
.deploy-tip code { background: #f1f5f9; padding: 1px 4px; border-radius: 3px; }
.deploy-tip strong { color: #c2410c; }

.cred-groups { display: flex; flex-direction: column; gap: 8px; }
.cred-group {
  border: 1px solid #e2e8f0; border-radius: 6px; background: #f8fafc;
}
.cred-group summary {
  cursor: pointer; padding: 8px 12px; display: flex; justify-content: space-between;
  align-items: center; font-size: 12px; font-weight: 600; color: #334155;
  user-select: none;
}
.cred-group summary:hover { background: #f1f5f9; }
.cred-group[open] summary { border-bottom: 1px solid #e2e8f0; background: #fff; }
.cred-group-name { display: flex; align-items: center; gap: 6px; }
.cred-group-count {
  font-size: 11px; font-weight: 500; color: #94a3b8;
  padding: 1px 6px; background: #f1f5f9; border-radius: 3px;
}
.cred-group-count.complete { background: #d1fae5; color: #065f46; }

.creds-grid {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
  gap: 10px; padding: 10px 12px;
}
.cred-field { display: flex; flex-direction: column; gap: 4px; }
.cred-field label { font-size: 11px; font-family: monospace; color: #334155; font-weight: 600; }
.secret-mark { color: #c2410c; }
.cred-field input { padding: 6px 8px; border: 1px solid #cbd5e1; border-radius: 4px; font-size: 12px; font-family: monospace; }

.deploy-actions { display: flex; gap: 8px; justify-content: flex-end; align-items: center; }
.deploy-block-hint {
  font-size: 11px; color: #b45309; margin-right: auto;
}

/* 导入 yaml 时让用户为每个仓库指定本机路径 */
.import-repo-paths .path-pending { color: #b45309; font-weight: 500; }
.import-repo-paths .path-ready { color: #047857; font-weight: 500; }
.import-repo-row {
  display: grid;
  grid-template-columns: 120px 1fr auto;
  gap: 8px; align-items: center;
  padding: 4px 0;
}
.import-repo-row .import-repo-name {
  font-family: monospace; font-size: 12px; color: #1e293b;
  font-weight: 500;
}
.import-repo-row input {
  padding: 6px 8px; border: 1px solid #cbd5e1; border-radius: 4px;
  font-size: 12px; font-family: monospace;
}
.import-repo-row input:focus { outline: none; border-color: #3b82f6; }
.import-repo-row .required { color: #dc2626; }
/* Cancel 按钮用危险色(红)区分于 primary(黑),提示"这是破坏性操作" */
.btn-cancel {
  background: #fef2f2; border-color: #fecaca; color: #991b1b;
}
.btn-cancel:hover:not(:disabled) { background: #fee2e2; border-color: #fca5a5; }
.btn-cancel:disabled { opacity: 0.6; }

/* 运行中进度指示:spinner + 秒表 + 日志行数,告诉用户 app 没卡 */
.install-progress {
  display: flex; align-items: center; gap: 8px;
  padding: 8px 12px; background: #eff6ff; border: 1px solid #bfdbfe;
  border-radius: 6px; font-size: 12px; color: #1e40af;
  font-variant-numeric: tabular-nums; /* 秒数跳变时数字不抖 */
}
.install-spinner {
  width: 12px; height: 12px; border-radius: 50%;
  border: 2px solid #bfdbfe; border-top-color: #2563eb;
  animation: spin 0.8s linear infinite;
}
@keyframes spin { to { transform: rotate(360deg); } }
.install-elapsed { font-weight: 600; }
.install-loglines { color: #64748b; }

.deploy-result {
  padding: 10px 12px; border-radius: 4px; font-size: 13px; font-weight: 600;
}
.deploy-result.ok { background: #f0fdf4; color: #166534; border: 1px solid #bbf7d0; }
.deploy-result.err { background: #fef2f2; color: #991b1b; border: 1px solid #fecaca; }
.deploy-log {
  background: #0f172a; color: #e2e8f0; padding: 12px; border-radius: 6px;
  font-family: 'SFMono-Regular', Menlo, monospace; font-size: 11px;
  max-height: 320px; overflow: auto; white-space: pre-wrap; word-break: break-all;
  margin-top: 10px;
}
/* 流式中的日志框加个脉动左边框,视觉提示'还在动' */
.deploy-log.live { border-left: 3px solid #22c55e; animation: pulse 1.4s ease-in-out infinite; }
@keyframes pulse {
  0%, 100% { border-left-color: #22c55e; }
  50%      { border-left-color: #4ade80; }
}
</style>
