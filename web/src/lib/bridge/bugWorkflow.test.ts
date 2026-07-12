import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  approveIncidentFix,
  approveIncidentMerge,
  cancelIncidentAttempt,
  continueIncidentCase,
  getIncidentCase,
  listIncidentCases,
  notifyIncidentDeployed,
  normalizeIncidentCaseEvent,
  startIncidentCase,
} from './bugWorkflow'

afterEach(() => {
  vi.restoreAllMocks()
  delete (window as any).go
})

describe('incident workflow bridge', () => {
  it('normalizes nullable collections while preserving numeric versions', async () => {
    const list = vi.fn().mockResolvedValue([{ id: 'case-1', status: 'validating', version: 7 }])
    const get = vi.fn().mockResolvedValue({
      case: { id: 'case-1', status: 'validating', version: 7 },
      attempts: null,
      artifacts: null,
      approvals: null,
      code_changes: null,
      deployment_observations: null,
      events: null,
    })
    ;(window as any).go = { main: { App: { ListIncidentCases: list, GetIncidentCase: get } } }

    const cases = await listIncidentCases()
    const detail = await getIncidentCase('case-1')

    expect(cases[0].version).toBe(7)
    expect(detail.case.version).toBe(7)
    expect(detail.attempts).toEqual([])
    expect(detail.artifacts).toEqual([])
    expect(detail.approvals).toEqual([])
    expect(detail.code_changes).toEqual([])
    expect(detail.deployment_observations).toEqual([])
    expect(detail.events).toEqual([])
  })

  it('returns an empty list in browser preview', async () => {
    await expect(listIncidentCases()).resolves.toEqual([])
  })

  it('rejects every mutation in browser preview with a desktop-only error', async () => {
    const base = { case_id: 'case-1', expected_version: 1, idempotency_key: 'command', actor_id: 'user' }
    await expect(startIncidentCase(base)).rejects.toThrow(/桌面 app/)
    await expect(continueIncidentCase({ ...base, phase: 'validation' })).rejects.toThrow(/桌面 app/)
    await expect(approveIncidentFix({ ...base, root_cause_attempt_id: 'attempt-1' })).rejects.toThrow(/桌面 app/)
    await expect(approveIncidentMerge({ ...base, fix_commits: { api: 'abc' }, target_branches: { api: 'test' } })).rejects.toThrow(/桌面 app/)
    await expect(notifyIncidentDeployed({ ...base, observed_version: 'build-1' })).rejects.toThrow(/桌面 app/)
    await expect(cancelIncidentAttempt({ ...base, attempt_id: 'attempt-1' })).rejects.toThrow(/桌面 app/)
  })

  it('forwards mutation inputs without coercing expected_version', async () => {
    const start = vi.fn().mockResolvedValue({ id: 'case-1', status: 'validating', version: 8 })
    ;(window as any).go = { main: { App: { StartIncidentCase: start } } }
    const input = { case_id: 'case-1', expected_version: 7, idempotency_key: 'start-7', actor_id: 'user' }

    const result = await startIncidentCase(input)

    expect(start).toHaveBeenCalledWith(input)
    expect(result.version).toBe(8)
  })

  it('normalizes a precise versioned incident-case event payload', () => {
    const event = normalizeIncidentCaseEvent({
      kind: 'snapshot',
      case: { id: 'case-1', status: 'validating', version: 9 },
      snapshot: { case: { id: 'case-1', status: 'validating', version: 9 }, attempts: null, artifacts: null, approvals: null, code_changes: null, deployment_observations: null, events: null },
      phase_event: { type: 'agent_message', meta: null },
    })
    expect(event.kind).toBe('snapshot')
    if (event.kind !== 'snapshot') throw new Error('unexpected event kind')
    expect(event.case.version).toBe(9)
    expect(event.snapshot.attempts).toEqual([])
    expect(event.phase_event?.meta).toEqual({})
  })

  it('normalizes startup errors without inventing a Case snapshot', () => {
    expect(normalizeIncidentCaseEvent({ kind: 'startup_error', error: { message: 'db unavailable', retryable: true } })).toEqual({
      kind: 'startup_error',
      error: { message: 'db unavailable', retryable: true },
    })
  })
})
