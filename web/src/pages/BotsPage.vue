<script setup lang="ts">
import { ref, computed, onMounted, reactive } from 'vue'
import type { ApplyResult, DiscoveredBot } from '../types/wails'
import {
  applyBot,
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
import type { InstallPrompt } from '../types/wails'

const bots = ref<DiscoveredBot[]>([])
const loading = ref(false)
const error = ref<string | null>(null)
const extraRoots = ref<string[]>([])
const newRootInput = ref('')

// 每张卡片的"重 gen"状态：key = path|target
const regenState = reactive<Record<string, { loading: boolean; ok?: string; err?: string }>>({})

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
    if (!yamlText) throw new Error('这个机器人的 tshoot.json 里没有 system_yaml（缺 system_yaml 字段，无法原地重 gen）')
    const res = await bridgeGen(yamlText, '')
    const outDir = String(res?.output_dir || '未知输出路径')
    regenState[k] = { loading: false, ok: `产物已写入 ${outDir}` }
  } catch (e: any) {
    regenState[k] = { loading: false, err: String(e?.message || e) }
  }
}

function toggleEditor(b: DiscoveredBot) {
  const k = regenKey(b)
  if (editingKey.value === k) {
    editingKey.value = null
    return
  }
  if (!b.meta.system_yaml) {
    error.value = '这个机器人没有嵌入 system_yaml，无法编辑（缺 system_yaml 字段）'
    return
  }
  editingKey.value = k
  editorDraft.value = b.meta.system_yaml
  delete applyState[k]
}

// 每张卡片的导出状态（避免影响 apply / regen 区）
const exportState = reactive<Record<string, { ok?: string; err?: string }>>({})

async function doExport(b: DiscoveredBot) {
  const k = regenKey(b)
  exportState[k] = {}
  try {
    const yamlText = b.meta.system_yaml
    if (!yamlText) throw new Error('这个机器人没有嵌入 system_yaml（缺 system_yaml 字段）')
    // 用编辑器里的草稿（如果当前在编辑）优先导，否则导存盘版本
    const payload = editingKey.value === k ? editorDraft.value : yamlText
    const filename = `${b.meta.system_id || 'system'}.yaml`
    const savedTo = await exportYAML(filename, payload)
    if (!savedTo) {
      exportState[k] = {} // user canceled, no message
      return
    }
    exportState[k] = { ok: `已导出到 ${savedTo}` }
  } catch (e: any) {
    exportState[k] = { err: String(e?.message || e) }
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

async function runDeployInstall() {
  importError.value = null
  importStage.value = 'installing'
  installLog.value = ''
  try {
    const r = await runInstall(importedOutputDir.value, installCreds.value)
    installLog.value = r.log
    installOK.value = r.ok
    importStage.value = 'done'
    await scan()
  } catch (e: any) {
    importError.value = String(e?.message || e)
    importStage.value = 'deployed'
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

onMounted(scan)
</script>

<template>
  <div class="page">
    <header class="page-header">
      <div>
        <h1>已装机器人</h1>
        <p class="subtitle">扫描本机 tshoot.json 锚点，列出已经部署到 AI 平台的排障机器人。</p>
      </div>
      <div class="page-actions">
        <button class="btn" :disabled="importStage !== 'idle'" @click="pickYAML">
          导入 yaml 部署
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
        </div>
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
      还没部署过机器人。点右上角「<strong>导入 yaml 部署</strong>」拿已有 yaml 直接装；或去「<strong>创建向导</strong>」从头建一份。
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
        <p v-if="regenState[regenKey(b)]?.ok" class="regen-ok">✓ {{ regenState[regenKey(b)]?.ok }}</p>
        <p v-if="regenState[regenKey(b)]?.err" class="regen-err">⚠ {{ regenState[regenKey(b)]?.err }}</p>
        <p v-if="exportState[regenKey(b)]?.ok" class="regen-ok">✓ {{ exportState[regenKey(b)]?.ok }}</p>
        <p v-if="exportState[regenKey(b)]?.err" class="regen-err">⚠ {{ exportState[regenKey(b)]?.err }}</p>

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
.page { padding: 24px 28px; max-width: 1100px; }
.page-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 20px; }
.page-header h1 { font-size: 22px; font-weight: 600; color: #0f172a; }
.subtitle { font-size: 13px; color: #64748b; margin-top: 4px; }

.btn {
  padding: 8px 16px; border: 1px solid #cbd5e1; border-radius: 6px;
  background: #fff; color: #334155; font-size: 13px; cursor: pointer;
}
.btn:hover { background: #f1f5f9; }
.btn.primary { background: #0f172a; color: #fff; border-color: #0f172a; }
.btn.primary:hover { background: #1e293b; }
.btn:disabled { opacity: 0.5; cursor: not-allowed; }

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

.alert { padding: 12px 14px; border-radius: 6px; font-size: 13px; margin-bottom: 16px; }
.alert.error { background: #fef2f2; border: 1px solid #fecaca; color: #991b1b; }
.alert.info { background: #eff6ff; border: 1px solid #bfdbfe; color: #1e40af; }

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

.regen-ok { margin-top: 8px; font-size: 11px; color: #059669; word-break: break-all; }
.regen-err { margin-top: 8px; font-size: 11px; color: #b91c1c; word-break: break-all; }

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

.deploy-result {
  padding: 10px 12px; border-radius: 4px; font-size: 13px; font-weight: 600;
}
.deploy-result.ok { background: #f0fdf4; color: #166534; border: 1px solid #bbf7d0; }
.deploy-result.err { background: #fef2f2; color: #991b1b; border: 1px solid #fecaca; }
.deploy-log {
  background: #0f172a; color: #e2e8f0; padding: 12px; border-radius: 6px;
  font-family: 'SFMono-Regular', Menlo, monospace; font-size: 11px;
  max-height: 320px; overflow: auto; white-space: pre-wrap; word-break: break-all;
}
</style>
