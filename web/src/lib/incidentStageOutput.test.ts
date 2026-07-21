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

    expect(view).toMatchObject({ phaseLabel: '验证', attemptStatusLabel: '需补证', resultLabel: '信息不足', tone: 'warning', environment: 'test' })
    expect(view.sections.map(section => section.title)).toEqual(['预期表现', '实际观察', '还需补充', '验证证据'])
    expect(view.sections[2].items).toEqual(['缺少测试账号', '缺少 Network 导出'])
    expect(view.sections[3].emptyText).toBe('尚无有效证据')
    expect(JSON.stringify(view)).not.toContain('scenario_hash')
  })

  it('marks structured evidence groups for individually labelled rendering', () => {
    const view = presentStageAttempt(attempt('validation', {
      verification_status: 'reproduced', environment: 'test', gaps: [],
      evidence: [{ kind: 'network', path: 'browser/network.json', captured_at: '2026-07-18T09:53:39Z', request_id: 'request-1' }],
    }))

    expect(view.sections[view.sections.length - 1]).toEqual({
      title: '验证证据',
      groupLabel: '证据',
      groups: [[
        { label: '类型', value: 'network' },
        { label: '路径', value: 'browser/network.json', mono: true },
        { label: '采集时间', value: '2026-07-18T09:53:39Z' },
        { label: 'Request ID', value: 'request-1', mono: true },
      ]],
    })
  })

  it('presents the nested phase result from the persisted completion-intent envelope', () => {
    const view = presentStageAttempt(attempt('validation', {
      kind: 'phase_completion_intent',
      version: 1,
      command: {
        CaseID: 'case-sensitive-1',
        AttemptID: 'validation-1',
        ExpectedVersion: 7,
        IdempotencyKey: 'agent-phase:validation-1',
        ActorID: 'validator-bot',
        Outcome: 'reproduced',
        OutputJSON: {
          verification_status: 'reproduced', environment: 'test',
          expected_behavior: '返回两名匹配用户', observed_behavior: '已稳定返回两名匹配用户',
          gaps: [], evidence: [], scenario_hash: 'internal-only',
        },
        ErrorCode: '', ErrorMessage: '', Usage: {}, CodeChanges: [],
      },
    }))

    expect(view).toMatchObject({ phaseLabel: '验证', resultLabel: '已复现', environment: 'test' })
    expect(view.sections.map(section => section.title)).toEqual(['预期表现', '实际观察', '验证证据'])
    const visible = JSON.stringify(view)
    expect(visible).toContain('已稳定返回两名匹配用户')
    for (const machineValue of ['phase_completion_intent', 'case-sensitive-1', 'agent-phase:validation-1', 'validator-bot', 'CaseID', 'IdempotencyKey', 'ActorID', 'OutputJSON', 'scenario_hash']) {
      expect(visible).not.toContain(machineValue)
    }
  })

  it.each([
    ['missing nested output', { kind: 'phase_completion_intent', version: 1, command: { CaseID: 'secret-case', ActorID: 'secret-actor' } }],
    ['malformed nested output', { kind: 'phase_completion_intent', version: 1, command: { OutputJSON: 'raw-command-json', IdempotencyKey: 'secret-key' }, arbitrary_envelope_field: 'secret-envelope' }],
  ])('safely presents a completion intent with %s', (_name, output) => {
    const view = presentStageAttempt(attempt('validation', output))

    expect(view).toMatchObject({ resultLabel: '结果待恢复', tone: 'info' })
    expect(view.sections).toEqual([{ title: '阶段结果', text: '阶段结果正在恢复，请稍后刷新' }])
    const visible = JSON.stringify(view)
    for (const machineValue of ['phase_completion_intent', 'secret-case', 'secret-actor', 'raw-command-json', 'secret-key', 'secret-envelope', 'CaseID', 'ActorID', 'IdempotencyKey', 'arbitrary_envelope_field']) {
      expect(visible).not.toContain(machineValue)
    }
  })

  it('presents investigation root cause and confidence', () => {
    const view = presentStageAttempt(attempt('investigation', {
      investigation_status: 'root_cause_ready', environment: 'test', root_cause: '昵称搜索只取首条精确匹配', confidence: 'high', gaps: [], evidence: [],
    }))

    expect(view).toMatchObject({ phaseLabel: '排障', resultLabel: '根因已确认', tone: 'success', environment: 'test' })
    expect(view.sections[0]).toMatchObject({ title: '根因结论', text: '昵称搜索只取首条精确匹配' })
    expect(view.sections[1]).toMatchObject({ title: '置信度', text: '高' })
  })

  it('presents a recovered legacy investigation failure as evidence required', () => {
    const view = presentStageAttempt(attempt('investigation', {
      investigation_status: 'insufficient_info', environment: 'test', root_cause: '前端可能重复渲染昵称', confidence: 'medium',
      gaps: ['缺少响应体', '缺少部署版本'], evidence: [],
    }, 'failed'))

    expect(view).toMatchObject({ phaseLabel: '排障', attemptStatusLabel: '需补证', resultLabel: '信息不足', tone: 'warning' })
    expect(view.sections.find(section => section.title === '需要你补充')?.items).toEqual(['缺少响应体', '缺少部署版本'])
  })

  it('separates automatic validation refresh, user blockers, and non-blocking scopes', () => {
    const view = presentStageAttempt(attempt('investigation', {
      investigation_status: 'insufficient_info', environment: 'test', confidence: 'medium',
      validation_gaps: ['Network 缺少响应体'], gaps: ['需要后台登录权限'], unchecked_scopes: ['未查询非关键指标'], evidence: [],
    }, 'failed'))

    expect(view.sections.find(section => section.title === '验证将自动补采')?.items).toEqual(['Network 缺少响应体'])
    expect(view.sections.find(section => section.title === '需要你补充')?.items).toEqual(['需要后台登录权限'])
    expect(view.sections.find(section => section.title === '非阻塞未覆盖')?.items).toEqual(['未查询非关键指标'])
    expect(view.sections.some(section => section.title === '还需补充')).toBe(false)
  })

  it('presents a successful regression result directly', () => {
    const view = presentStageAttempt(attempt('regression', {
      verification_status: 'fixed_verified', environment: 'test',
      expected_behavior: '昵称搜索返回全部匹配项', observed_behavior: '回归验证通过', gaps: [], evidence: [],
    }))

    expect(view).toMatchObject({ phaseLabel: '回归验证', resultLabel: '修复已验证', tone: 'success' })
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
      { title: '阶段结论', text: '旧版阶段结论' },
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

  it('keeps unknown fallback values readable without exposing machine key names', () => {
    const view = presentStageAttempt(attempt('investigation', {
      queue_depth_limit: 27,
      retry_window_seconds: '60 秒',
      custom_operator_notes: ['首次重试成功', '继续观察'],
      opaque_runtime_group: { current_value: true, threshold_value: 30 },
    }))

    const visible = JSON.stringify(view)
    for (const value of ['27', '60 秒', '首次重试成功', '继续观察', '是', '30']) expect(visible).toContain(value)
    for (const machineKey of ['queue_depth_limit', 'retry_window_seconds', 'custom_operator_notes', 'opaque_runtime_group', 'current_value', 'threshold_value', 'queue depth limit', 'retry window seconds']) {
      expect(visible).not.toContain(machineKey)
    }
    expect(view.sections.map(section => section.title)).toEqual(['补充信息', '补充信息', '补充信息', '补充信息'])
  })
})
