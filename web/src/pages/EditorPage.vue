<script setup lang="ts">
// EditorPage 定位:YAML 调试沙盒。只做两件事:
//   1. 验证:检查 yaml 语法 + 字段合法性,错误指出行/字段
//   2. 生成计划:干跑 gen,看会生成什么 skill/文件/config-map 投影
//
// 不做:
//   - 不做"执行生成"(桌面端 CWD = .app bundle 里,产物写进 bundle 下次被覆盖,坑)
//   - 不做"一键部署"(BotsPage 的"导入 YAML 一键部署"已经覆盖相同场景,这里重复就删了)
// 真要部署:在 BotsPage 导入 yaml 或去创建向导走 Step 7 一键部署。
//
// 验证错误显示增强:原先只把 raw error 字符串丢出去,用户看"parse yaml: yaml: line 5: ..."
// 不友好。现在解析错误文本:yaml 语法错按"第 N 行"高亮,schema 错按"字段 xxx"高亮,
// 并且把当前行源码截一段展示,让用户更快定位到问题。
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { genPreview, isDesktop, openYAML, plan as bridgePlan, validate as bridgeValidate } from '../lib/bridge'
import type { GenPreviewFile, GenPreviewResult } from '../lib/bridge'

// 跟"代码扫描"页对接的 localStorage key:AnalyzePage 用户点"应用到 YAML 沙盒"时
// 把改写过的 yaml 内容塞进这个 key 然后跳到本页;本页 onMounted 读出来 → 自动填进
// textarea + 显示一行 banner 让用户知道是从代码扫描带过来的。读完即清,避免下次进来又弹。
const PENDING_FROM_ANALYZE_KEY = 'tsf-pending-yaml-from-analyze'
// 显示 banner:用户从代码扫描带过来的 yaml,告诉他"已应用",可以直接验证。
const fromAnalyzeBanner = ref(false)

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

const yamlContent = ref('')

// 进页面时检查"代码扫描应用过来的 yaml":有就自动填 + 弹 banner。读完即清。
onMounted(() => {
  try {
    const pending = localStorage.getItem(PENDING_FROM_ANALYZE_KEY)
    if (pending) {
      yamlContent.value = pending
      fromAnalyzeBanner.value = true
      localStorage.removeItem(PENDING_FROM_ANALYZE_KEY)
    }
  } catch { /* localStorage 异常不阻塞页面 */ }
})
const loading = ref('')
const errorMsg = ref('')
const successMsg = ref('')
const resultTitle = ref('')
const resultData = ref<any>(null)

// 验证按钮的额外发现:HealthCheck 出来的 warn/info/error 列表,验证通过后展示。
// 跟 errorMsg 不同,issues 不阻断后续操作,仅供配置完整度提醒。
type HealthIssue = { severity: string, category: string, field?: string, message: string, hint?: string }
const validateIssues = ref<HealthIssue[]>([])
// 折叠状态:default 开,用户嫌长可以收起。按 category 折叠,粒度合适。
const issuesCollapsed = ref<Set<string>>(new Set())

// 产物预览状态:跑一次 GenPreview 把生成的所有文件读回来,UI 渲染成
// "左侧文件树 + 右侧内容预览"。activePath 控制选中哪个文件。
const previewResult = ref<GenPreviewResult | null>(null)
const previewActivePath = ref<string>('')

// ── 行号 gutter ──
// textarea 本身不支持行号,左边做个 <div class="gutter"> 同步滚动显示行号。
// 错误行高亮 + 验证失败时自动滚到那行。
const textareaRef = ref<HTMLTextAreaElement | null>(null)
const gutterRef = ref<HTMLDivElement | null>(null)

const lineCount = computed<number>(() => {
  // 至少 1 行(空 yaml 也显示 "1")。split('\n') 在末尾有换行时会多出空元素,
  // 这不影响行号显示,但我们要保证数量跟 textarea 里的行数一致。
  const text = yamlContent.value
  if (!text) return 1
  return text.split('\n').length
})

function onTextareaScroll() {
  if (textareaRef.value && gutterRef.value) {
    gutterRef.value.scrollTop = textareaRef.value.scrollTop
  }
}

// 当验证失败时,自动滚 textarea 到出错行,让用户不用手动找。
// computed parsedError 改变时触发(见下方)。
watch(
  () => errorMsg.value,
  async () => {
    await nextTick()
    if (!textareaRef.value || !parsedError.value?.lineNumber) return
    const line = parsedError.value.lineNumber
    // 19.5 = font-size 13px * line-height 1.5。粗糙估算,只要落在视口内就好。
    const lineHeight = 19.5
    // 定位到错误行 - 3 让它出现在视口上沿附近,别贴顶,留点上下文
    const targetTop = Math.max(0, (line - 3) * lineHeight)
    textareaRef.value.scrollTop = targetTop
    if (gutterRef.value) gutterRef.value.scrollTop = targetTop
  },
)

function loadExample() {
  yamlContent.value = exampleYaml
  errorMsg.value = ''
  successMsg.value = ''
  resultData.value = null
  validateIssues.value = []
}

// 桌面 app 走 Wails 原生 osascript 对话框(reliable on macOS WKWebView);
// 浏览器模式回退到 <input type="file"> + FileReader。
// 之前用 <label><input type="file"></label> 在 Wails 里点了会让窗口失焦
// "弹出去",原生对话框不会触发这个 bug。
async function loadFileNative() {
  if (!isDesktop()) return
  try {
    const r = await openYAML()
    if (!r || !r.path) return // 用户取消
    yamlContent.value = r.content || ''
    errorMsg.value = ''
    successMsg.value = ''
    resultData.value = null
    validateIssues.value = []
  } catch (e: any) {
    errorMsg.value = `加载文件失败: ${String(e?.message || e)}`
  }
}
function loadFileBrowser(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  const reader = new FileReader()
  reader.onload = () => {
    yamlContent.value = reader.result as string
    errorMsg.value = ''
    successMsg.value = ''
    resultData.value = null
    validateIssues.value = []
  }
  reader.readAsText(file)
  input.value = ''
}

async function apiCall(endpoint: 'validate' | 'plan', label: string) {
  errorMsg.value = ''
  successMsg.value = ''
  resultData.value = null
  resultTitle.value = ''
  validateIssues.value = []
  // 清掉产物预览,免得一份过期的内容跟新的 plan 结果同时显示
  previewResult.value = null
  previewActivePath.value = ''
  loading.value = label

  try {
    if (endpoint === 'validate') {
      const r = await bridgeValidate(yamlContent.value)
      const errs = (r.issues || []).filter((i: HealthIssue) => i.severity === 'error').length
      const warns = (r.issues || []).filter((i: HealthIssue) => i.severity === 'warn').length
      const infos = (r.issues || []).filter((i: HealthIssue) => i.severity === 'info').length
      let extra = ''
      if (errs + warns + infos > 0) {
        const parts: string[] = []
        if (errs) parts.push(`${errs} 个矛盾`)
        if (warns) parts.push(`${warns} 个缺口`)
        if (infos) parts.push(`${infos} 条提示`)
        extra = ` | 健康检查:${parts.join(' / ')}`
      }
      successMsg.value = `验证通过！系统: ${r.system} (${r.name}) | ${r.envs} 个环境 | ${r.repos} 个仓库${extra}`
      validateIssues.value = (r.issues || []) as HealthIssue[]
    } else {
      resultTitle.value = label
      resultData.value = await bridgePlan(yamlContent.value)
    }
  } catch (e: any) {
    // 控制台打全栈,方便用户截图给我看;errorMsg 给 UI 展示
    console.error('[EditorPage]', endpoint, '调用失败:', e)
    errorMsg.value = e?.message || String(e) || `${endpoint} 调用失败,请看控制台`
  } finally {
    loading.value = ''
  }
}

// 产物预览:真跑一次 generator 到 tmp,把所有产物文件读回来。比 plan 重,
// 但用户能看到每个 skill 的实际 SKILL.md / config-map 行 / 其它产物内容,
// 而不是只看到文件计数。点开左侧文件名 → 右侧加载内容。
async function runPreview() {
  errorMsg.value = ''
  successMsg.value = ''
  resultData.value = null
  resultTitle.value = ''
  previewResult.value = null
  previewActivePath.value = ''
  loading.value = '预览产物'
  try {
    const r = await genPreview(yamlContent.value)
    previewResult.value = r
    // 默认选中第一份"看着像入口的"文件:tshoot.json / SOUL.md / 第一份 SKILL.md
    const firstHit =
      r.files.find(f => f.path.endsWith('SOUL.md')) ||
      r.files.find(f => f.path.endsWith('tshoot.json')) ||
      r.files.find(f => /\bSKILL\.md$/.test(f.path)) ||
      r.files[0]
    previewActivePath.value = firstHit?.path || ''
  } catch (e: any) {
    console.error('[EditorPage] genPreview 失败:', e)
    errorMsg.value = e?.message || String(e) || '预览产物失败'
  } finally {
    loading.value = ''
  }
}

// 选中文件的引用,模板用
function activePreviewFile(): GenPreviewFile | null {
  if (!previewResult.value) return null
  return previewResult.value.files.find(f => f.path === previewActivePath.value) || null
}

// 把扁平 files 列表分组成 "目录 → 文件" 树结构,便于左侧渲染。
// 一层即可:每条文件按 "/" 切到第一段做组键,如 "skills/routing/SKILL.md" → 组 "skills/routing"。
// 两段以下视为根组 "/",避免无意义的组层级。
function previewGroups(): { dir: string, files: GenPreviewFile[] }[] {
  if (!previewResult.value) return []
  const map: Record<string, GenPreviewFile[]> = {}
  for (const f of previewResult.value.files) {
    const parts = f.path.split('/')
    const dir = parts.length > 1 ? parts.slice(0, -1).join('/') : '/'
    if (!map[dir]) map[dir] = []
    map[dir].push(f)
  }
  return Object.keys(map).sort().map(dir => ({ dir, files: map[dir] }))
}

function fmtSize(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(2)} MB`
}

// ── Plan 文件目录树 ──
// plan 返回 4 个扁平路径数组(files_create / files_modify / files_remove / preserved),
// 用户原来只看到计数数字,看不到具体哪些文件、按目录怎么组织。这里把它们合成一棵树:
//   - 中间层目录可折叠/展开(▼/▶),默认全展开
//   - 每个目录显示子级文件数量徽标(+N 新增 / ~N 改 / −N 删 / N 保留)
//   - 叶子节点显示文件名 + 状态徽标
type PlanFileStatus = 'create' | 'modify' | 'remove' | 'preserved'
interface PlanTreeNode {
  kind: 'dir' | 'file'
  name: string  // 末段
  path: string  // 完整路径(作 key + 折叠态键)
  depth: number
  status?: PlanFileStatus  // file 才有
  counts?: { create: number, modify: number, remove: number, preserved: number }  // dir 才有
}

// 折叠的目录路径集合;不在集合里的目录默认是展开的
const planCollapsed = ref<Set<string>>(new Set())

const planFlatTree = computed<PlanTreeNode[]>(() => {
  if (!resultData.value) return []
  const buckets: { paths: string[] | undefined, status: PlanFileStatus }[] = [
    { paths: resultData.value.files_create, status: 'create' },
    { paths: resultData.value.files_modify, status: 'modify' },
    { paths: resultData.value.files_remove, status: 'remove' },
    { paths: resultData.value.preserved, status: 'preserved' },
  ]
  // 同一路径可能在多个桶都出现(理论上不该,但 plan 数据没去重保证)。
  // 用 Map<path, status> 让后出现的覆盖前出现的;create 优先级最高 / preserved 最低,
  // 让"改动+保留"这种暧昧场景显示"改动",更符合用户预期。
  const priority: Record<PlanFileStatus, number> = {
    create: 4, modify: 3, remove: 2, preserved: 1,
  }
  const fileMap = new Map<string, PlanFileStatus>()
  for (const b of buckets) {
    if (!b.paths) continue
    for (const p of b.paths) {
      const cur = fileMap.get(p)
      if (!cur || priority[b.status] > priority[cur]) fileMap.set(p, b.status)
    }
  }

  // 构建嵌套 Tree:每层 dirs 是 Map(保留插入顺序但下面会排序),files 直接累加
  type RawNode = { dirs: Map<string, RawNode>, files: { name: string, status: PlanFileStatus }[] }
  const root: RawNode = { dirs: new Map(), files: [] }
  for (const [path, status] of fileMap) {
    const parts = path.split('/')
    let cur = root
    for (let i = 0; i < parts.length - 1; i++) {
      const seg = parts[i]
      let child = cur.dirs.get(seg)
      if (!child) {
        child = { dirs: new Map(), files: [] }
        cur.dirs.set(seg, child)
      }
      cur = child
    }
    cur.files.push({ name: parts[parts.length - 1], status })
  }

  // DFS 摊平:每层 dirs 先(按 name 排序),files 后(按 name 排序);同时算每个 dir 的递归子级计数
  const out: PlanTreeNode[] = []
  function walk(node: RawNode, prefix: string, depth: number): { create: number, modify: number, remove: number, preserved: number } {
    const total = { create: 0, modify: 0, remove: 0, preserved: 0 }
    const sortedDirs = [...node.dirs.keys()].sort()
    for (const dirName of sortedDirs) {
      const dirPath = prefix ? `${prefix}/${dirName}` : dirName
      // 占位:dir 节点先入栈,counts 在子树遍历完后回填(为了拿子树合计)
      const idx = out.length
      out.push({ kind: 'dir', name: dirName, path: dirPath, depth, counts: { create: 0, modify: 0, remove: 0, preserved: 0 } })
      const sub = walk(node.dirs.get(dirName)!, dirPath, depth + 1)
      out[idx].counts = sub
      total.create += sub.create
      total.modify += sub.modify
      total.remove += sub.remove
      total.preserved += sub.preserved
    }
    const sortedFiles = node.files.slice().sort((a, b) => a.name.localeCompare(b.name))
    for (const f of sortedFiles) {
      const fullPath = prefix ? `${prefix}/${f.name}` : f.name
      out.push({ kind: 'file', name: f.name, path: fullPath, depth, status: f.status })
      total[f.status]++
    }
    return total
  }
  walk(root, '', 0)
  return out
})

// 应用折叠态后的可见节点列表:任一祖先目录被折叠就过滤掉
const planVisibleNodes = computed<PlanTreeNode[]>(() => {
  const all = planFlatTree.value
  const collapsed = planCollapsed.value
  if (collapsed.size === 0) return all
  return all.filter(n => {
    const parts = n.path.split('/')
    // 只检查祖先(不含自己),所以 i < parts.length;n 自己被折叠时仍要显示(只是把子级隐藏)
    for (let i = 1; i < parts.length; i++) {
      const ancestor = parts.slice(0, i).join('/')
      if (collapsed.has(ancestor)) return false
    }
    return true
  })
})

const planTotalFiles = computed<number>(() => {
  const r = resultData.value
  if (!r) return 0
  return (r.files_create?.length || 0) + (r.files_modify?.length || 0)
    + (r.files_remove?.length || 0) + (r.preserved?.length || 0)
})

function planToggleDir(path: string) {
  const s = planCollapsed.value
  // 重新构造一个 Set 触发 ref 重算(直接 mutate 不会触发 computed)
  const next = new Set(s)
  if (next.has(path)) next.delete(path)
  else next.add(path)
  planCollapsed.value = next
}

function planExpandAll() {
  planCollapsed.value = new Set()
}

function planCollapseAll() {
  // 把所有 dir 节点都加进折叠集合
  const next = new Set<string>()
  for (const n of planFlatTree.value) {
    if (n.kind === 'dir') next.add(n.path)
  }
  planCollapsed.value = next
}

// ── 健康检查面板 ──
// validateIssues 来自 /api/validate 的 issues 字段:每条 {severity, category, field?, message, hint?}。
// UI 按 category 分组(repo/observability/generation/config_center/data_stores/messaging),
// 每组可折叠;组内按 severity 排序(error → warn → info)让最严重的先看到。
const issuesByCategory = computed(() => {
  const map: Record<string, HealthIssue[]> = {}
  for (const it of validateIssues.value) {
    if (!map[it.category]) map[it.category] = []
    map[it.category].push(it)
  }
  // 组内按严重度排:error > warn > info
  const order: Record<string, number> = { error: 0, warn: 1, info: 2 }
  for (const k of Object.keys(map)) {
    map[k].sort((a, b) => (order[a.severity] ?? 9) - (order[b.severity] ?? 9))
  }
  // 组间也按"组内最严重的优先"排序,避免一堆 info 把 error 顶到底下
  const ordered = Object.keys(map).sort((a, b) => {
    const ma = Math.min(...map[a].map(i => order[i.severity] ?? 9))
    const mb = Math.min(...map[b].map(i => order[i.severity] ?? 9))
    if (ma !== mb) return ma - mb
    return a.localeCompare(b)
  })
  return ordered.map(cat => ({ cat, issues: map[cat] }))
})

const categoryLabels: Record<string, string> = {
  repo: '仓库 / 环境关系',
  observability: '可观测性',
  generation: '生成 / 技能',
  config_center: '配置中心',
  data_stores: '数据层',
  messaging: '消息通知',
  env: '环境',
}
function categoryLabel(cat: string): string { return categoryLabels[cat] || cat }

function severityLabel(sev: string): string {
  if (sev === 'error') return '矛盾'
  if (sev === 'warn') return '缺口'
  if (sev === 'info') return '提示'
  return sev
}
function severityIcon(sev: string): string {
  if (sev === 'error') return '✗'
  if (sev === 'warn') return '!'
  return 'ⓘ'
}

function toggleIssueCategory(cat: string) {
  const next = new Set(issuesCollapsed.value)
  if (next.has(cat)) next.delete(cat)
  else next.add(cat)
  issuesCollapsed.value = next
}

// ── 错误诊断 ──
// config.LoadFromBytes 返回的错误格式大概三档:
//   1. "parse yaml: yaml: line 5: mapping values are not allowed in this context"
//      → 语法错,可以抽出 line N
//   2. "validate: system.id required" / "validate: repos[shop].url required"
//      → schema 错,抽出字段路径
//   3. "validate: system.id must match [a-z0-9][a-z0-9-]*, got \"Shop\""
//      → 格式错,带当前值
//
// 把这三档识别出来分别展示,比甩一串英文给用户友好得多。
interface ParsedError {
  kind: 'yaml-syntax' | 'field-missing' | 'field-invalid' | 'unknown'
  lineNumber?: number  // yaml-syntax 有
  fieldPath?: string   // field-* 有 (如 "system.id" / "repos[shop].url")
  detail: string       // 原始错误消息(翻译友好点)
  sourceLine?: string  // yaml-syntax 的上下文,从 yamlContent 里截第 N 行
}

const parsedError = computed<ParsedError | null>(() => {
  if (!errorMsg.value) return null
  const raw = errorMsg.value

  // 档 1:yaml 语法错
  const yamlLineMatch = raw.match(/yaml:\s*line\s+(\d+):\s*(.+)/)
  if (yamlLineMatch) {
    const lineNum = parseInt(yamlLineMatch[1], 10)
    const lines = yamlContent.value.split('\n')
    return {
      kind: 'yaml-syntax',
      lineNumber: lineNum,
      detail: translateYamlError(yamlLineMatch[2]),
      // yaml 库报的行号是 1-based,array 是 0-based
      sourceLine: lines[lineNum - 1] || '',
    }
  }

  // 档 2 & 3:validate: <field> required / must match / ...
  const validateMatch = raw.match(/validate:\s*(.+)/)
  if (validateMatch) {
    const body = validateMatch[1]
    // field 路径:system.id / agent.name / repos[0].name / repos[shop].url /
    //           environments[0].id / ...
    const pathMatch = body.match(/^([\w.[\]-]+)\s+(.*)$/)
    if (pathMatch) {
      const field = pathMatch[1]
      const rest = pathMatch[2]
      if (rest.startsWith('required')) {
        return { kind: 'field-missing', fieldPath: field, detail: translateSchemaError(rest) }
      }
      return { kind: 'field-invalid', fieldPath: field, detail: translateSchemaError(rest) }
    }
    // "duplicate environment id: dev" 这种,没有 field 前缀
    return { kind: 'field-invalid', detail: translateSchemaError(body) }
  }

  return { kind: 'unknown', detail: raw }
})

function translateYamlError(msg: string): string {
  // yaml 库的几条常见错信息翻译成人话
  if (msg.includes('mapping values are not allowed in this context')) {
    return '缩进或冒号错位:这一行前面可能少了 `-`(数组项)或多了空格'
  }
  if (msg.includes('did not find expected key')) {
    return '缺少字段名或缩进不对齐:检查上下行的对齐'
  }
  if (msg.includes('could not find expected')) {
    return '语法截断:引号 / 方括号 / 花括号没闭合'
  }
  if (msg.includes('found character that cannot start any token')) {
    return '有非法字符:可能是全角符号或多余的制表符'
  }
  return msg
}

function translateSchemaError(msg: string): string {
  if (msg === 'required') return '是必填字段,请补上'
  if (msg.startsWith('must match')) return `格式不合法 —— ${msg}`
  if (msg.startsWith('duplicate')) return `重复的 id/name ${msg}`
  if (msg.includes('references unknown env')) return `引用了不存在的 env(检查 environments 里有没有对应 id)`
  return msg
}
</script>

<template>
  <div class="page">
    <h1>YAML 沙盒</h1>

    <div v-if="fromAnalyzeBanner" class="alert success" style="margin-bottom: 12px;">
      ✓ 已把代码扫描的发现合并到下方 yaml(service_names / config-center 字段已更新)。
      点"<strong>✓ 验证</strong>"确认后,回「创建向导」末步部署,或在本页直接导出。
      <button type="button" class="btn-link" @click="fromAnalyzeBanner = false" style="float:right">知道了</button>
    </div>

    <div class="info-box">
      <div class="info-box-title">YAML 沙盒 — 只读校验 yaml,不动代码、不装机器人</div>
      <div class="info-box-body">
        <p class="info-box-lead">粘贴 system.yaml,快速判断「语法是否正确 / 部署后会是什么样」。</p>
        <ul class="info-box-actions">
          <li>
            <strong>✓ 验证</strong>
            <span>—— 语法 / 必填字段 / 格式校验,叠加配置健康检查(可观测性 wiring、仓库覆盖 env、多源 config_source、被静默跳过的 skill 等)</span>
          </li>
          <li>
            <strong>📋 生成计划</strong>
            <span>—— 干跑生成器,产出"启用哪些 skill、多少文件、配置映射几条"摘要,不写盘</span>
          </li>
          <li>
            <strong>📂 预览产物</strong>
            <span>—— 真跑生成器到临时目录,逐文件展开 SKILL.md / scripts 实际内容</span>
          </li>
        </ul>
        <p class="info-box-redirect">
          想<strong>扫代码反推 yaml</strong> → <router-link to="/analyze">代码扫描</router-link>;
          想<strong>真装到机器人</strong> → <router-link to="/bots">已装机器人</router-link> 或 <router-link to="/init">创建向导</router-link> 末步一键部署。
        </p>
      </div>
    </div>

    <div class="toolbar">
      <button v-if="isDesktop()" class="btn" @click="loadFileNative">加载文件</button>
      <label v-else class="btn">
        加载文件
        <input type="file" accept=".yaml,.yml" hidden @change="loadFileBrowser" />
      </label>
      <button class="btn" @click="loadExample">加载示例</button>
      <button class="btn primary" :disabled="!!loading" @click="apiCall('validate', '验证')">
        ✓ 验证
      </button>
      <button class="btn primary" :disabled="!!loading" @click="apiCall('plan', '生成计划')">
        📋 生成计划
      </button>
      <button v-if="isDesktop()" class="btn primary" :disabled="!!loading" @click="runPreview">
        📂 预览产物
      </button>
    </div>

    <div class="editor-wrap" :class="{ 'has-error': errorMsg }">
      <div ref="gutterRef" class="gutter" aria-hidden="true">
        <div
          v-for="n in lineCount"
          :key="n"
          class="gutter-line"
          :class="{ err: n === parsedError?.lineNumber }"
        >{{ n }}</div>
      </div>
      <textarea
        ref="textareaRef"
        v-model="yamlContent"
        class="yaml-editor"
        placeholder="# 在此粘贴或加载 system.yaml 内容..."
        spellcheck="false"
        @scroll="onTextareaScroll"
      />
    </div>

    <div v-if="successMsg" class="alert success">{{ successMsg }}</div>

    <!-- 健康检查结果:验证通过后跑 HealthCheck 给出的语义层缺口/矛盾/提示。
         不阻断后续操作,但用户可以一眼看到"我这 yaml 配齐了吗"。 -->
    <div v-if="validateIssues.length" class="health-card">
      <div class="health-card-head">
        <span class="health-card-title">📋 配置健康检查 ({{ validateIssues.length }})</span>
        <span class="sub-text">分类展示:✗ 矛盾必修 / ! 缺口建议补 / ⓘ 仅提示</span>
      </div>
      <div
        v-for="g in issuesByCategory"
        :key="g.cat"
        class="health-group"
        :class="'health-group-worst-' + g.issues[0].severity"
      >
        <button
          type="button"
          class="health-group-head"
          @click="toggleIssueCategory(g.cat)"
        >
          <span class="health-group-toggle">{{ issuesCollapsed.has(g.cat) ? '▶' : '▼' }}</span>
          <span class="health-group-cat">{{ categoryLabel(g.cat) }}</span>
          <span class="health-group-counts">
            <template v-for="sev in ['error', 'warn', 'info']" :key="sev">
              <span
                v-if="g.issues.some(i => i.severity === sev)"
                class="badge"
                :class="'badge-sev-' + sev"
              >
                {{ severityIcon(sev) }} {{ g.issues.filter(i => i.severity === sev).length }}
              </span>
            </template>
          </span>
        </button>
        <div v-if="!issuesCollapsed.has(g.cat)" class="health-group-body">
          <div
            v-for="(it, i) in g.issues"
            :key="g.cat + '-' + i"
            class="health-issue"
            :class="'health-issue-' + it.severity"
          >
            <span class="health-issue-icon">{{ severityIcon(it.severity) }}</span>
            <div class="health-issue-body">
              <div class="health-issue-msg">
                <span class="health-issue-sev-tag">{{ severityLabel(it.severity) }}</span>
                {{ it.message }}
              </div>
              <div v-if="it.field" class="health-issue-field">
                <code>{{ it.field }}</code>
              </div>
              <div v-if="it.hint" class="health-issue-hint">建议:{{ it.hint }}</div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- 错误卡片:分档展示,比裸 raw error 友好 -->
    <div v-if="parsedError" class="err-card" :class="'kind-' + parsedError.kind">
      <div class="err-header">
        <span class="err-icon">✖</span>
        <span class="err-kind-label">
          {{
            parsedError.kind === 'yaml-syntax' ? 'YAML 语法错' :
            parsedError.kind === 'field-missing' ? '字段缺失' :
            parsedError.kind === 'field-invalid' ? '字段非法' : '验证失败'
          }}
        </span>
        <span v-if="parsedError.lineNumber" class="err-locator">第 {{ parsedError.lineNumber }} 行</span>
        <span v-else-if="parsedError.fieldPath" class="err-locator">字段 <code>{{ parsedError.fieldPath }}</code></span>
      </div>
      <div class="err-body">{{ parsedError.detail }}</div>
      <!-- yaml 语法错把问题行截出来给用户看上下文 -->
      <pre v-if="parsedError.sourceLine" class="err-source"><code>{{ parsedError.lineNumber }} | {{ parsedError.sourceLine }}</code></pre>
      <details class="err-raw">
        <summary>原始错误信息</summary>
        <pre><code>{{ errorMsg }}</code></pre>
      </details>
    </div>

    <!-- Plan result -->
    <div v-if="resultData && resultTitle === '生成计划'" class="result-card">
      <h2>生成计划:{{ resultData.system }}</h2>
      <div class="result-grid">
        <div class="result-section">
          <h3>会启用的技能 ({{ resultData.skills_included?.length || 0 }})</h3>
          <ul v-if="resultData.skills_included?.length">
            <li v-for="s in resultData.skills_included" :key="s.name">
              <strong>{{ s.name }}</strong>
              <span v-if="s.reason" class="sub-text"> — {{ s.reason }}</span>
            </li>
          </ul>
          <p v-else class="sub-text">无</p>
        </div>
        <div class="result-section">
          <h3>会跳过的技能 ({{ resultData.skills_skipped?.length || 0 }})</h3>
          <ul v-if="resultData.skills_skipped?.length">
            <li v-for="s in resultData.skills_skipped" :key="s.name">
              <strong>{{ s.name }}</strong>
              <span v-if="s.reason" class="sub-text"> — {{ s.reason }}</span>
            </li>
          </ul>
          <p v-else class="sub-text">无</p>
        </div>
      </div>
      <div class="result-grid">
        <div class="result-section">
          <h3>会产出的文件</h3>
          <p><span class="badge badge-green">新建 {{ resultData.files_create?.length || 0 }}</span></p>
          <p><span class="badge badge-blue">改动 {{ resultData.files_modify?.length || 0 }}</span></p>
          <p><span class="badge badge-red">删除 {{ resultData.files_remove?.length || 0 }}</span></p>
          <p><span class="badge badge-gray">保留 {{ resultData.preserved?.length || 0 }}</span></p>
        </div>
        <div class="result-section">
          <h3>配置中心映射条数</h3>
          <table class="mini-table">
            <tr><td>仓库扫描得到</td><td>{{ resultData.config_map_projection?.verified_from_analyzer ?? 0 }}</td></tr>
            <tr><td>用户手填</td><td>{{ resultData.config_map_projection?.verified_from_prior ?? 0 }}</td></tr>
            <tr><td>规则推断</td><td>{{ resultData.config_map_projection?.inferred ?? 0 }}</td></tr>
            <tr><td><strong>总计</strong></td><td><strong>{{ resultData.config_map_projection?.total ?? 0 }}</strong></td></tr>
          </table>
        </div>
      </div>

      <div v-if="planTotalFiles > 0" class="result-section result-section-full">
        <div class="plan-tree-head">
          <h3>文件目录树 ({{ planTotalFiles }} 个文件)</h3>
          <div class="plan-tree-actions">
            <button type="button" class="btn-link" @click="planExpandAll">全部展开</button>
            <span class="sep">·</span>
            <button type="button" class="btn-link" @click="planCollapseAll">全部折叠</button>
          </div>
        </div>
        <div class="plan-tree">
          <div
            v-for="n in planVisibleNodes"
            :key="n.kind + ':' + n.path"
            class="plan-tree-row"
            :class="[
              'plan-tree-' + n.kind,
              n.kind === 'file' && n.status ? 'plan-tree-status-' + n.status : '',
            ]"
            :style="{ paddingLeft: (8 + n.depth * 16) + 'px' }"
            :role="n.kind === 'dir' ? 'button' : undefined"
            @click="n.kind === 'dir' ? planToggleDir(n.path) : null"
          >
            <span v-if="n.kind === 'dir'" class="plan-tree-toggle">
              {{ planCollapsed.has(n.path) ? '▶' : '▼' }}
            </span>
            <span v-else class="plan-tree-toggle plan-tree-toggle-spacer"></span>
            <span class="plan-tree-icon">{{ n.kind === 'dir' ? '📁' : '📄' }}</span>
            <span class="plan-tree-name">{{ n.name }}<span v-if="n.kind === 'dir'">/</span></span>
            <span class="plan-tree-meta">
              <template v-if="n.kind === 'dir' && n.counts">
                <span v-if="n.counts.create" class="badge badge-green">+{{ n.counts.create }}</span>
                <span v-if="n.counts.modify" class="badge badge-blue">~{{ n.counts.modify }}</span>
                <span v-if="n.counts.remove" class="badge badge-red">−{{ n.counts.remove }}</span>
                <span v-if="n.counts.preserved" class="badge badge-gray">{{ n.counts.preserved }}</span>
              </template>
              <template v-else-if="n.kind === 'file'">
                <span v-if="n.status === 'create'" class="badge badge-green">新建</span>
                <span v-else-if="n.status === 'modify'" class="badge badge-blue">改动</span>
                <span v-else-if="n.status === 'remove'" class="badge badge-red">删除</span>
                <span v-else-if="n.status === 'preserved'" class="badge badge-gray">保留</span>
              </template>
            </span>
          </div>
        </div>
      </div>
    </div>

    <!-- Preview result:左文件树 + 右内容浏览,按 yaml 的 generation.targets 真跑一遍 generator,
         路径对齐用户 AI 平台真实部署位置(openclaw 剥 staging 前缀,各 target 都加目标名前缀) -->
    <div v-if="previewResult" class="result-card preview-card">
      <h2>
        📂 产物预览:{{ previewResult.system }}
        <span class="sub-text" style="font-weight:normal">
          · 部署目标:{{ previewResult.targets.join(' / ') }}
          · 共 {{ previewResult.files.length }} 个文件
        </span>
      </h2>
      <div class="preview-layout">
        <div class="preview-tree">
          <div v-for="g in previewGroups()" :key="g.dir" class="preview-group">
            <div class="preview-group-head">{{ g.dir }}</div>
            <button
              v-for="f in g.files"
              :key="f.path"
              class="preview-file"
              :class="{ active: f.path === previewActivePath, binary: f.binary }"
              :title="`${f.path} (${fmtSize(f.size)}${f.binary ? ', 二进制' : ''}${f.truncated ? ', 已截断' : ''})`"
              @click="previewActivePath = f.path"
            >
              <span class="preview-file-name">{{ f.path.split('/').pop() }}</span>
              <span class="preview-file-meta">{{ fmtSize(f.size) }}<span v-if="f.binary"> · bin</span></span>
            </button>
          </div>
        </div>
        <div class="preview-content">
          <template v-if="activePreviewFile()">
            <div class="preview-content-head">
              <code>{{ activePreviewFile()!.path }}</code>
              <span class="sub-text">{{ fmtSize(activePreviewFile()!.size) }}</span>
              <span v-if="activePreviewFile()!.truncated" class="badge badge-orange">已截断(头部 200KB)</span>
              <span v-if="activePreviewFile()!.binary" class="badge badge-gray">二进制文件</span>
            </div>
            <pre v-if="activePreviewFile()!.binary" class="preview-content-body muted">[二进制文件,无法预览]</pre>
            <pre v-else class="preview-content-body"><code>{{ activePreviewFile()!.content }}</code></pre>
          </template>
          <p v-else class="sub-text" style="padding:20px">点左侧文件查看内容</p>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>

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
  margin: 0 0 12px;
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
.info-box-redirect {
  margin: 0;
  padding-top: 10px;
  border-top: 1px dashed var(--c-line);
  font-size: 12.5px;
  color: var(--c-muted);
}
.info-box-redirect strong { color: var(--c-text); }
.info-box-redirect a { color: var(--c-accent); text-decoration: none; }
.info-box-redirect a:hover { text-decoration: underline; }

.toolbar {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}


/* ── 编辑器 + 行号 gutter ── */
/* 结构:.editor-wrap 横向 flex, .gutter 固定宽, textarea 占剩余空间
 * 两者 line-height + font-size 必须完全一致,gutter 滚动跟着 textarea 同步
 * (onTextareaScroll 做的),这样行号跟正文对齐。 */
.editor-wrap {
  display: flex;
  min-height: 500px;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  border-radius: 6px;
  overflow: hidden;              /* 让 gutter 圆角跟随外框 */
  transition: border-color 0.15s;
  resize: vertical;
  /* Firefox/Chrome 都能让 flex 容器 resize */
  min-height: 500px;
}
.editor-wrap:focus-within { border-color: #3b82f6; }
.editor-wrap.has-error { border-color: #ef4444; }

.gutter {
  flex: 0 0 auto;
  min-width: 40px;
  padding: 12px 8px 12px 10px;
  background: #f1f5f9;
  border-right: 1px solid #e2e8f0;
  color: #94a3b8;
  font-family: 'SF Mono', 'Fira Code', 'Cascadia Code', monospace;
  font-size: 13px;
  line-height: 1.5;
  text-align: right;
  user-select: none;
  overflow: hidden;              /* scrollTop 通过 JS 同步,不自己滚 */
  font-variant-numeric: tabular-nums;
}
.gutter-line {
  height: 1.5em;                 /* 跟 textarea line-height 对齐 */
  white-space: nowrap;
}
.gutter-line.err {
  color: #991b1b; font-weight: 700; background: #fee2e2;
  margin: 0 -8px 0 -10px; padding: 0 8px 0 10px;  /* 背景吃满 gutter 宽度 */
}

.yaml-editor {
  flex: 1;
  min-height: 500px;
  background: transparent;
  border: none;
  padding: 12px 16px;
  font-family: 'SF Mono', 'Fira Code', 'Cascadia Code', monospace;
  font-size: 13px;
  line-height: 1.5;
  color: #1e293b;
  resize: none;
  outline: none;
}

/* ── 错误诊断卡片 ── */
.err-card {
  margin-top: 12px;
  padding: 14px 16px;
  background: #fef2f2;
  border: 1px solid #fecaca;
  border-left: 4px solid #dc2626;
  border-radius: 6px;
  color: #7f1d1d;
}
.err-header {
  display: flex; align-items: center; gap: 10px;
  margin-bottom: 8px;
  font-size: 13px; font-weight: 600;
}
.err-icon { color: #dc2626; font-size: 15px; }
.err-kind-label { color: #991b1b; }
.err-locator {
  margin-left: auto; padding: 2px 8px;
  background: #fee2e2; border-radius: 10px;
  font-size: 11px; font-weight: 500; color: #7f1d1d;
  font-variant-numeric: tabular-nums;
}
.err-locator code {
  background: transparent; padding: 0; color: inherit;
  font-family: 'SF Mono', monospace;
}
.err-body {
  color: #7f1d1d; font-size: 13px; line-height: 1.6;
  margin-bottom: 8px;
}
.err-source {
  background: #1e293b; color: #fbbf24;
  padding: 10px 12px; border-radius: 4px;
  font-family: 'SF Mono', monospace; font-size: 12px;
  margin-bottom: 8px;
  white-space: pre-wrap; word-break: break-all;
}
.err-raw {
  font-size: 11px; color: #991b1b;
}
.err-raw summary {
  cursor: pointer; user-select: none;
  font-weight: 500; padding: 4px 0;
}
.err-raw pre {
  background: #fff; border: 1px solid #fecaca; border-radius: 4px;
  padding: 8px 10px; font-family: 'SF Mono', monospace;
  white-space: pre-wrap; word-break: break-all;
  margin-top: 4px; color: #7f1d1d;
}

.result-card {
  margin-top: 20px;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  padding: 20px 24px;
}
.result-card h2 {
  font-size: 18px;
  color: #1e293b;
  margin-bottom: 16px;
  padding-bottom: 8px;
  border-bottom: 1px solid #e2e8f0;
}
.result-card h3 {
  font-size: 14px;
  color: #475569;
  margin-bottom: 8px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.result-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 20px;
  margin-bottom: 16px;
}

.result-section ul {
  list-style: none;
  padding: 0;
}
.result-section li {
  padding: 4px 0;
  font-size: 13px;
  color: #334155;
}

.sub-text {
  color: #94a3b8;
  font-size: 13px;
}

.badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 600;
}
.badge-green { background: #dcfce7; color: #166534; }
.badge-blue { background: #dbeafe; color: #1e40af; }
.badge-red { background: #fee2e2; color: #991b1b; }
.badge-gray { background: #f1f5f9; color: #475569; }
.badge-orange { background: #ffedd5; color: #9a3412; }

/* 产物预览:左文件树 + 右内容,IDE 风。响应窗口高度,内部滚 */
.preview-card { padding: 0; overflow: hidden; }
.preview-card h2 { padding: 14px 16px; margin: 0; border-bottom: 1px solid #e2e8f0; font-size: 15px; }
.preview-layout {
  display: grid;
  grid-template-columns: minmax(240px, 320px) 1fr;
  /* 响应窗口高度:占视口剩余空间但不少于 480 / 不多于 720 */
  height: clamp(480px, calc(100vh - 320px), 720px);
  min-height: 0; /* grid 子项可缩小,避免内部 overflow 失效 */
}
.preview-tree {
  border-right: 1px solid #e2e8f0;
  background: #f8fafc;
  overflow-y: auto; overflow-x: hidden;
  padding: 6px 0 24px; /* 底部多 24px 留白,防止最后一项被滚动条遮住"拉不到底" */
  min-height: 0; /* 同上 */
}
.preview-group { margin-bottom: 6px; }
.preview-group-head {
  font-size: 11px; font-family: monospace; color: #64748b;
  padding: 5px 12px 5px 14px; background: #eef2f7;
  /* sticky 时需要明显边界跟下面行区分 */
  border-bottom: 1px solid #e2e8f0;
  position: sticky; top: 0; z-index: 1;
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
}
.preview-file {
  display: flex; justify-content: space-between; align-items: center;
  width: 100%; padding: 6px 14px; gap: 8px;
  border: none; background: transparent; text-align: left; cursor: pointer;
  font-family: inherit; font-size: 12px;
}
.preview-file:hover { background: #e2e8f0; }
.preview-file.active { background: #dbeafe; color: #1e3a8a; font-weight: 600; }
.preview-file.binary { color: #94a3b8; }
.preview-file-name { font-family: monospace; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.preview-file-meta { font-size: 10px; color: #94a3b8; flex-shrink: 0; }
/* 左侧浅色滚动条:常显(不依赖 hover),宽度跟右侧暗色保持一致,视觉对称 */
.preview-tree::-webkit-scrollbar { width: 10px; height: 10px; }
.preview-tree::-webkit-scrollbar-track { background: #f1f5f9; }
.preview-tree::-webkit-scrollbar-thumb { background: #cbd5e1; border-radius: 5px; border: 2px solid #f1f5f9; }
.preview-tree::-webkit-scrollbar-thumb:hover { background: #94a3b8; }

.preview-content { display: flex; flex-direction: column; min-width: 0; min-height: 0; }
.preview-content-head {
  display: flex; align-items: center; gap: 10px;
  padding: 10px 14px; border-bottom: 1px solid #e2e8f0;
  background: #fff;
}
.preview-content-head code {
  font-family: monospace; font-size: 12px; color: #1e293b;
  background: #f1f5f9; padding: 2px 6px; border-radius: 3px;
}
/* 暗色 IDE 风格(参照 GitHub Dark Default):柔和深蓝灰底 + 浅灰文字,
 * 比纯黑舒服;关键是要把全局 code{ background: var(--c-surf-3) } 重置掉,
 * 不然每行 code span 都会带一块亮灰底,看着像被高亮但其实是 bug。 */
.preview-content-body {
  flex: 1 1 0; margin: 0; padding: 14px 16px 24px;
  background: #0d1117;
  color: #c9d1d9;
  font-family: 'SF Mono', 'Menlo', 'Consolas', monospace;
  font-size: 12.5px; line-height: 1.55;
  overflow: auto;
  min-height: 0;
  white-space: pre; tab-size: 2;
}
/* 关键修复:把全局 code 的 background / padding 在预览面板内一律清掉,
 * 让 <pre><code>{{ content }}</code></pre> 只显示 pre 的暗色底,不再撕裂。 */
.preview-content-body code {
  background: transparent;
  padding: 0;
  border-radius: 0;
  color: inherit;
  font-family: inherit;
  font-size: inherit;
}
.preview-content-body.muted { color: #94a3b8; background: #f8fafc; font-style: italic; }
.preview-content-body.muted code { color: inherit; }
/* 滚动条配色,跟暗底协调 */
.preview-content-body::-webkit-scrollbar { width: 10px; height: 10px; }
.preview-content-body::-webkit-scrollbar-track { background: #161b22; }
.preview-content-body::-webkit-scrollbar-thumb { background: #30363d; border-radius: 5px; }
.preview-content-body::-webkit-scrollbar-thumb:hover { background: #484f58; }

.mini-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.mini-table td {
  padding: 6px 12px;
  border-bottom: 1px solid #e2e8f0;
  color: #334155;
}
.mini-table tr:last-child td {
  border-bottom: none;
}
.mini-table td:first-child {
  color: #64748b;
  width: 180px;
}
.mini-table code {
  background: #e2e8f0;
  padding: 2px 6px;
  border-radius: 3px;
  font-size: 12px;
}

/* ── Plan 文件目录树 ── */
.result-section-full {
  /* result-grid 用 grid-column 1fr 1fr;这里跨两列(放在 result-grid 外即天然全宽,
     所以这条主要是跟前面块的间距) */
  margin-top: 16px;
}
.plan-tree-head {
  display: flex; align-items: center; justify-content: space-between;
  margin-bottom: 8px;
}
.plan-tree-head h3 { margin-bottom: 0; }
.plan-tree-actions { display: flex; align-items: center; gap: 6px; font-size: 12px; }
.plan-tree-actions .sep { color: #cbd5e1; }
.btn-link {
  background: none; border: none; padding: 0; cursor: pointer;
  color: #2563eb; font-size: 12px; font-family: inherit;
}
.btn-link:hover { text-decoration: underline; }

.plan-tree {
  border: 1px solid #e2e8f0;
  border-radius: 6px;
  background: #f8fafc;
  max-height: 480px;
  overflow-y: auto;
  padding: 4px 0;
  font-size: 12.5px;
}
.plan-tree-row {
  display: flex; align-items: center; gap: 6px;
  padding: 3px 12px 3px 8px;
  user-select: none;
  white-space: nowrap;
  font-family: 'SF Mono', Menlo, Consolas, monospace;
}
.plan-tree-dir { cursor: pointer; color: #1e293b; font-weight: 500; }
.plan-tree-dir:hover { background: #e2e8f0; }
.plan-tree-file { color: #334155; }
.plan-tree-file:hover { background: #eef2f7; }
.plan-tree-toggle {
  flex-shrink: 0; width: 12px; text-align: center;
  font-size: 9px; color: #64748b;
}
.plan-tree-toggle-spacer { visibility: hidden; }
.plan-tree-icon { flex-shrink: 0; font-size: 12px; }
.plan-tree-name {
  flex: 1 1 auto; overflow: hidden; text-overflow: ellipsis;
}
.plan-tree-meta {
  flex-shrink: 0; display: flex; align-items: center; gap: 4px;
}
.plan-tree-meta .badge { padding: 1px 6px; font-size: 10.5px; }

/* 状态色:删除文件用更弱的灰红色让人一眼看出是会减的 */
.plan-tree-status-remove .plan-tree-name { color: #991b1b; text-decoration: line-through; opacity: 0.7; }
.plan-tree-status-preserved .plan-tree-name { color: #64748b; font-style: italic; }

/* ── 健康检查面板 ── */
.health-card {
  background: #fff;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  padding: 14px 16px;
  margin-top: 12px;
  margin-bottom: 16px;
}
.health-card-head {
  display: flex; align-items: center; justify-content: space-between;
  margin-bottom: 10px;
  padding-bottom: 8px;
  border-bottom: 1px solid #e2e8f0;
}
.health-card-title {
  font-size: 14px; font-weight: 600; color: #1e293b;
}

.health-group { margin-top: 8px; border-radius: 6px; overflow: hidden; }
.health-group-worst-error { border-left: 3px solid #dc2626; }
.health-group-worst-warn  { border-left: 3px solid #d97706; }
.health-group-worst-info  { border-left: 3px solid #2563eb; }

.health-group-head {
  display: flex; align-items: center; gap: 8px; width: 100%;
  padding: 8px 12px;
  background: #f8fafc;
  border: none; cursor: pointer; text-align: left;
  font-family: inherit; font-size: 13px; color: #1e293b;
}
.health-group-head:hover { background: #f1f5f9; }
.health-group-toggle {
  flex-shrink: 0; width: 12px; text-align: center;
  font-size: 9px; color: #64748b;
}
.health-group-cat { flex: 1 1 auto; font-weight: 600; }
.health-group-counts { display: flex; gap: 4px; flex-shrink: 0; }
.badge-sev-error { background: #fee2e2; color: #991b1b; }
.badge-sev-warn  { background: #fef3c7; color: #92400e; }
.badge-sev-info  { background: #dbeafe; color: #1e40af; }

.health-group-body {
  padding: 4px 0;
  background: #fff;
}
.health-issue {
  display: flex; gap: 10px;
  padding: 8px 12px 8px 28px;
  border-top: 1px solid #f1f5f9;
}
.health-issue:first-child { border-top: none; }
.health-issue-icon {
  flex-shrink: 0;
  width: 18px; height: 18px;
  display: inline-flex; align-items: center; justify-content: center;
  border-radius: 50%;
  font-size: 11px; font-weight: 700;
}
.health-issue-error .health-issue-icon { background: #fee2e2; color: #991b1b; }
.health-issue-warn  .health-issue-icon { background: #fef3c7; color: #92400e; }
.health-issue-info  .health-issue-icon { background: #dbeafe; color: #1e40af; }
.health-issue-body { flex: 1 1 auto; min-width: 0; }
.health-issue-msg { font-size: 13px; color: #1e293b; line-height: 1.5; }
.health-issue-sev-tag {
  display: inline-block;
  padding: 1px 6px; margin-right: 6px;
  border-radius: 3px;
  font-size: 11px; font-weight: 600;
}
.health-issue-error .health-issue-sev-tag { background: #fee2e2; color: #991b1b; }
.health-issue-warn  .health-issue-sev-tag { background: #fef3c7; color: #92400e; }
.health-issue-info  .health-issue-sev-tag { background: #dbeafe; color: #1e40af; }
.health-issue-field { margin-top: 3px; font-size: 11.5px; }
.health-issue-field code {
  background: #f1f5f9; color: #334155;
  padding: 1px 5px; border-radius: 3px;
  font-family: 'SF Mono', Menlo, monospace; font-size: 11px;
}
.health-issue-hint { margin-top: 4px; font-size: 12px; color: #64748b; font-style: italic; }
</style>
