import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  approveIncidentFix,
  approveIncidentMerge,
  ackIncidentWorkflowReminder,
  cancelIncidentAttempt,
  continueIncidentCase,
  getIncidentCase,
  listIncidentCases,
  listPendingIncidentWorkflowReminders,
  notifyIncidentDeployed,
  normalizeIncidentCaseEvent,
  resetIncidentCase,
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
    await expect(listPendingIncidentWorkflowReminders()).resolves.toEqual([])
    await expect(ackIncidentWorkflowReminder({ case_id: 'case-1', reservation_key: 'slot-1', delivery_attempt: 1, actor_id: 'desktop-root' })).rejects.toThrow(/桌面 app/)
  })

  it('forwards durable reminder pull and acknowledgement', async () => {
    const reminder = { case_id: 'case-1', reservation_key: 'slot-1', delivery_attempt: 1 }
    const list = vi.fn().mockResolvedValue([reminder])
    const ack = vi.fn().mockResolvedValue(undefined)
    ;(window as any).go = { main: { App: { ListPendingIncidentWorkflowReminders: list, AckIncidentWorkflowReminder: ack } } }
    await expect(listPendingIncidentWorkflowReminders()).resolves.toEqual([reminder])
    const input = { ...reminder, actor_id: 'desktop-root' }
    await ackIncidentWorkflowReminder(input)
    expect(ack).toHaveBeenCalledWith(input)
  })

  it('rejects every mutation in browser preview with a desktop-only error', async () => {
    const base = { case_id: 'case-1', expected_version: 1, idempotency_key: 'command', actor_id: 'user' }
    await expect(startIncidentCase(base)).rejects.toThrow(/桌面 app/)
    await expect(continueIncidentCase({ ...base, phase: 'validation' })).rejects.toThrow(/桌面 app/)
    await expect(approveIncidentFix({ ...base, root_cause_attempt_id: 'attempt-1' })).rejects.toThrow(/桌面 app/)
    await expect(approveIncidentMerge({ ...base, fix_commits: { api: 'abc' }, target_branches: { api: 'test' } })).rejects.toThrow(/桌面 app/)
    await expect(notifyIncidentDeployed({ ...base, observed_version: 'build-1' })).rejects.toThrow(/桌面 app/)
    await expect(cancelIncidentAttempt({ ...base, attempt_id: 'attempt-1' })).rejects.toThrow(/桌面 app/)
    await expect(resetIncidentCase({ ...base, new_case_id: 'case-2', bot_key: 'base|codex' })).rejects.toThrow(/桌面 app/)
  })

  it('forwards mutation inputs without coercing expected_version', async () => {
    const start = vi.fn().mockResolvedValue({ id: 'case-1', status: 'validating', version: 8 })
    ;(window as any).go = { main: { App: { StartIncidentCase: start } } }
    const input = { case_id: 'case-1', expected_version: 7, idempotency_key: 'start-7', actor_id: 'user' }

    const result = await startIncidentCase(input)

    expect(start).toHaveBeenCalledWith(input)
    expect(result.version).toBe(8)
  })

  it('forwards resetIncidentCase to Wails', async () => {
    const reset = vi.fn().mockResolvedValue({ id: 'case-2', status: 'validating', version: 2, reset_from_case_id: 'case-1' })
    ;(window as any).go = { main: { App: { ResetIncidentCase: reset } } }
    const input = { case_id: 'case-1', new_case_id: 'case-2', expected_version: 7, idempotency_key: 'reset:case-1:v7', actor_id: 'desktop-user', bot_key: 'base|codex' }

    const result = await resetIncidentCase(input)

    expect(reset).toHaveBeenCalledWith(input)
    expect(result.reset_from_case_id).toBe('case-1')
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
