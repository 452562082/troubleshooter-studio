import { describe, expect, it, vi } from 'vitest'
import type { IncidentCase, IncidentCaseDetail, IncidentCaseEventPayload } from './bridge/bugWorkflow'
import { activeCaseForBug, botKeyForLegacyContinuation, casesForBug, continuationForDetail, createIncidentCaseController, terminalCaseStatuses } from './useIncidentCase'

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
  it('orders only the requested Bug Cases by newest update with a stable ID tie break', () => {
    const cases = [
      { ...detail(1, 'case-z').case, bug_id: 'bug-a', updated_at: '2026-07-12T12:00:00Z' },
      { ...detail(1, 'case-other').case, bug_id: 'bug-b', updated_at: '2026-07-13T12:00:00Z' },
      { ...detail(1, 'case-a').case, bug_id: 'bug-a', updated_at: '2026-07-12T12:00:00Z' },
      { ...detail(1, 'case-new').case, bug_id: 'bug-a', updated_at: '2026-07-13T12:00:00Z' },
    ] as IncidentCase[]

    expect(casesForBug(cases, 'bug-a').map(item => item.id)).toEqual(['case-new', 'case-a', 'case-z'])
  })

  it('selects the newest non-terminal Case and treats only durable terminal statuses as terminal', () => {
    const cases = [
      { ...detail(1, 'case-fixed').case, bug_id: 'bug-a', status: 'fixed_verified', updated_at: '2026-07-13T12:00:00Z' },
      { ...detail(1, 'case-active-old').case, bug_id: 'bug-a', status: 'waiting_evidence', updated_at: '2026-07-11T12:00:00Z' },
      { ...detail(1, 'case-active-new').case, bug_id: 'bug-a', status: 'investigating', updated_at: '2026-07-12T12:00:00Z' },
    ] as IncidentCase[]

    expect([...terminalCaseStatuses]).toEqual(['fixed_verified', 'legacy_archived', 'reset_archived'])
    expect(activeCaseForBug(cases, 'bug-a')?.id).toBe('case-active-new')
    expect(activeCaseForBug(cases.filter(item => terminalCaseStatuses.has(item.status)), 'bug-a')).toBeUndefined()
  })

  it('accepts a newer snapshot and ignores an older out-of-order event', () => {
    const controller = createIncidentCaseController()
    controller.applySnapshot(detail(6))

    controller.acceptEvent(event(7))
    controller.acceptEvent(event(5))

    expect(controller.detail.value?.case.version).toBe(7)
  })

  it('applies a returned Case state before a detail refresh is available', () => {
    const controller = createIncidentCaseController()
    const blocked = detail(7)
    blocked.case.status = 'waiting_evidence'
    controller.applySnapshot(blocked)

    controller.applyCase({ ...blocked.case, status: 'validating', current_attempt_id: 'attempt-2', version: 8 })

    expect(controller.detail.value?.case).toMatchObject({ status: 'validating', current_attempt_id: 'attempt-2', version: 8 })
    expect(controller.cases.value[0]).toMatchObject({ status: 'validating', current_attempt_id: 'attempt-2', version: 8 })
  })

  it('authoritatively replaces an equal-version partial Case shell with full detail', () => {
    const controller = createIncidentCaseController()
    const blocked = detail(7)
    blocked.case.status = 'waiting_evidence'
    blocked.attempts = [{ id: 'attempt-old', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '', error_code: 'browser_login_required', error_message: '', usage: {} }]
    controller.applySnapshot(blocked)
    controller.applyCase({ ...blocked.case, status: 'validating', current_attempt_id: 'attempt-2', version: 8 })
    const authoritative = detail(8)
    authoritative.case.current_attempt_id = 'attempt-2'
    authoritative.attempts = [{ ...blocked.attempts[0], id: 'attempt-2', status: 'running', error_code: '' }]
    authoritative.artifacts = [{ id: 'evidence-v8', case_id: 'case-1', attempt_id: 'attempt-2', kind: 'log', path_or_reference: 'opaque', sha256: 'a', captured_at: '', environment: 'test', version: '8', request_id: '', trace_id: '', redaction_status: 'redacted' }]

    expect(controller.applyAuthoritativeDetail(authoritative)).toBe(true)

    expect(controller.detail.value?.attempts.map(item => item.id)).toEqual(['attempt-2'])
    expect(controller.detail.value?.artifacts.map(item => item.id)).toEqual(['evidence-v8'])
  })

  it('rejects older and unselected authoritative detail', () => {
    const controller = createIncidentCaseController()
    const current = detail(8)
    current.artifacts = [{ id: 'current', case_id: 'case-1', attempt_id: 'attempt-1', kind: 'log', path_or_reference: 'opaque', sha256: 'a', captured_at: '', environment: 'test', version: '8', request_id: '', trace_id: '', redaction_status: 'redacted' }]
    controller.applySnapshot(current)

    expect(controller.applyAuthoritativeDetail(detail(7))).toBe(false)
    expect(controller.applyAuthoritativeDetail(detail(9, 'case-other'))).toBe(false)
    expect(controller.detail.value?.case).toMatchObject({ id: 'case-1', version: 8 })
    expect(controller.detail.value?.artifacts.map(item => item.id)).toEqual(['current'])
  })

  it('retains live progress for an authoritative equal-version running attempt and clears it when the full detail changes attempt or stops running', () => {
    const controller = createIncidentCaseController()
    const running = detail(8)
    running.case.current_attempt_id = 'attempt-2'
    controller.applySnapshot(running)
    controller.acceptEvent({
      kind: 'snapshot', case: running.case, snapshot: running,
      phase_event: { type: 'browser_progress', meta: { case_id: 'case-1', attempt_id: 'attempt-2', browser_code: 'browser_starting' } },
    })

    expect(controller.applyAuthoritativeDetail({ ...running, attempts: [{ id: 'attempt-2', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'running', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }] })).toBe(true)
    expect(controller.phaseEvents.value['attempt-2']).toHaveLength(1)

    const nextAttempt = detail(8)
    nextAttempt.case.current_attempt_id = 'attempt-3'
    expect(controller.applyAuthoritativeDetail(nextAttempt)).toBe(true)
    expect(controller.phaseEvents.value).toEqual({})

    controller.acceptEvent({
      kind: 'snapshot', case: nextAttempt.case, snapshot: nextAttempt,
      phase_event: { type: 'browser_progress', meta: { case_id: 'case-1', attempt_id: 'attempt-3', browser_code: 'browser_starting' } },
    })
    expect(controller.phaseEvents.value['attempt-3']).toHaveLength(1)
    const stopped = detail(8)
    stopped.case.current_attempt_id = 'attempt-3'
    stopped.case.status = 'waiting_evidence'
    expect(controller.applyAuthoritativeDetail(stopped)).toBe(true)
    expect(controller.phaseEvents.value).toEqual({})
  })

  it('uses authoritative detail application for an equal-version refresh', async () => {
    const blocked = detail(7)
    blocked.case.status = 'waiting_evidence'
    const full = detail(8)
    full.case.current_attempt_id = 'attempt-2'
    full.artifacts = [{ id: 'fresh', case_id: 'case-1', attempt_id: 'attempt-2', kind: 'log', path_or_reference: 'opaque', sha256: 'a', captured_at: '', environment: 'test', version: '8', request_id: '', trace_id: '', redaction_status: 'redacted' }]
    const controller = createIncidentCaseController({ getCase: vi.fn().mockResolvedValue(full) })
    controller.applySnapshot(blocked)
    controller.applyCase({ ...blocked.case, status: 'validating', current_attempt_id: 'attempt-2', version: 8 })

    await controller.refreshDetail('case-1')

    expect(controller.detail.value?.artifacts.map(item => item.id)).toEqual(['fresh'])
  })

  it('retains browser progress even when the Case snapshot version is unchanged', () => {
    const controller = createIncidentCaseController()
    const snapshot = detail(3)
    snapshot.attempts = [{ id: 'attempt-1', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'running', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }]
    controller.applySnapshot(snapshot)

    controller.acceptEvent({
      kind: 'snapshot',
      case: snapshot.case,
      snapshot,
      phase_event: {
        at: '2026-07-15T10:00:02Z',
        type: 'browser_progress',
        message: 'Cookie: sid=secret /Users/alice/private/trace.zip',
        raw: { Authorization: 'Bearer secret', password: 'hunter2', storageState: 'secret' },
        meta: { case_id: 'case-1', attempt_id: 'attempt-1', browser_code: 'browser_action_started', action_id: 'password=hunter2', current: 2, total: 4 },
      },
    })

    expect(controller.phaseEvents.value['attempt-1']).toHaveLength(1)
    expect(controller.phaseEvents.value['attempt-1'][0]).toEqual({
      type: 'browser_progress',
      meta: { case_id: 'case-1', attempt_id: 'attempt-1', browser_code: 'browser_action_started', current: 2, total: 4 },
    })
    expect(JSON.stringify(controller.phaseEvents.value)).not.toMatch(/Cookie|Authorization|password|storageState|private/)
    expect(controller.detail.value?.case.version).toBe(3)
  })

  it('deduplicates browser progress identity and caps each attempt at the newest 100 events', () => {
    const controller = createIncidentCaseController()
    const snapshot = detail(4)
    snapshot.attempts = [{ id: 'attempt-1', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'running', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }]
    controller.applySnapshot(snapshot)

    for (let index = 0; index < 101; index++) {
      controller.acceptEvent({
        kind: 'snapshot',
        case: snapshot.case,
        snapshot,
        phase_event: {
          at: `2026-07-15T10:${String(index).padStart(2, '0')}:00Z`,
          type: 'browser_progress',
          message: `执行 ${index}`,
          meta: { case_id: 'case-1', attempt_id: 'attempt-1', browser_code: 'browser_action_started', action_id: `action-${index}`, current: index, total: 100 },
        },
      })
    }
    controller.acceptEvent({
      kind: 'snapshot',
      case: snapshot.case,
      snapshot,
      phase_event: {
        at: '2026-07-15T10:100:00Z',
        type: 'browser_progress',
        message: '执行 100',
        meta: { case_id: 'case-1', attempt_id: 'attempt-1', browser_code: 'browser_action_started', action_id: 'action-100', current: 100, total: 100 },
      },
    })

    expect(controller.phaseEvents.value['attempt-1']).toHaveLength(100)
    expect(controller.phaseEvents.value['attempt-1'][0].meta.current).toBe(1)
    expect(controller.phaseEvents.value['attempt-1'][99].meta.current).toBe(100)
  })

  it('clears stale browser progress for a new current attempt and when the Case stops running', () => {
    const controller = createIncidentCaseController()
    const first = detail(5)
    first.attempts = [{ id: 'attempt-1', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'running', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }]
    controller.applySnapshot(first)
    controller.acceptEvent({ kind: 'snapshot', case: first.case, snapshot: first, phase_event: { type: 'browser_progress', message: '准备验证浏览器', meta: { case_id: 'case-1', attempt_id: 'attempt-1', browser_code: 'browser_starting' } } })
    expect(controller.phaseEvents.value['attempt-1']).toHaveLength(1)

    const second = detail(6)
    second.case.current_attempt_id = 'attempt-2'
    second.attempts = [{ ...first.attempts[0], id: 'attempt-2' }]
    controller.acceptEvent({ kind: 'snapshot', case: second.case, snapshot: second, phase_event: { type: 'browser_progress', message: '执行 1/2：打开页面', meta: { case_id: 'case-1', attempt_id: 'attempt-2', browser_code: 'browser_action_started', action_id: 'goto', current: 1, total: 2 } } })
    expect(controller.phaseEvents.value['attempt-1']).toBeUndefined()
    expect(controller.phaseEvents.value['attempt-2']).toBeUndefined()
    controller.acceptEvent({ kind: 'snapshot', case: second.case, snapshot: second, phase_event: { type: 'browser_progress', message: '执行 1/2：打开页面', meta: { case_id: 'case-1', attempt_id: 'attempt-2', browser_code: 'browser_action_started', action_id: 'goto', current: 1, total: 2 } } })
    expect(controller.phaseEvents.value['attempt-2']).toHaveLength(1)

    const stopped = detail(7)
    stopped.case.status = 'waiting_evidence'
    stopped.case.current_attempt_id = 'attempt-2'
    stopped.attempts = [{ ...second.attempts[0], status: 'failed', error_code: 'browser_locator_failed' }]
    controller.acceptEvent({ kind: 'snapshot', case: stopped.case, snapshot: stopped })
    expect(controller.phaseEvents.value).toEqual({})
  })

  it('does not let an unselected Case event clear the selected Case browser progress', () => {
    const controller = createIncidentCaseController()
    const selected = detail(5, 'case-selected')
    controller.selectedCaseID.value = selected.case.id
    controller.applySnapshot(selected)
    controller.acceptEvent({
      kind: 'snapshot',
      case: selected.case,
      snapshot: selected,
      phase_event: { type: 'browser_progress', message: '准备验证浏览器', meta: { case_id: selected.case.id, attempt_id: 'attempt-1', browser_code: 'browser_starting' } },
    })

    const background = detail(8, 'case-background')
    background.case.current_attempt_id = 'attempt-background'
    controller.acceptEvent({
      kind: 'snapshot',
      case: background.case,
      snapshot: background,
      phase_event: { type: 'browser_progress', message: '后台 Case 进度', meta: { case_id: background.case.id, attempt_id: 'attempt-background', browser_code: 'browser_starting' } },
    })

    expect(controller.phaseEvents.value['attempt-1']?.map(item => item.meta.browser_code)).toEqual(['browser_starting'])
    expect(controller.phaseEvents.value['attempt-background']).toBeUndefined()
  })

  it('does not let an older snapshot clear newer-attempt browser progress', () => {
    const controller = createIncidentCaseController()
    const current = detail(7)
    current.case.current_attempt_id = 'attempt-2'
    controller.selectedCaseID.value = current.case.id
    controller.applySnapshot(current)
    controller.acceptEvent({
      kind: 'snapshot',
      case: current.case,
      snapshot: current,
      phase_event: { type: 'browser_progress', message: '执行 2/4：切换用户页', meta: { case_id: 'case-1', attempt_id: 'attempt-2', browser_code: 'browser_action_started', action_id: 'open-users', current: 2, total: 4 } },
    })

    const stale = detail(6)
    stale.case.status = 'waiting_evidence'
    controller.acceptEvent({ kind: 'snapshot', case: stale.case, snapshot: stale })

    expect(controller.detail.value?.case.version).toBe(7)
    expect(controller.phaseEvents.value['attempt-2']?.map(item => item.meta.current)).toEqual([2])
  })

  it('appends eligible progress before rejecting an older attached snapshot', () => {
    const controller = createIncidentCaseController()
    const current = detail(7)
    current.case.current_attempt_id = 'attempt-2'
    controller.selectedCaseID.value = current.case.id
    controller.applySnapshot(current)
    const stale = detail(6)
    stale.case.current_attempt_id = 'attempt-2'

    controller.acceptEvent({
      kind: 'snapshot',
      case: stale.case,
      snapshot: stale,
      phase_event: {
        type: 'browser_progress',
        message: 'Authorization: Bearer secret',
        meta: { case_id: 'case-1', attempt_id: 'attempt-2', browser_code: 'browser_action_completed', action_id: '/private/action', current: 2, total: 4 },
      },
    })

    expect(controller.detail.value?.case.version).toBe(7)
    expect(controller.phaseEvents.value['attempt-2']).toEqual([{
      type: 'browser_progress',
      meta: { case_id: 'case-1', attempt_id: 'attempt-2', browser_code: 'browser_action_completed', current: 2, total: 4 },
    }])
  })

  it('drops unknown, non-browser, mismatched and non-running progress events', () => {
    const controller = createIncidentCaseController()
    const current = detail(7)
    controller.selectedCaseID.value = current.case.id
    controller.applySnapshot(current)
    const rejected = [
      { type: 'agent_text', message: 'Cookie: secret', meta: { case_id: 'case-1', attempt_id: 'attempt-1', browser_code: 'browser_starting' } },
      { type: 'browser_progress', message: 'password=hunter2', meta: { case_id: 'case-1', attempt_id: 'attempt-1', browser_code: 'browser_password_hunter2' } },
      { type: 'browser_progress', message: 'event type is not a progress code', meta: { case_id: 'case-1', attempt_id: 'attempt-1', browser_code: 'browser_progress' } },
      { type: 'browser_progress', message: 'storageState secret', meta: { case_id: 'case-other', attempt_id: 'attempt-1', browser_code: 'browser_starting' } },
      { type: 'browser_progress', message: '/private/old', meta: { case_id: 'case-1', attempt_id: 'attempt-old', browser_code: 'browser_starting' } },
    ]
    for (const phase_event of rejected) controller.acceptEvent({ kind: 'snapshot', case: current.case, snapshot: current, phase_event })
    expect(controller.phaseEvents.value).toEqual({})

    const stopped = detail(8)
    stopped.case.status = 'waiting_evidence'
    controller.applySnapshot(stopped)
    controller.acceptEvent({ kind: 'snapshot', case: stopped.case, snapshot: stopped, phase_event: { type: 'browser_progress', message: 'raw', meta: { case_id: 'case-1', attempt_id: 'attempt-1', browser_code: 'browser_starting' } } })
    expect(controller.phaseEvents.value).toEqual({})
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
