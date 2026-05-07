// useRepoScan —— Step 4 仓库扫描的全套异步操作打包。
//
// 包含:
//   - resolveCloneDest(r)         父目录 + repo.name 拼真实 clone 路径
//   - pickCloneTarget(r)          打开目录对话框选 clone 父目录
//   - pickLocalRepoDir(r)         打开目录对话框选已 clone 仓库
//   - resolveLocalRepoPath(r,p)   选了本地路径 → 反填 url/name + 触发扫描
//   - refreshRoleHint(r)          后端 RecommendRoleForRepo 推 role 推荐
//   - refreshSubmoduleHints(r)    后端 DetectSubmodules 探 monorepo 信号
//   - scanSingleRepo(r)           bridgeAnalyzeV2 主扫描:stack + service_names + branches
//
// 不包含:applyRoleHint / syncServiceNamesWithRole / pickedSubmoduleCount /
//        submodulePathFor / toggleSubmodulePick / onRepoSubPathInput / qualifyServiceName
//        —— 都是纯 UI mutation,留在 InitPage。
//
// 入参 deps 是 InitPage 那边的 reactive 引用 + helper 函数 + generateYAML closure;
// 通过 Vue 3 proxy 跨 composable 边界 mutate 仍然 reactive。
import type { Ref, Reactive } from 'vue'
import {
  analyzeV2 as bridgeAnalyzeV2,
  detectSubmodulesForRepo,
  getRemoteURL,
  isDesktop,
  openDir,
  pathExists,
  recommendRoleForRepo,
} from './bridge'
import { toast } from './toast'

// 跟 InitPage 的 RepoItem / RepoRole / EnvItem 形状对齐(放宽到 string 避免严格 union 跨边界匹配难)。
export interface RepoScanItem {
  name: string
  url: string
  stack: string
  framework: string
  role?: string
  sub_path?: string
  /** umbrella 子模块:本仓是从哪个 umbrella(主仓)切出去的,引用 repos[].name */
  parent_repo?: string
  /** umbrella 内的挂载相对路径(默认 = name) */
  parent_path?: string
  service_names: string
  env_branches: Record<string, string>
  _nameManual?: boolean
  _source?: 'local' | 'remote'
  _localPath?: string
  _cloneTarget?: string
  _scanning?: boolean
  _scanError?: string
  _scanned?: boolean
  _scannedSource?: string
  _serviceEntries?: Record<string, string>
  _submoduleHintsDismissed?: boolean
  _submoduleHints?: { name: string; sub_path: string; stack: string; role: string; reason: string; url?: string }[]
  _submoduleSelection?: Record<string, boolean>
  _roleHint?: { role: string; reason: string }
  /** 用户是否显式挑过 role(via 角色下拉 @change / "采用"按钮)。
   * 影响 refreshRoleHint:false → hint 跟当前 role 不一致时自动采用;true → 不再覆盖。
   * 老 saved draft 没此字段视为 false,首次 scan 后按 hint 自动 align(用户若不满意手挑一次即锁定)。 */
  _roleManuallyPicked?: boolean
  _roleHintLoading?: boolean
}

export interface RepoScanEnv {
  id: string
  is_prod?: boolean
}

export interface RepoScanDeps {
  /** repo.name → 真实 git 分支列表(scan 后填,env_branches 下拉的 options 用) */
  repoBranchesMap: Ref<Record<string, string[]>>
  /** 已配置 envs;扫到 branches 后给每个 env 自动选默认分支用 */
  environments: Reactive<RepoScanEnv[]>
  /** 当前所有仓库行(refreshSubmoduleHints 用来判定哪些子模块已经拆过、默认 uncheck) */
  repos: Reactive<RepoScanItem[]>
  /** 全局默认 clone 父目录(用户可空,resolvedReposRoot 永远非空兜底) */
  reposRootInput: Ref<string>
  resolvedReposRoot: Ref<string>
  /** 启发式:env id / is_prod → 从 branches 选最匹配的长期分支 */
  pickBranchForEnv: (env: RepoScanEnv, branches: string[]) => string
  /** 业务服务角色判定(只有这些角色才反填 service_names) */
  isServiceRole: (role?: string) => boolean
  /** url → 推 repo.name(本地反填用) */
  deriveRepoName: (url: string) => string
  /** 跑 bridgeAnalyzeV2 前要把当前 InitPage state 序列化成 yaml,closure 持有 25+ 个 InitPage reactive */
  generateYAML: () => string
}

// canonicalizeGitURL 把 git URL 归一化(ssh:// / https:// / scp 形式) → "host/owner/repo"
// 小写、无 .git 后缀。给"用户选的本地目录的 origin 跟 yaml 锁定 URL 比对"用,跨协议
// 不必改写 yaml 也能匹配。跟 internal/gitclone/clone.go 的 CanonicalURL 一致语义。
function canonicalizeGitURL(raw: string): string {
  let s = (raw || '').trim()
  if (!s) return ''
  s = s.replace(/^ssh:\/\//, '').replace(/^git\+ssh:\/\//, '')
  s = s.replace(/^https?:\/\//, '')
  // user@host:path(scp 风格)→ host:path
  const at = s.indexOf('@')
  if (at >= 0 && !s.slice(0, at).includes('/')) {
    s = s.slice(at + 1)
  }
  // 第一个 ':' 替换成 '/'
  const colon = s.indexOf(':')
  if (colon >= 0 && !s.slice(0, colon).includes('/')) {
    s = s.slice(0, colon) + '/' + s.slice(colon + 1)
  }
  return s.toLowerCase().replace(/\/+$/, '').replace(/\.git$/, '')
}

export function useRepoScan(deps: RepoScanDeps) {
  // resolveCloneDest 把"父目录 + repo.name"拼出真实 clone 落地路径。
  // 调用方:scanSingleRepo 构造 repoPaths、Step 8 一键部署构造 repoPaths。
  // 返回空串表示"无路径信息(name 也空)",调用方走 effectiveRoot 兜底逻辑。
  function resolveCloneDest(r: RepoScanItem): string {
    const parent = (r._cloneTarget || '').trim().replace(/\/$/, '')
    const name = r.name.trim()
    if (!parent || !name) return ''
    return `${parent}/${name}`
  }

  // refreshRoleHint 给 repo 拿一份"基于当前 name + stack + 本地路径"的 role 推荐。
  // 触发时机:onRepoNameInput / 仓库扫描完(stack 自动填好后)/ Step 4 进入时遍历刷一遍。
  //
  // 自动采用规则:hint 拿到后,如果用户**没显式手挑过 role**(_roleManuallyPicked=false 或
  // 字段不存在,常见情况是 makeEmptyRepo 兜底的 'backend' 跟实际 React 前端项目对不上),
  // 直接把 r.role 改成 hint.role —— 用户首次进 Step 4 看到的 role 就已是推荐值,不必手点
  // "采用"按钮再让它生效。用户若不满意推荐,手挑一次即锁定 _roleManuallyPicked=true,
  // 后续扫描不再被覆盖。
  async function refreshRoleHint(r: RepoScanItem) {
    if (!r.name.trim()) {
      r._roleHint = undefined
      return
    }
    r._roleHintLoading = true
    try {
      // 推 path 优先级:
      //   1. _localPath 显式给(本地模式 / splitMonorepo 远程子模块预填的 umbrella 内位置)→ 直接用
      //   2. 远程模式无显式覆盖 → 拼"clone 父目录(_cloneTarget / reposRootInput /
      //      resolvedReposRoot 三层兜底)+ repo.name"算落地路径
      // path 空 → 后端 RecommendRole 跳过文件级判别(读 package.json/pom.xml/go.mod)
      // 直接兜底 backend,远程仓 React/Vue 项目会被错判成后端。
      let path = ''
      const explicit = (r._localPath || '').trim()
      if (explicit) {
        path = explicit
      } else if (r._source === 'remote') {
        const parent = ((r._cloneTarget || '').trim() ||
          (deps.reposRootInput.value || '').trim() ||
          deps.resolvedReposRoot.value).replace(/\/+$/, '')
        if (parent) {
          path = parent + '/' + r.name.trim()
        }
      }
      // monorepo:把 sub_path 拼上,后端 RecommendRoleForRepo 会看子目录下的 package.json / pom.xml
      if (path && r.sub_path && r.sub_path.trim()) {
        path = path.replace(/\/+$/, '') + '/' + r.sub_path.trim().replace(/^\/+/, '')
      }
      const hint = await recommendRoleForRepo(r.stack || 'go', r.name, path)
      r._roleHint = hint
      // 自动采用:用户没手挑过 + hint 有效 + 跟当前 role 不一致
      if (hint?.role && hint.role !== r.role && !r._roleManuallyPicked) {
        r.role = hint.role
      }
    } catch {
      /* 推荐失败不阻塞用户填表 */
    } finally {
      r._roleHintLoading = false
    }
  }

  // refreshSubmoduleHints 调后端扫 monorepo 信号(workspaces / pom modules / cmd 多入口 / services 子目录)
  // → 命中即把列表存到 r._submoduleHints,UI banner 显示"检测到 N 个子模块,一键拆分"。
  // 触发时机:scan 完成后(此时本地路径已就位)。0 命中 → 不弹 banner。
  async function refreshSubmoduleHints(r: RepoScanItem) {
    // path 优先级跟 refreshRoleHint 一致:
    //   1. _localPath 显式给 → 直接用
    //   2. 远程模式无显式覆盖 → 拼 clone 父目录(三层兜底)+ repo.name
    let path = ''
    const explicit = (r._localPath || '').trim()
    if (explicit) {
      path = explicit
    } else if (r._source === 'remote') {
      const parent = ((r._cloneTarget || '').trim() ||
        (deps.reposRootInput.value || '').trim() ||
        deps.resolvedReposRoot.value).replace(/\/+$/, '')
      if (parent && r.name.trim()) {
        path = parent + '/' + r.name.trim()
      }
    }
    if (!path) {
      r._submoduleHints = []
      r._submoduleSelection = {}
      return
    }
    try {
      const hints = await detectSubmodulesForRepo(path)
      r._submoduleHints = hints
      // 默认勾选规则:
      //   - 跟 repos[] 里已有 entry name 撞 → 默认 uncheck(已经拆过了,再勾就是重复)
      //   - 跟 _serviceEntries 已合并的入口 sub_path 撞 → 默认 uncheck
      //   - 否则 check(常规第一次拆 / 新增子模块)
      // 用户仍可手动反勾;这里只决定"banner 出现时哪些预选"。
      const existingNames = new Set(deps.repos.map(rr => rr.name.trim()).filter(Boolean))
      const mergedSubPaths = new Set(Object.values(r._serviceEntries || {}))
      const sel: Record<string, boolean> = {}
      for (const h of hints) {
        sel[h.sub_path] = !existingNames.has(h.name) && !mergedSubPaths.has(h.sub_path)
      }
      r._submoduleSelection = sel
      // banner 是否重新弹:用户上次已经处理(全部 hints 都已拆 / 已合并)→ 不弹,避免
      // 'yaml 导入 / 重扫' 反复看到一样的 banner。有任何 new(没拆 / 没合并)→ 弹。
      const allHandled = hints.every(h =>
        existingNames.has(h.name) || mergedSubPaths.has(h.sub_path),
      )
      r._submoduleHintsDismissed = allHandled
    } catch {
      r._submoduleHints = []
      r._submoduleSelection = {}
    }
  }

  // pickCloneTarget 远程模式:可选地给该仓库自定义 clone "父目录"。
  // 实际 clone 路径 = <picked>/<repo.name>(跟全局默认 reposRoot 一致)。
  // 用户选 ~/code,git clone 会创建 ~/code/<name>/,不会污染 ~/code 本身。
  //
  // 兼容老 draft:如果用户在旧版本里把 path 存成 ~/code/<name>(自己手动加了 name 一层),
  // 这里检测到末段就是 r.name 时自动剥掉一层,免得最终落到 ~/code/<name>/<name>。
  async function pickCloneTarget(r: RepoScanItem) {
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

  // pickLocalRepoDir 本地模式:用户点"选目录"挑一个已 clone 好的仓库目录。
  // 选了新目录 = 换了仓库,彻底重置身份(URL / 名字 / 手改标记 / 已扫过)再从新目录反填,
  // 然后触发扫描。不保留上一个目录的任何身份字段 —— 新目录可能 git remote 完全不一样,
  // 继承旧 URL 会误导用户。scanSingleRepo 内部还会再清 stack / service_names / 分支映射,
  // 保证扫描结果不会混着两次的数据。
  async function pickLocalRepoDir(r: RepoScanItem) {
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
  async function resolveLocalRepoPath(r: RepoScanItem, p: string) {
    const newPath = (p || '').trim()
    if (!newPath) return
    // umbrella 父行(被 child parent_repo 引用)硬约束:本地目录的 git origin 必须
    // 跟 yaml 锁定的 r.url 匹配。否则用户可能选了别的项目目录,所有 child path
    // cascade 全错位。校验失败 → 拒绝设 _localPath,toast.error 报清原因。
    const childCount = deps.repos.filter(rr => (rr.parent_repo || '').trim() === r.name.trim()).length
    if (childCount > 0 && r.url.trim()) {
      let actualOrigin = ''
      try { actualOrigin = await getRemoteURL(newPath) } catch { /* 非 git 仓库,fallthrough */ }
      if (!actualOrigin) {
        toast.error(`目录 ${newPath} 不是 git 仓库或没读到 origin。umbrella 必须指向跟锁定 URL 同源的本地副本`)
        return
      }
      if (canonicalizeGitURL(actualOrigin) !== canonicalizeGitURL(r.url)) {
        toast.error(`目录 origin (${actualOrigin}) 跟 umbrella 锁定 URL (${r.url}) 不匹配。本仓被 ${childCount} 个子模块引用,选别的项目会让 child path 全错位`)
        return
      }
    }
    // 换路径 = 换仓库,先清旧 name 对应的分支缓存 + 身份字段
    // (umbrella 父行场景下 name 锁住了,这步实际不变 r.name;普通仓库照常清重填)
    if (r.name && r.name in deps.repoBranchesMap.value) {
      delete deps.repoBranchesMap.value[r.name]
    }
    r._localPath = newPath
    if (childCount === 0) {
      // 普通仓 / umbrella 子模块切目录 = 换仓库,清身份等会儿从新路径反填
      r.url = ''
      r.name = ''
      r._nameManual = false
    }
    r._scanned = false
    r._scannedSource = ''
    // 清空旧 submodule hints,避免上个仓库的检测结果残留
    r._submoduleHints = undefined
    if (childCount === 0) {
      try {
        const remote = await getRemoteURL(newPath)
        if (remote) {
          r.url = remote
          r.name = deps.deriveRepoName(remote)
        }
      } catch { /* 不是 git 仓库 / 没 origin,容忍继续 */ }
    }
    if (!r.name) {
      const parts = newPath.split(/[\\/]/).filter(Boolean)
      r.name = parts[parts.length - 1] || ''
    }
    // 选完路径立刻跑一次 monorepo 检测(不等 scanSingleRepo 跑完,monorepo 信号是文件结构,
    // 跟 stack/分支扫描独立)。给用户即时反馈,如果是 monorepo,banner 立刻出现。
    refreshSubmoduleHints(r)
    await scanSingleRepo(r)
  }

  // scanSingleRepo 主扫描:bridgeAnalyzeV2 → stack + service_names + 分支列表 + 配置中心识别。
  // 主流程:
  //   1) 入参校验(name 必填;remote 要 url;local 要 _localPath)
  //   2) 拼 repoPaths(local 直接用 _localPath;remote 用 resolveCloneDest);
  //   3) 远程模式拼 effectiveRoot;无 _cloneTarget 时 fallback 到 reposRootInput / resolvedReposRoot
  //   4) 调 bridgeAnalyzeV2,反填 stack / service_names / 分支
  //   5) 顺手 refreshRoleHint + refreshSubmoduleHints
  async function scanSingleRepo(r: RepoScanItem) {
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

    // 扫描前预检本地路径存在性(主要兜 umbrella 子模块代码被 rm 的场景):
    // umbrella 子模块的 _localPath 是 splitMonorepo 预填的 <umbrella>/<sub>,如果用户
    // 把 <umbrella>/<sub> 目录删了,这里 path 不存在 → backend 会返"path missing skipped"
    // 模糊错误。前端先 check 一下,umbrella 子模块给明确指引、普通仓给重选目录提示。
    if (r._localPath?.trim()) {
      const exists = await pathExists(r._localPath.trim())
      if (!exists) {
        if (r.parent_repo && r.parent_repo.trim()) {
          r._scanError = `代码缺失(${r._localPath} 不存在)。请先去 umbrella 行 "${r.parent_repo}" 点 "重新同步扫描",会自动 git submodule update 拉回本子模块代码`
        } else {
          r._scanError = `本地路径已不存在(${r._localPath}),请重选目录`
        }
        return
      }
    }

    // 构造 RepoPaths:仅这一个仓库的路径覆盖;效用上同 AnalyzeV2 的 per-repo 映射。
    // _localPath 显式给(本地模式 / splitMonorepo 远程子模块预填的 umbrella 内位置)→ 跳过 clone 直接扫
    const repoPaths: Record<string, string> = {}
    if (r._localPath?.trim()) {
      repoPaths[r.name] = r._localPath.trim()
    } else if (r._source === 'remote') {
      const dest = resolveCloneDest(r)
      if (dest) repoPaths[r.name] = dest
    }
    // 远程模式 + 没填 _localPath 才需要 autoClone;有显式 _localPath 直接用,免 clone
    const autoClone = r._source === 'remote' && !r._localPath?.trim()
    // 远程模式没填本仓库 clone 父目录时需要 effectiveRoot 来拼 ReposRoot/Name
    const effectiveRoot = deps.reposRootInput.value.trim() || deps.resolvedReposRoot.value
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
    if (r.name in deps.repoBranchesMap.value) {
      delete deps.repoBranchesMap.value[r.name]
    }
    try {
      const yamlText = deps.generateYAML()
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
      //
      // 多服务场景(rpt.service_names.length > 1):**不**自动把全部子服务名塞进 service_names —
      // 这跟 refreshSubmoduleHints 弹的"合并为本仓 N 个服务名"banner 冲突(banner 等用户显式决定,
      // analyzer 抢先填 = banner 形同虚设,Step 5 立刻看到一堆未确认的服务名)。多服务时按"单一
      // 仓 = 单一服务"兜底,用户决定 → banner 的"合并"或"拆分"按钮接管。
      const rpt = (res.report?.repos || []).find(rr => rr.name === r.name)
      if (deps.isServiceRole(r.role)) {
        if (rpt?.service_names?.length === 1) {
          // 单服务场景:直接填,不弹 banner
          r.service_names = rpt.service_names[0]
        } else if (rpt?.service_names && rpt.service_names.length > 1) {
          // 多服务场景:留给 refreshSubmoduleHints 的 banner;此处按 r.name 兜底,
          // 用户点"合并为本仓 N 个服务名"按钮才把 N 个服务名填进 r.service_names。
          if (!r.service_names.trim() && r.name) r.service_names = r.name
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
        deps.repoBranchesMap.value[r.name] = hit.branches
        for (const env of deps.environments) {
          if (!env.id) continue
          const mapped = deps.pickBranchForEnv(env, hit.branches)
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
      // umbrella 扫成功 → 级联回填所有 child 的 _localPath:
      // <umbrella resolved>/<child.parent_path 或 child.name>。
      // 场景:新机器导入 yaml,umbrella 没本地路径 → 子模块 _localPath 也空。用户点 umbrella
      // 同步扫描 → 后端 git clone + git submodule update --recursive 把子模块代码拉下来 →
      // 这里把子模块行的 _localPath 自动指向真实位置,不需要用户再对每个子模块手动操作。
      const umbrellaPath = (r._localPath || '').trim() ||
        (r._source === 'remote' ? resolveCloneDest(r) : '') ||
        ((deps.reposRootInput.value || '').trim() ||
          deps.resolvedReposRoot.value).replace(/\/+$/, '') + '/' + r.name.trim()
      if (umbrellaPath) {
        for (const child of deps.repos) {
          if ((child.parent_repo || '').trim() === r.name.trim()) {
            const sub = ((child.parent_path || '').trim() || child.name.trim()).replace(/^\/+/, '')
            const newPath = umbrellaPath.replace(/\/+$/, '') + '/' + sub
            if (child._localPath !== newPath) {
              // path 变了 → 旧扫描结果(stack/分支/服务名)可能 stale,清扫描态让 UI
              // 提示用户重扫。同 path 反复 scan 不影响(no-op,_scanned 保留)
              child._localPath = newPath
              child._scanned = false
              child._scannedSource = ''
            }
          }
        }
      }
    } catch (e: any) {
      r._scanError = String(e?.message || e)
    } finally {
      r._scanning = false
    }
  }

  return {
    resolveCloneDest,
    refreshRoleHint,
    refreshSubmoduleHints,
    pickCloneTarget,
    pickLocalRepoDir,
    resolveLocalRepoPath,
    scanSingleRepo,
  }
}
