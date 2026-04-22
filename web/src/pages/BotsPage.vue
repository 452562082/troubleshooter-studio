<script setup lang="ts">
import { ref, computed, nextTick, onMounted, onUnmounted, reactive, watch } from 'vue'
// Wails 运行时事件 API:Go 端 EventsEmit 推过来,这里 EventsOn 订阅。
// 注意 runtime.js 是 Wails 打进 app 的全局脚本,浏览器里 import 的效果是
// 引用 window.runtime.*;`tshoot serve` 模式下这些函数不会真实推事件(无源)。
import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime'
import {
  applyBot,
  cancelInstall,
  discoverBots,
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

function targetLabel(t: string): string {
  const map: Record<string, string> = {
    openclaw: 'OpenClaw',
    'claude-code': 'Claude Code',
    cursor: 'Cursor',
    standalone: 'Standalone',
  }
  return map[t] ?? t
}

// ── 导入 yaml → 部署 流程状态机 ──────────────────────────────────
// idle → picked → deploying → deployed → installing → done
type ImportStage = 'idle' | 'picked' | 'deploying' | 'deployed' | 'installing' | 'done'
const importStage = ref<ImportStage>('idle')
const importYAMLText = ref('')
const importYAMLPath = ref('')
const importTarget = ref<'openclaw' | 'claude-code' | 'cursor' | 'standalone'>('openclaw')
const importDestPath = ref('')
const importError = ref<string | null>(null)
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
    //   standalone:无凭证但要跑 venv setup,UI 简化成"直接安装"按钮
    //   claude-code / cursor:ImportAndApply 已 rsync,install.sh 无附加动作,直接齐活
    const needsInstall = importTarget.value === 'openclaw' || importTarget.value === 'standalone'
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

      <!-- Step 1: 选 target + destPath -->
      <div v-if="importStage === 'picked' || importStage === 'deploying'" class="deploy-step">
        <div class="deploy-field">
          <label>目标平台</label>
          <select v-model="importTarget" :disabled="importStage === 'deploying'">
            <option value="openclaw">OpenClaw（需要填凭证）</option>
            <option value="claude-code">Claude Code（装到项目根）</option>
            <option value="cursor">Cursor IDE（装到项目根）</option>
            <option value="standalone">Standalone（本机 venv / Docker）</option>
          </select>
        </div>
        <div class="deploy-field">
          <label>部署目标路径</label>
          <div class="path-row">
            <input
              v-model="importDestPath"
              :placeholder="importTarget === 'openclaw' ? '如 ./dist/shop（会创建产物目录）' : '项目根或工作目录'"
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
          install.sh 不需要凭证（{{ importTarget === 'standalone' ? 'venv 设置 + pip install' : '纯文件安装' }}），点下面按钮直接安装。
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
            <button
              class="btn btn-regen"
              :disabled="regenState[regenKey(b)]?.loading"
              :title="b.meta.system_yaml ? '' : 'tshoot.json 里没保存 system_yaml，无法原地重 gen'"
              @click="regen(b)"
            >
              {{ regenState[regenKey(b)]?.loading ? '生成中…' : '重新生成' }}
            </button>
            <button class="btn btn-regen" :title="'编辑 yaml + 应用到活 workspace'" @click="toggleEditor(b)">
              {{ editingKey === regenKey(b) ? '收起' : '编辑配置' }}
            </button>
            <button
              class="btn btn-regen"
              :title="editingKey === regenKey(b) ? '导出当前编辑中的草稿' : '导出活 workspace 的 system.yaml'"
              @click="doExport(b)"
            >
              导出 yaml
            </button>
          </div>
        </footer>

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
.bot-target[data-target="standalone"] { background: #d1fae5; color: #065f46; }
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
