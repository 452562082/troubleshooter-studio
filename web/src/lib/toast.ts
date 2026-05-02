// 全局 toast：跨页面轻提示。不依赖 Pinia,用一个 reactive ref 做单例 channel,
// 任何地方 import toast.success('...') 即可。UI 由 App.vue 里挂的 <ToastContainer /> 渲染。

import { ref } from 'vue'

export type ToastKind = 'info' | 'success' | 'error'

export interface Toast {
  id: number
  kind: ToastKind
  message: string
  /** auto-dismiss 毫秒数;0 或负数 = 不自动消,用户点击关 */
  ttl: number
}

const list = ref<Toast[]>([])
let nextId = 0

/** 返给 ToastContainer 订阅的 ref */
export function useToasts() {
  return { list }
}

export function showToast(
  message: string,
  opts?: { kind?: ToastKind; ttl?: number },
): number {
  const t: Toast = {
    id: ++nextId,
    kind: opts?.kind ?? 'info',
    message,
    ttl: opts?.ttl ?? 4000,
  }
  list.value.push(t)
  if (t.ttl > 0) {
    window.setTimeout(() => dismiss(t.id), t.ttl)
  }
  return t.id
}

export function dismiss(id: number) {
  list.value = list.value.filter((t) => t.id !== id)
}

/** 快捷方式;error 默认更长(8s),让用户有时间读 */
export const toast = {
  info: (msg: string, ttl?: number) => showToast(msg, { kind: 'info', ttl }),
  success: (msg: string, ttl?: number) => showToast(msg, { kind: 'success', ttl }),
  error: (msg: string, ttl?: number) => showToast(msg, { kind: 'error', ttl: ttl ?? 8000 }),
}

/** catch 块的"操作失败"通用 toast。把 catch(e) {...} 收口成一行:
 *
 *     try { ... } catch (e) { toastError('部署', e) }
 *
 * 自动从 e 里提取 message,跟散落 18 处的 `toast.error(label + ': ' + String(e?.message || e))`
 * 模板对齐。
 */
export function toastError(label: string, e: unknown, ttl?: number) {
  const msg = e instanceof Error ? e.message : String((e as any)?.message ?? e)
  return toast.error(`${label}失败:${msg}`, ttl)
}
