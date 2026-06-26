<script setup lang="ts">
// ObservabilityToolBlock —— Step 8 每个启用的可观测性工具(grafana/loki/prometheus/jaeger/elk/
// skywalking/tempo/k8s_runtime)的卡片外壳。
//
// 处理:
//   - ds-svc-block 容器 + status 样式
//   - 卡片 head(工具名 + "访问方式"select + URLProbeBadge)
//   - 直连模式下渲染本工具的字段集(用 CredentialField 复用 Step 5 同款)
//
// 不处理(由父端 slot 注入):
//   - k8s_runtime 的集群/ns + 服务级 Deployment 映射
//   - via_grafana 模式下的 datasource 选择 + 加载按钮
//   - loki 的标签映射 workflow
// 这些跟 InitPage 的 reactive(k8sRuntimeEnvLoc / lokiMappingByEnv / grafanaDsUidByObsEnv)
// 耦合太深,放 slot 让父端就近处理,组件保持外壳通用。

import type { CredField } from '../lib/credFields'
import type { URLProbeState } from '../lib/probeTypes'
import URLProbeBadge from './URLProbeBadge.vue'
import CredentialField from './CredentialField.vue'

interface ToolSpec {
  key: string
  label: string
  fields: CredField[]
}

defineProps<{
  envID: string
  spec: ToolSpec
  /** 'via_grafana' / 'direct'(只对 loki/prometheus/jaeger/tempo/elk 有意义,其它工具固定 direct) */
  accessMode: 'via_grafana' | 'direct'
  /** 当前工具是否能在 via_grafana / direct 之间切换(loki/prom/jaeger/tempo/elk + Grafana 启用时) */
  accessToggleable: boolean
  probeState: URLProbeState | undefined
  /** reactive map;子组件 v-model 等价行为通过 emit('update:toolInput', k, v) */
  toolInputs: Record<string, string>
  isRevealed: (k: string) => boolean
  isObsFieldHidden: (toolKey: string, envID: string, f: CredField) => boolean
  displayObsField: (toolKey: string, envID: string, f: CredField) => CredField
  toolKeyFor: (cat: 'obs' | 'ds', tool: string, envID: string, field: string) => string
}>()

const emit = defineEmits<{
  'update:accessMode': [mode: 'via_grafana' | 'direct']
  /** 字段值变更:父端写 toolInputs[k]=v + 触发 obsProbe / grafana ds autoload 副作用 */
  'update:toolInput': [key: string, value: string]
  toggleReveal: [key: string]
  clearInput: [key: string]
}>()
</script>

<template>
  <div :class="['ds-svc-block', 'status-' + (probeState?.status || 'pending')]">
    <div class="ds-svc-head">
      <span class="ds-svc-name">🗄 {{ spec.label }}</span>
      <!-- 访问方式 -->
      <span
        v-if="accessToggleable"
        class="cc-field-row"
        style="gap: 6px; align-items: center; margin-left: auto;"
      >
        <span style="font-size: 12px; color: #6b7280;">访问方式</span>
        <select
          :value="accessMode"
          class="cc-input"
          style="height: 28px; padding: 0 6px; font-size: 13px; min-width: 160px;"
          @change="(e: any) => emit('update:accessMode', e.target.value)"
        >
          <option value="via_grafana">🔗 通过 Grafana 代理</option>
          <option value="direct">🔌 直连(自己填 URL)</option>
        </select>
      </span>
      <URLProbeBadge :state="probeState" />
    </div>
    <!-- 字段集:只在 direct 模式下渲染(via_grafana 走的是 datasource UID 选择器,在下方 slot)。
         注:之前误用 `!accessToggleable || accessMode === 'direct'` — accessToggleable 锁成 false
         后 `!accessToggleable=true` 永远成立,via_grafana 的 loki/prometheus/tempo 也会让用户白填
         URL 字段。改成只看 accessMode 一个条件,跟 useObsAccessMode 的锁死规则对齐。 -->
    <div
      v-if="accessMode === 'direct'"
      class="ds-item-fields"
    >
      <CredentialField
        v-for="f in spec.fields"
        :key="f.key"
        v-show="!isObsFieldHidden(spec.key, envID, f)"
        :field="displayObsField(spec.key, envID, f)"
        :env-i-d="envID"
        :model-value="toolInputs[toolKeyFor('obs', spec.key, envID, f.key)] || ''"
        :is-revealed="isRevealed(toolKeyFor('obs', spec.key, envID, f.key))"
        :is-kuboard="false"
        @update:model-value="(v: string) => emit('update:toolInput', toolKeyFor('obs', spec.key, envID, f.key), v)"
        @toggle-reveal="emit('toggleReveal', toolKeyFor('obs', spec.key, envID, f.key))"
        @clear="emit('clearInput', toolKeyFor('obs', spec.key, envID, f.key))"
      />
    </div>
    <!-- 工具特定的尾部内容:k8s_runtime / via_grafana ds 选择器 / loki 标签映射 -->
    <slot />
  </div>
</template>
