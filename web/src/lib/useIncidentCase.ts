import { computed, onMounted, onUnmounted, ref } from 'vue'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import { getIncidentCase, listIncidentCases, normalizeIncidentCaseEvent, type IncidentCase, type IncidentCaseDetail, type IncidentCaseEventPayload } from './bridge/bugWorkflow'

type Dependencies = {
  listCases?: () => Promise<IncidentCase[]>
  getCase?: (caseID: string) => Promise<IncidentCaseDetail>
  listen?: (handler: (payload: unknown) => void) => (() => void)
}

const errorMessage = (error: unknown) => error instanceof Error ? error.message : String(error)

export function createIncidentCaseController(dependencies: Dependencies = {}) {
  const listCases = dependencies.listCases ?? listIncidentCases
  const getCase = dependencies.getCase ?? getIncidentCase
  const cases = ref<IncidentCase[]>([])
  const detail = ref<IncidentCaseDetail | null>(null)
  const selectedCaseID = ref('')
  const loading = ref(false)
  const error = ref('')
  const pendingKeys = ref(new Set<string>())
  const pendingPromises = new Map<string, Promise<unknown>>()

  function upsertCase(incoming: IncidentCase) {
    const index = cases.value.findIndex(item => item.id === incoming.id)
    if (index >= 0 && cases.value[index].version > incoming.version) return
    const next = index >= 0 ? cases.value.map(item => item.id === incoming.id ? incoming : item) : [...cases.value, incoming]
    cases.value = next.sort((a, b) => (b.updated_at || '').localeCompare(a.updated_at || '') || b.version - a.version)
  }

  function applySnapshot(snapshot: IncidentCaseDetail) {
    const current = detail.value
    if (current?.case.id === snapshot.case.id && current.case.version > snapshot.case.version) return false
    detail.value = snapshot
    selectedCaseID.value = snapshot.case.id
    upsertCase(snapshot.case)
    error.value = ''
    return true
  }

  function acceptEvent(payload: IncidentCaseEventPayload) {
    if (payload.kind === 'startup_error') {
      error.value = payload.error.message
      return
    }
    upsertCase(payload.case)
    if (!selectedCaseID.value || selectedCaseID.value === payload.case.id) applySnapshot(payload.snapshot)
  }

  async function refreshCases() {
    loading.value = true
    try {
      cases.value = (await listCases()).slice().sort((a, b) => (b.updated_at || '').localeCompare(a.updated_at || ''))
      error.value = ''
      return cases.value
    } catch (cause) {
      error.value = errorMessage(cause)
      throw cause
    } finally { loading.value = false }
  }

  async function refreshDetail(caseID = selectedCaseID.value) {
    if (!caseID) return null
    loading.value = true
    try {
      const snapshot = await getCase(caseID)
      applySnapshot(snapshot)
      return snapshot
    } catch (cause) {
      error.value = errorMessage(cause)
      throw cause
    } finally { loading.value = false }
  }

  async function selectCase(caseID: string) {
    selectedCaseID.value = caseID
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

  return { cases, detail, selectedCaseID, loading, error, pending: computed(() => pendingKeys.value.size > 0), applySnapshot, acceptEvent, refreshCases, refreshDetail, selectCase, runOnce }
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
