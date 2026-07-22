import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import type { CaseStatus, IncidentCase, IncidentCaseDetail, TransitionEvent } from '../lib/bridge/bugWorkflow'
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

function timelineEvents(count: number, caseID = 'case-1'): TransitionEvent[] {
  return Array.from({ length: count }, (_, index) => ({
    id: `${caseID}-event-${index + 1}`,
    case_id: caseID,
    from_status: 'validating',
    to_status: 'waiting_evidence',
    event_type: `event_${index + 1}`,
    actor_type: 'agent',
    actor_id: 'validator',
    idempotency_key: `${caseID}-event-${index + 1}`,
    payload_json: {},
    created_at: `2026-07-11T${String(index + 10).padStart(2, '0')}:00:00Z`,
  }))
}

describe('BugCaseLifecycle', () => {
  it.each([
    ['waiting_fix_approval', '允许修复'],
    ['waiting_merge_approval', '允许合并基线和环境分支'],
    ['waiting_deployment', '已部署，开始验证'],
    ['waiting_evidence', '补充证据并继续'],
    ['legacy_archived', '从新一轮验证继续'],
  ] as Array<[CaseStatus, string]>)('maps %s to its one primary action', (status, label) => {
    expect(primaryActionFor(incident(status))?.label).toBe(label)
  })

  it('does not offer a primary action after regression succeeds', () => {
    expect(primaryActionFor(incident('fixed_verified'))).toBeUndefined()
  })

  it('uses a four-stage remediation flow and requires an audited action before regression', async () => {
    const snapshot = detail('waiting_remediation')
    snapshot.case.current_attempt_id = 'root-config'
    snapshot.attempts = [{
      id: 'root-config', case_id: 'case-1', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded',
      agent_target: 'codex', bot_key: 'base|codex', input_json: {}, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {},
      output_json: {
        investigation_status: 'root_cause_ready', root_cause_type: 'configuration',
        remediation: { mode: 'operator_action', target: 'one2all/base-test', summary: '回滚错误配置', rollback: '恢复当前配置快照', verification: '重新执行原始场景' },
      },
    }]
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    expect(primaryActionFor(snapshot)).toEqual({ kind: 'complete_remediation', label: '确认处置完成并回归', approval: true })
    expect(wrapper.findAll('.lifecycle-stage')).toHaveLength(4)
    expect(wrapper.get('.stage-progress').classes()).toContain('is-remediation')
    expect(wrapper.get('.current-action-card').text()).toContain('根因不需要修改代码')

    await wrapper.get('.primary-action').trigger('click')
    const confirm = wrapper.get<HTMLButtonElement>('[data-confirm]')
    expect(wrapper.get('[role="dialog"]').text()).toContain('one2all/base-test')
    expect(wrapper.get('[role="dialog"]').text()).toContain('回滚错误配置')
    expect(confirm.element.disabled).toBe(true)
    await wrapper.get('#remediation-summary').setValue('已回滚 dataId base.yml 至版本 42')
    await wrapper.get('#remediation-evidence').setValue('变更单 CFG-42，Grafana 告警已恢复')
    expect(confirm.element.disabled).toBe(false)
    await confirm.trigger('click')
    expect(wrapper.emitted('primary')).toEqual([[{
      kind: 'complete_remediation', rootCauseAttemptID: 'root-config', caseVersion: 2,
      input: '已回滚 dataId base.yml 至版本 42', evidence: '变更单 CFG-42，Grafana 告警已恢复',
    }]])
  })

  it.each(['browser_login_required', 'browser_runtime_broken', 'validator_not_installed', 'browser_verifier_failed'])('does not map browser system code %s to the generic evidence textarea', errorCode => {
    const snapshot = detail('waiting_evidence')
    snapshot.case.current_attempt_id = 'validation-1'
    snapshot.attempts = [{ id: 'validation-1', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { error_code: errorCode }, parent_attempt_id: '', started_at: '', error_code: errorCode, error_message: 'private runtime path', usage: {} }]

    expect(primaryActionFor(snapshot)).toBeUndefined()
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })
    expect(wrapper.find('.primary-action').exists()).toBe(false)
    expect(wrapper.find('#case-supplement').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('private runtime path')
  })

  it('does not ask the user for evidence when automatic validation refresh is exhausted', () => {
    const snapshot = detail('waiting_evidence')
    snapshot.case.current_attempt_id = 'investigation-refresh-exhausted'
    snapshot.attempts = [{ id: 'investigation-refresh-exhausted', case_id: 'case-1', cycle_number: 1, phase: 'investigation', mode: '', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { error_code: 'validation_evidence_refresh_exhausted' }, parent_attempt_id: '', started_at: '', error_code: 'validation_evidence_refresh_exhausted', error_message: '', usage: {} }]

    expect(primaryActionFor(snapshot)).toBeUndefined()
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })
    expect(wrapper.find('.primary-action').exists()).toBe(false)
    expect(wrapper.find('#case-supplement').exists()).toBe(false)
    expect(wrapper.get('.current-action-card').text()).toContain('已停止自动循环')
  })

  it('retries an invalid browser plan inside the current Case without an evidence dialog', async () => {
    const snapshot = detail('waiting_evidence')
    snapshot.case.current_attempt_id = 'validation-plan'
    snapshot.attempts = [{ id: 'validation-plan', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: { mode: 'reproduce' }, output_json: { error_code: 'browser_validator_plan_invalid' }, parent_attempt_id: '', started_at: '', error_code: 'browser_validator_plan_invalid', error_message: 'raw rejected output', usage: {} }]

    expect(primaryActionFor(snapshot)).toEqual({ kind: 'retry_validation', label: '重新生成验证计划并重试' })
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })
    expect(wrapper.get('[data-browser-state="plan"]').text()).toContain('当前 Case')
    expect(wrapper.get('.primary-action').text()).toBe('重新生成验证计划并重试')
    await wrapper.get('.primary-action').trigger('click')
    expect(wrapper.emitted('primary')).toEqual([[{ kind: 'retry_validation' }]])
    expect(wrapper.find('#case-supplement').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('raw rejected output')
  })

  it('retries an invalid locator repair plan inside the current Case', () => {
    const snapshot = detail('waiting_evidence')
    snapshot.case.current_attempt_id = 'repair-plan'
    snapshot.attempts = [{ id: 'repair-plan', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { error_code: 'browser_locator_repair_plan_invalid' }, parent_attempt_id: '', started_at: '', error_code: 'browser_locator_repair_plan_invalid', error_message: 'raw rejected repair', usage: {} }]

    expect(primaryActionFor(snapshot)).toEqual({ kind: 'retry_validation', label: '重新生成验证计划并重试' })
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })
    expect(wrapper.get('[data-browser-state="plan"]').text()).toContain('页面定位修复计划')
    expect(wrapper.text()).not.toContain('raw rejected repair')
  })

  it('retries a failed validation agent inside the current Case without rebuilding it', async () => {
    const snapshot = detail('waiting_evidence')
    snapshot.case.current_attempt_id = 'validation-agent-failed'
    snapshot.attempts = [{ id: 'validation-agent-failed', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'claude-code', bot_key: 'base|claude-code', input_json: { mode: 'reproduce' }, output_json: { error_code: 'browser_validator_failed' }, parent_attempt_id: '', started_at: '', error_code: 'browser_validator_failed', error_message: 'private provider failure', usage: {} }]

    expect(primaryActionFor(snapshot)).toEqual({ kind: 'retry_validation', label: '重试当前验证' })
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })
    expect(wrapper.get('[data-browser-state="retry"]').text()).toContain('当前 Case')
    expect(wrapper.get('.primary-action').text()).toBe('重试当前验证')
    await wrapper.get('.primary-action').trigger('click')
    expect(wrapper.emitted('primary')).toEqual([[{ kind: 'retry_validation' }]])
    expect(wrapper.find('#case-supplement').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('private provider failure')
  })

  it.each([
    ['browser_validator_timeout', 'process'],
    ['browser_validator_attachment_failed', 'attachment'],
    ['browser_validator_no_output', 'process'],
    ['browser_validator_process_failed', 'process'],
    ['browser_validator_configuration_invalid', 'configuration'],
    ['browser_worker_protocol_invalid', 'system'],
  ])('retries classified validation agent failure %s inside the current Case', async (errorCode, browserState) => {
    const snapshot = detail('waiting_evidence')
    snapshot.case.current_attempt_id = 'validation-agent-classified'
    snapshot.attempts = [{ id: 'validation-agent-classified', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'claude-code', bot_key: 'base|claude-code', input_json: { mode: 'reproduce' }, output_json: { error_code: errorCode }, parent_attempt_id: '', started_at: '', error_code: errorCode, error_message: 'private provider failure', usage: {} }]

    expect(primaryActionFor(snapshot)).toEqual({ kind: 'retry_validation', label: '重试当前验证' })
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })
    expect(wrapper.find(`[data-browser-state="${browserState}"]`).exists()).toBe(true)
    expect(wrapper.get('.primary-action').text()).toBe('重试当前验证')
    await wrapper.get('.primary-action').trigger('click')
    expect(wrapper.emitted('primary')).toEqual([[{ kind: 'retry_validation' }]])
    expect(wrapper.find('#case-supplement').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('private provider failure')
  })

  it('renders login recovery controls and forwards exact browser actions', async () => {
    const snapshot = detail('waiting_evidence')
    snapshot.case.current_attempt_id = 'validation-login'
    snapshot.attempts = [{ id: 'validation-login', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { error_code: 'browser_login_required', application_origin: 'https://app.test', login_origin: 'https://login.test' }, parent_attempt_id: '', started_at: '', error_code: 'browser_login_required', error_message: '', usage: {} }]
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    expect(wrapper.get('[data-browser-state="login"]').text()).toContain('需要登录')
    await wrapper.get('[data-browser-action="login"]').trigger('click')
    await wrapper.get('[data-browser-action="clear-session"]').trigger('click')
    expect(wrapper.emitted('browser')).toEqual([['login'], ['clear-session']])
  })

  it.each([
    ['browser_assertion_failed', '补充业务预期并重试'],
  ])('keeps browser evidence gap %s distinct from system recovery', async (errorCode, expectedLabel) => {
    const snapshot = detail('waiting_evidence')
    snapshot.case.current_attempt_id = 'validation-gap'
    snapshot.attempts = [{ id: 'validation-gap', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { error_code: errorCode }, parent_attempt_id: '', started_at: '', error_code: errorCode, error_message: '', usage: {} }]

    expect(primaryActionFor(snapshot)?.label).toBe(expectedLabel)
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })
    expect(wrapper.get('.primary-action').text()).toBe(expectedLabel)
    await wrapper.get('.primary-action').trigger('click')
    expect(wrapper.find('#case-supplement').exists()).toBe(true)
    expect(wrapper.find('[data-browser-action="repair-runtime"]').exists()).toBe(false)
  })

  it('retries live observation after the single locator repair is exhausted', async () => {
    const snapshot = detail('waiting_evidence')
    snapshot.case.current_attempt_id = 'validation-locator'
    snapshot.attempts = [{ id: 'validation-locator', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { error_code: 'browser_locator_failed' }, parent_attempt_id: '', started_at: '', error_code: 'browser_locator_failed', error_message: '', usage: {} }]

    expect(primaryActionFor(snapshot)).toEqual({ kind: 'retry_validation', label: '重新观察页面并生成验证计划' })
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })
    await wrapper.get('.primary-action').trigger('click')
    expect(wrapper.emitted('primary')).toEqual([[{ kind: 'retry_validation' }]])
    expect(wrapper.find('#case-supplement').exists()).toBe(false)
  })

  it('routes a missing frontend URL to Bug synchronization without generic evidence input', async () => {
    const snapshot = detail('waiting_evidence')
    snapshot.case.current_attempt_id = 'validation-url'
    snapshot.attempts = [{ id: 'validation-url', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { error_code: 'browser_url_required' }, parent_attempt_id: '', started_at: '', error_code: 'browser_url_required', error_message: 'raw URL error /private/path', usage: {} }]

    expect(primaryActionFor(snapshot)).toBeUndefined()
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })
    expect(wrapper.find('.primary-action').exists()).toBe(false)
    expect(wrapper.find('#case-supplement').exists()).toBe(false)
    expect(wrapper.text()).toContain('来源工单')
    expect(wrapper.text()).not.toContain('/private/path')
    await wrapper.get('[data-browser-action="edit-bug-url"]').trigger('click')
    expect(wrapper.emitted('browser')).toEqual([['edit-bug-url']])
  })

  it('explains that a failed regression carried fresh evidence into the next cycle', () => {
    const snapshot = detail('investigating')
    snapshot.case.cycle_number = 2
    snapshot.events.push({ id: 'regression-failed', case_id: 'case-1', from_status: 'regression_validating', to_status: 'still_reproduces', event_type: 'regression_failed', actor_type: 'agent', actor_id: 'validator', idempotency_key: 'regression-failed', payload_json: {}, created_at: '2026-07-11T12:00:00Z' })
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    expect(wrapper.find('.current-action-card').text()).toContain('回归仍复现')
    expect(wrapper.find('.current-action-card').text()).toContain('新证据和差分')
    expect(wrapper.find('.current-action-card').text()).toContain('第 2 轮')
    expect(wrapper.get('.workflow-loop-hint').text()).toContain('回归仍复现')
    expect(wrapper.get('.workflow-loop-hint').text()).toContain('下一轮')
    expect(wrapper.get('.workflow-loop-hint').text()).toContain('排障')
  })

  it('mounts live Agent progress for the current investigation attempt', () => {
    const snapshot = detail('investigating')
    snapshot.case.current_attempt_id = 'investigation-1'
    snapshot.attempts = [{ id: 'investigation-1', case_id: 'case-1', cycle_number: 1, phase: 'investigation', mode: '', status: 'running', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }]
    const wrapper = mount(BugCaseLifecycle, { props: {
      detail: snapshot,
      phaseEvents: [{ type: 'command_execution', message: 'rg -n layout web/src', meta: { case_id: 'case-1', attempt_id: 'investigation-1', state: 'started' } }],
    } })

    expect(wrapper.get('[data-agent-phase="investigation"]').text()).toContain('排障 Agent 正在执行')
    expect(wrapper.get('[data-agent-phase="investigation"]').text()).toContain('rg -n layout web/src')
  })

  it('keeps waiting evidence on the investigation stage that produced it', () => {
    const snapshot = detail('waiting_evidence')
    snapshot.case.current_attempt_id = 'investigation-blocked'
    snapshot.attempts = [{
      id: 'investigation-blocked', case_id: 'case-1', cycle_number: 1, phase: 'investigation', mode: '', status: 'failed',
      agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { investigation_status: 'insufficient_info', gaps: ['missing deployed revision'] },
      parent_attempt_id: 'validation-1', started_at: '', error_code: '', error_message: '', usage: {},
    }]
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    expect(wrapper.findAll('.lifecycle-stage').map(stage => stage.attributes('data-state'))).toEqual([
      'complete', 'blocked', 'pending', 'pending', 'pending', 'pending',
    ])
    expect(wrapper.findAll('.lifecycle-stage').map(stage => stage.text())).toEqual([
      '1验证已完成', '2排障需处理', '3修复未开始', '4合并未开始', '5部署未开始', '6回归未开始',
    ])
    expect(wrapper.get('.primary-action').text()).toContain('补充权限或外部资料并继续')
  })

  it('labels an automatic validation evidence refresh instead of looking like an unexplained reset', () => {
    const snapshot = detail('validating')
    snapshot.case.current_attempt_id = 'validation-refresh'
    snapshot.attempts = [{
      id: 'validation-refresh', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'running',
      agent_target: 'codex', bot_key: 'base|codex', input_json: { source_investigation_attempt_id: 'investigation-1', evidence_refresh_gaps: ['Network 缺少响应体'] }, output_json: {},
      parent_attempt_id: 'validation-1', started_at: '', error_code: '', error_message: '', usage: {},
    }]
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    expect(wrapper.get('.status-pill').text()).toBe('排障中 · 自动补采')
    expect(wrapper.get('.current-action-card').text()).toContain('排障中 · 自动补采')
    expect(wrapper.findAll('.lifecycle-stage').slice(0, 2).map(stage => stage.text())).toEqual([
      '1验证已完成', '2排障自动补采中',
    ])
  })

  it('shows that only a successful regression resolves the source Bug ticket', () => {
    const snapshot = detail('fixed_verified')
    snapshot.bug_ticket_resolution = { state: 'resolved', source_status: 'resolved' }
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    expect(wrapper.get('.workflow-loop-hint').text()).toContain('回归通过')
    expect(wrapper.get('.workflow-loop-hint').text()).toContain('Bug 工单已转为已解决')
    expect(wrapper.find('.primary-action').exists()).toBe(false)
  })

  it('does not claim the source Bug is resolved while status synchronization is pending', () => {
    const snapshot = detail('fixed_verified')
    snapshot.bug_ticket_resolution = { state: 'pending', source_status: 'active' }
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    expect(wrapper.get('.workflow-loop-hint').text()).toContain('正在将 Bug 工单同步为已解决')
    expect(wrapper.get('.workflow-loop-hint').text()).not.toContain('Bug 工单已转为已解决')
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

  it('accepts screenshots as supplemental evidence without requiring text', async () => {
    const snapshot = detail('not_reproduced')
    snapshot.case.current_attempt_id = 'validation-1'
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    class EvidenceFileReader {
      result: string | ArrayBuffer | null = null
      onload: null | (() => void) = null
      onerror: null | (() => void) = null
      readAsDataURL() {
        this.result = 'data:image/png;base64,iVBORw=='
        queueMicrotask(() => this.onload?.())
      }
    }
    vi.stubGlobal('FileReader', EvidenceFileReader)

    await wrapper.get('.primary-action').trigger('click')
    const confirm = wrapper.get<HTMLButtonElement>('[data-confirm]')
    expect(confirm.element.disabled).toBe(true)
    const fileInput = wrapper.get<HTMLInputElement>('#case-evidence-images')
    const file = new File([new Uint8Array([137, 80, 78, 71])], '搜索结果.png', { type: 'image/png' })
    Object.defineProperty(fileInput.element, 'files', { configurable: true, value: [file] })
    await fileInput.trigger('change')
    await flushPromises()

    expect(wrapper.get('.evidence-image-list').text()).toContain('搜索结果.png')
    expect(confirm.element.disabled).toBe(false)
    await confirm.trigger('click')
    const emitted = wrapper.emitted('primary')
    expect(emitted).toHaveLength(1)
    expect(emitted?.[0]?.[0]).toMatchObject({
      kind: 'supply_evidence',
      input: '',
      images: [{ name: '搜索结果.png', mime_type: 'image/png' }],
    })
    expect((emitted?.[0]?.[0] as { images: Array<{ base64_data: string }> }).images[0].base64_data).toBe('iVBORw==')
    vi.unstubAllGlobals()
  })

  it('renders one full-width current Case and refreshes from its heading', async () => {
    const snapshot = detail('waiting_fix_approval')
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot, bugTitle: '支付页超时' } })
    const columns = wrapper.findAll('.case-column')
    const source = readFileSync('src/components/BugCaseLifecycle.vue', 'utf8')

    expect(columns).toHaveLength(2)
    expect(columns[0].classes()).toContain('case-main-column')
    expect(columns[1].classes()).toContain('case-detail-column')
    expect(wrapper.find('.case-list-column').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('故障 Cases')
    expect('cases' in wrapper.props()).toBe(false)
    expect(wrapper.find('.case-heading-copy').exists()).toBe(true)
    expect(wrapper.find('.case-heading-actions').exists()).toBe(true)
    expect(wrapper.get('.case-heading-copy').text()).toContain('故障闭环进度')
    expect(wrapper.get('.case-heading-copy').text()).toContain('支付页超时')
    expect(wrapper.get('.case-heading-copy').text()).toContain('第 1 轮 · test')
    expect(wrapper.get('.case-heading-copy').text()).not.toContain(snapshot.case.id)
    expect(wrapper.get('.case-heading-copy').text()).not.toContain(snapshot.case.bug_id)
    expect(source).toMatch(/\.case-lifecycle \{[^}]*grid-template-columns: minmax\(0, 1fr\);/)
    expect(source).not.toContain('.case-row')
    expect(wrapper.findAll('.lifecycle-stage')).toHaveLength(6)
    expect(wrapper.find('[aria-label="故障处理阶段"]').exists()).toBe(true)
    expect(wrapper.find('[aria-label="Case 时间线"]').text()).toContain('transition')
    expect(wrapper.findAll('.current-action-card .primary-action')).toHaveLength(1)

    const refresh = wrapper.get<HTMLButtonElement>('[aria-label="刷新故障闭环"]')
    expect(refresh.classes()).toContain('icon-button')
    await refresh.trigger('click')
    expect(wrapper.emitted('refresh')).toEqual([[]])
  })

  it('keeps the current-stage primary action without exposing a duplicate reset action', () => {
    const snapshot = detail('waiting_fix_approval')
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    expect(wrapper.findAll('.current-action-card .primary-action')).toHaveLength(1)
    expect(wrapper.findAll('.current-action-card .reconsider-action')).toHaveLength(1)
    expect(wrapper.find('.reset-action').exists()).toBe(false)
    expect(wrapper.emitted('primary')).toBeUndefined()
  })

  it.each(['fixed_verified', 'legacy_archived', 'reset_archived'] as CaseStatus[])('does not offer reset for terminal state %s', status => {
    const wrapper = mount(BugCaseLifecycle, { props: { detail: detail(status) } })
    expect(wrapper.find('.reset-action').exists()).toBe(false)
  })

  it('makes the current Case heading a programmatic focus target', () => {
    const wrapper = mount(BugCaseLifecycle, { props: { detail: detail('validating') } })
    expect(wrapper.get('.case-heading').attributes('tabindex')).toBe('-1')
  })

  it('opens an accessible approval dialog before emitting approval', async () => {
    const wrapper = mount(BugCaseLifecycle, { props: { detail: detail('waiting_merge_approval') } })

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
    snapshot.attempts = [{ id: 'investigation-7', case_id: 'case-1', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { investigation_status: 'root_cause_ready', confidence: 'high', gaps: [], call_chain: [{ repo: 'admin-web' }] }, parent_attempt_id: '', started_at: '2026-07-11T10:00:00Z', error_code: '', error_message: '', usage: {} }]
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    await wrapper.find('.primary-action').trigger('click')
    const baselineInputs = wrapper.findAll('.source-baseline-row input')
    expect((baselineInputs[0].element as HTMLInputElement).value).toBe('admin-web')
    await baselineInputs[1].setValue('feature/new-navigation')
    await wrapper.setProps({ detail: { ...snapshot, case: { ...snapshot.case, version: 8 } } })
    await wrapper.find('[data-confirm]').trigger('click')

    expect(wrapper.emitted('primary')?.[0]).toEqual([{ kind: 'approve_fix', rootCauseAttemptID: 'investigation-7', caseVersion: 7, sourceBaselines: { 'admin-web': 'feature/new-navigation' } }])
  })

  it('submits a user remediation proposal for reassessment without authorizing a fix', async () => {
    const snapshot = detail('waiting_fix_approval')
    snapshot.case.version = 7
    snapshot.case.current_attempt_id = 'investigation-7'
    snapshot.attempts = [{ id: 'investigation-7', case_id: 'case-1', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { investigation_status: 'root_cause_ready', confidence: 'high', root_cause: '字段语义错配', gaps: [], call_chain: [{ repo: 'admin-web' }] }, parent_attempt_id: '', started_at: '2026-07-11T10:00:00Z', error_code: '', error_message: '', usage: {} }]
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    expect(wrapper.get('.current-action-card').text()).toContain('前端、后端或其他修复思路')
    await wrapper.get('.reconsider-action').trigger('click')
    const dialog = wrapper.get('[role="dialog"]')
    expect(dialog.text()).toContain('只重新评估方案')
    expect(dialog.text()).toContain('不会授权修复 Agent')
    const confirm = wrapper.get<HTMLButtonElement>('[data-confirm]')
    expect(confirm.element.disabled).toBe(true)
    await wrapper.get('#remediation-proposal').setValue('优先由后端统一字段语义，前端只保留兼容兜底')
    await wrapper.setProps({ detail: { ...snapshot, case: { ...snapshot.case, version: 8 } } })
    expect(confirm.element.disabled).toBe(false)
    await confirm.trigger('click')

    expect(wrapper.emitted('primary')).toEqual([[{
      kind: 'reconsider_remediation',
      rootCauseAttemptID: 'investigation-7',
      caseVersion: 7,
      input: '优先由后端统一字段语义，前端只保留兼容兜底',
    }]])
  })

  it('allows an empty baseline and delegates the environment-branch default to the host', async () => {
    const snapshot = detail('waiting_fix_approval')
    snapshot.case.current_attempt_id = 'investigation-default'
    snapshot.attempts = [{ id: 'investigation-default', case_id: 'case-1', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { investigation_status: 'root_cause_ready', confidence: 'high', gaps: [], call_chain: [{ repo: 'admin-web' }] }, parent_attempt_id: '', started_at: '2026-07-11T10:00:00Z', error_code: '', error_message: '', usage: {} }]
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    await wrapper.find('.primary-action').trigger('click')
    const confirm = wrapper.get<HTMLButtonElement>('[data-confirm]')
    expect(confirm.element.disabled).toBe(false)
    expect(wrapper.get('[role="dialog"]').text()).toContain('留空时默认使用当前环境对应的分支')
    await confirm.trigger('click')

    expect(wrapper.emitted('primary')?.[0]).toEqual([{ kind: 'approve_fix', rootCauseAttemptID: 'investigation-default', caseVersion: 2, sourceBaselines: { 'admin-web': '' } }])
  })

  it('previews target environment, expected commits, and verifier before deployment validation', async () => {
    const snapshot = detail('waiting_deployment')
    snapshot.case.current_attempt_id = 'fix-1'
    snapshot.code_changes = [{ id: 'change-1', case_id: 'case-1', attempt_id: 'fix-1', repo: 'api', base_branch: 'main', fix_branch: 'fix/1', fix_commit: 'fix-abc', test_evidence: [], target_environment_branch: 'test', merge_base_head: 'base', merge_commit: 'merge-def', push_remote: 'origin', push_status: 'pushed' }]
    snapshot.deployment_observations = [{ id: 'observation-1', case_id: 'case-1', environment: 'test', expected_commits: { api: 'merge-def' }, observed_version: '', observed_images: {}, observed_commits: {}, verification_source: 'version endpoint', result: 'unavailable' }]
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    await wrapper.find('.primary-action').trigger('click')

    expect(wrapper.find('[role="dialog"]').text()).toContain('test')
    expect(wrapper.find('[role="dialog"]').text()).toContain('api: merge-def')
    expect(wrapper.find('[role="dialog"]').text()).toContain('version endpoint')
    expect(wrapper.find('[role="dialog"]').text()).not.toContain('部署已确认')
  })

  it('confirms deployment without asking the user for a version or commit', async () => {
    const snapshot = detail('waiting_deployment')
    snapshot.case.current_attempt_id = 'fix-1'
    snapshot.code_changes = [
      { id: 'api', case_id: 'case-1', attempt_id: 'fix-1', repo: 'api', base_branch: 'main', fix_branch: 'fix/1', fix_commit: 'fix-api', test_evidence: [], target_environment_branch: 'test', merge_base_head: 'base', merge_commit: 'merge-api', push_remote: 'origin', push_status: 'pushed' },
      { id: 'worker', case_id: 'case-1', attempt_id: 'fix-1', repo: 'worker', base_branch: 'main', fix_branch: 'fix/1', fix_commit: 'fix-worker', test_evidence: [], target_environment_branch: 'test', merge_base_head: 'base', merge_commit: 'merge-worker', push_remote: 'origin', push_status: 'pushed' },
    ]
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    await wrapper.find('.primary-action').trigger('click')
    expect(wrapper.find('#observed-version').exists()).toBe(false)
    expect(wrapper.find('[id^="observed-commit-"]').exists()).toBe(false)
    expect(wrapper.find('[role="dialog"]').text()).toContain('无需填写版本号或 commit')
    await wrapper.find('[data-confirm]').trigger('click')

    expect(wrapper.emitted('primary')?.[0]).toEqual([{ kind: 'notify_deployed' }])
  })

  it('starts automatic HTTP verification without manual proof fields', async () => {
    const snapshot = detail('waiting_deployment')
    snapshot.deployment_verification = { provider: 'http', available: true, hint: 'HTTP 版本接口自动验证 · /git/commit' }
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })
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
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    await wrapper.find('.primary-action').trigger('click')

    expect(wrapper.find('[role="dialog"]').text()).toContain('api: merge-new')
    expect(wrapper.find('[role="dialog"]').text()).not.toContain('merge-old')
    expect(wrapper.find('[role="dialog"]').text()).toContain('manual')
  })

  it('restores focus to the primary action when an approval dialog closes', async () => {
    const snapshot = detail('waiting_fix_approval')
    snapshot.case.current_attempt_id = 'investigation-focus'
    snapshot.attempts = [{ id: 'investigation-focus', case_id: 'case-1', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { investigation_status: 'root_cause_ready', confidence: 'high', gaps: [] }, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }]
    const wrapper = mount(BugCaseLifecycle, { attachTo: document.body, props: { detail: snapshot } })
    const trigger = wrapper.find<HTMLButtonElement>('.primary-action')
    await trigger.trigger('click')
    await new Promise(resolve => setTimeout(resolve, 0))
    expect(document.activeElement).toBe(wrapper.find('.source-baseline-row input').element)

    await wrapper.find('[role="dialog"] footer .btn').trigger('click')
    await wrapper.vm.$nextTick()

    expect(document.activeElement).toBe(trigger.element)
    wrapper.unmount()
  })

  it('contains responsive no-overflow contracts for all supported viewport fixtures', () => {
    const wrapper = mount(BugCaseLifecycle, { props: { detail: detail('validating') } })
    expect(wrapper.find('.case-lifecycle').attributes('data-responsive-viewports')).toBe('375,768,1024,1440')
    expect(wrapper.find('.case-lifecycle').attributes('data-overflow-safe')).toBe('true')
    const source = readFileSync('src/components/BugCaseLifecycle.vue', 'utf8')
    expect(source).toMatch(/\.case-lifecycle \{[^}]*min-width: 0;[^}]*grid-template-columns: minmax\(0, 1fr\);/)
    expect(source).toMatch(/\.case-column \{[^}]*min-width: 0;/)
    expect(source).not.toContain('.case-list-column')
    expect(source).not.toContain('.case-row')
    expect(source).toContain('@media (max-width: 560px)')
  })

  it('allows all six stage columns to shrink until the 560px two-column breakpoint', () => {
    const source = readFileSync('src/components/BugCaseLifecycle.vue', 'utf8')

    expect(source).toMatch(/\.stage-progress \{[^}]*grid-template-columns: repeat\(6, minmax\(0, 1fr\)\);/)
    expect(source).toMatch(/@media \(max-width: 560px\)[\s\S]*?\.stage-progress \{ grid-template-columns: repeat\(2, minmax\(0, 1fr\)\); \}/)
  })

  it('previews the newest three timeline events and expands or collapses the full history', async () => {
    const snapshot = detail('waiting_evidence')
    snapshot.events = timelineEvents(6)
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    expect(wrapper.find('.timeline-heading').text()).toContain('共 6 条')
    expect(wrapper.findAll('#case-timeline-events li').map(item => item.find('strong').text())).toEqual([
      'event_6', 'event_5', 'event_4',
    ])

    const toggle = wrapper.get<HTMLButtonElement>('.timeline-toggle')
    expect(toggle.text()).toContain('展开全部')
    expect(toggle.attributes('aria-expanded')).toBe('false')
    expect(toggle.attributes('aria-controls')).toBe('case-timeline-events')

    await toggle.trigger('click')
    expect(wrapper.findAll('#case-timeline-events li')).toHaveLength(6)
    expect(wrapper.get('.timeline-toggle').text()).toContain('收起')
    expect(wrapper.get('.timeline-toggle').attributes('aria-expanded')).toBe('true')
    expect(wrapper.get('#case-timeline-events').classes()).toContain('is-expanded')

    await wrapper.get('.timeline-toggle').trigger('click')
    expect(wrapper.findAll('#case-timeline-events li').map(item => item.find('strong').text())).toEqual([
      'event_6', 'event_5', 'event_4',
    ])
    expect(wrapper.get('.timeline-toggle').attributes('aria-expanded')).toBe('false')
  })

  it.each([1, 3])('does not show a timeline toggle for %i events', count => {
    const snapshot = detail('waiting_evidence')
    snapshot.events = timelineEvents(count)
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    expect(wrapper.find('.timeline-heading').text()).toContain(`共 ${count} 条`)
    expect(wrapper.find('.timeline-toggle').exists()).toBe(false)
    expect(wrapper.findAll('#case-timeline-events li')).toHaveLength(count)
  })

  it('keeps the timeline empty state without an unnecessary toggle', () => {
    const snapshot = detail('waiting_evidence')
    snapshot.events = []
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    expect(wrapper.find('.timeline-heading').text()).toContain('共 0 条')
    expect(wrapper.find('.timeline-toggle').exists()).toBe(false)
    expect(wrapper.find('#case-timeline-events').exists()).toBe(false)
    expect(wrapper.find('.timeline .empty-state').text()).toBe('暂无状态事件')
  })

  it('preserves expansion for same-Case updates and collapses when the Case changes', async () => {
    const caseA = detail('waiting_evidence')
    caseA.events = timelineEvents(6, 'case-1')
    const wrapper = mount(BugCaseLifecycle, { props: { detail: caseA } })

    await wrapper.get('.timeline-toggle').trigger('click')
    const updatedCaseA = { ...caseA, events: timelineEvents(7, 'case-1') }
    await wrapper.setProps({ detail: updatedCaseA })
    expect(wrapper.get('.timeline-toggle').attributes('aria-expanded')).toBe('true')
    expect(wrapper.findAll('#case-timeline-events li')).toHaveLength(7)

    const caseB = { ...detail('waiting_evidence'), case: incident('waiting_evidence', 'case-2'), events: timelineEvents(6, 'case-2') }
    await wrapper.setProps({ detail: caseB })
    expect(wrapper.get('.timeline-toggle').attributes('aria-expanded')).toBe('false')
    expect(wrapper.findAll('#case-timeline-events li').map(item => item.find('strong').text())).toEqual([
      'event_6', 'event_5', 'event_4',
    ])
  })

  it('clears stale expansion when the event count no longer needs a toggle', async () => {
    const snapshot = detail('waiting_evidence')
    snapshot.events = timelineEvents(6)
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    await wrapper.get('.timeline-toggle').trigger('click')
    await wrapper.setProps({ detail: { ...snapshot, events: timelineEvents(3) } })
    expect(wrapper.find('.timeline-toggle').exists()).toBe(false)
    expect(wrapper.findAll('#case-timeline-events li')).toHaveLength(3)

    await wrapper.setProps({ detail: { ...snapshot, events: timelineEvents(6) } })
    expect(wrapper.get('.timeline-toggle').attributes('aria-expanded')).toBe('false')
    expect(wrapper.findAll('#case-timeline-events li').map(item => item.find('strong').text())).toEqual([
      'event_6', 'event_5', 'event_4',
    ])
  })

  it('contains bounded timeline scrolling and accessible toggle style contracts', () => {
    const source = readFileSync('src/components/BugCaseLifecycle.vue', 'utf8')

    expect(source).toMatch(/\.timeline-events\.is-expanded \{[^}]*max-height: clamp\(280px, 38vh, 520px\);/)
    expect(source).toMatch(/\.timeline-events\.is-expanded \{[^}]*overflow-y: auto;/)
    expect(source).toMatch(/\.timeline-events\.is-expanded \{[^}]*overflow-x: hidden;/)
    expect(source).toMatch(/\.timeline-events\.is-expanded \{[^}]*overscroll-behavior: contain;/)
    expect(source).toMatch(/\.timeline-events\.is-expanded \{[^}]*scrollbar-gutter: stable;/)
    expect(source).toMatch(/\.timeline-heading \{[^}]*flex-wrap: wrap;/)
    expect(source).toMatch(/\.timeline-toggle \{[^}]*min-width: 44px;[^}]*min-height: 44px;/)
    expect(source).toMatch(/\.timeline-toggle:focus-visible \{[^}]*outline:/)
    expect(source).toMatch(/\.timeline-toggle-icon \{[^}]*transition: transform 180ms ease;/)
    expect(source).toMatch(/@media \(prefers-reduced-motion: reduce\)/)
  })

  it('scopes timeline count styling away from the toggle label without replacing visible count text', () => {
    const snapshot = detail('waiting_evidence')
    snapshot.events = timelineEvents(6)
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })
    const source = readFileSync('src/components/BugCaseLifecycle.vue', 'utf8')

    expect.soft(source).not.toMatch(/\.timeline-heading span \{/)
    expect.soft(source).toMatch(/\.timeline-count \{[^}]*color: var\(--c-muted\);[^}]*font-size: var\(--fs-xs\);/)

    const count = wrapper.find('.timeline-count')
    expect.soft(count.exists()).toBe(true)
    if (count.exists()) {
      expect.soft(count.text()).toBe('· 共 6 条')
      expect.soft(count.attributes('aria-label')).toBeUndefined()
    }
    expect.soft(wrapper.get('.timeline-toggle > span').classes()).not.toContain('timeline-count')
  })

  it('wraps a long unbroken event label in collapsed and expanded timelines', async () => {
    const longEventType = `event_${'x'.repeat(160)}`
    const snapshot = detail('waiting_evidence')
    snapshot.events = timelineEvents(6)
    snapshot.events[5].event_type = longEventType
    const wrapper = mount(BugCaseLifecycle, { props: { detail: snapshot } })

    expect(wrapper.get('#case-timeline-events li strong').text()).toBe(longEventType)
    const source = readFileSync('src/components/BugCaseLifecycle.vue', 'utf8')
    expect(source).toMatch(/\.timeline li > div \{[^}]*overflow-wrap: anywhere;/)

    await wrapper.get('.timeline-toggle').trigger('click')
    expect(wrapper.get('#case-timeline-events').classes()).toContain('is-expanded')
    expect(wrapper.get('#case-timeline-events li strong').text()).toBe(longEventType)
  })
})
