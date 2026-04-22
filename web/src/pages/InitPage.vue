<script setup lang="ts">
import { ref, reactive, computed, watch } from 'vue'
import yaml from 'js-yaml'
import { useRouter } from 'vue-router'
import { analyze as bridgeAnalyze, importAndDeploy, isDesktop, openDir, validate as bridgeValidate } from '../lib/bridge'
import { confirmDialog } from '../lib/confirm'
import { toast } from '../lib/toast'
import { useDeployPath } from '../lib/useDeployPath'

const router = useRouter()

// ── Draft persistence (survives route switches and reloads) ──
const STORAGE_KEY = 'tsf-init-wizard-v1'
function loadSavedDraft(): any {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}
const saved = loadSavedDraft()

// ── Step management ──
const currentStep = ref<number>(saved?.currentStep ?? 1)
const totalSteps = 7
const stepTitles = [
  '系统基本信息',
  '机器人身份',
  '环境列表',
  '代码仓库',
  '配置源',
  '可观测性 + 数据层',
  '预览 + 生成',
]

const validationErrors = ref<Set<string>>(new Set())

// ── Step 1: 系统基本信息 ──
const system = reactive({
  id: saved?.system?.id ?? '',
  name: saved?.system?.name ?? '',
  description: saved?.system?.description ?? '',
})

// ── Step 2: 机器人身份 ──
const agent = reactive({
  name: saved?.agent?.name ?? '',
  workspace_name: saved?.agent?.workspace_name ?? '',
  model: saved?.agent?.model ?? 'anthropic/claude-sonnet-4-6',
})

// ── Model presets ──────────────────────────────────────────────
// 按提供商分组；自定义项让用户填任意字符串（保留企业内部网关 / 新模型的灵活性）。
interface ModelOption { value: string; label: string; hint?: string }
interface ModelGroup { group: string; items: ModelOption[] }
const MODEL_CUSTOM = '__custom__'
// 模型预设:4 种 target 现在都走 OpenAI 兼容协议,每个 provider 都能直连,不再有
// "embedded 回落到 Claude" 这种历史局限。跟 internal/llmchat/providers.go 注册表对齐,
// 新加 provider:这里 + internal/llmchat/providers.go 注册表同步一条(server.py.tmpl 已删)。
const modelGroups: ModelGroup[] = [
  {
    group: 'Anthropic (Claude 系列)',
    items: [
      { value: 'anthropic/claude-opus-4-7',   label: 'Claude Opus 4.7 — 最强、偏贵' },
      { value: 'anthropic/claude-sonnet-4-6', label: 'Claude Sonnet 4.6 — 默认推荐，性价比最高' },
      { value: 'anthropic/claude-haiku-4-5',  label: 'Claude Haiku 4.5 — 便宜、快，适合高频轻量' },
    ],
  },
  {
    group: 'OpenAI',
    items: [
      { value: 'openai/gpt-5-codex', label: 'GPT-5 Codex' },
      { value: 'openai/gpt-4o',      label: 'GPT-4o' },
      { value: 'openai/o3',          label: 'o3' },
    ],
  },
  {
    group: '国产大模型',
    items: [
      { value: 'deepseek/deepseek-chat',   label: 'DeepSeek Chat' },
      { value: 'deepseek/deepseek-reasoner', label: 'DeepSeek Reasoner (推理)' },
      { value: 'qwen/qwen-max',            label: '通义千问 Qwen Max' },
      { value: 'qwen/qwen-plus',           label: '通义千问 Qwen Plus' },
      { value: 'minimax/abab6.5s-chat',    label: 'MiniMax abab6.5s' },
      { value: 'minimax/abab6.5-chat',     label: 'MiniMax abab6.5 (长上下文)' },
      { value: 'moonshot/moonshot-v1-8k',  label: 'Moonshot Kimi v1-8k' },
      { value: 'moonshot/moonshot-v1-128k', label: 'Moonshot Kimi v1-128k' },
      { value: 'zhipu/glm-4',              label: '智谱 GLM-4' },
      { value: 'zhipu/glm-4-plus',         label: '智谱 GLM-4 Plus' },
    ],
  },
  {
    group: '本地 / 自部署',
    items: [
      { value: 'ollama/llama3.1',   label: 'Ollama llama3.1 (本地)' },
      { value: 'ollama/qwen2.5',    label: 'Ollama qwen2.5 (本地)' },
    ],
  },
]
const allPresetModels = modelGroups.flatMap(g => g.items.map(i => i.value))
const modelSelectValue = computed({
  get: () => allPresetModels.includes(agent.model) ? agent.model : MODEL_CUSTOM,
  set: (v: string) => {
    if (v === MODEL_CUSTOM) {
      // 切到自定义时，若当前 model 已在 preset 列表里，清空方便用户输入
      if (allPresetModels.includes(agent.model)) agent.model = ''
    } else {
      agent.model = v
    }
  },
})
const modelIsCustom = computed(() => !allPresetModels.includes(agent.model))

// 自动派生默认值:
//   - agent.name   跟着 system.name 走(中文友好,用户可见)
//   - agent.workspace_name 跟着 system.id 走(ASCII 小写,目录名友好)
//
// 分成两个 watch:
//   - 之前把 workspace_name 设成跟 agent.name 一样,结果默认变成"shop排障机器人"
//     这种 CJK 目录名,macOS 能 work 但 cd 要引号、ls 显示乱、部分 shell 补全
//     踩坑。改成以 system.id 为基准 + "-bot" 后缀,保证 ASCII。
//   - 只在字段"还是上一次由对应基准自动生成的默认"时才覆盖,用户手改过就别动。
watch(() => system.name, (val, old) => {
  const prevDefault = `${old || ''}排障机器人`
  if (!agent.name || agent.name === prevDefault) {
    agent.name = `${val}排障机器人`
  }
})
watch(() => system.id, (val, old) => {
  const prevDefault = old ? `${old}-bot` : ''
  if (!agent.workspace_name || agent.workspace_name === prevDefault) {
    agent.workspace_name = val ? `${val}-bot` : ''
  }
})

const agentNameDefault = computed(() => `${system.name}排障机器人`)
// ${id}-bot 做目录默认;id 空时 placeholder 用占位字符,避免只显示 "-bot"
const workspaceNameDefault = computed(() => (system.id ? `${system.id}-bot` : 'my-system-bot'))

// ── Step 3: 环境列表 ──
interface EnvItem {
  id: string
  api_domain: string
  is_prod: boolean
}

const environments = reactive<EnvItem[]>(
  Array.isArray(saved?.environments) && saved.environments.length
    ? saved.environments
    : [
        { id: 'dev', api_domain: '', is_prod: false },
        { id: 'prod', api_domain: '', is_prod: true },
      ]
)

function addEnv() {
  environments.push({ id: '', api_domain: '', is_prod: false })
}

function removeEnv(idx: number) {
  if (environments.length > 1) environments.splice(idx, 1)
}

// ── Step 4: 代码仓库 ──
interface RepoItem {
  name: string
  url: string
  role: string
  stack: string
  framework: string
  service_names: string
  env_branches: Record<string, string>
}

function makeEmptyRepo(): RepoItem {
  const branches: Record<string, string> = {}
  for (const e of environments) {
    if (e.id) branches[e.id] = ''
  }
  return { name: '', url: '', role: 'backend', stack: 'go', framework: '', service_names: '', env_branches: branches }
}

const repos = reactive<RepoItem[]>(
  Array.isArray(saved?.repos) && saved.repos.length ? saved.repos : [makeEmptyRepo()]
)

function addRepo() {
  repos.push(makeEmptyRepo())
}

function removeRepo(idx: number) {
  if (repos.length > 1) repos.splice(idx, 1)
}

// Sync env_branches keys when environments change
watch(
  () => environments.map(e => e.id),
  (envIds) => {
    for (const repo of repos) {
      const newBranches: Record<string, string> = {}
      for (const eid of envIds) {
        if (!eid) continue
        newBranches[eid] = repo.env_branches[eid] || ''
      }
      repo.env_branches = newBranches
    }
  },
  { deep: true }
)

// ── Step 4 Analyze 集成 ──
// 让用户在填 repos 时一键扫描本机代码,反填 service_names(+ 给 Step 5 配置中心
// 类型一个建议)。不强制:reposRoot 空就不跑,repos 没名字扫不出东西。
// 保持范围克制:只改 service_names 字段,configCenter 建议作为 toast 提示,
// 不自动改 Step 5 选项(avoid silent 覆盖用户选择)。
const reposRootInput = ref('')
const analyzeLoading = ref(false)
const analyzeError = ref<string | null>(null)
const analyzeSummary = ref<string | null>(null) // 成功后给个一句话总结

async function pickReposRoot() {
  if (!isDesktop()) {
    analyzeError.value = '选目录需要桌面 app 环境;浏览器模式请手动输入路径'
    return
  }
  try {
    const p = await openDir('选择仓库根目录(含各个 repo.name 子目录)')
    if (p) reposRootInput.value = p
  } catch (e: any) {
    analyzeError.value = String(e?.message || e)
  }
}

async function runAnalyzeForRepos() {
  analyzeError.value = null
  analyzeSummary.value = null
  if (!reposRootInput.value.trim()) {
    analyzeError.value = '请先填仓库根目录'
    return
  }
  // 至少要一个 repo.name,不然 analyzerpipe 没东西可匹配
  const namedRepos = repos.filter(r => r.name.trim())
  if (namedRepos.length === 0) {
    analyzeError.value = '请先填至少一个仓库名'
    return
  }
  if (!isDesktop()) {
    analyzeError.value = 'Analyze 仅在桌面 app 可用(浏览器模式请用 CLI:tshoot analyze)'
    return
  }
  analyzeLoading.value = true
  try {
    const yamlText = generateYAML() // 复用 Step 7 的 yaml 构造器,当前向导状态够跑一次分析
    const r = (await bridgeAnalyze(yamlText, reposRootInput.value, false)) as {
      per_repo?: Array<{ name: string; service_names?: string[]; status: string; error?: string }>
      report?: { config_center?: string; repos?: Array<{ name: string; service_names?: string[] }> }
    }
    // 反填 service_names:per_repo 里每条去 repos 里按 name 找对应,把 service_names 数组改成逗号串
    let filled = 0
    const perRepo = r.per_repo || []
    for (const hit of perRepo) {
      if (!hit.service_names?.length) continue
      const target = repos.find(rp => rp.name === hit.name)
      if (!target) continue
      const joined = hit.service_names.join(', ')
      if (target.service_names !== joined) {
        target.service_names = joined
        filled++
      }
    }
    // 配置中心建议给 toast,避免 Step 5 静默被改
    const ccHint = r.report?.config_center && r.report.config_center !== 'unknown'
      ? `;识别到配置中心类型:${r.report.config_center}(Step 5 可据此选)`
      : ''
    analyzeSummary.value = `扫描完成:${filled} 个仓库反填了 service_names${ccHint}`
    if (filled > 0) toast.success(analyzeSummary.value)
    else toast.info(`扫描完成但没抓到 service_names;手填也行${ccHint}`)
  } catch (e: any) {
    analyzeError.value = String(e?.message || e)
  } finally {
    analyzeLoading.value = false
  }
}

// ── Step 5: 配置源 ──
const configCenterType = ref<string>(saved?.configCenterType ?? 'nacos')

// ── Step 6: 可观测性 + 数据层 ──
const observabilityOptions = ['grafana', 'loki', 'prometheus', 'jaeger', 'elk', 'skywalking', 'tempo'] as const
const dataStoreOptions = ['redis', 'mongodb', 'elasticsearch', 'mysql', 'postgresql', 'kafka', 'rocketmq', 'rabbitmq', 'clickhouse'] as const

const enabledObservability = reactive<Record<string, boolean>>({
  ...Object.fromEntries(observabilityOptions.map(k => [k, false])),
  ...(saved?.enabledObservability ?? {}),
})
const enabledDataStores = reactive<Record<string, boolean>>({
  ...Object.fromEntries(dataStoreOptions.map(k => [k, false])),
  ...(saved?.enabledDataStores ?? {}),
})

// ── Step 7: 输出目标 ──
const targetOptions = ['openclaw', 'claude-code', 'cursor', 'embedded'] as const
const targetDescriptions: Record<string, string> = {
  'openclaw': 'OpenClaw 安装包（bash install.sh 部署）',
  'claude-code': 'Claude Code（CLAUDE.md + skills/ 放到项目根即用）',
  'cursor': 'Cursor IDE（.cursorrules + .cursor/rules/）',
  'embedded': '桌面端内嵌对话(直连 LLM,8 个 provider 通吃)',
}
const enabledTargets = reactive<Record<string, boolean>>({
  ...Object.fromEntries(targetOptions.map(k => [k, true])),
  ...(saved?.enabledTargets ?? {}),
})

// Auto-save all form state so navigating away doesn't lose the draft
const lastSavedAt = ref<number | null>(null) // unix ms;null = 本会话还没保存过(首次读取态)
watch(
  () => ({
    currentStep: currentStep.value,
    system,
    agent,
    environments,
    repos,
    configCenterType: configCenterType.value,
    enabledObservability,
    enabledDataStores,
    enabledTargets,
  }),
  (val) => {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(val))
      lastSavedAt.value = Date.now()
    } catch {
      // quota / privacy-mode failures: skip silently
    }
  },
  { deep: true }
)

// "X 秒前"计时 —— 让徽章文案能随时间流动,用户看得到 Auto-save 真的在跑。
// 刷 500ms 够用;不用 setInterval 精确到毫秒,UI 节省 re-render。
const nowTick = ref(Date.now())
setInterval(() => { nowTick.value = Date.now() }, 1000)
const savedAgoLabel = computed<string>(() => {
  if (!lastSavedAt.value) return '进入页面尚未改动'
  const diffSec = Math.max(0, Math.floor((nowTick.value - lastSavedAt.value) / 1000))
  if (diffSec < 3) return '刚刚保存'
  if (diffSec < 60) return `${diffSec} 秒前保存`
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)} 分钟前保存`
  return `${Math.floor(diffSec / 3600)} 小时前保存`
})

async function clearDraft() {
  // 不用 window.confirm:Wails 的 WKWebView 默认吞掉 JS 原生对话框(避免阻塞
  // UI 线程),结果 confirm 永远返回 false。用自建 modal。
  const ok = await confirmDialog({
    title: '清空草稿',
    message: '确定清空当前草稿并重置向导吗?localStorage 里存的 7 步进度会全部删除,不可恢复。',
    confirmText: '清空',
    danger: true,
  })
  if (!ok) return
  try {
    localStorage.removeItem(STORAGE_KEY)
  } catch {
    // ignore
  }
  // 原来用 location.reload() 让 Vue 重新读 localStorage 挂状态,但 Wails
  // WKWebView 在 reload 的卸载阶段会把 Vue watcher 触发的任何 throw 向外报成
  // "Script error. at :0:0"(跨 origin 风格的匿名错),用户看到一脸懵。
  // 改成原地重置各 reactive 状态,把向导拉回 Step 1 —— 视觉等价,且没有 reload 副作用。
  currentStep.value = 1
  validationErrors.value = new Set()
  system.id = ''
  system.name = ''
  system.description = ''
  agent.name = ''
  agent.workspace_name = ''
  agent.model = 'anthropic/claude-sonnet-4-6'
  // 环境 / 仓库回到初始 1 条
  environments.splice(0, environments.length,
    { id: 'dev', api_domain: '', is_prod: false },
    { id: 'prod', api_domain: '', is_prod: true },
  )
  repos.splice(0, repos.length, makeEmptyRepo())
  // 配置源
  configCenterType.value = 'nacos'
  // 可观测 / 数据层:全关
  for (const k of observabilityOptions) enabledObservability[k] = false
  for (const k of dataStoreOptions) enabledDataStores[k] = false
  // targets:默认 4 个都开
  for (const k of targetOptions) enabledTargets[k] = true
  // Analyze 块的瞬态也清(reposRoot 输入 / 错误信息 / 上次扫描总结)
  reposRootInput.value = ''
  analyzeError.value = null
  analyzeSummary.value = null
}

// ── Import existing system.yaml into the wizard ──
const showImportDialog = ref(false)
const importText = ref('')
const importError = ref('')

function openImportDialog() {
  importText.value = ''
  importError.value = ''
  showImportDialog.value = true
}

function closeImportDialog() {
  showImportDialog.value = false
}

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

function applyImport() {
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

  // system
  if (parsed.system && typeof parsed.system === 'object') {
    system.id = parsed.system.id ?? ''
    system.name = parsed.system.name ?? ''
    system.description = parsed.system.description ?? ''
  }

  // agent
  if (parsed.agent && typeof parsed.agent === 'object') {
    agent.name = parsed.agent.name ?? ''
    agent.workspace_name = parsed.agent.workspace_name ?? ''
    agent.model = parsed.agent.model ?? agent.model
  }

  // environments
  if (Array.isArray(parsed.environments) && parsed.environments.length) {
    environments.splice(0, environments.length, ...parsed.environments.map((e: any) => ({
      id: e?.id ?? '',
      api_domain: e?.api_domain ?? '',
      is_prod: Boolean(e?.is_prod),
    })))
  }

  // repos
  if (Array.isArray(parsed.repos) && parsed.repos.length) {
    repos.splice(0, repos.length, ...parsed.repos.map((r: any) => {
      const branches: Record<string, string> = {}
      for (const env of environments) {
        if (env.id) branches[env.id] = r?.env_branches?.[env.id] ?? ''
      }
      const svcNames = Array.isArray(r?.service_names)
        ? r.service_names.join(', ')
        : (r?.service_names ?? '')
      return {
        name: r?.name ?? '',
        url: r?.url ?? '',
        role: r?.role ?? 'backend',
        stack: r?.stack ?? 'go',
        framework: r?.framework ?? '',
        service_names: svcNames,
        env_branches: branches,
      }
    }))
  }

  // config center
  const cc = parsed.infrastructure?.config_center?.type
  if (typeof cc === 'string') configCenterType.value = cc

  // observability
  const obs = parsed.infrastructure?.observability
  if (obs && typeof obs === 'object') {
    for (const key of Object.keys(enabledObservability)) {
      enabledObservability[key] = Boolean(obs?.[key]?.enabled)
    }
  }

  // data stores
  const ds = parsed.infrastructure?.data_stores
  if (Array.isArray(ds)) {
    for (const key of Object.keys(enabledDataStores)) {
      enabledDataStores[key] = false
    }
    for (const entry of ds) {
      const t = entry?.type
      if (typeof t === 'string' && t in enabledDataStores && entry?.enabled !== false) {
        enabledDataStores[t] = true
      }
    }
  }

  currentStep.value = 1
  showImportDialog.value = false
}

// ── Step 7: Preview / generate ──
const yamlOutput = ref('')
const validateResult = ref<{ ok: boolean; message: string } | null>(null)
const validateLoading = ref(false)
const copySuccess = ref(false)

// ── Skills whitelist derivation ──
function deriveSkillsWhitelist(): string[] {
  const skills: string[] = ['routing']
  if (configCenterType.value !== 'none') skills.push('config-executor')
  for (const [key, on] of Object.entries(enabledDataStores)) {
    if (on) skills.push(`${key}-runtime-query`)
  }
  if (enabledObservability.grafana) skills.push('diagram-generator')
  if (enabledObservability.jaeger || enabledObservability.skywalking || enabledObservability.tempo) skills.push('tracing-query')
  if (enabledObservability.elk) skills.push('elk-log-query')
  return skills
}

// ── YAML generation ──
function yamlStr(val: string): string {
  if (!val) return '""'
  if (/[:{}\[\],&*?|>!%#@`'"\n]/.test(val) || val.startsWith(' ') || val.endsWith(' ')) {
    return `"${val.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
  }
  return `"${val}"`
}

function generateYAML(): string {
  const lines: string[] = []

  // 顶部导言注释(解析时被忽略,只给用户看)
  lines.push('# 由初始化向导生成，可手工调整。字段说明：schema/system.schema.yaml')
  lines.push('# 以下行尾 # 注释仅为提示，YAML 解析时会被忽略。')

  // system
  lines.push('system:')
  lines.push(`  id: ${system.id || 'my-system'}                    # 机器可读标识，仅 [a-z0-9-]；用作 output_dir / agent id 前缀`)
  lines.push(`  name: ${yamlStr(system.name || 'My System')}          # 用户可见名称（中/英均可）`)
  if (system.description) lines.push(`  description: ${yamlStr(system.description)}`)

  // agent
  lines.push('')
  lines.push('agent:')
  lines.push(`  name: ${yamlStr(agent.name || agentNameDefault.value)}`)
  lines.push(`  workspace_name: ${yamlStr(agent.workspace_name || workspaceNameDefault.value)}    # OpenClaw 工作区目录名（~/.openclaw/workspace/<这里>）；推荐 ASCII 小写避免 CJK 目录名`)
  lines.push(`  model: ${agent.model}    # LLM model id;前缀决定 provider(anthropic/openai/deepseek/qwen/minimax/moonshot/zhipu/ollama)`)
  lines.push('  style:')
  lines.push('    tone: direct')
  lines.push('    verbosity: terse')

  // environments
  lines.push('')
  lines.push('# environments：声明系统的所有环境。每个 env 会注册一套独立的 MCP 实例')
  lines.push('# （如 nacos-mcp-server-dev / -prod），机器人按 is_prod 调整谨慎度。')
  lines.push('environments:')
  for (const env of environments) {
    lines.push(`  - id: ${env.id || 'env'}`)
    if (env.api_domain) lines.push(`    api_domain: ${env.api_domain}     # 本 env 的对外访问域名，用于接口实测`)
    lines.push(`    is_prod: ${env.is_prod}         # 生产环境标记：true 时机器人默认更保守、查询前二次确认`)
  }

  // repos
  lines.push('')
  lines.push('# repos：所有纳入排障范围的代码仓库。role/stack 决定 analyzer 与 skill 策略。')
  lines.push('repos:')
  for (const repo of repos) {
    lines.push(`  - name: ${repo.name || 'my-service'}`)
    lines.push(`    url: ${repo.url || 'git@github.com:org/repo.git'}`)
    lines.push(`    role: ${repo.role}         # backend/frontend/gateway/infra/shared`)
    lines.push(`    stack: ${repo.stack}             # go/java/node/php/python，决定用哪种配置扫描器`)
    if (repo.framework) lines.push(`    framework: ${repo.framework}`)
    if (repo.service_names.trim()) {
      lines.push('    service_names:       # 本 repo 实际部署出来的 service 名（config-map 以此为 key）')
      for (const sn of repo.service_names.split(',').map(s => s.trim()).filter(Boolean)) {
        lines.push(`      - ${sn}`)
      }
    }
    const branchEntries = Object.entries(repo.env_branches).filter(([, v]) => v)
    if (branchEntries.length) {
      lines.push('    env_branches:        # 每个 env 对应的长期分支；routing skill 据此切换代码')
      for (const [eid, branch] of branchEntries) {
        lines.push(`      ${eid}: ${branch}`)
      }
    }
  }

  // infrastructure
  lines.push('')
  lines.push('infrastructure:')

  // config_center
  lines.push('  config_center:        # 配置中心：nacos/apollo/consul/kubernetes/env-vars/none')
  lines.push(`    type: ${configCenterType.value}`)
  if (configCenterType.value !== 'none' && configCenterType.value !== 'env-vars' && configCenterType.value !== 'kubernetes') {
    lines.push('    endpoints:')
    for (const env of environments) {
      lines.push(`      - env: ${env.id}`)
      lines.push(`        addr: "{{CONFIG_CENTER_ADDR_${env.id.toUpperCase()}}}"`)
    }
    lines.push('    auth:')
    lines.push('      username_placeholder: "{{CONFIG_CENTER_USERNAME}}"')
    lines.push('      password_placeholder: "{{CONFIG_CENTER_PASSWORD}}"')
  }

  // observability
  const anyObs = Object.values(enabledObservability).some(Boolean)
  if (anyObs) {
    lines.push('')
    lines.push('  observability:')
    if (enabledObservability.grafana) {
      lines.push('    grafana:')
      lines.push('      enabled: true')
      lines.push('      url_by_env:')
      for (const env of environments) {
        lines.push(`        ${env.id}: "{{GRAFANA_${env.id.toUpperCase()}_URL}}"`)
      }
      lines.push('      auth:')
      lines.push('        username_placeholder: "{{GRAFANA_USERNAME}}"')
      lines.push('        password_placeholder: "{{GRAFANA_PASSWORD}}"')
    }
    if (enabledObservability.loki) {
      lines.push('    loki:')
      lines.push('      enabled: true')
      lines.push(`      via_grafana: ${enabledObservability.grafana}`)
    }
    if (enabledObservability.prometheus) {
      lines.push('    prometheus:')
      lines.push('      enabled: true')
      lines.push(`      via_grafana: ${enabledObservability.grafana}`)
    }
    if (enabledObservability.jaeger) {
      lines.push('    jaeger:')
      lines.push('      enabled: true')
      lines.push('      url_by_env:')
      for (const env of environments) {
        lines.push(`        ${env.id}: "{{JAEGER_${env.id.toUpperCase()}_URL}}"`)
      }
    }
    if (enabledObservability.elk) {
      lines.push('    elk:')
      lines.push('      enabled: true')
      lines.push(`      default_index: "${system.id || 'my-system'}-logs-*"`)
    }
    if (enabledObservability.skywalking) {
      lines.push('    skywalking:')
      lines.push('      enabled: true')
    }
    if (enabledObservability.tempo) {
      lines.push('    tempo:')
      lines.push('      enabled: true')
    }
  }

  // data_stores
  const enabledDS = Object.entries(enabledDataStores).filter(([, on]) => on)
  if (enabledDS.length) {
    lines.push('')
    lines.push('  data_stores:          # 启用的数据层：每个会生成对应 runtime-query skill（只读）')
    for (const [dsType] of enabledDS) {
      lines.push(`    - type: ${dsType}`)
      lines.push('      enabled: true')
      const disc = configCenterType.value !== 'none' ? 'from_config_center' : 'static'
      const discComment = disc === 'from_config_center' ? '# 运行时通过配置中心拿连接串' : '# install.sh 交互时直接收集连接串'
      lines.push(`      discovery: ${disc}   ${discComment}`)
      lines.push('      readonly_enforced: true    # 强制只读；generator 拒绝写操作')
    }
  }

  // generation
  const skills = deriveSkillsWhitelist()
  lines.push('')
  lines.push('generation:')
  lines.push('  target_host: openclaw                # 兼容字段；实际 target 由 targets 列表决定')
  lines.push(`  output_dir: ./dist/${system.id || 'my-system'}          # 产物目录；多 target 会额外产出 <output_dir>-<target>/`)
  const selectedTargets = targetOptions.filter(t => enabledTargets[t])
  const targetList = selectedTargets.length ? selectedTargets : ['openclaw']
  lines.push('  targets:                             # 每个 target 产出一份机器人产物（同一份 system.yaml）')
  for (const t of targetList) {
    lines.push(`    - ${t}`)
  }
  lines.push('  skills_whitelist:                    # 只有列出的 skill 会进工作区；未列入的模板不会渲染')
  for (const s of skills) {
    lines.push(`    - ${s}`)
  }
  lines.push('  verified_only: false')
  lines.push('  mapping_review_mode: strict')
  lines.push('  preserve_on_regenerate:              # 这些文件在下次 gen 时整体保留（让用户的手改不丢）')
  lines.push('    - SOUL.md')
  lines.push('    - USER.md')
  lines.push('    - CHECKLIST.md')

  // meta
  lines.push('')
  lines.push('meta:')
  lines.push('  schema_version: "0.1"')
  lines.push('  tshoot_template_ref:')
  lines.push('    repo: troubleshooter-studio')
  lines.push('    ref: main')

  return lines.join('\n') + '\n'
}

// ── Validation ──
function validateStep(step: number): boolean {
  validationErrors.value.clear()
  switch (step) {
    case 1:
      if (!system.id) validationErrors.value.add('system.id')
      if (system.id && !/^[a-z0-9-]+$/.test(system.id)) validationErrors.value.add('system.id')
      if (!system.name) validationErrors.value.add('system.name')
      break
    case 2:
      if (!agent.name) validationErrors.value.add('agent.name')
      if (!agent.workspace_name) validationErrors.value.add('agent.workspace_name')
      if (!agent.model) validationErrors.value.add('agent.model')
      break
    case 3:
      environments.forEach((e, i) => {
        if (!e.id) validationErrors.value.add(`env.${i}.id`)
        if (!e.api_domain) validationErrors.value.add(`env.${i}.api_domain`)
      })
      break
    case 4:
      repos.forEach((r, i) => {
        if (!r.name) validationErrors.value.add(`repo.${i}.name`)
        if (!r.url) validationErrors.value.add(`repo.${i}.url`)
      })
      break
    default:
      break
  }
  return validationErrors.value.size === 0
}

function hasError(field: string): boolean {
  return validationErrors.value.has(field)
}

function nextStep() {
  if (!validateStep(currentStep.value)) return
  if (currentStep.value < totalSteps) {
    currentStep.value++
    if (currentStep.value === totalSteps) {
      yamlOutput.value = generateYAML()
    }
  }
}

function prevStep() {
  validationErrors.value.clear()
  if (currentStep.value > 1) currentStep.value--
}

function goToStep(step: number) {
  if (step < currentStep.value) {
    validationErrors.value.clear()
    currentStep.value = step
  }
}

// ── Step 7 actions ──
async function validateYAML() {
  validateLoading.value = true
  validateResult.value = null
  try {
    const r = await bridgeValidate(yamlOutput.value)
    validateResult.value = {
      ok: true,
      message: `验证通过：${r.name || r.system}（${r.envs} 环境 / ${r.repos} 仓库）`,
    }
  } catch (err: any) {
    validateResult.value = { ok: false, message: `验证失败：${err.message || err}` }
  } finally {
    validateLoading.value = false
  }
}

async function copyYAML() {
  try {
    await navigator.clipboard.writeText(yamlOutput.value)
    copySuccess.value = true
    setTimeout(() => (copySuccess.value = false), 2000)
  } catch {
    // fallback
    const ta = document.createElement('textarea')
    ta.value = yamlOutput.value
    document.body.appendChild(ta)
    ta.select()
    document.execCommand('copy')
    document.body.removeChild(ta)
    copySuccess.value = true
    setTimeout(() => (copySuccess.value = false), 2000)
  }
}

function downloadYAML() {
  const blob = new Blob([yamlOutput.value], { type: 'text/yaml' })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = 'system.yaml'
  a.click()
  URL.revokeObjectURL(url)
}

// ── 一键部署 ──
// 之前走完向导只能下 yaml → 跳 BotsPage → 导入 → 选 target → 填路径,4 步。
// 现在在 Step 7 直接选 target + 目标路径一键部署:调 importAndDeploy(复用
// BotsPage 那条闭环),成功后跳 /bots 看刚装好的卡。
//
// target 分两类(useDeployPath 判):
//   - embedded/openclaw:Studio 替用户管路径(~/.tshoot/<target>/<id>/),默认隐路径 input
//   - claude-code/cursor:必须选项目根,保持 input 可见
const deployTarget = ref<'openclaw' | 'claude-code' | 'cursor' | 'embedded'>('openclaw')
const deployDestPath = ref('')
const deployLoading = ref(false)
const deployError = ref<string | null>(null)

const { isManagedTarget, customPathExpanded, autoDefaultPath, resetCustomPath } = useDeployPath(
  deployTarget,
  () => system.id,
  deployDestPath,
)

const deployTargetHint = computed(() => {
  switch (deployTarget.value) {
    case 'openclaw': return 'Studio 托管产物(→ install.sh 装到 ~/.openclaw/workspace/<workspace_name>/)'
    case 'claude-code': return '装到项目根:会写 CLAUDE.md + skills/ + install.sh'
    case 'cursor': return '装到项目根:会写 .cursorrules + .cursor/rules/ + skills/'
    case 'embedded': return 'Studio 托管产物;对话直接在工作台内开(直连 model 对应 provider 的 LLM API)'
  }
  return ''
})

async function pickDeployDestPath() {
  if (!isDesktop()) {
    deployError.value = '选目录需要桌面 app 环境'
    return
  }
  try {
    const p = await openDir('选择部署目标路径')
    if (p) deployDestPath.value = p
  } catch (e: any) {
    deployError.value = String(e?.message || e)
  }
}

async function runOneClickDeploy() {
  deployError.value = null
  if (!deployDestPath.value.trim()) {
    deployError.value = '请填部署目标路径'
    return
  }
  if (!isDesktop()) {
    deployError.value = '一键部署只在桌面 app 可用;浏览器模式请下载 yaml 去 BotsPage 或用 CLI'
    return
  }
  // 部署前校一把 yaml,失败就不提交到后端兜错
  try {
    await bridgeValidate(yamlOutput.value)
  } catch (e: any) {
    deployError.value = `yaml 校验失败:${String(e?.message || e)};请先点"✓ 验证"修复`
    return
  }
  deployLoading.value = true
  try {
    await importAndDeploy(yamlOutput.value, deployTarget.value, deployDestPath.value)
    toast.success(`部署完成,已写到 ${deployDestPath.value}`)
    // 跳已装机器人页让用户看到新装的卡;openclaw 还要跑 install.sh,BotsPage
    // "导入 yaml 一键部署"流程那边已经做过,这里新用户的第一印象是"看到卡就算成功"。
    router.push('/bots')
  } catch (e: any) {
    deployError.value = String(e?.message || e)
  } finally {
    deployLoading.value = false
  }
}

const roleOptions = ['backend', 'frontend', 'gateway', 'infra', 'shared']
const stackOptions = ['go', 'java', 'node', 'php', 'python']
const configTypeOptions = ['nacos', 'apollo', 'consul', 'env-vars', 'kubernetes', 'none']

const configTypeDescriptions: Record<string, string> = {
  nacos: 'Nacos 配置中心（阿里系最常用）',
  apollo: 'Apollo 配置中心（携程开源）',
  consul: 'Consul KV（HashiCorp）',
  'env-vars': '纯环境变量 / .env 文件（无远程配置中心）',
  kubernetes: 'K8s ConfigMap / Secret',
  none: '不使用配置中心',
}
</script>

<template>
  <div class="init-page">
    <div class="page-header">
      <div>
        <h1>初始化向导</h1>
        <p class="subtitle">通过可视化表单生成 system.yaml 配置文件（草稿会自动保存到本地）</p>
      </div>
      <div class="header-actions">
        <!-- 自动保存徽章:让用户感知到"改动一直在存"(类似 Notion/Google Docs 的风格),
             lastSavedAt=null = 首次进入无改动(徽章灰态),有值 = 蓝态 + 计时刷新 -->
        <span class="autosave-badge" :class="{ idle: lastSavedAt === null }" :title="lastSavedAt === null ? '尚未触发自动保存;做任何改动后会自动保存到浏览器 localStorage' : '草稿存在浏览器 localStorage,切换页面不丢;清空草稿按钮可重置'">
          <span class="autosave-dot" />
          {{ lastSavedAt === null ? '草稿空' : `✓ 自动保存 · ${savedAgoLabel}` }}
        </span>
        <button class="btn link" @click="openImportDialog" title="把已有 yaml 反填到向导各步骤,继续编辑调整">导入 YAML 到向导编辑</button>
        <button class="btn link" @click="clearDraft">清空草稿</button>
      </div>
    </div>

    <!-- Import YAML modal -->
    <div v-if="showImportDialog" class="modal-mask" @click.self="closeImportDialog">
      <div class="modal">
        <div class="modal-header">
          <span>导入已有 system.yaml</span>
          <button class="btn-icon close" @click="closeImportDialog">&times;</button>
        </div>
        <div class="modal-body">
          <p class="help-text" style="margin-bottom: 10px;">
            上传或粘贴现有 system.yaml 内容，字段会自动反填到各步骤。
          </p>
          <label class="btn file-label">
            选择文件...
            <input type="file" accept=".yaml,.yml" @change="handleImportFile" style="display:none" />
          </label>
          <textarea
            v-model="importText"
            rows="14"
            placeholder="或直接粘贴 system.yaml 的 YAML 内容…"
            class="import-textarea"
            spellcheck="false"
          />
          <div v-if="importError" class="error-text" style="margin-top: 6px;">{{ importError }}</div>
        </div>
        <div class="modal-footer">
          <button class="btn" @click="closeImportDialog">取消</button>
          <button class="btn primary" :disabled="!importText.trim()" @click="applyImport">反填到向导</button>
        </div>
      </div>
    </div>

    <!-- Guidance info box -->
    <div class="info-box">
      <p><strong>本向导帮助你快速生成 system.yaml 配置文件</strong></p>
      <p>system.yaml 描述你的系统架构（仓库、环境、配置中心、基础组件），tshoot 据此生成并部署定制化的 AI 排障机器人</p>
      <p>完成后可「验证」确保格式正确，然后「下载」到本地</p>
    </div>

    <!-- Step indicator -->
    <div class="step-indicator">
      <div
        v-for="s in totalSteps"
        :key="s"
        class="step-dot-group"
        :class="{ clickable: s < currentStep }"
        @click="goToStep(s)"
      >
        <div class="step-dot" :class="{ active: s === currentStep, done: s < currentStep }">
          {{ s }}
        </div>
        <div class="step-label" :class="{ active: s === currentStep }">{{ stepTitles[s - 1] }}</div>
        <div v-if="s < totalSteps" class="step-line" :class="{ done: s < currentStep }" />
      </div>
    </div>

    <!-- Step 1 -->
    <div v-if="currentStep === 1" class="card lg">
      <h2>系统基本信息</h2>
      <div class="form-group">
        <label>系统 ID <span class="required">*</span>
          <span class="help-icon" title="机器可读标识，仅允许 [a-z0-9-]。会用作 output_dir、agent id 前缀、MCP 实例名前缀。一般用产品英文缩写，如 shop / mall / order。">?</span>
        </label>
        <input
          v-model="system.id"
          type="text"
          placeholder="my-system (仅小写字母、数字、短横线)"
          :class="{ error: hasError('system.id') }"
        />
        <span v-if="hasError('system.id')" class="error-text">必填，且仅允许 [a-z0-9-]</span>
      </div>
      <div class="form-group">
        <label>系统显示名 <span class="required">*</span></label>
        <input
          v-model="system.name"
          type="text"
          placeholder="我的系统"
          :class="{ error: hasError('system.name') }"
        />
        <span v-if="hasError('system.name')" class="error-text">必填</span>
      </div>
      <div class="form-group">
        <label>系统描述</label>
        <textarea v-model="system.description" placeholder="一句话描述你的系统（选填）" rows="3" />
      </div>
    </div>

    <!-- Step 2 -->
    <div v-if="currentStep === 2" class="card lg">
      <h2>机器人身份</h2>
      <div class="form-group">
        <label>机器人名称 <span class="required">*</span></label>
        <input
          v-model="agent.name"
          type="text"
          :placeholder="agentNameDefault"
          :class="{ error: hasError('agent.name') }"
        />
      </div>
      <div class="form-group">
        <label>工作区名称 <span class="required">*</span>
          <span class="help-icon" title="OpenClaw 会在 ~/.openclaw/workspace/<这里>/ 创建目录。推荐 ASCII 小写（如 shop-bot）:CJK 目录名在 cd/ls/tab 补全时要引号比较烦。多个 agent 并存时每个用不同名字避免覆盖。">?</span>
        </label>
        <input
          v-model="agent.workspace_name"
          type="text"
          :placeholder="workspaceNameDefault"
          :class="{ error: hasError('agent.workspace_name') }"
        />
      </div>
      <div class="form-group">
        <label>模型 <span class="required">*</span>
          <span class="help-icon" title="agent.model 格式 <provider>/<model-id>,前缀决定路由到哪家 LLM(anthropic/openai/deepseek/qwen/minimax/moonshot/zhipu/ollama)。OpenClaw 走 gateway;Claude Code / Cursor 只作为文档记录;Embedded 内嵌对话由 Studio 直连 provider(不依赖 anthropic SDK)。">?</span>
        </label>
        <select v-model="modelSelectValue" :class="{ error: hasError('agent.model') }">
          <optgroup v-for="g in modelGroups" :key="g.group" :label="g.group">
            <option v-for="m in g.items" :key="m.value" :value="m.value">{{ m.label }}</option>
          </optgroup>
          <option :value="MODEL_CUSTOM">自定义 / 企业内部网关 / 新模型 …</option>
        </select>
        <input
          v-if="modelIsCustom"
          v-model="agent.model"
          type="text"
          placeholder="填任意 model id，如 openai-compat/my-gateway/some-model"
          style="margin-top: 6px"
          :class="{ error: hasError('agent.model') }"
        />
      </div>
    </div>

    <!-- Step 3 -->
    <div v-if="currentStep === 3" class="card lg">
      <h2>环境列表</h2>
      <p class="help-text">常见环境：dev（开发）、test（测试）、staging（预发布）、prod（生产）</p>
      <div v-for="(env, i) in environments" :key="i" class="dynamic-row">
        <div class="row-fields">
          <div class="form-group compact">
            <label>环境 ID
              <span class="help-icon" title="环境短标识（dev/test/staging/prod）。每个 env 会注册一套独立的 MCP 实例：nacos-mcp-server-<ID>、grafana-mcp-server-<ID> 等。">?</span>
            </label>
            <input
              v-model="env.id"
              type="text"
              placeholder="dev"
              :class="{ error: hasError(`env.${i}.id`) }"
            />
          </div>
          <div class="form-group compact">
            <label>API 域名</label>
            <input
              v-model="env.api_domain"
              type="text"
              placeholder="api-dev.example.com"
              :class="{ error: hasError(`env.${i}.api_domain`) }"
            />
          </div>
          <div class="form-group compact checkbox-group">
            <label :title="'is_prod=true 时机器人更保守：执行写入/重启类动作前会二次确认；OpenClaw 客户端 UI 也会标红。'">
              <input type="checkbox" v-model="env.is_prod" />
              生产环境
              <span class="help-icon">?</span>
            </label>
          </div>
          <button class="btn-icon remove" @click="removeEnv(i)" :disabled="environments.length <= 1" title="删除">
            &times;
          </button>
        </div>
      </div>
      <button class="btn" @click="addEnv">+ 添加环境</button>
    </div>

    <!-- Step 4 -->
    <div v-if="currentStep === 4" class="card lg">
      <h2>代码仓库</h2>
      <p class="help-text">每个仓库对应一个代码仓库。role 描述角色（backend=后端、frontend=前端、gateway=网关/BFF）</p>

      <!-- Analyze 集成:输入仓库根目录 + 已填仓库名 → 一键扫描反填 service_names。
           折叠在最上面,用户不关心时也不挡视线。-->
      <details class="analyze-block">
        <summary>
          <span>🔍 扫代码自动填 service_names(可选)</span>
          <span class="analyze-hint">需要先填仓库名,并填本机已 clone 的仓库根目录</span>
        </summary>
        <div class="analyze-body">
          <div class="analyze-row">
            <input
              v-model="reposRootInput"
              type="text"
              placeholder="例:~/code/all-repos 或绝对路径 /Users/xxx/repos"
            />
            <button type="button" class="btn" :disabled="analyzeLoading" @click="pickReposRoot">
              选目录…
            </button>
            <button
              type="button"
              class="btn primary"
              :disabled="analyzeLoading || !reposRootInput.trim()"
              @click="runAnalyzeForRepos"
            >
              {{ analyzeLoading ? '扫描中…' : '扫描并反填' }}
            </button>
          </div>
          <div v-if="analyzeError" class="alert error">{{ analyzeError }}</div>
          <div v-else-if="analyzeSummary" class="alert success">{{ analyzeSummary }}</div>
          <p class="help-text" style="margin-top: 8px;">
            analyzer 会按 repo.name 匹配 <code>&lt;仓库根&gt;/&lt;repo.name&gt;/</code> 子目录,
            扫出 service_names(K8s deployment / Nacos data_id 前缀)+ 配置中心类型线索。
            命中后会覆盖对应仓库的 service_names 字段;不命中的保持手填不动。
          </p>
        </div>
      </details>

      <div v-for="(repo, i) in repos" :key="i" class="repo-block">
        <div class="repo-header">
          <span class="repo-badge">仓库 {{ i + 1 }}</span>
          <button class="btn-icon remove" @click="removeRepo(i)" :disabled="repos.length <= 1">&times;</button>
        </div>
        <div class="row-fields">
          <div class="form-group compact">
            <label>仓库名 <span class="required">*</span></label>
            <input
              v-model="repo.name"
              type="text"
              placeholder="order-service"
              :class="{ error: hasError(`repo.${i}.name`) }"
            />
          </div>
          <div class="form-group compact">
            <label>仓库地址 <span class="required">*</span></label>
            <input
              v-model="repo.url"
              type="text"
              placeholder="git@github.com:org/repo.git"
              :class="{ error: hasError(`repo.${i}.url`) }"
            />
          </div>
        </div>
        <div class="row-fields">
          <div class="form-group compact">
            <label>角色</label>
            <select v-model="repo.role">
              <option v-for="r in roleOptions" :key="r" :value="r">{{ r }}</option>
            </select>
          </div>
          <div class="form-group compact">
            <label>技术栈</label>
            <select v-model="repo.stack">
              <option v-for="s in stackOptions" :key="s" :value="s">{{ s }}</option>
            </select>
          </div>
          <div class="form-group compact">
            <label>框架（可选）</label>
            <input v-model="repo.framework" type="text" placeholder="spring-boot" />
          </div>
        </div>
        <div class="form-group">
          <label>服务名 (逗号分隔)
            <span class="help-icon" title="本仓库实际部署出来的服务名（= K8s deployment / Nacos 的 data_id 前缀），可能与仓库名不同。一个仓库跑多个服务常见：order-repo → order-service, order-worker。留空则默认用仓库名。">?</span>
          </label>
          <input v-model="repo.service_names" type="text" placeholder="order-service, order-worker" />
        </div>
        <div class="form-group">
          <label>环境分支映射
            <span class="help-icon" title="每个 env 对应的长期分支。routing skill 会据此帮用户切到正确的代码分支再做代码定位。例：dev=develop / prod=main。">?</span>
          </label>
          <div class="branch-grid">
            <div v-for="env in environments" :key="env.id" class="branch-item">
              <span class="branch-env">{{ env.id || '?' }}</span>
              <input
                v-model="repo.env_branches[env.id]"
                type="text"
                :placeholder="env.is_prod ? 'main' : 'develop'"
              />
            </div>
          </div>
        </div>
      </div>
      <button class="btn" @click="addRepo">+ 添加仓库</button>
    </div>

    <!-- Step 5 -->
    <div v-if="currentStep === 5" class="card lg">
      <h2>配置源</h2>
      <div class="form-group">
        <label>配置中心类型</label>
        <div class="radio-options">
          <label v-for="t in configTypeOptions" :key="t" class="radio-label" :class="{ selected: configCenterType === t }">
            <input type="radio" v-model="configCenterType" :value="t" />
            <span class="radio-content">
              <span class="radio-title">{{ t }}</span>
              <span class="radio-desc">{{ configTypeDescriptions[t] }}</span>
            </span>
          </label>
        </div>
      </div>
    </div>

    <!-- Step 6 -->
    <div v-if="currentStep === 6" class="card lg">
      <h2>可观测性 + 数据层</h2>
      <h3>可观测性组件</h3>
      <div class="checkbox-grid">
        <label v-for="obs in observabilityOptions" :key="obs" class="check-label">
          <input type="checkbox" v-model="enabledObservability[obs]" />
          {{ obs }}
        </label>
      </div>
      <h3>数据层组件</h3>
      <div class="checkbox-grid">
        <label v-for="ds in dataStoreOptions" :key="ds" class="check-label">
          <input type="checkbox" v-model="enabledDataStores[ds]" />
          {{ ds }}
        </label>
      </div>
    </div>

    <!-- Step 7 -->
    <div v-if="currentStep === 7" class="card lg">
      <h2>预览 + 生成</h2>
      <h3 style="margin-top:0">输出目标（勾选要产出的部署形态）</h3>
      <p class="help-text">默认全部 4 种；运行 <code>tshoot gen</code> 时会一次性生成 <code>&lt;output_dir&gt;-&lt;target&gt;/</code> 兄弟目录。</p>
      <div class="checkbox-grid">
        <label v-for="t in targetOptions" :key="t" class="check-label" :title="targetDescriptions[t]">
          <input type="checkbox" v-model="enabledTargets[t]" @change="yamlOutput = generateYAML()" />
          <span>{{ t }}</span>
          <span style="color:#94a3b8;font-size:11px;margin-left:4px">— {{ targetDescriptions[t] }}</span>
        </label>
      </div>
      <div class="yaml-preview">
        <pre><code>{{ yamlOutput }}</code></pre>
      </div>
      <div class="action-bar">
        <button class="btn primary" @click="validateYAML" :disabled="validateLoading">
          {{ validateLoading ? '验证中...' : '✓ 验证' }}
        </button>
        <button class="btn" @click="copyYAML">
          {{ copySuccess ? '已复制 ✓' : '📋 复制到剪贴板' }}
        </button>
        <button class="btn" @click="downloadYAML">⬇ 下载 system.yaml</button>
      </div>
      <div v-if="validateResult" class="validate-result" :class="{ success: validateResult.ok, fail: !validateResult.ok }">
        {{ validateResult.message }}
      </div>

      <!-- 一键部署:yaml 预览完直接装,省 BotsPage 那一圈 -->
      <div class="deploy-inline">
        <div class="deploy-inline-title">🚀 一键部署到已装机器人</div>
        <p class="help-text" style="margin-bottom:10px;">
          跳过"下载 yaml → BotsPage 导入"的来回,直接选 target + 目标路径部署。部署完会跳到「已装机器人」页。
        </p>
        <div class="deploy-inline-row">
          <div class="deploy-inline-field">
            <label>目标平台</label>
            <select v-model="deployTarget" :disabled="deployLoading">
              <option value="openclaw">OpenClaw</option>
              <option value="claude-code">Claude Code</option>
              <option value="cursor">Cursor IDE</option>
              <option value="embedded">Embedded (内嵌对话)</option>
            </select>
            <span class="deploy-hint">{{ deployTargetHint }}</span>
          </div>
          <!-- embedded/openclaw:默认路径不露 input,用户不用操心;要改点"自定义"展开 -->
          <div v-if="isManagedTarget && !customPathExpanded" class="deploy-inline-field flex auto-path-field">
            <label>部署位置 <span class="auto-tag">自动管理</span></label>
            <div class="auto-path-display">
              <code>{{ autoDefaultPath || '…' }}</code>
              <button type="button" class="btn-link" @click="customPathExpanded = true">自定义 →</button>
            </div>
          </div>
          <!-- claude-code/cursor 必选,或 embedded/openclaw 展开"自定义"后的 input -->
          <div v-else class="deploy-inline-field flex">
            <label>
              部署目标路径
              <button
                v-if="isManagedTarget"
                type="button"
                class="btn-link"
                @click="resetCustomPath"
              >恢复默认</button>
            </label>
            <div class="deploy-inline-path">
              <input
                v-model="deployDestPath"
                type="text"
                :placeholder="isManagedTarget ? autoDefaultPath : '项目根路径(如 ~/my-project)'"
                :disabled="deployLoading"
              />
              <button type="button" class="btn" :disabled="deployLoading" @click="pickDeployDestPath">选目录…</button>
            </div>
          </div>
        </div>
        <div class="deploy-inline-actions">
          <button
            type="button"
            class="btn primary"
            :disabled="deployLoading || !deployDestPath.trim()"
            @click="runOneClickDeploy"
          >
            {{ deployLoading ? '部署中…' : '一键部署' }}
          </button>
        </div>
        <div v-if="deployError" class="alert error">{{ deployError }}</div>
      </div>
    </div>

    <!-- Navigation buttons -->
    <div class="nav-buttons">
      <button v-if="currentStep > 1" class="btn" @click="prevStep">上一步</button>
      <span v-else />
      <button v-if="currentStep < totalSteps" class="btn primary" @click="nextStep">下一步</button>
    </div>
  </div>
</template>

<style scoped>
.init-page {
  max-width: 860px;
  margin: 0 auto;
}

.init-page h1 {
  font-size: 24px;
  color: #1e293b;
  margin-bottom: 4px;
}

.subtitle {
  color: #64748b;
  font-size: 15px;
  margin-bottom: 28px;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
  margin-bottom: 28px;
}

.page-header .subtitle {
  margin-bottom: 0;
}

.header-actions {
  display: flex;
  gap: 8px;
  align-items: center;
  flex-shrink: 0;
  margin-top: 4px;
}

/* 自动保存徽章:默认蓝态(在跑),idle = 灰态(还没触发过)。跟 header 其它 link 按钮并排放,不抢眼。 */
.autosave-badge {
  display: inline-flex; align-items: center; gap: 6px;
  font-size: 11px; color: #1e40af; background: #eff6ff;
  padding: 3px 10px; border-radius: 10px; border: 1px solid #bfdbfe;
  cursor: default; user-select: none;
  font-variant-numeric: tabular-nums; /* 秒数跳变时数字不抖 */
  white-space: nowrap;
}
.autosave-badge.idle { color: #64748b; background: #f1f5f9; border-color: #e2e8f0; }
.autosave-dot {
  width: 6px; height: 6px; border-radius: 50%;
  background: #22c55e;
  box-shadow: 0 0 0 2px rgba(34, 197, 94, 0.2);
}
.autosave-badge.idle .autosave-dot {
  background: #94a3b8; box-shadow: none;
}

/* Import modal */
.modal-mask {
  position: fixed;
  inset: 0;
  background: rgba(15, 23, 42, 0.45);
  display: flex;
  align-items: center;
  justify-content: center;
  z-index: 100;
}
.modal {
  background: #fff;
  border-radius: 10px;
  width: min(720px, 92vw);
  max-height: 86vh;
  display: flex;
  flex-direction: column;
  box-shadow: 0 20px 40px rgba(0, 0, 0, 0.18);
}
.modal-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  padding: 14px 18px;
  border-bottom: 1px solid #e2e8f0;
  font-weight: 600;
  color: #1e293b;
}
.modal-header .close {
  background: none;
  color: #64748b;
  width: 28px;
  height: 28px;
  font-size: 20px;
  border: none;
  cursor: pointer;
  border-radius: 4px;
}
.modal-header .close:hover {
  background: #f1f5f9;
  color: #1e293b;
}
.modal-body {
  padding: 16px 18px;
  overflow-y: auto;
}
.modal-footer {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  padding: 12px 18px;
  border-top: 1px solid #e2e8f0;
}
.import-textarea {
  width: 100%;
  margin-top: 10px;
  font-family: 'SF Mono', 'Fira Code', Consolas, monospace;
  font-size: 12.5px;
  padding: 10px 12px;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  resize: vertical;
}
.file-label {
  display: inline-block;
  cursor: pointer;
}

/* .btn / .btn.link 来自全局 design.css */

/* ── Step indicator ── */
.step-indicator {
  display: flex;
  align-items: flex-start;
  margin-bottom: 32px;
  gap: 0;
}

.step-dot-group {
  display: flex;
  flex-direction: column;
  align-items: center;
  position: relative;
  flex: 1;
}

.step-dot-group.clickable {
  cursor: pointer;
}

.step-dot {
  width: 32px;
  height: 32px;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 14px;
  font-weight: 600;
  background: #e2e8f0;
  color: #64748b;
  position: relative;
  z-index: 1;
  transition: all 0.2s;
}

.step-dot.active {
  background: #3b82f6;
  color: #fff;
  box-shadow: 0 0 0 4px rgba(59, 130, 246, 0.2);
}

.step-dot.done {
  background: #10b981;
  color: #fff;
}

.step-label {
  font-size: 11px;
  color: #94a3b8;
  margin-top: 6px;
  text-align: center;
  white-space: nowrap;
}

.step-label.active {
  color: #3b82f6;
  font-weight: 600;
}

.step-line {
  position: absolute;
  top: 16px;
  left: calc(50% + 16px);
  width: calc(100% - 32px);
  height: 2px;
  background: #e2e8f0;
  z-index: 0;
}

.step-line.done {
  background: #10b981;
}

/* ── Card ── */
.card {
  background: #fff;
  border: 1px solid #e2e8f0;
  border-radius: 10px;
  padding: 28px;
  box-shadow: 0 1px 3px rgba(0, 0, 0, 0.06), 0 1px 2px rgba(0, 0, 0, 0.04);
  margin-bottom: 20px;
}

/* ── Form elements ── */
.form-group {
  margin-bottom: 16px;
}

.form-group label {
  display: block;
  font-size: 13px;
  font-weight: 500;
  color: #475569;
  margin-bottom: 5px;
}

.required {
  color: #ef4444;
}

.help-icon {
  display: inline-block;
  width: 14px;
  height: 14px;
  line-height: 14px;
  text-align: center;
  font-size: 10px;
  font-weight: 700;
  color: #64748b;
  background: #e2e8f0;
  border-radius: 50%;
  margin-left: 4px;
  cursor: help;
  vertical-align: middle;
  transition: all 0.15s;
}
.help-icon:hover {
  background: #3b82f6;
  color: #fff;
}

input[type="text"],
textarea,
select {
  width: 100%;
  padding: 8px 12px;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  font-size: 14px;
  color: #1e293b;
  background: #fff;
  transition: border-color 0.15s;
  font-family: inherit;
}

input[type="text"]:focus,
textarea:focus,
select:focus {
  outline: none;
  border-color: #3b82f6;
  box-shadow: 0 0 0 3px rgba(59, 130, 246, 0.1);
}

input.error,
textarea.error {
  border-color: #ef4444;
}

.error-text {
  color: #ef4444;
  font-size: 12px;
  margin-top: 3px;
  display: block;
}

/* ── Dynamic rows ── */
.dynamic-row {
  padding: 12px 0;
  border-bottom: 1px solid #f1f5f9;
}

.dynamic-row:last-of-type {
  border-bottom: none;
}

.row-fields {
  display: flex;
  gap: 12px;
  align-items: flex-end;
}

.form-group.compact {
  flex: 1;
  margin-bottom: 0;
}

.checkbox-group {
  flex: 0 0 auto;
  display: flex;
  align-items: center;
  padding-bottom: 2px;
}

.checkbox-group label {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  white-space: nowrap;
  margin-bottom: 0;
}

.btn-icon.remove {
  flex-shrink: 0;
  width: 30px;
  height: 30px;
  border: none;
  background: #fee2e2;
  color: #ef4444;
  border-radius: 6px;
  font-size: 18px;
  cursor: pointer;
  display: flex;
  align-items: center;
  justify-content: center;
  margin-bottom: 2px;
  transition: background 0.15s;
}

.btn-icon.remove:hover:not(:disabled) {
  background: #fecaca;
}

.btn-icon.remove:disabled {
  opacity: 0.3;
  cursor: not-allowed;
}

/* ── Repo block ── */
/* Analyze 集成块:折叠在 Step 4 顶部,不打扰不关心它的用户 */
.analyze-block {
  margin-bottom: 14px; padding: 10px 14px;
  background: #eff6ff; border: 1px solid #bfdbfe;
  border-radius: 8px; font-size: 13px;
}
.analyze-block summary {
  cursor: pointer; display: flex; align-items: baseline; gap: 10px;
  user-select: none; font-weight: 500; color: #1e40af;
}
.analyze-block[open] summary { margin-bottom: 10px; }
.analyze-hint { font-size: 11px; color: #64748b; font-weight: 400; }
.analyze-body { display: flex; flex-direction: column; gap: 8px; }
.analyze-row { display: flex; gap: 8px; align-items: center; }
.analyze-row input[type="text"] {
  flex: 1; padding: 7px 10px; border: 1px solid #cbd5e1; border-radius: 6px;
  font-size: 13px; font-family: monospace;
}
.analyze-row input[type="text"]:focus { outline: none; border-color: #3b82f6; }

.repo-block {
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  padding: 16px;
  margin-bottom: 16px;
  background: #f8fafc;
}

.repo-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 12px;
}

.repo-badge {
  font-size: 13px;
  font-weight: 600;
  color: #3b82f6;
  background: #eff6ff;
  padding: 2px 10px;
  border-radius: 12px;
}

.branch-grid {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
}

.branch-item {
  display: flex;
  align-items: center;
  gap: 8px;
}

.branch-env {
  font-size: 13px;
  font-weight: 500;
  color: #475569;
  min-width: 50px;
}

.branch-item input {
  width: 160px;
}

/* ── Checkboxes ── */
.checkbox-grid {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 20px;
  margin-bottom: 8px;
}

.check-label {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 14px;
  color: #334155;
  cursor: pointer;
  padding: 4px 0;
}

.check-label input[type="checkbox"] {
  width: 16px;
  height: 16px;
  accent-color: #3b82f6;
}

/* ── YAML preview ── */
.yaml-preview {
  background: #1e293b;
  border-radius: 8px;
  padding: 20px;
  margin-bottom: 16px;
  overflow-x: auto;
  max-height: 500px;
  overflow-y: auto;
}

.yaml-preview pre {
  margin: 0;
}

.yaml-preview code {
  font-family: 'SF Mono', 'Fira Code', 'Consolas', monospace;
  font-size: 13px;
  line-height: 1.6;
  color: #e2e8f0;
  white-space: pre;
}

/* ── Action bar ── */
.action-bar {
  display: flex;
  gap: 10px;
  margin-bottom: 16px;
}

.validate-result {
  padding: 10px 16px;
  border-radius: 6px;
  font-size: 14px;
}

.validate-result.success {
  background: #ecfdf5;
  color: #065f46;
  border: 1px solid #a7f3d0;
}

.validate-result.fail {
  background: #fef2f2;
  color: #991b1b;
  border: 1px solid #fecaca;
}

/* ── Step 7 一键部署块 ── */
.deploy-inline {
  margin-top: 18px; padding: 16px 18px;
  background: #eff6ff; border: 1px solid #bfdbfe; border-radius: 8px;
}
.deploy-inline-title {
  font-weight: 600; color: #1e40af; margin-bottom: 4px; font-size: 14px;
}
.deploy-inline-row {
  display: flex; gap: 12px; margin-bottom: 10px; flex-wrap: wrap;
}
.deploy-inline-field { display: flex; flex-direction: column; gap: 4px; min-width: 180px; }
.deploy-inline-field.flex { flex: 1; }
.deploy-inline-field label { font-size: 12px; font-weight: 600; color: #334155; }
.deploy-hint { color: #64748b; font-weight: 400; }
.deploy-inline-field select,
.deploy-inline-path input {
  padding: 7px 10px; border: 1px solid #cbd5e1; border-radius: 6px; font-size: 13px;
}
.deploy-inline-path { display: flex; gap: 6px; }
.deploy-inline-path input { flex: 1; font-family: monospace; }
.deploy-inline-actions { display: flex; justify-content: flex-end; }

/* embedded/openclaw 的"自动管理"展示 */
.auto-path-field label { display: flex; align-items: center; gap: 6px; }
.auto-tag {
  font-size: 10px; font-weight: 500; color: #065f46;
  background: #d1fae5; padding: 1px 6px; border-radius: 8px; letter-spacing: 0.2px;
}
.auto-path-display {
  display: flex; align-items: center; gap: 10px;
  padding: 7px 10px; background: var(--c-surf-3); border-radius: 6px;
  border: 1px dashed var(--c-line-2);
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

/* .btn / .btn.primary / .info-box 来自全局 design.css */

.nav-buttons {
  display: flex;
  justify-content: space-between;
  margin-top: 8px;
}

/* info-box 里的 <p> 组合 InitPage 独有,保留 */
.info-box p { margin: 0; }
.info-box p + p { margin-top: 4px; }
.info-box strong { font-size: var(--fs-md); }

/* ── Help text ── */
.help-text {
  color: #64748b;
  font-size: 13px;
  margin: -8px 0 16px;
  padding: 8px 12px;
  background: #f8fafc;
  border-radius: 6px;
  border-left: 3px solid #cbd5e1;
}

/* ── Radio options for config type ── */
.radio-options {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.radio-label {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  cursor: pointer;
  transition: all 0.15s;
  background: #fff;
}

.radio-label:hover {
  border-color: #93c5fd;
  background: #f8fafc;
}

.radio-label.selected {
  border-color: #3b82f6;
  background: #eff6ff;
}

.radio-label input[type="radio"] {
  width: 16px;
  height: 16px;
  accent-color: #3b82f6;
  flex-shrink: 0;
}

.radio-content {
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.radio-title {
  font-size: 14px;
  font-weight: 600;
  color: #1e293b;
}

.radio-desc {
  font-size: 12px;
  color: #64748b;
}
</style>
