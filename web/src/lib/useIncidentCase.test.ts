import { describe, expect, it, vi } from 'vitest'
import type { IncidentCaseDetail, IncidentCaseEventPayload } from './bridge/bugWorkflow'
import { botKeyForLegacyContinuation, continuationForDetail, createIncidentCaseController } from './useIncidentCase'

function detail(version: number, id = 'case-1'): IncidentCaseDetail {
  return {
    case: { id, bug_id: `bug-${id}`, source: 'zentao', system_id: 'base', environment: 'test', status: 'validating', cycle_number: 1, current_attempt_id: 'attempt-1', selected_bot_key: 'base|codex', version, created_at: '', updated_at: '' },
    attempts: [], artifacts: [], approvals: [], code_changes: [], deployment_observations: [], events: [],
  }
}

function event(version: number): IncidentCaseEventPayload {
  return { kind: 'snapshot', case: detail(version).case, snapshot: detail(version) }
}

describe('incident Case controller', () => {
  it('accepts a newer snapshot and ignores an older out-of-order event', () => {
    const controller = createIncidentCaseController()
    controller.applySnapshot(detail(6))

    controller.acceptEvent(event(7))
    controller.acceptEvent(event(5))

    expect(controller.detail.value?.case.version).toBe(7)
  })

  it.each([
    ['validation', 'reproduce'], ['investigation', undefined], ['fix', undefined], ['regression', 'regression'],
  ] as const)('continues waiting evidence from the exact latest %s attempt', (phase, expectedMode) => {
    const snapshot = detail(4)
    snapshot.case.status = 'waiting_evidence'
    snapshot.case.current_attempt_id = 'attempt-latest'
    snapshot.attempts = [{ id: 'attempt-latest', case_id: 'case-1', cycle_number: 1, phase, mode: expectedMode || '', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: { scenario: 'checkout', mode: expectedMode }, output_json: {}, parent_attempt_id: '', started_at: '', error_code: 'needs_evidence', error_message: '', usage: {} }]
    const continuation = continuationForDetail(snapshot, 'new evidence')
    expect(continuation.phase).toBe(phase)
    expect(continuation.input_json).toMatchObject({ scenario: 'checkout', user_input: 'new evidence' })
    if (expectedMode) expect(continuation.input_json.mode).toBe(expectedMode)
  })

  it('rejects waiting evidence without a runnable latest attempt instead of falling back', () => {
    const snapshot = detail(4)
    snapshot.case.status = 'waiting_evidence'
    expect(() => continuationForDetail(snapshot, 'evidence')).toThrow(/阶段/)
  })

  it('recovers a migrated archive bot from its latest legacy attempt and otherwise requires explicit reselection', () => {
    const snapshot = detail(1)
    snapshot.case.status = 'legacy_archived'
    snapshot.case.selected_bot_key = ''
    snapshot.attempts = [
      { id: 'old', case_id: 'case-1', cycle_number: 1, phase: 'legacy', mode: '', status: 'succeeded', agent_target: '', bot_key: 'old|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '2026-01-01', error_code: '', error_message: '', usage: {} },
      { id: 'new', case_id: 'case-1', cycle_number: 1, phase: 'legacy', mode: '', status: 'succeeded', agent_target: '', bot_key: 'new|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '2026-02-01', error_code: '', error_message: '', usage: {} },
    ]
    expect(botKeyForLegacyContinuation(snapshot, '', '')).toBe('new|codex')
    snapshot.attempts = []
    expect(botKeyForLegacyContinuation(snapshot, 'other-bug', 'picked|codex')).toBe('')
    expect(botKeyForLegacyContinuation(snapshot, snapshot.case.bug_id, 'picked|codex')).toBe('picked|codex')
  })

  it('retains the current snapshot when refresh fails', async () => {
    const controller = createIncidentCaseController({ getCase: vi.fn().mockRejectedValue(new Error('offline')) })
    controller.applySnapshot(detail(7))

    await expect(controller.refreshDetail('case-1')).rejects.toThrow('offline')

    expect(controller.detail.value?.case.version).toBe(7)
    expect(controller.error.value).toBe('offline')
  })

  it('shares one pending promise for duplicate actions', async () => {
    let resolve!: (value: string) => void
    const operation = vi.fn(() => new Promise<string>(done => { resolve = done }))
    const controller = createIncidentCaseController()

    const first = controller.runOnce('approve-fix', operation)
    const duplicate = controller.runOnce('approve-fix', operation)

    expect(duplicate).toBe(first)
    expect(operation).toHaveBeenCalledTimes(1)
    resolve('ok')
    await expect(first).resolves.toBe('ok')
  })

  it('handles startup errors without discarding an existing snapshot', () => {
    const controller = createIncidentCaseController()
    controller.applySnapshot(detail(4))

    controller.acceptEvent({ kind: 'startup_error', error: { message: 'database unavailable', retryable: true } })

    expect(controller.detail.value?.case.version).toBe(4)
    expect(controller.error.value).toBe('database unavailable')
  })

  it('merges a stale list response without downgrading a newer Case version', async () => {
    const controller = createIncidentCaseController({ listCases: vi.fn().mockResolvedValue([detail(6).case]) })
    controller.acceptEvent(event(7))
    await controller.refreshCases()
    expect(controller.cases.value[0].version).toBe(7)
  })

  it('does not let a late selection A response overwrite selected Case B', async () => {
    let resolveA!: (value: IncidentCaseDetail) => void
    let resolveB!: (value: IncidentCaseDetail) => void
    const getCase = vi.fn((id: string) => new Promise<IncidentCaseDetail>(resolve => {
      if (id === 'case-a') resolveA = resolve
      else resolveB = resolve
    }))
    const controller = createIncidentCaseController({ getCase })

    const pendingA = controller.selectCase('case-a')
    const pendingB = controller.selectCase('case-b')
    resolveB(detail(2, 'case-b'))
    await pendingB
    resolveA(detail(9, 'case-a'))
    await pendingA

    expect(controller.selectedCaseID.value).toBe('case-b')
    expect(controller.detail.value?.case.id).toBe('case-b')
  })

  it('does not let refresh v6 or an equal event replace event v7 detail', async () => {
    let resolveRefresh!: (value: IncidentCaseDetail) => void
    const controller = createIncidentCaseController({ getCase: vi.fn(() => new Promise<IncidentCaseDetail>(resolve => { resolveRefresh = resolve })) })
    controller.applySnapshot(detail(5))
    const refreshing = controller.refreshDetail('case-1')
    const newest = detail(7)
    newest.artifacts = [{ id: 'new', case_id: 'case-1', attempt_id: 'attempt-1', kind: 'log', path_or_reference: 'new', sha256: 'a', captured_at: '', environment: 'test', version: '7', request_id: '', trace_id: '', redaction_status: 'redacted' }]
    controller.acceptEvent({ kind: 'snapshot', case: newest.case, snapshot: newest })
    controller.acceptEvent(event(7))
    resolveRefresh(detail(6))
    await refreshing

    expect(controller.detail.value?.case.version).toBe(7)
    expect(controller.detail.value?.artifacts[0]?.id).toBe('new')
  })
})
