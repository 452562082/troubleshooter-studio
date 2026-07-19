import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { IncidentPhaseEvent, PhaseAttempt } from '../lib/bridge/bugWorkflow'
import BugAgentProgress from './BugAgentProgress.vue'

function attempt(phase: 'investigation' | 'fix' = 'investigation', status: PhaseAttempt['status'] = 'running'): PhaseAttempt {
  return { id: `${phase}-1`, case_id: 'case-1', cycle_number: 1, phase, mode: '', status, agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }
}

describe('BugAgentProgress', () => {
  it('shows live commands, tool calls, and Agent messages for an active investigation', () => {
    const events: IncidentPhaseEvent[] = [
      { at: '2026-07-18T10:00:00Z', type: 'turn_started', message: '开始排障', meta: {} },
      { at: '2026-07-18T10:00:01Z', type: 'command_execution', message: 'rg -n navigation web/src', meta: { state: 'started' } },
      { at: '2026-07-18T10:00:02Z', type: 'command_execution', message: 'go test ./...', meta: { state: 'completed', exit_code: 0 } },
      { at: '2026-07-18T10:00:03Z', type: 'mcp_tool_call', message: 'grafana/query', meta: { state: 'started' } },
      { at: '2026-07-18T10:00:04Z', type: 'agent_message', message: '已定位到布局计算逻辑', raw: { password: 'hidden' }, meta: {} },
    ]
    const wrapper = mount(BugAgentProgress, { props: { attempt: attempt(), events } })

    expect(wrapper.get('[data-agent-phase="investigation"]').text()).toContain('排障 Agent 正在执行')
    expect(wrapper.text()).toContain('codex · 实时更新')
    expect(wrapper.text()).toContain('正在执行命令')
    expect(wrapper.text()).toContain('命令执行完成 · exit 0')
    expect(wrapper.text()).toContain('正在调用工具')
    expect(wrapper.text()).toContain('已定位到布局计算逻辑')
    expect(wrapper.text()).not.toContain('hidden')
  })

  it('shows an explicit waiting state before the first event and hides after the phase stops', async () => {
    const wrapper = mount(BugAgentProgress, { props: { attempt: attempt('fix'), events: [] } })

    expect(wrapper.text()).toContain('修复 Agent 正在执行')
    expect(wrapper.text()).toContain('等待第一条执行事件')

    await wrapper.setProps({ attempt: attempt('fix', 'succeeded') })
    expect(wrapper.find('.agent-progress').exists()).toBe(false)
  })

  it('does not duplicate browser progress inside the Agent panel', () => {
    const wrapper = mount(BugAgentProgress, { props: {
      attempt: attempt(),
      events: [{ type: 'browser_progress', message: 'Cookie: secret', raw: { token: 'hidden' }, meta: { browser_code: 'browser_starting' } }],
    } })

    expect(wrapper.text()).toContain('等待第一条执行事件')
    expect(wrapper.text()).not.toContain('Cookie')
    expect(wrapper.text()).not.toContain('hidden')
  })

  it('shows the trusted seven-step investigation progress separately from command events', () => {
    const events: IncidentPhaseEvent[] = [
      { at: '2026-07-18T10:00:00Z', type: 'phase_step', message: 'untrusted label', meta: { phase: 'investigation', step_key: 'evidence_handoff', step_index: 1, step_total: 7, state: 'running' } },
      { at: '2026-07-18T10:00:01Z', type: 'phase_step', message: 'untrusted label', meta: { phase: 'investigation', step_key: 'timeline', step_index: 2, step_total: 7, state: 'running' } },
      { at: '2026-07-18T10:00:02Z', type: 'phase_step', message: 'untrusted label', meta: { phase: 'investigation', step_key: 'runtime_scope', step_index: 3, step_total: 7, state: 'running' } },
      { at: '2026-07-18T10:00:03Z', type: 'command_execution', message: 'kubectl get pods', meta: { state: 'started' } },
    ]
    const wrapper = mount(BugAgentProgress, { props: { attempt: attempt(), events } })

    expect(wrapper.get('.investigation-step-progress').text()).toContain('第 3/7 步 · 运行时')
    expect(wrapper.get('[aria-current="step"]').text()).toContain('运行时')
    expect(wrapper.findAll('.investigation-step-progress li.is-complete')).toHaveLength(2)
    expect(wrapper.text()).toContain('kubectl get pods')
    expect(wrapper.text()).not.toContain('untrusted label')
  })
})
