// useStepNavigation —— wizard 10 步流程的跳转 helpers + 越界钳制 + schema 迁移。
// InitPage.vue 把这部分独立出来减小巨型组件 script size。
//
// 设计:**不**持有 currentStep ref,InitPage 自己 `ref(initialStep)`(initialStep 用本
// 模块的 migrateSavedStep 算)然后传进来。理由:canGoNext computed 必然依赖 currentStep,
// 让 composable 持有 currentStep + 接受 canAdvance computed → 创建时循环依赖。
// 外置 currentStep + 函数接受参数 = 接口最干净,InitPage 想直接读写 currentStep 也方便。
//
// **不做**的事(故意留在 InitPage):
//   - canAdvance / nextBlockedHint / currentStepErrors —— 依赖 30+ reactive(system/agent/
//     enabledTargets/environments/repos/sourceCreds/...),抽出来要把整个 InitPage state
//     拖过来,得不偿失。InitPage 自己 computed,通过 canAdvance prop 喂给本 composable。
//   - localStorage 读写 —— 留给阶段 2 的 usePersistence 统一管。本 composable 只接受
//     一个 initialStep number 参数(InitPage 从 saved draft 解出来)。
//
// 边界保证:无论怎么进入 currentStep,clampCurrentStep 都把它钳在 [1, totalSteps];
// NaN / 越界 / 异常退化到 step 1。防 saved draft 损坏让 v-if="currentStep===N" 全部
// false 导致内容区白屏(老 bug)。
import type { ComputedRef, Ref } from 'vue'

export interface UseStepNavigationOptions {
  /** InitPage 自己创建并持有的 currentStep ref。本模块只读写,不创建。 */
  currentStep: Ref<number>
  /** wizard 总步数(目前固定 10)。改这个数前要同步 InitPage template 的 v-if 列表。 */
  totalSteps: number
  /** ComputedRef<boolean>:由 InitPage 算"当前 step 校验是否通过 + 不在最后一步"。
   *  nextStep / goToStep(向前)看这个决定能不能推进;prevStep / goToStep(向后)无视。 */
  canAdvance: ComputedRef<boolean>
  /** 出错回调(InitPage 注入 pushLog),让 navigation 内部 try/catch 能把错误信号传出去
   *  又不直接依赖 logStore;测试时不传或传 noop。 */
  onError?: (msg: string) => void
}

export interface UseStepNavigation {
  /** 钳到 [1, totalSteps];NaN / 非数字 → 1。public 是因为外部(import 草稿等)
   *  改 currentStep 后想立刻 clamp 一遍是常见用法。 */
  clampCurrentStep: () => void
  /** 校验通过且未到末步 → ++。否则 no-op。 */
  nextStep: () => void
  /** 不校验,> 1 → --。 */
  prevStep: () => void
  /** 倒退随意,前进需 canAdvance。允许跳多步。 */
  goToStep: (step: number) => void
}

export function useStepNavigation(opts: UseStepNavigationOptions): UseStepNavigation {
  const { currentStep, totalSteps, canAdvance } = opts
  const onError = opts.onError ?? (() => { /* noop */ })

  function clampCurrentStep(): void {
    if (typeof currentStep.value !== 'number' || isNaN(currentStep.value)) {
      currentStep.value = 1
      return
    }
    if (currentStep.value < 1) currentStep.value = 1
    else if (currentStep.value > totalSteps) currentStep.value = totalSteps
  }
  // 构造时立刻 clamp 一遍,防 initialStep 已是脏值就直接挂出去
  clampCurrentStep()

  function nextStep(): void {
    try {
      if (!canAdvance.value) return
      if (currentStep.value < totalSteps) {
        currentStep.value++
      }
      clampCurrentStep()
    } catch (e: any) {
      onError(`nextStep 失败: ${String(e?.message || e)}`)
      clampCurrentStep()
    }
  }

  function prevStep(): void {
    try {
      // 回退不校验,自由退
      if (currentStep.value > 1) currentStep.value--
      clampCurrentStep()
    } catch (e: any) {
      onError(`prevStep 失败: ${String(e?.message || e)}`)
      clampCurrentStep()
    }
  }

  function goToStep(step: number): void {
    try {
      // 倒退随意;前进必须当前步无 error。允许跳多步,但只校验 canAdvance 当前值
      // (严谨版可以逐步 validate,实际场景跳多步用例少,先简单化 — 跟原 InitPage 行为一致)
      if (step < currentStep.value) {
        currentStep.value = step
      } else if (step > currentStep.value && canAdvance.value) {
        currentStep.value = step
      }
      clampCurrentStep()
    } catch (e: any) {
      onError(`goToStep(${step}) 失败: ${String(e?.message || e)}`)
      clampCurrentStep()
    }
  }

  return { clampCurrentStep, nextStep, prevStep, goToStep }
}

/** migrateSavedStep 把"老 draft 里的 currentStep"按 wizardSchema 迁移到当前 schema。
 *  schema=1(老)的 step N 对应 schema=2(新,加了欢迎页)的 step N+1。
 *  没存过 / 字段缺失 → 返回 1(从欢迎页开始)。
 *
 *  抽成 pure function 是为了 InitPage 拼 initialStep 时跟本 composable 用同一份逻辑,
 *  也方便单测覆盖各种 schema 漂移。
 */
export function migrateSavedStep(
  savedStep: number | null | undefined,
  savedSchema: number | null | undefined,
  totalSteps: number,
): number {
  if (savedStep == null || typeof savedStep !== 'number' || isNaN(savedStep)) {
    return 1
  }
  const schema = savedSchema ?? 1
  const migrated = schema >= 2 ? savedStep : savedStep + 1
  return Math.min(Math.max(migrated, 1), totalSteps)
}
