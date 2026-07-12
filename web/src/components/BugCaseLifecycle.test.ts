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

  it.each([
    ['pending_validation', 'start_validation'], ['validating', 'cancel_attempt'],
    ['not_reproduced', 'supply_evidence'], ['investigating', 'cancel_attempt'],
    ['fixing', 'cancel_attempt'], ['fix_failed', 'continue_fix'],
    ['merge_conflict', 'supply_merge_decision'], ['deployment_unverified', 'supply_deployment_proof'],
    ['regression_validating', 'cancel_attempt'],
  ] as Array<[CaseStatus, string]>)('keeps exactly one action for actionable state %s', (status, kind) => {
    expect(primaryActionFor(incident(status))?.kind).toBe(kind)
  })

  it('renders three semantic columns, six stages, timeline and one primary button', () => {
    const wrapper = mount(BugCaseLifecycle, { props: { cases: [incident('waiting_fix_approval')], detail: detail('waiting_fix_approval') } })

    expect(wrapper.findAll('.case-column')).toHaveLength(3)
    expect(wrapper.findAll('.lifecycle-stage')).toHaveLength(6)
    expect(wrapper.find('[aria-label="故障处理阶段"]').exists()).toBe(true)
    expect(wrapper.find('[aria-label="Case 时间线"]').text()).toContain('transition')
    expect(wrapper.findAll('.current-action-card .primary-action')).toHaveLength(1)
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
    snapshot.code_changes = [{ id: 'change-1', case_id: 'case-1', attempt_id: 'fix-1', repo: 'api', base_branch: 'main', fix_branch: 'fix/1', fix_commit: 'fix-abc', test_evidence: [], target_environment_branch: 'test', merge_base_head: 'base', merge_commit: 'merge-def', push_remote: 'origin', push_status: 'pushed' }]
    snapshot.deployment_observations = [{ id: 'observation-1', case_id: 'case-1', environment: 'test', expected_commits: { api: 'merge-def' }, observed_version: '', observed_images: {}, observed_commits: {}, verification_source: 'version endpoint', result: 'unavailable' }]
    const wrapper = mount(BugCaseLifecycle, { props: { cases: [snapshot.case], detail: snapshot } })

    await wrapper.find('.primary-action').trigger('click')

    expect(wrapper.find('[role="dialog"]').text()).toContain('test')
    expect(wrapper.find('[role="dialog"]').text()).toContain('api: merge-def')
    expect(wrapper.find('[role="dialog"]').text()).toContain('version endpoint')
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

  it('contains responsive no-overflow contracts for all supported viewport fixtures', () => {
    const css = (BugCaseLifecycle as any).__cssModules ? '' : String((BugCaseLifecycle as any).__scopeId || '')
    expect(css).not.toContain('emoji')
    const wrapper = mount(BugCaseLifecycle, { props: { cases: [incident('validating')], detail: detail('validating') } })
    expect(wrapper.find('.case-lifecycle').attributes('data-responsive-viewports')).toBe('375,768,1024,1440')
    expect(wrapper.find('.case-lifecycle').attributes('data-overflow-safe')).toBe('true')
    const source = readFileSync('src/components/BugCaseLifecycle.vue', 'utf8')
    expect(source).toContain('@media (max-width: 899px)')
    expect(source).toMatch(/@media \(max-width: 899px\)[\s\S]*?\.case-lifecycle \{ grid-template-columns: minmax\(0, 1fr\); \}/)
    expect(source).toContain('@media (max-width: 560px)')
    expect(source).toMatch(/\.case-lifecycle \{[^}]*min-width: 0;/)
    expect(source).toMatch(/\.case-column \{[^}]*min-width: 0;/)
  })
})
