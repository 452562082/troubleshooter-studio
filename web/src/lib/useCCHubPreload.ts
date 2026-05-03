// useCCHubPreload —— Step 5 配置中心(nacos / apollo / consul)真实预加载 + 启发式 auto-match。
//
// 接 useCCHubState(state)。包含:
//   - buildPreloadPayload    从 ccCredInputs 抽 nacos/apollo/consul 各家凭证字段(含校验 missing)
//   - runCCHubPreload        两阶段预加载:NamespacesOnly + autoMatch + 精确拉
//   - loadConfigsForEnv      精确拉某 env 下某 namespace 的 configs
//   - reloadEnvNamespace     用户切 namespace 触发的重拉
//   - autoMatchNamespace     env.id → namespace 启发式匹配
//   - autoMatchDataID        service → dataId 启发式 3-pass 匹配
//   - autoFillSelections     预加载完跑一次:填空 namespace + 每服务 dataId(已选不覆盖)
//   - onNamespaceChanged     用户切 ns → 清旧 dataId 选 + 重拉(有凭证)/ autoFill(没凭证)
//   - onDataIdChanged        用户选 dataId → 同步记下对应 group(yaml 生成时要一起写)
//
// crossCheckImportedConfigSource(导入 yaml 后跨真实配置中心校验)还跟 import 流程多块状态
// 交织,留在 InitPage。
import { preloadConfigCenter, type CCHubEntry, type CCHubNamespace } from './bridge'
import { isDesktop } from './bridge/shared'
import { pushLog } from './logStore'
import { toast } from './toast'
import { ccKeyFor, svcKey } from './yamlShared'
import type { CCHubEnvState } from './useCCHubState'

export interface UseCCHubPreloadDeps {
  /** useCCHubState 暴露的 reactive */
  ccHubStateByEnv: Record<string, CCHubEnvState>
  /** Step 5 凭证输入 map(key 走 ccKeyFor("cc:<type>:<env>:<field>")) */
  ccCredInputs: Record<string, string>
  /** 主源 type 的 computed.value 取值器(rerun 时实时拿 type) */
  getPrimaryConfigCenterType: () => string
  /** Step 5 用户挑的 env → namespace */
  envNamespaces: Record<string, string>
  /** Step 5 用户挑的 service → dataId / group */
  serviceConfigSel: Record<string, string>
  serviceConfigGroup: Record<string, string>
  /** 当前所有服务名(computed.value) */
  allServiceNames: { value: readonly string[] }
  /** 取服务对应的源 type;multi-source 下区分主源/副源 */
  getServiceSource: (svc: string) => string
  /** 给 env 取可选 namespace 列表(autoFillSelections 用) */
  namespacesFor: (envID: string) => CCHubNamespace[]
  /** 给 env+namespace 取可选 entries(autoMatchDataID 用) */
  entriesForNamespace: (envID: string, nsID: string) => CCHubEntry[]
  /** "由具体到泛化"的服务名候选(loki / dataId / kuboard 多源共用) */
  serviceMatchKeys: (svc: string) => string[]
  /** 段对齐前缀判定:loc 等 cand,或以 cand+分隔符开头 */
  startsAtBoundary: (loc: string, cand: string) => boolean
}

interface PreloadPayload {
  type: string
  addr: string
  username: string
  password: string
  token: string
  namespace: string
  app_id: string
  valid: boolean
  missing: string[]
}

export function useCCHubPreload(deps: UseCCHubPreloadDeps) {
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
  // serviceMatchKeys 退化策略 + 3-pass(段对齐+env / 段对齐 / fuzzy)
  function autoMatchDataID(envID: string, svc: string, nsID: string): { dataId: string, group: string } {
    const entries = deps.entriesForNamespace(envID, nsID)
    if (entries.length === 0) return { dataId: '', group: '' }
    const candidates = deps.serviceMatchKeys(svc)
    const envLower = envID.toLowerCase()

    // Pass 1:locator 段对齐前缀 + 含 env 关键字 —— 最强信号
    for (const cand of candidates) {
      const hit = entries.find(e => {
        const loc = e.locator.toLowerCase()
        return deps.startsAtBoundary(loc, cand) && loc.includes(envLower)
      })
      if (hit) return { dataId: hit.locator, group: hit.group || '' }
    }
    // Pass 2:locator 段对齐前缀(不要求含 env)—— 适配 <service>.yaml 共享配置
    for (const cand of candidates) {
      const hit = entries.find(e => deps.startsAtBoundary(e.locator.toLowerCase(), cand))
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

  // 预加载成功后触发:帮用户把 namespace + 每个服务的 dataId 猜一遍 —— 只填还没填的。
  function autoFillSelections(envID: string) {
    const nsList = deps.namespacesFor(envID)
    if (nsList.length === 0) return
    if (!deps.envNamespaces[envID]) {
      deps.envNamespaces[envID] = autoMatchNamespace(envID, nsList)
    }
    // 只为"用户已勾选走当前 env 的源(主源)"的服务自动填 dataId;没勾选的服务跳过。
    const nsID = deps.envNamespaces[envID] || ''
    const primaryType = deps.getPrimaryConfigCenterType()
    for (const svc of deps.allServiceNames.value) {
      if (deps.getServiceSource(svc) !== primaryType) continue
      const k = svcKey(envID, svc)
      if (deps.serviceConfigSel[k]) continue // 已手挑 → 不覆盖
      const { dataId, group } = autoMatchDataID(envID, svc, nsID)
      if (dataId) {
        deps.serviceConfigSel[k] = dataId
        deps.serviceConfigGroup[k] = group
      }
    }
  }

  // 按 env 取当前输入组合(从 ccCredInputs 抽)。kuboard / env-vars / none 不走远程预读。
  function buildPreloadPayload(envID: string): PreloadPayload {
    const type = deps.getPrimaryConfigCenterType()
    const miss: string[] = []
    const get = (field: string) => (deps.ccCredInputs[ccKeyFor(type, envID, field)] || '').trim()

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
      namespace = ''
      if (!addr) miss.push('Portal URL')
      if (!token) miss.push('token')
      if (!appID) miss.push('App ID')
    } else if (type === 'consul') {
      addr = get('host')
      token = get('token')
      namespace = ''
      if (!addr) miss.push('consul host')
    }
    return {
      type, addr, username, password, token, namespace, app_id: appID,
      valid: miss.length === 0, missing: miss,
    }
  }

  // 两阶段预加载:Step 1 NamespacesOnly 列 namespaces;Step 2 按 env.id 启发式匹到 → 拉那个 namespace。
  // 匹不到就让用户在 UI 手选。
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
    deps.ccHubStateByEnv[envID] = { status: 'loading' }
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
        deps.ccHubStateByEnv[envID] = {
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
      deps.ccHubStateByEnv[envID] = { status: 'error', error: '拉取失败,详见日志' }
      pushLog('cchub', 'error', `[${envID}] 预加载失败: ${msg}`,
        { envID, type: payload.type, addr: payload.addr })
      toast.error(`${envID} 预加载失败,详见左侧「日志」`)
    }
  }

  // 精确拉某 env 下某 namespace 的 configs(第二阶段,或用户后续切 namespace 触发的重拉)。
  async function loadConfigsForEnv(
    envID: string,
    nsID: string,
    allNamespaces: CCHubNamespace[],
    payload: PreloadPayload,
  ) {
    deps.ccHubStateByEnv[envID] = { status: 'loading' }
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
      deps.ccHubStateByEnv[envID] = {
        status: 'ok',
        entries: r.entries || [],
        namespaces: r.namespaces || allNamespaces,
        notes: r.notes || [],
        loadedAt: Date.now(),
      }
      // 把匹到/选到的 namespace 写进 envNamespaces(autoFill 也需要它)
      deps.envNamespaces[envID] = nsID
      pushLog('cchub', 'info',
        `[${envID}] namespace=${nsID || 'public'} 拉到 ${r.entries?.length || 0} 条配置`,
        { envID, namespace: nsID })
      for (const n of r.notes || []) pushLog('cchub', 'info', `[${envID}] ${n}`, { envID })
      // 清掉 localStorage 遗留的脏 serviceConfigSel:如果之前存的 dataId 不在新 namespace
      // 的 entries 里,清空它;避免 UI 显示空 select(v-model 指向不存在的 option)。
      const validLocators = new Set((r.entries || []).map(e => e.locator))
      for (const svc of deps.allServiceNames.value) {
        const k = svcKey(envID, svc)
        if (deps.serviceConfigSel[k] && !validLocators.has(deps.serviceConfigSel[k])) {
          delete deps.serviceConfigSel[k]
          delete deps.serviceConfigGroup[k]
        }
      }
      // 只对当前 env 跑自动匹配,其他 env 要他们自己扫
      autoFillSelections(envID)
      toast.success(`${envID}: 拉到 ${r.entries?.length || 0} 条配置`)
    } catch (e: any) {
      const msg = String(e?.message || e)
      deps.ccHubStateByEnv[envID] = { status: 'error', error: '拉取失败,详见日志' }
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
    const st = deps.ccHubStateByEnv[envID]
    const allNs = st?.namespaces || []
    await loadConfigsForEnv(envID, nsID, allNs, payload)
  }

  // 用户切 namespace → 清空这个 env 下所有 service 的 dataId 选择。
  // 有凭证 → 精确重拉该 namespace 的 configs;没凭证 → 用已有数据重跑 autoFill。
  function onNamespaceChanged(envID: string, newNsID: string) {
    for (const svc of deps.allServiceNames.value) {
      const k = svcKey(envID, svc)
      delete deps.serviceConfigSel[k]
      delete deps.serviceConfigGroup[k]
    }
    deps.envNamespaces[envID] = newNsID
    const payload = buildPreloadPayload(envID)
    if (payload.valid && isDesktop()) {
      void reloadEnvNamespace(envID, newNsID)
    } else {
      autoFillSelections(envID)
    }
  }

  // 用户选 dataId → 同步记下对应的 group(生成 yaml 时要一起写)
  function onDataIdChanged(envID: string, svc: string) {
    const nsID = deps.envNamespaces[envID] || ''
    const chosen = deps.serviceConfigSel[svcKey(envID, svc)]
    if (!chosen) {
      delete deps.serviceConfigGroup[svcKey(envID, svc)]
      return
    }
    const entry = deps.entriesForNamespace(envID, nsID).find(e => e.locator === chosen)
    deps.serviceConfigGroup[svcKey(envID, svc)] = entry?.group || ''
  }

  return {
    autoMatchNamespace,
    autoMatchDataID,
    autoFillSelections,
    buildPreloadPayload,
    runCCHubPreload,
    loadConfigsForEnv,
    reloadEnvNamespace,
    onNamespaceChanged,
    onDataIdChanged,
  }
}
