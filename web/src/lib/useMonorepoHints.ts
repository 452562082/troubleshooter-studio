// useMonorepoHints —— Step 4 Monorepo 子模块 banner 操作:拆 / 合并 / 选 / 数。
//
// scanSingleRepo / refreshSubmoduleHints 跑完后,后端 DetectSubmodules 给出 _submoduleHints —
// 一组"这个仓库可能是 monorepo,有 N 个子模块"的提示。本 composable 处理用户对这条 banner
// 的所有操作:
//
//   - toggleSubmodulePick(r, subPath, picked)  banner 复选框勾/取消
//   - pickedSubmoduleCount(r)                  banner 拆分按钮数量 + disabled
//   - submodulePathFor(parent, subPath)        banner 列每条子模块时显示父仓本地路径 + sub_path
//   - isGitSubmodulesHints(hints)              判定一组 hints 是 .gitmodules 路径(每条带 url)
//                                              还是 workspaces / cmd-multi / services-dir / pom-modules
//   - qualifyServiceName(repo, entry)          cmd 入口名加 `<repo>-` 前缀消歧义
//   - mergeMonorepoIntoServices(idx)           合并到当前 repo 的 service_names + _serviceEntries(同仓多入口)
//   - splitMonorepo(idx)                       拆成 N 个独立 RepoItem 行(.gitmodules 真子模块)
//
// 拆分后异步对每个新行调 listBranchesForRepo 拉真实分支列表,落 repoBranchesMap[name]。
import type { Ref } from 'vue'
import { listBranchesForRepo } from './bridge'
import type { RepoScanItem } from './useRepoScan'

// RepoScanItem 上没有 config_source(它是 InitPage RepoItem 上的 multi-source 字段);
// useMonorepoHints splitMonorepo 要把父仓的 config_source 透传给新行,所以本地扩一层。
type MonorepoRepoItem = RepoScanItem & { config_source?: string }

export interface UseMonorepoHintsDeps {
  /** 当前所有仓库行(reactive 数组,直接 splice / mutate) */
  repos: MonorepoRepoItem[]
  /** 当前所有环境(为新行重算 env_branches 用) */
  environments: { id: string }[]
  /** 仓库 → 真实分支列表 cache(splitMonorepo 异步落进去) */
  repoBranchesMap: Ref<Record<string, string[]>>
  /** 远程模式仓库的 clone 落点(从 useRepoScan 透出) */
  resolveCloneDest: (r: MonorepoRepoItem) => string
  /** 全局默认 clone 父目录(用户输入,可空)+ 兜底解析值,跟 useRepoScan 的 deps 同名同义。
   * 远程模式 parent._cloneTarget 空时算实际 clone 落点要走这套兜底。 */
  reposRootInput: Ref<string>
  resolvedReposRoot: Ref<string>
  /** 启发式 env→branch 映射(同 scanSingleRepo) */
  pickBranchForEnv: (env: { id: string }, branches: string[]) => string
  /** 业务服务角色判定;只有 backend / gateway / middleware / admin 当 service */
  isServiceRole: (role?: string) => boolean
  /** 兜底空 RepoItem 模板(splitMonorepo 派生新行用) */
  makeEmptyRepo: () => MonorepoRepoItem
}

export function useMonorepoHints(deps: UseMonorepoHintsDeps) {
  // toggleSubmodulePick 用户在 banner 里勾/取消勾某个子模块,影响后续 splitMonorepo 展开哪些。
  function toggleSubmodulePick(r: MonorepoRepoItem, subPath: string, picked: boolean) {
    if (!r._submoduleSelection) r._submoduleSelection = {}
    r._submoduleSelection[subPath] = picked
  }

  // pickedSubmoduleCount banner 拆分按钮上显示数量 + disable 用。
  // 排除"已经拆过"的(name 撞 repos[] 已有 entry)—— 跟 splitMonorepo 防御性去重逻辑
  // 一致,按钮文案 / disabled 状态准确反映"真正会新增几行"。
  function pickedSubmoduleCount(r: MonorepoRepoItem): number {
    if (!r._submoduleHints) return 0
    const sel = r._submoduleSelection || {}
    const existingNames = new Set(
      deps.repos.map(rr => rr.name.trim()).filter(n => n && n !== r.name.trim()),
    )
    return r._submoduleHints
      .filter(h => sel[h.sub_path] !== false)
      .filter(h => !existingNames.has(h.name))
      .length
  }

  // isSubmoduleAlreadySplit:某个 hint 对应的 child 已经在 repos[] 里(之前已拆过)。
  // banner 用这个给行加"已拆"标识 + 禁勾选,避免用户误以为"刚扫到的新东西"反复拆出来。
  function isSubmoduleAlreadySplit(r: MonorepoRepoItem, hintName: string): boolean {
    if (!hintName) return false
    return deps.repos.some(rr => rr.name.trim() === hintName && rr.name.trim() !== r.name.trim())
  }

  // submodulePathFor 拼"父仓本地路径 + sub_path"得到子模块实际代码位置。
  // banner 列每条子模块时显示 + 已 split 的条目下方也显示,让用户能确认 routing skill 拿到的是哪个目录。
  function submodulePathFor(parent: MonorepoRepoItem, subPath: string): string {
    const base = (parent._localPath || '').replace(/\/+$/, '')
    const rel = subPath.replace(/^\/+/, '')
    if (!base) return rel
    if (!rel) return base
    return base + '/' + rel
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
    const parent = deps.repos[parentIdx]
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
    const parent = deps.repos[parentIdx]
    if (!parent || !parent._submoduleHints || parent._submoduleHints.length === 0) return
    const branches = { ...parent.env_branches } // 共用父仓库的 env_branches(同一个 git 仓库分支策略一致)
    const sel = parent._submoduleSelection || {}
    // 1. 只展开勾选了的子模块;空选状态(理论上不可能)兜底全选
    // 2. **防御性去重**:跟 repos[] 里已有的 name 撞 → 跳过(用户即便手动勾了已拆过的,
    //    也不重复展开。banner 默认应该已经反勾这些,这条是最后一道闸)
    const existingNames = new Set(
      deps.repos.map(rr => rr.name.trim()).filter(n => n && n !== parent.name.trim()),
    )
    const picked = parent._submoduleHints
      .filter(h => sel[h.sub_path] !== false)
      .filter(h => !existingNames.has(h.name))
    if (picked.length === 0) return
    // 父仓的真实磁盘路径:
    //   - local 模式 → parent._localPath
    //   - remote 模式 → scan 完只设 _scanned/_scannedSource,_localPath 为空,
    //     算 clone 落点 = (parent._cloneTarget / 全局默认 reposRootInput / 兜底
    //     resolvedReposRoot)+ parent.name。**之前只走 resolveCloneDest 看 _cloneTarget,
    //     用户没填 _cloneTarget 靠全局默认时返空 → children 拿到空 _localPath**(图里
    //     "尚未选择目录"的根因),跟 refreshRoleHint / refreshSubmoduleHints 同款 bug。
    let parentLocalBase = ''
    if (parent._source === 'remote') {
      const remoteParent = ((parent._cloneTarget || '').trim() ||
        (deps.reposRootInput.value || '').trim() ||
        deps.resolvedReposRoot.value).replace(/\/+$/, '')
      if (remoteParent && parent.name.trim()) {
        parentLocalBase = remoteParent + '/' + parent.name.trim()
      }
    } else {
      parentLocalBase = (parent._localPath || '').replace(/\/+$/, '')
    }
    const newRows: MonorepoRepoItem[] = picked.map(h => {
      // .gitmodules 路径下,h.url 非空 = 真"独立 git repo + 子目录共置";其它 monorepo 路径
      // h.url 为空 = "同一仓库子目录"。两者展开后形态不同:
      //   独立 git repo:每行用自己的 url + 自己的本地路径(父仓本地 + 子模块名)+ 无 sub_path
      //   同仓子目录:每行用父仓 url + 父仓本地路径 + 各自 sub_path
      const isIndependentRepo = !!h.url
      // .gitmodules 子模块 → 父仓本地路径 + sub_path(代码已在磁盘);
      // 同仓子目录 → 共用父仓的本地路径(parentLocalBase 已兼容 remote 模式的 resolveCloneDest)。
      const ownLocalPath = isIndependentRepo && parentLocalBase
        ? parentLocalBase + '/' + h.sub_path.replace(/^\/+/, '')
        : (parent._localPath || parentLocalBase)
      // 子模块的 source 模式:跟父仓保持一致(remote 父 → child 也 remote)。
      //   - 独立子模块 + parent 是 remote:子模块 url 是各自的(.gitmodules 里的 h.url),
      //     parent_repo 在 yaml 里声明 umbrella → 子模块关系,部署时走 analyzerpipe 的
      //     umbrella 继承编排(parent 先 clone,子模块 URL clone 到 <parent>/<parent_path>);
      //     wizard scan 本机优化:_localPath 预填 parentLocalBase/<sub_path>,refreshRoleHint
      //     拿到立刻能用,免重复 clone。
      //   - 独立子模块 + parent 是 local:同上 source 跟 parent 走 'local',
      //     _localPath 同样指向 parentLocalBase/<sub_path>(代码已在磁盘上)。
      //   - 同仓子目录(isIndependentRepo=false):跟父仓共用 _source / _localPath / url,
      //     由 sub_path 区分,父仓什么模式继续什么模式。
      const ownSource: 'local' | 'remote' = parent._source || 'remote'
      return {
        ...deps.makeEmptyRepo(),
        name: h.name,
        url: isIndependentRepo ? (h.url as string) : parent.url,
        stack: h.stack || parent.stack || 'go',
        role: h.role || 'backend',
        // sub_path 单义"本 URL clone 内的子目录":
        //   - 独立仓(commerce.git):仓根就是 service 代码,sub_path=""
        //   - 同仓子目录:跟父仓共用 URL,各行用 h.sub_path 区分
        sub_path: isIndependentRepo ? '' : h.sub_path,
        // 独立子模块:声明 parent_repo + parent_path,另一台机器拿 yaml 能恢复 umbrella →
        // 子模块关系,走 analyzerpipe 的 umbrella 继承编排
        // (parent 先 clone,本仓 URL clone 到 <parent>/<parent_path 或 name>)。
        // parent_path = h.sub_path(.gitmodules 里 commerce 子模块的挂载位置);
        // 跟 name 一致时省略 parent_path 字段(yaml 自动用 name 兜底)。
        ...(isIndependentRepo
          ? {
            parent_repo: parent.name,
            ...(h.sub_path && h.sub_path !== h.name ? { parent_path: h.sub_path } : {}),
          }
          : {}),
        // service_names 默认 = 子模块名,但只对"业务服务"角色赋值;frontend / common-lib /
        // mobile / infra / docs 这类不算服务,留空更准确(否则会被后续配置中心 / 数据层
        // 扫描当成 service ID 误用)。
        service_names: deps.isServiceRole(h.role) ? h.name : '',
        env_branches: { ...branches },
        config_source: parent.config_source,
        _source: ownSource,
        _localPath: ownLocalPath,
        _scanned: true,
        _scannedSource: parent._scannedSource,
        // 拆分后 role hint 已经从 monorepo_scan 拿到了,直接灌进去(用户一眼看到为啥推这 role)
        _roleHint: { role: h.role, reason: h.reason },
      }
    })
    // 拆分行为按"独立子模块" vs "同仓子目录"分两路:
    //   - 独立子模块(isIndependentRepo=true,.gitmodules 真子模块):**保留 umbrella 行**,
    //     在它后面插入 N 个 child(每个带 parent_repo=parent.name)。yaml 里要有 umbrella
    //     entry 才能让其他机器恢复"先 clone umbrella → child clone 进去"的部署编排。
    //   - 同仓子目录(isIndependentRepo=false,workspaces / cmd-multi 等共用 URL):**删 umbrella 行**,
    //     N 个 child 共享同 URL,各自 sub_path 区分,umbrella 行变成冗余。
    const allIndependent = picked.every(h => !!h.url)
    if (allIndependent) {
      // umbrella 留下,N 行紧跟其后。
      // umbrella 拆完变成"纯容器",服务都归子模块了,清自己的 service_names +
      // service_entries(否则 Step 6 / k8s 服务下拉等会把 umbrella 当独立服务列出来,
      // 让用户疑惑"我都拆了为什么还在")。role 不动 —— umbrella 是不是 infra/common-lib
      // 还是 backend 由用户决定,不替他做主。
      parent.service_names = ''
      parent._serviceEntries = undefined
      // 拆完关 banner —— 否则面板还在,用户能再点一次按钮重复拆出 N 行(用户截图反馈)。
      // 用户想重新看 banner → 重新扫一次 umbrella(refreshSubmoduleHints 会重置 dismissed)。
      parent._submoduleHintsDismissed = true
      deps.repos.splice(parentIdx + 1, 0, ...newRows)
    } else {
      // 老行为:全替换。父行没了 banner 自然不再显示(没载体)。
      deps.repos.splice(parentIdx, 1, ...newRows)
    }
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
        deps.repoBranchesMap.value[row.name] = branches
        // 已经被 splitMonorepo 设过的 env_branches(从父仓继承)如果不在新分支列表里,
        // 按启发式重新挑一次 —— 同 scanSingleRepo 的逻辑(pickBranchForEnv)。
        for (const env of deps.environments) {
          if (!env.id) continue
          const cur = (row.env_branches[env.id] || '').trim()
          if (cur && branches.includes(cur)) continue // 当前值在真实列表里 → 保留
          const mapped = deps.pickBranchForEnv(env, branches)
          if (mapped) row.env_branches[env.id] = mapped
        }
      }).catch(() => { /* 失败保持空,UI fallback text input */ })
    }
  }

  return {
    toggleSubmodulePick,
    pickedSubmoduleCount,
    isSubmoduleAlreadySplit,
    submodulePathFor,
    isGitSubmodulesHints,
    qualifyServiceName,
    mergeMonorepoIntoServices,
    splitMonorepo,
  }
}
