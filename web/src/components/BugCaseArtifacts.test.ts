import { flushPromises, mount } from '@vue/test-utils'
import { reactive } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { getIncidentArtifactPreview, saveIncidentArtifact, type IncidentCaseDetail } from '../lib/bridge/bugWorkflow'
import BugCaseArtifacts from './BugCaseArtifacts.vue'
import artifactSource from './BugCaseArtifacts.vue?raw'

const originalShowModal = Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, 'showModal')
const originalDialogClose = Object.getOwnPropertyDescriptor(HTMLDialogElement.prototype, 'close')

function installDialogStubs() {
  const showModal = vi.fn(function (this: HTMLDialogElement) { this.setAttribute('open', '') })
  const close = vi.fn(function (this: HTMLDialogElement) {
    this.removeAttribute('open')
    this.dispatchEvent(new Event('close'))
  })
  Object.defineProperty(HTMLDialogElement.prototype, 'showModal', { configurable: true, value: showModal })
  Object.defineProperty(HTMLDialogElement.prototype, 'close', { configurable: true, value: close })
  return { showModal, close }
}

function restoreDialogMethods() {
  if (originalShowModal) Object.defineProperty(HTMLDialogElement.prototype, 'showModal', originalShowModal)
  else delete (HTMLDialogElement.prototype as Partial<HTMLDialogElement>).showModal
  if (originalDialogClose) Object.defineProperty(HTMLDialogElement.prototype, 'close', originalDialogClose)
  else delete (HTMLDialogElement.prototype as Partial<HTMLDialogElement>).close
}

vi.mock('../lib/bridge/bugWorkflow', async importOriginal => ({
  ...(await importOriginal<typeof import('../lib/bridge/bugWorkflow')>()),
  getIncidentArtifactPreview: vi.fn(),
  saveIncidentArtifact: vi.fn(),
}))

beforeEach(() => {
  vi.mocked(getIncidentArtifactPreview).mockReset().mockResolvedValue({ artifact_id: 'evidence-1', mime_type: 'image/png', base64_data: 'iVBORw0KGgo=', size: 8 })
  vi.mocked(saveIncidentArtifact).mockReset().mockResolvedValue(true)
})
afterEach(() => {
  restoreDialogMethods()
  vi.restoreAllMocks()
})

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

  it('previews screenshots from safe bytes and never exposes artifact paths in text or DOM URLs', async () => {
    const dialogMethods = installDialogStubs()
    const privatePath = detail.artifacts[0].path_or_reference
    const wrapper = mount(BugCaseArtifacts, { props: { detail } })
    await flushPromises()

    const image = wrapper.get<HTMLImageElement>('img[data-artifact-id="evidence-1"]')
    expect(image.attributes('src')).toBe('data:image/png;base64,iVBORw0KGgo=')
    expect(image.attributes('src')).not.toContain(privatePath)
    expect(wrapper.text()).not.toContain(privatePath)
    expect(wrapper.html()).not.toContain(privatePath)

    await wrapper.get('[data-artifact-preview="evidence-1"]').trigger('click')
    const dialog = wrapper.get('dialog[open]')
    expect(dialogMethods.showModal).toHaveBeenCalledTimes(1)
    expect(dialog.attributes('aria-modal')).toBe('true')
    expect(dialog.attributes('aria-labelledby')).toBeTruthy()
    expect(dialog.get('img').attributes('src')).toBe('data:image/png;base64,iVBORw0KGgo=')
  })

  it('opens screenshots in the modal top layer, handles cancel, and restores thumbnail focus', async () => {
    const dialogMethods = installDialogStubs()
    const wrapper = mount(BugCaseArtifacts, { attachTo: document.body, props: { detail } })
    await flushPromises()
    const thumbnail = wrapper.get<HTMLButtonElement>('[data-artifact-preview="evidence-1"]')

    await thumbnail.trigger('click')
    await wrapper.vm.$nextTick()
    const dialog = wrapper.get<HTMLDialogElement>('dialog')
    expect(dialogMethods.showModal).toHaveBeenCalledWith()
    expect(dialog.element.open).toBe(true)
    expect(document.activeElement).toBe(dialog.get<HTMLButtonElement>('[data-dialog-close]').element)

    await dialog.trigger('cancel')
    await wrapper.vm.$nextTick()
    expect(dialogMethods.close).toHaveBeenCalledTimes(1)
    expect(wrapper.find('dialog').exists()).toBe(false)
    expect(document.activeElement).toBe(thumbnail.element)
    wrapper.unmount()
  })

  it('fails locally when the runtime has no modal dialog support', async () => {
    delete (HTMLDialogElement.prototype as Partial<HTMLDialogElement>).showModal
    const wrapper = mount(BugCaseArtifacts, { attachTo: document.body, props: { detail } })
    await flushPromises()
    const thumbnail = wrapper.get<HTMLButtonElement>('[data-artifact-preview="evidence-1"]')

    await thumbnail.trigger('click')
    await wrapper.vm.$nextTick()

    expect(wrapper.find('dialog').exists()).toBe(false)
    expect(wrapper.get('[data-artifact-id="evidence-1"]').text()).toContain('当前环境无法打开截图预览')
    expect(document.activeElement).toBe(thumbnail.element)
    wrapper.unmount()
  })

  it('closes an open modal through the dialog API when Case detail changes', async () => {
    const dialogMethods = installDialogStubs()
    const wrapper = mount(BugCaseArtifacts, { attachTo: document.body, props: { detail } })
    await flushPromises()
    await wrapper.get('[data-artifact-preview="evidence-1"]').trigger('click')
    await wrapper.vm.$nextTick()
    expect(wrapper.get<HTMLDialogElement>('dialog').element.open).toBe(true)

    await wrapper.setProps({ detail: { ...detail, case: { ...detail.case, id: 'case-2' } } })
    await wrapper.vm.$nextTick()

    expect(dialogMethods.close).toHaveBeenCalledTimes(1)
    expect(wrapper.find('dialog').exists()).toBe(false)
    wrapper.unmount()
  })

  it('keeps screenshot preview failures local to their card', async () => {
    vi.mocked(getIncidentArtifactPreview).mockRejectedValueOnce(new Error('/private/artifacts/shot.png is unreadable'))
    const wrapper = mount(BugCaseArtifacts, { props: { detail } })
    await flushPromises()

    const card = wrapper.get('[data-artifact-id="evidence-1"]')
    expect(card.text()).toContain('无法预览截图')
    expect(card.text()).not.toContain('/private/artifacts/shot.png')
    expect(wrapper.emitted()).toEqual({})
  })

  it('safely saves every artifact type without exposing the chosen destination', async () => {
    const artifacts = [
      detail.artifacts[0],
      { ...detail.artifacts[0], id: 'network-1', kind: 'network', path_or_reference: '/private/network.json' },
      { ...detail.artifacts[0], id: 'console-1', kind: 'console', path_or_reference: '/private/console.txt' },
      { ...detail.artifacts[0], id: 'actions-1', kind: 'browser_actions', path_or_reference: '/private/browser-actions.json' },
      { ...detail.artifacts[0], id: 'other-1', kind: 'log', path_or_reference: '/private/other.bin' },
    ]
    const wrapper = mount(BugCaseArtifacts, { props: { detail: { ...detail, artifacts } } })
    await flushPromises()

    const buttons = wrapper.findAll('[data-artifact-save]')
    expect(buttons).toHaveLength(artifacts.length)
    for (const button of buttons) await button.trigger('click')
    await flushPromises()

    expect(vi.mocked(saveIncidentArtifact).mock.calls).toEqual(artifacts.map(artifact => ['case-1', artifact.id]))
    expect(wrapper.text()).not.toContain('/private/')
    expect(wrapper.text()).toContain('已保存副本')
  })

  it('reports save failures only on the affected artifact card', async () => {
    vi.mocked(saveIncidentArtifact).mockRejectedValueOnce(new Error('/Users/alice/Desktop denied'))
    const wrapper = mount(BugCaseArtifacts, { props: { detail } })
    await flushPromises()
    await wrapper.get('[data-artifact-save="evidence-1"]').trigger('click')
    await flushPromises()

    const card = wrapper.get('[data-artifact-id="evidence-1"]')
    expect(card.text()).toContain('保存副本失败')
    expect(card.text()).not.toContain('/Users/alice/Desktop')
  })

  it('keeps the stage title outside a responsive keyboard-scrollable output region', () => {
    const wrapper = mount(BugCaseArtifacts, { props: { detail } })
    const card = wrapper.get('.attempt-output-card')
    const scroll = card.get('.attempt-output-scroll')

    expect(card.get(':scope > h3').text()).toBe('阶段输出')
    expect(scroll.attributes('role')).toBe('region')
    expect(scroll.attributes('aria-label')).toBe('阶段输出内容')
    expect(scroll.attributes('tabindex')).toBe('0')
    expect(scroll.findAll('.stage-attempt, .legacy-attempt')).toHaveLength(detail.attempts.length)
    expect(artifactSource).toMatch(/\.artifact-sections \{[^}]*grid-template-columns: repeat\(2, minmax\(0, 1fr\)\);/)
    expect(artifactSource).toMatch(/\.attempt-output-card \{[^}]*grid-column: 1 \/ -1;/)
    expect(artifactSource).toMatch(/\.attempt-output-scroll \{[^}]*height: clamp\(320px, 45vh, 640px\);/)
    expect(artifactSource).toMatch(/\.attempt-output-scroll \{[^}]*overflow-y: auto;/)
    expect(artifactSource).toMatch(/\.attempt-output-scroll \{[^}]*overflow-x: hidden;/)
    expect(artifactSource).toMatch(/\.attempt-output-scroll \{[^}]*scrollbar-gutter: stable;/)
    expect(artifactSource).toMatch(/\.attempt-output-scroll \{[^}]*overscroll-behavior: contain;/)
    expect(artifactSource).toContain('.attempt-output-scroll:focus-visible')
  })

  it('scrolls the stage viewport to the bottom initially and after nested attempt updates', async () => {
    vi.spyOn(Element.prototype, 'scrollHeight', 'get').mockReturnValue(640)
    const pageScroll = vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
    const scrollIntoView = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => undefined)

    const wrapper = mount(BugCaseArtifacts, { props: { detail } })
    await wrapper.vm.$nextTick()
    await wrapper.vm.$nextTick()
    const viewport = wrapper.get<HTMLElement>('.attempt-output-scroll').element
    expect(viewport.scrollTop).toBe(640)

    viewport.scrollTop = 120
    const appended = { ...detail.attempts[0], id: 'investigate-2', status: 'failed' as const, output_json: { summary: '新的阶段结论' }, error_message: '新错误' }
    await wrapper.setProps({ detail: { ...detail, attempts: [...detail.attempts, appended] } })
    await wrapper.vm.$nextTick()
    await wrapper.vm.$nextTick()
    expect(viewport.scrollTop).toBe(640)
    expect(scrollIntoView).not.toHaveBeenCalled()
    expect(pageScroll).not.toHaveBeenCalled()
  })

  it('scrolls after output, status and error mutate in place inside an existing attempt', async () => {
    vi.spyOn(Element.prototype, 'scrollHeight', 'get').mockReturnValue(720)
    const mutableDetail = reactive(structuredClone(detail))
    const wrapper = mount(BugCaseArtifacts, { props: { detail: mutableDetail } })
    await wrapper.vm.$nextTick()
    await wrapper.vm.$nextTick()
    const viewport = wrapper.get<HTMLElement>('.attempt-output-scroll').element

    viewport.scrollTop = 100
    mutableDetail.attempts[0].output_json.summary = '原地更新的阶段结论'
    await wrapper.vm.$nextTick()
    await wrapper.vm.$nextTick()
    expect(viewport.scrollTop).toBe(720)

    viewport.scrollTop = 110
    mutableDetail.attempts[0].status = 'failed'
    await wrapper.vm.$nextTick()
    await wrapper.vm.$nextTick()
    expect(viewport.scrollTop).toBe(720)

    viewport.scrollTop = 120
    mutableDetail.attempts[0].error_message = '原地更新的错误'
    await wrapper.vm.$nextTick()
    await wrapper.vm.$nextTick()
    expect(viewport.scrollTop).toBe(720)
  })

  it('follows a switched Case and does not reset scrolling without a data change', async () => {
    vi.spyOn(Element.prototype, 'scrollHeight', 'get').mockReturnValue(480)
    const wrapper = mount(BugCaseArtifacts, { props: { detail } })
    await wrapper.vm.$nextTick()
    await wrapper.vm.$nextTick()
    const viewport = wrapper.get<HTMLElement>('.attempt-output-scroll').element

    viewport.scrollTop = 90
    await wrapper.vm.$nextTick()
    expect(viewport.scrollTop).toBe(90)

    await wrapper.setProps({ detail: { ...detail, case: { ...detail.case, id: 'case-2' } } })
    await wrapper.vm.$nextTick()
    await wrapper.vm.$nextTick()
    expect(viewport.scrollTop).toBe(480)
  })

  it('renders current attempts as semantic history with only the latest expanded', () => {
    const first = { ...detail.attempts[0], id: 'validation-old', phase: 'validation' as const, mode: 'reproduce' as const, output_json: { verification_status: 'not_reproduced', environment: 'test', evidence: [], gaps: [] } }
    const latest = { ...first, id: 'validation-latest', status: 'failed' as const, output_json: { verification_status: 'insufficient_info', environment: 'test', expected_behavior: '显示两名用户', observed_behavior: '只显示一名用户', evidence: [], gaps: ['缺少 Network 导出'] } }
    const wrapper = mount(BugCaseArtifacts, { props: { detail: { ...detail, attempts: [first, latest] } } })
    const attempts = wrapper.findAll('.stage-attempt')
    expect(attempts).toHaveLength(2)
    expect(attempts[0].attributes('open')).toBeUndefined()
    expect(attempts[1].attributes()).toHaveProperty('open')
    expect(wrapper.text()).not.toContain('verification_status')
    expect(wrapper.find('[data-raw-output]').exists()).toBe(false)
  })

  it('keeps an investigation without a root cause semantic in the root-cause card', () => {
    const investigation = {
      ...detail.attempts[0],
      output_json: {
        investigation_status: 'insufficient_info',
        environment: 'test',
        evidence: [],
        gaps: ['缺少 trace'],
      },
    }
    const wrapper = mount(BugCaseArtifacts, { props: { detail: { ...detail, attempts: [investigation] } } })

    expect(wrapper.get('[aria-labelledby="cause-title"]').text()).toContain('尚无根因结论')
    expect(wrapper.text()).not.toContain('investigation_status')
    expect(wrapper.text()).not.toContain('{')
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

  it('does not expose reset archives even when persisted relations are present', () => {
    const resetDetail = {
      ...detail,
      case: {
        ...detail.case,
        reset_from_case_id: 'case-before-reset',
        superseded_by_case_id: 'case-after-reset',
      },
    }
    const wrapper = mount(BugCaseArtifacts, { props: { detail: resetDetail } })

    expect(wrapper.find('[aria-labelledby="reset-relations-title"]').exists()).toBe(false)
    expect(wrapper.find('[data-case-reference]').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('重置关系')
    expect(wrapper.text()).not.toContain('case-before-reset')
    expect(wrapper.text()).not.toContain('case-after-reset')
    expect(wrapper.emitted('select-case')).toBeUndefined()
  })
})
