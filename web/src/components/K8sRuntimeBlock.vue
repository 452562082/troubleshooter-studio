<script setup lang="ts">
// K8sRuntimeBlock —— Step 8 obs k8s_runtime 工具的 tail 内容(放在 ObservabilityToolBlock 的 slot 里):
//   1) 加载集群资源按钮(复用 PreloadButton)+ 集群计数 / 错误展示
//   2) env 级集群 + namespace 选择(只挑一次)
//   3) 每服务一行 Deployment 下拉(选完了 ns 才显示)
//
// 父端持有 kuboardStateByEnv / k8sRuntimeEnvLoc / k8sRuntimeSvcMap / k8sRtWorkloadCache
// reactive,以及各种 setter / fetcher 函数;本组件只渲染 + emit。

import PreloadButton from './PreloadButton.vue'
import type { KuboardResourceState } from '../lib/credFields'
import type { K8sRuntimeEnvLocator, K8sRuntimeSvcLocator } from '../lib/yamlGenerator'

interface WorkloadCacheEntry { status?: 'idle' | 'loading' | 'ok' | 'error' }

defineProps<{
  envID: string
  services: string[]
  /** kuboardStateByEnv[envID] —— 跟 KuboardSelector 共用同一棵 cluster 树 */
  kuboardState: KuboardResourceState | undefined
  /** k8sRuntimeEnvLoc[envID] —— env 级 cluster + namespace 选择 */
  envLoc: K8sRuntimeEnvLocator | undefined
  /** svcKey → workload 选择(reactive map) */
  svcMap: Record<string, K8sRuntimeSvcLocator>
  /** k8sRtWorkloadCache,Deployment 列表 fetch 状态 */
  workloadCache: Record<string, WorkloadCacheEntry | undefined>
  svcKey: (envID: string, svc: string) => string
  workloadKey: (envID: string, cluster: string, ns: string) => string
  workloadsFor: (envID: string, cluster: string, ns: string) => Array<{ name: string; selector: string }>
  namespacesFor: (envID: string, clusterName: string) => string[]
}>()

const emit = defineEmits<{
  preload: [envID: string]
  setEnvLoc: [envID: string, field: 'cluster' | 'namespace', value: string]
  setSvcWorkload: [envID: string, svc: string, workload: string]
}>()
</script>

<template>
  <div class="loki-env-mapping">
    <div class="loki-env-mapping-head">
      ☸️ K8s 服务定位 ({{ envID }}) —— 先挑集群 + namespace,再给每个服务挑 Deployment
    </div>
    <!-- Step 1: 加载集群资源 -->
    <div class="cc-preload-row" style="margin: 6px 0 10px 0;">
      <PreloadButton
        :status="kuboardState?.status"
        idle-text="📥 加载集群资源"
        ok-text="🔄 重新加载集群"
        @click="emit('preload', envID)"
      />
      <span v-if="kuboardState?.status === 'ok'" class="cc-preload-summary">
        ✓ {{ (kuboardState as any).clusters.length }} 个集群
      </span>
      <span v-else-if="kuboardState?.status === 'error'" class="cc-preload-error">
        ✗ {{ (kuboardState as any).error.slice(0, 60) }}
      </span>
    </div>

    <!-- Step 2: env 级挑集群 + namespace -->
    <div
      v-if="kuboardState?.status === 'ok'"
      class="cc-field-row"
      style="gap: 12px; margin-bottom: 10px; flex-wrap: wrap;"
    >
      <div class="cc-field" style="flex: 1; min-width: 180px;">
        <label class="cc-field-label">集群</label>
        <select
          :value="(envLoc || {}).cluster || ''"
          class="cc-input"
          @change="(e: any) => emit('setEnvLoc', envID, 'cluster', e.target.value)"
        >
          <option value="">— 选集群 —</option>
          <option
            v-for="c in (kuboardState as any).clusters"
            :key="c.name" :value="c.name"
          >{{ c.name }}</option>
        </select>
      </div>
      <div class="cc-field" style="flex: 1; min-width: 180px;">
        <label class="cc-field-label">Namespace</label>
        <select
          :value="(envLoc || {}).namespace || ''"
          :disabled="!((envLoc || {}).cluster)"
          class="cc-input"
          @change="(e: any) => emit('setEnvLoc', envID, 'namespace', e.target.value)"
        >
          <option v-if="!((envLoc || {}).cluster)" value="">— 先选集群 —</option>
          <option v-else value="">— 选 namespace —</option>
          <option
            v-for="n in namespacesFor(envID, (envLoc || {}).cluster || '')"
            :key="n" :value="n"
          >{{ n }}</option>
        </select>
      </div>
    </div>

    <!-- Step 3: 每服务一行 Deployment 下拉 -->
    <div
      v-if="(envLoc || {}).cluster && (envLoc || {}).namespace && services.length > 0"
      class="cc-map-block"
    >
      <div class="cc-map-head">
        <span class="cc-map-title">
          服务 → Deployment 映射 <span class="field-hint">— 留空表示该服务未在本环境部署到 K8s,排障时跳过 pod / log / metric 查询</span>
        </span>
      </div>
      <div class="cc-map-svc-list">
        <div
          v-for="svc in services"
          :key="`k8srt-${envID}-${svc}`"
          class="cc-map-svc-row"
        >
          <span class="cc-map-svc-name">{{ svc }}</span>
          <select
            :value="(svcMap[svcKey(envID, svc)] || {}).workload || ''"
            :disabled="workloadCache[workloadKey(envID, (envLoc || {}).cluster || '', (envLoc || {}).namespace || '')]?.status === 'loading'"
            class="cc-map-select"
            style="min-width: 240px;"
            @change="(e: any) => emit('setSvcWorkload', envID, svc, e.target.value)"
          >
            <option v-if="workloadCache[workloadKey(envID, (envLoc || {}).cluster || '', (envLoc || {}).namespace || '')]?.status === 'loading'" value="">— 正在拉取 Deployment 列表… —</option>
            <option v-else-if="workloadCache[workloadKey(envID, (envLoc || {}).cluster || '', (envLoc || {}).namespace || '')]?.status === 'error'" value="">— 拉取失败,详见日志面板 —</option>
            <option v-else-if="workloadsFor(envID, (envLoc || {}).cluster || '', (envLoc || {}).namespace || '').length === 0" value="">— 当前 namespace 下无可用 Deployment —</option>
            <option v-else value="">— 未部署 / 不在 K8s 内 —</option>
            <option
              v-for="d in workloadsFor(envID, (envLoc || {}).cluster || '', (envLoc || {}).namespace || '')"
              :key="d.name" :value="d.name"
              :title="d.selector ? 'selector: ' + d.selector : ''"
            >{{ d.name }}</option>
          </select>
        </div>
      </div>
    </div>
  </div>
</template>
