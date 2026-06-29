<script setup lang="ts">
import { ref, reactive, computed, watch, onMounted, onUnmounted, onErrorCaptured, nextTick, provide } from 'vue'

// 给 App.vue 的 keep-alive `:exclude="['InitPage']"` 用,让本页不被缓存。
// 跟 HomePage 的"清空重开"按钮配套:用户清掉 localStorage 后,InitPage 重 mount 取干净状态。
defineOptions({ name: 'InitPage' })
import { useRouter } from 'vue-router'
import {
  exportYAML,
  getRepoPathsForSystem,
  kuboardListResources,
  isDesktop,
  listBranchesForRepo,
  validate as bridgeValidate,
} from '../lib/bridge'
import type { CCHubEntry, CCHubNamespace } from '../lib/bridge'
import { confirmDialog } from '../lib/confirm'
import { WizardStoreKey } from '../lib/wizardStore'
import { pushLog } from '../lib/logStore'
import { toast } from '../lib/toast'
import { countUmbrellaChildren } from '../lib/repoUmbrella'
import { Target, type TargetId } from '../lib/constants'
import type { CredField } from '../lib/credFields'
import { isCredFieldHidden, resolveCredFieldDisplay } from '../lib/credFields'
import RepoListItem from '../components/RepoListItem.vue'
import ConfigSourceStep from '../components/ConfigSourceStep.vue'
import ObservabilityStep from '../components/ObservabilityStep.vue'
import DataStoreStep from '../components/DataStoreStep.vue'
import BotIdentityStep from '../components/BotIdentityStep.vue'
import WelcomeStep from '../components/WelcomeStep.vue'
import SystemBasicInfoStep from '../components/SystemBasicInfoStep.vue'
import YamlPreviewStep from '../components/YamlPreviewStep.vue'
import OneClickDeployStep from '../components/OneClickDeployStep.vue'
import EnvListStep from '../components/EnvListStep.vue'
import GlobalReposRootBlock from '../components/GlobalReposRootBlock.vue'
import { generateYAML as libGenerateYAML, type YAMLGenContext } from '../lib/yamlGenerator'
import { computeStepErrors as libComputeStepErrors, labelForErrorKey as libLabelForErrorKey, type ValidatorContext } from '../lib/yamlValidator'
import type { ApplyImportContext } from '../lib/yamlImporter'
import { copyToClipboard } from '../lib/clipboard'
import { useOpenClawDetect } from '../lib/useOpenClawDetect'
import { useURLProbe } from '../lib/useURLProbe'
import { useReposRoot } from '../lib/useReposRoot'
import { useAITools } from '../lib/useAITools'
import { useObsProbe } from '../lib/useObsProbe'
import {
  INIT_WIZARD_KEY as STORAGE_KEY,
  INIT_KUBOARD_STATE_KEY as KUBOARD_STATE_KEY,
  loadInitWizardDraft,
  loadInitKuboardState,
} from '../lib/useWizardDraft'
import { useKuboardState } from '../lib/useKuboardState'
import { useK8sRtWorkloads } from '../lib/useK8sRtWorkloads'
import { useObsAccessMode, obsAccessKey } from '../lib/useObsAccessMode'
import { useCCHubState } from '../lib/useCCHubState'
import { ccKeyFor, svcKey, probeKey } from '../lib/yamlShared'
import { useRepoScan } from '../lib/useRepoScan'
import { useLokiMappingState } from '../lib/useLokiMappingState'
import { useGrafanaDS, OBS_GRAFANA_DS_TYPES, obsGrafanaDsKey } from '../lib/useGrafanaDS'
import { useLokiLabels } from '../lib/useLokiLabels'
import { useCCHubPreload } from '../lib/useCCHubPreload'
import { useKuboardPreload } from '../lib/useKuboardPreload'
import { useDataStoreState } from '../lib/useDataStoreState'
import { useImportCrossCheck } from '../lib/useImportCrossCheck'
import { useDeployFlow } from '../lib/useDeployFlow'
import { migrateSavedStep } from '../lib/wizardStep'
import { useImportFlow } from '../lib/useImportFlow'
import { useDataStoreScan } from '../lib/useDataStoreScan'
import { useMonorepoHints } from '../lib/useMonorepoHints'
import { useSourceTypeReset } from '../lib/useSourceTypeReset'
import { useK8sRtAutoPick } from '../lib/useK8sRtAutoPick'
import { one2allListDeployments, one2allListResources } from '../lib/bridge/one2all'
import type { One2AllClusterEntry as O2ACluster } from '../lib/bridge/one2all'
import { one2allPreloadOptionsForPurpose, type One2AllPreloadOptions, type One2AllPreloadPurpose } from '../lib/one2allPreload'

const router = useRouter()

// ── Draft persistence (survives route switches and reloads) ──
// 草稿 load 助手 + storage key 常量收口在 lib/useWizardDraft.ts。
// Kuboard 资源树用独立 key 保存:大 draft blob 经常因 quota 静默失败,这层 fallback
// 让 kuboard 数据不会被波及;即使主 draft 没存上,只要这个 key 存了下拉 options 仍可用。
// 写侧(auto-save watch)还跟 30+ reactive 字段交织,留在原地。
const saved = loadInitWizardDraft()
const savedKuboardState = loadInitKuboardState()

// ── Step management ──
// migrateSavedStep 抽到 lib/wizardStep.ts(纯函数 + 7 条单测覆盖 schema=1/2 偏移、
// null / 越界 / 0 / 负数等)。行为跟原 inline 表达式严格等价 —— 不引入额外下限
// clamp(那是 InitPage 自己的 clampCurrentStep 兜底职责),避免任何运行时行为漂移。
const totalSteps = 10
const currentStep = ref<number>(migrateSavedStep(saved?.currentStep, saved?.wizardSchema, totalSteps))
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
const modelConsumingTargets = [Target.Openclaw] as const

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
  if (targetModels[Target.Openclaw]) agent.model = targetModels[Target.Openclaw]
  // openclaw 用户手挑过一次后,不再被探测出的 detected 列表自动覆盖(见 watch 注释)
  if (t === Target.Openclaw) onOpenclawModelChanged()
}

// ── OpenClaw 模型探测(只给 openclaw target 卡用) ──
// 勾上 openclaw → detect(默认 ~/.openclaw 或用户选目录)→ 成功填模型下拉 / 失败给"选目录"按钮 / 兜底回落 hardcoded modelGroups
// 完整逻辑在 lib/useOpenClawDetect.ts。InitPage 只负责把 saved 反填,以及把返回字段塞到模板/save。
const {
  openclawInstallDir,
  openclawDetectStatus,
  openclawDetectedModels,
  openclawDetectError,
  openclawResolvedDir,
  openclawVersion,
  openclawAuthProviders,
  runOpenClawDetect,
  pickOpenClawInstallDir,
} = useOpenClawDetect(saved?.openclawInstallDir ?? '')

// Claude Code / Cursor / Codex 检测在 lib/useAITools.ts。onMounted 自动 refreshAITools。
// detector 只做"提示"角色:扫到给绿勾,没扫到 badge 警告但 checkbox 仍可勾(信任用户)。
// BotsPage broken/ghost 状态兜底装坏的场景。
const { aitoolsResult, aitoolsRefreshing, refreshAITools } = useAITools()
// wrap:用户点"重新扫描"按钮时传 manual=true,触发 toast 反馈;
// onMounted 那次内部已经走 manual=false,toast 不弹。
function manualRefreshAITools() { refreshAITools(true) }

// watch / onMounted 已挪到 enabledTargets 声明之后(见该 const 下方),
// 这里留空避免重复声明。

// 自动派生默认值:
//   - agent.name   跟着 system.name 走(中文友好,用户可见)
//   - agent.workspace_name 跟着 system.id 走 + "-bot"(ASCII,目录名友好;CJK 目录名 cd/ls 有踩坑)
//   - 只在"字段还是上次自动生成的默认值"时才覆盖,用户手改过就不动。
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

// id 派生闭环(slugify / markIdManual / resetIdAuto / watch+onMounted)已收进
// SystemBasicInfoStep.vue。InitPage 这里只剩 idManualOverride 引用 —— 它要参与
// localStorage 草稿持久化(line ~4858 的 save()),所以走 v-model:idManualOverride
// 跟子组件双向绑定,父端持有 ref。
const idManualOverride = ref<boolean>(saved?.idManualOverride ?? false)

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
// 完整逻辑(800ms 防抖 + 切 step 时主动重试)在 lib/useURLProbe.ts。
const { urlProbeResults, urlProbeKey, scheduleURLProbe } = useURLProbe(currentStep, environments, 3)

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

// SourceSnapshot:_source 切换时打包暂存的"源相关字段集合"。
// url / _localPath / _cloneTarget 三者按当前 _source 二选一在用,但都纳入 snapshot
// (切到对面源再切回来,它们要原样恢复);name / stack / framework / service_names /
// env_branches / 扫描状态都跟"具体哪个源(那个 URL / 那个本地路径)" 强绑定,也一起。
interface SourceSnapshot {
  url: string
  name: string
  _nameManual: boolean
  stack: string
  framework: string
  service_names: string
  env_branches: Record<string, string>
  _localPath?: string
  _cloneTarget?: string
  _scanning: boolean
  _scanError?: string
  _scanned: boolean
  _scannedSource?: string
  _serviceEntries?: Record<string, string>
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
  // sub_path:本仓 URL clone 后的仓内子目录(同 URL 多服务 monorepo 场景)。
  // 例:truss 仓库下 services/commerce 是 Go 服务,web/admin 是 Node 后台 → 两个条目同 url
  // 不同 sub_path,clone 同一份 truss 后各自扫子目录。空 = 整 repo 当一个服务对待。
  // 跟 parent_path 正交:parent_path 是"在 umbrella 内的挂载位置",sub_path 是"本 URL clone 内的子目录"。
  sub_path?: string
  // parent_repo:引用 repos[].name,标明本仓是某 umbrella 切出去的独立 git 仓。
  // 默认部署:umbrella 先 clone,本仓 URL clone 到 <umbrella-clone>/<parent_path 或 name>(继承模式)。
  // 用户也可以填 _cloneTarget 走独立 clone 模式(跟普通仓库一样)。
  parent_repo?: string
  // parent_path:在 umbrella clone 内的挂载相对路径(只在 parent_repo 在场时生效),
  // 默认 = repo.name。.gitmodules 里 path != name 时(如 path=services/commerce, name=commerce)
  // 用 parent_path 显式给。
  parent_path?: string
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
  // 用户是否显式手挑过 role(via 角色下拉 @change / "采用"按钮)。
  // 影响 useRepoScan.refreshRoleHint:false → hint 跟当前 role 不一致时自动采用;true → 不再覆盖。
  _roleManuallyPicked?: boolean
  // _serviceEntries:服务名 → 仓库内入口子目录(相对仓库根)。
  // 给同仓多服务场景(cmd/<x>/main.go / services/<x>/ / workspaces / pom-modules)用 ——
  // 这些不是 git submodule,不该拆成独立 repo,只把名字塞进 service_names + 入口路径
  // 单独记录;routing skill 据此把 service → 源码入口对应起来。
  // gitmodules 拆出的独立 repo 不用本字段(它们各自占一行,有自己的 sub_path / 本地路径)。
  _serviceEntries?: Record<string, string>
  // _sourceCache:切 _source(local ↔ remote)时缓存离开侧的全部源相关字段,
  // 切回来能原样恢复。最终 yaml emit 只看当前 _source 的活动字段。
  // 不缓存 role / sub_path / _roleHint / _roleManuallyPicked —— 这些跟"哪个源"无关,
  // 是用户对仓库的固有判断,跨源持久。
  _sourceCache?: { remote?: SourceSnapshot, local?: SourceSnapshot }
  // _fromYAML:本仓从 yaml import 而来(不是 wizard 新加 / splitMonorepo 拆出)。
  // yaml 是身份源(system.id 绑定已部署机器人,URL 是 repo 身份锚),用户改 URL / 选错
  // 本地目录 = 等于换项目,跟已部署机器人对应的 repo 错位。
  // 体验:URL input **可编辑**(允许 ssh ↔ https 等同仓换协议),但提交时校验 canonicalize
  // 跟 _yamlOriginalURL 比对,不一致(换了项目)→ 标红阻塞下一步;选本地目录时校验
  // 目录 origin 跟 r.url 匹配,不匹配 toast 拒绝。
  // 新加 / splitMonorepo 拆出的不继承这个标记,可完全自由改。
  _fromYAML?: boolean
  // _yamlOriginalURL:yaml import 时冻结的原 URL,_fromYAML repo 校验"换协议 vs 换项目"
  // 的真源。canonicalizeGitURL 比对(ssh://...:2222/foo/bar.git ≡ https://.../foo/bar)。
  _yamlOriginalURL?: string
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
  // saved.repos 类型是 RepoScanItem[](role: string),InitPage RepoItem.role 是 RepoRole 窄化;
  // saved 真的存的是窄字符串,直接 cast 一下复用 reactive。yaml import / makeEmptyRepo 仍走窄类型。
  Array.isArray(saved?.repos) && saved.repos.length ? (saved.repos as RepoItem[]) : [makeEmptyRepo()]
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
  if (!r._nameManual) {
    r.name = deriveRepoName(r.url)
    // 自动派生的名字也要同步快照,后续用户手改 name 时 cascade 才有正确 oldName
    snapshotRepoName(r)
  }
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

// 仓库名 → 上一次确认的快照,用来检测改名时级联更新所有引用本仓的 parent_repo。
// WeakMap 跟 RepoItem 对象绑定,行被 splice 删了自动 GC,不需要手动清理。
const previousRepoNames = new WeakMap<RepoItem, string>()
function snapshotRepoName(r: RepoItem) {
  previousRepoNames.set(r, (r.name || '').trim())
}

// 用户在 name 输入框里动手就算"手改过",记录下来避免被 URL 再覆盖。
// 但如果用户把 name 清空,视作"回到自动推",清除标记。
function onRepoNameInput(r: RepoItem) {
  // 检测重命名 → 级联同步所有 child 的 parent_repo 引用
  // (umbrella 子模块的 name 是 readonly,这条只在普通仓库 / umbrella 父行触发)
  const newName = (r.name || '').trim()
  const oldName = previousRepoNames.get(r) || ''
  if (oldName && newName && oldName !== newName) {
    let cascadeCount = 0
    for (const child of repos) {
      if ((child.parent_repo || '').trim() === oldName) {
        child.parent_repo = newName
        cascadeCount++
      }
    }
    if (cascadeCount > 0) {
      toast.info(`${oldName} → ${newName}:已同步更新 ${cascadeCount} 个子模块的 parent_repo 引用`)
    }
  }
  previousRepoNames.set(r, newName)

  if (!r.name.trim()) {
    r._nameManual = false
    // 立即用当前 URL 重填
    r.name = deriveRepoName(r.url)
    previousRepoNames.set(r, (r.name || '').trim())
    return
  }
  r._nameManual = true
  // 名字改了 → 重新拿一次推荐(本地路径优先,无路径退化到名字+stack)
  refreshRoleHint(r)
}

// refreshRoleHint / refreshSubmoduleHints / pickLocalRepoDir / resolveLocalRepoPath /
// pickCloneTarget / resolveCloneDest / scanSingleRepo 全套搬到 lib/useRepoScan.ts。
// 实例化点必须在 generateYAML 之后(它要传 generateYAML callback);见本文件
// "── Step 8: yaml 预览 + 生成 ──" 段附近的 useRepoScan() destructure。

// applyRoleHint 把推荐 role 落到 r.role(用户点"采用"按钮调)。
// 标 _roleManuallyPicked = true:即使后续 refreshRoleHint 又跑出别的推荐,也不再覆盖
// (用户已经表态过)。
function applyRoleHint(r: RepoItem) {
  if (r._roleHint?.role) {
    r.role = r._roleHint.role as RepoRole
    r._roleManuallyPicked = true
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

// Step 4 Monorepo banner 7 个 helper 收口在 lib/useMonorepoHints.ts;实例化点见
// 下方 repoBranchesMap 声明之后(splitMonorepo 异步落 repoBranchesMap[name],
// 必须先有 ref;TDZ 防御)。
function addRepo() {
  const r = makeEmptyRepo()
  repos.push(r)
  snapshotRepoName(r) // 新行 name 现在是 '',先种快照,用户填名后 cascade 才能算 oldName
}

// (旧 addSubmodule 按钮已下线 —— 自动检测 monorepo + 一键拆分能覆盖所有真实场景。
// 真有非典型布局漏检,用户可走"+ 添加仓库"再粘 url,行为等价。)

function removeRepo(idx: number) {
  if (repos.length <= 1) return
  // umbrella 不能在还有子模块引用它的时候被删 —— 否则 children 的 parent_repo
  // 引用就坏(health check 会报 error,新机器导入也无法 umbrella 继承编排)。
  // 用户必须先把所有 parent_repo == 本 umbrella name 的子模块删掉,才能删 umbrella。
  const target = repos[idx]
  if (!target) return
  // umbrella 父仓有 child 引用时禁删,否则 child.parent_repo 引用会坏(health check 报 error,
  // 新机器导入也无法继承编排)。判定逻辑统一走 lib/repoUmbrella.countUmbrellaChildren,
  // 跟 isRepoDeletable / template umbrella-children-count 三处共用同一份。
  const childCount = countUmbrellaChildren(repos, target.name)
  if (childCount > 0) {
    toast.error(`先删除 ${target.name} 的 ${childCount} 个子模块行,才能删 umbrella`)
    return
  }
  repos.splice(idx, 1)
}

// 给 RepoListItem 的 × 按钮判定 disabled:本仓是 umbrella + 还有 child 引用时禁删。
// countUmbrellaChildren 详见 lib/repoUmbrella.ts(语义 + "空 name 误匹配" 防御写在那)。
function isRepoDeletable(r: RepoItem): boolean {
  if (repos.length <= 1) return false
  return countUmbrellaChildren(repos, r.name) === 0
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
// "研制环境偏好",不属于具体系统的配置 —— 绝对不能写进 troubleshooter.yaml,也不能进
// localStorage auto-save draft(见下方 watch(...) 的 tracked 字段列表)。
// 唯一合法的持久化路径:"💾 设为全局默认" 按钮 → setDefaultReposRoot → Go binding
// → userconfig.Save → ~/.tshoot/config.json。导入 yaml / 清空草稿都不动它。
// 完整逻辑(reposRootInput / global/resolved/homeDir + onMounted 反填 + displayPath
// + saveAsGlobalDefault + pickReposRoot)在 lib/useReposRoot.ts。
const {
  reposRootInput,
  globalDefaultReposRoot,
  resolvedReposRoot,
  homeDir,
  displayPath,
  saveAsGlobalDefault,
  pickReposRoot,
} = useReposRoot()

// repoName -> 真实 git 分支列表;扫描完填充,env_branches 下拉的 options 用它。
// 用 ref<Record> 而不是 per-repo 属性,避免跟 saved yaml 结构污染(env_branches
// 已经在 RepoItem 上了,再加个 branches 会影响序列化)。
// 但必须进 localStorage 草稿 —— 否则重开向导时 map 变成 {},模板里
//   v-if="repoBranchesMap[repo.name]?.length" 不成立 → <select> 退成 <input>,
// 用户会看到分支值(repo.env_branches 已持久化)但没有下拉选项,很迷惑。
const repoBranchesMap = ref<Record<string, string[]>>(
  saved?.repoBranchesMap ?? {},
)

// useMonorepoHints 实例化:repoBranchesMap 已 ready;resolveCloneDest 在下方 useRepoScan
// 才返回,但本 composable 内部调用(splitMonorepo)发生在用户 banner 操作触发时,lexical
// scope 解析晚于 useRepoScan init,所以包一层 closure 安全。
const {
  toggleSubmodulePick,
  pickedSubmoduleCount,
  isSubmoduleAlreadySplit,
  submodulePathFor,
  isGitSubmodulesHints,
  qualifyServiceName,
  mergeMonorepoIntoServices,
  splitMonorepo: splitMonorepoRaw,
} = useMonorepoHints({
  repos, environments, repoBranchesMap,
  resolveCloneDest: (r) => resolveCloneDest(r as RepoItem),
  reposRootInput,
  resolvedReposRoot,
  pickBranchForEnv: (env, branches) => pickBranchForEnv(env as EnvItem, branches),
  isServiceRole,
  makeEmptyRepo,
})
// 包一层:splitMonorepo 跑完会把 umbrella + N 个新 child 落进 repos[],
// 立刻给所有 repos 行(包括 umbrella 和 children)种 name 快照,
// 后续用户改 umbrella 名字时 cascade 才能跑得起来。
function splitMonorepo(idx: number) {
  splitMonorepoRaw(idx)
  for (const r of repos) snapshotRepoName(r)
}

// pickLocalRepoDir / resolveLocalRepoPath 已搬到 lib/useRepoScan.ts

// pickCloneTarget / resolveCloneDest 已搬到 lib/useRepoScan.ts

// hasRepoSource: 用户是否已经给这个仓库提供了来源线索(URL 或本地目录)。
// Why: 用户没填源时,"仓库名 / 自动识别 / 分支映射"三个下游块都推不出有意义的内容,
//      且能防 localStorage 老 draft 的残留露出来。
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

// branchOptionsFor: 给分支 <select> 提供选项列表。优先级:
//   1. 扫到的真实分支(repoBranchesMap[r.name])—— 本地已 clone 走 git for-each-ref
//   2. yaml env_branches 的 unique values —— 跨机器导入 + remote 模式没本地仓库时,
//      至少把同事 yaml 里声明过的分支当作可选项,无需 clone 就能选 / 确认
//   3. 都没有 —— 只显示当前已选值,模板会回落到 text input(给用户手敲)
// 当前值不在列表里时(用户手改过 yaml),先把当前值插到最前,保证下拉也能选回原值。
function branchOptionsFor(r: RepoItem, currentValue: string): string[] {
  const scanned = repoBranchesMap.value[r.name] || []
  if (scanned.length > 0) {
    if (currentValue && !scanned.includes(currentValue)) return [currentValue, ...scanned]
    return scanned
  }
  // 真分支列表空 → fallback yaml 已声明的分支(去重 + 排序)
  const yamlBranches = Array.from(new Set(
    Object.values(r.env_branches || {}).filter(b => b && b.trim()),
  )).sort()
  if (yamlBranches.length > 0) {
    if (currentValue && !yamlBranches.includes(currentValue)) return [currentValue, ...yamlBranches]
    return yamlBranches
  }
  return currentValue ? [currentValue] : []
}

// branchHasOptions: 模板 v-if 判定是否走 <select>(否则 <input>)。
// 跟 branchOptionsFor 同步:扫到真分支 ✓,或 yaml env_branches 有声明 ✓
function branchHasOptions(r: RepoItem): boolean {
  if ((repoBranchesMap.value[r.name] || []).length > 0) return true
  return Object.values(r.env_branches || {}).some(b => b && b.trim())
}

function setRepoSource(r: RepoItem, src: 'local' | 'remote') {
  if (r._source === src) return // 切到当前源不动,避免误清
  // umbrella 子模块代码已被 git submodule 拉到本地 <umbrella>/<parent_path>,
  // 不能独立 clone(没有自己的落点),source 锁死 'local'。要切到远程必须先解除
  // umbrella 关联(点 header 上 🌂 旁的 🗑)。这里防御性拦一下,即便 UI 那边
  // disabled 失效也不会切坏。
  if (src === 'remote' && r.parent_repo && r.parent_repo.trim()) return
  // 切源:把当前侧的所有源相关字段打包进 _sourceCache,切回来时原样恢复。
  // 之前的实现是无脑清空,用户来回切就丢数据 —— 实际场景常是"切过去看一眼对比就回来"。
  const oldSrc: 'local' | 'remote' = r._source === 'local' ? 'local' : 'remote'
  if (!r._sourceCache) r._sourceCache = {}
  r._sourceCache[oldSrc] = {
    url: r.url,
    name: r.name,
    _nameManual: !!r._nameManual,
    stack: r.stack,
    framework: r.framework,
    service_names: r.service_names,
    env_branches: { ...r.env_branches },
    _localPath: r._localPath,
    _cloneTarget: r._cloneTarget,
    _scanning: !!r._scanning,
    _scanError: r._scanError,
    _scanned: !!r._scanned,
    _scannedSource: r._scannedSource,
    _serviceEntries: r._serviceEntries ? { ...r._serviceEntries } : undefined,
  }
  r._source = src

  // 目标侧之前缓存过 → 原样恢复;没缓存过(首次切到这一侧) → 按"全新仓库"清空
  const restored = r._sourceCache[src]
  // umbrella 子模块(parent_repo 在场)的"身份"由 parent_repo + parent_path 锁定,
  // 切源只是"换个访问方式"(URL clone / 本地已有目录),name / stack / role / 服务名 /
  // 分支映射这些都不该丢。
  // umbrella 父行(被 child 引用)同理 —— URL 是 child path 解析的真源,绝不能被 source
  // 切换搞丢(否则 resolveLocalRepoPath 校验 r.url.trim() 失效,用户选啥目录都过)。
  // 普通独立仓库(parent_repo 空 + 没人引用本仓)切源仍可能是"换仓库"语义,保留旧行为
  // (全清,等用户重扫填回)。
  const isUmbrellaChild = !!(r.parent_repo && r.parent_repo.trim())
  const isUmbrellaParent = repos.some(rr => (rr.parent_repo || '').trim() === r.name.trim())
  // _fromYAML repo 跟 umbrella 一起走"身份保留"分支:URL 是 yaml 锚定的身份,切源
  // 不该清。否则用户切到 local 再切回 remote,URL 框就空了 → 用户以为系统忘了,得
  // 重填一遍 + 还得过 canonicalize 校验,体验差。
  const isFromYAML = !!r._fromYAML
  if (restored) {
    r.url = restored.url
    r.name = restored.name
    r._nameManual = restored._nameManual
    r.stack = restored.stack
    r.framework = restored.framework
    r.service_names = restored.service_names
    for (const eid of Object.keys(r.env_branches)) {
      r.env_branches[eid] = restored.env_branches[eid] || ''
    }
    r._localPath = restored._localPath
    r._cloneTarget = restored._cloneTarget
    r._scanning = restored._scanning
    r._scanError = restored._scanError
    r._scanned = restored._scanned
    r._scannedSource = restored._scannedSource
    r._serviceEntries = restored._serviceEntries
  } else if (isUmbrellaChild || isUmbrellaParent || isFromYAML) {
    // 身份保留分支(三种 case 都走):身份字段全留(URL / name / role / 服务名 / 分支
    // 映射),只清"源访问方式"相关 + 扫描态。
    //   - umbrella 子模块:身份由 parent_repo + parent_path 锁定
    //   - umbrella 父行:URL 是 child path 解析的真源,被 readonly 锁;切源时也不能丢
    //   - _fromYAML repo:URL 是 yaml 身份锚,切源不该清(用户切回 remote 应看到原 URL)
    // 切到 local 模式时 _cloneTarget 清掉(local 不 clone);切到 remote 模式 _localPath
    // 留着(子模块场景的预填值仍然有用;父行 / yaml repo 场景下用户挑过的副本仍是合法
    // 提示)。
    if (src === 'local') {
      r._cloneTarget = ''
    }
    r._scanned = false
    r._scannedSource = ''
    r._scanError = undefined
  } else {
    // 普通独立仓库首次切源:跟旧版同款清空逻辑(以前所有切源都走这条)
    const oldName = r.name
    if (oldName && oldName in repoBranchesMap.value) {
      delete repoBranchesMap.value[oldName]
    }
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
    if (src === 'local') {
      r._cloneTarget = ''
    } else {
      r._localPath = ''
    }
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
const ALL_SOURCE_TYPES = ['nacos', 'apollo', 'consul', 'kuboard', 'one2all', 'env-vars'] as const

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
  // ── 单→多过渡保留:之前 activeSourceTypes 只有 1 个源,服务靠 getServiceSource
  //   的默认 fallback 隐式归属那个源(map 里其实没记录)。一旦勾上第 2 个源,
  //   getServiceSource 在多源模式下默认值变空,所有"隐式勾选"的 chip 会突然全失活。
  //   这里在"切换前"把所有服务的隐式归属固化成显式 map 项,用户视觉上保持不变。
  const willBeMulti = checked && t !== 'none' && !enabledSourceTypes[t]
  const wasSingleSource = activeSourceTypes.value.length === 1 ? activeSourceTypes.value[0] : ''
  enabledSourceTypes[t] = checked
  const idx = enabledSourceOrder.indexOf(t)
  if (checked) {
    if (idx === -1) enabledSourceOrder.push(t) // 后勾的排到末尾
  } else {
    if (idx !== -1) enabledSourceOrder.splice(idx, 1)
  }
  if (willBeMulti && wasSingleSource && activeSourceTypes.value.length >= 2) {
    for (const svc of allServiceNames.value) {
      if (!(svc in serviceSourceMap)) {
        serviceSourceMap[svc] = wasSingleSource
      }
    }
  }
}

const sourceCreds = reactive<Record<string, SourceData>>(
  saved?.sourceCreds ?? {},
)
for (const t of ALL_SOURCE_TYPES) {
  if (!sourceCreds[t]) sourceCreds[t] = { creds: {} }
}
// 兜底:enabledSourceOrder / enabledSourceTypes 里可能含 ALL_SOURCE_TYPES 之外的 type
// (yaml 里写了未来 type / 老 schema 残留如 'kubernetes' / 'none' / 自定义),Step 6 模板
// 副源块迭代 activeSourceTypes 时会访问 `sourceCreds[t].creds.<env>`,t 没初始化就抛
// "Cannot read properties of undefined (reading 'creds')" → 整个 InitPage 渲染崩 → 白屏。
// 这里把 saved 里出现过的所有 type 都补出空骨架,模板永远拿到非 undefined。
if (saved?.enabledSourceOrder && Array.isArray(saved.enabledSourceOrder)) {
  for (const t of saved.enabledSourceOrder) {
    if (typeof t === 'string' && t && !sourceCreds[t]) sourceCreds[t] = { creds: {} }
  }
}
if (saved?.enabledSourceTypes && typeof saved.enabledSourceTypes === 'object') {
  for (const t of Object.keys(saved.enabledSourceTypes)) {
    if (t && !sourceCreds[t]) sourceCreds[t] = { creds: {} }
  }
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
// 完整 state + persistKuboardState + 5 个读 helper 收口在 lib/useKuboardState.ts;
// 下面两个 runner(runKuboardPreloadFromSource / runK8sRtPreload)还跟 sourceCreds /
// toolInputs / k8sRuntimeEnvLoc / autoPickK8sRtWorkloads 多块状态交织,留在 InitPage,
// 直接 mutate 本 composable 暴露的 kuboardStateByEnv 并显式调用 persistKuboardState。
const {
  kuboardStateByEnv,
  persistKuboardState,
  kuboardClustersOf,
  kuboardClusterCountOf,
  kuboardErrorOf,
  kuboardNamespacesFor,
  kuboardConfigMapsFor,
} = useKuboardState({
  savedKuboardState,
  draftKuboardState: saved?.kuboardStateByEnv,
})

// runKuboardPreloadFromSource / runKuboardPreload / autoMatchKuboardLocation /
// autoFillKuboardSelections 全收口在 lib/useKuboardPreload.ts。
// 实例化点必须在 sourceCreds + kuboardSvcMap + allServiceNames + getServiceSource +
// serviceMatchKeys + startsAtBoundary 之后,见下方"Step 5 Kuboard 预加载"段。

// k8s 运行时(可观测性)拉集群资源:先吃 obs k8s_runtime 自己的 URL+鉴权,
// 没填的话回落到 sourceCreds['kuboard'](同一个 Kuboard 实例时复用)。
// 拉到的资源直接写进 kuboardStateByEnv,跟配置源用同一棵 cluster→ns→cm 树。
async function runK8sRtPreload(envID: string) {
  // 检查 provider:one2all 不需要 kuboard 预加载,直接让用户填 cluster_id
  const rtProvider = (toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'provider')] || '').trim() || 'kuboard'
  if (rtProvider === 'one2all') {
    if (!k8sRuntimeEnvLoc[envID]) k8sRuntimeEnvLoc[envID] = { cluster: '', cluster_id: '', namespace: '' }
    await runOne2AllPreload(envID, 'k8s_runtime')
    return
  }
  if (!isDesktop()) {
    toast.error('Kuboard 拉取只在桌面 app 可用')
    return
  }
  const obsURL = (toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'url')] || '').trim()
  const obsAccessKey = (toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'access_key')] || '').trim()
  const obsUser = (toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'username')] || '').trim()
  const obsPass = toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'password')] || ''
  const obsAuthMode = (toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'auth_mode')] || '').trim()
  const fallback = sourceCreds['kuboard']?.creds?.[envID] || {}
  const url = obsURL || (fallback.url || '').trim()
  const accessKey = obsAccessKey || (fallback.access_key || '').trim()
  const username = obsUser || (fallback.username || '').trim()
  const password = obsPass || fallback.password || ''
  const clusterHint = (fallback.cluster_hint || '').trim() // Kuboard v3 必填(v4 忽略)
  // auth_mode 默认 access_key(没填过时按推荐项算,跟 isFieldHidden 同款兜底)
  const authMode = obsAuthMode || (fallback.auth_mode || '').trim() || 'access_key'
  if (!url) {
    toast.error(`${envID}: 先填 Kuboard URL(可观测性 K8s 运行时 字段)`)
    return
  }
  if (!accessKey && (!username || !password)) {
    toast.error(`${envID}: 鉴权填 API 访问凭证 或 用户名+密码`)
    return
  }
  // Kuboard v3 走 access-key 时鉴权靠 Cookie KuboardUsername,必须有用户名;v4 access-key
  // 不需要。前端无法可靠区分 v3/v4,故 access-key 模式下用户名空就拦截 —— 现场默认是 v3,
  // 漏填用户名会在运行时报 no-username。v4 用户可忽略此要求改用「用户名+密码」鉴权。
  if (authMode === 'access_key' && !username) {
    toast.error(`${envID}: Kuboard v3(API 访问凭证)需要填用户名;若是 v4 可改用「用户名+密码」鉴权`)
    return
  }
  kuboardStateByEnv[envID] = { status: 'loading' }
  try {
    const res = await kuboardListResources(url, username, password, accessKey, clusterHint)
    const clusters = (res.clusters || []).map(c => ({
      name: c.name,
      namespaces: (c.namespaces || []).map(n => ({ name: n.name, configmaps: n.configmaps || [] })),
    }))
    kuboardStateByEnv[envID] = { status: 'ok', clusters, notes: res.notes }
    persistKuboardState()
    toast.success(`${envID}: 拉到 ${clusters.length} 个集群`)
    // 拉到结果后给一组 env-aware 默认选择,免得用户每个 env 手点两次下拉。
    //
    // 集群挑选优先级(用户点"重新加载集群"是显式重评信号,允许覆盖含别 env 名的旧值):
    //   1) 名字含 envID(如 ccs-aws-dev 命中 env=dev)→ 最准
    //      - 若当前已选项不含 envID,被认为是"草稿里残留的错值",切到 env 命中
    //      - 当前已选项含 envID(用户手选过且名字匹配)→ 不动
    //   2) 当前为空 + 只有 1 个集群 → 直接定型
    //   3) 多集群无 env 信号 + 当前为空 → 不自动选(让用户决定,免得选错触发下游 deployment 全空)
    // namespace 同理。另外:cluster 切换后旧 namespace 可能在新 cluster 不存在 → 清掉再选。
    if (!k8sRuntimeEnvLoc[envID]) k8sRuntimeEnvLoc[envID] = { cluster: '', cluster_id: '', namespace: '' }
    const eloc = k8sRuntimeEnvLoc[envID]
    const envLower = envID.toLowerCase()
    if (clusters.length > 0) {
      // 草稿里的 cluster 在拉到的新列表里不存在了(集群重命名/账号换集群池)→ 当作空,免得卡死
      const stillExists = !!eloc.cluster && clusters.some(c => c.name === eloc.cluster)
      if (!stillExists) eloc.cluster = ''
      const envHit = clusters.find(c => c.name.toLowerCase().includes(envLower))
      const currentMatchesEnv = !!eloc.cluster && eloc.cluster.toLowerCase().includes(envLower)
      if (envHit && !currentMatchesEnv) {
        eloc.cluster = envHit.name
      } else if (!eloc.cluster && clusters.length === 1) {
        eloc.cluster = clusters[0].name
      }
    }
    if (eloc.cluster) {
      const c = clusters.find(c => c.name === eloc.cluster)
      if (c && c.namespaces.length > 0) {
        const nsExistsInCluster = !!eloc.namespace && c.namespaces.some(n => n.name === eloc.namespace)
        if (!nsExistsInCluster) eloc.namespace = '' // cluster 换了/旧 ns 失效,先清掉再重挑
        const envHit = c.namespaces.find(n => n.name.toLowerCase().includes(envLower))
        const currentMatchesEnv = !!eloc.namespace && eloc.namespace.toLowerCase().includes(envLower)
        if (envHit && !currentMatchesEnv) {
          eloc.namespace = envHit.name
        } else if (!eloc.namespace && c.namespaces.length === 1) {
          eloc.namespace = c.namespaces[0].name
        }
      } else if (c && c.namespaces.length === 0) {
        eloc.namespace = ''
      }
    }
    // cluster + namespace 都齐了 → 立即拉 deployment 列表,服务行 workload 下拉直接有内容
    if (eloc.cluster && eloc.namespace) {
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

// ── k8s 运行时(可观测性)Deployments 缓存 ───────────────────────────
// 跟 useKuboardState(集群+ns+cm 树)平行,(env, cluster, ns) → deployments[]
// 状态 + load 收口在 lib/useK8sRtWorkloads.ts。本变量声明实际挪到 toolInputs
// 之后(useK8sRtWorkloads 入参依赖它),见下方 "── 可观测性 toolInputs ──" 段。
// 这里只保留前向引用占位,函数体内引用 k8sRtWorkloadCache / k8sRtWorkloadKey /
// loadK8sRtWorkloads 都靠 lexical scope,等到调用时(user action / onMounted)才解析。
// autoPickK8sRtWorkloads 收口在 lib/useK8sRtAutoPick.ts;实例化点见下方 allServiceNames /
// ensureK8sRtSvcLoc 之后(由 loadK8sRtWorkloads 的 onLoaded 回调触发,本变量声明前的 lexical
// 引用都在函数体里,user action 触发时已 ready)。

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
      delete one2allSvcMap[k]
    }
  }
}

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

// 配置中心凭证字段定义:CredField interface 见 lib/credFields.ts。
// 每个 type 对应一组字段;envVar 拼出来的名字部署时由 Studio 注入到各 AI 平台的 MCP env。
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
      { key: 'access_key', label: 'API 访问凭证', secret: true, envVar: (e) => `KUBOARD_ACCESS_KEY_${e.toUpperCase()}`, placeholder: 'v3: 密钥ID.密钥(如 scyfw6txxw7i.x6t2…);v4: 单串 token', showWhen: { field: 'auth_mode', equals: 'access_key' } },
      // username 两种鉴权模式都显示:Kuboard v3 免账密(access-key)其实走 Cookie KuboardUsername=<user>,
      //   必须有用户名;v4 走 access-key 时可留空。故不设 showWhen,两模式都可填。
      { key: 'username', label: '用户名(v3 必填 / Cookie KuboardUsername)', secret: false, envVar: (e) => `KUBOARD_USER_${e.toUpperCase()}`, placeholder: 'Kuboard v3 走 access-key 时也要填;v4 可留空', optional: true },
      { key: 'password', label: '密码', secret: true, envVar: (e) => `KUBOARD_PASS_${e.toUpperCase()}`, showWhen: { field: 'auth_mode', equals: 'username_password' } },
      // cluster_hint:Kuboard v3 无法用 access-key 枚举集群,需先填集群名再"拉资源"(uiOnly,不写 yaml;
      // 真正落 yaml 的是下面 per-service 的 cluster 三联映射)。v4 留空(tree 自动列全部集群)。
      { key: 'cluster_hint', label: '集群名(仅 v3 需填)', secret: false, envVar: () => '', placeholder: '例如 my-cluster', uiOnly: true },
      { key: 'cluster', label: '集群名', secret: false, envVar: (e) => `KUBOARD_CLUSTER_${e.toUpperCase()}`, placeholder: 'default' },
      { key: 'namespace', label: 'Namespace', secret: false, envVar: (e) => `KUBOARD_NAMESPACE_${e.toUpperCase()}`, placeholder: 'default' },
      { key: 'configmap', label: 'ConfigMap 名称', secret: false, envVar: (e) => `KUBOARD_CONFIGMAP_${e.toUpperCase()}`, placeholder: 'app-config' },
    ],
    // one2all:单一 streamable-http MCP server,不分 env;凭据由 install 阶段写入 MCP headers。
    // cluster_id/namespace/configmap 是 per-service 在 one2allSvcMap 里,不在此 per-env 表单。
    one2all: [
      { key: 'mcp_url', label: 'MCP Server URL', secret: false, envVar: () => 'ONE2ALL_MCP_URL', placeholder: 'http://192.168.113.115:32633/one2all/api/v1/platform/public/mcp/xxx' },
      { key: 'token', label: 'Bearer Token', secret: true, envVar: () => 'ONE2ALL_TOKEN', placeholder: 'MCP 鉴权 token(从 one2all 平台获取)' },
    ],
    // env-vars:动态字段,Step 6 启用了哪些 data store 这里就出哪些
    'env-vars': envVarsFields,
  }
})
// ccKeyFor / svcKey / probeKey 收口在 lib/yamlShared.ts。

// 判断字段在当前 env 下是否要隐藏(基于 showWhen 条件)。
// getSibling 由调用方提供,主源走 ccCredInputs,副源走 sourceCreds[t].creds[env]。
function isFieldHidden(_t: string, _envID: string, f: CredField, getSibling: (key: string) => string): boolean {
  return isCredFieldHidden(f, getSibling)
}

// 可观测性字段隐藏判断:同 isFieldHidden,但走 toolInputs(obs:tool:env:field)读 sibling。
function isObsFieldHidden(toolKey: string, envID: string, f: CredField): boolean {
  return isFieldHidden('obs', envID, f, (k) => toolInputs[toolKeyFor('obs', toolKey, envID, k)] || '')
}

function displayObsField(toolKey: string, envID: string, f: CredField): CredField {
  return resolveCredFieldDisplay(f, (k) => toolInputs[toolKeyFor('obs', toolKey, envID, k)] || '')
}
// ccCredInputs:所有配置中心字段的当前输入值(key = ccKeyFor)。
// 流向:输入 → localStorage draft(持久) → troubleshooter.yaml → 部署时注入各 AI 平台的 MCP
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
// 双向 sync 防互相 retrigger:forward / reverse watch 任一边主动写时把 syncing 置 true,
// 对端 watch 起来看到 flag 直接 return,不再多跑一轮 O(types × envs × fields) 迭代。
// diff check 仍保留作 saftey net,防 Vue scheduler 异步合并丢 flag 的边角。
let syncing = false
function syncCcCredInputsFromSource() {
  if (syncing) return
  syncing = true
  try {
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
  } finally {
    syncing = false
  }
}
syncCcCredInputsFromSource()
watch(
  [sourceCreds, enabledSourceTypes],
  () => syncCcCredInputsFromSource(),
  { deep: true },
)
// 反向 sync:主源表单 v-model 写 ccCredInputs → 同步回 sourceCreds(yaml emit 真源)。
watch(
  ccCredInputs,
  () => {
    if (syncing) return
    syncing = true
    try {
      for (const k of Object.keys(ccCredInputs)) {
        const m = k.match(/^cc:([^:]+):([^:]+):(.+)$/)
        if (!m) continue
        const [, t, env, field] = m
        if (!sourceCreds[t]) sourceCreds[t] = { creds: {} }
        if (!sourceCreds[t].creds[env]) sourceCreds[t].creds[env] = {}
        const v = ccCredInputs[k] ?? ''
        if (sourceCreds[t].creds[env][field] !== v) sourceCreds[t].creds[env][field] = v
      }
    } finally {
      syncing = false
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
// 状态按 env 分开:类型 + 持久化收口在 lib/useCCHubState.ts(CCHubEnvState 也从那导出)。
const { ccHubStateByEnv } = useCCHubState(saved?.ccHubStateByEnv)

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

// one2all 配置源 per-service 映射:cluster_id / namespace / configmap。
// 跟 kuboardSvcMap 平行,key = svcKey(envID, svc) = "envID::svc"
type One2AllSvcLocator = { cluster_id: string; namespace: string; configmap: string }
const one2allSvcMap = reactive<Record<string, One2AllSvcLocator>>(saved?.one2allSvcMap ?? {})
function ensureOne2AllLoc(envID: string, svc: string): One2AllSvcLocator {
  const k = svcKey(envID, svc)
  if (!one2allSvcMap[k]) one2allSvcMap[k] = { cluster_id: '', namespace: '', configmap: '' }
  return one2allSvcMap[k]
}
function setOne2AllLoc(envID: string, svc: string, field: 'cluster_id' | 'namespace' | 'configmap', value: string) {
  const loc = ensureOne2AllLoc(envID, svc)
  loc[field] = value
  if (field === 'cluster_id') { loc.namespace = ''; loc.configmap = '' }
  if (field === 'namespace') { loc.configmap = '' }
}

// k8s 运行时(可观测性)专属。两层结构:
//   - 环境级:k8sRuntimeEnvLoc[env] = { cluster, namespace } —— 一个 env 对应一组 K8s 定位,
//     不强求每服务重选(常见情况:一个 env 一个 ns,所有服务都在里面)。
//   - 服务级:k8sRuntimeSvcMap[svcKey] = { workload, label_selector } —— 服务名→Deployment 名 + 自动
//     从 spec.selector.matchLabels 取的 label selector(routing skill 直接喂 KuboardListPods)。
type K8sRuntimeEnvLocator = { cluster: string; cluster_id: string; namespace: string }
type K8sRuntimeSvcLocator = { workload: string; label_selector: string }
const k8sRuntimeEnvLoc = reactive<Record<string, K8sRuntimeEnvLocator>>(saved?.k8sRuntimeEnvLoc ?? {})
const k8sRuntimeSvcMap = reactive<Record<string, K8sRuntimeSvcLocator>>(saved?.k8sRuntimeSvcMap ?? {})
function ensureK8sRtEnvLoc(envID: string): K8sRuntimeEnvLocator {
  if (!k8sRuntimeEnvLoc[envID]) k8sRuntimeEnvLoc[envID] = { cluster: '', cluster_id: '', namespace: '' }
  return k8sRuntimeEnvLoc[envID]
}
function ensureK8sRtSvcLoc(envID: string, svc: string): K8sRuntimeSvcLocator {
  const k = svcKey(envID, svc)
  if (!k8sRuntimeSvcMap[k]) k8sRuntimeSvcMap[k] = { workload: '', label_selector: '' }
  return k8sRuntimeSvcMap[k]
}
function setK8sRtEnvLoc(envID: string, field: 'cluster' | 'cluster_id' | 'namespace', value: string) {
  const loc = ensureK8sRtEnvLoc(envID)
  loc[field] = value
  if (field === 'cluster' || field === 'cluster_id') loc.namespace = ''
  // 换 cluster / ns 后,本 env 所有服务的 workload + selector 失效,清掉
  if (field === 'cluster' || field === 'cluster_id' || field === 'namespace') {
    for (const k of Object.keys(k8sRuntimeSvcMap)) {
      if (k.startsWith(envID + '::')) {
        k8sRuntimeSvcMap[k] = { workload: '', label_selector: '' }
      }
    }
  }
  // ns 选好后立即拉本 env 下所有服务可选的 deployment 列表
  const provider = (toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'provider')] || '').trim() || 'kuboard'
  if (field === 'namespace' && value && provider === 'one2all' && loc.cluster_id) {
    loadOne2AllK8sRtWorkloads(envID, loc.cluster_id, value)
  } else if (field === 'namespace' && value && loc.cluster) {
    loadK8sRtWorkloads(envID, loc.cluster, value)
  }
}
function setK8sRtSvcWorkload(envID: string, svc: string, workload: string) {
  const sloc = ensureK8sRtSvcLoc(envID, svc)
  sloc.workload = workload
  const eloc = ensureK8sRtEnvLoc(envID)
  const provider = (toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'provider')] || '').trim() || 'kuboard'
  const clusterKey = provider === 'one2all' ? eloc.cluster_id : eloc.cluster
  const list = k8sRtWorkloadsFor(envID, clusterKey, eloc.namespace)
  const dep = list.find(d => d.name === workload)
  sloc.label_selector = dep?.selector || ''
}

// 从 repos[].service_names 抽出去重的服务名列表 —— 下拉的每个 env 块都要遍历这一份。
// **角色非业务服务**(common-lib / docs / infra / frontend / mobile)的 repo 直接跳:
// 即便它的 service_names 残留了字符串(老 wizard 没清 / yaml 手编辑漏 / state 不一致),
// 这里 runtime 兜底,Step 6 服务清单不会出现 'docs 仓的名字当成服务' 的噪音。
// 跟 wizard syncServiceNamesWithRole + yamlImporter 的 role-based 清理三层联动,
// 任何一层漏了 runtime 这层都能兜住。
const allServiceNames = computed<string[]>(() => {
  const set = new Set<string>()
  for (const r of repos) {
    if (!isServiceRole(r.role)) continue
    for (const s of r.service_names.split(',').map(s => s.trim()).filter(Boolean)) {
      set.add(s)
    }
  }
  return Array.from(set)
})

// useK8sRtAutoPick 实例化:allServiceNames + ensureK8sRtSvcLoc 都已 ready;
// loadK8sRtWorkloads(还在下方)的 onLoaded 回调把 deployment 列表传过来,
// composable 内部 lexical scope 解析时 callback 已注入。
const { autoPickK8sRtWorkloads } = useK8sRtAutoPick({
  allServiceNames,
  ensureK8sRtSvcLoc: (envID, svc) => ensureK8sRtSvcLoc(envID, svc),
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


// serviceMatchKeys / startsAtBoundary 收口在 lib/serviceMatchHelpers.ts,
// 三家自动匹(useCCHubPreload / useKuboardPreload / useLokiLabels)+ InitPage 的
// autoPickK8sRtWorkloads 共用,避免飘开。

// applyImport 期间禁用 configCenterType watcher 的破坏性清空 —— 否则:
//   1. applyImport sync 段 ingest 多源时,configCenterType 在 "" → "nacos" 之间瞬变
//   2. 同步段结束后 watcher 异步触发,把刚反填的 envNamespaces / serviceConfigSel /
//      ccHubStateByEnv 全删了
//   3. 用户看到"导入失败、什么都没反填"的假象
// 用 importInProgress flag 包住整个 applyImport 调用,期间不清。
// 声明前置到 watcher / useImportCrossCheck 之前,保证它们都能闭包持有同一 ref。
const importInProgress = ref(false)

// ── Step 5 配置中心预加载 ─────────────────────────────────────────
// runCCHubPreload / loadConfigsForEnv / reloadEnvNamespace / buildPreloadPayload /
// autoMatchNamespace / autoMatchDataID / autoFillSelections / onNamespaceChanged /
// onDataIdChanged 全收口在 lib/useCCHubPreload.ts。
// crossCheckImportedConfigSource / crossCheckImportedKuboard / crossCheckImportedObservability /
// runImportCrossChecks 全收口在 lib/useImportCrossCheck.ts(实例化点在下方 useGrafanaDS 之后,
// 因为依赖 lokiAuthFor / getLokiMapping / scheduleObsProbe 等)。
const {
  autoMatchNamespace: _autoMatchNamespace,
  autoMatchDataID: _autoMatchDataID,
  autoFillSelections: _autoFillSelections,
  buildPreloadPayload,
  runCCHubPreload,
  loadConfigsForEnv: _loadConfigsForEnv,
  reloadEnvNamespace: _reloadEnvNamespace,
  onNamespaceChanged,
  onDataIdChanged,
} = useCCHubPreload({
  ccHubStateByEnv,
  ccCredInputs,
  getPrimaryConfigCenterType: () => configCenterType.value,
  envNamespaces,
  serviceConfigSel,
  serviceConfigGroup,
  allServiceNames,
  getServiceSource,
  namespacesFor,
  entriesForNamespace,
})
// 这几个 composable 里互相调用 / 由 onNamespaceChanged 内部触发,InitPage 模板没直接用
void _autoMatchNamespace; void _autoMatchDataID; void _autoFillSelections
void _loadConfigsForEnv; void _reloadEnvNamespace

// ── Step 5 Kuboard 预加载 ─────────────────────────────────────────
// runKuboardPreload / runKuboardPreloadFromSource / autoMatchKuboardLocation /
// autoFillKuboardSelections 全收口在 lib/useKuboardPreload.ts;runK8sRtPreload 跟
// 可观测性 toolInputs / k8sRuntimeEnvLoc 多块状态交织,留在 InitPage 直接 mutate
// 暴露的 kuboardStateByEnv + 调用 autoFillKuboardSelections。
const {
  autoMatchKuboardLocation: _autoMatchKuboardLocation,
  autoFillKuboardSelections: _autoFillKuboardSelections,
  runKuboardPreloadFromSource,
  runKuboardPreload,
} = useKuboardPreload({
  kuboardStateByEnv,
  persistKuboardState,
  sourceCreds,
  kuboardSvcMap,
  allServiceNames,
  getServiceSource,
})
// autoMatchKuboardLocation/autoFillKuboardSelections 由 composable 内部 runKuboardPreload* 调用,
// InitPage 模板直接用的是 runKuboardPreload / runKuboardPreloadFromSource 两个 runner
void _autoMatchKuboardLocation; void _autoFillKuboardSelections

// ── Step 5 one2all 预加载 ─────────────────────────────────────────
// one2all:通过 MCP JSON-RPC 调 platform_list_clusters/namespaces 拉资源树,
// 跟 kuboard 预加载平行,结果存入 one2allStateByEnv。

type One2AllJSState =
  | { status: 'idle' }
  | { status: 'loading' }
  | { status: 'ok'; clusters: O2ACluster[]; notes?: string[] }
  | { status: 'error'; error: string }

const one2allStateByEnv = reactive<Record<string, One2AllJSState>>({})

function one2allClustersOf(envID: string): O2ACluster[] {
  const s = one2allStateByEnv[envID]
  return s?.status === 'ok' ? s.clusters : []
}
function one2allClusterCountOf(envID: string): number {
  return one2allClustersOf(envID).length
}
function one2allErrorOf(envID: string): string {
  const s = one2allStateByEnv[envID]
  return s?.status === 'error' ? s.error.slice(0, 60) : ''
}
function one2allNamespacesFor(envID: string, clusterID: string): string[] {
  const s = one2allStateByEnv[envID]
  if (s?.status !== 'ok') return []
  const c = s.clusters.find(c => c.cluster_id === clusterID)
  return c ? c.namespaces.map(n => n.name) : []
}
function one2allConfigMapsFor(envID: string, clusterID: string, ns: string): string[] {
  const s = one2allStateByEnv[envID]
  if (s?.status !== 'ok') return []
  const c = s.clusters.find(c => c.cluster_id === clusterID)
  if (!c) return []
  const n = c.namespaces.find(n => n.name === ns)
  return n ? n.configmaps : []
}

function one2allCredsFor(envID: string): { mcpURL: string; token: string } {
  // 读 MCP URL + token:先看 one2all 源自己的 _shared_ creds,再 fallback 到 obs k8s_runtime。
  const shared = sourceCreds['one2all']?.creds?.['_shared_'] || {}
  let mcpURL = (shared['mcp_url'] || '').trim()
  let token = (shared['token'] || '').trim()
  if (!mcpURL) mcpURL = (toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'url')] || '').trim()
  if (!token) token = (toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'api_key')] || '').trim()
  return { mcpURL, token }
}

async function runOne2AllPreload(
  envID: string,
  purpose: One2AllPreloadPurpose = 'config_source',
  options: One2AllPreloadOptions = one2allPreloadOptionsForPurpose(purpose),
) {
  if (!isDesktop()) { toast.error('one2all 预加载仅桌面 app 支持'); return }
  const { mcpURL, token } = one2allCredsFor(envID)
  if (!mcpURL) { toast.error(`${envID}: 请先在「全局连接」填 MCP Server URL`); return }
  if (!token) { toast.error(`${envID}: 请先填 Bearer Token`); return }
  one2allStateByEnv[envID] = { status: 'loading' }
  try {
    const res = await one2allListResources(mcpURL, token, options.includeConfigMaps)
    one2allStateByEnv[envID] = { status: 'ok', clusters: res.clusters, notes: res.notes }
    toast.success(`${envID}: 拉到 ${res.clusters.length} 个集群`)
    // 自动选:env 名匹配的集群/namespace
    if (!k8sRuntimeEnvLoc[envID]) k8sRuntimeEnvLoc[envID] = { cluster: '', cluster_id: '', namespace: '' }
    const eloc = k8sRuntimeEnvLoc[envID]
    if (res.clusters.length === 1) {
      eloc.cluster = res.clusters[0].name
      eloc.cluster_id = res.clusters[0].cluster_id
      if (res.clusters[0].namespaces.length === 1) {
        eloc.namespace = res.clusters[0].namespaces[0].name
      }
    }
    // 也自动填 one2allSvcMap —— 如果只有一个集群+namespace,全服务统一用这个
    for (const svc of allServiceNames.value) {
      const loc = ensureOne2AllLoc(envID, svc)
      if (!loc.cluster_id) loc.cluster_id = eloc.cluster_id
      if (!loc.namespace) loc.namespace = eloc.namespace
    }
    if (options.loadDeployments && eloc.cluster_id && eloc.namespace) {
      loadOne2AllK8sRtWorkloads(envID, eloc.cluster_id, eloc.namespace)
    }
  } catch (e: any) {
    const msg = String(e?.message || e)
    one2allStateByEnv[envID] = { status: 'error', error: msg }
    toast.error(`${envID} one2all 加载失败: ${msg.slice(0, 80)}`)
  }
}


// ── Step 7: 可观测性 + 数据层 ──
const observabilityOptions = ['grafana', 'loki', 'prometheus', 'jaeger', 'elk', 'skywalking', 'tempo', 'k8s_runtime'] as const
const dataStoreOptions = ['redis', 'mongodb', 'elasticsearch', 'mysql', 'postgresql', 'kafka', 'rabbitmq', 'clickhouse'] as const

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
    key: 'k8s_runtime', label: 'K8s 运行时', description: '查 pod 状态 / events / 容器日志 / Deployment 滚动状态;支持 Kuboard v4 API 或 one2all MCP',
    fields: [
      {
        key: 'provider', label: 'Provider', secret: false, envVar: () => '',
        options: [
          { value: 'kuboard', label: 'Kuboard(v4 API)' },
          { value: 'one2all', label: 'one2all-remote MCP' },
        ],
        uiOnly: true,
      },
      {
        key: 'url', label: 'Kuboard URL', secret: false, envVar: (e) => `KUBOARD_URL_${e.toUpperCase()}`,
        placeholder: 'http://kuboard.example.com',
        envVarBy: {
          field: 'provider',
          values: {
            one2all: () => 'ONE2ALL_MCP_URL',
          },
        },
        labelBy: {
          field: 'provider',
          values: {
            kuboard: 'Kuboard URL',
            one2all: 'MCP Server URL',
          },
        },
        placeholderBy: {
          field: 'provider',
          values: {
            kuboard: 'http://kuboard.example.com',
            one2all: 'http://192.168.113.115:32633/one2all/api/v1/platform/public/mcp/xxx',
          },
        },
        optional: true,
      },
      { key: 'api_key', label: 'one2all Bearer Token', secret: true, envVar: () => 'ONE2ALL_TOKEN', placeholder: 'o2a_xxx', showWhen: { field: 'provider', equals: 'one2all' } },
      {
        key: 'auth_mode', label: '鉴权方式', secret: false, envVar: () => '',
        options: [
          { value: 'access_key', label: 'API 访问凭证(推荐 / 免账密)' },
          { value: 'username_password', label: '用户名 + 密码' },
        ],
        uiOnly: true,
        showWhen: { field: 'provider', equals: 'kuboard' },
      },
      {
        key: 'access_key', label: 'API 访问凭证', secret: true, envVar: (e) => `KUBOARD_ACCESS_KEY_${e.toUpperCase()}`,
        placeholder: 'v3: 密钥ID.密钥;v4: 单串 token',
        showWhenAll: [
          { field: 'provider', equals: 'kuboard' },
          { field: 'auth_mode', equals: 'access_key' },
        ],
      },
      {
        key: 'username', label: '用户名(v3 必填 / Cookie KuboardUsername)', secret: false, envVar: (e) => `KUBOARD_USER_${e.toUpperCase()}`,
        placeholder: 'Kuboard v3 走 access-key 时也要填;v4 可留空',
        optional: true,
        showWhenAll: [{ field: 'provider', equals: 'kuboard' }],
      },
      {
        key: 'password', label: '密码', secret: true, envVar: (e) => `KUBOARD_PASS_${e.toUpperCase()}`,
        showWhenAll: [
          { field: 'provider', equals: 'kuboard' },
          { field: 'auth_mode', equals: 'username_password' },
        ],
      },
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

// useK8sRtWorkloads 必须在 toolInputs 声明之后实例化(它会闭包持有这个 reactive)。
// 上方"k8s 运行时 Deployments 缓存"段对 k8sRtWorkloadCache / Key / loadK8sRtWorkloads
// 的引用都是函数体里的 lexical 查找,JS hoisting 保证 user action 触发时
// (一定晚于本行 init)能拿到值,不会撞 TDZ。
const { k8sRtWorkloadCache, k8sRtWorkloadKey, k8sRtWorkloadsFor, loadK8sRtWorkloads } = useK8sRtWorkloads({
  initialCache: saved?.k8sRtWorkloadCache,
  toolInputs,
  toolKeyFor,
  getKuboardCredsFor: (envID) => sourceCreds['kuboard']?.creds?.[envID],
  onLoaded: (envID, deployments) => autoPickK8sRtWorkloads(envID, deployments),
})

async function loadOne2AllK8sRtWorkloads(envID: string, clusterID: string, ns: string) {
  if (!clusterID || !ns) return
  const key = k8sRtWorkloadKey(envID, clusterID, ns)
  if (k8sRtWorkloadCache[key]?.status === 'loading') return
  const { mcpURL, token } = one2allCredsFor(envID)
  if (!mcpURL || !token) {
    k8sRtWorkloadCache[key] = { status: 'error', error: '缺 one2all MCP URL 或 Bearer Token' }
    return
  }
  k8sRtWorkloadCache[key] = { status: 'loading' }
  pushLog('cchub', 'info', `[${envID}] one2all k8s_runtime 拉 deployments: cluster_id=${clusterID}, ns=${ns}`, { envID })
  try {
    const res = await one2allListDeployments(mcpURL, token, clusterID, ns)
    const deployments = (res.deployments || []).map(d => ({ name: d.name, selector: d.selector || '' }))
    k8sRtWorkloadCache[key] = { status: 'ok', deployments }
    if (deployments.length > 0) {
      autoPickK8sRtWorkloads(envID, deployments)
    }
    pushLog('cchub', 'info', `[${envID}] one2all k8s_runtime: 拉到 ${deployments.length} 个 Deployment`, { envID })
  } catch (e: any) {
    const msg = String(e?.message || e)
    k8sRtWorkloadCache[key] = { status: 'error', error: msg }
    pushLog('cchub', 'error', `[${envID}] one2all k8s_runtime 列 deployments 失败: ${msg}`, { envID })
  }
}

// useGrafanaDS / useLokiLabels 实例化点挪到 useLokiMappingState 之后(它们要 lokiMappingByEnv +
// getLokiMapping)。见下方"Step 7 Loki 标签映射"段附近。
function clearToolFieldInput(k: string) {
  toolInputs[k] = ''
}

function toolSpecByKey(cat: 'obs' | 'ds', key: string): ToolSpec | undefined {
  const arr = cat === 'obs' ? OBS_TOOL_SPECS : DS_TOOL_SPECS
  return arr.find(s => s.key === key)
}

// ── Step 7 可观测性自动连通性测试 ─────────────────────────────────────
// 完整逻辑(每工具按 url 字段 + auth_mode 选鉴权方式 + 800ms 防抖)在 lib/useObsProbe.ts。
// 切到 Step 7 时主动重试的 triggerStep7Init 逻辑还跟 grafana DS / loki labels /
// k8s_runtime workload 三个独立子流程交织,留在下面 InitPage 里。
const { obsProbeResults, obsProbeKey, scheduleObsProbe } = useObsProbe(OBS_TOOL_SPECS, toolInputs, toolKeyFor)
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
  // 缓存已 'ok' 的(saved draft 恢复)跳过 fetch,但本 env 任一服务还没挑过 workload 时
  // 仍跑一次 autoPickK8sRtWorkloads —— 避免"切回页面 deployment 列表在但服务都'未部署'"。
  if (enabledObservability['k8s_runtime']) {
    for (const [envID, loc] of Object.entries(k8sRuntimeEnvLoc)) {
      const provider = (toolInputs[toolKeyFor('obs', 'k8s_runtime', envID, 'provider')] || '').trim() || 'kuboard'
      const clusterKey = provider === 'one2all' ? loc?.cluster_id : loc?.cluster
      if (!clusterKey || !loc?.namespace) continue
      const cacheKey = k8sRtWorkloadKey(envID, clusterKey, loc.namespace)
      const cached = k8sRtWorkloadCache[cacheKey]
      if (cached?.status === 'ok') {
        const anyUnmatched = allServiceNames.value.some(svc => {
          const sloc = k8sRuntimeSvcMap[svcKey(envID, svc)]
          return !sloc?.workload
        })
        if (anyUnmatched && cached.deployments.length > 0) {
          autoPickK8sRtWorkloads(envID, cached.deployments)
        }
        continue
      }
      if (provider === 'one2all') {
        loadOne2AllK8sRtWorkloads(envID, clusterKey, loc.namespace)
      } else {
        loadK8sRtWorkloads(envID, clusterKey, loc.namespace)
      }
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
  // loki labels 自动重拉:dsUID 已选(yaml 反填或之前已挑过)+ labels 空(quota fallback
  // 把 labels / *LabelValues 都剔了,或第一次拉过但浏览器重启清缓存)→ 拉一次 listLokiLabels,
  // 它内部会级联拉 envLabelValues / serviceLabelValues,UI 立刻有内容,不必用户手点"加载标签"。
  if (enabledObservability['loki']) {
    for (const env of environments) {
      if (!env.id) continue
      const lm = getLokiMapping(env.id)
      if (!lm.dsUID) continue
      if (lm.labelStatus === 'ok' || lm.labelStatus === 'loading') continue
      if (Array.isArray(lm.labels) && lm.labels.length > 0) continue
      // fire-and-forget;失败会自己写 lm.labelStatus='fail' + 日志
      loadLokiLabels(env.id).catch(() => { /* 静默 */ })
    }
  }
}
watch(() => currentStep.value, triggerStep7Init)
onMounted(() => triggerStep7Init(currentStep.value))

// ── Loki 标签映射(Step 7 grafana/loki 子区,per-env) ──────────────────
// 每个 env 独立维护 grafana 凭证 → datasource 列表 → labels → values → 选中映射,
// 因为 dev / prod 可能用不同 Grafana / Loki 实例,UID 和 label values 都不一样。
// envLabelKey / serviceLabelKey 也 per-env(虽然通常 namespace/app 会一致,但允许差异)。
// LokiMappingPerEnv 类型 + makeEmptyLokiMappingPerEnv + getLokiMapping 兜底初始化
// 全收口在 lib/useLokiMappingState.ts(同 useCCHubState 模式)。写侧 runners
// (loadLokiDatasources / loadLokiLabels / scheduleGrafanaDsAutoload / onEnvLabelKeyChanged
// 等)还跟 toolInputs / sourceCreds / pushLog 多块状态交织,留在下方,直接 mutate
// 暴露的 lokiMappingByEnv。
const { lokiMappingByEnv, getLokiMapping } = useLokiMappingState(
  saved?.lokiMappingByEnv,
)

// useGrafanaDS / useLokiLabels 必须在 useLokiMappingState 之后实例化(它们 closure 持有
// lokiMappingByEnv + getLokiMapping)。上方"Step 7 可观测性"段对它们的引用都是函数体内
// lexical 查找(用户操作触发,一定晚于本行 init),不会撞 TDZ。
const {
  grafanaDsUidByObsEnv,
  lokiAuthFor,
  obsGrafanaDsCandidates,
  loadLokiDatasources,
  scheduleGrafanaDsAutoload,
} = useGrafanaDS({
  toolInputs,
  toolKeyFor,
  enabledObservability,
  getLokiMapping,
  lokiMappingByEnv,
})
// 反填 saved.grafanaDsUidByObsEnv(composable 的 reactive 是空 init,这里把 saved 的值 merge 进去)
Object.assign(grafanaDsUidByObsEnv, saved?.grafanaDsUidByObsEnv ?? {})

const {
  loadLokiLabels,
  loadEnvLabelValues: _loadEnvLabelValues,
  loadServiceLabelValues: _loadServiceLabelValues,
  onEnvValueChanged,
  autoMatchLokiMapping: _autoMatchLokiMapping,
  onEnvLabelKeyChanged,
  onServiceLabelKeyChanged,
} = useLokiLabels({
  getLokiMapping,
  lokiAuthFor,
  allServiceNames,
})
// loadEnvLabelValues / loadServiceLabelValues / autoMatchLokiMapping 内部由 loadLokiLabels /
// onEnvValueChanged 调,InitPage 模板没直接用
void _loadEnvLabelValues; void _loadServiceLabelValues; void _autoMatchLokiMapping

// 每个 (obs, env) 的访问方式开关收口在 lib/useObsAccessMode.ts。
const { obsAccessModeMap, getObsAccessMode, setObsAccessMode } = useObsAccessMode({
  initialMap: saved?.obsAccessModeMap,
  enabledObservability,
})
// 导入 yaml 反填后的"真实端交叉校验"四件套(配置中心 / kuboard / 可观测性 + orchestrator):
//   - URL probe 失败  → toast.error + 日志;UI 上 obs probe 徽章自然呈红色
//   - URL probe 通过  → 静默,UI 上呈绿色
//   - grafana 额外:listGrafanaDatasources 对比反填的 UID,缺失的 toast + 日志
//   - k8s_runtime 额外:用 kuboard listResources 验 cluster/namespace 还在
// 失败容错:网络 / 凭证错时 toast 提醒,不阻塞 import,UI 仍可看 yaml 反填的值。
//
// 实现全收口在 lib/useImportCrossCheck.ts;此处 wire deps,由 applyImport 的 setTimeout
// 异步触发 runImportCrossChecks。
const {
  crossCheckImportedConfigSource: _crossCheckImportedConfigSource,
  crossCheckImportedKuboard: _crossCheckImportedKuboard,
  crossCheckImportedObservability: _crossCheckImportedObservability,
  runImportCrossChecks,
} = useImportCrossCheck({
  envNamespaces, serviceConfigSel, ccHubStateByEnv,
  kuboardSvcMap, kuboardStateByEnv, sourceCreds,
  enabledObservability, grafanaDsUidByObsEnv, k8sRuntimeEnvLoc, toolInputs,
  importInProgress,
  environments, activeSourceTypes, configCenterType,
  buildPreloadPayload, runCCHubPreload,
  runKuboardPreloadFromSource, persistKuboardState,
  scheduleObsProbe, lokiAuthFor, getLokiMapping,
  OBS_TOOL_SPECS, toolKeyFor, obsGrafanaDsKey,
})
// 三个 crossCheck* 由 composable 内部 runImportCrossChecks orchestrator 调,InitPage 模板没直接用
void _crossCheckImportedConfigSource; void _crossCheckImportedKuboard; void _crossCheckImportedObservability


// ── Step 7 数据层:"从配置中心读取" 自动识别 ────────────────────────
// 流程:
//  1. 拿 Step 5 挑的 envNamespaces + serviceConfigSel + serviceConfigGroup,构造要拉的 dataId 列表
//  2. 串行(避免并发轰炸配置中心) 调 fetchConfigContent 取原文
//  3. js-yaml 解析 / properties 解析;找顶级 key 匹 redis/mysql/mongodb/... 配置块
//  4. 命中则:enabledDataStores[type] = true、dsAutoFilled[type] = true,
//     toolInputs[ds:<type>:<env>:<field>] 填上从 yaml 抽出来的 url/dsn/...
// 没命中的保留原状(不覆盖用户已手填的字段)。

// scannedDS / dsScanState / dsAutoFilled / dsImportStatus / dsImportStats / dsProbeResults
// state + scanStateKey/Of + removeScannedDS + recomputeEnabledDataStoresFromScanned 全收口在
// lib/useDataStoreState.ts。autoImportDataStores / probeOneDS / probeAllAcrossEnvs 这三个
// runner 还跟 sourceCreds / preloadConfigCenter / fetchConfigContentBatch / probeDataStore
// 多块状态交织,留在下方 InitPage,直接 mutate 暴露的 reactive。
const {
  dsImportStatus, dsImportStats, dsAutoFilled,
  scannedDS, dsScanState, dsProbeResults,
  scanStateKey, scanStateOf,
  removeScannedDS,
  recomputeEnabledDataStoresFromScanned,
} = useDataStoreState(
  {
    scannedDS: saved?.scannedDS,
    dsScanState: saved?.dsScanState,
  },
  dataStoreOptions,
  enabledDataStores,
)
// Step 7 数据层 runners 全收口在 lib/useDataStoreScan.ts:
//   - autoImportDataStores  nacos/apollo/consul 凭证去重批拉 + kuboard 批拉 + DS_MATCHERS 识别
//   - probeOneDS            单条连通性测试
//   - probeAllAcrossEnvs    全环境一键测(跨 env 并发,env 内串行)
//   - canAutoImportDS       computed:Step 5 至少有一条服务能扫
// 纯解析(DS_MATCHERS / parseConfigContent / envFlatToRoot / parseProperties)在 lib/dataStoreParser.ts。
const {
  canAutoImportDS,
  probingByEnv, probingAll, probingAllStats,
  probeOneDS,
  probeAllAcrossEnvs,
  autoImportDataStores,
  enumerateDataStoreProbeTargets,
} = useDataStoreScan({
  scannedDS, dsScanState, dsProbeResults,
  dsImportStatus, dsImportStats, dsAutoFilled,
  enabledDataStores,
  scanStateKey,
  environments, allServiceNames, getServiceSource, svcKey,
  buildPreloadPayload, envNamespaces, serviceConfigSel, serviceConfigGroup,
  enabledSourceTypes, activeSourceTypes,
  ccCredInputs, ccKeyFor, sourceCreds, kuboardSvcMap, one2allSvcMap,
})
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


// ── Step 7: 输出目标 ──
// (历史上有 embedded 这个 target,后已下线;若 saved draft 里残留 enabledTargets.embedded
//  会被忽略,生成 yaml / 校验都不再考虑它)
const targetOptions: readonly TargetId[] = [Target.Openclaw, Target.ClaudeCode, Target.Cursor, Target.Codex]
const targetDescriptions: Record<TargetId, string> = {
  [Target.Openclaw]: 'OpenClaw agent(~/.openclaw/workspace/<workspace_name>/,OpenClaw 内选 agent 切换)',
  [Target.ClaudeCode]: 'Claude Code 用户级 subagent(~/.claude/agents/<name>.md,@<name> 调用)',
  [Target.Cursor]: 'Cursor 用户级 Custom Agent(~/.cursor/agents/<name>.md,AI 侧栏选用)',
  [Target.Codex]: 'OpenAI Codex CLI subagent(~/.codex/agents/<name>.toml,主 chat 里说 "spawn the <name> agent" 派生)',
}
const targetLabels: Record<TargetId, string> = {
  [Target.Openclaw]: 'OpenClaw',
  [Target.ClaudeCode]: 'Claude Code',
  [Target.Cursor]: 'Cursor IDE',
  [Target.Codex]: 'Codex CLI',
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
  if (t === Target.Openclaw) {
    if (openclawDetectStatus.value === 'idle' || openclawDetectStatus.value === 'loading') return null
    return openclawDetectStatus.value === 'ok'
  }
  if (!aitoolsResult.value) return null
  if (t === Target.ClaudeCode) return !!aitoolsResult.value.claude_code?.installed
  if (t === Target.Cursor) return !!aitoolsResult.value.cursor?.installed
  if (t === Target.Codex) return !!aitoolsResult.value.codex?.installed
  return null
}
// targetBadgeProps(t) — 把 4 家 target 异构的 detect 结果归一成 <TargetInstallBadge> 三个 prop。
// undefined detected = 完全不渲染 badge(对应原来 openclaw idle 不出徽章 + aitoolsResult 还没回的 ide 三家)。
function targetBadgeProps(t: string): { detected: boolean | null | undefined; versionText?: string; title?: string } {
  if (t === Target.Openclaw) {
    if (openclawDetectStatus.value === 'idle') return { detected: undefined }
    if (openclawDetectStatus.value === 'loading') return { detected: null, title: openclawDetectError.value || '' }
    return {
      detected: openclawDetectStatus.value === 'ok',
      versionText: openclawDetectStatus.value === 'ok' ? openclawVersion.value : undefined,
      title: openclawDetectError.value || '',
    }
  }
  if (!aitoolsResult.value) return { detected: undefined }
  const k = t === Target.ClaudeCode ? 'claude_code' : t === Target.Cursor ? 'cursor' : t === Target.Codex ? 'codex' : null
  if (!k) return { detected: undefined }
  const r = (aitoolsResult.value as any)[k] as { installed?: boolean; version?: string; note?: string; path?: string } | undefined
  if (!r) return { detected: undefined }
  return {
    detected: !!r.installed,
    versionText: r.installed ? (r.version || '?') : undefined,
    title: r.note || r.path || '',
  }
}
// 勾上 openclaw 时触发一次 openclaw 配置探测(还没跑过 / 上次失败都重试)。
// Why: 这段 watch / onMounted 必须放在 enabledTargets 声明之后 ——  放前面会 TDZ
//      触发 getter,读未初始化的 enabledTargets 报错。
watch(() => enabledTargets[Target.Openclaw], (on) => {
  if (on && openclawDetectStatus.value === 'idle') {
    runOpenClawDetect()
  }
})
// 进入向导即探一次 OpenClaw,跟 detectAITools (claude-code/cursor) 一起填卡片头徽章。
// 不依赖 enabledTargets[Target.Openclaw] —— 即使没勾,头部也能看到"v2026.4.9 / ⚠ 未检测到"。
onMounted(() => {
  if (openclawDetectStatus.value === 'idle') {
    runOpenClawDetect()
  }
})

// 探测出 openclaw 可用模型后,把"当前默认值不在该实例可用列表里"的情况自动改成 primary。
// 触发条件:status 变 ok + detected 非空 + 当前 targetModels.openclaw 不在 detected.id 集合内。
// 典型场景:
//   - 老 saved draft 留的 anthropic/claude-sonnet-4-6,但本机 openclaw 只配了 openai-codex/gpt-5.4
//     → 用户进 Step 2 看到默认是个不存在的模型,部署后 OpenClaw 报错"unknown model"
//   - 新用户首次进 + 本机刚装的 openclaw 没 anthropic 凭证 → 同上
// 用户已手挑过且仍在 detected 列表里 → 不覆盖;手挑了一个不在的 → 也不覆盖(尊重用户显式选择,
// 由部署期 OpenClaw 自己报错)。判定"用户手挑过":sourcedraft 里 target_models.openclaw
// 字段已存在(saved.agent.target_models.openclaw 非 undefined)→ 视为已挑过。
const openclawModelManuallyPicked = ref<boolean>(saved?.agent?.target_models?.openclaw !== undefined)
function onOpenclawModelChanged() {
  openclawModelManuallyPicked.value = true
}
watch([openclawDetectStatus, openclawDetectedModels], () => {
  if (openclawDetectStatus.value !== 'ok') return
  const detected = openclawDetectedModels.value
  if (!detected || detected.length === 0) return
  const ids = new Set(detected.map(m => m.id))
  if (ids.has(targetModels[Target.Openclaw])) return // 当前选择在 detected 里,不动
  if (openclawModelManuallyPicked.value && targetModels[Target.Openclaw]) {
    // 用户显式挑过一个不在 detected 的 model(企业网关 / 自部署 / 临时未注册),不强制覆盖
    return
  }
  // 默认到 primary;没标 primary 就用第一项
  const pick = detected.find(m => m.primary) ?? detected[0]
  targetModels[Target.Openclaw] = pick.id
  agent.model = pick.id // 同步 agent.model(yaml schema 必填兜底)
}, { flush: 'post' })

// 探测结果回填后,把"未装"的 target 自动取消勾选 ——
// 默认 enabledTargets 全 true 是"探测前先假设都装着",真探测出来未装就回退到未勾。
// 用户看到 badge 警告后可以再主动勾(checkbox 不 disabled);此 watch 只防"默认勾选 +
// 实际未装 = 静默装到孤儿目录"这种坏 case。
watch([aitoolsResult, openclawDetectStatus], () => {
  for (const t of targetOptions) {
    const det = targetDetectedInstalled(t)
    if (det === false && enabledTargets[t]) {
      enabledTargets[t] = false
    }
  }
}, { flush: 'post' })

// 环境列表变化 → 清掉不属于当前任何 env.id 的孤儿状态,防 draft 越攒越脏。
// Why: 用户改 env.id(重命名)/ removeEnv 时旧 envID 残留在各 map 里,
//      kuboardStateByEnv 等可能撑出 MB 级体积,挤爆 localStorage 配额。
watch(() => environments.map(e => e.id).join('|'), () => {
  const valid = new Set(environments.map(e => e.id).filter(Boolean))
  // 所有 per-env map:key = env.id
  for (const k of Object.keys(envNamespaces))        if (!valid.has(k)) delete envNamespaces[k]
  for (const k of Object.keys(ccHubStateByEnv))      if (!valid.has(k)) delete ccHubStateByEnv[k]
  for (const k of Object.keys(scannedDS))            if (!valid.has(k)) delete scannedDS[k]
  for (const k of Object.keys(kuboardStateByEnv))    if (!valid.has(k)) delete kuboardStateByEnv[k]
  for (const k of Object.keys(k8sRuntimeEnvLoc))     if (!valid.has(k)) delete k8sRuntimeEnvLoc[k]
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
  for (const k of Object.keys(k8sRuntimeSvcMap)) {
    const env = k.split('::')[0]; if (!valid.has(env)) delete k8sRuntimeSvcMap[k]
  }
  for (const k of Object.keys(kuboardSvcMap)) {
    const env = k.split('::')[0]; if (!valid.has(env)) delete kuboardSvcMap[k]
  }
  for (const k of Object.keys(one2allSvcMap)) {
    const env = k.split('::')[0]; if (!valid.has(env)) delete one2allSvcMap[k]
  }
  // k8sRtWorkloadCache key 形如 "<envID>::<cluster>::<ns>"
  for (const k of Object.keys(k8sRtWorkloadCache)) {
    const env = k.split('::')[0]; if (!valid.has(env)) delete k8sRtWorkloadCache[k]
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

// 切配置源类型时清空 Step 5/7 状态:逻辑收口在 lib/useSourceTypeReset.ts。
// 凭证输入(ccCredInputs)按 type 前缀分 key,保留不清,切回旧 type 还能复用。
// importInProgress 期间禁清(避免 applyImport reset → ingest 多源时把刚反填的 state 抹掉)。
useSourceTypeReset({
  configCenterType, importInProgress,
  envNamespaces, serviceConfigSel, serviceConfigGroup, ccHubStateByEnv,
  scannedDS, dsScanState, dsAutoFilled, dsImportStatus, dsImportStats,
})

// 自动保存草稿用的"上次保存时间"。Why: InitPage 不进 keep-alive,每次 mount
// 重建,有 saved 草稿时 badge 应直接显示"✓ 自动保存"——挂载时占位 Date.now,
// 用户改字段后由 auto-save watch 覆盖成真实时间。
const lastSavedAt = ref<number | null>(saved ? Date.now() : null)
// autosave debounce:用户连续输入时,reactive 每个字符都触发本 watch,deep: true 会
// 把整个 30+ key 的 reactive 树 stringify 一遍 + 写 localStorage(已撞过 quota
// 走 slim 兜底)。300ms 防抖把"输入字符串"期间的写次数压到 1 次,onUnmounted
// flush 保证页面切走时 pending payload 落地不丢。
let persistTimer: ReturnType<typeof setTimeout> | null = null
let lastPersistVal: any = null
function flushPersist() {
  if (persistTimer != null) {
    clearTimeout(persistTimer)
    persistTimer = null
  }
  if (lastPersistVal == null) return
  const val = lastPersistVal
  lastPersistVal = null
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
    // 把 Loki 的 labels / values / dsList 这类列表型大数据剥掉,只留 dsUID + 选好的 mapping。
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
}

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
    one2allSvcMap,
    // 可观测性 k8s_runtime:env→cluster/namespace + (env,svc)→workload/label_selector 两层映射
    k8sRuntimeEnvLoc,
    k8sRuntimeSvcMap,
    // k8s_runtime per-(env, cluster, namespace) deployments 列表缓存 —— 跨会话保留 ok 项,
    // 切回 Step 7 时下拉直接有内容,不用等 onMounted 异步重拉。loading/error 不存。
    k8sRtWorkloadCache,
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
    idManualOverride: idManualOverride.value,
    openclawInstallDir: openclawInstallDir.value,
  }),
  (val) => {
    lastPersistVal = val
    if (persistTimer != null) clearTimeout(persistTimer)
    persistTimer = setTimeout(flushPersist, 300)
  },
  { deep: true }
)
onUnmounted(flushPersist)

// "X 秒前"计时 —— 让徽章文案能随时间流动,用户看得到 Auto-save 真的在跑。
const nowTick = ref(Date.now())
const nowTickTimer = setInterval(() => { nowTick.value = Date.now() }, 1000)
onUnmounted(() => clearInterval(nowTickTimer))
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
  for (const k of Object.keys(one2allSvcMap)) delete one2allSvcMap[k]
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

// ── Import existing troubleshooter.yaml into the wizard ──
// 整条入口闭环(对话框状态 + open/close/file-pick + applyImport)收口在 lib/useImportFlow.ts。
// 反填主体仍在 lib/yamlImporter.ts(applyParsedYAMLToWizardState),通过 buildContext callback
// 把 InitPage 闭包里的 30+ reactive / helper / bridge 函数打包成一个 ApplyImportContext 传进去。
// Vue 3 reactive proxy 跨组件边界仍然工作,lib 内直接 obj[k]=v 等价于 InitPage 写 reactive。
const {
  showImportDialog, importText, importError,
  openImportDialog, closeImportDialog,
  handleImportFile, pickImportYAMLNative,
  applyImport,
} = useImportFlow({
  importInProgress,
  currentStep,
  runImportCrossChecks,
  buildContext: (): ApplyImportContext => ({
    system, agent, targetModels,
    environments, repos,
    enabledSourceTypes, enabledSourceOrder, sourceCreds,
    serviceSourceMap, ccCredInputs,
    envNamespaces, serviceConfigSel, serviceConfigGroup, ccHubStateByEnv,
    enabledObservability, toolInputs, obsAccessModeMap, grafanaDsUidByObsEnv,
    k8sRuntimeEnvLoc, k8sRuntimeSvcMap,
    scannedDS, enabledDataStores, dsAutoFilled, dsScanState,
    ALL_SOURCE_TYPES,
    CC_FIELDS_BY_TYPE: CC_FIELDS_BY_TYPE.value,
    allServiceNames: allServiceNames.value,
    ensureKuboardLoc, ensureOne2AllLoc, getLokiMapping,
    ccKeyFor, svcKey, scanStateKey, toolKeyFor,
    obsAccessKey, obsGrafanaDsKey, toolSpecByKey,
    pickBranchForEnv,
    getRepoPathsForSystem, listBranchesForRepo,
    setRepoBranches: (name, branches) => { repoBranchesMap.value[name] = branches },
  }),
})


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

// 启动时给所有已存在的 repos 行打 name 快照,后续 onRepoNameInput 改名时
// 才能算出 oldName → 级联更新 child.parent_repo
onMounted(() => {
  for (const r of repos) snapshotRepoName(r)
})

// ── Skills whitelist derivation ──
// 数据层 enabledDataStores 的 key 跟 skill 目录名不是 1:1 对应:特例 elasticsearch → es-runtime-query。
// 其他类型(redis/mongodb/mysql/postgresql/kafka/rabbitmq/clickhouse)就是 <key>-runtime-query。
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
// 整个 generateYAML 主体在 lib/yamlGenerator.ts;此处只负责把 setup() 里的 25+
// 个 closure deps 打包成 YAMLGenContext 传进去。
function generateYAML(): string {
  const ctx: YAMLGenContext = {
    system, agent, agentNameDefault: agentNameDefault.value,
    targetModels, enabledTargets, enabledObservability,
    environments: environments.map(e => ({ id: e.id, api_domain: e.api_domain, web_domain: e.web_domain, is_prod: e.is_prod })),
    repos: repos.map(r => ({
      name: r.name, url: r.url, stack: r.stack, framework: r.framework,
      role: r.role, sub_path: r.sub_path,
      parent_repo: r.parent_repo,
      parent_path: r.parent_path,
      service_names: r.service_names,
      env_branches: r.env_branches,
      _serviceEntries: r._serviceEntries,
    })),
    sourceCreds, serviceConfigSel, serviceConfigGroup, envNamespaces,
    kuboardSvcMap, one2allSvcMap, lokiMappingByEnv, toolInputs, grafanaDsUidByObsEnv,
    k8sRuntimeEnvLoc, k8sRuntimeSvcMap, scannedDS,
    activeSourceTypes: activeSourceTypes.value,
    allServiceNames: allServiceNames.value,
    isMultiSource: isMultiSource.value,
    targetOptions, modelConsumingTargets,
    OBS_TOOL_SPECS, CC_FIELDS_BY_TYPE: CC_FIELDS_BY_TYPE.value,
    normalizeDomain, getServiceSource, isFieldHidden, isObsFieldHidden,
    getObsAccessMode, obsGrafanaDsKey, svcKey, toolKeyFor, toolSpecByKey,
    deriveSkillsWhitelist, recomputeEnabledDataStoresFromScanned,
  }
  return libGenerateYAML(ctx)
}

// ── Step 4 仓库扫描(useRepoScan) ──────────────────────────────────
// 必须在 generateYAML 之后实例化:scanSingleRepo 跑 bridgeAnalyzeV2 前要拿当前 yaml,
// 通过 generateYAML callback 闭包持有 InitPage 25+ 个 reactive。上方"Step 4"段对
// 这些函数的引用都在函数体里(scanSingleRepo / pickLocalRepoDir / refreshRoleHint /
// refreshSubmoduleHints / pickCloneTarget / resolveCloneDest / resolveLocalRepoPath
// 都是用户操作触发,一定晚于本行 init),lexical scope 自然解析。
const {
  resolveCloneDest,
  refreshRoleHint,
  pickCloneTarget,
  pickLocalRepoDir,
  scanSingleRepo,
} = useRepoScan({
  repoBranchesMap,
  environments,
  repos,
  reposRootInput,
  resolvedReposRoot,
  // pickBranchForEnv / isServiceRole 的 env 参数类型 InitPage 用的是含 api_domain
  // 等更多字段的 EnvItem,composable 只用到 id + is_prod 子集 —— 入参用 any 桥过去,
  // 比把 RepoScanEnv 映射到 EnvItem 写一堆 cast 干净。
  pickBranchForEnv: (env, branches) => pickBranchForEnv(env as EnvItem, branches),
  isServiceRole,
  deriveRepoName,
  generateYAML,
})

// ── 校验 ─────────────────────────────────────────────────────────────
// computed:每次字段变动立刻重算 errors,模板按 key 显示红框,按钮按 size 决定 disabled。
// validate 规则:
//   Step 1:system.id / name(workspace_name / model 移到 Step 2)
//   Step 2:agent.name、≥1 个 target、勾 openclaw 要 workspace_name、
//          勾 openclaw/embedded 要对应 model
//   Step 3:env.id + api_domain
//   Step 4:每个 repo:name + (remote 要 url,local 要 _localPath)
//   Step 5:所选 type 的 non-optional 字段 per env 必填(optional 的可以留空让 install.sh 问)
//   Step 6/7:无硬校验

// labelForErrorKey 实现在 lib/yamlValidator.ts;此处 thin shim 注入 repos 引用。
function labelForErrorKey(k: string): string {
  return libLabelForErrorKey(k, repos)
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
// computeStepErrors 主体在 lib/yamlValidator.ts;此处 thin shim 注入 reactive deps。
function computeStepErrors(): Set<string> {
  const ctx: ValidatorContext = {
    step: currentStep.value,
    system, agent,
    enabledTargets, targetModels,
    anyTargetSelected: anyTargetSelected.value,
    environments: environments.map(e => ({ id: e.id, api_domain: e.api_domain, is_prod: e.is_prod })),
    repos: repos.map(r => ({
      name: r.name, url: r.url,
      _source: r._source, _localPath: r._localPath, _cloneTarget: r._cloneTarget,
      _fromYAML: r._fromYAML, _yamlOriginalURL: r._yamlOriginalURL,
    })),
    isMultiSource: isMultiSource.value,
    allServiceNames: allServiceNames.value,
    activeSourceTypes: activeSourceTypes.value,
    CC_FIELDS_BY_TYPE: CC_FIELDS_BY_TYPE.value,
    ccCredInputs, sourceCreds, envNamespaces, serviceConfigSel,
    kuboardStateByEnv, kuboardSvcMap, one2allStateByEnv, one2allSvcMap, ccHubStateByEnv, dsProbeResults, dsScanState,
    isFieldHidden, getServiceSource, enumerateDataStoreProbeTargets,
    enabledObservability,
  }
  return libComputeStepErrors(ctx)
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
  await copyToClipboard(yamlOutput.value)
  copySuccess.value = true
  setTimeout(() => (copySuccess.value = false), 2000)
}

async function downloadYAML() {
  const filename = 'troubleshooter.yaml'
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

// ── Step 10 一键部署 ──
// runOneClickDeploy / buildOpenclawCreds / installEnvVarName / deploySummary /
// targetDeployPaths / targetDeployPathHints 全收口在 lib/useDeployFlow.ts。
// 实例化点必须在 yamlOutput / resolveCloneDest / resolvedReposRoot 之后(它们在 useRepoScan /
// useReposRoot 暴露,本行之前已 ready)。
const {
  deployLoading,
  deployError,
  deploySummary,
  deployProgressLine,
  targetDeployPaths,
  targetDeployPathHints,
  runOneClickDeploy,
} = useDeployFlow({
  agent, system, targetModels,
  enabledTargets, targetOptions, targetLabels, homeDir,
  activeSourceTypes, sourceCreds, environments, enabledDataStores,
  enabledObservability, toolInputs, OBS_TOOL_SPECS, DS_TOOL_SPECS,
  toolKeyFor, isObsFieldHidden, displayObsField,
  yamlOutput, reposRootInput, resolvedReposRoot, repos, resolveCloneDest,
  storageKey: STORAGE_KEY, router,
})

const configTypeOptions = ['nacos', 'apollo', 'consul', 'env-vars', 'kuboard', 'one2all', 'none']

const configTypeDescriptions: Record<string, string> = {
  nacos: 'Nacos — 配置与服务发现中心(阿里巴巴开源)',
  apollo: 'Apollo — 分布式配置中心(携程开源)',
  consul: 'Consul KV — HashiCorp 键值存储',
  'env-vars': '环境变量 / .env 文件 — 不使用远程配置中心',
  kuboard: 'Kuboard — 通过 Kuboard 后台读 K8s ConfigMap,无需 kubeconfig',
  one2all: 'one2all-remote — 通过 MCP 读 ConfigMap/Secret + K8s 运行时,单 token 鉴权',
  none: '不使用任何配置源',
}

// 共享上下文:把 2+ Step 子组件都用的高频 reactive 引用 + helper 一次性 provide,
// 子组件 inject('wizard') 后直接 wizard.X,不再每个 prop 单独透传。
provide(WizardStoreKey, {
  environments,
  // allServiceNames 是 ComputedRef,reactive proxy 不能直接放 — 这里取 .value,
  // 子组件每次访问时仍然走 reactive(因为 environments + repos 是 reactive 源)。
  get allServiceNames() { return allServiceNames.value },
  kuboardStateByEnv,
  hasError,
  svcKey,
  isRevealed,
  toggleReveal,
  kuboardClustersOf,
  kuboardClusterCountOf,
  kuboardErrorOf,
  kuboardNamespacesFor,
  kuboardConfigMapsFor,
  one2allStateByEnv,
  one2allClustersOf,
  one2allClusterCountOf,
  one2allErrorOf,
  one2allNamespacesFor,
  one2allConfigMapsFor,
})
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
          <p class="subtitle">通过可视化表单生成 troubleshooter.yaml 配置文件(草稿会自动保存到本地)</p>
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
          <span>导入已有 troubleshooter.yaml</span>
          <button class="btn-icon close" @click="closeImportDialog">&times;</button>
        </div>
        <div class="modal-body">
          <p class="help-text" style="margin-bottom: 10px;">
            上传或粘贴现有 troubleshooter.yaml 内容,字段会自动反填到各步骤。
          </p>
          <!-- 桌面 app 走 osascript 弹文件选择器(避开 macOS 26 上 WKWebView 原生 panel 闪退);
               浏览器模式回退到 HTML5 input(type=file)。 -->
          <button v-if="isDesktop()" type="button" class="btn file-label" @click="pickImportYAMLNative">
            选择文件...
          </button>
          <label v-else class="btn file-label">
            选择文件...
            <input type="file" accept=".yaml,.yml" @change="handleImportFile" style="display:none" />
          </label>
          <textarea
            v-model="importText"
            rows="14"
            placeholder="或直接粘贴 troubleshooter.yaml 的 YAML 内容…"
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
        <p><strong>本向导帮助你快速生成 troubleshooter.yaml 配置文件</strong></p>
        <p>troubleshooter.yaml 描述你的系统架构(仓库、环境、配置中心、基础组件),tshoot 据此生成并部署定制化的 AI 排障机器人</p>
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
    <WelcomeStep
      v-if="currentStep === 1"
      @start="goToStep(2)"
      @import="openImportDialog"
    />

    <SystemBasicInfoStep
      v-if="currentStep === 2"
      v-model:id-manual-override="idManualOverride"
      :system="system"
      :has-error="hasError"
    />

    <!-- Step 2 -->
    <BotIdentityStep
      v-if="currentStep === 3"
      :agent="agent"
      :agent-name-default="agentNameDefault"
      :agent-id-default="agentIdDefault"
      :has-error="hasError"
      :target-options="targetOptions"
      :target-labels="targetLabels"
      :target-descriptions="targetDescriptions"
      :enabled-targets="enabledTargets"
      :target-detected-installed="targetDetectedInstalled"
      :target-badge-props="targetBadgeProps"
      :target-deploy-paths="targetDeployPaths"
      :target-deploy-path-hints="targetDeployPathHints"
      :any-target-selected="anyTargetSelected"
      :target-models="targetModels"
      :openclaw-detect-status="openclawDetectStatus"
      :openclaw-detect-error="openclawDetectError"
      :openclaw-detected-models="openclawDetectedModels"
      :openclaw-resolved-dir="openclawResolvedDir"
      :openclaw-version="openclawVersion"
      :openclaw-auth-providers="openclawAuthProviders"
      :openclaw-install-dir="openclawInstallDir"
      :aitools-refreshing="aitoolsRefreshing"
      @refresh-a-i-tools="manualRefreshAITools"
      @pick-open-claw-install-dir="pickOpenClawInstallDir"
      @run-open-claw-detect="runOpenClawDetect"
      @model-change="onModelChange"
    />

    <EnvListStep
      v-if="currentStep === 4"
      :environments="environments"
      :url-probe-results="urlProbeResults"
      :url-probe-key="urlProbeKey"
      :has-error="hasError"
      @probe="(envIdx, kind, url) => scheduleURLProbe(envIdx, kind, url)"
      @remove="removeEnv"
      @add="addEnv"
    />

    <!-- Step 4 -->
    <div v-if="currentStep === 5" class="card lg">
      <h2>代码仓库</h2>
      <p class="help-text">
        填业务的代码仓库:可以选本地已 clone 的目录,也可以填远程 URL 让 Studio 帮你拉下来。扫描后会自动识别技术栈、服务名和分支。
      </p>

      <GlobalReposRootBlock
        :repos-root-input="reposRootInput"
        :resolved-repos-root="resolvedReposRoot"
        :global-default-repos-root="globalDefaultReposRoot"
        :display-path="displayPath"
        @pick="pickReposRoot"
        @save="saveAsGlobalDefault"
      />

      <RepoListItem
        v-for="(repo, i) in repos"
        :key="i"
        :repo="repo"
        :index="i"
        :environments="environments"
        :can-remove="isRepoDeletable(repo)"
        :umbrella-children-count="countUmbrellaChildren(repos, repo.name)"
        :svc-add-inputs="svcAddInputs"
        :repo-branches-map="repoBranchesMap"
        :repos-root-input="reposRootInput"
        :resolved-repos-root="resolvedReposRoot"
        :has-error="hasError"
        :has-repo-source="hasRepoSource"
        :display-path="displayPath"
        :resolve-clone-dest="resolveCloneDest"
        :submodule-path-for="submodulePathFor"
        :is-git-submodules-hints="isGitSubmodulesHints"
        :is-service-role="isServiceRole"
        :qualify-service-name="qualifyServiceName"
        :repo-service-names-list="repoServiceNamesList"
        :branch-has-options="branchHasOptions"
        :branch-options-for="branchOptionsFor"
        :picked-submodule-count="pickedSubmoduleCount"
        :is-submodule-already-split="isSubmoduleAlreadySplit"
        @remove="removeRepo"
        @set-source="(r, source) => setRepoSource(r, source)"
        @url-input="(r) => onRepoUrlInput(r)"
        @name-input="(r) => onRepoNameInput(r)"
        @sub-path-input="(r) => onRepoSubPathInput(r)"
        @pick-clone-target="(r) => pickCloneTarget(r)"
        @pick-local-repo-dir="(r) => pickLocalRepoDir(r)"
        @scan-single-repo="(r) => scanSingleRepo(r)"
        @toggle-submodule-pick="(r, sub, checked) => toggleSubmodulePick(r, sub, checked)"
        @split-monorepo="(idx) => splitMonorepo(idx)"
        @merge-monorepo-into-services="(idx) => mergeMonorepoIntoServices(idx)"
        @sync-service-names-with-role="(r) => syncServiceNamesWithRole(r)"
        @apply-role-hint="(r) => applyRoleHint(r)"
        @remove-service-name="(r, svc) => removeServiceName(r, svc)"
        @add-service-name="(r, idx) => addServiceName(r, idx)"
      />
      <button class="btn" @click="addRepo">+ 添加仓库</button>
    </div>

    <!-- Step 5 -->
    <ConfigSourceStep
      v-if="currentStep === 6"
      :config-type-options="configTypeOptions"
      :config-type-descriptions="configTypeDescriptions"
      :enabled-source-types="enabledSourceTypes"
      :active-source-types="activeSourceTypes"
      :is-multi-source="isMultiSource"
      :config-center-type="configCenterType"
      :cc-fields-by-type="CC_FIELDS_BY_TYPE"
      :cc-cred-inputs="ccCredInputs"
      :source-creds="sourceCreds"
      :cc-hub-state-by-env="ccHubStateByEnv"
      :env-namespaces="envNamespaces"
      :service-config-sel="serviceConfigSel"
      :service-config-group="serviceConfigGroup"
      :kuboard-svc-map="kuboardSvcMap"
      :one2all-svc-map="one2allSvcMap"
      :cc-key-for="ccKeyFor"
      :is-field-hidden="isFieldHidden"
      :env-scanned="envScanned"
      :namespaces-for="namespacesFor"
      :entries-for-namespace="entriesForNamespace"
      :get-service-source="getServiceSource"
      @toggle-source-type="toggleSourceType"
      @update-cred="(k, v) => (ccCredInputs[k] = v)"
      @clear-cred="clearCCFieldInput"
      @run-kuboard-preload="runKuboardPreload"
      @run-one2-all-preload="(envID, purpose) => runOne2AllPreload(envID, purpose)"
      @run-c-c-hub-preload="runCCHubPreload"
      @set-service-source="(svc, src) => setServiceSource(svc, src)"
      @namespace-changed="onNamespaceChanged"
      @data-id-changed="onDataIdChanged"
      @set-kuboard-loc="setKuboardLoc"
      @set-one2-all-loc="setOne2AllLoc"
      @preload-kuboard-from-source="runKuboardPreloadFromSource"
    />

    <!-- Step 7:可观测性 -->
    <ObservabilityStep
      v-if="currentStep === 8"
      :obs-tool-specs="OBS_TOOL_SPECS"
      :enabled-observability="enabledObservability"
      :obs-probe-results="obsProbeResults"
      :tool-inputs="toolInputs"
      :is-obs-field-hidden="isObsFieldHidden"
      :display-obs-field="displayObsField"
      :tool-key-for="toolKeyFor"
      :obs-probe-key="obsProbeKey"
      :get-obs-access-mode="getObsAccessMode"
      :k8s-runtime-env-loc="k8sRuntimeEnvLoc"
      :k8s-runtime-svc-map="k8sRuntimeSvcMap"
      :k8s-rt-workload-cache="k8sRtWorkloadCache"
      :one2all-state-by-env="one2allStateByEnv"
      :k8s-rt-workload-key="k8sRtWorkloadKey"
      :k8s-rt-workloads-for="k8sRtWorkloadsFor"
      :get-loki-mapping="getLokiMapping"
      :obs-grafana-ds-candidates="obsGrafanaDsCandidates"
      :grafana-ds-uid-by-obs-env="grafanaDsUidByObsEnv"
      :obs-grafana-ds-key="obsGrafanaDsKey"
      :obs-grafana-ds-types="OBS_GRAFANA_DS_TYPES"
      @set-obs-access-mode="setObsAccessMode"
      @update-tool-input="(k, v, toolKey, envID) => {
        toolInputs[k] = v
        scheduleObsProbe(toolKey, envID)
        if (toolKey === 'grafana') scheduleGrafanaDsAutoload(envID)
      }"
      @clear-tool-input="clearToolFieldInput"
      @run-k8s-rt-preload="runK8sRtPreload"
      @set-k8s-rt-env-loc="(envID, field, value) => setK8sRtEnvLoc(envID, field, value)"
      @set-k8s-rt-svc-workload="(envID, svc, workload) => setK8sRtSvcWorkload(envID, svc, workload)"
      @load-loki-datasources="loadLokiDatasources"
      @load-loki-labels="loadLokiLabels"
      @env-label-key-changed="onEnvLabelKeyChanged"
      @service-label-key-changed="onServiceLabelKeyChanged"
      @env-value-changed="onEnvValueChanged"
    />

    <DataStoreStep
      v-if="currentStep === 7"
      :ds-import-status="dsImportStatus"
      :ds-import-stats="dsImportStats"
      :can-auto-import-d-s="canAutoImportDS"
      :probing-all="probingAll"
      :probing-all-stats="probingAllStats"
      :probing-by-env="probingByEnv"
      :scanned-d-s="scannedDS"
      :service-config-sel="serviceConfigSel"
      :ds-probe-results="dsProbeResults"
      :scan-state-of="scanStateOf"
      :ds-label="dsLabel"
      :ds-field-label="dsFieldLabel"
      :ds-field-is-secret="dsFieldIsSecret"
      :probe-key="probeKey"
      @auto-import-data-stores="autoImportDataStores"
      @probe-all-across-envs="probeAllAcrossEnvs"
      @remove-d-s="(envID, svc, dsKey) => removeScannedDS(envID, svc, dsKey)"
      @probe-d-s="(envID, svc, dsKey) => probeOneDS(envID, svc, dsKey)"
    />

    <YamlPreviewStep
      v-if="currentStep === 9"
      :yaml-output="yamlOutput"
      :validate-loading="validateLoading"
      :validate-result="validateResult"
      :copy-success="copySuccess"
      :target-options="targetOptions"
      :enabled-targets="enabledTargets"
      :target-labels="targetLabels"
      :any-target-selected="anyTargetSelected"
      @validate="validateYAML"
      @copy="copyYAML"
      @download="downloadYAML"
    />

    <OneClickDeployStep
      v-if="currentStep === 10"
      :deploy-summary="deploySummary"
      :deploy-loading="deployLoading"
      :deploy-progress-line="deployProgressLine"
      :deploy-error="deployError"
      @run-deploy="runOneClickDeploy"
    />

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

<style>
/* 注:本页 CSS 不加 scoped —— InitPage 抽出 18 个子组件后,子组件 template 引用的
   .dynamic-row / .cc-field / .ds-svc-block / .repo-block 等 ~140 个类全在这里定义,
   scoped 会让样式无法穿透到子组件 DOM。这些类名都是 InitPage 域专属(.repo-block /
   .cc-env-block / .monorepo-banner 等),不会跟其他页冲突。.btn 等通用类各页 scoped
   内自有定义,不受本块影响。 */
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

/* .subtitle / .page-header 跟 design.css(全局共享)+ BotsPage(scoped)同名;本块不 scoped,
   所以 prefix .init-page 父选择器把作用域锁在 InitPage 树内,避免污染全局 */
.init-page .subtitle {
  color: #64748b;
  font-size: 14px;
  margin-bottom: 0;
  line-height: 1.6;
}

.init-page .page-header {
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

.init-page .page-header .subtitle {
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
/* InitPage 卡片 = .card.lg(尺寸/阴影来自 design.css)。本页加大步骤间距 + 标题样式,
   并限定在 .init-page 子树,避免泄漏到 .card 同名的其它页(BotsPage / EditorPage 等)。 */
.init-page .card.lg { margin-bottom: 20px; }
.init-page .card.lg h2 {
  font-size: 20px;
  font-weight: 600;
  color: #1e293b;
  margin: 0 0 14px 0;
  line-height: 1.3;
}
.init-page .card.lg .help-text {
  margin-bottom: 18px;
}

/* .form-group / .help-icon / .error-text / .dynamic-row / .row-fields /
   .checkbox-group / .required / .btn-icon.remove / input/textarea/select 基础样式
   全部已上提到 design.css —— 多组件共享避免依赖 InitPage CSS 加载顺序 */

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
  background: #f8fafc;
}
/* opacity 只灰化 checkbox + 标题(暗示"未启用"),底部"重新扫描"按钮保持 100%
   清晰可点 —— 否则整张卡片半透明,按钮跟着糊,用户以为按钮 disabled。 */
.target-card.target-disabled .target-card-head { opacity: 0.6; }
.target-card.target-disabled .target-card-head .target-title { color: #64748b; }
.target-card.target-disabled .target-hint { opacity: 0.6; }
.target-card.target-disabled .target-missing-actions { opacity: 1; }
.target-card.target-disabled .target-missing-actions .btn-link {
  font-weight: 600; /* 加粗强调按钮可点 */
}
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
.obs-tool-chip.needs-grafana {
  background: #fef3c7; border-color: #f59e0b; color: #92400e;
}
.obs-tool-chip.needs-grafana::after { content: " ⚠ 需 Grafana"; font-size: 10px; }
.obs-tool-chip input[type=checkbox] { width: 12px; height: 12px; margin: 0; cursor: pointer; }

/* Loki/Prometheus/Tempo 启用但 Grafana 未启用时的强警示 */
.obs-grafana-required-banner {
  margin: 12px 0 18px;
  padding: 12px 14px;
  background: #fef3c7; border: 1px solid #f59e0b; border-radius: 6px;
  font-size: 13px; color: #78350f;
}
.obs-grafana-required-banner strong { color: #92400e; }
.obs-grafana-required-banner p { margin: 6px 0 0; line-height: 1.55; }
.obs-grafana-required-banner code {
  background: #fde68a; padding: 1px 4px; border-radius: 3px; font-size: 12px;
}

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
/* Step 7 数据层入口两枚 CTA(读取配置中心 / 全环境连通性):本步起手与收尾动作,
   字号/内边距比普通 .btn 加大,跟旁边数据流位置对齐易点 */
.ds-autoimport-row .btn {
  display: inline-flex; align-items: center; gap: 6px;
  padding: 10px 22px; font-size: 14px; font-weight: 600;
}
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
/* Step 9 预览+生成:验证 / 复制 / 导出 都是流程末段动作,放大跟旁边 yaml 预览体量对齐 */
.action-bar {
  display: flex;
  gap: 10px;
  margin-bottom: 16px;
}
.action-bar .btn { padding: 10px 22px; font-size: 14px; font-weight: 600; }

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

/* Step 10 部署 CTA:整个向导最终动作,放大到醒目尺寸,色彩仍走 .btn.primary 主调 */
.deploy-final-block { padding: 6px 0; }
.deploy-final-btn {
  padding: 16px 36px; font-size: 17px; font-weight: 700;
  letter-spacing: 0.3px; min-width: 320px;
  justify-content: center;  /* 文本+emoji 在 min-width 撑出来的多余空间里居中,不靠左 */
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
/* .btn / .btn.primary / .btn-link / .btn-icon / .info-box 来自全局 design.css */

.nav-buttons {
  display: flex;
  justify-content: space-between;
  align-items: flex-end;
  margin-top: 8px;
}
/* 上一步 / 下一步:每步底部固定动作,放大避免误点和频繁找位 */
.nav-buttons .btn { padding: 11px 28px; font-size: 14px; font-weight: 600; min-width: 100px; justify-content: center; }
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

/* info-box 里的 <p> 组合 InitPage 独有,prefix .init-page 锁作用域 —— design.css 的
   全局 .info-box base 不动,只调 InitPage 内部的 p / strong 排版 */
.init-page .info-box p { margin: 0; }
.init-page .info-box p + p { margin-top: 4px; }
.init-page .info-box strong { font-size: var(--fs-md); }

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
