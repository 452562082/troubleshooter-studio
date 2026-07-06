import { mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { cancelBugInvestigation, listBugs, matchBugBots, startBugInvestigation, listBugInvestigationRuns } from '../lib/bridge'
import BugWorkbenchPage from './BugWorkbenchPage.vue'

vi.mock('../lib/bridge', () => ({
  bugHookBaseURL: vi.fn().mockResolvedValue(''),
  cancelBugInvestigation: vi.fn(),
  clearBugPlatformLogin: vi.fn(),
  deleteBugPlatform: vi.fn(),
  fetchBugByID: vi.fn(),
  generateBugContext: vi.fn(),
  loginBugPlatform: vi.fn(),
  listBugInvestigationRuns: vi.fn().mockResolvedValue([]),
  listBugPlatforms: vi.fn().mockResolvedValue([]),
  listBugs: vi.fn().mockResolvedValue([]),
  matchBugBots: vi.fn().mockResolvedValue([]),
  saveBugPlatform: vi.fn(),
  startBugInvestigation: vi.fn(),
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
  vi.mocked(cancelBugInvestigation).mockReset()
  vi.mocked(listBugInvestigationRuns).mockResolvedValue([])
  vi.mocked(listBugs).mockResolvedValue([])
  vi.mocked(matchBugBots).mockResolvedValue([])
  vi.mocked(startBugInvestigation).mockReset()
})

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
    expect(wrapper.text()).toContain('登录禅道')
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

    const loginButton = wrapper.findAll('button').find(button => button.text() === '登录禅道')
    const saveButton = wrapper.findAll('button').find(button => button.text() === '保存配置')
    expect(loginButton?.attributes('disabled')).toBeUndefined()
    expect(saveButton?.attributes('disabled')).toBeUndefined()
  })

  it('labels immediate sync as syncing my assigned bugs', async () => {
    const wrapper = mount(BugWorkbenchPage)

    expect(wrapper.text()).toContain('同步我的 Bug')
  })

  it('renders bot matches when backend returns null reasons', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-1842', source: 'zentao', source_id: '1842', title: '支付页 500' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'shop-prod', system_id: 'shop', target: 'prod', path: '/bots/shop' }, score: 10, reasons: null as any },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await new Promise(resolve => setTimeout(resolve, 0))
    await new Promise(resolve => setTimeout(resolve, 0))

    expect(wrapper.text()).toContain('shop')
    expect(wrapper.text()).toContain('无显式匹配,可手动选择')
  })

  it('shows start investigation for codex bot', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' }, score: 10, reasons: [] },
    ])
    const wrapper = mount(BugWorkbenchPage)
    await new Promise(resolve => setTimeout(resolve, 0))
    await new Promise(resolve => setTimeout(resolve, 0))

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
    await new Promise(resolve => setTimeout(resolve, 0))
    await new Promise(resolve => setTimeout(resolve, 0))

    const button = wrapper.findAll('button').find(b => b.text() === '开始排障')
    expect(button).toBeTruthy()
    await button!.trigger('click')

    expect(startBugInvestigation).toHaveBeenCalledWith({
      bug_id: 'zentao-577',
      bot: { key: 'base|codex', system_id: 'base', target: 'codex', path: '/repo' },
    })
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
    await new Promise(resolve => setTimeout(resolve, 0))
    await new Promise(resolve => setTimeout(resolve, 0))

    expect(wrapper.text()).toContain('缓存配置错误')
  })

  it('disables start investigation and explains unsupported non-codex bot', async () => {
    vi.mocked(listBugs).mockResolvedValue([
      { id: 'zentao-577', source: 'zentao', source_id: '577', title: '搜索页异常' },
    ])
    vi.mocked(matchBugBots).mockResolvedValue([
      { bot: { key: 'base|cursor', system_id: 'base', target: 'cursor', path: '/repo' }, score: 10, reasons: [] },
    ])

    const wrapper = mount(BugWorkbenchPage)
    await new Promise(resolve => setTimeout(resolve, 0))
    await new Promise(resolve => setTimeout(resolve, 0))

    expect(wrapper.text()).toContain('当前只支持 Codex 机器人直接排障。')
    const button = wrapper.findAll('button').find(b => b.text() === '开始排障')
    expect(button?.attributes('disabled')).toBeDefined()
  })
})
