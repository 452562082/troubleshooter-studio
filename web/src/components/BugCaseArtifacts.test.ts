import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { IncidentCaseDetail } from '../lib/bridge/bugWorkflow'
import BugCaseArtifacts from './BugCaseArtifacts.vue'

const detail: IncidentCaseDetail = {
  case: { id: 'case-1', bug_id: 'bug-1', source: 'zentao', system_id: 'base', environment: 'test', status: 'waiting_deployment', cycle_number: 1, current_attempt_id: 'fix-1', selected_bot_key: 'base|codex', version: 9, created_at: '', updated_at: '' },
  attempts: [{ id: 'investigate-1', case_id: 'case-1', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { summary: '根因是空指针' }, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }],
  artifacts: [{ id: 'evidence-1', case_id: 'case-1', attempt_id: 'investigate-1', kind: 'screenshot', path_or_reference: '/artifact/screenshot.png', sha256: 'abc', captured_at: '2026-07-11T11:00:00Z', environment: 'test', version: 'build-1', request_id: 'req-1', trace_id: 'trace-1', redaction_status: 'redacted' }],
  approvals: [{ id: 'approval-1', case_id: 'case-1', kind: 'merge_environment_branch', actor: 'alice', approved_at: '2026-07-11T12:00:00Z', case_version: 8, scope_json: {}, fix_commits: { api: 'abc' }, target_branches: { api: 'test' } }],
  code_changes: [{ id: 'change-1', case_id: 'case-1', attempt_id: 'fix-1', repo: 'api', base_branch: 'main', fix_branch: 'fix/bug-1', fix_commit: 'abc', test_evidence: ['go test ./...'], target_environment_branch: 'test', merge_base_head: 'base', merge_commit: 'merge', push_remote: 'origin', push_status: 'pushed' }],
  deployment_observations: [{ id: 'deploy-1', case_id: 'case-1', environment: 'test', expected_commits: { api: 'merge' }, observed_version: 'build-1', observed_images: { api: 'api:build-1' }, observed_commits: { api: 'merge' }, verification_source: 'version endpoint', result: 'matched' }],
  events: [],
}

describe('BugCaseArtifacts', () => {
  it('renders evidence, root cause, code tests, approvals and deployment observations', () => {
    const wrapper = mount(BugCaseArtifacts, { props: { detail } })

    expect(wrapper.text()).toContain('验证证据')
    expect(wrapper.text()).toContain('根因结论')
    expect(wrapper.text()).toContain('代码变更与测试')
    expect(wrapper.text()).toContain('授权记录')
    expect(wrapper.text()).toContain('部署观察')
    expect(wrapper.text()).toContain('trace-1')
    expect(wrapper.text()).toContain('go test ./...')
    expect(wrapper.text()).toContain('build-1')
  })

  it('keeps imported legacy attempt output readable', () => {
    const archived = {
      ...detail,
      case: { ...detail.case, status: 'legacy_archived' as const },
      attempts: [{ ...detail.attempts[0], id: 'legacy-1', phase: 'legacy' as const, output_json: { final_message: '**旧排障结论**：缓存击穿', events: [{ type: 'message', message: '检查 Redis 命中率' }] } }],
    }
    const wrapper = mount(BugCaseArtifacts, { props: { detail: archived } })
    expect(wrapper.text()).toContain('阶段输出')
    expect(wrapper.find('.legacy-final strong').text()).toBe('旧排障结论')
    expect(wrapper.text()).toContain('检查 Redis 命中率')
    expect(wrapper.find('.legacy-attempt > pre').exists()).toBe(false)
  })
})
