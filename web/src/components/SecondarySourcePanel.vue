<script setup lang="ts">
// SecondarySourcePanel —— Step 5 副源 v-for 的整段外壳。
// 一个副源(如 kuboard 或 legacy-nacos)整个 form-group 模板:
//   - 标题 "<type> 连接配置 [副源]"
//   - 每 env 一个 cc-env-block:env-head + CredentialField 字段集 + (kuboard preload) + ServiceChecklist + KuboardServiceMap
//
// 父端拥有 reactive(sourceCreds / kuboardStateByEnv / kuboardSvcMap)+ helper 函数,
// 通过 props 传入,本组件只组合已有的子组件 + 触发 emit。

import { computed } from 'vue'
import type { CredField, KuboardResourceState } from '../lib/credFields'
import type { KuboardSvcLocator } from '../lib/yamlGenerator'
import CredentialField from './CredentialField.vue'
import PreloadStatusRow from './PreloadStatusRow.vue'
import ServiceChecklist from './ServiceChecklist.vue'
import KuboardServiceMap from './KuboardServiceMap.vue'
import NamespaceServiceMap from './NamespaceServiceMap.vue'
import type { CCHubEntry, CCHubNamespace } from '../lib/bridge'

const DATA_ID_CONFIG_TYPES = new Set(['nacos', 'apollo', 'consul'])

interface Environment { id: string; is_prod: boolean }
interface SourceCredsEntry { creds: Record<string, Record<string, string>>; rawExtra?: Record<string, unknown> }

const props = defineProps<{
  /** 稳定 source instance id；允许与 sourceType 不同。 */
  sourceID: string
  sourceType: string
  fields: CredField[]
  environments: Environment[]
  /** 全部服务名列表(用于 ServiceChecklist 和 KuboardServiceMap 内部 filter) */
  allServiceNames: string[]
  sourceCreds: Record<string, SourceCredsEntry>
  kuboardStateByEnv: Record<string, KuboardResourceState | undefined>
  kuboardSvcMap: Record<string, KuboardSvcLocator>
  ccHubStateByEnv: Record<string, { status: 'idle' | 'loading' | 'ok' | 'error'; entries?: CCHubEntry[]; namespaces?: CCHubNamespace[]; error?: string } | undefined>
  sourceEnvNamespaces: Record<string, string>
  serviceConfigSel: Record<string, string>
  serviceConfigGroup: Record<string, string>
  namespacesFor: (envID: string, sourceID?: string) => CCHubNamespace[]
  entriesForNamespace: (envID: string, ns: string, sourceID?: string) => CCHubEntry[]
  hasError: (key: string) => boolean
  isFieldHidden: (t: string, envID: string, f: CredField, getSibling: (k: string) => string) => boolean
  getServiceSource: (svc: string, envID?: string) => string
  svcKey: (envID: string, svc: string) => string
  kuboardNamespacesFor: (envID: string, clusterName: string) => string[]
  kuboardConfigMapsFor: (envID: string, clusterName: string, nsName: string) => string[]
}>()

// kuboard 的 cluster/namespace/configmap 不是 env-level 连接凭证,改 per-service 映射
// (走下方 KuboardServiceMap)。yamlGenerator emitSourceBody 也按这个语义只在 endpoints
// 写 url + auth 类字段。这里把这三个字段从 endpoint 表单里过滤掉,避免界面上同一个数据
// 出现两遍(env 级 + per-service)误导用户。
const endpointFields = computed<CredField[]>(() => {
  if (props.sourceType !== 'kuboard') return props.fields
  return props.fields.filter(f => f.key !== 'cluster' && f.key !== 'namespace' && f.key !== 'configmap')
})

const emit = defineEmits<{
  preloadKuboard: [sourceType: string, envID: string]
  preloadCCHub: [envID: string, sourceID: string]
  namespaceChanged: [envID: string, namespace: string, sourceID: string]
  dataIdChanged: [envID: string, svc: string, sourceID: string]
  toggleServiceSource: [svc: string, checked: boolean, sourceType: string, envID: string]
  setKuboardLoc: [envID: string, svc: string, field: 'cluster' | 'namespace' | 'configmap', value: string]
}>()

// 模板里反复 (state as any).clusters/.error,改成窄化 helper 一次性收
function kuboardClustersOf(envID: string) {
  const st = props.kuboardStateByEnv[envID]
  return (st && st.status === 'ok') ? st.clusters : []
}
function kuboardClusterCountOf(envID: string): number {
  return kuboardClustersOf(envID).length
}
function kuboardErrorOf(envID: string): string {
  const st = props.kuboardStateByEnv[envID]
  return (st && st.status === 'error') ? st.error.slice(0, 60) : ''
}
</script>

<template>
  <div class="form-group secondary-source-form">
    <label>
      <code>{{ sourceID }}</code> 连接配置 <span class="field-hint">({{ sourceType }})</span>
      <span class="auto-tag" style="background:#fef3c7;color:#92400e;">副源</span>
      <span class="field-hint">
        — 每个 env 一份连接凭证;下方"本环境包含的服务"勾选要走本副源的服务,然后给每个服务挑对应的配置定位
      </span>
    </label>
    <div v-for="env in environments" :key="env.id" class="cc-env-block">
      <div class="cc-env-head">
        <span class="cc-env-label">{{ env.id || '(未命名 env)' }}</span>
        <span v-if="env.is_prod" class="cc-env-prod-tag">prod</span>
      </div>
      <div class="cc-env-fields">
        <CredentialField
          v-for="f in endpointFields"
          :key="f.key"
          v-show="!isFieldHidden(sourceType, env.id, f, (k) => (sourceCreds[sourceID]?.creds?.[env.id] || {})[k] || '')"
          :field="f"
          :env-i-d="env.id"
          :model-value="(sourceCreds[sourceID]?.creds?.[env.id] || {})[f.key] || ''"
          :is-kuboard="sourceType === 'kuboard'"
          :kuboard-state="kuboardStateByEnv[env.id]"
          :sibling-cluster-value="(sourceCreds[sourceID]?.creds?.[env.id] || {}).cluster || ''"
          :sibling-namespace-value="(sourceCreds[sourceID]?.creds?.[env.id] || {}).namespace || ''"
          compact
          :env-var-suffix="`_${sourceID.toUpperCase().replace(/[^A-Z0-9]/g, '_')}`"
          @update:model-value="(v: string) => { if (!sourceCreds[sourceID]?.creds?.[env.id]) sourceCreds[sourceID].creds[env.id] = {}; sourceCreds[sourceID].creds[env.id][f.key] = v }"
        />
      </div>

      <!-- kuboard 副源:加"📥 拉取资源"按钮,根据 sourceCreds[t].creds[env] 读 -->
      <PreloadStatusRow
        v-if="sourceType === 'kuboard'"
        :status="kuboardStateByEnv[env.id]?.status"
        idle-text="📥 从 Kuboard 读取可选项"
        ok-text="🔄 重新读取"
        :error-message="kuboardErrorOf(env.id)"
          @click="emit('preloadKuboard', sourceID, env.id)"
      >
        <template #ok>✓ {{ kuboardClusterCountOf(env.id) }} 个集群</template>
      </PreloadStatusRow>

      <PreloadStatusRow
        v-if="DATA_ID_CONFIG_TYPES.has(sourceType)"
        :status="ccHubStateByEnv[`${sourceID}::${env.id}`]?.status"
        idle-text="📥 拉取本实例配置"
        ok-text="🔄 重新拉取本实例配置"
        :error-message="ccHubStateByEnv[`${sourceID}::${env.id}`]?.error || ''"
        @click="emit('preloadCCHub', env.id, sourceID)"
      >
        <template #ok>✓ {{ ccHubStateByEnv[`${sourceID}::${env.id}`]?.entries?.length || 0 }} 条</template>
      </PreloadStatusRow>

      <!-- 服务勾选清单:只列"主源没勾走的"剩余服务 + 已经勾给本副源的服务。
           主源已勾走的服务不出现,避免一个服务同时被两个源认领。 -->
      <ServiceChecklist
        v-if="allServiceNames.filter(s => !getServiceSource(s, env.id) || getServiceSource(s, env.id) === sourceID).length > 0"
        :services="allServiceNames.filter(s => !getServiceSource(s, env.id) || getServiceSource(s, env.id) === sourceID)"
        :source-i-d="sourceID"
        :hint-html="`主源已认领的服务在此不显示;勾选要走 <code>${sourceType}</code> 副源的服务`"
        :get-service-source="(svc) => getServiceSource(svc, env.id)"
        @toggle="(svc, checked) => emit('toggleServiceSource', svc, checked, sourceID, env.id)"
      />
      <div v-else-if="allServiceNames.length > 0" class="cc-svc-checklist-empty">
        所有服务都已被其他源认领;若想让某个服务改走 <code>{{ sourceType }}</code> 源,先在对应源把它取消勾选。
      </div>

      <!-- kuboard 副源 per-service 三联映射(跟主源共用 KuboardServiceMap) -->
      <KuboardServiceMap
        v-if="sourceType === 'kuboard'
              && kuboardStateByEnv[env.id]?.status === 'ok'
              && allServiceNames.filter(s => getServiceSource(s, env.id) === sourceID).length > 0"
        :env-i-d="env.id"
        :services="allServiceNames.filter(s => getServiceSource(s, env.id) === sourceID)"
        :kuboard-svc-map="kuboardSvcMap"
        :clusters="kuboardClustersOf(env.id)"
        :svc-key="svcKey"
        :namespaces-for="kuboardNamespacesFor"
        :configmaps-for="kuboardConfigMapsFor"
        @set-loc="(envID, svc, field, value) => emit('setKuboardLoc', envID, svc, field, value)"
      />

      <NamespaceServiceMap
        v-if="DATA_ID_CONFIG_TYPES.has(sourceType)
              && ccHubStateByEnv[`${sourceID}::${env.id}`]?.status === 'ok'
              && allServiceNames.filter(s => getServiceSource(s, env.id) === sourceID).length > 0"
        :env-i-d="env.id"
        :config-center-type="sourceID"
        :services="allServiceNames.filter(s => getServiceSource(s, env.id) === sourceID)"
        :env-namespaces="sourceEnvNamespaces"
        :namespace-map-key="`${sourceID}::${env.id}`"
        :service-config-sel="serviceConfigSel"
        :service-config-group="serviceConfigGroup"
        :namespaces="namespacesFor(env.id, sourceID)"
        :entries="entriesForNamespace(env.id, sourceEnvNamespaces[`${sourceID}::${env.id}`] || '', sourceID)"
        :svc-key="svcKey"
        :has-error="hasError"
        @namespace-changed="(_env, value) => emit('namespaceChanged', env.id, value, sourceID)"
        @data-id-changed="(_env, svc) => emit('dataIdChanged', env.id, svc, sourceID)"
      />
    </div>
  </div>
</template>
