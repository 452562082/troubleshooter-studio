<script setup lang="ts">
import { ref, reactive, computed, watch } from 'vue'
import yaml from 'js-yaml'
import { validate as bridgeValidate } from '../lib/bridge'

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
const modelGroups: ModelGroup[] = [
  {
    group: 'Anthropic（4 种 target 都支持）',
    items: [
      { value: 'anthropic/claude-opus-4-7',   label: 'Claude Opus 4.7 — 最强、偏贵' },
      { value: 'anthropic/claude-sonnet-4-6', label: 'Claude Sonnet 4.6 — 默认推荐，性价比最高' },
      { value: 'anthropic/claude-haiku-4-5',  label: 'Claude Haiku 4.5 — 便宜、快，适合高频轻量' },
    ],
  },
  {
    group: 'OpenAI（openclaw 直用；standalone 会回落到 Claude Sonnet 4.6）',
    items: [
      { value: 'openai/gpt-5-codex', label: 'GPT-5 Codex' },
      { value: 'openai/gpt-4o',      label: 'GPT-4o' },
      { value: 'openai/o3',          label: 'o3' },
    ],
  },
  {
    group: '国内',
    items: [
      { value: 'qwen/qwen3-max',    label: '通义千问 Qwen3 Max' },
      { value: 'deepseek/deepseek-v3', label: 'DeepSeek V3' },
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

// Auto-derive defaults when system name changes
watch(() => system.name, (val) => {
  if (!agent.name || agent.name === agentNameDefault.value.replace(val, '')) {
    agent.name = `${val}排障机器人`
    agent.workspace_name = agent.name
  }
})

const agentNameDefault = computed(() => `${system.name}排障机器人`)

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
const targetOptions = ['openclaw', 'claude-code', 'cursor', 'standalone'] as const
const targetDescriptions: Record<string, string> = {
  'openclaw': 'OpenClaw 安装包（bash install.sh 部署）',
  'claude-code': 'Claude Code（CLAUDE.md + skills/ 放到项目根即用）',
  'cursor': 'Cursor IDE（.cursorrules + .cursor/rules/）',
  'standalone': '独立 Web 聊天（server.py + docker-compose）',
}
const enabledTargets = reactive<Record<string, boolean>>({
  ...Object.fromEntries(targetOptions.map(k => [k, true])),
  ...(saved?.enabledTargets ?? {}),
})

// Auto-save all form state so navigating away doesn't lose the draft
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
    } catch {
      // quota / privacy-mode failures: skip silently
    }
  },
  { deep: true }
)

function clearDraft() {
  if (!confirm('确定清空当前草稿并重置向导吗？')) return
  try {
    localStorage.removeItem(STORAGE_KEY)
  } catch {
    // ignore
  }
  location.reload()
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
  lines.push(`  workspace_name: ${yamlStr(agent.workspace_name || agent.name || agentNameDefault.value)}    # OpenClaw 工作区目录名（~/.openclaw/workspace/<这里>）`)
  lines.push(`  model: ${agent.model}    # LLM model id；standalone 可用 LLM_MODEL 环境变量覆盖`)
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
        <button class="btn link" @click="openImportDialog">导入 YAML</button>
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
          <span class="help-icon" title="OpenClaw 会在 ~/.openclaw/workspace/<这里>/ 创建目录。一般跟机器人名同名即可；如果多个 agent 并存，用不同工作区名避免覆盖。">?</span>
        </label>
        <input
          v-model="agent.workspace_name"
          type="text"
          :placeholder="agent.name"
          :class="{ error: hasError('agent.workspace_name') }"
        />
      </div>
      <div class="form-group">
        <label>模型 <span class="required">*</span>
          <span class="help-icon" title="agent.model 主要给 OpenClaw 网关路由用（<provider>/<model-id>）。Claude Code / Cursor 只作为文档记录；Standalone 只能直连 Anthropic，非 claude-* 会自动回落到 claude-sonnet-4-6（可用 LLM_MODEL 环境变量覆盖）。">?</span>
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
  gap: 4px;
  flex-shrink: 0;
  margin-top: 4px;
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
