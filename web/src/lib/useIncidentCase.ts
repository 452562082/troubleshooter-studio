import { computed, onMounted, onUnmounted, ref } from 'vue'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { getIncidentCase, incidentBrowserProgressCodes, listIncidentCases, normalizeIncidentCaseEvent, type CaseStatus, type IncidentCase, type IncidentCaseDetail, type IncidentCaseEventPayload, type IncidentPhaseEvent, type Phase } from './bridge/bugWorkflow'

type Dependencies = {
  listCases?: () => Promise<IncidentCase[]>
  getCase?: (caseID: string) => Promise<IncidentCaseDetail>
  listen?: (handler: (payload: unknown) => void) => (() => void)
}

const errorMessage = (error: unknown) => error instanceof Error ? error.message : String(error)

export const terminalCaseStatuses = new Set<CaseStatus>(['fixed_verified', 'legacy_archived', 'reset_archived'])
const browserRunningCaseStatuses = new Set<CaseStatus>(['validating', 'regression_validating'])
const browserProgressCodeAllowlist = new Set<string>(incidentBrowserProgressCodes)
const maxPhaseEventsPerAttempt = 100
const maxBrowserProgressStep = 100

export function casesForBug(cases: IncidentCase[], bugID: string): IncidentCase[] {
  return cases
    .filter(item => item.bug_id === bugID)
    .sort((a, b) => b.updated_at.localeCompare(a.updated_at) || a.id.localeCompare(b.id))
}

export function activeCaseForBug(cases: IncidentCase[], bugID: string): IncidentCase | undefined {
  return casesForBug(cases, bugID).find(item => !terminalCaseStatuses.has(item.status))
}

export function continuationForDetail(detail: IncidentCaseDetail, evidence: string): { phase: Exclude<Phase, 'legacy'>; input_json: Record<string, unknown> } {
  const latest = detail.attempts.find(attempt => attempt.id === detail.case.current_attempt_id && attempt.phase !== 'legacy')
  if (!latest || !['validation', 'investigation', 'fix', 'regression'].includes(latest.phase)) {
    throw new Error('未找到可继续的最近阶段，请刷新 Case 后重试')
  }
  const phase = latest.phase as Exclude<Phase, 'legacy'>
  const input: Record<string, unknown> = { ...(latest.input_json || {}), user_input: evidence }
  if (phase === 'validation') input.mode = 'reproduce'
  if (phase === 'regression') input.mode = 'regression'
  if (phase === 'investigation' || phase === 'fix') delete input.mode
  return { phase, input_json: input }
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
    if (changedCase || changedAttempt || !browserRunningCaseStatuses.has(snapshot.case.status)) phaseEvents.value = {}
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
    return commitSnapshot(snapshot)
  }

  function applyCase(incident: IncidentCase) {
    upsertCase(incident)
    const current = detail.value
    if (!current || current.case.id !== incident.id || current.case.version > incident.version) return false
    const snapshot = { ...current, case: incident }
    reconcilePhaseEvents(snapshot)
    detail.value = snapshot
    return true
  }

  function appendPhaseEvent(payloadCaseID: string, event?: IncidentPhaseEvent) {
    const current = detail.value
    if (!current || !event || event.type !== 'browser_progress' || !browserRunningCaseStatuses.has(current.case.status)) return
    if (payloadCaseID !== current.case.id || event.meta.case_id !== current.case.id || event.meta.attempt_id !== current.case.current_attempt_id) return
    const code = typeof event.meta.browser_code === 'string' ? event.meta.browser_code : ''
    if (!browserProgressCodeAllowlist.has(code)) return
    const hasCurrent = event.meta.current !== undefined
    const hasTotal = event.meta.total !== undefined
    const validStep = (value: unknown) => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 && value <= maxBrowserProgressStep
    if (hasCurrent !== hasTotal || hasCurrent && (!validStep(event.meta.current) || !validStep(event.meta.total) || Number(event.meta.current) > Number(event.meta.total))) return
    const attemptID = current.case.current_attempt_id
    const safeEvent: IncidentPhaseEvent = {
      type: 'browser_progress',
      meta: {
        case_id: current.case.id,
        attempt_id: attemptID,
        browser_code: code,
        ...(hasCurrent ? { current: event.meta.current as number, total: event.meta.total as number } : {}),
      },
    }
    const identity = [code, String(safeEvent.meta.current ?? ''), String(safeEvent.meta.total ?? '')].join('\u001f')
    const existing = phaseEvents.value[attemptID] || []
    const duplicate = existing.some(item => [String(item.meta.browser_code ?? ''), String(item.meta.current ?? ''), String(item.meta.total ?? '')].join('\u001f') === identity)
    if (duplicate) return
    phaseEvents.value = {
      ...phaseEvents.value,
      [attemptID]: [...existing, safeEvent].slice(-maxPhaseEventsPerAttempt),
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
      commitSnapshot(payload.snapshot)
    }
  }

  async function refreshCases() {
    loading.value = true
    try {
      for (const incident of await listCases()) upsertCase(incident)
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
      if (generation === detailGeneration && selectedCaseID.value === caseID) applySnapshot(snapshot)
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

  return { cases, detail, selectedCaseID, loading, error, phaseEvents, pending: computed(() => pendingKeys.value.size > 0), applyCase, applySnapshot, acceptEvent, refreshCases, refreshDetail, selectCase, runOnce }
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
