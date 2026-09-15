import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

describe('shared select controls', () => {
  const source = readFileSync('src/assets/design.css', 'utf8')
  const selectRule = [...source.matchAll(/(?:^|\n)select \{([^}]*)\}/g)]
    .map(match => match[1] || '')
    .find(rule => rule.includes('appearance: none;')) || ''
  const sharedChevronRule = source.match(/select:not\(\.frontend-kind-select\) \{([^}]*)\}/)?.[1] || ''

  it('uses one accessible native-select shell across the wizard', () => {
    expect(selectRule).toContain('min-height: 40px;')
    expect(selectRule).toContain('padding: 9px 40px 9px 12px;')
    expect(selectRule).toContain('appearance: none;')
    expect(selectRule).toContain('-webkit-appearance: none;')
    expect(selectRule).toContain('cursor: pointer;')
    expect(source).toContain('select:hover:not(:disabled)')
    expect(source).toContain('select:focus-visible')
    expect(source).toContain('select:disabled')
  })

  it('draws a consistent chevron without duplicating the frontend-entry icon', () => {
    expect(sharedChevronRule).toContain('background-image: url(')
    expect(sharedChevronRule).toContain('background-position: right 12px center;')
    expect(sharedChevronRule).toContain('background-size: 16px 16px;')
  })

  it('keeps touch targets large and respects reduced-motion preferences', () => {
    const mobileRule = source.match(/@media \(max-width: 760px\) \{\s*select \{([^}]*)\}/)?.[1] || ''
    const reducedMotionRule = source.match(/@media \(prefers-reduced-motion: reduce\) \{\s*select \{([^}]*)\}/)?.[1] || ''

    expect(mobileRule).toContain('min-height: 44px;')
    expect(mobileRule).toContain('font-size: 16px;')
    expect(reducedMotionRule).toContain('transition: none;')
  })
})
