import { computed, onMounted, onUnmounted, ref } from 'vue'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { getIncidentCase, listIncidentCases, normalizeIncidentCaseEvent, type CaseStatus, type IncidentCase, type IncidentCaseDetail, type IncidentCaseEventPayload, type IncidentPhaseEvent } from './bridge/bugWorkflow'

type Dependencies = {
  listCases?: () => Promise<IncidentCase[]>
  getCase?: (caseID: string) => Promise<IncidentCaseDetail>
  listen?: (handler: (payload: unknown) => void) => (() => void)
}

const errorMessage = (error: unknown) => error instanceof Error ? error.message : String(error)

export const terminalCaseStatuses = new Set<CaseStatus>(['submitted', 'remediation_recorded', 'fixed_verified', 'legacy_archived', 'reset_archived'])
const phaseRunningCaseStatuses = new Set<CaseStatus>(['investigating', 'fixing'])
const agentProgressTypeAllowlist = new Set(['thread_started', 'turn_started', 'turn_completed', 'command_execution', 'mcp_tool_call', 'agent_message', 'phase_step', 'code_intelligence', 'retry', 'error', 'turn_failed', 'result'])
const maxPhaseEventsPerAttempt = 100
const maxAgentProgressMessageLength = 2000
const investigationStepKeys = ['evidence_handoff', 'timeline', 'runtime_scope', 'dependency_chain', 'evidence_correlation', 'root_cause', 'knowledge_sink'] as const
const investigationStepLabels: Record<(typeof investigationStepKeys)[number], string> = {
  evidence_handoff: '工单与证据',
  timeline: '时间轴与最近变更',
  runtime_scope: '横向运行时检查',
  dependency_chain: '依赖与调用链',
  evidence_correlation: '多维证据交叉',
  root_cause: '根因收敛',
  knowledge_sink: '沉淀与结果',
}

function capPhaseEvents(events: IncidentPhaseEvent[]): IncidentPhaseEvent[] {
  if (events.length <= maxPhaseEventsPerAttempt) return events
  const recent = events.slice(-maxPhaseEventsPerAttempt)
  const latestStep = [...events].reverse().find(event => event.type === 'phase_step')
  if (!latestStep || recent.includes(latestStep)) return recent
  return [latestStep, ...events.slice(-(maxPhaseEventsPerAttempt - 1))]
}

export function casesForBug(cases: IncidentCase[], bugID: string): IncidentCase[] {
  return cases
    .filter(item => item.bug_id === bugID)
    .sort((a, b) => b.updated_at.localeCompare(a.updated_at) || a.id.localeCompare(b.id))
}

export function activeCaseForBug(cases: IncidentCase[], bugID: string): IncidentCase | undefined {
  return casesForBug(cases, bugID).find(item => !terminalCaseStatuses.has(item.status))
}

export function continuationForDetail(detail: IncidentCaseDetail, evidence: string): { phase: 'investigation' | 'fix'; input_json: Record<string, unknown> } {
  const latest = detail.attempts.find(attempt => attempt.id === detail.case.current_attempt_id)
  if (!latest || (latest.phase !== 'investigation' && latest.phase !== 'fix')) throw new Error('该历史阶段已停用，请开启新一轮排障')
  const input = { ...(latest.input_json || {}), user_input: evidence }
  return { phase: latest.phase, input_json: input }
}

export function botKeyForLegacyContinuation(detail: IncidentCaseDetail, selectedBugID: string, selectedBotKey: string): string {
  if (detail.case.selected_bot_key.trim()) return detail.case.selected_bot_key.trim()
  const legacyBot = [...detail.attempts]
    .sort((a, b) => (b.started_at || '').localeCompare(a.started_at || '') || b.id.localeCompare(a.id))
    .find(attempt => attempt.phase === 'legacy' && attempt.bot_key.trim())?.bot_key.trim()
  if (legacyBot) return legacyBot
  return selectedBugID === detail.case.bug_id ? selectedBotKey.trim() : ''
}

export function createIncidentCaseController(dependencies: Dependencies = {}) {
  const listCases = dependencies.listCases ?? listIncidentCases
  const getCase = dependencies.getCase ?? getIncidentCase
  const cases = ref<IncidentCase[]>([])
  const detail = ref<IncidentCaseDetail | null>(null)
  const selectedCaseID = ref('')
  const loading = ref(false)
  const error = ref('')
  const phaseEvents = ref<Record<string, IncidentPhaseEvent[]>>({})
  const pendingKeys = ref(new Set<string>())
  const pendingPromises = new Map<string, Promise<unknown>>()
  let detailGeneration = 0

  function upsertCase(incoming: IncidentCase) {
    const index = cases.value.findIndex(item => item.id === incoming.id)
    if (index >= 0 && cases.value[index].version >= incoming.version) return
    const next = index >= 0 ? cases.value.map(item => item.id === incoming.id ? incoming : item) : [...cases.value, incoming]
    cases.value = next.sort((a, b) => (b.updated_at || '').localeCompare(a.updated_at || '') || b.version - a.version)
  }

  function reconcilePhaseEvents(snapshot: IncidentCaseDetail) {
    const current = detail.value
    const changedCase = Boolean(current && current.case.id !== snapshot.case.id)
    const changedAttempt = Boolean(current?.case.id === snapshot.case.id && current.case.current_attempt_id !== snapshot.case.current_attempt_id)
    if (changedCase || changedAttempt || !phaseRunningCaseStatuses.has(snapshot.case.status)) phaseEvents.value = {}
  }

  function restoreSnapshotPhaseEvents(snapshot: IncidentCaseDetail) {
    if (!phaseRunningCaseStatuses.has(snapshot.case.status)) return
    for (const event of snapshot.phase_events || []) appendPhaseEvent(snapshot.case.id, event)
  }

  function commitSnapshot(snapshot: IncidentCaseDetail) {
    const current = detail.value
    if (current?.case.id === snapshot.case.id && current.case.version >= snapshot.case.version) return false
    detail.value = snapshot
    selectedCaseID.value = snapshot.case.id
    upsertCase(snapshot.case)
    error.value = ''
    return true
  }

  function snapshotIsOlder(snapshot: IncidentCaseDetail): boolean {
    const current = detail.value
    return Boolean(current?.case.id === snapshot.case.id && current.case.version > snapshot.case.version)
  }

  function applySnapshot(snapshot: IncidentCaseDetail) {
    if (snapshotIsOlder(snapshot)) return false
    const current = detail.value
    if (current?.case.id !== snapshot.case.id || current.case.version !== snapshot.case.version) reconcilePhaseEvents(snapshot)
    const committed = commitSnapshot(snapshot)
    if (committed) restoreSnapshotPhaseEvents(snapshot)
    return committed
  }

  function applyCase(incident: IncidentCase) {
    upsertCase(incident)
    const current = detail.value
    if (!current || current.case.id !== incident.id || current.case.version > incident.version) return false
    const snapshot = { ...current, case: incident }
    reconcilePhaseEvents(snapshot)
    detail.value = snapshot
    restoreSnapshotPhaseEvents(snapshot)
    return true
  }

  function applyAuthoritativeDetail(snapshot: IncidentCaseDetail) {
    if (!selectedCaseID.value || snapshot.case.id !== selectedCaseID.value) return false
    const current = detail.value
    if (current?.case.id === snapshot.case.id && snapshot.case.version < current.case.version) return false
    reconcilePhaseEvents(snapshot)
    detail.value = snapshot
    restoreSnapshotPhaseEvents(snapshot)
    upsertCase(snapshot.case)
    error.value = ''
    return true
  }

  function appendPhaseEvent(payloadCaseID: string, event?: IncidentPhaseEvent) {
    const current = detail.value
    if (!current || !event || !phaseRunningCaseStatuses.has(current.case.status)) return
    if (payloadCaseID !== current.case.id || event.meta.case_id !== current.case.id || event.meta.attempt_id !== current.case.current_attempt_id) return
    const attemptID = current.case.current_attempt_id
    let safeEvent: IncidentPhaseEvent
    let identity: string

    if (event.type !== 'browser_progress') {
      const type = typeof event.type === 'string' ? event.type : ''
      if (!agentProgressTypeAllowlist.has(type)) return
      const message = typeof event.message === 'string' ? event.message.trim().slice(0, maxAgentProgressMessageLength) : ''
      if (!message && !['thread_started', 'turn_started', 'turn_completed'].includes(type)) return
      const meta: Record<string, unknown> = { case_id: current.case.id, attempt_id: attemptID }
      for (const key of ['cycle_number', 'phase', 'state', 'status', 'exit_code', 'step_key', 'step_index', 'step_total']) {
        const value = event.meta[key]
        if (typeof value === 'string' || typeof value === 'number') meta[key] = value
      }
      let safeMessage = message
      if (type === 'phase_step') {
        const stepIndex = meta.step_index
        const stepTotal = meta.step_total
        const stepKey = meta.step_key
        if (meta.phase !== 'investigation' || typeof stepIndex !== 'number' || !Number.isSafeInteger(stepIndex) || stepIndex < 1 || stepIndex > investigationStepKeys.length || stepTotal !== investigationStepKeys.length || stepKey !== investigationStepKeys[stepIndex - 1]) return
        safeMessage = investigationStepLabels[stepKey as (typeof investigationStepKeys)[number]]
      }
      safeEvent = {
        ...(typeof event.at === 'string' ? { at: event.at } : {}),
        type,
        ...(safeMessage ? { message: safeMessage } : {}),
        meta,
      }
      identity = [type, String(meta.state ?? ''), String(meta.step_key ?? ''), safeMessage, String(event.at ?? '')].join('\u001f')
    } else { return }
    const existing = phaseEvents.value[attemptID] || []
    const duplicate = existing.some(item => [String(item.type ?? ''), String(item.meta.state ?? ''), String(item.meta.step_key ?? ''), String(item.message ?? ''), String(item.at ?? '')].join('\u001f') === identity)
    if (duplicate) return
    phaseEvents.value = {
      ...phaseEvents.value,
      [attemptID]: capPhaseEvents([...existing, safeEvent]),
    }
  }

  function acceptEvent(payload: IncidentCaseEventPayload) {
    if (payload.kind === 'startup_error') {
      error.value = payload.error.message
      return
    }
    if (!selectedCaseID.value || selectedCaseID.value === payload.case.id) appendPhaseEvent(payload.case.id, payload.phase_event)
    upsertCase(payload.case)
    if ((!selectedCaseID.value || selectedCaseID.value === payload.case.id) && !snapshotIsOlder(payload.snapshot)) {
      const current = detail.value
      if (current?.case.id !== payload.snapshot.case.id || current.case.version !== payload.snapshot.case.version) reconcilePhaseEvents(payload.snapshot)
      if (commitSnapshot(payload.snapshot)) restoreSnapshotPhaseEvents(payload.snapshot)
    }
  }

  async function refreshCases() {
    loading.value = true
    try {
      const listed = await listCases()
      const currentByID = new Map(cases.value.map(item => [item.id, item]))
      cases.value = listed
        .map(incident => {
          const current = currentByID.get(incident.id)
          return current && current.version > incident.version ? current : incident
        })
        .sort((a, b) => (b.updated_at || '').localeCompare(a.updated_at || '') || b.version - a.version)
      if (selectedCaseID.value && !cases.value.some(item => item.id === selectedCaseID.value)) {
        selectedCaseID.value = ''
        detail.value = null
        phaseEvents.value = {}
        detailGeneration++
      }
      error.value = ''
      return cases.value
    } catch (cause) {
      error.value = errorMessage(cause)
      throw cause
    } finally { loading.value = false }
  }

  async function refreshDetail(caseID = selectedCaseID.value) {
    if (!caseID) return null
    selectedCaseID.value = caseID
    const generation = ++detailGeneration
    loading.value = true
    try {
      const snapshot = await getCase(caseID)
      if (generation === detailGeneration && selectedCaseID.value === caseID) applyAuthoritativeDetail(snapshot)
      return snapshot
    } catch (cause) {
      if (generation === detailGeneration) error.value = errorMessage(cause)
      throw cause
    } finally {
      if (generation === detailGeneration) loading.value = false
    }
  }

  async function selectCase(caseID: string) {
    return refreshDetail(caseID)
  }

  function runOnce<T>(key: string, operation: () => Promise<T>): Promise<T> {
    const existing = pendingPromises.get(key)
    if (existing) return existing as Promise<T>
    pendingKeys.value = new Set(pendingKeys.value).add(key)
    let started: Promise<T>
    try {
      started = Promise.resolve(operation())
    } catch (cause) {
      started = Promise.reject(cause)
    }
    const promise = started.finally(() => {
      pendingPromises.delete(key)
      const next = new Set(pendingKeys.value)
      next.delete(key)
      pendingKeys.value = next
    })
    pendingPromises.set(key, promise)
    return promise
  }

  return { cases, detail, selectedCaseID, loading, error, phaseEvents, pending: computed(() => pendingKeys.value.size > 0), applyAuthoritativeDetail, applyCase, applySnapshot, acceptEvent, refreshCases, refreshDetail, selectCase, runOnce }
}

export function useIncidentCase(dependencies: Dependencies = {}) {
  const controller = createIncidentCaseController(dependencies)
  let unlisten: (() => void) | undefined
  onMounted(async () => {
    const listen = dependencies.listen ?? ((handler: (payload: unknown) => void) => EventsOn('incident-case:event', handler))
    unlisten = listen(raw => controller.acceptEvent(normalizeIncidentCaseEvent(raw)))
    try {
      const items = await controller.refreshCases()
      const initial = controller.selectedCaseID.value || items[0]?.id
      if (initial) await controller.refreshDetail(initial)
    } catch { /* controller retains the error for aria-live */ }
  })
  onUnmounted(() => unlisten?.())
  return controller
}
