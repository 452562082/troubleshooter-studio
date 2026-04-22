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
/* 配色策略:浅色语义底 + 深色文字,跟页面 .alert.* 系列同色系,避免深灰底白字
 * 的"通知吐司" UI —— 亮色主界面里非常扎眼,而且 success/error 只靠左边 4px
 * 色条区分很难一眼看出(之前是 #166534/#991b1b/#1e40af 三种深色整块底色)。
 * 现在 success 浅绿 + 深绿字,error 浅红 + 深红字,和 .alert 视觉一致。
 * z-index 99999 压过所有 modal(之前 99998 被个别 .modal-backdrop 遮)。 */
.toast-stack {
  position: fixed; top: 16px; right: 16px; z-index: 99999;
  display: flex; flex-direction: column; gap: 10px;
  max-width: 440px; pointer-events: none;
}
.toast {
  pointer-events: auto;
  display: flex; align-items: flex-start; gap: 10px;
  padding: 12px 14px; border-radius: var(--r-md);
  font-size: var(--fs-base); line-height: 1.45; font-weight: 500;
  box-shadow: 0 6px 16px rgba(15, 23, 42, 0.12), 0 2px 4px rgba(15, 23, 42, 0.06);
  cursor: pointer; user-select: none;
  min-width: 260px; max-width: 440px;
  word-break: break-word;
  /* 左边 4px 语义色条,点击跳变不明显,所以加粗让颜色更容易注意到 */
  border-left: 4px solid transparent;
  /* 底色 default = info */
  background: #eff6ff; color: #1e3a8a; border-left-color: var(--c-accent);
}
.toast-success {
  background: var(--c-success-bg); color: var(--c-success);
  border-left-color: #16a34a;
}
.toast-error {
  background: var(--c-danger-bg); color: var(--c-danger);
  border-left-color: #dc2626;
}
.toast-info {
  background: #eff6ff; color: #1e3a8a;
  border-left-color: var(--c-accent);
}

.toast-icon { flex-shrink: 0; font-weight: 700; font-size: var(--fs-md); width: 18px; text-align: center; }
.toast-msg  { flex: 1; }
.toast-close {
  flex-shrink: 0; opacity: 0.5; font-size: var(--fs-lg); line-height: 1;
  /* 继承当前 toast 的语义色,避免默认白色在浅底看不见 */
  color: inherit;
}
.toast:hover .toast-close { opacity: 0.9; }

.toast-enter-active, .toast-leave-active { transition: all 0.25s ease; }
.toast-enter-from { opacity: 0; transform: translateX(100%); }
.toast-leave-to   { opacity: 0; transform: translateX(40%); }
.toast-move       { transition: transform 0.25s ease; }
</style>
