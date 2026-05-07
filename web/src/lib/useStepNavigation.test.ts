// useStepNavigation 单测:钳制 / 推进 / 回退 / 跳转 / schema migration / 错误回调。
import { computed, ref } from 'vue'
import { describe, expect, it, vi } from 'vitest'
import { migrateSavedStep, useStepNavigation } from './useStepNavigation'

// helper:常用 setup,canAdvance 默认 true
function setup(overrides: Partial<{ initialStep: number; totalSteps: number; canAdvance: boolean }> = {}) {
  const totalSteps = overrides.totalSteps ?? 10
  const currentStep = ref(overrides.initialStep ?? 1)
  const canAdvanceRef = ref(overrides.canAdvance ?? true)
  const onError = vi.fn()
  const nav = useStepNavigation({
    currentStep,
    totalSteps,
    canAdvance: computed(() => canAdvanceRef.value),
    onError,
  })
  return { nav, currentStep, canAdvanceRef, onError, totalSteps }
}

describe('useStepNavigation - clampCurrentStep', () => {
  it('initialStep=0 钳到 1', () => {
    const { currentStep } = setup({ initialStep: 0 })
    expect(currentStep.value).toBe(1)
  })

  it('initialStep=11 (>totalSteps) 钳到 10', () => {
    const { currentStep } = setup({ initialStep: 11 })
    expect(currentStep.value).toBe(10)
  })

  it('initialStep=NaN 钳到 1', () => {
    const { currentStep } = setup({ initialStep: NaN })
    expect(currentStep.value).toBe(1)
  })

  it('合法 initialStep 保留', () => {
    const { currentStep } = setup({ initialStep: 5 })
    expect(currentStep.value).toBe(5)
  })

  it('clampCurrentStep public:外部 mutate 后能手动钳', () => {
    const { nav, currentStep } = setup({ initialStep: 3 })
    currentStep.value = 99
    nav.clampCurrentStep()
    expect(currentStep.value).toBe(10)
  })
})

describe('useStepNavigation - nextStep', () => {
  it('canAdvance=true 推进 +1', () => {
    const { nav, currentStep } = setup({ initialStep: 3, canAdvance: true })
    nav.nextStep()
    expect(currentStep.value).toBe(4)
  })

  it('canAdvance=false no-op', () => {
    const { nav, currentStep } = setup({ initialStep: 3, canAdvance: false })
    nav.nextStep()
    expect(currentStep.value).toBe(3)
  })

  it('已在最后一步即使 canAdvance=true 也不超', () => {
    const { nav, currentStep } = setup({ initialStep: 10, canAdvance: true })
    nav.nextStep()
    expect(currentStep.value).toBe(10)
  })

  it('多次推进直到末步停', () => {
    const { nav, currentStep } = setup({ initialStep: 1, canAdvance: true })
    for (let i = 0; i < 20; i++) nav.nextStep()
    expect(currentStep.value).toBe(10)
  })
})

describe('useStepNavigation - prevStep', () => {
  it('正常回退 -1', () => {
    const { nav, currentStep } = setup({ initialStep: 5 })
    nav.prevStep()
    expect(currentStep.value).toBe(4)
  })

  it('canAdvance=false 也能回退(回退不校验)', () => {
    const { nav, currentStep } = setup({ initialStep: 5, canAdvance: false })
    nav.prevStep()
    expect(currentStep.value).toBe(4)
  })

  it('已在 step 1 不能再退', () => {
    const { nav, currentStep } = setup({ initialStep: 1 })
    nav.prevStep()
    expect(currentStep.value).toBe(1)
  })
})

describe('useStepNavigation - goToStep', () => {
  it('倒退随意,无视 canAdvance', () => {
    const { nav, currentStep } = setup({ initialStep: 7, canAdvance: false })
    nav.goToStep(2)
    expect(currentStep.value).toBe(2)
  })

  it('前进需 canAdvance=true', () => {
    const { nav, currentStep, canAdvanceRef } = setup({ initialStep: 2, canAdvance: false })
    nav.goToStep(8)
    expect(currentStep.value).toBe(2) // canAdvance=false 阻塞前进

    canAdvanceRef.value = true
    nav.goToStep(8)
    expect(currentStep.value).toBe(8)
  })

  it('跳到当前 step no-op', () => {
    const { nav, currentStep } = setup({ initialStep: 5 })
    nav.goToStep(5)
    expect(currentStep.value).toBe(5)
  })

  it('越界目标被 clamp', () => {
    const { nav, currentStep } = setup({ initialStep: 5, canAdvance: true })
    nav.goToStep(99)
    expect(currentStep.value).toBe(10)
    nav.goToStep(-3)
    expect(currentStep.value).toBe(1)
  })
})

describe('useStepNavigation - 错误回调', () => {
  it('canAdvance computed 抛错时,nextStep 接住 + onError 上报 + clamp 兜底', () => {
    const onError = vi.fn()
    const currentStep = ref(3)
    const nav = useStepNavigation({
      currentStep,
      totalSteps: 10,
      canAdvance: computed<boolean>(() => { throw new Error('boom') }),
      onError,
    })
    nav.nextStep()
    expect(onError).toHaveBeenCalledWith(expect.stringContaining('nextStep 失败'))
    expect(onError.mock.calls[0][0]).toContain('boom')
    expect(currentStep.value).toBe(3) // 没推进,clamp 后还在 3
  })
})

describe('migrateSavedStep', () => {
  it('schema=2 透传(不偏移)', () => {
    expect(migrateSavedStep(5, 2, 10)).toBe(5)
  })

  it('schema=1(老 draft)+1 偏移到新欢迎页 schema', () => {
    expect(migrateSavedStep(5, 1, 10)).toBe(6)
  })

  it('schema 缺失视为 1', () => {
    expect(migrateSavedStep(5, undefined, 10)).toBe(6)
    expect(migrateSavedStep(5, null, 10)).toBe(6)
  })

  it('savedStep null → 1(全新用户)', () => {
    expect(migrateSavedStep(null, undefined, 10)).toBe(1)
    expect(migrateSavedStep(undefined, 2, 10)).toBe(1)
  })

  it('迁移后越界被钳到 totalSteps', () => {
    expect(migrateSavedStep(10, 1, 10)).toBe(10) // 10+1=11 → clamp 10
  })

  it('NaN savedStep → 1', () => {
    expect(migrateSavedStep(NaN, 2, 10)).toBe(1)
  })

  it('负值 savedStep clamp 到 1', () => {
    expect(migrateSavedStep(-3, 2, 10)).toBe(1)
  })
})
