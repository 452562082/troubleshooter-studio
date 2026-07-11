<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { topology } from '../../wailsjs/go/models'
import type { ServiceTopologyOverrideState } from '../lib/yamlGenerator'
import {
  overrideForEdge,
  retargetTopologyOverride,
  upsertTopologyOverride,
} from '../lib/useServiceTopology'

const props = withDefaults(defineProps<{
  snapshot: topology.Snapshot | null
  overrides: ServiceTopologyOverrideState[]
  loading: boolean
  disabled?: boolean
  error?: string
}>(), {
  disabled: false,
  error: '',
})

const emit = defineEmits<{
  'update:overrides': [value: ServiceTopologyOverrideState[]]
  refresh: []
}>()

const selectedKey = ref('')
const retargetService = ref('')
const addFrom = ref('')
const addTo = ref('')
const addProtocol = ref<'http' | 'grpc'>('http')
const addMethod = ref('GET')
const addPath = ref('')
const addRPCMethod = ref('')

const locked = computed(() => props.loading || props.disabled)
const edges = computed(() => {
  const source = props.snapshot?.edges ?? []
  return [...source].sort((left, right) => {
    const priority = (status: string) => status === 'candidate' ? 0 : status === 'stale' ? 1 : 2
    return priority(left.status) - priority(right.status)
  })
})
const services = computed(() => new Set([
  ...(props.snapshot?.services ?? []).map(service => service.service),
  ...(props.snapshot?.endpoints ?? []).map(endpoint => endpoint.service),
  ...edges.value.flatMap(edge => [edge.from_service, edge.to_service]),
].filter(Boolean)))
const manualRouteValid = computed(() => addProtocol.value === 'http'
  ? !!addMethod.value.trim() && addPath.value.trim().startsWith('/')
  : !!addRPCMethod.value.trim())

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

function edgeID(edge: topology.CandidateEdge): string {
  const endpointID = edge.from_endpoint?.replace(/:out$/, '')
  return endpointID || `${edge.from_service}-${edge.to_service}`
}

const selectedEdge = computed(() => (
  edges.value.find(edge => edgeKey(edge) === selectedKey.value) ?? null
))

watch(edges, (nextEdges) => {
  if (nextEdges.some(edge => edgeKey(edge) === selectedKey.value)) return
  const preferred = nextEdges.find(edge => edge.status === 'candidate') ?? nextEdges[0]
  selectedKey.value = preferred ? edgeKey(preferred) : ''
}, { immediate: true })

watch(selectedEdge, (edge) => {
  retargetService.value = edge?.to_service ?? ''
})

const selectedEndpoints = computed(() => {
  const edge = selectedEdge.value
  if (!edge) return []
  const ids = new Set([edge.from_endpoint, edge.to_endpoint].filter(Boolean))
  return (props.snapshot?.endpoints ?? []).filter(endpoint => ids.has(endpoint.id))
})

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

function selectEdge(edge: topology.CandidateEdge) {
  selectedKey.value = edgeKey(edge)
}

function emitDecision(action: 'confirm' | 'reject') {
  const edge = selectedEdge.value
  if (!edge || locked.value) return
  emit('update:overrides', upsertTopologyOverride(props.overrides, overrideForEdge(action, edge)))
}

function emitRetarget() {
  const edge = selectedEdge.value
  const target = retargetService.value.trim()
  if (!edge || !target || target === edge.to_service.trim() || locked.value) return
  emit('update:overrides', retargetTopologyOverride(props.overrides, edge, target))
}

function emitAdd() {
  const fromService = addFrom.value.trim()
  const toService = addTo.value.trim()
  if (!fromService || !toService || locked.value) return
  const decision: ServiceTopologyOverrideState = {
    action: 'add',
    fromService,
    toService,
    protocol: addProtocol.value,
    ...(addProtocol.value === 'http'
      ? { method: addMethod.value.trim().toUpperCase(), path: addPath.value.trim() }
      : { rpcMethod: addRPCMethod.value.trim() }),
  }
  emit('update:overrides', upsertTopologyOverride(props.overrides, decision))
  addFrom.value = ''
  addTo.value = ''
  addPath.value = ''
  addRPCMethod.value = ''
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
      >{{ loading ? '分析中…' : '刷新拓扑' }}</button>
    </header>

    <p v-if="error" class="topology-error" role="alert">{{ error }}</p>
    <p class="topology-feedback" data-feedback aria-live="polite">
      <template v-if="loading">正在分析仓库端点和跨服务调用关系，请稍候。</template>
      <template v-else-if="disabled">其他任务正在运行，拓扑操作暂时不可用。</template>
      <template v-else-if="snapshot">已载入 {{ services.size }} 个服务、{{ edges.length }} 条证据边。</template>
      <template v-else>点击“刷新拓扑”开始分析；编辑仓库字段不会自动触发扫描。</template>
    </p>

    <div v-if="snapshot" class="topology-workbench">
      <section class="topology-graph" aria-labelledby="topology-graph-title">
        <div class="section-heading">
          <div>
            <h4 id="topology-graph-title">服务关系</h4>
            <p>待确认候选优先排列，其他高置信度关系仍可检查或拒绝。</p>
          </div>
          <span class="queue-count">{{ edges.filter(edge => edge.status === 'candidate').length }} 条待确认</span>
        </div>

        <div v-if="edges.length" class="edge-list">
          <button
            v-for="edge in edges"
            :key="edgeKey(edge)"
            class="edge-button"
            :class="{ 'edge-button--selected': edgeKey(edge) === selectedKey }"
            type="button"
            :data-edge="edgeID(edge)"
            :aria-pressed="edgeKey(edge) === selectedKey"
            :aria-label="`检查 ${edge.from_service} 到 ${edge.to_service} 的调用证据`"
            @click="selectEdge(edge)"
          >
            <span class="edge-route">
              <strong>{{ edge.from_service }}</strong>
              <span aria-hidden="true" class="edge-arrow">→</span>
              <strong>{{ edge.to_service }}</strong>
            </span>
            <span class="edge-meta">
              <span
                data-status
                class="status-badge"
                :class="statusFor(edge.status).className"
              >{{ statusFor(edge.status).label }}</span>
              <span>{{ Math.round(edge.confidence * 100) }}%</span>
              <span>{{ edge.protocol.toUpperCase() }}</span>
            </span>
          </button>
        </div>
        <p v-else class="empty-state">当前扫描没有发现跨服务调用边。</p>
      </section>

      <aside class="evidence-panel" aria-labelledby="topology-evidence-title">
        <template v-if="selectedEdge">
          <div class="section-heading">
            <div>
              <h4 id="topology-evidence-title">端点证据</h4>
              <p>{{ selectedEdge.method || selectedEdge.rpc_method || selectedEdge.protocol }} {{ selectedEdge.path || '' }}</p>
            </div>
            <span
              data-status
              class="status-badge"
              :class="statusFor(selectedEdge.status).className"
            >{{ statusFor(selectedEdge.status).label }}</span>
          </div>

          <dl class="evidence-list">
            <div v-for="endpoint in selectedEndpoints" :key="endpoint.id" class="evidence-row">
              <dt>{{ endpoint.direction === 'outbound' ? '调用端' : '接收端' }}</dt>
              <dd>
                <strong>{{ endpoint.service }}</strong>
                <code>{{ endpoint.location || '无源码位置' }}</code>
              </dd>
            </div>
            <div class="evidence-row">
              <dt>匹配理由</dt>
              <dd class="reason-list">
                <code v-for="reason in selectedEdge.reasons" :key="reason">{{ reason }}</code>
                <code v-for="conflict in selectedEdge.conflicts" :key="conflict" class="reason-conflict">{{ conflict }}</code>
              </dd>
            </div>
          </dl>

          <div class="decision-actions">
            <button
              class="topology-button topology-button--primary"
              type="button"
              data-action="confirm"
              data-mutation
              :disabled="locked"
              @click="emitDecision('confirm')"
            >确认关系</button>
            <button
              class="topology-button topology-button--danger"
              type="button"
              data-action="reject"
              data-mutation
              :disabled="locked"
              @click="emitDecision('reject')"
            >拒绝关系</button>
          </div>

          <div class="retarget-row">
            <label for="topology-retarget">改为目标服务</label>
            <div class="inline-controls">
              <input id="topology-retarget" v-model="retargetService" data-retarget-service type="text" list="topology-service-options">
              <button
                class="topology-button"
                type="button"
                data-action="retarget"
                data-mutation
                :disabled="locked || !retargetService.trim() || retargetService.trim() === selectedEdge.to_service"
                @click="emitRetarget"
              >重定目标</button>
            </div>
          </div>
        </template>
        <p v-else id="topology-evidence-title" class="empty-state">选择一条关系查看代码位置与匹配理由。</p>
      </aside>
    </div>

    <form class="manual-edge" @submit.prevent="emitAdd">
      <div class="section-heading">
        <div>
          <h4>人工新增关系</h4>
          <p>用于补充静态扫描无法识别的动态调用。</p>
        </div>
      </div>
      <div class="manual-grid">
        <label>
          来源服务
          <input v-model="addFrom" data-add-from type="text" list="topology-service-options" autocomplete="off">
        </label>
        <label>
          目标服务
          <input v-model="addTo" data-add-to type="text" list="topology-service-options" autocomplete="off">
        </label>
        <label>
          协议
          <select v-model="addProtocol" data-add-protocol>
            <option value="http">HTTP</option>
            <option value="grpc">gRPC</option>
          </select>
        </label>
        <label v-if="addProtocol === 'http'">
          HTTP 方法
          <input v-model="addMethod" data-add-method type="text" autocomplete="off">
        </label>
        <label v-if="addProtocol === 'http'" class="manual-route-field">
          HTTP 路径
          <input v-model="addPath" data-add-path type="text" placeholder="/internal/orders" autocomplete="off">
        </label>
        <label v-else class="manual-route-field">
          gRPC 方法
          <input v-model="addRPCMethod" data-add-rpc-method type="text" placeholder="package.Service/Method" autocomplete="off">
        </label>
        <button
          class="topology-button"
          type="button"
          data-action="add"
          data-mutation
          :disabled="locked || !addFrom.trim() || !addTo.trim() || !manualRouteValid"
          @click="emitAdd"
        >新增关系</button>
      </div>
      <datalist id="topology-service-options">
        <option v-for="service in services" :key="service" :value="service" />
      </datalist>
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
  grid-template-columns: minmax(0, 1fr) minmax(0, 1fr);
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
  margin-top: 14px;
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
.evidence-row { display: grid; grid-template-columns: 76px minmax(0, 1fr); gap: 8px; padding: 10px 0; border-top: 1px solid var(--c-line); }
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
.retarget-row { margin-top: 14px; }
.retarget-row label,
.manual-grid label { display: grid; gap: 6px; color: #475569; font-size: 13px; font-weight: 600; }
.inline-controls { margin-top: 6px; }
.inline-controls input { flex: 1 1 160px; min-height: 44px; }

.manual-edge { margin-top: 16px; }
.manual-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)) 100px 100px; align-items: end; gap: 8px; margin-top: 12px; }
.manual-route-field { grid-column: span 2; }
.manual-grid input,
.manual-grid select { min-height: 44px; }
.empty-state { margin: 16px 0 0; color: var(--c-muted); line-height: 1.5; }

@media (max-width: 767px) {
  .topology-panel { padding: 16px; font-size: 16px; }
  .topology-header,
  .section-heading { flex-direction: column; }
  .topology-header .topology-button { width: 100%; }
  .topology-workbench { grid-template-columns: minmax(0, 1fr); }
  .manual-grid { grid-template-columns: minmax(0, 1fr); }
  .manual-route-field { grid-column: auto; }
  .manual-grid label,
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

@media (min-width: 768px) and (max-width: 1023px) {
  .topology-workbench { grid-template-columns: minmax(0, 1fr); }
  .manual-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}

@media (prefers-reduced-motion: reduce) {
  .topology-button,
  .edge-button { transition: none; }
}
</style>
