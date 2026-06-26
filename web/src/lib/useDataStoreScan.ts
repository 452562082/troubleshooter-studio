// useDataStoreScan —— Step 7 数据层"从配置中心读取"runners + 连通性测试。
//
// 暴露:
//   - canAutoImportDS                     computed:能否触发自动导入(Step 5 至少有一条服务能扫)
//   - autoImportDataStores()              主入口:nacos/apollo/consul 批量分组拉 + kuboard 批拉
//                                         → 跑 DS_MATCHERS → 写 scannedDS / dsScanState /
//                                         enabledDataStores / dsAutoFilled → 跑 probeAllAcrossEnvs
//   - probeOneDS(envID, svc, dsKey)       单条连通性测试
//   - probeAllAcrossEnvs()                全环境一键测试(跨 env 并发,env 内串行)
//   - probingByEnv / probingAll / probingAllStats  UI 用的进度状态
//
// state 容器在 lib/useDataStoreState.ts(scannedDS / dsScanState / dsImportStatus 等),纯解析在
// lib/dataStoreParser.ts(parseConfigContent + DS_MATCHERS)。本 composable 把这两层 + InitPage
// 闭包里的 25+ deps 串成完整流程。
import { computed, reactive, ref, type ComputedRef } from 'vue'
import { fetchConfigContentBatch, kuboardFetchConfigMaps, probeDataStore, type KuboardFetchBatchResult } from './bridge'
import { isDesktop } from './bridge/shared'
import { one2allFetchConfigMaps } from './bridge/one2all'
import { pushLog } from './logStore'
import { toast } from './toast'
import { probeKey } from './yamlShared'
import { DS_MATCHERS, parseConfigContent, parseK8sConfigMapDataContents } from './dataStoreParser'
import type { DSByService, DSScanState, DSProbeState } from './useDataStoreState'

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

interface KuboardSvcLocator { cluster?: string; namespace?: string; configmap?: string }

export interface UseDataStoreScanDeps {
  // useDataStoreState 暴露的容器
  scannedDS: Record<string, DSByService>
  dsScanState: Record<string, DSScanState>
  dsProbeResults: Record<string, DSProbeState>
  dsImportStatus: { value: 'idle' | 'loading' | 'ok' | 'error' }
  dsImportStats: { scanned: number; matched: number }
  dsAutoFilled: Record<string, boolean>
  enabledDataStores: Record<string, boolean>
  scanStateKey: (envID: string, svc: string) => string

  // 环境 / 服务
  environments: { id: string }[]
  allServiceNames: { value: readonly string[] }
  getServiceSource: (svc: string) => string
  svcKey: (envID: string, svc: string) => string

  // 配置中心拉取(nacos/apollo/consul)
  buildPreloadPayload: (envID: string) => PreloadPayload
  envNamespaces: Record<string, string>
  serviceConfigSel: Record<string, string>
  serviceConfigGroup: Record<string, string>

  // kuboard 副源拉取
  enabledSourceTypes: Record<string, boolean>
  activeSourceTypes: ComputedRef<readonly string[]>
  ccCredInputs: Record<string, string>
  ccKeyFor: (sourceType: string, envID: string, field: string) => string
  sourceCreds: Record<string, { creds: Record<string, Record<string, string>>; rawExtra?: Record<string, unknown> }>
  kuboardSvcMap: Record<string, KuboardSvcLocator>
  one2allSvcMap?: Record<string, { cluster_id: string; namespace: string; configmap: string }>
}

export function useDataStoreScan(deps: UseDataStoreScanDeps) {
  // per-env 开关:probeAllAcrossEnvs 内部用它防止跟未来其它 per-env 测试重入。
  // 单 env 一键测试已废弃 —— 全局按钮覆盖,每条 ✓/✗ 徽章 + 失败 env 列表能定位到具体哪条。
  const probingByEnv = reactive<Record<string, boolean>>({})

  // 全部环境一键连通性测试 —— 跨 env 并发(每 env 内串行),配上汇总进度。
  // 设计取舍:
  //   - 跨 env 并发:多环境是不同后端集群,并行不会互相打,体感快很多
  //   - env 内串行:同一集群 100 条同时打容易被限流 / 触发熔断,80ms 间隔保持温和
  //   - 汇总:成功 / 失败 / 总数,失败的 env 在 toast 里点出来,详情让用户去看每条 ✗ 徽章 + 日志
  const probingAll = ref(false)
  const probingAllStats = reactive<{ total: number; done: number; ok: number; fail: number }>({
    total: 0, done: 0, ok: 0, fail: 0,
  })

  // 能否触发自动导入 = Step 5 至少有一条服务能扫:
  //   - nacos/apollo/consul:在 serviceConfigSel 里挑了 dataId,或
  //   - kuboard:在 kuboardSvcMap 里填齐了 cluster/namespace/configmap
  const canAutoImportDS = computed<boolean>(() => {
    if (!isDesktop()) return false
    for (const k of Object.keys(deps.serviceConfigSel)) {
      if ((deps.serviceConfigSel[k] || '').trim()) return true
    }
    for (const k of Object.keys(deps.kuboardSvcMap)) {
      const loc = deps.kuboardSvcMap[k]
      if (loc?.cluster && loc?.namespace && loc?.configmap) return true
    }
    if (deps.one2allSvcMap) {
      for (const k of Object.keys(deps.one2allSvcMap)) {
        const loc = deps.one2allSvcMap[k]
        if (loc?.cluster_id && loc?.namespace && loc?.configmap) return true
      }
    }
    return false
  })

  async function probeOneDS(envID: string, svc: string, dsKey: string) {
    if (!isDesktop()) {
      toast.error('连通性测试只在桌面 app 可用')
      return
    }
    const fields = deps.scannedDS[envID]?.[svc]?.[dsKey]
    if (!fields || Object.keys(fields).length === 0) {
      toast.error('该数据层无字段可测')
      return
    }
    const k = probeKey(envID, svc, dsKey)
    deps.dsProbeResults[k] = { status: 'loading' }
    try {
      const r = await probeDataStore({ type: dsKey, fields: { ...fields } })
      if (r.ok) {
        deps.dsProbeResults[k] = { status: 'ok', latency: r.latency, detail: r.detail }
        pushLog('cchub', 'info',
          `[${envID}/${svc}] ${dsKey} 连通性 OK (${r.latency || ''}) ${r.detail || ''}`,
          { envID, svc, dsKey })
      } else {
        deps.dsProbeResults[k] = { status: 'fail', error: r.error || '未知错误' }
        pushLog('cchub', 'warn',
          `[${envID}/${svc}] ${dsKey} 连通性失败: ${r.error || ''}`,
          { envID, svc, dsKey })
      }
    } catch (e: any) {
      const msg = String(e?.message || e)
      deps.dsProbeResults[k] = { status: 'fail', error: msg }
      pushLog('cchub', 'error', `[${envID}/${svc}] ${dsKey} 测试异常: ${msg}`, { envID, svc, dsKey })
    }
  }

  // "什么算一条数据层组件"的唯一权威枚举。probeAllAcrossEnvs 跟 Step 7 校验门必须用
  // 同一个枚举,否则会出现"测了 108 条但校验门看到 103 条 / 反过来"的撕裂(用户实测撞过)。
  // 撕裂典型成因:probeAll 拿首次快照后 scannedDS 被异步动了 —— 用户删组件 / 重 import /
  // recomputeEnabledDataStoresFromScanned 副作用,等等。统一函数返回的是即时读 reactive
  // 的扁平列表,调用方各自决定"用这个列表瞬时跑"还是"实时再读一遍"。
  //
  // 过滤孤儿服务:scannedDS 里可能残留 allServiceNames 不包含的服务名(早期 yaml 反填用
  // 短名 'user',后来 Step 4 改成 'user-grpc-server',旧条目没清)。这些孤儿在 UI 上不
  // 渲染(allServiceNames 决定页面展示),用户看不到也测不了,但校验若枚举到永远卡住。
  // 解决:枚举只考虑 allServiceNames 里的服务,孤儿跳过(并记一行 debug 日志可定位)。
  function enumerateDataStoreProbeTargets(): Array<{ envID: string; svc: string; dsKey: string }> {
    const out: Array<{ envID: string; svc: string; dsKey: string }> = []
    const validSvcs = new Set(deps.allServiceNames.value)
    for (const env of deps.environments) {
      if (!env.id || !env.id.trim()) continue
      const svcs = deps.scannedDS[env.id]
      if (!svcs) continue
      for (const svc of Object.keys(svcs).sort()) {
        if (!validSvcs.has(svc)) continue
        const byKey = svcs[svc] || {}
        for (const dsKey of Object.keys(byKey).sort()) {
          out.push({ envID: env.id, svc, dsKey })
        }
      }
    }
    return out
  }

  async function probeAllAcrossEnvs() {
    if (probingAll.value) return
    if (!isDesktop()) {
      toast.error('连通性测试只在桌面 app 可用')
      return
    }
    // 用唯一枚举函数取一份快照 —— 跟 Step 7 校验门绝对对齐,避免"测了 N 条但校验门看到 M 条"。
    // 拿快照后整个测试期间用这份固定列表(若期间用户删组件,新列表少了几条不影响已跑完的统计;
    // 校验门下一帧自然会按新 scannedDS 重新算,小偏差由用户感知"我刚删了几条"自洽)。
    const targets = enumerateDataStoreProbeTargets()
    const total = targets.length
    if (total === 0) {
      toast.info('没有可测试的数据层组件 —— 先点"📥 从配置中心读取"识别')
      return
    }
    for (const k of Object.keys(deps.dsProbeResults)) delete deps.dsProbeResults[k]
    probingAllStats.total = total
    probingAllStats.done = 0
    probingAllStats.ok = 0
    probingAllStats.fail = 0
    probingAll.value = true
    pushLog('cchub', 'info',
      `[probeAll] 启动全环境连通性测试,共 ${total} 条(${deps.environments.filter(e => e.id).length} 个环境跨 env 并行 / env 内串行)`)
    try {
      const byEnv: Record<string, Array<{ svc: string; dsKey: string }>> = {}
      for (const t of targets) {
        if (!byEnv[t.envID]) byEnv[t.envID] = []
        byEnv[t.envID].push({ svc: t.svc, dsKey: t.dsKey })
      }
      const tasks = Object.entries(byEnv).map(async ([envID, items]) => {
        probingByEnv[envID] = true
        try {
          for (const { svc, dsKey } of items) {
            await probeOneDS(envID, svc, dsKey)
            const k = probeKey(envID, svc, dsKey)
            const st = deps.dsProbeResults[k]?.status
            probingAllStats.done++
            if (st === 'ok') probingAllStats.ok++
            else if (st === 'fail') probingAllStats.fail++
            await new Promise(r => setTimeout(r, 80))
          }
        } finally {
          probingByEnv[envID] = false
        }
      })
      await Promise.all(tasks)
      const failedEnvs: string[] = []
      for (const env of deps.environments) {
        if (!env.id) continue
        const prefix = `${env.id}::`
        const hasFail = Object.keys(deps.dsProbeResults).some(k =>
          k.startsWith(prefix) && deps.dsProbeResults[k].status === 'fail')
        if (hasFail) failedEnvs.push(env.id)
      }
      pushLog('cchub', 'info',
        `[probeAll] 完成: ${probingAllStats.ok} 通 / ${probingAllStats.fail} 失败 / 共 ${probingAllStats.total};失败环境: ${failedEnvs.join(', ') || '无'}`)
      if (probingAllStats.fail === 0) {
        toast.success(`全部连通性测试通过 (${probingAllStats.ok}/${probingAllStats.total})`)
      } else {
        toast.error(`${probingAllStats.fail} / ${probingAllStats.total} 条失败 —— 失败环境: ${failedEnvs.join(', ')};详见每条 ✗ 徽章 + 左侧日志`)
      }
    } finally {
      probingAll.value = false
    }
  }

  // applyMatchersToParsedConfig:把 parseConfigContent 出来的 root object 喂给 DS_MATCHERS,
  // 命中就写 scannedDS / enabledDataStores / dsAutoFilled / matchedSet,并落 dsScanState
  // 状态(ok/empty)。autoImportDataStores 的 nacos 和 kuboard 两 pass 都跑一样的 matcher 流程,
  // 抽出来去重。emptyReasonPrefix 控制 empty 状态的提示前缀("yaml" vs "cm")。
  function applyMatchersToParsedConfig(
    env: string, svc: string, root: any,
    matchedSet: Set<string>,
    emptyReasonPrefix: string,
    matchLogPrefix: string,
  ): number {
    const stateKey = deps.scanStateKey(env, svc)
    const hits: string[] = []
    if (!deps.scannedDS[env]) deps.scannedDS[env] = {}
    deps.scannedDS[env][svc] = {}
    for (const m of DS_MATCHERS) {
      const hit = m.matchYAML(root)
      if (!hit) continue
      hits.push(m.dsKey)
      // 命中数据层 → 同步把 enabledDataStores[dsKey] = true。否则 deriveSkillsWhitelist
      // 看到 enabledDataStores.redis 仍 false,就不会把 redis-runtime-query push 进白名单。
      deps.enabledDataStores[m.dsKey] = true
      deps.dsAutoFilled[m.dsKey] = true
      matchedSet.add(`${env}:${m.dsKey}`)
      deps.scannedDS[env][svc][m.dsKey] = { ...hit }
      pushLog('cchub', 'info',
        `[${env}/${svc}] ${matchLogPrefix} ${m.dsKey}: ${Object.keys(hit).join(',')}`,
        { envID: env, svc, dsKey: m.dsKey })
    }
    if (hits.length === 0) {
      const topKeys = (root && typeof root === 'object') ? Object.keys(root).slice(0, 15).join(',') : '(非对象)'
      deps.dsScanState[stateKey] = { status: 'empty', reason: `${emptyReasonPrefix}(顶级 key: ${topKeys})` }
      pushLog('cchub', 'warn', `[${env}/${svc}] 未识别到任何数据层(顶级 key:${topKeys})`,
        { envID: env, svc })
    } else {
      deps.dsScanState[stateKey] = { status: 'ok' }
    }
    return hits.length
  }

  async function autoImportDataStores() {
    if (!canAutoImportDS.value) {
      toast.error('先在 Step 5 完成配置源扫描 + 服务 dataId 映射')
      return
    }
    deps.dsImportStatus.value = 'loading'
    deps.dsImportStats.scanned = 0
    deps.dsImportStats.matched = 0
    for (const k of Object.keys(deps.dsAutoFilled)) delete deps.dsAutoFilled[k]
    for (const k of Object.keys(deps.dsScanState)) delete deps.dsScanState[k]
    // 旧的连通性结果一并清空 —— 重新拉完字段可能变了,旧 ✓/✗ 不该残留
    for (const k of Object.keys(deps.dsProbeResults)) delete deps.dsProbeResults[k]

    let scanned = 0
    const matchedSet = new Set<string>()
    try {
      for (const env of deps.environments) {
        if (!env.id) continue
        if (!deps.scannedDS[env.id]) deps.scannedDS[env.id] = {}
      }

      // 按凭证去重分组:所有用同一组 (type/addr/user/pass/token/app_id) 的 env 合并一次 batch,
      // 后端只 connect 一次(probe + login 只 1 次)。典型场景:5 服务 × 2 环境共用同一个 Nacos →
      // 一次 batch 就能拉完 10 个 config,login 只 1 次。
      type BatchItem = { key: string; env: string; svc: string; dataId: string; group: string; nsID: string }
      const groups = new Map<string, { payload: PreloadPayload; items: BatchItem[] }>()

      const remotePreloadTypes = new Set(['nacos', 'apollo', 'consul'])
      for (const env of deps.environments) {
        if (!env.id) continue
        const payload = deps.buildPreloadPayload(env.id)
        const nsID = deps.envNamespaces[env.id]
        // 主源不是 nacos/apollo/consul(比如 kuboard / env-vars / none) → 这一段不处理,
        // 让后面的 kuboard 专项 pass 接管;只把"挂在 nacos/apollo/consul 但当前主源不支持"
        // 的服务标 skipped(其实多源 + 主源是 kuboard 时这种组合罕见)。
        if (!remotePreloadTypes.has(payload.type)) continue
        if (!payload.valid) {
          const reason = `凭证不完整(缺: ${payload.missing.join(', ')})`
          pushLog('cchub', 'warn', `[${env.id}] ${reason},跳过本 env 的 nacos 类批拉`, { envID: env.id })
          for (const svc of deps.allServiceNames.value) {
            if (deps.getServiceSource(svc) !== payload.type) continue
            deps.dsScanState[deps.scanStateKey(env.id, svc)] = { status: 'skipped', reason }
          }
          continue
        }
        if (nsID === undefined) {
          const reason = '未选 namespace,先回 Step 5 扫一次'
          pushLog('cchub', 'warn', `[${env.id}] ${reason}`, { envID: env.id })
          for (const svc of deps.allServiceNames.value) {
            if (deps.getServiceSource(svc) !== payload.type) continue
            deps.dsScanState[deps.scanStateKey(env.id, svc)] = { status: 'skipped', reason }
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
        const primarySrcType = payload.type
        for (const svc of deps.allServiceNames.value) {
          const svcSource = deps.getServiceSource(svc)
          if (!svcSource) {
            deps.dsScanState[deps.scanStateKey(env.id, svc)] = {
              status: 'skipped',
              reason: '未分配源,回 Step 5 在某个源面板的"本环境包含的服务"里勾上',
            }
            continue
          }
          if (svcSource !== primarySrcType) {
            deps.dsScanState[deps.scanStateKey(env.id, svc)] = {
              status: 'skipped',
              reason: `挂在 ${svcSource} 源,自动扫只针对 ${primarySrcType} 源`,
            }
            continue
          }
          const dataId = (deps.serviceConfigSel[deps.svcKey(env.id, svc)] || '').trim()
          if (!dataId) {
            deps.dsScanState[deps.scanStateKey(env.id, svc)] = {
              status: 'skipped',
              reason: '未映射 dataId,回 Step 5 为此服务挑一条',
            }
            pushLog('cchub', 'warn', `[${env.id}/${svc}] 未映射 dataId`, { envID: env.id, svc })
            continue
          }
          const group = (deps.serviceConfigGroup[deps.svcKey(env.id, svc)] || '').trim()
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
          const msg = String(e?.message || e)
          pushLog('cchub', 'error', `批量组 ${gi} 拉取失败(probe/login 问题): ${msg}`)
          for (const it of group.items) {
            deps.dsScanState[deps.scanStateKey(it.env, it.svc)] = { status: 'error', reason: '批量拉取失败,详见日志' }
          }
          continue
        }
        for (const n of (batch.notes || [])) pushLog('cchub', 'info', n)

        const byKey = new Map(group.items.map(it => [it.key, it]))
        for (const itemResult of batch.items) {
          const info = byKey.get(itemResult.key)
          if (!info) continue
          const stateKey = deps.scanStateKey(info.env, info.svc)
          if (!itemResult.ok || !itemResult.result) {
            deps.dsScanState[stateKey] = { status: 'error', reason: '拉取失败,详见日志' }
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
            deps.dsScanState[stateKey] = { status: 'error', reason }
            pushLog('cchub', 'warn',
              `[${info.env}/${info.svc}] ${reason},内容开头: ${String(r.content || '').slice(0, 80)}`,
              { envID: info.env, svc: info.svc })
            continue
          }
          applyMatchersToParsedConfig(info.env, info.svc, root, matchedSet,
            'yaml 里没匹到数据层', '识别数据层')
        }
      }

      // ── Kuboard 源:per-env 批拉 cm.data,跟 nacos 同样跑 DS_MATCHERS ──
      // 单独一段是因为 kuboard 凭证 / locator 数据结构跟 nacos 不一样:
      //   - 凭证:url + access_key 或 username+password(per env);
      //   - locator:cluster/namespace/configmap(per service,from kuboardSvcMap);
      //   - 内容:N 个 data 字段拼成 multi-doc YAML(后端 KuboardFetchConfigMaps).
      if (deps.enabledSourceTypes['kuboard']) {
        const isPrimaryKb = deps.activeSourceTypes.value[0] === 'kuboard'
        const getKbCred = (envID: string, field: string): string => {
          if (isPrimaryKb) return (deps.ccCredInputs[deps.ccKeyFor('kuboard', envID, field)] || '').trim()
          return ((deps.sourceCreds['kuboard']?.creds?.[envID]?.[field]) || '').trim()
        }
        for (const env of deps.environments) {
          if (!env.id) continue
          const kbItems: { key: string; env: string; svc: string; cluster: string; namespace: string; configmap: string }[] = []
          for (const svc of deps.allServiceNames.value) {
            if (deps.getServiceSource(svc) !== 'kuboard') continue
            const loc = deps.kuboardSvcMap[deps.svcKey(env.id, svc)]
            if (!loc?.cluster || !loc?.namespace || !loc?.configmap) {
              deps.dsScanState[deps.scanStateKey(env.id, svc)] = {
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
              deps.dsScanState[deps.scanStateKey(it.env, it.svc)] = { status: 'skipped', reason: 'kuboard 凭证不完整,回 Step 5 补' }
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
              deps.dsScanState[deps.scanStateKey(it.env, it.svc)] = { status: 'error', reason: 'kuboard 批拉失败,详见日志' }
            }
            continue
          }
          for (const n of (kbBatch.notes || [])) pushLog('cchub', 'info', n)
          const byKey = new Map(kbItems.map(it => [it.key, it]))
          for (const r of kbBatch.items) {
            const info = byKey.get(r.key)
            if (!info) continue
            const stateKey = deps.scanStateKey(info.env, info.svc)
            if (!r.ok) {
              deps.dsScanState[stateKey] = { status: 'error', reason: r.error || '拉取失败' }
              pushLog('cchub', 'error', `[${info.env}/${info.svc}] kuboard cm 拉取失败: ${r.error || '(未知)'}`,
                { envID: info.env, svc: info.svc })
              continue
            }
            scanned++
            if (!r.content) {
              deps.dsScanState[stateKey] = { status: 'empty', reason: 'configmap 是空的' }
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
              deps.dsScanState[stateKey] = { status: 'error', reason: `解析 configmap 失败(format=${r.format || '?'})` }
              continue
            }
            applyMatchersToParsedConfig(info.env, info.svc, root, matchedSet,
              'cm 里没匹到数据层', 'kuboard 识别数据层')
          }
        }
      }

      // one2all pass
      if (deps.enabledSourceTypes['one2all'] && deps.one2allSvcMap) {
        const isO2A = deps.activeSourceTypes.value[0] === 'one2all'
        const o2aCred = (f: string): string => {
          if (isO2A) return (deps.ccCredInputs[deps.ccKeyFor('one2all', '_shared_', f)] || '').trim()
          return ((deps.sourceCreds['one2all']?.creds?.['_shared_']?.[f]) || '').trim()
        }
        const mURL = o2aCred('mcp_url'); const tok = o2aCred('token')
        if (mURL && tok) {
          for (const env of deps.environments) {
            if (!env.id) continue
            const cfs: any[] = []
            for (const svc of deps.allServiceNames.value) {
              if (deps.getServiceSource(svc) !== 'one2all') continue
              const loc = deps.one2allSvcMap?.[deps.svcKey(env.id, svc)]
              if (!loc?.cluster_id || !loc?.namespace || !loc?.configmap) continue
              for (const cm of loc.configmap.split(',').filter(Boolean))
                cfs.push({ cid: loc.cluster_id, ns: loc.namespace, name: cm, env: env.id, svc })
            }
            if (!cfs.length) continue
            pushLog('cchub', 'info', `[${env.id}] one2all 批拉 ${cfs.length} 个 ConfigMap`)
            try {
              const res = await one2allFetchConfigMaps(mURL, tok, cfs.map(c => ({ cluster_id: c.cid, namespace: c.ns, name: c.name })))
              const byService = new Map<string, { env: string; svc: string; names: string[]; contents: string[]; errors: string[] }>()
              for (let i = 0; i < res.length; i++) {
                const r = res[i]; const info = cfs[i]; if (!info) continue
                const sk = deps.scanStateKey(info.env, info.svc)
                const groupKey = `${info.env}::${info.svc}`
                if (!byService.has(groupKey)) {
                  byService.set(groupKey, { env: info.env, svc: info.svc, names: [], contents: [], errors: [] })
                }
                const g = byService.get(groupKey)!
                g.names.push(info.name)
                if (r.error) {
                  g.errors.push(`${info.name}: ${r.error}`)
                  deps.dsScanState[sk] = { status: 'error', reason: r.error }
                  continue
                }
                scanned++
                if (!r.content || r.content === '{}' || r.content === 'null') continue
                g.contents.push(r.content)
              }
              for (const g of byService.values()) {
                const sk = deps.scanStateKey(g.env, g.svc)
                if (g.contents.length === 0) {
                  deps.dsScanState[sk] = {
                    status: g.errors.length ? 'error' : 'empty',
                    reason: g.errors.length ? g.errors.join('; ') : 'cm 空',
                  }
                  continue
                }
                const root = parseK8sConfigMapDataContents(g.contents)
                if (!root || Object.keys(root).length === 0) {
                  deps.dsScanState[sk] = { status: 'empty', reason: '无有效 ConfigMap data 内容' }
                  continue
                }
                pushLog('cchub', 'info',
                  `[${g.env}/${g.svc}] one2all 合并 ${g.contents.length}/${g.names.length} 个 ConfigMap 后识别`,
                  { envID: g.env, svc: g.svc })
                applyMatchersToParsedConfig(g.env, g.svc, root, matchedSet, 'no match', 'one2all')
              }
            } catch (e: any) {
              for (const c of cfs) deps.dsScanState[deps.scanStateKey(c.env, c.svc)] = { status: 'error', reason: String(e?.message || e) }
            }
          }
        }
      }

      deps.dsImportStats.scanned = scanned
      deps.dsImportStats.matched = matchedSet.size
      deps.dsImportStatus.value = 'ok'
      toast.success(`扫描 ${scanned} 条配置,识别 ${matchedSet.size} 个 (env, 数据层) 组合`)
    } catch (e: any) {
      deps.dsImportStatus.value = 'error'
      toast.error(`自动识别失败,详见左侧「日志」`)
      pushLog('cchub', 'error', `自动识别异常: ${String(e?.message || e)}`)
      return
    }
    // 自动对识别出的所有数据层组件跑一遍连通性测试 —— 跨 env 并行,比逐 env 串行快几倍
    pushLog('cchub', 'info', '识别完成,开始自动跑连通性测试...')
    await probeAllAcrossEnvs()
    pushLog('cchub', 'info', '连通性测试完成')
  }

  return {
    canAutoImportDS,
    probingByEnv, probingAll, probingAllStats,
    probeOneDS,
    probeAllAcrossEnvs,
    autoImportDataStores,
    enumerateDataStoreProbeTargets,
  }
}
