// useDataStoreState —— Step 7 数据层"从配置中心读取"识别结果的状态容器(saved 兜底 + 删/重算 helpers)。
//
// 包含:
//   - scannedDS / dsScanState / dsAutoFilled / dsImportStatus / dsImportStats / dsProbeResults 容器
//   - scanStateKey / scanStateOf 读 helper
//   - removeScannedDS 用户手动"这个我不要了"
//   - recomputeEnabledDataStoresFromScanned 删/加之后同步 enabledDataStores
//
// 写侧 runners(autoImportDataStores / probeOneDS / probeAllAcrossEnvs)还跟 sourceCreds /
// preloadConfigCenter / fetchConfigContentBatch / probeDataStore 多块状态交织,留在 InitPage
// 直接 mutate 暴露的 reactive。
import { reactive, ref } from 'vue'
import { probeKey } from './yamlShared'

export type DSFieldMap = Record<string, string>
export type DSByKey = Record<string, DSFieldMap>
export type DSByService = Record<string, DSByKey>

export interface DSScanState {
  status: 'ok' | 'empty' | 'skipped' | 'error'
  reason?: string
}

export interface DSProbeState {
  status: 'idle' | 'loading' | 'ok' | 'fail'
  latency?: string
  detail?: string
  error?: string
}

export interface UseDataStoreStateInitial {
  /** saved.scannedDS 反填(env→svc→dsKey→{ field: value }) */
  scannedDS?: Record<string, DSByService>
  /** saved.dsScanState 反填,加载时按特征字符串清掉旧版本的 stale "未映射 dataId" reason */
  dsScanState?: Record<string, DSScanState>
}

export function useDataStoreState(
  initial: UseDataStoreStateInitial,
  /** dataStoreOptions: 全工具支持的 data store type 列表(redis/mongodb/...) */
  dataStoreOptions: readonly string[],
  /** enabledDataStores: 上层 reactive,recomputeEnabledDataStoresFromScanned 会改它 */
  enabledDataStores: Record<string, boolean>,
  initialTypes: Record<string, string> = {},
) {
  const dsImportStatus = ref<'idle' | 'loading' | 'ok' | 'error'>('idle')
  const dsImportStats = reactive<{ scanned: number; matched: number }>({ scanned: 0, matched: 0 })
  const dsAutoFilled = reactive<Record<string, boolean>>({}) // dsType → 是否本次自动识别过

  const scannedDS = reactive<Record<string, DSByService>>(initial.scannedDS ?? {})
  const dataStoreTypes = reactive<Record<string, string>>({ ...initialTypes })
  for (const services of Object.values(scannedDS)) {
    for (const stores of Object.values(services)) {
      for (const id of Object.keys(stores)) {
        if (!dataStoreTypes[id]) dataStoreTypes[id] = id
      }
    }
  }
  const dataStoreType = (id: string) => dataStoreTypes[id] || id

  // 一次性迁移:旧版本 nacos 批拉对"未分配源"和"挂在副源"的服务都笼统报"未映射 dataId",
  // 这些 stale 状态会跨会话留在 localStorage 里。新版本对未分配源 / 跨源服务给的 reason 不一样,
  // 加载时按特征字符串清掉它们,让用户进 Step 6 看到的是新逻辑跑出来的状态(或新触发后的结果)。
  const dsScanState = reactive<Record<string, DSScanState>>(
    (() => {
      const src = initial.dsScanState ?? {}
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

  const dsProbeResults = reactive<Record<string, DSProbeState>>({})

  function scanStateKey(envID: string, svc: string): string { return `${envID}::${svc}` }
  function scanStateOf(envID: string, svc: string): DSScanState | undefined {
    return dsScanState[scanStateKey(envID, svc)]
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
            live.add(dataStoreType(dsKey))
          }
        }
      }
    }
    for (const k of dataStoreOptions) {
      enabledDataStores[k] = live.has(k)
    }
  }

  // 排除某个 (env, service) 下识别出的某类数据层。重新读取配置会重新发现它；
  // UI 使用“排除能力”文案,避免让用户误以为删除了真实运行时资源。
  function removeScannedDS(envID: string, svc: string, dsKey: string) {
    if (scannedDS[envID]?.[svc]?.[dsKey]) {
      delete scannedDS[envID][svc][dsKey]
    }
    delete dsProbeResults[probeKey(envID, svc, dsKey)]
    // 同步 enabledDataStores —— 删掉的可能是该 type 的最后一条,enabledDataStores 得跟着关。
    recomputeEnabledDataStoresFromScanned()
  }

  function addManualDataStore(envID: string, svc: string, dsType: string, fieldKeys: readonly string[]) {
    if (!envID || !svc || !dsType) return
    if (!scannedDS[envID]) scannedDS[envID] = {}
    if (!scannedDS[envID][svc]) scannedDS[envID][svc] = {}
    const usedIDs = new Set<string>()
    for (const services of Object.values(scannedDS)) {
      for (const stores of Object.values(services)) {
        for (const id of Object.keys(stores)) usedIDs.add(id)
      }
    }
    let dsKey = dsType
    if (usedIDs.has(dsKey)) {
      for (let n = 2; ; n++) {
        const candidate = `${dsType}-${n}`
        if (!usedIDs.has(candidate)) { dsKey = candidate; break }
      }
    }
    dataStoreTypes[dsKey] = dsType
    scannedDS[envID][svc][dsKey] = {}
    for (const field of fieldKeys) {
      if (!(field in scannedDS[envID][svc][dsKey])) scannedDS[envID][svc][dsKey][field] = ''
    }
    dsScanState[scanStateKey(envID, svc)] = { status: 'ok', reason: '包含人工补录的数据组件' }
    delete dsProbeResults[probeKey(envID, svc, dsKey)]
    recomputeEnabledDataStoresFromScanned()
  }

  return {
    dsImportStatus, dsImportStats, dsAutoFilled,
    scannedDS, dataStoreTypes, dataStoreType, dsScanState, dsProbeResults,
    scanStateKey, scanStateOf,
    removeScannedDS,
    addManualDataStore,
    recomputeEnabledDataStoresFromScanned,
  }
}
