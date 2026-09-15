<script setup lang="ts">
// ObservabilityStep —— Step 8 可观测性整段(组件 chip 选择 + 每 env 工具卡 + k8s_runtime / loki tail)。
// 从 InitPage 抽出来,InitPage 调用变 <ObservabilityStep ... /> 一行。
//
// props 形态对齐 InitPage 现有 reactive object / closure helper(同 ConfigSourceStep 的迁移取舍),
// 不重新设计签名以最小化迁移风险。

import { inject } from 'vue'
import { setLokiDatasource } from '../lib/useLokiMappingState'
import type { CredField } from '../lib/credFields'
import type { One2AllResourceState } from '../lib/wizardStore'
import type { K8sRuntimeEnvLocator, K8sRuntimeSvcLocator } from '../lib/yamlGenerator'
import type { URLProbeState } from '../lib/probeTypes'
import type { GrafanaDatasource } from '../lib/bridge'
import { WizardStoreKey } from '../lib/wizardStore'
import ObservabilityToolBlock from './ObservabilityToolBlock.vue'
import K8sRuntimeBlock from './K8sRuntimeBlock.vue'
import LokiMappingStep from './LokiMappingStep.vue'

interface ToolSpec { key: string; label: string; description: string; fields: CredField[] }
interface WorkloadCacheEntry { status?: 'idle' | 'loading' | 'ok' | 'error' }
interface LokiMappingPerEnv {
  dsList: GrafanaDatasource[]
  dsUID: string
  dsListStatus: 'idle' | 'loading' | 'ok' | 'fail'
  dsListError?: string
  labels: string[]
  labelStatus: 'idle' | 'loading' | 'ok' | 'fail'
  labelError?: string
  envLabelKey: string
  serviceLabelKey: string
  envValue: string
  serviceValues: Record<string, string>
  envLabelValues: string[]
  serviceLabelValues: string[]
  serviceMatchTried?: Record<string, boolean>
}
type ObsAccessMode = 'via_grafana' | 'direct'

// 通用 reactive + helper 走 inject(避免每个 prop 单独透传)
const wizard = inject(WizardStoreKey)!

const props = defineProps<{
  // 组件 chip 启用状态
  obsToolSpecs: ToolSpec[]
  enabledObservability: Record<string, boolean>

  // ObservabilityToolBlock 用
  obsProbeResults: Record<string, URLProbeState>
  toolInputs: Record<string, string>
  isObsFieldHidden: (toolKey: string, envID: string, f: CredField) => boolean
  displayObsField: (toolKey: string, envID: string, f: CredField) => CredField
  toolKeyFor: (cat: 'obs' | 'ds', tool: string, envID: string, field: string) => string
  obsProbeKey: (toolKey: string, envID: string) => string
  getObsAccessMode: (toolKey: string, envID: string) => ObsAccessMode

  // K8sRuntimeBlock 用
  k8sRuntimeEnvLoc: Record<string, K8sRuntimeEnvLocator | undefined>
  k8sRuntimeSvcMap: Record<string, K8sRuntimeSvcLocator>
  k8sRtWorkloadCache: Record<string, WorkloadCacheEntry | undefined>
  one2allStateByEnv: Record<string, One2AllResourceState | undefined>
  k8sRtWorkloadKey: (envID: string, cluster: string, ns: string) => string
  k8sRtWorkloadsFor: (envID: string, cluster: string, ns: string) => Array<{ name: string; selector: string }>

  // via_grafana datasource 选择
  getLokiMapping: (envID: string) => LokiMappingPerEnv
  obsGrafanaDsCandidates: (envID: string, obsKey: string) => GrafanaDatasource[]
  grafanaDsUidByObsEnv: Record<string, string>
  obsGrafanaDsKey: (obsKey: string, envID: string) => string
  obsGrafanaDsTypes: Record<string, string[]>
  k8sConnectionReuseLabel: (envID: string) => string
}>()

const emit = defineEmits<{
  setObsAccessMode: [toolKey: string, envID: string, mode: ObsAccessMode]
  updateToolInput: [key: string, value: string, toolKey: string, envID: string]
  clearToolInput: [key: string]
  runK8sRtPreload: [envID: string]
  setK8sRtEnvLoc: [envID: string, field: 'cluster' | 'cluster_id' | 'namespace', value: string]
  setK8sRtSvcWorkload: [envID: string, svc: string, workload: string]
  loadLokiDatasources: [envID: string]
  loadLokiLabels: [envID: string]
  envLabelKeyChanged: [envID: string, key: string]
  serviceLabelKeyChanged: [envID: string, key: string]
  envValueChanged: [envID: string]
}>()

// 下拉 v-model 行为:Vue 模板里直接 mutate 父端 reactive(getLokiMapping 返回 ref,
// grafanaDsUidByObsEnv 是 Record),Vue 自动追踪
function setLokiDsUid(envID: string, value: string) {
  setLokiDatasource(props.getLokiMapping(envID), value)
}
function setGrafanaDsUid(obsKey: string, envID: string, value: string) {
  props.grafanaDsUidByObsEnv[props.obsGrafanaDsKey(obsKey, envID)] = value
}

function connectionProbe(tool: string, envID: string): URLProbeState | undefined {
  if (tool !== 'grafana') return props.obsProbeResults[props.obsProbeKey(tool, envID)]
  const status = props.getLokiMapping(envID).dsListStatus
  return { status: status || 'idle', error: status === 'fail' ? '账号或数据源检查失败' : undefined, latency: status === 'ok' ? '账号与数据源可用' : undefined }
}
const grafanaTools = ['loki', 'prometheus', 'tempo']
function onToolToggle(tool: string, checked: boolean) {
  props.enabledObservability[tool] = checked
  if (tool === 'grafana' && !checked) {
    for (const key of grafanaTools) props.enabledObservability[key] = false
  }
  if (checked && ['loki', 'prometheus', 'tempo'].includes(tool)) {
    props.enabledObservability.grafana = true
  }
}
</script>

<template>
  <div class="card lg">
    <h2>日志与服务状态</h2>
    <p class="help-text">先连接平台，再选择需要查询的内容。Grafana 可发现日志、指标和调用链数据源。</p>
    <div class="obs-tool-chips">
      <label v-for="spec in obsToolSpecs.filter(s => ['grafana', 'k8s_runtime'].includes(s.key))" :key="spec.key" class="obs-tool-chip" :class="{ active: enabledObservability[spec.key] }">
        <input type="checkbox" :checked="enabledObservability[spec.key]" @change="onToolToggle(spec.key, ($event.target as HTMLInputElement).checked)">{{ spec.key === 'k8s_runtime' ? '运行平台 · Kuboard / one2all' : 'Grafana · 日志、指标与调用链' }}
      </label>
    </div>
    <details :open="['jaeger','elk','skywalking'].some(k => enabledObservability[k])" class="wizard-advanced">
      <summary>其他独立平台</summary>
      <div class="obs-tool-chips"><label v-for="spec in obsToolSpecs.filter(s => ['jaeger','elk','skywalking'].includes(s.key))" :key="spec.key" class="obs-tool-chip" :class="{ active: enabledObservability[spec.key] }"><input type="checkbox" :checked="enabledObservability[spec.key]" @change="onToolToggle(spec.key, ($event.target as HTMLInputElement).checked)">{{ spec.label }}</label></div>
    </details>
    <div v-if="grafanaTools.some(k => enabledObservability[k]) && !enabledObservability.grafana" class="obs-grafana-required-banner" role="alert">已有日志或指标连接需要 Grafana。<button class="btn" @click="onToolToggle('grafana', true)">连接 Grafana</button><button class="btn link" @click="onToolToggle('grafana', false)">暂不接入这些能力</button></div>

    <!-- 主内容:按 env → 启用的工具 → 字段 层级 -->
    <div class="ds-hierarchy" style="margin-top:14px">
      <div v-for="env in wizard.environments" :key="env.id" class="ds-env-section">
        <div class="ds-env-title">
          <span class="cc-env-label">{{ env.id || '(未命名 env)' }}</span>
          <span v-if="env.is_prod" class="cc-env-prod-tag">prod</span>
          <span class="ds-env-count">
            {{ obsToolSpecs.filter(s => enabledObservability[s.key]).length }} 个组件已启用
          </span>
        </div>

        <div
          v-if="obsToolSpecs.filter(s => enabledObservability[s.key]).length === 0"
          class="ds-empty"
        >⧗ 还没启用任何可观测性组件 — 在上方勾选要用的</div>

        <div v-else class="ds-svc-container">
          <!-- Loki/Prometheus/Tempo 启用但 Grafana 未启用 → 整块不渲染(数据源选择器 / 加载标签按钮
               都依赖 grafana,渲染出来只会给用户看到能加载 stale 缓存的假象)。banner + chip 黄色
               已是用户感知信号,这里再硬过滤一遍防止旧 dsList 被刷出来 / 用户误点加载按钮。 -->
          <ObservabilityToolBlock
            v-for="spec in obsToolSpecs.filter(s => enabledObservability[s.key]
              && !(['loki','prometheus','tempo'].includes(s.key) && !enabledObservability['grafana']))"
            :key="spec.key"
            :env-i-d="env.id"
            :spec="spec"
            :access-mode="getObsAccessMode(spec.key, env.id)"
            :access-toggleable="false"
            :connection-reuse-label="spec.key === 'k8s_runtime' ? k8sConnectionReuseLabel(env.id) : ''"
            :probe-state="connectionProbe(spec.key, env.id)"
            :tool-inputs="toolInputs"
            :is-revealed="wizard.isRevealed"
            :is-obs-field-hidden="isObsFieldHidden"
            :display-obs-field="displayObsField"
            :tool-key-for="toolKeyFor"
            @update:access-mode="(mode) => emit('setObsAccessMode', spec.key, env.id, mode)"
            @update:tool-input="(k, v) => emit('updateToolInput', k, v, spec.key, env.id)"
            @toggle-reveal="(k) => wizard.toggleReveal(k)"
            @clear-input="(k) => emit('clearToolInput', k)"
          >
            <div v-if="spec.key === 'grafana'" class="grafana-discovery">
              <div class="discovery-heading"><strong>可接入的数据源</strong><button class="btn" :disabled="getLokiMapping(env.id).dsListStatus === 'loading'" @click="emit('loadLokiDatasources', env.id)">{{ getLokiMapping(env.id).dsListStatus === 'loading' ? '发现中…' : '刷新数据源' }}</button></div>
              <p v-if="getLokiMapping(env.id).dsListStatus === 'fail'" role="alert">未能获取数据源，请检查连接后重试。</p>
              <p v-else-if="getLokiMapping(env.id).dsListStatus !== 'ok'">填写连接信息后自动发现，选择需要接入的能力。</p>
              <template v-else>
                <label v-for="key in grafanaTools.filter(k => obsGrafanaDsCandidates(env.id, k).length || enabledObservability[k])" :key="key" class="discovered-source"><input type="checkbox" :checked="enabledObservability[key]" @change="onToolToggle(key, ($event.target as HTMLInputElement).checked)"><span>{{ key === 'loki' ? '查询日志 · Loki' : key === 'prometheus' ? '查询指标 · Prometheus' : '查看调用链 · Tempo' }}</span><small>{{ obsGrafanaDsCandidates(env.id, key).length }} 个数据源</small></label>
                <p v-if="!grafanaTools.some(k => obsGrafanaDsCandidates(env.id, k).length)">未发现可用的 Loki、Prometheus 或 Tempo 数据源，可刷新或接入其他平台。</p>
                <p>能力选择应用于项目；各环境使用各自的数据源。</p>
              </template>
            </div>
            <K8sRuntimeBlock
              v-if="spec.key === 'k8s_runtime'"
              :env-i-d="env.id"
              :provider="toolInputs[toolKeyFor('obs', 'k8s_runtime', env.id, 'provider')] || 'kuboard'"
              :services="wizard.runtimeWorkloadNames"
              :kuboard-state="wizard.kuboardStateByEnv[env.id]"
              :one2all-state="one2allStateByEnv[env.id]"
              :env-loc="k8sRuntimeEnvLoc[env.id]"
              :svc-map="k8sRuntimeSvcMap"
              :workload-cache="k8sRtWorkloadCache"
              :svc-key="wizard.svcKey"
              :workload-key="k8sRtWorkloadKey"
              :workloads-for="k8sRtWorkloadsFor"
              :namespaces-for="wizard.kuboardNamespacesFor"
              @preload="(envID) => emit('runK8sRtPreload', envID)"
              @set-env-loc="(envID, field, value) => emit('setK8sRtEnvLoc', envID, field, value)"
              @set-svc-workload="(envID, svc, workload) => emit('setK8sRtSvcWorkload', envID, svc, workload)"
            />

            <!-- via_grafana 模式(loki/prometheus/jaeger/tempo/elk 共用) -->
            <div
              v-if="['loki','prometheus','jaeger','tempo','elk'].includes(spec.key) && getObsAccessMode(spec.key, env.id) === 'via_grafana'"
              class="loki-env-mapping"
            >
              <div class="loki-env-mapping-head">
                {{ spec.label }} 数据源
              </div>
              <div class="cc-field-row" style="gap: 12px; align-items: center; flex-wrap: wrap;">
                <select
                  v-if="spec.key === 'loki'"
                  :value="getLokiMapping(env.id).dsUID || ''"
                  class="cc-input"
                  style="max-width: 420px;"
                  @change="(e: any) => setLokiDsUid(env.id, e.target.value)"
                >
                  <option value="">请选择日志数据源</option>
                  <option
                    v-for="ds in obsGrafanaDsCandidates(env.id, 'loki')"
                    :key="ds.uid" :value="ds.uid"
                  >{{ ds.name }}({{ ds.type }}{{ ds.default ? ', default' : '' }})</option>
                </select>
                <select
                  v-else
                  :value="grafanaDsUidByObsEnv[obsGrafanaDsKey(spec.key, env.id)] || ''"
                  class="cc-input"
                  style="max-width: 420px;"
                  @change="(e: any) => setGrafanaDsUid(spec.key, env.id, e.target.value)"
                >
                  <option value="">请选择数据源</option>
                  <option
                    v-for="ds in obsGrafanaDsCandidates(env.id, spec.key)"
                    :key="ds.uid" :value="ds.uid"
                  >{{ ds.name }}({{ ds.type }}{{ ds.default ? ', default' : '' }})</option>
                </select>
                <button
                  v-if="getLokiMapping(env.id).dsListStatus === 'loading'"
                  type="button" class="btn cc-preload-btn" disabled
                >
                  <span class="cc-preload-spinner" aria-hidden="true"></span>
                  加载中…
                </button>
                <button
                  v-else
                  type="button" class="btn cc-preload-btn"
                  @click="emit('loadLokiDatasources', env.id)"
                >🔄 {{ (getLokiMapping(env.id).dsList || []).length > 0 ? '刷新' : '加载' }}数据源</button>
                <span
                  v-if="getLokiMapping(env.id).dsListStatus === 'fail'"
                  class="cc-preload-error"
                  :title="getLokiMapping(env.id).dsListError"
                >✗ {{ getLokiMapping(env.id).dsListError?.slice(0, 50) }}</span>
                <span
                  v-else-if="(getLokiMapping(env.id).dsList || []).length > 0 && obsGrafanaDsCandidates(env.id, spec.key).length === 0"
                  class="cc-preload-summary"
                  style="background: #fee2e2; color: #991b1b;"
                >该 Grafana 里没找到 type={{ obsGrafanaDsTypes[spec.key]?.join('/') }} 的 数据源</span>
                <span
                  v-else-if="(getLokiMapping(env.id).dsList || []).length > 0"
                  class="cc-preload-summary"
                >✓ {{ obsGrafanaDsCandidates(env.id, spec.key).length }} 个 {{ obsGrafanaDsTypes[spec.key]?.join('/') }} 候选</span>
              </div>
            </div>

            <LokiMappingStep
              v-if="spec.key === 'loki'"
              :env-i-d="env.id"
              :mapping="getLokiMapping(env.id)"
              :services="wizard.runtimeWorkloadNames"
              @load-labels="(envID) => emit('loadLokiLabels', envID)"
              @env-label-key-changed="(envID, key) => emit('envLabelKeyChanged', envID, key)"
              @service-label-key-changed="(envID, key) => emit('serviceLabelKeyChanged', envID, key)"
              @env-value-changed="(envID) => emit('envValueChanged', envID)"
            />
          </ObservabilityToolBlock>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.grafana-discovery { margin:16px 0; padding:16px; background:#f8fafc; border:1px solid #e2e8f0; border-radius:10px; }
.discovery-heading,.discovered-source { display:flex; align-items:center; gap:12px; flex-wrap:wrap; }
.discovery-heading { justify-content:space-between; font-size:14px; }
.discovered-source { min-height:44px; font-size:13px; cursor:pointer; }
.discovered-source span { flex:1; } .discovered-source small,p { color:#64748b; font-size:12px; line-height:1.6; }
</style>
