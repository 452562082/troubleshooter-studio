import { probeKey } from './yamlShared'

export type CoverageState = 'ready' | 'partial' | 'missing' | 'na'

export interface CoverageCell {
  state: CoverageState
  label: string
}

export interface ResourceCoverageRow {
  env: string
  resource: string
  kind: 'service' | 'workload'
  code: CoverageCell
  config: CoverageCell
  data: CoverageCell
  runtime: CoverageCell
  logs: CoverageCell
  trace: CoverageCell
}

export interface ResourceCoverage {
  level: 'blocked' | 'basic' | 'standard' | 'complete'
  rows: ResourceCoverageRow[]
  ready: number
  partial: number
  missing: number
}

interface CoverageRepo {
  name: string
  role?: string
  stack?: string
  service_names?: string
  env_branches?: Record<string, string>
}

export interface BuildResourceCoverageInput {
  environments: Array<{ id: string }>
  repos: CoverageRepo[]
  businessServices: readonly string[]
  runtimeWorkloads: readonly string[]
  activeSourceTypes: readonly string[]
  getServiceSource: (service: string, envID?: string) => string
  scannedDS: Record<string, Record<string, Record<string, Record<string, string>>>>
  dsProbeResults: Record<string, { status?: string } | undefined>
  enabledObservability: Record<string, boolean>
  obsProbeResults: Record<string, { status?: string } | undefined>
  k8sRuntimeEnvLoc: Record<string, { cluster?: string; cluster_id?: string; namespace?: string } | undefined>
  k8sRuntimeSvcMap: Record<string, { workload?: string } | undefined>
  svcKey: (env: string, service: string) => string
  lokiMappingByEnv: Record<string, {
    dsUID?: string
    envLabelKey?: string
    serviceLabelKey?: string
    envValue?: string
    serviceValues?: Record<string, string>
  } | undefined>
}

function findRepo(repos: CoverageRepo[], resource: string): CoverageRepo | undefined {
  return repos.find(repo => (repo.service_names || '').split(',').map(s => s.trim()).includes(resource))
    || repos.find(repo => repo.name.trim() === resource)
}

export function buildResourceCoverage(input: BuildResourceCoverageInput): ResourceCoverage {
  const business = new Set(input.businessServices)
  const rows: ResourceCoverageRow[] = []
  const explicitNoConfig = input.activeSourceTypes.includes('none')
  const traceTools = ['jaeger', 'tempo', 'skywalking'].filter(k => input.enabledObservability[k])

  for (const env of input.environments) {
    if (!env.id) continue
    for (const resource of input.runtimeWorkloads) {
      const repo = findRepo(input.repos, resource)
      const branch = repo?.env_branches?.[env.id]
      const isBusiness = business.has(resource)
      const code: CoverageCell = !repo
        ? { state: 'missing', label: '未关联仓库' }
        : !repo.stack?.trim()
          ? { state: 'missing', label: '技术栈未确认' }
          : branch
            ? { state: 'ready', label: `${repo.stack} · ${branch}` }
            : { state: 'partial', label: `${repo.stack} · 默认分支` }

      let config: CoverageCell
      if (!isBusiness) config = { state: 'na', label: '无需配置源' }
      else if (explicitNoConfig) config = { state: 'na', label: '明确无配置源' }
      else {
        const source = input.getServiceSource(resource, env.id)
        config = source
          ? { state: 'ready', label: source }
          : { state: 'missing', label: '未分配配置源' }
      }

      let data: CoverageCell
      if (!isBusiness) data = { state: 'na', label: '无需扫描' }
      else {
        const types = Object.keys(input.scannedDS[env.id]?.[resource] || {})
        if (types.length === 0) data = { state: 'partial', label: '未发现/待确认' }
        else {
          const states = types.map(type => input.dsProbeResults[probeKey(env.id, resource, type)]?.status)
          data = states.every(s => s === 'ok')
            ? { state: 'ready', label: types.join(', ') }
            : states.some(s => s === 'fail')
              ? { state: 'missing', label: `${types.join(', ')} · 有连接失败` }
              : { state: 'partial', label: `${types.join(', ')} · 待测试` }
        }
      }

      let runtime: CoverageCell = { state: 'na', label: '未启用 K8s' }
      if (input.enabledObservability.k8s_runtime) {
        const loc = input.k8sRuntimeEnvLoc[env.id]
        const svcLoc = input.k8sRuntimeSvcMap[input.svcKey(env.id, resource)]
        if (!loc?.namespace || (!loc.cluster && !loc.cluster_id)) runtime = { state: 'missing', label: '未选集群/Namespace' }
        else if (svcLoc?.workload) runtime = { state: 'ready', label: svcLoc.workload }
        else runtime = { state: 'partial', label: `${loc.namespace} · 模糊匹配` }
      }

      let logs: CoverageCell = { state: 'na', label: '未启用日志' }
      if (input.enabledObservability.loki) {
        const lm = input.lokiMappingByEnv[env.id]
        const serviceValue = lm?.serviceValues?.[resource]
        if (lm?.dsUID && lm.envLabelKey && lm.serviceLabelKey && lm.envValue && serviceValue) {
          logs = { state: 'ready', label: `${lm.serviceLabelKey}=${serviceValue}` }
        } else if (lm?.dsUID) logs = { state: 'partial', label: 'Datasource 已选，标签未完整' }
        else logs = { state: 'missing', label: 'Loki datasource 未选择' }
      } else if (input.enabledObservability.elk) {
        logs = { state: 'partial', label: 'ELK 已接入，按服务搜索' }
      }

      let trace: CoverageCell = { state: 'na', label: '未启用 Trace' }
      if (traceTools.length > 0) {
        const ok = traceTools.every(tool => input.obsProbeResults[`${tool}::${env.id}`]?.status === 'ok'
          || tool === 'tempo')
        trace = ok
          ? { state: 'ready', label: traceTools.join(', ') }
          : { state: 'partial', label: `${traceTools.join(', ')} · 待连接` }
      }

      rows.push({ env: env.id, resource, kind: isBusiness ? 'service' : 'workload', code, config, data, runtime, logs, trace })
    }
  }

  const cells = rows.flatMap(row => [row.code, row.config, row.data, row.runtime, row.logs, row.trace])
  const missing = cells.filter(c => c.state === 'missing').length
  const partial = cells.filter(c => c.state === 'partial').length
  const ready = cells.filter(c => c.state === 'ready').length
  const codeOrConfigMissing = rows.some(row => row.code.state === 'missing' || row.config.state === 'missing')
  const hasRuntimeEvidence = rows.some(row => row.runtime.state === 'ready' || row.logs.state === 'ready')
  const enabledOptionalCells = rows.flatMap(row => [row.runtime, row.logs, row.trace]).filter(c => c.state !== 'na')
  const allOptionalReady = enabledOptionalCells.length > 0 && enabledOptionalCells.every(c => c.state === 'ready')
  const allDataReady = rows.every(row => row.data.state === 'ready' || row.data.state === 'na')
  const level: ResourceCoverage['level'] = codeOrConfigMissing
    ? 'blocked'
    : allOptionalReady && allDataReady
      ? 'complete'
      : hasRuntimeEvidence
        ? 'standard'
        : 'basic'
  return { level, rows, ready, partial, missing }
}
