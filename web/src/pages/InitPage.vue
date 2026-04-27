<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted } from 'vue'
import yaml from 'js-yaml'
import { useRouter } from 'vue-router'
import {
  analyzeV2 as bridgeAnalyzeV2,
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
  importAndDeploy,
  isDesktop,
  openDir,
  preloadConfigCenter,
  setDefaultReposRoot,
  validate as bridgeValidate,
} from '../lib/bridge'
import type { AIToolResult, CCHubEntry, CCHubNamespace, GrafanaDatasource, OpenClawModelEntry } from '../lib/bridge'
import { confirmDialog } from '../lib/confirm'
import { pushLog } from '../lib/logStore'
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
const totalSteps = 8
const stepTitles = [
  '系统基本信息',
  '机器人身份',
  '环境列表',
  '代码仓库',
  '配置源',
  '数据层',
  '可观测性',
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
// agent.model 是"默认模型"(兜底值,schema 要求非空)。
// agent.target_models 是 per-target 的覆盖 —— 仅 openclaw 这一个 target 消费模型
// (claude-code / cursor 由用户在各自客户端里挑,填这儿没意义)。
const agent = reactive({
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
// 不暴露到模板,用 void 抑制 unused 警告(跟 _roleOptions 等同套路)。
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

// Claude Code / Cursor 安装状态 —— 跟 openclaw 不同,这俩只做"装了没"信息展示,
// 不影响卡片能不能被勾选。用户勾了但没装的话,部署后 claude/cursor 客户端需要自己装,
// 产物只是 CLAUDE.md / .cursorrules 文件,不依赖桌面 app 能跑就行。
const aitoolsResult = ref<{ claude_code: AIToolResult; cursor: AIToolResult } | null>(null)
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
// ${id}-bot 做目录默认;id 空时 placeholder 用占位字符,避免只显示 "-bot"
const workspaceNameDefault = computed(() => (system.id ? `${system.id}-bot` : 'my-system-bot'))

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
interface RepoItem {
  name: string
  url: string
  role: string
  stack: string
  framework: string
  service_names: string
  env_branches: Record<string, string>
  // _nameManual: 用户手动编辑过 name,URL 变化不会再覆盖 name。
  _nameManual?: boolean
  // _source: 仓库来源 "local"(本地已 clone,直接选目录) / "remote"(填 URL,扫描时 clone)
  // 默认 remote(新建仓库填 URL 是更常见的场景)
  _source?: 'local' | 'remote'
  // _localPath: _source=local 时的本地绝对路径;扫描时直接读,不走 ReposRoot/Name
  _localPath?: string
  // _cloneTarget: _source=remote 时的自定义 clone 目标目录(可选);
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
  // 下划线前缀字段都是 UI 态;不参与 yaml 序列化(generateYAML 不读),但跟
  // localStorage auto-save 持久化,跨次刷新不丢。
}

function makeEmptyRepo(): RepoItem {
  const branches: Record<string, string> = {}
  for (const e of environments) {
    if (e.id) branches[e.id] = ''
  }
  return {
    name: '', url: '', role: '', stack: '', framework: '', service_names: '', env_branches: branches,
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
}

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
  await scanSingleRepo(r)
}

// 远程模式:可选地给该仓库自定义 clone 目标(覆盖全局默认 + repo.name 的默认拼法)
async function pickCloneTarget(r: RepoItem) {
  if (!isDesktop()) {
    toast.error('选目录需要桌面 app 环境')
    return
  }
  try {
    const p = await openDir(`选择 ${r.name || '该仓库'} 的 clone 目标目录`)
    if (p) r._cloneTarget = p
  } catch (e: any) {
    toast.error(String(e?.message || e))
  }
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
  // 切源 = 换了一个仓库,之前扫出来的元信息全作废:URL / 仓库名 / stack / role /
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
  r.role = ''
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
  } else if (r._source === 'remote' && r._cloneTarget?.trim()) {
    repoPaths[r.name] = r._cloneTarget.trim()
  }
  const autoClone = r._source === 'remote'
  // 远程模式没填 _cloneTarget 时需要 effectiveRoot 来拼 ReposRoot/Name
  const effectiveRoot = reposRootInput.value.trim() || resolvedReposRoot.value
  if (autoClone && !repoPaths[r.name] && !effectiveRoot) {
    r._scanError = '远程仓库需要 clone 目标 —— 填本仓库的 clone 目录或设全局默认'
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
        detected_role?: string
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

    // service_names 在 report.repos[i] 里,per_repo 只有 count。
    // 只接 stack —— role / framework 启发式误报率太高,不自动填(用户想改回 yaml 手改)。
    const rpt = (res.report?.repos || []).find(rr => rr.name === r.name)
    if (rpt?.service_names?.length) {
      r.service_names = rpt.service_names.join(', ')
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
  } catch (e: any) {
    r._scanError = String(e?.message || e)
  } finally {
    r._scanning = false
  }
}

// ── Step 5: 配置源 ──
const configCenterType = ref<string>(saved?.configCenterType ?? 'nacos')

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
}
const CC_FIELDS_BY_TYPE: Record<string, CredField[]> = {
  nacos: [
    { key: 'addr', label: 'Nacos 地址 (host:port)', secret: false, envVar: (e) => `CC_ADDR_${e.toUpperCase()}`, placeholder: 'nacos.example.com:8848' },
    { key: 'user', label: '用户名', secret: false, envVar: (e) => `CC_USER_${e.toUpperCase()}`, placeholder: 'nacos', optional: true },
    { key: 'pass', label: '密码', secret: true, envVar: (e) => `CC_PASS_${e.toUpperCase()}`, optional: true },
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
}
// keychain 里用 "cc:<type>:<env>:<field>" 命名;UI 读写走这个 key
function ccKeyFor(type: string, envID: string, field: string): string {
  return `cc:${type}:${envID}:${field}`
}
// ccCredInputs:所有配置中心字段的当前输入值(key = ccKeyFor)。
// 流向:输入 → localStorage draft(持久) → system.yaml → 部署时注入各 AI 平台的 MCP
// server config(openclaw.json / ~/.claude/config.json / .cursor/mcp.json / embedded)。
// **不再走 Studio 自己的钥匙串** —— 对 MCP 用途来说钥匙串是多余中间层,
// 凭证最终要成为 AI 平台 MCP server 的 env 字段,yaml 是直接源。
// UI 上 secret 字段仍用 type=password 做显示遮码(纯视觉)。
// ⚠ yaml 带明文,分享时必须控制范围(团队私密频道 / 内网 git),不要 push 公网。
const ccCredInputs = reactive<Record<string, string>>(saved?.ccCredInputs ?? {})

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
function autoMatchDataID(envID: string, svc: string, nsID: string): { dataId: string, group: string } {
  const entries = entriesForNamespace(envID, nsID)
  const svcLower = svc.toLowerCase()
  const envLower = envID.toLowerCase()
  // 优先同时含 service + env
  let hit = entries.find(e => {
    const loc = e.locator.toLowerCase()
    return loc.includes(svcLower) && loc.includes(envLower)
  })
  // 退化:仅含 service
  if (!hit) hit = entries.find(e => e.locator.toLowerCase().includes(svcLower))
  if (hit) return { dataId: hit.locator, group: hit.group || '' }
  return { dataId: '', group: '' }
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
  // 2) 每个服务
  const nsID = envNamespaces[envID] || ''
  for (const svc of allServiceNames.value) {
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
      { key: 'user', label: '用户名', secret: false, envVar: (e) => `GRAFANA_USER_${e.toUpperCase()}`, optional: true },
      { key: 'pass', label: '密码', secret: true, envVar: (e) => `GRAFANA_PASS_${e.toUpperCase()}`, optional: true },
      { key: 'api_key', label: 'API Key', secret: true, envVar: (e) => `GRAFANA_API_KEY_${e.toUpperCase()}`, optional: true, placeholder: 'glsa_xxx(用 Key 替代用户名密码)' },
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
// 切到 Step 7 时主动跑一次(草稿恢复后立刻看状态,不等用户重新输入)
watch(() => currentStep.value, (s) => {
  if (s !== 7) return
  for (const spec of OBS_TOOL_SPECS) {
    if (!enabledObservability[spec.key]) continue
    for (const env of environments) {
      if (!env.id) continue
      scheduleObsProbe(spec.key, env.id)
    }
  }
})

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
}
function makeEmptyLokiMappingPerEnv(): LokiMappingPerEnv {
  return {
    dsList: [], dsUID: '', dsListStatus: 'idle',
    labels: [], labelStatus: 'idle',
    envLabelKey: '', serviceLabelKey: '',
    envLabelValues: [], serviceLabelValues: [],
    envValue: '', serviceValues: {},
  }
}
const lokiMappingByEnv = reactive<Record<string, LokiMappingPerEnv>>(
  (saved?.lokiMappingByEnv as Record<string, LokiMappingPerEnv>) ?? {},
)
function getLokiMapping(envID: string): LokiMappingPerEnv {
  if (!lokiMappingByEnv[envID]) {
    lokiMappingByEnv[envID] = makeEmptyLokiMappingPerEnv()
  }
  return lokiMappingByEnv[envID]
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
    pushLog('cchub', 'info', `[${envID}] Grafana 列到 ${list.length} 个 datasource`, { envID })
  } catch (e: any) {
    lm.dsListStatus = 'fail'
    lm.dsListError = String(e?.message || e)
    pushLog('cchub', 'error', `[${envID}] 列 Grafana datasource 失败: ${lm.dsListError}`, { envID })
  }
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
  for (const svc of allServiceNames.value) {
    if (lm.serviceValues[svc]) continue
    const sLower = svc.toLowerCase()
    const hit = lm.serviceLabelValues.find(v => v.toLowerCase().includes(sLower))
    if (hit) lm.serviceValues[svc] = hit
  }
}

async function onEnvLabelKeyChanged(envID: string, newKey: string) {
  const lm = getLokiMapping(envID)
  lm.envLabelKey = newKey
  lm.envValue = ''
  await loadEnvLabelValues(envID)
  autoMatchLokiMapping(envID)
}
async function onServiceLabelKeyChanged(envID: string, newKey: string) {
  const lm = getLokiMapping(envID)
  lm.serviceLabelKey = newKey
  lm.serviceValues = {}
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
const dsScanState = reactive<Record<string, DSScanState>>((saved?.dsScanState as Record<string, DSScanState>) ?? {})
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

// 能否触发自动导入 = Step 5 至少有一条 (env, service) 挑了 dataId
const canAutoImportDS = computed<boolean>(() => {
  if (!isDesktop()) return false
  for (const k of Object.keys(serviceConfigSel)) {
    if ((serviceConfigSel[k] || '').trim()) return true
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
    for (const env of environments) {
      if (!env.id) continue
      const payload = buildPreloadPayload(env.id)
      const nsID = envNamespaces[env.id]
      if (!payload.valid) {
        const reason = `凭证不完整(缺: ${payload.missing.join(', ')})`
        pushLog('cchub', 'warn', `[${env.id}] ${reason},跳过整个 env`, { envID: env.id })
        for (const svc of allServiceNames.value) {
          dsScanState[scanStateKey(env.id, svc)] = { status: 'skipped', reason }
        }
        continue
      }
      if (nsID === undefined) {
        const reason = '未选 namespace,先回 Step 5 扫一次'
        pushLog('cchub', 'warn', `[${env.id}] ${reason}`, { envID: env.id })
        for (const svc of allServiceNames.value) {
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
      for (const svc of allServiceNames.value) {
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
function parseConfigContent(content: string, format?: string): any {
  const fmt = (format || '').toLowerCase()
  try {
    if (fmt === 'json') return JSON.parse(content)
    if (fmt === 'properties') return parseProperties(content)
    // 默认按 yaml 试 —— js-yaml 兼容大部分 scalar 单值的 properties 也能勉强吃
    return yaml.load(content)
  } catch {
    // 最后降级:按 properties 试一次
    try { return parseProperties(content) } catch { return null }
  }
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
const targetOptions = ['openclaw', 'claude-code', 'cursor'] as const
const targetDescriptions: Record<string, string> = {
  'openclaw': 'OpenClaw 安装包（bash install.sh 部署）',
  'claude-code': 'Claude Code 用户级 subagent（~/.claude/agents/<name>.md，@<name> 调用）',
  'cursor': 'Cursor 用户级 Custom Agent（~/.cursor/agents/<name>.md，AI 侧栏选用）',
}
const targetLabels: Record<string, string> = {
  'openclaw': 'OpenClaw',
  'claude-code': 'Claude Code',
  'cursor': 'Cursor IDE',
}
const enabledTargets = reactive<Record<string, boolean>>({
  ...Object.fromEntries(targetOptions.map(k => [k, true])),
  ...(saved?.enabledTargets ?? {}),
})
// 任一目标勾选 / 无目标勾选:Step 1 校验 + 后续步骤按需隐藏字段
const anyTargetSelected = computed(() => targetOptions.some(t => enabledTargets[t]))
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
const lastSavedAt = ref<number | null>(null) // unix ms;null = 本会话还没保存过(首次读取态)
watch(
  () => ({
    currentStep: currentStep.value,
    system,
    agent,
    targetModels,
    environments,
    repos,
    repoBranchesMap: repoBranchesMap.value,
    configCenterType: configCenterType.value,
    // 所有配置中心字段(含 secret)持久化到 draft —— 跟 yaml 策略对齐,
    // 用户已选择"明文也 OK"的分享模式。
    ccCredInputs,
    // env → namespace / (env,service) → dataId 用户手挑的映射(生成 yaml 的关键)
    envNamespaces,
    serviceConfigSel,
    serviceConfigGroup,
    // 预加载结果(entries + namespaces + notes),跨会话恢复;重进后下拉直接可用
    // 不用再扫一次。凭证变 / 配置中心改动 → 用户点"重新拉取"刷新即可。
    ccHubStateByEnv,
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
    idManualOverride: idManualOverride.value,
    openclawInstallDir: openclawInstallDir.value,
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
  // 导入 yaml 里 endpoints[].<field> 回填到 ccCredInputs(所有字段,含 secret):
  // yaml 带明文是设计如此,同事导入就齐活。占位符 {{XXX}} 跳过不覆盖用户可能已填的值。
  const endpoints = parsed.infrastructure?.config_center?.endpoints
  if (Array.isArray(endpoints) && typeof cc === 'string') {
    const fields = CC_FIELDS_BY_TYPE[cc] || []
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
  // 挑过的下拉选项都恢复。
  const svcMap = parsed.infrastructure?.config_center?.service_map
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
  lines.push(`  model: ${agent.model}    # 默认 LLM model id;claude-code / cursor 不消费本字段(用户客户端里选)`)
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
  lines.push('# repos：所有纳入排障范围的代码仓库。role/stack 决定 analyzer 与 skill 策略。')
  lines.push('repos:')
  for (const repo of repos) {
    // role / stack 若没扫到,兜底成 backend/go,保证 yaml schema 合法;
    // 这两个字段 Step 4 UI 是 readonly 自动识别 badge,用户如果对兜底不满意,
    // 预览这里改或回 Step 4 点"扫一下"。
    lines.push(`  - name: ${repo.name || 'my-service'}`)
    lines.push(`    url: ${repo.url || 'git@github.com:org/repo.git'}`)
    lines.push(`    role: ${repo.role || 'backend'}         # backend/frontend/gateway/infra/shared`)
    lines.push(`    stack: ${repo.stack || 'go'}             # go/java/node/php/python，决定用哪种配置扫描器`)
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
  // 设计:**所有字段**(包括密码 / token)的填过的值**都写进 system.yaml**,让同事
  // 导入后开箱即用,不用再问不用再填。没填的字段仍给 {{占位符}},让 install.sh 兜底。
  //
  // ⚠ 代价:yaml 带明文密码,分享范围必须可控(团队私有 git / 私密频道),**绝不能**
  // push 到公开 github / 贴公开论坛。secret 字段旁边有"🔒"图标提醒。
  lines.push('  config_center:        # 配置中心:nacos/apollo/consul/kubernetes/env-vars/none')
  lines.push(`    type: ${configCenterType.value}`)
  const ccFields = CC_FIELDS_BY_TYPE[configCenterType.value] || []
  if (ccFields.length > 0) {
    lines.push('    endpoints:     # ⚠ 含明文凭证,仅团队私密范围分享,别 commit 公开 git')
    for (const env of environments) {
      if (!env.id) continue
      lines.push(`      - env: ${env.id}`)
      for (const f of ccFields) {
        const k = ccKeyFor(configCenterType.value, env.id, f.key)
        const envVar = f.envVar(env.id)
        const v = (ccCredInputs[k] || '').trim()
        if (v) {
          const comment = f.secret ? '      # ⚠ secret,yaml 分享注意范围' : ''
          lines.push(`        ${f.key}: ${yamlStr(v)}${comment}`)
        } else {
          lines.push(`        ${f.key}: "{{${envVar}}}"      # 没填,由 install.sh 交互收集`)
        }
      }
    }
  }

  // service_map:每个 env → 每个 service → 用的 namespace + group + data_id。
  // 这是 Step 5 预加载 + 下拉框挑出来的结果,routing skill / config-executor 部署后据此
  // 直接打到配置中心某条具体记录。没挑过的 (env, service) 不写入,留给 skill 运行时动态解析。
  const svcMapLines: string[] = []
  for (const env of environments) {
    if (!env.id) continue
    const perEnv: string[] = []
    for (const svc of allServiceNames.value) {
      const dataId = (serviceConfigSel[svcKey(env.id, svc)] || '').trim()
      if (!dataId) continue
      const ns = (envNamespaces[env.id] || '').trim()
      const group = (serviceConfigGroup[svcKey(env.id, svc)] || '').trim()
      perEnv.push(`        ${yamlStr(svc)}:`)
      if (ns) perEnv.push(`          namespace: ${yamlStr(ns)}`)
      if (group) perEnv.push(`          group: ${yamlStr(group)}`)
      perEnv.push(`          data_id: ${yamlStr(dataId)}`)
    }
    if (perEnv.length > 0) {
      svcMapLines.push(`      ${env.id}:`)
      svcMapLines.push(...perEnv)
    }
  }
  if (svcMapLines.length > 0) {
    lines.push('    service_map:   # 向导 Step 5 挑的:每个环境每个服务对应哪条配置(namespace / group / data_id)')
    lines.push(...svcMapLines)
  }

  // observability:对每个勾选的工具写 endpoints(按 env 列连接字段,跟 Step 5 同样的策略)。
  // 用户填过的值直接进 yaml;空字段不输出。保留 loki/prometheus via_grafana 标志
  // (这俩常见"通过 Grafana 代理访问"的用法,在选 Grafana 时自动标 true 给 routing skill 参考)。
  const anyObs = Object.values(enabledObservability).some(Boolean)
  if (anyObs) {
    lines.push('')
    lines.push('  observability:        # ⚠ 含明文凭证,仅团队私密范围分享')
    for (const spec of OBS_TOOL_SPECS) {
      if (!enabledObservability[spec.key]) continue
      lines.push(`    ${spec.key}:`)
      lines.push('      enabled: true')
      if (spec.key === 'loki' || spec.key === 'prometheus') {
        lines.push(`      via_grafana: ${enabledObservability.grafana}`)
      }
      if (spec.key === 'elk') {
        lines.push(`      default_index: "${system.id || 'my-system'}-logs-*"`)
      }
      // Loki 标签映射:per-env(每个 env 自己的 envLabelKey / serviceLabelKey / dsUID / 选中值)
      if (spec.key === 'loki') {
        const lmEnvs = environments.filter(env => env.id && lokiMappingByEnv[env.id]
          && lokiMappingByEnv[env.id].envLabelKey && lokiMappingByEnv[env.id].serviceLabelKey)
        if (lmEnvs.length > 0) {
          lines.push('      label_mapping_by_env:    # routing skill 拼 LogQL 时按 (env, service) 注入 label 过滤器')
          for (const env of lmEnvs) {
            const lm = lokiMappingByEnv[env.id]!
            lines.push(`        ${env.id}:`)
            lines.push(`          env_label: ${yamlStr(lm.envLabelKey)}`)
            lines.push(`          service_label: ${yamlStr(lm.serviceLabelKey)}`)
            if (lm.dsUID) lines.push(`          grafana_ds_uid: ${yamlStr(lm.dsUID)}`)
            if (lm.envValue) {
              lines.push(`          ${lm.envLabelKey}: ${yamlStr(lm.envValue)}`)
            }
            const svcLines: string[] = []
            for (const svc of allServiceNames.value) {
              const v = (lm.serviceValues || {})[svc]
              if (!v) continue
              svcLines.push(`            ${yamlStr(svc)}:`)
              svcLines.push(`              ${lm.serviceLabelKey}: ${yamlStr(v)}`)
            }
            if (svcLines.length > 0) {
              lines.push('          service_map:')
              lines.push(...svcLines)
            }
          }
        }
      }
      // 按 env 列填过的字段
      const envRows: string[] = []
      for (const env of environments) {
        if (!env.id) continue
        const fieldLines: string[] = []
        for (const f of spec.fields) {
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
    // 细分:cc.<env>.scan / cc.<env>.namespace / cc.<env>.svc.<service> / cc.<env>.<field>
    const parts = k.split('.')
    const envID = parts[1]
    const kind = parts[2]
    if (kind === 'scan') return `${envID} 环境未预加载成功`
    if (kind === 'namespace') return `${envID} 环境未选 namespace`
    if (kind === 'svc') return `${envID} 环境 "${parts[3]}" 服务未映射 dataId`
    return `${configCenterType.value}.${envID}.${kind}`
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
  const errs = new Set<string>()
  const step = currentStep.value
  if (step === 1) {
    if (!system.id.trim()) errs.add('system.id')
    else if (!/^[a-z0-9][a-z0-9-]*$/.test(system.id)) errs.add('system.id')
    if (!system.name.trim()) errs.add('system.name')
  } else if (step === 2) {
    if (!agent.name.trim()) errs.add('agent.name')
    if (!anyTargetSelected.value) errs.add('targets.none')
    if (enabledTargets['openclaw']) {
      if (!agent.workspace_name.trim()) errs.add('agent.workspace_name')
      if (!(targetModels['openclaw'] || '').trim()) errs.add('model.openclaw')
    }
  } else if (step === 3) {
    environments.forEach((e, i) => {
      if (!e.id.trim()) errs.add(`env.${i}.id`)
      if (!e.api_domain.trim()) errs.add(`env.${i}.api_domain`)
    })
  } else if (step === 4) {
    repos.forEach((r, i) => {
      if (!r.name.trim()) errs.add(`repo.${i}.name`)
      if (r._source === 'local') {
        if (!(r._localPath || '').trim()) errs.add(`repo.${i}.localPath`)
      } else {
        if (!r.url.trim()) errs.add(`repo.${i}.url`)
      }
    })
  } else if (step === 5) {
    const fields = CC_FIELDS_BY_TYPE[configCenterType.value] || []
    if (fields.length > 0) {
      environments.forEach((e) => {
        if (!e.id.trim()) return // env.id 空已经是 Step 3 的锅
        // 1) 凭证字段必填(optional 的跳过)
        for (const f of fields) {
          if (f.optional) continue
          const k = ccKeyFor(configCenterType.value, e.id, f.key)
          if (!(ccCredInputs[k] || '').trim()) {
            errs.add(`cc.${e.id}.${f.key}`)
          }
        }
        // 2) 本 env 必须预加载成功一次 —— 配置源没通过不能继续
        const st = ccHubStateByEnv[e.id]
        if (!st || st.status !== 'ok') {
          errs.add(`cc.${e.id}.scan`)
          return // 扫描都没成 → 没必要再校验 namespace / service,避免一堆冗余项
        }
        // 3) 必须选中一个 namespace(空字符串是合法的 "public",非 undefined 才算选过)
        if (!(e.id in envNamespaces)) {
          errs.add(`cc.${e.id}.namespace`)
          return // namespace 没选 → dataId 校验无意义
        }
        // 4) 每个服务必须映射到一条 dataId(没定义服务的时候不校验)
        for (const svc of allServiceNames.value) {
          const k = svcKey(e.id, svc)
          if (!(serviceConfigSel[k] || '').trim()) {
            errs.add(`cc.${e.id}.svc.${svc}`)
          }
        }
      })
    }
  } else if (step === 6) {
    // 数据层校验:
    //   1) 配置拉取必须 ok / empty(skipped/error 拦)
    //   2) 每个识别出的组件都必须做过连通性测试且通过
    //      没测过 → 拦(notested);测过失败 → 拦(probefail);测过通过 → 放行
    //      用户嫌某组件用不上 → 点 ✕ 删掉,删后就不在校验范围里了
    for (const e of environments) {
      if (!e.id.trim()) continue
      for (const svc of allServiceNames.value) {
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
})

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

function nextStep() {
  if (!canGoNext.value) return
  if (currentStep.value < totalSteps) {
    currentStep.value++
    if (currentStep.value === totalSteps) {
      yamlOutput.value = generateYAML()
    }
  }
}

function prevStep() {
  // 回退不校验,自由退
  if (currentStep.value > 1) currentStep.value--
}

function goToStep(step: number) {
  // 倒退随意;前进必须当前步无 error
  if (step < currentStep.value) {
    currentStep.value = step
  } else if (step > currentStep.value && canGoNext.value) {
    // 允许跳多步,但中间每步都得满足(这里只检查当前步;严谨版可以逐步 validate,先简单化)
    currentStep.value = step
    if (currentStep.value === totalSteps) {
      yamlOutput.value = generateYAML()
    }
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
const deployTarget = ref<'openclaw' | 'claude-code' | 'cursor'>('openclaw')
// Step 1 里 enabledTargets 变动后,如果当前 deployTarget 已经不在勾选列表里,
// 自动切到第一个还在勾选的 target,避免 Step 7 下拉显示空选项。
watch(() => ({ ...enabledTargets }), (cur) => {
  if (!cur[deployTarget.value]) {
    const next = targetOptions.find(t => cur[t])
    if (next) deployTarget.value = next
  }
}, { deep: true })
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
    case 'claude-code': return '装到 ~/.claude/agents/<name>.md(用户级 subagent,所有项目共享)'
    case 'cursor': return '装到 ~/.cursor/agents/<name>.md(用户级 Custom Agent,所有项目共享)'
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
    // 构造 repoPaths:每个仓库算一下本机绝对路径,后端会烤进
    // 产物 skills/routing/references/repo-path-map.yaml。system.yaml 本身不含这些
    // 路径(故意的,保持可分享)。
    //   - local 模式:用 _localPath(用户选的已有目录)
    //   - remote 模式:优先 _cloneTarget(自定义),回落到 <默认>/<name>
    //   - 名字空或算不出路径的跳过(后端模板会留空提示用户补齐)
    const repoPaths: Record<string, string> = {}
    const effectiveRoot = reposRootInput.value.trim() || resolvedReposRoot.value
    for (const r of repos) {
      if (!r.name.trim()) continue
      let path = ''
      if (r._source === 'local') {
        path = (r._localPath || '').trim()
      } else {
        path = (r._cloneTarget || '').trim()
        if (!path && effectiveRoot) {
          path = `${effectiveRoot.replace(/\/$/, '')}/${r.name}`
        }
      }
      if (path) repoPaths[r.name] = path
    }
    await importAndDeploy(yamlOutput.value, deployTarget.value, deployDestPath.value, repoPaths)
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

// role / stack 的枚举集合:原本给 Step 4 的下拉 select 用,现在改成自动识别
// readonly badge 后没 UI 消费点了,但 generateYAML 写注释时还提示合法值,
// 留着当"文档"引用。未来补"手改覆盖"功能时也能直接复用。
const _roleOptions = ['backend', 'frontend', 'gateway', 'infra', 'shared']
const _stackOptions = ['go', 'java', 'node', 'php', 'python']
void _roleOptions; void _stackOptions
const configTypeOptions = ['nacos', 'apollo', 'consul', 'env-vars', 'kubernetes', 'none']

const configTypeDescriptions: Record<string, string> = {
  nacos: 'Nacos — 配置与服务发现中心(阿里巴巴开源)',
  apollo: 'Apollo — 分布式配置中心(携程开源)',
  consul: 'Consul KV — HashiCorp 键值存储',
  'env-vars': '环境变量 / .env 文件 — 不使用远程配置中心',
  kubernetes: 'Kubernetes ConfigMap / Secret',
  none: '不使用任何配置源',
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
      <p class="help-text" style="margin-bottom:14px">
        这里填"给哪摊业务打工"的信息 — 系统的业务名、ID、一句话描述。
        机器人自己叫什么、装哪些平台、用什么模型到下一步配。
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
    <div v-if="currentStep === 2" class="card lg">
      <h2>机器人身份</h2>
      <p class="help-text" style="margin-bottom:14px">
        这里定义"机器人自己"—— 叫什么、装到哪些 AI 平台、用什么模型。
        部署目标决定后面 Step 4 / Step 7 等几步的字段按需展开。
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
            :class="{ selected: enabledTargets[t] }"
          >
            <label class="target-card-head">
              <input type="checkbox" v-model="enabledTargets[t]" />
              <span class="target-title">{{ targetLabels[t] }}</span>
              <!-- claude-code / cursor 旁边挂"已装 vX / 未装"徽标,让用户一眼看到
                   本机有没有对应客户端。openclaw 另有专门的探测 UI,不在这里重复;
                   embedded 由 Studio 自己承载,也不需要外部客户端标。 -->
              <span
                v-if="t === 'claude-code' && aitoolsResult"
                class="target-install-badge"
                :class="aitoolsResult.claude_code.installed ? 'ok' : 'miss'"
                :title="aitoolsResult.claude_code.note || ''"
              >
                {{ aitoolsResult.claude_code.installed
                  ? `✓ v${aitoolsResult.claude_code.version || '?'}`
                  : '⚠ 未检测到' }}
              </span>
              <span
                v-else-if="t === 'cursor' && aitoolsResult"
                class="target-install-badge"
                :class="aitoolsResult.cursor.installed ? 'ok' : 'miss'"
                :title="aitoolsResult.cursor.note || ''"
              >
                {{ aitoolsResult.cursor.installed
                  ? `✓ v${aitoolsResult.cursor.version || '?'}`
                  : '⚠ 未检测到' }}
              </span>
              <!-- openclaw:onMounted 时已探一次,这里用 status === 'ok' + 版本号决定徽章 -->
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

              <!-- openclaw 独有:工作区目录名 -->
              <div v-if="t === 'openclaw'" class="target-field">
                <label class="target-field-label">
                  工作区目录名
                  <span class="help-icon" title="~/.openclaw/workspace/<这里>/ 创建目录。推荐 ASCII 小写(如 shop-bot)。多个 agent 并存时每个用不同名字避免覆盖。">?</span>
                </label>
                <input
                  v-model="agent.workspace_name"
                  type="text"
                  :placeholder="workspaceNameDefault"
                  :class="{ error: hasError('agent.workspace_name') }"
                />
              </div>
            </div>
          </div>
        </div>
        <div v-if="!anyTargetSelected" class="error-text" style="margin-top:6px">
          至少勾选一个部署目标
        </div>
      </div>
    </div>

    <!-- Step 3 -->
    <div v-if="currentStep === 3" class="card lg">
      <h2>环境列表</h2>
      <p class="help-text">
        常见环境:dev(开发) / test(测试) / staging(预发布) / prod(生产)。
        每行两个域名 —— API 域名(后端接口)、Web 域名(前端入口,选填)。<br/>
        <strong>推荐带上 http/https 前缀</strong> —— bot 要实际发请求时 Studio 不替你猜协议
        (内部 dev 常是 http、公网 prod 多是 https)。不带前缀也接受,下游会按 https 兜底。
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
    <div v-if="currentStep === 4" class="card lg">
      <h2>代码仓库</h2>
      <p class="help-text">
        每个仓库可选 <strong>本地已有目录</strong> 或 <strong>远程 URL</strong>:<br/>
        • <strong>本地</strong> → 选目录即 <strong>自动扫描</strong>(读 <code>git remote</code> + 探测技术栈 + 列分支)<br/>
        • <strong>远程</strong> → 填 URL + 目标目录后点 <strong>"🔄 同步到本地并扫描"</strong>(先 clone 再扫)<br/>
        • 技术栈 / 服务名 / 分支 从扫描结果读(role 用 yaml 里的默认 <code>backend</code>,想改去 Step 7 手改)。
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
              Clone 目标目录
              <span class="auto-tag">可选</span>
              <span class="field-hint">
                — 不填就 clone 到<strong>默认目录</strong>
                <code>{{ displayPath(reposRootInput.trim() || resolvedReposRoot) }}/{{ repo.name || '&lt;repo.name&gt;' }}</code>
              </span>
            </label>
            <!-- 跟本地仓库目录一致:readonly,只能通过按钮选。
                 有"🗑 清空"按钮把自定义 clone 目标还原为"用默认目录"(仍然 clone 到
                 reposRootInput/repo.name 下)。 -->
            <div class="path-input-row">
              <input
                :value="displayPath(repo._cloneTarget || '')"
                type="text"
                :placeholder="`${displayPath(reposRootInput.trim() || resolvedReposRoot)}/${repo.name || '<repo.name>'}`"
                readonly
                class="path-readonly"
                :title="repo._cloneTarget || ''"
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
              <span>正在 git clone + DetectStack/Role/Framework + 读取分支列表…</span>
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
              <span>DetectStack / Role / Framework + 读取分支…</span>
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

        <!-- 自动识别结果:只展 技术栈(仅 readonly)。其它字段(role/framework)
             启发式误报率高,不自动填,用户在 yaml 里手改。 -->
        <div v-if="hasRepoSource(repo)" class="form-group">
          <label>
            技术栈
            <span class="field-hint">(扫描后自动填,只读)</span>
          </label>
          <!-- 只展示 技术栈 —— 角色 / 框架 启发式误报率太高,用户宁可 yaml 里手改 role
               (backend/frontend/gateway/...),不看 UI 里的错 pill。 -->
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

        <!-- 服务名:自动识别出 chip 列表,每个右上角 ✕ 可删。monorepo 常见多服务,
             也支持用户"+" 手动补未识别到的。 -->
        <div v-if="hasRepoSource(repo)" class="form-group">
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

      <!-- 凭证表单:nacos/apollo/consul 才展;env-vars/kubernetes/none 不需要。
           不走 Studio 钥匙串 —— 值直接存 localStorage draft + 写进 system.yaml,
           部署时由生成器注入到各 AI 平台的 MCP server 配置文件
           (openclaw.json / .mcp.json / .cursor/mcp.json)里。 -->
      <div v-if="CC_FIELDS_BY_TYPE[configCenterType]" class="form-group">
        <label>
          {{ configCenterType }} 连接配置
          <span class="field-hint">— 按环境维度填写,保存后写入 system.yaml,部署时注入目标平台 MCP Server 配置</span>
        </label>
        <div class="cc-share-warn">
          <div class="cc-share-warn-title">⚠ 数据流与共享范围</div>
          <ul class="cc-share-warn-list">
            <li>
              本页填写的所有字段(含密码、token 等凭证)将保存至
              <code>system.yaml</code>。
            </li>
            <li>
              部署阶段,生成器会把对应值注入目标 AI 平台的 MCP Server 环境变量
              (OpenClaw / Claude Code / Cursor / 桌面内嵌对话;每项字段旁标注了对应的
              环境变量名)。
            </li>
            <li>
              <strong>system.yaml 含明文凭证</strong>,请仅在可信范围内分享
              (团队私有仓库 / 内部协作渠道),<strong>避免提交至公开代码仓库或公开社区</strong>。
            </li>
            <li>
              留空的字段不会写入 yaml;部署 OpenClaw 时由
              <code>install.sh</code> 引导交互式补充。
            </li>
          </ul>
        </div>
        <div v-for="env in environments" :key="env.id" class="cc-env-block">
          <div class="cc-env-head">
            <span class="cc-env-label">{{ env.id || '(未命名 env)' }}</span>
            <span v-if="env.is_prod" class="cc-env-prod-tag">prod</span>
          </div>
          <div class="cc-env-fields">
            <div
              v-for="f in CC_FIELDS_BY_TYPE[configCenterType]"
              :key="f.key"
              class="cc-field"
            >
              <label class="cc-field-label">
                {{ f.label }}
                <span v-if="f.optional" class="auto-tag">选填</span>
                <span v-if="f.secret" class="cc-scope-tag secret" title="Secret:会写入 yaml,分享时注意范围">🔒 Secret</span>
              </label>
              <div class="cc-field-row">
                <input
                  v-model="ccCredInputs[ccKeyFor(configCenterType, env.id, f.key)]"
                  :type="f.secret && !isRevealed(ccKeyFor(configCenterType, env.id, f.key)) ? 'password' : 'text'"
                  :placeholder="f.placeholder || ''"
                  autocomplete="off"
                  spellcheck="false"
                  class="cc-input"
                />
                <button
                  v-if="f.secret"
                  type="button"
                  class="btn-link cc-reveal"
                  @click="toggleReveal(ccKeyFor(configCenterType, env.id, f.key))"
                  :title="isRevealed(ccKeyFor(configCenterType, env.id, f.key)) ? '隐藏明文' : '显示明文'"
                >{{ isRevealed(ccKeyFor(configCenterType, env.id, f.key)) ? '🙈' : '👁' }}</button>
                <button
                  v-if="ccCredInputs[ccKeyFor(configCenterType, env.id, f.key)]"
                  type="button"
                  class="btn-link cc-delete"
                  @click="clearCCFieldInput(ccKeyFor(configCenterType, env.id, f.key))"
                  title="清空本字段"
                >🗑</button>
              </div>
              <div class="cc-env-hint">
                对应环境变量:<code>{{ f.envVar(env.id || 'ENV') }}</code>
              </div>
            </div>
          </div>

          <!-- 真实预加载:用户填完凭证后,点一下连目标配置中心拉可用条目清单。
               按钮挨着每个 env 块,各 env 独立 loading / 错误态。 -->
          <div class="cc-preload-row">
            <!-- 文字 wrap 在 <span> 里,spinner 和 label 都是明确的 flex 子元素,
                 避免"裸文本 text node + 元素 span"混用导致 flex gap / 对齐奇怪。 -->
            <!-- 三种状态分别用独立 <button>,外加 :key 指示 Vue 不要复用旧 DOM。
                 之前同一 <button> 内切换文本,触发过 WebKit(Wails)的 GPU layer 残影 ——
                 老按钮形状"拍下来"没刷掉,新按钮旁边多一个空矩形。:key 换掉整个元素,
                 WebKit 每次 layout 都新建 / 销毁,残影无法跨帧保留。 -->
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
            >🔄 重新拉取</button>
            <button
              v-else
              :key="`cc-preload-${env.id}-idle`"
              type="button"
              class="btn cc-preload-btn"
              @click="runCCHubPreload(env.id)"
            >🔍 加载配置</button>
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
          <div v-if="envScanned(env.id)" class="cc-map-block">
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
                  :class="{ error: hasError(`cc.${env.id}.namespace`) }"
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

            <!-- 每个 service 一行:name + dataId 下拉 -->
            <div v-if="allServiceNames.length > 0" class="cc-map-svc-list">
              <div
                v-for="svc in allServiceNames"
                :key="svc"
                class="cc-map-svc-row"
              >
                <span class="cc-map-svc-name">{{ svc }}</span>
                <select
                  v-model="serviceConfigSel[svcKey(env.id, svc)]"
                  class="cc-map-select cc-map-select-svc"
                  :class="{ error: hasError(`cc.${env.id}.svc.${svc}`) }"
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
            <div v-else class="cc-map-hint">
              先在 Step 4 填好 repos 的 <code>service_names</code>,这里才有服务列表可映射。
            </div>
          </div>

          <!-- 预加载失败时,不在页面渲染长错误文本;summary 行已显示 "✗ 拉取失败,详见日志",
               用户按 toast 提示进左侧「日志」页看完整栈。
               "下一步" 按钮旁的"还差 N 项"汇总提示已经包含该 env 未预加载的信息,
               env 块内不再重复展示,避免视觉冗余。 -->

        </div>
      </div>

      <!-- env-vars / kubernetes / none:无需连接凭证,给出相应说明 -->
      <div v-else-if="configCenterType === 'env-vars'" class="form-group">
        <p class="help-text">
          已选择 <strong>env-vars</strong>:机器人直接读取仓库内 <code>.env</code> 文件,
          无需连接远程配置中心,也无需填写连接凭证。仓库扫描器会在各环境目录下定位 .env 文件。
        </p>
      </div>
      <div v-else-if="configCenterType === 'kubernetes'" class="form-group">
        <p class="help-text">
          已选择 <strong>kubernetes</strong>:机器人通过当前 <code>kube-context</code> 读取
          ConfigMap / Secret。访问凭证来源于本机 <code>~/.kube/config</code>,不在此处收集。
        </p>
      </div>
      <div v-else-if="configCenterType === 'none'" class="form-group">
        <p class="help-text">
          已选择 <strong>none</strong>:机器人不连接任何配置中心,本步骤无需配置。
        </p>
      </div>
    </div>

    <!-- Step 7:可观测性 -->
    <div v-if="currentStep === 7" class="card lg">
      <h2>可观测性</h2>
      <p class="help-text">
        勾选本系统依赖的可观测性组件(Grafana / Loki / Prometheus / Jaeger 等)。
        勾中后展开,按环境填写连接信息。所填值写入 <code>system.yaml</code>,部署时
        注入各 AI 平台的 MCP server 配置。留空的字段不进 yaml。
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
              <div class="ds-item-fields">
                <div v-for="f in spec.fields" :key="f.key" class="cc-field">
                  <label class="cc-field-label">
                    {{ f.label }}
                    <span v-if="f.optional" class="auto-tag">选填</span>
                    <span v-if="f.secret" class="cc-scope-tag secret" title="Secret:会写入 yaml,分享时注意范围">🔒 Secret</span>
                  </label>
                  <div class="cc-field-row">
                    <input
                      v-model="toolInputs[toolKeyFor('obs', spec.key, env.id, f.key)]"
                      :type="f.secret && !isRevealed(toolKeyFor('obs', spec.key, env.id, f.key)) ? 'password' : 'text'"
                      :placeholder="f.placeholder || ''"
                      autocomplete="off"
                      spellcheck="false"
                      class="cc-input"
                      @input="scheduleObsProbe(spec.key, env.id)"
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
                  </div>
                </div>
              </div>

              <!-- Loki 标签映射(per-env):每个 env 用自己的 grafana/loki 凭证拉 datasources/labels/values -->
              <div
                v-if="(spec.key === 'grafana' || spec.key === 'loki')"
                class="loki-env-mapping"
              >
                <div class="loki-env-mapping-head">
                  🏷 Loki 标签映射 ({{ env.id }}) —— 用本环境 Grafana / Loki 凭证拉实时标签
                </div>

                <!-- Step 1: datasource(只 grafana 卡有) -->
                <div v-if="spec.key === 'grafana'" class="loki-mapping-step">
                  <div class="loki-mapping-step-head">
                    <span class="loki-step-num">1</span> Grafana datasource
                  </div>
                  <div class="cc-field-row">
                    <button
                      v-if="getLokiMapping(env.id).dsListStatus === 'loading'"
                      :key="`loki-ds-${env.id}-loading`"
                      type="button" class="btn cc-preload-btn" disabled
                    >
                      <span class="cc-preload-spinner" aria-hidden="true"></span>
                      加载中…
                    </button>
                    <button
                      v-else
                      :key="`loki-ds-${env.id}-idle`"
                      type="button" class="btn cc-preload-btn"
                      @click="loadLokiDatasources(env.id)"
                    >🔍 加载 datasources</button>
                    <select
                      v-if="getLokiMapping(env.id).dsList.length > 0"
                      v-model="getLokiMapping(env.id).dsUID"
                      class="cc-map-select"
                    >
                      <option value="">— 选 Loki datasource —</option>
                      <option
                        v-for="ds in getLokiMapping(env.id).dsList.filter(d => d.is_loki)"
                        :key="ds.uid"
                        :value="ds.uid"
                      >{{ ds.name }} ({{ ds.uid }})</option>
                    </select>
                    <span
                      v-if="getLokiMapping(env.id).dsListStatus === 'fail'"
                      class="url-probe-badge fail"
                      :title="getLokiMapping(env.id).dsListError"
                    >✗ {{ getLokiMapping(env.id).dsListError }}</span>
                  </div>
                </div>

                <!-- Step 2: labels -->
                <div class="loki-mapping-step">
                  <div class="loki-mapping-step-head">
                    <span class="loki-step-num">{{ spec.key === 'grafana' ? 2 : 1 }}</span> 加载 Loki 标签
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
                    <span class="loki-step-num">{{ spec.key === 'grafana' ? 3 : 2 }}</span> 选环境 / 服务维度的 label key
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
                    <span class="loki-step-num">{{ spec.key === 'grafana' ? 4 : 3 }}</span> 选 env / service 具体 label 值
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
                    >
                      <option value="">— 选 —</option>
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
    </div>

    <!-- Step 6:数据层 —— 从配置源拉取各服务配置,按"环境 → 服务 → 数据层组件"展示识别结果 -->
    <div v-if="currentStep === 6" class="card lg">
      <h2>数据层</h2>
      <p class="help-text">
        点「📥 从配置中心读取」会拉 Step 5 已挑的各服务配置,扫描里面的
        redis / mysql / mongodb / kafka / ... 配置块。本页按 <strong>环境 → 服务 → 数据层组件</strong>
        展示识别结果,字段可直接编辑(明文修改,不会影响源配置中心)。
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

    <!-- Step 8 -->
    <div v-if="currentStep === 8" class="card lg">
      <h2>预览 + 生成</h2>
      <!-- target 已在 Step 1 勾选;这里只读展示一下"会产出哪些平台",不让改。
           想改回去 Step 1。 -->
      <div class="target-readonly-row">
        <span class="target-readonly-label">本次部署目标:</span>
        <span
          v-for="t in targetOptions"
          v-show="enabledTargets[t]"
          :key="t"
          class="target-readonly-chip"
        >{{ targetLabels[t] }}</span>
        <span v-if="!anyTargetSelected" class="error-text">
          Step 1 没勾选任何部署目标,无法生成产物
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
            <!-- 只列 Step 1 勾选的 target,避免让用户在这里部署一个没生成的平台产物 -->
            <select v-model="deployTarget" :disabled="deployLoading">
              <option
                v-for="t in targetOptions"
                v-show="enabledTargets[t]"
                :key="t"
                :value="t"
              >{{ targetLabels[t] }}</option>
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
            <!-- readonly input,只能通过按钮选 —— 所有路径输入框统一约束 -->
            <div class="deploy-inline-path">
              <input
                :value="deployDestPath"
                type="text"
                :placeholder="isManagedTarget ? autoDefaultPath : '点右侧按钮选择项目根路径'"
                :disabled="deployLoading"
                readonly
                class="path-readonly"
                :title="deployDestPath"
              />
              <button type="button" class="btn" :disabled="deployLoading" @click="pickDeployDestPath">
                {{ deployDestPath ? '重新选…' : '选目录…' }}
              </button>
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
  margin-left: 4px;
}
/* "(扫一下自动填)" 这种轻提示,比 .auto-tag 再弱一档;跟 label 同行不抢视觉 */
.field-hint {
  font-size: 11px; font-weight: 400; color: var(--c-muted);
  margin-left: 6px;
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
