<script setup lang="ts">
import { onActivated, onMounted, onUnmounted, reactive, ref } from 'vue'
import {
  applyBot,
  discoverBots,
  doctor as bridgeDoctor,
  exportYAML,
  isDesktop as bridgeIsDesktop,
  uninstallBot,
} from '../lib/bridge'
import { Target } from '../lib/constants'
import type { ApplyResult, DiscoveredBot } from '../lib/bridge'
import { toast, toastError } from '../lib/toast'
import { confirmDialog } from '../lib/confirm'
import WorkspaceBrowser from '../components/WorkspaceBrowser.vue'
import BotCard from '../components/BotCard.vue'

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

// doctorSeverityIcon / doctorClassForSeverity 已搬到 BotCard.vue 内部

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
  } catch (e) {
    toastError(`${b.meta.system_id} 重新生成`, e)
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
  const message = target === Target.Openclaw
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
  } catch (e) {
    toastError('卸载', e)
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
  } catch (e) {
    toastError(`导出 ${b.meta.system_id}`, e)
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

function targetLabel(t: string): string {
  const map: Record<string, string> = {
    openclaw: 'OpenClaw',
    'claude-code': 'Claude Code',
    cursor: 'Cursor',
    codex: 'Codex CLI',
  }
  return map[t] ?? t
}

onMounted(() => { scan() })
// keep-alive 缓存了 BotsPage,InitPage 末步部署 → 切回 BotsPage 时组件不会重 mount,
// onMounted 里的 scan() 不跑,卸载/重装的状态变更看不到。onActivated 是 keep-alive
// 专属钩子,每次组件被激活(切回页面)都会触发 → 自动 rescan,卡片永远跟磁盘真态一致。
onActivated(() => { scan() })
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
      <BotCard
        v-for="b in bots"
        :key="b.path + b.meta.target"
        v-model:editor-draft="editorDraft"
        :bot="b"
        :doctor="doctorState[regenKey(b)]"
        :regen-loading="regenState[regenKey(b)]?.loading"
        :uninstall-loading="uninstallState[regenKey(b)]?.loading"
        :editing="editingKey === regenKey(b)"
        :apply="applyState[regenKey(b)]"
        :menu-open="menuOpenKey === regenKey(b)"
        :target-label="targetLabel"
        @run-doctor="runDoctor(b)"
        @close-doctor="doctorState[regenKey(b)].open = false"
        @open-browser="openBrowser(b)"
        @toggle-menu="toggleMenu(regenKey(b))"
        @close-menu="closeMenu"
        @regen="regen(b)"
        @toggle-editor="toggleEditor(b)"
        @do-export="doExport(b)"
        @uninstall="uninstall(b)"
        @run-apply="(dryRun: boolean) => runApply(b, dryRun)"
      />
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

/* .bot-card / .bot-head / .bot-target / .bot-stats / .bot-foot / .bot-menu /
   .doctor-* / .editor / .apply-* 全部 CSS 已搬到 components/BotCard.vue */
.bot-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(320px, 1fr)); gap: 14px; }
.page-actions { display: flex; gap: 8px; }

</style>
