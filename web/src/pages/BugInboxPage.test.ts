import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  approveIncidentFix,
  approveIncidentMerge,
  cancelBugInvestigation,
  clearBugPlatformLogin,
  continueIncidentCase,
  deleteBugPlatform,
  discoverBots,
  fetchBugByID,
  listBugInvestigationRuns,
  listBugPlatforms,
  listBugs,
  loginBugPlatform,
  notifyIncidentDeployed,
  previewBugAttachment,
  saveBugPlatform,
  startBugInvestigation,
  startIncidentCase,
  syncBugPlatform,
} from '../lib/bridge'
import { copyToClipboard } from '../lib/clipboard'
import { confirmDialog } from '../lib/confirm'
import BugInboxPage from './BugInboxPage.vue'

const router = vi.hoisted(() => ({ push: vi.fn() }))

vi.mock('vue-router', () => ({ useRouter: () => router }))

vi.mock('../lib/bridge', () => ({
  approveIncidentFix: vi.fn(),
  approveIncidentMerge: vi.fn(),
  bugHookBaseURL: vi.fn().mockResolvedValue('http://127.0.0.1:34115'),
  cancelBugInvestigation: vi.fn(),
  clearBugPlatformLogin: vi.fn(),
  continueIncidentCase: vi.fn(),
  deleteBugPlatform: vi.fn(),
  discoverBots: vi.fn().mockResolvedValue([]),
  fetchBugByID: vi.fn(),
  listBugInvestigationRuns: vi.fn().mockResolvedValue([]),
  listBugPlatforms: vi.fn().mockResolvedValue([]),
  listBugs: vi.fn().mockResolvedValue([]),
  loginBugPlatform: vi.fn(),
  notifyIncidentDeployed: vi.fn(),
  previewBugAttachment: vi.fn(),
  saveBugPlatform: vi.fn(),
  startBugInvestigation: vi.fn(),
  startIncidentCase: vi.fn(),
  syncBugPlatform: vi.fn(),
}))

vi.mock('../lib/clipboard', () => ({ copyToClipboard: vi.fn().mockResolvedValue(true) }))
vi.mock('../lib/confirm', () => ({ confirmDialog: vi.fn().mockResolvedValue(true) }))
vi.mock('../lib/toast', () => ({
  toast: { error: vi.fn(), success: vi.fn(), info: vi.fn() },
  toastError: vi.fn(),
}))

const bug = {
  id: 'zentao-840',
  source: 'zentao',
  source_id: '840',
  title: '支付页超时',
  product: '商城',
  module: '结算',
  steps: '**打开结算页**\n\n1. 点击支付',
  description: '接口返回 `504`',
  env: 'test',
  attachments: [{ id: 'shot-1', name: 'timeout.png', type: 'image/png' }],
}

function flushPromises() {
  return new Promise(resolve => setTimeout(resolve, 0))
}

async function mountedInbox() {
  const wrapper = mount(BugInboxPage)
  await flushPromises()
  await flushPromises()
  return wrapper
}

afterEach(() => {
  vi.clearAllMocks()
  router.push.mockReset()
  vi.mocked(discoverBots).mockReset().mockResolvedValue([])
  vi.mocked(clearBugPlatformLogin).mockReset()
  vi.mocked(deleteBugPlatform).mockReset()
  vi.mocked(fetchBugByID).mockReset()
  vi.mocked(listBugInvestigationRuns).mockReset().mockResolvedValue([])
  vi.mocked(listBugPlatforms).mockReset().mockResolvedValue([])
  vi.mocked(listBugs).mockReset().mockResolvedValue([])
  vi.mocked(loginBugPlatform).mockReset()
  vi.mocked(previewBugAttachment).mockReset()
  vi.mocked(saveBugPlatform).mockReset()
  vi.mocked(syncBugPlatform).mockReset()
  vi.mocked(startBugInvestigation).mockReset()
  vi.mocked(cancelBugInvestigation).mockReset()
  vi.mocked(startIncidentCase).mockReset()
  vi.mocked(continueIncidentCase).mockReset()
  vi.mocked(approveIncidentFix).mockReset()
  vi.mocked(approveIncidentMerge).mockReset()
  vi.mocked(notifyIncidentDeployed).mockReset()
  vi.mocked(copyToClipboard).mockReset().mockResolvedValue(true)
  vi.mocked(confirmDialog).mockReset().mockResolvedValue(true)
})

describe('BugInboxPage', () => {
  it.each([375, 768, 1024, 1440])('keeps list, detail and the incident action usable at %dpx', async width => {
    Object.defineProperty(window, 'innerWidth', { configurable: true, value: width })
    const otherBug = { ...bug, id: 'lark-17', source: 'lark', source_id: 'TASK-17', title: '缓存命中下降', product: '平台' }
    vi.mocked(listBugs).mockResolvedValue([bug, otherBug])
    const wrapper = await mountedInbox()
    const root = wrapper.get('.bug-inbox-page')
    const workspace = wrapper.get('.inbox-workspace')
    const listPanel = wrapper.get('.ticket-list-panel')
    const detailPanel = wrapper.get('.ticket-detail-panel')

    expect(root.attributes('data-responsive-viewports')).toBe('375,768,1024,1440')
    expect(root.attributes('data-overflow-safe')).toBe('true')
    expect(workspace.attributes('data-overflow-safe')).toBe('true')
    expect(listPanel.attributes('data-overflow-safe')).toBe('true')
    expect(detailPanel.attributes('data-overflow-safe')).toBe('true')

    await wrapper.get('[data-ticket-id="lark-17"]').trigger('click')
    expect(wrapper.get('[data-ticket-id="lark-17"]').attributes('aria-pressed')).toBe('true')
    expect(detailPanel.get('h2').text()).toBe('缓存命中下降')
    await detailPanel.get('[data-action="open-incident"]').trigger('click')
    expect(router.push).toHaveBeenCalledWith({ path: '/incidents', query: { bug_id: 'lark-17' } })
    wrapper.unmount()
  })

  it('is a browse-only inbox and opens the selected ticket in the incident route', async () => {
    vi.mocked(listBugs).mockResolvedValue([bug])
    const wrapper = await mountedInbox()

    expect(wrapper.text()).toContain('Bug 工单')
    expect(wrapper.text()).toContain('复现步骤')
    expect(wrapper.text()).toContain('打开结算页')
    expect(wrapper.findComponent({ name: 'BugTicketList' }).exists()).toBe(true)
    expect(wrapper.findComponent({ name: 'BugTicketDetail' }).props('mode')).toBe('full')
    expect(wrapper.text()).not.toContain('开始故障闭环')
    expect(wrapper.text()).not.toContain('允许修复')
    expect(wrapper.find('.workbench-view-tabs').exists()).toBe(false)

    await wrapper.get('[data-action="open-incident"]').trigger('click')

    expect(router.push).toHaveBeenCalledWith({ path: '/incidents', query: { bug_id: 'zentao-840' } })
    expect(startBugInvestigation).not.toHaveBeenCalled()
    expect(cancelBugInvestigation).not.toHaveBeenCalled()
    expect(startIncidentCase).not.toHaveBeenCalled()
    expect(continueIncidentCase).not.toHaveBeenCalled()
    expect(approveIncidentFix).not.toHaveBeenCalled()
    expect(approveIncidentMerge).not.toHaveBeenCalled()
    expect(notifyIncidentDeployed).not.toHaveBeenCalled()
  })

  it('filters shared ticket rows and selects another full detail', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      bug,
      { ...bug, id: 'lark-17', source: 'lark', source_id: 'TASK-17', title: '缓存命中下降', product: '平台' },
    ])
    const wrapper = await mountedInbox()

    await wrapper.get('input[type="search"]').setValue('lark')
    expect(wrapper.findAll('[data-ticket-id]')).toHaveLength(1)
    await wrapper.get('[data-ticket-id="lark-17"]').trigger('click')

    expect(wrapper.get('.ticket-detail h2').text()).toBe('缓存命中下降')
    expect(wrapper.text()).toContain('平台')
  })

  it('keeps platform configuration collapsed and saves mapped bots with their environment', async () => {
    vi.mocked(discoverBots).mockResolvedValue([{
      path: '/repo/base',
      ghost: false,
      meta: { system_id: 'base', system_name: 'Base', target: 'codex', agent_id: 'base-troubleshooter' },
      environments: ['test', 'prod'],
    } as any])
    vi.mocked(saveBugPlatform).mockResolvedValue({
      id: 'zentao-main', name: '禅道', type: 'zentao', base_url: 'https://zentao.example.com',
      auth_mode: 'feishu_sso', bot_mappings: [{ bot_key: '/repo/base|codex', env: 'prod' }], enabled: true,
    })
    const wrapper = await mountedInbox()

    expect(wrapper.find('.platform-config').classes()).not.toContain('open')
    await wrapper.get('[data-action="toggle-platform-config"]').trigger('click')
    expect(wrapper.text()).toContain('飞书授权登录')
    expect(wrapper.text()).toContain('后台定时同步')
    expect(wrapper.find('input[aria-label="后台同步间隔分钟"]').exists()).toBe(true)
    expect(wrapper.find('input[placeholder="我的禅道账号"]').exists()).toBe(false)

    await wrapper.get('input[placeholder="平台名称,如 测试环境 Bug 平台"]').setValue('禅道')
    await wrapper.get('input[placeholder="平台地址 https://bug-platform.example.com"]').setValue('https://zentao.example.com')
    await wrapper.get('[data-action="toggle-bot-picker"]').trigger('click')
    await wrapper.get('[data-bot-key="/repo/base|codex"]').trigger('click')
    await wrapper.get('.bot-config-row select').setValue('prod')
    await wrapper.get('[data-action="save-platform"]').trigger('click')

    expect(saveBugPlatform).toHaveBeenCalledWith(expect.objectContaining({
      name: '禅道',
      base_url: 'https://zentao.example.com',
      bot_mappings: [{ bot_key: '/repo/base|codex', env: 'prod' }],
    }))
  })

  it('exposes the selected platform chip to assistive technology', async () => {
    vi.mocked(listBugPlatforms).mockResolvedValue([
      { id: 'zentao-main', name: '禅道', type: 'zentao', enabled: true },
      { id: 'generic-backup', name: '备用平台', type: 'generic', enabled: false },
    ])
    const wrapper = await mountedInbox()
    await wrapper.get('[data-action="toggle-platform-config"]').trigger('click')

    const chips = wrapper.findAll('.platform-chip')
    expect(chips[0].attributes('aria-pressed')).toBe('true')
    expect(chips[0].classes()).toContain('active')
    expect(chips[1].attributes('aria-pressed')).toBe('false')

    await chips[1].trigger('click')

    expect(chips[0].attributes('aria-pressed')).toBe('false')
    expect(chips[1].attributes('aria-pressed')).toBe('true')
    expect(chips[1].classes()).toContain('active')
  })

  it('preserves the platform config structure while ticket loading is pending', async () => {
    vi.mocked(listBugs).mockReturnValue(new Promise(() => {}))
    const wrapper = mount(BugInboxPage)
    await flushPromises()
    await wrapper.get('[data-action="toggle-platform-config"]').trigger('click')

    expect(wrapper.find('.config-row.basic-row').exists()).toBe(true)
    expect(wrapper.find('.config-row.auth-row').exists()).toBe(true)
    expect(wrapper.find('.config-row.ops-row').exists()).toBe(true)
    expect(wrapper.get('button.add-platform[aria-label="新增平台"]').text()).toBe('+')
    expect(wrapper.text()).toContain('同步我的 Bug')
    expect(wrapper.find('input[placeholder="指派人账号,仅后台同步时用于筛选"]').exists()).toBe(false)
    expect(wrapper.find('input[placeholder="Hook Secret,留空自动生成"]').exists()).toBe(false)
    expect(wrapper.get('[data-action="login-platform"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.get('[data-action="save-platform"]').attributes('disabled')).toBeUndefined()
  })

  it('clears a saved platform login session', async () => {
    const loggedIn = {
      id: 'zentao-main', name: '禅道', type: 'zentao', base_url: 'https://zentao.example.com',
      auth_mode: 'feishu_sso', session_header: 'cookie=secret', enabled: true,
    }
    const loggedOut = { ...loggedIn, session_header: '' }
    vi.mocked(listBugPlatforms).mockResolvedValueOnce([loggedIn]).mockResolvedValue([loggedOut])
    vi.mocked(clearBugPlatformLogin).mockResolvedValue({
      platform_id: 'zentao-main', auth_mode: 'feishu_sso', session_saved: false, cookie_count: 0,
    })
    const wrapper = await mountedInbox()
    await wrapper.get('[data-action="toggle-platform-config"]').trigger('click')

    expect(wrapper.text()).toContain('已保存')
    await wrapper.get('[data-action="clear-platform-login"]').trigger('click')
    await flushPromises()

    expect(clearBugPlatformLogin).toHaveBeenCalledWith({ platform_id: 'zentao-main' })
    expect(wrapper.text()).toContain('未登录')
  })

  it('requires confirmation before deleting a platform', async () => {
    const platform = { id: 'zentao-main', name: '禅道', type: 'zentao', enabled: true }
    vi.mocked(listBugPlatforms).mockResolvedValue([platform])
    vi.mocked(confirmDialog).mockResolvedValueOnce(false).mockResolvedValueOnce(true)
    vi.mocked(deleteBugPlatform).mockResolvedValue()
    const wrapper = await mountedInbox()
    await wrapper.get('[data-action="toggle-platform-config"]').trigger('click')

    await wrapper.get('[data-action="delete-platform"]').trigger('click')
    expect(deleteBugPlatform).not.toHaveBeenCalled()

    await wrapper.get('[data-action="delete-platform"]').trigger('click')
    await flushPromises()

    expect(confirmDialog).toHaveBeenLastCalledWith(expect.objectContaining({
      title: '删除平台', danger: true, defaultAction: 'cancel',
    }))
    expect(deleteBugPlatform).toHaveBeenCalledWith({ platform_id: 'zentao-main' })
  })

  it('copies the exact encoded Hook URL for the selected platform', async () => {
    vi.mocked(listBugPlatforms).mockResolvedValue([{
      id: 'zentao/main', name: '禅道', type: 'zentao', hook_secret: 'a+b&c', enabled: true,
    }])
    const wrapper = await mountedInbox()
    await wrapper.get('[data-action="toggle-platform-config"]').trigger('click')

    await wrapper.get('[data-action="copy-hook-url"]').trigger('click')

    expect(copyToClipboard).toHaveBeenCalledWith('http://127.0.0.1:34115/api/bug-hooks/zentao%2Fmain?secret=a%2Bb%26c')
  })

  it('logs in, synchronizes assigned bugs, and manually fetches a ticket', async () => {
    const platform = {
      id: 'zentao-main', name: '禅道', type: 'zentao', base_url: 'https://zentao.example.com',
      auth_mode: 'feishu_sso', enabled: true,
    }
    vi.mocked(listBugPlatforms).mockResolvedValue([platform])
    vi.mocked(saveBugPlatform).mockResolvedValue(platform)
    vi.mocked(loginBugPlatform).mockResolvedValue({ platform_id: 'zentao-main', auth_mode: 'feishu_sso', session_saved: true, cookie_count: 2 })
    vi.mocked(syncBugPlatform).mockResolvedValue({ platform_id: 'zentao-main', fetched: 2, stored: 2, selected_bug_id: 'zentao-840' })
    vi.mocked(fetchBugByID).mockResolvedValue({ platform_id: 'zentao-main', fetched: 1, stored: 1, selected_bug_id: 'zentao-840' })
    vi.mocked(listBugs).mockResolvedValue([bug])
    const wrapper = await mountedInbox()

    await wrapper.get('[data-action="toggle-platform-config"]').trigger('click')
    await wrapper.get('[data-action="login-platform"]').trigger('click')
    await flushPromises()
    expect(loginBugPlatform).toHaveBeenCalledWith({ platform_id: 'zentao-main' })

    await wrapper.get('[data-action="sync-platform"]').trigger('click')
    await flushPromises()
    expect(syncBugPlatform).toHaveBeenCalledWith('zentao-main')

    await wrapper.get('input[placeholder="Bug ID 或飞书消息"]').setValue('#840')
    await wrapper.get('[data-action="fetch-bug"]').trigger('click')
    await flushPromises()
    expect(fetchBugByID).toHaveBeenCalledWith({ platform_id: 'zentao-main', bug_id: '#840' })
  })

  it('previews attachments from the shared full detail', async () => {
    vi.mocked(listBugPlatforms).mockResolvedValue([
      { id: 'zentao-main', name: '禅道', type: 'zentao', enabled: true },
    ])
    vi.mocked(listBugs).mockResolvedValue([bug])
    vi.mocked(previewBugAttachment).mockResolvedValue({
      name: 'timeout.png', content_type: 'image/png', data_url: 'data:image/png;base64,AA==',
    })
    const wrapper = await mountedInbox()

    await wrapper.get('[data-attachment-index="0"]').trigger('click')
    await flushPromises()

    expect(previewBugAttachment).toHaveBeenCalledWith({
      platform_id: 'zentao-main', bug_id: 'zentao-840', attachment_index: 0,
    })
    expect(wrapper.get('.attachment-preview-image').attributes('src')).toBe('data:image/png;base64,AA==')
  })

  it('renders ZenTao raw HTML as rich text with the complete public Bug fields', async () => {
    vi.mocked(listBugs).mockResolvedValue([{
      id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常',
      product: 'S基建项目', module: 'frontend', bug_type: '代码错误', severity: '3', priority: '1',
      assignee: 'alice', reporter: 'bob', created_at: '2026-07-03T10:03:21Z', updated_at: '2026-07-03T11:04:22Z',
      os: 'WEB', browser: 'Chrome', keywords: 'PC 搜索',
      frontend_repo: 'pc-web', frontend_url: 'https://example.com/search', api_paths: ['/api/search'],
      trace_ids: ['trace-1'], request_ids: ['request-1'],
      steps: '<p>[步骤]</p><ol><li>PC端进入搜索页</li><li>输入电影名称</li></ol>',
    }])
    const wrapper = await mountedInbox()
    const detail = wrapper.get('.ticket-detail')

    expect(detail.get('.ticket-markdown li').text()).toBe('PC端进入搜索页')
    expect(detail.text()).not.toContain('<p>[步骤]</p>')
    for (const text of ['S基建项目', 'frontend', '代码错误', 'S3', 'P1', 'alice', 'bob', 'WEB', 'Chrome', 'PC 搜索']) {
      expect(detail.get('.ticket-fields').text()).toContain(text)
    }
    expect(detail.text()).not.toContain('API 路径')
    expect(detail.text()).not.toContain('Trace / Request')
    expect(detail.text()).not.toContain('前端仓库')
    expect(detail.text()).not.toContain('前端 URL')
    expect(detail.text()).not.toContain('/api/search')
    expect(detail.text()).not.toContain('trace-1')
  })

  it('keeps legacy runs collapsed, rendered, and read-only', async () => {
    vi.mocked(listBugs).mockResolvedValue([bug])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([{
      id: 'run-1', bug_id: 'zentao-840', bot_key: 'base|codex', status: 'succeeded',
      final_message: '## 历史结论\n\n缓存配置错误',
      events: [{ type: 'agent_message', message: '读取缓存配置' }],
    }])
    const wrapper = await mountedInbox()

    const history = wrapper.get('.legacy-history')
    expect(history.attributes('open')).toBeUndefined()
    expect(history.get('summary').text()).toContain('历史运行记录（只读）')
    expect(history.text()).not.toContain('停止')
    expect(history.text()).not.toContain('启动修复 Agent')

    await history.get('summary').trigger('click')
    expect(history.get('.markdown-result h2').text()).toBe('历史结论')
    expect(history.text()).toContain('读取缓存配置')

    await history.get('[data-action="copy-legacy-result"]').trigger('click')
    expect(copyToClipboard).toHaveBeenCalledWith('## 历史结论\n\n缓存配置错误')
  })

  it('separates validation, investigation, and fix snapshots and copies the fix final first', async () => {
    vi.mocked(listBugs).mockResolvedValue([bug])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([{
      id: 'run-fix', bug_id: 'zentao-840', bot_key: 'base|codex', status: 'succeeded',
      prompt_preview: '启动修复 Agent', final_message: '## 修复结果\n\n提交 `fix-123`',
      events: [
        { type: 'stage', message: '验证 Agent 开始取证', meta: { phase: 'validation' } },
        { type: 'agent_message', message: 'verification_status: reproduced', meta: { phase: 'validation' } },
        { type: 'agent_message', message: '定位缓存根因', meta: { phase: 'investigation' } },
        { type: 'agent_message', message: '修改缓存配置', meta: { phase: 'fix' } },
      ],
    }])
    const wrapper = await mountedInbox()
    const history = wrapper.get('.legacy-history')
    await history.get('summary').trigger('click')

    expect(history.get('[role="tablist"]').attributes('aria-label')).toContain('历史验证与排障输出')
    expect(history.get('.output-tab.active').text()).toContain('修复提交')
    expect(history.get('.context-preview').text()).toContain('修改缓存配置')
    expect(history.get('.context-preview').text()).toContain('提交 fix-123')
    expect(history.get('.context-preview').text()).not.toContain('定位缓存根因')

    const validationTab = history.findAll('.output-tab').find(tab => tab.text().includes('验证证据'))!
    await validationTab.trigger('click')
    expect(history.get('.context-preview').text()).toContain('verification_status: reproduced')
    expect(history.get('.context-preview').text()).not.toContain('定位缓存根因')

    const investigationTab = history.findAll('.output-tab').find(tab => tab.text().includes('排障分析'))!
    await investigationTab.trigger('click')
    expect(history.get('.context-preview').text()).toContain('定位缓存根因')
    expect(history.get('.context-preview').text()).not.toContain('修改缓存配置')

    await history.get('[data-action="copy-legacy-result"]').trigger('click')
    expect(copyToClipboard).toHaveBeenCalledWith('## 修复结果\n\n提交 `fix-123`')
  })

  it('normalizes a validation final and selects its current phase', async () => {
    vi.mocked(listBugs).mockResolvedValue([bug])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([{
      id: 'run-validation', bug_id: 'zentao-840', bot_key: 'base|codex', status: 'succeeded',
      prompt_preview: '验证 Agent',
      final_message: '### 验证报告 | bug env: -, bot env: test | 未复现\n\n- 结论: 未复现原始 Bug',
      events: [{ type: 'stage', message: '验证完成', meta: { phase: 'validation' } }],
    }])
    const wrapper = await mountedInbox()

    expect(wrapper.get('.output-tab.active').text()).toContain('验证证据')
    expect(wrapper.get('.context-preview').text()).toContain('验证报告 | test | 未复现')
    expect(wrapper.get('.context-preview').text()).not.toContain('bug env: -')
  })

  it('promotes a terminal investigation report above completion markers', async () => {
    vi.mocked(listBugs).mockResolvedValue([bug])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([{
      id: 'run-report', bug_id: 'zentao-840', bot_key: 'base|codex', status: 'succeeded',
      events: [
        { type: 'agent_message', message: '### 1. 现象复述\n\n| # | 事实 |\n|---|---|\n| 1 | 搜索结果显示一集全 |', meta: { phase: 'investigation' } },
        { type: 'turn_completed', message: '排障完成', meta: { phase: 'investigation' } },
      ],
    }])
    const wrapper = await mountedInbox()

    expect(wrapper.get('.context-preview .markdown-result h3').text()).toBe('1. 现象复述')
    expect(wrapper.find('.process-log').text()).not.toContain('|---|')
  })

  it('selects investigation errors and does not copy running process logs', async () => {
    vi.mocked(listBugs).mockResolvedValue([bug])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([{
      id: 'run-failed', bug_id: 'zentao-840', bot_key: 'base|codex', status: 'failed', error: 'request timed out',
      events: [{ type: 'agent_message', message: 'verification_status: reproduced', meta: { phase: 'validation' } }],
    }])
    const failed = await mountedInbox()
    expect(failed.get('.output-tab.active').text()).toContain('排障分析')
    expect(failed.get('.context-preview').text()).toContain('request timed out')
    expect(failed.get('.context-preview').text()).not.toContain('verification_status')

    vi.mocked(listBugInvestigationRuns).mockResolvedValue([{
      id: 'run-running', bug_id: 'zentao-840', bot_key: 'base|codex', status: 'running',
      events: [{ type: 'agent_message', message: '正在读取配置', meta: { phase: 'investigation' } }],
    }])
    const running = await mountedInbox()
    expect(running.get('.context-preview').text()).toContain('正在读取配置')
    expect(running.find('[data-action="copy-legacy-result"]').exists()).toBe(false)
  })

  it('shows and copies saved context when no legacy run exists', async () => {
    vi.mocked(listBugs).mockResolvedValue([{ ...bug, last_context: '只读机器人上下文' }])
    const wrapper = await mountedInbox()

    expect(wrapper.get('.generated-context-panel').text()).toContain('只读机器人上下文')
    await wrapper.get('[data-action="copy-legacy-context"]').trigger('click')
    expect(copyToClipboard).toHaveBeenCalledWith('只读机器人上下文')
  })
})
