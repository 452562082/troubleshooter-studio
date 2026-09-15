import { describe, expect, it } from 'vitest'
import { JOURNEY, journeyPhase, normalizeJourneyStep, phaseSections, suggestedSystemID, resolveSystemID } from './wizardJourney'
import { migrateSavedStep } from './wizardStep'

describe('four-phase creation journey', () => {
  it('starts with repository selection and preserves old draft destinations', () => {
    expect(normalizeJourneyStep(migrateSavedStep(undefined, undefined, 10))).toBe(5)
    expect(normalizeJourneyStep(migrateSavedStep(1, 1, 10))).toBe(5)
    expect(normalizeJourneyStep(migrateSavedStep(4, 2, 10))).toBe(3)
    expect(normalizeJourneyStep(migrateSavedStep(10, 2, 10))).toBe(9)
    for (const step of [6, 7, 8]) expect(normalizeJourneyStep(step)).toBe(step)
    for (const step of [NaN, -1, 999]) expect(normalizeJourneyStep(step)).toBe(5)
  })
  it('groups validation by phase, checking every section before deployment', () => {
    expect(JOURNEY.map(item => journeyPhase(item.step))).toEqual([0, 1, 2, 3])
    expect(phaseSections(0)).toEqual([2, 5])
    expect(phaseSections(1)).toEqual([3, 4])
    expect(phaseSections(2)).toEqual([6, 7, 8])
    expect(phaseSections(3)).toEqual([2, 3, 4, 5, 6, 7, 8])
  })
  it('uses repository identity and supports Chinese names without manual IDs', () => {
    expect(suggestedSystemID('order-service', '订单')).toBe('order-service')
    const generated = suggestedSystemID('', '订单系统')
    expect(generated).toMatch(/^[a-z0-9][a-z0-9-]+$/)
    expect(generated).toBe(suggestedSystemID('', '订单系统'))
    expect(generated).not.toBe(suggestedSystemID('', '用户系统'))
  })
})

describe('automatic system identity', () => {
  it('repairs uppercase and invalid saved IDs without changing valid identities', () => {
    expect(resolveSystemID('Base', 'base-backend', 'Base')).toBe('base')
    expect(resolveSystemID(' My Project! ', '', '项目')).toBe('my-project')
    expect(resolveSystemID('existing-bot', 'new-repo', '新名称')).toBe('existing-bot')
  })
  it('generates IDs for empty and Chinese-only drafts', () => {
    expect(resolveSystemID('', 'base', 'Base')).toBe('base')
    expect(resolveSystemID('中文', '', '中文项目')).toMatch(/^project-[a-z0-9]+$/)
    expect(resolveSystemID('', '', '')).toBe('')
  })
})
