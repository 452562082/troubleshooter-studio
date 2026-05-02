<script setup lang="ts">
// PreloadButton —— "📥 / 🔄 / 拉取中…" 三态预加载按钮。
// Step 5(主源 + 副源 kuboard / 各 env CCHub)和 Step 7 / 8 各处的"拉取资源"
// 按钮长得一致:同样的 spinner + 同样的 idle "📥" / ok "🔄 重新…" / loading "拉取中…"。
// 各处差别仅在按钮文本 + 点击处理函数。
//
// 不同 v-if 分支里用 :key 区分 loading / ok / idle 是因为 WebKit 在 reactive
// 切换 disabled / 文本时会有 GPU layer 残影,加 :key 强制 remount。本组件
// 内部用 v-if 渲染对应分支,Vue 自动保证不复用 DOM 节点。

defineProps<{
  /** 当前状态。'error' 状态显示成 idle 按钮(给用户重试),具体 error 文案父端在按钮后面渲染 */
  status: 'idle' | 'loading' | 'ok' | 'error' | undefined
  /** idle / error 状态下的按钮文字,如 "📥 拉取资源" */
  idleText: string
  /** ok 状态下的按钮文字,通常 "🔄 重新读取" 或 "🔄 重新拉取..." */
  okText: string
  /** loading 状态下的文本,默认 "拉取中…" */
  loadingText?: string
}>()

defineEmits<{ click: [] }>()
</script>

<template>
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
</template>
