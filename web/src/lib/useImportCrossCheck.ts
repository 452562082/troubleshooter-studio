// useImportCrossCheck —— applyImport 反填后跑的"真实端交叉校验"四件套。
//
// applyImport 把 yaml 里的字段(namespace / dataId / cluster / configmap / grafana DS UID 等)
// 反填进 ccHubStateByEnv / kuboardStateByEnv / 等"合成态(synthesized=true)"reactive 容器,
// 但 yaml 里的值可能跟真实远端对不上(被删了 / 重命名 / 切实例 / 旧 yaml)。本 composable 在反填
// 完成后异步触发真实校验:
//   - crossCheckImportedConfigSource(envID)         nacos / apollo / consul 通用,看 envNamespaces +
//                                                   serviceConfigSel 反填的值在不在
//   - crossCheckImportedKuboard(envID, sourceType)  kuboard 主源/副源:看 kuboardSvcMap 三元组在不在
//   - crossCheckImportedObservability()             grafana DS UID + k8s_runtime cluster/namespace
//   - runImportCrossChecks(cc)                      上面 3 个的 orchestrator,按主/副源类型调度
//
// 失败 / 凭证不全 / 非桌面 app 都静默跳过 —— 用户后续仍可手动点"📥 拉取勾选服务的配置"重试。
import type { Ref, ComputedRef } from 'vue'
import { kuboardListResources, listGrafanaDatasources, preloadConfigCenter } from './bridge'
import { isDesktop } from './bridge/shared'
import { pushLog } from './logStore'
import { toast } from './toast'
import type { CCHubEnvState } from './useCCHubState'
import type { KuboardResourceState } from './credFields'
import type { LokiMappingPerEnv } from './useLokiMappingState'

interface KuboardSvcLocator { cluster?: string; namespace?: string; configmap?: string }
interface ToolSpecLike { key: string }

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

export interface UseImportCrossCheckDeps {
  // reactive 容器(passed-by-ref,直接 mutate)
  envNamespaces: Record<string, string>
  serviceConfigSel: Record<string, string>
  ccHubStateByEnv: Record<string, CCHubEnvState>
  kuboardSvcMap: Record<string, KuboardSvcLocator>
  kuboardStateByEnv: Record<string, KuboardResourceState>
  sourceCreds: Record<string, { creds: Record<string, Record<string, string>>; rawExtra?: Record<string, unknown> }>
  enabledObservability: Record<string, boolean>
  grafanaDsUidByObsEnv: Record<string, string>
  k8sRuntimeEnvLoc: Record<string, { cluster?: string; namespace?: string }>
  toolInputs: Record<string, string>
  importInProgress: Ref<boolean>

  // computed / refs
  environments: { id: string }[]
  activeSourceTypes: ComputedRef<readonly string[]>
  configCenterType: { value: string }

  // 兄弟 composable 暴露的 helpers
  buildPreloadPayload: (envID: string) => PreloadPayload
  runCCHubPreload: (envID: string) => Promise<void> | void
  runKuboardPreloadFromSource: (sourceType: string, envID: string) => Promise<void>
  persistKuboardState: () => void
  scheduleObsProbe: (toolKey: string, envID: string) => void
  lokiAuthFor: (envID: string) => {
    grafana_url: string; api_key: string; user: string; pass: string; loki_url: string; ds_uid: string
  }
  getLokiMapping: (envID: string) => LokiMappingPerEnv

  // 工具规范 + key helpers(InitPage 本地定义,通过 deps 注入)
  OBS_TOOL_SPECS: readonly ToolSpecLike[]
  toolKeyFor: (cat: 'obs' | 'ds', tool: string, envID: string, field: string) => string
  obsGrafanaDsKey: (tool: string, envID: string) => string
}

export function useImportCrossCheck(deps: UseImportCrossCheckDeps) {
  // ── 1. nacos / apollo / consul 通用交叉校验 ──
  // applyImport 用 yaml service_map 给 ccHubStateByEnv 合成了"虚假 ok"(synthesized=true),
  // 但 yaml 里写的 namespace / dataId / kv 前缀 可能跟真实配置中心不一致。本函数:
  //   1. 用 yaml 里的 namespace 直接调真实配置中心拉 entries(同时返回 namespaces 列表)
  //   2. 比对:yaml namespace 在不在真实 namespaces / yaml locator 在不在真实 entries
  //   3. 用真实数据替换 ccHubStateByEnv(下拉选项变成全量真实列表)
  //   4. 缺失的 yaml 值在 notes 里登记 + pushLog warn + toast 提醒
  //
  // 不破坏选中值:envNamespaces / serviceConfigSel 仍保留 yaml 反填的值。如果该值在真实
  // 配置中心不存在,UI 下拉会因为 v-model 找不到 option 而显示"空"——这时用户能立刻看到
  // 不一致(看起来选了某条但下拉空白)+ toast 警告 + 日志列出具体哪些缺失。
  async function crossCheckImportedConfigSource(envID: string): Promise<void> {
    const payload = deps.buildPreloadPayload(envID)
    const ctype = payload.type
    // 用户友好的术语,UI / log 文案按 type 切换
    const termLocator = ctype === 'nacos' ? 'dataId' : ctype === 'apollo' ? 'namespace name' : ctype === 'consul' ? 'kv key' : 'locator'
    const termNs = ctype === 'apollo' ? 'apollo env' : ctype === 'consul' ? 'kv 前缀' : 'namespace'
    if (!payload.valid) {
      pushLog('cchub', 'info',
        `[applyImport] ${envID} 跳过真实 ${ctype} 校验(凭证不全): ${payload.missing.join(', ')}`,
        { envID })
      return
    }
    const yamlNs = (deps.envNamespaces[envID] || '').trim()
    if (!yamlNs) {
      // yaml 没给这个 env 写 namespace —— 直接走标准 runCCHubPreload(autoMatch + load)
      await deps.runCCHubPreload(envID)
      return
    }
    // 收集 yaml 写过的 (svc, locator) 做交叉校验
    const yamlLocators = new Map<string, string>()
    for (const k of Object.keys(deps.serviceConfigSel)) {
      if (!k.startsWith(envID + '::')) continue
      const svc = k.slice(envID.length + 2)
      const loc = (deps.serviceConfigSel[k] || '').trim()
      if (svc && loc) yamlLocators.set(svc, loc)
    }
    // 失败时复原用,先快照合成态
    const prevSynth = deps.ccHubStateByEnv[envID]
    deps.ccHubStateByEnv[envID] = { status: 'loading' }
    try {
      const r = await preloadConfigCenter({
        type: ctype as 'nacos' | 'apollo' | 'consul',
        addr: payload.addr,
        username: payload.username,
        password: payload.password,
        token: payload.token,
        namespace: yamlNs,
        app_id: payload.app_id,
      })
      const realLocators = new Set((r.entries || []).map(e => e.locator))
      const realNsIDs = new Set((r.namespaces || []).map(n => n.id))
      const missingLocators: Array<[string, string]> = []
      for (const [svc, loc] of yamlLocators) {
        if (!realLocators.has(loc)) missingLocators.push([svc, loc])
      }
      const nsMissing = realNsIDs.size > 0 && !realNsIDs.has(yamlNs)
      const notes = [...(r.notes || [])]
      if (nsMissing) {
        notes.push(`⚠ yaml 中 ${termNs}=${yamlNs} 在真实 ${ctype} 不存在(共 ${realNsIDs.size} 个 ${termNs})`)
      }
      if (missingLocators.length > 0) {
        notes.push(`⚠ yaml 中以下 ${termLocator} 在 ${termNs}=${yamlNs} 不存在: ${missingLocators.map(([s, d]) => `${s}→${d}`).join(', ')}`)
      }
      deps.ccHubStateByEnv[envID] = {
        status: 'ok',
        entries: r.entries || [],
        namespaces: r.namespaces || [],
        notes,
        loadedAt: Date.now(),
        synthesized: false,
      }
      pushLog('cchub', 'info',
        `[applyImport] ${envID} 真实 ${ctype} preload ok: ${termNs}=${yamlNs} 拉到 ${r.entries?.length || 0} 条`,
        { envID })
      if (nsMissing) {
        pushLog('cchub', 'warn',
          `[applyImport] ${envID} yaml 里的 ${termNs}=${yamlNs} 在真实 ${ctype} 不存在`, { envID })
        toast.error(`${envID}: yaml 里的 ${termNs}=${yamlNs} 在真实 ${ctype} 找不到,部署后路由会失败`)
      }
      if (missingLocators.length > 0) {
        const desc = missingLocators.map(([s, d]) => `${s}→${d}`).join(', ')
        pushLog('cchub', 'warn',
          `[applyImport] ${envID} yaml 里以下 ${termLocator} 在真实 ${ctype} 不存在: ${desc}`, { envID })
        toast.error(`${envID}: ${missingLocators.length} 个服务的 ${termLocator} 在真实 ${ctype} 找不到 —— ${desc.length > 80 ? desc.slice(0, 80) + '…' : desc}`)
      }
      if (!nsMissing && missingLocators.length === 0) {
        pushLog('cchub', 'info',
          `[applyImport] ${envID} 交叉校验通过:yaml 里所有 ${termNs} + ${termLocator} 都在真实 ${ctype} 存在`,
          { envID })
      }
    } catch (e: any) {
      const msg = String(e?.message || e)
      // 失败保留合成态,UI 仍可用 yaml 反填的有限选项;只记日志不切 error 状态 ——
      // 否则 UI 直接红字"拉取失败"可能误导用户以为整个 import 失败了。
      pushLog('cchub', 'error',
        `[applyImport] ${envID} 真实 ${ctype} 校验失败,保留 yaml 合成态: ${msg}`, { envID })
      toast.error(`${envID}: 连不上真实 ${ctype} 做校验,先用 yaml 里的值,部署前请手动验证`)
      if (prevSynth && prevSynth.status === 'ok') {
        deps.ccHubStateByEnv[envID] = prevSynth
      } else {
        deps.ccHubStateByEnv[envID] = { status: 'error', error: '校验失败,详见日志' }
      }
    }
  }

  // ── 2. kuboard 版交叉校验 ──
  // applyImport 时把 yaml 的 service_map(cluster/namespace/configmap 三元组)反填到
  // kuboardSvcMap[envID::svc],但 yaml 里的值可能跟真实 kuboard 集群对不上(集群删了 /
  // 重命名 / namespace 删了 / configmap 删了 / 切实例)。本函数:
  //   1. 调真实 kuboard listResources 拿全 cluster→namespace→configmap 树
  //   2. 对每个 (env, svc) 的 (cluster, namespace, configmap),逐级在树里查
  //   3. 缺失登记到 log + toast,kuboardStateByEnv 用真实树替换合成态
  // 走的是配置中心场景的 sourceCreds['kuboard']:URL/access_key/username/password。
  async function crossCheckImportedKuboard(envID: string, sourceType: string = 'kuboard'): Promise<void> {
    if (!isDesktop()) return
    const data = deps.sourceCreds[sourceType]
    if (!data) return
    const envCreds = data.creds[envID] || {}
    const url = (envCreds.url || '').trim()
    const accessKey = (envCreds.access_key || '').trim()
    const username = (envCreds.username || '').trim()
    const password = (envCreds.password || '').trim()
    if (!url || (!accessKey && (!username || !password))) {
      pushLog('cchub', 'info',
        `[applyImport] ${envID} 跳过真实 kuboard 校验(凭证不全):url=${!!url} accessKey=${!!accessKey} basic=${!!username && !!password}`,
        { envID })
      return
    }
    // 收集 yaml 反填进 kuboardSvcMap 的所有 (svc, cluster, ns, cm)
    const yamlEntries: Array<{ svc: string; cluster: string; namespace: string; configmap: string }> = []
    for (const k of Object.keys(deps.kuboardSvcMap)) {
      if (!k.startsWith(envID + '::')) continue
      const svc = k.slice(envID.length + 2)
      const loc = deps.kuboardSvcMap[k]
      if (loc.cluster || loc.namespace || loc.configmap) {
        yamlEntries.push({ svc, cluster: loc.cluster ?? '', namespace: loc.namespace ?? '', configmap: loc.configmap ?? '' })
      }
    }
    if (yamlEntries.length === 0) {
      // 没反填任何 kuboard svcmap → 走标准 preload(给 UI 列出可选项)
      return deps.runKuboardPreloadFromSource(sourceType, envID)
    }
    deps.kuboardStateByEnv[envID] = { status: 'loading' }
    try {
      // Kuboard v3 需指定集群名;import 流程从 yaml 反填的 service_map 里直接拿(已知)。
      const clusterHint = yamlEntries.find(e => e.cluster)?.cluster || ''
      const res = await kuboardListResources(url, username, password, accessKey, clusterHint)
      const clusters = (res.clusters || []).map(c => ({
        name: c.name,
        namespaces: (c.namespaces || []).map(n => ({
          name: n.name,
          configmaps: n.configmaps || [],
        })),
      }))
      deps.kuboardStateByEnv[envID] = { status: 'ok', clusters, notes: res.notes }
      deps.persistKuboardState()

      // 交叉校验:逐级查
      const missingCluster: string[] = []
      const missingNS: Array<[string, string, string]> = []
      const missingCM: Array<[string, string, string, string]> = []
      for (const e of yamlEntries) {
        if (!e.cluster) continue
        const cl = clusters.find(c => c.name === e.cluster)
        if (!cl) {
          missingCluster.push(`${e.svc}→${e.cluster}`)
          continue
        }
        if (!e.namespace) continue
        const ns = cl.namespaces.find(n => n.name === e.namespace)
        if (!ns) {
          missingNS.push([e.svc, e.cluster, e.namespace])
          continue
        }
        if (!e.configmap) continue
        if (!ns.configmaps.includes(e.configmap)) {
          missingCM.push([e.svc, e.cluster, e.namespace, e.configmap])
        }
      }

      pushLog('cchub', 'info',
        `[applyImport] ${envID} 真实 kuboard preload ok: 拉到 ${clusters.length} 个集群`, { envID })
      if (missingCluster.length > 0) {
        pushLog('cchub', 'warn',
          `[applyImport] ${envID} yaml 里以下集群在真实 kuboard 不存在: ${missingCluster.join(', ')}`, { envID })
        toast.error(`${envID}: ${missingCluster.length} 个 cluster 在真实 kuboard 找不到`)
      }
      if (missingNS.length > 0) {
        const desc = missingNS.map(([s, c, n]) => `${s}→${c}/${n}`).join(', ')
        pushLog('cchub', 'warn',
          `[applyImport] ${envID} yaml 里以下 namespace 在真实 kuboard 不存在: ${desc}`, { envID })
        toast.error(`${envID}: ${missingNS.length} 个 namespace 在 kuboard 找不到`)
      }
      if (missingCM.length > 0) {
        const desc = missingCM.map(([s, c, n, cm]) => `${s}→${c}/${n}/${cm}`).join(', ')
        pushLog('cchub', 'warn',
          `[applyImport] ${envID} yaml 里以下 configmap 在真实 kuboard 不存在: ${desc}`, { envID })
        toast.error(`${envID}: ${missingCM.length} 个 configmap 在 kuboard 找不到 —— ${desc.length > 80 ? desc.slice(0, 80) + '…' : desc}`)
      }
      if (missingCluster.length === 0 && missingNS.length === 0 && missingCM.length === 0) {
        pushLog('cchub', 'info',
          `[applyImport] ${envID} kuboard 交叉校验通过:yaml 里所有 cluster/namespace/configmap 都在真实 kuboard 存在`,
          { envID })
      }
    } catch (e: any) {
      const msg = String(e?.message || e)
      deps.kuboardStateByEnv[envID] = { status: 'error', error: msg }
      pushLog('cchub', 'error',
        `[applyImport] ${envID} 真实 kuboard 校验失败: ${msg}`, { envID })
      toast.error(`${envID}: 连不上真实 kuboard 做校验,先用 yaml 里的值,部署前请手动验证`)
    }
  }

  // ── 3. 可观测性交叉校验 ──
  // 启用的每个 obs 工具:
  //   - 触发 scheduleObsProbe(URL+鉴权 通断 → ✗ 徽章 + 错误 hover)
  //   - grafana 额外列真实 datasources,对比 yaml 反填的 datasource_uid_by_env / loki dsUID
  //   - k8s_runtime 额外用 kuboard listResources 验 cluster/namespace 还在不在
  async function crossCheckImportedObservability(): Promise<void> {
    if (!isDesktop()) return
    const checks: Promise<void>[] = []
    for (const spec of deps.OBS_TOOL_SPECS) {
      if (!deps.enabledObservability[spec.key]) continue
      for (const env of deps.environments) {
        if (!env.id) continue
        // 直接用现成的 scheduleObsProbe 走防抖触发,800ms 后真实调 probeURLAuth
        // (它会写 obsProbeResults,UI 上 ✗ 徽章 + 错误 hover 都能看见)
        deps.scheduleObsProbe(spec.key, env.id)
      }
    }

    // grafana datasource UID 校验:用反填的 grafana 凭证列真实 datasources,对比 yaml 写的 UID
    if (deps.enabledObservability['grafana']) {
      for (const env of deps.environments) {
        if (!env.id) continue
        checks.push((async () => {
          const auth = deps.lokiAuthFor(env.id)
          if (!auth.grafana_url) return
          if (!auth.api_key && (!auth.user || !auth.pass)) return
          try {
            const dsList = await listGrafanaDatasources(auth)
            const realUids = new Set(dsList.map(d => d.uid))
            // 检查反填的 datasource_uid_by_env 里每条
            const missing: Array<{ tool: string; uid: string }> = []
            for (const tool of ['prometheus', 'jaeger', 'tempo', 'elk']) {
              const uid = (deps.grafanaDsUidByObsEnv[deps.obsGrafanaDsKey(tool, env.id)] || '').trim()
              if (uid && !realUids.has(uid)) {
                missing.push({ tool, uid })
              }
            }
            // loki datasource UID(在 lokiMappingByEnv 里)
            const lm = deps.getLokiMapping(env.id)
            if (lm.dsUID && !realUids.has(lm.dsUID)) {
              missing.push({ tool: 'loki', uid: lm.dsUID })
            }
            if (missing.length === 0) {
              pushLog('cchub', 'info',
                `[applyImport] ${env.id} grafana 交叉校验通过(真实 ${dsList.length} 个 datasource)`,
                { envID: env.id })
              return
            }
            const desc = missing.map(m => `${m.tool}→${m.uid.slice(0, 12)}…`).join(', ')
            pushLog('cchub', 'warn',
              `[applyImport] ${env.id} yaml 里以下 grafana datasource UID 在真实 grafana 不存在: ${desc}`,
              { envID: env.id })
            toast.error(`${env.id}: ${missing.length} 个 grafana datasource UID 找不到 —— ${desc}`)
          } catch (e: any) {
            const msg = String(e?.message || e)
            pushLog('cchub', 'warn',
              `[applyImport] ${env.id} grafana 校验失败(连不上 / 鉴权错):${msg}`,
              { envID: env.id })
          }
        })())
      }
    }

    // k8s_runtime:若反填了 envLoc,用 kuboard listResources 验 cluster/namespace 是否还在
    if (deps.enabledObservability['k8s_runtime']) {
      for (const [envID, loc] of Object.entries(deps.k8sRuntimeEnvLoc)) {
        if (!loc?.cluster && !loc?.namespace) continue
        checks.push((async () => {
          const obsURL = (deps.toolInputs[deps.toolKeyFor('obs', 'k8s_runtime', envID, 'url')] || '').trim()
            || (deps.sourceCreds['kuboard']?.creds?.[envID]?.url || '').trim()
          const obsKey = (deps.toolInputs[deps.toolKeyFor('obs', 'k8s_runtime', envID, 'access_key')] || '').trim()
            || (deps.sourceCreds['kuboard']?.creds?.[envID]?.access_key || '').trim()
          const obsUser = (deps.toolInputs[deps.toolKeyFor('obs', 'k8s_runtime', envID, 'username')] || '').trim()
            || (deps.sourceCreds['kuboard']?.creds?.[envID]?.username || '').trim()
          const obsPass = (deps.toolInputs[deps.toolKeyFor('obs', 'k8s_runtime', envID, 'password')] || '').trim()
            || (deps.sourceCreds['kuboard']?.creds?.[envID]?.password || '').trim()
          if (!obsURL || (!obsKey && (!obsUser || !obsPass))) return
          try {
            // v3 需集群名;k8s_runtime 的 envLoc 里已有 loc.cluster。
            const res = await kuboardListResources(obsURL, obsUser, obsPass, obsKey, loc.cluster || '')
            const cl = (res.clusters || []).find(c => c.name === loc.cluster)
            if (!cl) {
              pushLog('cchub', 'warn',
                `[applyImport] ${envID} k8s_runtime cluster=${loc.cluster} 在真实 kuboard 不存在`,
                { envID })
              toast.error(`${envID}: k8s_runtime cluster ${loc.cluster} 找不到`)
              return
            }
            if (loc.namespace && !cl.namespaces.find(n => n.name === loc.namespace)) {
              pushLog('cchub', 'warn',
                `[applyImport] ${envID} k8s_runtime ${loc.cluster}/${loc.namespace} namespace 不存在`,
                { envID })
              toast.error(`${envID}: k8s_runtime namespace ${loc.namespace} 找不到`)
              return
            }
            pushLog('cchub', 'info',
              `[applyImport] ${envID} k8s_runtime 交叉校验通过(${loc.cluster}/${loc.namespace})`,
              { envID })
          } catch (e: any) {
            pushLog('cchub', 'warn',
              `[applyImport] ${envID} k8s_runtime 校验失败:${String(e?.message || e)}`,
              { envID })
          }
        })())
      }
    }

    await Promise.all(checks)
  }

  // ── 4. orchestrator ──
  // applyImport 异步触发:对每个 env × 每个 source 调对应 backend probe,跟 yaml 里反填的
  // namespace / dataId / cm locator 做真实存在性比对,失败给徽章提示。
  // 失败 / 凭证不全 / 非桌面 app 都静默跳过 —— 用户后续仍可手动点"📥 拉取勾选服务的配置"。
  async function runImportCrossChecks(cc: string) {
    deps.importInProgress.value = false  // 反填阶段已结束,放开 watcher
    pushLog('cchub', 'info',
      `[applyImport] 自动预加载触发: cc=${cc} cct=${deps.configCenterType.value} isDesktop=${isDesktop()} envs=${deps.environments.map(e => e.id).filter(Boolean).join(',')}`,
      { cc, cct: deps.configCenterType.value })
    if (!cc || !isDesktop()) return

    // 每种 type 走自己的逻辑:
    //   - nacos / apollo / consul → crossCheckImportedConfigSource(用 ccHubStateByEnv + envNamespaces + serviceConfigSel)
    //   - kuboard                  → crossCheckImportedKuboard(用 kuboardStateByEnv + kuboardSvcMap)
    //   - env-vars / none / unknown future source → 不需要校验(没远端可比对)
    const checkOneSource = async (sourceType: string, envID: string) => {
      if (sourceType === 'kuboard') {
        return crossCheckImportedKuboard(envID, sourceType)
      }
      if (sourceType !== 'nacos' && sourceType !== 'apollo' && sourceType !== 'consul') {
        return
      }
      const payload = deps.buildPreloadPayload(envID)
      if (!payload.valid) {
        pushLog('cchub', 'info',
          `[applyImport] ${envID}@${sourceType} 跳过自动预加载,缺字段: ${payload.missing.join(', ')}`,
          { envID, sourceType })
        return
      }
      const cur = deps.ccHubStateByEnv[envID]
      if (cur?.status === 'ok' && cur.synthesized) {
        pushLog('cchub', 'info',
          `[applyImport] ${envID}@${sourceType} 启动真实交叉校验(yaml namespace=${deps.envNamespaces[envID] || '(空)'})`,
          { envID, sourceType })
        return crossCheckImportedConfigSource(envID)
      }
      if (cur?.status === 'ok') {
        pushLog('cchub', 'info',
          `[applyImport] ${envID}@${sourceType} 已是真实 ok 状态(${cur.entries?.length || 0} 条),跳过`,
          { envID, sourceType })
        return
      }
      pushLog('cchub', 'info',
        `[applyImport] ${envID}@${sourceType} 触发自动预加载 addr=${payload.addr}`,
        { envID, sourceType })
      return deps.runCCHubPreload(envID)
    }
    for (const env of deps.environments) {
      if (!env.id) continue
      // 主源 + 副源都跑校验。ccHubStateByEnv 是全局单源 keyed by envID(老设计),
      // nacos/apollo/consul 同时只能一个跑;副源 kuboard 单独走 kuboardStateByEnv 不冲突。
      for (const t of deps.activeSourceTypes.value) {
        checkOneSource(t, env.id).catch((e) => {
          pushLog('cchub', 'error',
            `[applyImport] ${env.id}@${t} 交叉校验抛错: ${String(e)}`, { envID: env.id, sourceType: t })
        })
      }
    }
    // 可观测性交叉校验:启用的每个 obs 工具逐 env 真实 HTTP probe + datasource UID 比对。
    crossCheckImportedObservability().catch((e) => {
      pushLog('cchub', 'error',
        `[applyImport] 可观测性交叉校验抛错: ${String(e)}`)
    })
  }

  return {
    crossCheckImportedConfigSource,
    crossCheckImportedKuboard,
    crossCheckImportedObservability,
    runImportCrossChecks,
  }
}
