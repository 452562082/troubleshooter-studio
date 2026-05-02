// yamlCodeDiff —— AnalyzePage 的"yaml 声明 vs 代码扫描实态"diff 计算。
//
// 输入:parsed yaml(infrastructure.config_center / repos[]) + analyzer report(repos[])
// 输出:per-repo 对账结果 + 整体 totalNew / totalMissing + config_center 一致性。
//
// 业务规则:
//   - SERVICE_ROLES(backend / gateway / middleware / admin)才对账 service_names
//   - 其它角色(frontend / mobile / common-lib / infra / docs)跳过 missing/new
//   - role 优先级:yaml 显式 role > analyzer 推断 role_hint.role > 'backend' 兜底
//   - service_names 接受字符串("a, b")或数组(["a", "b"])两种形态
//   - 业务服务且 yaml 没写 service_names → 用 repo.name 兜底进 yamlServices

export interface YamlVsCodeDiffItem {
  name: string
  yamlServices: string[]
  codeServices: string[]
  newInCode: string[]
  missingInCode: string[]
  isServiceRole: boolean
  effectiveRole: string
}

export interface YamlVsCodeDiff {
  repos: YamlVsCodeDiffItem[]
  configCenterYaml: string
  configCenterCode: string
  configCenterMismatch: boolean
  totalNew: number
  totalMissing: number
}

const SERVICE_ROLES = new Set(['backend', 'gateway', 'middleware', 'admin'])

interface YamlRepoLite {
  name?: string
  url?: string
  role?: string
  service_names?: string | string[]
}

interface CodeRepoLite {
  name: string
  service_names?: string[]
  role_hint?: { role?: string }
}

interface ParsedYaml {
  infrastructure?: { config_center?: { type?: string } }
  repos?: YamlRepoLite[]
}

interface AnalyzerReportLite {
  config_center?: string
  repos?: CodeRepoLite[]
}

export function computeYamlCodeDiff(
  parsedYaml: ParsedYaml,
  report: AnalyzerReportLite,
): YamlVsCodeDiff {
  const yamlRepos = Array.isArray(parsedYaml.repos) ? parsedYaml.repos : []
  const codeRepos = report.repos || []
  const out: YamlVsCodeDiffItem[] = []
  let totalNew = 0
  let totalMissing = 0

  for (const yRepo of yamlRepos) {
    const yName = String(yRepo.name || '')
    const yServicesRaw = yRepo.service_names
    const yServices: string[] = Array.isArray(yServicesRaw)
      ? yServicesRaw.map(s => String(s).trim()).filter(Boolean)
      : typeof yServicesRaw === 'string'
        ? yServicesRaw.split(',').map(s => s.trim()).filter(Boolean)
        : []
    const codeEntry = codeRepos.find(r => r.name === yName)
    const yamlRole = (typeof yRepo.role === 'string' && yRepo.role.trim()) ? yRepo.role.trim() : ''
    const hintedRole = codeEntry?.role_hint?.role || ''
    const effectiveRole = yamlRole || hintedRole || 'backend'
    const isServiceRole = SERVICE_ROLES.has(effectiveRole)
    const effectiveYaml = yServices.length > 0
      ? yServices
      : (isServiceRole ? [yName] : [])
    const cServices = codeEntry?.service_names || []
    let newIn: string[] = []
    let miss: string[] = []
    if (isServiceRole) {
      const ySet = new Set(effectiveYaml)
      const cSet = new Set(cServices)
      newIn = cServices.filter(s => !ySet.has(s))
      miss = effectiveYaml.filter(s => !cSet.has(s))
    }
    totalNew += newIn.length
    totalMissing += miss.length
    out.push({
      name: yName,
      yamlServices: effectiveYaml,
      codeServices: cServices,
      newInCode: newIn,
      missingInCode: miss,
      isServiceRole,
      effectiveRole,
    })
  }

  const configCenterYaml = String(parsedYaml.infrastructure?.config_center?.type || '')
  const configCenterCode = String(report.config_center || '')
  return {
    repos: out,
    configCenterYaml,
    configCenterCode,
    configCenterMismatch:
      configCenterYaml !== '' && configCenterCode !== ''
      && configCenterCode !== 'unknown' && configCenterYaml !== configCenterCode,
    totalNew,
    totalMissing,
  }
}
