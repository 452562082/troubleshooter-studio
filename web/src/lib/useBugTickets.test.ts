import { describe, expect, it, vi } from 'vitest'
import { fetchBugByID as bridgeFetchBugByID, listBugs as bridgeListBugs } from './bridge/bugs'
import type { BugFetchInput, BugRecord } from './bridge/bugs'
import { useBugTickets } from './useBugTickets'

const bugs: BugRecord[] = [
  {
    id: 'zentao-840',
    source: 'zentao',
    source_id: '840',
    title: 'Checkout timeout',
    env: 'prod',
    system_id: 'storefront',
    service_hints: ['order-api'],
  },
  {
    id: 'lark-17',
    source: 'lark',
    source_id: 'TASK-17',
    title: 'Cache misses after release',
    bot_env: 'staging',
    system_id: 'platform',
    service_hints: ['redis-worker', 'metrics-api'],
  },
]

describe('useBugTickets', () => {
  it('accepts the real Bug bridge signatures without an adapter', () => {
    const tickets = useBugTickets({ listBugs: bridgeListBugs, fetchBugByID: bridgeFetchBugByID })
    const fetchExact: (input: BugFetchInput) => Promise<unknown> = tickets.fetchByID

    expect(fetchExact).toBe(tickets.fetchByID)
  })

  it.each([
    ['title', 'checkout', 'zentao-840'],
    ['source', 'LARK', 'lark-17'],
    ['source ID', 'task-17', 'lark-17'],
    ['environment', 'STAGING', 'lark-17'],
    ['system', 'storefront', 'zentao-840'],
    ['service hint', 'metrics-api', 'lark-17'],
  ])('filters deterministically by %s', async (_field, query, expectedID) => {
    const tickets = useBugTickets({
      listBugs: vi.fn().mockResolvedValue(bugs),
      fetchBugByID: vi.fn(),
    })
    await tickets.load()

    tickets.query.value = query

    expect(tickets.filteredBugs.value.map(bug => bug.id)).toEqual([expectedID])
  })

  it('keeps source order for an empty or whitespace-only query', async () => {
    const tickets = useBugTickets({
      listBugs: vi.fn().mockResolvedValue(bugs),
      fetchBugByID: vi.fn(),
    })
    await tickets.load()

    tickets.query.value = '   '

    expect(tickets.filteredBugs.value.map(bug => bug.id)).toEqual(['zentao-840', 'lark-17'])
  })

  it('does not guess the first ticket when the requested ID is invalid', async () => {
    const tickets = useBugTickets({
      listBugs: vi.fn().mockResolvedValue(bugs),
      fetchBugByID: vi.fn(),
    })
    tickets.select('missing-404')

    await tickets.load()

    expect(tickets.selectedID.value).toBe('missing-404')
    expect(tickets.selectedBug.value).toBeUndefined()
  })

  it('selects the first ticket only when no selection was requested and can clear it', async () => {
    const tickets = useBugTickets({
      listBugs: vi.fn().mockResolvedValue(bugs),
      fetchBugByID: vi.fn(),
    })

    await tickets.load()
    expect(tickets.selectedBug.value?.id).toBe('zentao-840')

    tickets.clearSelection()
    expect(tickets.selectedID.value).toBe('')
    expect(tickets.selectedBug.value).toBeUndefined()
  })

  it('fetches the exact requested ID, reloads tickets, and selects the persisted ID', async () => {
    const listBugs = vi.fn()
      .mockResolvedValueOnce(bugs)
      .mockResolvedValueOnce([...bugs, { id: 'zentao-999', source: 'zentao', source_id: '999', title: 'New bug' }])
    const fetchBugByID = vi.fn().mockResolvedValue({ selected_bug_id: 'zentao-999' })
    const tickets = useBugTickets({ listBugs, fetchBugByID })
    await tickets.load()

    await tickets.fetchByID('#999')

    expect(fetchBugByID).toHaveBeenCalledWith('#999')
    expect(listBugs).toHaveBeenCalledTimes(2)
    expect(tickets.selectedID.value).toBe('zentao-999')
    expect(tickets.selectedBug.value?.title).toBe('New bug')
  })

  it('falls back to the requested bridge bug ID when no persisted ID is returned', async () => {
    const fetchBugByID = vi.fn().mockResolvedValue({ platform_id: 'zentao-main' })
    const tickets = useBugTickets({
      listBugs: vi.fn().mockResolvedValue(bugs),
      fetchBugByID,
    })
    const input = { platform_id: 'zentao-main', bug_id: 'zentao-840' }

    await tickets.fetchByID(input)

    expect(fetchBugByID).toHaveBeenCalledWith(input)
    expect(tickets.selectedID.value).toBe('zentao-840')
    expect(tickets.selectedBug.value?.id).toBe('zentao-840')
  })

  it('exposes load failures and always clears loading', async () => {
    const error = new Error('offline')
    const tickets = useBugTickets({
      listBugs: vi.fn().mockRejectedValue(error),
      fetchBugByID: vi.fn(),
    })

    await expect(tickets.load()).rejects.toThrow('offline')

    expect(tickets.error.value).toBe(error)
    expect(tickets.loading.value).toBe(false)
  })
})
