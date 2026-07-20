import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  approveIncidentFix,
  approveIncidentMerge,
  ackIncidentWorkflowReminder,
  cancelIncidentAttempt,
  continueIncidentCase,
  clearIncidentBrowserSession,
  completeIncidentRemediation,
  getIncidentArtifactPreview,
  getIncidentCase,
  listIncidentCases,
  listPendingIncidentWorkflowReminders,
  notifyIncidentDeployed,
  normalizeIncidentCaseEvent,
  openIncidentBrowserLogin,
  repairIncidentBrowserRuntime,
  resetIncidentCase,
  resetIncidentCaseWithWarnings,
  IncidentWorkflowCommandError,
  isIncidentWorkflowConflict,
  saveIncidentArtifact,
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

  it('projects artifacts onto the path-free public contract', async () => {
    const privatePath = '/Users/alice/.troubleshooter/artifacts/case-1/private.png'
    const get = vi.fn().mockResolvedValue({
      case: { id: 'case-1', status: 'validating', version: 7 },
      attempts: [], approvals: [], code_changes: [], deployment_observations: [], events: [],
      artifacts: [{
        id: 'shot-1', case_id: 'case-1', attempt_id: 'attempt-1', kind: 'screenshot',
        path_or_reference: privatePath, sha256: 'abc', size: 8, captured_at: '2026-07-16T10:00:00Z',
        environment: 'test', version: 'build-1', request_id: 'req-1', trace_id: 'trace-1', redaction_status: 'redacted',
      }],
    })
    ;(window as any).go = { main: { App: { GetIncidentCase: get } } }

    const detail = await getIncidentCase('case-1')

    expect(detail.artifacts).toEqual([{
      id: 'shot-1', case_id: 'case-1', attempt_id: 'attempt-1', kind: 'screenshot', sha256: 'abc', size: 8,
      captured_at: '2026-07-16T10:00:00Z', environment: 'test', version: 'build-1', request_id: 'req-1', trace_id: 'trace-1',
    }])
    expect(JSON.stringify(detail)).not.toContain(privatePath)
    expect(JSON.stringify(detail)).not.toContain('path_or_reference')
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
    await expect(completeIncidentRemediation({ ...base, root_cause_attempt_id: 'attempt-1', summary: 'rolled back config', evidence: 'ticket-42' })).rejects.toThrow(/桌面 app/)
    await expect(approveIncidentMerge({ ...base, fix_commits: { api: 'abc' }, target_branches: { api: 'test' } })).rejects.toThrow(/桌面 app/)
    await expect(notifyIncidentDeployed({ ...base, observed_version: 'build-1' })).rejects.toThrow(/桌面 app/)
    await expect(cancelIncidentAttempt({ ...base, attempt_id: 'attempt-1' })).rejects.toThrow(/桌面 app/)
    await expect(resetIncidentCase({ ...base, new_case_id: 'case-2', bot_key: 'base|codex' })).rejects.toThrow(/桌面 app/)
    const browser = { ...base, attempt_id: 'attempt-1' }
    await expect(openIncidentBrowserLogin(browser)).rejects.toThrow(/桌面 app/)
    await expect(repairIncidentBrowserRuntime(browser)).rejects.toThrow(/桌面 app/)
    await expect(clearIncidentBrowserSession(browser)).rejects.toThrow(/桌面 app/)
    await expect(getIncidentArtifactPreview('case-1', 'shot-1')).rejects.toThrow(/桌面 app/)
    await expect(saveIncidentArtifact('case-1', 'shot-1')).rejects.toThrow(/桌面 app/)
  })

  it('forwards exact browser recovery inputs through the desktop bridge', async () => {
    const login = vi.fn().mockResolvedValue({ id: 'case-1', status: 'validating', version: 8 })
    const repair = vi.fn().mockResolvedValue({ id: 'case-1', status: 'validating', version: 9 })
    const clear = vi.fn().mockResolvedValue(undefined)
    ;(window as any).go = { main: { App: {
      OpenIncidentBrowserLogin: login,
      RepairIncidentBrowserRuntime: repair,
      ClearIncidentBrowserSession: clear,
    } } }
    const input = { case_id: 'case-1', attempt_id: 'attempt-1', expected_version: 7, idempotency_key: 'browser-login:case-1:attempt-1:v7', actor_id: 'desktop-user' }

    await expect(openIncidentBrowserLogin(input)).resolves.toMatchObject({ id: 'case-1', version: 8 })
    await expect(repairIncidentBrowserRuntime(input)).resolves.toMatchObject({ id: 'case-1', version: 9 })
    await expect(clearIncidentBrowserSession(input)).resolves.toBeUndefined()
    expect(login).toHaveBeenCalledWith(input)
    expect(repair).toHaveBeenCalledWith(input)
    expect(clear).toHaveBeenCalledWith(input)
  })

  it('returns only strict PNG preview data and hides save destinations behind a boolean', async () => {
    const preview = vi.fn().mockResolvedValue({ artifact_id: 'shot-1', mime_type: 'image/png', base64_data: 'iVBORw0KGgo=', size: 8 })
    const save = vi.fn().mockResolvedValue(true)
    ;(window as any).go = { main: { App: { GetIncidentArtifactPreview: preview, SaveIncidentArtifact: save } } }

    await expect(getIncidentArtifactPreview('case-1', 'shot-1')).resolves.toEqual({ artifact_id: 'shot-1', mime_type: 'image/png', base64_data: 'iVBORw0KGgo=', size: 8 })
    await expect(saveIncidentArtifact('case-1', 'shot-1')).resolves.toBe(true)
    save.mockResolvedValueOnce(false)
    await expect(saveIncidentArtifact('case-1', 'shot-1')).resolves.toBe(false)
    save.mockResolvedValueOnce('/Users/alice/Desktop/private-screenshot.png')
    await expect(saveIncidentArtifact('case-1', 'shot-1')).rejects.toThrow(/保存故障证据/)
    expect(preview).toHaveBeenCalledWith('case-1', 'shot-1')
    expect(save).toHaveBeenCalledWith('case-1', 'shot-1')
  })

  it('rejects malformed artifact preview payloads before they reach an image source', async () => {
    const preview = vi.fn().mockResolvedValue({ artifact_id: 'shot-1', mime_type: 'text/html', base64_data: 'PHNjcmlwdD4=', size: 9 })
    ;(window as any).go = { main: { App: { GetIncidentArtifactPreview: preview } } }

    await expect(getIncidentArtifactPreview('case-1', 'shot-1')).rejects.toThrow(/PNG/)
  })

  it.each([
    ['string size', { artifact_id: 'shot-1', mime_type: 'image/png', base64_data: 'iVBORw0KGgo=', size: '8' }],
    ['boolean size', { artifact_id: 'shot-1', mime_type: 'image/png', base64_data: 'iVBORw0KGgo=', size: true }],
    ['zero size', { artifact_id: 'shot-1', mime_type: 'image/png', base64_data: 'iVBORw0KGgo=', size: 0 }],
    ['oversize', { artifact_id: 'shot-1', mime_type: 'image/png', base64_data: 'iVBORw0KGgo=', size: 16 * 1024 * 1024 + 1 }],
    ['whitespace', { artifact_id: 'shot-1', mime_type: 'image/png', base64_data: 'iVBORw0K Ggo=', size: 8 }],
    ['URL alphabet', { artifact_id: 'shot-1', mime_type: 'image/png', base64_data: 'iVBORw0KGg-_', size: 8 }],
    ['bad padding', { artifact_id: 'shot-1', mime_type: 'image/png', base64_data: 'iVBORw0KGgo===', size: 8 }],
    ['invalid length', { artifact_id: 'shot-1', mime_type: 'image/png', base64_data: 'AAAAA', size: 8 }],
    ['empty data', { artifact_id: 'shot-1', mime_type: 'image/png', base64_data: '', size: 8 }],
    ['non-canonical pad bits', { artifact_id: 'shot-1', mime_type: 'image/png', base64_data: 'iVBORw0KGgp=', size: 8 }],
    ['wrong signature', { artifact_id: 'shot-1', mime_type: 'image/png', base64_data: 'bm90LXBuZw==', size: 7 }],
    ['size mismatch', { artifact_id: 'shot-1', mime_type: 'image/png', base64_data: 'iVBORw0KGgo=', size: 9 }],
  ])('rejects %s PNG preview payloads', async (_name, payload) => {
    ;(window as any).go = { main: { App: { GetIncidentArtifactPreview: vi.fn().mockResolvedValue(payload) } } }
    await expect(getIncidentArtifactPreview('case-1', 'shot-1')).rejects.toThrow(/PNG/)
  })

  it('forwards mutation inputs without coercing expected_version', async () => {
    const start = vi.fn().mockResolvedValue({ id: 'case-1', status: 'validating', version: 8 })
    ;(window as any).go = { main: { App: { StartIncidentCase: start } } }
    const input = { case_id: 'case-1', expected_version: 7, idempotency_key: 'start-7', actor_id: 'user' }

    const result = await startIncidentCase(input)

    expect(start).toHaveBeenCalledWith(input)
    expect(result.version).toBe(8)
  })

  it('forwards the exact non-code remediation audit scope to Wails', async () => {
    const complete = vi.fn().mockResolvedValue({ id: 'case-1', status: 'regression_validating', version: 9 })
    ;(window as any).go = { main: { App: { CompleteIncidentRemediation: complete } } }
    const input = { case_id: 'case-1', expected_version: 7, idempotency_key: 'complete-remediation:case-1:root-1:7', actor_id: 'user', root_cause_attempt_id: 'root-1', summary: 'rolled back config version 42', evidence: 'change ticket CFG-42' }

    const result = await completeIncidentRemediation(input)

    expect(complete).toHaveBeenCalledWith(input)
    expect(result.status).toBe('regression_validating')
    expect(result.version).toBe(9)
  })

  it('forwards resetIncidentCase to Wails', async () => {
    const reset = vi.fn().mockResolvedValue({ id: 'case-2', status: 'validating', version: 2, reset_from_case_id: 'case-1' })
    ;(window as any).go = { main: { App: { ResetIncidentCase: reset } } }
    const input = { case_id: 'case-1', new_case_id: 'case-2', expected_version: 7, idempotency_key: 'reset:case-1:v7', actor_id: 'desktop-user', bot_key: 'base|codex' }

    const result = await resetIncidentCase(input)

    expect(reset).toHaveBeenCalledWith(input)
    expect(result.reset_from_case_id).toBe('case-1')
  })

  it('normalizes structured reset warnings from the compatible Wails binding', async () => {
	const reset = vi.fn().mockResolvedValue({
	  case: { id: 'case-2', status: 'waiting_evidence', version: 3, reset_from_case_id: 'case-1' },
	  warnings: [
	    { code: 'reset_runner_cancel_failed', message: '旧阶段 Agent 未能确认停止，请人工检查其运行状态。' },
	    { code: 'reset_replacement_start_failed', message: '接替 Case 的新阶段未能启动，已保留为可恢复状态；请刷新 Case 或重试开始验证。' },
	  ],
	})
	;(window as any).go = { main: { App: { ResetIncidentCaseWithWarnings: reset } } }
	const input = { case_id: 'case-1', new_case_id: 'case-2', expected_version: 7, idempotency_key: 'reset:case-1:v7', actor_id: 'desktop-user', bot_key: 'base|codex' }

	const result = await resetIncidentCaseWithWarnings(input)

	expect(reset).toHaveBeenCalledWith(input)
	expect(result.case.reset_from_case_id).toBe('case-1')
	expect(result.warnings).toEqual([
	  { code: 'reset_runner_cancel_failed', message: '旧阶段 Agent 未能确认停止，请人工检查其运行状态。' },
	  { code: 'reset_replacement_start_failed', message: '接替 Case 的新阶段未能启动，已保留为可恢复状态；请刷新 Case 或重试开始验证。' },
	])
  })

  it.each([
	new IncidentWorkflowCommandError('case_version_conflict', 'Case 已更新'),
	new Error('workflow_conflict:case_version_conflict: incident case version conflict: expected 7, current 8'),
	new Error('workflow_conflict:idempotency_conflict: idempotency key conflicts with committed request'),
	new Error('incident case version conflict: expected 7, current 8'),
	new Error('idempotency key conflicts with committed request: reset key'),
  ])('classifies workflow conflicts without requiring one exact English error string', error => {
	expect(isIncidentWorkflowConflict(error)).toBe(true)
  })

	it.each([
	  new Error('deployment version conflict: expected current production build'),
	  new Error('documentation mentions idempotency conflict handling'),
	  new Error('prefix workflow_conflict:case_version_conflict appears later'),
	])('does not classify incidental conflict wording as a workflow sentinel', error => {
	  expect(isIncidentWorkflowConflict(error)).toBe(false)
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
