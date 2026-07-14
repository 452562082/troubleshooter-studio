import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { IncidentCaseDetail } from '../lib/bridge/bugWorkflow'
import BugCaseArtifacts from './BugCaseArtifacts.vue'
import artifactSource from './BugCaseArtifacts.vue?raw'

const detail: IncidentCaseDetail = {
  case: { id: 'case-1', bug_id: 'bug-1', source: 'zentao', system_id: 'base', environment: 'test', status: 'waiting_deployment', cycle_number: 1, current_attempt_id: 'fix-1', selected_bot_key: 'base|codex', version: 9, created_at: '', updated_at: '' },
  attempts: [{ id: 'investigate-1', case_id: 'case-1', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { summary: '根因是空指针' }, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }],
  artifacts: [{ id: 'evidence-1', case_id: 'case-1', attempt_id: 'investigate-1', kind: 'screenshot', path_or_reference: '/artifact/screenshot.png', sha256: 'abc', captured_at: '2026-07-11T11:00:00Z', environment: 'test', version: 'build-1', request_id: 'req-1', trace_id: 'trace-1', redaction_status: 'redacted' }],
  approvals: [{ id: 'approval-1', case_id: 'case-1', kind: 'merge_environment_branch', actor: 'alice', approved_at: '2026-07-11T12:00:00Z', case_version: 8, scope_json: {}, fix_commits: { api: 'abc' }, target_branches: { api: 'test' } }],
  code_changes: [{ id: 'change-1', case_id: 'case-1', attempt_id: 'fix-1', repo: 'api', base_branch: 'main', fix_branch: 'fix/bug-1', fix_commit: 'abc', test_evidence: ['go test ./...'], target_environment_branch: 'test', merge_base_head: 'base', merge_commit: 'merge', push_remote: 'origin', push_status: 'pushed' }],
  deployment_observations: [{ id: 'deploy-1', case_id: 'case-1', environment: 'test', expected_commits: { api: 'merge' }, observed_version: 'build-1', observed_images: { api: 'api:build-1' }, observed_commits: { api: 'merge' }, observed_at: '2026-07-11T12:05:00Z', diagnostic_code: 'commit_mismatch', diagnostic_message: '运行版本与期望提交不一致', verification_source: 'version endpoint', result: 'matched' }],
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
    expect(wrapper.text()).toContain('2026-07-11T12:05:00Z')
    expect(wrapper.text()).toContain('commit_mismatch')
  })

  it('keeps the stage title outside a responsive keyboard-scrollable output region', () => {
    const wrapper = mount(BugCaseArtifacts, { props: { detail } })
    const card = wrapper.get('.attempt-output-card')
    const scroll = card.get('.attempt-output-scroll')

    expect(card.get(':scope > h3').text()).toBe('阶段输出')
    expect(scroll.attributes('role')).toBe('region')
    expect(scroll.attributes('aria-label')).toBe('阶段输出内容')
    expect(scroll.attributes('tabindex')).toBe('0')
    expect(scroll.findAll('.artifact-item')).toHaveLength(detail.attempts.length)
    expect(artifactSource).toMatch(/\.artifact-sections \{[^}]*grid-template-columns: repeat\(2, minmax\(0, 1fr\)\);/)
    expect(artifactSource).toMatch(/\.attempt-output-card \{[^}]*grid-column: 1 \/ -1;/)
    expect(artifactSource).toMatch(/\.attempt-output-scroll \{[^}]*height: clamp\(320px, 45vh, 640px\);/)
    expect(artifactSource).toMatch(/\.attempt-output-scroll \{[^}]*overflow-y: auto;/)
    expect(artifactSource).toMatch(/\.attempt-output-scroll \{[^}]*overflow-x: hidden;/)
    expect(artifactSource).toMatch(/\.attempt-output-scroll \{[^}]*scrollbar-gutter: stable;/)
    expect(artifactSource).toMatch(/\.attempt-output-scroll \{[^}]*overscroll-behavior: contain;/)
    expect(artifactSource).toContain('.attempt-output-scroll:focus-visible')
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
    expect(wrapper.get('.attempt-output-scroll').find('.legacy-attempt').exists()).toBe(true)
    expect(wrapper.get('.attempt-output-card > h3').text()).toBe('阶段输出')
  })

  it('stacks artifact cards and scales the output viewport on narrow screens', () => {
    expect(artifactSource).toMatch(/@media \(max-width: 899px\)[\s\S]*?\.artifact-sections \{ grid-template-columns: minmax\(0, 1fr\); \}/)
    expect(artifactSource).toMatch(/@media \(max-width: 899px\)[\s\S]*?\.attempt-output-card \{ grid-column: auto; \}/)
    expect(artifactSource).toMatch(/@media \(max-width: 899px\)[\s\S]*?\.attempt-output-scroll \{ height: clamp\(280px, 42vh, 480px\); \}/)
  })

  it('renders hostile legacy Markdown as inert readable text without HTML or executable URLs', () => {
    const hostile = [
      '**结论可读**',
      '<IMG SRC=x ONERROR=alert(1)>',
      '[危险链接](JaVaScRiPt:alert(2))',
      '<svg/onload=alert(3)>',
      '&lt;img src=x onerror=alert(4)&gt;',
      '<ScRiPt>alert(5)</sCrIpT>',
      '[实体链接](jav&#x61;script:alert(6))',
    ].join('\n')
    const archived = {
      ...detail,
      case: { ...detail.case, status: 'legacy_archived' as const },
      attempts: [{ ...detail.attempts[0], id: 'legacy-hostile', phase: 'legacy' as const, output_json: { final_message: hostile } }],
    }

    const wrapper = mount(BugCaseArtifacts, { props: { detail: archived } })

    expect(wrapper.find('.legacy-final strong').text()).toBe('结论可读')
    expect(wrapper.findAll('.legacy-final img, .legacy-final script, .legacy-final svg, .legacy-final a')).toHaveLength(0)
    for (const element of wrapper.findAll('.legacy-final *')) {
      expect(Object.keys(element.attributes()).some(name => name.toLowerCase().startsWith('on'))).toBe(false)
      expect(`${element.attributes('href') || ''}${element.attributes('src') || ''}`.toLowerCase()).not.toContain('javascript:')
    }
    expect(wrapper.find('.legacy-final').text()).toContain('<IMG SRC=x ONERROR=alert(1)>')
    expect(wrapper.find('.legacy-final').text()).toContain('JaVaScRiPt:alert(2)')
    expect(wrapper.find('.legacy-final').text()).toContain('<svg/onload=alert(3)>')
    expect(wrapper.find('.legacy-final').text()).toContain('&lt;img src=x onerror=alert(4)&gt;')
    expect(wrapper.find('.legacy-final').text()).toContain('jav&#x61;script:alert(6)')
  })

  it('renders both reset relations as read-only navigable Case references', async () => {
    const resetDetail = {
      ...detail,
      case: {
        ...detail.case,
        reset_from_case_id: 'case-before-reset',
        superseded_by_case_id: 'case-after-reset',
      },
    }
    const wrapper = mount(BugCaseArtifacts, { props: { detail: resetDetail } })

    expect(wrapper.get('[aria-labelledby="reset-relations-title"]').text()).toContain('重置关系')
    const links = wrapper.findAll<HTMLAnchorElement>('[data-case-reference]')
    expect(links.map(link => link.text())).toEqual(['case-before-reset', 'case-after-reset'])
    expect(links.map(link => link.attributes('href'))).toEqual(['#case-before-reset', '#case-after-reset'])

    await links[0].trigger('click')
    await links[1].trigger('click')
    expect(wrapper.emitted('select-case')).toEqual([['case-before-reset'], ['case-after-reset']])
    expect(wrapper.find('[aria-labelledby="reset-relations-title"] button').exists()).toBe(false)
  })
})
