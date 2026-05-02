<script setup lang="ts">
// CredentialField —— Step 5 配置中心 + Step 7 可观测性 凭证表单的单字段编辑器。
// 内部根据 field 形状渲染五种输入态:
//   1. options 非空 → <select> 下拉(枚举字段,如 auth_mode)
//   2. isKuboard + key='cluster'/'namespace'/'configmap' → 三级联动 <select>(从 kuboardState 取候选)
//   3. f.secret → password 输入 + 眼睛按钮切显隐 + 清空按钮
//   4. 普通文本 input + 清空按钮
//
// 父组件负责维护 ccCredInputs(扁平 keychain map)和 kuboardStateByEnv(每 env 的 cluster→ns→cm 树),
// 把对应字段的 value / kuboardState slice 传进来,子组件不直接读全局 reactive。

import { computed, onMounted } from 'vue'
import type { CredField, KuboardResourceState } from '../lib/credFields'

const props = withDefaults(defineProps<{
  field: CredField
  envID: string
  /** 当前值;对 select 来说也是当前选中项 */
  modelValue: string
  /** secret 字段是否当前显示明文(父端 revealedSecrets set 算)。compact 模式忽略 */
  isRevealed?: boolean
  /** 是否正在渲染 kuboard 配置中心(决定 cluster/namespace/configmap 走级联下拉) */
  isKuboard: boolean
  /** 本 env 的 kuboard 资源拉取状态;非 kuboard 字段忽略 */
  kuboardState?: KuboardResourceState
  /** namespace / configmap 字段需要拿到 sibling cluster / namespace 的当前值算可选项 */
  siblingClusterValue?: string
  siblingNamespaceValue?: string
  /** compact=true:不渲染 reveal / clear 按钮(副源凭证表单用)。secret 字段 type 固定 password */
  compact?: boolean
  /** envVar 提示行追加的后缀,如副源的 "_LEGACY_NACOS"。默认空串。 */
  envVarSuffix?: string
}>(), {
  isRevealed: false,
  compact: false,
  envVarSuffix: '',
})

const emit = defineEmits<{
  'update:modelValue': [value: string]
  toggleReveal: []
  clear: []
}>()

// options 字段(如 auth_mode)首次挂载时,如果父端 modelValue 还是空字符串,
// <select> UI 因 fallback 显示首项,但底层 reactive 值仍空 → showWhen 判定的兄弟字段值
// 跟视觉不一致,被它依赖的字段(api_key / username 等)看似该显示却被判隐藏。
// 这里在挂载时把"视觉上的默认值"显式同步回父端,消除视觉/状态错位。
onMounted(() => {
  if (props.field.options && props.field.options.length > 0 && !props.modelValue) {
    const def = props.field.options[0]?.value || ''
    if (def) emit('update:modelValue', def)
  }
})

const isKuboardCascade = computed(() =>
  props.isKuboard && (props.field.key === 'cluster' || props.field.key === 'namespace' || props.field.key === 'configmap'),
)

const namespaces = computed<string[]>(() => {
  const st = props.kuboardState
  if (!st || st.status !== 'ok') return []
  const c = st.clusters.find(c => c.name === props.siblingClusterValue)
  return c ? c.namespaces.map(n => n.name) : []
})

const configmaps = computed<string[]>(() => {
  const st = props.kuboardState
  if (!st || st.status !== 'ok') return []
  const cluster = st.clusters.find(cl => cl.name === props.siblingClusterValue)
  if (!cluster) return []
  const ns = cluster.namespaces.find(n => n.name === props.siblingNamespaceValue)
  return ns ? ns.configmaps : []
})

const inputType = computed(() => {
  if (!props.field.secret) return 'text'
  // compact 模式没有眼睛按钮可切;固定 password 遮码,跟原副源行为对齐
  if (props.compact) return 'password'
  return props.isRevealed ? 'text' : 'password'
})

function onSelect(e: Event) {
  emit('update:modelValue', (e.target as HTMLSelectElement).value)
}

function onInput(e: Event) {
  emit('update:modelValue', (e.target as HTMLInputElement).value)
}
</script>

<template>
  <div class="cc-field">
    <label class="cc-field-label">
      {{ field.label }}
      <span v-if="field.optional" class="auto-tag">选填</span>
      <span v-if="field.secret" class="cc-scope-tag secret" title="Secret:会写入 yaml,分享时注意范围">🔒 Secret</span>
    </label>
    <div class="cc-field-row">
      <!-- 1. enum 字段 -->
      <select
        v-if="field.options"
        :value="modelValue || (field.options[0]?.value || '')"
        class="cc-input"
        @change="onSelect"
      >
        <option v-for="opt in field.options" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
      </select>

      <!-- 2. kuboard cluster -->
      <select
        v-else-if="isKuboardCascade && field.key === 'cluster'"
        :value="modelValue"
        :disabled="kuboardState?.status !== 'ok'"
        class="cc-input"
        @change="onSelect"
      >
        <option v-if="kuboardState?.status !== 'ok'" value="">— 先填 URL+鉴权,点上方"📥 拉取" —</option>
        <option v-else value="">— 选集群 —</option>
        <option v-for="c in (kuboardState?.status === 'ok' ? kuboardState.clusters : [])" :key="c.name" :value="c.name">{{ c.name }}</option>
      </select>

      <!-- 3. kuboard namespace -->
      <select
        v-else-if="isKuboardCascade && field.key === 'namespace'"
        :value="modelValue"
        :disabled="kuboardState?.status !== 'ok' || !siblingClusterValue"
        class="cc-input"
        @change="onSelect"
      >
        <option v-if="kuboardState?.status !== 'ok'" value="">— 先拉取资源 —</option>
        <option v-else-if="!siblingClusterValue" value="">— 先选集群 —</option>
        <option v-else value="">— 选 namespace —</option>
        <option v-for="n in namespaces" :key="n" :value="n">{{ n }}</option>
      </select>

      <!-- 4. kuboard configmap -->
      <select
        v-else-if="isKuboardCascade && field.key === 'configmap'"
        :value="modelValue"
        :disabled="kuboardState?.status !== 'ok' || !siblingNamespaceValue"
        class="cc-input"
        @change="onSelect"
      >
        <option v-if="kuboardState?.status !== 'ok'" value="">— 先拉取资源 —</option>
        <option v-else-if="!siblingNamespaceValue" value="">— 先选 namespace —</option>
        <option v-else value="">— 选 ConfigMap —</option>
        <option v-for="cm in configmaps" :key="cm" :value="cm">{{ cm }}</option>
      </select>

      <!-- 5. 普通文本 / password 输入 -->
      <input
        v-else
        :value="modelValue"
        :type="inputType"
        :placeholder="field.placeholder || ''"
        autocomplete="off"
        spellcheck="false"
        class="cc-input"
        @input="onInput"
      />

      <button
        v-if="!compact && field.secret && !field.options"
        type="button"
        class="btn-link cc-reveal"
        :title="isRevealed ? '隐藏明文' : '显示明文'"
        @click="emit('toggleReveal')"
      >{{ isRevealed ? '🙈' : '👁' }}</button>
      <button
        v-if="!compact && !field.options && modelValue"
        type="button"
        class="btn-link cc-delete"
        title="清空本字段"
        @click="emit('clear')"
      >🗑</button>
    </div>
    <div v-if="!field.uiOnly" class="cc-env-hint">
      对应环境变量:<code>{{ field.envVar(envID || 'ENV') }}{{ envVarSuffix }}</code>
    </div>
  </div>
</template>
