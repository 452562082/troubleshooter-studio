import { describe, expect, it } from 'vitest'
import type { PhaseAttempt } from './bridge/bugWorkflow'
import { presentStageAttempt } from './incidentStageOutput'

function attempt(phase: PhaseAttempt['phase'], output_json: Record<string, unknown>, status: PhaseAttempt['status'] = 'succeeded'): PhaseAttempt {
  return { id: `${phase}-1`, case_id: 'case-1', cycle_number: 1, phase, mode: phase === 'validation' ? 'reproduce' : phase === 'regression' ? 'regression' : '', status, agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json, parent_attempt_id: '', started_at: '2026-07-14T10:00:00Z', finished_at: '2026-07-14T10:01:00Z', error_code: '', error_message: '', usage: {} }
}

describe('presentStageAttempt', () => {
  it('presents validation results as conclusion-first Chinese sections', () => {
    const view = presentStageAttempt(attempt('validation', {
      verification_status: 'insufficient_info', environment: 'test',
      expected_behavior: '显示全部匹配用户', observed_behavior: 'APP 只显示一名用户',
      gaps: ['缺少测试账号', '缺少 Network 导出'], evidence: [], scenario_hash: 'internal-only',
    }, 'failed'))

    expect(view).toMatchObject({ phaseLabel: '验证', attemptStatusLabel: '失败', resultLabel: '信息不足', tone: 'warning', environment: 'test' })
    expect(view.sections.map(section => section.title)).toEqual(['预期表现', '实际观察', '还需补充', '验证证据'])
    expect(view.sections[2].items).toEqual(['缺少测试账号', '缺少 Network 导出'])
    expect(view.sections[3].emptyText).toBe('尚无有效证据')
    expect(JSON.stringify(view)).not.toContain('scenario_hash')
  })

  it('presents investigation root cause and confidence', () => {
    const view = presentStageAttempt(attempt('investigation', {
      investigation_status: 'root_cause_ready', environment: 'test', root_cause: '昵称搜索只取首条精确匹配', confidence: 'high', gaps: [], evidence: [],
    }))

    expect(view).toMatchObject({ phaseLabel: '排障', resultLabel: '根因已确认', tone: 'success', environment: 'test' })
    expect(view.sections[0]).toMatchObject({ title: '根因结论', fields: [{ label: '', value: '昵称搜索只取首条精确匹配' }] })
    expect(view.sections[1]).toMatchObject({ title: '置信度', fields: [{ label: '', value: '高' }] })
  })

  it('presents fix changes, tests, branches, deployment notice and risks', () => {
    const view = presentStageAttempt(attempt('fix', {
      fix_status: 'fixed_pushed', environment: 'test', deployment_notice: '请部署 api:test', blocked_reason: '',
      changes: [{ repo: 'api', summary: '修复模糊搜索合并逻辑' }],
      tests: [{ repo: 'api', commit: 'abc123', command: 'go test ./...', result: 'passed' }],
      branches: [{ repo: 'api', base_branch: 'main', fix_branch: 'fix/840', commit: 'abc123', pushed: true, target_environment_branch: 'test', push_remote: 'origin' }],
      risks: ['需要观察搜索延迟'], evidence: [],
    }))

    expect(view).toMatchObject({ phaseLabel: '修复', resultLabel: '修复已推送', tone: 'success', environment: 'test' })
    expect(view.sections.map(section => section.title)).toEqual(['部署说明', '代码变更', '测试结果', '分支与提交', '风险', '修复证据'])
    expect(view.sections[1].groups?.[0]).toContainEqual({ label: '仓库', value: 'api', mono: true })
    expect(view.sections[2].groups?.[0]).toContainEqual({ label: '命令', value: 'go test ./...', mono: true })
  })

  it('uses structured fallback content without serializing unknown output', () => {
    const view = presentStageAttempt(attempt('investigation', { summary: '旧版阶段结论', notes: ['第一项', '第二项'], metadata: { owner: 'search-team', retried: true } }))
    expect(view.resultLabel).toBe('阶段结果')
    expect(view.sections).toEqual([
      { title: '阶段结论', fields: [{ label: '', value: '旧版阶段结论' }] },
      { title: '备注', items: ['第一项', '第二项'] },
      { title: '元数据', groups: [[{ label: '负责人', value: 'search-team' }, { label: '是否重试', value: '是' }]] },
    ])
    const visibleText = view.sections.flatMap(section => [
      ...(section.fields || []).map(field => `${field.label}${field.value}`),
      ...(section.items || []),
      ...(section.groups || []).flatMap(group => group.map(field => `${field.label}${field.value}`)),
    ]).join(' ')
    expect(visibleText).not.toContain('{"')
  })
})
