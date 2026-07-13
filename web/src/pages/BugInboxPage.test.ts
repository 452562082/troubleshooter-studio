import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  approveIncidentFix,
  approveIncidentMerge,
  cancelBugInvestigation,
  continueIncidentCase,
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
  router.push.mockReset()
  vi.mocked(discoverBots).mockResolvedValue([])
  vi.mocked(fetchBugByID).mockReset()
  vi.mocked(listBugInvestigationRuns).mockResolvedValue([])
  vi.mocked(listBugPlatforms).mockResolvedValue([])
  vi.mocked(listBugs).mockResolvedValue([])
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
  vi.mocked(copyToClipboard).mockResolvedValue(true)
})

describe('BugInboxPage', () => {
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
})
