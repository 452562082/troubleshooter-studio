// useSourceTypeReset —— 切配置源类型(nacos ↔ apollo ↔ consul ↔ env-vars ↔ kuboard ↔ none)时,
// 把 Step 5 / Step 7 里跟"上一种源"绑定的扫描状态全部清掉。
//
// 那些下拉选项 / 服务映射 / 识别出的数据层都基于旧源的 API 拉的,切源后完全无意义。
// 凭证输入(ccCredInputs)按 type 前缀分 key,保留不清,切回旧 type 还能复用。
//
// importInProgress 是 useImportFlow / useImportCrossCheck 共享的 ref:applyImport 反填阶段
// configCenterType 会在 "" → "nacos" 之间瞬变(reset → ingest 多源),期间禁用本 watcher 的
// 破坏性清空,否则刚反填的 envNamespaces / serviceConfigSel / ccHubStateByEnv 全没了。
import { watch, type Ref } from 'vue'
import { toast } from './toast'
import type { CCHubEnvState } from './useCCHubState'
import type { DSByService, DSScanState } from './useDataStoreState'

export interface UseSourceTypeResetDeps {
  configCenterType: Ref<string> | { value: string }
  importInProgress: Ref<boolean>

  // Step 5 状态(切源后清)
  envNamespaces: Record<string, string>
  serviceConfigSel: Record<string, string>
  serviceConfigGroup: Record<string, string>
  ccHubStateByEnv: Record<string, CCHubEnvState>

  // Step 7 数据层状态(切源后清)
  scannedDS: Record<string, DSByService>
  dsScanState: Record<string, DSScanState>
  dsAutoFilled: Record<string, boolean>
  dsImportStatus: { value: 'idle' | 'loading' | 'ok' | 'error' }
  dsImportStats: { scanned: number; matched: number }
}

export function useSourceTypeReset(deps: UseSourceTypeResetDeps) {
  watch(() => deps.configCenterType.value, (newType, oldType) => {
    if (newType === oldType) return
    if (deps.importInProgress.value) {
      // import 还在反填阶段,configCenterType 短暂变化是正常的(reset → ingest 多源),
      // 不要清空我们刚反填进去的 state。importInProgress 在 applyImport 开头置 true、
      // nextTick 里完成自动预加载触发后置 false。
      return
    }
    // 统计要清的项数,给用户一个"确实发生了清理"的提示
    const cleaned = {
      namespaces: Object.keys(deps.envNamespaces).length,
      services: Object.keys(deps.serviceConfigSel).length,
      scans: Object.keys(deps.ccHubStateByEnv).length,
      dsEntries: Object.keys(deps.scannedDS).length,
    }
    for (const k of Object.keys(deps.envNamespaces))      delete deps.envNamespaces[k]
    for (const k of Object.keys(deps.serviceConfigSel))   delete deps.serviceConfigSel[k]
    for (const k of Object.keys(deps.serviceConfigGroup)) delete deps.serviceConfigGroup[k]
    for (const k of Object.keys(deps.ccHubStateByEnv))    delete deps.ccHubStateByEnv[k]
    for (const k of Object.keys(deps.scannedDS))          delete deps.scannedDS[k]
    for (const k of Object.keys(deps.dsScanState))        delete deps.dsScanState[k]
    for (const k of Object.keys(deps.dsAutoFilled))       delete deps.dsAutoFilled[k]
    deps.dsImportStatus.value = 'idle'
    deps.dsImportStats.scanned = 0
    deps.dsImportStats.matched = 0
    const any = cleaned.namespaces || cleaned.services || cleaned.scans || cleaned.dsEntries
    if (any) {
      toast.info(`已切至 ${newType},清空上一源(${oldType})的 Step 5/7 扫描与数据层识别结果`)
    }
  })
}
