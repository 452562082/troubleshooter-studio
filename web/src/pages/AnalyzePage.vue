<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import yaml from 'js-yaml'
import { analyzeV2 as bridgeAnalyzeV2, isDesktop, getRepoPathsForSystem, getUserConfig, type AnalyzeResult } from '../lib/bridge'
import { toast, toastError } from '../lib/toast'
import { useYamlFileLoader } from '../lib/useYamlFileLoader'
import { computeYamlCodeDiff, type YamlVsCodeDiff } from '../lib/yamlCodeDiff'
import { copyToClipboard } from '../lib/clipboard'
import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime'
import AnalyzeRepoPathsPanel from '../components/AnalyzeRepoPathsPanel.vue'
import AnalyzeDiffCard from '../components/AnalyzeDiffCard.vue'
import AnalyzeRepoFindingsCard from '../components/AnalyzeRepoFindingsCard.vue'

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

meta:
  schema_version: "0.1"
`

function loadExample() {
  yamlContent.value = exampleYaml
  // 加载示例 = 换 yaml,顺手清旧扫描结果(跟 loadFileNative / loadFileBrowser 一致)
  result.value = null
  error.value = ''
  progressLog.value = ''
}

const yamlContent = ref('')
const autoClone = ref(false)
const loading = ref(false)
const result = ref<any>(null)
const error = ref('')

// cloneFallbackRoot:autoClone 时仓库落盘的父目录,后端 analyzerpipe 必须知道往哪个根 clone。
// 不暴露给用户填 —— 默认用 userconfig.default_repos_root(没设就 fallback ~/.tshoot/repos)。
// 想改默认 clone 位置去 Settings 改 defaultReposRoot,本页不重复占输入框。
const cloneFallbackRoot = ref('')
const cloneFallbackDisplay = computed(() => cloneFallbackRoot.value || '~/.tshoot/repos')
async function loadCloneFallbackRoot() {
  if (!isDesktop()) return
  try {
    const c = await getUserConfig()
    cloneFallbackRoot.value = c.resolved_repos_root || '' // resolved 永远非空(后端 fallback ~/.tshoot/repos)
  } catch { /* ignore;runAnalyze 时再容错 */ }
}
loadCloneFallbackRoot()

// savedRepoPaths:从 ~/.tshoot/config.json 读到的"该 system 已部署时记下的仓库本地路径"。
// 后端 App.Analyze 已经会自动 merge,这里只是为了 UI 上提示用户"已经有 N 个仓库自动用 saved 路径,
// 你不必再选父目录"。yaml 内容变化时(粘新 yaml / load file)需要重新拉一次。
const savedRepoPaths = ref<Record<string, string>>({})
const yamlSystemID = computed(() => {
  try {
    const obj = yaml.load(yamlContent.value) as any
    return obj?.system?.id || ''
  } catch { return '' }
})
const yamlRepoNames = computed<string[]>(() => {
  try {
    const obj = yaml.load(yamlContent.value) as any
    return Array.isArray(obj?.repos) ? obj.repos.map((r: any) => r?.name).filter(Boolean) : []
  } catch { return [] }
})
// repoPathDrafts:用户手填的 per-repo 本地路径(key=repo.name)。优先级 drafts > saved >
// <reposRoot>/<repo.name>(后端兜底)。AnalyzeRepoPathsPanel v-model:drafts 双向绑定。
const repoPathDrafts = ref<Record<string, string>>({})

// effectiveRepoPaths / allReposCovered / reposNeedingReposRoot 的窄化逻辑都在 panel 里复算。
// runAnalyze 通过 ref expose 拿:panelRef.value?.effectiveRepoPaths 等。
const panelRef = ref<InstanceType<typeof AnalyzeRepoPathsPanel> | null>(null)
const effectiveRepoPaths = computed<Record<string, string>>(() => {
  const out: Record<string, string> = {}
  for (const n of yamlRepoNames.value) {
    const v = (repoPathDrafts.value[n] || '').trim() || (savedRepoPaths.value[n] || '').trim()
    if (v) out[n] = v
  }
  return out
})
const reposNeedingReposRoot = computed(() => yamlRepoNames.value.filter(n => !effectiveRepoPaths.value[n]))
const allReposCovered = computed(() => yamlRepoNames.value.length > 0 && reposNeedingReposRoot.value.length === 0)

async function refreshSavedRepoPaths() {
  if (!isDesktop()) { savedRepoPaths.value = {}; repoPathDrafts.value = {}; return }
  const id = yamlSystemID.value
  if (!id) { savedRepoPaths.value = {}; repoPathDrafts.value = {}; return }
  try {
    savedRepoPaths.value = await getRepoPathsForSystem(id) || {}
  } catch {
    savedRepoPaths.value = {}
  }
  // 切到新 system 时,drafts 重置为 saved 的副本 —— 用户看到的是"已存的路径",改完才 dirty
  repoPathDrafts.value = { ...savedRepoPaths.value }
}
// yaml 改了 → 重拉(用户切换 system / 加载文件 / 编辑 system.id)
import { watch } from 'vue'
watch(yamlSystemID, (newId, oldId) => {
  refreshSavedRepoPaths()
  // 切到不同 system(粘贴 / 加载新 yaml 改了 system.id),旧扫描结果跟新 yaml 错位,清掉。
  // 仅在真正变更后清(immediate=true 首次 oldId=undefined 跳过)。
  if (oldId !== undefined && newId !== oldId) {
    result.value = null
    error.value = ''
    progressLog.value = ''
  }
}, { immediate: true })

// pickRepoPath / batchFillFromParent / clearRepoPath / saveDraftsToUserConfig 已搬到
// AnalyzeRepoPathsPanel;父端只接 @saved 事件同步 savedRepoPaths,@error 事件展示错误条
function onPathsSaved(filtered: Record<string, string>) {
  savedRepoPaths.value = filtered
}

// 重置上次扫描结果 —— 加载新 yaml 文件 / 系统 id 变化时调,避免旧结果跟新 yaml 错位。
// 不动 yamlContent 本身(load 流程外面已经赋值);只清 result + error + progressLog。
function clearScanResult() {
  result.value = null
  error.value = ''
  progressLog.value = ''
}

// 桌面 app 走 Wails 原生 osascript 对话框(reliable on macOS WKWebView);
// 浏览器模式回退 <input type="file"> + FileReader。
const { loadFileNative, loadFileBrowser } = useYamlFileLoader({
  onLoaded: (content) => {
    yamlContent.value = content
    clearScanResult()
  },
  onError: (msg) => { error.value = msg },
})

// ── yaml vs 代码实态 diff ──
// 跑完 analyze 用户最想知道的是"跟我 yaml 里写的比,差了啥":
//   - new:代码里有 service_names 但 yaml 没声明(可能漏配,复制 yaml 片段提醒用户加)
//   - missing:yaml 声明了但 analyzer 没扫到(可能写错仓库名 / service 不存在了 / 代码没 clone)
//   - config-center 冲突:yaml 说 nacos,代码扫出 apollo
// 纯前端算:yaml 用 js-yaml parse,result.report.repos 按 name 匹配,集合运算。
// diff 计算实现见 lib/yamlCodeDiff.ts(纯函数 + 12 个单测覆盖);此处仅 parse yaml 后委托。
const diff = computed<YamlVsCodeDiff | null>(() => {
  if (!result.value) return null
  let yamlCfg: any = {}
  try { yamlCfg = yaml.load(yamlContent.value) || {} } catch { return null }
  return computeYamlCodeDiff(yamlCfg, result.value.report || {})
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
  copyToClipboard(lines.join('\n'))
    .then((ok) => ok
      ? toast.success('片段已复制到剪贴板,粘到你的 yaml 对应 repo 下')
      : toast.error('复制失败'))
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
  } catch (e) {
    toastError('yaml 解析', e)
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
  } catch (e) {
    toastError('保存到 localStorage', e)
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


async function runAnalyze() {
  if (!yamlContent.value.trim()) { error.value = '请先填写或加载 troubleshooter.yaml'; return }
  // 路径来源:effectiveRepoPaths(drafts > saved)。要么全部 repo 有路径,要么开 autoClone
  // 让后端把缺的浅克隆到 cloneFallbackRoot;两者都不满足直接报错,提示用户用上方表格填或开 clone。
  if (!allReposCovered.value && !autoClone.value) {
    const missing = reposNeedingReposRoot.value
    error.value = `还有 ${missing.length} 个仓库没本地路径 (${missing.slice(0,3).join(', ')}${missing.length>3?'…':''}),请在上方"仓库本地路径"挨个选(或点📁批量填),或勾"自动 clone"让后端自己 clone`
    return
  }
  if (!isDesktop()) {
    error.value = '代码扫描依赖本机文件访问,浏览器 tshoot serve 模式不支持;请用桌面 app 或 CLI:\n  tshoot analyze -i <yaml> --repos-root ... -o analysis.json'
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
    // 用 V2:per-repo paths(drafts ∪ saved)优先;autoClone 时把 cloneFallbackRoot 当落盘根。
    // 没勾 autoClone 不传 reposRoot,后端只用 RepoPaths(已校验全覆盖,不会缺)。
    const r = (await bridgeAnalyzeV2(
      yamlContent.value,
      autoClone.value ? cloneFallbackRoot.value : '',
      effectiveRepoPaths.value,
      autoClone.value,
    )) as AnalyzeResult
    result.value = r
    toast.success(`analyze 完成: ${r.per_repo?.length ?? 0} 个仓库,共 ${r.report?.repos?.length ?? 0} 条 report`)
  } catch (e) {
    error.value = e instanceof Error ? e.message : String(e)
    toastError('analyze', e)
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
// 是否非服务角色(不管 status):common-lib / frontend / mobile / infra / docs。
// 用来抑制顶部 per-repo 卡上的"服务 N 个"标签 —— 后端扫描器对 common-lib 仓库
// 也会派生出 1 个跟仓库同名的 service_name(默认行为),前端再显示"服务 1 个"
// 会让用户以为它是真实业务服务,跟下方"不参与服务对账"自相矛盾。
function isNonServiceRole(role?: string): boolean {
  const r = (role || '').trim()
  if (!r) return false
  return !SERVICE_ROLES_SET.has(r)
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
      <div class="info-box-title">代码扫描 — 从源码反推 yaml 应该怎么写,顺带补依赖图 / 数据 schema</div>
      <div class="info-box-body">
        <p class="info-box-lead">把已 clone 到本机的代码当真源,扫"实际跑的样子"跟 yaml 对账;扫到的有用线索合并进 yaml + 排障映射表,跳沙盒做最终验证。</p>
        <ul class="info-box-actions">
          <li>
            <strong>🔍 服务名 + 配置中心线索</strong>
            <span>—— 每仓库的 service_names(go.mod / pom / package.json + monorepo cmd 入口加 <code>&lt;repo&gt;-</code> 前缀消歧)+ Nacos / Apollo / Consul / Kuboard 的 dataId / namespace / configmap</span>
          </li>
          <li>
            <strong>🔗 服务依赖图 (downstream)</strong>
            <span>—— Go 识别 <code>http.Get</code> / <code>grpc.Dial</code> / <code>NewXxxClient(naming, ServiceName)</code>;Java 识 <code>@FeignClient</code>;Python 识 requests/httpx。下游写进 service-dependency-map 给排障"沿依赖图追"用</span>
          </li>
          <li>
            <strong>🗄️ 数据层 + 业务表 schema</strong>
            <span>—— redis / mongodb / mysql / doris / kafka / es / clickhouse 等客户端构造 + 主流 ORM(GORM / JPA / SQLAlchemy / TypeORM 等)抽出表 / collection / cache prefix,写进 data-schema-map</span>
          </li>
          <li>
            <strong>📊 差异对比</strong>
            <span>—— 跟 yaml 声明逐项对照:<code>missing</code>(漏写)/ <code>extra</code>(多写)/ <code>verified</code>(对齐)</span>
          </li>
          <li>
            <strong>↩️ 一键回填</strong>
            <span>—— 把差异合并回 yaml,自动跳 <router-link to="/editor">YAML 沙盒</router-link> 做验证 + 健康检查</span>
          </li>
        </ul>
        <div class="info-box-inputs">
          <div class="info-box-inputs-title">📝 需要的输入:</div>
          <ul>
            <li><strong>troubleshooter.yaml</strong> — 粘贴或从文件加载</li>
            <li><strong>仓库本地路径</strong> — yaml 加载后表格挨个选,或📁批量从父目录填(已部署过的系统自动复用 <code>~/.tshoot/config.json</code> 上次记录的路径)</li>
          </ul>
        </div>
        <p class="info-box-redirect">
          ⚠️ 本机没 clone 的仓库默认跳过;勾「自动 clone 缺失仓库」会按 yaml <code>repos[].url</code> 浅克隆到默认目录(需 git + 凭证)。识别精度:Go 70-80% / Java 60-70% / Python 60% / Node 50%,小众框架的部分需在沙盒手补。
        </p>
      </div>
    </div>

    <div class="form-section">
      <div class="label-row">
        <label>troubleshooter.yaml</label>
        <div class="label-row-actions">
          <button v-if="isDesktop()" class="btn small" @click="loadFileNative">加载文件</button>
          <label v-else class="btn small">加载文件 <input type="file" accept=".yaml,.yml" @change="loadFileBrowser" hidden /></label>
          <button class="btn small" @click="loadExample">加载示例</button>
        </div>
      </div>
      <textarea v-model="yamlContent" class="yaml-input" placeholder="把 troubleshooter.yaml 内容粘到这里,或点上面「加载文件」选本机文件…" spellcheck="false" :class="{ err: error }" />
    </div>

    <AnalyzeRepoPathsPanel
      ref="panelRef"
      v-model:drafts="repoPathDrafts"
      v-model:auto-clone="autoClone"
      :yaml-repo-names="yamlRepoNames"
      :yaml-system-i-d="yamlSystemID"
      :saved-repo-paths="savedRepoPaths"
      :clone-fallback-display="cloneFallbackDisplay"
      :loading="loading"
      @saved="onPathsSaved"
      @error="(msg: string) => (error = msg)"
    />

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
          <span v-if="rs.service_name_count && !isNonServiceRole(rs.role)" class="muted">服务 {{ rs.service_name_count }} 个</span>
          <span v-if="isNonServiceRole(rs.role)" class="muted">{{ rs.role }} · 不参与对账</span>
          <span v-if="rs.finding_count" class="muted">线索 {{ rs.finding_count }} 条</span>
          <span v-if="rs.error && !isSkippedNonService(rs)" class="err">{{ rs.error }}</span>
        </div>
      </div>

      <AnalyzeDiffCard
        v-if="diff"
        :diff="diff"
        :non-service-role-hint="nonServiceRoleHint"
        @copy-snippet="copySuggestedYamlSnippet"
        @apply-to-yaml="applyDiffToYAMLAndOpen"
      />

      <AnalyzeRepoFindingsCard
        v-for="repo in result.report?.repos || []"
        :key="repo.name"
        :repo="repo"
      />
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

.label-row-actions { display: flex; gap: 6px; }
.form-section { margin-bottom: var(--sp-4); }
.label-row { display: flex; justify-content: space-between; align-items: center; margin-bottom: 6px; }
/* 必须用 `>`(直接子)而非后代选择器:
 * 结构是
 *   .label-row
 *     <label>troubleshooter.yaml</label>           ← 表单 label,要 600 粗 + 14px
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

/* yaml 输入区 = 等宽多行,跟基础 textarea(普通文本输入)区别开,避免泄漏到其它 textarea */
.yaml-input {
  width: 100%; min-height: 180px; padding: 10px var(--sp-3); border: 1px solid var(--c-line); border-radius: var(--r-md);
  font-family: 'SF Mono', 'Fira Code', monospace; font-size: var(--fs-base); line-height: 1.5;
  background: var(--c-surf-2); resize: vertical; box-sizing: border-box;
}
.yaml-input:focus { outline: none; border-color: var(--c-accent); }

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
/* RepoPathsPanel 整套 CSS 已搬到 components/AnalyzeRepoPathsPanel.vue */

/* primary 按钮在 footer 里。注:这条全局覆盖跟设计系统的 .btn.primary 颜色一致,只是
   为了在浏览器一进 /analyze 就有正确蓝调(避免依赖 design.css 的加载顺序),保留全局 */
.btn.primary {
  background: #2563eb; color: #fff; border: 1px solid #2563eb;
}
.btn.primary:hover:not(:disabled) { background: #1d4ed8; border-color: #1d4ed8; }
.btn.primary:disabled { background: #cbd5e1; border-color: #cbd5e1; }

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

/* DiffCard / RepoFindingsCard CSS 已搬到对应子组件;per-repo-grid 顶层标签仍在本页用,
   保留 .tag* / .name 给那块用 */
.tag {
  display: inline-block; padding: 3px 10px; border-radius: 12px; font-size: 12px; font-weight: 500;
}
.tag.blue { background: #dbeafe; color: #1e40af; }
.tag.green { background: #d1fae5; color: #065f46; }
.tag.orange { background: #fef3c7; color: #92400e; }
.tag.gray { background: #f1f5f9; color: #475569; }
.name { font-weight: 700; color: #1e293b; font-size: 15px; }

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
