// yamlShared —— yaml emit / generator / validator / importer 四件套 + InitPage / 各 step
// 子组件之间共用的小常量、key 拼接、locator 类型。集中一处避免:
//   - InitPage 跟 yamlValidator 各定义一份 ccKeyFor / svcKey / probeKey,字符串 schema
//     不慎漂移 → 模板里 v-if 的 key 算出来跟 reactive 里不一样,UI 默默坏掉
//   - VIA_GRAFANA_ELIGIBLE 散在 yamlGenerator + useObsAccessMode 两份,加新候选要改两处
//   - KuboardSvcLocator 同样形状跨 yamlGenerator + yamlValidator 各写一遍

/** 通过 Grafana 代理访问的 obs 工具候选(loki/prometheus/jaeger/tempo/elk)。
 *  yamlGenerator / useObsAccessMode / yamlImporter 三处共用,加新候选只改这里。 */
export const VIA_GRAFANA_ELIGIBLE = ['loki', 'prometheus', 'jaeger', 'tempo', 'elk'] as const

/** Kuboard 三级定位:cluster / namespace / configmap;config-map.yaml 的服务行用。 */
export interface KuboardSvcLocator {
  cluster?: string
  namespace?: string
  configmap?: string
}

// ── 共享 key 拼接 ──────────────────────────────────────────────────
// reactive map 的 key 必须由这三个函数算,InitPage 模板和 ts 端任何一边漂移都会
// 让模板 v-if 跟 reactive 写入对不上,UI 默默坏掉。

/** ccCredInputs map 的 key:cc:<type>:<envID>:<field> */
export function ccKeyFor(type: string, envID: string, field: string): string {
  return `cc:${type}:${envID}:${field}`
}

/** kuboardSvcMap / k8sRuntimeSvcMap 等"per-(env,service)"map 的 key:<envID>::<svc> */
export function svcKey(envID: string, svc: string): string {
  return `${envID}::${svc}`
}

/** dsProbeResults 等"per-(env,service,dsKey)"map 的 key:<envID>::<svc>::<dsKey> */
export function probeKey(envID: string, svc: string, dsKey: string): string {
  return `${envID}::${svc}::${dsKey}`
}

// ── 占位符 / source 段命名 ────────────────────────────────────────
// 注意:install 侧(internal/agent.envVar / InitPage::installEnvVarName)的顺序是
// "<PREFIX>_<SOURCE>_<ENV>",跟下方 yaml 侧的 "<ENV_VAR>_<SOURCE>" 不一样,刻意
// 保持两份;Go 端 envVar() 是真源,本文件只覆盖 yaml emit / yaml import 这一侧。

/** sourceID 的环境变量段:'legacy-nacos' → 'LEGACY_NACOS';default 源 / 空串 → ''。 */
export function sourceIDToEnvSegment(sourceID: string): string {
  if (!sourceID || sourceID === 'default') return ''
  return sourceID.toUpperCase().replace(/-/g, '_')
}

/** 多源场景下,字段值缺失时 yaml emit 给的环境变量占位符名(env 段在前)。
 *  例:envVar=`CC_ADDR_DEV`,sourceID='legacy-nacos' → "CC_ADDR_DEV_LEGACY_NACOS"。
 *  default 源 / 空串不加后缀,跟 single-source 命名兼容。
 *  yamlGenerator emit + yamlImporter 反推占位符两边共用。 */
export function placeholderName(envVarBase: string, sourceID: string): string {
  const seg = sourceIDToEnvSegment(sourceID)
  return seg ? `${envVarBase}_${seg}` : envVarBase
}
