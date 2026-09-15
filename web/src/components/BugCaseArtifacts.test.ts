import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import type { IncidentCaseDetail, PhaseAttempt } from '../lib/bridge/bugWorkflow'
import BugCaseArtifacts from './BugCaseArtifacts.vue'

const preview = vi.hoisted(() => vi.fn())
vi.mock('../lib/bridge/bugWorkflow', async original => ({ ...(await original<object>()), getIncidentArtifactPreview: preview }))
function attempt(id: string, phase: PhaseAttempt['phase'] = 'investigation', output: Record<string, unknown> = {}): PhaseAttempt {
  return { id, phase, case_id: 'case-1', cycle_number: 1, mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'bot', input_json: {}, output_json: output, parent_attempt_id: '', started_at: '2026-09-14T10:00:00Z', error_code: '', error_message: '', usage: {} }
}
function detail(attempts: PhaseAttempt[] = []): IncidentCaseDetail {
  return { case: { id: 'case-1', bug_id: 'bug', source: 'zentao', system_id: 'base', environment: 'test', status: 'investigating', cycle_number: 1, current_attempt_id: attempts[attempts.length - 1]?.id || '', selected_bot_key: 'bot', version: 1, created_at: '', updated_at: '' }, attempts, artifacts: [], approvals: [], code_changes: [], deployment_observations: [], events: [] }
}
const rootOutput = { investigation_status: 'root_cause_ready', root_cause: '缓存键未包含用户 ID', remediation: { mode: 'code_change', summary: '将用户 ID 加入缓存键' }, evidence: [] }

describe('incident result workspace', () => {
  it('removes verification and deployment cards without fetching screenshot previews', () => {
    const data = detail()
    data.artifacts = [{ id: 'old-image', case_id: 'case-1', attempt_id: 'old-validation', kind: 'screenshot', sha256: '', size: 1, captured_at: '', environment: '', version: '', request_id: '', trace_id: '' }]
    const wrapper = mount(BugCaseArtifacts, { props: { detail: data } })
    expect(wrapper.text()).toBe('')
    expect(wrapper.findAll('section, details')).toHaveLength(0)
    expect(preview).not.toHaveBeenCalled()
  })
  it('shows one root cause and remediation report without empty result panels', () => {
    const wrapper = mount(BugCaseArtifacts, { props: { detail: detail([attempt('root', 'investigation', rootOutput)]) } })
    expect(wrapper.findAll('.stage-attempt')).toHaveLength(1)
    expect(wrapper.text().match(/缓存键未包含用户 ID/g)).toHaveLength(1)
    expect(wrapper.text()).toContain('将用户 ID 加入缓存键')
    for (const label of ['验证证据', '尚无', '部署观察', '阶段输出', '调用链定位', '授权记录']) expect(wrapper.text()).not.toContain(label)
  })
  it('shows call-chain locations inside the report only when provided', () => {
    const wrapper = mount(BugCaseArtifacts, { props: { detail: detail([attempt('root', 'investigation', { ...rootOutput, call_chain: [{ name: '加载用户', service: 'api', repo: 'api', file: 'user.go', line: 42, revision: 'abc', evidence: '请求日志与源码一致' }] })]) } })
    expect(wrapper.text()).toContain('调用链定位')
    expect(wrapper.text()).toContain('api/user.go:42')
    expect(wrapper.text()).toContain('请求日志与源码一致')
  })
  it('does not create an empty report while the Agent is running', () => {
    const running = { ...attempt('running'), status: 'running' as const }
    const wrapper = mount(BugCaseArtifacts, { props: { detail: detail([running]) } })
    expect(wrapper.find('.current-reports').exists()).toBe(false)
    expect(wrapper.text()).toBe('')
  })
  it('keeps errors visible even if an attempt has no structured result', () => {
    const failed = { ...attempt('failed'), status: 'failed' as const, error_message: '日志服务暂时不可用' }
    const wrapper = mount(BugCaseArtifacts, { props: { detail: detail([failed]) } })
    expect(wrapper.get('[data-attempt-error]').text()).toBe('日志服务暂时不可用')
  })
  it('moves superseded root causes and fixes into closed history when investigation restarts', () => {
    const old = attempt('old-root', 'investigation', rootOutput)
    const fix = attempt('old-fix', 'fix', { fix_status: 'fixed_pushed' })
    const running = { ...attempt('new-root'), status: 'running' as const }
    const wrapper = mount(BugCaseArtifacts, { props: { detail: detail([old, fix, running]) } })
    expect(wrapper.find('.current-reports').exists()).toBe(false)
    expect(wrapper.get('.result-history').attributes('open')).toBeUndefined()
    expect(wrapper.findAll('.result-history .stage-attempt')).toHaveLength(2)
  })
  it('does not promote a disputed or previous-cycle conclusion to current', () => {
    const root = attempt('root', 'investigation', rootOutput)
    const data = detail([root])
    data.events = [{ id: 'event', case_id: 'case-1', from_status: 'waiting_fix_approval', to_status: 'investigating', event_type: 'root_cause_disputed', actor_type: 'human', actor_id: 'me', idempotency_key: 'dispute', payload_json: { source_root_cause_attempt_id: 'root' }, created_at: '' }]
    const wrapper = mount(BugCaseArtifacts, { props: { detail: data } })
    expect(wrapper.find('.current-reports').exists()).toBe(false)
    expect(wrapper.text()).toContain('此结论已被质疑')
    const priorCycle = detail([{ ...root, cycle_number: 0 }])
    expect(mount(BugCaseArtifacts, { props: { detail: priorCycle } }).find('.current-reports').exists()).toBe(false)
  })
  it('keeps investigation context alongside the latest fix and archives earlier fixes', () => {
    const data = detail([attempt('root', 'investigation', rootOutput), attempt('fix-old', 'fix', { fix_status: 'blocked' }), attempt('fix-new', 'fix', { fix_status: 'fixed_pushed', tests: [{ command: 'go test ./...', result: 'passed' }] })])
    const wrapper = mount(BugCaseArtifacts, { props: { detail: data } })
    expect(wrapper.findAll('.current-reports .stage-attempt')).toHaveLength(2)
    expect(wrapper.get('.current-reports').text()).toContain('go test ./...')
    expect(wrapper.get('.result-history').text()).toContain('修复受阻')
  })
  it('only shows current submission records and preserves uncertain push outcomes', async () => {
    const data = detail([attempt('fix', 'fix', { fix_status: 'fixed_pushed' })])
    const change = { id: 'change', case_id: 'case-1', attempt_id: 'fix', repo: 'api', base_branch: 'main', fix_branch: 'fix/bug', fix_commit: 'fix-sha', test_evidence: [{ command: 'go test ./...', result: 'passed' }], target_environment_branch: 'test', merge_base_head: '', merge_commit: 'merge-sha', push_remote: 'origin', push_status: 'pushed' }
    data.code_changes = [change, { ...change, id: 'old', attempt_id: 'old-fix', repo: 'obsolete' }]
    const wrapper = mount(BugCaseArtifacts, { props: { detail: data } })
    expect(wrapper.text()).toContain('merge-sha')
    expect(wrapper.text()).toContain('go test ./... · passed')
    expect(wrapper.text()).not.toContain('obsolete')
    await wrapper.setProps({ detail: { ...data, code_changes: [{ ...change, push_status: 'push_unknown' }] } })
    expect(wrapper.get('.submission-card').text()).toContain('推送结果待确认')
    expect(wrapper.get('.submission-card').text()).not.toContain('已推送')
  })
  it('retains historical output as inert text and suppresses old browser errors', () => {
    const data = detail([attempt('legacy', 'legacy', { final_message: '<img src=x onerror=alert(1)>' }), { ...attempt('old-validation', 'validation', { notes: 'sensitive' }), error_code: 'browser_failed', error_message: 'secret password' }])
    const wrapper = mount(BugCaseArtifacts, { props: { detail: data } })
    expect(wrapper.find('img').exists()).toBe(false)
    expect(wrapper.text()).toContain('<img src=x onerror=alert(1)>')
    expect(wrapper.text()).not.toContain('secret password')
    expect(wrapper.text()).not.toContain('sensitive')
  })
})
