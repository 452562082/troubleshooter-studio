import { computed, ref } from 'vue'
import type { BugRecord } from './bridge/bugs'

type FetchBugResult = BugRecord | { selected_bug_id?: string } | void

export interface BugTicketDependencies<TFetchArgument, TFetchResult extends FetchBugResult = FetchBugResult> {
  listBugs: () => Promise<BugRecord[]>
  fetchBugByID: (id: TFetchArgument) => Promise<TFetchResult>
}

export function useBugTickets<TFetchArgument, TFetchResult extends FetchBugResult>(
  dependencies: BugTicketDependencies<TFetchArgument, TFetchResult>,
) {
  const bugs = ref<BugRecord[]>([])
  const selectedID = ref('')
  const query = ref('')
  const loading = ref(false)
  const error = ref<unknown>()

  const selectedBug = computed(() => bugs.value.find(bug => bug.id === selectedID.value))
  const filteredBugs = computed(() => {
    const keyword = query.value.trim().toLowerCase()
    if (!keyword) return bugs.value
    return bugs.value.filter(bug => [
      bug.title,
      bug.source,
      bug.source_id,
      bug.env,
      bug.bot_env,
      bug.system_id,
      ...(bug.service_hints || []),
    ].filter(Boolean).join(' ').toLowerCase().includes(keyword))
  })

  async function load() {
    loading.value = true
    error.value = undefined
    try {
      bugs.value = await dependencies.listBugs()
      if (!selectedID.value && bugs.value.length > 0) selectedID.value = bugs.value[0].id
    } catch (cause) {
      error.value = cause
      throw cause
    } finally {
      loading.value = false
    }
  }

  function select(id: string) {
    selectedID.value = id
  }

  async function fetchByID(id: TFetchArgument) {
    loading.value = true
    error.value = undefined
    try {
      const result = await dependencies.fetchBugByID(id)
      selectedID.value = selectedIDFromFetch(result) || requestedBugID(id)
      await load()
      return result
    } catch (cause) {
      error.value = cause
      throw cause
    } finally {
      loading.value = false
    }
  }

  function clearSelection() {
    selectedID.value = ''
  }

  return {
    bugs,
    selectedID,
    selectedBug,
    query,
    filteredBugs,
    loading,
    error,
    load,
    select,
    fetchByID,
    clearSelection,
  }
}

function selectedIDFromFetch(result: FetchBugResult): string {
  if (!result) return ''
  if ('selected_bug_id' in result) return result.selected_bug_id || ''
  return 'id' in result ? result.id : ''
}

function requestedBugID(input: unknown): string {
  if (typeof input === 'string') return input
  if (!input || typeof input !== 'object' || !('bug_id' in input)) return ''
  return typeof input.bug_id === 'string' ? input.bug_id : ''
}
