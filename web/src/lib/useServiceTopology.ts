import { computed, ref, type Ref } from 'vue'
import { topology } from '../../wailsjs/go/models'
import { analyzeServiceTopology } from './bridge'
import type { ServiceTopologyOverrideState } from './yamlGenerator'

export interface UseServiceTopologyOptions {
  overrides: Ref<ServiceTopologyOverrideState[]>
  yamlText: () => string
  repoPaths: () => Record<string, string>
  analyze?: (yamlText: string, repoPaths: Record<string, string>) => Promise<topology.Snapshot>
  blocked?: () => boolean
}

function normalizedProtocol(value: string): 'http' | 'grpc' {
  return value.trim().toLowerCase() === 'grpc' ? 'grpc' : 'http'
}

function normalizedMethod(value?: string): string | undefined {
  const method = value?.trim().toUpperCase()
  return method || undefined
}

function normalizedOptional(value?: string): string | undefined {
  const normalized = value?.trim()
  return normalized || undefined
}

function normalizedRoutePath(value?: string): string {
  let path = value?.trim() ?? ''
  if (/^[a-z][a-z0-9+.-]*:\/\//i.test(path)) {
    try { path = new URL(path).pathname } catch { /* fall back to the raw path below */ }
  } else {
    path = path.split(/[?#]/, 1)[0] ?? ''
  }
  path = path.replace(/\/{2,}/g, '/')
  if (path.length > 1) path = path.replace(/\/$/, '')
  return path.split('/').map(segment => {
    if (segment.startsWith('*')) return '{wildcard}'
    if (segment === '{wildcard}') return segment
    if (segment.startsWith(':')) return segment.endsWith('*') ? '{wildcard}' : '{param}'
    if (segment.startsWith('{') && segment.endsWith('}')) {
      return segment.slice(1).startsWith('*') ? '{wildcard}' : '{param}'
    }
    if (segment.startsWith('[') && segment.endsWith(']')) {
      return segment.includes('...') ? '{wildcard}' : '{param}'
    }
    return segment
  }).join('/')
}

function normalizedRPCMethod(value?: string): string {
  return (value?.trim() ?? '').replace(/^\/+/, '')
}

export function overrideForEdge(
  action: 'confirm' | 'reject' | 'add',
  edge: topology.CandidateEdge,
  toService = edge.to_service,
): ServiceTopologyOverrideState {
  return {
    action,
    fromService: edge.from_service.trim(),
    toService: toService.trim(),
    protocol: normalizedProtocol(edge.protocol),
    method: normalizedMethod(edge.method),
    path: normalizedOptional(edge.path),
    rpcMethod: normalizedOptional(edge.rpc_method),
  }
}

function semanticKey(value: ServiceTopologyOverrideState): string {
  const protocol = normalizedProtocol(value.protocol)
  return [
    value.fromService.trim(),
    value.toService.trim(),
    protocol,
    protocol === 'http' ? normalizedMethod(value.method) ?? '' : '',
    protocol === 'http' ? normalizedRoutePath(value.path) : '',
    protocol === 'grpc' ? normalizedRPCMethod(value.rpcMethod) : '',
  ].join('\u001f')
}

function edgeKey(edge: topology.CandidateEdge): string {
  return semanticKey(overrideForEdge('confirm', edge))
}

export function upsertTopologyOverride(
  current: readonly ServiceTopologyOverrideState[],
  decision: ServiceTopologyOverrideState,
): ServiceTopologyOverrideState[] {
  const key = semanticKey(decision)
  return [...current.filter(item => semanticKey(item) !== key), decision]
}

export function retargetTopologyOverride(
  current: readonly ServiceTopologyOverrideState[],
  edge: topology.CandidateEdge,
  toService: string,
): ServiceTopologyOverrideState[] {
  const rejected = overrideForEdge('reject', edge)
  const replacement = overrideForEdge('add', edge, toService)
  return upsertTopologyOverride(upsertTopologyOverride(current, rejected), replacement)
}

function derivedSnapshot(
  source: topology.Snapshot | null,
  overrides: readonly ServiceTopologyOverrideState[],
): topology.Snapshot | null {
  if (!source) return null

  const decisions = new Map(overrides.map(item => [semanticKey(item), item]))
  const presentKeys = new Set<string>()
  const edges = (source.edges ?? []).map((edge) => {
    const key = edgeKey(edge)
    presentKeys.add(key)
    const decision = decisions.get(key)
    let status = edge.status
    // 后端把“仍有 confirm override，但本轮证据消失”的关系标为 stale。
    // 这类状态不能被本地即时派生重新涂成 confirmed，否则用户看不到证据已过期。
    if (decision?.action === 'confirm' && edge.status !== 'stale') status = 'confirmed'
    if (decision?.action === 'reject') status = 'rejected'
    if (decision?.action === 'add') status = 'manual'
    return topology.CandidateEdge.createFrom({ ...edge, status })
  })

  for (const decision of overrides) {
    const key = semanticKey(decision)
    if (presentKeys.has(key) || decision.action === 'reject') continue
    edges.push(topology.CandidateEdge.createFrom({
      from_endpoint: '',
      to_endpoint: '',
      from_service: decision.fromService,
      to_service: decision.toService,
      protocol: decision.protocol,
      method: decision.method,
      path: decision.path,
      rpc_method: decision.rpcMethod,
      confidence: decision.action === 'add' ? 1 : 0,
      status: decision.action === 'add' ? 'manual' : 'stale',
      reasons: [decision.action === 'add' ? 'human_override_add' : 'human_override_confirm_stale'],
      conflicts: [],
    }))
  }

  return topology.Snapshot.createFrom({
    schema_version: source.schema_version,
    services: source.services ?? [],
    endpoints: source.endpoints ?? [],
    edges,
    repositories: source.repositories ?? [],
  })
}

export function useServiceTopology(options: UseServiceTopologyOptions) {
  const rawSnapshot = ref<topology.Snapshot | null>(null)
  const loading = ref(false)
  const error = ref('')
  let generation = 0

  const snapshot = computed(() => derivedSnapshot(rawSnapshot.value, options.overrides.value))

  async function refresh(): Promise<boolean> {
    if (loading.value || options.blocked?.()) return false
    const currentGeneration = ++generation
    loading.value = true
    error.value = ''
    try {
      const runAnalyze = options.analyze ?? analyzeServiceTopology
      const result = await runAnalyze(options.yamlText(), options.repoPaths())
      if (currentGeneration !== generation) return false
      rawSnapshot.value = result
      return true
    } catch (cause) {
      if (currentGeneration !== generation) return false
      const message = cause instanceof Error ? cause.message : String(cause)
      error.value = `拓扑分析失败: ${message}`
      return false
    } finally {
      if (currentGeneration === generation) loading.value = false
    }
  }

  function confirm(edge: topology.CandidateEdge) {
    options.overrides.value = upsertTopologyOverride(options.overrides.value, overrideForEdge('confirm', edge))
  }

  function reject(edge: topology.CandidateEdge) {
    options.overrides.value = upsertTopologyOverride(options.overrides.value, overrideForEdge('reject', edge))
  }

  function retarget(edge: topology.CandidateEdge, toService: string) {
    const target = toService.trim()
    if (!target || target === edge.to_service.trim()) return
    options.overrides.value = retargetTopologyOverride(options.overrides.value, edge, target)
  }

  function add(decision: ServiceTopologyOverrideState) {
    if (!decision.fromService.trim() || !decision.toService.trim()) return
    options.overrides.value = upsertTopologyOverride(options.overrides.value, { ...decision, action: 'add' })
  }

  function clear() {
    generation++
    loading.value = false
    error.value = ''
    rawSnapshot.value = null
    options.overrides.value = []
  }

  return { snapshot, loading, error, refresh, confirm, reject, retarget, add, clear }
}
