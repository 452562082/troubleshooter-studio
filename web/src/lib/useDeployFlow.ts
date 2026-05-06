// useDeployFlow —— Step 10 一键部署:遍历 Step 2 已勾的 target,各自走 importAndDeploy 闭环。
//
// 暴露:
//   - 状态        deployLoading / deployError / deploySummary / targetDeployPaths / targetDeployPathHints
//   - 入口        runOneClickDeploy()
//   - 内部 helper installEnvVarName / buildOpenclawCreds(只服务 runOneClickDeploy,不导出)
//
// 跟 BotsPage 那条手动闭环对齐:wizard 已填的所有凭证(配置中心 + 可观测性 + ELK 共用 + 模型 +
// messaging)按 install_naming.go 的 envVar() 命名拼成 creds map,直接喂给 RunInstall —— 不再
// 让用户去 BotsPage 二次输入。openclaw / claude-code / cursor / codex 路径全自动:
//   - openclaw     ~/.openclaw/workspace/<workspace_name>/(用 customInstallRoots[t] 覆盖根目录)
//   - claude-code  ~/.claude/agents/<name>.md(<name>=workspace_name 兜底 system.id-bot)
//   - cursor       ~/.cursor/agents/<name>.md
//   - codex        ~/.codex/agents/<name>.toml(TOML subagent;主 chat 自然语言 spawn)
//
// 失败容错:任一 target 倒了 → 整体停下保留已成功的,error 里显示是哪个 target 倒了;
// openclaw RunInstall 失败 → 保留中间包,toast 提示用户去 BotsPage 补凭证。
import { computed, ref, type ComputedRef, type Ref } from 'vue'
import type { Router } from 'vue-router'
import {
  defaultDestPath, importAndDeploy, runInstall, selfTestAgent,
  validate as bridgeValidate, isDesktop,
} from './bridge'
import { Target, IDE_TARGETS, type TargetId } from './constants'
import { pushLog } from './logStore'
import { toast } from './toast'
import type { CredField } from './credFields'
import type { RepoScanItem } from './useRepoScan'

interface ToolSpecLike {
  key: string
  fields: CredField[]
}

export interface UseDeployFlowDeps {
  // 系统 / agent 基本信息
  agent: { workspace_name: string; model: string }
  system: { id: string }
  targetModels: Record<string, string>

  // target 选择
  enabledTargets: Record<string, boolean>
  targetOptions: readonly TargetId[]
  targetLabels: Record<string, string>
  customInstallRoots: Record<string, string>
  homeDir: Ref<string>

  // 配置源 / 服务 / 环境
  activeSourceTypes: ComputedRef<readonly string[]>
  sourceCreds: Record<string, { creds: Record<string, Record<string, string>>; rawExtra?: Record<string, unknown> }>
  environments: { id: string }[]
  enabledDataStores: Record<string, boolean>

  // 可观测性
  enabledObservability: Record<string, boolean>
  toolInputs: Record<string, string>
  OBS_TOOL_SPECS: readonly ToolSpecLike[]
  toolKeyFor: (cat: 'obs' | 'ds', tool: string, envID: string, field: string) => string
  isObsFieldHidden: (toolKey: string, envID: string, f: CredField) => boolean

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
  const deployLoading = ref(false)
  const deployError = ref<string | null>(null)

  // 部署路径展示:Step 2 卡片要让用户看到"AI 平台最终从哪儿读 agent",
  // 因此这里展示的是 install.sh 跑完后的最终落地路径,不是中间包路径。
  // 中间包 ~/.tshoot/<target>/<id>/ 由 defaultDestPath 给后端用,这里只为 UI 提示。
  // homeDir 已在前面声明(getUserConfig 拿的),空字符串时回退 "~" 给用户看。

  // agent 名:workspace_name 优先,否则 system.id-bot 兜底,否则 my-system-bot
  const agentNameForPath = computed(() => (
    deps.agent.workspace_name.trim() || (deps.system.id ? `${deps.system.id}-bot` : 'my-system-bot')
  ))

  const targetDeployPaths = computed<Record<string, string>>(() => {
    const home = deps.homeDir.value || '~'
    const wsName = agentNameForPath.value
    // 用户手选的自定义根目录优先,没有就用默认 ~/.<target>
    const rootFor = (t: string, def: string) => (deps.customInstallRoots[t] || '').trim() || def
    return {
      'openclaw': `${rootFor('openclaw', `${home}/.openclaw`)}/workspace/${wsName}/`,
      'claude-code': `${rootFor('claude-code', `${home}/.claude`)}/agents/${wsName}.md`,
      'cursor': `${rootFor('cursor', `${home}/.cursor`)}/agents/${wsName}.md`,
      'codex': `${rootFor('codex', `${home}/.codex`)}/agents/${wsName}.toml`,
    }
  })

  // 鼠标悬停"自动"标签时提示:这个路径是该 AI 平台官方约定的 agent 读取位置,
  // 不是 Studio 自己塞的;改路径只能改 workspace_name(回 Step 1 改 system.id)。
  const targetDeployPathHints: Record<string, string> = {
    'openclaw': 'OpenClaw 启动时扫 ~/.openclaw/workspace/* 列出可用 agent,选一个进入。',
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
    const isMulti = deps.activeSourceTypes.value.length > 1

    // ── 配置中心:每个激活源 × 每个 env ──
    for (const t of deps.activeSourceTypes.value) {
      const cc = deps.sourceCreds[t]
      if (!cc) continue
      const sourceID = isMulti ? t : 'default'
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
          // uiOnly(如 auth_mode)不喂 install 凭证;showWhen 命中隐藏的字段也跳过(避免把
          // 用户填过又切换鉴权方式后残留的旧值灌进去)。
          if (f.uiOnly) continue
          if (deps.isObsFieldHidden(tool.key, env.id, f)) continue
          const v = (deps.toolInputs[deps.toolKeyFor('obs', tool.key, env.id, f.key)] || '').trim()
          if (v) creds[f.envVar(env.id)] = v
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
    const model = (deps.targetModels[Target.Openclaw] || deps.agent.model || '').trim()
    if (model) creds['MODEL'] = model

    // ── messaging:lark / feishu_project ──
    if (deps.toolInputs['msg:lark:app_id']) creds['LARK_APP_ID'] = deps.toolInputs['msg:lark:app_id']
    if (deps.toolInputs['msg:lark:app_secret']) creds['LARK_APP_SECRET'] = deps.toolInputs['msg:lark:app_secret']
    if (deps.toolInputs['pt:feishu_project:user_token']) creds['MCP_USER_TOKEN'] = deps.toolInputs['pt:feishu_project:user_token']

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
    const enabled = deps.targetOptions.filter(t => deps.enabledTargets[t])
    if (enabled.length === 0) {
      deployError.value = 'Step 2 没勾选任何部署目标'
      return
    }
    // 部署前校一把 yaml,失败就不提交到后端兜错
    try {
      await bridgeValidate(deps.yamlOutput.value)
    } catch (e: any) {
      deployError.value = `yaml 校验失败:${String(e?.message || e)};请先点"✓ 验证"修复`
      return
    }
    deployLoading.value = true
    try {
      // 构造 repoPaths(三个 target 共用同一份本机仓库路径表)
      const repoPaths: Record<string, string> = {}
      const effectiveRoot = deps.reposRootInput.value.trim() || deps.resolvedReposRoot.value
      for (const r of deps.repos) {
        if (!r.name.trim()) continue
        let path = ''
        if (r._source === 'local') {
          path = (r._localPath || '').trim()
        } else {
          // _cloneTarget 是父目录,实际仓库路径要拼上 repo.name
          path = deps.resolveCloneDest(r)
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
        const dest = await defaultDestPath(t, deps.system.id || '')
        // 同一份 creds 顺带传给 claude-code/cursor:installNative 走完文件拷贝后会用它
        // 注入 ~/.claude.json (user-scope dotfile) / ~/.cursor/mcp.json 的 mcpServers,装完即可用 MCP 工具。
        // openclaw 的自定义目录走 openclawInstallDir 那条独立 UI;这里只对 ide 三家生效
        const isIDE = (IDE_TARGETS as string[]).includes(t)
        const cir = isIDE ? (deps.customInstallRoots[t] || '').trim() : ''
        await importAndDeploy(deps.yamlOutput.value, t, dest, repoPaths, openclawCreds, cir)
        if (isIDE) {
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
        ? await defaultDestPath('openclaw', deps.system.id || '')
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
      // 部署成功 → 给 saved 草稿打 lastDeployAt 时间戳。HomePage 的"下一步推荐"读到它就
      // 切成"已部署"语义,不再引导"继续部署"(用户实测撞过:已经部署完了首页还显示"继续部署")。
      // 改 currentStep 不安全(用户可能想留在 Step 10 重部),只加个时间戳。
      try {
        const raw = localStorage.getItem(deps.storageKey)
        if (raw) {
          const parsed = JSON.parse(raw)
          parsed.lastDeployAt = Date.now()
          parsed.lastDeployedTargets = installedTargets
          localStorage.setItem(deps.storageKey, JSON.stringify(parsed))
        }
      } catch { /* localStorage 读写失败不影响部署主流程 */ }
      deps.router.push('/bots')
    } catch (e: any) {
      deployError.value = String(e?.message || e)
    } finally {
      deployLoading.value = false
    }
  }

  return {
    deployLoading,
    deployError,
    deploySummary,
    targetDeployPaths,
    targetDeployPathHints,
    runOneClickDeploy,
  }
}
