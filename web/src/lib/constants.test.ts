import { describe, expect, it } from 'vitest'
import { IDE_TARGETS, Target, TARGETS } from './constants'

describe('target constants', () => {
  it('lists every backend-supported generation target', () => {
    expect(TARGETS).toEqual([
      Target.ClaudeCode,
      Target.Cursor,
      Target.Codex,
      Target.OpenCode,
    ])
  })

  it('supports exactly four IDE targets', () => {
    expect(IDE_TARGETS).toEqual([
      Target.ClaudeCode,
      Target.Cursor,
      Target.Codex,
      Target.OpenCode,
    ])
  })
})
