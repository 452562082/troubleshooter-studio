// useObsAccessMode —— 每个 (obs tool, env) 的访问方式:
//   - via_grafana: 走 Grafana 代理(选 datasource UID,不再填本工具的 URL+auth)
//   - direct:     直连(填本工具自己的 URL+auth)
//
// 锁死规则(2026-05 后):每个工具在本系统**只有一条路径**,wizard 不再让用户选:
//   - VIA_GRAFANA_ONLY(loki/prometheus/tempo):mcp-grafana-npx 内置 query_loki_logs /
//     query_prometheus 等工具,无独立 MCP 包 → 永远 via_grafana
//   - DIRECT_ONLY(jaeger/elk):独立 MCP 包(opentelemetry-mcp / @elastic/mcp-server-elasticsearch)
//     直连 → 永远 direct
//   - 其它工具(grafana / k8s_runtime / skywalking 等):永远 direct
//
// obsAccessModeMap 保留是为兼容老 yaml 反填(老用户配过 via_grafana=true 的 jaeger 等),
// 但读取时会被锁死规则覆盖,不影响 yaml emit;setObsAccessMode 仍可写但等于 no-op。
import { reactive } from 'vue'
import { VIA_GRAFANA_ELIGIBLE, VIA_GRAFANA_ONLY, DIRECT_ONLY } from './yamlShared'

export type ObsAccessMode = 'via_grafana' | 'direct'
// re-export 给已经 from './useObsAccessMode' 拿这个常量的老调用方;新代码请直接走 yamlShared。
export { VIA_GRAFANA_ELIGIBLE, VIA_GRAFANA_ONLY, DIRECT_ONLY }

export function useObsAccessMode(deps: {
  initialMap?: Record<string, ObsAccessMode>
  /** 老接口:之前根据 grafana 是否启用决定默认值,现在锁死规则覆盖,本字段保留以免破坏调用方。 */
  enabledObservability: Record<string, boolean>
}) {
  void deps.enabledObservability // 锁死规则下不再依赖此字段,但保留参数避免 callsite 改一遍
  const obsAccessModeMap = reactive<Record<string, ObsAccessMode>>(deps.initialMap ?? {})

  function getObsAccessMode(obsKey: string, _envID: string): ObsAccessMode {
    if ((VIA_GRAFANA_ONLY as readonly string[]).includes(obsKey)) return 'via_grafana'
    if ((DIRECT_ONLY as readonly string[]).includes(obsKey)) return 'direct'
    return 'direct'
  }

  function setObsAccessMode(obsKey: string, envID: string, mode: ObsAccessMode) {
    // 仅留 map 写入兼容 yaml importer 反填路径,实际 getObsAccessMode 总走锁死规则。
    obsAccessModeMap[obsAccessKey(obsKey, envID)] = mode
  }

  return { obsAccessModeMap, obsAccessKey, getObsAccessMode, setObsAccessMode }
}

// 模板/外部 importer 用的 key 拼接,跟 reactive map 的 key 完全一致。
// 单独导出方便 yamlImporter 不接 composable 也能算 key。
export function obsAccessKey(obsKey: string, envID: string): string {
  return `${obsKey}:${envID}`
}
