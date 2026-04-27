<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import yaml from 'js-yaml'
import { analyze as bridgeAnalyze, isDesktop, openDir, type AnalyzeResult } from '../lib/bridge'
import { toast } from '../lib/toast'
import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime'

const exampleYaml = `system:
  id: demo
  name: "Demo"
  description: "示例系统"

agent:
  name: "Demo排障机器人"
  workspace_name: demo-bot
  model: anthropic/claude-sonnet-4-6

environments:
  - id: dev
    api_domain: api-dev.demo.com
    is_prod: false
  - id: prod
    api_domain: api.demo.com
    is_prod: true

repos:
  - name: demo-api
    url: git@github.com:demo/demo-api.git
    role: backend
    stack: go
    env_branches:
      dev: develop
      prod: main

infrastructure:
  config_center:
    type: nacos
    endpoints:
      - env: dev
        addr: nacos-dev:8848
      - env: prod
        addr: nacos-prod:8848
  observability:
    grafana: {enabled: true, url_by_env: {dev: "http://grafana-dev", prod: "http://grafana-prod"}}
    loki: {enabled: true, via_grafana: true}
    prometheus: {enabled: true, via_grafana: true}
  data_stores:
    - {type: redis, enabled: true, discovery: from_config_center, readonly_enforced: true}
    - {type: mongodb, enabled: false}
    - {type: elasticsearch, enabled: false}
  messaging: []
  project_tracking: []

generation:
  target_host: openclaw
  output_dir: ./dist/demo
  skills_whitelist: [routing, config-executor, redis-runtime-query, diagram-generator]
  preserve_on_regenerate: [SOUL.md]

meta:
  schema_version: "0.1"
`

function loadExample() {
  yamlContent.value = exampleYaml
  error.value = ''
  result.value = null
}

const yamlContent = ref('')
const reposRoot = ref('')
const autoClone = ref(false)
const loading = ref(false)
const result = ref<any>(null)
const error = ref('')

function loadFile(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0]
  if (!file) return
  const reader = new FileReader()
  reader.onload = () => { yamlContent.value = reader.result as string }
  reader.readAsText(file)
}

// ── yaml vs 代码实态 diff ──
// 跑完 analyze 用户最想知道的是"跟我 yaml 里写的比,差了啥":
//   - new:代码里有 service_names 但 yaml 没声明(可能漏配,复制 yaml 片段提醒用户加)
//   - missing:yaml 声明了但 analyzer 没扫到(可能写错仓库名 / service 不存在了 / 代码没 clone)
//   - config-center 冲突:yaml 说 nacos,代码扫出 apollo
// 纯前端算:yaml 用 js-yaml parse,result.report.repos 按 name 匹配,集合运算。
interface YamlVsCodeDiffItem {
  name: string
  yamlServices: string[]
  codeServices: string[]
  newInCode: string[]       // 代码有 yaml 无
  missingInCode: string[]   // yaml 有代码无
}
interface YamlVsCodeDiff {
  repos: YamlVsCodeDiffItem[]
  configCenterYaml: string
  configCenterCode: string
  configCenterMismatch: boolean
  totalNew: number          // 所有仓库加起来 new 数
  totalMissing: number
}

const diff = computed<YamlVsCodeDiff | null>(() => {
  if (!result.value) return null
  let yamlCfg: any = {}
  try { yamlCfg = yaml.load(yamlContent.value) || {} } catch { return null }
  const yamlRepos = Array.isArray(yamlCfg.repos) ? yamlCfg.repos : []
  const codeRepos = result.value.report?.repos || []
  const out: YamlVsCodeDiffItem[] = []
  let totalNew = 0
  let totalMissing = 0
  for (const yRepo of yamlRepos) {
    const yName = yRepo.name as string
    // yaml 里 service_names 可能是 "a, b" 字符串或 ["a", "b"] 数组,归一
    const yServicesRaw = yRepo.service_names
    const yServices: string[] = Array.isArray(yServicesRaw)
      ? yServicesRaw.map((s: any) => String(s).trim()).filter(Boolean)
      : typeof yServicesRaw === 'string'
      ? yServicesRaw.split(',').map((s: string) => s.trim()).filter(Boolean)
      : []
    // yaml 里没写 service_names 时,约定用 repo.name 兜底(跟 generator 对齐)
    const effectiveYaml = yServices.length > 0 ? yServices : [yName]
    const codeEntry = codeRepos.find((r: any) => r.name === yName)
    const cServices: string[] = codeEntry?.service_names || []
    const ySet = new Set(effectiveYaml)
    const cSet = new Set(cServices)
    const newIn = cServices.filter((s) => !ySet.has(s))
    const miss = effectiveYaml.filter((s) => !cSet.has(s))
    totalNew += newIn.length
    totalMissing += miss.length
    out.push({
      name: yName,
      yamlServices: effectiveYaml,
      codeServices: cServices,
      newInCode: newIn,
      missingInCode: miss,
    })
  }
  const configCenterYaml = String(yamlCfg.infrastructure?.config_center?.type || '')
  const configCenterCode = String(result.value.report?.config_center || '')
  return {
    repos: out,
    configCenterYaml,
    configCenterCode,
    configCenterMismatch:
      configCenterYaml !== '' && configCenterCode !== '' &&
      configCenterCode !== 'unknown' && configCenterYaml !== configCenterCode,
    totalNew,
    totalMissing,
  }
})

// 一键复制 yaml 片段:方便用户粘到自己 yaml 里更新 service_names
function copySuggestedYamlSnippet() {
  if (!diff.value) return
  const lines: string[] = ['# 建议更新 repos[].service_names (基于分析器发现):']
  for (const r of diff.value.repos) {
    if (r.codeServices.length === 0) continue
    lines.push(`  - name: ${r.name}`)
    lines.push(`    service_names: [${r.codeServices.map((s) => `"${s}"`).join(', ')}]`)
  }
  navigator.clipboard.writeText(lines.join('\n'))
    .then(() => toast.success('片段已复制到剪贴板,粘到你的 yaml 对应 repo 下'))
    .catch(() => toast.error('复制失败'))
}

// analyze:log 事件流(analyzerpipe.OnProgress 每行 EventsEmit)
const progressLog = ref('')
// 跑 analyze 是长任务(大仓库 + auto-clone 可能跑分钟级),秒表让用户知道没卡。
// 目前 analyzerpipe 没 ctx 支持,不给 cancel 按钮(避免假承诺),只展示进度。
const analyzeStartTime = ref<number | null>(null)
const analyzeElapsed = ref(0)
let analyzeTimer: number | null = null

async function pickReposRoot() {
  if (!isDesktop()) { error.value = '选目录需要桌面 app 环境'; return }
  try {
    const p = await openDir('选择仓库根目录(含多个 repo.name 子目录)')
    if (p) reposRoot.value = p
  } catch (e: any) {
    error.value = String(e?.message || e)
  }
}

async function runAnalyze() {
  if (!yamlContent.value.trim()) { error.value = '请先填写或加载 system.yaml'; return }
  if (!reposRoot.value.trim()) { error.value = '请填写仓库根目录路径'; return }
  if (!isDesktop()) {
    error.value = 'Analyze 仅在桌面 app 可用;浏览器 tshoot serve 模式请改用 CLI:\n  tshoot analyze -i <yaml> --repos-root ... -o analysis.json'
    return
  }
  loading.value = true
  error.value = ''
  result.value = null
  progressLog.value = ''
  analyzeStartTime.value = Date.now()
  analyzeElapsed.value = 0
  if (analyzeTimer) clearInterval(analyzeTimer)
  analyzeTimer = window.setInterval(() => {
    if (analyzeStartTime.value) {
      analyzeElapsed.value = Math.floor((Date.now() - analyzeStartTime.value) / 1000)
    }
  }, 1000)
  try {
    const r = (await bridgeAnalyze(yamlContent.value, reposRoot.value, autoClone.value)) as AnalyzeResult
    result.value = r
    toast.success(`analyze 完成: ${r.per_repo?.length ?? 0} 个仓库,共 ${r.report?.repos?.length ?? 0} 条 report`)
  } catch (e: any) {
    error.value = e.message || String(e)
    toast.error(`analyze 失败: ${e.message || e}`)
  } finally {
    loading.value = false
    if (analyzeTimer) { clearInterval(analyzeTimer); analyzeTimer = null }
    analyzeStartTime.value = null
  }
}

onMounted(() => {
  EventsOn('analyze:log', (line: string) => {
    progressLog.value += line + '\n'
  })
})
onUnmounted(() => {
  EventsOff('analyze:log')
  if (analyzeTimer) { clearInterval(analyzeTimer); analyzeTimer = null }
})
</script>

<template>
  <div class="page">
    <h1>仓库分析</h1>

    <div class="info-box">
      <div class="info-box-title">使用说明</div>
      <div>
        输入 system.yaml + <strong>仓库父目录</strong>(含多个 repo.name 子目录),
        分析器会按 yaml 里每个 <code>repos[].name</code> 去匹配
        <code>&lt;父目录&gt;/&lt;name&gt;/</code>,扫所有匹配到的仓库,抽 service_names
        + 配置中心线索,结果标 verified(机械确认)。<br/>
        示例:yaml 里 repos=[<code>order-service</code>, <code>pay-service</code>],父目录 <code>~/code</code>,
        会扫 <code>~/code/order-service/</code> 和 <code>~/code/pay-service/</code> 两个仓库。
      </div>
      <div class="info-box-note">注意:缺失的仓库会标 skipped;勾"自动 clone"缺失仓库会从 url 浅克隆(需 git + 凭证)</div>
    </div>

    <div class="form-section">
      <div class="label-row">
        <label>system.yaml</label>
        <div class="label-row-actions">
          <label class="btn small">加载文件 <input type="file" accept=".yaml,.yml" @change="loadFile" hidden /></label>
          <button class="btn small" @click="loadExample">加载示例</button>
        </div>
      </div>
      <textarea v-model="yamlContent" placeholder="粘贴 system.yaml 内容..." spellcheck="false" :class="{ err: error }" />
    </div>

    <div class="form-row">
      <div class="field">
        <label>仓库父目录 <span class="field-hint">(含多个仓库子目录,不是某一个仓库的根)</span></label>
        <!-- readonly + 按钮:跟 wizard 所有路径字段一致的强约束,避免用户手写打错 / 路径不存在 -->
        <div class="path-row">
          <input
            :value="reposRoot"
            type="text"
            placeholder="点右侧按钮选「多个仓库的父目录」(如 ~/code),analyzer 按 yaml 里每个 repo.name 找子目录"
            readonly
            class="path-readonly"
            :title="reposRoot"
          />
          <button type="button" class="btn" :disabled="loading" @click="pickReposRoot">
            {{ reposRoot ? '重新选…' : '选目录…' }}
          </button>
        </div>
      </div>
      <div class="field check">
        <label><input type="checkbox" v-model="autoClone" /> 自动 clone 缺失仓库</label>
      </div>
    </div>

    <button class="btn accent" @click="runAnalyze" :disabled="loading">
      {{ loading ? '分析中...' : '运行分析' }}
    </button>

    <div v-if="error" class="alert error">{{ error }}</div>

    <!-- 运行时秒表 + 进度行数:跑大仓库 + autoClone 可能分钟级,让用户确信没卡 -->
    <div v-if="loading" class="analyze-progress">
      <span class="analyze-spinner" aria-hidden="true"></span>
      <span class="analyze-elapsed">已运行 {{ analyzeElapsed }}s</span>
      <span class="analyze-loglines">· {{ progressLog.split('\n').length - 1 }} 行进度</span>
    </div>
    <!-- 实时进度日志(analyze:log 事件) -->
    <pre v-if="loading && progressLog" class="progress-log">{{ progressLog }}</pre>

    <!-- 分析结果 -->
    <div v-if="result" class="results">
      <div class="summary-bar">
        <span class="tag blue">config_center: {{ result.report?.config_center || '-' }}</span>
        <span class="tag green">{{ result.report?.repos?.length || 0 }} 个仓库有 findings</span>
        <span class="tag gray">{{ result.per_repo?.length || 0 }} 个仓库处理过</span>
      </div>

      <!-- 每仓库状态摘要(per_repo) -->
      <div v-if="result.per_repo?.length" class="per-repo-grid">
        <div
          v-for="rs in result.per_repo"
          :key="rs.name"
          class="repo-status"
          :class="rs.status"
        >
          <span class="name">{{ rs.name }}</span>
          <span class="status-tag">{{ rs.status }}</span>
          <span v-if="rs.service_name_count" class="muted">{{ rs.service_name_count }} svc</span>
          <span v-if="rs.finding_count" class="muted">{{ rs.finding_count }} findings</span>
          <span v-if="rs.error" class="err">{{ rs.error }}</span>
        </div>
      </div>

      <!-- yaml vs 代码实态 diff:这是用户真正想看的"发现了啥没在 yaml 里" -->
      <div v-if="diff" class="card diff-card">
        <div class="card-header">
          <span class="name">⚖️ 跟 yaml 声明对比</span>
          <span v-if="diff.totalNew > 0" class="tag green">{{ diff.totalNew }} 处新发现</span>
          <span v-if="diff.totalMissing > 0" class="tag orange">{{ diff.totalMissing }} 处声明但没扫到</span>
          <span v-if="diff.configCenterMismatch" class="tag red">config_center 不一致</span>
          <span v-if="diff.totalNew === 0 && diff.totalMissing === 0 && !diff.configCenterMismatch" class="tag green">完全一致</span>
          <button
            v-if="diff.totalNew > 0"
            class="btn small"
            title="复制建议的 yaml 片段到剪贴板,粘进自己 system.yaml 的 repos 下就能补全 service_names"
            @click="copySuggestedYamlSnippet"
            style="margin-left:auto"
          >
            📋 复制建议 yaml 片段
          </button>
        </div>

        <div v-if="diff.configCenterMismatch" class="detail warn">
          <strong>⚠ 配置中心类型冲突:</strong>
          yaml 声明 <code>{{ diff.configCenterYaml || '(空)' }}</code>,代码扫出 <code>{{ diff.configCenterCode }}</code>。
          检查 infrastructure.config_center.type 是否要改。
        </div>

        <div v-for="r in diff.repos" :key="r.name" class="diff-row">
          <div class="diff-row-head">
            <strong>{{ r.name }}</strong>
            <span class="muted">yaml 声明 {{ r.yamlServices.length }} · 代码扫出 {{ r.codeServices.length }}</span>
          </div>
          <div v-if="r.newInCode.length" class="detail">
            <span class="tag green" style="min-width: 80px;">代码有 yaml 无</span>
            <span v-for="s in r.newInCode" :key="s" class="tag blue">{{ s }}</span>
          </div>
          <div v-if="r.missingInCode.length" class="detail">
            <span class="tag orange" style="min-width: 80px;">yaml 有但没扫到</span>
            <span v-for="s in r.missingInCode" :key="s" class="tag gray">{{ s }}</span>
          </div>
          <div v-if="r.newInCode.length === 0 && r.missingInCode.length === 0" class="detail muted">
            ✓ 一致
          </div>
        </div>
      </div>

      <!-- 详细 findings(report.repos) -->
      <div v-for="repo in result.report?.repos || []" :key="repo.name" class="card">
        <div class="card-header">
          <span class="name">{{ repo.name }}</span>
          <span v-if="repo.stack" class="tag gray">{{ repo.stack }}</span>
          <span v-if="repo.verified" class="tag green">verified</span>
        </div>

        <div v-if="repo.service_names?.length" class="detail">
          <strong>Services：</strong>
          <span v-for="s in repo.service_names" :key="s" class="tag blue">{{ s }}</span>
        </div>

        <div v-if="repo.findings?.length" class="detail">
          <strong>Findings（{{ repo.findings.length }}）：</strong>
          <div v-for="(f, i) in repo.findings" :key="i" class="finding">
            <span class="src">{{ f.source_file }}</span>
            <span v-if="f.data_id" class="kv">dataId={{ f.data_id }}</span>
            <span v-if="f.namespace_id" class="kv">ns={{ f.namespace_id }}</span>
            <span v-if="f.group" class="kv">group={{ f.group }}</span>
            <span v-if="f.app_id" class="kv">appId={{ f.app_id }}</span>
            <span v-if="f.kv_prefix" class="kv">prefix={{ f.kv_prefix }}</span>
            <span v-if="f.env_profile" class="tag orange">{{ f.env_profile }}</span>
          </div>
        </div>

        <div v-if="repo.warnings?.length" class="detail warn">
          <strong>Warnings：</strong>
          <div v-for="w in repo.warnings" :key="w" class="warn-line">{{ w }}</div>
        </div>

        <div v-if="!repo.findings?.length && !repo.warnings?.length" class="detail muted">
          无 findings 和 warnings
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* .page / .btn / .info-box* 来自 design.css;这里只写个性化 */

.label-row-actions { display: flex; gap: 6px; }
.form-section { margin-bottom: var(--sp-4); }
.label-row { display: flex; justify-content: space-between; align-items: center; margin-bottom: 6px; }
/* 必须用 `>`(直接子)而非后代选择器:
 * 结构是
 *   .label-row
 *     <label>system.yaml</label>           ← 表单 label,要 600 粗 + 14px
 *     .label-row-actions
 *       <label class="btn small">加载文件  ← 这也是 label,但走按钮样式
 *       <button class="btn small">加载示例
 * `.label-row label`(后代)会把里面的 <label class="btn small"> 也抓上,
 * 按 CSS 特异性它 (0,1,1) 赢过 .btn (0,1,0) 的 font-weight:500,结果"加载
 * 文件"变 600 粗,"加载示例"还是 500 —— 两个按钮字形看起来就不一样了。
 * 用 `>` 只命中直接子 label,不泄漏到 .label-row-actions 里的 .btn。 */
.label-row > label { font-weight: 600; color: var(--c-text); font-size: var(--fs-md); }
/* 文件 input 用 <label class="btn small"> 当触发器,需要 overflow/display 让嵌套 input hidden 不影响布局 */
label.btn { cursor: pointer; }

textarea {
  width: 100%; min-height: 180px; padding: 10px var(--sp-3); border: 1px solid var(--c-line); border-radius: var(--r-md);
  font-family: 'SF Mono', 'Fira Code', monospace; font-size: var(--fs-base); line-height: 1.5;
  background: var(--c-surf-2); resize: vertical; box-sizing: border-box;
}
textarea:focus { outline: none; border-color: var(--c-accent); }
textarea.err { border-color: #ef4444; }

.form-row { display: flex; gap: var(--sp-4); margin-bottom: var(--sp-4); align-items: flex-end; }
.field { flex: 1; }
.field label { display: block; font-weight: 600; color: var(--c-text); margin-bottom: 6px; font-size: var(--fs-md); }
.field input[type="text"] {
  width: 100%; padding: 8px var(--sp-3); border: 1px solid var(--c-line-2); border-radius: var(--r-md);
  font-size: var(--fs-md); box-sizing: border-box;
}
.field input[type="text"]:focus { outline: none; border-color: var(--c-accent); }
.field.check { flex: none; }
.field.check label { font-weight: 400; font-size: var(--fs-md); color: var(--c-text); cursor: pointer; display: flex; align-items: center; gap: 6px; }
/* "父目录,不是单仓库根" 这类轻提示放 label 旁,font-weight 400 + 灰色不抢主 label */
.field-hint { font-size: var(--fs-sm); font-weight: 400; color: var(--c-muted); margin-left: 6px; }
/* 输入框 + "选目录…"按钮同行 */
.path-row { display: flex; gap: 6px; }
.path-row input[type="text"] { flex: 1; }
/* readonly 路径:灰底 + 默认鼠标,跟 wizard 其它路径字段视觉统一 */
input.path-readonly {
  background: #f8fafc !important;
  color: #475569 !important;
  cursor: default;
  text-overflow: ellipsis;
}

/* banner 用旧 class 名,但样式等价于 .alert.error;保留 class 避免改 template */
.banner { margin-top: var(--sp-3); padding: 10px var(--sp-3); border-radius: var(--r-md); font-size: var(--fs-base); }

.results { margin-top: var(--sp-5); }
.summary-bar { display: flex; gap: var(--sp-2); margin-bottom: var(--sp-4); }

.tag {
  display: inline-block; padding: 3px 10px; border-radius: 12px; font-size: 12px; font-weight: 500;
}
.tag.blue { background: #dbeafe; color: #1e40af; }
.tag.green { background: #d1fae5; color: #065f46; }
.tag.orange { background: #fef3c7; color: #92400e; }
.tag.gray { background: #f1f5f9; color: #475569; }

.name { font-weight: 700; color: #1e293b; font-size: 15px; }

.detail { margin-bottom: 8px; font-size: 13px; color: #475569; }
.detail strong { color: #334155; margin-right: 6px; }
.detail.muted { color: #94a3b8; font-style: italic; }

.finding {
  display: flex; flex-wrap: wrap; gap: 6px; padding: 4px 0; border-bottom: 1px solid #f1f5f9; align-items: center;
}
.src { font-family: monospace; font-size: 12px; color: #3b82f6; }
.kv { font-family: monospace; font-size: 12px; background: #f1f5f9; padding: 1px 6px; border-radius: 3px; }

.detail.warn { color: #92400e; }
.warn-line { font-size: 12px; padding: 2px 0; }

/* yaml vs 代码 diff 卡片:红框不合适(不是错),用蓝色强调,tag 并排展示差异 */
.diff-card {
  border-left: 4px solid #3b82f6;
  background: #eff6ff;
}
.diff-card .card-header {
  display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
}
.tag.red { background: #fee2e2; color: #991b1b; }
.diff-row {
  margin-top: 10px; padding-top: 10px;
  border-top: 1px dashed #bfdbfe;
}
.diff-row-head {
  display: flex; align-items: baseline; gap: 8px; margin-bottom: 6px;
}
.diff-row-head strong { color: #1e40af; }
.diff-row-head .muted { font-size: 12px; color: #64748b; }
.diff-row .detail { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; margin-bottom: 4px; }

/* 运行时秒表 —— 跟 BotsPage install 的进度条视觉对齐 */
.analyze-progress {
  display: flex; align-items: center; gap: 8px;
  margin-top: 12px; padding: 8px 12px; background: #eff6ff; border: 1px solid #bfdbfe;
  border-radius: 6px; font-size: 12px; color: #1e40af;
  font-variant-numeric: tabular-nums;
}
.analyze-spinner {
  width: 12px; height: 12px; border-radius: 50%;
  border: 2px solid #bfdbfe; border-top-color: #2563eb;
  animation: spin 0.8s linear infinite;
}
@keyframes spin { to { transform: rotate(360deg); } }
.analyze-elapsed { font-weight: 600; }
.analyze-loglines { color: #64748b; }

/* 新增:实时进度日志 + 每仓库摘要 grid */
.progress-log {
  background: #0f172a; color: #e2e8f0; padding: 10px 12px; border-radius: 6px;
  font-family: 'SFMono-Regular', Menlo, monospace; font-size: 11px;
  max-height: 240px; overflow: auto; white-space: pre-wrap; word-break: break-all;
  margin-top: 12px; border-left: 3px solid #22c55e;
}
.per-repo-grid {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
  gap: 8px; margin: 12px 0;
}
.repo-status {
  display: flex; gap: 6px; align-items: center; flex-wrap: wrap;
  padding: 6px 10px; border-radius: 4px; font-size: 12px;
  background: #f8fafc; border: 1px solid #e2e8f0;
}
.repo-status .name { font-weight: 600; flex: 1; }
.repo-status .status-tag { font-family: monospace; font-size: 10px; padding: 1px 6px; background: #e2e8f0; border-radius: 3px; }
.repo-status .muted { color: #64748b; font-size: 11px; }
.repo-status .err { color: #b91c1c; font-size: 11px; }
.repo-status.analyzed { border-color: #86efac; }
.repo-status.cloned-then-analyzed { border-color: #60a5fa; }
.repo-status.skipped { opacity: 0.7; }
.repo-status.clone-failed { background: #fef2f2; border-color: #fecaca; }
</style>
