<script setup lang="ts">
// ObservabilityStep —— Step 8 可观测性整段(组件 chip 选择 + 每 env 工具卡 + k8s_runtime / loki tail)。
// 从 InitPage 抽出来,InitPage 调用变 <ObservabilityStep ... /> 一行。
//
// props 形态对齐 InitPage 现有 reactive object / closure helper(同 ConfigSourceStep 的迁移取舍),
// 不重新设计签名以最小化迁移风险。

import { inject } from 'vue'
import type { CredField } from '../lib/credFields'
import type { One2AllResourceState } from '../lib/wizardStore'
import type { K8sRuntimeEnvLocator, K8sRuntimeSvcLocator } from '../lib/yamlGenerator'
import type { URLProbeState } from '../lib/probeTypes'
import type { GrafanaDatasource } from '../lib/bridge'
import { WizardStoreKey } from '../lib/wizardStore'
import CredsShareWarning from './CredsShareWarning.vue'
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
  props.getLokiMapping(envID).dsUID = value
}
function setGrafanaDsUid(obsKey: string, envID: string, value: string) {
  props.grafanaDsUidByObsEnv[props.obsGrafanaDsKey(obsKey, envID)] = value
}

function onToolToggle(tool: string, checked: boolean) {
  props.enabledObservability[tool] = checked
  if (checked && ['loki', 'prometheus', 'tempo'].includes(tool)) {
    props.enabledObservability.grafana = true
  }
}
</script>

<template>
  <div class="card lg">
    <h2>可观测性</h2>
    <p class="help-text">
      勾选系统用到的可观测性组件(Grafana / Loki / Prometheus / Jaeger 等),按环境填上连接地址,机器人查日志 / 指标时会用。
    </p>

    <CredsShareWarning :margin-bottom="18">
      <li>URL、Datasource 和资源映射保存至 <code>troubleshooter.yaml</code>。</li>
      <li>密码、Token 等 secret 仅保存到系统钥匙串，YAML 只保留环境变量引用。</li>
      <li>部署时由 Studio 把钥匙串值注入目标 AI 平台的 MCP Server 环境变量。</li>
    </CredsShareWarning>

    <!-- 启用的可观测性组件:横排 chip 选择 -->
    <h3 style="margin-top:4px">启用的可观测性组件</h3>
    <div class="obs-tool-chips">
      <label
        v-for="spec in obsToolSpecs"
        :key="spec.key"
        class="obs-tool-chip"
        :class="{
          active: enabledObservability[spec.key],
          'needs-grafana': ['loki','prometheus','tempo'].includes(spec.key)
            && enabledObservability[spec.key] && !enabledObservability['grafana']
        }"
        :title="['loki','prometheus','tempo'].includes(spec.key)
          ? spec.description + ' — 本系统通过 grafana MCP 的内置工具查询(无独立 MCP 包),启用后必须同时启用 grafana'
          : spec.description"
      >
        <input
          type="checkbox"
          :checked="enabledObservability[spec.key]"
          @change="(event) => onToolToggle(spec.key, (event.target as HTMLInputElement).checked)"
        />
        {{ spec.label }}
      </label>
    </div>

    <!-- Loki/Prometheus/Tempo 启用但 Grafana 未启用:必报错(后端 health_observability 也会挡) -->
    <div
      v-if="['loki','prometheus','tempo'].some(k => enabledObservability[k]) && !enabledObservability['grafana']"
      class="obs-grafana-required-banner"
      role="alert"
    >
      <strong>⚠ Loki / Prometheus / Tempo 必须搭配 Grafana</strong>
      <p>
        这三家在本系统通过 <code>mcp-grafana-npx</code> 内置的 <code>query_loki_logs</code> /
        <code>query_prometheus</code> 等工具查询(没有独立 MCP 包,社区也没成熟实现)。
        <strong>启用它们就必须同时启用 Grafana 并填 URL/凭据</strong>,否则 yaml validate / 部署阶段都会报错。
      </p>
      <p style="margin-top:6px">
        请在上方勾选 <strong>Grafana</strong>,或取消勾选这三家。
      </p>
    </div>

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
            :probe-state="obsProbeResults[obsProbeKey(spec.key, env.id)]"
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
            <div
              v-if="spec.key === 'k8s_runtime' && k8sConnectionReuseLabel(env.id)"
              class="cc-preload-summary"
              style="margin: 8px 0; width: fit-content;"
            >✓ {{ k8sConnectionReuseLabel(env.id) }}，部署产物会引用同一连接</div>
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
                🔗 选中 {{ spec.label }} 在 Grafana 里的 datasource
              </div>
              <div class="cc-field-row" style="gap: 12px; align-items: center; flex-wrap: wrap;">
                <select
                  v-if="spec.key === 'loki'"
                  :value="getLokiMapping(env.id).dsUID || ''"
                  class="cc-input"
                  style="max-width: 420px;"
                  @change="(e: any) => setLokiDsUid(env.id, e.target.value)"
                >
                  <option value="">— 选 Loki datasource —</option>
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
                  <option value="">— 不通过 Grafana / 留空 —</option>
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
                >🔄 {{ (getLokiMapping(env.id).dsList || []).length > 0 ? '刷新' : '加载' }} datasources</button>
                <span
                  v-if="getLokiMapping(env.id).dsListStatus === 'fail'"
                  class="cc-preload-error"
                  :title="getLokiMapping(env.id).dsListError"
                >✗ {{ getLokiMapping(env.id).dsListError?.slice(0, 50) }}</span>
                <span
                  v-else-if="(getLokiMapping(env.id).dsList || []).length > 0 && obsGrafanaDsCandidates(env.id, spec.key).length === 0"
                  class="cc-preload-summary"
                  style="background: #fee2e2; color: #991b1b;"
                >该 Grafana 里没找到 type={{ obsGrafanaDsTypes[spec.key]?.join('/') }} 的 datasource</span>
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
