// useObsAccessMode —— 每个 (obs tool, env) 的访问方式开关:
//   - via_grafana: 走 Grafana 代理(只需选 datasource UID,不再填本工具的 URL+auth)
//   - direct:     直连(填本工具自己的 URL+auth)
//
// 默认值规则:
//   - 工具不在 VIA_GRAFANA_ELIGIBLE 候选(loki/prometheus/jaeger/tempo/elk)→ 永远 direct
//   - 候选内 + Grafana 启用 → via_grafana
//   - 候选内 + Grafana 未启用 → direct
//
// 用户在 ObservabilityToolBlock 顶部 select 显式切换会写 obsAccessModeMap,后续 getObsAccessMode
// 读到非空就用显式值。
import { reactive } from 'vue'
import { VIA_GRAFANA_ELIGIBLE } from './yamlShared'

export type ObsAccessMode = 'via_grafana' | 'direct'
// re-export 给已经 from './useObsAccessMode' 拿这个常量的老调用方;新代码请直接走 yamlShared。
export { VIA_GRAFANA_ELIGIBLE }

export function useObsAccessMode(deps: {
  initialMap?: Record<string, ObsAccessMode>
  /** Step 7 顶部勾选 map;getObsAccessMode 用它判断"Grafana 启没启用"决定默认值 */
  enabledObservability: Record<string, boolean>
}) {
  const obsAccessModeMap = reactive<Record<string, ObsAccessMode>>(deps.initialMap ?? {})

  function getObsAccessMode(obsKey: string, envID: string): ObsAccessMode {
    if (!(VIA_GRAFANA_ELIGIBLE as readonly string[]).includes(obsKey)) return 'direct'
    const explicit = obsAccessModeMap[obsAccessKey(obsKey, envID)]
    if (explicit) return explicit
    return deps.enabledObservability['grafana'] ? 'via_grafana' : 'direct'
  }

  function setObsAccessMode(obsKey: string, envID: string, mode: ObsAccessMode) {
    obsAccessModeMap[obsAccessKey(obsKey, envID)] = mode
  }

  return { obsAccessModeMap, obsAccessKey, getObsAccessMode, setObsAccessMode }
}

// 模板/外部 importer 用的 key 拼接,跟 reactive map 的 key 完全一致。
// 单独导出方便 yamlImporter 不接 composable 也能算 key。
export function obsAccessKey(obsKey: string, envID: string): string {
  return `${obsKey}:${envID}`
}
