import { describe, expect, it, vi } from 'vitest'
import type { IncidentCaseDetail, IncidentCaseEventPayload } from './bridge/bugWorkflow'
import { createIncidentCaseController } from './useIncidentCase'

function detail(version: number): IncidentCaseDetail {
  return {
    case: { id: 'case-1', bug_id: 'bug-1', source: 'zentao', system_id: 'base', environment: 'test', status: 'validating', cycle_number: 1, current_attempt_id: 'attempt-1', selected_bot_key: 'base|codex', version, created_at: '', updated_at: '' },
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
})
