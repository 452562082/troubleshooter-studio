<script setup lang="ts">
// CredsShareWarning —— "⚠ 凭证将进 yaml,分享注意范围" 通用警告块。
// Step 5(主源凭证下面)/ 7(数据层页头)/ 8(可观测性页头) 各有一份措辞略微不同的版本,
// 共享外壳 + 标题 prop + 默认 slot 让 li 列表内容自定义。

withDefaults(defineProps<{
  title?: string
  /** 顶部 margin px,Step 7/8 用 18,Step 5 主源用 0(下面紧接 env block) */
  marginBottom?: number
}>(), { title: '⚠ 数据流与共享范围', marginBottom: 0 })
</script>

<template>
  <div class="cc-share-warn" :style="{ marginBottom: marginBottom + 'px' }">
    <div class="cc-share-warn-title">{{ title }}</div>
    <ul class="cc-share-warn-list">
      <slot />
    </ul>
  </div>
</template>
