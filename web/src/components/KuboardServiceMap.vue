<script setup lang="ts">
// KuboardServiceMap —— Step 6 kuboard 配置中心的 per-service cluster/namespace/configmap
// 三联映射块。结构上跟 NamespaceServiceMap 是姐妹组件:NSM 用于 nacos/apollo/consul
// 的 namespace + dataId 二维选择,本组件用于 kuboard 的三级级联(cluster→ns→cm)。
//
// 父端(InitPage)持有 kuboardSvcMap 和 kuboardStateByEnv;每个 (env, svc) 一行,
// 用户每选一级,emit 事件,父端 setKuboardLoc 写回 kuboardSvcMap。
//
// 候选项不在子组件内派生(父端 kuboardNamespacesFor / kuboardConfigMapsFor 已经做),
// 避免子组件再次依赖 kuboardStateByEnv 整棵树。

import type { KuboardSvcLocator } from '../lib/yamlGenerator'

defineProps<{
  envID: string
  /** 已勾选走 kuboard 源的服务列表 */
  services: string[]
  /** svcKey → {cluster, namespace, configmap};reactive 引用,select :value 直接读 */
  kuboardSvcMap: Record<string, KuboardSvcLocator>
  /** 集群候选项(父端从 kuboardStateByEnv[envID].clusters 派生) */
  clusters: Array<{ name: string }>
  svcKey: (envID: string, svc: string) => string
  /** 给当前选中 cluster 算可选 namespace 列表 */
  namespacesFor: (envID: string, clusterName: string) => string[]
  /** 给当前选中 (cluster, namespace) 算可选 configmap 列表 */
  configmapsFor: (envID: string, clusterName: string, nsName: string) => string[]
}>()

const emit = defineEmits<{
  setLoc: [envID: string, svc: string, field: 'cluster' | 'namespace' | 'configmap', value: string]
}>()
</script>

<template>
  <div class="cc-map-block">
    <div class="cc-map-head">
      <span class="cc-map-title">
        {{ envID }} → 每个服务对应的 集群 / namespace / ConfigMap
      </span>
    </div>
    <div class="cc-map-svc-list">
      <div
        v-for="svc in services"
        :key="svc"
        class="cc-map-svc-row cc-map-svc-row-kuboard"
      >
        <span class="cc-map-svc-name">{{ svc }}</span>
        <select
          :value="(kuboardSvcMap[svcKey(envID, svc)] || {}).cluster || ''"
          class="cc-map-select cc-map-select-kuboard"
          @change="(e: any) => emit('setLoc', envID, svc, 'cluster', e.target.value)"
        >
          <option value="">— 选集群 —</option>
          <option
            v-for="c in clusters"
            :key="c.name"
            :value="c.name"
          >{{ c.name }}</option>
        </select>
        <select
          :value="(kuboardSvcMap[svcKey(envID, svc)] || {}).namespace || ''"
          :disabled="!((kuboardSvcMap[svcKey(envID, svc)] || {}).cluster)"
          class="cc-map-select cc-map-select-kuboard"
          @change="(e: any) => emit('setLoc', envID, svc, 'namespace', e.target.value)"
        >
          <option v-if="!((kuboardSvcMap[svcKey(envID, svc)] || {}).cluster)" value="">— 先选集群 —</option>
          <option v-else value="">— 选 namespace —</option>
          <option
            v-for="n in namespacesFor(envID, (kuboardSvcMap[svcKey(envID, svc)] || {}).cluster || '')"
            :key="n"
            :value="n"
          >{{ n }}</option>
        </select>
        <select
          :value="(kuboardSvcMap[svcKey(envID, svc)] || {}).configmap || ''"
          :disabled="!((kuboardSvcMap[svcKey(envID, svc)] || {}).namespace)"
          class="cc-map-select cc-map-select-kuboard"
          @change="(e: any) => emit('setLoc', envID, svc, 'configmap', e.target.value)"
        >
          <option v-if="!((kuboardSvcMap[svcKey(envID, svc)] || {}).namespace)" value="">— 先选 namespace —</option>
          <option v-else value="">— 选 ConfigMap —</option>
          <option
            v-for="cm in configmapsFor(envID, (kuboardSvcMap[svcKey(envID, svc)] || {}).cluster || '', (kuboardSvcMap[svcKey(envID, svc)] || {}).namespace || '')"
            :key="cm"
            :value="cm"
          >{{ cm }}</option>
        </select>
      </div>
    </div>
  </div>
</template>
