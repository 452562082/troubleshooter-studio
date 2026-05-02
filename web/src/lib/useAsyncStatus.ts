// useAsyncStatus —— 把"异步操作 + loading 文本 + error/success 消息"三段式状态机
// 收口到一个 composable。EditorPage 多个按钮(validate / plan / 产物预览)各跑一次同款
// try { setLoading; ...; setSuccess } catch { setError } finally { clearLoading } 的样板。
//
// 用法:
//   const { loading, errorMsg, successMsg, run, reset } = useAsyncStatus()
//   async function validate() {
//     await run('验证', async () => {
//       const r = await bridgeValidate(yaml)
//       successMsg.value = `验证通过 ...`
//     })
//   }
// run 的第二参数是 thunk;返回它的 resolve 值给 caller(成功路径用)。

import { ref } from 'vue'

export function useAsyncStatus() {
  const loading = ref('')      // 非空 = 正在跑;模板按钮 disabled / loading 文本据此显示
  const errorMsg = ref('')
  const successMsg = ref('')

  /** 清三态 + 自定义重置(调用方传 fn 复位本地额外字段:resultData / previewResult 等)。 */
  function reset(extra?: () => void) {
    errorMsg.value = ''
    successMsg.value = ''
    loading.value = ''
    extra?.()
  }

  /** run(label, fn):走 reset(extra) → setLoading → fn() → finally clearLoading;
   *  抛错走 catch 设 errorMsg,不重抛(调用方一般不需要进一步处理)。
   *  返回 fn 的 resolve 值,失败返回 undefined。 */
  async function run<T>(
    label: string,
    fn: () => Promise<T>,
    extra?: () => void,
  ): Promise<T | undefined> {
    reset(extra)
    loading.value = label
    try {
      return await fn()
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : String((e as any)?.message ?? e)
      console.error('[useAsyncStatus]', label, '失败:', e)
      errorMsg.value = msg || `${label} 失败,请看控制台`
      return undefined
    } finally {
      loading.value = ''
    }
  }

  return { loading, errorMsg, successMsg, reset, run }
}
