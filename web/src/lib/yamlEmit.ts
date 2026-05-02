// yamlEmit.ts —— YAML 字符串生成的纯工具集合。
// 从 InitPage::generateYAML 抽出来,只放"输入 → 输出"型纯函数;依赖 Vue
// reactive 状态的部分(generateYAML 主体)还在 InitPage 里。
//
// 单测可达:lib 文件可被 vitest 直接 import,不必走 mount InitPage。
// 后续 generateYAML 整体抽出到 lib 时,这层先做"零依赖工具"沉淀。

/** YAML 字符串字面量化:含特殊字符时加双引号 + 转义反斜杠和双引号。 */
export function yamlStr(val: string): string {
  if (!val) return '""'
  if (/[:{}\[\],&*?|>!%#@`'"\n]/.test(val) || val.startsWith(' ') || val.endsWith(' ')) {
    return `"${val.replace(/\\/g, '\\\\').replace(/"/g, '\\"')}"`
  }
  return `"${val}"`
}

// ── Loki 标签映射 emit ─────────────────────────────────────────────
// label_mapping_by_env 节生成,InitPage 在两处复用(loki 启用 / loki 未启用但有映射兜底块)。

export interface LokiEnvMapping {
  envLabelKey: string
  serviceLabelKey: string
  envValue?: string
  dsUID?: string
  serviceValues?: Record<string, string>
}

export interface LokiEmitDeps {
  environments: Array<{ id: string }>
  lokiMappingByEnv: Record<string, LokiEnvMapping | undefined>
  allServiceNames: string[]
}

/** 当前 environments 是否有任意 env 配过完整的 Loki 标签映射。
 *  yaml 是否输出 loki.label_mapping_by_env 节的开关。 */
export function hasAnyLokiMapping(deps: LokiEmitDeps): boolean {
  return deps.environments.some(env => {
    if (!env.id) return false
    const lm = deps.lokiMappingByEnv[env.id]
    return !!(lm && lm.envLabelKey && lm.serviceLabelKey)
  })
}

/** 把 label_mapping_by_env 块追加到 lines 上。
 *  indent 是父字段的缩进(eg 6 空格表 loki: 字段),内部按 +2 空格逐层缩进。 */
export function emitLokiLabelMapping(lines: string[], indent: string, deps: LokiEmitDeps): void {
  const lmEnvs = deps.environments.filter(env => env.id && deps.lokiMappingByEnv[env.id]
    && deps.lokiMappingByEnv[env.id]!.envLabelKey && deps.lokiMappingByEnv[env.id]!.serviceLabelKey)
  if (lmEnvs.length === 0) return
  lines.push(`${indent}label_mapping_by_env:    # routing skill 拼 LogQL 时按 (env, service) 注入 label 过滤器`)
  for (const env of lmEnvs) {
    const lm = deps.lokiMappingByEnv[env.id]!
    lines.push(`${indent}  ${env.id}:`)
    lines.push(`${indent}    env_label: ${yamlStr(lm.envLabelKey)}`)
    lines.push(`${indent}    service_label: ${yamlStr(lm.serviceLabelKey)}`)
    if (lm.dsUID) lines.push(`${indent}    grafana_ds_uid: ${yamlStr(lm.dsUID)}`)
    if (lm.envValue) {
      lines.push(`${indent}    ${lm.envLabelKey}: ${yamlStr(lm.envValue)}`)
    }
    const svcLines: string[] = []
    for (const svc of deps.allServiceNames) {
      const v = (lm.serviceValues || {})[svc]
      // 空值 = "无 / 不进 loki":不写 service 块,routing skill 该服务跳过 loki 查询
      if (!v) continue
      svcLines.push(`${indent}      ${yamlStr(svc)}:`)
      svcLines.push(`${indent}        ${lm.serviceLabelKey}: ${yamlStr(v)}`)
    }
    if (svcLines.length > 0) {
      lines.push(`${indent}    service_map:`)
      lines.push(...svcLines)
    }
  }
}
