<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted, onErrorCaptured, nextTick } from 'vue'

// 给 App.vue 的 keep-alive `:exclude="['InitPage']"` 用,让本页不被缓存。
// 跟 HomePage 的"清空重开"按钮配套:用户清掉 localStorage 后,InitPage 重 mount 取干净状态。
defineOptions({ name: 'InitPage' })
import yaml from 'js-yaml'
import { useRouter } from 'vue-router'
import {
  analyzeV2 as bridgeAnalyzeV2,
  defaultDestPath,
  detectAITools,
  detectOpenClawModels,
  fetchConfigContentBatch,
  listGrafanaDatasources,
  listLokiLabelValues,
  listLokiLabels,
  probeDataStore,
  probeURL,
  probeURLAuth,
  getRemoteURL,
  getUserConfig,
  exportYAML,
  getRepoPathsForSystem,
  importAndDeploy,
  kuboardListResources,
  kuboardListDeployments,
  kuboardFetchConfigMaps,
  runInstall,
  selfTestAgent,
  isDesktop,
  openDir,
  detectSubmodulesForRepo,
  listBranchesForRepo,
  preloadConfigCenter,
  recommendRoleForRepo,
  setDefaultReposRoot,
  validate as bridgeValidate,
  getCustomInstallRoots,
  setCustomInstallRoot,
} from '../lib/bridge'
import type { AIToolResult, CCHubEntry, CCHubNamespace, GrafanaDatasource, OpenClawModelEntry, KuboardFetchBatchResult } from '../lib/bridge'
import { confirmDialog } from '../lib/confirm'
import { pushLog } from '../lib/logStore'
import { toast } from '../lib/toast'

const router = useRouter()

// ── Draft persistence (survives route switches and reloads) ──
const STORAGE_KEY = 'tsf-init-wizard-v1'
// Kuboard 资源树用独立 key 保存:
//   1) 大 draft blob 经常因 quota 静默失败,这层 fallback 让 kuboard 数据不会被波及
//   2) 即使主 draft 没存上,只要这个 key 存了,下次进来下拉 options 仍可用
const KUBOARD_STATE_KEY = 'tsf-init-wizard-kuboard-state-v1'
function loadSavedDraft(): any {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}
function loadSavedKuboardState(): any {
  try {
    const raw = localStorage.getItem(KUBOARD_STATE_KEY)
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}
const saved = loadSavedDraft()
const savedKuboardState = loadSavedKuboardState()

// ── Step management ──
// currentStep 持久化:加了 Step 1=欢迎页后,旧 saved 1..9 → 新 2..10 步内容才对齐。
// 用 saved.wizardSchema 标记本次 saved 是新 schema(>=2)还是老 schema(undefined / 1):
//   - 老 saved(无 wizardSchema):currentStep + 1 一次性迁移,然后保存时写入 wizardSchema=2
//   - 新 saved(wizardSchema=2):直接用 currentStep
//   - 没 saved:默认 1(欢迎页)
const _savedSchema: number = (saved?.wizardSchema ?? 1) as number
const currentStep = ref<number>(
  saved?.currentStep != null
    ? Math.min(_savedSchema >= 2 ? saved.currentStep : saved.currentStep + 1, 10)
    : 1,
)
const totalSteps = 10
const stepTitles = [
  '开始',          // Step 1:欢迎页(导入 yaml / 从零开始)
  '系统基本信息',
  '机器人身份',
  '环境列表',
  '代码仓库',
  '配置源',
  '数据层',
  '可观测性',
  '预览 + 生成',
  '一键部署',
]

const validationErrors = ref<Set<string>>(new Set())

// ── Step 1: 系统基本信息 ──
const system = reactive({
  id: saved?.system?.id ?? '',
  name: saved?.system?.name ?? '',
  description: saved?.system?.description ?? '',
})

// ── Step 2: 机器人身份 ──
// agent.model 是"默认模型"(兜底值,schema 要求非空)。
// agent.target_models 是 per-target 的覆盖 —— 仅 openclaw 这一个 target 消费模型
// (claude-code / cursor 由用户在各自客户端里挑,填这儿没意义)。
const agent = reactive({
  // id 是机器人在 AI 平台里的稳定标识(OpenClaw agents.list[*].id /
  // claude-code / cursor 用 subagent 名)。空时 yaml emit 自动写 ${system.id}-troubleshooter,
  // 部署期 Go 端 ResolveID() 也走同一推导,跟老命名 100% 兼容。
  id: saved?.agent?.id ?? '',
  name: saved?.agent?.name ?? '',
  workspace_name: saved?.agent?.workspace_name ?? '',
  model: saved?.agent?.model ?? 'anthropic/claude-sonnet-4-6',
})
const targetModels = reactive<Record<string, string>>({
  openclaw: saved?.agent?.target_models?.openclaw ?? (saved?.agent?.model ?? 'anthropic/claude-sonnet-4-6'),
})
const modelConsumingTargets = ['openclaw'] as const
// 这俩 computed 在"合并到 Step 1 target 卡片"之后不再有 UI 消费点,但
// scanSingleRepo / yaml 校验之类地方以后可能要查"有没有任一 model-consuming target",
// 保留不删;以 _ 前缀避免 unused 告警。
const _activeModelTargets = computed(() => modelConsumingTargets.filter(t => enabledTargets[t]))
const _needsAnyModel = computed(() => _activeModelTargets.value.length > 0)
void _activeModelTargets; void _needsAnyModel

// ── Model presets ──────────────────────────────────────────────
// 按提供商分组；自定义项让用户填任意字符串（保留企业内部网关 / 新模型的灵活性）。
interface ModelOption { value: string; label: string; hint?: string }
interface ModelGroup { group: string; items: ModelOption[] }
const MODEL_CUSTOM = '__custom__'
// 模型预设(2026-04 更新 — 跟各家 provider 当前主力模型对齐)
// 规则:
//   - 每家只列该 provider 当下在官方 API 上主推的 2-4 个型号;历史 / 弃用的不列
//   - 顺序:旗舰 → 性价比 → 细分(推理 / 编码 / 多模态)
//   - 用户想用没列出的 id(企业网关 / 新模型 / 私有 fine-tune),走"自定义"选项手填任意字符串
//
// 扩展新 provider 时:这里 + internal/llmchat/providers.go 注册表同步加一条
const modelGroups: ModelGroup[] = [
  {
    group: 'Anthropic (Claude 系列)',
    items: [
      { value: 'anthropic/claude-opus-4-7',   label: 'Claude Opus 4.7 — 最强、偏贵' },
      { value: 'anthropic/claude-sonnet-4-6', label: 'Claude Sonnet 4.6 — 默认推荐,性价比最高' },
      { value: 'anthropic/claude-haiku-4-5',  label: 'Claude Haiku 4.5 — 便宜、快,适合高频轻量' },
    ],
  },
  {
    group: 'OpenAI',
    items: [
      { value: 'openai/gpt-5',         label: 'GPT-5 — 旗舰多模态' },
      { value: 'openai/gpt-5-mini',    label: 'GPT-5 mini — 便宜、快' },
      { value: 'openai/gpt-5-codex',   label: 'GPT-5 Codex — 编码专用' },
      { value: 'openai/o3',            label: 'o3 — 深度推理' },
      { value: 'openai/o3-mini',       label: 'o3 mini — 推理、便宜' },
      { value: 'openai/gpt-4o',        label: 'GPT-4o — 上一代,仍可用' },
    ],
  },
  {
    group: 'DeepSeek',
    items: [
      // deepseek-chat / deepseek-reasoner 是官方 API 上"永远指向最新" V3 / R1 的稳定别名
      { value: 'deepseek/deepseek-chat',     label: 'DeepSeek Chat — V3 系列,通用对话' },
      { value: 'deepseek/deepseek-reasoner', label: 'DeepSeek Reasoner — R1 系列,推理' },
    ],
  },
  {
    group: '通义千问 (Qwen)',
    items: [
      { value: 'qwen/qwen3-max',    label: 'Qwen3 Max — 旗舰' },
      { value: 'qwen/qwen3-coder',  label: 'Qwen3 Coder — 编码专用' },
      { value: 'qwen/qwen-plus',    label: 'Qwen Plus — 性价比' },
      { value: 'qwen/qwen-vl-max',  label: 'Qwen VL Max — 多模态(视觉)' },
    ],
  },
  {
    group: 'MiniMax',
    items: [
      { value: 'minimax/MiniMax-M2',      label: 'MiniMax M2 — 最新旗舰' },
      { value: 'minimax/MiniMax-M1',      label: 'MiniMax M1 — 推理' },
      { value: 'minimax/MiniMax-Text-01', label: 'MiniMax Text-01 — 长上下文' },
    ],
  },
  {
    group: 'Moonshot (Kimi)',
    items: [
      { value: 'moonshot/kimi-k2',           label: 'Kimi K2 — 最新旗舰' },
      { value: 'moonshot/kimi-latest',       label: 'Kimi Latest — 自动跟随最新' },
      { value: 'moonshot/moonshot-v1-128k',  label: 'Moonshot v1 128k — 长上下文(legacy)' },
    ],
  },
  {
    group: '智谱 (GLM)',
    items: [
      { value: 'zhipu/glm-4-plus',        label: 'GLM-4 Plus — 旗舰' },
      { value: 'zhipu/glm-4-air',         label: 'GLM-4 Air — 性价比' },
      { value: 'zhipu/glm-4-long',        label: 'GLM-4 Long — 长上下文' },
      { value: 'zhipu/glm-zero-preview',  label: 'GLM Zero — 推理预览' },
    ],
  },
  {
    group: '本地 / 自部署 (Ollama)',
    items: [
      { value: 'ollama/llama3.3',      label: 'Llama 3.3 (Meta)' },
      { value: 'ollama/qwen3',         label: 'Qwen3 (Alibaba)' },
      { value: 'ollama/qwen2.5-coder', label: 'Qwen2.5 Coder — 编码' },
      { value: 'ollama/deepseek-r1',   label: 'DeepSeek R1 — 推理' },
      { value: 'ollama/mistral-nemo',  label: 'Mistral Nemo' },
    ],
  },
]
const allPresetModels = modelGroups.flatMap(g => g.items.map(i => i.value))
// 老的单 model 选择器 computed —— 保留让未来单 model 模式复用,目前通过 target 版本替代。
// 不暴露到模板,用 void 抑制 unused 警告(跟 _stackOptions 等同套路)。
const _modelSelectValue = computed({
  get: () => allPresetModels.includes(agent.model) ? agent.model : MODEL_CUSTOM,
  set: (v: string) => {
    if (v === MODEL_CUSTOM) {
      if (allPresetModels.includes(agent.model)) agent.model = ''
    } else {
      agent.model = v
    }
  },
})
const _modelIsCustom = computed(() => !allPresetModels.includes(agent.model))
void _modelSelectValue; void _modelIsCustom

// target 版本:按 target 取/写 model,支持 preset select + 自定义输入
// (embedded target 下线后这俩在模板里没再用,但 BotsPage / 部署侧可能仍引用,留着)
function modelSelectValueFor(t: string): string {
  const m = targetModels[t] || agent.model
  return allPresetModels.includes(m) ? m : MODEL_CUSTOM
}
void modelSelectValueFor
function modelIsCustomFor(t: string): boolean {
  return !allPresetModels.includes(targetModels[t] || agent.model)
}
void modelIsCustomFor
function onModelChange(t: string, e: Event) {
  const v = (e.target as HTMLSelectElement).value
  if (v === MODEL_CUSTOM) {
    // 切自定义:清空当前 preset 值,让用户在下面 input 里填
    if (allPresetModels.includes(targetModels[t])) targetModels[t] = ''
  } else {
    targetModels[t] = v
  }
  // 顺手更新 agent.model 作为"默认"(给 schema 的必填兜底):
  // openclaw 是唯一消费模型的 target;它的值覆盖 agent.model,保 yaml 里 agent.model 永远非空。
  if (targetModels['openclaw']) agent.model = targetModels['openclaw']
}

function currentModelFor(t: string): string {
  return targetModels[t] || agent.model
}
void currentModelFor

// ── OpenClaw 模型探测(只给 openclaw target 卡用) ──
// 流程:
//   1. 用户勾上 openclaw card → 触发一次 detect(空参 = 默认 ~/.openclaw)
//   2. detect 成功 → 模型下拉从 openclawDetected 里选;用户选完 targetModels.openclaw 更新
//   3. detect 失败(installed=false) → UI 展示"未检测到 OpenClaw 安装,选择目录 →"
//      用户点"选目录"走 openDir → 拿绝对路径重试 detect
//   4. 用户坚持走 hardcoded 模型列表:给个"用预设模型列表"开关回落到 modelGroups
const openclawInstallDir = ref<string>(saved?.openclawInstallDir ?? '') // localStorage 持久,换会话不用重试
const openclawDetectStatus = ref<'idle' | 'loading' | 'ok' | 'not-installed' | 'error'>('idle')
const openclawDetectedModels = ref<OpenClawModelEntry[]>([])
const openclawDetectError = ref<string>('')
const openclawResolvedDir = ref<string>('') // backend 返回的实际路径(展开 ~ 后)
const openclawVersion = ref<string>('') // openclaw.json meta.lastTouchedVersion
const openclawAuthProviders = ref<string[]>([]) // auth.profiles 里出现的 provider 名字

// Claude Code / Cursor / Codex 安装状态 —— 决定卡片能否被勾选:
//   - 检测到 → 默认可勾,部署落到检测出的位置(~/.<target>/agents)
//   - 未检测到 → checkbox 默认禁用,展示"未检测到"提示;用户可点"我已自行安装"
//     强制启用(手填路径或确认默认 ~/.<target> 已存在),也可点"重新扫描"。
const aitoolsResult = ref<{ claude_code: AIToolResult; cursor: AIToolResult; codex: AIToolResult } | null>(null)
// 用户对未检测到的 target 强制启用("我自己装了" / "我会装") —— per-target bool。
// 一旦置 true,checkbox 解锁,enabledTargets 才能勾上。持久化到 draft 跟其它字段一样。
const forceEnableMissingTarget = reactive<Record<string, boolean>>({
  ...(saved?.forceEnableMissingTarget ?? {}),
})

// customInstallRoots[t] —— 用户对未检测到的 target 手选的安装根目录(如 /opt/myclaude/);
// 非空时部署位置从默认 `~/.<target>` 改成 `<customRoot>` 拼 agents/workspace 后缀。
// openclaw 的自定义安装目录另有专用 UI(openclawInstallDir),不走这里。
const customInstallRoots = reactive<Record<string, string>>({
  ...(saved?.customInstallRoots ?? {}),
})
async function pickCustomInstallRoot(t: string) {
  try {
    const dir = await openDir(`选 ${t} 安装根目录(目录下应有 agents/ 子目录)`)
    if (dir) {
      customInstallRoots[t] = dir
      forceEnableMissingTarget[t] = true
      // 持久化到 ~/.tshoot/config.json,跨 wizard 会话和 BotsPage 扫描共用同一份
      await setCustomInstallRoot(t, dir).catch((e: any) => {
        pushLog('install', 'warn', `setCustomInstallRoot(${t}) 持久化失败: ${String(e?.message || e)}`)
      })
    }
  } catch (e: any) {
    pushLog('install', 'warn', `pickCustomInstallRoot(${t}) 失败: ${String(e?.message || e)}`)
  }
}
async function clearCustomInstallRoot(t: string) {
  delete customInstallRoots[t]
  // 同步清掉本地文件里的覆盖,否则下次启动又被反填回来
  await setCustomInstallRoot(t, '').catch((e: any) => {
    pushLog('install', 'warn', `setCustomInstallRoot(${t}, '') 清除失败: ${String(e?.message || e)}`)
  })
}
// 启动时从 ~/.tshoot/config.json 反填一次 customInstallRoots —— 优先于 saved draft,
// 因为本地文件是"跨向导会话的权威";draft 里的值只是这次会话的快照,持久化口径以文件为准。
onMounted(async () => {
  try {
    const m = await getCustomInstallRoots()
    for (const [t, dir] of Object.entries(m || {})) {
      if (dir) {
        customInstallRoots[t] = dir
        forceEnableMissingTarget[t] = true
      }
    }
  } catch {
    // 静默兜底:浏览器模式 / binding 还没跑 generate 都返空,不影响 UI
  }
})
async function refreshAITools() {
  try {
    aitoolsResult.value = await detectAITools()
  } catch {
    // 探测失败静默处理,UI 回落到"不显示徽标"
  }
}
onMounted(() => { refreshAITools() })
// (之前有 openclawManualInput 手填模式,已删 —— openclaw gateway 只认自己
//  config.yaml 里声明的 model id,让用户手填一个它不认的 id 部署完就跑不动,
//  不如直接"如实告知,去装 openclaw 并配好模型再回来扫描"。)

async function runOpenClawDetect(dir: string = openclawInstallDir.value) {
  if (!isDesktop()) {
    openclawDetectStatus.value = 'error'
    openclawDetectError.value = '浏览器模式不支持探测 OpenClaw,请用桌面 app'
    return
  }
  openclawDetectStatus.value = 'loading'
  openclawDetectError.value = ''
  try {
    const r = await detectOpenClawModels(dir)
    if (r.ok) {
      openclawDetectStatus.value = 'ok'
      openclawDetectedModels.value = r.models || []
      openclawResolvedDir.value = r.install_dir || ''
      openclawVersion.value = r.version || ''
      openclawAuthProviders.value = r.auth_providers || []
    } else {
      openclawDetectStatus.value = r.installed ? 'error' : 'not-installed'
      openclawDetectError.value = r.err || '未知错误'
    }
  } catch (e: any) {
    openclawDetectStatus.value = 'error'
    openclawDetectError.value = String(e?.message || e)
  }
}
async function pickOpenClawInstallDir() {
  if (!isDesktop()) return
  try {
    const p = await openDir('选择 OpenClaw 安装目录(含 config.yaml / gateway/ 等)')
    if (!p) return
    openclawInstallDir.value = p
    await runOpenClawDetect(p)
  } catch (e: any) {
    openclawDetectError.value = String(e?.message || e)
    openclawDetectStatus.value = 'error'
  }
}
// watch / onMounted 已挪到 enabledTargets 声明之后(见该 const 下方),
// 这里留空避免重复声明。

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

// system.id 从 system.name 自动派生:去掉非 ASCII、空格转 -、小写、裁剪到 32 字符。
// 派生失败(name 全中文)时返回空串,UI 回落"让用户手填"(一般加一个"my-system"
// 的占位就够)。用户自己在 id 输入框改过之后就 lock 住(idManualOverride=true),
// 不再被 name 变动冲掉。
const idManualOverride = ref<boolean>(saved?.idManualOverride ?? false)
function slugifyToId(name: string): string {
  const s = (name || '')
    .toLowerCase()
    .replace(/[^\x00-\x7F]/g, '') // 去掉所有非 ASCII(中文 / emoji 等)
    .replace(/[^a-z0-9]+/g, '-')  // 连续非法字符压成一个短横线
    .replace(/^-+|-+$/g, '')       // 去头尾短横线
    .slice(0, 32)
  // system.id 的 regex 要求首字符必须 [a-z0-9]
  if (!s || !/^[a-z0-9]/.test(s)) return ''
  return s
}
// system.name 改动 → 自动改 system.id(前提:用户没手改过)
watch(() => system.name, (val) => {
  if (idManualOverride.value) return
  const derived = slugifyToId(val)
  if (derived) system.id = derived
  // 派生为空(纯中文名)时不动 system.id,保留之前的值(可能是空,UI 会露出输入框让用户填)
})
// 初次挂载 / draft 恢复时,若 id 空但 name 能派生,填上
onMounted(() => {
  if (!system.id && !idManualOverride.value) {
    const derived = slugifyToId(system.name)
    if (derived) system.id = derived
  }
})
function markIdManual() {
  idManualOverride.value = true
}
function resetIdAuto() {
  idManualOverride.value = false
  const derived = slugifyToId(system.name)
  system.id = derived
}
// 纯中文(或其它派生失败)的 name,UI 需要露出 id 输入框让用户手填
const idAutoFailed = computed(() => {
  if (!system.name.trim()) return false
  return slugifyToId(system.name) === ''
})
const idCanAutoDerive = computed(() => slugifyToId(system.name) !== '')

const agentNameDefault = computed(() => `${system.name}排障机器人`)
// agent.id:AI 平台里的稳定标识。默认 <system.id>-troubleshooter,跟历史命名兼容。
// workspace 目录名跟它共用,不再单独 emit workspace_name(Go 端 ResolveWorkspaceName 兜底)。
const agentIdDefault = computed(() => (system.id ? `${system.id}-troubleshooter` : 'my-system-troubleshooter'))

// ── Step 3: 环境列表 ──
interface EnvItem {
  id: string
  api_domain: string
  // web_domain 可选,前端入口(管理后台 / 用户站)的域名;routing skill 里跟 api_domain
  // 同级记到 env-domain-map.yaml,bot 排障时知道"用户在哪个 URL 看到这个 bug"
  // vs "后端哪个接口报的错"。很多系统就一个域名,这栏留空也没关系。
  web_domain: string
  is_prod: boolean
}

const environments = reactive<EnvItem[]>(
  Array.isArray(saved?.environments) && saved.environments.length
    ? saved.environments
    : [
        { id: 'dev', api_domain: '', web_domain: '', is_prod: false },
        { id: 'prod', api_domain: '', web_domain: '', is_prod: true },
      ]
)

// normalizeDomain: 清理用户输入,但 **保留 scheme**(http/https)。
// 原因:下游 bot 要实际发 HTTP 请求时需要知道协议 —— 开发环境常见 http,
// 生产 https,Studio 不替用户猜。用户想明确是 http 就带 http://,https 同理;
// 空白/末尾斜杠/path/query 剥掉保持干净。scheme 缺省 → 留裸 host,下游视为"未指定",
// 默认 https 兜底或提示手填。
function normalizeDomain(input: string): string {
  let s = (input || '').trim()
  if (!s) return ''
  // 解析 scheme(如果有)
  let scheme = ''
  const m = s.match(/^([a-zA-Z][a-zA-Z0-9+.-]*:\/\/)/)
  if (m) {
    scheme = m[1]
    s = s.slice(scheme.length)
  }
  // 剥 path / query,只保留 host[:port]
  const slash = s.indexOf('/')
  if (slash >= 0) s = s.slice(0, slash)
  const q = s.indexOf('?')
  if (q >= 0) s = s.slice(0, q)
  return scheme + s.trim()
}

function addEnv() {
  environments.push({ id: '', api_domain: '', web_domain: '', is_prod: false })
}

function removeEnv(idx: number) {
  if (environments.length > 1) environments.splice(idx, 1)
}

// ── Step 3 域名自动连通性测试 ─────────────────────────────────────────
// 用户填 api_domain / web_domain 时,800ms 防抖触发 GET 探测;不显示按钮。
// key = `${envIndex}:${kind}` (kind = api / web)。重新填 / 切 env 顺序都能正确刷新。
interface URLProbeState { status: 'idle' | 'loading' | 'ok' | 'fail'; latency?: string; detail?: string; error?: string }
const urlProbeResults = reactive<Record<string, URLProbeState>>({})
const urlProbeTimers: Record<string, ReturnType<typeof setTimeout>> = {}
function urlProbeKey(envIdx: number, kind: 'api' | 'web'): string {
  return `${envIdx}:${kind}`
}
function scheduleURLProbe(envIdx: number, kind: 'api' | 'web', rawURL: string) {
  const k = urlProbeKey(envIdx, kind)
  if (urlProbeTimers[k]) clearTimeout(urlProbeTimers[k])
  const url = (rawURL || '').trim()
  if (!url) {
    delete urlProbeResults[k]
    return
  }
  urlProbeTimers[k] = setTimeout(async () => {
    if (!isDesktop()) return
    urlProbeResults[k] = { status: 'loading' }
    try {
      const r = await probeURL(url)
      urlProbeResults[k] = r.ok
        ? { status: 'ok', latency: r.latency, detail: r.detail }
        : { status: 'fail', error: r.error || '不可达' }
    } catch (e: any) {
      urlProbeResults[k] = { status: 'fail', error: String(e?.message || e) }
    }
  }, 800)
}
// 切到 Step 3 / 已存在的 env 值,做一次主动探测(不等用户重新输入)
watch(() => currentStep.value, (s) => {
  if (s !== 3) return
  environments.forEach((env, i) => {
    if (env.api_domain) scheduleURLProbe(i, 'api', env.api_domain)
    if (env.web_domain) scheduleURLProbe(i, 'web', env.web_domain)
  })
}, { immediate: true })

// 用户删掉某个 env / 改 env.id 后,对应的 Step 5 扫描缓存 / Step 7 数据层扫描结果
// 仍然挂在各 reactive map 里(因为 key 是 env.id)。清掉孤儿,避免 draft 越攒越脏。
// 注意这个 watch 必须在相关 reactive 声明之后调用(文件末尾 watch 段),否则 TDZ。
// —— 实际挂在 2300 附近的 auto-save watch 旁(见下方).

// ── Step 4: 代码仓库 ──
// RepoRole 跟后端 internal/config/types.go 的 RepoRole 字符串字面量保持一致。
// 仓库角色:决定 incident-investigator 看本 repo 的视角(主角 / 上下游 / 不入图)。
type RepoRole =
  | 'backend'      // 后端业务服务(默认)
  | 'frontend'     // web 前端
  | 'gateway'      // API 网关 / BFF
  | 'middleware'   // 接入层 / worker / 调度器
  | 'common-lib'   // 公共库;不入服务依赖图
  | 'mobile'       // 移动端 app
  | 'admin'        // 管理后台
  | 'infra'        // 基础设施(k8s/terraform);不入图
  | 'docs'         // 文档;不入图

// 仅这 4 类角色对应"业务服务",需要识别 service_names 喂给配置中心 / 数据层
// 扫描;其它角色(frontend / common-lib / mobile / infra / docs)不是服务,不参与
// 服务名提取 —— 扫了也是噪音(前端没 nacos key、infra 仓没 service ID),反而
// 误导用户后续步骤。
function isServiceRole(role?: string): boolean {
  return role === 'backend' || role === 'gateway' || role === 'middleware' || role === 'admin'
}

interface RepoItem {
  name: string
  url: string
  stack: string
  framework: string
  // role:仓库角色。空 / 未设置时按 backend 兜底。
  // wizard 里有自动推荐(基于 stack):php/go/java/python → backend;node 由用户手挑
  // (前端 vs 后端 vs gateway/BFF 都是 node 常见用法,不能 stack 一刀切)。
  role?: RepoRole
  // sub_path:monorepo 子目录。多个服务在同一个 git 仓库不同子目录时,在 repos[] 添加
  // 多个条目共用相同 url + 不同 sub_path,name 各取服务名。
  // 例:truss 仓库下 services/commerce 是 Go 服务,web/admin 是 Node 后台 → 两个条目同 url 不同 sub_path。
  // 空 = 整 repo 当一个服务对待(默认行为,大部分单服务仓库)。
  sub_path?: string
  service_names: string
  env_branches: Record<string, string>
  // _nameManual: 用户手动编辑过 name,URL 变化不会再覆盖 name。
  _nameManual?: boolean
  // _source: 仓库来源 "local"(本地已 clone,直接选目录) / "remote"(填 URL,扫描时 clone)
  // 默认 remote(新建仓库填 URL 是更常见的场景)
  _source?: 'local' | 'remote'
  // _localPath: _source=local 时的本地绝对路径;扫描时直接读,不走 ReposRoot/Name
  _localPath?: string
  // _cloneTarget: _source=remote 时的自定义 clone "父目录"(可选);
  // 实际 clone 路径 = _cloneTarget + "/" + repo.name(跟全局 reposRoot 行为一致),
  // 空则 clone 到 <全局默认>/<repo.name>/
  _cloneTarget?: string
  // _scanning / _scanError / _scanned: 单仓库 inline 扫描状态。
  //   scanning = clone+analyze 进行中(远程模式要几秒,本地模式几乎瞬间)
  //   scanError = 最近一次失败原因(用户重试后清零)
  //   scanned = 至少成功跑过一次;控制"重新扫描" vs "Clone 并扫描"按钮文案
  _scanning?: boolean
  _scanError?: string
  _scanned?: boolean
  // _scannedSource: 最近一次成功扫描对应的 URL / 本地路径。用来判定用户改了
  // URL / 重选了目录之后,当前下方展示的 stack / service_names / 分支是否已过期,
  // 过期就主动清零,免得 UI 里挂着上个仓库的数据让用户误以为"重置失败"。
  _scannedSource?: string
  // ConfigSource:多源场景下本仓库引用哪个 config_centers[].id。空 = 默认源(default)。
  // 单源系统(extraConfigSources 空)下不暴露此字段;yaml 多源时 Step 4 会渲染下拉。
  config_source?: string
  // _roleHint:最近一次 RecommendRoleForRepo 的结果(role + reason)。仅 UI 展示用,
  // 让用户看到"扫描推荐什么 role + 理由",方便决定是否改默认值。不进 yaml。
  _roleHint?: { role: string, reason: string }
  _roleHintLoading?: boolean
  // _serviceEntries:服务名 → 仓库内入口子目录(相对仓库根)。
  // 给同仓多服务场景(cmd/<x>/main.go / services/<x>/ / workspaces / pom-modules)用 ——
  // 这些不是 git submodule,不该拆成独立 repo,只把名字塞进 service_names + 入口路径
  // 单独记录;routing skill 据此把 service → 源码入口对应起来。
  // gitmodules 拆出的独立 repo 不用本字段(它们各自占一行,有自己的 sub_path / 本地路径)。
  _serviceEntries?: Record<string, string>
  // 用户已经合并过 monorepo hints 到 service_names,banner 应隐藏不再追问。
  _submoduleHintsDismissed?: boolean
  // _submoduleHints:扫描后探测到的"这是 monorepo,有 N 个子模块"列表。
  // 非空且长度 > 1 时,UI 在仓库 header 下方弹横幅:
  //   - 全部来自 .gitmodules(每个 hint 都有 url)→ "拆成 N 个独立仓库"按钮
  //   - 其余路径(workspaces / cmd-multi / services-dir / pom-modules)→ "合并为本仓 N 个服务名"按钮
  // 真正拆 / 合并由 splitMonorepo / mergeMonorepoIntoServices 决定。
  // url 字段非空时(.gitmodules 路径)→ 当独立仓库展开;空 → 当同仓子目录展开。
  _submoduleHints?: { name: string, sub_path: string, stack: string, role: string, reason: string, url?: string }[]
  // _submoduleSelection:per-hint 复选框状态(默认全选)。用户在 banner 里能去掉
  // "这个不是真服务"的条目(典型:tools/lint-rules 这种带 go.mod 的工具子目录),
  // 拆分时只展开勾选的那些。
  _submoduleSelection?: Record<string, boolean>
  // 下划线前缀字段都是 UI 态;不参与 yaml 序列化(generateYAML 不读),但跟
  // localStorage auto-save 持久化,跨次刷新不丢。
}

function makeEmptyRepo(): RepoItem {
  const branches: Record<string, string> = {}
  for (const e of environments) {
    if (e.id) branches[e.id] = ''
  }
  return {
    name: '', url: '', stack: '', framework: '',
    role: 'backend', // 兜底 backend(单服务/小项目 95% 是后端);node / 公共库需手挑
    sub_path: '',    // 默认空 = 整仓当一个服务;monorepo 多服务 → 添加多条同 url 不同 sub_path
    service_names: '', env_branches: branches,
    _source: 'remote', // 默认"远程 URL"模式(大部分用户从 GitHub/GitLab 起步)
  }
}

const repos = reactive<RepoItem[]>(
  Array.isArray(saved?.repos) && saved.repos.length ? saved.repos : [makeEmptyRepo()]
)

// 从 URL 推导仓库名。支持三种常见格式:
//   git@github.com:org/order-service.git    → order-service
//   https://github.com/org/order-service.git → order-service
//   https://gitlab.com/group/sub/order-svc   → order-svc
function deriveRepoName(url: string): string {
  const s = (url || '').trim()
  if (!s) return ''
  // 从末尾抓最后一段 path / colon-separated segment,去 .git 后缀
  const m = s.match(/[:/]([^/:]+?)(?:\.git)?\/?$/)
  return m ? m[1] : ''
}

// URL 输入时触发:如果没被手改过,把 name 改成新 URL 的推导结果。
// _nameManual 放在 RepoItem 上是因为 WeakSet 不是 Vue 的 reactive 源,
// 模板里 v-if="..." 读 WeakSet 状态不会重新渲染 —— 放 repo 本身就自然响应。
//
// 另外:只要 URL 跟"上次成功扫描的 URL"不一致,就把下方的 stack / service_names /
// 分支等扫描结果清掉 —— 用户删了本地 clone 重新输入 URL、或者换了另一个仓库 URL,
// 下方老数据留着会误导。清 + 把 _scanned 翻成 false,按钮文案会变回"同步到本地并扫描"。
function onRepoUrlInput(r: RepoItem) {
  if (!r._nameManual) r.name = deriveRepoName(r.url)
  if (r._scanned && r.url !== r._scannedSource) {
    resetRepoScanResults(r)
  }
}

// (原 onRepoLocalPathInput / onRepoLocalPathBlur 已删除 —— 本地目录输入框改为 readonly,
//  用户只能通过"选目录…"按钮挑目录,避免手写路径打错 / 空格漏 / 存在性没核对这些问题。)

// resetRepoScanResults 清掉单个仓库的扫描结果 + 下拉缓存,但保留身份字段
// (URL / 本地路径 / 仓库名 / _nameManual)。给"URL/目录换了,扫描结果过期"场景用。
function resetRepoScanResults(r: RepoItem) {
  r._scanned = false
  r._scannedSource = ''
  r._scanError = undefined
  r.stack = ''
  r.service_names = ''
  for (const eid of Object.keys(r.env_branches)) {
    r.env_branches[eid] = ''
  }
  if (r.name && r.name in repoBranchesMap.value) {
    delete repoBranchesMap.value[r.name]
  }
}

// 启发式:根据 env id / is_prod 从 branches 里挑一个最可能的长期分支。
// 映射顺序(命中优先):精确匹配 env.id → 常见别名 → 按 prod/non-prod 分组 fallback。
// 所有都不中返回 ''(UI 不填)。
function pickBranchForEnv(env: EnvItem, branches: string[]): string {
  if (!branches.length) return ''
  const has = (n: string) => branches.includes(n)
  const eid = (env.id || '').toLowerCase()

  // 1) env.id 本身就是分支名
  if (has(eid)) return eid

  // 2) env.id 的常见别名
  const aliases: Record<string, string[]> = {
    dev: ['develop', 'dev', 'development'],
    prod: ['main', 'master', 'prod', 'production', 'release'],
    test: ['test', 'testing', 'qa'],
    staging: ['staging', 'stage', 'pre', 'preview', 'uat'],
    pre: ['pre', 'preview', 'staging', 'uat'],
  }
  for (const cand of aliases[eid] || []) {
    if (has(cand)) return cand
  }

  // 3) 按 is_prod 分组 fallback:
  //    prod → main/master/release,非 prod → develop/dev/main
  const prodFallbacks = ['main', 'master', 'release', 'prod', 'production']
  const nonProdFallbacks = ['develop', 'dev', 'main']
  const pool = env.is_prod ? prodFallbacks : nonProdFallbacks
  for (const cand of pool) {
    if (has(cand)) return cand
  }
  return ''
}

// 用户在 name 输入框里动手就算"手改过",记录下来避免被 URL 再覆盖。
// 但如果用户把 name 清空,视作"回到自动推",清除标记。
function onRepoNameInput(r: RepoItem) {
  if (!r.name.trim()) {
    r._nameManual = false
    // 立即用当前 URL 重填
    r.name = deriveRepoName(r.url)
    return
  }
  r._nameManual = true
  // 名字改了 → 重新拿一次推荐(本地路径优先,无路径退化到名字+stack)
  refreshRoleHint(r)
}

// refreshRoleHint 给 repo 拿一份"基于当前 name + stack + 本地路径"的 role 推荐,塞到 _roleHint。
// UI 在下拉旁边渲染 "📍 推荐 X(理由)";命中跟当前 role 不一致时显示对比按钮"采用"。
// 触发时机:onRepoNameInput / 仓库扫描完(stack 自动填好后)/ Step 4 进入时遍历刷一遍。
async function refreshRoleHint(r: RepoItem) {
  if (!r.name.trim()) {
    r._roleHint = undefined
    return
  }
  r._roleHintLoading = true
  try {
    let path = r._source === 'local' ? (r._localPath || '') : ''
    // monorepo:把 sub_path 拼上,后端 RecommendRoleForRepo 会看子目录下的 package.json / pom.xml
    if (path && r.sub_path && r.sub_path.trim()) {
      path = path.replace(/\/+$/, '') + '/' + r.sub_path.trim().replace(/^\/+/, '')
    }
    const hint = await recommendRoleForRepo(r.stack || 'go', r.name, path)
    r._roleHint = hint
  } catch {
    /* 推荐失败不阻塞用户填表 */
  } finally {
    r._roleHintLoading = false
  }
}

// applyRoleHint 把推荐 role 落到 r.role(用户点"采用"按钮调)。
function applyRoleHint(r: RepoItem) {
  if (r._roleHint?.role) {
    r.role = r._roleHint.role as RepoRole
    syncServiceNamesWithRole(r)
  }
}

// syncServiceNamesWithRole role 改变后,按"是不是业务服务"调 service_names:
//   service → 留着已有值;空时回填 repo.name 兜底
//   非 service → 清空(避免污染后续配置中心 / 数据层 / k8s_runtime 扫描)
function syncServiceNamesWithRole(r: RepoItem) {
  if (isServiceRole(r.role)) {
    if (!r.service_names.trim() && r.name) {
      r.service_names = r.name
    }
  } else {
    r.service_names = ''
  }
}

// onRepoSubPathInput sub_path 改了 → role hint 重算(后端会进入子目录读 package.json/pom.xml 等)
function onRepoSubPathInput(r: RepoItem) {
  refreshRoleHint(r)
}

// toggleSubmodulePick 用户在 banner 里勾/取消勾某个子模块,影响后续 splitMonorepo 展开哪些。
function toggleSubmodulePick(r: RepoItem, subPath: string, picked: boolean) {
  if (!r._submoduleSelection) r._submoduleSelection = {}
  r._submoduleSelection[subPath] = picked
}

// pickedSubmoduleCount banner 拆分按钮上显示数量 + disable 用
function pickedSubmoduleCount(r: RepoItem): number {
  if (!r._submoduleHints) return 0
  const sel = r._submoduleSelection || {}
  return r._submoduleHints.filter(h => sel[h.sub_path] !== false).length
}

// submodulePathFor 拼"父仓本地路径 + sub_path"得到子模块实际代码位置。
// banner 列每条子模块时显示 + 已 split 的条目下方也显示,让用户能确认 routing skill 拿到的是哪个目录。
function submodulePathFor(parent: RepoItem, subPath: string): string {
  const base = (parent._localPath || '').replace(/\/+$/, '')
  const rel = subPath.replace(/^\/+/, '')
  if (!base) return rel
  if (!rel) return base
  return base + '/' + rel
}

// refreshSubmoduleHints 调后端扫 monorepo 信号(workspaces / pom modules / cmd 多入口 / services 子目录)
// → 命中即把列表存到 r._submoduleHints,UI banner 显示"检测到 N 个子模块,一键拆分"。
// 触发时机:scan 完成后(此时本地路径已就位)。0 命中 → 不弹 banner。
async function refreshSubmoduleHints(r: RepoItem) {
  // 本地模式直接用 _localPath;远程模式 clone 完成后落点 = resolveCloneDest(r),
  // 也是个有效本地路径,送进 detectSubmodules 同样能扫。
  let path = ''
  if (r._source === 'local') {
    path = r._localPath || ''
  } else if (r._source === 'remote') {
    const dest = resolveCloneDest(r)
    if (dest) path = dest
  }
  if (!path) {
    r._submoduleHints = []
    r._submoduleSelection = {}
    return
  }
  try {
    const hints = await detectSubmodulesForRepo(path)
    r._submoduleHints = hints
    // 默认全选,用户能取消勾不想要的(如 tools/lint-rules)
    const sel: Record<string, boolean> = {}
    for (const h of hints) sel[h.sub_path] = true
    r._submoduleSelection = sel
    // 重新扫了一次 → 老的"合并状态"作废,banner 重新出现给用户决定
    r._submoduleHintsDismissed = false
  } catch {
    r._submoduleHints = []
    r._submoduleSelection = {}
  }
}

// isGitSubmodulesHints 一组 hints 是不是都来自 .gitmodules ——
// 后端 DetectSubmodules 命中 .gitmodules 路径时每条 hint 都带 url,其它路径(workspaces /
// cmd-multi / services-dir / pom-modules)hint.url 全空。简单按 url 区分两类。
function isGitSubmodulesHints(hints?: Array<{ url?: string }>): boolean {
  if (!hints || hints.length === 0) return false
  return hints.every(h => !!h.url)
}

// qualifyServiceName 把 cmd 入口名加 `<repo>-` 前缀消歧义。
//
// 背景:Go 项目惯例 cmd/<x>/main.go 里 <x> 全是泛词(grpc-server / queue /
// scheduler / worker / consumer / migrate / cron 等)。多个仓库各有同名入口,
// 直接拿入口名当 service_names 会导致跨仓服务名重叠 —— 排障 routing /
// service-dependency-map / config-map 都靠 service 名做 key,撞名全炸。
//
// 规则:
//   - entry === repo  → 不重复加前缀(如 repo=order, cmd/order/main.go → order)
//   - entry 已含 repo 名作前/后缀 → 已经消过歧,直接用
//   - 其它 → `<repo>-<entry>`(grpc-server in interaction → interaction-grpc-server)
//
// .gitmodules 那条路径不走本函数(每个 submodule 是独立 git repo,展开成独立 repos[] 行);
// node workspaces 的 hint.name 来自 package.json:name,通常已带 scope/独特命名,但仍走
// 同一规则做兜底 —— 避免万一 fallback 到目录名(如纯 "admin")时撞名。
function qualifyServiceName(repoName: string, entryName: string): string {
  const repo = (repoName || '').trim()
  const ent = (entryName || '').trim()
  if (!repo) return ent
  if (!ent) return repo
  if (ent === repo) return ent
  if (
    ent.startsWith(repo + '-') || ent.startsWith(repo + '_') ||
    ent.endsWith('-' + repo) || ent.endsWith('_' + repo)
  ) {
    return ent
  }
  return `${repo}-${ent}`
}

// mergeMonorepoIntoServices 把命中的"同仓多服务"hints 合并填进当前 repo 的 service_names,
// 不拆成多行(因为它们本来就是一个 git 仓库,只是有多个入口)。
// 同时把每个服务的入口子目录记录到 _serviceEntries,UI 上让用户看到映射,yaml emit 时也带上。
// 用户点 banner 上的"合并到服务名"按钮调这个。
function mergeMonorepoIntoServices(parentIdx: number) {
  const parent = repos[parentIdx]
  if (!parent || !parent._submoduleHints || parent._submoduleHints.length === 0) return
  const sel = parent._submoduleSelection || {}
  const picked = parent._submoduleHints.filter(h => sel[h.sub_path] !== false)
  if (picked.length === 0) return
  // 服务名:扫到的 N 个入口,带 `<repo>-` 前缀消歧义(去重保序)。仓库整体 role 保留
  // 用户已选(默认 backend),不被入口的 role 推断覆盖 —— 入口的 role 只在 banner 上展示。
  const names: string[] = []
  parent._serviceEntries = {}
  for (const h of picked) {
    const qn = qualifyServiceName(parent.name, h.name)
    if (!qn) continue
    if (!names.includes(qn)) names.push(qn)
    parent._serviceEntries[qn] = h.sub_path
  }
  parent.service_names = names.join(', ')
  // 合并完关掉 banner —— 除非用户切目录重扫,否则不再追问。保留 hints 数据兜底,
  // _submoduleHintsDismissed=true 让模板隐藏面板。
  parent._submoduleHintsDismissed = true
}

// splitMonorepo 把当前 RepoItem 替换成 N 个 (同 url + 同本地路径,各自 sub_path) 条目。
// 用户点 banner 上的"拆分"按钮调这个。
function splitMonorepo(parentIdx: number) {
  const parent = repos[parentIdx]
  if (!parent || !parent._submoduleHints || parent._submoduleHints.length === 0) return
  const branches = { ...parent.env_branches } // 共用父仓库的 env_branches(同一个 git 仓库分支策略一致)
  const sel = parent._submoduleSelection || {}
  // 只展开勾选了的子模块;空选状态(理论上不可能)兜底全选
  const picked = parent._submoduleHints.filter(h => sel[h.sub_path] !== false)
  if (picked.length === 0) return
  const parentLocalBase = (parent._localPath || '').replace(/\/+$/, '')
  const newRows: RepoItem[] = picked.map(h => {
    // .gitmodules 路径下,h.url 非空 = 真"独立 git repo + 子目录共置";其它 monorepo 路径
    // h.url 为空 = "同一仓库子目录"。两者展开后形态不同:
    //   独立 git repo:每行用自己的 url + 自己的本地路径(父仓本地 + 子模块名)+ 无 sub_path
    //   同仓子目录:每行用父仓 url + 父仓本地路径 + 各自 sub_path
    const isIndependentRepo = !!h.url
    const ownLocalPath = isIndependentRepo && parentLocalBase
      ? parentLocalBase + '/' + h.sub_path.replace(/^\/+/, '')
      : parent._localPath
    return {
      ...makeEmptyRepo(),
      name: h.name,
      url: isIndependentRepo ? (h.url as string) : parent.url,
      stack: h.stack || parent.stack || 'go',
      role: (h.role || 'backend') as RepoRole,
      sub_path: isIndependentRepo ? '' : h.sub_path,
      // service_names 默认 = 子模块名,但只对"业务服务"角色赋值;frontend / common-lib /
      // mobile / infra / docs 这类不算服务,留空更准确(否则会被后续配置中心 / 数据层
      // 扫描当成 service ID 误用)。
      service_names: isServiceRole(h.role) ? h.name : '',
      env_branches: { ...branches },
      config_source: parent.config_source,
      _source: parent._source,
      _localPath: ownLocalPath,
      _scanned: true,
      _scannedSource: parent._scannedSource,
      // 拆分后 role hint 已经从 monorepo_scan 拿到了,直接灌进去(用户一眼看到为啥推这 role)
      _roleHint: { role: h.role, reason: h.reason },
    }
  })
  // 用 N 行替换原来的 1 行;splice 第三参数起是要插入的元素
  repos.splice(parentIdx, 1, ...newRows)
  // 各新行的"环境 → 分支映射"下拉数据:并行调 listBranchesForRepo 拉每个子模块的真实分支,
  // 落到 repoBranchesMap[hint.name] → UI 下拉立即可用。同时按已有 env_branches 做启发式
  // 重映射(如 dev → develop / main 之类)。失败的子模块保持空(text input 兜底,跟原行为一致)。
  for (const row of newRows) {
    const path = row._localPath || ''
    if (!path || !row.name) continue
    const fullPath = row.sub_path
      ? path.replace(/\/+$/, '') + '/' + row.sub_path.replace(/^\/+/, '')
      : path
    listBranchesForRepo(fullPath).then((branches) => {
      if (!branches.length) return
      repoBranchesMap.value[row.name] = branches
      // 已经被 splitMonorepo 设过的 env_branches(从父仓继承)如果不在新分支列表里,
      // 按启发式重新挑一次 —— 同 scanSingleRepo 的逻辑(pickBranchForEnv)。
      for (const env of environments) {
        if (!env.id) continue
        const cur = (row.env_branches[env.id] || '').trim()
        if (cur && branches.includes(cur)) continue // 当前值在真实列表里 → 保留
        const mapped = pickBranchForEnv(env, branches)
        if (mapped) row.env_branches[env.id] = mapped
      }
    }).catch(() => { /* 失败保持空,UI fallback text input */ })
  }
}

function addRepo() {
  repos.push(makeEmptyRepo())
}

// (旧 addSubmodule 按钮已下线 —— 自动检测 monorepo + 一键拆分能覆盖所有真实场景。
// 真有非典型布局漏检,用户可走"+ 添加仓库"再粘 url,行为等价。)

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
//
// 重要:reposRootInput / globalDefaultReposRoot / resolvedReposRoot 三个都是
// "研制环境偏好",不属于具体系统的配置 —— 绝对不能写进 system.yaml,也不能进
// localStorage auto-save draft(见下方 watch(...) 的 tracked 字段列表)。
// 唯一合法的持久化路径:"💾 设为全局默认" 按钮 → setDefaultReposRoot → Go binding
// → userconfig.Save → ~/.tshoot/config.json。导入 yaml / 清空草稿都不动它。
//
const reposRootInput = ref('')
// 全局默认 clone 目录:从 ~/.tshoot/config.json 读,用户一次性设置,跨 wizard 持久
// resolvedReposRoot 永远非空(内置 fallback ~/.tshoot/repos),用作 placeholder +
// 每个仓库 _cloneTarget 空时的实际 clone 目标。
const globalDefaultReposRoot = ref('') // 用户设过的,可能空
const resolvedReposRoot = ref('~/.tshoot/repos') // 永远非空;load 后会覆盖
// homeDir: 后端报的 $HOME,用来把绝对路径前缀折成 ~/... 给用户看。
// 拿不到(浏览器模式 / 后端报错)就留空,displayPath 回落到"原样展示"。
const homeDir = ref('')
onMounted(async () => {
  try {
    const r = await getUserConfig()
    globalDefaultReposRoot.value = r.default_repos_root
    homeDir.value = r.home_dir || ''
    if (r.resolved_repos_root) resolvedReposRoot.value = r.resolved_repos_root
    // 本会话没人改过 reposRootInput(还是空)的话,拿它填一下方便扫描
    if (!reposRootInput.value && r.resolved_repos_root) {
      reposRootInput.value = r.resolved_repos_root
    }
  } catch { /* 读不到 config.json 不打扰用户 */ }
})

// displayPath: 把绝对路径前缀 $HOME 折成 ~,仅用于 UI 展示 placeholder / hint。
// 实际存盘 / 传给后端的路径保持绝对路径不变(git clone / Go os.Stat 不识别 ~)。
// homeDir 拿不到时直接原样返回,不影响可用性。
function displayPath(abs: string): string {
  if (!abs) return ''
  const h = homeDir.value
  if (h && abs === h) return '~'
  if (h && abs.startsWith(h + '/')) return '~' + abs.slice(h.length)
  return abs
}
async function saveAsGlobalDefault() {
  if (!reposRootInput.value.trim()) {
    toast.error('先填路径再设默认')
    return
  }
  try {
    await setDefaultReposRoot(reposRootInput.value.trim())
    globalDefaultReposRoot.value = reposRootInput.value.trim()
    resolvedReposRoot.value = reposRootInput.value.trim()
    toast.success(`已设为全局默认 clone 目录,下次打开 Studio 自动用这里`)
  } catch (e: any) {
    toast.error(`保存失败: ${String(e?.message || e)}`)
  }
}

// repoName -> 真实 git 分支列表;扫描完填充,env_branches 下拉的 options 用它。
// 用 ref<Record> 而不是 per-repo 属性,避免跟 saved yaml 结构污染(env_branches
// 已经在 RepoItem 上了,再加个 branches 会影响序列化)。
// 但必须进 localStorage 草稿 —— 否则重开向导时 map 变成 {},模板里
//   v-if="repoBranchesMap[repo.name]?.length" 不成立 → <select> 退成 <input>,
// 用户会看到分支值(repo.env_branches 已持久化)但没有下拉选项,很迷惑。
const repoBranchesMap = ref<Record<string, string[]>>(
  (saved?.repoBranchesMap as Record<string, string[]>) ?? {},
)


async function pickReposRoot() {
  if (!isDesktop()) {
    toast.error('选目录需要桌面 app 环境;浏览器模式请手动输入路径')
    return
  }
  try {
    const p = await openDir('选择仓库根目录(含各个 repo.name 子目录)')
    if (p) reposRootInput.value = p
  } catch (e: any) {
    toast.error(String(e?.message || e))
  }
}

// 本地模式:用户点"选目录"挑一个已 clone 好的仓库目录。
// 选了新目录 = 换了仓库,彻底重置身份(URL / 名字 / 手改标记 / 已扫过)再从新目录反填,
// 然后触发扫描。不保留上一个目录的任何身份字段 —— 新目录可能 git remote 完全不一样,
// 继承旧 URL 会误导用户。scanSingleRepo 内部还会再清 stack / service_names / 分支映射,
// 保证扫描结果不会混着两次的数据。
async function pickLocalRepoDir(r: RepoItem) {
  if (!isDesktop()) {
    toast.error('选目录需要桌面 app 环境')
    return
  }
  try {
    const p = await openDir('选择已 clone 的仓库目录')
    if (!p) return
    await resolveLocalRepoPath(r, p)
  } catch (e: any) {
    toast.error(String(e?.message || e))
  }
}

// resolveLocalRepoPath 把一个新的本地路径应用到 repo,跑 url/name 反填 + 扫描。
// 唯一入口是 pickLocalRepoDir(选目录按钮) —— 输入框不让手敲,路径一律由 openDir
// 返回保证存在且是绝对路径。
async function resolveLocalRepoPath(r: RepoItem, p: string) {
  const newPath = (p || '').trim()
  if (!newPath) return
  // 换路径 = 换仓库,先清旧 name 对应的分支缓存 + 身份字段
  if (r.name && r.name in repoBranchesMap.value) {
    delete repoBranchesMap.value[r.name]
  }
  r._localPath = newPath
  r.url = ''
  r.name = ''
  r._nameManual = false
  r._scanned = false
  r._scannedSource = ''
  // 清空旧 submodule hints,避免上个仓库的检测结果残留
  r._submoduleHints = undefined
  try {
    const remote = await getRemoteURL(newPath)
    if (remote) {
      r.url = remote
      r.name = deriveRepoName(remote)
    }
  } catch { /* 不是 git 仓库 / 没 origin,容忍继续 */ }
  if (!r.name) {
    const parts = newPath.split(/[\\/]/).filter(Boolean)
    r.name = parts[parts.length - 1] || ''
  }
  // 选完路径立刻跑一次 monorepo 检测(不等 scanSingleRepo 跑完,monorepo 信号是文件结构,
  // 跟 stack/分支扫描独立)。给用户即时反馈,如果是 monorepo,banner 立刻出现。
  refreshSubmoduleHints(r)
  await scanSingleRepo(r)
}

// 远程模式:可选地给该仓库自定义 clone "父目录"。
// 实际 clone 路径 = <picked>/<repo.name>(跟全局默认 reposRoot 一致)。
// 用户选 ~/code,git clone 会创建 ~/code/<name>/,不会污染 ~/code 本身。
//
// 兼容老 draft:如果用户在旧版本里把 path 存成 ~/code/<name>(自己手动加了 name 一层),
// 这里检测到末段就是 r.name 时自动剥掉一层,免得最终落到 ~/code/<name>/<name>。
async function pickCloneTarget(r: RepoItem) {
  if (!isDesktop()) {
    toast.error('选目录需要桌面 app 环境')
    return
  }
  try {
    const p = await openDir(`选 ${r.name || '该仓库'} 的 clone 父目录(会自动建 /${r.name || '<name>'} 子目录)`)
    if (p) {
      // 末段意外撞上 repo.name 时剥一层(用户重复 pick 或拖了老 draft 进来)
      const trimmed = p.replace(/\/$/, '')
      const lastSeg = trimmed.split('/').pop() || ''
      r._cloneTarget = (r.name && lastSeg === r.name) ? trimmed.slice(0, -lastSeg.length - 1) : trimmed
    }
  } catch (e: any) {
    toast.error(String(e?.message || e))
  }
}

// resolveCloneDest 把 "父目录 + repo.name" 拼出真实 clone 落地路径。
// 调用方:scanSingleRepo 构造 repoPaths、Step 8 一键部署构造 repoPaths。
// 返回空串表示"无路径信息(name 也空)",调用方走 effectiveRoot 兜底逻辑。
function resolveCloneDest(r: RepoItem): string {
  const parent = (r._cloneTarget || '').trim().replace(/\/$/, '')
  const name = r.name.trim()
  if (!parent || !name) return ''
  return `${parent}/${name}`
}

// hasRepoSource: 用户是否已经给这个仓库提供了来源线索(URL 或本地目录)。
// 给模板决定要不要展示"仓库名 / 自动识别 / 分支映射"三个下游块 —— 用户没填源
// 之前这些块没有意义(仓库名都没法自动推),一起显示会让"空输入框里怎么有值"
// 显得违和。同时也防 localStorage 里老 draft 的残留数据露出来。
function hasRepoSource(r: RepoItem): boolean {
  if (r._source === 'local') return !!r._localPath?.trim()
  return !!r.url?.trim()
}

// service_names 当前在 yaml 里是逗号分隔的单字段(历史遗留);UI 里要渲染成 chip 列表
// 让用户能点 ✕ 删。拆/合函数成对,保持 yaml 写回时的格式稳定。
function repoServiceNamesList(r: RepoItem): string[] {
  return r.service_names.split(',').map(s => s.trim()).filter(Boolean)
}
function removeServiceName(r: RepoItem, name: string) {
  const list = repoServiceNamesList(r).filter(s => s !== name)
  r.service_names = list.join(', ')
}

// 扫出来识别不全 / 想补一个未被识别的服务名:用户点 "+" 触发 inline 输入 + 回车添加
// svcAddInputs 按 repo 索引存当前"还没提交的输入值",避免互相串。
const svcAddInputs = reactive<Record<number, string>>({})
function addServiceName(r: RepoItem, idx: number) {
  const v = (svcAddInputs[idx] || '').trim()
  if (!v) return
  // 按逗号分段批量添,方便粘一串;去重 + 过滤空串
  const existing = repoServiceNamesList(r)
  const seen = new Set(existing)
  const additions = v.split(/[,\s]+/).map(s => s.trim()).filter(s => s && !seen.has(s))
  if (additions.length === 0) {
    svcAddInputs[idx] = ''
    return
  }
  r.service_names = [...existing, ...additions].join(', ')
  svcAddInputs[idx] = ''
}

// branchOptionsFor: 给分支 <select> 提供选项列表。
//   - 扫到了真实分支(repoBranchesMap[r.name]) → 用那组
//   - 没扫到(首次进入 / 用户手填的老 draft) → 只显示当前已选值,回落到 text input
// 当前值不在列表里时(用户手改过 yaml),先把当前值插到最前,保证下拉也能选回原值。
function branchOptionsFor(r: RepoItem, currentValue: string): string[] {
  const scanned = repoBranchesMap.value[r.name] || []
  if (scanned.length === 0) return currentValue ? [currentValue] : []
  if (currentValue && !scanned.includes(currentValue)) {
    return [currentValue, ...scanned]
  }
  return scanned
}

function setRepoSource(r: RepoItem, src: 'local' | 'remote') {
  if (r._source === src) return // 切到当前源不动,避免误清
  // 切源 = 换了一个仓库,之前扫出来的元信息全作废:URL / 仓库名 / stack /
  // framework / service_names / env_branches / branches 缓存 / 扫描状态。
  // 用户如果真的是"同一个仓库,只是从远程切到本地"或反之,下一步选目录/填 URL
  // 后会立即触发扫描,数据会自动回来,不用保留旧值。
  // 先把分支缓存按旧 name 删(后面 r.name 会清成空,删不了旧 key)
  const oldName = r.name
  if (oldName && oldName in repoBranchesMap.value) {
    delete repoBranchesMap.value[oldName]
  }
  r._source = src
  r.url = ''
  r.name = ''
  r._nameManual = false
  r.stack = ''
  r.framework = ''
  r.service_names = ''
  for (const eid of Object.keys(r.env_branches)) {
    r.env_branches[eid] = ''
  }
  r._scanning = false
  r._scanError = undefined
  r._scanned = false
  r._scannedSource = ''
  // 切到 local:清掉 remote 侧独有的 _cloneTarget;切到 remote:清 _localPath
  if (src === 'local') {
    r._cloneTarget = ''
  } else {
    r._localPath = ''
  }
}

// 单仓库 inline 扫描:本地模式选完目录自动触发;远程模式点"Clone 并扫描"按钮触发。
//
// 本地 vs 远程的差别:
//   - 本地:autoClone=false,直接扫 _localPath;瞬间完成(只跑 marker 探测 + git for-each-ref)
//   - 远程:autoClone=true,先 gitclone 到 _cloneTarget(或 <默认>/name),再扫;耗时几秒到几十秒
//
// 错误隔离:只改当前 repo 的状态字段,其它 repo 不受影响。
async function scanSingleRepo(r: RepoItem) {
  if (!isDesktop()) {
    r._scanError = '扫描仅在桌面 app 可用(浏览器模式请用 CLI:tshoot analyze)'
    return
  }
  if (!r.name.trim()) {
    r._scanError = '仓库名为空,无法扫描(通常 URL / 目录选完会自动填)'
    return
  }
  // 远程模式需要 URL;本地模式需要 _localPath
  if (r._source === 'remote' && !r.url.trim()) {
    r._scanError = '远程模式需要先填仓库 URL'
    return
  }
  if (r._source === 'local' && !r._localPath?.trim()) {
    r._scanError = '本地模式需要先选目录'
    return
  }

  // 构造 RepoPaths:仅这一个仓库的路径覆盖;效用上同 AnalyzeV2 的 per-repo 映射
  const repoPaths: Record<string, string> = {}
  if (r._source === 'local' && r._localPath?.trim()) {
    repoPaths[r.name] = r._localPath.trim()
  } else if (r._source === 'remote') {
    const dest = resolveCloneDest(r)
    if (dest) repoPaths[r.name] = dest
  }
  const autoClone = r._source === 'remote'
  // 远程模式没填本仓库 clone 父目录时需要 effectiveRoot 来拼 ReposRoot/Name
  const effectiveRoot = reposRootInput.value.trim() || resolvedReposRoot.value
  if (autoClone && !repoPaths[r.name] && !effectiveRoot) {
    r._scanError = '远程仓库需要 clone 落地点 —— 填本仓库的 clone 父目录或设全局默认 reposRoot'
    return
  }

  r._scanning = true
  r._scanError = undefined
  // 扫描开始前,把上一次扫描留下的 stack / service_names / 分支全清零。
  // 这样用户换了目录(比如从 truss 切到 nacos-go)后,新目录如果没识别出 service_names,
  // UI 会老老实实显示空,而不是残留前一个仓库的 7 个服务名。分支下拉同理。
  // 名字 / URL 不清:用户可能已经在上面的 pickLocalRepoDir / 自动反填改掉了,不动。
  r.stack = ''
  r.service_names = ''
  for (const eid of Object.keys(r.env_branches)) {
    r.env_branches[eid] = ''
  }
  if (r.name in repoBranchesMap.value) {
    delete repoBranchesMap.value[r.name]
  }
  try {
    const yamlText = generateYAML()
    const res = (await bridgeAnalyzeV2(yamlText, effectiveRoot, repoPaths, autoClone, r.name)) as {
      per_repo?: Array<{
        name: string
        status: string
        error?: string
        detected_stack?: string
        detected_framework?: string
        branches?: string[]
      }>
      report?: {
        config_center?: string
        repos?: Array<{ name: string; service_names?: string[] }>
      }
    }
    const hit = (res.per_repo || []).find(p => p.name === r.name)
    if (!hit) {
      r._scanError = '后端没返回该仓库的扫描结果(name 不匹配?)'
      return
    }
    if (hit.status === 'skipped' || hit.status === 'clone-failed') {
      r._scanError = `${hit.status}: ${hit.error || '未知原因'}`
      return
    }

    // service_names 只对"业务服务"类角色(backend / gateway / middleware / admin)
    // 反填 —— frontend / common-lib / mobile / infra / docs 这类不是服务,反填上服务
    // 名只会污染 routing skill 和后续的配置中心 / 数据层扫描。role 还没识别出来时(空)
    // 也按"业务服务"处理,等 refreshRoleHint 跑完再说。
    const rpt = (res.report?.repos || []).find(rr => rr.name === r.name)
    if (isServiceRole(r.role)) {
      if (rpt?.service_names?.length) {
        r.service_names = rpt.service_names.join(', ')
      } else if (!r.service_names.trim() && r.name) {
        // analyzer 没扫出 service_names(配置 key 不显式 / 单服务仓 / monorepo 子目录 等场景),
        // 默认就用 repo.name 当服务名。"一个仓 = 一个服务"是 95% 用户的预期。
        // 用户想覆盖直接改 chip;routing skill 用这个 key 命中 config-map / k8s_runtime.service_map。
        r.service_names = r.name
      }
    } else {
      // 非业务服务角色:即便 analyzer 扫到 service_names 也清掉(可能是误判)
      r.service_names = ''
    }
    if (hit.detected_stack) r.stack = hit.detected_stack
    if (hit.branches?.length) {
      repoBranchesMap.value[r.name] = hit.branches
      for (const env of environments) {
        if (!env.id) continue
        const mapped = pickBranchForEnv(env, hit.branches)
        if (mapped) r.env_branches[env.id] = mapped
      }
    }

    // 配置中心提示:toast 一次,不静默改 Step 5
    const cc = res.report?.config_center
    if (cc && cc !== 'unknown') {
      toast.info(`扫描完成:识别到配置中心 ${cc}(Step 5 可据此选)`)
    }
    r._scanned = true
    // 记下这次扫描对应的身份(URL 或本地目录),用户以后改了就判定结果过期
    r._scannedSource = r._source === 'local' ? (r._localPath || '') : r.url
    // 扫完顺手刷一次 role 推荐 —— 此时 stack 已经识别出来,本地路径也已就位,
    // 后端的 RecommendRoleForRepo 能进一步看 package.json/pom.xml/go.mod 的依赖,推得最准。
    refreshRoleHint(r)
    // monorepo 检测:看是不是 workspaces / multi-module pom / cmd 多入口 / services/ 多子目录。
    // 命中 N>1 → UI 下面会弹"一键拆成 N 行"banner。
    refreshSubmoduleHints(r)
  } catch (e: any) {
    r._scanError = String(e?.message || e)
  } finally {
    r._scanning = false
  }
}

// ── Step 5: 配置源(多源 schema)──

// ── 多源 schema(顶部多选 + 每源独立填表 + 每服务挑源)──
//
// 数据模型:
//   enabledSourceTypes:{ 'nacos': true, 'kubernetes': true, ... } —— 顶部多选
//     勾哪些 type,系统就声明那些源;每个 type 在 yaml 里 id == type(e.g. id=nacos)。
//     不支持同 type 多个源(罕见场景,需要时手编辑 yaml + 自定义 id)。
//   sourceCreds:每个源的 per-env 凭证 / 端点表单数据,key 是 source type。
//   serviceSourceMap:服务 → 源 type 映射,Step 5 per-env 工作区里每服务选一个。
//     单源时(只勾 1 个)默认全部走那个源;多源时用户显式挑。
//
// yaml emit:
//   - 1 个 type 选中:写老 config_center: { type } 单数(yaml 干净,跟现存项目兼容)
//   - 2+ type 选中:写 config_centers: [{id, type, endpoints, ...}, ...] 数组
//     每个 repo 的 config_source 用其服务们的众数源 type(repo 一般同源)
//
// yaml load:
//   - 老 config_center: → enable 那个 type
//   - 新 config_centers: → enable 数组里所有 type,每个 endpoints 拆进 sourceCreds
type SourceData = {
  creds: Record<string, Record<string, string>>  // creds[envID][fieldKey]
  rawExtra?: Record<string, unknown>             // yaml 高级字段透传(service_map / auth)
}
const ALL_SOURCE_TYPES = ['nacos', 'apollo', 'consul', 'kuboard', 'env-vars'] as const

const enabledSourceTypes = reactive<Record<string, boolean>>(
  saved?.enabledSourceTypes ?? { nacos: true },
)
// 老 draft 兼容:有 configCenterType / extraConfigSources 没有 enabledSourceTypes 时迁移
if (!saved?.enabledSourceTypes && saved?.configCenterType) {
  enabledSourceTypes[saved.configCenterType] = true
}
// enabledSourceOrder:勾选顺序(主源在 [0],副源按勾选先后排,UI v-for 据此渲染)。
// 没有 saved order 时 fallback 到 enabledSourceTypes 的 ALL_SOURCE_TYPES 顺序。
const enabledSourceOrder = reactive<string[]>(
  Array.isArray(saved?.enabledSourceOrder) && saved.enabledSourceOrder.length > 0
    ? saved.enabledSourceOrder.filter((t: string) => enabledSourceTypes[t])
    : ALL_SOURCE_TYPES.filter(t => enabledSourceTypes[t]),
)
// 修补:enabledSourceTypes 里有但 order 里漏掉的(老 draft、import yaml 走老路径) → 追加到末尾
for (const t of ALL_SOURCE_TYPES) {
  if (enabledSourceTypes[t] && !enabledSourceOrder.includes(t)) enabledSourceOrder.push(t)
}
function toggleSourceType(t: string, checked: boolean) {
  // 'none' 与其他源互斥(radio 语义):勾 none 清掉所有其他源;勾其他源清掉 none。
  // 否则会出现 ['nacos','none'] 这种无意义的组合,emit 也走多源路径报错。
  if (checked && t === 'none') {
    for (const k of Object.keys(enabledSourceTypes)) enabledSourceTypes[k] = false
    enabledSourceOrder.splice(0, enabledSourceOrder.length)
    enabledSourceTypes['none'] = true
    enabledSourceOrder.push('none')
    return
  }
  if (checked && t !== 'none' && enabledSourceTypes['none']) {
    enabledSourceTypes['none'] = false
    const i = enabledSourceOrder.indexOf('none')
    if (i !== -1) enabledSourceOrder.splice(i, 1)
  }
  enabledSourceTypes[t] = checked
  const idx = enabledSourceOrder.indexOf(t)
  if (checked) {
    if (idx === -1) enabledSourceOrder.push(t) // 后勾的排到末尾
  } else {
    if (idx !== -1) enabledSourceOrder.splice(idx, 1)
  }
}

const sourceCreds = reactive<Record<string, SourceData>>(
  saved?.sourceCreds ?? {},
)
for (const t of ALL_SOURCE_TYPES) {
  if (!sourceCreds[t]) sourceCreds[t] = { creds: {} }
}

// 兼容:把老 ccCredInputs 里的值搬进 sourceCreds(只在迁移时跑一次)
if (saved?.ccCredInputs && (!saved?.sourceCreds || Object.keys(saved.sourceCreds).length === 0)) {
  for (const k of Object.keys(saved.ccCredInputs)) {
    const m = k.match(/^cc:([^:]+):([^:]+):(.+)$/)
    if (!m) continue
    const [, t, env, field] = m
    if (!sourceCreds[t]) sourceCreds[t] = { creds: {} }
    if (!sourceCreds[t].creds[env]) sourceCreds[t].creds[env] = {}
    sourceCreds[t].creds[env][field] = saved.ccCredInputs[k]
  }
}

const serviceSourceMap = reactive<Record<string, string>>(
  saved?.serviceSourceMap ?? {},
)

// 当前激活的源 types(按固定顺序展示)
const activeSourceTypes = computed<string[]>(() =>
  enabledSourceOrder.filter(t => enabledSourceTypes[t]),
)

// 多源模式:激活源 > 1
const isMultiSource = computed(() => activeSourceTypes.value.length > 1)

// ── Kuboard 资源探测(每 env 独立 state)──
// 用户填了 URL+账密后点"📥 拉取资源"会调 bridge.kuboardListResources,把
// 集群 / namespace / configmap 三级目录拉回来,UI 渲染成级联下拉,免手填。
type KuboardResourceState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'ok', clusters: KuboardClusterEntry[], notes?: string[] }
  | { status: 'error', error: string }
type KuboardClusterEntry = {
  name: string
  namespaces: { name: string; configmaps: string[] }[]
}
// 跨会话恢复:优先吃独立的 KUBOARD_STATE_KEY,fallback 到大 draft blob 里的拷贝。
// 只恢复 status==='ok' 的;loading/error 状态对历史无意义。
const kuboardStateByEnv = reactive<Record<string, KuboardResourceState>>(
  (() => {
    const out: Record<string, KuboardResourceState> = {}
    const src = savedKuboardState ?? saved?.kuboardStateByEnv
    if (src && typeof src === 'object') {
      for (const [k, v] of Object.entries(src as Record<string, any>)) {
        if (v && v.status === 'ok' && Array.isArray(v.clusters)) {
          out[k] = { status: 'ok', clusters: v.clusters, notes: v.notes }
        }
      }
    }
    return out
  })(),
)
// 只保存 ok 状态;loading/error 不持久化。每次 status 改变时立即同步写入,
// 不依赖大 draft watch(它可能因 quota 或排程而错过这次写入)。
function persistKuboardState() {
  try {
    const out: Record<string, KuboardResourceState> = {}
    for (const [k, v] of Object.entries(kuboardStateByEnv)) {
      if (v && v.status === 'ok') out[k] = v
    }
    if (Object.keys(out).length > 0) {
      localStorage.setItem(KUBOARD_STATE_KEY, JSON.stringify(out))
    } else {
      localStorage.removeItem(KUBOARD_STATE_KEY)
    }
  } catch {
    // quota 失败 silent skip
  }
}

async function runKuboardPreloadFromSource(sourceType: string, envID: string) {
  if (!isDesktop()) {
    toast.error('Kuboard 拉取只在桌面 app 可用')
    return
  }
  const data = sourceCreds[sourceType]
  if (!data) return
  const envCreds = data.creds[envID] || {}
  const url = (envCreds.url || '').trim()
  const accessKey = (envCreds.access_key || '').trim()
  const username = (envCreds.username || '').trim()
  const password = (envCreds.password || '').trim()
  if (!url) {
    toast.error(`${envID}: 先填 Kuboard URL`)
    return
  }
  if (!accessKey && (!username || !password)) {
    toast.error(`${envID}: 鉴权填 API 访问凭证(优先),或 用户名+密码`)
    return
  }
  kuboardStateByEnv[envID] = { status: 'loading' }
  try {
    const res = await kuboardListResources(url, username, password, accessKey)
    const clusters = (res.clusters || []).map(c => ({
      name: c.name,
      namespaces: (c.namespaces || []).map(n => ({
        name: n.name,
        configmaps: n.configmaps || [],
      })),
    }))
    kuboardStateByEnv[envID] = { status: 'ok', clusters, notes: res.notes }
    persistKuboardState() // 立即落盘,不等大 draft watch
    if (clusters.length === 0) {
      toast.info(`${envID}: 没拉到集群,看看账号在 Kuboard 里的权限`)
    } else {
      // 顺手给本 env 下走 kuboard 源(主或副)的服务跑一次 auto-match,把 cluster/namespace/configmap
      // 三级下拉自动填上 —— 跟 nacos autoFillSelections 行为对齐,免得用户每个服务手挑 3 次。
      // 主源 vs 副源:这条入口走的是 sourceType,直接传它就行。
      autoFillKuboardSelections(envID, sourceType)
      toast.success(`${envID}: 拉到 ${clusters.length} 个集群`)
    }
  } catch (e: any) {
    const msg = String(e?.message || e)
    kuboardStateByEnv[envID] = { status: 'error', error: msg }
    pushLog('cchub', 'error', `[${envID}] kuboard 拉取失败: ${msg}`, { envID })
    toast.error(`${envID} kuboard 拉取失败: ${msg.slice(0, 80)}`)
  }
}
// 主源版:固定从 sourceCreds['kuboard'] 读
async function runKuboardPreload(envID: string) {
  return runKuboardPreloadFromSource('kuboard', envID)
}

// k8s 运行时(可观测性)拉集群资源:先吃 obs k8s_runtime 自己的 URL+鉴权,
// 没填的话回落到 sourceCreds['kuboard'](同一个 Kuboard 实例时复用)。
// 拉到的资源直接写进 kuboardStateByEnv,跟配置源用同一棵 cluster→ns→cm 树。
async function runK8sRtPreload(envID: string) {
  if (!isDesktop()) {
    toast.error('Kuboard 拉取只在桌面 app 可用')
    return
  }
  const obsURL = (toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'url')] || '').trim()
  const obsAccessKey = (toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'access_key')] || '').trim()
  const obsUser = (toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'username')] || '').trim()
  const obsPass = toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'password')] || ''
  const fallback = sourceCreds['kuboard']?.creds?.[envID] || {}
  const url = obsURL || (fallback.url || '').trim()
  const accessKey = obsAccessKey || (fallback.access_key || '').trim()
  const username = obsUser || (fallback.username || '').trim()
  const password = obsPass || fallback.password || ''
  if (!url) {
    toast.error(`${envID}: 先填 Kuboard URL(可观测性 K8s 运行时 字段)`)
    return
  }
  if (!accessKey && (!username || !password)) {
    toast.error(`${envID}: 鉴权填 API 访问凭证 或 用户名+密码`)
    return
  }
  kuboardStateByEnv[envID] = { status: 'loading' }
  try {
    const res = await kuboardListResources(url, username, password, accessKey)
    const clusters = (res.clusters || []).map(c => ({
      name: c.name,
      namespaces: (c.namespaces || []).map(n => ({ name: n.name, configmaps: n.configmaps || [] })),
    }))
    kuboardStateByEnv[envID] = { status: 'ok', clusters, notes: res.notes }
    persistKuboardState()
    toast.success(`${envID}: 拉到 ${clusters.length} 个集群`)
    // 重新加载集群后,如果该 env 已选了 cluster + namespace,顺手把 deployment 列表也刷一次,
    // 避免用户还要手动再点一遍 namespace 触发拉取。
    const eloc = k8sRuntimeEnvLoc[envID]
    if (eloc?.cluster && eloc?.namespace) {
      // 强制重拉:先清掉旧缓存
      const cacheKey = k8sRtWorkloadKey(envID, eloc.cluster, eloc.namespace)
      delete k8sRtWorkloadCache[cacheKey]
      loadK8sRtWorkloads(envID, eloc.cluster, eloc.namespace)
    }
  } catch (e: any) {
    const msg = String(e?.message || e)
    kuboardStateByEnv[envID] = { status: 'error', error: msg }
    pushLog('cchub', 'error', `[${envID}] k8s_runtime 加载集群失败: ${msg}`, { envID })
    toast.error(`${envID} 加载失败: ${msg.slice(0, 80)}`)
  }
}

// 取当前 env 下,某 cluster 的 namespace 列表(级联下拉用)。
// clusterName 由调用方从所在 form 的 state 读出来传入(主源走 ccCredInputs / 副源走 sourceCreds)。
function kuboardNamespacesFor(envID: string, clusterName: string): string[] {
  const st = kuboardStateByEnv[envID]
  if (!st || st.status !== 'ok') return []
  const c = st.clusters.find(c => c.name === clusterName)
  return c ? c.namespaces.map(n => n.name) : []
}
// 取当前 env 下,某 (cluster, namespace) 的 configmap 列表
function kuboardConfigMapsFor(envID: string, clusterName: string, nsName: string): string[] {
  const st = kuboardStateByEnv[envID]
  if (!st || st.status !== 'ok') return []
  const cluster = st.clusters.find(cl => cl.name === clusterName)
  if (!cluster) return []
  const ns = cluster.namespaces.find(n => n.name === nsName)
  return ns ? ns.configmaps : []
}

// ── k8s 运行时(可观测性)Deployments 缓存 ───────────────────────────
// 跟 kuboardStateByEnv(集群+ns+cm 树)平行,单独存"(env, cluster, ns) → deployments[]"。
// 走 bridge.kuboardListDeployments,返回 name + selector。
type K8sRtWorkloadState =
  | { status: 'loading' }
  | { status: 'ok', deployments: Array<{ name: string; selector: string }> }
  | { status: 'error', error: string }
const k8sRtWorkloadCache = reactive<Record<string, K8sRtWorkloadState>>({})
function k8sRtWorkloadKey(envID: string, cluster: string, ns: string): string {
  return `${envID}::${cluster}::${ns}`
}
function k8sRtWorkloadsFor(envID: string, cluster: string, ns: string): Array<{ name: string; selector: string }> {
  const st = k8sRtWorkloadCache[k8sRtWorkloadKey(envID, cluster, ns)]
  return (st && st.status === 'ok') ? st.deployments : []
}
async function loadK8sRtWorkloads(envID: string, cluster: string, ns: string) {
  if (!cluster || !ns) return
  const key = k8sRtWorkloadKey(envID, cluster, ns)
  if (k8sRtWorkloadCache[key]?.status === 'loading') return
  // 凭证优先吃 obs k8s_runtime 自己填的,fallback 用 kuboard 配置源的(同集群常见复用)
  const url = (toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'url')] || '').trim() ||
              (sourceCreds['kuboard']?.creds?.[envID]?.url || '').trim()
  const accessKey = (toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'access_key')] || '').trim() ||
                    (sourceCreds['kuboard']?.creds?.[envID]?.access_key || '').trim()
  const username = (toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'username')] || '').trim() ||
                   (sourceCreds['kuboard']?.creds?.[envID]?.username || '').trim()
  const password = (toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'password')] || '').trim() ||
                   (sourceCreds['kuboard']?.creds?.[envID]?.password || '').trim()
  if (!url || (!accessKey && (!username || !password))) {
    k8sRtWorkloadCache[key] = { status: 'error', error: '缺 URL 或鉴权信息' }
    return
  }
  k8sRtWorkloadCache[key] = { status: 'loading' }
  pushLog('cchub', 'info', `[${envID}] k8s_runtime 拉 deployments: cluster=${cluster}, ns=${ns}`, { envID })
  try {
    const list = await kuboardListDeployments({
      url, access_key: accessKey, username, password, cluster, namespace: ns,
    })
    const deployments = list.map(d => ({ name: d.name, selector: d.selector || '' }))
    k8sRtWorkloadCache[key] = { status: 'ok', deployments }
    if (list.length === 0) {
      pushLog('cchub', 'warn',
        `[${envID}] k8s_runtime: ns=${ns} 下 Deployment 数为 0(选错 ns?账号 RBAC 权限够 list deployments?)`,
        { envID })
    } else {
      pushLog('cchub', 'info', `[${envID}] k8s_runtime: 拉到 ${list.length} 个 Deployment`, { envID })
      // 自动给每个服务挑最匹配的 deployment(只在用户没手动选过时填,不覆盖已有选择)
      autoPickK8sRtWorkloads(envID, deployments)
    }
  } catch (e: any) {
    const msg = String(e?.message || e)
    k8sRtWorkloadCache[key] = { status: 'error', error: msg }
    pushLog('cchub', 'error', `[${envID}] k8s_runtime 列 deployments 失败: ${msg}`, { envID })
  }
}

// 给本 env 下所有服务自动挑最匹配的 Deployment(不覆盖用户已经手动选过的)。
// 匹配策略(由强到弱):
//   1) deployment 名 == 服务名 / selector app=<服务名> 精确命中
//   2) 候选退化(serviceMatchKeys)+ 段对齐前缀 + env 信号双约束 —— 适配 base-svc-dev / svc-dev 这类
//   3) 候选退化 + 段对齐前缀(不含 env)
//   4) 退化 + 边界中段命中(允许 base- / app- 前缀)
//   5) 模糊兜底(归一化后双向 substring,老行为)
function autoPickK8sRtWorkloads(envID: string, deployments: Array<{ name: string; selector: string }>) {
  if (deployments.length === 0) return
  const norm = (s: string) => s.toLowerCase().replace(/[-_]/g, '')
  const envLower = envID.toLowerCase()
  // boundaryHas 跟 loki / kuboard 一致:候选要么以 boundary 开头,要么以 -cand- / -cand 边界出现。
  // 这样 deployment "base-admin-truss-dev" 能被候选 "admin-truss" 命中,但 "communityfeed-dev"
  // 不会被候选 "community" 命中(community 没在 token 边界)。
  const boundaryHas = (low: string, cand: string) =>
    startsAtBoundary(low, cand) ||
    low.includes('-' + cand + '-') || low.endsWith('-' + cand) ||
    low.includes('_' + cand + '_') || low.endsWith('_' + cand)
  for (const svc of allServiceNames.value) {
    const sloc = ensureK8sRtSvcLoc(envID, svc)
    if (sloc.workload) continue // 用户已经手动选过,不动
    const svcLower = svc.toLowerCase()
    const svcNorm = norm(svc)
    const candidates = serviceMatchKeys(svc)
    let pick: { name: string; selector: string } | undefined
    // 1a) 精确同名
    pick = deployments.find(d => d.name === svc)
    // 1b) selector 标签命中(app= / app.kubernetes.io/name=)
    if (!pick) {
      pick = deployments.find(d => {
        const sel = d.selector
        if (!sel) return false
        const kvs = sel.split(',')
        for (const kv of kvs) {
          const [k, v] = kv.split('=')
          if (!k || !v) continue
          if ((k === 'app' || k === 'app.kubernetes.io/name') && v.toLowerCase() === svcLower) return true
        }
        return false
      })
    }
    // 2) 候选退化 + 边界对齐 + 含 env —— 同 nacos / loki 套路。覆盖 community-grpc-server →
    //    `community-dev` / `base-community-dev`(env 信号兜底,避免误中跨 env 同名 deployment)。
    if (!pick) {
      for (const cand of candidates) {
        const m = deployments.find(d => {
          const dl = d.name.toLowerCase()
          return boundaryHas(dl, cand) && dl.includes(envLower)
        })
        if (m) { pick = m; break }
      }
    }
    // 3) 候选退化 + 边界对齐(不含 env)—— 命名空间已经按 env 分时(base-dev / base-prod),
    //    deployment 名常省略 env 后缀,此 pass 兜底。
    if (!pick) {
      for (const cand of candidates) {
        const m = deployments.find(d => boundaryHas(d.name.toLowerCase(), cand))
        if (m) { pick = m; break }
      }
    }
    // 4) 模糊兜底:归一化后双向 substring(老行为,接非典型命名)
    if (!pick) pick = deployments.find(d => norm(d.name).includes(svcNorm))
    if (!pick) pick = deployments.find(d => svcNorm.includes(norm(d.name)))
    if (pick) {
      sloc.workload = pick.name
      sloc.label_selector = pick.selector || ''
    }
  }
}

// 服务 → 源 type 取值。语义:
//   - 显式设过(包括 '' 空值)→ 用 map 里的(允许"显式取消"是有效状态)
//   - 从未设过(svc 不在 map 里)→ 单源场景默认走唯一源;多源场景默认空(用户必须显式勾)
function getServiceSource(svc: string): string {
  if (svc in serviceSourceMap) {
    const m = serviceSourceMap[svc]
    if (m && enabledSourceTypes[m]) return m
    return '' // 显式 '' 或 type 已被禁
  }
  // 从未被设过(全新仓库刚扫出来的服务)
  if (activeSourceTypes.value.length === 1) return activeSourceTypes.value[0]
  return ''
}
// 服务勾选状态切换:t='' = 显式取消(写空字符串而不是 delete,这样 getServiceSource
// 就知道用户主动取消过,不会再 fallback 到默认主源)
function setServiceSource(svc: string, t: string) {
  const prev = svc in serviceSourceMap ? serviceSourceMap[svc] : (activeSourceTypes.value.length === 1 ? activeSourceTypes.value[0] : '')
  serviceSourceMap[svc] = t
  // 切换源(包括取消勾)→ 清掉旧的 dataId 选择,数据不残留
  if (prev !== t) {
    for (const env of environments) {
      if (!env.id) continue
      const k = svcKey(env.id, svc)
      delete serviceConfigSel[k]
      delete serviceConfigGroup[k]
      delete kuboardSvcMap[k]
    }
  }
}

// ── 兼容 stub:旧代码 / 老 draft 还引用 hasMultiSource / extraConfigSources 等,
// 这里做 noop 兜底,等老 UI 全清完(本 commit 已动 Step 4/5)再删。 ──
const hasMultiSource = computed(() => isMultiSource.value)
const extraConfigSources = reactive<any[]>([]) // 不再使用;新代码看 enabledSourceTypes
const allConfigSourceIDs = computed(() => activeSourceTypes.value.length > 0 ? activeSourceTypes.value : ['default'])
function addExtraConfigSource() { /* no-op,已被顶部 checkbox 多选取代 */ }
void hasMultiSource; void extraConfigSources; void allConfigSourceIDs; void addExtraConfigSource

// 兼容 legacy 模板/代码用:configCenterType 仍然存在,反映"主源"(第一个激活的)。
// 老 ccCredInputs 也保留(yaml 老 emit 用),由 watch 从 sourceCreds[primary] 同步过来。
const configCenterType = computed<string>({
  get: () => activeSourceTypes.value[0] || 'nacos',
  set: (v: string) => {
    // 单选模式 -> 多选模式过渡:setter 只清旧、设新(很少被用)
    for (const t of ALL_SOURCE_TYPES) enabledSourceTypes[t] = false
  enabledSourceTypes['none'] = false
    enabledSourceOrder.splice(0, enabledSourceOrder.length)
    enabledSourceTypes[v] = true
    enabledSourceOrder.push(v)
  },
})

// 配置中心凭证:每个 type 对应一组字段;install.sh read_var 名规则对齐
// (部署时 keychain 值会被导出成这些 env var → install.sh 跳过交互)。
// Secret 字段用 password input + 钥匙串存,非 secret 也一并存钥匙串保持一致。
//
// envVar 是 install.sh 里 read_var 的变量名;Studio 部署逻辑会按规则从 keychain
// 读 "cc:<type>:<env>:<key>" → 写成 env var <envVar> 喂给 install.sh。
interface CredField {
  key: string                         // keychain 子 key,例:"addr" "user" "pass"
  label: string
  secret: boolean
  envVar: (env: string) => string    // install.sh read_var 的变量名
  placeholder?: string
  optional?: boolean
  // options 非空 → 渲染成 <select>(枚举字段,如鉴权方式 access_key / username_password)
  options?: Array<{ value: string; label: string }>
  // showWhen 非空 → 仅当同 env 下某 sibling 字段值匹配时才显示(条件表单)
  showWhen?: { field: string; equals: string }
  // uiOnly:UI 状态字段,不参与 yaml emit / install 凭证收集(如 auth_mode)
  uiOnly?: boolean
}
// CC_FIELDS_BY_TYPE 是个 computed:
//   - nacos / apollo / consul / kubernetes 是固定字段集
//   - env-vars 字段集动态跟着 Step 6 enabledDataStores 走(每个启用的 data store 一条 STATIC_<TYPE>_<ENV> 字段)
//
// 这种 cross-step 联动让用户在 Step 5 就能给 env-vars 源填具体连接串,不用跳到 Step 6 再填。
const CC_FIELDS_BY_TYPE = computed<Record<string, CredField[]>>(() => {
  const envVarsFields: CredField[] = []
  for (const [dsType, on] of Object.entries(enabledDataStores)) {
    if (!on) continue
    envVarsFields.push({
      key: `static_${dsType}`,
      label: `${dsType} 地址`,
      secret: false,
      envVar: (e) => `STATIC_${dsType.toUpperCase()}_${e.toUpperCase()}`,
      placeholder: 'host:port 或 URI',
      optional: true,
    })
  }
  return {
    nacos: [
      { key: 'addr', label: 'Nacos 地址 (host:port)', secret: false, envVar: (e) => `CC_ADDR_${e.toUpperCase()}`, placeholder: 'nacos.example.com:8848' },
      // user/pass 标必填(nacos-mcp-router 强制非空,空字符串会抛 ValueError);默认值
      // 不填留空,生成时会兜底 nacos/nacos(标准默认账号);用户实际不同时手填覆盖。
      { key: 'user', label: '用户名', secret: false, envVar: (e) => `CC_USER_${e.toUpperCase()}`, placeholder: 'nacos(默认)' },
      { key: 'pass', label: '密码', secret: true, envVar: (e) => `CC_PASS_${e.toUpperCase()}`, placeholder: 'nacos(默认)' },
    ],
    apollo: [
      { key: 'meta', label: 'Portal URL', secret: false, envVar: (e) => `APOLLO_META_${e.toUpperCase()}`, placeholder: 'http://apollo-portal:8070' },
      { key: 'token', label: 'Open API Token', secret: true, envVar: (e) => `APOLLO_TOKEN_${e.toUpperCase()}` },
      { key: 'app_id', label: 'App ID', secret: false, envVar: (e) => `APOLLO_APP_ID_${e.toUpperCase()}`, placeholder: 'SampleApp' },
    ],
    consul: [
      { key: 'host', label: 'Consul host (host:port)', secret: false, envVar: (e) => `CONSUL_HOST_${e.toUpperCase()}`, placeholder: 'consul:8500' },
      { key: 'token', label: 'ACL Token', secret: true, envVar: (e) => `CONSUL_TOKEN_${e.toUpperCase()}`, optional: true },
    ],
    // Kuboard 模式:鉴权下拉二选一(API 访问凭证 / 用户名+密码),根据选择条件展开对应字段
    kuboard: [
      { key: 'url', label: 'Kuboard URL', secret: false, envVar: (e) => `KUBOARD_URL_${e.toUpperCase()}`, placeholder: 'https://kuboard.example.com' },
      {
        key: 'auth_mode', label: '鉴权方式', secret: false, envVar: () => '',
        options: [
          { value: 'access_key', label: 'API 访问凭证(推荐 / 免账密)' },
          { value: 'username_password', label: '用户名 + 密码' },
        ],
        uiOnly: true,
      },
      { key: 'access_key', label: 'API 访问凭证', secret: true, envVar: (e) => `KUBOARD_ACCESS_KEY_${e.toUpperCase()}`, placeholder: 'Kuboard 后台 个人中心 → API 访问凭证 → 创建', showWhen: { field: 'auth_mode', equals: 'access_key' } },
      { key: 'username', label: '用户名', secret: false, envVar: (e) => `KUBOARD_USER_${e.toUpperCase()}`, placeholder: 'admin', showWhen: { field: 'auth_mode', equals: 'username_password' } },
      { key: 'password', label: '密码', secret: true, envVar: (e) => `KUBOARD_PASS_${e.toUpperCase()}`, showWhen: { field: 'auth_mode', equals: 'username_password' } },
      { key: 'cluster', label: '集群名', secret: false, envVar: (e) => `KUBOARD_CLUSTER_${e.toUpperCase()}`, placeholder: 'default' },
      { key: 'namespace', label: 'Namespace', secret: false, envVar: (e) => `KUBOARD_NAMESPACE_${e.toUpperCase()}`, placeholder: 'default' },
      { key: 'configmap', label: 'ConfigMap 名称', secret: false, envVar: (e) => `KUBOARD_CONFIGMAP_${e.toUpperCase()}`, placeholder: 'app-config' },
    ],
    // env-vars:动态字段,Step 6 启用了哪些 data store 这里就出哪些
    'env-vars': envVarsFields,
  }
})
// keychain 里用 "cc:<type>:<env>:<field>" 命名;UI 读写走这个 key
function ccKeyFor(type: string, envID: string, field: string): string {
  return `cc:${type}:${envID}:${field}`
}

// 判断字段在当前 env 下是否要隐藏(基于 showWhen 条件)。
// getSibling 由调用方提供,主源走 ccCredInputs,副源走 sourceCreds[t].creds[env]。
function isFieldHidden(_t: string, _envID: string, f: CredField, getSibling: (key: string) => string): boolean {
  if (!f.showWhen) return false
  let siblingVal = getSibling(f.showWhen.field)
  // auth_mode 默认 access_key(没填过时按推荐项算)
  if (f.showWhen.field === 'auth_mode' && !siblingVal) siblingVal = 'access_key'
  return siblingVal !== f.showWhen.equals
}

// 可观测性字段隐藏判断:同 isFieldHidden,但走 toolInputs(obs:tool:env:field)读 sibling。
function isObsFieldHidden(toolKey: string, envID: string, f: CredField): boolean {
  return isFieldHidden('obs', envID, f, (k) => toolInputs[toolKeyFor('obs', toolKey, envID, k)] || '')
}
// ccCredInputs:所有配置中心字段的当前输入值(key = ccKeyFor)。
// 流向:输入 → localStorage draft(持久) → system.yaml → 部署时注入各 AI 平台的 MCP
// server config(openclaw.json / ~/.claude/config.json / .cursor/mcp.json / embedded)。
// **不再走 Studio 自己的钥匙串** —— 对 MCP 用途来说钥匙串是多余中间层,
// 凭证最终要成为 AI 平台 MCP server 的 env 字段,yaml 是直接源。
// UI 上 secret 字段仍用 type=password 做显示遮码(纯视觉)。
// ⚠ yaml 带明文,分享时必须控制范围(团队私密频道 / 内网 git),不要 push 公网。
// ccCredInputs 已被 sourceCreds 替代为多源数据源,这里保留作为"主源镜像"读视图,
// 喂给现存的 yaml emit / preload / 命名空间下拉等老逻辑(它们用 ccKeyFor 拼 key)。
// 用 computed setter 让老代码写也能反向同步到 sourceCreds。
// 初始化:先吃 saved.ccCredInputs(主源表单 v-model 写到这里) → 再让正向 sync 把
// sourceCreds 里更新过的字段(yaml 导入 / 副源迁移)合并进来。两边都不 destructively clear
// —— 那会把"只写到 ccCredInputs 没回写 sourceCreds"的主源凭证整体抹掉。
const ccCredInputs = reactive<Record<string, string>>(saved?.ccCredInputs ?? {})
function syncCcCredInputsFromSource() {
  for (const t of activeSourceTypes.value) {
    const data = sourceCreds[t]
    if (!data) continue
    for (const env of Object.keys(data.creds)) {
      for (const f of Object.keys(data.creds[env])) {
        const k = `cc:${t}:${env}:${f}`
        const v = data.creds[env][f]
        if (ccCredInputs[k] !== v) ccCredInputs[k] = v
      }
    }
  }
}
syncCcCredInputsFromSource()
watch(
  () => JSON.stringify({ s: sourceCreds, e: { ...enabledSourceTypes } }),
  () => syncCcCredInputsFromSource(),
)
// 反向 sync:主源表单 v-model 写 ccCredInputs → 同步回 sourceCreds(yaml emit 真源)。
// diff check 防回环;forward sync 也同样 diff check,所以两边互改不会无限触发。
watch(
  ccCredInputs,
  () => {
    for (const k of Object.keys(ccCredInputs)) {
      const m = k.match(/^cc:([^:]+):([^:]+):(.+)$/)
      if (!m) continue
      const [, t, env, field] = m
      if (!sourceCreds[t]) sourceCreds[t] = { creds: {} }
      if (!sourceCreds[t].creds[env]) sourceCreds[t].creds[env] = {}
      const v = ccCredInputs[k] ?? ''
      if (sourceCreds[t].creds[env][field] !== v) sourceCreds[t].creds[env][field] = v
    }
  },
  { deep: true },
)

// (原本 refreshCCCredStatus / saveCCCreds / clearCCField + ccCredSaved 钥匙串
// 中间层已删除 —— 用户意图是凭证直接进 yaml,最终由部署流程注入到各 AI 平台的 MCP
// server config。Studio 自己的钥匙串对 MCP 场景无意义。)
function clearCCFieldInput(key: string) {
  ccCredInputs[key] = ''
}

// secret 字段"眼睛按钮"状态:key = inputKey(ccCredInputs / toolInputs 的同一个 key),
// set 里有 = 当前显示明文;没有 = password 遮码。不进 draft(纯 UI 态)。
const revealedSecrets = reactive<Set<string>>(new Set<string>())
function toggleReveal(k: string) {
  if (revealedSecrets.has(k)) revealedSecrets.delete(k)
  else revealedSecrets.add(k)
}
function isRevealed(k: string): boolean {
  return revealedSecrets.has(k)
}

// ── 配置中心"真实预加载"─────────────────────────────────────────────
// 按 env 触发一次;用 Step 5 刚填的凭证连目标 nacos/apollo/consul HTTP API,
// 拉出实际 dataId / appId / kv key 列表。UI 列出来给用户挑哪个 locator 对哪个服务。
//
// 状态按 env 分开:正在扫 loading / 扫完 entries / 扫失败 error —— 同时扫多 env 互不干扰。
interface CCHubEnvState {
  status: 'idle' | 'loading' | 'ok' | 'error'
  entries?: CCHubEntry[]
  namespaces?: CCHubNamespace[]   // nacos / apollo 返回的 namespace 列表,用户用它下拉挑
  notes?: string[]
  error?: string
  loadedAt?: number // 记录时间戳给 UI 显示"N 秒前拉的"
}
// 持久化:跨会话保留已成功扫过的 env 的 entries + namespaces + notes(恢复 UI 下拉 / 服务映射);
// loading / error 等瞬态不存 —— 重进应该从 idle 开始,loading 是"还在拉"的状态
const initialCCHubState: Record<string, CCHubEnvState> = {}
for (const [envID, raw] of Object.entries((saved?.ccHubStateByEnv || {}) as Record<string, CCHubEnvState>)) {
  if (raw && raw.status === 'ok') initialCCHubState[envID] = raw
}
const ccHubStateByEnv = reactive<Record<string, CCHubEnvState>>(initialCCHubState)

// ── per-env × per-service 映射:用户挑的"环境对应 namespace" + "服务对应 dataId"──
// 需求:dev 环境 + user 服务 → 应该定位到 nacos 里 dev namespace 下某条 user-*.yaml。
// 数据结构:
//   envNamespaces["dev"] = "go-truss-dev"         // 用户挑的 namespace ID(UUID 或 public)
//   serviceConfigSel["dev::user"] = "user.yaml"   // 用户挑的 dataId
//   serviceConfigGroup["dev::user"] = "DEFAULT_GROUP"  // 记下对应的 group(有些 nacos 非默认 group)
// 生成 yaml 时,落到 infrastructure.config_center.service_map.<env>.<service>:
//   { namespace: go-truss-dev, group: DEFAULT_GROUP, data_id: user.yaml }
const envNamespaces = reactive<Record<string, string>>(saved?.envNamespaces ?? {})
const serviceConfigSel = reactive<Record<string, string>>(saved?.serviceConfigSel ?? {})
const serviceConfigGroup = reactive<Record<string, string>>(saved?.serviceConfigGroup ?? {})

// kuboard 专属:每个 (env, service) 对应的 cluster/namespace/configmap 三元组。
// 与 nacos 的"env→ns + svc→dataId"语义平行,但 kuboard 三个字段都是 per-service 级联挑选,
// 因为同一个 env 不同微服务可能落在不同 ns、不同 cm,甚至不同 cluster。
// key = svcKey(envID, svc) = "envID::svc"
type KuboardSvcLocator = { cluster: string; namespace: string; configmap: string }
const kuboardSvcMap = reactive<Record<string, KuboardSvcLocator>>(saved?.kuboardSvcMap ?? {})
function ensureKuboardLoc(envID: string, svc: string): KuboardSvcLocator {
  const k = svcKey(envID, svc)
  if (!kuboardSvcMap[k]) kuboardSvcMap[k] = { cluster: '', namespace: '', configmap: '' }
  return kuboardSvcMap[k]
}
function setKuboardLoc(envID: string, svc: string, field: 'cluster' | 'namespace' | 'configmap', value: string) {
  const loc = ensureKuboardLoc(envID, svc)
  loc[field] = value
  // 级联清:换 cluster → 清 ns/cm;换 ns → 清 cm。避免遗留无效组合。
  if (field === 'cluster') { loc.namespace = ''; loc.configmap = '' }
  if (field === 'namespace') { loc.configmap = '' }
}

// k8s 运行时(可观测性)专属。两层结构:
//   - 环境级:k8sRuntimeEnvLoc[env] = { cluster, namespace } —— 一个 env 对应一组 K8s 定位,
//     不强求每服务重选(常见情况:一个 env 一个 ns,所有服务都在里面)。
//   - 服务级:k8sRuntimeSvcMap[svcKey] = { workload, label_selector } —— 服务名→Deployment 名 + 自动
//     从 spec.selector.matchLabels 取的 label selector(routing skill 直接喂 KuboardListPods)。
type K8sRuntimeEnvLocator = { cluster: string; namespace: string }
type K8sRuntimeSvcLocator = { workload: string; label_selector: string }
const k8sRuntimeEnvLoc = reactive<Record<string, K8sRuntimeEnvLocator>>(saved?.k8sRuntimeEnvLoc ?? {})
const k8sRuntimeSvcMap = reactive<Record<string, K8sRuntimeSvcLocator>>(saved?.k8sRuntimeSvcMap ?? {})
function ensureK8sRtEnvLoc(envID: string): K8sRuntimeEnvLocator {
  if (!k8sRuntimeEnvLoc[envID]) k8sRuntimeEnvLoc[envID] = { cluster: '', namespace: '' }
  return k8sRuntimeEnvLoc[envID]
}
function ensureK8sRtSvcLoc(envID: string, svc: string): K8sRuntimeSvcLocator {
  const k = svcKey(envID, svc)
  if (!k8sRuntimeSvcMap[k]) k8sRuntimeSvcMap[k] = { workload: '', label_selector: '' }
  return k8sRuntimeSvcMap[k]
}
function setK8sRtEnvLoc(envID: string, field: 'cluster' | 'namespace', value: string) {
  const loc = ensureK8sRtEnvLoc(envID)
  loc[field] = value
  if (field === 'cluster') loc.namespace = ''
  // 换 cluster / ns 后,本 env 所有服务的 workload + selector 失效,清掉
  if (field === 'cluster' || field === 'namespace') {
    for (const k of Object.keys(k8sRuntimeSvcMap)) {
      if (k.startsWith(envID + '::')) {
        k8sRuntimeSvcMap[k] = { workload: '', label_selector: '' }
      }
    }
  }
  // ns 选好后立即拉本 env 下所有服务可选的 deployment 列表
  if (field === 'namespace' && value && loc.cluster) {
    loadK8sRtWorkloads(envID, loc.cluster, value)
  }
}
function setK8sRtSvcWorkload(envID: string, svc: string, workload: string) {
  const sloc = ensureK8sRtSvcLoc(envID, svc)
  sloc.workload = workload
  const eloc = ensureK8sRtEnvLoc(envID)
  const list = k8sRtWorkloadsFor(envID, eloc.cluster, eloc.namespace)
  const dep = list.find(d => d.name === workload)
  sloc.label_selector = dep?.selector || ''
}

function svcKey(envID: string, svc: string): string {
  return `${envID}::${svc}`
}

// 从 repos[].service_names 抽出去重的服务名列表 —— 下拉的每个 env 块都要遍历这一份。
const allServiceNames = computed<string[]>(() => {
  const set = new Set<string>()
  for (const r of repos) {
    for (const s of r.service_names.split(',').map(s => s.trim()).filter(Boolean)) {
      set.add(s)
    }
  }
  return Array.from(set)
})

// 每个 env 独立扫描独立展示。没扫过的 env 不显示映射块,不借用其他 env 的结果。
// (需求变更:之前是"任何 env 扫过 = 全 env 显示下拉",会让用户误以为 prod 的扫描结果
// 是基于 prod 凭证拉的;实际是 dev 的。改成严格自扫自显。)
function envScanned(envID: string): boolean {
  return ccHubStateByEnv[envID]?.status === 'ok'
}

// 给某 env 取 namespace 列表:只看自己的扫描结果,没扫过返回空数组
function namespacesFor(envID: string): CCHubNamespace[] {
  const own = ccHubStateByEnv[envID]
  if (own?.status === 'ok') return own.namespaces || []
  return []
}

// 给某 env+namespace 取可选 entries:只看自己的扫描结果
function entriesSourceFor(envID: string): CCHubEntry[] {
  const own = ccHubStateByEnv[envID]
  if (own?.status === 'ok') return own.entries || []
  return []
}

// 两阶段流程下,state.entries 已经由"只拉 envNamespaces[envID] 指向那个 namespace"精确得到,
// 天然只包含目标 namespace 的条目 —— 不需要再按 tenant 二次过滤。
//
// (之前这里用 e.tenant === nsID 过滤,但后端给 entry.tenant 塞的是 namespace 的 show_name
//  "go-truss-dev",而前端 envNamespaces[envID] 存的是 UUID,二者对不上,filter 全空,
//  dataId 下拉看起来"读不出来"。) nsID 参数保留只为 API 形状一致 + 未来需要时扩展。
function entriesForNamespace(envID: string, _nsID: string): CCHubEntry[] {
  return entriesSourceFor(envID)
}

// 自动匹配 env → namespace:比如 env.id="dev" 找 show_name 含 "dev" 的 namespace。
// 没匹配到就返回第一个非 public 的(避免默认落到空 public 误导)。
function autoMatchNamespace(envID: string, list: CCHubNamespace[]): string {
  if (!list || list.length === 0) return ''
  const lower = envID.toLowerCase()
  // 优先 id / show_name 里含 env 名的(忽略大小写)
  const hit = list.find(n =>
    n.id.toLowerCase().includes(lower) ||
    n.show_name.toLowerCase().includes(lower),
  )
  if (hit) return hit.id
  // 退化:第一个非 public("" 或 "public")的 namespace
  const nonPublic = list.find(n => n.id !== '' && n.id.toLowerCase() !== 'public')
  if (nonPublic) return nonPublic.id
  return list[0].id
}

// 自动匹配 service → dataId:给定环境 + 服务名,在该 namespace 下的 entries 里
// 找 locator 含服务名的;优先同时含 env 名。
// serviceMatchKeys 给一个服务名生成"由具体到泛化"的候选键序列,用于 dataId 匹配。
// 例:
//   community-grpc-server → [community-grpc-server, community-grpc, community]
//   api-truss             → [api-truss, api]
//   order                 → [order]
//
// 背景:wizard 给同仓多入口加了 `<repo>-` 前缀做命名消歧(避免跨仓 cmd/grpc-server 撞名),
// 但 nacos / apollo 的 dataId 经常只用 `<repo>` 这一级(`community-test.yaml`),
// 不会带 cmd entry。如果死按完整服务名找,带前缀的 4 个 *-grpc-server 全部匹不到。
// 退化策略:从最右段开始逐段砍,试更短的前缀,直到命中或剩 1 段。
function serviceMatchKeys(svc: string): string[] {
  const parts = svc.toLowerCase().split('-').filter(Boolean)
  const out: string[] = []
  for (let i = parts.length; i >= 1; i--) {
    out.push(parts.slice(0, i).join('-'))
  }
  return out
}

// startsAtBoundary 段对齐"开头"判定:locator 等于 cand,或 locator 以 cand + 分隔符开头(- / _ / .)。
// 这样 "community" 不会误命中 "communityfeed-test.yaml",但能命中 "community-test.yaml"。
// 抽出来供 nacos / kuboard / 其它源的 auto-match 共享。
function startsAtBoundary(loc: string, cand: string): boolean {
  return loc === cand ||
    loc.startsWith(cand + '-') ||
    loc.startsWith(cand + '.') ||
    loc.startsWith(cand + '_')
}

function autoMatchDataID(envID: string, svc: string, nsID: string): { dataId: string, group: string } {
  const entries = entriesForNamespace(envID, nsID)
  if (entries.length === 0) return { dataId: '', group: '' }
  const candidates = serviceMatchKeys(svc)
  const envLower = envID.toLowerCase()

  // Pass 1:locator 段对齐前缀 + 含 env 关键字 —— 最强信号(典型 nacos 命名 <service>-<env>.yaml)
  for (const cand of candidates) {
    const hit = entries.find(e => {
      const loc = e.locator.toLowerCase()
      return startsAtBoundary(loc, cand) && loc.includes(envLower)
    })
    if (hit) return { dataId: hit.locator, group: hit.group || '' }
  }
  // Pass 2:locator 段对齐前缀(不要求含 env)—— 适配 <service>.yaml 共享配置
  for (const cand of candidates) {
    const hit = entries.find(e => startsAtBoundary(e.locator.toLowerCase(), cand))
    if (hit) return { dataId: hit.locator, group: hit.group || '' }
  }
  // Pass 3:遗留 fuzzy 兜底(完整服务名 substring)—— 老行为,接非常规命名
  const svcLower = svc.toLowerCase()
  let hit = entries.find(e => {
    const loc = e.locator.toLowerCase()
    return loc.includes(svcLower) && loc.includes(envLower)
  })
  if (!hit) hit = entries.find(e => e.locator.toLowerCase().includes(svcLower))
  if (hit) return { dataId: hit.locator, group: hit.group || '' }
  return { dataId: '', group: '' }
}

// autoMatchKuboardLocation 给 kuboard 源的服务找最匹配的 cluster/namespace/configmap。
// 跟 autoMatchDataID 同一套退化策略:serviceMatchKeys 退化候选 + startsAtBoundary 段对齐 + 3-pass。
// 返回 null 表示没找到,UI 保持空让用户手挑。
function autoMatchKuboardLocation(envID: string, svc: string): { cluster: string, namespace: string, configmap: string } | null {
  const state = kuboardStateByEnv[envID]
  if (!state || state.status !== 'ok') return null
  const candidates = serviceMatchKeys(svc)
  const envLower = envID.toLowerCase()
  // 把所有 cluster→namespace→configmap 三联拍平,方便扫描;按出现顺序保留(首个命中赢)。
  const flat: Array<{ cluster: string, namespace: string, configmap: string }> = []
  for (const c of state.clusters) {
    for (const n of c.namespaces) {
      for (const cm of n.configmaps) {
        flat.push({ cluster: c.name, namespace: n.name, configmap: cm })
      }
    }
  }
  if (flat.length === 0) return null
  // Pass 1:configmap 段对齐前缀 + (configmap 含 env 或 namespace 含 env)—— 最强信号
  for (const cand of candidates) {
    const hit = flat.find(x => {
      const cmL = x.configmap.toLowerCase()
      return startsAtBoundary(cmL, cand) && (cmL.includes(envLower) || x.namespace.toLowerCase().includes(envLower))
    })
    if (hit) return hit
  }
  // Pass 2:configmap 段对齐前缀(不要求含 env)—— 跨集群共享或 env 体现在 namespace
  for (const cand of candidates) {
    const hit = flat.find(x => startsAtBoundary(x.configmap.toLowerCase(), cand))
    if (hit) return hit
  }
  // Pass 3:fuzzy 兜底(完整服务名 substring)
  const svcLower = svc.toLowerCase()
  let hit = flat.find(x => {
    const cmL = x.configmap.toLowerCase()
    return cmL.includes(svcLower) && (cmL.includes(envLower) || x.namespace.toLowerCase().includes(envLower))
  })
  if (!hit) hit = flat.find(x => x.configmap.toLowerCase().includes(svcLower))
  return hit || null
}

// autoFillKuboardSelections 给某个 env 的所有"以 kuboard 为源的服务"自动填三联映射。
// sourceType 决定从哪条服务源筛(主源走 configCenterType,副源走传入值,如 'kuboard')。
// 行为跟 autoFillSelections 对齐:已有用户选择的格子不覆盖,只填空的。
function autoFillKuboardSelections(envID: string, sourceType: string = 'kuboard') {
  const state = kuboardStateByEnv[envID]
  if (!state || state.status !== 'ok') return
  for (const svc of allServiceNames.value) {
    if (getServiceSource(svc) !== sourceType) continue
    const k = svcKey(envID, svc)
    const cur = kuboardSvcMap[k]
    if (cur && cur.cluster && cur.namespace && cur.configmap) continue // 三联齐了 → 不动
    const hit = autoMatchKuboardLocation(envID, svc)
    if (!hit) continue
    if (!cur) {
      kuboardSvcMap[k] = { cluster: hit.cluster, namespace: hit.namespace, configmap: hit.configmap }
    } else {
      // 部分填:只补空字段,保留用户已挑的(如已选 cluster 想换 namespace)
      if (!cur.cluster) cur.cluster = hit.cluster
      if (!cur.namespace) cur.namespace = hit.namespace
      if (!cur.configmap) cur.configmap = hit.configmap
    }
  }
}

// 预加载成功后触发一次:帮用户把 namespace + 每个服务的 dataId 猜一遍 —— 只填还没填的,
// 已有用户选择的格子不覆盖(用户想二次预加载刷新列表,但保留手动挑过的映射)。
function autoFillSelections(envID: string) {
  const nsList = namespacesFor(envID)
  if (nsList.length === 0) return
  // 1) namespace
  if (!envNamespaces[envID]) {
    envNamespaces[envID] = autoMatchNamespace(envID, nsList)
  }
  // 2) 只为"用户已勾选走当前 env 的源(主源)"的服务自动填 dataId。
  //    没勾选的服务 = 用户明确不要把这个服务挂到当前源,即便预读拉到了配置项,也不要给它填值。
  //    这样"勾才填"的语义跟 Step 5 服务清单的 UI 期望一致。
  const nsID = envNamespaces[envID] || ''
  const primaryType = configCenterType.value
  for (const svc of allServiceNames.value) {
    if (getServiceSource(svc) !== primaryType) continue // 没勾给这个源的,跳过
    const k = svcKey(envID, svc)
    if (serviceConfigSel[k]) continue // 已手挑 → 不覆盖
    const { dataId, group } = autoMatchDataID(envID, svc, nsID)
    if (dataId) {
      serviceConfigSel[k] = dataId
      serviceConfigGroup[k] = group
    }
  }
}

// 用户切 namespace → 清空这个 env 下所有 service 的 dataId 选择(因为旧 dataId 可能不在新 namespace)。
// 然后如果该 env 有有效凭证,精确重拉新 namespace 下的 configs;没凭证就只跑 autoFill(借其他 env 扫过的 entries)。
function onNamespaceChanged(envID: string, newNsID: string) {
  for (const svc of allServiceNames.value) {
    const k = svcKey(envID, svc)
    delete serviceConfigSel[k]
    delete serviceConfigGroup[k]
  }
  envNamespaces[envID] = newNsID
  // 有凭证 → 精确重拉该 namespace 的 configs
  const payload = buildPreloadPayload(envID)
  if (payload.valid && isDesktop()) {
    void reloadEnvNamespace(envID, newNsID)
  } else {
    // 没凭证:用已有数据(其他 env 扫过的 entries)重跑一次自动匹配
    autoFillSelections(envID)
  }
}

// 用户选 dataId → 同步记下对应的 group(生成 yaml 时要一起写)
function onDataIdChanged(envID: string, svc: string) {
  const nsID = envNamespaces[envID] || ''
  const chosen = serviceConfigSel[svcKey(envID, svc)]
  if (!chosen) {
    delete serviceConfigGroup[svcKey(envID, svc)]
    return
  }
  const entry = entriesForNamespace(envID, nsID).find(e => e.locator === chosen)
  serviceConfigGroup[svcKey(envID, svc)] = entry?.group || ''
}

// 按 env 取当前输入组合(从 ccCredInputs 抽)。跟 install.sh read_var 变量名对齐的字段:
//   nacos:  cc:nacos:<env>:addr / user / pass
//   apollo: cc:apollo:<env>:meta / token
//   consul: cc:consul:<env>:host / token
// Namespace 字段配置里没专门收,用 system.id 当 nacos tenant 默认。
function buildPreloadPayload(envID: string): {
  type: string, addr: string, username: string, password: string,
  token: string, namespace: string, app_id: string,
  valid: boolean, missing: string[]
} {
  const type = configCenterType.value
  const miss: string[] = []
  const get = (field: string) => (ccCredInputs[ccKeyFor(type, envID, field)] || '').trim()

  // kuboard / env-vars / none:不走远程预读(没有 namespace 列表概念,或字段固定),
  // 直接给"missing"提示让按钮 disable
  if (type !== 'nacos' && type !== 'apollo' && type !== 'consul') {
    return {
      type, addr: '', username: '', password: '', token: '', namespace: '', app_id: '',
      valid: false, missing: [`${type} 不支持远程预读(直接在表单填字段即可,部署时按 env 写到 creds.json)`],
    }
  }

  let addr = '', username = '', password = '', token = '', namespace = '', appID = ''
  if (type === 'nacos') {
    addr = get('addr')
    username = get('user')
    password = get('pass')
    // namespace 空 —— 两阶段流程第 1 步用 NamespacesOnly 列全,第 2 步用选中的 UUID
    namespace = ''
    if (!addr) miss.push('nacos 地址')
  } else if (type === 'apollo') {
    addr = get('meta')
    token = get('token')
    appID = get('app_id')
    // namespace = Apollo env 名 —— 两阶段:第 1 步空(用 NamespacesOnly 列 envs),
    // 第 2 步用用户挑的 env 名("DEV"/"UAT"/...)
    namespace = ''
    if (!addr) miss.push('Portal URL')
    if (!token) miss.push('token')
    if (!appID) miss.push('App ID')
  } else if (type === 'consul') {
    addr = get('host')
    token = get('token')
    // namespace = kv 根下的 top-level prefix —— 两阶段:第 1 步空(用 NamespacesOnly 列根),
    // 第 2 步用用户挑的 prefix
    namespace = ''
    if (!addr) miss.push('consul host')
  }
  return {
    type, addr, username, password, token, namespace, app_id: appID,
    valid: miss.length === 0, missing: miss,
  }
}

// 两阶段预加载:
//   第 1 步: NamespacesOnly=true 调用 → 后端只列 namespaces,不拉 configs(快)
//   第 2 步: 按 env.id 启发式匹配到某个 namespace → 只拉那个 namespace 的 configs
// 匹不到时不再拉 configs,给 UI 提示"请手选 namespace",用户从下拉挑后会触发 loadConfigs。
//
// 为什么不用第一次就全量扫? —— 扫了 test/uat/prod 的 configs 也白扫,用户只想要 dev 的。
async function runCCHubPreload(envID: string) {
  if (!isDesktop()) {
    toast.error('预加载只在桌面 app 可用')
    return
  }
  const payload = buildPreloadPayload(envID)
  if (!payload.valid) {
    toast.error(`先把这些字段填上再预加载:${payload.missing.join(', ')}`)
    return
  }
  ccHubStateByEnv[envID] = { status: 'loading' }
  try {
    // ── Step 1: 轻量列 namespaces ──
    const ns = await preloadConfigCenter({
      type: payload.type as 'nacos' | 'apollo' | 'consul',
      addr: payload.addr,
      username: payload.username,
      password: payload.password,
      token: payload.token,
      namespace: '',
      app_id: payload.app_id,
      namespaces_only: true,
    })
    pushLog('cchub', 'info',
      `[${envID}] 列到 ${ns.namespaces?.length || 0} 个 namespace`,
      { envID, type: payload.type, addr: payload.addr })
    for (const n of ns.notes || []) pushLog('cchub', 'info', `[${envID}] ${n}`, { envID })

    // ── Step 2: 按 env.id 启发式匹到对应 namespace,再精确拉那一个 ──
    const matchedNs = autoMatchNamespace(envID, ns.namespaces || [])
    if (!matchedNs && (ns.namespaces?.length || 0) > 0) {
      // 有 namespace 列表但没匹到 → 让用户手选。先把 ns 列表存进 state,UI 展示下拉。
      ccHubStateByEnv[envID] = {
        status: 'ok',
        entries: [],
        namespaces: ns.namespaces || [],
        notes: ns.notes || [],
        loadedAt: Date.now(),
      }
      pushLog('cchub', 'warn',
        `[${envID}] 无法按 env.id 启发式匹到 namespace,请在 UI 手选`, { envID })
      toast.info(`${envID}: 列到 ${ns.namespaces?.length} 个 namespace,但没一条含 "${envID}",请在下拉手选`)
      return
    }
    await loadConfigsForEnv(envID, matchedNs, ns.namespaces || [], payload)
  } catch (e: any) {
    const msg = String(e?.message || e)
    ccHubStateByEnv[envID] = { status: 'error', error: '拉取失败,详见日志' }
    pushLog('cchub', 'error', `[${envID}] 预加载失败: ${msg}`,
      { envID, type: payload.type, addr: payload.addr })
    toast.error(`${envID} 预加载失败,详见左侧「日志」`)
  }
}

// 精确拉某 env 下某 namespace 的 configs(第二阶段,或用户后续切 namespace 触发的重拉)。
// 共享 payload 以避免重取凭证;namespaces 是 Step 1 的结果,挂进 state 供下拉用。
async function loadConfigsForEnv(
  envID: string,
  nsID: string,
  allNamespaces: CCHubNamespace[],
  payload: ReturnType<typeof buildPreloadPayload>,
) {
  ccHubStateByEnv[envID] = { status: 'loading' }
  try {
    const r = await preloadConfigCenter({
      type: payload.type as 'nacos' | 'apollo' | 'consul',
      addr: payload.addr,
      username: payload.username,
      password: payload.password,
      token: payload.token,
      namespace: nsID,
      app_id: payload.app_id,
    })
    // 后端也会带回 namespaces 列表(跟 Step 1 一致),直接用 r.namespaces 覆盖
    ccHubStateByEnv[envID] = {
      status: 'ok',
      entries: r.entries || [],
      namespaces: r.namespaces || allNamespaces,
      notes: r.notes || [],
      loadedAt: Date.now(),
    }
    // 把匹到/选到的 namespace 写进 envNamespaces(autoFill 也需要它)
    envNamespaces[envID] = nsID
    pushLog('cchub', 'info',
      `[${envID}] namespace=${nsID || 'public'} 拉到 ${r.entries?.length || 0} 条配置`,
      { envID, namespace: nsID })
    for (const n of r.notes || []) pushLog('cchub', 'info', `[${envID}] ${n}`, { envID })
    // 清掉 localStorage 遗留的脏 serviceConfigSel:如果之前存的 dataId 不在新 namespace
    // 的 entries 里,清空它;避免 UI 显示空 select(v-model 指向不存在的 option)。
    const validLocators = new Set((r.entries || []).map(e => e.locator))
    for (const svc of allServiceNames.value) {
      const k = svcKey(envID, svc)
      if (serviceConfigSel[k] && !validLocators.has(serviceConfigSel[k])) {
        delete serviceConfigSel[k]
        delete serviceConfigGroup[k]
      }
    }
    // 只对当前 env 跑自动匹配,其他 env 要他们自己扫
    autoFillSelections(envID)
    toast.success(`${envID}: 拉到 ${r.entries?.length || 0} 条配置`)
  } catch (e: any) {
    const msg = String(e?.message || e)
    ccHubStateByEnv[envID] = { status: 'error', error: '拉取失败,详见日志' }
    pushLog('cchub', 'error',
      `[${envID}] namespace=${nsID} 拉取失败: ${msg}`, { envID, namespace: nsID })
    toast.error(`${envID} 拉取失败,详见左侧「日志」`)
  }
}

// 用户在下拉手动切 namespace → 用新 namespace 重拉 configs。没凭证 / 没扫过的 env 忽略。
async function reloadEnvNamespace(envID: string, nsID: string) {
  if (!isDesktop()) return
  const payload = buildPreloadPayload(envID)
  if (!payload.valid) {
    toast.error(`先把这些字段填上再切 namespace:${payload.missing.join(', ')}`)
    return
  }
  const st = ccHubStateByEnv[envID]
  const allNs = st?.namespaces || []
  await loadConfigsForEnv(envID, nsID, allNs, payload)
}

// ── Step 7: 可观测性 + 数据层 ──
const observabilityOptions = ['grafana', 'loki', 'prometheus', 'jaeger', 'elk', 'skywalking', 'tempo', 'k8s_runtime'] as const
const dataStoreOptions = ['redis', 'mongodb', 'elasticsearch', 'mysql', 'postgresql', 'kafka', 'rocketmq', 'rabbitmq', 'clickhouse'] as const

const enabledObservability = reactive<Record<string, boolean>>({
  ...Object.fromEntries(observabilityOptions.map(k => [k, false])),
  ...(saved?.enabledObservability ?? {}),
})
const enabledDataStores = reactive<Record<string, boolean>>({
  ...Object.fromEntries(dataStoreOptions.map(k => [k, false])),
  ...(saved?.enabledDataStores ?? {}),
})

// ── 可观测性 / 数据层 工具规格(类似 CC_FIELDS_BY_TYPE)─────────────
// 每个工具声明:label(中文显示名)、description(一句话)、fields(按环境填的字段)。
// envVar 命名跟未来 install.sh 里 read_var 对齐,保证 wizard 填值 → 部署时直接可喂给
// 各 AI 平台 MCP server 的 env 字段。
//
// 字段 secret 标:跟 Step 5 一致 —— UI 用 type=password,yaml 里也带明文(用户选了共享模式)。
// optional 标:yaml 里填不填都行,install.sh 不会卡流程。

interface ToolSpec {
  key: string
  label: string
  description: string
  fields: CredField[]
}

const OBS_TOOL_SPECS: ToolSpec[] = [
  {
    key: 'grafana', label: 'Grafana', description: '可视化仪表板;Loki / Prometheus / Jaeger 可由它代理',
    fields: [
      { key: 'url', label: 'URL', secret: false, envVar: (e) => `GRAFANA_URL_${e.toUpperCase()}`, placeholder: 'https://grafana-dev.example.com' },
      {
        key: 'auth_mode', label: '鉴权方式', secret: false, envVar: () => '',
        options: [
          { value: 'api_key', label: 'API Key(推荐 / 免账密)' },
          { value: 'username_password', label: '用户名 + 密码' },
        ],
        uiOnly: true,
      },
      { key: 'api_key', label: 'API Key', secret: true, envVar: (e) => `GRAFANA_API_KEY_${e.toUpperCase()}`, placeholder: 'glsa_xxx(Grafana → Service accounts / API keys)', showWhen: { field: 'auth_mode', equals: 'api_key' } },
      { key: 'user', label: '用户名', secret: false, envVar: (e) => `GRAFANA_USER_${e.toUpperCase()}`, placeholder: 'admin', showWhen: { field: 'auth_mode', equals: 'username_password' } },
      { key: 'pass', label: '密码', secret: true, envVar: (e) => `GRAFANA_PASS_${e.toUpperCase()}`, showWhen: { field: 'auth_mode', equals: 'username_password' } },
    ],
  },
  {
    key: 'loki', label: 'Loki', description: '日志聚合;若走 Grafana 代理可仅填 Grafana',
    fields: [
      { key: 'url', label: 'URL', secret: false, envVar: (e) => `LOKI_URL_${e.toUpperCase()}`, optional: true, placeholder: 'http://loki-dev:3100' },
    ],
  },
  {
    key: 'prometheus', label: 'Prometheus', description: '指标与告警;若走 Grafana 代理可仅填 Grafana',
    fields: [
      { key: 'url', label: 'URL', secret: false, envVar: (e) => `PROMETHEUS_URL_${e.toUpperCase()}`, optional: true, placeholder: 'http://prometheus-dev:9090' },
    ],
  },
  {
    key: 'jaeger', label: 'Jaeger', description: '分布式追踪(OpenTelemetry 生态)',
    fields: [
      { key: 'url', label: 'URL', secret: false, envVar: (e) => `JAEGER_URL_${e.toUpperCase()}`, placeholder: 'http://jaeger-dev:16686' },
    ],
  },
  {
    key: 'elk', label: 'ELK (Kibana + Elasticsearch)', description: '日志检索与分析',
    fields: [
      { key: 'kibana_url', label: 'Kibana URL', secret: false, envVar: (e) => `KIBANA_URL_${e.toUpperCase()}`, placeholder: 'https://kibana-dev.example.com' },
      { key: 'es_url', label: 'Elasticsearch URL', secret: false, envVar: (e) => `ELK_ES_URL_${e.toUpperCase()}`, optional: true, placeholder: 'https://es-dev.example.com:9200' },
      { key: 'user', label: '用户名', secret: false, envVar: (e) => `ELK_USER_${e.toUpperCase()}`, optional: true },
      { key: 'pass', label: '密码', secret: true, envVar: (e) => `ELK_PASS_${e.toUpperCase()}`, optional: true },
    ],
  },
  {
    key: 'skywalking', label: 'SkyWalking', description: '国产 APM 追踪',
    fields: [
      { key: 'url', label: 'OAP URL', secret: false, envVar: (e) => `SKYWALKING_URL_${e.toUpperCase()}`, placeholder: 'http://skywalking-oap-dev:12800' },
    ],
  },
  {
    key: 'tempo', label: 'Tempo', description: 'Grafana Labs 追踪后端',
    fields: [
      { key: 'url', label: 'URL', secret: false, envVar: (e) => `TEMPO_URL_${e.toUpperCase()}`, optional: true },
    ],
  },
  {
    key: 'k8s_runtime', label: 'K8s 运行时(Kuboard)', description: '查 pod 状态 / events / 容器日志 / Deployment 滚动状态;走 Kuboard v4 API,无需本机 kubeconfig',
    fields: [
      { key: 'url', label: 'Kuboard URL', secret: false, envVar: (e) => `KUBOARD_URL_${e.toUpperCase()}`, placeholder: 'https://kuboard-dev.example.com(若与配置源同集群可填同样的值)' },
      {
        key: 'auth_mode', label: '鉴权方式', secret: false, envVar: () => '',
        options: [
          { value: 'access_key', label: 'API 访问凭证(推荐 / 免账密)' },
          { value: 'username_password', label: '用户名 + 密码' },
        ],
        uiOnly: true,
      },
      { key: 'access_key', label: 'API 访问凭证', secret: true, envVar: (e) => `KUBOARD_ACCESS_KEY_${e.toUpperCase()}`, placeholder: 'Kuboard 后台 个人中心 → API 访问凭证 → 创建', showWhen: { field: 'auth_mode', equals: 'access_key' } },
      { key: 'username', label: '用户名', secret: false, envVar: (e) => `KUBOARD_USER_${e.toUpperCase()}`, placeholder: 'admin', showWhen: { field: 'auth_mode', equals: 'username_password' } },
      { key: 'password', label: '密码', secret: true, envVar: (e) => `KUBOARD_PASS_${e.toUpperCase()}`, showWhen: { field: 'auth_mode', equals: 'username_password' } },
    ],
  },
]

const DS_TOOL_SPECS: ToolSpec[] = [
  {
    key: 'redis', label: 'Redis', description: '缓存 / 键值存储 / pub-sub',
    fields: [
      { key: 'url', label: '连接 URL', secret: true, envVar: (e) => `REDIS_URL_${e.toUpperCase()}`, placeholder: 'redis://:password@host:6379/0' },
    ],
  },
  {
    key: 'mongodb', label: 'MongoDB', description: '文档数据库',
    fields: [
      { key: 'uri', label: '连接 URI', secret: true, envVar: (e) => `MONGODB_URI_${e.toUpperCase()}`, placeholder: 'mongodb://user:pass@host:27017/dbname' },
    ],
  },
  {
    key: 'elasticsearch', label: 'Elasticsearch', description: '全文检索引擎',
    fields: [
      { key: 'url', label: 'URL', secret: false, envVar: (e) => `ES_URL_${e.toUpperCase()}`, placeholder: 'https://es-dev.example.com:9200' },
      { key: 'user', label: '用户名', secret: false, envVar: (e) => `ES_USER_${e.toUpperCase()}`, optional: true },
      { key: 'pass', label: '密码', secret: true, envVar: (e) => `ES_PASS_${e.toUpperCase()}`, optional: true },
    ],
  },
  {
    key: 'mysql', label: 'MySQL', description: '关系数据库',
    fields: [
      { key: 'dsn', label: 'DSN', secret: true, envVar: (e) => `MYSQL_DSN_${e.toUpperCase()}`, placeholder: 'user:pass@tcp(host:3306)/dbname' },
    ],
  },
  {
    key: 'postgresql', label: 'PostgreSQL', description: '关系数据库',
    fields: [
      { key: 'dsn', label: 'DSN', secret: true, envVar: (e) => `POSTGRES_DSN_${e.toUpperCase()}`, placeholder: 'postgres://user:pass@host:5432/dbname' },
    ],
  },
  {
    key: 'kafka', label: 'Kafka', description: '消息队列 / 流处理',
    fields: [
      { key: 'brokers', label: 'Brokers', secret: false, envVar: (e) => `KAFKA_BROKERS_${e.toUpperCase()}`, placeholder: 'host1:9092,host2:9092' },
      { key: 'user', label: 'SASL 用户名', secret: false, envVar: (e) => `KAFKA_USER_${e.toUpperCase()}`, optional: true },
      { key: 'pass', label: 'SASL 密码', secret: true, envVar: (e) => `KAFKA_PASS_${e.toUpperCase()}`, optional: true },
    ],
  },
  {
    key: 'rocketmq', label: 'RocketMQ', description: '阿里系消息中间件',
    fields: [
      { key: 'namesrv', label: 'Name Server', secret: false, envVar: (e) => `ROCKETMQ_NAMESRV_${e.toUpperCase()}`, placeholder: 'host1:9876;host2:9876' },
    ],
  },
  {
    key: 'rabbitmq', label: 'RabbitMQ', description: 'AMQP 消息中间件',
    fields: [
      { key: 'url', label: '连接 URL', secret: true, envVar: (e) => `RABBITMQ_URL_${e.toUpperCase()}`, placeholder: 'amqp://user:pass@host:5672/vhost' },
    ],
  },
  {
    key: 'clickhouse', label: 'ClickHouse', description: 'OLAP 分析型数据库',
    fields: [
      { key: 'url', label: 'URL', secret: false, envVar: (e) => `CLICKHOUSE_URL_${e.toUpperCase()}`, placeholder: 'http://clickhouse-dev:8123' },
      { key: 'user', label: '用户名', secret: false, envVar: (e) => `CLICKHOUSE_USER_${e.toUpperCase()}`, optional: true },
      { key: 'pass', label: '密码', secret: true, envVar: (e) => `CLICKHOUSE_PASS_${e.toUpperCase()}`, optional: true },
    ],
  },
]

// 统一 key:"obs:<tool>:<env>:<field>" / "ds:<tool>:<env>:<field>"
function toolKeyFor(cat: 'obs' | 'ds', tool: string, envID: string, field: string): string {
  return `${cat}:${tool}:${envID}:${field}`
}

// 所有工具字段的输入值(含 secret);跟 ccCredInputs 同策略:进 localStorage draft + 写进 yaml
const toolInputs = reactive<Record<string, string>>(saved?.toolInputs ?? {})

function clearToolFieldInput(k: string) {
  toolInputs[k] = ''
}

function toolSpecByKey(cat: 'obs' | 'ds', key: string): ToolSpec | undefined {
  const arr = cat === 'obs' ? OBS_TOOL_SPECS : DS_TOOL_SPECS
  return arr.find(s => s.key === key)
}

// ── Step 7 可观测性自动连通性测试 ─────────────────────────────────────
// 每个工具的 (envID, toolKey) 对应一次结果。用户改任一字段(url / user / pass / api_key)
// 都重新触发,800ms 防抖。不显示按钮,跟 Step 3 一致。
interface OBSProbeState { status: 'idle' | 'loading' | 'ok' | 'fail'; latency?: string; detail?: string; error?: string }
const obsProbeResults = reactive<Record<string, OBSProbeState>>({})
const obsProbeTimers: Record<string, ReturnType<typeof setTimeout>> = {}
function obsProbeKey(toolKey: string, envID: string): string { return `${toolKey}::${envID}` }
// 每个 obs 工具的 "主 URL 字段" key —— 多数是 'url',ELK 是 'kibana_url'
function obsPrimaryURLField(spec: ToolSpec): string {
  if (spec.fields.find(f => f.key === 'url')) return 'url'
  if (spec.fields.find(f => f.key === 'kibana_url')) return 'kibana_url'
  return ''
}
function scheduleObsProbe(toolKey: string, envID: string) {
  const spec = OBS_TOOL_SPECS.find(s => s.key === toolKey)
  if (!spec) return
  const urlField = obsPrimaryURLField(spec)
  if (!urlField) return
  const k = obsProbeKey(toolKey, envID)
  if (obsProbeTimers[k]) clearTimeout(obsProbeTimers[k])
  const url = (toolInputs[toolKeyFor('obs', toolKey, envID, urlField)] || '').trim()
  if (!url) {
    delete obsProbeResults[k]
    return
  }
  const user = (toolInputs[toolKeyFor('obs', toolKey, envID, 'user')] || '').trim()
  const pass = toolInputs[toolKeyFor('obs', toolKey, envID, 'pass')] || ''
  const apiKey = toolInputs[toolKeyFor('obs', toolKey, envID, 'api_key')] || ''
  obsProbeTimers[k] = setTimeout(async () => {
    if (!isDesktop()) return
    obsProbeResults[k] = { status: 'loading' }
    try {
      const r = await probeURLAuth(url, user, pass, apiKey)
      obsProbeResults[k] = r.ok
        ? { status: 'ok', latency: r.latency, detail: r.detail }
        : { status: 'fail', error: r.error || '不可达' }
    } catch (e: any) {
      obsProbeResults[k] = { status: 'fail', error: String(e?.message || e) }
    }
  }, 800)
}
// 切到 Step 7 时主动跑一次(草稿恢复后立刻看状态,不等用户重新输入)。
// 注意:不能用 immediate:true,因为 callback 里会访问到声明在本 watch 之后的 const
// (lokiMappingByEnv / OBS_GRAFANA_DS_TYPES 等),同步触发会撞 TDZ。
// 改用 onMounted 兜底首次触发,确保所有 const 已初始化。
const triggerStep7Init = (s: number) => {
  if (s !== 7) return
  for (const spec of OBS_TOOL_SPECS) {
    if (!enabledObservability[spec.key]) continue
    for (const env of environments) {
      if (!env.id) continue
      scheduleObsProbe(spec.key, env.id)
    }
  }
  // k8s_runtime:进入 Step 7 时,把每个 env 已选的 (cluster, ns) 对应的 deployment 列表预拉一次,
  // 让服务行的 workload 下拉直接有内容、跟之前选过的 workload 匹配显示。
  if (enabledObservability['k8s_runtime']) {
    for (const [envID, loc] of Object.entries(k8sRuntimeEnvLoc)) {
      if (!loc?.cluster || !loc?.namespace) continue
      const cacheKey = k8sRtWorkloadKey(envID, loc.cluster, loc.namespace)
      if (k8sRtWorkloadCache[cacheKey]?.status === 'ok') continue
      loadK8sRtWorkloads(envID, loc.cluster, loc.namespace)
    }
  }
  // grafana:草稿恢复后若 URL+鉴权已填,自动拉一次 datasources(同样不等用户重新输入)
  if (enabledObservability['grafana']) {
    for (const env of environments) {
      if (!env.id) continue
      const lm = getLokiMapping(env.id)
      if (lm.dsListStatus === 'ok' || lm.dsListStatus === 'loading') continue
      scheduleGrafanaDsAutoload(env.id)
    }
  }
}
watch(() => currentStep.value, triggerStep7Init)
onMounted(() => triggerStep7Init(currentStep.value))

// ── Loki 标签映射(Step 7 grafana/loki 子区,per-env) ──────────────────
// 每个 env 独立维护 grafana 凭证 → datasource 列表 → labels → values → 选中映射,
// 因为 dev / prod 可能用不同 Grafana / Loki 实例,UID 和 label values 都不一样。
// envLabelKey / serviceLabelKey 也 per-env(虽然通常 namespace/app 会一致,但允许差异)。
interface LokiMappingPerEnv {
  dsList: GrafanaDatasource[]
  dsUID: string
  dsListStatus: 'idle' | 'loading' | 'ok' | 'fail'
  dsListError?: string
  labels: string[]
  labelStatus: 'idle' | 'loading' | 'ok' | 'fail'
  labelError?: string
  envLabelKey: string
  serviceLabelKey: string
  envLabelValues: string[]
  serviceLabelValues: string[]
  envValue: string
  serviceValues: Record<string, string>
  // serviceMatchTried[svc] = true 表示 auto-match 已经跑过这个服务但没找到候选,
  // UI 据此区分"未触发自动匹配(默认空)"vs"匹配过但没找到(应该提示用户)"。
  // 用户手挑后 serviceValues[svc] 非空,UI 自然不再显示"未匹配"提示。
  serviceMatchTried?: Record<string, boolean>
}
function makeEmptyLokiMappingPerEnv(): LokiMappingPerEnv {
  return {
    dsList: [], dsUID: '', dsListStatus: 'idle',
    labels: [], labelStatus: 'idle',
    envLabelKey: '', serviceLabelKey: '',
    envLabelValues: [], serviceLabelValues: [],
    envValue: '', serviceValues: {},
    serviceMatchTried: {},
  }
}
const lokiMappingByEnv = reactive<Record<string, LokiMappingPerEnv>>(
  (saved?.lokiMappingByEnv as Record<string, LokiMappingPerEnv>) ?? {},
)
function getLokiMapping(envID: string): LokiMappingPerEnv {
  if (!lokiMappingByEnv[envID]) {
    lokiMappingByEnv[envID] = makeEmptyLokiMappingPerEnv()
  } else {
    // 防御:saved 里可能是被 quota 兜底瘦身后的残缺对象(缺 dsList/labels/*LabelValues 等)。
    // 补齐所有字段,免得模板访问 undefined.length 之类直接 throw 把整个页面拉白屏。
    const lm = lokiMappingByEnv[envID] as Partial<LokiMappingPerEnv>
    if (!Array.isArray(lm.dsList)) lm.dsList = []
    if (!lm.dsListStatus) lm.dsListStatus = 'idle'
    if (!Array.isArray(lm.labels)) lm.labels = []
    if (!lm.labelStatus) lm.labelStatus = 'idle'
    if (typeof lm.dsUID !== 'string') lm.dsUID = ''
    if (typeof lm.envLabelKey !== 'string') lm.envLabelKey = ''
    if (typeof lm.serviceLabelKey !== 'string') lm.serviceLabelKey = ''
    if (!Array.isArray(lm.envLabelValues)) lm.envLabelValues = []
    if (!Array.isArray(lm.serviceLabelValues)) lm.serviceLabelValues = []
    if (typeof lm.envValue !== 'string') lm.envValue = ''
    if (!lm.serviceValues || typeof lm.serviceValues !== 'object') lm.serviceValues = {}
  }
  return lokiMappingByEnv[envID]
}

// 通过 Grafana 代理访问的可观测性组件(prometheus/jaeger/tempo/elk)在每个 env 下
// 对应的 Grafana datasource UID。Loki 走 lokiMappingByEnv[env].dsUID(因为还要拉 labels);
// 其他类型只需选 UID,所以放进这个扁平 map。key="<obsType>:<env>"。
// dsList(候选下拉项)继续复用 lokiMappingByEnv[env].dsList,各 obsType 按 .type 字段过滤。
const grafanaDsUidByObsEnv = reactive<Record<string, string>>(saved?.grafanaDsUidByObsEnv ?? {})
function obsGrafanaDsKey(obsKey: string, envID: string): string {
  return `${obsKey}:${envID}`
}

// 每个 (obs, env) 的访问方式:via_grafana = 走 Grafana 代理(只需选 ds);direct = 直连(填 URL+auth)。
// 默认值:Grafana 启用 + 该工具属 via-grafana 候选(loki/prometheus/jaeger/tempo/elk)→ via_grafana;
// 否则 → direct。用户可在卡顶 select 显式切换。
type ObsAccessMode = 'via_grafana' | 'direct'
const VIA_GRAFANA_ELIGIBLE = ['loki', 'prometheus', 'jaeger', 'tempo', 'elk'] as const
const obsAccessModeMap = reactive<Record<string, ObsAccessMode>>(saved?.obsAccessModeMap ?? {})
function obsAccessKey(obsKey: string, envID: string): string {
  return `${obsKey}:${envID}`
}
function getObsAccessMode(obsKey: string, envID: string): ObsAccessMode {
  if (!(VIA_GRAFANA_ELIGIBLE as readonly string[]).includes(obsKey)) return 'direct'
  const explicit = obsAccessModeMap[obsAccessKey(obsKey, envID)]
  if (explicit) return explicit
  return enabledObservability['grafana'] ? 'via_grafana' : 'direct'
}
function setObsAccessMode(obsKey: string, envID: string, mode: ObsAccessMode) {
  obsAccessModeMap[obsAccessKey(obsKey, envID)] = mode
}
// obsKey → grafana datasource.type 的允许值(可多个,如 elk 需要 elasticsearch)
const OBS_GRAFANA_DS_TYPES: Record<string, string[]> = {
  loki: ['loki'],
  prometheus: ['prometheus'],
  jaeger: ['jaeger'],
  tempo: ['tempo'],
  elk: ['elasticsearch'],
}
function obsGrafanaDsCandidates(envID: string, obsKey: string): GrafanaDatasource[] {
  const lm = lokiMappingByEnv[envID]
  if (!lm || lm.dsList.length === 0) return []
  const types = OBS_GRAFANA_DS_TYPES[obsKey] || []
  return lm.dsList.filter(d => types.includes(d.type))
}

function lokiAuthFor(envID: string) {
  const lm = getLokiMapping(envID)
  return {
    grafana_url: (toolInputs[toolKeyFor('obs', 'grafana', envID, 'url')] || '').trim(),
    api_key: toolInputs[toolKeyFor('obs', 'grafana', envID, 'api_key')] || '',
    user: (toolInputs[toolKeyFor('obs', 'grafana', envID, 'user')] || '').trim(),
    pass: toolInputs[toolKeyFor('obs', 'grafana', envID, 'pass')] || '',
    loki_url: (toolInputs[toolKeyFor('obs', 'loki', envID, 'url')] || '').trim(),
    ds_uid: lm.dsUID,
  }
}

async function loadLokiDatasources(envID: string) {
  const lm = getLokiMapping(envID)
  lm.dsListStatus = 'loading'
  lm.dsListError = undefined
  try {
    const auth = lokiAuthFor(envID)
    if (!auth.grafana_url) throw new Error('请先填本环境 Grafana URL')
    const list = await listGrafanaDatasources(auth)
    lm.dsList = list
    lm.dsListStatus = 'ok'
    if (!lm.dsUID) {
      const loki = list.find(d => d.is_loki)
      if (loki) lm.dsUID = loki.uid
    }
    // 自动给 prometheus / jaeger / tempo / elk 也填默认 datasource:每种 type 取第一个匹配的
    for (const obsKey of ['prometheus', 'jaeger', 'tempo', 'elk']) {
      const k = obsGrafanaDsKey(obsKey, envID)
      if (grafanaDsUidByObsEnv[k]) continue // 用户填过则不动
      const candidates = list.filter(d => (OBS_GRAFANA_DS_TYPES[obsKey] || []).includes(d.type))
      const def = candidates.find(d => d.default) || candidates[0]
      if (def) grafanaDsUidByObsEnv[k] = def.uid
    }
    const counts: Record<string, number> = {}
    for (const d of list) counts[d.type] = (counts[d.type] || 0) + 1
    const summary = Object.entries(counts).map(([t, n]) => `${t}=${n}`).join(', ')
    pushLog('cchub', 'info', `[${envID}] Grafana 列到 ${list.length} 个 datasource(${summary})`, { envID })
  } catch (e: any) {
    lm.dsListStatus = 'fail'
    lm.dsListError = String(e?.message || e)
    pushLog('cchub', 'error', `[${envID}] 列 Grafana datasource 失败: ${lm.dsListError}`, { envID })
  }
}

// Grafana URL+鉴权填好后自动拉一次 datasources(800ms 防抖)。
// 用户改 URL/Key/账号 → 等输入稳定 → 自动重新加载,不必手动点"加载"。
const grafanaDsAutoTimers: Record<string, ReturnType<typeof setTimeout>> = {}
function scheduleGrafanaDsAutoload(envID: string) {
  if (!isDesktop()) return
  if (!enabledObservability['grafana']) return
  const auth = lokiAuthFor(envID)
  if (!auth.grafana_url) return
  if (!auth.api_key && (!auth.user || !auth.pass)) return
  const k = `gads:${envID}`
  if (grafanaDsAutoTimers[k]) clearTimeout(grafanaDsAutoTimers[k])
  grafanaDsAutoTimers[k] = setTimeout(() => loadLokiDatasources(envID), 800)
}

async function loadLokiLabels(envID: string) {
  const lm = getLokiMapping(envID)
  lm.labelStatus = 'loading'
  lm.labelError = undefined
  try {
    const auth = lokiAuthFor(envID)
    if (!auth.grafana_url && !auth.loki_url) {
      throw new Error('请先填本环境 Grafana URL 或 Loki URL')
    }
    if (auth.grafana_url && !auth.ds_uid) {
      throw new Error('请先选中本环境的 Loki datasource')
    }
    const r = await listLokiLabels(auth)
    lm.labels = r.labels || []
    lm.labelStatus = 'ok'
    pushLog('cchub', 'info', `[${envID}] Loki 拉到 ${lm.labels.length} 个 label key`, { envID })
    if (!lm.envLabelKey) {
      const guess = lm.labels.find(l => l === 'namespace')
        || lm.labels.find(l => l.includes('namespace'))
        || lm.labels.find(l => l.includes('env'))
      if (guess) lm.envLabelKey = guess
    }
    if (!lm.serviceLabelKey) {
      const guess = lm.labels.find(l => l === 'app')
        || lm.labels.find(l => l === 'service')
        || lm.labels.find(l => l === 'job')
        || lm.labels.find(l => l.includes('container'))
      if (guess) lm.serviceLabelKey = guess
    }
    if (lm.envLabelKey) await loadEnvLabelValues(envID)
    autoMatchLokiMapping(envID)
    // envValue 自动匹完之后再拉 serviceLabelValues —— 这次会带 selector 过滤,
    // 只列该 namespace 下出现过的 app,避免列出来一堆别 namespace 的 app
    if (lm.serviceLabelKey) await loadServiceLabelValues(envID)
    autoMatchLokiMapping(envID)
  } catch (e: any) {
    lm.labelStatus = 'fail'
    lm.labelError = String(e?.message || e)
    pushLog('cchub', 'error', `[${envID}] 列 Loki labels 失败: ${lm.labelError}`, { envID })
  }
}

async function loadEnvLabelValues(envID: string) {
  const lm = getLokiMapping(envID)
  if (!lm.envLabelKey) return
  try {
    const auth = lokiAuthFor(envID)
    const r = await listLokiLabelValues(auth, lm.envLabelKey)
    lm.envLabelValues = r.values || []
  } catch (e: any) {
    pushLog('cchub', 'error', `[${envID}] 列 ${lm.envLabelKey} 值失败: ${e?.message || e}`, { envID })
  }
}
// loadServiceLabelValues:如果已选了 envValue,会带 LogQL selector 过滤,
// 只拉该 namespace 下确实出现过的 app 值,避免列出来一堆别的 namespace 的 app。
async function loadServiceLabelValues(envID: string) {
  const lm = getLokiMapping(envID)
  if (!lm.serviceLabelKey) return
  let query = ''
  if (lm.envLabelKey && lm.envValue) {
    // 转义 envValue 里的双引号(防止破坏 LogQL 语法)
    const safeVal = lm.envValue.replace(/"/g, '\\"')
    query = `{${lm.envLabelKey}="${safeVal}"}`
  }
  try {
    const auth = lokiAuthFor(envID)
    const r = await listLokiLabelValues(auth, lm.serviceLabelKey, query)
    lm.serviceLabelValues = r.values || []
    pushLog('cchub', 'info',
      `[${envID}] ${lm.serviceLabelKey} ${query ? '(限定 ' + query + ')' : ''} 拉到 ${lm.serviceLabelValues.length} 个值`,
      { envID })
  } catch (e: any) {
    pushLog('cchub', 'error', `[${envID}] 列 ${lm.serviceLabelKey} 值失败: ${e?.message || e}`, { envID })
  }
}

// envValue 改变 → 旧 service 选择全清(可能新 namespace 下根本没那些 app),
// 重拉 serviceLabelValues(限定到新 namespace 内),再启发式自动匹一遍。
async function onEnvValueChanged(envID: string) {
  const lm = getLokiMapping(envID)
  lm.serviceValues = {}
  await loadServiceLabelValues(envID)
  autoMatchLokiMapping(envID)
}

// 启发式自动匹:env.id="dev" → 在 envLabelValues 里找含 "dev" 的;
// service 名 → serviceLabelValues 里找含服务名的。仅填空,不覆盖。
function autoMatchLokiMapping(envID: string) {
  const lm = getLokiMapping(envID)
  if (!lm.envValue) {
    const lower = envID.toLowerCase()
    const hit = lm.envLabelValues.find(v => v.toLowerCase().includes(lower))
    if (hit) lm.envValue = hit
  }
  // 服务 label 值匹配:跟 nacos / kuboard 同套退化策略 ——
  // serviceMatchKeys 生成 [community-grpc-server, community-grpc, community] 候选,
  // 段对齐前缀 + env 信号优先,逐级 fallback。loki app 标签常见命名:
  //   <service>-<env>             如 community-scheduler-dev
  //   base-<service>-<env>        如 base-admin-truss-dev
  //   <repo>-<env>                如 community-dev(没区分 cmd entry 时)
  // env 信号比 nacos 更强(loki 标签几乎一定带 env 后缀),所以 Pass 1 require env match。
  const envLower = envID.toLowerCase()
  const lmValuesLower = lm.serviceLabelValues.map(v => ({ raw: v, low: v.toLowerCase() }))
  // boundaryWith:label 值要么以 cand 开头、要么含 -cand- / -cand 边界(允许前缀加 base- / app- 这种)。
  // loki app 标签常有 base-/app- 前缀,纯 startsAtBoundary 太严会漏 base-admin-truss-dev → admin-truss。
  const boundaryHas = (low: string, cand: string): boolean => {
    if (startsAtBoundary(low, cand)) return true
    return low.includes('-' + cand + '-') || low.endsWith('-' + cand) || low.includes('_' + cand + '_') || low.endsWith('_' + cand)
  }
  for (const svc of allServiceNames.value) {
    if (lm.serviceValues[svc]) continue // 已选(真实标签值)→ 不覆盖
    const candidates = serviceMatchKeys(svc)
    let hit: string | undefined
    // Pass 1:候选 boundary + 含 env
    for (const cand of candidates) {
      const m = lmValuesLower.find(v => boundaryHas(v.low, cand) && v.low.includes(envLower))
      if (m) { hit = m.raw; break }
    }
    // Pass 2:候选 boundary(不含 env)
    if (!hit) {
      for (const cand of candidates) {
        const m = lmValuesLower.find(v => boundaryHas(v.low, cand))
        if (m) { hit = m.raw; break }
      }
    }
    // Pass 3:fuzzy 完整服务名 substring
    if (!hit) {
      const sLower = svc.toLowerCase()
      const m = lmValuesLower.find(v => v.low.includes(sLower) && v.low.includes(envLower))
        || lmValuesLower.find(v => v.low.includes(sLower))
      if (m) hit = m.raw
    }
    if (hit) {
      lm.serviceValues[svc] = hit
    } else {
      // 跑过没找到 → 标记给 UI 显示"未匹配"提示,跟"还没跑"区分开
      if (!lm.serviceMatchTried) lm.serviceMatchTried = {}
      lm.serviceMatchTried[svc] = true
    }
  }
}

async function onEnvLabelKeyChanged(envID: string, newKey: string) {
  const lm = getLokiMapping(envID)
  lm.envLabelKey = newKey
  lm.envValue = ''
  lm.serviceMatchTried = {} // 切 envLabelKey 后重新匹配,清掉老 tried 标记
  await loadEnvLabelValues(envID)
  autoMatchLokiMapping(envID)
}
async function onServiceLabelKeyChanged(envID: string, newKey: string) {
  const lm = getLokiMapping(envID)
  lm.serviceLabelKey = newKey
  lm.serviceValues = {}
  lm.serviceMatchTried = {}
  await loadServiceLabelValues(envID)
  autoMatchLokiMapping(envID)
}

// ── Step 7 数据层:"从配置中心读取" 自动识别 ────────────────────────
// 流程:
//  1. 拿 Step 5 挑的 envNamespaces + serviceConfigSel + serviceConfigGroup,构造要拉的 dataId 列表
//  2. 串行(避免并发轰炸配置中心) 调 fetchConfigContent 取原文
//  3. js-yaml 解析 / properties 解析;找顶级 key 匹 redis/mysql/mongodb/... 配置块
//  4. 命中则:enabledDataStores[type] = true、dsAutoFilled[type] = true,
//     toolInputs[ds:<type>:<env>:<field>] 填上从 yaml 抽出来的 url/dsn/...
// 没命中的保留原状(不覆盖用户已手填的字段)。

const dsImportStatus = ref<'idle' | 'loading' | 'ok' | 'error'>('idle')
const dsImportStats = reactive<{ scanned: number; matched: number }>({ scanned: 0, matched: 0 })
const dsAutoFilled = reactive<Record<string, boolean>>({}) // dsType → 是否本次自动识别过

// 新 Step 7 数据结构:env → service → dsKey → { fieldKey: value }
// 每个服务拉回来的 yaml 单独识别、单独存,UI 也按 env → service → ds 分层展示。
// 用户可改字段值,yaml 生成时从 scannedDS 推导 data_stores。
type DSFieldMap = Record<string, string>
type DSByKey = Record<string, DSFieldMap>
type DSByService = Record<string, DSByKey>
const scannedDS = reactive<Record<string, DSByService>>((saved?.scannedDS as Record<string, DSByService>) ?? {})

// 每个 (env, service) 的扫描状态 —— 让 UI 可以完整展示矩阵,缺失项明确标原因:
//   'ok'      拉取成功且识别到至少一个数据层(scannedDS 有内容)
//   'empty'   拉取成功但 yaml 里没匹到任何 redis/mysql/...(服务可能不用数据层)
//   'skipped' 没挑 dataId 或 env 未预加载 —— 需要用户回 Step 5 补全
//   'error'   拉取 / 解析失败 —— 详情进日志
interface DSScanState { status: 'ok' | 'empty' | 'skipped' | 'error'; reason?: string }
// 一次性迁移:旧版本 nacos 批拉对"未分配源"和"挂在副源"的服务都笼统报"未映射 dataId",
// 这些 stale 状态会跨会话留在 localStorage 里。新版本对未分配源 / 跨源服务给的 reason 不一样,
// 加载时按特征字符串清掉它们,让用户进 Step 6 看到的是新逻辑跑出来的状态(或新触发后的结果)。
const dsScanState = reactive<Record<string, DSScanState>>(
  (() => {
    const src = (saved?.dsScanState as Record<string, DSScanState>) ?? {}
    const out: Record<string, DSScanState> = {}
    const obsoleteReasons = [
      '未映射 dataId,回 Step 5 为此服务挑一条',
      '挂在', // "挂在 X 源,自动扫只针对 Y 源" 系列
    ]
    for (const [k, v] of Object.entries(src)) {
      if (!v || typeof v !== 'object') continue
      if (v.status === 'skipped' && obsoleteReasons.some(r => (v.reason || '').includes(r))) continue
      out[k] = v
    }
    return out
  })(),
)
function scanStateKey(envID: string, svc: string): string { return `${envID}::${svc}` }
function scanStateOf(envID: string, svc: string): DSScanState | undefined {
  return dsScanState[scanStateKey(envID, svc)]
}

// 删掉某个 (env, service) 下识别出的某类数据层(用户手动:"这个我不要了")。
// 不改 scanState —— 用户主观删不算"没读取到",下一步校验仍视该 (env, svc) 通过。
function removeScannedDS(envID: string, svc: string, dsKey: string) {
  if (scannedDS[envID]?.[svc]?.[dsKey]) {
    delete scannedDS[envID][svc][dsKey]
  }
  delete dsProbeResults[probeKey(envID, svc, dsKey)]
  // 同步 enabledDataStores —— 删掉的可能是该 type 的最后一条,enabledDataStores 得跟着关。
  recomputeEnabledDataStoresFromScanned()
}

// 按当前 scannedDS 实际还有哪些数据层条目,实时派生 enabledDataStores。
// scannedDS 是用户在 Step 6 见到的真相(添/删都直接改它),enabledDataStores 是
// "这个 type 启用了"的派生结论。emit yaml / 删组件时调一次,保证两边永远一致,
// 避免"已删除但 skill 还在白名单"或反过来的撕裂。
function recomputeEnabledDataStoresFromScanned() {
  const live = new Set<string>()
  for (const envID of Object.keys(scannedDS)) {
    for (const svc of Object.keys(scannedDS[envID] || {})) {
      for (const dsKey of Object.keys(scannedDS[envID]?.[svc] || {})) {
        if (Object.keys(scannedDS[envID]?.[svc]?.[dsKey] || {}).length > 0) {
          live.add(dsKey)
        }
      }
    }
  }
  for (const k of dataStoreOptions) {
    enabledDataStores[k] = live.has(k)
  }
}

// ── 数据层连通性测试 ───────────────────────────────────────────────────
// per (env, svc, dsKey) 一个测试结果。idle/loading/ok/fail 四种状态。
// 不进 localStorage —— 网络状态会变,缓存意义不大,重开重测。
interface DSProbeState {
  status: 'idle' | 'loading' | 'ok' | 'fail'
  latency?: string
  detail?: string
  error?: string
}
const dsProbeResults = reactive<Record<string, DSProbeState>>({})
function probeKey(envID: string, svc: string, dsKey: string): string {
  return `${envID}::${svc}::${dsKey}`
}
async function probeOneDS(envID: string, svc: string, dsKey: string) {
  if (!isDesktop()) {
    toast.error('连通性测试只在桌面 app 可用')
    return
  }
  const fields = scannedDS[envID]?.[svc]?.[dsKey]
  if (!fields || Object.keys(fields).length === 0) {
    toast.error('该数据层无字段可测')
    return
  }
  const k = probeKey(envID, svc, dsKey)
  dsProbeResults[k] = { status: 'loading' }
  try {
    const r = await probeDataStore({ type: dsKey, fields: { ...fields } })
    if (r.ok) {
      dsProbeResults[k] = { status: 'ok', latency: r.latency, detail: r.detail }
      pushLog('cchub', 'info',
        `[${envID}/${svc}] ${dsKey} 连通性 OK (${r.latency || ''}) ${r.detail || ''}`,
        { envID, svc, dsKey })
    } else {
      dsProbeResults[k] = { status: 'fail', error: r.error || '未知错误' }
      pushLog('cchub', 'warn',
        `[${envID}/${svc}] ${dsKey} 连通性失败: ${r.error || ''}`,
        { envID, svc, dsKey })
    }
  } catch (e: any) {
    const msg = String(e?.message || e)
    dsProbeResults[k] = { status: 'fail', error: msg }
    pushLog('cchub', 'error', `[${envID}/${svc}] ${dsKey} 测试异常: ${msg}`, { envID, svc, dsKey })
  }
}
// 一键测当前 env 下所有识别到的数据层(串行,80ms 间隔避免限流)。
// per-env 开关防重入 —— 用户狂点按钮不会跑出 N 倍并发请求。
const probingByEnv = reactive<Record<string, boolean>>({})
async function probeAllForEnv(envID: string) {
  if (probingByEnv[envID]) return
  const svcs = scannedDS[envID]
  if (!svcs) return
  // 清掉本 env 范围内的旧连通性结果(其他 env 不动),不然 ✓/✗ 跟当前测试混在一起难分辨
  const prefix = `${envID}::`
  for (const k of Object.keys(dsProbeResults)) {
    if (k.startsWith(prefix)) delete dsProbeResults[k]
  }
  probingByEnv[envID] = true
  try {
    for (const svc of Object.keys(svcs).sort()) {
      for (const dsKey of Object.keys(svcs[svc] || {}).sort()) {
        await probeOneDS(envID, svc, dsKey)
        await new Promise(r => setTimeout(r, 80))
      }
    }
  } finally {
    probingByEnv[envID] = false
  }
}

// UI helper:DS_TOOL_SPECS 查 spec
function dsSpecByKey(key: string) {
  return DS_TOOL_SPECS.find(s => s.key === key)
}
function dsFieldIsSecret(dsKey: string, fKey: string): boolean {
  const spec = dsSpecByKey(dsKey)
  if (!spec) return false
  return !!spec.fields.find(f => f.key === fKey && f.secret)
}
function dsFieldLabel(dsKey: string, fKey: string): string {
  const spec = dsSpecByKey(dsKey)
  if (!spec) return fKey
  const f = spec.fields.find(ff => ff.key === fKey)
  return f?.label || fKey
}
function dsLabel(dsKey: string): string {
  return dsSpecByKey(dsKey)?.label || dsKey
}

// 能否触发自动导入 = Step 5 至少有一条服务能扫:
//   - nacos/apollo/consul:在 serviceConfigSel 里挑了 dataId,或
//   - kuboard:在 kuboardSvcMap 里填齐了 cluster/namespace/configmap
const canAutoImportDS = computed<boolean>(() => {
  if (!isDesktop()) return false
  for (const k of Object.keys(serviceConfigSel)) {
    if ((serviceConfigSel[k] || '').trim()) return true
  }
  for (const k of Object.keys(kuboardSvcMap)) {
    const loc = kuboardSvcMap[k]
    if (loc?.cluster && loc?.namespace && loc?.configmap) return true
  }
  return false
})

// 数据层配置块识别规则:yaml 里常见的 key → DS_TOOL_SPECS 中某个 spec.key
// 每条规则含:匹 key 正则、字段抽取器(把 yaml 子对象转成 ds:<type>:<env>:<field> 的值)
interface DSMatcher {
  dsKey: string // spec.key
  // matchYAML 接受解析后的 js-yaml 对象(object 根),返回识别到的字段 map(若该 ds 未配置则返 null)
  matchYAML: (root: any) => Record<string, string> | null
}

// 针对常见 Go / Java / Hyperf / Spring 应用配置的结构,启发式识别。多份 yaml 会合并(取第一条匹上的)。
// 每个 field key 对齐 DS_TOOL_SPECS 里 spec.fields[i].key。
//
// 关键点:yaml 里很多组件有"connection 名"这一层(如 `redis.default.host` / `databases.primary.host`),
// 所以 matcher 拿到"组件根"后要再 pickConnection(block, [host, url...]) —— 这个 helper 支持
// block 直接带 host 字段,或 block 下某个 child 对象带 host 字段。
const DS_MATCHERS: DSMatcher[] = [
  {
    dsKey: 'redis',
    matchYAML: (r) => {
      const block = findKey(r, ['redis', 'Redis', 'REDIS'])
      const c = pickConnection(block, ['host', 'url', 'address', 'uri'])
      if (!c) return null
      const host = str(c.host) || str(c.address)
      const port = str(c.port) || extractPort(str(c.address))
      const pass = str(c.password) || str(c.auth)
      const db = str(c.db) || str(c.database)
      const explicit = str(c.url) || str(c.uri)
      if (explicit) return { url: explicit }
      if (!host) return null
      return { url: `redis://${pass ? ':' + pass + '@' : ''}${host}${port ? ':' + port : ''}${db ? '/' + db : ''}` }
    },
  },
  {
    dsKey: 'mongodb',
    matchYAML: (r) => {
      const block = findKey(r, ['mongodb', 'mongo', 'MongoDB'])
      const c = pickConnection(block, ['uri', 'url', 'dsn', 'host'])
      if (!c) return null
      const uri = str(c.uri) || str(c.url) || str(c.dsn)
      if (uri) return { uri }
      // 组合 host+port
      const host = str(c.host), port = str(c.port), user = str(c.user) || str(c.username), pass = str(c.password), database = str(c.database) || str(c.db)
      if (!host) return null
      return { uri: `mongodb://${user ? user + (pass ? ':' + pass : '') + '@' : ''}${host}${port ? ':' + port : ''}${database ? '/' + database : ''}` }
    },
  },
  {
    dsKey: 'mysql',
    matchYAML: (r) => {
      // 三类常见布局:mysql.default.* / databases.primary(driver=mysql) / datasource/db
      let c: any = null
      // 1) 直接 mysql key
      const mysqlBlock = findKey(r, ['mysql', 'MySQL'])
      c = pickConnection(mysqlBlock, ['host', 'dsn', 'url', 'uri'])
      // 2) databases.<conn>.driver == mysql
      if (!c) {
        const dbBlock = findKey(r, ['databases', 'datasource', 'database', 'db'])
        if (dbBlock && typeof dbBlock === 'object') {
          for (const v of Object.values(dbBlock)) {
            if (!v || typeof v !== 'object') continue
            const driver = str((v as any).driver || (v as any).dialect).toLowerCase()
            if (driver === 'mysql' || driver.includes('mysql')) { c = v; break }
            // 或者直接有 host/dsn 且没显式声明 driver(常见简化写法)
            if (!driver && (str((v as any).host) || str((v as any).dsn))) { c = v; break }
          }
          if (!c) c = pickConnection(dbBlock, ['host', 'dsn', 'url'])
        }
      }
      if (!c) return null
      const dsn = str(c.dsn) || str(c.uri) || str(c.url)
      if (dsn && /mysql|tcp\(.*\)/i.test(dsn)) return { dsn }
      const host = str(c.host), port = str(c.port), user = str(c.user) || str(c.username), pass = str(c.password), database = str(c.database) || str(c.name)
      if (!host) return null
      return { dsn: `${user || ''}${pass ? ':' + pass : ''}@tcp(${host}${port ? ':' + port : '3306'})/${database || ''}` }
    },
  },
  {
    dsKey: 'postgresql',
    matchYAML: (r) => {
      let c: any = null
      const pgBlock = findKey(r, ['postgres', 'postgresql', 'pg'])
      c = pickConnection(pgBlock, ['host', 'dsn', 'url', 'uri'])
      if (!c) {
        // databases.<conn>.driver = postgres
        const dbBlock = findKey(r, ['databases', 'datasource', 'database'])
        if (dbBlock && typeof dbBlock === 'object') {
          for (const v of Object.values(dbBlock)) {
            if (!v || typeof v !== 'object') continue
            const driver = str((v as any).driver || (v as any).dialect).toLowerCase()
            if (driver === 'postgres' || driver === 'postgresql' || driver === 'pg') { c = v; break }
          }
        }
      }
      if (!c) return null
      const dsn = str(c.dsn) || str(c.uri) || str(c.url)
      if (dsn) return { dsn }
      const host = str(c.host), port = str(c.port), user = str(c.user) || str(c.username), pass = str(c.password), database = str(c.database) || str(c.name)
      if (!host) return null
      return { dsn: `postgres://${user || ''}${pass ? ':' + pass : ''}@${host}${port ? ':' + port : ''}/${database || ''}` }
    },
  },
  {
    dsKey: 'elasticsearch',
    matchYAML: (r) => {
      const block = findKey(r, ['elasticsearch', 'es'])
      if (!block || typeof block !== 'object') return null
      const c = pickConnection(block, ['url', 'endpoint', 'hosts', 'host'])
      if (!c) return null
      const url = str(c.url) || str(c.endpoint) || (Array.isArray(c.hosts) && c.hosts[0]) || str(c.host)
      if (!url) return null
      return {
        url,
        user: str(c.username) || str(c.user) || '',
        pass: str(c.password) || '',
      }
    },
  },
  {
    dsKey: 'kafka',
    matchYAML: (r) => {
      const block = findKey(r, ['kafka'])
      if (!block || typeof block !== 'object') return null
      const c = pickConnection(block, ['brokers', 'servers', 'bootstrap_servers', 'bootstrapServers'])
      if (!c) return null
      const brokers = (Array.isArray(c.brokers) && c.brokers.join(',')) || str(c.brokers) ||
                      (Array.isArray(c.servers) && c.servers.join(',')) || str(c.servers) ||
                      str(c.bootstrap_servers) || str(c.bootstrapServers)
      if (!brokers) return null
      return {
        brokers,
        user: str(c.username) || str(c.sasl_username) || '',
        pass: str(c.password) || str(c.sasl_password) || '',
      }
    },
  },
  {
    dsKey: 'rocketmq',
    matchYAML: (r) => {
      const block = findKey(r, ['rocketmq', 'rocket_mq', 'rocketMQ'])
      const c = pickConnection(block, ['namesrv', 'name_srv', 'nameserver', 'nameServer', 'servers', 'host'])
      if (!c) return null
      const namesrv = str(c.namesrv) || str(c.name_srv) || str(c.nameserver) || str(c.nameServer) || str(c.servers) ||
                      (str(c.host) ? `${c.host}${c.port ? ':' + c.port : ''}` : '')
      if (!namesrv) return null
      return { namesrv }
    },
  },
  {
    dsKey: 'rabbitmq',
    matchYAML: (r) => {
      const block = findKey(r, ['rabbitmq', 'amqp'])
      const c = pickConnection(block, ['url', 'uri', 'dsn', 'host'])
      if (!c) return null
      const url = str(c.url) || str(c.uri) || str(c.dsn)
      if (url) return { url }
      const host = str(c.host), port = str(c.port), user = str(c.user) || str(c.username), pass = str(c.password), vhost = str(c.vhost)
      if (!host) return null
      return { url: `amqp://${user || ''}${pass ? ':' + pass : ''}@${host}${port ? ':' + port : ''}${vhost ? '/' + vhost : ''}` }
    },
  },
  {
    dsKey: 'clickhouse',
    matchYAML: (r) => {
      const block = findKey(r, ['clickhouse', 'ck', 'ClickHouse'])
      const c = pickConnection(block, ['url', 'host', 'addr'])
      if (!c) return null
      const url = str(c.url) || str(c.addr) || str(c.host)
      if (!url) return null
      return {
        url,
        user: str(c.user) || str(c.username) || '',
        pass: str(c.password) || '',
      }
    },
  },
]

// 深度在第 1 / 2 / 3 层找 key(配置 yaml 常见结构如 `spring.redis` 或 `databases.redis`)
function findKey(obj: any, keys: string[], depth: number = 3): any {
  if (!obj || typeof obj !== 'object') return null
  for (const k of keys) {
    if (obj[k] !== undefined) return obj[k]
  }
  if (depth <= 1) return null
  for (const v of Object.values(obj)) {
    const r = findKey(v, keys, depth - 1)
    if (r) return r
  }
  return null
}

// pickConnection 处理"组件根下可能还嵌一层 connection 名"的情况。
// 例 redis 根 = { default: { host, port }, cache: {...} },我们挑第一个包含目标字段的 child;
// 如果组件根自己就有目标字段(如 redis.host 平铺),直接返回根。
function pickConnection(block: any, targetFields: string[]): any {
  if (!block || typeof block !== 'object' || Array.isArray(block)) return null
  // 根自带任一目标字段
  for (const f of targetFields) {
    if (block[f] !== undefined && block[f] !== null) return block
  }
  // 扫 children:任一 child 对象带目标字段就算它
  for (const v of Object.values(block)) {
    if (!v || typeof v !== 'object' || Array.isArray(v)) continue
    for (const f of targetFields) {
      if ((v as any)[f] !== undefined && (v as any)[f] !== null) return v
    }
  }
  return null
}

function str(v: any): string {
  if (v === undefined || v === null) return ''
  return String(v).trim()
}
function extractPort(addr: string): string {
  const m = addr.match(/:(\d+)$/)
  return m ? m[1] : ''
}

async function autoImportDataStores() {
  if (!canAutoImportDS.value) {
    toast.error('先在 Step 5 完成配置源扫描 + 服务 dataId 映射')
    return
  }
  dsImportStatus.value = 'loading'
  dsImportStats.scanned = 0
  dsImportStats.matched = 0
  for (const k of Object.keys(dsAutoFilled)) delete dsAutoFilled[k]
  for (const k of Object.keys(dsScanState)) delete dsScanState[k]
  // 旧的连通性结果一并清空 —— 重新拉完字段可能变了,旧 ✓/✗ 不该残留
  for (const k of Object.keys(dsProbeResults)) delete dsProbeResults[k]

  let scanned = 0
  const matchedSet = new Set<string>()
  try {
    // 保证 scannedDS 对每个 env 都存在,UI 遍历 environments × allServiceNames 时不 crash
    for (const env of environments) {
      if (!env.id) continue
      if (!scannedDS[env.id]) scannedDS[env.id] = {}
    }

    // 按凭证去重分组:所有用同一组 (type/addr/user/pass/token/app_id) 的 env 合并一次 batch,
    // 后端只 connect 一次(probe + login 只 1 次)。典型场景:5 服务 × 2 环境共用同一个 Nacos →
    // 一次 batch 就能拉完 10 个 config,login 只 1 次。
    type BatchItem = { key: string; env: string; svc: string; dataId: string; group: string; nsID: string }
    const groups = new Map<string, { payload: ReturnType<typeof buildPreloadPayload>; items: BatchItem[] }>()

    // 先做 per-env 的前置校验(凭证 / namespace),然后为合法的 (env, svc) 条目按凭证分组
    const remotePreloadTypes = new Set(['nacos', 'apollo', 'consul'])
    for (const env of environments) {
      if (!env.id) continue
      const payload = buildPreloadPayload(env.id)
      const nsID = envNamespaces[env.id]
      // 主源不是 nacos/apollo/consul(比如 kuboard / env-vars / none) → 这一段不处理,
      // 让后面的 kuboard 专项 pass 接管;只把"挂在 nacos/apollo/consul 但当前主源不支持"
      // 的服务标 skipped(其实多源 + 主源是 kuboard 时这种组合罕见)。
      if (!remotePreloadTypes.has(payload.type)) continue
      if (!payload.valid) {
        const reason = `凭证不完整(缺: ${payload.missing.join(', ')})`
        pushLog('cchub', 'warn', `[${env.id}] ${reason},跳过本 env 的 nacos 类批拉`, { envID: env.id })
        for (const svc of allServiceNames.value) {
          if (getServiceSource(svc) !== payload.type) continue
          dsScanState[scanStateKey(env.id, svc)] = { status: 'skipped', reason }
        }
        continue
      }
      if (nsID === undefined) {
        const reason = '未选 namespace,先回 Step 5 扫一次'
        pushLog('cchub', 'warn', `[${env.id}] ${reason}`, { envID: env.id })
        for (const svc of allServiceNames.value) {
          if (getServiceSource(svc) !== payload.type) continue
          dsScanState[scanStateKey(env.id, svc)] = { status: 'skipped', reason }
        }
        continue
      }
      const credKey = [
        payload.type, payload.addr, payload.username, payload.password,
        payload.token, payload.app_id,
      ].join('\x1f')
      if (!groups.has(credKey)) {
        groups.set(credKey, { payload, items: [] })
      }
      const g = groups.get(credKey)!
      // 本批 batch 走的是当前主源(buildPreloadPayload 用 configCenterType.value)。
      // 多源场景下,某些服务可能走 kuboard / env-vars 副源,它们不能用 nacos 的 dataId 拉,
      // 在这里只能"按源跳过",并标记原因让用户在 Step 6 仍能继续;数据层最终值由副源
      // 自己的扫描或部署时手填提供。
      const primarySrcType = payload.type
      for (const svc of allServiceNames.value) {
        const svcSource = getServiceSource(svc)
        // 服务没分配给任何源(多源模式下,用户没在任一源的 checklist 里勾过它)→
        // 这里只能 skip 并提示用户回 Step 5 勾;不再误报"未映射 dataId"。
        if (!svcSource) {
          dsScanState[scanStateKey(env.id, svc)] = {
            status: 'skipped',
            reason: '未分配源,回 Step 5 在某个源面板的"本环境包含的服务"里勾上',
          }
          continue
        }
        if (svcSource !== primarySrcType) {
          dsScanState[scanStateKey(env.id, svc)] = {
            status: 'skipped',
            reason: `挂在 ${svcSource} 源,自动扫只针对 ${primarySrcType} 源`,
          }
          continue
        }
        const dataId = (serviceConfigSel[svcKey(env.id, svc)] || '').trim()
        if (!dataId) {
          dsScanState[scanStateKey(env.id, svc)] = {
            status: 'skipped',
            reason: '未映射 dataId,回 Step 5 为此服务挑一条',
          }
          pushLog('cchub', 'warn', `[${env.id}/${svc}] 未映射 dataId`, { envID: env.id, svc })
          continue
        }
        const group = (serviceConfigGroup[svcKey(env.id, svc)] || '').trim()
        g.items.push({ key: `${env.id}::${svc}`, env: env.id, svc, dataId, group, nsID })
      }
    }

    // 每组发 1 次 batch RPC(后端仅 1 次 probe + login,共享 token 拉完组内全部 item)
    const groupCount = groups.size
    let gi = 0
    for (const group of groups.values()) {
      gi++
      if (group.items.length === 0) continue
      const envSet = new Set(group.items.map(it => it.env))
      pushLog('cchub', 'info',
        `批量组 ${gi}/${groupCount}: 覆盖 envs=[${Array.from(envSet).join(',')}] 共 ${group.items.length} 条(复用一次 probe+login)`)
      let batch: Awaited<ReturnType<typeof fetchConfigContentBatch>>
      try {
        batch = await fetchConfigContentBatch({
          type: group.payload.type as 'nacos' | 'apollo' | 'consul',
          addr: group.payload.addr,
          username: group.payload.username,
          password: group.payload.password,
          token: group.payload.token,
          items: group.items.map(it => ({
            key: it.key,
            namespace: it.nsID,
            group: it.group,
            data_id: it.dataId,
            app_id: group.payload.app_id,
          })),
        })
      } catch (e: any) {
        // 整批 RPC 失败(probe/login 失败) → 本组 items 全标 error
        const msg = String(e?.message || e)
        pushLog('cchub', 'error', `批量组 ${gi} 拉取失败(probe/login 问题): ${msg}`)
        for (const it of group.items) {
          dsScanState[scanStateKey(it.env, it.svc)] = { status: 'error', reason: '批量拉取失败,详见日志' }
        }
        continue
      }
      for (const n of (batch.notes || [])) pushLog('cchub', 'info', n)

      // 按 key 逐条处理
      const byKey = new Map(group.items.map(it => [it.key, it]))
      for (const itemResult of batch.items) {
        const info = byKey.get(itemResult.key)
        if (!info) continue
        const stateKey = scanStateKey(info.env, info.svc)
        if (!itemResult.ok || !itemResult.result) {
          dsScanState[stateKey] = { status: 'error', reason: '拉取失败,详见日志' }
          pushLog('cchub', 'error',
            `[${info.env}/${info.svc}] 拉 dataId=${info.dataId} 失败: ${itemResult.error || '(未知错误)'}`,
            { envID: info.env, svc: info.svc })
          continue
        }
        scanned++
        const r = itemResult.result
        for (const n of (r.notes || [])) {
          pushLog('cchub', 'info', `[${info.env}/${info.svc}] ${n}`, { envID: info.env, svc: info.svc })
        }
        const root = parseConfigContent(r.content, r.format)
        if (!root) {
          const reason = `解析失败(format=${r.format || '?'})`
          dsScanState[stateKey] = { status: 'error', reason }
          pushLog('cchub', 'warn',
            `[${info.env}/${info.svc}] ${reason},内容开头: ${String(r.content || '').slice(0, 80)}`,
            { envID: info.env, svc: info.svc })
          continue
        }
        const hits: string[] = []
        if (!scannedDS[info.env]) scannedDS[info.env] = {}
        scannedDS[info.env][info.svc] = {}
        for (const m of DS_MATCHERS) {
          const hit = m.matchYAML(root)
          if (!hit) continue
          hits.push(m.dsKey)
          // 命中数据层 → 同步把 enabledDataStores[dsKey] = true。否则 deriveSkillsWhitelist
          // 看到 enabledDataStores.redis 仍 false,就不会把 redis-runtime-query push 进白名单,
          // 工作区里就生成不出这个 skill —— 用户看到的"redis/mongodb/kafka 被跳过"就是这个 bug。
          enabledDataStores[m.dsKey] = true
          dsAutoFilled[m.dsKey] = true
          matchedSet.add(`${info.env}:${m.dsKey}`)
          scannedDS[info.env][info.svc][m.dsKey] = { ...hit }
          pushLog('cchub', 'info',
            `[${info.env}/${info.svc}] 识别数据层 ${m.dsKey}: ${Object.keys(hit).join(',')}`,
            { envID: info.env, svc: info.svc, dsKey: m.dsKey })
        }
        if (hits.length === 0) {
          const topKeys = (root && typeof root === 'object') ? Object.keys(root).slice(0, 15).join(',') : '(非对象)'
          dsScanState[stateKey] = { status: 'empty', reason: `yaml 里没匹到数据层(顶级 key: ${topKeys})` }
          pushLog('cchub', 'warn', `[${info.env}/${info.svc}] 未识别到任何数据层(顶级 key:${topKeys})`,
            { envID: info.env, svc: info.svc })
        } else {
          dsScanState[stateKey] = { status: 'ok' }
        }
      }
    }
    // ── Kuboard 源:per-env 批拉 cm.data,跟 nacos 同样跑 DS_MATCHERS ──
    // 单独一段是因为 kuboard 凭证 / locator 数据结构跟 nacos 不一样:
    //   - 凭证:url + access_key 或 username+password(per env);
    //   - locator:cluster/namespace/configmap(per service,from kuboardSvcMap);
    //   - 内容:N 个 data 字段拼成 multi-doc YAML(后端 KuboardFetchConfigMaps).
    if (enabledSourceTypes['kuboard']) {
      const isPrimaryKb = activeSourceTypes.value[0] === 'kuboard'
      const getKbCred = (envID: string, field: string): string => {
        if (isPrimaryKb) return (ccCredInputs[ccKeyFor('kuboard', envID, field)] || '').trim()
        return ((sourceCreds['kuboard']?.creds?.[envID]?.[field]) || '').trim()
      }
      for (const env of environments) {
        if (!env.id) continue
        // 收集本 env 挂在 kuboard 源的所有服务,凑成 batch items
        const kbItems: { key: string; env: string; svc: string; cluster: string; namespace: string; configmap: string }[] = []
        for (const svc of allServiceNames.value) {
          if (getServiceSource(svc) !== 'kuboard') continue
          const loc = kuboardSvcMap[svcKey(env.id, svc)]
          if (!loc?.cluster || !loc?.namespace || !loc?.configmap) {
            dsScanState[scanStateKey(env.id, svc)] = {
              status: 'skipped',
              reason: '未挑齐 cluster/namespace/configmap,回 Step 5 补全',
            }
            continue
          }
          kbItems.push({
            key: `${env.id}::${svc}`,
            env: env.id, svc,
            cluster: loc.cluster, namespace: loc.namespace, configmap: loc.configmap,
          })
        }
        if (kbItems.length === 0) continue
        const url = getKbCred(env.id, 'url')
        const accessKey = getKbCred(env.id, 'access_key')
        const username = getKbCred(env.id, 'username')
        const password = getKbCred(env.id, 'password')
        if (!url || (!accessKey && (!username || !password))) {
          for (const it of kbItems) {
            dsScanState[scanStateKey(it.env, it.svc)] = { status: 'skipped', reason: 'kuboard 凭证不完整,回 Step 5 补' }
          }
          continue
        }
        pushLog('cchub', 'info', `[${env.id}] kuboard 批拉 ${kbItems.length} 条 cm`)
        let kbBatch: KuboardFetchBatchResult
        try {
          kbBatch = await kuboardFetchConfigMaps({
            url, access_key: accessKey, username, password,
            items: kbItems.map(it => ({ key: it.key, cluster: it.cluster, namespace: it.namespace, configmap: it.configmap })),
          })
        } catch (e: any) {
          const msg = String(e?.message || e)
          pushLog('cchub', 'error', `[${env.id}] kuboard 批拉失败: ${msg}`)
          for (const it of kbItems) {
            dsScanState[scanStateKey(it.env, it.svc)] = { status: 'error', reason: 'kuboard 批拉失败,详见日志' }
          }
          continue
        }
        for (const n of (kbBatch.notes || [])) pushLog('cchub', 'info', n)
        const byKey = new Map(kbItems.map(it => [it.key, it]))
        for (const r of kbBatch.items) {
          const info = byKey.get(r.key)
          if (!info) continue
          const stateKey = scanStateKey(info.env, info.svc)
          if (!r.ok) {
            dsScanState[stateKey] = { status: 'error', reason: r.error || '拉取失败' }
            pushLog('cchub', 'error', `[${info.env}/${info.svc}] kuboard cm 拉取失败: ${r.error || '(未知)'}`,
              { envID: info.env, svc: info.svc })
            continue
          }
          scanned++
          if (!r.content) {
            dsScanState[stateKey] = { status: 'empty', reason: 'configmap 是空的' }
            continue
          }
          // 诊断:dump cm.data 字段名列表(从 JSON 平铺内容抽 keys)+ 重塑后的顶级组件
          // 字段名,匹不到时方便用户/我回看 cm 实际形态。
          let dataKeys: string[] = []
          try { dataKeys = Object.keys(JSON.parse(r.content || '{}')) } catch { /* skip */ }
          pushLog('cchub', 'info',
            `[${info.env}/${info.svc}] kuboard cm 拉到 ${dataKeys.length} 个 data 字段: ${dataKeys.slice(0, 30).join(', ')}${dataKeys.length > 30 ? '...' : ''}`,
            { envID: info.env, svc: info.svc })
          const root = parseConfigContent(r.content, r.format)
          if (!root) {
            dsScanState[stateKey] = { status: 'error', reason: `解析 configmap 失败(format=${r.format || '?'})` }
            continue
          }
          const hits: string[] = []
          if (!scannedDS[info.env]) scannedDS[info.env] = {}
          scannedDS[info.env][info.svc] = {}
          for (const m of DS_MATCHERS) {
            const hit = m.matchYAML(root)
            if (!hit) continue
            hits.push(m.dsKey)
            // 跟 nacos pass 一样:命中数据层同步打开 enabledDataStores 标记,
            // 让 deriveSkillsWhitelist 能把 <type>-runtime-query push 进白名单。
            enabledDataStores[m.dsKey] = true
            dsAutoFilled[m.dsKey] = true
            matchedSet.add(`${info.env}:${m.dsKey}`)
            scannedDS[info.env][info.svc][m.dsKey] = { ...hit }
            pushLog('cchub', 'info',
              `[${info.env}/${info.svc}] kuboard 识别数据层 ${m.dsKey}: ${Object.keys(hit).join(',')}`,
              { envID: info.env, svc: info.svc, dsKey: m.dsKey })
          }
          if (hits.length === 0) {
            const topKeys = (root && typeof root === 'object') ? Object.keys(root).slice(0, 15).join(',') : '(非对象)'
            dsScanState[stateKey] = { status: 'empty', reason: `cm 里没匹到数据层(顶级 key:${topKeys})` }
          } else {
            dsScanState[stateKey] = { status: 'ok' }
          }
        }
      }
    }

    dsImportStats.scanned = scanned
    dsImportStats.matched = matchedSet.size
    dsImportStatus.value = 'ok'
    toast.success(`扫描 ${scanned} 条配置,识别 ${matchedSet.size} 个 (env, 数据层) 组合`)
  } catch (e: any) {
    dsImportStatus.value = 'error'
    toast.error(`自动识别失败,详见左侧「日志」`)
    pushLog('cchub', 'error', `自动识别异常: ${String(e?.message || e)}`)
    return
  }
  // 自动对识别出的所有数据层组件跑一遍连通性测试 —— 用户不用再手点
  pushLog('cchub', 'info', '识别完成,开始自动跑连通性测试...')
  for (const env of environments) {
    if (!env.id) continue
    if (!scannedDS[env.id]) continue
    await probeAllForEnv(env.id)
  }
  pushLog('cchub', 'info', '连通性测试完成')
}

// 把后端返回的原文按 format 解析成 object;yaml/properties/json 都支持
// yaml-multi:Kuboard configmap 把多个 data 字段拼成 multi-doc YAML(--- 分隔),
// 这里 loadAll 拿到 N 个根对象后浅合并成一个,DS_MATCHERS 直接吃。
function parseConfigContent(content: string, format?: string): any {
  const fmt = (format || '').toLowerCase()
  try {
    if (fmt === 'json') return JSON.parse(content)
    if (fmt === 'properties') return parseProperties(content)
    if (fmt === 'k8s-env-flat') {
      // K8s ConfigMap 的 data 是平铺 KV(典型 Laravel/Spring .env 用法,字段名即 env 变量名)。
      // 把扁平 KEY=VALUE 重塑成 {redis:{host,port,password,...}, mysql:{...}, ...} 让现有
      // DS_MATCHERS(用 findKey + pickConnection)能找到。原 flat key 仍保留以备其他规则查找。
      let flat: Record<string, string> = {}
      try { flat = JSON.parse(content) } catch { flat = {} }
      return envFlatToRoot(flat)
    }
    if (fmt === 'yaml-multi') {
      // ConfigMap 各 data 字段拼成的多 doc:每段可能是 yaml / json / properties / 其他。
      // yaml.load 对 properties 形会得到 scalar 字符串;但反过来 parseProperties 对 URL /
      // base64 / 证书 等任意文本也会强行按 ":" / "=" 切出假 key(典型坑:base64 / https 顶级 key)。
      // 所以要严格判断:只在内容明显是 properties(有合理比例的 IDENTIFIER=VALUE 或
      // IDENTIFIER:VALUE 行)时才走 properties 兜底。
      const merged: Record<string, any> = {}
      const segments = content.split(/^---\s*$/m)
      for (const seg of segments) {
        const text = seg.trim()
        if (!text) continue
        let parsed: any = null
        try { parsed = yaml.load(text) } catch { parsed = null }
        if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
          if (looksLikeProperties(text)) {
            try { parsed = parseProperties(text) } catch { parsed = null }
          }
        }
        if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
          Object.assign(merged, parsed)
        }
      }
      return merged
    }
    // 默认按 yaml 试 —— js-yaml 兼容大部分 scalar 单值的 properties 也能勉强吃
    return yaml.load(content)
  } catch {
    // 最后降级:按 properties 试一次
    try { return parseProperties(content) } catch { return null }
  }
}

// envFlatToRoot:把 K8s ConfigMap 的扁平 .env 形态(DB_HOST=... / REDIS_PORT=...)
// 重塑成 DS_MATCHERS 能直接匹的嵌套对象 {redis:{...},mysql:{...},mongodb:{...},...}。
// 原始 flat key 仍保留在 root 里,以备未来扩展规则。
//
// 前缀映射(大小写不敏感):
//   REDIS_*                       → redis
//   MONGO_* / MONGODB_*           → mongodb
//   ES_* / ELASTIC_* / ELASTICSEARCH_* → elasticsearch
//   KAFKA_*                       → kafka
//   MYSQL_*                       → mysql
//   PGSQL_* / POSTGRES_* / POSTGRESQL_* → pgsql
//   DB_*  → 由 DB_CONNECTION 决定(mysql / pgsql / sqlite / etc)
//
// 字段名归一化(小写):
//   HOST/HOSTS/SERVER → host  PORT → port  USERNAME/USER → username
//   PASSWORD/PASS/PWD → password  DATABASE/DB/NAME → database
//   URI/URL/DSN → uri/url/dsn  BROKERS/BOOTSTRAP_SERVERS → brokers
function envFlatToRoot(flat: Record<string, string>): Record<string, any> {
  const root: Record<string, any> = { ...flat } // 保留原 flat key

  // 归一化 field 名
  const normField = (s: string): string => {
    const k = s.toLowerCase()
    if (k === 'host' || k === 'hosts' || k === 'server' || k === 'addr' || k === 'address') return 'host'
    if (k === 'port') return 'port'
    if (k === 'username' || k === 'user') return 'username'
    if (k === 'password' || k === 'pass' || k === 'pwd' || k === 'auth') return 'password'
    if (k === 'database' || k === 'db' || k === 'dbname' || k === 'name') return 'database'
    if (k === 'uri') return 'uri'
    if (k === 'url') return 'url'
    if (k === 'dsn') return 'dsn'
    if (k === 'brokers' || k === 'bootstrap_servers' || k === 'bootstrap') return 'brokers'
    if (k === 'index') return 'index'
    if (k === 'sasl_username' || k === 'sasl_user') return 'sasl_username'
    if (k === 'sasl_password' || k === 'sasl_pass') return 'sasl_password'
    return k
  }

  // 把 flat 的 PREFIX_FIELD 归到 root[component][normField] 里。
  const groupBy = (prefixes: string[], component: string) => {
    const block: Record<string, any> = root[component] && typeof root[component] === 'object' && !Array.isArray(root[component]) ? root[component] : {}
    let touched = false
    for (const [k, v] of Object.entries(flat)) {
      if (typeof v !== 'string') continue
      for (const p of prefixes) {
        if (k.toUpperCase().startsWith(p + '_')) {
          const tail = k.substring(p.length + 1)
          const nf = normField(tail)
          // sasl_xxx 拆到 block.sasl.{username|password}
          if (nf === 'sasl_username' || nf === 'sasl_password') {
            if (!block.sasl || typeof block.sasl !== 'object') block.sasl = {}
            block.sasl[nf === 'sasl_username' ? 'username' : 'password'] = v
          } else {
            block[nf] = v
          }
          touched = true
          break
        }
      }
    }
    if (touched) root[component] = block
  }

  groupBy(['REDIS'], 'redis')
  groupBy(['MONGO', 'MONGODB'], 'mongodb')
  groupBy(['ES', 'ELASTIC', 'ELASTICSEARCH'], 'elasticsearch')
  groupBy(['KAFKA'], 'kafka')
  groupBy(['MYSQL'], 'mysql')
  groupBy(['PGSQL', 'POSTGRES', 'POSTGRESQL'], 'pgsql')

  // DB_* 归到 DB_CONNECTION 指明的 driver 下(Laravel 风格)
  const dbConn = (flat['DB_CONNECTION'] || flat['db_connection'] || '').toLowerCase()
  if (dbConn) {
    const dbDriver =
      dbConn === 'mysql' ? 'mysql' :
      (dbConn === 'pgsql' || dbConn === 'postgres' || dbConn === 'postgresql') ? 'pgsql' :
      (dbConn === 'mongodb' || dbConn === 'mongo') ? 'mongodb' :
      ''
    if (dbDriver) {
      const block: Record<string, any> = (root[dbDriver] && typeof root[dbDriver] === 'object' && !Array.isArray(root[dbDriver])) ? root[dbDriver] : {}
      for (const [k, v] of Object.entries(flat)) {
        if (typeof v !== 'string') continue
        if (!k.toUpperCase().startsWith('DB_')) continue
        const tail = k.substring(3)
        if (tail.toUpperCase() === 'CONNECTION') continue
        block[normField(tail)] = v
      }
      root[dbDriver] = block
    }
  }

  return root
}

// 严格判断"这段文本是 properties 风格"。规则:
//   - 排除明显的 URL / data: URI / 证书块开头(避免被强切 key);
//   - 至少 2 条 IDENTIFIER=VALUE 或 IDENTIFIER:VALUE 行,且占非空行 50% 以上;
//   - IDENTIFIER 必须是合法标识符(可含 . _ -),否则视为伪命中。
function looksLikeProperties(text: string): boolean {
  const lines = text.split(/\r?\n/).map(l => l.trim()).filter(l => l && !l.startsWith('#') && !l.startsWith('!'))
  if (lines.length === 0) return false
  // 整段以 URL / data URI / 证书块 / HTML / 单行 base64 开头 → 不是 properties
  const head = lines[0]
  if (/^(https?|ftp|wss?):\/\//i.test(head)) return false
  if (/^data:[a-z]+\//i.test(head)) return false
  if (head.startsWith('-----BEGIN ')) return false
  if (head.startsWith('<')) return false // html/xml
  // 计 properties-style 行数:IDENTIFIER([.\w-]*)\s*[=:]\s*VALUE
  const propRe = /^[a-zA-Z_][\w.\-]*\s*[=:]\s*\S/
  let propCount = 0
  for (const l of lines) {
    if (propRe.test(l)) propCount++
  }
  return propCount >= 2 && propCount / lines.length >= 0.5
}

// 极简 properties 解析:`k.v.x = y` → 嵌套对象 {k: {v: {x: "y"}}}
function parseProperties(text: string): Record<string, any> {
  const out: Record<string, any> = {}
  for (const rawLine of text.split(/\r?\n/)) {
    const line = rawLine.trim()
    if (!line || line.startsWith('#') || line.startsWith('!')) continue
    const m = line.match(/^([^=:]+)[=:]\s*(.*)$/)
    if (!m) continue
    const key = m[1].trim(), val = m[2].trim()
    const segs = key.split('.')
    let cur: Record<string, any> = out
    for (let i = 0; i < segs.length - 1; i++) {
      const s = segs[i]
      if (typeof cur[s] !== 'object' || cur[s] === null) cur[s] = {}
      cur = cur[s]
    }
    cur[segs[segs.length - 1]] = val
  }
  return out
}

// ── Step 7: 输出目标 ──
// (历史上有 embedded 这个 target,后已下线;若 saved draft 里残留 enabledTargets.embedded
//  会被忽略,生成 yaml / 校验都不再考虑它)
const targetOptions = ['openclaw', 'claude-code', 'cursor', 'codex'] as const
const targetDescriptions: Record<string, string> = {
  'openclaw': 'OpenClaw agent（~/.openclaw/workspace/<workspace_name>/，OpenClaw 内选 agent 切换）',
  'claude-code': 'Claude Code 用户级 subagent（~/.claude/agents/<name>.md，@<name> 调用）',
  'cursor': 'Cursor 用户级 Custom Agent（~/.cursor/agents/<name>.md，AI 侧栏选用）',
  'codex': 'OpenAI Codex CLI 用户级 agent（~/.codex/agents/<name>.md，CLI 内 @<name> 调用）',
}
const targetLabels: Record<string, string> = {
  'openclaw': 'OpenClaw',
  'claude-code': 'Claude Code',
  'cursor': 'Cursor IDE',
  'codex': 'Codex CLI',
}
const enabledTargets = reactive<Record<string, boolean>>({
  ...Object.fromEntries(targetOptions.map(k => [k, true])),
  ...(saved?.enabledTargets ?? {}),
})
// 任一目标勾选 / 无目标勾选:Step 1 校验 + 后续步骤按需隐藏字段
const anyTargetSelected = computed(() => targetOptions.some(t => enabledTargets[t]))

// targetDetectedInstalled(t) — 该 target 是否被本机探测到已装。
//   openclaw     → openclawDetectStatus === 'ok'
//   claude-code  → aitoolsResult.claude_code.installed
//   cursor       → aitoolsResult.cursor.installed
//   codex        → aitoolsResult.codex.installed
// 探测还没跑(aitoolsResult / openclawDetectStatus 为初始) → 返回 null(unknown),
// UI 据此显示"扫描中…"而不是"未检测到"。
function targetDetectedInstalled(t: string): boolean | null {
  if (t === 'openclaw') {
    if (openclawDetectStatus.value === 'idle' || openclawDetectStatus.value === 'loading') return null
    return openclawDetectStatus.value === 'ok'
  }
  if (!aitoolsResult.value) return null
  if (t === 'claude-code') return !!aitoolsResult.value.claude_code?.installed
  if (t === 'cursor') return !!aitoolsResult.value.cursor?.installed
  if (t === 'codex') return !!aitoolsResult.value.codex?.installed
  return null
}
// targetCanBeEnabled(t) — checkbox 是否能被勾选:已检测到 OR 用户强制启用过。
function targetCanBeEnabled(t: string): boolean {
  return targetDetectedInstalled(t) === true || forceEnableMissingTarget[t] === true
}
// openclaw 是唯一需要"工作区目录"概念的 target,其它 3 个都装到用户自选位置
// (claude-code / cursor = 项目根,embedded = Studio 内嵌)。用 computed 单独暴露,
// 模板里读这个 flag 判断要不要露 workspace_name 输入框。
// workspace_name 现在直接在 Step 1 卡片里按 openclaw 勾选状态展开,这里留着
// 给未来潜在消费点(BotsPage 显示 / 校验错误提示等);以 _ 前缀避免 unused 告警。
const _needsWorkspaceName = computed(() => enabledTargets['openclaw'])
void _needsWorkspaceName

// 勾上 openclaw 时触发一次 openclaw 配置探测(还没跑过 / 上次失败都重试)。
// 注意:这段 watch / onMounted 必须放在 enabledTargets 声明之后 —— 早期放前面
// 会因 TDZ(Temporal Dead Zone)在 setup() 初始化时立即触发 getter,读还没声明的
// enabledTargets 报 "Cannot access ... before initialization"。
// openclawDetectStatus 等 ref 在文件上方已声明,跨位置 closure 引用无问题。
watch(() => enabledTargets['openclaw'], (on) => {
  if (on && openclawDetectStatus.value === 'idle') {
    runOpenClawDetect()
  }
})
// 进入向导即探一次 OpenClaw,跟 detectAITools (claude-code/cursor) 一起填卡片头徽章。
// 不依赖 enabledTargets['openclaw'] —— 即使没勾,头部也能看到"v2026.4.9 / ⚠ 未检测到"。
onMounted(() => {
  if (openclawDetectStatus.value === 'idle') {
    runOpenClawDetect()
  }
})

// 探测结果回填后,把"未装且没强制启用"的 target 自动取消勾选 ——
// 默认 enabledTargets 全 true 是"探测前先假设都装着",真探测出来未装就回退。
// 只在探测刚返回时跑一次,后续用户手动操作不被覆盖。
watch([aitoolsResult, openclawDetectStatus], () => {
  for (const t of targetOptions) {
    const det = targetDetectedInstalled(t)
    if (det === false && !forceEnableMissingTarget[t] && enabledTargets[t]) {
      enabledTargets[t] = false
    }
  }
}, { flush: 'post' })

// 环境列表变化 → 清掉不属于当前任何 env.id 的孤儿状态,防 draft 越攒越脏。
// 用户改 env.id(重命名) / removeEnv 都会触发。
// 依赖 environments.map().join() 作为 dependency trigger(deep watch 开销大)。
watch(() => environments.map(e => e.id).join('|'), () => {
  const valid = new Set(environments.map(e => e.id).filter(Boolean))
  // 所有 per-env map:key = env.id
  for (const k of Object.keys(envNamespaces))        if (!valid.has(k)) delete envNamespaces[k]
  for (const k of Object.keys(ccHubStateByEnv))      if (!valid.has(k)) delete ccHubStateByEnv[k]
  for (const k of Object.keys(scannedDS))            if (!valid.has(k)) delete scannedDS[k]
  // 所有 per-(env,svc) 复合 key:前缀是 "<envID>::"(svcKey 与 scanStateKey 一致)
  for (const k of Object.keys(serviceConfigSel)) {
    const env = k.split('::')[0]; if (!valid.has(env)) delete serviceConfigSel[k]
  }
  for (const k of Object.keys(serviceConfigGroup)) {
    const env = k.split('::')[0]; if (!valid.has(env)) delete serviceConfigGroup[k]
  }
  for (const k of Object.keys(dsScanState)) {
    const env = k.split('::')[0]; if (!valid.has(env)) delete dsScanState[k]
  }
  // ccCredInputs 以 "cc:<type>:<env>:<field>" 为 key
  for (const k of Object.keys(ccCredInputs)) {
    const parts = k.split(':'); if (parts.length >= 3 && !valid.has(parts[2])) delete ccCredInputs[k]
  }
  // toolInputs 以 "obs:<tool>:<env>:<field>" / "ds:..." 为 key
  for (const k of Object.keys(toolInputs)) {
    const parts = k.split(':'); if (parts.length >= 3 && !valid.has(parts[2])) delete toolInputs[k]
  }
})

// 切换配置源类型(nacos ↔ apollo ↔ consul ↔ env-vars ↔ kubernetes ↔ none)时,
// 把 Step 5 / Step 7 里跟"上一种源"绑定的扫描状态全部清掉 —— 那些下拉选项 / 服务映射 /
// 识别出的数据层都基于旧源的 API 拉的,切源后完全无意义。
// 凭证输入(ccCredInputs)按 type 前缀分 key,保留不清,切回旧 type 还能复用。
watch(configCenterType, (newType, oldType) => {
  if (newType === oldType) return
  // 统计要清的项数,给用户一个"确实发生了清理"的提示
  const cleaned = {
    namespaces: Object.keys(envNamespaces).length,
    services: Object.keys(serviceConfigSel).length,
    scans: Object.keys(ccHubStateByEnv).length,
    dsEntries: Object.keys(scannedDS).length,
  }
  for (const k of Object.keys(envNamespaces))        delete envNamespaces[k]
  for (const k of Object.keys(serviceConfigSel))     delete serviceConfigSel[k]
  for (const k of Object.keys(serviceConfigGroup))   delete serviceConfigGroup[k]
  for (const k of Object.keys(ccHubStateByEnv))      delete ccHubStateByEnv[k]
  for (const k of Object.keys(scannedDS))            delete scannedDS[k]
  for (const k of Object.keys(dsScanState))          delete dsScanState[k]
  for (const k of Object.keys(dsAutoFilled))         delete dsAutoFilled[k]
  dsImportStatus.value = 'idle'
  dsImportStats.scanned = 0
  dsImportStats.matched = 0
  const any = cleaned.namespaces || cleaned.services || cleaned.scans || cleaned.dsEntries
  if (any) {
    toast.info(`已切至 ${newType},清空上一源(${oldType})的 Step 5/7 扫描与数据层识别结果`)
  }
})

// Auto-save all form state so navigating away doesn't lose the draft
// lastSavedAt:页面右上角"自动保存"徽章用。null = "草稿空 / 尚未保存过"。
// 之前一直 null 兜底,在 keep-alive 模式下没问题(切回保留 ref 值)。但 InitPage 现在
// 不进 keep-alive(App.vue::exclude InitPage),每次进 /init 都重新 mount,如果本地
// 已经有 saved 草稿(loadSavedDraft 命中),badge 应该立刻显示"✓ 自动保存"而不是"草稿空"。
// 所以挂载时根据 saved 是否存在初始化:有 saved → Date.now() 占位(用户改任一字段后由
// auto-save watch 覆盖成真实保存时间);无 saved → null。
const lastSavedAt = ref<number | null>(saved ? Date.now() : null)
watch(
  () => ({
    wizardSchema: 2, // 见 currentStep 上方注释:标记本 draft 已是新 step 编号(欢迎页+9 配置步)
    currentStep: currentStep.value,
    system,
    agent,
    targetModels,
    environments,
    repos,
    repoBranchesMap: repoBranchesMap.value,
    configCenterType: configCenterType.value,
    // 多源:enabledSourceTypes(顶部勾哪些源)+ sourceCreds(每源每 env 的字段值) +
    // serviceSourceMap(每服务选哪个源)
    enabledSourceTypes,
    enabledSourceOrder,
    sourceCreds,
    serviceSourceMap,
    // 所有配置中心字段(含 secret)持久化到 draft —— 跟 yaml 策略对齐,
    // 用户已选择"明文也 OK"的分享模式。
    ccCredInputs,
    // env → namespace / (env,service) → dataId 用户手挑的映射(生成 yaml 的关键)
    envNamespaces,
    serviceConfigSel,
    serviceConfigGroup,
    kuboardSvcMap,
    // 可观测性 k8s_runtime:env→cluster/namespace + (env,svc)→workload/label_selector 两层映射
    k8sRuntimeEnvLoc,
    k8sRuntimeSvcMap,
    // 可观测性各 via-grafana 工具(prometheus/jaeger/tempo/elk)在每 env 选中的 Grafana datasource UID
    grafanaDsUidByObsEnv,
    // 每个 (obs, env) 显式选择的访问方式 via_grafana / direct(默认按 grafana 是否启用兜底)
    obsAccessModeMap,
    // 预加载结果(entries + namespaces + notes),跨会话恢复;重进后下拉直接可用
    // 不用再扫一次。凭证变 / 配置中心改动 → 用户点"重新拉取"刷新即可。
    ccHubStateByEnv,
    // 同上,kuboard 的 cluster/namespace/configmap 三级列表跨会话恢复;
    // 不存的话 kuboardSvcMap 里虽有 selected 值,但下拉 options 是空的 → 视觉上看像没保存
    kuboardStateByEnv,
    // Step 7 "从配置中心读取" 标记,重进后自动识别的徽章仍显示
    dsAutoFilled,
    // Step 7 每个服务识别出的数据层配置(env → service → dsKey → fields)
    scannedDS,
    // Step 7 每个 (env, service) 的扫描状态(ok/empty/skipped/error)
    dsScanState,
    // Step 7 Loki 标签映射(per-env:datasource UID + labels + values + 选中)
    lokiMappingByEnv,
    toolInputs,
    enabledObservability,
    enabledDataStores,
    enabledTargets,
    forceEnableMissingTarget,
    customInstallRoots,
    idManualOverride: idManualOverride.value,
    openclawInstallDir: openclawInstallDir.value,
  }),
  (val) => {
    // 配额吃不下时,先剔除最大头(lokiMappingByEnv 的 dsList / values)再写,
    // 避免你刚选的 k8sRuntimeSvcMap / kuboardSvcMap 等小字段一起被静默丢弃。
    const stringify = (v: any) => JSON.stringify(v)
    let payload = stringify(val)
    try {
      localStorage.setItem(STORAGE_KEY, payload)
      lastSavedAt.value = Date.now()
      return
    } catch (e: any) {
      pushLog('cchub', 'warn',
        `localStorage 写入失败(可能超 quota,size=${payload.length}B): ${String(e?.message || e)};尝试瘦身后重写`,
        {})
    }
    try {
      const slim = { ...val } as any
      // 把 Loki 的 labels / values / dsList 这类列表型大数据剥掉,只保留 dsUID + 选好的 mapping key/value。
      // 重进 Step 7 时会自动重新拉一次,体验上没差,但 quota 大幅下降。
      if (slim.lokiMappingByEnv && typeof slim.lokiMappingByEnv === 'object') {
        const trimmed: Record<string, any> = {}
        for (const [env, m] of Object.entries(slim.lokiMappingByEnv as Record<string, any>)) {
          trimmed[env] = {
            dsUID: m?.dsUID ?? '',
            envLabelKey: m?.envLabelKey ?? '',
            serviceLabelKey: m?.serviceLabelKey ?? '',
            envValue: m?.envValue ?? '',
            serviceValues: m?.serviceValues ?? {},
            // 其它 dsList / labels / *LabelValues 都舍弃
          }
        }
        slim.lokiMappingByEnv = trimmed
      }
      payload = stringify(slim)
      localStorage.setItem(STORAGE_KEY, payload)
      lastSavedAt.value = Date.now()
    } catch (e2: any) {
      pushLog('cchub', 'error',
        `瘦身后写入仍失败: ${String(e2?.message || e2)};你刚改的字段没存到本地`,
        {})
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
    localStorage.removeItem(KUBOARD_STATE_KEY)
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
  agent.id = ''
  agent.name = ''
  agent.workspace_name = ''
  agent.model = 'anthropic/claude-sonnet-4-6'
  // 环境 / 仓库回到初始 1 条
  environments.splice(0, environments.length,
    { id: 'dev', api_domain: '', web_domain: '', is_prod: false },
    { id: 'prod', api_domain: '', web_domain: '', is_prod: true },
  )
  repos.splice(0, repos.length, makeEmptyRepo())
  repoBranchesMap.value = {}
  // 配置源:type 回到默认,输入值清空(clear draft 意图 = 全 reset 输入;
  // 钥匙串里的值不动,用户需显式点 🗑 删按钮才清钥匙串)
  configCenterType.value = 'nacos'
  for (const k of Object.keys(ccCredInputs)) delete ccCredInputs[k]
  // 清掉 env↔namespace 和 service↔dataId 的全部映射(跟 ccCredInputs 同语义)
  for (const k of Object.keys(envNamespaces)) delete envNamespaces[k]
  for (const k of Object.keys(serviceConfigSel)) delete serviceConfigSel[k]
  for (const k of Object.keys(serviceConfigGroup)) delete serviceConfigGroup[k]
  for (const k of Object.keys(kuboardSvcMap)) delete kuboardSvcMap[k]
  for (const k of Object.keys(k8sRuntimeEnvLoc)) delete k8sRuntimeEnvLoc[k]
  for (const k of Object.keys(k8sRuntimeSvcMap)) delete k8sRuntimeSvcMap[k]
  for (const k of Object.keys(k8sRtWorkloadCache)) delete k8sRtWorkloadCache[k]
  for (const k of Object.keys(grafanaDsUidByObsEnv)) delete grafanaDsUidByObsEnv[k]
  for (const k of Object.keys(obsAccessModeMap)) delete obsAccessModeMap[k]
  for (const k of Object.keys(kuboardStateByEnv)) delete kuboardStateByEnv[k]
  persistKuboardState()
  // 清掉 CCHub 扫描缓存 + 数据层自动识别标记
  for (const k of Object.keys(ccHubStateByEnv)) delete ccHubStateByEnv[k]
  for (const k of Object.keys(dsAutoFilled)) delete dsAutoFilled[k]
  for (const k of Object.keys(scannedDS)) delete scannedDS[k]
  for (const k of Object.keys(dsScanState)) delete dsScanState[k]
  for (const k of Object.keys(lokiMappingByEnv)) delete lokiMappingByEnv[k]
  dsImportStatus.value = 'idle'
  dsImportStats.scanned = 0
  dsImportStats.matched = 0
  // 可观测 / 数据层:全关
  for (const k of observabilityOptions) enabledObservability[k] = false
  for (const k of dataStoreOptions) enabledDataStores[k] = false
  // 清所有工具字段输入(跟 ccCredInputs 一致的语义)
  for (const k of Object.keys(toolInputs)) delete toolInputs[k]
  // targets:默认 4 个都开
  for (const k of targetOptions) enabledTargets[k] = true
  // Analyze 块的瞬态也清(reposRoot 输入清掉;per-repo _scanning/_scanError
  // 跟着 repos 重置一起走,单独处理即可)
  reposRootInput.value = ''
  for (const r of repos) {
    r._scanning = false
    r._scanError = undefined
    r._scanned = false
  }
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

async function applyImport() {
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
    agent.id = parsed.agent.id ?? ''
    agent.name = parsed.agent.name ?? ''
    agent.workspace_name = parsed.agent.workspace_name ?? ''
    agent.model = parsed.agent.model ?? agent.model
    // target_models 可选;没填的 target 回落到 agent.model
    const tm = parsed.agent.target_models || {}
    targetModels.openclaw = tm.openclaw || agent.model
  }

  // environments
  if (Array.isArray(parsed.environments) && parsed.environments.length) {
    environments.splice(0, environments.length, ...parsed.environments.map((e: any) => ({
      id: e?.id ?? '',
      api_domain: e?.api_domain ?? '',
      web_domain: e?.web_domain ?? '',
      is_prod: Boolean(e?.is_prod),
    })))
  }

  // repos
  if (Array.isArray(parsed.repos) && parsed.repos.length) {
    // 仓库本地路径不在 yaml 里(可分享性约束),从 ~/.tshoot/config.json 按 system.id
    // 查回来。同一台机器之前 wizard 部署过 → 这里拿到非空 map → 自动回填 _localPath,
    // 用户在 Step 4 不必再点"选目录"。换台机器或全新机器 → 空 map,UI 还是要求用户挑。
    let savedRepoPaths: Record<string, string> = {}
    if (system.id) {
      try { savedRepoPaths = await getRepoPathsForSystem(system.id) } catch { /* 失败不阻塞 import */ }
    }
    repos.splice(0, repos.length, ...parsed.repos.map((r: any) => {
      const branches: Record<string, string> = {}
      for (const env of environments) {
        if (env.id) branches[env.id] = r?.env_branches?.[env.id] ?? ''
      }
      const svcNames = Array.isArray(r?.service_names)
        ? r.service_names.join(', ')
        : (r?.service_names ?? '')
      const repoName = r?.name ?? ''
      const localPath = repoName ? (savedRepoPaths[repoName] || '') : ''
      return {
        name: repoName,
        url: r?.url ?? '',
        stack: r?.stack ?? 'go',
        framework: r?.framework ?? '',
        // role:yaml 里有就保留,否则按 backend 兜底(老 yaml 兼容)
        role: (typeof r?.role === 'string' && r.role.trim()) ? r.role.trim() : 'backend',
        sub_path: typeof r?.sub_path === 'string' ? r.sub_path.trim() : '',
        service_names: svcNames,
        env_branches: branches,
        config_source: typeof r?.config_source === 'string' ? r.config_source : '',
        // 有本地路径就默认进 'local' 模式;否则保持远程 clone 模式
        _source: localPath ? 'local' : 'remote',
        _localPath: localPath,
        // service_entries:同仓多服务的入口路径,反填回 _serviceEntries
        _serviceEntries: (typeof r?.service_entries === 'object' && r?.service_entries) ? { ...r.service_entries } : undefined,
        // 已经是反填回来的(用户上次合并过),banner 默认收起,有需要再点"改"
        _submoduleHintsDismissed: typeof r?.service_entries === 'object' && r?.service_entries && Object.keys(r.service_entries).length > 0,
      }
    }))
  }

  // ── 配置中心:多源 schema(config_centers 数组) > 单源(config_center)──
  // 多源:每个源 → enabledSourceTypes[type]=true + sourceCreds[type] 填表数据
  // 单源:同上,只是 enabled 集合大小为 1
  for (const t of ALL_SOURCE_TYPES) enabledSourceTypes[t] = false
  enabledSourceTypes['none'] = false
  for (const t of ALL_SOURCE_TYPES) sourceCreds[t] = { creds: {} }
  // 重置 order:yaml import 时按 config_centers 数组顺序排,主源(第一个)排在 [0]
  enabledSourceOrder.splice(0, enabledSourceOrder.length)

  const ingestSource = (s: any, sourceID: string) => {
    if (!s || typeof s.type !== 'string') return
    const t = s.type
    enabledSourceTypes[t] = true
    if (!enabledSourceOrder.includes(t)) enabledSourceOrder.push(t)
    const fields = CC_FIELDS_BY_TYPE.value[t] || []
    if (Array.isArray(s.endpoints)) {
      for (const ep of s.endpoints) {
        if (!ep || typeof ep.env !== 'string') continue
        const envCreds: Record<string, string> = sourceCreds[t].creds[ep.env] || {}
        for (const f of fields) {
          const v = ep[f.key]
          if (typeof v === 'string' && !(v.startsWith('{{') && v.endsWith('}}'))) {
            envCreds[f.key] = v
          }
        }
        if (Object.keys(envCreds).length > 0) sourceCreds[t].creds[ep.env] = envCreds
      }
    }
    // kuboard 专属:把 service_map 回填到 kuboardSvcMap(cluster/namespace/configmap 三联)。
    // 这样多源场景(主源 nacos + 副源 kuboard)下,kuboard 的 per-service 选项也能在 UI 复现。
    if (t === 'kuboard' && s.service_map && typeof s.service_map === 'object') {
      for (const [envID, svcs] of Object.entries(s.service_map as Record<string, unknown>)) {
        if (!svcs || typeof svcs !== 'object') continue
        for (const [svc, rec] of Object.entries(svcs as Record<string, unknown>)) {
          if (!rec || typeof rec !== 'object') continue
          const r = rec as { cluster?: string; namespace?: string; configmap?: string }
          const loc = ensureKuboardLoc(envID, svc)
          if (typeof r.cluster === 'string') loc.cluster = r.cluster
          if (typeof r.namespace === 'string') loc.namespace = r.namespace
          if (typeof r.configmap === 'string') loc.configmap = r.configmap
        }
      }
    }
    // 其它高级字段(service_map[非kuboard] / auth / dataid_patterns)整块进 rawExtra round-trip 保留。
    // kuboard 的 service_map 排除在外:已经吃进 kuboardSvcMap,emitSourceBody 会重新输出,
    // 留在 rawExtra 会双写(dump 段会再 emit 一次同样的 service_map 块)。
    const rawExtra: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(s)) {
      if (k === 'id' || k === 'type' || k === 'endpoints') continue
      // 兼容老 yaml:per_env_credentials 字段已废弃,丢掉不回写
      if (k === 'per_env_credentials') continue
      if (t === 'kuboard' && k === 'service_map') continue
      rawExtra[k] = v
    }
    if (Object.keys(rawExtra).length > 0) sourceCreds[t].rawExtra = rawExtra
    void sourceID // reserved 给以后同 type 多源做 disambiguation
  }

  let primarySource: any = null
  const ccArray = parsed.infrastructure?.config_centers
  if (Array.isArray(ccArray) && ccArray.length > 0) {
    for (const s of ccArray) {
      ingestSource(s, typeof s?.id === 'string' ? s.id : '')
    }
    // 主源(用于 service_map 回填):取 id=default 的,没有就第一个
    const defaultEntry = ccArray.find((s: any) => s?.id === 'default')
    primarySource = defaultEntry || ccArray[0]
  } else if (parsed.infrastructure?.config_center) {
    ingestSource(parsed.infrastructure.config_center, 'default')
    primarySource = parsed.infrastructure.config_center
  }

  const cc = primarySource?.type
  // 导入 yaml 里 endpoints[].<field> 回填到 ccCredInputs(所有字段,含 secret):
  // yaml 带明文是设计如此,同事导入就齐活。占位符 {{XXX}} 跳过不覆盖用户可能已填的值。
  // 注:ingestSource 已写到 sourceCreds,这里再写一份到 ccCredInputs 是为了让老 preload /
  // 老命名空间下拉等代码继续用 ccKeyFor 拼 key。后面 syncCcCredInputsFromSource 会刷新。
  const endpoints = primarySource?.endpoints
  if (Array.isArray(endpoints) && typeof cc === 'string') {
    const fields = CC_FIELDS_BY_TYPE.value[cc] || []
    for (const ep of endpoints) {
      const envID = ep?.env
      if (!envID || typeof envID !== 'string') continue
      for (const f of fields) {
        const v = ep?.[f.key]
        if (typeof v !== 'string') continue
        if (v.startsWith('{{') && v.endsWith('}}')) continue // 占位符不回填
        ccCredInputs[ccKeyFor(cc, envID, f.key)] = v
      }
    }
  }

  // service_map:每个 env → 每个服务 → {namespace, group, data_id} 回填到
  // envNamespaces + serviceConfigSel + serviceConfigGroup。用户之前在 Step 5
  // 挑过的下拉选项都恢复。多源时只回填主源的;副源 service_map 在 extraConfigSources blob 里随源透传。
  // kuboard 的 service_map 已在 ingestSource 里吃进 kuboardSvcMap,这里只处理 nacos/apollo/consul shape。
  const svcMap = (cc !== 'kuboard') ? primarySource?.service_map : null
  if (svcMap && typeof svcMap === 'object') {
    for (const [envID, svcs] of Object.entries(svcMap)) {
      if (!svcs || typeof svcs !== 'object') continue
      for (const [svc, rec] of Object.entries(svcs as Record<string, unknown>)) {
        if (!rec || typeof rec !== 'object') continue
        const r = rec as { namespace?: string; group?: string; data_id?: string }
        if (typeof r.namespace === 'string' && r.namespace) {
          envNamespaces[envID] = r.namespace
        }
        if (typeof r.data_id === 'string' && r.data_id) {
          serviceConfigSel[svcKey(envID, svc)] = r.data_id
        }
        if (typeof r.group === 'string' && r.group) {
          serviceConfigGroup[svcKey(envID, svc)] = r.group
        }
      }
    }
  }

  // observability:勾选态 + 每个工具的 endpoints[].<field> 回填到 toolInputs
  const obs = parsed.infrastructure?.observability
  if (obs && typeof obs === 'object') {
    for (const key of Object.keys(enabledObservability)) {
      enabledObservability[key] = Boolean(obs?.[key]?.enabled)
      // 回填 endpoints
      const spec = toolSpecByKey('obs', key)
      const endpoints = obs?.[key]?.endpoints
      if (spec && Array.isArray(endpoints)) {
        for (const ep of endpoints) {
          const envID = ep?.env
          if (!envID || typeof envID !== 'string') continue
          for (const f of spec.fields) {
            const v = ep?.[f.key]
            if (typeof v !== 'string') continue
            if (v.startsWith('{{') && v.endsWith('}}')) continue
            toolInputs[toolKeyFor('obs', key, envID, f.key)] = v
          }
        }
      }
      // datasource_uid_by_env(prometheus/jaeger/tempo/elk 走 Grafana 代理时记录的 UID)
      const uidMap = obs?.[key]?.datasource_uid_by_env
      if (uidMap && typeof uidMap === 'object' && ['prometheus','jaeger','tempo','elk'].includes(key)) {
        for (const [envID, uid] of Object.entries(uidMap)) {
          if (typeof uid === 'string' && uid) {
            grafanaDsUidByObsEnv[obsGrafanaDsKey(key, envID)] = uid
          }
        }
      }
      // 访问方式回填:依据 yaml 里 via_grafana 标志 + 是否有 endpoints/uid_map 推断每个 env 的模式。
      // 没法从 yaml 反推 per-env 不同 mode(yaml 只有一个 via_grafana 全局值),按 yaml 标志全 env 同步设。
      if ((VIA_GRAFANA_ELIGIBLE as readonly string[]).includes(key)) {
        const viaGrafana = Boolean(obs?.[key]?.via_grafana)
        for (const env of environments) {
          if (!env.id) continue
          obsAccessModeMap[obsAccessKey(key, env.id)] = viaGrafana ? 'via_grafana' : 'direct'
        }
      }
    }
    // k8s_runtime.service_map 回填:cluster/ns 提到 env 级(envLoc),workload/selector 进服务级(svcMap)。
    // 同 env 多行 cluster/ns 不一致时取第一条(向导本来也只允许 env 级单选)。
    const k8sSvcMap = obs?.['k8s_runtime']?.service_map
    if (Array.isArray(k8sSvcMap)) {
      for (const k of Object.keys(k8sRuntimeSvcMap)) delete k8sRuntimeSvcMap[k]
      for (const k of Object.keys(k8sRuntimeEnvLoc)) delete k8sRuntimeEnvLoc[k]
      for (const entry of k8sSvcMap) {
        const envID = entry?.env, svc = entry?.service
        if (typeof envID !== 'string' || typeof svc !== 'string' || !envID || !svc) continue
        if (!k8sRuntimeEnvLoc[envID]) {
          k8sRuntimeEnvLoc[envID] = {
            cluster: typeof entry?.cluster === 'string' ? entry.cluster : '',
            namespace: typeof entry?.namespace === 'string' ? entry.namespace : '',
          }
        }
        k8sRuntimeSvcMap[svcKey(envID, svc)] = {
          workload: typeof entry?.workload === 'string' ? entry.workload : '',
          label_selector: typeof entry?.label_selector === 'string' ? entry.label_selector : '',
        }
      }
    }
  }

  // data stores:新 schema endpoints 里每条有 env + service + 字段,回填到 scannedDS。
  // 兼容老 schema(没有 service 字段的 endpoints):视为 service="legacy",也能呈现给用户。
  const ds = parsed.infrastructure?.data_stores
  if (Array.isArray(ds)) {
    for (const key of Object.keys(scannedDS)) delete scannedDS[key]
    for (const entry of ds) {
      const t = entry?.type
      if (typeof t !== 'string' || entry?.enabled === false) continue
      const spec = toolSpecByKey('ds', t)
      const endpoints = entry?.endpoints
      if (!spec || !Array.isArray(endpoints)) continue
      // yaml 里 enabled !== false 即开启 → 同步打开 enabledDataStores 标记。
      // 否则下次 emit yaml 时这个数据层被丢掉,skills_whitelist 里 <type>-runtime-query
      // 也不会出现(跟 auto-import 漏写 enabledDataStores 同一个 bug)。
      enabledDataStores[t] = true
      for (const ep of endpoints) {
        const envID = ep?.env
        if (!envID || typeof envID !== 'string') continue
        const svc = typeof ep?.service === 'string' && ep.service ? ep.service : 'legacy'
        if (!scannedDS[envID]) scannedDS[envID] = {}
        if (!scannedDS[envID][svc]) scannedDS[envID][svc] = {}
        const fields: DSFieldMap = {}
        for (const f of spec.fields) {
          const v = ep?.[f.key]
          if (typeof v !== 'string' || !v) continue
          if (v.startsWith('{{') && v.endsWith('}}')) continue
          fields[f.key] = v
        }
        if (Object.keys(fields).length > 0) {
          scannedDS[envID][svc][t] = fields
          dsAutoFilled[t] = true
        }
      }
    }
  }

  // 导入完直接跳到 Step 2(系统基本信息)— 反填的字段从这里展开,用户能逐步检查 / 改。
  // 留在欢迎页(Step 1)没意义,反填的内容在那看不见。
  currentStep.value = 2
  showImportDialog.value = false
}

// ── Step 7: Preview / generate ──
const yamlOutput = ref('')
const validateResult = ref<{ ok: boolean; message: string } | null>(null)
const validateLoading = ref(false)
const copySuccess = ref(false)

// 进入预览步骤就自动生成 yaml(不再依赖 nextStep / goToStep 的副作用):
//   - 退出 app 后重启,currentStep 从 localStorage 恢复到 totalSteps,
//     这时不再需要"上一步 → 下一步"才能看到预览。
//
// 关键 1:try/catch 不能丢 —— generateYAML 在某些 saved 状态下读到尚未初始化的字段
// 抛错会直接让 Vue setup 失败,整个 InitPage 白屏。捕获后给个空字符串兜底,
// 用户至少能看到 Step 8 容器框,顶上显示"yaml 生成失败,详见日志"。
//
// 关键 2:**不能用 immediate: true**。watch 同步触发会发生在 setup 流程中,
// 此时 `const` 还在按顺序声明的过程中,generateYAML / 它的 helper 调用到的某个
// 后置 const 就会撞 TDZ("Cannot access 'X' before initialization")。
// 改用 onMounted 兜底首次触发(跟 line 2259-2260 的 triggerStep7Init 同款),
// 这时所有 const 都已 ready。watch 自身只处理"用户 next 进 Step 8"的非首次情况。
// Step 9 = yaml 预览(总步数最后一步是 Step 10 部署,所以 yaml 不再 ===  totalSteps)
const YAML_PREVIEW_STEP = 9
const runYAMLGen = (s: number) => {
  // 进 Step 8 / Step 9 都触发(部署期也可能要看 yaml 内容);其它步直接 return
  if (s !== YAML_PREVIEW_STEP && s !== totalSteps) return
  try {
    yamlOutput.value = generateYAML()
  } catch (e) {
    console.error('[generateYAML] failed:', e)
    yamlOutput.value = `# yaml 生成失败,详见日志面板\n# error: ${String((e as any)?.message || e)}\n`
    try {
      pushLog('cchub', 'error', `yaml 生成失败: ${String((e as any)?.message || e)}`)
    } catch { /* pushLog 自身可能在 setup 期间还没初始化好,吞掉避免连锁失败 */ }
  }
}
// watch 用 nextTick 包一层 —— 用户从 Step 7 → Step 8 切换时,setup 可能还在执行某些后续的
// const 声明(影响 generateYAML 用到的 helper)。同步触发会撞已知的 TDZ;让它进 microtask
// 队列,等当前 sync 调用栈结束、所有 const 都 ready 再跑。
watch(currentStep, (s) => {
  nextTick(() => runYAMLGen(s))
})
onMounted(() => runYAMLGen(currentStep.value))

// ── Skills whitelist derivation ──
// 数据层 enabledDataStores 的 key 跟 skill 目录名不是 1:1 对应:特例 elasticsearch → es-runtime-query。
// 其他类型(redis/mongodb/mysql/postgresql/kafka/rocketmq/rabbitmq/clickhouse)就是 <key>-runtime-query。
const DS_SKILL_NAME: Record<string, string> = {
  elasticsearch: 'es-runtime-query',
}
function deriveSkillsWhitelist(): string[] {
  // routing / incident-investigator / recent-changes 是"编排者 / 路由"三大基础 skill,
  // 跟具体后端无关,任何启用配置都需要它们;前者给数据,后两者把数据串成排障流程。
  const skills: string[] = ['routing', 'incident-investigator', 'recent-changes']
  if (configCenterType.value !== 'none') skills.push('config-executor')
  for (const [key, on] of Object.entries(enabledDataStores)) {
    if (on) skills.push(DS_SKILL_NAME[key] || `${key}-runtime-query`)
  }
  // K8s 运行时查询是"可观测性"维度,跟"配置源是不是 kuboard"正交。
  // 用户可能从 nacos 读配置但仍要查 pod 健康(走 Kuboard v4 API)。
  if (enabledObservability['k8s_runtime']) skills.push('k8s-runtime-query')
  if (enabledObservability.grafana) skills.push('diagram-generator')
  // 三家 tracing 各有专门 skill;jaeger 用通用 tracing-query,skywalking / tempo 用各自的
  if (enabledObservability.jaeger) skills.push('tracing-query')
  if (enabledObservability.skywalking) skills.push('skywalking-query')
  if (enabledObservability.tempo) skills.push('tempo-query')
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

// hasAnyLokiMapping 当前 environments 是否有任意 env 配过完整的 Loki 标签映射(envLabelKey + serviceLabelKey)。
// 用作 yaml 是否输出 loki.label_mapping_by_env 节的开关。
function hasAnyLokiMapping(): boolean {
  return environments.some(env => {
    if (!env.id) return false
    const lm = lokiMappingByEnv[env.id]
    return !!(lm && lm.envLabelKey && lm.serviceLabelKey)
  })
}

// emitLokiLabelMapping 输出 label_mapping_by_env 块到给定 lines。
// indent 是父字段的缩进(eg "      " 6 空格表 loki: 字段),内部按 +2 空格逐层缩进。
// 给 yaml 生成两处复用(loki 启用时的 loki 块下 / loki 未启用但有映射时的兜底块下)。
function emitLokiLabelMapping(lines: string[], indent: string): void {
  const lmEnvs = environments.filter(env => env.id && lokiMappingByEnv[env.id]
    && lokiMappingByEnv[env.id].envLabelKey && lokiMappingByEnv[env.id].serviceLabelKey)
  if (lmEnvs.length === 0) return
  lines.push(`${indent}label_mapping_by_env:    # routing skill 拼 LogQL 时按 (env, service) 注入 label 过滤器`)
  for (const env of lmEnvs) {
    const lm = lokiMappingByEnv[env.id]!
    lines.push(`${indent}  ${env.id}:`)
    lines.push(`${indent}    env_label: ${yamlStr(lm.envLabelKey)}`)
    lines.push(`${indent}    service_label: ${yamlStr(lm.serviceLabelKey)}`)
    if (lm.dsUID) lines.push(`${indent}    grafana_ds_uid: ${yamlStr(lm.dsUID)}`)
    if (lm.envValue) {
      lines.push(`${indent}    ${lm.envLabelKey}: ${yamlStr(lm.envValue)}`)
    }
    const svcLines: string[] = []
    for (const svc of allServiceNames.value) {
      const v = (lm.serviceValues || {})[svc]
      // 空值 = "无 / 不进 loki":不写 service 块进 service_map ——
      // routing skill 拿不到该服务的 label 值就跳过 loki 查询。
      if (!v) continue
      svcLines.push(`${indent}      ${yamlStr(svc)}:`)
      svcLines.push(`${indent}        ${lm.serviceLabelKey}: ${yamlStr(v)}`)
    }
    if (svcLines.length > 0) {
      lines.push(`${indent}    service_map:`)
      lines.push(...svcLines)
    }
  }
}

function generateYAML(): string {
  // 出 yaml 之前先按 scannedDS 实时刷一次 enabledDataStores —— 这是 skills_whitelist
  // 派生 + Step 5 env-vars 字段集 + 校验逻辑共同的"启用清单",必须跟用户 Step 6 实际看到的
  // 数据层组件一致。否则:用户删了某 type 的最后一条 → enabledDataStores 还 true → 白名单
  // 仍带 <type>-runtime-query → 部署后空跑;反之同理。
  recomputeEnabledDataStoresFromScanned()
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
  // agent.id 是机器人在 AI 平台里的稳定标识(OpenClaw agents.list[*].id / Claude Code、Cursor 的 subagent 名)
  // 用户没填就推导成 "<system.id>-troubleshooter",跟历史命名兼容。
  const agentID = (agent.id || '').trim() || `${system.id || 'my-system'}-troubleshooter`
  lines.push(`  id: ${agentID}            # AI 平台里的稳定标识(OpenClaw agents.list / Claude Code / Cursor subagent 名)`)
  lines.push(`  name: ${yamlStr(agent.name || agentNameDefault.value)}`)
  // model 是 openclaw 专属:claude-code / cursor 在客户端自己挑,没勾 openclaw 就不写。
  // workspace_name 不再单独 emit —— Go 端 ResolveWorkspaceName() 会用 agent.id 当目录名,
  // 跟 OpenClaw agents.list 共享同一标识,用户少看一个字段。
  if (enabledTargets['openclaw']) {
    lines.push(`  model: ${agent.model}    # OpenClaw gateway 路由用的 LLM model id`)
    // target_models:仅在 openclaw 被勾选,且其模型跟 agent.model 不一致时才写出。
    const tmEntries: [string, string][] = []
    for (const t of modelConsumingTargets) {
      if (!enabledTargets[t]) continue
      const v = (targetModels[t] || '').trim()
      if (v && v !== agent.model) tmEntries.push([t, v])
    }
    if (tmEntries.length > 0) {
      lines.push('  target_models:     # per-target 模型覆盖;key 只认 openclaw(其它 target 不消费)')
      for (const [t, m] of tmEntries) {
        lines.push(`    ${t}: ${m}`)
      }
    }
  }

  // environments
  lines.push('')
  lines.push('# environments：声明系统的所有环境。每个 env 会注册一套独立的 MCP 实例')
  lines.push('# （如 nacos-mcp-server-dev / -prod），机器人按 is_prod 调整谨慎度。')
  lines.push('environments:')
  for (const env of environments) {
    lines.push(`  - id: ${env.id || 'env'}`)
    // 域名保留用户给的 scheme(http/https)—— 下游 bot 实际发请求时需要知道协议,
    // 我们不替用户猜;只剥 path/query 保留 host[:port] 部分。裸 host(无 scheme)
    // 也接受,下游按 https 兜底。
    const apiD = normalizeDomain(env.api_domain)
    const webD = normalizeDomain(env.web_domain)
    // 带 scheme 的值(https://...)里含 ":",用 yamlStr 做 double-quote 转义防严格 parser 误解
    if (apiD) lines.push(`    api_domain: ${yamlStr(apiD)}     # 后端接口(带 http/https 前缀更明确;不带视为 https)`)
    if (webD) lines.push(`    web_domain: ${yamlStr(webD)}     # 前端入口(同上)`)
    lines.push(`    is_prod: ${env.is_prod}         # 生产环境标记:true 时机器人默认更保守、查询前二次确认`)
  }

  // repos
  lines.push('')
  lines.push('# repos：所有纳入排障范围的代码仓库。stack 决定 analyzer 与 skill 策略。')
  lines.push('repos:')
  for (const repo of repos) {
    // stack 若没扫到兜底成 go,保证 yaml schema 合法;Step 4 UI 是 readonly 自动识别 badge,
    // 用户如果对兜底不满意,预览这里改或回 Step 4 点"扫一下"。
    lines.push(`  - name: ${repo.name || 'my-service'}`)
    lines.push(`    url: ${repo.url || 'git@github.com:org/repo.git'}`)
    lines.push(`    stack: ${repo.stack || 'go'}             # go/java/node/php/python，决定用哪种配置扫描器`)
    // role 默认 backend 时不写出(老 yaml 兼容);非默认才 emit
    if (repo.role && repo.role !== 'backend') {
      lines.push(`    role: ${repo.role}             # 仓库角色:决定排障时是否进服务依赖图`)
    }
    // sub_path:monorepo 子目录,空时不写出
    if (repo.sub_path && repo.sub_path.trim()) {
      lines.push(`    sub_path: ${repo.sub_path.trim()}             # monorepo 子目录(本服务在仓库内的相对路径)`)
    }
    if (repo.framework) lines.push(`    framework: ${repo.framework}`)
    if (repo.service_names.trim()) {
      lines.push('    service_names:       # 本 repo 实际部署出来的 service 名（config-map 以此为 key）')
      for (const sn of repo.service_names.split(',').map(s => s.trim()).filter(Boolean)) {
        lines.push(`      - ${sn}`)
      }
    }
    // service_entries:同仓多服务时记录每个服务的入口子目录(routing skill 据此 grep 代码)。
    // 只在 _serviceEntries 非空时 emit;独立 repo / 单服务仓不写。
    if (repo._serviceEntries && Object.keys(repo._serviceEntries).length > 0) {
      lines.push('    service_entries:     # 同仓多服务时,每个服务在仓库内的入口子目录')
      for (const [name, entry] of Object.entries(repo._serviceEntries)) {
        if (!name || !entry) continue
        lines.push(`      ${name}: ${entry}`)
      }
    }
    const branchEntries = Object.entries(repo.env_branches).filter(([, v]) => v)
    if (branchEntries.length) {
      lines.push('    env_branches:        # 每个 env 对应的长期分支；routing skill 据此切换代码')
      for (const [eid, branch] of branchEntries) {
        lines.push(`      ${eid}: ${branch}`)
      }
    }
    // config_source:多源场景下显式声明本仓库走哪个源(从 repo 的服务们的源里取
    // 第一个;单仓多服务用不同源是边角场景,一般同源)。单源场景不写。
    if (isMultiSource.value) {
      const svcs = repo.service_names.split(',').map(s => s.trim()).filter(Boolean)
      const firstSvc = svcs[0] || repo.name
      const src = getServiceSource(firstSvc)
      if (src && src !== activeSourceTypes.value[0]) {
        lines.push(`    config_source: ${src}    # 引用 infrastructure.config_centers[].id`)
      }
    }
  }

  // infrastructure
  lines.push('')
  lines.push('infrastructure:')

  // config_center / config_centers
  // 设计:**所有字段**(包括密码 / token)的填过的值**都写进 system.yaml**,让同事
  // 导入后开箱即用,不用再问不用再填。没填的字段仍给 {{占位符}},让 install.sh 兜底。
  //
  // ⚠ 代价:yaml 带明文密码,分享范围必须可控(团队私有 git / 私密频道),**绝不能**
  // push 到公开 github / 贴公开论坛。secret 字段旁边有"🔒"图标提醒。
  //
  // schema 选择:
  //   - 单源(extraConfigSources 空):写老 config_center 单数(yaml 干净,跟现存项目兼容)
  //   - 多源:写新 config_centers 数组,主源(default)+ 副源(从 yaml 透传 blob)
  // emitSourceBody:把 endpoints + service_map 按指定缩进写到 lines。
  // type 决定字段集;sourceID 给"主源 vs 副源"显示提示用(影响 placeholder 命名)。
  // includeServiceMap 仅主源(default 或单源 type)写,副源的 service_map 走 rawExtra。
  const emitSourceBody = (out: string[], baseIndent: string, type: string, sourceID: string, includeServiceMap: boolean) => {
    const data = sourceCreds[type] || { creds: {} }
    const fields = CC_FIELDS_BY_TYPE.value[type] || []
    // kuboard:cluster/namespace/configmap 不是连接凭证,改成 per-service service_map 输出。
    // endpoints 段只写 url + auth 类字段(剩下的 fields)。
    const isKuboard = type === 'kuboard'
    const endpointFields = isKuboard
      ? fields.filter(f => f.key !== 'cluster' && f.key !== 'namespace' && f.key !== 'configmap')
      : fields
    if (endpointFields.length > 0) {
      out.push(`${baseIndent}endpoints:     # ⚠ 含明文凭证,仅团队私密范围分享,别 commit 公开 git`)
      for (const env of environments) {
        if (!env.id) continue
        out.push(`${baseIndent}  - env: ${env.id}`)
        const envCreds = data.creds[env.id] || {}
        for (const f of endpointFields) {
          if (f.uiOnly) continue // UI-only(如 auth_mode)不进 yaml
          // showWhen 命中(如 kuboard 的 access_key / username+password 二选一)→
          // UI 隐藏的字段就别 emit,不然 yaml 里会同时列两套凭证字段误导用户。
          if (isFieldHidden(type, env.id, f, (k) => (envCreds[k] || ''))) continue
          const v = (envCreds[f.key] || '').trim()
          if (v) {
            const comment = f.secret ? '      # ⚠ secret,yaml 分享注意范围' : ''
            out.push(`${baseIndent}    ${f.key}: ${yamlStr(v)}${comment}`)
          } else {
            // placeholder 包含 sourceID(多源时区分),单源 default 保持老命名兼容
            const ph = sourceID === 'default'
              ? f.envVar(env.id)
              : `${f.envVar(env.id)}_${sourceID.toUpperCase().replace(/-/g, '_')}`
            out.push(`${baseIndent}    ${f.key}: "{{${ph}}}"      # 没填,部署时交互收集`)
          }
        }
      }
    }
    if (includeServiceMap) {
      const svcMapLines: string[] = []
      for (const env of environments) {
        if (!env.id) continue
        const perEnv: string[] = []
        for (const svc of allServiceNames.value) {
          // 只把"挑了走当前 source"的服务写进当前 source 的 service_map
          if (getServiceSource(svc) !== type) continue
          if (isKuboard) {
            const loc = kuboardSvcMap[svcKey(env.id, svc)]
            if (!loc) continue
            const cluster = (loc.cluster || '').trim()
            const ns = (loc.namespace || '').trim()
            const cm = (loc.configmap || '').trim()
            // 至少要有 cluster + namespace + configmap 才落 yaml,否则跳过(不完整不可用)
            if (!cluster || !ns || !cm) continue
            perEnv.push(`${baseIndent}      ${yamlStr(svc)}:`)
            perEnv.push(`${baseIndent}        cluster: ${yamlStr(cluster)}`)
            perEnv.push(`${baseIndent}        namespace: ${yamlStr(ns)}`)
            perEnv.push(`${baseIndent}        configmap: ${yamlStr(cm)}`)
          } else {
            const dataId = (serviceConfigSel[svcKey(env.id, svc)] || '').trim()
            if (!dataId) continue
            const ns = (envNamespaces[env.id] || '').trim()
            const group = (serviceConfigGroup[svcKey(env.id, svc)] || '').trim()
            perEnv.push(`${baseIndent}      ${yamlStr(svc)}:`)
            if (ns) perEnv.push(`${baseIndent}        namespace: ${yamlStr(ns)}`)
            if (group) perEnv.push(`${baseIndent}        group: ${yamlStr(group)}`)
            perEnv.push(`${baseIndent}        data_id: ${yamlStr(dataId)}`)
          }
        }
        if (perEnv.length > 0) {
          svcMapLines.push(`${baseIndent}    ${env.id}:`)
          svcMapLines.push(...perEnv)
        }
      }
      if (svcMapLines.length > 0) {
        const fieldList = isKuboard ? 'cluster / namespace / configmap' : 'namespace / group / data_id'
        out.push(`${baseIndent}service_map:   # 每个环境每个服务对应哪条配置(${fieldList})`)
        out.push(...svcMapLines)
      }
    }
    // rawExtra(yaml 高级字段透传)
    if (data.rawExtra) {
      const dump = yaml.dump(data.rawExtra, { indent: 2, lineWidth: -1 })
      for (const line of dump.split('\n')) {
        if (line.trim() === '') continue
        out.push(`${baseIndent}${line}`)
      }
    }
  }

  const active = activeSourceTypes.value
  if (active.length === 0) {
    // 没勾任何源:写 type=none 占位
    lines.push('  config_center:        # 没勾配置源,写 none 占位')
    lines.push('    type: none')
  } else if (active.length === 1) {
    // 单源:走老 schema config_center: { ... }(yaml 干净 + 跟现存项目兼容)
    const t = active[0]
    lines.push('  config_center:        # 配置中心:nacos/apollo/consul/kubernetes/env-vars/none')
    lines.push(`    type: ${t}`)
    emitSourceBody(lines, '    ', t, 'default', true)
  } else {
    // 多源:写 config_centers: [...] 数组,每个源 id 取 type
    lines.push('  config_centers:        # 多源配置:每个源独立 type/凭证;repos[].config_source 引用 id')
    for (const t of active) {
      lines.push(`    - id: ${t}        # 源 id 跟 type 同名(简单模式;同 type 多源需手编辑)`)
      lines.push(`      type: ${t}`)
      emitSourceBody(lines, '      ', t, t, true)
    }
  }

  // observability:对每个勾选的工具写 endpoints(按 env 列连接字段,跟 Step 5 同样的策略)。
  // 用户填过的值直接进 yaml;空字段不输出。保留 loki/prometheus via_grafana 标志
  // (这俩常见"通过 Grafana 代理访问"的用法,在选 Grafana 时自动标 true 给 routing skill 参考)。
  // 即使没勾任何工具,只要 Loki 标签映射有数据也要输出 observability.loki.label_mapping_by_env
  // (用户可能只用 grafana,loki 走代理但 mapping 仍要进 yaml)
  const anyObs = Object.values(enabledObservability).some(Boolean) || hasAnyLokiMapping()
  if (anyObs) {
    lines.push('')
    lines.push('  observability:        # ⚠ 含明文凭证,仅团队私密范围分享')
    for (const spec of OBS_TOOL_SPECS) {
      if (!enabledObservability[spec.key]) continue
      lines.push(`    ${spec.key}:`)
      lines.push('      enabled: true')
      if (spec.key === 'elk') {
        lines.push(`      default_index: "${system.id || 'my-system'}-logs-*"`)
      }
      // 每个 env 的 via_grafana 模式状态:per-env 不同的混合模式也支持
      // (但常见就是全 env 统一)。loki 总是带 via_grafana(老代码兼容);其他类型按需。
      const isViaGrafanaEligible = (VIA_GRAFANA_ELIGIBLE as readonly string[]).includes(spec.key)
      const anyViaGrafana = isViaGrafanaEligible && environments.some(env =>
        env.id && getObsAccessMode(spec.key, env.id) === 'via_grafana')
      if (spec.key === 'loki' || spec.key === 'prometheus' || spec.key === 'jaeger' || spec.key === 'tempo' || spec.key === 'elk') {
        lines.push(`      via_grafana: ${anyViaGrafana}`)
      }
      // Loki 标签映射(per-env)在 loki 块下输出。indent="      " 表 6 空格(loki: 字段缩进)
      if (spec.key === 'loki') {
        emitLokiLabelMapping(lines, '      ')
      }
      // 按 env 列填过的字段(via_grafana 模式下 endpoints 字段不进 yaml,改走 datasource_uid_by_env)
      const envRows: string[] = []
      for (const env of environments) {
        if (!env.id) continue
        // via_grafana 模式 → 端点信息走 Grafana ds,不写本工具自己的 url/auth
        if (isViaGrafanaEligible && getObsAccessMode(spec.key, env.id) === 'via_grafana') continue
        const fieldLines: string[] = []
        for (const f of spec.fields) {
          if (f.uiOnly) continue
          if (isObsFieldHidden(spec.key, env.id, f)) continue
          const k = toolKeyFor('obs', spec.key, env.id, f.key)
          const v = (toolInputs[k] || '').trim()
          if (v) {
            const note = f.secret ? '      # ⚠ secret' : ''
            fieldLines.push(`          ${f.key}: ${yamlStr(v)}${note}`)
          }
        }
        if (fieldLines.length > 0) {
          envRows.push(`        - env: ${env.id}`)
          envRows.push(...fieldLines)
        }
      }
      if (envRows.length > 0) {
        lines.push('      endpoints:')
        lines.push(...envRows)
      }
      // Loki 之外的 via-grafana 工具(prometheus/jaeger/tempo/elk):via_grafana 模式选了 ds → emit。
      // Loki 自己的 ds_uid 在 emitLokiLabelMapping 里输出(老 schema 维持兼容)。
      if (['prometheus', 'jaeger', 'tempo', 'elk'].includes(spec.key)) {
        const uidRows: string[] = []
        for (const env of environments) {
          if (!env.id) continue
          if (getObsAccessMode(spec.key, env.id) !== 'via_grafana') continue
          const uid = (grafanaDsUidByObsEnv[obsGrafanaDsKey(spec.key, env.id)] || '').trim()
          if (uid) uidRows.push(`        ${env.id}: ${yamlStr(uid)}`)
        }
        if (uidRows.length > 0) {
          lines.push('      datasource_uid_by_env:        # 走 Grafana 代理时用的 datasource UID')
          lines.push(...uidRows)
        }
      }
      // k8s_runtime 专属:cluster/namespace 从 env 级 k8sRuntimeEnvLoc 拿(一个 env 一组),
      // workload + selector 从服务级 k8sRuntimeSvcMap 拿。每行 env+service 把这两层拼成
      // 一条完整的 service_map 记录,routing skill 直接喂给 KuboardListPods。
      if (spec.key === 'k8s_runtime') {
        const svcRows: string[] = []
        for (const env of environments) {
          if (!env.id) continue
          const eloc = k8sRuntimeEnvLoc[env.id]
          if (!eloc?.cluster || !eloc?.namespace) continue
          for (const svc of allServiceNames.value) {
            const sloc = k8sRuntimeSvcMap[svcKey(env.id, svc)]
            // 没挑 workload 也照样落一行 cluster+ns,routing skill 至少能定位到 ns 级,
            // 落到具体 pod 时再 fallback 到 svc 名做 label 模糊匹配。
            svcRows.push(`        - env: ${env.id}`)
            svcRows.push(`          service: ${yamlStr(svc)}`)
            svcRows.push(`          cluster: ${yamlStr(eloc.cluster)}`)
            svcRows.push(`          namespace: ${yamlStr(eloc.namespace)}`)
            if (sloc?.workload) svcRows.push(`          workload: ${yamlStr(sloc.workload)}`)
            if (sloc?.label_selector) svcRows.push(`          label_selector: ${yamlStr(sloc.label_selector)}`)
          }
        }
        if (svcRows.length > 0) {
          lines.push('      service_map:        # routing skill 解析 env+服务名时用')
          lines.push(...svcRows)
        }
      }
    }
    // 兜底:用户只勾了 grafana(没勾 loki)但配过 Loki 标签映射。
    // 写一个只含 enabled:false + via_grafana:true + label_mapping_by_env 的 loki 节点,
    // 让 routing skill 仍能拿到映射。
    if (!enabledObservability.loki && hasAnyLokiMapping()) {
      lines.push('    loki:')
      lines.push('      enabled: false      # 仅承载标签映射,实际通过 Grafana 代理查询')
      lines.push('      via_grafana: true')
      emitLokiLabelMapping(lines, '      ')
    }
  }

  // data_stores:从 scannedDS(env → service → dsKey → fields)推导。
  // schema 为每个数据层 type 一条,endpoints 按 env × service 展开,
  // 让 generator 知道"dev 环境 user-service 用的 redis 是 X,order-service 用的 redis 是 Y"。
  const dsTypesUsed = new Set<string>()
  for (const envID of Object.keys(scannedDS)) {
    for (const svc of Object.keys(scannedDS[envID])) {
      for (const dsKey of Object.keys(scannedDS[envID][svc])) {
        dsTypesUsed.add(dsKey)
      }
    }
  }
  if (dsTypesUsed.size > 0) {
    lines.push('')
    lines.push('  data_stores:          # 从各服务配置自动识别的数据层;⚠ 含明文凭证,分享注意范围')
    for (const dsType of Array.from(dsTypesUsed).sort()) {
      const spec = toolSpecByKey('ds', dsType)
      lines.push(`    - type: ${dsType}`)
      lines.push('      enabled: true')
      lines.push('      readonly_enforced: true    # 强制只读;generator 拒绝写操作')
      const epRows: string[] = []
      for (const env of environments) {
        if (!env.id) continue
        const svcs = scannedDS[env.id]
        if (!svcs) continue
        for (const svc of Object.keys(svcs).sort()) {
          const fields = svcs[svc]?.[dsType]
          if (!fields) continue
          const fieldLines: string[] = []
          for (const [fKey, val] of Object.entries(fields)) {
            if (!val) continue
            const note = spec?.fields.find(f => f.key === fKey)?.secret ? '          # ⚠ secret' : ''
            fieldLines.push(`          ${fKey}: ${yamlStr(val)}${note}`)
          }
          if (fieldLines.length > 0) {
            epRows.push(`        - env: ${env.id}`)
            epRows.push(`          service: ${yamlStr(svc)}`)
            epRows.push(...fieldLines)
          }
        }
      }
      if (epRows.length > 0) {
        lines.push('      endpoints:')
        lines.push(...epRows)
      }
    }
  }

  // generation
  const skills = deriveSkillsWhitelist()
  lines.push('')
  lines.push('generation:')
  // output_dir 故意不写:CLI `tshoot gen` 才会读它,桌面 ImportAndDeploy 走 ~/.tshoot/...,
  // wizard 用户不需要;CLI 用户可以手动加这一行覆盖默认 ./dist。
  const selectedTargets = targetOptions.filter(t => enabledTargets[t])
  const targetList = selectedTargets.length ? selectedTargets : ['openclaw']
  lines.push('  targets:                             # 每个 target 产出一份机器人产物（同一份 system.yaml）')
  for (const t of targetList) {
    lines.push(`    - ${t}`)
  }
  lines.push('  skills_whitelist:                    # 只列出的 skill 会进工作区(留空 = 全开)')
  for (const s of skills) {
    lines.push(`    - ${s}`)
  }
  // preserve_on_regenerate 只对 openclaw target 生效:列表里的文件只存在于
  // ~/.openclaw/workspace/<id>/(由 SOUL.md / USER.md / CHECKLIST.md 模板产出),
  // claude-code / cursor 的产物不走这条 snapshot-restore 路径,emit 出来纯属噪音。
  if (enabledTargets['openclaw']) {
    lines.push('  preserve_on_regenerate:              # 二次 gen 时整体保留这些文件,让用户手改不丢(仅 openclaw)')
    lines.push('    - SOUL.md')
    lines.push('    - USER.md')
    lines.push('    - CHECKLIST.md')
  }

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
// ── 校验 ─────────────────────────────────────────────────────────────
// 以前是 nextStep 时单次 validate;现在改成 computed,每次字段变动立刻重算
// errors,模板里按 key 显示红框,按钮按 size 决定 disabled。
// 另外 validate 规则跟着向导结构升级:
//   Step 1:system.id / name(workspace_name / model 移到 Step 2)
//   Step 2:agent.name、≥1 个 target、勾 openclaw 要 workspace_name、
//          勾 openclaw/embedded 要对应 model
//   Step 3:env.id + api_domain
//   Step 4:每个 repo:name + (remote 要 url,local 要 _localPath)
//   Step 5:所选 type 的 non-optional 字段 per env 必填(optional 的可以留空让 install.sh 问)
//   Step 6/7:无硬校验

// errorLabels 把 error key 翻成人话,给按钮旁的"还差 N 项"提示用。
const errorLabels: Record<string, string> = {
  'system.id': '系统 ID',
  'system.name': '系统显示名',
  'agent.name': '机器人名称',
  'agent.workspace_name': 'OpenClaw 工作区名',
  'targets.none': '至少勾一个部署平台',
  'model.openclaw': 'OpenClaw 模型',
}
function labelForErrorKey(k: string): string {
  if (errorLabels[k]) return errorLabels[k]
  if (k.startsWith('env.')) {
    const [, i, field] = k.split('.')
    return `环境 #${Number(i) + 1} ${field === 'id' ? 'ID' : 'API 域名'}`
  }
  if (k.startsWith('repo.')) {
    const parts = k.split('.')
    const i = Number(parts[1]) + 1
    const f = parts[2]
    if (f === 'localPath') return `仓库 #${i} 本地目录`
    if (f === 'url') return `仓库 #${i} URL`
    return `仓库 #${i} ${f}`
  }
  if (k.startsWith('cc.')) {
    // 多源未分配:cc.unassigned.<service>
    if (k.startsWith('cc.unassigned.')) {
      const svcName = k.substring('cc.unassigned.'.length)
      return `服务 "${svcName}" 未分配源 —— 在某个源面板的"本环境包含的服务"里勾上`
    }
    // 细分:cc.<source>.<env>.scan / cc.<source>.<env>.namespace
    //      cc.<source>.<env>.svc.<service> / cc.<source>.<env>.<field>
    const parts = k.split('.')
    const source = parts[1]
    const envID = parts[2]
    const kind = parts[3]
    if (kind === 'scan') return `${envID} 环境(${source} 源)未预加载成功`
    if (kind === 'namespace') return `${envID} 环境(${source} 源)未选 namespace`
    if (kind === 'svc') {
      const svcName = parts[4]
      if (source === 'kuboard') return `${envID} 环境 "${svcName}" 服务未挑齐 集群/namespace/ConfigMap`
      return `${envID} 环境 "${svcName}" 服务未映射 dataId`
    }
    return `${source}.${envID}.${kind}`
  }
  if (k.startsWith('ds.')) {
    const parts = k.split('.')
    const last = parts[parts.length - 1]
    // ds.<env>.<svc>.notfetched / ds.<env>.<svc>.<dsKey>.probefail / .notested
    if (last === 'probefail') return `${parts[1]} 环境 "${parts[2]}" 服务的 ${parts[3]} 连通性失败`
    if (last === 'notested')  return `${parts[1]} 环境 "${parts[2]}" 服务的 ${parts[3]} 未测连通性`
    return `${parts[1]} 环境 "${parts[2]}" 服务配置未拉取 / 解析成功`
  }
  return k
}

// 当前步骤的错误集合:computed,字段改了立即重算
const currentStepErrors = computed<Set<string>>(() => {
  try {
    return computeStepErrors()
  } catch (e) {
    // 校验内部抛错(罕见,通常是某个 reactive 字段值异常)→ 返回空集合,避免阻塞用户。
    // 错误进日志,模板上让用户能继续(自由前进总比白屏强)。
    try { pushLog('cchub', 'error', `currentStepErrors 异常: ${String((e as any)?.message || e)}`) } catch {}
    return new Set<string>()
  }
})
function computeStepErrors(): Set<string> {
  const errs = new Set<string>()
  const step = currentStep.value
  if (step === 1) {
    // Step 1 = 欢迎页(导入 yaml / 从零开始),无强制校验。任意一条路径走完都让 nextStep。
  } else if (step === 2) {
    if (!system.id.trim()) errs.add('system.id')
    else if (!/^[a-z0-9][a-z0-9-]*$/.test(system.id)) errs.add('system.id')
    if (!system.name.trim()) errs.add('system.name')
  } else if (step === 3) {
    if (!agent.name.trim()) errs.add('agent.name')
    if (!anyTargetSelected.value) errs.add('targets.none')
    if (enabledTargets['openclaw']) {
      // workspace 目录名跟 agent.id 共用,system.id 非空就有自动派生值;不再单独校验
      if (!(targetModels['openclaw'] || '').trim()) errs.add('model.openclaw')
    }
  } else if (step === 4) {
    environments.forEach((e, i) => {
      if (!e.id.trim()) errs.add(`env.${i}.id`)
      if (!e.api_domain.trim()) errs.add(`env.${i}.api_domain`)
    })
  } else if (step === 5) {
    repos.forEach((r, i) => {
      if (!r.name.trim()) errs.add(`repo.${i}.name`)
      if (r._source === 'local') {
        if (!(r._localPath || '').trim()) errs.add(`repo.${i}.localPath`)
      } else {
        if (!r.url.trim()) errs.add(`repo.${i}.url`)
      }
    })
  } else if (step === 6) {
    // Step 5 校验 = 每个激活的源,只校验"有服务路由到本源"的 (env, svc) 组合。
    // 没服务挂上去的源/env 不强制填凭证(用户可以在 Step 4 把所有服务都挪到别的源)。
    // 多源场景下,任何服务都必须明确归属某个源(否则 Step 6 自动扫无从定位)。
    if (isMultiSource.value && allServiceNames.value.length > 0) {
      for (const svc of allServiceNames.value) {
        if (!getServiceSource(svc)) {
          errs.add(`cc.unassigned.${svc}`)
        }
      }
    }
    const primary = activeSourceTypes.value[0] || ''
    for (const t of activeSourceTypes.value) {
      const fields = CC_FIELDS_BY_TYPE.value[t] || []
      if (fields.length === 0) continue
      // 取该源的字段值:主源走 ccCredInputs,副源走 sourceCreds[t].creds
      const getField = (envID: string, fieldKey: string): string => {
        if (t === primary) return (ccCredInputs[ccKeyFor(t, envID, fieldKey)] || '').trim()
        return ((sourceCreds[t]?.creds?.[envID]?.[fieldKey]) || '').trim()
      }
      // 是否 kuboard:locator 走 per-service map,不校验 env 级 cluster/namespace/configmap 字段
      const isKuboard = t === 'kuboard'
      environments.forEach((e) => {
        if (!e.id.trim()) return
        // 本 env 是否有服务挂在本源 → 没有就跳过(不强制填没用的源)
        const svcsOnThisSource = allServiceNames.value.filter(svc => getServiceSource(svc) === t)
        if (svcsOnThisSource.length === 0) return
        // 1) 凭证字段必填(optional / kuboard 的 cluster/ns/cm 跳过)。
        //    showWhen 命中(如 kuboard 的 access_key / username+password 二选一)→ 隐藏的字段不算缺。
        for (const f of fields) {
          if (f.optional) continue
          if (f.uiOnly) continue
          if (isKuboard && (f.key === 'cluster' || f.key === 'namespace' || f.key === 'configmap')) continue
          if (isFieldHidden(t, e.id, f, (k) => getField(e.id, k))) continue
          if (!getField(e.id, f.key)) {
            errs.add(`cc.${t}.${e.id}.${f.key}`)
          }
        }
        if (isKuboard) {
          // kuboard:必须先 📥 拉取一次,然后每个服务都填齐 cluster/namespace/configmap
          const kbSt = kuboardStateByEnv[e.id]
          if (!kbSt || kbSt.status !== 'ok') {
            errs.add(`cc.${t}.${e.id}.scan`)
            return
          }
          for (const svc of svcsOnThisSource) {
            const loc = kuboardSvcMap[svcKey(e.id, svc)]
            if (!loc || !loc.cluster || !loc.namespace || !loc.configmap) {
              errs.add(`cc.${t}.${e.id}.svc.${svc}`)
            }
          }
        } else {
          // nacos / apollo / consul:走 ccHub 预加载 + namespace + per-svc dataId
          const st = ccHubStateByEnv[e.id]
          if (!st || st.status !== 'ok') {
            errs.add(`cc.${t}.${e.id}.scan`)
            return
          }
          if (!(e.id in envNamespaces)) {
            errs.add(`cc.${t}.${e.id}.namespace`)
            return
          }
          for (const svc of svcsOnThisSource) {
            if (!(serviceConfigSel[svcKey(e.id, svc)] || '').trim()) {
              errs.add(`cc.${t}.${e.id}.svc.${svc}`)
            }
          }
        }
      })
    }
  } else if (step === 7) {
    // 数据层校验:
    //   1) 配置拉取必须 ok / empty(skipped/error 拦);kuboard 也走自动扫(KuboardFetchConfigMaps),
    //      所以跟 nacos/apollo/consul 一样要求 ok|empty;env-vars/none 没自动扫,跳过此项校验。
    //   2) 每个识别出的组件都必须做过连通性测试且通过
    //      没测过 → 拦(notested);测过失败 → 拦(probefail);测过通过 → 放行
    //      用户嫌某组件用不上 → 点 ✕ 删掉,删后就不在校验范围里了
    const autoFetchSources = new Set(['nacos', 'apollo', 'consul', 'kuboard'])
    for (const e of environments) {
      if (!e.id.trim()) continue
      for (const svc of allServiceNames.value) {
        const svcSource = getServiceSource(svc)
        if (!svcSource) continue // 没分配源 — Step 5 校验已拦,这里不重复报
        if (!autoFetchSources.has(svcSource)) continue // 不自动扫的源 → 不强制 fetched
        const st = scanStateOf(e.id, svc)
        if (!st || (st.status !== 'ok' && st.status !== 'empty')) {
          errs.add(`ds.${e.id}.${svc}.notfetched`)
        }
      }
      const svcs = scannedDS[e.id]
      if (svcs) {
        for (const svc of Object.keys(svcs)) {
          const byKey = svcs[svc] || {}
          for (const dsKey of Object.keys(byKey)) {
            const probeSt = dsProbeResults[probeKey(e.id, svc, dsKey)]
            if (!probeSt || probeSt.status !== 'ok') {
              if (probeSt?.status === 'fail') {
                errs.add(`ds.${e.id}.${svc}.${dsKey}.probefail`)
              } else {
                errs.add(`ds.${e.id}.${svc}.${dsKey}.notested`)
              }
            }
          }
        }
      }
    }
  }
  return errs
}

// 能不能点"下一步":当前步无 error + 不是最后一步
const canGoNext = computed(() => currentStepErrors.value.size === 0 && currentStep.value < totalSteps)
// 给按钮 title 用的"还差什么"提示
const nextBlockedHint = computed(() => {
  if (canGoNext.value) return ''
  const items = Array.from(currentStepErrors.value).slice(0, 5).map(labelForErrorKey)
  const more = currentStepErrors.value.size - items.length
  return `还差 ${currentStepErrors.value.size} 项:${items.join(' / ')}${more > 0 ? ` (+${more})` : ''}`
})

// 保留原 Set 给模板 .error 类(hasError)使用;每次 computed 触发就同步
watch(currentStepErrors, (newErrs) => {
  validationErrors.value = newErrs
}, { immediate: true })

function hasError(field: string): boolean {
  return validationErrors.value.has(field)
}

// nextStep / goToStep 不再内联调 generateYAML —— 已被 watch(currentStep) 接管(见上面),
// 那个 watch 带 try/catch 兜底,不会让一次抛错把整个 InitPage 渲染挂掉(老路径会白屏)。
//
// 越界保护:无论怎么进入,都把 currentStep 钳在 [1, totalSteps] —— 防止异常状态(比如 saved
// draft 损坏)让 v-if 全部 false 导致内容区白屏。
function clampCurrentStep() {
  if (typeof currentStep.value !== 'number' || isNaN(currentStep.value)) {
    currentStep.value = 1
    return
  }
  if (currentStep.value < 1) currentStep.value = 1
  else if (currentStep.value > totalSteps) currentStep.value = totalSteps
}

function nextStep() {
  try {
    if (!canGoNext.value) return
    if (currentStep.value < totalSteps) {
      currentStep.value++
    }
    clampCurrentStep()
  } catch (e) {
    pushLog('cchub', 'error', `nextStep 失败: ${String((e as any)?.message || e)}`)
    clampCurrentStep()
  }
}

function prevStep() {
  try {
    // 回退不校验,自由退
    if (currentStep.value > 1) currentStep.value--
    clampCurrentStep()
  } catch (e) {
    pushLog('cchub', 'error', `prevStep 失败: ${String((e as any)?.message || e)}`)
    clampCurrentStep()
  }
}

function goToStep(step: number) {
  try {
    // 倒退随意;前进必须当前步无 error
    if (step < currentStep.value) {
      currentStep.value = step
    } else if (step > currentStep.value && canGoNext.value) {
      // 允许跳多步,但中间每步都得满足(这里只检查当前步;严谨版可以逐步 validate,先简单化)
      currentStep.value = step
    }
    clampCurrentStep()
  } catch (e) {
    pushLog('cchub', 'error', `goToStep(${step}) 失败: ${String((e as any)?.message || e)}`)
    clampCurrentStep()
  }
}

// 防白屏兜底:子组件 / step 模板渲染抛错时,Vue 默认把整个 InitPage 子树清空,
// 用户只看见侧栏 + 一片白。捕获后展示明确错误 + 提供"回到 Step 1"按钮自救,
// 同时把错误 push 到日志面板,便于事后排查。返回 false 阻止错误向上传播 unmount 父级。
const renderError = ref<{ message: string, stack?: string, info?: string, step: number } | null>(null)
onErrorCaptured((err: any, _vm, info) => {
  const msg = String(err?.message || err)
  console.error('[InitPage] render error captured:', err, 'info=', info)
  renderError.value = {
    message: msg,
    stack: err?.stack ? String(err.stack).split('\n').slice(0, 8).join('\n') : undefined,
    info: String(info || ''),
    step: currentStep.value,
  }
  try { pushLog('cchub', 'error', `[InitPage step ${currentStep.value}] 渲染异常: ${msg}`) } catch {}
  // 自动把 step 钳到合法范围,允许用户至少能看到下一次渲染
  clampCurrentStep()
  return false
})
function dismissRenderError() {
  renderError.value = null
}
function recoverToStep1() {
  renderError.value = null
  currentStep.value = 1
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

async function downloadYAML() {
  const filename = 'system.yaml'
  try {
    const path = await exportYAML(filename, yamlOutput.value)
    if (!path) {
      // 用户取消(桌面 app 走 SaveYAML 时返回空串)
      return
    }
    if (isDesktop()) {
      toast.success(`已导出到 ${path}`)
    } else {
      toast.success(`已下载 ${path}`)
    }
  } catch (e: any) {
    toast.error(`导出失败: ${String(e?.message || e)}`)
  }
}

// ── 一键部署 ──
// 之前走完向导只能下 yaml → 跳 BotsPage → 导入 → 选 target → 填路径,4 步。
// 现在 Step 8 直接遍历 Step 2 已勾选的所有 target 一键部署:每个 target 走
// importAndDeploy(复用 BotsPage 那条闭环)装到自动路径 ~/.tshoot/<target>/<id>/,
// 全部成功后跳 /bots 看刚装好的卡。
const deployLoading = ref(false)
const deployError = ref<string | null>(null)

// 部署路径展示:Step 2 卡片要让用户看到"AI 平台最终从哪儿读 agent",
// 因此这里展示的是 install.sh 跑完后的最终落地路径,不是中间包路径。
//   - openclaw     ~/.openclaw/workspace/<workspace_name>/
//   - claude-code  ~/.claude/agents/<name>.md(<name> = workspace_name 兜底 system.id-bot)
//   - cursor       ~/.cursor/agents/<name>.md
// 中间包 ~/.tshoot/<target>/<id>/ 由 defaultDestPath 给后端用,这里只为 UI 提示。
// homeDir 已在前面声明(getUserConfig 拿的),空字符串时回退 "~" 给用户看。

// agent 名:workspace_name 优先,否则 system.id-bot 兜底,否则 my-system-bot
const agentNameForPath = computed(() => (
  agent.workspace_name.trim() || (system.id ? `${system.id}-bot` : 'my-system-bot')
))

const targetDeployPaths = computed<Record<string, string>>(() => {
  const home = homeDir.value || '~'
  const wsName = agentNameForPath.value
  // 用户手选的自定义根目录优先,没有就用默认 ~/.<target>
  const rootFor = (t: string, def: string) => (customInstallRoots[t] || '').trim() || def
  return {
    'openclaw': `${rootFor('openclaw', `${home}/.openclaw`)}/workspace/${wsName}/`,
    'claude-code': `${rootFor('claude-code', `${home}/.claude`)}/agents/${wsName}.md`,
    'cursor': `${rootFor('cursor', `${home}/.cursor`)}/agents/${wsName}.md`,
    'codex': `${rootFor('codex', `${home}/.codex`)}/agents/${wsName}.md`,
  }
})

// 鼠标悬停"自动"标签时提示:这个路径是该 AI 平台官方约定的 agent 读取位置,
// 不是 Studio 自己塞的;改路径只能改 workspace_name(回 Step 1 改 system.id)。
const targetDeployPathHints: Record<string, string> = {
  'openclaw': 'OpenClaw 启动时扫 ~/.openclaw/workspace/* 列出可用 agent,选一个进入。',
  'claude-code': 'Claude Code 启动时读 ~/.claude/agents/*.md(用户级 subagent),所有项目都能 @<name> 调用。',
  'cursor': 'Cursor 启动时读 ~/.cursor/agents/*.md(用户级 Custom Agent),侧栏选用。',
  'codex': 'OpenAI Codex CLI 启动时读 ~/.codex/agents/*.md,CLI 内 @<name> 调用。',
}

// Step 8 一键部署摘要:Step 2 勾了哪些 target → 渲染对应路径
const deploySummary = computed(() =>
  targetOptions
    .filter(t => enabledTargets[t])
    .map(t => ({ target: t, label: targetLabels[t] || t, path: targetDeployPaths.value[t] || '' })),
)

// 拼出跟 Go 端 envVar() 一致的 install env 变量名。Go 的形态:
//   - sourceID 为 "" / "default" → "<PREFIX>_<ENV>"(老 single-source 兼容)
//   - 显式多源 → "<PREFIX>_<SOURCE>_<ENV>"
// 注:wizard yaml emit 的 placeholder 顺序是反的(env 在前),但 install_native_openclaw
// 通过 envVar() 查 creds 走的是 Go 这套,所以预填 creds map 必须用 Go 这套。
function installEnvVarName(prefix: string, sourceID: string, envID: string): string {
  let base = prefix + '_'
  if (sourceID && sourceID !== 'default') {
    base += sourceID.toUpperCase().replace(/-/g, '_') + '_'
  }
  return base + envID.toUpperCase()
}

// 把 wizard 已填的所有凭证拼成 install.sh / RunInstall 用的 creds map。
// 命名严格匹 install_naming.go 的 envVar();值从 sourceCreds + toolInputs 直接读。
// 这是把"已填一次"打通到"OpenClaw 部署即可跑"的关键 —— 不再去 BotsPage 二次输入。
function buildOpenclawCreds(): Record<string, string> {
  const creds: Record<string, string> = {}
  const isMulti = activeSourceTypes.value.length > 1

  // ── 配置中心:每个激活源 × 每个 env ──
  for (const t of activeSourceTypes.value) {
    const cc = sourceCreds[t]
    if (!cc) continue
    const sourceID = isMulti ? t : 'default'
    for (const env of environments) {
      if (!env.id) continue
      const envCreds = cc.creds[env.id] || {}
      const put = (prefix: string, val: string) => {
        if ((val || '').trim()) creds[installEnvVarName(prefix, sourceID, env.id)] = val.trim()
      }
      switch (t) {
        case 'nacos':
          put('CC_ADDR', envCreds.addr)
          put('CC_USER', envCreds.username)
          put('CC_PASS', envCreds.password)
          break
        case 'apollo':
          put('APOLLO_META', envCreds.meta_url)
          put('APOLLO_TOKEN', envCreds.token)
          break
        case 'consul':
          put('CONSUL_HOST', envCreds.host)
          put('CONSUL_TOKEN', envCreds.token)
          break
        case 'kuboard':
          put('KUBOARD_URL', envCreds.url)
          put('KUBOARD_USER', envCreds.username)
          put('KUBOARD_PASS', envCreds.password)
          put('KUBOARD_ACCESS_KEY', envCreds.access_key)
          break
        case 'env-vars':
          // 数据层静态连接串:STATIC_<TYPE>_<env> per enabled data store
          for (const [dsType, on] of Object.entries(enabledDataStores)) {
            if (!on) continue
            const fkey = `static_${dsType}`
            put(`STATIC_${dsType.toUpperCase()}`, (envCreds[fkey] || ''))
          }
          break
      }
    }
  }

  // ── 可观测性:工具规格里 envVar() 已经是 install 名(系统级,不带 source 前缀)──
  for (const tool of OBS_TOOL_SPECS) {
    if (!enabledObservability[tool.key]) continue
    for (const env of environments) {
      if (!env.id) continue
      for (const f of tool.fields) {
        // uiOnly(如 auth_mode)不喂 install 凭证;showWhen 命中隐藏的字段也跳过(避免把
        // 用户填过又切换鉴权方式后残留的旧值灌进去)。
        if (f.uiOnly) continue
        if (isObsFieldHidden(tool.key, env.id, f)) continue
        const v = (toolInputs[toolKeyFor('obs', tool.key, env.id, f.key)] || '').trim()
        if (v) creds[f.envVar(env.id)] = v
      }
    }
  }

  // ── ELK 共享凭证(install_prompts 把 ELK_USERNAME/PASSWORD 当 system-wide 共用)──
  if (enabledObservability['elk']) {
    // 取第一个 env 填的当共用值(各 env 一般一样;UI 没拆出"system-wide"输入区)
    for (const env of environments) {
      if (!env.id) continue
      const u = (toolInputs[toolKeyFor('obs', 'elk', env.id, 'user')] || '').trim()
      const p = (toolInputs[toolKeyFor('obs', 'elk', env.id, 'pass')] || '').trim()
      if (u && !creds['ELK_USERNAME']) creds['ELK_USERNAME'] = u
      if (p && !creds['ELK_PASSWORD']) creds['ELK_PASSWORD'] = p
    }
  }

  // ── Agent 模型 ──
  const model = (targetModels['openclaw'] || agent.model || '').trim()
  if (model) creds['MODEL'] = model

  // ── messaging:lark / feishu_project ──
  if (toolInputs['msg:lark:app_id']) creds['LARK_APP_ID'] = toolInputs['msg:lark:app_id']
  if (toolInputs['msg:lark:app_secret']) creds['LARK_APP_SECRET'] = toolInputs['msg:lark:app_secret']
  if (toolInputs['pt:feishu_project:user_token']) creds['MCP_USER_TOKEN'] = toolInputs['pt:feishu_project:user_token']

  return creds
}

// 一键部署:遍历 Step 2 已勾选的所有 target,各自走 importAndDeploy。
// 路径全自动,无需用户在 Step 8 再选 target / 选目录(都用 ~/.tshoot/<target>/<id>/)。
// 任一 target 部署失败 → 整体停下保留已成功的,error 里显示是哪个 target 倒了。
async function runOneClickDeploy() {
  deployError.value = null
  if (!isDesktop()) {
    deployError.value = '一键部署只在桌面 app 可用;浏览器模式请下载 yaml 去 BotsPage 或用 CLI'
    return
  }
  const enabled = targetOptions.filter(t => enabledTargets[t])
  if (enabled.length === 0) {
    deployError.value = 'Step 2 没勾选任何部署目标'
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
    // 构造 repoPaths(三个 target 共用同一份本机仓库路径表)
    const repoPaths: Record<string, string> = {}
    const effectiveRoot = reposRootInput.value.trim() || resolvedReposRoot.value
    for (const r of repos) {
      if (!r.name.trim()) continue
      let path = ''
      if (r._source === 'local') {
        path = (r._localPath || '').trim()
      } else {
        // _cloneTarget 是父目录,实际仓库路径要拼上 repo.name
        path = resolveCloneDest(r)
        if (!path && effectiveRoot) {
          path = `${effectiveRoot.replace(/\/$/, '')}/${r.name}`
        }
      }
      if (path) repoPaths[r.name] = path
    }

    // 每个勾选的 target:
    //   - claude-code / cursor:importAndDeploy 内部已 native install 到 ~/.claude|cursor/,
    //     跑完即生效,无须二次操作
    //   - openclaw:importAndDeploy 出中间包,**自动**用 wizard 已填凭证调 runInstall
    //     完成 workspace 安装 + creds.json + openclaw.json 注入,跑完即生效。
    //     如果有字段没填(用户在 Step 5/7 留空了),就 fallback 到 BotsPage 让用户补全。
    const installedTargets: string[] = []
    const stagedOnly: string[] = []
    const openclawCreds = buildOpenclawCreds()
    for (const t of enabled) {
      const dest = await defaultDestPath(t, system.id || '')
      // 同一份 creds 顺带传给 claude-code/cursor:installNative 走完文件拷贝后会用它
      // 注入 ~/.claude/settings.json / ~/.cursor/mcp.json 的 mcpServers,装完即可用 MCP 工具。
      // openclaw 的自定义目录走 openclawInstallDir 那条独立 UI;这里只对 ide 三家生效
      const cir = (t === 'claude-code' || t === 'cursor' || t === 'codex')
        ? (customInstallRoots[t] || '').trim()
        : ''
      await importAndDeploy(yamlOutput.value, t, dest, repoPaths, openclawCreds, cir)
      if (t === 'claude-code' || t === 'cursor' || t === 'codex') {
        installedTargets.push(t)
        continue
      }
      // openclaw:用 wizard 已填的凭证直接 RunInstall 完成全部安装
      try {
        const r = await runInstall(dest, openclawCreds)
        if (r && r.ok) {
          installedTargets.push(t)
        } else {
          stagedOnly.push(t)
          pushLog('install', 'warn', `[${t}] auto-install 失败,保留中间包待手动完成: ${r?.log?.slice(-200) || ''}`)
        }
      } catch (e: any) {
        stagedOnly.push(t)
        pushLog('install', 'warn', `[${t}] auto-install 异常,保留中间包: ${String(e?.message || e)}`)
      }
    }
    // 部署完自动跑一次 self-test,把端点 ping 结果反馈给用户(只对 openclaw 跑;
    // claude-code/cursor 的 self-test 还没适配,跳过避免误报"openclaw.json 缺失")。
    const openclawDest = installedTargets.includes('openclaw')
      ? await defaultDestPath('openclaw', system.id || '')
      : ''
    let selfTestSummary = ''
    if (openclawDest) {
      try {
        const st = await selfTestAgent(openclawDest)
        const failCount = (st.checks || []).filter(c => c.status === 'FAIL').length
        const warnCount = (st.checks || []).filter(c => c.status === 'WARN').length
        const passCount = (st.checks || []).filter(c => c.status === 'PASS').length
        if (failCount > 0) {
          const fails = (st.checks || []).filter(c => c.status === 'FAIL')
            .map(c => `${c.name}: ${c.detail?.slice(0, 60) || ''}`).join('; ')
          selfTestSummary = `🩺 自检 ${passCount}✓ ${warnCount}⚠ ${failCount}✗ → ${fails}`
          pushLog('install', 'error', `[self-test] ${failCount} 项失败: ${fails}`)
        } else if (warnCount > 0) {
          selfTestSummary = `🩺 自检 ${passCount}✓ ${warnCount}⚠ 0✗(警告项不阻塞)`
        } else {
          selfTestSummary = `🩺 自检 ${passCount}✓ 全绿`
        }
      } catch (e: any) {
        pushLog('install', 'warn', `[self-test] 跑不起来: ${String(e?.message || e)}`)
      }
    }

    if (stagedOnly.length > 0) {
      toast.success(`已就绪:${installedTargets.join(' / ') || '无'};需补凭证:${stagedOnly.join(' / ')}(到「已装机器人」页完成)`)
    } else {
      const tail = selfTestSummary ? `\n${selfTestSummary}` : ''
      toast.success(`部署完成,共 ${installedTargets.length} 个目标已生效:${installedTargets.join(' / ')}${tail}`)
    }
    router.push('/bots')
  } catch (e: any) {
    deployError.value = String(e?.message || e)
  } finally {
    deployLoading.value = false
  }
}

// stack 的枚举集合:原本给 Step 4 的下拉 select 用,现在改成自动识别 readonly badge,
// 但 generateYAML 写注释时还提示合法值,留着当"文档"引用。
const _stackOptions = ['go', 'java', 'node', 'php', 'python']
void _stackOptions
const configTypeOptions = ['nacos', 'apollo', 'consul', 'env-vars', 'kuboard', 'none']

const configTypeDescriptions: Record<string, string> = {
  nacos: 'Nacos — 配置与服务发现中心(阿里巴巴开源)',
  apollo: 'Apollo — 分布式配置中心(携程开源)',
  consul: 'Consul KV — HashiCorp 键值存储',
  'env-vars': '环境变量 / .env 文件 — 不使用远程配置中心',
  kuboard: 'Kuboard — 通过 Kuboard 后台读 K8s ConfigMap,无需 kubeconfig',
  none: '不使用任何配置源',
}
</script>

<template>
  <div class="init-page">
    <!-- 渲染错误兜底:onErrorCaptured 抓到子组件 / step 模板抛错时,在向导顶部展示
         明确的错误信息 + 自救按钮,而不是默认 Vue 行为(整页清空成白屏)。 -->
    <div v-if="renderError" class="render-error-banner">
      <div class="render-error-head">
        ⚠ Step {{ renderError.step }} 渲染异常 — 已阻止白屏,可点下方按钮自救
      </div>
      <div class="render-error-msg">{{ renderError.message }}</div>
      <pre v-if="renderError.stack" class="render-error-stack">{{ renderError.stack }}</pre>
      <div class="render-error-actions">
        <button type="button" class="btn" @click="recoverToStep1">↺ 回到 Step 1</button>
        <button type="button" class="btn" @click="dismissRenderError">关闭(我知道了)</button>
      </div>
    </div>
    <!-- 顶部信息卡(标题 + 自动保存 + 简介 + 进度条)合并成一张 card,
         视觉风格跟下面 step 卡片一致(白底 + border + radius + padding),
         避免裸标题 + 裸 info-box + 裸进度条三段不齐的"散装"感。 -->
    <div class="card lg init-header-card">
      <div class="page-header">
        <div>
          <h1>初始化向导</h1>
          <p class="subtitle">通过可视化表单生成 system.yaml 配置文件(草稿会自动保存到本地)</p>
        </div>
        <div class="header-actions">
          <!-- 自动保存徽章:让用户感知到"改动一直在存"(类似 Notion/Google Docs 的风格) -->
          <span class="autosave-badge" :class="{ idle: lastSavedAt === null }" :title="lastSavedAt === null ? '尚未触发自动保存;做任何改动后会自动保存到浏览器 localStorage' : '草稿存在浏览器 localStorage,切换页面不丢;清空草稿按钮可重置'">
            <span class="autosave-dot" />
            {{ lastSavedAt === null ? '草稿空' : `✓ 自动保存 · ${savedAgoLabel}` }}
          </span>
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
            上传或粘贴现有 system.yaml 内容,字段会自动反填到各步骤。
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

      <!-- Guidance info box(嵌在 header 卡里,info-box 的浅蓝边框 + 卡片白底叠出层级) -->
      <div class="info-box init-header-info">
        <p><strong>本向导帮助你快速生成 system.yaml 配置文件</strong></p>
        <p>system.yaml 描述你的系统架构(仓库、环境、配置中心、基础组件),tshoot 据此生成并部署定制化的 AI 排障机器人</p>
        <p>完成后可「验证」确保格式正确,然后「下载」到本地</p>
      </div>

      <!-- Step indicator(8 步骤进度条,跟标题 + info-box 同 card) -->
      <div class="step-indicator init-header-progress">
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
    </div>

    <!-- Step 1: 欢迎页 - 选导入 yaml 还是从零开始 -->
    <div v-if="currentStep === 1" class="card lg welcome-card">
      <h2>开始创建机器人</h2>
      <p class="help-text" style="margin-bottom: 24px;">
        本向导帮你生成 system.yaml 配置 + 一键部署到 OpenClaw / Claude Code / Cursor / Codex CLI。
        选下方任一方式开始 —— 草稿会自动保存,中途可关闭 app 下次继续。
      </p>
      <div class="welcome-choices">
        <button
          type="button"
          class="welcome-choice"
          @click="goToStep(2)"
        >
          <div class="welcome-choice-icon">✏️</div>
          <div class="welcome-choice-text">
            <strong>从零开始配置</strong>
            <span>按表单一步步填,默认值已经按主流约定预设好</span>
          </div>
        </button>
        <button
          type="button"
          class="welcome-choice"
          @click="openImportDialog"
        >
          <div class="welcome-choice-icon">📥</div>
          <div class="welcome-choice-text">
            <strong>导入已有 system.yaml</strong>
            <span>有 yaml 文件直接反填到向导,继续编辑调整</span>
          </div>
        </button>
      </div>
    </div>

    <!-- Step 2: 系统基本信息(原 Step 1) -->
    <div v-if="currentStep === 2" class="card lg">
      <h2>系统基本信息</h2>
      <p class="help-text" style="margin-bottom:14px">
        填一下机器人服务的业务系统:展示名、ID、一句话描述。
      </p>

      <div class="form-group">
        <label>系统显示名 <span class="required">*</span>
          <span class="field-hint">— 机器人打招呼 / 文档标题都用这个(可中文)</span>
        </label>
        <input
          v-model="system.name"
          type="text"
          placeholder="我的系统"
          :class="{ error: hasError('system.name') }"
        />
        <span v-if="hasError('system.name')" class="error-text">必填</span>
      </div>

      <!-- 系统 ID:默认自动从 system.name 派生,不让用户操心;仅在派生失败
           (纯中文名)或用户点"自定义 ID"后才露输入框 -->
      <div class="form-group">
        <label>
          系统 ID
          <span class="help-icon" title="机器可读标识(ASCII),用作目录名、agent id 前缀、MCP 实例名。默认从「系统显示名」自动派生(Shop → shop)。纯中文名派生不出来时会露出手填输入框。">?</span>
          <span v-if="idManualOverride" class="field-hint">(已手改,改完不再跟随系统名)</span>
          <span v-else-if="idCanAutoDerive" class="field-hint">— 自动从系统名派生</span>
          <span v-else-if="idAutoFailed" class="field-hint" style="color:#b45309">— 系统名全是中文,派生不出,请手填</span>
        </label>

        <!-- 自动派生成功且用户没手改:readonly 小 badge + "自定义" 链接 -->
        <div v-if="!idManualOverride && idCanAutoDerive" class="id-autoderive">
          <code class="id-badge">{{ system.id || '(填完系统名后自动生成)' }}</code>
          <button type="button" class="btn-link" @click="markIdManual">自定义 ID →</button>
        </div>

        <!-- 手改模式 / 派生失败:露出输入框 -->
        <div v-else>
          <div class="id-input-row">
            <input
              v-model="system.id"
              type="text"
              placeholder="my-system (仅小写字母/数字/短横线,首字符 [a-z0-9])"
              :class="{ error: hasError('system.id') }"
              @input="markIdManual"
            />
            <button
              v-if="idManualOverride && idCanAutoDerive"
              type="button"
              class="btn-link"
              @click="resetIdAuto"
              title="恢复：从系统名自动派生"
            >↺ 跟随系统名</button>
          </div>
          <span v-if="hasError('system.id')" class="error-text">仅允许 [a-z0-9-],首字符必须是字母或数字</span>
        </div>
      </div>

      <div class="form-group">
        <label>系统描述</label>
        <textarea v-model="system.description" placeholder="一句话描述你的系统（选填）" rows="3" />
      </div>
    </div>

    <!-- Step 2 -->
    <div v-if="currentStep === 3" class="card lg">
      <h2>机器人身份</h2>
      <p class="help-text" style="margin-bottom:14px">
        给机器人起个名字,选要部署到哪些 AI 平台。
      </p>
      <div class="form-group">
        <label>机器人名称 <span class="required">*</span></label>
        <input
          v-model="agent.name"
          type="text"
          :placeholder="agentNameDefault"
          :class="{ error: hasError('agent.name') }"
        />
      </div>

      <!-- agent.id:AI 平台里的稳定标识(OpenClaw agents.list[*].id / Claude Code / Cursor subagent 名),
           同时也作为 OpenClaw workspace 目录名(~/.openclaw/workspace/<id>/)。
           跟随 system.id 自动派生,只读;想改请回 Step 1 改 system.id。 -->
      <div class="form-group">
        <label>
          AI 平台标识
          <span class="help-icon" title="OpenClaw agents.list[*].id + workspace 目录名;Claude Code / Cursor 的 subagent 名。从 system.id 派生(<system.id>-troubleshooter)。">?</span>
          <span class="auto-tag">自动派生</span>
        </label>
        <input
          :value="agent.id || agentIdDefault"
          type="text"
          readonly
          class="readonly-input"
          title="只读;跟随 system.id 自动派生"
        />
      </div>

      <!-- 部署平台卡片:每家一张,勾选的卡片内联露出该 target 相关配置(模型 / 工作区名)。
           claude-code / cursor 不消费模型,只展示"模型由用户客户端自己选"。
           openclaw 是唯一需要工作区名的,勾选时多一行输入框。 -->
      <div class="form-group">
        <label>
          部署到哪些 AI 平台 <span class="required">*</span>
          <span class="field-hint">— 可多选;勾了哪些,相关配置(模型 / 工作区)就展开填</span>
        </label>
        <div class="target-grid">
          <div
            v-for="t in targetOptions"
            :key="t"
            class="target-card"
            :class="{ selected: enabledTargets[t], 'target-disabled': targetDetectedInstalled(t) === false && !forceEnableMissingTarget[t] }"
          >
            <label class="target-card-head">
              <input
                type="checkbox"
                v-model="enabledTargets[t]"
                :disabled="!targetCanBeEnabled(t)"
                :title="!targetCanBeEnabled(t) ? '本机未检测到该 AI 平台,先安装或点下方「我已自行安装」再勾选' : ''"
              />
              <span class="target-title">{{ targetLabels[t] }}</span>
              <span
                v-if="t === 'claude-code' && aitoolsResult"
                class="target-install-badge"
                :class="aitoolsResult.claude_code.installed ? 'ok' : 'miss'"
                :title="aitoolsResult.claude_code.note || aitoolsResult.claude_code.path || ''"
              >
                {{ aitoolsResult.claude_code.installed
                  ? `✓ v${aitoolsResult.claude_code.version || '?'}`
                  : '⚠ 未检测到' }}
              </span>
              <span
                v-else-if="t === 'cursor' && aitoolsResult"
                class="target-install-badge"
                :class="aitoolsResult.cursor.installed ? 'ok' : 'miss'"
                :title="aitoolsResult.cursor.note || aitoolsResult.cursor.path || ''"
              >
                {{ aitoolsResult.cursor.installed
                  ? `✓ v${aitoolsResult.cursor.version || '?'}`
                  : '⚠ 未检测到' }}
              </span>
              <span
                v-else-if="t === 'codex' && aitoolsResult"
                class="target-install-badge"
                :class="aitoolsResult.codex.installed ? 'ok' : 'miss'"
                :title="aitoolsResult.codex.note || aitoolsResult.codex.path || ''"
              >
                {{ aitoolsResult.codex.installed
                  ? `✓ v${aitoolsResult.codex.version || '?'}`
                  : '⚠ 未检测到' }}
              </span>
              <span
                v-else-if="t === 'openclaw' && openclawDetectStatus !== 'idle'"
                class="target-install-badge"
                :class="openclawDetectStatus === 'ok' ? 'ok' : 'miss'"
                :title="openclawDetectError || ''"
              >
                <template v-if="openclawDetectStatus === 'ok'">
                  {{ openclawVersion ? `✓ v${openclawVersion}` : '✓ 已装' }}
                </template>
                <template v-else-if="openclawDetectStatus === 'loading'">扫描中…</template>
                <template v-else>⚠ 未检测到</template>
              </span>
            </label>
            <div class="target-hint">{{ targetDescriptions[t] }}</div>
            <!-- 未检测到 + 没强制启用 → 露出"我已自行安装/选目录/重新扫描"操作条;
                 强制启用后 checkbox 解锁,部署位置改用 customInstallRoots[t] 拼接(若选了自定义)。 -->
            <div
              v-if="t !== 'openclaw' && targetDetectedInstalled(t) === false && !forceEnableMissingTarget[t]"
              class="target-missing-actions"
            >
              <span>本机未找到 {{ targetLabels[t] }} —— 先安装,或</span>
              <button type="button" class="btn-link" @click="forceEnableMissingTarget[t] = true">
                我已自行安装,继续
              </button>
              <button type="button" class="btn-link" @click="pickCustomInstallRoot(t)">📁 选安装目录…</button>
              <button type="button" class="btn-link" @click="refreshAITools">🔄 重新扫描</button>
            </div>
            <div
              v-else-if="t !== 'openclaw' && targetDetectedInstalled(t) === false && forceEnableMissingTarget[t]"
              class="target-missing-actions overridden"
            >
              <span v-if="customInstallRoots[t]">📁 自定义安装目录:</span>
              <span v-else>⚠ 未检测到本机安装,已强制启用(默认部署 ~/.{{ t }})</span>
              <code v-if="customInstallRoots[t]" :title="customInstallRoots[t]">{{ customInstallRoots[t] }}</code>
              <button type="button" class="btn-link" @click="pickCustomInstallRoot(t)">
                {{ customInstallRoots[t] ? '改目录…' : '📁 选安装目录…' }}
              </button>
              <button v-if="customInstallRoots[t]" type="button" class="btn-link" @click="clearCustomInstallRoot(t)">清除</button>
              <button
                type="button"
                class="btn-link"
                @click="() => { forceEnableMissingTarget[t] = false; enabledTargets[t] = false; clearCustomInstallRoot(t) }"
              >撤销</button>
              <button type="button" class="btn-link" @click="refreshAITools">🔄 重新扫描</button>
            </div>
            <!-- 勾选后展示 install.sh 跑完后的最终落地位置 —— AI 平台从这里读 agent。
                 路径长时省略号截断,鼠标悬停看完整;Step 8 一键部署也不再问路径。 -->
            <div v-if="enabledTargets[t]" class="target-deploy-path">
              <span class="target-deploy-path-label">部署位置</span>
              <span class="auto-tag" :title="targetDeployPathHints[t]">自动</span>
              <code :title="targetDeployPaths[t]">{{ targetDeployPaths[t] || '…' }}</code>
            </div>

            <!-- 勾选后才展开下面配置区。claude-code / cursor 没有要配的字段,
                 直接不渲染 target-body,免得露出空白容器很难看 -->
            <div v-if="enabledTargets[t] && t === 'openclaw'" class="target-body">
              <!-- OpenClaw 模型:只从本地 openclaw 配置读,不给手填回路。
                   原因:openclaw gateway 只认自己 config.yaml 里声明过的 model id,
                   Studio 让用户填一个 openclaw 不认的 id 部署完 gateway 就跑不动 ——
                   不如如实告知"请先装 openclaw 配置模型再来"。
                   API key 也不在这里收:openclaw 的凭证走自家 install.sh 交互流程,
                   跟 Studio 的 keychain 不是一回事。 -->
              <template v-if="t === 'openclaw'">
                <div v-if="openclawDetectStatus === 'loading'" class="target-field target-note">
                  <span class="scan-spinner-mini"></span>正在读 OpenClaw 配置…
                </div>
                <div v-else-if="openclawDetectStatus === 'not-installed'" class="target-field openclaw-warn">
                  <div>⚠ 本机未检测到 OpenClaw 安装(默认找 <code>~/.openclaw</code>)</div>
                  <div style="margin-top:4px">
                    请先安装 OpenClaw 并配置好 <code>config.yaml</code> 里的
                    <code>models:</code> 字段,然后回来点"重新扫描";
                    或者手动选择 OpenClaw 安装目录。
                  </div>
                  <div class="openclaw-warn-actions">
                    <button type="button" class="btn" @click="runOpenClawDetect('')">🔄 重新扫描</button>
                    <button type="button" class="btn" @click="pickOpenClawInstallDir">选择安装目录…</button>
                  </div>
                </div>
                <div v-else-if="openclawDetectStatus === 'error'" class="target-field openclaw-warn">
                  <div>✗ 读 OpenClaw 配置失败: {{ openclawDetectError }}</div>
                  <div class="openclaw-warn-actions">
                    <button type="button" class="btn" @click="pickOpenClawInstallDir">改选目录…</button>
                    <button type="button" class="btn" @click="runOpenClawDetect(openclawInstallDir)">重试</button>
                  </div>
                </div>
                <div v-else-if="openclawDetectStatus === 'ok' && openclawDetectedModels.length > 0" class="target-field">
                  <label class="target-field-label">
                    使用的模型
                    <span class="auto-tag">读自 {{ openclawResolvedDir }}{{ openclawVersion ? ` · v${openclawVersion}` : '' }}</span>
                    <button type="button" class="btn-link" @click="pickOpenClawInstallDir">改目录</button>
                    <button type="button" class="btn-link" @click="runOpenClawDetect(openclawInstallDir)">🔄 重读</button>
                  </label>
                  <select
                    :value="targetModels['openclaw']"
                    @change="onModelChange('openclaw', $event)"
                  >
                    <!-- model.id 本身已经是完整 "<provider>/<model>" 格式(openclaw 约定),
                         直接用 id 作 option value;不再给它拼额外前缀(避免 double-prefix)。 -->
                    <option
                      v-for="m in openclawDetectedModels"
                      :key="m.id"
                      :value="m.id"
                    >{{ m.label || m.id }}</option>
                  </select>
                  <div v-if="openclawAuthProviders.length" class="target-hint" style="padding-left:0;margin-top:4px">
                    已配置凭证 provider: {{ openclawAuthProviders.join(', ') }}
                  </div>
                </div>
                <!-- 目录找到 + openclaw.json 能解析,但三个模型源全空:
                     typical case 是用户刚装 openclaw 还没 configure 过 / 没装过任何 agent。 -->
                <div v-else-if="openclawDetectStatus === 'ok'" class="target-field openclaw-warn">
                  <div>
                    ⚠ 找到 OpenClaw 安装(<code>{{ openclawResolvedDir }}</code>),
                    但<strong>配置里还没声明任何模型</strong>
                  </div>
                  <div style="margin-top:4px">
                    openclaw.json 里的 <code>agents.defaults.model.primary</code> /
                    <code>agents.defaults.models</code> / <code>agents.list[].model</code> 三处都空。
                    先跑一次 <code>openclaw configure</code> 选默认模型,
                    或装一个 agent 让它产生 model 记录,再回来"重新扫描"。
                  </div>
                  <div class="openclaw-warn-actions">
                    <button type="button" class="btn" @click="runOpenClawDetect(openclawInstallDir)">🔄 重新扫描</button>
                    <button type="button" class="btn" @click="pickOpenClawInstallDir">改选目录…</button>
                  </div>
                </div>
              </template>

            </div>
          </div>
        </div>
        <div v-if="!anyTargetSelected" class="error-text" style="margin-top:6px">
          至少勾选一个部署目标
        </div>
      </div>
    </div>

    <!-- Step 3 -->
    <div v-if="currentStep === 4" class="card lg">
      <h2>环境列表</h2>
      <p class="help-text">
        填业务系统的运行环境(如 dev / test / prod),每个环境填后端 API 域名,可选填前端 Web 域名。建议带上 http/https 前缀。
      </p>
      <div v-for="(env, i) in environments" :key="i" class="dynamic-row">
        <div class="row-fields">
          <div class="form-group compact" style="flex: 0 0 100px">
            <label>环境 ID
              <span class="help-icon" title="环境短标识(dev/test/staging/prod)。每个 env 会注册一套独立的 MCP 实例:nacos-mcp-server-<ID>、grafana-mcp-server-<ID> 等。">?</span>
            </label>
            <input
              v-model="env.id"
              type="text"
              placeholder="dev"
              :class="{ error: hasError(`env.${i}.id`) }"
            />
          </div>
          <div class="form-group compact">
            <label>API 域名
              <span class="help-icon" title="后端接口域名,机器人做接口实测 / 日志查询时拼 URL 用。建议带 http/https 前缀明确协议(内部 dev 常 http,公网 prod 多 https);不带也行,下游按 https 兜底。">?</span>
              <span
                v-if="urlProbeResults[urlProbeKey(i, 'api')]?.status === 'loading'"
                class="url-probe-badge loading"
              >测试中…</span>
              <span
                v-else-if="urlProbeResults[urlProbeKey(i, 'api')]?.status === 'ok'"
                class="url-probe-badge ok"
                :title="urlProbeResults[urlProbeKey(i, 'api')]?.detail"
              >✓ {{ urlProbeResults[urlProbeKey(i, 'api')]?.latency }}</span>
              <span
                v-else-if="urlProbeResults[urlProbeKey(i, 'api')]?.status === 'fail'"
                class="url-probe-badge fail"
                :title="urlProbeResults[urlProbeKey(i, 'api')]?.error"
              >✗ {{ urlProbeResults[urlProbeKey(i, 'api')]?.error }}</span>
            </label>
            <input
              v-model="env.api_domain"
              type="text"
              placeholder="https://api-dev.example.com"
              :class="{ error: hasError(`env.${i}.api_domain`) }"
              @input="scheduleURLProbe(i, 'api', env.api_domain)"
            />
          </div>
          <div class="form-group compact">
            <label>Web 域名
              <span class="auto-tag">选填</span>
              <span class="help-icon" title="前端入口域名(管理后台 / 用户站)。机器人排障时知道 '用户在哪个 URL 看到 bug' vs '后端哪个接口报错'。单域名系统留空即可。建议带 http/https 前缀。">?</span>
              <span
                v-if="urlProbeResults[urlProbeKey(i, 'web')]?.status === 'loading'"
                class="url-probe-badge loading"
              >测试中…</span>
              <span
                v-else-if="urlProbeResults[urlProbeKey(i, 'web')]?.status === 'ok'"
                class="url-probe-badge ok"
                :title="urlProbeResults[urlProbeKey(i, 'web')]?.detail"
              >✓ {{ urlProbeResults[urlProbeKey(i, 'web')]?.latency }}</span>
              <span
                v-else-if="urlProbeResults[urlProbeKey(i, 'web')]?.status === 'fail'"
                class="url-probe-badge fail"
                :title="urlProbeResults[urlProbeKey(i, 'web')]?.error"
              >✗ {{ urlProbeResults[urlProbeKey(i, 'web')]?.error }}</span>
            </label>
            <input
              v-model="env.web_domain"
              type="text"
              placeholder="https://www-dev.example.com"
              @input="scheduleURLProbe(i, 'web', env.web_domain)"
            />
          </div>
          <div class="form-group compact checkbox-group">
            <label :title="'is_prod=true 时机器人更保守:执行写入/重启类动作前会二次确认;OpenClaw 客户端 UI 也会标红。'">
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
    <div v-if="currentStep === 5" class="card lg">
      <h2>代码仓库</h2>
      <p class="help-text">
        填业务的代码仓库:可以选本地已 clone 的目录,也可以填远程 URL 让 Studio 帮你拉下来。扫描后会自动识别技术栈、服务名和分支。
      </p>

      <!-- 全局默认 clone 目录:一次性设置,跨向导持久 -->
      <div class="global-default-block">
        <label class="global-default-label">
          🌐 默认 clone 父目录(全局)
          <span class="field-hint">
            — 远程仓库默认 clone 到 <code>&lt;这里&gt;/&lt;repo.name&gt;/</code>
            <span v-if="globalDefaultReposRoot" class="saved-indicator">✓ 已保存</span>
            <span v-else>(未设置 · 将使用 <code>{{ displayPath(resolvedReposRoot) }}</code>)</span>
          </span>
        </label>
        <!-- 输入框 readonly,只能通过"选目录"选 —— 跟 Step 4 本地仓库目录一致的约束。 -->
        <div class="global-default-row">
          <input
            :value="displayPath(reposRootInput) || displayPath(resolvedReposRoot)"
            type="text"
            :placeholder="displayPath(resolvedReposRoot)"
            readonly
            class="path-readonly"
            :title="reposRootInput || resolvedReposRoot"
          />
          <button type="button" class="btn" @click="pickReposRoot">
            {{ reposRootInput ? '重新选…' : '选目录…' }}
          </button>
          <button
            type="button"
            class="btn"
            :disabled="!reposRootInput.trim() || reposRootInput.trim() === globalDefaultReposRoot"
            @click="saveAsGlobalDefault"
            title="把当前路径写入 ~/.tshoot/config.json;下次打开 Studio 自动用"
          >💾 设为全局默认</button>
        </div>
      </div>

      <div v-for="(repo, i) in repos" :key="i" class="repo-block">
        <div class="repo-header">
          <span class="repo-badge">仓库 {{ i + 1 }}</span>
          <span v-if="repo.sub_path && repo.sub_path.trim()" class="submodule-tag" :title="`子目录: ${repo.sub_path}`">
            📂 {{ repo.sub_path.trim() }}
          </span>
          <button class="btn-icon remove" @click="removeRepo(i)" :disabled="repos.length <= 1">&times;</button>
        </div>

        <!-- 来源切换:本地已有目录 vs 远程 URL。切换清对侧字段避免残留。 -->
        <div class="form-group">
          <label>仓库来源</label>
          <div class="source-toggle">
            <label class="source-option" :class="{ selected: repo._source === 'remote' }">
              <input
                type="radio"
                :checked="repo._source === 'remote'"
                @change="setRepoSource(repo, 'remote')"
              />
              <span class="source-title">🌐 远程 URL</span>
              <span class="source-hint">填 git URL,扫描时 clone 到本地</span>
            </label>
            <label class="source-option" :class="{ selected: repo._source === 'local' }">
              <input
                type="radio"
                :checked="repo._source === 'local'"
                @change="setRepoSource(repo, 'local')"
              />
              <span class="source-title">📁 本地已有</span>
              <span class="source-hint">选一个已 clone 好的仓库目录</span>
            </label>
          </div>
        </div>

        <!-- 远程模式 -->
        <template v-if="repo._source === 'remote'">
          <div class="form-group">
            <label>仓库地址 <span class="required">*</span>
              <span class="field-hint">— 仓库名从 URL 自动推;扫描前需要 clone 到本地</span>
            </label>
            <input
              v-model="repo.url"
              type="text"
              placeholder="git@github.com:org/order-service.git"
              :class="{ error: hasError(`repo.${i}.url`) }"
              @input="onRepoUrlInput(repo)"
            />
          </div>
          <div class="form-group">
            <label>
              Clone 父目录
              <span class="auto-tag">可选</span>
              <span class="field-hint">
                — 选父目录,git clone 自动建 <code>/{{ repo.name || '&lt;repo.name&gt;' }}</code> 子目录;不填就用<strong>全局默认</strong>
                <code>{{ displayPath(reposRootInput.trim() || resolvedReposRoot) }}/{{ repo.name || '&lt;repo.name&gt;' }}</code>
              </span>
            </label>
            <!-- 跟本地仓库目录一致:readonly,只能通过按钮选。
                 input 显示"父目录 → 实际仓库路径"两段,让用户清楚 git 不会污染所选父目录本身。
                 "🗑 清空"按钮把自定义还原为"用全局默认 reposRoot"。 -->
            <div class="path-input-row">
              <input
                :value="repo._cloneTarget ? displayPath(resolveCloneDest(repo)) : ''"
                type="text"
                :placeholder="`点选父目录(如 ${displayPath(reposRootInput.trim() || resolvedReposRoot)}),会自动建 /${repo.name || '<repo.name>'}`"
                readonly
                class="path-readonly"
                :title="repo._cloneTarget ? `父目录: ${repo._cloneTarget}\n实际仓库: ${resolveCloneDest(repo)}` : ''"
              />
              <button type="button" class="btn" @click="pickCloneTarget(repo)">
                {{ repo._cloneTarget ? '重新选…' : '选目录…' }}
              </button>
              <button
                v-if="repo._cloneTarget"
                type="button"
                class="btn-link cc-delete"
                :title="'清空自定义目标,回到默认目录'"
                @click="repo._cloneTarget = ''"
              >🗑</button>
            </div>
          </div>
          <!-- 远程模式必须用户点这个按钮才 clone+扫(不搞 URL 输入完自动 clone,避免失误 URL 拉垃圾) -->
          <div class="form-group repo-sync-row">
            <button
              type="button"
              class="btn primary"
              :disabled="!repo.url.trim() || repo._scanning"
              @click="scanSingleRepo(repo)"
            >
              <span v-if="repo._scanning" class="scan-spinner" aria-hidden="true"></span>
              {{ repo._scanning ? 'Clone + 扫描中…' : (repo._scanned ? '🔄 重新同步扫描' : '🔄 同步到本地并扫描') }}
            </button>
            <span v-if="repo._scanning" class="analyze-progress-inline">
              <span class="scan-spinner-mini"></span>
              <span>正在 git clone + DetectStack/Framework + 读取分支列表…</span>
            </span>
            <span v-else-if="repo._scanError" class="repo-scan-error">✗ {{ repo._scanError }}</span>
            <span v-else-if="repo._scanned" class="repo-scan-ok">✓ 已扫描,结果见下方</span>
          </div>
        </template>

        <!-- 本地模式:只显示目录;URL 静默从 git remote 反填(用户不用操心),
             读不到 remote 时用占位值兜底,保证 yaml 合法。 -->
        <template v-else>
          <div class="form-group">
            <label>本地仓库目录 <span class="required">*</span>
              <span class="field-hint">— 点"选目录"选一个已 clone 好的目录,自动反填 URL + 推仓库名 + 扫描</span>
            </label>
            <!-- 强制用"选目录"按钮,不让用户手敲路径(手写路径要么空格漏了、要么打错、
                 要么存在性没核对,比让 openDir 返回一个保证存在的绝对路径麻烦多了)。
                 input 只做 readonly 展示;想改就点按钮重选。 -->
            <div class="path-input-row">
              <input
                :value="repo._localPath"
                type="text"
                placeholder="尚未选择目录"
                readonly
                class="path-readonly"
                :title="repo._localPath || ''"
              />
              <button type="button" class="btn" @click="pickLocalRepoDir(repo)">
                {{ repo._localPath ? '重新选目录…' : '选目录…' }}
              </button>
            </div>
            <div v-if="repo._localPath && repo.url" class="local-url-probe ok">
              ✓ 已识别 origin: <code>{{ repo.url }}</code>
            </div>
            <div v-else-if="repo._localPath && !repo.url" class="local-url-probe warn">
              ⚠ 没读到 <code>git remote origin</code>;yaml 里会用占位 URL(仓库已在本地,不影响扫描)
            </div>
          </div>
          <!-- 本地模式:选完目录会自动扫,但允许手动"重新扫描"(用户改了代码 / 切了分支后刷新) -->
          <div v-if="repo._localPath" class="form-group repo-sync-row">
            <button
              type="button"
              class="btn"
              :disabled="repo._scanning"
              @click="scanSingleRepo(repo)"
            >
              <span v-if="repo._scanning" class="scan-spinner-mini" aria-hidden="true"></span>
              {{ repo._scanning ? '扫描中…' : '🔄 重新扫描' }}
            </button>
            <span v-if="repo._scanning" class="analyze-progress-inline">
              <span>DetectStack / Framework + 读取分支…</span>
            </span>
            <span v-else-if="repo._scanError" class="repo-scan-error">✗ {{ repo._scanError }}</span>
            <span v-else-if="repo._scanned" class="repo-scan-ok">✓ 已扫描</span>
          </div>
        </template>

        <!-- 仓库名 + 自动识别 块:只在用户已经填了来源(URL / 本地目录)后才显示,
             免得空白状态就一堆输入框 —— 用户还没给线索,仓库名从哪来? -->
        <div v-if="hasRepoSource(repo)" class="form-group">
          <label>
            仓库名
            <span v-if="!repo._nameManual" class="auto-tag">
              {{ repo._source === 'local' ? '自动从目录名推' : '自动从 URL 推' }}
            </span>
            <span v-else class="field-hint">(已手改;清空可回到自动推)</span>
          </label>
          <input
            v-model="repo.name"
            type="text"
            :placeholder="repo._source === 'local' ? '自动从目录名推出' : '自动从仓库地址推出'"
            :class="{ error: hasError(`repo.${i}.name`) }"
            @input="onRepoNameInput(repo)"
          />
        </div>

        <!-- 自动识别结果:只展 技术栈(仅 readonly)。framework 启发式误报率高,
             不自动填,用户在 yaml 里手改。 -->
        <div v-if="hasRepoSource(repo)" class="form-group">
          <label>
            技术栈
            <span class="field-hint">(扫描后自动填,只读)</span>
          </label>
          <div v-if="repo._source === 'remote' && repo.url.trim() && !repo._scanned && !repo._scanning" class="auto-scan-hint">
            ⚠ 还没扫描 —
            <strong>点上方"🔄 同步到本地并扫描"按钮</strong>触发
          </div>
          <div class="stack-display" :class="{ empty: !repo.stack }">
            <span v-if="repo._scanning" class="auto-scanning">
              <span class="scan-spinner-mini"></span>扫描中…
            </span>
            <span v-else>{{ repo.stack || '—' }}</span>
          </div>
        </div>

        <!-- monorepo 自动检测结果:始终展示(让用户清楚"有没有检测到 + 检测到啥")。
             0 个子模块  → 绿色"单服务仓库"
             1 个       → 灰色提示(边缘情况)
             N>1        → 紫色 banner 列出每个子模块完整路径 + 一键拆分按钮 -->
        <div
          v-if="hasRepoSource(repo) && repo._submoduleHints !== undefined && !repo._submoduleHintsDismissed"
          class="monorepo-banner"
          :class="{
            'monorepo-banner-mono': repo._submoduleHints.length > 1,
            'monorepo-banner-single': repo._submoduleHints.length <= 1,
          }"
        >
          <div v-if="repo._submoduleHints.length === 0" class="monorepo-banner-head ok">
            ✓ 检测结果:单服务仓库(整仓当一个服务处理,无需拆分)
          </div>
          <div v-else-if="repo._submoduleHints.length === 1" class="monorepo-banner-head warn">
            ⚠ 仅检测到 1 个入口,看着不像 monorepo(整仓当一个服务也行)
          </div>
          <template v-else-if="isGitSubmodulesHints(repo._submoduleHints)">
            <!-- 分支 A:.gitmodules 路径 —— 每个 hint 都有自己的 git URL,真"独立 git repo + 子目录共置"
                 这种才拆成独立条目;每行有自己的 url + 本地路径,后续配置中心 / 数据层映射独立。 -->
            <div class="monorepo-banner-head">
              🔍 检测到 .gitmodules 声明的 {{ repo._submoduleHints.length }} 个独立子模块(每个都是独立 git repo,默认全选):
            </div>
            <div class="monorepo-banner-hint">
              💡 <strong>不点"拆分"按钮就不影响</strong> —— 如果当成单仓处理,直接在下方"服务名"里手填即可。
            </div>
            <ul class="monorepo-banner-list">
              <li v-for="h in repo._submoduleHints" :key="h.sub_path">
                <label class="monorepo-row-check">
                  <input
                    type="checkbox"
                    :checked="repo._submoduleSelection?.[h.sub_path] !== false"
                    @change="toggleSubmodulePick(repo, h.sub_path, ($event.target as HTMLInputElement).checked)"
                  />
                  <div class="monorepo-row-content">
                    <div class="monorepo-row-top">
                      <strong>{{ h.name }}</strong>
                      <span class="monorepo-stack">{{ h.stack }}</span>
                      <span class="monorepo-role">{{ h.role }}</span>
                    </div>
                    <div class="monorepo-row-path">
                      📂 <code>{{ submodulePathFor(repo, h.sub_path) }}</code>
                    </div>
                    <div class="monorepo-row-url" :title="h.url">
                      🔗 <code>{{ h.url }}</code>
                      <span class="field-hint">(独立 git repo)</span>
                    </div>
                    <div class="monorepo-row-reason">
                      <span class="field-hint">{{ h.reason }}</span>
                    </div>
                  </div>
                </label>
              </li>
            </ul>
            <button
              type="button"
              class="btn primary monorepo-split-btn"
              :disabled="pickedSubmoduleCount(repo) === 0"
              @click="splitMonorepo(i)"
            >
              ✂ 拆成 {{ pickedSubmoduleCount(repo) }} 个独立仓库条目(各自 url / 本地路径 / role)
            </button>
          </template>
          <template v-else>
            <!-- 分支 B:同仓多入口(cmd/<x>/main.go / services/<x>/ / workspaces / pom-modules)
                 这些**不是**独立 git repo,只是同一仓库下的多个服务入口 —— 不该拆成多条 repos[],
                 而是合并到本仓的 service_names + 各自记录入口路径。 -->
            <div class="monorepo-banner-head">
              🔍 在本仓检测到 {{ repo._submoduleHints.length }} 个服务入口(同一 git 仓库,多服务模式):
            </div>
            <div class="monorepo-banner-hint">
              💡 这些是<strong>同仓多服务</strong>(不是独立 git repo),建议合并到本仓的服务名列表 —— 每个服务自动加 <code>{{ repo.name || '&lt;repo&gt;' }}-</code> 前缀消歧义(避免跨仓重名,如 4 个仓都有 cmd/grpc-server 时撞成一团)。
            </div>
            <ul class="monorepo-banner-list">
              <li v-for="h in repo._submoduleHints" :key="h.sub_path">
                <label class="monorepo-row-check">
                  <input
                    type="checkbox"
                    :checked="repo._submoduleSelection?.[h.sub_path] !== false"
                    @change="toggleSubmodulePick(repo, h.sub_path, ($event.target as HTMLInputElement).checked)"
                  />
                  <div class="monorepo-row-content">
                    <div class="monorepo-row-top">
                      <strong>{{ qualifyServiceName(repo.name, h.name) }}</strong>
                      <span v-if="qualifyServiceName(repo.name, h.name) !== h.name" class="field-hint">
                        (原入口名:{{ h.name }})
                      </span>
                      <span class="monorepo-stack">{{ h.stack }}</span>
                      <span class="monorepo-role">{{ h.role }}</span>
                    </div>
                    <div class="monorepo-row-path">
                      📂 入口 <code>{{ submodulePathFor(repo, h.sub_path) }}</code>
                    </div>
                    <div class="monorepo-row-reason">
                      <span class="field-hint">{{ h.reason }}</span>
                    </div>
                  </div>
                </label>
              </li>
            </ul>
            <button
              type="button"
              class="btn primary monorepo-split-btn"
              :disabled="pickedSubmoduleCount(repo) === 0"
              @click="mergeMonorepoIntoServices(i)"
            >
              ➕ 合并为本仓 {{ pickedSubmoduleCount(repo) }} 个服务名(自动加 <code>{{ repo.name || '&lt;repo&gt;' }}-</code> 前缀)
            </button>
          </template>
        </div>

        <!-- 已合并的"同仓多服务"展示 —— 用户合并完后这里露出 service → entry 映射作为提醒。
             想重选时点"重新扫描"按钮就回到 banner 状态。 -->
        <div
          v-if="repo._serviceEntries && Object.keys(repo._serviceEntries).length > 0"
          class="service-entries-display"
        >
          <div class="service-entries-head">
            ✓ 本仓 {{ Object.keys(repo._serviceEntries).length }} 个服务的入口路径:
            <button type="button" class="btn-link" @click="repo._submoduleHintsDismissed = false">改</button>
          </div>
          <ul class="service-entries-list">
            <li v-for="(entry, name) in repo._serviceEntries" :key="name">
              <strong>{{ name }}</strong> → <code>{{ entry }}</code>
            </li>
          </ul>
        </div>

        <!-- 已 split 的子模块行:把"父仓本地路径 + sub_path"拼出来明确显示给用户看 -->
        <div
          v-if="hasRepoSource(repo) && repo.sub_path && repo.sub_path.trim() && repo._localPath"
          class="submodule-path-display"
        >
          <span class="field-hint">本服务源码实际位置:</span>
          <code>{{ submodulePathFor(repo, repo.sub_path) }}</code>
        </div>

        <!-- 子目录(monorepo):入口在仓库 header 的 +子模块 按钮 + 上方的"一键拆分"banner,
             两条途径设进来后,sub_path 已有值,只在这里以"已设 + 可改"的方式展示;
             否则整个表单隐藏(默认整仓单服务,99% 用户用不到本字段)。 -->
        <div v-if="hasRepoSource(repo) && repo.sub_path && repo.sub_path.trim()" class="form-group form-group-subpath">
          <label>
            子目录
            <span class="field-hint">(monorepo 子模块路径,跟 url 一起决定本服务源码位置)</span>
          </label>
          <input
            v-model="repo.sub_path"
            type="text"
            class="subpath-input"
            placeholder="services/commerce"
            @input="onRepoSubPathInput(repo)"
          />
        </div>

        <!-- 角色:决定 incident-investigator 看本 repo 的视角。stack 不能一刀切
             (node 既能写前端也能写 BFF/服务),所以独立一个下拉给用户挑。
             默认值在 makeEmptyRepo 兜底 backend;node/php 用户常需要改成 frontend / admin。 -->
        <div v-if="hasRepoSource(repo)" class="form-group">
          <label>
            角色
            <span class="field-hint">(影响 AI 排障时的依赖图分析方向)</span>
          </label>
          <select v-model="repo.role" class="role-select" @change="syncServiceNamesWithRole(repo)">
            <option value="backend">后端服务 (backend) — 业务微服务,双向依赖图</option>
            <option value="frontend">前端 (frontend) — web app,只调下游不被调</option>
            <option value="gateway">网关 / BFF (gateway) — API 聚合层</option>
            <option value="middleware">中间层 (middleware) — worker / 调度器 / 接入层</option>
            <option value="mobile">移动端 (mobile) — iOS / Android</option>
            <option value="admin">管理后台 (admin) — 内部运营系统</option>
            <option value="common-lib">公共库 (common-lib) — 不入服务图,仅作版本对比</option>
            <option value="infra">基础设施 (infra) — k8s manifest / terraform</option>
            <option value="docs">文档 (docs) — 仅作背景资料</option>
          </select>
          <!-- role 推荐 chip:RecommendRoleForRepo 基于"仓库名 + 顶层目录 + stack 专属依赖文件"算出来。
               跟当前选的 role 一致 → 显示绿色 ✓;不一致 → 显示橙色 + 一个"采用"按钮。
               用户能看到推荐理由(如"package.json 含 react,无后端框架"),不是黑盒推断。 -->
          <div v-if="repo._roleHintLoading" class="role-hint-loading">📍 推荐分析中…</div>
          <div
            v-else-if="repo._roleHint"
            class="role-hint"
            :class="{ matched: repo._roleHint.role === repo.role }"
          >
            <span v-if="repo._roleHint.role === repo.role" class="role-hint-icon">✓</span>
            <span v-else class="role-hint-icon">📍</span>
            <span class="role-hint-text">
              <template v-if="repo._roleHint.role === repo.role">推荐:{{ repo._roleHint.role }}</template>
              <template v-else>建议:<strong>{{ repo._roleHint.role }}</strong></template>
              <span class="role-hint-reason">— {{ repo._roleHint.reason }}</span>
            </span>
            <button
              v-if="repo._roleHint.role !== repo.role"
              type="button"
              class="role-hint-apply"
              @click="applyRoleHint(repo)"
            >采用</button>
          </div>
        </div>

        <!-- 服务名:自动识别出 chip 列表,每个右上角 ✕ 可删。monorepo 常见多服务,
             也支持用户"+" 手动补未识别到的。
             仅"业务服务"角色(backend / gateway / middleware / admin)显示这个块 ——
             frontend / common-lib / mobile / infra / docs 不是服务,不需要服务名。 -->
        <div v-if="hasRepoSource(repo) && isServiceRole(repo.role)" class="form-group">
          <label>
            服务名
            <span class="help-icon" title="config-map 以此为 key。扫描会自动识别(monorepo 列所有子模块);识别不全时点 + 手动补,不想要的点 ✕ 删。">?</span>
            <span v-if="repoServiceNamesList(repo).length" class="field-hint">
              — {{ repoServiceNamesList(repo).length }} 个(✕ 删 / + 补)
            </span>
            <span v-else class="field-hint">(扫一下自动填,或点下方 + 手动补)</span>
          </label>
          <div v-if="repo._scanning" class="service-chips-row">
            <span class="auto-scanning"><span class="scan-spinner-mini"></span>扫描中…</span>
          </div>
          <div v-else class="service-chips-row">
            <span
              v-for="svc in repoServiceNamesList(repo)"
              :key="svc"
              class="service-chip"
            >
              <span class="service-chip-name">{{ svc }}</span>
              <button
                type="button"
                class="service-chip-x"
                :title="`删除 ${svc}`"
                @click="removeServiceName(repo, svc)"
              >✕</button>
            </span>
            <!-- inline "+" 输入行:永远显示,方便随时补漏。Enter 提交,逗号/空白能一次粘多个。 -->
            <span class="service-chip-add">
              <input
                v-model="svcAddInputs[i]"
                type="text"
                :placeholder="repoServiceNamesList(repo).length ? '+ 补一个服务名' : '+ 手填服务名'"
                @keydown.enter.prevent="addServiceName(repo, i)"
              />
              <button
                type="button"
                class="service-chip-add-btn"
                :disabled="!(svcAddInputs[i] || '').trim()"
                :title="'添加(Enter 也行;逗号/空格分隔可一次加多个)'"
                @click="addServiceName(repo, i)"
              >+</button>
            </span>
          </div>
        </div>

        <!-- 注:配置源(config_source)的服务级映射现在在 Step 5/6 内按服务挑(更直观);
             Step 4 的仓库表只关心代码层面(URL / 服务名 / 分支),不再叠 config_source。 -->

        <!-- env_branches:扫到真实分支列表后用 <select> 下拉;没扫到回落 text input。
             当前值不在列表里时(用户改过 yaml)会被插到最前,保证可选回。 -->
        <div v-if="hasRepoSource(repo)" class="form-group">
          <label>
            环境 → 分支映射
            <span class="help-icon" title="routing skill 根据此映射切到正确代码分支做代码定位。扫描仓库时按 env.id/is_prod 跟真实分支名做启发式匹配(dev→develop, prod→main/master,..),点下拉可改。">?</span>
            <span v-if="repoBranchesMap[repo.name]?.length" class="field-hint">
              — ✓ 从 {{ repoBranchesMap[repo.name]!.length }} 个真实分支里挑(可改)
            </span>
            <span v-else class="field-hint">(扫一下自动映射)</span>
          </label>
          <div class="branch-select-grid">
            <div v-for="env in environments" :key="env.id" class="branch-select-item">
              <span class="branch-env">{{ env.id || '?' }}</span>
              <span class="branch-arrow">→</span>
              <select
                v-if="repoBranchesMap[repo.name]?.length"
                v-model="repo.env_branches[env.id]"
                class="branch-select"
              >
                <option value="">—</option>
                <option
                  v-for="b in branchOptionsFor(repo, repo.env_branches[env.id])"
                  :key="b"
                  :value="b"
                >{{ b }}</option>
              </select>
              <input
                v-else
                v-model="repo.env_branches[env.id]"
                type="text"
                class="branch-input"
                placeholder="扫一下自动填,也可手填"
              />
            </div>
          </div>
        </div>
      </div>
      <button class="btn" @click="addRepo">+ 添加仓库</button>
    </div>

    <!-- Step 5 -->
    <div v-if="currentStep === 6" class="card lg">
      <h2>配置源</h2>

      <!-- 多源:顶部多选,勾哪些 type 就声明哪些源 -->
      <div class="form-group">
        <label>
          系统用到的配置源(可多选)
          <span class="field-hint">
            — 一种源勾一次(nacos / apollo / kuboard 等);多选会让你为每个服务挑走哪个源,单选则全员默认走它
          </span>
        </label>
        <div class="source-types-checkboxes">
          <label
            v-for="t in configTypeOptions"
            :key="t"
            class="source-type-pill"
            :class="{ active: enabledSourceTypes[t] }"
          >
            <input
              type="checkbox"
              :checked="!!enabledSourceTypes[t]"
              @change="(e) => toggleSourceType(t, (e.target as HTMLInputElement).checked)"
            />
            <span class="source-type-pill-name">{{ t }}</span>
            <span class="source-type-pill-desc">{{ configTypeDescriptions[t] }}</span>
          </label>
        </div>
        <div v-if="activeSourceTypes.length === 0" class="alert warn" style="margin-top:8px;">
          至少勾选一个配置源(若系统真不用配置中心,后面 Step 6/7 也基本啥都填不了)
        </div>
        <div v-else-if="isMultiSource" class="multi-source-mgr-hint">
          🔀 多源模式:每个源独立填写下面的连接信息;Step 6/7 数据层和可观测会按服务的源路由
        </div>
      </div>

      <!-- 凭证表单:主源(activeSourceTypes[0])完整功能(连接 + 预读 + namespace + 服务 dataId 选择)。
           nacos/apollo/consul 才展;env-vars/kubernetes/none 不需要。
           副源在主源块下方独立渲染只填连接信息(预读 + namespace 下拉留给主源,副源走 yaml 手填或 CLI)。 -->
      <div v-if="CC_FIELDS_BY_TYPE[configCenterType]" class="form-group">
        <label>
          <code>{{ configCenterType }}</code> 连接配置
          <span v-if="isMultiSource" class="auto-tag" style="background:#dbeafe;color:#1e40af;">主源 · 完整 preload</span>
          <span class="field-hint">— 按环境维度填写,保存后写入 system.yaml(标 <code># ⚠ secret</code> 注释),部署时注入到目标平台的 MCP Server env 块</span>
        </label>
        <div class="cc-share-warn">
          <div class="cc-share-warn-title">⚠ 凭证与共享提醒</div>
          <ul class="cc-share-warn-list">
            <li>这里填的账号密码会以明文写入 <code>system.yaml</code>(每条带 <code># ⚠ secret</code> 注释),并部署时注入到机器人 MCP Server 的 env 块 + <code>~/.tshoot/&lt;agent-id&gt;-creds.json</code>。</li>
            <li>分享 yaml 请限**团队内部 / 私有仓库**,<strong>不要提交到公开代码仓库</strong>。</li>
            <li>nacos USERNAME/PASSWORD 留空时自动回落到 nacos 标准默认账号 <code>nacos / nacos</code>(代码层兜底,避免 MCP 起不来);真账号不一致就在这里填。</li>
          </ul>
        </div>
        <div v-for="env in environments" :key="env.id" class="cc-env-block">
          <div class="cc-env-head">
            <span class="cc-env-label">{{ env.id || '(未命名 env)' }}</span>
            <span v-if="env.is_prod" class="cc-env-prod-tag">prod</span>
          </div>
          <div class="cc-env-fields">
            <template v-for="f in CC_FIELDS_BY_TYPE[configCenterType]" :key="f.key">
              <!-- kuboard 的 cluster/namespace/configmap 不在 env 级凭证里出现,改成下面的 per-service 映射块 -->
              <div
                v-if="!isFieldHidden(configCenterType, env.id, f, (k) => ccCredInputs[ccKeyFor(configCenterType, env.id, k)] || '') && !(configCenterType === 'kuboard' && (f.key === 'cluster' || f.key === 'namespace' || f.key === 'configmap'))"
                class="cc-field"
              >
                <label class="cc-field-label">
                  {{ f.label }}
                  <span v-if="f.optional" class="auto-tag">选填</span>
                  <span v-if="f.secret" class="cc-scope-tag secret" title="Secret:会写入 yaml,分享时注意范围">🔒 Secret</span>
                </label>
                <div class="cc-field-row">
                  <!-- options 非空 → <select> 下拉(枚举字段,如 auth_mode) -->
                  <select
                    v-if="f.options"
                    :value="ccCredInputs[ccKeyFor(configCenterType, env.id, f.key)] || (f.options[0]?.value || '')"
                    class="cc-input"
                    @change="ccCredInputs[ccKeyFor(configCenterType, env.id, f.key)] = ($event.target as HTMLSelectElement).value"
                  >
                    <option v-for="opt in f.options" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
                  </select>
                  <!-- kuboard: cluster/namespace/configmap 永远 <select>,未拉取时禁用 -->
                  <select
                    v-else-if="configCenterType === 'kuboard' && f.key === 'cluster'"
                    v-model="ccCredInputs[ccKeyFor(configCenterType, env.id, f.key)]"
                    :disabled="kuboardStateByEnv[env.id]?.status !== 'ok'"
                    class="cc-input"
                  >
                    <option v-if="kuboardStateByEnv[env.id]?.status !== 'ok'" value="">— 先填 URL+鉴权,点上方"📥 拉取" —</option>
                    <option v-else value="">— 选集群 —</option>
                    <option v-for="c in ((kuboardStateByEnv[env.id] as any)?.clusters || [])" :key="c.name" :value="c.name">{{ c.name }}</option>
                  </select>
                  <select
                    v-else-if="configCenterType === 'kuboard' && f.key === 'namespace'"
                    v-model="ccCredInputs[ccKeyFor(configCenterType, env.id, f.key)]"
                    :disabled="kuboardStateByEnv[env.id]?.status !== 'ok' || !ccCredInputs[ccKeyFor(configCenterType, env.id, 'cluster')]"
                    class="cc-input"
                  >
                    <option v-if="kuboardStateByEnv[env.id]?.status !== 'ok'" value="">— 先拉取资源 —</option>
                    <option v-else-if="!ccCredInputs[ccKeyFor(configCenterType, env.id, 'cluster')]" value="">— 先选集群 —</option>
                    <option v-else value="">— 选 namespace —</option>
                    <option v-for="n in kuboardNamespacesFor(env.id, ccCredInputs[ccKeyFor(configCenterType, env.id, 'cluster')] || '')" :key="n" :value="n">{{ n }}</option>
                  </select>
                  <select
                    v-else-if="configCenterType === 'kuboard' && f.key === 'configmap'"
                    v-model="ccCredInputs[ccKeyFor(configCenterType, env.id, f.key)]"
                    :disabled="kuboardStateByEnv[env.id]?.status !== 'ok' || !ccCredInputs[ccKeyFor(configCenterType, env.id, 'namespace')]"
                    class="cc-input"
                  >
                    <option v-if="kuboardStateByEnv[env.id]?.status !== 'ok'" value="">— 先拉取资源 —</option>
                    <option v-else-if="!ccCredInputs[ccKeyFor(configCenterType, env.id, 'namespace')]" value="">— 先选 namespace —</option>
                    <option v-else value="">— 选 ConfigMap —</option>
                    <option v-for="cm in kuboardConfigMapsFor(env.id, ccCredInputs[ccKeyFor(configCenterType, env.id, 'cluster')] || '', ccCredInputs[ccKeyFor(configCenterType, env.id, 'namespace')] || '')" :key="cm" :value="cm">{{ cm }}</option>
                  </select>
                  <input
                    v-else
                    v-model="ccCredInputs[ccKeyFor(configCenterType, env.id, f.key)]"
                    :type="f.secret && !isRevealed(ccKeyFor(configCenterType, env.id, f.key)) ? 'password' : 'text'"
                    :placeholder="f.placeholder || ''"
                    autocomplete="off"
                    spellcheck="false"
                    class="cc-input"
                  />
                  <button
                    v-if="f.secret && !f.options"
                    type="button"
                    class="btn-link cc-reveal"
                    @click="toggleReveal(ccKeyFor(configCenterType, env.id, f.key))"
                    :title="isRevealed(ccKeyFor(configCenterType, env.id, f.key)) ? '隐藏明文' : '显示明文'"
                  >{{ isRevealed(ccKeyFor(configCenterType, env.id, f.key)) ? '🙈' : '👁' }}</button>
                  <button
                    v-if="!f.options && ccCredInputs[ccKeyFor(configCenterType, env.id, f.key)]"
                    type="button"
                    class="btn-link cc-delete"
                    @click="clearCCFieldInput(ccKeyFor(configCenterType, env.id, f.key))"
                    title="清空本字段"
                  >🗑</button>
                </div>
                <div v-if="!f.uiOnly" class="cc-env-hint">
                  对应环境变量:<code>{{ f.envVar(env.id || 'ENV') }}</code>
                </div>
              </div>
            </template>
          </div>

          <!-- kuboard 专属:点这个按钮拉资源,把后面 cluster/namespace/cm 三个字段从手填变下拉 -->
          <div v-if="configCenterType === 'kuboard'" class="cc-preload-row">
            <button
              v-if="kuboardStateByEnv[env.id]?.status === 'loading'"
              :key="`kb-load-${env.id}-loading`"
              type="button"
              class="btn cc-preload-btn"
              disabled
            >
              <span class="cc-preload-spinner" aria-hidden="true"></span>
              拉取中…
            </button>
            <button
              v-else-if="kuboardStateByEnv[env.id]?.status === 'ok'"
              :key="`kb-load-${env.id}-ok`"
              type="button"
              class="btn cc-preload-btn"
              @click="runKuboardPreload(env.id)"
            >🔄 重新读取</button>
            <button
              v-else
              :key="`kb-load-${env.id}-idle`"
              type="button"
              class="btn cc-preload-btn"
              @click="runKuboardPreload(env.id)"
            >📥 从 Kuboard 读取可选项</button>
            <span v-if="kuboardStateByEnv[env.id]?.status === 'ok'" class="cc-preload-summary">
              ✓ {{ (kuboardStateByEnv[env.id] as any).clusters.length }} 个集群
            </span>
            <span v-else-if="kuboardStateByEnv[env.id]?.status === 'error'" class="cc-preload-error">
              ✗ {{ (kuboardStateByEnv[env.id] as any).error.slice(0, 60) }}
              <router-link to="/logs" class="cc-preload-log-link">查看日志</router-link>
            </span>
          </div>

          <!-- 服务勾选清单:勾哪些服务走当前源(主源)。多源场景下,某服务在主源勾选 = 它的
               config_source 设为主源 type;副源场景下用户去对应副源面板勾选。
               单源场景默认所有服务都走唯一源,checkbox 全勾。 -->
          <div v-if="allServiceNames.length > 0" class="cc-svc-checklist">
            <div class="cc-svc-checklist-head">
              <span class="cc-svc-checklist-title">本环境包含的服务</span>
              <span class="cc-svc-checklist-hint">勾选要走 <code>{{ configCenterType }}</code> 源的服务;点下面"拉取配置"会列出这些服务对应的配置项</span>
            </div>
            <div class="cc-svc-checklist-grid">
              <label
                v-for="svc in allServiceNames"
                :key="svc"
                class="cc-svc-checklist-item"
                :class="{ checked: getServiceSource(svc) === configCenterType }"
              >
                <input
                  type="checkbox"
                  :checked="getServiceSource(svc) === configCenterType"
                  @change="(e) => setServiceSource(svc, (e.target as HTMLInputElement).checked ? configCenterType : '')"
                />
                <span class="cc-svc-checklist-name">{{ svc }}</span>
              </label>
            </div>
          </div>

          <!-- 真实预加载:用户填完凭证 + 勾选服务后,点一下连目标配置中心拉可用条目清单。
               按钮挨着每个 env 块,各 env 独立 loading / 错误态。 -->
          <div class="cc-preload-row">
            <button
              v-if="ccHubStateByEnv[env.id]?.status === 'loading'"
              :key="`cc-preload-${env.id}-loading`"
              type="button"
              class="btn cc-preload-btn"
              disabled
            >
              <span class="cc-preload-spinner" aria-hidden="true"></span>
              拉取中…
            </button>
            <button
              v-else-if="ccHubStateByEnv[env.id]?.status === 'ok'"
              :key="`cc-preload-${env.id}-ok`"
              type="button"
              class="btn cc-preload-btn"
              @click="runCCHubPreload(env.id)"
            >🔄 重新拉取勾选服务的配置</button>
            <button
              v-else
              :key="`cc-preload-${env.id}-idle`"
              type="button"
              class="btn cc-preload-btn"
              @click="runCCHubPreload(env.id)"
            >📥 拉取勾选服务的配置</button>
            <span v-if="ccHubStateByEnv[env.id]?.status === 'ok'" class="cc-preload-summary">
              ✓ {{ ccHubStateByEnv[env.id]!.entries?.length || 0 }} 条
            </span>
            <span v-else-if="ccHubStateByEnv[env.id]?.status === 'error'" class="cc-preload-error">
              ✗ 拉取失败
              <router-link to="/logs" class="cc-preload-log-link">查看日志</router-link>
            </span>
          </div>

          <!-- 映射块:只有**本 env** 自己预加载成功时才显示。不借其他 env 的扫描结果 ——
               每个 env 必须用自己的凭证各扫一次,才能呈现自己的 namespace / dataId 选项。 -->
          <!-- namespace + dataId 映射块只在"勾了服务 + 拉取过"时才出现。
               没勾任何服务时,即便 ccHubStateByEnv 里有上次扫的缓存,也不渲染 ——
               避免老 namespace / dataId 把"还没决定要走这个源"的视觉混淆掉。 -->
          <div
            v-if="envScanned(env.id) && allServiceNames.filter(s => getServiceSource(s) === configCenterType).length > 0"
            class="cc-map-block"
          >
            <div class="cc-map-head">
              <span class="cc-map-title">
                {{ env.id }} → 挑 namespace + 每个服务对应哪个 dataId
              </span>
            </div>

            <!-- namespace 下拉:布局完全仿 Step 4 "环境 → 分支" —— env.id 左、箭头、右 select -->
            <div class="cc-map-ns-grid">
              <div class="cc-map-ns-item">
                <span class="cc-map-ns-env">{{ env.id || '?' }}</span>
                <span class="cc-map-ns-arrow">→</span>
                <select
                  :value="envNamespaces[env.id] || ''"
                  class="cc-map-select"
                  :class="{ error: hasError(`cc.${configCenterType}.${env.id}.namespace`) }"
                  @change="(e: any) => onNamespaceChanged(env.id, e.target.value)"
                >
                  <option value="">— 选 namespace —</option>
                  <option
                    v-for="ns in namespacesFor(env.id)"
                    :key="ns.id || 'public'"
                    :value="ns.id"
                  >{{ ns.show_name || ns.id || 'public' }}</option>
                </select>
                <span class="cc-map-ns-count">
                  {{ entriesForNamespace(env.id, envNamespaces[env.id] || '').length }} 条
                </span>
              </div>
            </div>

            <!-- 配置项映射:只列勾选了走当前源的服务。每行 service → dataId 下拉。
                 走其它源的服务,在那个源的面板里勾选 + 配置(本面板不显示)。 -->
            <div
              v-if="allServiceNames.filter(s => getServiceSource(s) === configCenterType).length > 0"
              class="cc-map-svc-list"
            >
              <div
                v-for="svc in allServiceNames.filter(s => getServiceSource(s) === configCenterType)"
                :key="svc"
                class="cc-map-svc-row"
              >
                <span class="cc-map-svc-name">{{ svc }}</span>
                <select
                  v-model="serviceConfigSel[svcKey(env.id, svc)]"
                  class="cc-map-select cc-map-select-svc"
                  :class="{ error: hasError(`cc.${configCenterType}.${env.id}.svc.${svc}`) }"
                  @change="onDataIdChanged(env.id, svc)"
                >
                  <option value="">(不映射)</option>
                  <option
                    v-for="entry in entriesForNamespace(env.id, envNamespaces[env.id] || '')"
                    :key="entry.locator + '@' + (entry.group || '')"
                    :value="entry.locator"
                  >
                    {{ entry.locator }}<template v-if="entry.group && entry.group !== 'DEFAULT_GROUP'">  @{{ entry.group }}</template>
                  </option>
                </select>
                <span
                  v-if="serviceConfigGroup[svcKey(env.id, svc)]"
                  class="cc-map-group-tag"
                  :title="'group = ' + serviceConfigGroup[svcKey(env.id, svc)]"
                >
                  {{ serviceConfigGroup[svcKey(env.id, svc)] }}
                </span>
              </div>
            </div>
            <div v-else-if="allServiceNames.length === 0" class="cc-map-hint">
              先在 Step 4 填好 repos 的 <code>service_names</code>,这里才有服务列表可映射。
            </div>
            <div v-else class="cc-map-hint">
              没有服务被勾选走 <code>{{ configCenterType }}</code> 源 —— 在上面的"本环境包含的服务"清单里勾要走当前源的服务。
            </div>
          </div>

          <!-- kuboard 主源:per-service cluster/namespace/configmap 三联映射。
               nacos 走上面的 cc-map-block(envNamespaces + serviceConfigSel),kuboard 走这里。 -->
          <div
            v-if="configCenterType === 'kuboard'
                  && kuboardStateByEnv[env.id]?.status === 'ok'
                  && allServiceNames.filter(s => getServiceSource(s) === configCenterType).length > 0"
            class="cc-map-block"
          >
            <div class="cc-map-head">
              <span class="cc-map-title">
                {{ env.id }} → 每个服务对应的 集群 / namespace / ConfigMap
              </span>
            </div>
            <div class="cc-map-svc-list">
              <div
                v-for="svc in allServiceNames.filter(s => getServiceSource(s) === configCenterType)"
                :key="svc"
                class="cc-map-svc-row cc-map-svc-row-kuboard"
              >
                <span class="cc-map-svc-name">{{ svc }}</span>
                <select
                  :value="(kuboardSvcMap[svcKey(env.id, svc)] || {}).cluster || ''"
                  class="cc-map-select cc-map-select-kuboard"
                  @change="(e: any) => setKuboardLoc(env.id, svc, 'cluster', e.target.value)"
                >
                  <option value="">— 选集群 —</option>
                  <option
                    v-for="c in ((kuboardStateByEnv[env.id] as any)?.clusters || [])"
                    :key="c.name"
                    :value="c.name"
                  >{{ c.name }}</option>
                </select>
                <select
                  :value="(kuboardSvcMap[svcKey(env.id, svc)] || {}).namespace || ''"
                  :disabled="!((kuboardSvcMap[svcKey(env.id, svc)] || {}).cluster)"
                  class="cc-map-select cc-map-select-kuboard"
                  @change="(e: any) => setKuboardLoc(env.id, svc, 'namespace', e.target.value)"
                >
                  <option v-if="!((kuboardSvcMap[svcKey(env.id, svc)] || {}).cluster)" value="">— 先选集群 —</option>
                  <option v-else value="">— 选 namespace —</option>
                  <option
                    v-for="n in kuboardNamespacesFor(env.id, (kuboardSvcMap[svcKey(env.id, svc)] || {}).cluster || '')"
                    :key="n"
                    :value="n"
                  >{{ n }}</option>
                </select>
                <select
                  :value="(kuboardSvcMap[svcKey(env.id, svc)] || {}).configmap || ''"
                  :disabled="!((kuboardSvcMap[svcKey(env.id, svc)] || {}).namespace)"
                  class="cc-map-select cc-map-select-kuboard"
                  @change="(e: any) => setKuboardLoc(env.id, svc, 'configmap', e.target.value)"
                >
                  <option v-if="!((kuboardSvcMap[svcKey(env.id, svc)] || {}).namespace)" value="">— 先选 namespace —</option>
                  <option v-else value="">— 选 ConfigMap —</option>
                  <option
                    v-for="cm in kuboardConfigMapsFor(env.id, (kuboardSvcMap[svcKey(env.id, svc)] || {}).cluster || '', (kuboardSvcMap[svcKey(env.id, svc)] || {}).namespace || '')"
                    :key="cm"
                    :value="cm"
                  >{{ cm }}</option>
                </select>
              </div>
            </div>
          </div>

          <!-- 预加载失败时,不在页面渲染长错误文本;summary 行已显示 "✗ 拉取失败,详见日志",
               用户按 toast 提示进左侧「日志」页看完整栈。
               "下一步" 按钮旁的"还差 N 项"汇总提示已经包含该 env 未预加载的信息,
               env 块内不再重复展示,避免视觉冗余。 -->

        </div>
      </div>

      <!-- 副源连接表单:每个非主源 type 一份,只收 endpoints 凭证;预读 / namespace 下拉
           暂不支持(留给后续迭代或 yaml 手填)。 -->
      <div
        v-for="t in activeSourceTypes.slice(1).filter(t2 => CC_FIELDS_BY_TYPE[t2])"
        :key="`secsrc-${t}`"
        class="form-group secondary-source-form"
      >
        <label>
          <code>{{ t }}</code> 连接配置
          <span class="auto-tag" style="background:#fef3c7;color:#92400e;">副源</span>
          <span class="field-hint">
            — 每个 env 一份连接凭证;下方"本环境包含的服务"勾选要走本副源的服务,然后给每个服务挑对应的配置定位
          </span>
        </label>
        <div v-for="env in environments" :key="env.id" class="cc-env-block">
          <div class="cc-env-head">
            <span class="cc-env-label">{{ env.id || '(未命名 env)' }}</span>
            <span v-if="env.is_prod" class="cc-env-prod-tag">prod</span>
          </div>
          <div class="cc-env-fields">
            <template v-for="f in CC_FIELDS_BY_TYPE[t]" :key="f.key">
              <!-- kuboard 的 cluster/namespace/configmap 不在 env 级凭证里出现,改成下面的 per-service 映射块 -->
              <div
                v-if="!isFieldHidden(t, env.id, f, (k) => (sourceCreds[t].creds[env.id] || {})[k] || '') && !(t === 'kuboard' && (f.key === 'cluster' || f.key === 'namespace' || f.key === 'configmap'))"
                class="cc-field"
              >
                <label class="cc-field-label">
                  {{ f.label }}
                  <span v-if="f.optional" class="auto-tag">选填</span>
                  <span v-if="f.secret" class="cc-scope-tag secret">🔒 Secret</span>
                </label>
                <div class="cc-field-row">
                  <select
                    v-if="f.options"
                    :value="((sourceCreds[t].creds[env.id] || {})[f.key]) || (f.options[0]?.value || '')"
                    class="cc-input"
                    @change="(e) => { if (!sourceCreds[t].creds[env.id]) sourceCreds[t].creds[env.id] = {}; sourceCreds[t].creds[env.id][f.key] = (e.target as HTMLSelectElement).value }"
                  >
                    <option v-for="opt in f.options" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
                  </select>
                  <select
                    v-else-if="t === 'kuboard' && f.key === 'cluster'"
                    :value="(sourceCreds[t].creds[env.id] || {})[f.key] || ''"
                    :disabled="kuboardStateByEnv[env.id]?.status !== 'ok'"
                    class="cc-input"
                    @change="(e) => { if (!sourceCreds[t].creds[env.id]) sourceCreds[t].creds[env.id] = {}; sourceCreds[t].creds[env.id][f.key] = (e.target as HTMLSelectElement).value }"
                  >
                    <option v-if="kuboardStateByEnv[env.id]?.status !== 'ok'" value="">— 先填 URL+鉴权,点上方"📥 拉取" —</option>
                    <option v-else value="">— 选集群 —</option>
                    <option v-for="c in ((kuboardStateByEnv[env.id] as any)?.clusters || [])" :key="c.name" :value="c.name">{{ c.name }}</option>
                  </select>
                  <select
                    v-else-if="t === 'kuboard' && f.key === 'namespace'"
                    :value="(sourceCreds[t].creds[env.id] || {})[f.key] || ''"
                    :disabled="kuboardStateByEnv[env.id]?.status !== 'ok' || !((sourceCreds[t].creds[env.id] || {}).cluster)"
                    class="cc-input"
                    @change="(e) => { if (!sourceCreds[t].creds[env.id]) sourceCreds[t].creds[env.id] = {}; sourceCreds[t].creds[env.id][f.key] = (e.target as HTMLSelectElement).value }"
                  >
                    <option v-if="kuboardStateByEnv[env.id]?.status !== 'ok'" value="">— 先拉取资源 —</option>
                    <option v-else-if="!((sourceCreds[t].creds[env.id] || {}).cluster)" value="">— 先选集群 —</option>
                    <option v-else value="">— 选 namespace —</option>
                    <option v-for="n in kuboardNamespacesFor(env.id, (sourceCreds[t].creds[env.id] || {}).cluster || '')" :key="n" :value="n">{{ n }}</option>
                  </select>
                  <select
                    v-else-if="t === 'kuboard' && f.key === 'configmap'"
                    :value="(sourceCreds[t].creds[env.id] || {})[f.key] || ''"
                    :disabled="kuboardStateByEnv[env.id]?.status !== 'ok' || !((sourceCreds[t].creds[env.id] || {}).namespace)"
                    class="cc-input"
                    @change="(e) => { if (!sourceCreds[t].creds[env.id]) sourceCreds[t].creds[env.id] = {}; sourceCreds[t].creds[env.id][f.key] = (e.target as HTMLSelectElement).value }"
                  >
                    <option v-if="kuboardStateByEnv[env.id]?.status !== 'ok'" value="">— 先拉取资源 —</option>
                    <option v-else-if="!((sourceCreds[t].creds[env.id] || {}).namespace)" value="">— 先选 namespace —</option>
                    <option v-else value="">— 选 ConfigMap —</option>
                    <option v-for="cm in kuboardConfigMapsFor(env.id, (sourceCreds[t].creds[env.id] || {}).cluster || '', (sourceCreds[t].creds[env.id] || {}).namespace || '')" :key="cm" :value="cm">{{ cm }}</option>
                  </select>
                  <input
                    v-else
                    :type="f.secret ? 'password' : 'text'"
                    :value="(sourceCreds[t].creds[env.id] || {})[f.key] || ''"
                    :placeholder="f.placeholder || ''"
                    class="cc-input"
                    autocomplete="off"
                    spellcheck="false"
                    @input="(e) => { if (!sourceCreds[t].creds[env.id]) sourceCreds[t].creds[env.id] = {}; sourceCreds[t].creds[env.id][f.key] = (e.target as HTMLInputElement).value }"
                  />
                </div>
                <div v-if="!f.uiOnly" class="cc-env-hint">
                  对应环境变量:<code>{{ f.envVar(env.id || 'ENV') }}_{{ t.toUpperCase().replace(/-/g, '_') }}</code>
                </div>
              </div>
            </template>
          </div>

          <!-- kuboard 副源:同主源,加"📥 拉取资源"按钮,根据 sourceCreds[t].creds[env].url/username/password 读 -->
          <div v-if="t === 'kuboard'" class="cc-preload-row">
            <button
              v-if="kuboardStateByEnv[env.id]?.status === 'loading'"
              :key="`kb-sec-${env.id}-loading`"
              type="button"
              class="btn cc-preload-btn"
              disabled
            >
              <span class="cc-preload-spinner" aria-hidden="true"></span>
              拉取中…
            </button>
            <button
              v-else-if="kuboardStateByEnv[env.id]?.status === 'ok'"
              :key="`kb-sec-${env.id}-ok`"
              type="button"
              class="btn cc-preload-btn"
              @click="runKuboardPreloadFromSource(t, env.id)"
            >🔄 重新读取</button>
            <button
              v-else
              :key="`kb-sec-${env.id}-idle`"
              type="button"
              class="btn cc-preload-btn"
              @click="runKuboardPreloadFromSource(t, env.id)"
            >📥 从 Kuboard 读取可选项</button>
            <span v-if="kuboardStateByEnv[env.id]?.status === 'ok'" class="cc-preload-summary">
              ✓ {{ (kuboardStateByEnv[env.id] as any).clusters.length }} 个集群
            </span>
            <span v-else-if="kuboardStateByEnv[env.id]?.status === 'error'" class="cc-preload-error">
              ✗ {{ (kuboardStateByEnv[env.id] as any).error.slice(0, 60) }}
              <router-link to="/logs" class="cc-preload-log-link">查看日志</router-link>
            </span>
          </div>

          <!-- 副源服务勾选清单:只列"主源没勾走的"剩余服务 + 已经勾给本副源的服务。
               主源已经勾走的服务在这里不出现,避免一个服务同时被两个源认领。
               允许 0 勾(意味着这个副源虽然连接好了,但暂时没服务挂上去,合法但不实用)。 -->
          <div v-if="allServiceNames.filter(s => !getServiceSource(s) || getServiceSource(s) === t).length > 0" class="cc-svc-checklist">
            <div class="cc-svc-checklist-head">
              <span class="cc-svc-checklist-title">本环境包含的服务</span>
              <span class="cc-svc-checklist-hint">主源已认领的服务在此不显示;勾选要走 <code>{{ t }}</code> 副源的服务</span>
            </div>
            <div class="cc-svc-checklist-grid">
              <label
                v-for="svc in allServiceNames.filter(s => !getServiceSource(s) || getServiceSource(s) === t)"
                :key="svc"
                class="cc-svc-checklist-item"
                :class="{ checked: getServiceSource(svc) === t }"
              >
                <input
                  type="checkbox"
                  :checked="getServiceSource(svc) === t"
                  @change="(e) => setServiceSource(svc, (e.target as HTMLInputElement).checked ? t : '')"
                />
                <span class="cc-svc-checklist-name">{{ svc }}</span>
              </label>
            </div>
          </div>
          <div v-else-if="allServiceNames.length > 0" class="cc-svc-checklist-empty">
            所有服务都已被其他源认领;若想让某个服务改走 <code>{{ t }}</code> 源,先在对应源把它取消勾选。
          </div>

          <!-- kuboard per-service 映射:每个勾给本副源的服务 → cluster/namespace/configmap 三联级联下拉。
               不同服务可能落在不同 ns、不同 cm,所以三个字段都按服务挑,而不是 env 级共享。 -->
          <div
            v-if="t === 'kuboard'
                  && kuboardStateByEnv[env.id]?.status === 'ok'
                  && allServiceNames.filter(s => getServiceSource(s) === t).length > 0"
            class="cc-map-block"
          >
            <div class="cc-map-head">
              <span class="cc-map-title">
                {{ env.id }} → 每个服务对应的 集群 / namespace / ConfigMap
              </span>
            </div>
            <div class="cc-map-svc-list">
              <div
                v-for="svc in allServiceNames.filter(s => getServiceSource(s) === t)"
                :key="svc"
                class="cc-map-svc-row cc-map-svc-row-kuboard"
              >
                <span class="cc-map-svc-name">{{ svc }}</span>
                <select
                  :value="(kuboardSvcMap[svcKey(env.id, svc)] || {}).cluster || ''"
                  class="cc-map-select cc-map-select-kuboard"
                  @change="(e: any) => setKuboardLoc(env.id, svc, 'cluster', e.target.value)"
                >
                  <option value="">— 选集群 —</option>
                  <option
                    v-for="c in ((kuboardStateByEnv[env.id] as any)?.clusters || [])"
                    :key="c.name"
                    :value="c.name"
                  >{{ c.name }}</option>
                </select>
                <select
                  :value="(kuboardSvcMap[svcKey(env.id, svc)] || {}).namespace || ''"
                  :disabled="!((kuboardSvcMap[svcKey(env.id, svc)] || {}).cluster)"
                  class="cc-map-select cc-map-select-kuboard"
                  @change="(e: any) => setKuboardLoc(env.id, svc, 'namespace', e.target.value)"
                >
                  <option v-if="!((kuboardSvcMap[svcKey(env.id, svc)] || {}).cluster)" value="">— 先选集群 —</option>
                  <option v-else value="">— 选 namespace —</option>
                  <option
                    v-for="n in kuboardNamespacesFor(env.id, (kuboardSvcMap[svcKey(env.id, svc)] || {}).cluster || '')"
                    :key="n"
                    :value="n"
                  >{{ n }}</option>
                </select>
                <select
                  :value="(kuboardSvcMap[svcKey(env.id, svc)] || {}).configmap || ''"
                  :disabled="!((kuboardSvcMap[svcKey(env.id, svc)] || {}).namespace)"
                  class="cc-map-select cc-map-select-kuboard"
                  @change="(e: any) => setKuboardLoc(env.id, svc, 'configmap', e.target.value)"
                >
                  <option v-if="!((kuboardSvcMap[svcKey(env.id, svc)] || {}).namespace)" value="">— 先选 namespace —</option>
                  <option v-else value="">— 选 ConfigMap —</option>
                  <option
                    v-for="cm in kuboardConfigMapsFor(env.id, (kuboardSvcMap[svcKey(env.id, svc)] || {}).cluster || '', (kuboardSvcMap[svcKey(env.id, svc)] || {}).namespace || '')"
                    :key="cm"
                    :value="cm"
                  >{{ cm }}</option>
                </select>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- env-vars 源(无远程连接,但每个 env 各数据层的静态连接串在 Step 6 数据层里按 data_store 维度填) -->
      <div v-if="enabledSourceTypes['env-vars']" class="form-group">
        <p class="help-text">
          <strong>env-vars</strong> 源:机器人直接读取仓库内 <code>.env</code> 文件 + Step 6 数据层里填的静态连接串。
          这里没有连接信息要填,具体数据层(redis / mysql / ...)的 endpoint 走 Step 6 的"数据层"页。
        </p>
      </div>

      <!-- none 源:整个系统不接配置中心,本步无需任何输入,继续往下走即可 -->
      <div v-if="enabledSourceTypes['none']" class="form-group">
        <p class="help-text" style="background:#fffbeb;border-left-color:#f59e0b;color:#92400e;line-height:1.7;">
          <strong>不使用任何配置源</strong><br/>
          系统的连接串 / 业务配置不来自 nacos/apollo/consul/kuboard,也不走 <code>.env</code>。本步骤无需填写,直接"下一步"即可。
          <br/>
          下游影响:
          <br/>
          ① <code>config-executor</code> skill 不会装到工作区(机器人不会主动去读配置中心);
          <br/>
          ② Step 6 数据层连接串需要在仓库代码里硬编码 / 部署时手动注入,机器人不再帮忙读;
          <br/>
          ③ 生成的 <code>system.yaml</code> 仅占位 <code>config_center.type: none</code>。
        </p>
      </div>

      <!-- kuboard 源说明:简短引导用户填 URL + 鉴权,点拉取按钮自动加载 K8s 资源 -->
      <div v-if="enabledSourceTypes['kuboard']" class="form-group">
        <p class="help-text" style="background:#eff6ff;border-left-color:#3b82f6;color:#1e3a8a;line-height:1.7;">
          <strong>Kuboard 源使用说明</strong><br/>
          通过 Kuboard v4 API 读 K8s ConfigMap,本机无需 <code>~/.kube/config</code>,适合<strong>能登 Kuboard、拿不到 kubeconfig</strong> 的场景。
          <br/>
          <strong>鉴权方式(二选一)</strong>:
          <br/>
          ① <strong>API 访问凭证</strong>(推荐):Kuboard <strong>个人中心 → API 访问凭证 → 创建</strong>,粘到下方"API 访问凭证"字段。不暴露密码、可独立吊销、长期有效。
          <br/>
          ② <strong>用户名 + 密码</strong>:走 Kuboard <code>/login</code> 换临时 token,适合临时验证。
          <br/>
          填好 URL + 任一鉴权 → 点 <strong>📥 从 Kuboard 读取可选项</strong>,集群 / namespace / ConfigMap 自动下拉,再为每个服务挑对应位置即可。
        </p>
      </div>
    </div>

    <!-- Step 7:可观测性 -->
    <div v-if="currentStep === 8" class="card lg">
      <h2>可观测性</h2>
      <p class="help-text">
        勾选系统用到的可观测性组件(Grafana / Loki / Prometheus / Jaeger 等),按环境填上连接地址,机器人查日志 / 指标时会用。
      </p>

      <!-- 共享警告:同 Step 5 ,提醒密码会进 yaml -->
      <div class="cc-share-warn" style="margin-bottom:18px">
        <div class="cc-share-warn-title">⚠ 数据流与共享范围</div>
        <ul class="cc-share-warn-list">
          <li>本页填写字段(含密码、token 等凭证)将保存至 <code>system.yaml</code>。</li>
          <li>部署时,生成器把对应值注入目标 AI 平台的 MCP Server 环境变量。</li>
          <li><strong>system.yaml 含明文凭证</strong>,请仅在可信范围内分享。</li>
        </ul>
      </div>

      <!-- 启用的可观测性组件:横排 chip 选择(默认全展开,跟数据层 Step 6 一致 —— 数据层是自动识别勾选,这里手动) -->
      <h3 style="margin-top:4px">启用的可观测性组件</h3>
      <div class="obs-tool-chips">
        <label
          v-for="spec in OBS_TOOL_SPECS"
          :key="spec.key"
          class="obs-tool-chip"
          :class="{ active: enabledObservability[spec.key] }"
          :title="spec.description"
        >
          <input type="checkbox" v-model="enabledObservability[spec.key]" />
          {{ spec.label }}
        </label>
      </div>

      <!-- 主内容:按 env → 启用的工具 → 字段 层级,跟 Step 6 数据层布局一致。
           Loki 标签映射拆到每 env 独立加载(dev/prod 可能用不同 grafana 实例)。 -->
      <div class="ds-hierarchy" style="margin-top:14px">
        <div v-for="env in environments" :key="env.id" class="ds-env-section">
          <div class="ds-env-title">
            <span class="cc-env-label">{{ env.id || '(未命名 env)' }}</span>
            <span v-if="env.is_prod" class="cc-env-prod-tag">prod</span>
            <span class="ds-env-count">
              {{ OBS_TOOL_SPECS.filter(s => enabledObservability[s.key]).length }} 个组件已启用
            </span>
          </div>

          <div
            v-if="OBS_TOOL_SPECS.filter(s => enabledObservability[s.key]).length === 0"
            class="ds-empty"
          >⧗ 还没启用任何可观测性组件 — 在上方勾选要用的</div>

          <div v-else class="ds-svc-container">
            <!-- 每个启用的工具一块,展示字段 + 连通性徽章 -->
            <div
              v-for="spec in OBS_TOOL_SPECS.filter(s => enabledObservability[s.key])"
              :key="spec.key"
              class="ds-svc-block"
              :class="['status-' + (obsProbeResults[obsProbeKey(spec.key, env.id)]?.status || 'pending')]"
            >
              <div class="ds-svc-head">
                <span class="ds-svc-name">🗄 {{ spec.label }}</span>
                <!-- 访问方式选择:仅对 loki/prometheus/jaeger/tempo/elk 且 Grafana 已启用时可切换。
                     默认走 Grafana 代理(只需选 datasource);用户也可强制直连(填本工具自己的 URL+鉴权)。 -->
                <span
                  v-if="['loki','prometheus','jaeger','tempo','elk'].includes(spec.key) && enabledObservability['grafana']"
                  class="cc-field-row"
                  style="gap: 6px; align-items: center; margin-left: auto;"
                >
                  <span style="font-size: 12px; color: #6b7280;">访问方式</span>
                  <select
                    :value="getObsAccessMode(spec.key, env.id)"
                    class="cc-input"
                    style="height: 28px; padding: 0 6px; font-size: 13px; min-width: 160px;"
                    @change="(e: any) => setObsAccessMode(spec.key, env.id, e.target.value)"
                  >
                    <option value="via_grafana">🔗 通过 Grafana 代理</option>
                    <option value="direct">🔌 直连(自己填 URL)</option>
                  </select>
                </span>
                <span
                  v-if="obsProbeResults[obsProbeKey(spec.key, env.id)]?.status === 'loading'"
                  class="url-probe-badge loading"
                >测试中…</span>
                <span
                  v-else-if="obsProbeResults[obsProbeKey(spec.key, env.id)]?.status === 'ok'"
                  class="url-probe-badge ok"
                  :title="obsProbeResults[obsProbeKey(spec.key, env.id)]?.detail"
                >✓ {{ obsProbeResults[obsProbeKey(spec.key, env.id)]?.latency }}</span>
                <span
                  v-else-if="obsProbeResults[obsProbeKey(spec.key, env.id)]?.status === 'fail'"
                  class="url-probe-badge fail"
                  :title="obsProbeResults[obsProbeKey(spec.key, env.id)]?.error"
                >✗ {{ obsProbeResults[obsProbeKey(spec.key, env.id)]?.error }}</span>
              </div>
              <!-- 字段块仅在以下情况显示:
                   - grafana 卡(始终显示自己的 URL+鉴权)
                   - SkyWalking / Loki 标签映射等不归 Grafana 代理管的
                   - 用户为本工具明确选了"直连"模式 -->
              <div
                v-if="!['loki','prometheus','jaeger','tempo','elk'].includes(spec.key) || getObsAccessMode(spec.key, env.id) === 'direct'"
                class="ds-item-fields"
              >
                <template v-for="f in spec.fields" :key="f.key">
                  <div
                    v-if="!isObsFieldHidden(spec.key, env.id, f)"
                    class="cc-field"
                  >
                    <label class="cc-field-label">
                      {{ f.label }}
                      <span v-if="f.optional" class="auto-tag">选填</span>
                      <span v-if="f.secret" class="cc-scope-tag secret" title="Secret:会写入 yaml,分享时注意范围">🔒 Secret</span>
                    </label>
                    <div class="cc-field-row">
                      <select
                        v-if="f.options"
                        v-model="toolInputs[toolKeyFor('obs', spec.key, env.id, f.key)]"
                        class="cc-input"
                        @change="spec.key === 'grafana' && scheduleGrafanaDsAutoload(env.id)"
                      >
                        <option v-for="opt in f.options" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
                      </select>
                      <template v-else>
                        <input
                          v-model="toolInputs[toolKeyFor('obs', spec.key, env.id, f.key)]"
                          :type="f.secret && !isRevealed(toolKeyFor('obs', spec.key, env.id, f.key)) ? 'password' : 'text'"
                          :placeholder="f.placeholder || ''"
                          autocomplete="off"
                          spellcheck="false"
                          class="cc-input"
                          @input="scheduleObsProbe(spec.key, env.id); spec.key === 'grafana' && scheduleGrafanaDsAutoload(env.id)"
                        />
                        <button
                          v-if="f.secret"
                          type="button"
                          class="btn-link cc-reveal"
                          @click="toggleReveal(toolKeyFor('obs', spec.key, env.id, f.key))"
                          :title="isRevealed(toolKeyFor('obs', spec.key, env.id, f.key)) ? '隐藏明文' : '显示明文'"
                        >{{ isRevealed(toolKeyFor('obs', spec.key, env.id, f.key)) ? '🙈' : '👁' }}</button>
                        <button
                          v-if="toolInputs[toolKeyFor('obs', spec.key, env.id, f.key)]"
                          type="button"
                          class="btn-link cc-delete"
                          @click="clearToolFieldInput(toolKeyFor('obs', spec.key, env.id, f.key))"
                          title="清空本字段"
                        >🗑</button>
                      </template>
                    </div>
                  </div>
                </template>
              </div>

              <!-- k8s 运行时:env 级先挑一次 集群 + namespace,服务级只挑各自的 Deployment。
                   类比 Grafana datasource:env 级一次性定位,然后所有标签/服务都基于这个上下文。
                   集群+ns 树复用 kuboardStateByEnv(同一个 Kuboard 不重拉)。 -->
              <div v-if="spec.key === 'k8s_runtime'" class="loki-env-mapping">
                <div class="loki-env-mapping-head">
                  ☸️ K8s 服务定位 ({{ env.id }}) —— 先挑集群 + namespace,再给每个服务挑 Deployment
                </div>
                <!-- Step 1: 加载集群资源 -->
                <div class="cc-preload-row" style="margin: 6px 0 10px 0;">
                  <button
                    v-if="kuboardStateByEnv[env.id]?.status === 'loading'"
                    type="button" class="btn cc-preload-btn" disabled
                  >
                    <span class="cc-preload-spinner" aria-hidden="true"></span>
                    拉取中…
                  </button>
                  <button
                    v-else-if="kuboardStateByEnv[env.id]?.status === 'ok'"
                    type="button" class="btn cc-preload-btn"
                    @click="runK8sRtPreload(env.id)"
                  >🔄 重新加载集群</button>
                  <button
                    v-else
                    type="button" class="btn cc-preload-btn"
                    @click="runK8sRtPreload(env.id)"
                  >📥 加载集群资源</button>
                  <span v-if="kuboardStateByEnv[env.id]?.status === 'ok'" class="cc-preload-summary">
                    ✓ {{ (kuboardStateByEnv[env.id] as any).clusters.length }} 个集群
                  </span>
                  <span v-else-if="kuboardStateByEnv[env.id]?.status === 'error'" class="cc-preload-error">
                    ✗ {{ (kuboardStateByEnv[env.id] as any).error.slice(0, 60) }}
                  </span>
                </div>

                <!-- Step 2: env 级挑集群 + namespace(只挑一次) -->
                <div
                  v-if="kuboardStateByEnv[env.id]?.status === 'ok'"
                  class="cc-field-row"
                  style="gap: 12px; margin-bottom: 10px; flex-wrap: wrap;"
                >
                  <div class="cc-field" style="flex: 1; min-width: 180px;">
                    <label class="cc-field-label">集群</label>
                    <select
                      :value="(k8sRuntimeEnvLoc[env.id] || {}).cluster || ''"
                      class="cc-input"
                      @change="(e: any) => setK8sRtEnvLoc(env.id, 'cluster', e.target.value)"
                    >
                      <option value="">— 选集群 —</option>
                      <option
                        v-for="c in ((kuboardStateByEnv[env.id] as any)?.clusters || [])"
                        :key="c.name" :value="c.name"
                      >{{ c.name }}</option>
                    </select>
                  </div>
                  <div class="cc-field" style="flex: 1; min-width: 180px;">
                    <label class="cc-field-label">Namespace</label>
                    <select
                      :value="(k8sRuntimeEnvLoc[env.id] || {}).namespace || ''"
                      :disabled="!((k8sRuntimeEnvLoc[env.id] || {}).cluster)"
                      class="cc-input"
                      @change="(e: any) => setK8sRtEnvLoc(env.id, 'namespace', e.target.value)"
                    >
                      <option v-if="!((k8sRuntimeEnvLoc[env.id] || {}).cluster)" value="">— 先选集群 —</option>
                      <option v-else value="">— 选 namespace —</option>
                      <option
                        v-for="n in kuboardNamespacesFor(env.id, (k8sRuntimeEnvLoc[env.id] || {}).cluster || '')"
                        :key="n" :value="n"
                      >{{ n }}</option>
                    </select>
                  </div>
                </div>

                <!-- Step 3: 每服务一行 Deployment 下拉(选完了 ns 才显示) -->
                <div
                  v-if="(k8sRuntimeEnvLoc[env.id] || {}).cluster && (k8sRuntimeEnvLoc[env.id] || {}).namespace && allServiceNames.length > 0"
                  class="cc-map-block"
                >
                  <div class="cc-map-head">
                    <span class="cc-map-title">
                      服务 → Deployment 映射 <span class="field-hint">— 留空表示该服务未在本环境部署到 K8s,排障时跳过 pod / log / metric 查询</span>
                    </span>
                  </div>
                  <div class="cc-map-svc-list">
                    <div
                      v-for="svc in allServiceNames"
                      :key="`k8srt-${env.id}-${svc}`"
                      class="cc-map-svc-row"
                    >
                      <span class="cc-map-svc-name">{{ svc }}</span>
                      <select
                        :value="(k8sRuntimeSvcMap[svcKey(env.id, svc)] || {}).workload || ''"
                        :disabled="k8sRtWorkloadCache[k8sRtWorkloadKey(env.id, (k8sRuntimeEnvLoc[env.id] || {}).cluster || '', (k8sRuntimeEnvLoc[env.id] || {}).namespace || '')]?.status === 'loading'"
                        class="cc-map-select"
                        style="min-width: 240px;"
                        @change="(e: any) => setK8sRtSvcWorkload(env.id, svc, e.target.value)"
                      >
                        <option v-if="k8sRtWorkloadCache[k8sRtWorkloadKey(env.id, (k8sRuntimeEnvLoc[env.id] || {}).cluster || '', (k8sRuntimeEnvLoc[env.id] || {}).namespace || '')]?.status === 'loading'" value="">— 正在拉取 Deployment 列表… —</option>
                        <option v-else-if="k8sRtWorkloadCache[k8sRtWorkloadKey(env.id, (k8sRuntimeEnvLoc[env.id] || {}).cluster || '', (k8sRuntimeEnvLoc[env.id] || {}).namespace || '')]?.status === 'error'" value="">— 拉取失败,详见日志面板 —</option>
                        <option v-else-if="k8sRtWorkloadsFor(env.id, (k8sRuntimeEnvLoc[env.id] || {}).cluster || '', (k8sRuntimeEnvLoc[env.id] || {}).namespace || '').length === 0" value="">— 当前 namespace 下无可用 Deployment —</option>
                        <option v-else value="">— 未部署 / 不在 K8s 内 —</option>
                        <option
                          v-for="d in k8sRtWorkloadsFor(env.id, (k8sRuntimeEnvLoc[env.id] || {}).cluster || '', (k8sRuntimeEnvLoc[env.id] || {}).namespace || '')"
                          :key="d.name" :value="d.name"
                          :title="d.selector ? 'selector: ' + d.selector : ''"
                        >{{ d.name }}</option>
                      </select>
                      <!-- selector 自动从 spec.selector.matchLabels 取后写进 yaml,
                           大多数 Deployment 是 app=<name> 跟下拉值重复;真要看用 hover option 的 tooltip。 -->
                    </div>
                  </div>
                </div>
              </div>

              <!-- via_grafana 模式(loki/prometheus/jaeger/tempo/elk 共用):
                   显示 datasource 选择 + 加载/刷新按钮。dsList 由 Grafana 卡填好 URL+鉴权后自动拉,
                   各工具卡也能就地点"刷新 datasources"。Loki 用 lokiMappingByEnv[env].dsUID 走自己的标签流程,
                   其他用 grafanaDsUidByObsEnv[obs:env]。 -->
              <div
                v-if="['loki','prometheus','jaeger','tempo','elk'].includes(spec.key) && getObsAccessMode(spec.key, env.id) === 'via_grafana'"
                class="loki-env-mapping"
              >
                <div class="loki-env-mapping-head">
                  🔗 选中 {{ spec.label }} 在 Grafana 里的 datasource
                </div>
                <div class="cc-field-row" style="gap: 12px; align-items: center; flex-wrap: wrap;">
                  <select
                    v-if="spec.key === 'loki'"
                    :value="getLokiMapping(env.id).dsUID || ''"
                    class="cc-input"
                    style="max-width: 420px;"
                    @change="(e: any) => getLokiMapping(env.id).dsUID = e.target.value"
                  >
                    <option value="">— 选 Loki datasource —</option>
                    <option
                      v-for="ds in obsGrafanaDsCandidates(env.id, 'loki')"
                      :key="ds.uid" :value="ds.uid"
                    >{{ ds.name }}({{ ds.type }}{{ ds.default ? ', default' : '' }})</option>
                  </select>
                  <select
                    v-else
                    :value="grafanaDsUidByObsEnv[obsGrafanaDsKey(spec.key, env.id)] || ''"
                    class="cc-input"
                    style="max-width: 420px;"
                    @change="(e: any) => grafanaDsUidByObsEnv[obsGrafanaDsKey(spec.key, env.id)] = e.target.value"
                  >
                    <option value="">— 不通过 Grafana / 留空 —</option>
                    <option
                      v-for="ds in obsGrafanaDsCandidates(env.id, spec.key)"
                      :key="ds.uid" :value="ds.uid"
                    >{{ ds.name }}({{ ds.type }}{{ ds.default ? ', default' : '' }})</option>
                  </select>
                  <button
                    v-if="getLokiMapping(env.id).dsListStatus === 'loading'"
                    type="button" class="btn cc-preload-btn" disabled
                  >
                    <span class="cc-preload-spinner" aria-hidden="true"></span>
                    加载中…
                  </button>
                  <button
                    v-else
                    type="button" class="btn cc-preload-btn"
                    @click="loadLokiDatasources(env.id)"
                  >🔄 {{ (getLokiMapping(env.id).dsList || []).length > 0 ? '刷新' : '加载' }} datasources</button>
                  <span
                    v-if="getLokiMapping(env.id).dsListStatus === 'fail'"
                    class="cc-preload-error"
                    :title="getLokiMapping(env.id).dsListError"
                  >✗ {{ getLokiMapping(env.id).dsListError?.slice(0, 50) }}</span>
                  <span
                    v-else-if="(getLokiMapping(env.id).dsList || []).length > 0 && obsGrafanaDsCandidates(env.id, spec.key).length === 0"
                    class="cc-preload-summary"
                    style="background: #fee2e2; color: #991b1b;"
                  >该 Grafana 里没找到 type={{ OBS_GRAFANA_DS_TYPES[spec.key]?.join('/') }} 的 datasource</span>
                  <span
                    v-else-if="(getLokiMapping(env.id).dsList || []).length > 0"
                    class="cc-preload-summary"
                  >✓ {{ obsGrafanaDsCandidates(env.id, spec.key).length }} 个 {{ OBS_GRAFANA_DS_TYPES[spec.key]?.join('/') }} 候选</span>
                </div>
              </div>

              <!-- Loki 标签映射(per-env):只在 Loki 卡显示。datasource 已在上面"访问方式"卡片块里选过,
                   这里只走"加载标签 → 选 env/service label key → 选 env/service label value"流程。 -->
              <div
                v-if="spec.key === 'loki'"
                class="loki-env-mapping"
              >
                <div class="loki-env-mapping-head">
                  🏷 Loki 标签映射 ({{ env.id }}) —— 拉实时标签后选出"区分 env"和"区分服务"的两个 label key
                </div>

                <!-- Step 1: labels -->
                <div class="loki-mapping-step">
                  <div class="loki-mapping-step-head">
                    <span class="loki-step-num">1</span> 加载 Loki 标签
                  </div>
                  <div class="cc-field-row">
                    <button
                      v-if="getLokiMapping(env.id).labelStatus === 'loading'"
                      :key="`loki-label-${env.id}-loading`"
                      type="button" class="btn cc-preload-btn" disabled
                    >
                      <span class="cc-preload-spinner" aria-hidden="true"></span>
                      加载中…
                    </button>
                    <button
                      v-else
                      :key="`loki-label-${env.id}-idle`"
                      type="button" class="btn cc-preload-btn"
                      @click="loadLokiLabels(env.id)"
                    >🏷 加载标签</button>
                    <span
                      v-if="getLokiMapping(env.id).labelStatus === 'ok'"
                      class="cc-preload-summary"
                    >✓ {{ getLokiMapping(env.id).labels.length }} 个 label</span>
                    <span
                      v-else-if="getLokiMapping(env.id).labelStatus === 'fail'"
                      class="url-probe-badge fail"
                      :title="getLokiMapping(env.id).labelError"
                    >✗ {{ getLokiMapping(env.id).labelError }}</span>
                  </div>
                </div>

                <!-- Step 3: 选 label keys -->
                <div v-if="getLokiMapping(env.id).labels.length > 0" class="loki-mapping-step">
                  <div class="loki-mapping-step-head">
                    <span class="loki-step-num">2</span> 选环境 / 服务维度的 label key
                  </div>
                  <div class="loki-axes">
                    <label class="loki-axis-label">
                      环境维度:
                      <select
                        :value="getLokiMapping(env.id).envLabelKey"
                        class="cc-map-select"
                        @change="(e: any) => onEnvLabelKeyChanged(env.id, e.target.value)"
                      >
                        <option value="">— 选 label key —</option>
                        <option v-for="l in getLokiMapping(env.id).labels" :key="l" :value="l">{{ l }}</option>
                      </select>
                    </label>
                    <label class="loki-axis-label">
                      服务维度:
                      <select
                        :value="getLokiMapping(env.id).serviceLabelKey"
                        class="cc-map-select"
                        @change="(e: any) => onServiceLabelKeyChanged(env.id, e.target.value)"
                      >
                        <option value="">— 选 label key —</option>
                        <option v-for="l in getLokiMapping(env.id).labels" :key="l" :value="l">{{ l }}</option>
                      </select>
                    </label>
                  </div>
                </div>

                <!-- Step 4: env value + per-service value -->
                <div
                  v-if="getLokiMapping(env.id).envLabelKey && getLokiMapping(env.id).serviceLabelKey"
                  class="loki-mapping-step"
                >
                  <div class="loki-mapping-step-head">
                    <span class="loki-step-num">3</span> 选 env / service 具体 label 值
                  </div>
                  <div class="loki-mapping-env-head">
                    <span class="loki-mapping-axis-name">{{ getLokiMapping(env.id).envLabelKey }}:</span>
                    <select
                      v-model="getLokiMapping(env.id).envValue"
                      class="cc-map-select"
                      @change="onEnvValueChanged(env.id)"
                    >
                      <option value="">— 选 —</option>
                      <option
                        v-for="v in getLokiMapping(env.id).envLabelValues"
                        :key="v" :value="v"
                      >{{ v }}</option>
                    </select>
                  </div>
                  <div
                    v-for="svc in allServiceNames"
                    :key="svc"
                    class="loki-mapping-svc-row"
                  >
                    <span class="loki-mapping-svc-name">{{ svc }}</span>
                    <span class="loki-mapping-axis-name">{{ getLokiMapping(env.id).serviceLabelKey }}:</span>
                    <select
                      v-model="getLokiMapping(env.id).serviceValues[svc]"
                      class="cc-map-select"
                      :class="{ 'cc-map-select-none': !getLokiMapping(env.id).serviceValues[svc] }"
                      :title="!getLokiMapping(env.id).serviceValues[svc] && getLokiMapping(env.id).serviceMatchTried?.[svc] && getLokiMapping(env.id).serviceLabelValues.length > 0
                        ? `自动匹配未找到候选(${getLokiMapping(env.id).serviceLabelValues.length} 个 label 都不含 ${svc} 任一前缀:${svc.split('-').filter(Boolean).join(' / ')})。本环境可能没部署该服务,留空即可。`
                        : ''"
                    >
                      <option value="">{{
                        !getLokiMapping(env.id).serviceValues[svc]
                          && getLokiMapping(env.id).serviceMatchTried?.[svc]
                          && getLokiMapping(env.id).serviceLabelValues.length > 0
                          ? '— 无 / 不进 loki(未自动匹配到) —'
                          : '— 无 / 不进 loki —'
                      }}</option>
                      <option
                        v-for="v in getLokiMapping(env.id).serviceLabelValues"
                        :key="v" :value="v"
                      >{{ v }}</option>
                    </select>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>

      <!-- 副源卡片块已废弃 —— 多源现在由顶部 checkbox 多选驱动,每个勾选的 type 在
           上方主表单里独立渲染(通过 activeSourceTypes v-for 包装)。
           Step 4 已删除 config_source 下拉,服务到源的映射改在 Step 6/7 里按服务配。 -->
    </div>

    <!-- Step 6:数据层 —— 从配置源拉取各服务配置,按"环境 → 服务 → 数据层组件"展示识别结果 -->
    <div v-if="currentStep === 7" class="card lg">
      <h2>数据层</h2>
      <p class="help-text">
        从配置中心读取各服务配置,自动识别用到的数据层(Redis / MySQL / MongoDB 等),按 <strong>环境 → 服务 → 组件</strong> 展示。字段可直接编辑,改的是本地副本,不会回写配置中心。
      </p>

      <div class="cc-share-warn" style="margin-bottom:18px">
        <div class="cc-share-warn-title">⚠ 数据流与共享范围</div>
        <ul class="cc-share-warn-list">
          <li>本页字段(含密码、token 等凭证)将保存至 <code>system.yaml</code>。</li>
          <li>部署时,生成器把对应值注入目标 AI 平台的 MCP Server 环境变量。</li>
          <li><strong>system.yaml 含明文凭证</strong>,请仅在可信范围内分享。</li>
        </ul>
      </div>

      <div class="ds-autoimport-row">
        <!-- loading / idle 分别用独立 <button> + :key,避免 WebKit GPU layer 残影(同 Step 5 按钮) -->
        <button
          v-if="dsImportStatus === 'loading'"
          :key="'ds-import-loading'"
          class="btn primary cc-preload-btn"
          disabled
        >
          <span class="cc-preload-spinner" aria-hidden="true"></span>
          读取中…
        </button>
        <button
          v-else
          :key="'ds-import-idle'"
          class="btn primary cc-preload-btn"
          :disabled="!canAutoImportDS"
          @click="autoImportDataStores"
        >📥 从配置中心读取</button>
        <span v-if="!canAutoImportDS" class="ds-autoimport-hint">
          需先在 Step 5 完成配置源扫描 + 映射服务 dataId
        </span>
        <span v-else-if="dsImportStatus === 'ok'" class="cc-preload-summary">
          ✓ 成功拉 {{ dsImportStats.scanned }} / 应拉 {{ environments.length * allServiceNames.length }} 条配置(env × service),识别 {{ dsImportStats.matched }} 个数据层
        </span>
      </div>

      <!-- 按 env → 全部 service(allServiceNames) → ds 层级完整展示;
           缺失项(没映射 / 拉失败 / 未识别)也会出现一条,明确标原因。
           这样 5 服务 × 2 环境 = 10 条永远能看全,不会"只扫出 8 条"。 -->
      <div class="ds-hierarchy">
        <div v-for="env in environments" :key="env.id" class="ds-env-section">
          <div class="ds-env-title">
            <span class="cc-env-label">{{ env.id || '(未命名 env)' }}</span>
            <span v-if="env.is_prod" class="cc-env-prod-tag">prod</span>
            <span class="ds-env-count">
              {{ allServiceNames.length }} 个服务 ·
              已识别 {{ Object.values(scannedDS[env.id] || {}).filter(s => Object.keys(s).length > 0).length }}
            </span>
            <button
              v-if="probingByEnv[env.id]"
              :key="`probe-all-${env.id}-loading`"
              type="button"
              class="ds-env-probe-all loading"
              disabled
            >
              <span class="cc-preload-spinner" aria-hidden="true"></span>
              测试中…
            </button>
            <button
              v-else-if="Object.values(scannedDS[env.id] || {}).some(s => Object.keys(s).length > 0)"
              :key="`probe-all-${env.id}-idle`"
              type="button"
              class="ds-env-probe-all"
              title="对本环境所有数据层组件批量执行连通性测试(串行,逐条 5s 超时)"
              @click="probeAllForEnv(env.id)"
            >🔌 一键连通性测试</button>
          </div>

          <div v-if="allServiceNames.length === 0" class="ds-empty">
            Step 4 还没填 <code>service_names</code>,这里没服务可扫
          </div>

          <div v-else class="ds-svc-container">
            <div
              v-for="svc in allServiceNames"
              :key="svc"
              class="ds-svc-block"
              :class="['status-' + (scanStateOf(env.id, svc)?.status || 'pending')]"
            >
              <div class="ds-svc-head">
                <span class="ds-svc-name">📁 {{ svc }}</span>
                <span
                  v-if="serviceConfigSel[svcKey(env.id, svc)]"
                  class="ds-svc-dataid"
                  :title="'来源 dataId: ' + serviceConfigSel[svcKey(env.id, svc)]"
                >← {{ serviceConfigSel[svcKey(env.id, svc)] }}</span>
                <span
                  v-if="scanStateOf(env.id, svc)"
                  class="ds-svc-status"
                  :class="'status-' + scanStateOf(env.id, svc)!.status"
                >
                  <template v-if="scanStateOf(env.id, svc)!.status === 'ok'">✓ 已识别</template>
                  <template v-else-if="scanStateOf(env.id, svc)!.status === 'empty'">✓ 已读取 · 无数据层</template>
                  <template v-else-if="scanStateOf(env.id, svc)!.status === 'skipped'">⊘ 跳过</template>
                  <template v-else-if="scanStateOf(env.id, svc)!.status === 'error'">✗ 拉取失败</template>
                </span>
              </div>

              <!-- 状态详情 -->
              <div v-if="scanStateOf(env.id, svc)?.reason" class="ds-status-reason">
                {{ scanStateOf(env.id, svc)!.reason }}
              </div>

              <!-- 识别结果列表 -->
              <div
                v-if="Object.keys(scannedDS[env.id]?.[svc] || {}).length > 0"
                class="ds-item-list"
              >
                <div
                  v-for="dsKey in Object.keys(scannedDS[env.id][svc]).sort()"
                  :key="dsKey"
                  class="ds-item"
                >
                  <div class="ds-item-head">
                    <span class="ds-item-badge">🗄 {{ dsLabel(dsKey) }}</span>
                    <!-- 连通性测试按钮 + 状态徽章 -->
                    <button
                      v-if="dsProbeResults[probeKey(env.id, svc, dsKey)]?.status === 'loading'"
                      :key="`probe-${env.id}-${svc}-${dsKey}-loading`"
                      type="button"
                      class="ds-item-probe loading"
                      disabled
                    >测试中…</button>
                    <button
                      v-else
                      :key="`probe-${env.id}-${svc}-${dsKey}-${dsProbeResults[probeKey(env.id, svc, dsKey)]?.status || 'idle'}`"
                      type="button"
                      class="ds-item-probe"
                      :class="dsProbeResults[probeKey(env.id, svc, dsKey)]?.status || 'idle'"
                      :title="dsProbeResults[probeKey(env.id, svc, dsKey)]?.detail || dsProbeResults[probeKey(env.id, svc, dsKey)]?.error || '点击测试连通性 (TCP dial + 协议握手,不读不写数据)'"
                      @click="probeOneDS(env.id, svc, dsKey)"
                    >
                      <template v-if="dsProbeResults[probeKey(env.id, svc, dsKey)]?.status === 'ok'">
                        ✓ 已连通 · {{ dsProbeResults[probeKey(env.id, svc, dsKey)]?.latency }}
                      </template>
                      <template v-else-if="dsProbeResults[probeKey(env.id, svc, dsKey)]?.status === 'fail'">
                        ✗ 连接异常,点击重试
                      </template>
                      <template v-else>🔌 连通性测试</template>
                    </button>
                    <button
                      type="button"
                      class="ds-item-delete"
                      :title="`删除本服务识别到的 ${dsLabel(dsKey)} —— 不影响下一步校验(扫描状态保留)`"
                      @click="removeScannedDS(env.id, svc, dsKey)"
                    >✕</button>
                  </div>
                  <!-- 失败时把详细 error 展开显示在 head 下方 -->
                  <div
                    v-if="dsProbeResults[probeKey(env.id, svc, dsKey)]?.status === 'fail'"
                    class="ds-probe-error"
                  >
                    {{ dsProbeResults[probeKey(env.id, svc, dsKey)]?.error }}
                  </div>
                  <div class="ds-item-fields">
                    <div
                      v-for="fKey in Object.keys(scannedDS[env.id][svc][dsKey])"
                      :key="fKey"
                      class="cc-field"
                    >
                      <label class="cc-field-label">
                        {{ dsFieldLabel(dsKey, fKey) }}
                        <span
                          v-if="dsFieldIsSecret(dsKey, fKey)"
                          class="cc-scope-tag secret"
                          title="Secret:会写入 yaml,分享时注意范围"
                        >🔒 Secret</span>
                      </label>
                      <div class="cc-field-row">
                        <input
                          v-model="scannedDS[env.id][svc][dsKey][fKey]"
                          :type="dsFieldIsSecret(dsKey, fKey) && !isRevealed(`ds:${env.id}:${svc}:${dsKey}:${fKey}`) ? 'password' : 'text'"
                          autocomplete="off"
                          spellcheck="false"
                          class="cc-input"
                        />
                        <button
                          v-if="dsFieldIsSecret(dsKey, fKey)"
                          type="button"
                          class="btn-link cc-reveal"
                          @click="toggleReveal(`ds:${env.id}:${svc}:${dsKey}:${fKey}`)"
                          :title="isRevealed(`ds:${env.id}:${svc}:${dsKey}:${fKey}`) ? '隐藏明文' : '显示明文'"
                        >{{ isRevealed(`ds:${env.id}:${svc}:${dsKey}:${fKey}`) ? '🙈' : '👁' }}</button>
                      </div>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <!-- Step 9: 预览 + 生成 -->
    <div v-if="currentStep === 9" class="card lg">
      <h2>预览 + 生成</h2>
      <!-- target 已在 Step 2 勾选;这里只读展示一下"会产出哪些平台",不让改。
           想改回去 Step 2(机器人身份)那一步。 -->
      <div class="target-readonly-row">
        <span class="target-readonly-label">本次部署目标:</span>
        <span
          v-for="t in targetOptions"
          v-show="enabledTargets[t]"
          :key="t"
          class="target-readonly-chip"
        >{{ targetLabels[t] }}</span>
        <span v-if="!anyTargetSelected" class="error-text">
          Step 2 没勾选任何部署目标,无法生成产物
        </span>
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
        <button class="btn" @click="downloadYAML">⬇ 导出</button>
      </div>
      <div v-if="validateResult" class="validate-result" :class="{ success: validateResult.ok, fail: !validateResult.ok }">
        {{ validateResult.message }}
      </div>
    </div>

    <!-- Step 9: 一键部署 (流程末步) -->
    <div v-if="currentStep === 10" class="card lg">
      <h2>一键部署</h2>
      <p class="help-text" style="margin-bottom:14px;">
        按 Step 2 勾选的目标一次性部署,直接复用前面填的凭证,<strong>跑完即生效</strong>。
        OpenClaw 若有字段前面没填,会回退到「已装机器人」页让你补齐。
      </p>
      <div v-if="deploySummary.length === 0" class="alert warn">
        Step 2 没勾选任何部署目标,无法一键部署。请回 Step 2 至少勾选一个 AI 平台。
      </div>
      <div v-else class="deploy-final-block">
        <div class="deploy-targets-line">
          将部署到:
          <span v-for="(item, i) in deploySummary" :key="item.target">
            <span class="deploy-target-chip">{{ item.label }}</span><span v-if="i < deploySummary.length - 1">、</span>
          </span>
        </div>
        <div class="deploy-inline-actions">
          <button
            type="button"
            class="btn primary deploy-final-btn"
            :disabled="deployLoading || deploySummary.length === 0"
            @click="runOneClickDeploy"
          >
            {{ deployLoading ? '部署中…' : `🚀 一键部署到 ${deploySummary.length} 个目标` }}
          </button>
        </div>
        <div v-if="deployError" class="alert error">{{ deployError }}</div>
      </div>
    </div>

    <!-- Navigation buttons - 欢迎页(Step 1)隐藏,因为它有两个大选择按钮做导航,
         底部再加"下一步"会跟那两个 CTA 视觉混淆。-->
    <div v-if="currentStep > 1" class="nav-buttons">
      <button class="btn" @click="prevStep">上一步</button>
      <div v-if="currentStep < totalSteps" class="next-wrap">
        <!-- 未过校验时在按钮上方显示"还差什么",用户一眼看出要填啥 -->
        <div v-if="!canGoNext" class="next-block-hint">{{ nextBlockedHint }}</div>
        <button
          class="btn primary"
          :disabled="!canGoNext"
          :title="nextBlockedHint || ''"
          @click="nextStep"
        >下一步</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.render-error-banner {
  margin-bottom: 16px; padding: 14px 18px;
  background: #fef2f2; border: 1px solid #fca5a5; border-radius: 8px;
  color: #7f1d1d; font-size: 13px;
}
.render-error-head { font-weight: 700; margin-bottom: 6px; }
.render-error-msg { font-family: monospace; font-size: 12px; margin-bottom: 6px; word-break: break-word; }
.render-error-stack {
  font-family: monospace; font-size: 11px; color: #991b1b;
  background: #fff; border: 1px solid #fecaca; border-radius: 4px;
  padding: 8px 10px; max-height: 160px; overflow: auto; white-space: pre-wrap;
  margin-bottom: 8px;
}
.render-error-actions { display: flex; gap: 8px; }

.init-page {
  max-width: 860px;
  margin: 0 auto;
}

/* 标识符等只读字段:灰底 + 鼠标 default,跟普通 input 视觉拉开 */
.readonly-input {
  background: #f1f5f9 !important;
  color: #475569;
  cursor: default;
  font-family: monospace;
}
.readonly-input:focus { outline: none; border-color: #cbd5e1; }

.init-page h1 {
  font-size: 24px;
  color: #1e293b;
  margin-bottom: 4px;
}

.subtitle {
  color: #64748b;
  font-size: 14px;
  margin-bottom: 0;
  line-height: 1.6;
}

.page-header {
  display: flex;
  justify-content: space-between;
  align-items: flex-start;
  gap: 16px;
  margin-bottom: 16px;
}

/* 顶部信息卡:把标题 + 自动保存 + 简介 + 进度条合并成一张卡,
   跟下面 step 卡视觉一致(白底 + border + 圆角)。 */
.init-header-card {
  margin-bottom: 18px;
}
.init-header-info {
  margin-bottom: 16px;
}
.init-header-progress {
  margin-bottom: 0;       /* 卡内最后一项,不要尾部多余空白 */
  padding-top: 4px;
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
/* 每步卡片标题 + 说明 + 表单之间留出呼吸空间,避免标题贴下面 help-text */
.card h2 {
  font-size: 20px;
  font-weight: 600;
  color: #1e293b;
  margin: 0 0 14px 0;
  line-height: 1.3;
}
.card .help-text {
  margin-bottom: 18px;
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

/* 全局默认 clone 父目录:Step 4 顶部一次性设置 */
.global-default-block {
  margin-bottom: 14px; padding: 12px 14px;
  background: #f0fdf4; border: 1px solid #bbf7d0;
  border-left: 3px solid #10b981;
  border-radius: 8px;
}
.global-default-label {
  display: block; font-size: 13px; font-weight: 500;
  color: #065f46; margin-bottom: 8px;
}
.global-default-label .field-hint { font-weight: 400; color: #047857; font-size: 11px; }
.global-default-label .saved-indicator {
  color: #059669; font-weight: 600; margin-left: 4px;
}
.global-default-row {
  display: flex; gap: 8px; align-items: center;
}
.global-default-row input[type="text"] {
  flex: 1; padding: 7px 10px; border: 1px solid #86efac; border-radius: 6px;
  font-size: 13px; font-family: monospace; background: #fff;
}
.global-default-row input[type="text"]:focus { outline: none; border-color: #10b981; }

/* 扫描按钮单独一行,跟 global-default-block 分开 */
.scan-action-row {
  display: flex; gap: 12px; align-items: center;
  margin-bottom: 14px;
}
.scan-btn { font-size: 14px; padding: 10px 20px; }
.analyze-progress-inline {
  display: inline-flex; align-items: center; gap: 6px;
  font-size: 12px; color: #64748b;
}

/* 本地 / 远程 来源切换 */
.source-toggle {
  display: flex; gap: 10px;
}
.source-option {
  flex: 1; display: flex; flex-direction: column; gap: 4px;
  padding: 10px 14px; border: 1px solid #cbd5e1; border-radius: 8px;
  cursor: pointer; background: #fff; transition: all 0.15s;
}
.source-option:hover { border-color: #94a3b8; }
.source-option.selected {
  border-color: #3b82f6; background: #eff6ff;
  box-shadow: 0 0 0 2px rgba(59, 130, 246, 0.15);
}
.source-option input[type="radio"] { margin-right: 6px; }
.source-option .source-title { font-size: 13px; font-weight: 500; color: #1e293b; }
.source-option .source-hint { font-size: 11px; color: #64748b; padding-left: 22px; }

/* 路径输入 + 选目录按钮 */
.path-input-row {
  display: flex; gap: 8px;
}
.path-input-row input[type="text"] {
  flex: 1; font-family: monospace; font-size: 13px;
}
/* .path-readonly 全局样式(不挂 .path-input-row 下),所有路径 input 用同一套灰底样式 */
input.path-readonly {
  background: #f8fafc !important;
  color: #475569 !important;
  cursor: default;
  text-overflow: ellipsis;
}

/* 本地模式下 git remote 反填结果提示(内联小字,不占独立输入框) */
.local-url-probe {
  margin-top: 6px; font-size: 12px; line-height: 1.5;
}
.local-url-probe code {
  background: #f1f5f9; padding: 1px 5px; border-radius: 3px; font-size: 11px;
}
.local-url-probe.ok { color: #047857; }
.local-url-probe.warn { color: #b45309; }

/* 每个仓库的同步/重扫按钮 + 状态行 */
.repo-sync-row {
  display: flex; gap: 10px; align-items: center;
  margin-bottom: 12px;
}
.repo-scan-error { font-size: 12px; color: #b91c1c; }
.repo-scan-ok { font-size: 12px; color: #047857; }

/* Step 1 部署目标卡片:每家一张,勾选后 inline 展开该 target 的配置(模型 / 工作区名) */
.target-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}
.target-card {
  display: flex; flex-direction: column; gap: 8px;
  padding: 12px 14px;
  border: 1px solid #cbd5e1; border-radius: 8px;
  background: #fff;
  transition: all 0.15s;
}
.target-card:hover { border-color: #94a3b8; }
.target-card.selected {
  border-color: #3b82f6; background: #eff6ff;
  box-shadow: 0 0 0 2px rgba(59,130,246,0.15);
}
.target-card.target-disabled {
  background: #f8fafc; opacity: 0.78;
}
.target-card.target-disabled .target-card-head .target-title { color: #64748b; }
.target-card .target-missing-actions {
  display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
  margin-top: 4px; padding: 6px 10px 6px 22px;
  font-size: 11px; line-height: 1.4; color: #92400e;
}
.target-card .target-missing-actions.overridden { color: #b45309; }
.target-card .target-missing-actions .btn-link {
  background: none; border: none; padding: 0;
  color: #2563eb; font-size: 11px; cursor: pointer; text-decoration: underline;
}
.target-card-head {
  display: flex; align-items: center; gap: 8px; cursor: pointer;
}
.target-card-head .target-title {
  font-size: 14px; font-weight: 600; color: #1e293b;
}
.target-card-head .target-install-badge {
  margin-left: auto;
  padding: 2px 8px; border-radius: 10px;
  font-size: 11px; font-weight: 500;
  font-family: monospace;
}
.target-card-head .target-install-badge.ok {
  background: #dcfce7; color: #166534; border: 1px solid #86efac;
}
.target-card-head .target-install-badge.miss {
  background: #fef3c7; color: #92400e; border: 1px solid #fde68a;
}
.target-card .target-hint {
  font-size: 11px; color: #64748b; padding-left: 22px; line-height: 1.4;
}
/* 勾选后展示的部署位置一行,跟 target-hint 一档缩进 */
.target-card .target-deploy-path {
  display: flex; align-items: center; gap: 8px; flex-wrap: wrap;
  margin-top: 6px; padding: 6px 10px 6px 22px;
  font-size: 11px; line-height: 1.4;
}
.target-card .target-deploy-path-label { font-weight: 600; color: #334155; }
.target-card .target-deploy-path code {
  flex: 1; min-width: 0; font-size: 11px; color: #1e40af;
  background: var(--c-surf-3); padding: 2px 6px; border-radius: 4px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.target-card .target-body {
  margin-top: 4px; padding: 10px 12px;
  background: #fff; border: 1px solid #dbeafe; border-radius: 6px;
  display: flex; flex-direction: column; gap: 10px;
}
.target-card .target-field {
  display: flex; flex-direction: column; gap: 4px;
}
.target-card .target-field-label {
  font-size: 11px; font-weight: 600; color: #334155;
  display: flex; align-items: center; gap: 6px;
}
.target-card .target-field select,
.target-card .target-field input {
  padding: 6px 8px; border: 1px solid #cbd5e1; border-radius: 4px;
  font-size: 12px;
}
.target-card .target-field select:focus,
.target-card .target-field input:focus {
  outline: none; border-color: #3b82f6;
}
.target-card .target-field.target-note {
  font-size: 11px; color: #64748b; padding: 4px 0;
}
.target-card .target-custom-input { margin-top: 4px; font-family: monospace; }

/* API key 块:输入框 + 保存按钮;已存态用绿色 + "换一个"切回输入 */
.apikey-field .apikey-input-row {
  display: flex; gap: 6px; align-items: center;
}
.apikey-field .apikey-input-row input { flex: 1; font-family: monospace; }
.apikey-field .apikey-saved {
  display: flex; align-items: center; justify-content: space-between;
  padding: 4px 8px;
  background: #f0fdf4; border: 1px solid #bbf7d0; border-radius: 4px;
  font-size: 12px; color: #047857;
}
.apikey-field .apikey-error { color: #b91c1c; font-size: 11px; margin-top: 4px; }
.apikey-field .apikey-hint { color: #64748b; font-size: 11px; margin-top: 4px; }

/* Step 5 配置中心凭证:按 env 分块。用左侧色条做"这是 env 分组"的信号,
   不用全边框 + 圆角 —— 避免跟按钮视觉撞车(用户看到圆角矩形会误以为是另一个按钮)。 */
.cc-env-block {
  padding: 10px 14px 12px 18px; margin-bottom: 16px;
  background: #f8fafc;
  border-left: 3px solid #3b82f6;
  border-top: none; border-right: none; border-bottom: none;
  border-radius: 0 4px 4px 0;
}
.cc-env-head {
  display: flex; align-items: center; gap: 8px; margin-bottom: 10px;
  font-size: 13px; font-weight: 600; color: #1e293b;
}
.cc-env-label { font-family: monospace; color: #3b82f6; }
.cc-env-prod-tag {
  font-size: 10px; padding: 1px 6px;
  background: #fef3c7; color: #92400e; border: 1px solid #fde68a;
  border-radius: 10px; font-weight: 500;
}
.cc-env-fields {
  display: grid; grid-template-columns: 1fr 1fr; gap: 12px;
}
.cc-field { display: flex; flex-direction: column; gap: 4px; }
.cc-field-label {
  font-size: 11px; font-weight: 600; color: #334155;
  display: flex; align-items: center; gap: 6px;
}
.cc-scope-tag {
  font-size: 10px; font-weight: 500;
  padding: 1px 6px; border-radius: 8px;
}
.cc-scope-tag.secret { color: #92400e; background: #fef3c7; }

/* Step 5 顶部:提醒 yaml 含明文凭证,分享要限范围(正式文案 + 列表样式) */
/* ── 顶部多选 checkbox 行(Step 5 配置源)── */
.source-types-checkboxes {
  display: flex; flex-wrap: wrap; gap: 8px;
  margin-top: 6px;
}
.source-type-pill {
  display: flex; align-items: center; gap: 8px;
  padding: 8px 12px;
  border: 1px solid #e2e8f0; border-radius: 6px;
  background: #fff; cursor: pointer;
  transition: border-color 0.15s, background 0.15s;
  flex: 1 1 calc(50% - 8px);
  min-width: 240px;
}
.source-type-pill:hover { border-color: #93c5fd; }
.source-type-pill.active {
  background: #eff6ff; border-color: #3b82f6;
}
.source-type-pill input { margin: 0; }
.source-type-pill-name {
  font-family: monospace; font-weight: 600; font-size: 13px;
  color: #1e3a8a;
  flex-shrink: 0;
}
.source-type-pill-desc {
  font-size: 11px; color: #64748b; line-height: 1.4;
}

/* 副源连接表单(简化版,跟主源同视觉密度但黄色 left-border 区分) */
.secondary-source-form {
  margin-top: 18px; padding-top: 14px;
  border-top: 1px dashed #cbd5e1;
}
.secondary-source-form > label {
  font-weight: 600; color: #92400e;
}
.secondary-source-form > label code {
  font-family: monospace; background: #fffbeb; padding: 1px 6px; border-radius: 3px;
  border: 1px solid #fde68a; color: #92400e;
}

/* 服务勾选清单(每个 env 工作区顶部) */
.cc-svc-checklist {
  margin-top: 12px; margin-bottom: 12px;
  padding: 10px 12px;
  background: #f8fafc; border: 1px solid #e2e8f0; border-radius: 6px;
}
.cc-svc-checklist-head { margin-bottom: 8px; line-height: 1.6; }
.cc-svc-checklist-title { font-size: 13px; font-weight: 600; color: #334155; }
.cc-svc-checklist-hint { font-size: 11px; color: #64748b; margin-left: 8px; }
.cc-svc-checklist-hint code {
  font-family: monospace; background: #fff; padding: 1px 4px; border-radius: 3px;
}
.cc-svc-checklist-grid {
  display: flex; flex-wrap: wrap; gap: 6px;
}
.cc-svc-checklist-item {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 4px 10px;
  background: #fff; border: 1px solid #e2e8f0; border-radius: 14px;
  font-size: 12px; color: #475569; cursor: pointer;
  transition: all 0.15s;
}
.cc-svc-checklist-item:hover { border-color: #93c5fd; }
.cc-svc-checklist-item.checked {
  background: #eff6ff; border-color: #3b82f6; color: #1e40af; font-weight: 600;
}
.cc-svc-checklist-item input { margin: 0; }
.cc-svc-checklist-name { font-family: monospace; }
.cc-svc-checklist-empty {
  margin-top: 12px; margin-bottom: 12px;
  padding: 8px 12px;
  background: #fffbeb; border: 1px dashed #fde68a; border-radius: 6px;
  font-size: 11px; color: #92400e; line-height: 1.6;
}
.cc-svc-checklist-empty code {
  font-family: monospace; background: #fff; padding: 1px 4px; border-radius: 3px;
}

.multi-source-mgr-hint {
  font-size: 12px; color: #075985; line-height: 1.6;
}
.multi-source-mgr-hint code {
  font-family: monospace; background: #fff; padding: 1px 4px; border-radius: 3px;
}

/* 副源卡片:跟主源 form-group 同视觉密度,不弱化(都是平等的源) */
.extra-source-card {
  margin-top: 18px; padding: 16px;
  background: #fff; border: 1px solid #cbd5e1; border-radius: 8px;
  border-left: 3px solid #3b82f6;
}
.extra-source-head {
  display: flex; align-items: center; justify-content: space-between;
  margin-bottom: 14px; padding-bottom: 10px; border-bottom: 1px dashed #e2e8f0;
}
.extra-source-id { font-size: 14px; font-weight: 600; color: #1e3a8a; }
.extra-source-id code {
  font-family: monospace; background: #dbeafe; padding: 2px 6px; border-radius: 3px;
  margin-left: 4px; color: #1e40af;
}
/* ── 多源 banner / tip(Step 5,legacy 名,保留兼容)── */
.multi-source-banner {
  padding: 14px 18px; margin-bottom: 18px;
  background: #eff6ff; border: 1px solid #bfdbfe; border-left: 3px solid #3b82f6;
  border-radius: 6px; font-size: 13px; color: #1e3a8a; line-height: 1.6;
}
.multi-source-title { font-weight: 700; font-size: 14px; margin-bottom: 6px; }
.multi-source-body { margin-bottom: 8px; }
.multi-source-list {
  list-style: none; padding: 0; margin: 0 0 10px;
  display: flex; flex-direction: column; gap: 6px;
}
.multi-source-list li {
  display: flex; align-items: center; gap: 8px;
  padding: 6px 10px; background: #fff; border: 1px solid #dbeafe; border-radius: 4px;
}
.multi-source-list code { font-family: monospace; color: #1e40af; font-size: 12px; }
.multi-source-list .muted { color: #64748b; font-size: 11px; }
.multi-source-list .btn-link { margin-left: auto; color: #b91c1c; }
.multi-source-hint {
  font-size: 11px; color: #475569; line-height: 1.5;
  padding-top: 8px; border-top: 1px dashed #c7d2fe;
}
.multi-source-tip {
  padding: 10px 14px; margin-bottom: 14px;
  background: #f8fafc; border: 1px dashed #cbd5e1; border-radius: 6px;
  font-size: 12px; color: #475569; line-height: 1.6;
}
.multi-source-tip code { font-family: monospace; background: #fff; padding: 1px 4px; border-radius: 3px; }

.cc-share-warn {
  padding: 12px 16px; margin-bottom: 14px;
  background: #fffbeb; border: 1px solid #fde68a; border-left: 3px solid #f59e0b;
  border-radius: 6px; font-size: 12px; color: #78350f; line-height: 1.6;
}
.cc-share-warn-title {
  font-weight: 600; font-size: 13px; color: #92400e;
  margin-bottom: 6px;
}
.cc-share-warn-list {
  margin: 0; padding-left: 20px;
  display: flex; flex-direction: column; gap: 4px;
}
.cc-share-warn-list li { line-height: 1.6; }
.cc-share-warn code {
  background: #fde68a; color: #78350f;
  padding: 1px 4px; border-radius: 3px; font-size: 11px;
}
.cc-field-row { display: flex; gap: 4px; align-items: center; }
.cc-input {
  flex: 1 1 0;
  /* min-width:0 让 flex:1 真的抢到全部宽度,不被 input 内置 default size(~20em)撑开;
     box-sizing:border-box + 显式 line-height + height 固定盒模型,避免 password ↔ text
     切换时 user-agent 对两种 input 各自的默认字符 metrics / 内边距差异引起的高/宽抖动。 */
  min-width: 0; width: 100%;
  box-sizing: border-box;
  height: 30px; line-height: 18px;
  padding: 6px 8px;
  border: 1px solid #cbd5e1; border-radius: 4px;
  font-size: 12px; font-family: monospace;
  /* 固定字符表现:password 和 text 在某些 webkit 下字符宽度不同,letter-spacing 拉一致。
     另外 password 的圆点字符行高在某些字体下偏大,显式 line-height 压住。 */
  letter-spacing: 0;
}
.cc-input:focus { outline: none; border-color: #3b82f6; }

/* Step 3 域名连通性徽章 —— 跟在 label 后面,跟字段输入框水平对齐 */
.url-probe-badge {
  margin-left: 8px; padding: 1px 6px;
  font-size: 10px; font-weight: 600; border-radius: 8px;
  white-space: nowrap; max-width: 200px; overflow: hidden; text-overflow: ellipsis;
  display: inline-block; vertical-align: middle;
}
.url-probe-badge.loading { background: #f1f5f9; color: #64748b; }
.url-probe-badge.ok      { background: #d1fae5; color: #047857; }
.url-probe-badge.fail    { background: #fee2e2; color: #991b1b; }

/* Step 7 Loki 标签映射子区 —— 挂在 grafana / loki 卡片下,跟同卡片下 env-row 平级 */
.loki-mapping {
  margin-bottom: 12px; padding: 12px;
  background: #fef9c3; border: 1px solid #fde68a; border-left: 3px solid #f59e0b;
  border-radius: 6px;
}
.loki-mapping-head {
  font-size: 13px; font-weight: 600; color: #78350f;
  margin-bottom: 10px;
}
.loki-mapping-step { margin-bottom: 10px; }
.loki-mapping-step-head {
  display: flex; align-items: center; gap: 6px;
  font-size: 11px; font-weight: 600; color: #92400e;
  margin-bottom: 6px;
}
.loki-step-num {
  display: inline-flex; justify-content: center; align-items: center;
  width: 16px; height: 16px;
  background: #f59e0b; color: #fff;
  border-radius: 50%; font-size: 10px; font-weight: 700;
}
.loki-axes {
  display: flex; flex-wrap: wrap; gap: 12px;
}
.loki-axis-label {
  display: flex; align-items: center; gap: 6px;
  font-size: 11px; color: #78350f; font-weight: 500;
}
.loki-mapping-grid {
  display: flex; flex-direction: column; gap: 8px;
}
.loki-mapping-env-section {
  padding: 8px 10px;
  background: #fff; border: 1px solid #fde68a; border-radius: 4px;
}
.loki-mapping-env-head {
  display: flex; align-items: center; gap: 8px;
  margin-bottom: 6px;
}
.loki-mapping-svc-row {
  display: flex; align-items: center; gap: 8px;
  padding: 3px 8px; margin-top: 4px;
  background: #fef9c3; border-radius: 3px;
}
.loki-mapping-svc-name {
  font-family: monospace; font-size: 12px; font-weight: 500; color: #78350f;
  min-width: 100px;
}
.loki-mapping-axis-name {
  font-size: 11px; color: #92400e; font-family: monospace;
  min-width: 80px;
}

/* Step 7 启用组件 chip 选择栏(顶部) */
.obs-tool-chips {
  display: flex; flex-wrap: wrap; gap: 8px;
  margin-bottom: 14px;
}
.obs-tool-chip {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 6px 12px;
  background: #fff; border: 1px solid #cbd5e1; border-radius: 16px;
  font-size: 12px; color: #475569; cursor: pointer;
  user-select: none;
  transition: all 0.15s;
}
.obs-tool-chip:hover { border-color: #3b82f6; }
.obs-tool-chip.active {
  background: #dbeafe; border-color: #3b82f6; color: #1e40af; font-weight: 500;
}
.obs-tool-chip input[type=checkbox] { width: 12px; height: 12px; margin: 0; cursor: pointer; }

/* Step 7 grafana/loki 块下的 per-env 标签映射子区(浅黄,跟全局 loki-mapping 区分但配色协调) */
.loki-env-mapping {
  margin-top: 8px; padding: 8px 10px;
  background: #fef9c3; border: 1px dashed #fde68a; border-radius: 4px;
}
.loki-env-mapping-head {
  font-size: 11px; font-weight: 600; color: #78350f;
  margin-bottom: 6px;
}
.cc-delete {
  padding: 4px 6px; font-size: 13px; color: #b91c1c;
  background: transparent; border: none; cursor: pointer;
}
.cc-delete:hover { color: #7f1d1d; }

/* 👁/🙈 明文切换按钮,secret 输入右侧 */
.cc-reveal {
  padding: 4px 6px; font-size: 14px; color: #475569;
  background: transparent; border: none; cursor: pointer;
  user-select: none; line-height: 1;
}
.cc-reveal:hover { color: #1e293b; }
.cc-env-hint {
  font-size: 10px; color: #94a3b8; font-family: monospace;
}

/* Step 5 真实预加载按钮 + 结果展示(去掉 border-top 虚线,跟 loading 态按钮叠加容易被当成"残余") */
.cc-preload-row {
  display: flex; align-items: center; gap: 10px;
  margin-top: 12px;
}

/* 预加载按钮:明确所有状态不漏 UA 默认样式,avoid 文字跑位 / 双重边框显影 */
.cc-preload-btn {
  appearance: none;
  -webkit-appearance: none;
  box-shadow: none;       /* 有些浏览器 inline button 会自带内阴影 */
  white-space: nowrap;    /* 不让 loading 文字换行 */
  line-height: 1.2;       /* 跟全局 .btn 对齐;防 loading 态 span 包裹后行高异常 */
}
.cc-preload-btn:disabled {
  opacity: 0.65;
  cursor: not-allowed;
}
.cc-preload-btn .cc-preload-spinner {
  display: inline-block;
  width: 12px; height: 12px;
  border: 2px solid #cbd5e1;
  border-top-color: #3b82f6;
  border-radius: 50%;
  margin: 0;              /* 间距完全交给 .btn 的 flex gap,不额外 margin-right */
  animation: scan-spin 0.7s linear infinite;
  flex-shrink: 0;         /* 防 flex 压扁变椭圆 */
}
.cc-preload-summary { font-size: 12px; color: #047857; font-weight: 500; }
.cc-preload-error {
  font-size: 12px; color: #b91c1c;
  display: inline-flex; align-items: center; gap: 6px;
}
.cc-preload-log-link {
  font-size: 11px; color: #2563eb; text-decoration: underline;
  text-underline-offset: 2px; cursor: pointer;
}
.cc-preload-log-link:hover { color: #1d4ed8; }
.cc-preload-note { color: #0369a1; background: #e0f2fe; padding: 1px 6px; border-radius: 3px; font-size: 10px; }
/* 空 entries 但有 notes(eg. "namespace 下无配置"):只展 notes,不用 entries 容器的灰底框 */
.cc-preload-notes-only {
  margin-top: 8px;
  display: flex; flex-wrap: wrap; gap: 6px;
}

/* Step 5 映射块:namespace 下拉 + 每个服务的 dataId 下拉 */
.cc-map-block {
  margin-top: 10px; padding: 10px 12px;
  background: #f8fafc; border: 1px solid #e2e8f0; border-radius: 6px;
}
.cc-map-head {
  display: flex; flex-wrap: wrap; gap: 8px; align-items: baseline;
  font-size: 12px; color: #475569; margin-bottom: 8px;
}
.cc-map-title { font-weight: 600; color: #1e293b; }
/* namespace 下拉的布局完全跟 Step 4 "环境 → 分支" 保持一致:
   env.id 放左,箭头,右边 select,尾部一个条数提示 */
.cc-map-ns-grid {
  display: flex; flex-direction: column; gap: 8px;
  margin-bottom: 8px;
}
.cc-map-ns-item {
  display: flex; align-items: center; gap: 8px;
}
.cc-map-ns-env {
  font-family: monospace; font-size: 12px; color: #3b82f6;
  min-width: 48px;
}
.cc-map-ns-arrow { color: #94a3b8; font-size: 12px; }
.cc-map-ns-count {
  font-size: 11px; color: #64748b;
  padding: 2px 6px; background: #f1f5f9; border-radius: 3px;
  white-space: nowrap;
}
.cc-map-select {
  flex: 1; padding: 6px 8px;
  border: 1px solid #cbd5e1; border-radius: 6px;
  font-size: 13px; font-family: monospace;
  background: #fff; color: #1e293b;
}
.cc-map-select:focus { outline: none; border-color: #3b82f6; }
.cc-map-select.error { border-color: #dc2626; background: #fef2f2; }
.cc-map-select.cc-map-select-none {
  background: #f8fafc; color: #64748b; font-style: italic; border-color: #cbd5e1;
}
.cc-map-select-svc { flex: 1; }

/* Step 7 数据层:自动导入按钮行 */
.ds-autoimport-row {
  display: flex; align-items: center; gap: 12px; flex-wrap: wrap;
  margin-bottom: 12px; padding: 10px 14px;
  background: #eff6ff; border: 1px solid #bfdbfe; border-left: 3px solid #3b82f6;
  border-radius: 6px;
}
.ds-autoimport-row .btn { display: inline-flex; align-items: center; gap: 6px; }
.ds-autoimport-hint { font-size: 11px; color: #64748b; }

/* Step 7 env → service → ds 层级展示 */
.ds-hierarchy { display: flex; flex-direction: column; gap: 18px; margin-top: 14px; }
.ds-env-section {
  padding: 12px 16px;
  background: #f8fafc;
  border-left: 3px solid #3b82f6; border-radius: 0 6px 6px 0;
}
.ds-env-title {
  display: flex; align-items: center; gap: 8px;
  font-size: 13px; font-weight: 600; color: #1e293b;
  margin-bottom: 10px;
}
.ds-empty {
  padding: 10px 12px; color: #94a3b8;
  background: #fff; border: 1px dashed #cbd5e1; border-radius: 4px;
  font-size: 12px;
}
.ds-svc-container { display: flex; flex-direction: column; gap: 10px; }
.ds-svc-block {
  padding: 10px 12px;
  background: #fff; border: 1px solid #e2e8f0; border-radius: 6px;
}
.ds-svc-head {
  display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap;
  margin-bottom: 6px;
}
.ds-svc-name {
  font-family: monospace; font-weight: 600; font-size: 13px; color: #1e293b;
}
.ds-svc-dataid {
  font-size: 11px; color: #64748b; font-family: monospace;
  background: #f1f5f9; padding: 1px 6px; border-radius: 3px;
}
.ds-empty-inner { font-size: 11px; color: #94a3b8; padding: 4px 0; }
.ds-item-list {
  display: flex; flex-direction: column; gap: 10px;
  padding-left: 10px;
  border-left: 2px solid #e2e8f0;
}
.ds-item { padding: 6px 8px; background: #f8fafc; border-radius: 4px; }
.ds-item-head {
  display: flex; align-items: center; gap: 6px;
  margin-bottom: 6px;
}
.ds-item-badge {
  display: inline-block; padding: 2px 8px;
  background: #dbeafe; color: #1e40af;
  border-radius: 10px; font-size: 11px; font-weight: 600;
}
.ds-item-delete {
  padding: 2px 8px; font-size: 12px; line-height: 1;
  background: transparent; border: 1px solid transparent; border-radius: 4px;
  color: #94a3b8; cursor: pointer;
  transition: all 0.15s;
}
.ds-item-delete:hover {
  background: #fee2e2; border-color: #fca5a5; color: #b91c1c;
}

/* 连通性测试按钮 + 状态徽章 */
.ds-item-probe {
  margin-left: auto;
  padding: 2px 10px; font-size: 11px; line-height: 1.6;
  border: 1px solid #cbd5e1; border-radius: 10px;
  background: #fff; color: #475569; cursor: pointer;
  white-space: nowrap;
  transition: all 0.15s;
}
.ds-item-probe.idle:hover { background: #eff6ff; border-color: #3b82f6; color: #1e40af; }
.ds-item-probe.loading { background: #f1f5f9; color: #64748b; cursor: wait; }
.ds-item-probe.ok { background: #d1fae5; border-color: #10b981; color: #047857; cursor: pointer; }
.ds-item-probe.ok:hover { background: #a7f3d0; }
.ds-item-probe.fail { background: #fee2e2; border-color: #fca5a5; color: #991b1b; cursor: pointer; }
.ds-item-probe.fail:hover { background: #fecaca; }

.ds-probe-error {
  margin-top: 4px; padding: 4px 8px;
  background: #fef2f2; border-left: 2px solid #dc2626; border-radius: 0 3px 3px 0;
  font-size: 11px; font-family: monospace; color: #991b1b;
  word-break: break-all;
}

.ds-env-probe-all {
  margin-left: 8px; padding: 2px 10px; font-size: 11px; line-height: 1.6;
  border: 1px solid #bfdbfe; border-radius: 10px;
  background: #eff6ff; color: #1e40af; cursor: pointer;
}
.ds-env-probe-all:hover { background: #dbeafe; }
.ds-env-probe-all.loading {
  display: inline-flex; align-items: center; gap: 4px;
  background: #f1f5f9; color: #64748b; cursor: wait;
  border-color: #cbd5e1;
}
.ds-item-fields {
  display: grid; grid-template-columns: 1fr 1fr; gap: 8px;
  padding-top: 4px;
}
@media (max-width: 900px) { .ds-item-fields { grid-template-columns: 1fr; } }

/* 环境总览 "5 个服务 · 已识别 3" */
.ds-env-count {
  margin-left: auto; font-size: 11px; color: #64748b;
  font-weight: normal; font-family: monospace;
}

/* 每个 svc-block 根据状态加边框 —— 一眼看出缺失项 */
.ds-svc-block.status-ok      { border-left: 3px solid #10b981; }
.ds-svc-block.status-empty   { border-left: 3px solid #60a5fa; } /* 合法通过,蓝色不刺眼 */
.ds-svc-block.status-skipped { border-left: 3px solid #94a3b8; background: #f8fafc; }
.ds-svc-block.status-error   { border-left: 3px solid #dc2626; background: #fef2f2; }
.ds-svc-block.status-pending { border-left: 3px solid #cbd5e1; }

.ds-svc-status {
  font-size: 10px; font-weight: 600;
  padding: 1px 6px; border-radius: 8px; white-space: nowrap;
}
.ds-svc-status.status-ok      { background: #d1fae5; color: #047857; }
.ds-svc-status.status-empty   { background: #dbeafe; color: #1e40af; }
.ds-svc-status.status-skipped { background: #e2e8f0; color: #475569; }
.ds-svc-status.status-error   { background: #fee2e2; color: #991b1b; }

.ds-status-reason {
  font-size: 11px; color: #64748b; padding: 4px 0;
  font-family: monospace;
}
.ds-svc-block.status-error .ds-status-reason { color: #991b1b; }
.cc-map-svc-list {
  display: flex; flex-direction: column; gap: 6px;
}
.cc-map-svc-row {
  display: flex; align-items: center; gap: 10px;
  padding: 4px 6px;
  background: #fff; border: 1px solid #e2e8f0; border-radius: 4px;
}
.cc-map-svc-name {
  font-family: monospace; font-size: 12px; font-weight: 500;
  color: #1e293b; min-width: 120px;
}
/* kuboard 行:3 个下拉(cluster / namespace / configmap),用 grid 等宽对齐 */
.cc-map-svc-row-kuboard {
  display: grid;
  grid-template-columns: minmax(120px, 1fr) repeat(3, minmax(140px, 2fr));
  gap: 8px;
}
.cc-map-select-kuboard {
  width: 100%;
  padding: 4px 8px;
  font-size: 12px;
  border: 1px solid #cbd5e1;
  border-radius: 4px;
  background: #fff;
}
.cc-map-select-kuboard:disabled { background: #f1f5f9; color: #94a3b8; cursor: not-allowed; }
.cc-map-group-tag {
  font-size: 10px; font-family: monospace;
  color: #0369a1; background: #e0f2fe;
  padding: 1px 6px; border-radius: 3px;
  cursor: default;
}
.cc-map-hint {
  font-size: 11px; color: #94a3b8; padding: 6px 8px;
}
.cc-map-hint code {
  background: #e2e8f0; padding: 1px 4px; border-radius: 3px;
}

/* Step 6 工具卡网格:复用 target-card 样式,额外补展开态的 body 布局 */
.tool-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}
.tool-card .tool-body {
  max-height: 600px; overflow-y: auto;
}
.tool-card .tool-env-row {
  padding: 8px 0; border-top: 1px dashed #e2e8f0;
}
.tool-card .tool-env-row:first-child { border-top: none; padding-top: 0; }
.tool-card .tool-env-head {
  display: flex; align-items: center; gap: 6px;
  margin-bottom: 6px;
  font-size: 12px; font-weight: 600;
}
.tool-card .tool-env-fields {
  display: grid; grid-template-columns: 1fr; gap: 8px;
}

/* OpenClaw 探测失败/警告块 */
.openclaw-warn {
  padding: 10px 12px;
  background: #fffbeb; border: 1px solid #fde68a; border-left: 3px solid #f59e0b;
  border-radius: 6px; font-size: 12px; color: #78350f; line-height: 1.5;
}
.openclaw-warn code { background: #fef3c7; padding: 1px 4px; border-radius: 3px; font-size: 11px; }
.openclaw-warn-actions {
  display: flex; gap: 8px; margin-top: 8px; flex-wrap: wrap;
}

/* Step 7 顶部只读展示本次部署目标(不给改,改回 Step 1) */
.target-readonly-row {
  display: flex; flex-wrap: wrap; align-items: center; gap: 6px;
  margin-bottom: 12px; padding: 8px 12px;
  background: #f8fafc; border: 1px solid #e2e8f0; border-radius: 6px;
  font-size: 12px;
}
.target-readonly-label { color: #64748b; font-weight: 500; }
.target-readonly-chip {
  display: inline-block; padding: 2px 8px;
  background: #eff6ff; border: 1px solid #bfdbfe;
  border-radius: 10px; color: #1e40af; font-weight: 500;
}

/* Step 1 系统 ID 自动派生展示:默认是 readonly badge + "自定义" 链接 */
.id-autoderive {
  display: flex; align-items: center; gap: 10px;
  padding: 6px 0;
}
.id-badge {
  padding: 4px 10px; background: #f1f5f9;
  border: 1px solid #cbd5e1; border-radius: 6px;
  font-size: 13px; font-family: monospace; color: #1e293b;
  min-width: 100px;
}
.id-input-row {
  display: flex; gap: 8px; align-items: center;
}
.id-input-row input { flex: 1; }

/* 技术栈单值展示(唯一的 auto 字段,简单些) */
.stack-display {
  display: inline-block;
  padding: 4px 12px;
  background: #f1f5f9; border: 1px solid #cbd5e1;
  border-radius: 6px; font-size: 13px;
  font-family: monospace; color: #1e293b;
  min-width: 80px;
}
.stack-display.empty { color: #94a3b8; }

/* 角色下拉:跟普通 input 同尺寸,但加上下拉箭头 */
.role-select {
  width: 100%;
  padding: 8px 12px; padding-right: 32px;
  background: #fff;
  border: 1px solid #cbd5e1;
  border-radius: 6px;
  font-size: 13px;
  appearance: none;
  background-image: url('data:image/svg+xml;utf8,<svg xmlns="http://www.w3.org/2000/svg" width="12" height="12" viewBox="0 0 12 12"><path fill="%2364748b" d="M2 4l4 4 4-4z"/></svg>');
  background-repeat: no-repeat;
  background-position: right 10px center;
}
.role-select:focus { outline: none; border-color: #2563eb; }

/* role 推荐 chip:跟下拉同行下方,绿色=匹配,橙色=有更优推荐 */
.role-hint-loading {
  margin-top: 6px;
  font-size: 12px;
  color: #94a3b8;
}
.role-hint {
  margin-top: 6px;
  display: flex; align-items: center; gap: 6px;
  padding: 6px 10px;
  border-radius: 6px;
  background: #fff7ed; /* 默认橙色:有更优推荐 */
  border: 1px solid #fdba74;
  font-size: 12px;
}
.role-hint.matched {
  background: #f0fdf4; /* 绿色:推荐 = 当前选的 */
  border-color: #86efac;
}
.role-hint-icon { flex-shrink: 0; font-size: 13px; }
.role-hint-text { flex: 1 1 auto; color: #1e293b; line-height: 1.4; }
.role-hint-text strong { color: #d97706; font-weight: 600; }
.role-hint.matched .role-hint-icon { color: #16a34a; }
.role-hint-reason { color: #64748b; margin-left: 4px; }
.role-hint-apply {
  flex-shrink: 0;
  padding: 3px 10px; border-radius: 4px;
  border: 1px solid #d97706; background: #fff;
  color: #d97706; font-size: 11px; font-weight: 600;
  cursor: pointer;
}
.role-hint-apply:hover { background: #d97706; color: #fff; }

/* sub_path 紫色 chip:已设 sub_path 的 repo header 显示,让用户一眼看出"这是 monorepo 拆出来的子条目" */
.submodule-tag {
  display: inline-block;
  margin-left: 8px;
  padding: 2px 8px;
  border-radius: 4px;
  background: #ede9fe;
  color: #6d28d9;
  font-size: 11px;
  font-family: monospace;
}

/* monorepo 自动检测 banner */
.monorepo-banner {
  margin: 12px 0;
  padding: 12px 14px;
  border-radius: 8px;
  border: 1px solid;
}
.monorepo-banner-mono { background: #f5f3ff; border-color: #c4b5fd; }
.monorepo-banner-single { background: #f8fafc; border-color: #e2e8f0; padding: 8px 12px; }
.service-entries-display {
  margin: 8px 0 12px; padding: 10px 14px;
  border: 1px dashed #86efac; border-radius: 6px; background: #f0fdf4;
}
.service-entries-head {
  font-size: 12px; color: #166534; font-weight: 600; margin-bottom: 6px;
  display: flex; align-items: center; gap: 8px;
}
.service-entries-list {
  list-style: none; padding-left: 0; margin: 0;
  font-size: 12px; color: #1e293b; display: flex; flex-direction: column; gap: 4px;
}
.service-entries-list code {
  background: #fff; padding: 1px 6px; border-radius: 3px; font-size: 11px;
}
.monorepo-banner-head {
  font-size: 13px; font-weight: 600; color: #5b21b6;
  margin-bottom: 8px;
}
.monorepo-banner-head.ok { color: #166534; font-weight: 500; margin-bottom: 0; }
.monorepo-banner-head.warn { color: #92400e; font-weight: 500; margin-bottom: 0; }
.monorepo-banner-hint {
  font-size: 11.5px; color: #475569;
  margin-bottom: 8px;
  padding: 5px 8px;
  background: #fff;
  border-radius: 4px;
  line-height: 1.5;
}
.monorepo-banner-list {
  list-style: none; padding: 0; margin: 0 0 10px 0;
  font-size: 12px; color: #475569;
  display: flex; flex-direction: column; gap: 8px;
}
.monorepo-banner-list > li {
  padding: 0;
  background: #fff;
  border: 1px solid #ddd6fe;
  border-radius: 6px;
}
.monorepo-row-check {
  display: flex; align-items: flex-start; gap: 10px;
  padding: 8px 10px;
  cursor: pointer;
}
.monorepo-row-check:hover { background: #f5f3ff; }
.monorepo-row-check > input[type=checkbox] {
  flex-shrink: 0;
  margin-top: 2px;
  cursor: pointer;
}
.monorepo-row-content { flex: 1 1 auto; min-width: 0; }
.monorepo-split-btn:disabled {
  background: #cbd5e1; cursor: not-allowed;
}
.monorepo-row-top {
  display: flex; align-items: center; gap: 6px;
  margin-bottom: 3px;
}
.monorepo-row-top strong { color: #1e293b; font-size: 13px; }
.monorepo-row-path {
  font-size: 11.5px; color: #475569;
  margin-bottom: 3px;
}
.monorepo-row-path code {
  background: #ede9fe; color: #6d28d9;
  padding: 1px 6px; border-radius: 3px;
  font-family: monospace; font-size: 11px;
}
.monorepo-row-url {
  font-size: 11.5px; color: #475569;
  margin-bottom: 3px;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.monorepo-row-url code {
  background: #dbeafe; color: #1e40af;
  padding: 1px 6px; border-radius: 3px;
  font-family: monospace; font-size: 11px;
  margin-right: 4px;
}
.monorepo-row-reason { font-size: 11px; }
.monorepo-stack, .monorepo-role {
  display: inline-block;
  padding: 0 6px; border-radius: 3px;
  background: #f8fafc; border: 1px solid #cbd5e1;
  font-size: 10.5px; color: #475569;
}
.monorepo-split-btn {
  font-size: 12px; padding: 7px 16px;
  background: #6d28d9; color: #fff; border: none; border-radius: 5px;
  cursor: pointer; font-weight: 600;
}
.monorepo-split-btn:hover { background: #5b21b6; }

/* 已拆分的子模块行下方,展示"实际代码位置 = 父路径 + sub_path" */
.submodule-path-display {
  margin: 6px 0 12px;
  padding: 6px 12px;
  background: #ede9fe;
  border-left: 3px solid #6d28d9;
  border-radius: 4px;
  font-size: 12px;
}
.submodule-path-display code {
  margin-left: 4px;
  background: transparent;
  color: #5b21b6;
  font-family: monospace; font-size: 11.5px;
}

/* 服务名 chip 列表:右上角 ✕ 可删单个 */
.service-chips-row {
  display: flex; flex-wrap: wrap; gap: 6px;
  padding: 6px 0;
}
.service-chip {
  display: inline-flex; align-items: center; gap: 4px;
  padding: 3px 6px 3px 10px;
  background: #eff6ff; border: 1px solid #bfdbfe;
  border-radius: 14px; font-size: 12px; color: #1e40af;
  line-height: 1; font-family: monospace;
}
.service-chip-name { font-weight: 500; }
.service-chip-x {
  width: 18px; height: 18px; padding: 0;
  display: inline-flex; align-items: center; justify-content: center;
  background: transparent; border: none; cursor: pointer;
  color: #64748b; font-size: 10px; border-radius: 50%;
  transition: all 0.12s;
}
.service-chip-x:hover {
  background: #ef4444; color: #fff;
}
.service-chips-empty { color: #94a3b8; font-size: 13px; padding: 4px 0; }

/* 服务名"+ 补一个"inline 输入 chip */
.service-chip-add {
  display: inline-flex; align-items: center; gap: 0;
  padding: 0; border-radius: 14px;
  background: #fff; border: 1px dashed #cbd5e1;
  overflow: hidden;
}
.service-chip-add input {
  border: none; outline: none;
  padding: 3px 10px; font-size: 12px;
  font-family: monospace; min-width: 140px;
  background: transparent;
}
.service-chip-add:focus-within { border-color: #3b82f6; border-style: solid; }
.service-chip-add-btn {
  width: 20px; height: 20px; margin: 0 3px;
  padding: 0; border: none; background: #eff6ff;
  color: #1e40af; font-size: 14px; font-weight: 600;
  border-radius: 50%; cursor: pointer; line-height: 1;
  transition: all 0.12s;
}
.service-chip-add-btn:hover:not(:disabled) {
  background: #3b82f6; color: #fff;
}
.service-chip-add-btn:disabled {
  opacity: 0.35; cursor: not-allowed;
}

/* 分支映射 <select>:点开即下拉,保留 env → branch 左右布局 */
.branch-select-grid {
  display: flex; flex-direction: column; gap: 8px;
}
.branch-select-item {
  display: flex; align-items: center; gap: 8px;
}
.branch-select, .branch-input {
  flex: 1; padding: 6px 8px; border: 1px solid #cbd5e1;
  border-radius: 6px; font-size: 13px; font-family: monospace;
  background: #fff;
}
.branch-select:focus, .branch-input:focus {
  outline: none; border-color: #3b82f6;
}

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

/* ── Step 4 自动识别 readonly 展示 ── */
/* "扫一下才能识别"引导 banner,URL 填了但还没扫时显示 */
.auto-scan-hint {
  padding: 8px 12px; margin-bottom: 8px;
  background: #fffbeb; border: 1px solid #fde68a; border-left: 3px solid #f59e0b;
  border-radius: 6px; font-size: 12px; color: #78350f; line-height: 1.5;
}
.auto-scan-hint strong { color: #92400e; }

/* 扫描按钮里的 spinner + pill 内的迷你 spinner */
@keyframes scan-spin { to { transform: rotate(360deg); } }
.scan-spinner {
  display: inline-block; width: 12px; height: 12px;
  border: 2px solid rgba(255,255,255,0.35);
  border-top-color: #fff;
  border-radius: 50%; margin-right: 4px;
  animation: scan-spin 0.7s linear infinite;
  vertical-align: -2px;
}
.scan-spinner-mini {
  display: inline-block; width: 10px; height: 10px;
  border: 2px solid #dbeafe; border-top-color: #3b82f6;
  border-radius: 50%; margin-right: 4px;
  animation: scan-spin 0.7s linear infinite;
  vertical-align: -1px;
}

.auto-summary {
  display: flex; flex-wrap: wrap; gap: 8px;
  padding: 10px 12px; background: #f8fafc;
  border: 1px dashed #cbd5e1; border-radius: 6px;
}
.auto-pill.scanning {
  background: #eff6ff !important; border-color: #bfdbfe !important;
  color: #1e40af !important;
}
.auto-pill.scanning .auto-label { opacity: 0.6; }
.auto-scanning {
  display: inline-flex; align-items: center;
  font-weight: 500; font-size: 11px; color: #1e40af;
}

/* analyze-block 内的"进行中"提示,比 alert 样式更轻,跟 scan-spinner 搭配 */
.analyze-progress-row {
  display: flex; align-items: center; gap: 8px;
  margin-top: 8px; padding: 8px 12px;
  background: #eff6ff; border: 1px solid #bfdbfe; border-radius: 6px;
  font-size: 12px; color: #1e40af; line-height: 1.5;
}
.auto-pill {
  display: flex; align-items: baseline; gap: 6px;
  padding: 4px 10px; background: #eff6ff;
  border: 1px solid #bfdbfe; border-radius: 6px;
  font-size: 12px; color: #1e40af;
}
.auto-pill.empty { background: #f1f5f9; border-color: #e2e8f0; color: #94a3b8; }
.auto-pill.wide { min-width: 200px; flex: 1; }
.auto-pill .auto-label {
  font-size: 10px; text-transform: uppercase; letter-spacing: 0.3px;
  color: inherit; opacity: 0.7; font-weight: 500;
}
.auto-pill .auto-val { font-weight: 600; font-family: monospace; }
.auto-pill:not(.empty) .auto-val { color: #0f172a; }
.auto-pill.empty .auto-val { color: #94a3b8; font-family: inherit; }

.branch-readonly-grid {
  display: flex; flex-wrap: wrap; gap: 6px;
  padding: 10px 12px; background: #f8fafc;
  border: 1px dashed #cbd5e1; border-radius: 6px;
}
.branch-readonly-item {
  display: flex; align-items: center; gap: 6px;
  padding: 4px 10px; background: #fff;
  border: 1px solid #e2e8f0; border-radius: 6px;
  font-size: 12px;
}
.branch-readonly-item .branch-env {
  min-width: auto; font-weight: 600; color: #334155;
}
.branch-readonly-item .branch-arrow { color: #94a3b8; }
.branch-readonly-item .branch-value {
  font-family: monospace; color: #1e40af; font-weight: 500;
}
.branch-readonly-item .branch-value.empty { color: #94a3b8; font-family: inherit; }

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
  background: transparent !important;
}

.yaml-preview code {
  /* 显式禁掉全局 code 的浅色 user-agent / 全局背景(否则深色面板上每行都浮一片白条) */
  background: transparent !important;
  padding: 0 !important;
  border: none !important;
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
.deploy-inline-actions { display: flex; justify-content: flex-end; }

/* Step 9 一键部署主要 CTA:比内联版更醒目(独立 step,这是流程最终动作) */
.deploy-final-block { padding: 6px 0; }
.deploy-final-btn {
  font-size: 15px; padding: 12px 28px;
  font-weight: 600;
}

/* Step 1 欢迎页:两个大按钮选择"从零开始"或"导入 yaml" */
.welcome-card { padding: 28px 32px; }
.welcome-choices {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 16px;
}
.welcome-choice {
  display: flex; align-items: center; gap: 14px;
  padding: 20px 18px;
  background: #fff;
  border: 1px solid #e2e8f0;
  border-radius: 10px;
  text-align: left;
  cursor: pointer;
  font-family: inherit;
  transition: border-color .15s, background .15s, transform .1s;
}
.welcome-choice:hover {
  border-color: #2563eb;
  background: #eff6ff;
}
.welcome-choice:active { transform: translateY(1px); }
.welcome-choice-icon {
  flex-shrink: 0;
  font-size: 32px;
  width: 48px; height: 48px;
  display: flex; align-items: center; justify-content: center;
  background: #f1f5f9;
  border-radius: 10px;
}
.welcome-choice-text {
  display: flex; flex-direction: column; gap: 4px;
}
.welcome-choice-text strong {
  font-size: 15px; color: #1e293b; font-weight: 600;
}
.welcome-choice-text span {
  font-size: 12.5px; color: #64748b; line-height: 1.4;
}
@media (max-width: 700px) {
  .welcome-choices { grid-template-columns: 1fr; }
}

/* 部署摘要一行:Step 8 简短列出"将部署到 X、Y、Z",路径在 Step 2 卡上看 */
.deploy-targets-line {
  font-size: 12px; color: #334155; margin-bottom: 12px; line-height: 1.7;
}
.deploy-target-chip {
  display: inline-block; padding: 2px 8px; margin: 0 2px;
  background: #e0e7ff; color: #1e40af; border-radius: 10px; font-weight: 600;
}

.auto-tag {
  font-size: 10px; font-weight: 500; color: #065f46;
  background: #d1fae5; padding: 1px 6px; border-radius: 8px; letter-spacing: 0.2px;
  margin-left: 4px;
}
/* "(扫一下自动填)" 这种轻提示,跟 label 同行不抢视觉 */
.field-hint {
  font-size: 11px; font-weight: 400; color: var(--c-muted);
  margin-left: 6px;
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
  align-items: flex-end;
  margin-top: 8px;
}
.nav-buttons .next-wrap {
  display: flex; flex-direction: column; align-items: flex-end; gap: 6px;
}
.nav-buttons .next-block-hint {
  font-size: 11px; color: #b45309;
  padding: 4px 10px;
  background: #fef3c7; border: 1px solid #fde68a; border-radius: 6px;
  max-width: 520px; text-align: right; line-height: 1.4;
}
.nav-buttons .btn.primary:disabled {
  opacity: 0.5; cursor: not-allowed;
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
