<script setup lang="ts">
// LokiMappingStep —— Step 8 Loki 标签映射(per-env)。3 步:
//   1) 加载 Loki 标签 → labels[] 拉到内存
//   2) 选环境/服务维度的 label key
//   3) 选 env value + 每个服务对应的 label value
// 父端(InitPage)管理 LokiMappingPerEnv 的 reactive 引用 + onLabelKeyChanged /
// onEnvValueChanged 等副作用回调,本组件只渲染 + 触发 emit。

interface LokiMapping {
  labels: string[]
  labelStatus: 'idle' | 'loading' | 'ok' | 'fail'
  labelError?: string
  envLabelKey: string
  serviceLabelKey: string
  envLabelValues: string[]
  serviceLabelValues: string[]
  envValue: string
  serviceValues: Record<string, string>
  serviceMatchTried?: Record<string, boolean>
}

defineProps<{
  envID: string
  /** reactive 引用;v-model 绑定 envValue / serviceValues 字段直接同步回父 */
  mapping: LokiMapping
  services: string[]
}>()

const emit = defineEmits<{
  loadLabels: [envID: string]
  envLabelKeyChanged: [envID: string, key: string]
  serviceLabelKeyChanged: [envID: string, key: string]
  envValueChanged: [envID: string]
}>()
</script>

<template>
  <div class="loki-env-mapping">
    <div class="loki-env-mapping-head">
      🏷 Loki 标签映射 ({{ envID }}) —— 拉实时标签后选出"区分 env"和"区分服务"的两个 label key
    </div>

    <!-- Step 1: 加载 labels -->
    <div class="loki-mapping-step">
      <div class="loki-mapping-step-head">
        <span class="loki-step-num">1</span> 加载 Loki 标签
      </div>
      <div class="cc-field-row">
        <button
          v-if="mapping.labelStatus === 'loading'"
          type="button" class="btn cc-preload-btn" disabled
        >
          <span class="cc-preload-spinner" aria-hidden="true"></span>
          加载中…
        </button>
        <button
          v-else
          type="button" class="btn cc-preload-btn"
          @click="emit('loadLabels', envID)"
        >🏷 加载标签</button>
        <span
          v-if="mapping.labelStatus === 'ok'"
          class="cc-preload-summary"
        >✓ {{ mapping.labels.length }} 个 label</span>
        <span
          v-else-if="mapping.labelStatus === 'fail'"
          class="url-probe-badge fail"
          :title="mapping.labelError"
        >✗ {{ mapping.labelError }}</span>
      </div>
    </div>

    <!-- Step 2: 选 label keys -->
    <div v-if="mapping.labels.length > 0" class="loki-mapping-step">
      <div class="loki-mapping-step-head">
        <span class="loki-step-num">2</span> 选环境 / 服务维度的 label key
      </div>
      <div class="loki-axes">
        <label class="loki-axis-label">
          环境维度:
          <select
            :value="mapping.envLabelKey"
            class="cc-map-select"
            @change="(e: any) => emit('envLabelKeyChanged', envID, e.target.value)"
          >
            <option value="">— 选 label key —</option>
            <option v-for="l in mapping.labels" :key="l" :value="l">{{ l }}</option>
          </select>
        </label>
        <label class="loki-axis-label">
          服务维度:
          <select
            :value="mapping.serviceLabelKey"
            class="cc-map-select"
            @change="(e: any) => emit('serviceLabelKeyChanged', envID, e.target.value)"
          >
            <option value="">— 选 label key —</option>
            <option v-for="l in mapping.labels" :key="l" :value="l">{{ l }}</option>
          </select>
        </label>
      </div>
    </div>

    <!-- Step 3: env value + per-service value -->
    <div
      v-if="mapping.envLabelKey && mapping.serviceLabelKey"
      class="loki-mapping-step"
    >
      <div class="loki-mapping-step-head">
        <span class="loki-step-num">3</span> 选 env / service 具体 label 值
      </div>
      <div class="loki-mapping-env-head">
        <span class="loki-mapping-axis-name">{{ mapping.envLabelKey }}:</span>
        <select
          v-model="mapping.envValue"
          class="cc-map-select"
          @change="emit('envValueChanged', envID)"
        >
          <option value="">— 选 —</option>
          <option
            v-for="v in mapping.envLabelValues"
            :key="v" :value="v"
          >{{ v }}</option>
        </select>
      </div>
      <div
        v-for="svc in services"
        :key="svc"
        class="loki-mapping-svc-row"
      >
        <span class="loki-mapping-svc-name">{{ svc }}</span>
        <span class="loki-mapping-axis-name">{{ mapping.serviceLabelKey }}:</span>
        <select
          v-model="mapping.serviceValues[svc]"
          class="cc-map-select"
          :class="{ 'cc-map-select-none': !mapping.serviceValues[svc] }"
          :title="!mapping.serviceValues[svc] && mapping.serviceMatchTried?.[svc] && mapping.serviceLabelValues.length > 0
            ? `自动匹配未找到候选(${mapping.serviceLabelValues.length} 个 label 都不含 ${svc} 任一前缀:${svc.split('-').filter(Boolean).join(' / ')})。本环境可能没部署该服务,留空即可。`
            : ''"
        >
          <option value="">{{
            !mapping.serviceValues[svc]
              && mapping.serviceMatchTried?.[svc]
              && mapping.serviceLabelValues.length > 0
              ? '— 无 / 不进 loki(未自动匹配到) —'
              : '— 无 / 不进 loki —'
          }}</option>
          <option
            v-for="v in mapping.serviceLabelValues"
            :key="v" :value="v"
          >{{ v }}</option>
        </select>
      </div>
    </div>
  </div>
</template>
