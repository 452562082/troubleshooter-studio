<script setup lang="ts">
import { useToasts, dismiss } from '../lib/toast'

const { list } = useToasts()
</script>

<template>
  <TransitionGroup tag="div" name="toast" class="toast-stack">
    <div
      v-for="t in list"
      :key="t.id"
      class="toast"
      :class="'toast-' + t.kind"
      role="status"
      @click="dismiss(t.id)"
    >
      <span class="toast-icon">
        {{ t.kind === 'success' ? '✓' : t.kind === 'error' ? '⚠' : 'ⓘ' }}
      </span>
      <span class="toast-msg">{{ t.message }}</span>
      <span class="toast-close" aria-label="关闭">×</span>
    </div>
  </TransitionGroup>
</template>

<style scoped>
.toast-stack {
  position: fixed; top: 16px; right: 16px; z-index: 99998;
  display: flex; flex-direction: column; gap: 8px;
  max-width: 420px; pointer-events: none;
}
.toast {
  pointer-events: auto;
  display: flex; align-items: flex-start; gap: 10px;
  padding: 10px 12px 10px 14px; border-radius: 6px;
  background: #1e293b; color: #f1f5f9; font-size: 13px; line-height: 1.4;
  box-shadow: 0 4px 12px rgba(0, 0, 0, 0.15);
  cursor: pointer; user-select: none;
  min-width: 220px; max-width: 420px;
  word-break: break-word;
}
.toast-success { background: #166534; }
.toast-error   { background: #991b1b; }
.toast-info    { background: #1e40af; }

.toast-icon { flex-shrink: 0; font-weight: bold; font-size: 14px; }
.toast-msg  { flex: 1; }
.toast-close {
  flex-shrink: 0; opacity: 0.6; font-size: 16px; line-height: 1;
}
.toast:hover .toast-close { opacity: 1; }

.toast-enter-active, .toast-leave-active { transition: all 0.25s ease; }
.toast-enter-from { opacity: 0; transform: translateX(100%); }
.toast-leave-to   { opacity: 0; transform: translateX(40%); }
.toast-move       { transition: transform 0.25s ease; }
</style>
