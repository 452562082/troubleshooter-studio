import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  approveIncidentFix,
  approveIncidentMerge,
  continueIncidentCase,
  getIncidentCase,
  listBugs,
  listIncidentCases,
  matchBugBots,
  notifyIncidentDeployed,
  saveBugSelectedBot,
  startIncidentCase,
  type CaseStatus,
  type IncidentCase,
  type IncidentCaseDetail,
} from '../lib/bridge'
import IncidentWorkbenchPage from './IncidentWorkbenchPage.vue'

const route = vi.hoisted(() => ({ query: {} as Record<string, string> }))
const router = vi.hoisted(() => ({ replace: vi.fn() }))
const runtime = vi.hoisted(() => ({ EventsOn: vi.fn(() => vi.fn()) }))

vi.mock('vue-router', () => ({ useRoute: () => route, useRouter: () => router }))
vi.mock('../../wailsjs/runtime/runtime', () => ({ EventsOn: runtime.EventsOn }))
vi.mock('../lib/bridge', () => ({
  approveIncidentFix: vi.fn(),
  approveIncidentMerge: vi.fn(),
  cancelIncidentAttempt: vi.fn(),
  continueIncidentCase: vi.fn(),
  fetchBugByID: vi.fn(),
  getIncidentCase: vi.fn(),
  listBugs: vi.fn().mockResolvedValue([]),
  listIncidentCases: vi.fn().mockResolvedValue([]),
  matchBugBots: vi.fn().mockResolvedValue([]),
  notifyIncidentDeployed: vi.fn(),
  saveBugSelectedBot: vi.fn(),
  startIncidentCase: vi.fn(),
}))
vi.mock('../lib/toast', () => ({
  toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() },
  toastError: vi.fn(),
}))

const bugA = {
  id: 'bug-a', source: 'zentao', source_id: '840', title: '支付页超时', steps: '打开支付页', env: 'test', system_id: 'base', service_hints: ['checkout'],
}
const bugB = {
  id: 'bug-b', source: 'lark', source_id: 'TASK-17', title: '缓存命中下降', steps: '查看缓存指标', env: 'prod', system_id: 'base', service_hints: ['cache'],
}
const botMatch = {
  bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo/base', name: 'Base', env: 'test' }, score: 10, reasons: ['系统匹配'],
}

function incident(id: string, status: CaseStatus, updatedAt: string, overrides: Partial<IncidentCase> = {}): IncidentCase {
  return {
    id,
    bug_id: 'bug-a',
    source: 'zentao',
    system_id: 'base',
    environment: 'test',
    status,
    cycle_number: 1,
    current_attempt_id: 'attempt-1',
    selected_bot_key: 'base|codex',
    version: 7,
    created_at: '2026-07-10T00:00:00Z',
    updated_at: updatedAt,
    ...overrides,
  }
}

function detail(item: IncidentCase, overrides: Partial<IncidentCaseDetail> = {}): IncidentCaseDetail {
  return {
    case: item,
    attempts: [],
    artifacts: [],
    approvals: [],
    code_changes: [],
    deployment_observations: [],
    events: [],
    ...overrides,
  }
}

function mockCaseDetails(...snapshots: IncidentCaseDetail[]) {
  const byID = new Map(snapshots.map(snapshot => [snapshot.case.id, snapshot]))
  vi.mocked(getIncidentCase).mockImplementation(async caseID => {
    const snapshot = byID.get(caseID)
    if (!snapshot) throw new Error(`missing Case ${caseID}`)
    return snapshot
  })
}

function flushPromises() {
  return new Promise(resolve => setTimeout(resolve, 0))
}

async function mountedPage() {
  const wrapper = mount(IncidentWorkbenchPage)
  await flushPromises()
  await flushPromises()
  await flushPromises()
  return wrapper
}

afterEach(() => {
  route.query = {}
  router.replace.mockReset()
  runtime.EventsOn.mockClear()
  vi.mocked(listBugs).mockReset().mockResolvedValue([])
  vi.mocked(listIncidentCases).mockReset().mockResolvedValue([])
  vi.mocked(getIncidentCase).mockReset()
  vi.mocked(matchBugBots).mockReset().mockResolvedValue([botMatch])
  vi.mocked(saveBugSelectedBot).mockReset().mockResolvedValue(bugA as any)
  vi.mocked(startIncidentCase).mockReset()
  vi.mocked(continueIncidentCase).mockReset()
  vi.mocked(approveIncidentFix).mockReset()
  vi.mocked(approveIncidentMerge).mockReset()
  vi.mocked(notifyIncidentDeployed).mockReset()
})

describe('IncidentWorkbenchPage', () => {
  it('restores the exact bug_id from the route without guessing', async () => {
    route.query = { bug_id: 'bug-b' }
    vi.mocked(listBugs).mockResolvedValue([bugA, bugB])

    const wrapper = await mountedPage()

    expect(wrapper.get('[data-ticket-id="bug-b"]').attributes('aria-pressed')).toBe('true')
    expect(wrapper.get('.ticket-detail h2').text()).toBe('缓存命中下降')
    expect(wrapper.get('[data-ticket-id="bug-a"]').attributes('aria-pressed')).toBe('false')
  })

  it('updates the query when another Bug is selected', async () => {
    route.query = { bug_id: 'bug-a', view: 'audit' }
    vi.mocked(listBugs).mockResolvedValue([bugA, bugB])
    const wrapper = await mountedPage()

    await wrapper.get('[data-ticket-id="bug-b"]').trigger('click')
    await flushPromises()

    expect(router.replace).toHaveBeenCalledWith({ query: { bug_id: 'bug-b', view: 'audit' } })
    expect(wrapper.get('.ticket-detail h2').text()).toBe('缓存命中下降')
  })

  it('opens the newest active Case without calling Start', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const oldActive = incident('case-active-old', 'waiting_evidence', '2026-07-11T00:00:00Z')
    const newActive = incident('case-active-new', 'waiting_fix_approval', '2026-07-12T00:00:00Z', { current_attempt_id: 'root-1' })
    const terminal = incident('case-terminal', 'fixed_verified', '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([terminal, oldActive, newActive])
    mockCaseDetails(detail(terminal), detail(oldActive), detail(newActive))

    const wrapper = await mountedPage()

    expect(wrapper.get('.case-heading').text()).toContain('case-active-new')
    expect(wrapper.text()).toContain('允许修复')
    expect(startIncidentCase).not.toHaveBeenCalled()
    expect(wrapper.find('[data-action="start-case"]').exists()).toBe(false)
  })

  it('shows start for a Bug with no Case', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])

    const wrapper = await mountedPage()

    const start = wrapper.get('[data-action="start-case"]')
    expect(start.text()).toContain('开始故障闭环')
    expect(start.attributes('disabled')).toBeUndefined()
  })

  it('shows new round when all Cases are terminal', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const fixed = incident('case-fixed', 'fixed_verified', '2026-07-13T00:00:00Z')
    const archived = incident('case-archive', 'legacy_archived', '2026-07-12T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([archived, fixed])
    mockCaseDetails(detail(archived), detail(fixed))

    const wrapper = await mountedPage()

    expect(wrapper.get('[data-action="start-case"]').text()).toContain('开始新一轮')
    expect(wrapper.get('.case-heading').text()).toContain('case-fixed')
  })

  it('uses an existing Case returned by the backend', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const existing = incident('case-existing', 'validating', '2026-07-13T00:00:00Z', { version: 4 })
    vi.mocked(startIncidentCase).mockResolvedValue(existing)
    mockCaseDetails(detail(existing))
    const wrapper = await mountedPage()

    await wrapper.get('[data-action="start-case"]').trigger('click')
    await flushPromises()
    await flushPromises()

    const input = vi.mocked(startIncidentCase).mock.calls[0][0]
    expect(input.case_id).not.toBe('case-existing')
    expect(input).toMatchObject({ bug_id: 'bug-a', bot_key: 'base|codex', expected_version: 0, actor_id: 'desktop-user' })
    expect(getIncidentCase).toHaveBeenCalledWith('case-existing')
    expect(wrapper.text()).toContain('已打开现有闭环')
  })

  it('shows a recoverable empty state for an invalid URL Bug', async () => {
    route.query = { bug_id: 'missing-bug' }
    vi.mocked(listBugs).mockResolvedValue([bugA])

    const wrapper = await mountedPage()

    expect(wrapper.text()).toContain('URL 中的 Bug 不存在')
    expect(wrapper.get('[data-ticket-id="bug-a"]').attributes('aria-pressed')).toBe('false')
    expect(matchBugBots).not.toHaveBeenCalled()

    await wrapper.get('[data-ticket-id="bug-a"]').trigger('click')
    await flushPromises()
    expect(router.replace).toHaveBeenCalledWith({ query: { bug_id: 'bug-a' } })
    expect(wrapper.get('.ticket-detail h2').text()).toBe('支付页超时')
  })

  it('continues a legacy archive by starting a fresh durable Case round', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const archived = incident('legacy-1', 'legacy_archived', '2026-07-13T00:00:00Z', { source: 'legacy-runs-json', version: 3 })
    vi.mocked(listIncidentCases).mockResolvedValue([archived])
    mockCaseDetails(detail(archived))
    vi.mocked(startIncidentCase).mockResolvedValue(incident('case-new', 'validating', '2026-07-13T00:01:00Z', { version: 1 }))
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    await flushPromises()

    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({
      bug_id: 'bug-a', expected_version: 0, bot_key: 'base|codex', actor_id: 'desktop-user',
    }))
    expect(vi.mocked(startIncidentCase).mock.calls[0][0].case_id).not.toBe('legacy-1')
  })

  it('submits fix approval with the dialog-captured root cause and exact Case version key', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-1', 'waiting_fix_approval', '2026-07-13T00:00:00Z')
    const snapshot = detail(item, {
      attempts: [{ id: 'attempt-1', case_id: 'case-1', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { investigation_status: 'root_cause_ready' }, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(snapshot)
    vi.mocked(approveIncidentFix).mockResolvedValue({ ...item, status: 'fixing', version: 8 })
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    await wrapper.get('[data-confirm]').trigger('click')
    await flushPromises()

    expect(approveIncidentFix).toHaveBeenCalledWith(expect.objectContaining({
      case_id: 'case-1', expected_version: 7, root_cause_attempt_id: 'attempt-1', idempotency_key: 'start-fix:case-1:attempt-1:7', actor_id: 'desktop-user',
    }))
  })

  it('forwards the exact persisted target heads with merge approval', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-1', 'waiting_merge_approval', '2026-07-13T00:00:00Z')
    const snapshot = detail(item, {
      code_changes: [
        { id: 'a', case_id: 'case-1', attempt_id: 'attempt-1', repo: 'api', base_branch: 'main', fix_branch: 'fix/api', fix_commit: 'fix-api', test_evidence: {}, target_environment_branch: 'test', merge_base_head: 'head-api', merge_commit: '', push_remote: 'origin', push_status: 'pushed' },
        { id: 'w', case_id: 'case-1', attempt_id: 'attempt-1', repo: 'web', base_branch: 'main', fix_branch: 'fix/web', fix_commit: 'fix-web', test_evidence: {}, target_environment_branch: 'test', merge_base_head: 'head-web', merge_commit: '', push_remote: 'origin', push_status: 'pushed' },
      ],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(snapshot)
    vi.mocked(approveIncidentMerge).mockResolvedValue({ ...item, status: 'merging', version: 8 })
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    await wrapper.get('[data-confirm]').trigger('click')
    await flushPromises()

    expect(approveIncidentMerge).toHaveBeenCalledWith(expect.objectContaining({
      target_heads: { api: 'head-api', web: 'head-web' },
      fix_commits: { api: 'fix-api', web: 'fix-web' },
      target_branches: { api: 'test', web: 'test' },
    }))
  })

  it.each([
    ['merge_conflict', approveIncidentMerge, 'resolve_merge_conflict', 'fix'],
    ['deployment_unverified', notifyIncidentDeployed, 'update_deployment_proof', 'regression'],
  ] as const)('uses ContinueIncidentCase for %s recovery before the gated action', async (status, forbidden, decision, phase) => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-1', status, '2026-07-13T00:00:00Z')
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(detail(item))
    vi.mocked(continueIncidentCase).mockResolvedValue({ ...item, status: status === 'merge_conflict' ? 'waiting_merge_approval' : 'waiting_deployment', version: 8 })
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    await wrapper.get('[role="dialog"] textarea').setValue('人工确认已处理')
    await wrapper.get('[data-confirm]').trigger('click')
    await flushPromises()

    expect(continueIncidentCase).toHaveBeenCalledWith(expect.objectContaining({ phase, input_json: expect.objectContaining({ decision, evidence: '人工确认已处理' }) }))
    expect(forbidden).not.toHaveBeenCalled()
  })

  it('recovers an empty migrated selected_bot_key from the latest legacy attempt', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-1', 'legacy_archived', '2026-07-13T00:00:00Z', { selected_bot_key: '' })
    const snapshot = detail(item, {
      attempts: [{ id: 'legacy-attempt', case_id: 'case-1', cycle_number: 1, phase: 'legacy', mode: '', status: 'succeeded', agent_target: '', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '2026-07-11T00:00:00Z', error_code: '', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(snapshot)
    vi.mocked(startIncidentCase).mockResolvedValue(incident('new-case', 'validating', '2026-07-13T00:01:00Z', { version: 1 }))
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    await flushPromises()

    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({ bot_key: 'base|codex' }))
  })

  it('requires explicit bot reselection for an unbound archive before starting a new round', async () => {
    route.query = { bug_id: 'bug-a' }
    vi.mocked(listBugs).mockResolvedValue([bugA])
    const item = incident('case-1', 'legacy_archived', '2026-07-13T00:00:00Z', { selected_bot_key: '' })
    vi.mocked(listIncidentCases).mockResolvedValue([item])
    mockCaseDetails(detail(item))
    vi.mocked(startIncidentCase).mockResolvedValue(incident('new-case', 'validating', '2026-07-13T00:01:00Z', { version: 1 }))
    const wrapper = await mountedPage()

    await wrapper.get('.primary-action').trigger('click')
    await flushPromises()
    expect(startIncidentCase).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('重新选择当前 Bug 的机器人')

    await wrapper.get('.bot-picker input[type="radio"]').setValue(true)
    await wrapper.get('.primary-action').trigger('click')
    await flushPromises()
    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({ bot_key: 'base|codex' }))
  })
})
