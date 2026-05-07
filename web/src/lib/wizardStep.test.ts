// migrateSavedStep 单测:跟 InitPage 原 inline 逻辑行为严格等价的所有 case。
import { describe, expect, it } from 'vitest'
import { migrateSavedStep } from './wizardStep'

describe('migrateSavedStep', () => {
  // 等价于:savedStep != null ? Math.min(schema>=2 ? savedStep : savedStep+1, totalSteps) : 1
  // 把这个表达式的所有分支都覆盖到。

  it('savedStep 为 null → 默认 1(全新用户)', () => {
    expect(migrateSavedStep(null, undefined, 10)).toBe(1)
    expect(migrateSavedStep(undefined, 2, 10)).toBe(1)
    expect(migrateSavedStep(null, 1, 10)).toBe(1)
  })

  it('schema=2(当前)透传不偏移', () => {
    expect(migrateSavedStep(1, 2, 10)).toBe(1)
    expect(migrateSavedStep(5, 2, 10)).toBe(5)
    expect(migrateSavedStep(10, 2, 10)).toBe(10)
  })

  it('schema=1(老 draft)→ +1 偏移到新欢迎页 schema', () => {
    expect(migrateSavedStep(1, 1, 10)).toBe(2)
    expect(migrateSavedStep(5, 1, 10)).toBe(6)
    expect(migrateSavedStep(9, 1, 10)).toBe(10)
  })

  it('savedSchema 缺失视为 schema=1(向后兼容)', () => {
    expect(migrateSavedStep(5, undefined, 10)).toBe(6)
    expect(migrateSavedStep(5, null, 10)).toBe(6)
  })

  it('schema=1 偏移后超 totalSteps → 钳到 totalSteps', () => {
    expect(migrateSavedStep(10, 1, 10)).toBe(10) // 10+1=11 → min(11,10)=10
    expect(migrateSavedStep(15, 2, 10)).toBe(10) // 直接 schema=2 越界也钳
  })

  // 不做下限 clamp(故意跟原 InitPage 逻辑等价)—— 0 / 负数照样透传,
  // 让 InitPage clampCurrentStep 运行时兜底。如果哪天要改这条,得同步 verify
  // InitPage 的兜底逻辑能 cover。
  it('savedStep=0 透传 0(不做下限 clamp)', () => {
    expect(migrateSavedStep(0, 2, 10)).toBe(0)
  })
  it('savedStep 负数 透传(不做下限 clamp)', () => {
    expect(migrateSavedStep(-3, 2, 10)).toBe(-3)
  })
})
