// useDeployFlow —— Step 10 一键部署:遍历 Step 2 已勾的 target,各自走 importAndDeploy 闭环。
//
// 暴露:
//   - 状态        deployLoading / deployError / deploySummary / targetDeployPaths / targetDeployPathHints
//   - 入口        runOneClickDeploy()
//   - 内部 helper installEnvVarName / buildInstallCreds(只服务 runOneClickDeploy,不导出)
//
// wizard 已填的组件凭证按 install_naming.go 的命名拼成 creds map，交给原生部署。
//   - claude-code  ~/.claude/agents/<name>.md(<name>=workspace_name 兜底 system.id-bot)
//   - cursor       ~/.cursor/agents/<name>.md
//   - codex        ~/.codex/agents/<name>.toml(TOML subagent;主 chat 自然语言 spawn)
//
// 失败容错:任一 target 倒了 → 整体停下保留已成功的,error 里显示是哪个 target 倒了;
import type { TargetId } from './constants'
import { computed, ref, reactive, type ComputedRef, type Ref } from 'vue'
import type { Router } from 'vue-router'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import {
  defaultDestPath, detectAITools, importAndDeploy, reindexCodeGraph,
  validate as bridgeValidate, isDesktop,
} from './bridge'
import type { CodeGraphIndexReport } from './bridge'
import { confirmDialog } from './confirm'
import { pushLog } from './logStore'
import { toast } from './toast'
import type { CredField } from './credFields'
import type { RepoScanItem } from './useRepoScan'
import { resolveObsFieldValue } from './obsConnection'
import type { ConfigSourceInstance } from './configSourceInstances'

interface ToolSpecLike {
  key: string
  fields: CredField[]
}

export interface TargetDeployState { status: 'pending' | 'running' | 'success' | 'error'; message: string }

export interface UseDeployFlowDeps {
  onComplete?: (targets: string[]) => void
  // 系统 / agent 基本信息
  agent: { workspace_name: string; model: string }
  system: { id: string }
  targetModels: Record<string, string>

  // target 选择
  enabledTargets: Record<string, boolean>
  targetOptions: readonly TargetId[]
  targetLabels: Record<string, string>
  homeDir: Ref<string>
  openCodeConfigRoot?: ComputedRef<string>

  // 配置源 / 服务 / 环境
  activeSourceTypes: ComputedRef<readonly string[]>
  sourceInstances: ComputedRef<readonly ConfigSourceInstance[]>
  sourceCreds: Record<string, { creds: Record<string, Record<string, string>>; rawExtra?: Record<string, unknown> }>
  environments: { id: string }[]
  enabledDataStores: Record<string, boolean>
  scannedDS?: Record<string, Record<string, Record<string, Record<string, string>>>>
  dataStoreTypes: Record<string, string>

  // 可观测性 + 数据层
  enabledObservability: Record<string, boolean>
  toolInputs: Record<string, string>
  OBS_TOOL_SPECS: readonly ToolSpecLike[]
  DS_TOOL_SPECS: readonly ToolSpecLike[]
  toolKeyFor: (cat: 'obs' | 'ds', tool: string, envID: string, field: string) => string
  isObsFieldHidden: (toolKey: string, envID: string, f: CredField) => boolean
  displayObsField?: (toolKey: string, envID: string, f: CredField) => CredField

  // 部署上下文
  yamlOutput: Ref<string>
  reposRootInput: Ref<string>
  resolvedReposRoot: Ref<string>
  repos: readonly RepoScanItem[]
  resolveCloneDest: (r: RepoScanItem) => string

  // 草稿持久化 + 路由
  storageKey: string
  router: Router
}

export function useDeployFlow(deps: UseDeployFlowDeps) {
  const targetStates = reactive<Record<string, TargetDeployState>>({})
  const completedInputs = new Map<string, string>()
  const deployComplete = ref(false)
  const deployLoading = ref(false)
  const deployError = ref<string | null>(null)
  const codeGraphReport = ref<CodeGraphIndexReport | null>(null)
  const codeGraphRetrying = ref(false)
  const codeGraphRetryFeedback = ref('')
  const codeGraphRetryState = ref<'idle' | 'loading' | 'success' | 'error'>('idle')
  let codeGraphOperationGeneration = 0
  // deployProgressLine 部署进度的最近一行(后端 OnLog 透 wails event "install:log" 推上来)。
  // 主要给 mcp-grafana 二进制下载用,首次部署 ~30 MiB,UI 不显示就让人误以为卡死。
  // logStore 也存了一份(全局日志面板),这里只是给 InitPage loading 状态做"实时一行"提示。
  const deployProgressLine = ref<string>('')

  // 部署路径展示:Step 2 卡片要让用户看到"AI 平台最终从哪儿读 agent",
  // 因此这里展示的是 原生部署完成后的最终落地路径,不是中间包路径。
  // 中间包 ~/.tshoot/<target>/<id>/ 由 defaultDestPath 给后端用,这里只为 UI 提示。
  // homeDir 已在前面声明(getUserConfig 拿的),空字符串时回退 "~" 给用户看。

  // agent 名:workspace_name 优先,否则 system.id-bot 兜底,否则 my-system-bot
  const agentNameForPath = computed(() => (
    deps.agent.workspace_name.trim() || (deps.system.id ? `${deps.system.id}-bot` : 'my-system-bot')
  ))

  const targetDeployPaths = computed<Record<string, string>>(() => {
    const home = deps.homeDir.value || '~'
    const wsName = agentNameForPath.value
    return {
      'claude-code': `${home}/.claude/agents/${wsName}.md`,
      'cursor': `${home}/.cursor/agents/${wsName}.md`,
      'codex': `${home}/.codex/agents/${wsName}.toml`,
      'opencode': `${deps.openCodeConfigRoot?.value || `${home}/.config/opencode`}/agents/${wsName}.md`,
    }
  })

  // 鼠标悬停"自动"标签时提示:这个路径是该 AI 平台官方约定的 agent 读取位置,
  // 不是 Studio 自己塞的;改路径只能改 workspace_name(回 Step 1 改 system.id)。
  const targetDeployPathHints: Record<string, string> = {
    opencode: 'OpenCode 全局 Agent；若配置了 XDG_CONFIG_HOME，则使用该目录下的 opencode。',
    'claude-code': 'Claude Code 启动时读 ~/.claude/agents/*.md(用户级 subagent),所有项目都能 @<name> 调用。',
    'cursor': 'Cursor 启动时读 ~/.cursor/agents/*.md(用户级 Custom Agent),侧栏选用。',
    'codex': 'OpenAI Codex CLI 扫 ~/.codex/agents/*.toml 注册 subagent;在主 chat 里说 "spawn the <name> agent ..." 派生独立 thread(MCP 嵌入 toml 内联段,只在 spawn 时启动)。文档:https://developers.openai.com/codex/subagents',
  }

  // Step 8 一键部署摘要:Step 2 勾了哪些 target → 渲染对应路径
  const deploySummary = computed(() =>
    deps.targetOptions
      .filter(t => deps.enabledTargets[t])
      .map(t => ({ target: t, label: deps.targetLabels[t] || t, path: targetDeployPaths.value[t] || '' })),
  )

  // 拼出跟 Go 端 envVar() 一致的 install env 变量名。Go 的形态:
  //   - sourceID 为 "" / "default" → "<PREFIX>_<ENV>"(老 single-source 兼容)
  //   - 显式多源 → "<PREFIX>_<SOURCE>_<ENV>"
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
  function buildInstallCreds(): Record<string, string> {
    const creds: Record<string, string> = {}
    const sourceInstances = deps.sourceInstances.value
    const isMulti = sourceInstances.length > 1

    // ── 配置中心:每个激活源 × 每个 env ──
    for (const instance of sourceInstances) {
      const t = instance.type
      const cc = deps.sourceCreds[instance.id] || deps.sourceCreds[t]
      if (!cc) continue
      const sourceID = isMulti ? instance.id : 'default'
      if (t === 'one2all') {
        const shared = cc.creds['_shared_'] || {}
        if ((shared.mcp_url || '').trim()) creds.ONE2ALL_MCP_URL = shared.mcp_url.trim()
        if ((shared.token || '').trim()) creds.ONE2ALL_TOKEN = shared.token.trim()
        continue
      }
      for (const env of deps.environments) {
        if (!env.id) continue
        const envCreds = cc.creds[env.id] || {}
        const put = (prefix: string, val: string) => {
          if ((val || '').trim()) creds[installEnvVarName(prefix, sourceID, env.id)] = val.trim()
        }
        switch (t) {
          case 'nacos':
            // 表单 field key 是 user / pass(见 sourceTypeFields.nacos),不是 username / password。
            // 之前用错 key 导致 putValue 永远 undefined → MCP env 块没 NACOS_USERNAME/PASSWORD →
            // nacos-mcp-router 启动时 "ValueError: passwd must be a non-empty string"。
            put('CC_ADDR', envCreds.addr)
            put('CC_USER', envCreds.user)
            put('CC_PASS', envCreds.pass)
            break
          case 'apollo':
            // 表单 field key 是 meta(见 sourceTypeFields.apollo),不是 meta_url。
            put('APOLLO_META', envCreds.meta)
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
            for (const [dsType, on] of Object.entries(deps.enabledDataStores)) {
              if (!on) continue
              const fkey = `static_${dsType}`
              put(`STATIC_${dsType.toUpperCase()}`, (envCreds[fkey] || ''))
            }
            break
        }
      }
    }

    // ── 可观测性:工具规格里 envVar() 已经是 install 名(系统级,不带 source 前缀)──
    for (const tool of deps.OBS_TOOL_SPECS) {
      if (!deps.enabledObservability[tool.key]) continue
      for (const env of deps.environments) {
        if (!env.id) continue
        for (const f of tool.fields) {
          const field = deps.displayObsField ? deps.displayObsField(tool.key, env.id, f) : f
          // uiOnly(如 auth_mode)不喂 install 凭证;showWhen 命中隐藏的字段也跳过(避免把
          // 用户填过又切换鉴权方式后残留的旧值灌进去)。
          if (field.uiOnly) continue
          if (deps.isObsFieldHidden(tool.key, env.id, f)) continue
          const v = resolveObsFieldValue({
            toolInputs: deps.toolInputs,
            sourceCreds: deps.sourceCreds,
            toolKeyFor: deps.toolKeyFor,
          }, tool.key, env.id, field.key).trim()
          if (v) creds[field.envVar(env.id)] = v
        }
      }
    }

    // ── 数据层:DS_TOOL_SPECS 同款循环把每家 (env, field) → env var 吐到 creds map。
    // 用法:install_native_mcp_common.BuildMCPServers 把这些 env vars 注入到 mcp server
    //       env 段(MONGODB_URI_<env> / POSTGRES_DSN_<env> / ES_URL_<env> / ...)。
    // 之前漏了这块循环 → 用户在 wizard 填了 endpoint 但 install creds map 拿不到 →
    // 注册的 mcp server env 段全空 → mcp 启动失败。
    const dataStoreIDs = new Set<string>()
    for (const byService of Object.values(deps.scannedDS || {})) {
      for (const stores of Object.values(byService)) {
        for (const id of Object.keys(stores)) dataStoreIDs.add(id)
      }
    }
    const countsByType = new Map<string, number>()
    for (const id of dataStoreIDs) {
      const type = deps.dataStoreTypes[id] || id
      countsByType.set(type, (countsByType.get(type) || 0) + 1)
    }
    for (const dsID of Array.from(dataStoreIDs).sort()) {
      const dsType = deps.dataStoreTypes[dsID] || dsID
      const tool = deps.DS_TOOL_SPECS.find(item => item.key === dsType)
      if (!tool || !deps.enabledDataStores[dsType]) continue
      const installSourceID = (countsByType.get(dsType) || 0) > 1 || dsID !== dsType ? dsID : 'default'
      for (const env of deps.environments) {
        if (!env.id) continue
        for (const f of tool.fields) {
          if (f.uiOnly) continue
          let v = (deps.toolInputs[deps.toolKeyFor('ds', dsID, env.id, f.key)]
            || deps.toolInputs[deps.toolKeyFor('ds', dsType, env.id, f.key)] || '').trim()
          if (!v) {
            for (const serviceStores of Object.values(deps.scannedDS?.[env.id] || {})) {
              const candidate = (serviceStores?.[dsID]?.[f.key] || '').trim()
              if (candidate) {
                v = candidate
                break
              }
            }
          }
          if (v) {
            const legacyName = f.envVar(env.id)
            const envSuffix = `_${env.id.toUpperCase()}`
            const prefix = legacyName.endsWith(envSuffix)
              ? legacyName.slice(0, -envSuffix.length)
              : legacyName
            creds[installEnvVarName(prefix, installSourceID, env.id)] = v
          }
        }
      }
    }

    // ── ELK 共享凭证(install_prompts 把 ELK_USERNAME/PASSWORD 当 system-wide 共用)──
    if (deps.enabledObservability['elk']) {
      // 取第一个 env 填的当共用值(各 env 一般一样;UI 没拆出"system-wide"输入区)
      for (const env of deps.environments) {
        if (!env.id) continue
        const u = (deps.toolInputs[deps.toolKeyFor('obs', 'elk', env.id, 'user')] || '').trim()
        const p = (deps.toolInputs[deps.toolKeyFor('obs', 'elk', env.id, 'pass')] || '').trim()
        if (u && !creds['ELK_USERNAME']) creds['ELK_USERNAME'] = u
        if (p && !creds['ELK_PASSWORD']) creds['ELK_PASSWORD'] = p
      }
    }

    // ── Agent 模型 ──

    // ── messaging:lark / feishu_project ──
    if (deps.toolInputs['msg:lark:app_id']) creds['LARK_APP_ID'] = deps.toolInputs['msg:lark:app_id']
    if (deps.toolInputs['msg:lark:app_secret']) creds['LARK_APP_SECRET'] = deps.toolInputs['msg:lark:app_secret']
    if (deps.toolInputs['pt:feishu_project:user_token']) creds['MCP_USER_TOKEN'] = deps.toolInputs['pt:feishu_project:user_token']

    return creds
  }

  // 部署和显式重试必须共享这一份路径解析。尤其 umbrella 子仓库需要先解析 parent,
  // 再用 parent_path 拼接；分叉实现很容易让首次部署与 retry 指向不同目录。
  function buildDeployRepoPaths(): Record<string, string> {
    const repoPaths: Record<string, string> = {}
    const effectiveRoot = (deps.reposRootInput.value.trim() || deps.resolvedReposRoot.value).replace(/\/$/, '')
    const resolveRoot = (r: typeof deps.repos[number]): string => {
      const explicit = (r._localPath || '').trim()
      if (explicit) return explicit
      const dest = deps.resolveCloneDest(r)
      if (dest) return dest
      return effectiveRoot ? `${effectiveRoot}/${r.name}` : ''
    }

    for (const r of deps.repos) {
      if (!r.name.trim()) continue
      if (r.parent_repo && r.parent_repo.trim()) continue
      const path = resolveRoot(r)
      if (path) repoPaths[r.name] = path
    }
    for (const r of deps.repos) {
      if (!r.name.trim()) continue
      const parentName = (r.parent_repo || '').trim()
      if (!parentName) continue
      const explicit = (r._localPath || '').trim()
      if (explicit) {
        repoPaths[r.name] = explicit
        continue
      }
      const parentPath = repoPaths[parentName]
      if (parentPath) {
        const mount = (r.parent_path || '').trim() || r.name
        repoPaths[r.name] = `${parentPath.replace(/\/$/, '')}/${mount}`
        continue
      }
      const dest = deps.resolveCloneDest(r)
      if (dest) repoPaths[r.name] = dest
      else if (effectiveRoot) repoPaths[r.name] = `${effectiveRoot}/${r.name}`
    }
    return repoPaths
  }

  async function retryCodeGraph() {
    if (codeGraphRetrying.value || deployLoading.value) return
    const generation = ++codeGraphOperationGeneration
    codeGraphRetrying.value = true
    codeGraphRetryState.value = 'loading'
    codeGraphRetryFeedback.value = '正在重新索引失败仓库…'
    let unlisten: (() => void) | undefined
    try {
      unlisten = EventsOn('install:log', (line: string) => {
        if (generation === codeGraphOperationGeneration && typeof line === 'string' && line.trim()) {
          deployProgressLine.value = line
          codeGraphRetryFeedback.value = line
        }
      })
      const report = await reindexCodeGraph(deps.yamlOutput.value, buildDeployRepoPaths())
      if (generation !== codeGraphOperationGeneration) return
      codeGraphReport.value = report
      codeGraphRetryState.value = report.ready === report.total ? 'success' : 'error'
      codeGraphRetryFeedback.value = report.ready === report.total
        ? `重新索引完成：CodeGraph ${report.ready}/${report.total} repos ready`
        : `重新索引完成，但仍有 ${report.total - report.ready} 个仓库未就绪`
    } catch (e: any) {
      if (generation === codeGraphOperationGeneration) {
        codeGraphRetryState.value = 'error'
        codeGraphRetryFeedback.value = `重新索引失败：${String(e?.message || e)}`
      }
      pushLog('install', 'error', `[codegraph-retry] ${String(e?.message || e)}`)
    } finally {
      if (generation === codeGraphOperationGeneration) codeGraphRetrying.value = false
      try { unlisten?.() } catch { /* unlisten 失败无害 */ }
    }
  }

  // 一键部署:遍历 Step 2 已勾选的所有 target,各自走 importAndDeploy。
  // 路径全自动,无需用户在 Step 8 再选 target / 选目录(都用 ~/.tshoot/<target>/<id>/)。
  // 任一 target 部署失败 → 整体停下保留已成功的,error 里显示是哪个 target 倒了。
  async function runOneClickDeploy() {
    deployComplete.value = false
    if (deployLoading.value || codeGraphRetrying.value) return
    const generation = ++codeGraphOperationGeneration
    deployLoading.value = true
    deployError.value = null
    codeGraphReport.value = null
    codeGraphRetryState.value = 'idle'
    codeGraphRetryFeedback.value = ''
    deployProgressLine.value = '正在准备部署…'
    let unlisten: (() => void) | undefined
    try {
      if (!isDesktop()) {
        deployError.value = '一键部署只在桌面 app 可用;浏览器模式请下载 yaml 去 BotsPage 或用 CLI'
        return
      }
      const enabled = deps.targetOptions.filter(t => deps.enabledTargets[t])
      if (enabled.length === 0) {
        deployError.value = '请在“运行方式”选择至少一个 AI 平台'
        return
      }
      // 部署前校一把 yaml,失败就不提交到后端兜错
      try {
        await bridgeValidate(deps.yamlOutput.value)
      } catch (e: any) {
        deployError.value = `yaml 校验失败:${String(e?.message || e)};请先点"✓ 验证"修复`
        return
      }

      // 部署前 re-detect IDE —— Step 2 勾选时 detect 过一次,但用户可能在向导
      // 跑完 / 离开 wizard 这段时间卸载 IDE。装下去会成孤儿(IDE 看不到 agent),
      try {
        const cur = await detectAITools()
        const ideStatus: Record<string, boolean> = {
          'claude-code': !!cur.claude_code?.installed,
          'cursor':      !!cur.cursor?.installed,
          'codex':       !!cur.codex?.installed,
            'opencode': !!cur.opencode?.installed,
        }
        const missing = enabled.filter(t => !ideStatus[t])
        if (missing.length > 0) {
          const labels = missing.map(t => deps.targetLabels[t] || t).join(' / ')
          const ok = await confirmDialog({
            title: `${labels} 已不在本机,继续部署?`,
            message: `选目标时检测到 ${labels} 已安装,现在重新探测发现已不在(可能你刚才卸载了 IDE)。
继续部署会把 agent 文件装到 ~/.${missing[0]}/agents/<name>.md,但 IDE 没装就调不到 ——
机器人会出现在「已装机器人」页面但标 "⚠ IDE 已卸载,机器人不可用"。
要么取消去重装 IDE,要么继续装(等装回 IDE 自动恢复)。`,
            confirmText: '继续装',
            cancelText: '取消',
            defaultAction: 'cancel',
          })
          if (!ok) {
            deployError.value = `已取消:${labels} 缺失`
            return
          }
        }
      } catch {
        // 探测失败(浏览器模式 / detect 接口异常)→ 不阻塞部署,后端会按默认路径装,
        // BotsPage 那边的 ide_available 会标 broken,跟 wizard 探测失败的兜底语义一致。
      }

      // 订阅本次部署期间的 install:log。EventsOn 返回 unlisten,finally 里调一次防泄漏。
      // logStore 的全局桥接也在收同一个 event,不冲突(用途不同:logStore 累积全部、这里只
      // 反映"最新一行"做实时提示)。
      unlisten = EventsOn('install:log', (line: string) => {
        if (generation === codeGraphOperationGeneration && typeof line === 'string' && line.trim()) {
          deployProgressLine.value = line
        }
      })
      // 构造 repoPaths(三个 target 共用同一份本机仓库路径表)。
      // 解析优先级跟 analyzerpipe.Run / useRepoScan refresh helpers 完全一致:
      //   1. _localPath 显式给(本地模式 / splitMonorepo 给 umbrella 子模块预填的位置)
      //   2. parent_repo 在场 → 等 parent 已解析,然后 <parent 路径>/<parent_path 或 name>
      //      (umbrella 继承编排;之前漏了这条,umbrella 子模块本地路径错落到 ReposRoot/<name>
      //      上,bot 的 repo-path-map.yaml 写成 ~/go/src/api 而不是 ~/go/src/truss/api)
      //   3. 远程模式 _cloneTarget + name(用户挑过)
      //   4. 兜底 effectiveRoot + name(全局默认 reposRoot)
      const repoPaths = buildDeployRepoPaths()

      // 每个勾选的 target:
      //   - claude-code / cursor:importAndDeploy 内部已 native install 到 ~/.claude|cursor/,
      //     跑完即生效,无须二次操作
      //     如果有字段没填(用户在 Step 5/7 留空了),就 fallback 到 BotsPage 让用户补全。
      const installedTargets: string[] = []
      const installCreds = buildInstallCreds()
      const input = JSON.stringify([deps.yamlOutput.value, repoPaths, installCreds])
      for (const target of enabled) {
        targetStates[target] = completedInputs.get(target) === input
          ? { status: 'success', message: '已创建，无需重复部署' }
          : { status: 'pending', message: '等待创建' }
      }
      for (const t of enabled) {
        if (completedInputs.get(t) === input) { installedTargets.push(t); continue }
        targetStates[t] = { status: 'running', message: '正在生成、部署并检查工具连接…' }
        try {
          const dest = await defaultDestPath(t, deps.system.id || '')
          const applied = await importAndDeploy(deps.yamlOutput.value, t, dest, repoPaths, installCreds)
          if (generation === codeGraphOperationGeneration && !codeGraphReport.value && applied.codegraph) {
            codeGraphReport.value = applied.codegraph
          }
          installedTargets.push(t)
          completedInputs.set(t, input)
          targetStates[t] = { status: 'success', message: '创建完成' }
        } catch (error: any) {
          targetStates[t] = { status: 'error', message: String(error?.message || error) }
        }
      }
      const failed = enabled.filter(t => targetStates[t]?.status === 'error')
      deployComplete.value = failed.length === 0
      if (failed.length) deployError.value = `${failed.length} 个平台创建失败。修改后再次创建，相同配置下已成功的平台不会重复部署。`
      else {
        toast.success(`创建完成，共 ${installedTargets.length} 个平台`)
        deps.onComplete?.(installedTargets)
      }

      // 部署成功 → 给 saved 草稿打 lastDeployAt 时间戳。HomePage 的"下一步推荐"读到它就
      // 切成"已部署"语义,不再引导"继续部署"(用户实测撞过:已经部署完了首页还显示"继续部署")。
      // 改 currentStep 不安全(用户可能想留在 Step 10 重部),只加个时间戳。
      try {
        const raw = localStorage.getItem(deps.storageKey)
        if (raw && deployComplete.value) {
          const parsed = JSON.parse(raw)
          parsed.lastDeployAt = Date.now()
          parsed.lastDeployedTargets = installedTargets
          localStorage.setItem(deps.storageKey, JSON.stringify(parsed))
        }
      } catch { /* localStorage 读写失败不影响部署主流程 */ }
      // Keep the result visible. The user chooses whether to configure Bug tickets or start troubleshooting.
    } catch (e: any) {
      if (generation === codeGraphOperationGeneration) deployError.value = String(e?.message || e)
    } finally {
      if (generation === codeGraphOperationGeneration) deployLoading.value = false
      try { unlisten?.() } catch { /* unlisten 失败无害 */ }
    }
  }

  return {
    targetStates,
    deployComplete,
    deployLoading,
    deployError,
    deploySummary,
    deployProgressLine,
    codeGraphReport,
    codeGraphRetrying,
    codeGraphRetryFeedback,
    codeGraphRetryState,
    targetDeployPaths,
    targetDeployPathHints,
    runOneClickDeploy,
    retryCodeGraph,
  }
}
