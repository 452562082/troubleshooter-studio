<script setup lang="ts">
// PreloadStatusRow —— PreloadButton + 旁边的 ✓ summary / ✗ error 整段。
// Step 5 主源/副源 kuboard、Step 5 CCHub、Step 7 k8s_runtime 的 4 处布局完全一致,
// 共用本组件后只剩"按钮文案 + summary 文本 + 点击 handler"由 props/slot 注入。
//
// 错误态自带"查看日志"router-link(全场景一致),只用 errorMessage 传 60 字以内
// 摘要文本即可。

defineProps<{
  status: 'idle' | 'loading' | 'ok' | 'error' | undefined
  idleText: string
  okText: string
  loadingText?: string
  /** error 状态摘要(已 slice(0, 60));留空 → 只显示"✗ 拉取失败" */
  errorMessage?: string
}>()

defineEmits<{ click: [] }>()
</script>

<template>
  <div class="cc-preload-row">
    <button
      v-if="status === 'loading'"
      type="button"
      class="btn cc-preload-btn"
      disabled
    >
      <span class="cc-preload-spinner" aria-hidden="true"></span>
      {{ loadingText || '拉取中…' }}
    </button>
    <button
      v-else-if="status === 'ok'"
      type="button"
      class="btn cc-preload-btn"
      @click="$emit('click')"
    >{{ okText }}</button>
    <button
      v-else
      type="button"
      class="btn cc-preload-btn"
      @click="$emit('click')"
    >{{ idleText }}</button>

    <span v-if="status === 'ok'" class="cc-preload-summary">
      <slot name="ok" />
    </span>
    <span v-else-if="status === 'error'" class="cc-preload-error">
      ✗ {{ errorMessage || '拉取失败' }}
      <router-link to="/logs" class="cc-preload-log-link">查看日志</router-link>
    </span>
  </div>
</template>
