<script setup lang="ts">
// NamespaceServiceMap —— Step 6 的 nacos/apollo/consul 主源 / 副源都用的
// "namespace 选 + 每个服务对应哪个 dataId(可选 group)"映射块。
//
// 父端(InitPage)负责 reactive 状态 + 实际副作用(onNamespaceChanged 同步刷
// serviceConfigSel default / autoselect等);组件只管渲染 + 触发 emit。
//
// envNamespaces / serviceConfigSel / serviceConfigGroup 直传 reactive 引用,
// v-model 在子组件写值通过 Vue 3 proxy 自动同步回父。

import type { CCHubNamespace, CCHubEntry } from '../lib/bridge'

defineProps<{
  envID: string
  /** 用于错误 key 拼接(cc.<source>.<env>.namespace / cc.<source>.<env>.svc.<svc>) */
  configCenterType: string
  /** 已勾选走当前源的服务列表(allServiceNames.filter(s => getServiceSource(s) === configCenterType)) */
  services: string[]
  /** envID → namespace ID;v-model 直接读写 */
  envNamespaces: Record<string, string>
  /** 多实例时 namespace map 使用 sourceID::env；默认仍用 envID。 */
  namespaceMapKey?: string
  /** svcKey → 选中的 dataId */
  serviceConfigSel: Record<string, string>
  /** svcKey → 选中条目的 group(展示用,从 entry.group 派生) */
  serviceConfigGroup: Record<string, string>
  /** 该 env 下可选的所有 namespace */
  namespaces: CCHubNamespace[]
  /** 当前选中 namespace 下可选的所有 entry */
  entries: CCHubEntry[]
  svcKey: (envID: string, svc: string) => string
  /** 当前 (env, key) 是否有校验错(hasError) */
  hasError: (key: string) => boolean
}>()

const emit = defineEmits<{
  /** namespace select 切换 → 父端 onNamespaceChanged 执行 default 选项重置等 */
  namespaceChanged: [envID: string, newNsID: string]
  /** dataId select 切换 → 父端 onDataIdChanged 同步 group */
  dataIdChanged: [envID: string, svc: string]
}>()
</script>

<template>
  <div class="cc-map-block">
    <div class="cc-map-head">
      <span class="cc-map-title">
        {{ envID }} → 挑 namespace + 每个服务对应哪个 dataId
      </span>
    </div>

    <!-- namespace 下拉:env.id 左 + 箭头 + 右 select。 -->
    <div class="cc-map-ns-grid">
      <div class="cc-map-ns-item">
        <span class="cc-map-ns-env">{{ envID || '?' }}</span>
        <span class="cc-map-ns-arrow">→</span>
        <select
          :value="envNamespaces[namespaceMapKey || envID] || ''"
          class="cc-map-select"
          :class="{ error: hasError(`cc.${configCenterType}.${envID}.namespace`) }"
          @change="(e: any) => emit('namespaceChanged', envID, e.target.value)"
        >
          <option value="">— 选 namespace —</option>
          <option
            v-for="ns in namespaces"
            :key="ns.id || 'public'"
            :value="ns.id"
          >{{ ns.show_name || ns.id || 'public' }}</option>
        </select>
        <span class="cc-map-ns-count">{{ entries.length }} 条</span>
      </div>
    </div>

    <!-- 配置项映射:每行 service → dataId 下拉,group 用 chip 标 -->
    <div class="cc-map-svc-list">
      <div
        v-for="svc in services"
        :key="svc"
        class="cc-map-svc-row"
      >
        <span class="cc-map-svc-name">{{ svc }}</span>
        <select
          v-model="serviceConfigSel[svcKey(envID, svc)]"
          class="cc-map-select cc-map-select-svc"
          :class="{ error: hasError(`cc.${configCenterType}.${envID}.svc.${svc}`) }"
          @change="emit('dataIdChanged', envID, svc)"
        >
          <option value="">(不映射)</option>
          <option
            v-for="entry in entries"
            :key="entry.locator + '@' + (entry.group || '')"
            :value="entry.locator"
          >
            {{ entry.locator }}<template v-if="entry.group && entry.group !== 'DEFAULT_GROUP'">  @{{ entry.group }}</template>
          </option>
        </select>
        <span
          v-if="serviceConfigGroup[svcKey(envID, svc)]"
          class="cc-map-group-tag"
          :title="'group = ' + serviceConfigGroup[svcKey(envID, svc)]"
        >
          {{ serviceConfigGroup[svcKey(envID, svc)] }}
        </span>
      </div>
    </div>
  </div>
</template>
