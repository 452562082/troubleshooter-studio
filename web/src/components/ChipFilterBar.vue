<script setup lang="ts">
// ChipFilterBar —— 通用 chip 多选过滤栏。
// LogsPage 同时用两份(source 一份 + level 一份),结构一致,只是数据源和颜色 class 略不同。
//
// 父端持有 modelValue:Record<key, boolean>,直接 v-model 绑;chip 列表由 keys 决定;
// 标签和颜色 class 由 props 决定。

defineProps<{
  /** 标签:"来源" / "级别" */
  label: string
  /** 候选项 keys,顺序决定 chip 顺序 */
  keys: readonly string[]
  /** 每个 key 的开关状态;v-model 直接绑 */
  modelValue: Record<string, boolean>
  /** 每个 key 的展示文本(可缺,缺时直接渲染 key) */
  labels?: Record<string, string>
  /** 每个 key 当前条目数 */
  counts?: Record<string, number>
  /** 每个 key 自己的额外 class(给 level chip 上"lvl-warn / lvl-error"差异化颜色用) */
  itemClass?: (key: string) => string
}>()

defineEmits<{
  'update:modelValue': [v: Record<string, boolean>]
}>()
</script>

<template>
  <div class="toolbar-group">
    <span class="toolbar-label">{{ label }}</span>
    <label
      v-for="k in keys"
      :key="k"
      class="toolbar-chip"
      :class="[itemClass?.(k) || '', { active: modelValue[k] }]"
    >
      <input type="checkbox" v-model="modelValue[k]" />
      {{ labels?.[k] ?? k }}
      <span v-if="counts" class="toolbar-count">{{ counts[k] ?? 0 }}</span>
    </label>
  </div>
</template>
