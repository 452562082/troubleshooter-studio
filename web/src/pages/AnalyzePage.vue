<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import yaml from 'js-yaml'
import { analyze as bridgeAnalyze, isDesktop, openDir, openYAML, type AnalyzeResult } from '../lib/bridge'
import { toast } from '../lib/toast'
import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime'

const router = useRouter()
// 跟"YAML 沙盒"页对接的 localStorage key:在 EditorPage.vue 同名常量(两边手动同步)。
// 用户在本页点"应用到 YAML 沙盒"时,把 patch 过的 yaml 写进 key,然后路由跳过去,
// 沙盒页 onMounted 读取 + 自动填 + 显示 banner。读完即清。
const PENDING_FROM_ANALYZE_KEY = 'tsf-pending-yaml-from-analyze'

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
  targets: [openclaw]
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

// 桌面 app 走 Wails 原生 osascript 对话框(reliable on macOS WKWebView);
// 浏览器模式回退 <input type="file"> + FileReader。
async function loadFileNative() {
  if (!isDesktop()) return
  try {
    const r = await openYAML()
    if (r && r.path) yamlContent.value = r.content || ''
  } catch (e: any) {
    error.value = `加载文件失败: ${String(e?.message || e)}`
  }
}
function loadFileBrowser(e: Event) {
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
  // role 决定是否需要 service_names 对账。infra / common-lib / docs / frontend / mobile
  // 不是业务服务,本来就不该有 service_names —— diff 跳过 missing/new 检查,只展示一行说明。
  isServiceRole: boolean
  effectiveRole: string
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
  // 业务服务角色判定 —— 跟前端 InitPage / 后端 RequiresServiceNames 一致。
  // 仅这 4 类需要对账 service_names,其它角色(frontend / mobile / common-lib / infra / docs)
  // 跳过 missing/new 计算 —— 它们本就不该有 service_names。
  const SERVICE_ROLES = new Set(['backend', 'gateway', 'middleware', 'admin'])
  for (const yRepo of yamlRepos) {
    const yName = yRepo.name as string
    // yaml 里 service_names 可能是 "a, b" 字符串或 ["a", "b"] 数组,归一
    const yServicesRaw = yRepo.service_names
    const yServices: string[] = Array.isArray(yServicesRaw)
      ? yServicesRaw.map((s: any) => String(s).trim()).filter(Boolean)
      : typeof yServicesRaw === 'string'
      ? yServicesRaw.split(',').map((s: string) => s.trim()).filter(Boolean)
      : []
    // role:yaml 显式给的优先,否则用 analyzer 推断的 role_hint(都没就当 backend 兜底)
    const codeEntry = codeRepos.find((r: any) => r.name === yName)
    const yamlRole = (typeof yRepo.role === 'string' && yRepo.role.trim()) ? yRepo.role.trim() : ''
    const hintedRole = (codeEntry?.role_hint?.role as string) || ''
    const effectiveRole = yamlRole || hintedRole || 'backend'
    const isServiceRole = SERVICE_ROLES.has(effectiveRole)
    // yaml 里没写 service_names 时,业务服务用 repo.name 兜底,非业务服务保持空(它们本来就不需要)
    const effectiveYaml = yServices.length > 0
      ? yServices
      : (isServiceRole ? [yName] : [])
    const cServices: string[] = codeEntry?.service_names || []
    let newIn: string[] = []
    let miss: string[] = []
    if (isServiceRole) {
      // 只对业务服务跑 missing/new 对账
      const ySet = new Set(effectiveYaml)
      const cSet = new Set(cServices)
      newIn = cServices.filter((s) => !ySet.has(s))
      miss = effectiveYaml.filter((s) => !cSet.has(s))
    }
    totalNew += newIn.length
    totalMissing += miss.length
    out.push({
      name: yName,
      yamlServices: effectiveYaml,
      codeServices: cServices,
      newInCode: newIn,
      missingInCode: miss,
      isServiceRole,
      effectiveRole,
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

// 非业务服务角色的解释文案 —— 不同 role 排障路径不同,提示也按 role 定制
const NON_SERVICE_ROLE_HINTS: Record<string, string> = {
  frontend: '前端 / 浏览器侧,排障从用户报告路径入手,不查后端配置中心或 k8s 部署',
  mobile: '移动端 app,排障从崩溃日志 / 版本分布入手,不进服务依赖图',
  'common-lib': '共享库 / SDK,作为版本对比目标存在,本身没有部署节点',
  infra: '基础设施声明仓(k8s manifest / terraform / helm 等),AI 排障时仅作为配置定义来源被引用',
  docs: '文档仓库,只作为背景资料,不参与运行时排障',
}
function nonServiceRoleHint(role: string): string {
  return NON_SERVICE_ROLE_HINTS[role] || '该角色不参与服务依赖图,yaml 中是否声明 service_names 不影响排障'
}

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

// applyDiffToYAMLAndOpen 把代码扫描发现的差异打进 yaml,然后跳到 YAML 沙盒。
// 修改点:
//   1. 每个 repo 的 service_names 用代码扫到的 union(yaml + code 取并集,信息从不丢)
//   2. infrastructure.config_center.type 跟代码扫到不一致时,提醒(注释行),不强改
//   完事写进 PENDING_FROM_ANALYZE_KEY → router.push('/editor') → 沙盒页接管。
function applyDiffToYAMLAndOpen() {
  if (!diff.value || !result.value) {
    toast.error('请先成功跑一次扫描')
    return
  }
  let cfg: any
  try {
    cfg = yaml.load(yamlContent.value) || {}
  } catch (e: any) {
    toast.error(`yaml 解析失败:${e?.message || e}`)
    return
  }
  if (!Array.isArray(cfg.repos)) cfg.repos = []
  // 按 repo.name 索引代码扫描结果,逐 repo 合并 service_names
  const codeReposByName = new Map<string, any>()
  for (const r of result.value.report?.repos || []) {
    if (r?.name) codeReposByName.set(r.name, r)
  }
  let touchedRepos = 0
  for (const yRepo of cfg.repos) {
    const codeEntry = codeReposByName.get(yRepo.name)
    if (!codeEntry) continue
    const codeNames: string[] = codeEntry.service_names || []
    if (codeNames.length === 0) continue
    // yaml 里 service_names 可能是 string[] / "a,b" string,归一为 string[]
    const yamlNames: string[] = Array.isArray(yRepo.service_names)
      ? yRepo.service_names.map((s: any) => String(s).trim()).filter(Boolean)
      : typeof yRepo.service_names === 'string'
      ? yRepo.service_names.split(',').map((s: string) => s.trim()).filter(Boolean)
      : []
    const merged = Array.from(new Set([...yamlNames, ...codeNames]))
    if (merged.length !== yamlNames.length) {
      yRepo.service_names = merged
      touchedRepos++
    }
  }
  // config_center type 不同时,在文件顶部加一条注释行,不替换用户原值(可能是有意的)
  let header = ''
  if (diff.value.configCenterMismatch) {
    header = `# 代码扫描发现 config_center.type=${diff.value.configCenterCode}, 跟 yaml 声明 (${diff.value.configCenterYaml}) 不一致,请人工核对\n`
  }
  const patchedYAML = header + yaml.dump(cfg, { lineWidth: 200, noRefs: true })
  try {
    localStorage.setItem(PENDING_FROM_ANALYZE_KEY, patchedYAML)
  } catch (e: any) {
    toast.error(`保存到 localStorage 失败:${e?.message || e}`)
    return
  }
  if (touchedRepos === 0 && !diff.value.configCenterMismatch) {
    toast.info('代码扫描跟 yaml 已对齐,无需变更;已把 yaml 带到沙盒做最终验证')
  } else {
    toast.success(`已合并 ${touchedRepos} 个仓库的 service_names 到 yaml,跳到沙盒验证`)
  }
  router.push('/editor')
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

// 把 backend status enum 翻译成人话(原始值留在 title 里方便排查)。
// 跟 .repo-status.<status> 的 CSS class 对应,enum 改了两边都得跟。
function statusZh(s: string): string {
  switch (s) {
    case 'analyzed': return '已扫描'
    case 'cloned-then-analyzed': return '已 clone + 扫描'
    case 'skipped': return '跳过(本机没有)'
    case 'clone-failed': return 'clone 失败'
    case 'analyze-failed': return '扫描出错'
    default: return s
  }
}

// SERVICE_ROLES 与 yamlVsCode diff 共享:仅这 4 类业务服务才算"扫描失败 = 真问题"。
// 其它角色(common-lib / frontend / mobile / infra / docs)本就不需要源码扫描,
// status=skipped 是预期行为,不该刷红色 not-found。
const SERVICE_ROLES_SET = new Set(['backend', 'gateway', 'middleware', 'admin'])
function isSkippedNonService(rs: { status: string; role?: string }): boolean {
  if (rs.status !== 'skipped') return false
  const role = (rs.role || '').trim()
  if (!role) return false
  return !SERVICE_ROLES_SET.has(role)
}
function statusZhFor(rs: { status: string; role?: string }): string {
  if (isSkippedNonService(rs)) {
    // 把"跳过(本机没有)"的红色警告改成"无需扫描"的中性提示
    return `无需扫描(${rs.role})`
  }
  return statusZh(rs.status)
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
    <h1>代码扫描</h1>

    <div class="info-box">
      <div class="info-box-title">代码扫描 — 从源码反推 yaml 应该怎么写</div>
      <div class="info-box-body">
        <p class="info-box-lead">扫已 clone 到本机的代码,把"实际跑的样子"跟 yaml 声明对账,差异一键回填。</p>
        <ul class="info-box-actions">
          <li>
            <strong>🔍 自动识别</strong>
            <span>—— 每个仓库提供的服务名,以及配置中心(Nacos / Apollo / Consul / Kuboard)用到的 dataId / namespace / configmap 等线索</span>
          </li>
          <li>
            <strong>📊 差异对比</strong>
            <span>—— 跟 yaml 声明逐项对照:<code>missing</code>(yaml 漏写)/ <code>extra</code>(yaml 多写)/ <code>verified</code>(已对齐)分类一目了然</span>
          </li>
          <li>
            <strong>↩️ 一键回填</strong>
            <span>—— 把扫描出的差异合并回 yaml,自动跳到 <router-link to="/editor">YAML 沙盒</router-link> 做验证 + 健康检查</span>
          </li>
        </ul>
        <div class="info-box-inputs">
          <div class="info-box-inputs-title">📝 需要两样东西:</div>
          <ul>
            <li><strong>system.yaml</strong> — 粘贴或从文件加载</li>
            <li><strong>仓库父目录</strong> — 如 <code>~/code</code>,即 <code>repos[].name</code> 子目录的共同父目录</li>
          </ul>
        </div>
        <p class="info-box-redirect">
          ⚠️ 本机没下载的仓库会跳过;勾上「自动 clone」会按 yaml 里的 <code>url</code> 浅克隆再扫(需要本机有 git + 仓库访问凭证)。
        </p>
      </div>
    </div>

    <div class="form-section">
      <div class="label-row">
        <label>system.yaml</label>
        <div class="label-row-actions">
          <button v-if="isDesktop()" class="btn small" @click="loadFileNative">加载文件</button>
          <label v-else class="btn small">加载文件 <input type="file" accept=".yaml,.yml" @change="loadFileBrowser" hidden /></label>
          <button class="btn small" @click="loadExample">加载示例</button>
        </div>
      </div>
      <textarea v-model="yamlContent" placeholder="把 system.yaml 内容粘到这里,或点上面「加载文件」选本机文件…" spellcheck="false" :class="{ err: error }" />
    </div>

    <div class="form-row">
      <div class="field">
        <label>仓库父目录 <span class="field-hint">(选含多个仓库子目录的那一层,不是单个仓库的根)</span></label>
        <!-- readonly + 按钮:跟 wizard 所有路径字段一致的强约束,避免用户手写打错 / 路径不存在 -->
        <div class="path-row">
          <input
            :value="reposRoot"
            type="text"
            placeholder="点右边按钮选,通常是 ~/code 这种装着多个仓库的目录"
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
        <label><input type="checkbox" v-model="autoClone" /> 本机没有的仓库,自动 clone</label>
      </div>
    </div>

    <button class="btn accent" @click="runAnalyze" :disabled="loading">
      {{ loading ? '正在扫…' : '🔍 开始扫描' }}
    </button>

    <div v-if="error" class="alert error">{{ error }}</div>

    <!-- 运行时秒表 + 进度行数:跑大仓库 + autoClone 可能分钟级,让用户确信没卡 -->
    <div v-if="loading" class="analyze-progress">
      <span class="analyze-spinner" aria-hidden="true"></span>
      <span class="analyze-elapsed">已扫 {{ analyzeElapsed }} 秒</span>
      <span class="analyze-loglines">· 进度日志 {{ progressLog.split('\n').length - 1 }} 行</span>
    </div>
    <!-- 实时进度日志(analyze:log 事件) -->
    <pre v-if="loading && progressLog" class="progress-log">{{ progressLog }}</pre>

    <!-- 分析结果 -->
    <div v-if="result" class="results">
      <div class="summary-bar">
        <span class="tag blue">配置中心:{{ result.report?.config_center || '-' }}</span>
        <span class="tag green">扫到线索 {{ result.report?.repos?.length || 0 }} 个仓库</span>
        <span class="tag gray">共扫 {{ result.per_repo?.length || 0 }} 个仓库</span>
      </div>

      <!-- 每仓库状态摘要(per_repo)。skipped 状态分两类显示:
           - 业务服务角色(backend/gateway/middleware/admin)→ 红色 not-found 警告(真问题:本机没 clone)
           - 非业务服务角色(infra/common-lib/frontend/mobile/docs)→ 灰色"无需扫描"(预期行为) -->
      <div v-if="result.per_repo?.length" class="per-repo-grid">
        <div
          v-for="rs in result.per_repo"
          :key="rs.name"
          class="repo-status"
          :class="[rs.status, isSkippedNonService(rs) ? 'skipped-nonservice' : '']"
        >
          <span class="name">{{ rs.name }}</span>
          <span class="status-tag" :title="rs.status">{{ statusZhFor(rs) }}</span>
          <span v-if="rs.service_name_count" class="muted">服务 {{ rs.service_name_count }} 个</span>
          <span v-if="rs.finding_count" class="muted">线索 {{ rs.finding_count }} 条</span>
          <span v-if="rs.error && !isSkippedNonService(rs)" class="err">{{ rs.error }}</span>
        </div>
      </div>

      <!-- yaml vs 代码实态 diff:这是用户真正想看的"发现了啥没在 yaml 里" -->
      <div v-if="diff" class="card diff-card">
        <div class="card-header">
          <span class="name">⚖️ 对照 system.yaml</span>
          <span v-if="diff.totalNew > 0" class="tag green">代码有但 yaml 没写 {{ diff.totalNew }} 项</span>
          <span v-if="diff.totalMissing > 0" class="tag orange">yaml 写了但代码没扫到 {{ diff.totalMissing }} 项</span>
          <span v-if="diff.configCenterMismatch" class="tag red">配置中心对不上</span>
          <span v-if="diff.totalNew === 0 && diff.totalMissing === 0 && !diff.configCenterMismatch" class="tag green">完全一致</span>
          <div class="apply-actions" style="margin-left:auto; display:flex; gap:6px;">
            <button
              v-if="diff.totalNew > 0"
              class="btn small"
              title="把建议的 yaml 片段复制到剪贴板,贴回 system.yaml 的 repos 下就能补全 service_names"
              @click="copySuggestedYamlSnippet"
            >
              📋 复制补丁片段
            </button>
            <button
              class="btn small primary"
              title="把代码扫描发现的 service_names / config_center 差异合并到 yaml,跳到 YAML 沙盒做验证"
              @click="applyDiffToYAMLAndOpen"
            >
              ✨ 应用到 YAML 沙盒
            </button>
          </div>
        </div>

        <div v-if="diff.configCenterMismatch" class="detail warn">
          <strong>⚠ 配置中心类型对不上:</strong>
          yaml 写的是 <code>{{ diff.configCenterYaml || '(空)' }}</code>,代码里实际扫到 <code>{{ diff.configCenterCode }}</code>。
          回 yaml 把 <code>infrastructure.config_center.type</code> 改一下。
        </div>

        <div v-for="r in diff.repos" :key="r.name" class="diff-row">
          <div class="diff-row-head">
            <strong>{{ r.name }}</strong>
            <span v-if="r.isServiceRole" class="muted">yaml 写了 {{ r.yamlServices.length }} 个服务 · 代码里扫到 {{ r.codeServices.length }} 个</span>
            <span v-else class="muted">
              <span class="tag gray">{{ r.effectiveRole }}</span>
              不参与服务对账
            </span>
          </div>
          <!-- 业务服务才跑 missing/new 对账 -->
          <template v-if="r.isServiceRole">
            <div v-if="r.newInCode.length" class="detail">
              <span class="tag green" style="min-width: 110px;">代码里多出来的</span>
              <span v-for="s in r.newInCode" :key="s" class="tag blue">{{ s }}</span>
            </div>
            <div v-if="r.missingInCode.length" class="detail">
              <span class="tag orange" style="min-width: 110px;">yaml 写了但没扫到</span>
              <span v-for="s in r.missingInCode" :key="s" class="tag gray">{{ s }}</span>
            </div>
            <div v-if="r.newInCode.length === 0 && r.missingInCode.length === 0" class="detail muted">
              ✓ 完全一致
            </div>
          </template>
          <!-- 非业务服务:仅展示一行说明,不报"missing/extra" -->
          <template v-else>
            <div class="detail muted skip-row">
              ℹ️ {{ nonServiceRoleHint(r.effectiveRole) }}
            </div>
          </template>
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
          <strong>扫到的服务名:</strong>
          <span v-for="s in repo.service_names" :key="s" class="tag blue">{{ s }}</span>
        </div>

        <div v-if="repo.findings?.length" class="detail">
          <strong>配置中心线索({{ repo.findings.length }} 条):</strong>
          <div v-for="(f, i) in repo.findings" :key="i" class="finding">
            <span class="src">{{ f.source_file }}</span>
            <span v-if="f.data_id" class="kv">dataId={{ f.data_id }}</span>
            <span v-if="f.namespace_id" class="kv">namespace={{ f.namespace_id }}</span>
            <span v-if="f.group" class="kv">group={{ f.group }}</span>
            <span v-if="f.app_id" class="kv">appId={{ f.app_id }}</span>
            <span v-if="f.kv_prefix" class="kv">前缀={{ f.kv_prefix }}</span>
            <span v-if="f.env_profile" class="tag orange">{{ f.env_profile }}</span>
          </div>
        </div>

        <!-- Notes:扫描发现的中性事实(框架、API URL、build tool 等)。
             跟 Warnings 分开渲染,避免把 frontend_framework=next 这种"FYI"信息误显示成红色错误。 -->
        <div v-if="(repo as any).notes?.length" class="detail">
          <strong>📋 扫描发现({{ (repo as any).notes.length }} 条):</strong>
          <div v-for="n in (repo as any).notes" :key="n" class="note-line">{{ n }}</div>
        </div>

        <div v-if="repo.warnings?.length" class="detail warn">
          <strong>⚠ 异常提示:</strong>
          <div v-for="w in repo.warnings" :key="w" class="warn-line">{{ w }}</div>
        </div>

        <div v-if="!repo.findings?.length && !repo.warnings?.length && !(repo as any).notes?.length" class="detail muted">
          没扫到配置中心线索,也没异常提示
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* .page / .btn / .info-box* 来自 design.css;这里只写个性化 */

.info-box-body {
  font-size: 13px;
  line-height: 1.65;
}
.info-box-lead {
  margin: 0 0 10px;
  color: var(--c-text);
}
.info-box-actions {
  list-style: none;
  margin: 0 0 10px;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.info-box-actions li {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 4px 0;
}
.info-box-actions li strong {
  flex-shrink: 0;
  min-width: 92px;
  font-weight: 600;
  color: var(--c-ink);
}
.info-box-actions li span {
  color: var(--c-text);
  font-size: 12.5px;
}
.info-box-actions li code {
  background: var(--c-surf-3);
  padding: 1px 6px;
  border-radius: 3px;
  font-size: 11.5px;
}
.info-box-actions li a { color: var(--c-accent); text-decoration: none; }
.info-box-actions li a:hover { text-decoration: underline; }
.info-box-inputs {
  margin-bottom: 10px;
  padding: 8px 12px;
  background: var(--c-surf-3);
  border-radius: var(--r-sm);
  border-left: 2px solid var(--c-accent);
}
.info-box-inputs-title {
  font-weight: 600;
  font-size: 12.5px;
  color: var(--c-ink);
  margin-bottom: 4px;
}
.info-box-inputs ul {
  list-style: disc;
  margin: 0;
  padding-left: 20px;
  font-size: 12.5px;
}
.info-box-inputs ul li { padding: 2px 0; }
.info-box-inputs code {
  background: var(--c-surf-2);
  padding: 1px 5px;
  border-radius: 3px;
  font-size: 11.5px;
}
.info-box-redirect {
  margin: 0;
  padding-top: 10px;
  border-top: 1px dashed var(--c-line);
  font-size: 12.5px;
  color: var(--c-muted);
}
.info-box-redirect code {
  background: var(--c-surf-3);
  padding: 1px 5px;
  border-radius: 3px;
  font-size: 11.5px;
}

.diff-row .skip-row {
  font-size: 12px;
  color: #64748b;
  padding: 4px 0 2px;
  font-style: italic;
}

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
.note-line {
  font-size: 12px; padding: 2px 0;
  color: #475569; font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}

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
.repo-status.skipped-nonservice {
  /* 非业务服务角色无需扫描 → 不刷红色 / 警告色,改为中性灰底浅蓝边,跟"已扫描"分开但不显眼 */
  opacity: 1;
  background: #f8fafc;
  border-color: #cbd5e1;
}
.repo-status.skipped-nonservice .status-tag {
  background: #e0e7ff; color: #3730a3; font-style: italic;
}
.repo-status.clone-failed { background: #fef2f2; border-color: #fecaca; }
</style>
