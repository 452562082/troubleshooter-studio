import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { approveIncidentFix, approveIncidentMerge, cancelBugInvestigation, continueIncidentCase, discoverBots, generateBugContext, getIncidentCase, listBugInvestigationRuns, listBugPlatforms, listBugs, listIncidentCases, matchBugBots, notifyIncidentDeployed, previewBugAttachment, saveBugPlatform, startBugInvestigation, startIncidentCase } from '../lib/bridge'
import { copyToClipboard } from '../lib/clipboard'
import BugWorkbenchPage from './BugWorkbenchPage.vue'

const runtimeMock = vi.hoisted(() => {
  const handlers: Record<string, (...args: any[]) => void> = {}
  const unlisten = vi.fn()
  return {
    handlers,
    unlisten,
    EventsOn: vi.fn((name: string, handler: (...args: any[]) => void) => {
      handlers[name] = handler
      return unlisten
    }),
  }
})

vi.mock('../../wailsjs/runtime/runtime', () => ({
  EventsOn: runtimeMock.EventsOn,
}))

vi.mock('../lib/bridge', () => ({
  bugHookBaseURL: vi.fn().mockResolvedValue(''),
  cancelBugInvestigation: vi.fn(),
  clearBugPlatformLogin: vi.fn(),
  deleteBugPlatform: vi.fn(),
  discoverBots: vi.fn().mockResolvedValue([]),
  fetchBugByID: vi.fn(),
  generateBugContext: vi.fn(),
  loginBugPlatform: vi.fn(),
  listBugInvestigationRuns: vi.fn().mockResolvedValue([]),
  listIncidentCases: vi.fn().mockResolvedValue([]),
  getIncidentCase: vi.fn(),
  listBugPlatforms: vi.fn().mockResolvedValue([]),
  listBugs: vi.fn().mockResolvedValue([]),
  matchBugBots: vi.fn().mockResolvedValue([]),
  previewBugAttachment: vi.fn(),
  saveBugPlatform: vi.fn(),
  startBugInvestigation: vi.fn(),
  startIncidentCase: vi.fn().mockResolvedValue({ id: 'case-new', bug_id: 'zentao-577', status: 'validating', version: 1 }),
  continueIncidentCase: vi.fn(),
  approveIncidentFix: vi.fn(),
  approveIncidentMerge: vi.fn(),
  notifyIncidentDeployed: vi.fn(),
  cancelIncidentAttempt: vi.fn(),
  syncBugPlatform: vi.fn(),
}))

vi.mock('../lib/clipboard', () => ({
  copyToClipboard: vi.fn().mockResolvedValue(true),
}))

vi.mock('../lib/confirm', () => ({
  confirmDialog: vi.fn().mockResolvedValue(true),
}))

vi.mock('../lib/toast', () => ({
  toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() },
  toastError: vi.fn(),
}))

afterEach(() => {
  delete (window as any).runtime
  for (const key of Object.keys(runtimeMock.handlers)) delete runtimeMock.handlers[key]
  runtimeMock.EventsOn.mockClear()
  runtimeMock.unlisten.mockClear()
  vi.mocked(cancelBugInvestigation).mockReset()
  vi.mocked(discoverBots).mockResolvedValue([])
  vi.mocked(generateBugContext).mockReset()
  vi.mocked(listBugInvestigationRuns).mockResolvedValue([])
  vi.mocked(listIncidentCases).mockResolvedValue([])
  vi.mocked(getIncidentCase).mockReset()
  vi.mocked(continueIncidentCase).mockReset()
  vi.mocked(approveIncidentFix).mockReset()
  vi.mocked(approveIncidentMerge).mockReset()
  vi.mocked(notifyIncidentDeployed).mockReset()
  vi.mocked(listBugPlatforms).mockResolvedValue([])
  vi.mocked(listBugs).mockResolvedValue([])
  vi.mocked(matchBugBots).mockResolvedValue([])
  vi.mocked(previewBugAttachment).mockReset()
  vi.mocked(saveBugPlatform).mockReset()
  vi.mocked(startBugInvestigation).mockReset()
  vi.mocked(startIncidentCase).mockReset()
  vi.mocked(startIncidentCase).mockResolvedValue({ id: 'case-new', bug_id: 'zentao-577', status: 'validating', version: 1 } as any)
  vi.mocked(copyToClipboard).mockResolvedValue(true)
})

function flushPromises() {
  return new Promise(resolve => setTimeout(resolve, 0))
}

function durableDetail(status: string, overrides: Record<string, unknown> = {}) {
  const incident = { id: 'case-1', bug_id: 'zentao-577', source: 'zentao', system_id: 'base', environment: 'test', status, cycle_number: 1, current_attempt_id: 'attempt-1', selected_bot_key: 'base|codex', version: 7, created_at: '', updated_at: '' }
  return { case: incident, attempts: [], artifacts: [], approvals: [], code_changes: [], deployment_observations: [], events: [], ...overrides }
}

describe('BugWorkbenchPage', () => {
  it('keeps platform configuration collapsed by default', async () => {
    const wrapper = mount(BugWorkbenchPage)

    expect(wrapper.find('.platform-config').classes()).not.toContain('open')
    expect(wrapper.text()).toContain('平台配置')
  })

  it('does not ask for zentao account as login credential in feishu auth mode', async () => {
    const wrapper = mount(BugWorkbenchPage)

    await wrapper.find('button.accent').trigger('click')

    expect(wrapper.text()).toContain('飞书授权登录')
    expect(wrapper.text()).toContain('登录平台')
    expect(wrapper.find('input[placeholder="我的禅道账号"]').exists()).toBe(false)
    expect(wrapper.find('input[placeholder="指派人账号,仅后台同步时用于筛选"]').exists()).toBe(false)
    expect(wrapper.find('input[placeholder="Hook Secret,留空自动生成"]').exists()).toBe(false)
  })

  it('does not show assignee account even when background sync is enabled', async () => {
    const wrapper = mount(BugWorkbenchPage)

    await wrapper.find('button.accent').trigger('click')
    const checkboxes = wrapper.findAll('input[type="checkbox"]')
    await checkboxes[1].setValue(true)

    expect(wrapper.find('input[placeholder="指派人账号,仅后台同步时用于筛选"]').exists()).toBe(false)
  })

  it('labels background sync interval as minutes', async () => {
    const wrapper = mount(BugWorkbenchPage)

    await wrapper.find('button.accent').trigger('click')

    expect(wrapper.text()).toContain('后台定时同步')
    expect(wrapper.text()).toContain('分钟')
    expect(wrapper.find('input[aria-label="后台同步间隔分钟"]').exists()).toBe(true)
  })

  it('groups platform config into basic auth and ops rows', async () => {
    const wrapper = mount(BugWorkbenchPage)

    await wrapper.find('button.accent').trigger('click')

    expect(wrapper.find('.config-row.basic-row').exists()).toBe(true)
    expect(wrapper.find('.config-row.auth-row').exists()).toBe(true)
    expect(wrapper.find('.config-row.ops-row').exists()).toBe(true)
  })

  it('uses a right-side plus button for adding platforms', async () => {
    const wrapper = mount(BugWorkbenchPage)

    await wrapper.find('button.accent').trigger('click')

    expect(wrapper.find('button.add-platform[aria-label="新增平台"]').exists()).toBe(true)
    expect(wrapper.find('button.add-platform').text()).toBe('+')
  })

  it('keeps platform login and save buttons enabled while bugs are loading', async () => {
    vi.mocked(listBugs).mockReturnValue(new Promise(() => {}))
    const wrapper = mount(BugWorkbenchPage)

    await wrapper.find('button.accent').trigger('click')

    const loginButton = wrapper.findAll('button').find(button => button.text() === '登录平台')
    const saveButton = wrapper.findAll('button').find(button => button.text() === '保存配置')
    expect(loginButton?.attributes('disabled')).toBeUndefined()
    expect(saveButton?.attributes('disabled')).toBeUndefined()
  })

  it('saves selected platform bots and their mapped environments', async () => {
    vi.mocked(discoverBots).mockResolvedValue([
      {
        path: '/repo/base',
        ghost: false,
        meta: {
          system_id: 'base',
          system_name: 'Base',
          target: 'codex',
          agent_id: 'base-troubleshooter',
          role: 'troubleshooter',
          internal_agents: [
            { id: 'base-troubleshooter', role: 'troubleshooter' },
            { id: 'base-validator', role: 'validator' },
          ],
        },
        environments: ['test', 'prod'],
      } as any,
    ])
    vi.mocked(saveBugPlatform).mockResolvedValue({
      id: 'zentao-main',
      name: '禅道',
      type: 'zentao',
      base_url: 'https://zentao.example.com',
      bot_mappings: [{ bot_key: '/repo/base|codex', env: 'prod' }],
      enabled: true,
    })
    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await wrapper.find('button.accent').trigger('click')

    await wrapper.find('input[placeholder="平台名称,如 测试环境 Bug 平台"]').setValue('禅道')
    await wrapper.find('input[placeholder="平台地址 https://bug-platform.example.com"]').setValue('https://zentao.example.com')
    expect(wrapper.findAll('.bot-config-row')).toHaveLength(0)
    await wrapper.find('.add-bot-btn').trigger('click')
    expect(wrapper.findAll('.bot-picker-row')).toHaveLength(1)
    await wrapper.find('.bot-picker-row').trigger('click')
    expect(wrapper.findAll('.bot-config-row')).toHaveLength(1)
    expect(wrapper.findAll('.bot-picker-row')).toHaveLength(0)
    await wrapper.find('.bot-config-row select').setValue('prod')
    const saveButton = wrapper.findAll('button').find(button => button.text() === '保存配置')
    await saveButton!.trigger('click')

    expect(saveBugPlatform).toHaveBeenCalledWith(expect.objectContaining({
      name: '禅道',
      base_url: 'https://zentao.example.com',
      bot_mappings: [{ bot_key: '/repo/base|codex', env: 'prod' }],
    }))
  })

  it('labels immediate sync as syncing my assigned bugs', async () => {
    const wrapper = mount(BugWorkbenchPage)

    expect(wrapper.text()).toContain('同步我的 Bug')
  })

  it('renders zentao html steps as rich text', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      {
        id: 'zentao-577',
        source: 'zentao',
        source_id: '577',
        title: '搜索页异常',
        product: 'S基建项目',
        module: 'frontend',
        bug_type: '代码错误',
        os: 'WEB',
        browser: 'Chrome',
        keywords: 'PC 搜索',
        severity: '3',
        priority: '1',
        created_at: '2026-07-03T10:03:21Z',
        frontend_repo: 'pc-web',
        frontend_url: 'https://example.com/search',
        api_paths: ['/api/search'],
        trace_ids: ['trace-1'],
        request_ids: ['request-1'],
        steps: '<p>[步骤]</p><ol><li>PC端进入搜索页</li><li>输入电影名称</li></ol>',
      },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('.text-block li').text()).toBe('PC端进入搜索页')
    expect(wrapper.find('.text-block').text()).not.toContain('<p>[步骤]</p>')
    expect(wrapper.find('.bug-fields').text()).toContain('S基建项目')
    expect(wrapper.find('.bug-fields').text()).toContain('frontend')
    expect(wrapper.find('.bug-fields').text()).toContain('代码错误')
    expect(wrapper.find('.bug-fields').text()).toContain('WEB')
    expect(wrapper.find('.bug-fields').text()).toContain('Chrome')
    expect(wrapper.find('.bug-fields').text()).toContain('PC 搜索')
    expect(wrapper.find('.bug-fields').text()).toContain('S3')
    expect(wrapper.find('.bug-fields').text()).toContain('P1')
    expect(wrapper.find('.bug-detail').text()).not.toContain('API 路径')
    expect(wrapper.find('.bug-detail').text()).not.toContain('Trace / Request')
    expect(wrapper.find('.bug-detail').text()).not.toContain('前端仓库')
    expect(wrapper.find('.bug-detail').text()).not.toContain('前端 URL')
    expect(wrapper.find('.bug-detail').text()).not.toContain('/api/search')
    expect(wrapper.find('.bug-detail').text()).not.toContain('trace-1')
  })

  it('renders and previews bug attachments', async () => {
    vi.mocked(listBugPlatforms).mockResolvedValue([
      { id: 'zentao-main', name: '禅道', type: 'zentao', base_url: 'https://zentao.example.com', enabled: true },
    ])
    vi.mocked(listBugs).mockResolvedValue([
      {
        id: 'zentao-718',
        source: 'zentao',
        source_id: '718',
        title: '搜索页异常',
        attachments: [{ id: '101', name: 'screen.png', type: 'image/png' }],
      },
    ])
    vi.mocked(previewBugAttachment).mockResolvedValue({
      name: 'screen.png',
      content_type: 'image/png',
      data_url: 'data:image/png;base64,AA==',
    })

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()
    await flushPromises()
    await flushPromises()

    expect(wrapper.text()).toContain('附件')
    expect(wrapper.text()).toContain('screen.png')
    expect(wrapper.find('.attachment-thumb-img').attributes('src')).toBe('data:image/png;base64,AA==')
    await wrapper.find('.attachment-item').trigger('click')
    await flushPromises()

    expect(previewBugAttachment).toHaveBeenCalledWith({
      platform_id: 'zentao-main',
      bug_id: 'zentao-718',
      attachment_index: 0,
    })
    expect(wrapper.find('.attachment-preview-image').attributes('src')).toBe('data:image/png;base64,AA==')
  })

  it('renders bot matches when backend returns null reasons', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-1842', source: 'zentao', source_id: '1842', title: '支付页 500' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'shop-prod', system_id: 'shop', target: 'codex', path: '/bots/shop', envs: ['test', 'prod'] }, score: 10, reasons: null as any },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    expect(wrapper.text()).toContain('shop')
    expect(wrapper.text()).toContain('codex')
    expect(wrapper.text()).not.toContain('score 10')
    expect(wrapper.text()).not.toContain('支持环境')
    expect(wrapper.text()).not.toContain('无显式匹配')
  })

  it('filters selectable bots by platform mapping and passes mapped env to start', async () => {
    vi.mocked(listBugPlatforms).mockResolvedValue([
      {
        id: 'zentao-main',
        name: '禅道',
        type: 'zentao',
        base_url: 'https://zentao.example.com',
        enabled: true,
        bot_mappings: [{ bot_key: 'base|codex', env: 'prod' }],
      },
    ])
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo', envs: ['test', 'prod'] }, score: 10, reasons: [] },
      { bot: { key: 'base|claude-code', system_id: 'base', target: 'claude-code', path: '/repo' }, score: 9, reasons: [] },
    ])
    vi.mocked(startBugInvestigation).mockResolvedValue({
      id: 'run-1',
      bug_id: 'zentao-577',
      bot_key: 'base|codex',
      status: 'running',
      events: [],
    })

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()
    await flushPromises()

    expect(wrapper.text()).toContain('codex')
    expect(wrapper.text()).toContain('环境 prod')
    expect(wrapper.text()).not.toContain('score 10')
    expect(wrapper.text()).not.toContain('claude-code')

    const button = wrapper.findAll('button').find(b => b.text() === '开始排障')
    await button!.trigger('click')

    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({
      bug_id: 'zentao-577',
      bot_key: 'base|codex',
      expected_version: 0,
      input_json: expect.objectContaining({ mode: 'reproduce', target_environment: 'prod' }),
    }))
  })

  it('shows start investigation for codex bot', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
    ])
    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    expect(wrapper.text()).toContain('开始排障')
  })

  it('starts codex investigation from selected bug and bot', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
    ])
    vi.mocked(startBugInvestigation).mockResolvedValue({
      id: 'run-1',
      bug_id: 'zentao-577',
      bot_key: 'base|codex',
      status: 'running',
      events: [],
    })
    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    const button = wrapper.findAll('button').find(b => b.text() === '开始排障')
    expect(button).toBeTruthy()
    await button!.trigger('click')

    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({
      bug_id: 'zentao-577', bot_key: 'base|codex', expected_version: 0,
    }))
  })

  it('passes a single bot with internal agents when starting investigation', async () => {
    vi.mocked(discoverBots).mockResolvedValue([
      {
        path: '/repo/base-troubleshooter',
        ghost: false,
        meta: {
          system_id: 'base',
          system_name: 'Base',
          target: 'codex',
          agent_id: 'base-troubleshooter',
          role: 'troubleshooter',
          internal_agents: [
            { id: 'base-troubleshooter', role: 'troubleshooter' },
            { id: 'base-validator', role: 'validator' },
          ],
        },
        environments: ['test'],
      } as any,
    ])
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      {
        bot: {
          key: '/repo/base-troubleshooter|codex',
          system_id: 'base',
          target: 'codex',
          path: '/repo/base-troubleshooter',
          agent_id: 'base-troubleshooter',
          role: 'troubleshooter',
          internal_agents: [
            { id: 'base-troubleshooter', role: 'troubleshooter' },
            { id: 'base-validator', role: 'validator' },
          ],
          env: 'test',
        },
        score: 10,
        reasons: [],
      },
    ])
    vi.mocked(startBugInvestigation).mockResolvedValue({
      id: 'run-1',
      bug_id: 'zentao-577',
      bot_key: '/repo/base-troubleshooter|codex',
      status: 'running',
      events: [],
    })
    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    const button = wrapper.findAll('button').find(b => b.text() === '开始排障')
    await button!.trigger('click')

    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({
      bug_id: 'zentao-577', bot_key: '/repo/base-troubleshooter|codex', expected_version: 0,
      input_json: expect.objectContaining({ target_environment: 'test' }),
    }))
  })

  it('starts claude code investigation from selected bot', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|claude-code', system_id: 'base', target: 'claude-code', path: '/Users/me/.claude/agents/base.md' }, score: 10, reasons: [] },
    ])
    vi.mocked(startBugInvestigation).mockResolvedValue({
      id: 'run-1',
      bug_id: 'zentao-577',
      bot_key: 'base|claude-code',
      status: 'running',
      events: [],
    })
    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    const button = wrapper.findAll('button').find(b => b.text() === '开始排障')
    expect(button?.attributes('disabled')).toBeUndefined()
    await button!.trigger('click')

    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({
      bug_id: 'zentao-577', bot_key: 'base|claude-code', expected_version: 0,
    }))
  })

  it('renders final investigation output', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
    ])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([
      { id: 'run-1', bug_id: 'zentao-577', bot_key: 'base|codex', status: 'succeeded', final_message: '缓存配置错误', events: [] },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('.context-preview').text()).toContain('缓存配置错误')
    expect(wrapper.find('.context-preview .markdown-result').html()).toContain('<p>缓存配置错误</p>')
  })

  it('renders investigation final message as markdown', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
    ])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([
      {
        id: 'run-1',
        bug_id: 'zentao-577',
        bot_key: 'base|codex',
        status: 'succeeded',
        final_message: '## 现象复述\n\n1. 输入电影名称\n2. 电影卡片显示 **一集全**',
        events: [],
      },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('.context-preview .markdown-result h2').text()).toBe('现象复述')
    expect(wrapper.find('.context-preview .markdown-result strong').text()).toBe('一集全')
  })

  it('disables start investigation and offers context fallback for cursor bot', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|cursor', system_id: 'base', target: 'cursor', path: '/repo' }, score: 10, reasons: [] },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    expect(wrapper.text()).toContain('Cursor 暂不支持后台直启')
    const button = wrapper.findAll('button').find(b => b.text() === '开始排障')
    expect(button?.attributes('disabled')).toBeDefined()
    expect(wrapper.findAll('button').some(b => b.text() === '生成上下文')).toBe(true)
  })

  it('hides output actions until they are applicable', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
    ])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    const actionText = wrapper.find('.bot-actions').text()
    expect(actionText).not.toContain('停止')
    expect(actionText).not.toContain('复制')
  })

  it('shows copy for completed output but hides stop', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
    ])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([
      { id: 'run-1', bug_id: 'zentao-577', bot_key: 'base|codex', status: 'succeeded', final_message: '排障结论', events: [] },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    const actionText = wrapper.find('.bot-actions').text()
    expect(actionText).not.toContain('停止')
    expect(actionText).toContain('复制结果')
  })

  it('does not show copy for running process logs without final result', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
    ])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([
      {
        id: 'run-1',
        bug_id: 'zentao-577',
        bot_key: 'base|codex',
        status: 'running',
        events: [{ type: 'agent_message', message: '正在读取配置' }],
      },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('.context-preview').text()).toContain('正在读取配置')
    expect(wrapper.find('.bot-actions').text()).not.toContain('复制')
  })

  it('copies final result without process logs', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
    ])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([
      {
        id: 'run-1',
        bug_id: 'zentao-577',
        bot_key: 'base|codex',
        status: 'succeeded',
        final_message: '最终根因',
        events: [{ type: 'agent_message', message: '正在读取配置' }],
      },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    const copyButton = wrapper.findAll('.bot-actions button').find(b => b.text() === '复制结果')
    await copyButton!.trigger('click')

    expect(copyToClipboard).toHaveBeenCalledWith('最终根因')
  })

  it('shows stop for running investigation and cancels the active run', async () => {
    vi.mocked(cancelBugInvestigation).mockResolvedValue()
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
    ])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([
      { id: 'run-1', bug_id: 'zentao-577', bot_key: 'base|codex', status: 'running', events: [] },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    const stopButton = wrapper.findAll('.bot-actions button').find(b => b.text() === '停止')
    expect(stopButton?.exists()).toBe(true)
    await stopButton!.trigger('click')

    expect(cancelBugInvestigation).toHaveBeenCalledWith({ run_id: 'run-1' })
  })

  it('streams investigation events into the selected run output', async () => {
    ;(window as any).runtime = { EventsOnMultiple: vi.fn() }
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
    ])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([
      { id: 'run-1', bug_id: 'zentao-577', bot_key: 'base|codex', status: 'running', events: [] },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    runtimeMock.handlers['bug-investigation:event']({
      run: { id: 'run-1', bug_id: 'zentao-577', bot_key: 'base|codex', status: 'running' },
      event: { type: 'message', message: '正在检查缓存配置' },
    })
    await wrapper.vm.$nextTick()

    expect(wrapper.find('.context-preview').text()).toContain('正在检查缓存配置')

    runtimeMock.handlers['bug-investigation:event']({
      run: { id: 'run-1', bug_id: 'zentao-577', bot_key: 'base|codex', status: 'succeeded', final_message: '缓存配置错误', events: [] },
      event: { type: 'final', message: '缓存配置错误' },
    })
    await wrapper.vm.$nextTick()

    expect(wrapper.find('.process-log').text()).toContain('正在检查缓存配置')
    expect(wrapper.find('.context-preview .markdown-result').text()).toContain('缓存配置错误')
    wrapper.unmount()
    expect(runtimeMock.unlisten).toHaveBeenCalled()
  })

  it('separates validation evidence from investigation output tabs', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
    ])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([
      {
        id: 'run-1',
        bug_id: 'zentao-577',
        bot_key: 'base|codex',
        status: 'succeeded',
        final_message: '最终根因',
        events: [
          { type: 'stage', message: '验证 Agent 开始取证验证', meta: { phase: 'validation' } },
          { type: 'agent_message', message: 'verification_status: reproduced', meta: { phase: 'validation' } },
          { type: 'agent_message', message: '正在定位后端代码', meta: { phase: 'investigation' } },
        ],
      },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('.context-preview').text()).toContain('正在定位后端代码')
    expect(wrapper.find('.context-preview').text()).toContain('最终根因')
    expect(wrapper.find('.context-preview').text()).not.toContain('verification_status')

    const validationTab = wrapper.findAll('.output-tab').find(tab => tab.text().includes('验证证据'))
    expect(validationTab?.exists()).toBe(true)
    await validationTab!.trigger('click')

    expect(wrapper.find('.context-preview').text()).toContain('verification_status: reproduced')
    expect(wrapper.find('.context-preview').text()).not.toContain('最终根因')
  })

  it('normalizes validation report environment labels from older runs', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-909', source: 'zentao', source_id: '909', title: '分类计数错误' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo', env: 'test' }, score: 10, reasons: [] },
    ])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([
      {
        id: 'run-1',
        bug_id: 'zentao-909',
        bot_key: 'base|codex',
        status: 'succeeded',
        final_message: '### 验证报告 | bug env: -, bot env: test | 未复现\n\n- 结论: 未复现原始 Bug',
        events: [
          { type: 'stage', message: '验证 Agent 未复现原始 Bug，已暂停进入排障 Agent', meta: { phase: 'validation' } },
        ],
      },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    const text = wrapper.find('.context-preview').text()
    expect(text).toContain('验证报告 | test | 未复现')
    expect(text).not.toContain('bug env: -')
  })

  it('switches to investigation output when a run fails', async () => {
    ;(window as any).runtime = { EventsOnMultiple: vi.fn() }
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
    ])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([
      {
        id: 'run-1',
        bug_id: 'zentao-577',
        bot_key: 'base|codex',
        status: 'running',
        events: [
          { type: 'agent_message', message: 'verification_status: reproduced', meta: { phase: 'validation' } },
        ],
      },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    const validationTab = wrapper.findAll('.output-tab').find(tab => tab.text().includes('验证证据'))
    await validationTab!.trigger('click')
    expect(wrapper.find('.context-preview').text()).toContain('verification_status: reproduced')

    runtimeMock.handlers['bug-investigation:event']({
      run: { id: 'run-1', bug_id: 'zentao-577', bot_key: 'base|codex', status: 'failed', error: 'request timed out' },
      event: { type: 'status', message: 'failed' },
    })
    await wrapper.vm.$nextTick()

    expect(wrapper.find('.output-tab.active').text()).toContain('排障分析')
    expect(wrapper.find('.context-preview').text()).toContain('request timed out')
    expect(wrapper.find('.context-preview').text()).not.toContain('verification_status: reproduced')
  })

  it('renders streamed final investigation event as markdown', async () => {
    ;(window as any).runtime = { EventsOnMultiple: vi.fn() }
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
    ])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([
      { id: 'run-1', bug_id: 'zentao-577', bot_key: 'base|codex', status: 'running', events: [] },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    runtimeMock.handlers['bug-investigation:event']({
      run: { id: 'run-1', bug_id: 'zentao-577', bot_key: 'base|codex', status: 'succeeded', events: [] },
      event: {
        type: 'final',
        message: '## 故障快报\n\n### 1. 已验证事实\n\n| # | 事实 |\n|---|---|\n| 1 | 命中搜索接口 |',
      },
    })
    await wrapper.vm.$nextTick()

    expect(wrapper.find('.process-log').exists()).toBe(false)
    expect(wrapper.find('.context-preview .markdown-result h2').text()).toBe('故障快报')
    expect(wrapper.find('.context-preview .markdown-result table').exists()).toBe(true)
    expect(wrapper.find('.context-preview').text()).not.toContain('|---|')
  })

  it('promotes terminal markdown agent messages above completion markers', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
    ])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([
      {
        id: 'run-1',
        bug_id: 'zentao-577',
        bot_key: 'base|codex',
        status: 'succeeded',
        events: [
          {
            type: 'agent_message',
            message: '### 1. 现象复述\n\n| # | 事实 |\n|---|---|\n| 1 | 搜索结果显示一集全 |',
          },
          { type: 'turn_completed', message: '排障完成' },
        ],
      },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('.context-preview .markdown-result h3').text()).toBe('1. 现象复述')
    expect(wrapper.find('.context-preview .markdown-result table').exists()).toBe(true)
    expect(wrapper.find('.process-log').text()).not.toContain('|---|')
  })

  it('scrolls investigation output to the bottom as events arrive', async () => {
    ;(window as any).runtime = { EventsOnMultiple: vi.fn() }
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
    ])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([
      { id: 'run-1', bug_id: 'zentao-577', bot_key: 'base|codex', status: 'running', events: [] },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()
    const output = wrapper.find('.context-preview').element as HTMLElement
    Object.defineProperty(output, 'scrollHeight', { configurable: true, value: 900 })
    output.scrollTop = 0

    runtimeMock.handlers['bug-investigation:event']({
      run: { id: 'run-1', bug_id: 'zentao-577', bot_key: 'base|codex', status: 'running' },
      event: { type: 'message', message: '第 1 步：检查最近变更' },
    })
    await wrapper.vm.$nextTick()
    await wrapper.vm.$nextTick()

    expect(output.scrollTop).toBe(900)
  })

  it('ignores investigation events for a non-selected bug', async () => {
    ;(window as any).runtime = { EventsOnMultiple: vi.fn() }
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常', last_context: '当前 Bug 上下文' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    runtimeMock.handlers['bug-investigation:event']({
      run: { id: 'run-other', bug_id: 'zentao-999', bot_key: 'base|codex', status: 'succeeded', final_message: '其他 Bug 结论', events: [] },
      event: { type: 'final', message: '其他 Bug 结论' },
    })
    await wrapper.vm.$nextTick()

    expect(wrapper.find('.context-preview').text()).toBe('当前 Bug 上下文')
  })

  it('switches to the durable lifecycle when persisted Cases exist', async () => {
    const incident = { id: 'case-1', bug_id: 'zentao-577', source: 'zentao', system_id: 'base', environment: 'test', status: 'waiting_fix_approval', cycle_number: 1, current_attempt_id: 'root-1', selected_bot_key: 'base|codex', version: 7, created_at: '', updated_at: '' } as const
    vi.mocked(listIncidentCases).mockResolvedValue([incident as any])
    vi.mocked(getIncidentCase).mockResolvedValue({ case: incident, attempts: [], artifacts: [], approvals: [], code_changes: [], deployment_observations: [], events: [] } as any)

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()

    expect(wrapper.find('.case-lifecycle').exists()).toBe(true)
    expect(wrapper.text()).toContain('允许修复')
    expect(wrapper.find('.bug-output-panel').exists()).toBe(false)
  })

  it('continues a legacy archive by starting a new durable Case', async () => {
    const archived = { id: 'legacy-1', bug_id: 'zentao-577', source: 'legacy-runs-json', system_id: '', environment: '', status: 'legacy_archived', cycle_number: 1, current_attempt_id: 'legacy-attempt', selected_bot_key: 'base|codex', version: 3, created_at: '', updated_at: '' } as const
    vi.mocked(listIncidentCases).mockResolvedValue([archived as any])
    vi.mocked(getIncidentCase).mockResolvedValue({ case: archived, attempts: [], artifacts: [], approvals: [], code_changes: [], deployment_observations: [], events: [] } as any)
    vi.mocked(startIncidentCase).mockResolvedValue({ ...archived, id: 'case-new', source: 'zentao', status: 'validating', cycle_number: 2, version: 1 } as any)

    const wrapper = mount(BugWorkbenchPage)
    await flushPromises()
    await flushPromises()
    await wrapper.find('.primary-action').trigger('click')
    await flushPromises()

    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({
      case_id: 'legacy-1', bug_id: 'zentao-577', expected_version: 3, bot_key: 'base|codex', actor_id: 'desktop-user',
    }))
  })

  it('submits fix approval with the dialog-captured root cause and exact Case version key', async () => {
    const snapshot = durableDetail('waiting_fix_approval', {
      attempts: [{ id: 'attempt-1', case_id: 'case-1', cycle_number: 1, phase: 'investigation', mode: '', status: 'succeeded', agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json: { investigation_status: 'root_cause_ready', confidence: 'high', gaps: [] }, parent_attempt_id: '', started_at: '', error_code: '', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([snapshot.case as any])
    vi.mocked(getIncidentCase).mockResolvedValue(snapshot as any)
    vi.mocked(approveIncidentFix).mockResolvedValue({ ...snapshot.case, status: 'fixing', version: 8 } as any)
    const wrapper = mount(BugWorkbenchPage)
    await flushPromises(); await flushPromises()

    await wrapper.find('.primary-action').trigger('click')
    await wrapper.find('[data-confirm]').trigger('click')
    await flushPromises()

    expect(approveIncidentFix).toHaveBeenCalledWith(expect.objectContaining({
      case_id: 'case-1', expected_version: 7, root_cause_attempt_id: 'attempt-1', idempotency_key: 'start-fix:case-1:attempt-1:7', actor_id: 'desktop-user',
    }))
  })

  it('forwards the exact persisted target heads with merge approval', async () => {
    const snapshot = durableDetail('waiting_merge_approval', {
      code_changes: [
        { id: 'a', repo: 'api', fix_commit: 'fix-api', target_environment_branch: 'test', merge_base_head: 'head-api' },
        { id: 'w', repo: 'web', fix_commit: 'fix-web', target_environment_branch: 'test', merge_base_head: 'head-web' },
      ],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([snapshot.case as any])
    vi.mocked(getIncidentCase).mockResolvedValue(snapshot as any)
    vi.mocked(approveIncidentMerge).mockResolvedValue({ ...snapshot.case, status: 'merging', version: 8 } as any)
    const wrapper = mount(BugWorkbenchPage)
    await flushPromises(); await flushPromises()

    await wrapper.find('.primary-action').trigger('click')
    await wrapper.find('[data-confirm]').trigger('click')
    await flushPromises()

    expect(approveIncidentMerge).toHaveBeenCalledWith(expect.objectContaining({
      target_heads: { api: 'head-api', web: 'head-web' },
      fix_commits: { api: 'fix-api', web: 'fix-web' },
      target_branches: { api: 'test', web: 'test' },
    }))
  })

  it.each([
    ['merge_conflict', 'approveIncidentMerge', 'resolve_merge_conflict', 'fix'],
    ['deployment_unverified', 'notifyIncidentDeployed', 'update_deployment_proof', 'regression'],
  ])('uses ContinueIncidentCase for %s recovery before the gated action', async (status, forbidden, decision, phase) => {
    const snapshot = durableDetail(status)
    vi.mocked(listIncidentCases).mockResolvedValue([snapshot.case as any])
    vi.mocked(getIncidentCase).mockResolvedValue(snapshot as any)
    vi.mocked(continueIncidentCase).mockResolvedValue({ ...snapshot.case, status: status === 'merge_conflict' ? 'waiting_merge_approval' : 'waiting_deployment', version: 8 } as any)
    const wrapper = mount(BugWorkbenchPage)
    await flushPromises(); await flushPromises()

    await wrapper.find('.primary-action').trigger('click')
    await wrapper.find('[role="dialog"] textarea').setValue('人工确认已处理')
    await wrapper.find('[data-confirm]').trigger('click')
    await flushPromises()

    expect(continueIncidentCase).toHaveBeenCalledWith(expect.objectContaining({ phase, input_json: expect.objectContaining({ decision, evidence: '人工确认已处理' }) }))
    expect(forbidden === 'approveIncidentMerge' ? approveIncidentMerge : notifyIncidentDeployed).not.toHaveBeenCalled()
  })

  it('recovers an empty migrated selected_bot_key from the latest legacy attempt', async () => {
    const snapshot = durableDetail('legacy_archived', {
      case: { ...durableDetail('legacy_archived').case, selected_bot_key: '' },
      attempts: [{ id: 'legacy-attempt', case_id: 'case-1', cycle_number: 1, phase: 'legacy', mode: '', status: 'succeeded', agent_target: '', bot_key: 'base|codex', input_json: {}, output_json: {}, parent_attempt_id: '', started_at: '2026-07-11', error_code: '', error_message: '', usage: {} }],
    })
    vi.mocked(listIncidentCases).mockResolvedValue([snapshot.case as any])
    vi.mocked(getIncidentCase).mockResolvedValue(snapshot as any)
    vi.mocked(startIncidentCase).mockResolvedValue({ ...snapshot.case, id: 'new-case', status: 'validating' } as any)
    const wrapper = mount(BugWorkbenchPage)
    await flushPromises(); await flushPromises()

    await wrapper.find('.primary-action').trigger('click')
    await flushPromises()

    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({ bot_key: 'base|codex' }))
  })

  it('keeps the Bug inbox reachable when Cases exist and requires bot reselection for an unbound archive', async () => {
    const snapshot = durableDetail('legacy_archived', { case: { ...durableDetail('legacy_archived').case, selected_bot_key: '' } })
    vi.mocked(listIncidentCases).mockResolvedValue([snapshot.case as any])
    vi.mocked(getIncidentCase).mockResolvedValue(snapshot as any)
    vi.mocked(listBugs).mockResolvedValue([{ id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' }])
    vi.mocked(matchBugBots).mockResolvedValue([{ bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] }])
    const wrapper = mount(BugWorkbenchPage)
    await flushPromises(); await flushPromises()

    await wrapper.find('.primary-action').trigger('click')
    await flushPromises()
    expect(startIncidentCase).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('切换到 Bug 收件箱')

    await wrapper.findAll('.workbench-view-tabs button').find(button => button.text() === 'Bug 收件箱')!.trigger('click')
    expect(wrapper.find('.bug-workbench').exists()).toBe(true)
    expect(wrapper.find('.case-lifecycle').exists()).toBe(false)
    expect((wrapper.find('.bot-match input').element as HTMLInputElement).checked).toBe(false)

    await wrapper.find('.bot-match input').setValue(true)
    await wrapper.findAll('.workbench-view-tabs button').find(button => button.text().includes('故障闭环'))!.trigger('click')
    await wrapper.find('.primary-action').trigger('click')
    await flushPromises()
    expect(startIncidentCase).toHaveBeenCalledWith(expect.objectContaining({ bot_key: 'base|codex' }))
  })
})
