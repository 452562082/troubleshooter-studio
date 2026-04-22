<script setup lang="ts">
import { ref, computed, nextTick, onMounted, onUnmounted, reactive, watch } from 'vue'
// Wails 运行时事件 API:Go 端 EventsEmit 推过来,这里 EventsOn 订阅。
// 注意 runtime.js 是 Wails 打进 app 的全局脚本,浏览器里 import 的效果是
// 引用 window.runtime.*;`tshoot serve` 模式下这些函数不会真实推事件(无源)。
import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime'
import { useRouter } from 'vue-router'
import {
  applyBot,
  cancelInstall,
  discoverBots,
  doctor as bridgeDoctor,
  exportYAML,
  gen as bridgeGen,
  importAndDeploy,
  isDesktop as bridgeIsDesktop,
  openDir,
  openYAML,
  readEnv,
  revealInFinder,
  runInstall,
  scanInstallPrompts,
} from '../lib/bridge'
import yaml from 'js-yaml'
import { useDeployPath } from '../lib/useDeployPath'

const router = useRouter()
import type { ApplyResult, DiscoveredBot, InstallPrompt } from '../lib/bridge'
import { toast } from '../lib/toast'

const bots = ref<DiscoveredBot[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const extraRoots = ref<string[]>([])
const newRootInput = ref('')

// 每张卡片的"重 gen"状态：key = path|target
// 只留 loading 让按钮禁用;结果反馈走 toast,不留 inline 文案
const regenState = reactive<Record<string, { loading: boolean }>>({})

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
const doctorState = reactive<
  Record<string, {
    loading: boolean
    issues?: DoctorIssue[]
    err?: string
    open?: boolean
    reposRoot?: string  // 深度扫用,空 = 声明级
    showReposRootInput?: boolean // 用户点了"深度扫"展开 input 填路径
  }>
>({})

async function runDoctor(b: DiscoveredBot, reposRoot = '') {
  const k = regenKey(b)
  if (!b.meta.system_yaml) {
    toast.error(`${b.meta.system_id}: tshoot.json 缺 system_yaml,无法诊断`)
    return
  }
  const prev = doctorState[k]
  doctorState[k] = {
    loading: true,
    open: true,
    reposRoot,
    showReposRootInput: prev?.showReposRootInput ?? false,
  }
  try {
    const data = (await bridgeDoctor(b.meta.system_yaml, reposRoot)) as { issues?: DoctorIssue[] }
    doctorState[k] = {
      loading: false,
      open: true,
      issues: data.issues || [],
      reposRoot,
      showReposRootInput: prev?.showReposRootInput ?? false,
    }
  } catch (e: any) {
    doctorState[k] = {
      loading: false,
      open: true,
      err: String(e?.message || e),
      reposRoot,
      showReposRootInput: prev?.showReposRootInput ?? false,
    }
  }
}

// "加 reposRoot 深度扫"按钮:展开 input + 选目录。用户填完直接跑(再点诊断冗余)。
function toggleDoctorDeepScan(b: DiscoveredBot) {
  const k = regenKey(b)
  const s = doctorState[k]
  if (!s) return // 主面板还没开,别让深度扫独行
  s.showReposRootInput = !s.showReposRootInput
  if (!s.showReposRootInput) {
    // 关起来也清掉用户半填的 reposRoot,避免下次误以为在跑深度扫
    s.reposRoot = ''
  }
}

async function pickDoctorReposRoot(b: DiscoveredBot) {
  const k = regenKey(b)
  try {
    const p = await openDir('选择仓库根目录(含各个 repo.name 子目录)')
    if (p && doctorState[k]) doctorState[k].reposRoot = p
  } catch (e: any) {
    if (doctorState[k]) doctorState[k].err = String(e?.message || e)
  }
}

async function runDoctorDeep(b: DiscoveredBot) {
  const k = regenKey(b)
  const r = doctorState[k]?.reposRoot?.trim()
  if (!r) {
    toast.error('请先填仓库根目录(或选目录)')
    return
  }
  await runDoctor(b, r)
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
    error.value = '需要在桌面 app 里打开此页面（window.go 不可用）'
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

async function regen(b: DiscoveredBot) {
  const k = regenKey(b)
  regenState[k] = { loading: true }
  try {
    const yamlText = b.meta.system_yaml
    if (!yamlText) throw new Error('tshoot.json 里没 system_yaml 字段,无法原地重 gen')
    const res = await bridgeGen(yamlText, '')
    const outDir = String(res?.output_dir || '未知输出路径')
    toast.success(`${b.meta.system_id}: 产物已写入 ${outDir}`)
  } catch (e: any) {
    toast.error(`${b.meta.system_id} 重 gen 失败: ${String(e?.message || e)}`)
  } finally {
    regenState[k] = { loading: false }
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

function addRoot() {
  const v = newRootInput.value.trim()
  if (!v) return
  if (!extraRoots.value.includes(v)) extraRoots.value.push(v)
  newRootInput.value = ''
  scan()
}

function removeRoot(r: string) {
  extraRoots.value = extraRoots.value.filter((x) => x !== r)
  scan()
}

// openChat:跳转到内嵌 chat 页(BotsChat.vue);那边走 Studio 原生 chat 协议
// 直连 LLM,不依赖 server.py。key 表单/spinner/错误处理都在那里。
function openChat(b: DiscoveredBot) {
  router.push({
    path: '/bots/chat',
    query: { path: b.path, name: b.meta.system_name || b.meta.system_id },
  })
}

function targetLabel(t: string): string {
  const map: Record<string, string> = {
    openclaw: 'OpenClaw',
    'claude-code': 'Claude Code',
    cursor: 'Cursor',
    embedded: 'Embedded (内嵌对话)',
    standalone: 'Embedded (内嵌对话)', // 历史 tshoot.json 里可能还写着 standalone,展示统一成 Embedded
  }
  return map[t] ?? t
}

// ── 导入 yaml → 部署 流程状态机 ──────────────────────────────────
// idle → picked → deploying → deployed → installing → done
type ImportStage = 'idle' | 'picked' | 'deploying' | 'deployed' | 'installing' | 'done'
const importStage = ref<ImportStage>('idle')
const importYAMLText = ref('')
const importYAMLPath = ref('')
const importTarget = ref<'openclaw' | 'claude-code' | 'cursor' | 'embedded'>('openclaw')
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

async function pickYAML() {
  importError.value = null
  try {
    const r = await openYAML()
    if (!r.path) return // 用户取消
    importYAMLPath.value = r.path
    importYAMLText.value = r.content
    importStage.value = 'picked'
    // 默认 destPath：claude-code/cursor 建议选项目根，openclaw 建议 ./dist/<system-id>
    importDestPath.value = ''
  } catch (e: any) {
    importError.value = String(e?.message || e)
  }
}

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
  importError.value = null
  importStage.value = 'deploying'
  try {
    const res = await importAndDeploy(importYAMLText.value, importTarget.value, importDestPath.value)
    importedOutputDir.value = res.agent_path
    // 各 target 是否需要 install.sh 步骤:
    //   openclaw:有交互凭证,必须跑,UI 展示字段表单
    //   claude-code / cursor:ImportAndApply 已 rsync,install.sh 无附加动作
    //   embedded:桌面端内嵌对话,没有 install.sh,产物写完直接齐活
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
onUnmounted(() => {
  EventsOff('install:log')
})
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <h1>已装机器人</h1>
        <p class="subtitle">扫描本机 tshoot.json 锚点，列出已经部署到 AI 平台的排障机器人。</p>
      </div>
      <div class="page-actions">
        <button
          class="btn"
          :disabled="importStage !== 'idle'"
          title="选 yaml + target + 部署路径,跳过编辑直接装;跟创建向导的'导入到向导编辑'不同"
          @click="pickYAML"
        >
          导入 YAML 一键部署
        </button>
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

      <!-- Step 1: 选 target + destPath。embedded/openclaw 自动路径,其它要选 -->
      <div v-if="importStage === 'picked' || importStage === 'deploying'" class="deploy-step">
        <div class="deploy-field">
          <label>目标平台</label>
          <select v-model="importTarget" :disabled="importStage === 'deploying'">
            <option value="openclaw">OpenClaw（Studio 托管,需填凭证）</option>
            <option value="claude-code">Claude Code（装到项目根）</option>
            <option value="cursor">Cursor IDE（装到项目根）</option>
            <option value="embedded">Embedded (内嵌对话,Studio 托管)</option>
          </select>
        </div>
        <!-- embedded/openclaw 自动管理路径,折叠;用户点"自定义"才露 input -->
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
          <div class="path-row">
            <input
              v-model="importDestPath"
              :placeholder="importIsManagedTarget ? importAutoDefaultPath : '项目根或工作目录'"
              :disabled="importStage === 'deploying'"
            />
            <button class="btn" :disabled="importStage === 'deploying'" @click="pickDestDir">
              选目录…
            </button>
          </div>
        </div>
        <div class="deploy-actions">
          <button
            class="btn primary"
            :disabled="importStage === 'deploying' || !importDestPath.trim()"
            @click="runImportDeploy"
          >
            {{ importStage === 'deploying' ? '部署中…' : '部署' }}
          </button>
        </div>
      </div>

      <!-- Step 2: install.sh 阶段 -->
      <div v-if="importStage === 'deployed' || importStage === 'installing'" class="deploy-step">
        <p v-if="installPrompts.length > 0" class="deploy-tip">
          产物已写到 <code>{{ importedOutputDir }}</code>。
          install.sh 需要下面 {{ installPrompts.length }} 个凭证字段；已存在的
          <code>.env</code> 值自动预填。<strong>有 * 的是 password 型</strong>。
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

    <section class="roots">
      <div class="roots-head">
        <span class="roots-label">扫描路径</span>
        <span class="hint">默认扫 <code>~/.openclaw/workspace</code>。如果机器人装在 Claude Code / Cursor 项目根里，把项目路径加进来。</span>
      </div>
      <div class="root-list">
        <span class="root-item builtin">~/.openclaw/workspace <span class="tag">默认</span></span>
        <span v-for="r in extraRoots" :key="r" class="root-item">
          {{ r }}
          <button class="root-remove" @click="removeRoot(r)">×</button>
        </span>
      </div>
      <div class="root-add">
        <input
          v-model="newRootInput"
          placeholder="/path/to/project 或 ~/my-repo"
          @keyup.enter="addRoot"
        />
        <button class="btn" @click="addRoot">添加并扫描</button>
      </div>
    </section>

    <div v-if="error" class="alert error">⚠️ {{ error }}</div>
    <div v-else-if="!isDesktop()" class="alert info">
      这个页面需要在桌面 app 里打开。浏览器模式暂不可用。
    </div>
    <div v-else-if="loading" class="empty">扫描中…</div>
    <div v-else-if="bots.length === 0" class="empty">
      还没部署过机器人。点右上角「<strong>导入 YAML 一键部署</strong>」拿已有 yaml 直接装；或去「<strong>创建向导</strong>」从头建一份。
    </div>

    <div v-else class="bot-grid">
      <article v-for="b in bots" :key="b.path + b.meta.target" class="bot-card">
        <header class="bot-head">
          <span class="bot-target" :data-target="b.meta.target">{{ targetLabel(b.meta.target) }}</span>
          <span class="bot-ver">tshoot {{ b.meta.tshoot_version || '?' }}</span>
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
          <span class="bot-time">最近更新：{{ b.mod_time }}</span>
          <div class="bot-actions">
            <!-- 外露:高频操作(对话 + 诊断),用绿色强调 -->
            <button
              class="btn btn-regen btn-chat"
              title="在 Studio 内跟机器人对话(Go 直连 Anthropic API,不依赖 python/flask)"
              @click="openChat(b)"
            >
              💬 打开对话
            </button>
            <button
              class="btn btn-regen"
              :disabled="doctorState[regenKey(b)]?.loading"
              :title="'跑一次 doctor 声明级诊断;想做代码实态深度扫描,面板展开后点「+ 深度扫描代码」填 reposRoot'"
              @click="runDoctor(b)"
            >
              {{ doctorState[regenKey(b)]?.loading ? '诊断中…' : '🩺 诊断' }}
            </button>
            <!-- ⋯ 更多:低频/管理类操作折进下拉,省卡片版面 + 降视觉噪声 -->
            <div class="bot-more-wrap">
              <button class="btn btn-regen btn-more" :title="'更多操作'" @click.stop="toggleMenu(regenKey(b))">⋯</button>
              <div v-if="menuOpenKey === regenKey(b)" class="bot-menu" role="menu">
                <button
                  class="menu-item"
                  :disabled="regenState[regenKey(b)]?.loading"
                  :title="b.meta.system_yaml ? '' : 'tshoot.json 里没保存 system_yaml，无法原地重 gen'"
                  @click="closeMenu(); regen(b)"
                >
                  {{ regenState[regenKey(b)]?.loading ? '生成中…' : '♻ 重新生成' }}
                </button>
                <button class="menu-item" @click="closeMenu(); toggleEditor(b)">
                  {{ editingKey === regenKey(b) ? '收起编辑器' : '✎ 编辑配置' }}
                </button>
                <button class="menu-item" @click="closeMenu(); doExport(b)">
                  {{ editingKey === regenKey(b) ? '⇩ 导出草稿' : '⇩ 导出 yaml' }}
                </button>
              </div>
            </div>
          </div>
        </footer>

        <!-- Doctor 诊断结果:按卡展开。两档:声明级(默认) / 深度扫(加 reposRoot)。 -->
        <section v-if="doctorState[regenKey(b)]?.open" class="doctor-panel">
          <div class="doctor-head">
            <strong>🩺 诊断结果</strong>
            <span v-if="doctorState[regenKey(b)]?.reposRoot" class="doctor-mode deep">
              深度扫 · {{ doctorState[regenKey(b)]!.reposRoot }}
            </span>
            <span v-else class="doctor-mode">声明级</span>
            <div class="doctor-head-actions">
              <button
                class="btn btn-regen"
                :title="'填 reposRoot 做代码实态深度扫描(检测 8 种漂移)'"
                @click="toggleDoctorDeepScan(b)"
              >
                {{ doctorState[regenKey(b)]?.showReposRootInput ? '收起深度扫' : '+ 深度扫描代码' }}
              </button>
              <button class="btn btn-regen" @click="doctorState[regenKey(b)].open = false">收起</button>
            </div>
          </div>

          <!-- 深度扫输入行,用户填仓库根然后重诊断 -->
          <div v-if="doctorState[regenKey(b)]?.showReposRootInput" class="doctor-deep-row">
            <input
              v-model="doctorState[regenKey(b)]!.reposRoot"
              type="text"
              placeholder="~/code/all-repos 或绝对路径 /Users/xxx/repos"
              :disabled="doctorState[regenKey(b)]?.loading"
              class="doctor-deep-input"
            />
            <button class="btn btn-regen" :disabled="doctorState[regenKey(b)]?.loading" @click="pickDoctorReposRoot(b)">
              选目录…
            </button>
            <button
              class="btn primary"
              :disabled="doctorState[regenKey(b)]?.loading || !doctorState[regenKey(b)]?.reposRoot?.trim()"
              @click="runDoctorDeep(b)"
            >
              {{ doctorState[regenKey(b)]?.loading ? '扫描中…' : '跑深度扫描' }}
            </button>
          </div>

          <div v-if="doctorState[regenKey(b)]?.err" class="alert error">
            {{ doctorState[regenKey(b)]!.err }}
          </div>
          <div v-else-if="doctorState[regenKey(b)]?.issues?.length === 0" class="alert success">
            ✓ {{ doctorState[regenKey(b)]?.reposRoot ? '深度扫描未发现漂移' : '未发现声明漂移。想对比代码实态(检测 8 种漂移),点上方「+ 深度扫描代码」填仓库根' }}
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
          <label class="editor-label">system.yaml（改完先「预演」看改动列表，再「应用」写盘）</label>
          <textarea v-model="editorDraft" class="editor-textarea" spellcheck="false" />
          <div class="editor-actions">
            <button
              class="btn"
              :disabled="applyState[regenKey(b)]?.loading"
              @click="runApply(b, true)"
            >
              {{ applyState[regenKey(b)]?.loading && applyState[regenKey(b)]?.mode === 'dry' ? '预演中…' : '预演' }}
            </button>
            <button
              class="btn primary"
              :disabled="applyState[regenKey(b)]?.loading"
              @click="runApply(b, false)"
            >
              {{ applyState[regenKey(b)]?.loading && applyState[regenKey(b)]?.mode === 'real' ? '应用中…' : '应用到活 workspace' }}
            </button>
          </div>
          <div v-if="applyState[regenKey(b)]?.result" class="apply-result">
            <div class="apply-row"><strong>写入文件：</strong>{{ applyState[regenKey(b)]!.result!.files_written }}</div>
            <div v-if="applyState[regenKey(b)]!.result!.files_preserved?.length" class="apply-row">
              <strong>保留（用户手改）：</strong>
              <code v-for="f in applyState[regenKey(b)]!.result!.files_preserved" :key="f">{{ f }}</code>
            </div>
            <div v-if="applyState[regenKey(b)]!.result!.files_removed?.length" class="apply-row removed">
              <strong>移除（陈旧产物）：</strong>
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
  </div>
</template>

<style scoped>
/* .page / .page-header h1 / .subtitle / .btn / .btn.primary / .alert 来自 design.css */
.page-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 20px; }

.roots { background: #f8fafc; border: 1px solid #e2e8f0; border-radius: 8px; padding: 14px 16px; margin-bottom: 20px; }
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
.bot-target[data-target="embedded"],
.bot-target[data-target="standalone"] { background: #d1fae5; color: #065f46; } /* standalone 是 embedded 的老别名,保 CSS 兼容 */
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
/* embedded target(老名 standalone)的"打开对话"按钮:绿色强调,区分于管理类操作;
 * 现在所有 target 都有对话能力,不再是 embedded 专属,但绿色按钮样式保留。 */
.btn-chat {
  background: #d1fae5; border-color: #86efac; color: #065f46; font-weight: 600;
}
.btn-chat:hover:not(:disabled) { background: #a7f3d0; border-color: #4ade80; }

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

.doctor-deep-row {
  display: flex; gap: 6px; margin-bottom: 8px;
  padding: 8px 10px; background: #eff6ff; border: 1px solid #bfdbfe; border-radius: 6px;
}
.doctor-deep-input {
  flex: 1; padding: 6px 10px; font-size: 12px; font-family: monospace;
  border: 1px solid #cbd5e1; border-radius: 4px;
}
.doctor-deep-input:focus { outline: none; border-color: #3b82f6; }
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

.deploy-actions { display: flex; gap: 8px; justify-content: flex-end; }
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
