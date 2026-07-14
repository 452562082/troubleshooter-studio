import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import { readFileSync } from 'node:fs'
import type { CaseStatus, IncidentCase, IncidentCaseDetail } from '../lib/bridge/bugWorkflow'
import BugCaseLifecycle, { primaryActionFor } from './BugCaseLifecycle.vue'

function incident(status: CaseStatus, id = 'case-1'): IncidentCase {
  return { id, bug_id: `bug-${id}`, source: 'zentao', system_id: 'base', environment: 'test', status, cycle_number: 1, current_attempt_id: '', selected_bot_key: 'base|codex', version: 2, created_at: '2026-07-11T10:00:00Z', updated_at: '2026-07-11T11:00:00Z' }
}

function detail(status: CaseStatus): IncidentCaseDetail {
  return {
    case: incident(status), attempts: [], artifacts: [], approvals: [], code_changes: [], deployment_observations: [],
    events: [{ id: 'event-1', case_id: 'case-1', from_status: 'root_cause_ready', to_status: status, event_type: 'transition', actor_type: 'studio', actor_id: 'studio', idempotency_key: 'event-1', payload_json: {}, created_at: '2026-07-11T11:00:00Z' }],
  }
}

describe('BugCaseLifecycle', () => {
  it.each([
    ['waiting_fix_approval', '允许修复'],
    ['waiting_merge_approval', '允许合并环境分支'],
    ['waiting_deployment', '已部署，开始验证'],
    ['waiting_evidence', '补充证据并继续'],
    ['legacy_archived', '从新一轮验证继续'],
  ] as Array<[CaseStatus, string]>)('maps %s to its one primary action', (status, label) => {
    expect(primaryActionFor(incident(status))?.label).toBe(label)
  })

  it('does not offer a primary action after regression succeeds', () => {
    expect(primaryActionFor(incident('fixed_verified'))).toBeUndefined()
  })

  it('explains that a failed regression carried fresh evidence into the next cycle', () => {
    const snapshot = detail('investigating')
    snapshot.case.cycle_number = 2
    snapshot.events.push({ id: 'regression-failed', case_id: 'case-1', from_status: 'regression_validating', to_status: 'still_reproduces', event_type: 'regression_failed', actor_type: 'agent', actor_id: 'validator', idempotency_key: 'regression-failed', payload_json: {}, created_at: '2026-07-11T12:00:00Z' })
    const wrapper = mount(BugCaseLifecycle, { props: { cases: [snapshot.case], detail: snapshot } })

    expect(wrapper.find('.current-action-card').text()).toContain('回归仍复现')
    expect(wrapper.find('.current-action-card').text()).toContain('新证据和差分')
    expect(wrapper.find('.current-action-card').text()).toContain('第 2 轮')
  })

  it.each([
    ['pending_validation', 'start_validation'], ['validating', 'cancel_attempt'],
    ['not_reproduced', 'supply_evidence'], ['investigating', 'cancel_attempt'],
    ['fixing', 'cancel_attempt'], ['fix_failed', 'continue_fix'],
    ['merge_conflict', 'supply_merge_decision'], ['deployment_unverified', 'supply_deployment_proof'],
    ['regression_validating', 'cancel_attempt'],
  ] as Array<[CaseStatus, string]>)('keeps exactly one action for actionable state %s', (status, kind) => {
    expect(primaryActionFor(incident(status))?.kind).toBe(kind)
  })

  it('renders Cases and the current Case above a full-width detail region', () => {
    const wrapper = mount(BugCaseLifecycle, { props: { cases: [incident('waiting_fix_approval')], detail: detail('waiting_fix_approval') } })
    const columns = wrapper.findAll('.case-column')
    const source = readFileSync('src/components/BugCaseLifecycle.vue', 'utf8')

    expect(columns).toHaveLength(3)
    expect(columns[0].classes()).toContain('case-list-column')
    expect(columns[1].classes()).toContain('case-main-column')
    expect(columns[2].classes()).toContain('case-detail-column')
    expect(source).toMatch(/\.case-lifecycle \{[^}]*grid-template-columns: minmax\(210px, \.72fr\) minmax\(0, 1\.65fr\);/)
    expect(source).toMatch(/\.case-detail-column \{[^}]*grid-column: 1 \/ -1;/)
    expect(wrapper.findAll('.lifecycle-stage')).toHaveLength(6)
    expect(wrapper.find('[aria-label="故障处理阶段"]').exists()).toBe(true)
    expect(wrapper.find('[aria-label="Case 时间线"]').text()).toContain('transition')
    expect(wrapper.findAll('.current-action-card .primary-action')).toHaveLength(1)
  })

  it('offers reset as a separate dangerous secondary action without changing the one-primary-action rule', async () => {
    const snapshot = detail('waiting_fix_approval')
    const wrapper = mount(BugCaseLifecycle, { props: { cases: [snapshot.case], detail: snapshot } })

    expect(wrapper.findAll('.current-action-card .primary-action')).toHaveLength(1)
    const reset = wrapper.get('.reset-action')
    expect(reset.classes()).toContain('danger-secondary')
    expect(reset.classes()).not.toContain('primary')
    await reset.trigger('click')

    expect(wrapper.emitted('reset')?.[0]).toEqual([snapshot.case])
    expect(wrapper.emitted('primary')).toBeUndefined()
  })

  it.each(['fixed_verified', 'legacy_archived', 'reset_archived'] as CaseStatus[])('does not offer reset for terminal state %s', status => {
    const wrapper = mount(BugCaseLifecycle, { props: { cases: [incident(status)], detail: detail(status) } })
    expect(wrapper.find('.reset-action').exists()).toBe(false)
  })

  it('disables reset while another Case operation is pending', () => {
    const wrapper = mount(BugCaseLifecycle, { props: { cases: [incident('waiting_evidence')], detail: detail('waiting_evidence'), pending: true } })
    expect(wrapper.get<HTMLButtonElement>('.reset-action').element.disabled).toBe(true)
  })

  it('opens an accessible approval dialog before emitting approval', async () => {
    const wrapper = mount(BugCaseLifecycle, { props: { cases: [incident('waiting_merge_approval')], detail: detail('waiting_merge_approval') } })

    await wrapper.find('.primary-action').trigger('click')
    const dialog = wrapper.find('[role="dialog"]')
    expect(dialog.attributes('aria-modal')).toBe('true')
    expect(dialog.attributes('aria-labelledby')).toBeTruthy()
    await dialog.find('[data-confirm]').trigger('click')

    expect(wrapper.emitted('primary')?.[0]).toEqual([{ kind: 'approve_merge' }])
  })

  it('emits the root-cause attempt and Case version captured when the fix dialog opens', async () => {
    const snapshot = detail('waiting_fix_approval')
    snapshot.case.version = 7
    snapshot.case.current_attempt_id = 'investigation-7'
    snapshot.attempts = [{ id: 'investigation-7', case_id: 'case-1', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { investigation_status: 'root_cause_ready', confidence: 'high', gaps: [] }, parent_attempt_id: '', started_at: '2026-07-11T10:00:00Z', error_code: '', error_message: '', usage: {} }]
    const wrapper = mount(BugCaseLifecycle, { props: { cases: [snapshot.case], detail: snapshot } })

    await wrapper.find('.primary-action').trigger('click')
    await wrapper.setProps({ detail: { ...snapshot, case: { ...snapshot.case, version: 8 } } })
    await wrapper.find('[data-confirm]').trigger('click')

    expect(wrapper.emitted('primary')?.[0]).toEqual([{ kind: 'approve_fix', rootCauseAttemptID: 'investigation-7', caseVersion: 7 }])
  })

  it('previews target environment, expected commits, and verifier before deployment validation', async () => {
    const snapshot = detail('waiting_deployment')
    snapshot.case.current_attempt_id = 'fix-1'
    snapshot.code_changes = [{ id: 'change-1', case_id: 'case-1', attempt_id: 'fix-1', repo: 'api', base_branch: 'main', fix_branch: 'fix/1', fix_commit: 'fix-abc', test_evidence: [], target_environment_branch: 'test', merge_base_head: 'base', merge_commit: 'merge-def', push_remote: 'origin', push_status: 'pushed' }]
    snapshot.deployment_observations = [{ id: 'observation-1', case_id: 'case-1', environment: 'test', expected_commits: { api: 'merge-def' }, observed_version: '', observed_images: {}, observed_commits: {}, verification_source: 'version endpoint', result: 'unavailable' }]
    const wrapper = mount(BugCaseLifecycle, { props: { cases: [snapshot.case], detail: snapshot } })

    await wrapper.find('.primary-action').trigger('click')

    expect(wrapper.find('[role="dialog"]').text()).toContain('test')
    expect(wrapper.find('[role="dialog"]').text()).toContain('api: merge-def')
    expect(wrapper.find('[role="dialog"]').text()).toContain('version endpoint')
    expect(wrapper.find('[role="dialog"]').text()).not.toContain('部署已确认')
  })

  it('submits manual proof without a caller-controlled version source', async () => {
    const snapshot = detail('waiting_deployment')
    snapshot.case.current_attempt_id = 'fix-1'
    snapshot.code_changes = [
      { id: 'api', case_id: 'case-1', attempt_id: 'fix-1', repo: 'api', base_branch: 'main', fix_branch: 'fix/1', fix_commit: 'fix-api', test_evidence: [], target_environment_branch: 'test', merge_base_head: 'base', merge_commit: 'merge-api', push_remote: 'origin', push_status: 'pushed' },
      { id: 'worker', case_id: 'case-1', attempt_id: 'fix-1', repo: 'worker', base_branch: 'main', fix_branch: 'fix/1', fix_commit: 'fix-worker', test_evidence: [], target_environment_branch: 'test', merge_base_head: 'base', merge_commit: 'merge-worker', push_remote: 'origin', push_status: 'pushed' },
    ]
    const wrapper = mount(BugCaseLifecycle, { props: { cases: [snapshot.case], detail: snapshot } })

    await wrapper.find('.primary-action').trigger('click')
    await wrapper.find('#observed-version').setValue('build-42')
    await wrapper.find('#observed-commit-api').setValue('merge-api')
    await wrapper.find('[data-confirm]').trigger('click')

    expect(wrapper.emitted('primary')?.[0]).toEqual([{
      kind: 'notify_deployed', observedVersion: 'build-42', observedCommits: { api: 'merge-api' },
    }])
  })

  it('starts automatic HTTP verification without manual proof fields', async () => {
    const snapshot = detail('waiting_deployment')
    snapshot.deployment_verification = { provider: 'http', available: true, hint: 'HTTP 版本接口自动验证 · /git/commit' }
    const wrapper = mount(BugCaseLifecycle, { props: { cases: [snapshot.case], detail: snapshot } })
    await wrapper.find('.primary-action').trigger('click')
    expect(wrapper.find('#observed-version').exists()).toBe(false)
    expect(wrapper.find('[role="dialog"]').text()).toContain('HTTP 版本接口自动验证')
    expect(wrapper.find<HTMLButtonElement>('[data-confirm]').element.disabled).toBe(false)
    await wrapper.find('[data-confirm]').trigger('click')
    expect(wrapper.emitted('primary')?.[0]).toEqual([{ kind: 'notify_deployed' }])
  })

  it('previews only the current fix attempt deployment scope in a later cycle', async () => {
    const snapshot = detail('waiting_deployment')
    snapshot.case.current_attempt_id = 'fix-2'
    snapshot.case.cycle_number = 2
    snapshot.code_changes = [
      { id: 'old', case_id: 'case-1', attempt_id: 'fix-1', repo: 'api', base_branch: 'main', fix_branch: 'fix/old', fix_commit: 'fix-old', test_evidence: [], target_environment_branch: 'test', merge_base_head: 'base-1', merge_commit: 'merge-old', push_remote: 'origin', push_status: 'pushed' },
      { id: 'new', case_id: 'case-1', attempt_id: 'fix-2', repo: 'api', base_branch: 'main', fix_branch: 'fix/new', fix_commit: 'fix-new', test_evidence: [], target_environment_branch: 'test', merge_base_head: 'base-2', merge_commit: 'merge-new', push_remote: 'origin', push_status: 'pushed' },
    ]
    snapshot.deployment_observations = [{ id: 'old-observation', case_id: 'case-1', environment: 'test', expected_commits: { api: 'merge-old' }, observed_version: 'old', observed_images: {}, observed_commits: { api: 'merge-old' }, verification_source: 'old source', result: 'matched' }]
    const wrapper = mount(BugCaseLifecycle, { props: { cases: [snapshot.case], detail: snapshot } })

    await wrapper.find('.primary-action').trigger('click')

    expect(wrapper.find('[role="dialog"]').text()).toContain('api: merge-new')
    expect(wrapper.find('[role="dialog"]').text()).not.toContain('merge-old')
    expect(wrapper.find('[role="dialog"]').text()).toContain('manual')
  })

  it('restores focus to the primary action when an approval dialog closes', async () => {
    const snapshot = detail('waiting_fix_approval')
    snapshot.case.current_attempt_id = 'investigation-focus'
    snapshot.attempts = [{ id: 'investigation-focus', case_id: 'case-1', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { investigation_status: 'root_cause_ready', confidence: 'high', gaps: [] }, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }]
    const wrapper = mount(BugCaseLifecycle, { attachTo: document.body, props: { cases: [snapshot.case], detail: snapshot } })
    const trigger = wrapper.find<HTMLButtonElement>('.primary-action')
    await trigger.trigger('click')
    expect(document.activeElement).toBe(wrapper.find('[data-confirm]').element)

    await wrapper.find('[role="dialog"] footer .btn').trigger('click')
    await wrapper.vm.$nextTick()

    expect(document.activeElement).toBe(trigger.element)
    wrapper.unmount()
  })

  it('shows archived Cases as read-only and continues through a new Case action', () => {
    const wrapper = mount(BugCaseLifecycle, { props: { cases: [incident('legacy_archived')], detail: detail('legacy_archived') } })

    expect(wrapper.text()).toContain('历史记录只读')
    expect(wrapper.find('.primary-action').text()).toBe('从新一轮验证继续')
  })

  it('shows reset archives as terminal read-only history', () => {
    const wrapper = mount(BugCaseLifecycle, { props: { cases: [incident('reset_archived')], detail: detail('reset_archived') } })

    expect(wrapper.text()).toContain('已重置归档')
    expect(wrapper.text()).toContain('历史记录只读')
    expect(wrapper.find('.primary-action').exists()).toBe(false)
    expect(wrapper.findAll('.lifecycle-stage').every(stage => stage.attributes('data-state') === 'archived')).toBe(true)
    expect(wrapper.find('.terminal-copy').text()).toBe('已归档，由新 Case 接替')
    expect(wrapper.text()).not.toContain('当前阶段自动推进')
  })

  it('contains responsive no-overflow contracts for all supported viewport fixtures', () => {
    const css = (BugCaseLifecycle as any).__cssModules ? '' : String((BugCaseLifecycle as any).__scopeId || '')
    expect(css).not.toContain('emoji')
    const wrapper = mount(BugCaseLifecycle, { props: { cases: [incident('validating')], detail: detail('validating') } })
    expect(wrapper.find('.case-lifecycle').attributes('data-responsive-viewports')).toBe('375,768,1024,1440')
    expect(wrapper.find('.case-lifecycle').attributes('data-overflow-safe')).toBe('true')
    const source = readFileSync('src/components/BugCaseLifecycle.vue', 'utf8')
    expect(source).toContain('@media (max-width: 899px)')
    expect(source).not.toContain('@media (max-width: 1180px)')
    expect(source).toMatch(/@media \(max-width: 899px\)[\s\S]*?\.case-lifecycle \{ grid-template-columns: minmax\(0, 1fr\); \}/)
    expect(source).toMatch(/@media \(max-width: 899px\)[\s\S]*?\.case-detail-column \{ grid-column: auto; \}/)
    expect(source).toContain('@media (max-width: 560px)')
    expect(source).toMatch(/\.case-lifecycle \{[^}]*min-width: 0;/)
    expect(source).toMatch(/\.case-column \{[^}]*min-width: 0;/)
  })
})
