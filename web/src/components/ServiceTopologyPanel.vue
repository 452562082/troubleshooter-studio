<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { topology } from '../../wailsjs/go/models'
import type { ServiceTopologyOverrideState } from '../lib/yamlGenerator'
import { upsertTopologyOverride } from '../lib/useServiceTopology'

const props = withDefaults(defineProps<{
  snapshot: topology.Snapshot | null
  overrides: ServiceTopologyOverrideState[]
  configuredServices?: string[]
  loading: boolean
  disabled?: boolean
  error?: string
}>(), {
  configuredServices: () => [],
  disabled: false,
  error: '',
})

const emit = defineEmits<{
  'update:overrides': [value: ServiceTopologyOverrideState[]]
  refresh: []
}>()

const selectedRelationKey = ref('')
const retargetService = ref('')
const addFrom = ref('')
const addTo = ref('')
const retargetTouched = ref(false)
const addTouched = ref(false)

const locked = computed(() => props.loading || props.disabled)
const evidenceEdges = computed(() => {
  const source = props.snapshot?.edges ?? []
  return [...source].sort((left, right) => {
    const priority = (status: string) => status === 'candidate' ? 0 : status === 'stale' ? 1 : 2
    return priority(left.status) - priority(right.status)
  })
})
interface RelationGroup {
  key: string
  fromService: string
  toService: string
  edges: topology.CandidateEdge[]
  status: string
  confidence: number
  candidateCount: number
}
function relationKey(fromService: string, toService: string): string {
  return `${fromService.trim()}\u001f${toService.trim()}`
}
function relationStatus(edges: topology.CandidateEdge[]): string {
  const priority = ['candidate', 'manual', 'confirmed', 'automatic', 'rejected', 'stale']
  return priority.find(status => edges.some(edge => edge.status === status)) ?? edges[0]?.status ?? ''
}
const relationGroups = computed<RelationGroup[]>(() => {
  const groups = new Map<string, RelationGroup>()
  for (const edge of evidenceEdges.value) {
    const key = relationKey(edge.from_service, edge.to_service)
    let group = groups.get(key)
    if (!group) {
      group = {
        key,
        fromService: edge.from_service,
        toService: edge.to_service,
        edges: [],
        status: '',
        confidence: 0,
        candidateCount: 0,
      }
      groups.set(key, group)
    }
    group.edges.push(edge)
    group.confidence = Math.max(group.confidence, edge.confidence)
    if (edge.status === 'candidate') group.candidateCount++
  }
  return [...groups.values()].map(group => ({ ...group, status: relationStatus(group.edges) })).sort((left, right) => {
    const priority = (status: string) => status === 'candidate' ? 0 : status === 'stale' ? 1 : 2
    return priority(left.status) - priority(right.status)
      || left.fromService.localeCompare(right.fromService)
      || left.toService.localeCompare(right.toService)
  })
})
const services = computed(() => [...new Set([
  ...props.configuredServices,
  ...(props.snapshot?.services ?? []).map(service => service.service),
].map(service => service.trim()).filter(Boolean))].sort((left, right) => left.localeCompare(right)))
const endpoints = computed(() => props.snapshot?.endpoints ?? [])
const repositories = computed(() => props.snapshot?.repositories ?? [])
const scannedRepositoryCount = computed(() => repositories.value.filter(repo => repo.state === 'scanned').length)
const outboundEndpointCount = computed(() => endpoints.value.filter(endpoint => endpoint.direction === 'outbound').length)
const inboundEndpointCount = computed(() => endpoints.value.filter(endpoint => endpoint.direction === 'inbound').length)
const serviceSet = computed(() => new Set(services.value))
function serviceValue(value: string | undefined): string {
  return value?.trim() ?? ''
}
const retargetValid = computed(() => {
  const group = selectedRelation.value
  const target = serviceValue(retargetService.value)
  return !!group && serviceSet.value.has(target) && target !== group.toService.trim()
})
const manualServicesValid = computed(() => {
  const fromService = serviceValue(addFrom.value)
  const toService = serviceValue(addTo.value)
  return serviceSet.value.has(fromService)
    && serviceSet.value.has(toService)
    && fromService !== toService
})
const validationMessage = computed(() => {
  if (retargetTouched.value && !retargetValid.value) {
    const target = serviceValue(retargetService.value)
    if (target && target === selectedRelation.value?.toService.trim()) return '新目标服务不能与当前目标相同。'
    return '请选择快照中的有效服务作为新目标。'
  }
  if (addTouched.value && !manualServicesValid.value) {
    const fromService = serviceValue(addFrom.value)
    const toService = serviceValue(addTo.value)
    if (fromService && fromService === toService) return '来源服务与目标服务不能相同。'
    return '请选择快照中的有效服务作为来源和目标。'
  }
  return ''
})

function edgeKey(edge: topology.CandidateEdge): string {
  return [
    edge.from_endpoint,
    edge.to_endpoint,
    edge.from_service,
    edge.to_service,
    edge.protocol,
    edge.method ?? '',
    edge.path ?? '',
    edge.rpc_method ?? '',
  ].join('\u001f')
}

const selectedRelation = computed(() => (
  relationGroups.value.find(group => group.key === selectedRelationKey.value) ?? null
))
const canConfirmSelectedRelation = computed(() => {
  const status = selectedRelation.value?.status
  return status === 'candidate' || status === 'rejected'
})
const canRejectSelectedRelation = computed(() => (
  !!selectedRelation.value && selectedRelation.value.status !== 'rejected'
))
const decisionFeedback = computed(() => {
  switch (selectedRelation.value?.status) {
    case 'confirmed': return '该关系已确认并写入 YAML，无需重复确认。'
    case 'automatic': return '该关系已由扫描结果自动采纳；如判断错误可拒绝。'
    case 'manual': return '该关系由人工新增并已写入 YAML；如需撤销可拒绝。'
    case 'rejected': return '该关系已拒绝；可以重新确认或重定到其他目标服务。'
    case 'stale': return '该关系的确认证据已失效，请重新扫描后再决定。'
    default: return '确认或拒绝会立即更新 YAML overrides。'
  }
})

watch(relationGroups, (nextGroups) => {
  if (nextGroups.some(group => group.key === selectedRelationKey.value)) return
  const preferred = nextGroups.find(group => group.status === 'candidate') ?? nextGroups[0]
  selectedRelationKey.value = preferred?.key ?? ''
}, { immediate: true })

watch(selectedRelation, (group) => {
  retargetService.value = group?.toService ?? ''
  retargetTouched.value = false
})

function endpointsForEdge(edge: topology.CandidateEdge): topology.Endpoint[] {
  const ids = new Set([edge.from_endpoint, edge.to_endpoint].filter(Boolean))
  return (props.snapshot?.endpoints ?? []).filter(endpoint => ids.has(endpoint.id))
}

const statusMeta: Record<string, { label: string, className: string }> = {
  automatic: { label: '自动采纳', className: 'status--automatic' },
  candidate: { label: '待确认', className: 'status--candidate' },
  confirmed: { label: '已确认', className: 'status--confirmed' },
  manual: { label: '人工新增', className: 'status--manual' },
  rejected: { label: '已拒绝', className: 'status--rejected' },
  stale: { label: '已失效', className: 'status--stale' },
}

function statusFor(status: string) {
  return statusMeta[status] ?? { label: status || '未知', className: 'status--unknown' }
}

function selectRelation(group: RelationGroup) {
  selectedRelationKey.value = group.key
}

function emitDecision(action: 'confirm' | 'reject') {
  const group = selectedRelation.value
  if (!group || locked.value) return
  if (action === 'confirm' && !canConfirmSelectedRelation.value) return
  if (action === 'reject' && !canRejectSelectedRelation.value) return
  emit('update:overrides', upsertTopologyOverride(props.overrides, {
    action,
    scope: 'service',
    fromService: group.fromService,
    toService: group.toService,
  }))
}

function emitRetarget() {
  retargetTouched.value = true
  const group = selectedRelation.value
  const target = serviceValue(retargetService.value)
  if (!group || !retargetValid.value || locked.value) return
  const rejected: ServiceTopologyOverrideState = {
    action: 'reject', scope: 'service', fromService: group.fromService, toService: group.toService,
  }
  const replacement: ServiceTopologyOverrideState = {
    action: 'add', scope: 'service', fromService: group.fromService, toService: target,
  }
  emit('update:overrides', upsertTopologyOverride(upsertTopologyOverride(props.overrides, rejected), replacement))
}

function emitAdd() {
  addTouched.value = true
  const fromService = serviceValue(addFrom.value)
  const toService = serviceValue(addTo.value)
  if (!manualServicesValid.value || locked.value) return
  const decision: ServiceTopologyOverrideState = {
    action: 'add',
    scope: 'service',
    fromService,
    toService,
  }
  emit('update:overrides', upsertTopologyOverride(props.overrides, decision))
  addFrom.value = ''
  addTo.value = ''
  addTouched.value = false
}

function requestRefresh() {
  if (!locked.value) emit('refresh')
}
</script>

<template>
  <section class="topology-panel" aria-labelledby="service-topology-title">
    <header class="topology-header">
      <div>
        <p class="topology-kicker">跨仓服务拓扑</p>
        <h3 id="service-topology-title">确认代码识别出的调用关系</h3>
        <p class="topology-description">
          扫描结果只作证据；确认、拒绝和人工新增仅写入 YAML overrides。
        </p>
      </div>
      <button
        class="topology-button topology-button--primary"
        type="button"
        data-action="refresh"
        :disabled="locked"
        @click="requestRefresh"
      >{{ loading ? '扫描调用关系中…' : '重新扫描调用关系' }}</button>
    </header>

    <p v-if="error" class="topology-error" role="alert">{{ error }}</p>
    <p class="topology-feedback" data-feedback aria-live="polite">
      <template v-if="loading">正在分析仓库端点和跨服务调用关系，请稍候。</template>
      <template v-else-if="disabled">其他任务正在运行，拓扑操作暂时不可用。</template>
      <template v-else-if="snapshot">
        已扫描 {{ scannedRepositoryCount }}/{{ repositories.length }} 个仓库，识别
        {{ outboundEndpointCount }} 个调用出口、{{ inboundEndpointCount }} 个服务入口，形成
        {{ evidenceEdges.length }} 条端点证据，聚合为 {{ relationGroups.length }} 条服务关系。
      </template>
      <template v-else>点击“重新扫描调用关系”开始分析；编辑仓库字段不会自动触发扫描。</template>
    </p>

    <div v-if="snapshot" class="topology-workbench">
      <section class="topology-graph" aria-labelledby="topology-graph-title">
        <div class="section-heading">
          <div>
            <h4 id="topology-graph-title">服务关系</h4>
            <p>待确认候选优先排列，其他高置信度关系仍可检查或拒绝。</p>
          </div>
          <span class="queue-count">{{ relationGroups.filter(group => group.status === 'candidate').length }} 条关系待确认</span>
        </div>

        <div v-if="relationGroups.length" class="edge-list">
          <button
            v-for="group in relationGroups"
            :key="group.key"
            class="edge-button"
            :class="{ 'edge-button--selected': group.key === selectedRelationKey }"
            type="button"
            :data-relation="group.key"
            :data-from-service="group.fromService"
            :data-to-service="group.toService"
            :aria-pressed="group.key === selectedRelationKey"
            :aria-label="`检查 ${group.fromService} 到 ${group.toService} 的调用证据`"
            @click="selectRelation(group)"
          >
            <span class="edge-route">
              <strong>{{ group.fromService }}</strong>
              <span aria-hidden="true" class="edge-arrow">→</span>
              <strong>{{ group.toService }}</strong>
            </span>
            <span class="edge-meta">
              <span
                data-status
                class="status-badge"
                :class="statusFor(group.status).className"
              >{{ statusFor(group.status).label }}</span>
              <span>{{ group.edges.length }} 条端点证据</span>
              <span>最高置信度 {{ Math.round(group.confidence * 100) }}%</span>
            </span>
          </button>
        </div>
        <div v-else class="empty-state topology-empty" data-topology-empty>
          <strong v-if="endpoints.length">已找到端点，但没有形成跨仓调用关系。</strong>
          <strong v-else>没有识别到可用于匹配的代码端点。</strong>
          <p v-if="endpoints.length">
            当前有 {{ outboundEndpointCount }} 个调用出口、{{ inboundEndpointCount }} 个服务入口；
            方法、路径或服务别名未能唯一对应。同一仓库内的两个服务不会计为“跨仓”关系。
          </p>
          <p v-else>请先确认至少配置了两个不同仓库路径，并且仓库扫描状态正常。</p>
          <ul v-if="repositories.length" class="repository-scan-list" aria-label="仓库端点扫描结果">
            <li v-for="repo in repositories" :key="repo.repo">
              <code>{{ repo.repo }}</code>
              <span v-if="repo.state === 'scanned'">已识别 {{ repo.endpoint_count }} 个端点</span>
              <span v-else>{{ repo.state }}<template v-if="repo.error">：{{ repo.error }}</template></span>
            </li>
          </ul>
        </div>
      </section>

      <aside class="evidence-panel" aria-labelledby="topology-evidence-title">
        <template v-if="selectedRelation">
          <div class="section-heading">
            <div>
              <h4 id="topology-evidence-title">关系证据</h4>
              <p>{{ selectedRelation.fromService }} → {{ selectedRelation.toService }} · {{ selectedRelation.edges.length }} 条端点证据</p>
            </div>
            <span
              data-status
              class="status-badge"
              :class="statusFor(selectedRelation.status).className"
            >{{ statusFor(selectedRelation.status).label }}</span>
          </div>

          <div class="decision-actions">
            <button
              class="topology-button topology-button--primary"
              type="button"
              data-action="confirm"
              data-mutation
              :disabled="locked || !canConfirmSelectedRelation"
              @click="emitDecision('confirm')"
            >{{ selectedRelation.status === 'confirmed' ? '已确认' : '确认关系' }}</button>
            <button
              class="topology-button topology-button--danger"
              type="button"
              data-action="reject"
              data-mutation
              :disabled="locked || !canRejectSelectedRelation"
              @click="emitDecision('reject')"
            >{{ selectedRelation.status === 'rejected' ? '已拒绝' : '拒绝关系' }}</button>
          </div>
          <p class="decision-feedback" data-decision-feedback aria-live="polite">{{ decisionFeedback }}</p>

          <div class="retarget-row">
            <label for="topology-retarget">改为目标服务</label>
            <div class="inline-controls">
              <select
                id="topology-retarget"
                v-model="retargetService"
                data-retarget-service
                @change="retargetTouched = true"
              >
                <option value="">请选择服务</option>
                <option v-for="service in services" :key="service" :value="service">{{ service }}</option>
              </select>
              <button
                class="topology-button"
                type="button"
                data-action="retarget"
                data-mutation
                :disabled="locked || !retargetValid"
                @click="emitRetarget"
              >重定目标</button>
            </div>
          </div>

          <div class="relation-evidence-list" aria-label="端点证据列表">
            <article v-for="edge in selectedRelation.edges" :key="edgeKey(edge)" class="relation-evidence-card">
              <header>
                <strong>{{ edge.method || edge.rpc_method || edge.protocol || '服务级关系' }} {{ edge.path || '' }}</strong>
                <span>{{ Math.round(edge.confidence * 100) }}%</span>
              </header>
              <dl class="evidence-list">
                <div v-for="endpoint in endpointsForEdge(edge)" :key="endpoint.id" class="evidence-row">
                  <dt>{{ endpoint.direction === 'outbound' ? '调用端' : '接收端' }}</dt>
                  <dd>
                    <strong>{{ endpoint.service }}</strong>
                    <code>{{ endpoint.location || '无源码位置' }}</code>
                  </dd>
                </div>
                <div v-if="edge.reasons?.length || edge.conflicts?.length" class="evidence-row">
                  <dt>匹配理由</dt>
                  <dd class="reason-list">
                    <code v-for="reason in (edge.reasons ?? [])" :key="reason">{{ reason }}</code>
                    <code v-for="conflict in (edge.conflicts ?? [])" :key="conflict" class="reason-conflict">{{ conflict }}</code>
                  </dd>
                </div>
              </dl>
            </article>
          </div>
        </template>
        <p v-else id="topology-evidence-title" class="empty-state">选择一条关系查看代码位置与匹配理由。</p>
      </aside>
    </div>

    <form class="manual-edge" @submit.prevent="emitAdd">
      <div class="section-heading">
        <div>
          <h4>人工新增关系</h4>
          <p>只补充服务之间的方向关系；协议、方法和路径属于端点证据，无需人工填写。</p>
        </div>
      </div>
      <div class="manual-grid">
        <label for="topology-add-from">
          来源服务
          <select id="topology-add-from" v-model="addFrom" data-add-from @change="addTouched = true">
            <option value="">请选择服务</option>
            <option v-for="service in services" :key="service" :value="service">{{ service }}</option>
          </select>
        </label>
        <label for="topology-add-to">
          目标服务
          <select id="topology-add-to" v-model="addTo" data-add-to @change="addTouched = true">
            <option value="">请选择服务</option>
            <option v-for="service in services" :key="service" :value="service">{{ service }}</option>
          </select>
        </label>
        <div class="manual-action">
          <span class="manual-control-label">操作</span>
          <button
            class="topology-button"
            type="button"
            data-action="add"
            data-mutation
            :disabled="locked || !manualServicesValid"
            @click="emitAdd"
          >新增关系</button>
        </div>
      </div>
      <p class="topology-validation" data-validation role="alert" aria-live="polite">{{ validationMessage }}</p>
    </form>
  </section>
</template>

<style scoped>
.topology-panel {
  margin-top: 18px;
  padding: 20px;
  border: 1px solid var(--c-line);
  border-radius: var(--r-lg);
  background: var(--c-surf-2);
  color: var(--c-text);
  min-width: 0;
}

.topology-header,
.section-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}

.topology-kicker {
  margin: 0 0 4px;
  color: #1d4ed8;
  font-size: 12px;
  font-weight: 700;
  letter-spacing: .05em;
  text-transform: uppercase;
}

.topology-panel h3,
.topology-panel h4,
.topology-panel p {
  margin-top: 0;
}

.topology-panel h3 {
  margin-bottom: 6px;
  color: var(--c-ink);
  font-size: 18px;
}

.topology-panel h4 {
  margin-bottom: 4px;
  color: var(--c-ink);
  font-size: 15px;
}

.topology-description,
.section-heading p,
.topology-feedback {
  margin-bottom: 0;
  color: var(--c-muted);
  font-size: 13px;
  line-height: 1.55;
}

.topology-feedback {
  min-height: 21px;
  margin-top: 12px;
}

.topology-error {
  margin: 12px 0 0;
  padding: 10px 12px;
  border: 1px solid var(--c-danger-border);
  border-radius: var(--r-md);
  background: var(--c-danger-bg);
  color: var(--c-danger);
  line-height: 1.5;
}

.topology-button {
  min-height: 44px;
  padding: 9px 14px;
  border: 1px solid var(--c-line-2);
  border-radius: var(--r-md);
  background: var(--c-surf);
  color: var(--c-text);
  cursor: pointer;
  font: inherit;
  font-weight: 600;
  -webkit-user-select: none;
  user-select: none;
  transition: background-color 180ms ease, border-color 180ms ease, color 180ms ease, opacity 180ms ease;
}

.topology-button:hover:not(:disabled) { background: var(--c-surf-3); }
.topology-button:disabled { cursor: not-allowed; opacity: .5; }
.topology-button--primary { border-color: #1d4ed8; background: #1d4ed8; color: #fff; }
.topology-button--primary:hover:not(:disabled) { border-color: #1e40af; background: #1e40af; }
.topology-button--danger { border-color: #fecaca; color: #991b1b; }
.topology-button--danger:hover:not(:disabled) { background: #fef2f2; }

.topology-button:focus-visible,
.edge-button:focus-visible,
.topology-panel input:focus-visible,
.topology-panel select:focus-visible {
  outline: 3px solid rgba(37, 99, 235, .42);
  outline-offset: 2px;
}

.topology-workbench {
  display: grid;
  grid-template-columns: minmax(320px, .75fr) minmax(480px, 1.25fr);
  align-items: start;
  gap: 16px;
  margin-top: 16px;
}

.topology-graph,
.evidence-panel,
.manual-edge {
  min-width: 0;
  padding: 16px;
  border: 1px solid var(--c-line);
  border-radius: var(--r-md);
  background: var(--c-surf);
}

.topology-graph,
.evidence-panel {
  align-self: start;
}

.queue-count {
  flex: 0 0 auto;
  padding: 4px 8px;
  border-radius: 999px;
  background: #fff7ed;
  color: #9a3412;
  font-size: 12px;
  font-weight: 700;
}

.edge-list {
  display: grid;
  gap: 8px;
  max-height: min(560px, 60vh);
  margin-top: 14px;
  padding-right: 4px;
  overflow-y: auto;
  scrollbar-gutter: stable;
}

.topology-empty {
  display: grid;
  gap: 8px;
  margin-top: 14px;
}

.topology-empty p { margin-bottom: 0; }

.repository-scan-list {
  display: grid;
  gap: 6px;
  margin: 4px 0 0;
  padding: 0;
  list-style: none;
}

.repository-scan-list li {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  padding: 7px 9px;
  border-radius: var(--r-sm);
  background: var(--c-surf-2);
  color: var(--c-muted);
  font-size: 12px;
}

.edge-button {
  min-height: 58px;
  width: 100%;
  padding: 10px 12px;
  border: 1px solid var(--c-line);
  border-radius: var(--r-md);
  background: var(--c-surf);
  color: var(--c-text);
  cursor: pointer;
  font: inherit;
  text-align: left;
  -webkit-user-select: none;
  user-select: none;
  transition: background-color 180ms ease, border-color 180ms ease;
}

.edge-button:hover { background: var(--c-surf-2); }
.edge-button--selected { border-color: #2563eb; background: #eff6ff; }
.edge-route { display: flex; align-items: center; gap: 8px; min-width: 0; }
.edge-route strong { overflow-wrap: anywhere; }
.edge-arrow { color: var(--c-muted); }
.edge-meta { display: flex; align-items: center; flex-wrap: wrap; gap: 8px; margin-top: 7px; color: var(--c-muted); font-size: 12px; }

.status-badge {
  display: inline-flex;
  align-items: center;
  min-height: 24px;
  padding: 2px 8px;
  border: 1px solid transparent;
  border-radius: 999px;
  font-size: 12px;
  font-weight: 700;
}

.status--automatic,
.status--confirmed,
.status--manual { border-color: #bbf7d0; background: #f0fdf4; color: #166534; }
.status--candidate { border-color: #fed7aa; background: #fff7ed; color: #9a3412; }
.status--rejected { border-color: #fecaca; background: #fef2f2; color: #991b1b; }
.status--stale,
.status--unknown { border-color: #cbd5e1; background: #f1f5f9; color: #475569; }

.evidence-list { margin: 14px 0 0; }
.relation-evidence-list {
  display: grid;
  gap: 10px;
  max-height: min(560px, 60vh);
  margin-top: 16px;
  padding-right: 4px;
  overflow-y: auto;
  scrollbar-gutter: stable;
}
.relation-evidence-card {
  min-width: 0;
  padding: 12px;
  border: 1px solid var(--c-line);
  border-radius: var(--r-md);
  background: var(--c-surf-2);
}
.relation-evidence-card > header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  min-height: 24px;
  color: var(--c-muted);
  font-size: 12px;
  line-height: 1.45;
}
.relation-evidence-card > header strong { min-width: 0; color: var(--c-text); overflow-wrap: anywhere; }
.relation-evidence-card > header span { flex: 0 0 auto; }
.relation-evidence-card .evidence-list { margin-top: 6px; }
.evidence-row { display: grid; grid-template-columns: 72px minmax(0, 1fr); align-items: start; gap: 10px; padding: 10px 0; border-top: 1px solid var(--c-line); }
.evidence-row dt { color: var(--c-muted); font-size: 12px; font-weight: 600; }
.evidence-row dd { min-width: 0; margin: 0; }
.evidence-row dd strong,
.evidence-row dd code { display: block; overflow-wrap: anywhere; }
.evidence-row dd code { margin-top: 4px; color: #334155; font-size: 12px; }
.reason-list { display: flex; flex-wrap: wrap; gap: 6px; }
.reason-list code { margin-top: 0 !important; padding: 3px 6px; border-radius: 4px; background: var(--c-surf-3); }
.reason-list .reason-conflict { background: #fef2f2; color: #991b1b; }

.decision-actions,
.inline-controls {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.decision-actions { margin-top: 14px; }
.decision-feedback { margin: 8px 0 0; color: var(--c-muted); font-size: 12px; line-height: 1.5; }
.retarget-row { margin-top: 14px; }
.retarget-row label,
.manual-grid label { display: grid; gap: 6px; color: #475569; font-size: 13px; font-weight: 600; }
.inline-controls { margin-top: 6px; }
.inline-controls select { flex: 1 1 160px; min-height: 44px; }

.manual-edge { margin-top: 16px; }
.manual-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)) 144px; align-items: start; gap: 12px; margin-top: 12px; }
.manual-grid input,
.manual-grid select,
.manual-grid .topology-button { height: 44px; min-height: 44px; }
.manual-action { display: grid; min-width: 0; gap: 6px; }
.manual-control-label { color: #475569; font-size: 13px; font-weight: 600; }
.manual-grid .topology-button { width: 100%; padding-block: 0; }
.topology-validation { min-height: 21px; margin: 10px 0 0; color: var(--c-danger); font-size: 13px; line-height: 1.5; }
.empty-state { margin: 16px 0 0; color: var(--c-muted); line-height: 1.5; }

@media (max-width: 767px) {
  .topology-panel { padding: 16px; font-size: 16px; }
  .topology-header,
  .section-heading { flex-direction: column; }
  .topology-header .topology-button { width: 100%; }
  .topology-workbench { grid-template-columns: minmax(0, 1fr); }
  .edge-list,
  .relation-evidence-list { max-height: min(480px, 60vh); }
  .manual-grid { grid-template-columns: minmax(0, 1fr); }
  .manual-route-field { grid-column: auto; }
  .manual-grid label,
  .manual-control-label,
  .retarget-row label,
  .topology-description,
  .section-heading p,
  .topology-feedback,
  .topology-kicker,
  .queue-count,
  .edge-meta,
  .status-badge,
  .evidence-row dt,
  .evidence-row dd code { font-size: 16px; }
  .topology-panel h4 { font-size: 17px; }
  .manual-grid .topology-button { width: 100%; }
}

@media (min-width: 768px) and (max-width: 1279px) {
  .topology-workbench { grid-template-columns: minmax(0, 1fr); }
  .manual-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
  .manual-action { grid-column: 1 / -1; }
}

@media (prefers-reduced-motion: reduce) {
  .topology-button,
  .edge-button { transition: none; }
}
</style>
