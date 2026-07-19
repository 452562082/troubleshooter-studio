import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { DATA_STORE_STEP, OBSERVABILITY_STEP, shouldInitializeObservability } from './wizardSteps'

describe('wizard step routing', () => {
  it('initializes observability on step 8 after the data-store step', () => {
    expect(DATA_STORE_STEP).toBe(7)
    expect(OBSERVABILITY_STEP).toBe(8)
    expect(shouldInitializeObservability(DATA_STORE_STEP)).toBe(false)
    expect(shouldInitializeObservability(OBSERVABILITY_STEP)).toBe(true)
  })

  it('binds InitPage auto-probes and rendering to the observability step', () => {
    const source = readFileSync('src/pages/InitPage.vue', 'utf8')
    expect(source).toContain('if (!shouldInitializeObservability(s)) return')
    expect(source).toContain('v-if="currentStep === OBSERVABILITY_STEP"')
    expect(source).not.toContain('if (s !== 7) return')
  })
})
