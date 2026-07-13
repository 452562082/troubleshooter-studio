import { readFileSync } from 'node:fs'
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

  it('declares concrete mobile touch-target and full-width save contracts', () => {
    const source = readFileSync('src/pages/BugInboxPage.vue', 'utf8')
    const mobileCSS = source.split('@media (max-width: 640px) {')[1]?.split('\n}')[0] || ''

    expect(mobileCSS).toMatch(/\.config-disclosure,[^{]*\.platform-chip,[^{]*\.bot-picker-row,[^{]*\.interval-control input \{ min-height: 44px; \}/)
    expect(mobileCSS).toContain('.platform-config .form-control, .compact-button, .danger-link, .toggle-control { min-height: 44px; }')
    expect(mobileCSS).toContain('.bot-config-row .icon-button { justify-self: end; width: 44px; height: 44px; }')
    expect(mobileCSS).toContain('.config-footer { align-items: stretch; flex-direction: column; }')
    expect(mobileCSS).toContain('.config-footer .danger-link { align-self: flex-start; }')
    expect(mobileCSS).toContain('.config-footer .primary-button { width: 100%; min-width: 0; }')
  })

  it('declares readable panel disabled colors and red danger-icon focus treatment', () => {
    const source = readFileSync('src/pages/BugInboxPage.vue', 'utf8')

    expect(source).not.toMatch(/\.compact-button:disabled[^}]*opacity/)
    expect(source).toMatch(/\.compact-button:disabled,[\s\S]*?\.icon-button:disabled \{[^}]*border-color:[^;]+;[^}]*background:[^;]+;[^}]*color:[^;]+;[^}]*cursor: not-allowed;/)
    expect(source).toMatch(/\.compact-button:disabled svg,[\s\S]*?\.icon-button:disabled svg \{ color: [^;]+; \}/)
    expect(source).toMatch(/\.platform-config input:disabled, \.platform-config select:disabled \{[^}]*border-color:[^;]+;[^}]*background:[^;]+;[^}]*color:[^;]+;[^}]*cursor: not-allowed;/)
    expect(source).toContain('.danger-icon-button:hover, .danger-icon-button:focus-visible { background: var(--c-danger-bg); color: var(--c-danger); }')
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

    await wrapper.get('input[placeholder="如：测试环境"]').setValue('禅道')
    await wrapper.get('input[placeholder="https://bug-platform.example.com"]').setValue('https://zentao.example.com')
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

  it('presents platform configuration as labelled compact sections with a readable disclosure state', async () => {
    vi.mocked(listBugPlatforms).mockResolvedValue([{
      id: 'zentao-main', name: '测试环境', type: 'zentao', base_url: 'https://zentao.example.com',
      auth_mode: 'feishu_sso', enabled: true,
    }])
    const wrapper = await mountedInbox()
    const disclosure = wrapper.get('[data-action="toggle-platform-config"]')

    expect(disclosure.attributes('aria-expanded')).toBe('false')
    expect(disclosure.attributes('aria-controls')).toBe('bug-platform-config')
    expect(disclosure.text()).toContain('平台配置')
    expect(disclosure.findAll('svg')).toHaveLength(2)

    await disclosure.trigger('click')

    expect(disclosure.attributes('aria-expanded')).toBe('true')
    expect(disclosure.classes()).toContain('expanded')
    expect(disclosure.text()).toContain('收起配置')
    const config = wrapper.get('#bug-platform-config')
    expect(config.attributes('data-density')).toBe('compact')
    expect(config.attributes('data-responsive-viewports')).toBe('375,768,1024,1440')
    expect(config.findAll('.platform-config-section h2').map(node => node.text())).toEqual([
      '平台信息', '排障机器人', '同步与接入',
    ])
    expect(config.findAll('.field-label > span').map(node => node.text())).toEqual(expect.arrayContaining([
      '平台名称', '平台类型', '平台地址', '登录方式',
    ]))
    expect(config.get('.login-status-badge').text()).toBe('未登录')
  })

  it('keeps save beside platform status and persists disabling the platform', async () => {
    const platform = {
      id: 'zentao-main', name: '测试环境', type: 'zentao', base_url: 'https://zentao.example.com',
      auth_mode: 'feishu_sso', enabled: true,
    }
    vi.mocked(listBugPlatforms).mockResolvedValue([platform])
    vi.mocked(saveBugPlatform).mockResolvedValue({ ...platform, enabled: false })
    const wrapper = await mountedInbox()
    await wrapper.get('[data-action="toggle-platform-config"]').trigger('click')

    const details = wrapper.get('.platform-details-section')
    const footer = wrapper.get('.config-footer')
    expect(details.element.nextElementSibling).toBe(footer.element)
    expect(footer.text()).toContain('修改后需保存才会生效')

    await details.get('.toggle-control input').setValue(false)
    await footer.get('[data-action="save-platform"]').trigger('click')

    expect(saveBugPlatform).toHaveBeenCalledWith(expect.objectContaining({
      id: 'zentao-main',
      enabled: false,
    }))

    const source = readFileSync('src/pages/BugInboxPage.vue', 'utf8')
    expect(source).toMatch(/\.config-footer \{[^}]*position: sticky;[^}]*top: var\(--sp-2\);[^}]*z-index: 5;/)
  })

  it('uses SVG actions and separates destructive platform deletion from the primary save action', async () => {
    vi.mocked(discoverBots).mockResolvedValue([{
      path: '/repo/base', ghost: false,
      meta: { system_id: 'base', system_name: 'Base', target: 'codex', agent_id: 'base-troubleshooter' },
      environments: ['test'],
    } as any])
    vi.mocked(listBugPlatforms).mockResolvedValue([{
      id: 'zentao-main', name: '测试环境', type: 'zentao', auth_mode: 'feishu_sso',
      bot_mappings: [{ bot_key: '/repo/base|codex', env: 'test' }], enabled: true,
    }])
    const wrapper = await mountedInbox()
    await wrapper.get('[data-action="toggle-platform-config"]').trigger('click')

    const newPlatform = wrapper.get('[data-action="new-platform"]')
    expect(newPlatform.text()).toContain('新建平台')
    expect(newPlatform.find('svg[aria-hidden="true"]').exists()).toBe(true)
    expect(newPlatform.text()).not.toBe('+')

    const addBot = wrapper.get('[data-action="toggle-bot-picker"]')
    expect(addBot.find('svg[aria-hidden="true"]').exists()).toBe(true)

    const sync = wrapper.get('[data-action="sync-platform"]')
    expect(sync.classes()).toContain('secondary-button')
    expect(sync.classes()).not.toContain('accent-button')
    expect(sync.classes()).not.toContain('primary-button')

    const removeBot = wrapper.get('button.icon-button[aria-label="移除机器人"]')
    expect(removeBot.find('svg[aria-hidden="true"]').exists()).toBe(true)
    expect(removeBot.text()).toBe('')
    await removeBot.trigger('click')
    expect(wrapper.find('.bot-config-row').exists()).toBe(false)

    const footer = wrapper.get('.config-footer')
    expect(footer.findAll('button')[0].attributes('data-action')).toBe('delete-platform')
    expect(footer.findAll('button').slice(-1)[0]?.attributes('data-action')).toBe('save-platform')
    expect(footer.get('[data-action="delete-platform"]').classes()).toContain('danger-link')
    expect(footer.get('[data-action="save-platform"]').classes()).toContain('primary-button')
  })

  it('keeps platform sync separate from local list refresh', async () => {
    const platform = {
      id: 'zentao-main', name: '测试环境', type: 'zentao',
      auth_mode: 'feishu_sso', enabled: true,
    }
    vi.mocked(listBugPlatforms).mockResolvedValue([platform])
    vi.mocked(listBugs).mockResolvedValue([bug])
    vi.mocked(syncBugPlatform).mockResolvedValue({
      platform_id: 'zentao-main', fetched: 1, stored: 1,
    })
    const wrapper = await mountedInbox()
    await wrapper.get('[data-action="toggle-platform-config"]').trigger('click')

    vi.mocked(listBugs).mockClear()
    vi.mocked(syncBugPlatform).mockClear()
    const refresh = wrapper.get('[data-action="refresh-tickets"]')
    expect(refresh.text()).toContain('刷新列表')
    expect(refresh.find('svg[aria-hidden="true"]').exists()).toBe(true)
    await refresh.trigger('click')
    await flushPromises()
    expect(listBugs).toHaveBeenCalledTimes(1)
    expect(syncBugPlatform).not.toHaveBeenCalled()

    vi.mocked(listBugs).mockClear()
    const sync = wrapper.get('[data-action="sync-platform"]')
    expect(sync.text()).toContain('从平台同步')
    expect(sync.find('svg[aria-hidden="true"]').exists()).toBe(true)
    await sync.trigger('click')
    await flushPromises()
    expect(syncBugPlatform).toHaveBeenCalledWith('zentao-main')
    expect(listBugs).toHaveBeenCalledTimes(1)
  })

  it('shows a disabled loading state while refreshing the local list', async () => {
    const wrapper = await mountedInbox()
    let resolveRefresh!: (bugs: (typeof bug)[]) => void
    vi.mocked(listBugs).mockImplementationOnce(() => new Promise(resolve => {
      resolveRefresh = resolve
    }))

    const refresh = wrapper.get('[data-action="refresh-tickets"]')
    await refresh.trigger('click')
    await wrapper.vm.$nextTick()
    expect(refresh.attributes('disabled')).toBeDefined()
    expect(refresh.text()).toContain('刷新中…')
    expect(refresh.get('svg').classes()).toContain('spinning')

    resolveRefresh([bug])
    await flushPromises()
    expect(refresh.attributes('disabled')).toBeUndefined()
    expect(refresh.text()).toContain('刷新列表')
  })

  it('shows a disabled loading state while synchronizing the platform', async () => {
    const platform = {
      id: 'zentao-main', name: '测试环境', type: 'zentao',
      auth_mode: 'feishu_sso', enabled: true,
    }
    vi.mocked(listBugPlatforms).mockResolvedValue([platform])
    const wrapper = await mountedInbox()
    await wrapper.get('[data-action="toggle-platform-config"]').trigger('click')
    let resolveSync!: (result: { platform_id: string; fetched: number; stored: number }) => void
    vi.mocked(syncBugPlatform).mockImplementationOnce(() => new Promise(resolve => {
      resolveSync = resolve
    }))

    const sync = wrapper.get('[data-action="sync-platform"]')
    await sync.trigger('click')
    await wrapper.vm.$nextTick()
    expect(sync.attributes('disabled')).toBeDefined()
    expect(sync.text()).toContain('同步中…')

    resolveSync({ platform_id: 'zentao-main', fetched: 1, stored: 1 })
    await flushPromises()
    expect(sync.attributes('disabled')).toBeUndefined()
    expect(sync.text()).toContain('从平台同步')
  })

  it('associates visible labels with bot environment, bot search, and manual Bug controls', async () => {
    vi.mocked(discoverBots).mockResolvedValue([
      {
        path: '/repo/base', ghost: false,
        meta: { system_id: 'base', system_name: 'Base', target: 'codex', agent_id: 'base-troubleshooter' },
        environments: ['test'],
      } as any,
      {
        path: '/repo/payments', ghost: false,
        meta: { system_id: 'payments', system_name: 'Payments', target: 'codex', agent_id: 'payments-troubleshooter' },
        environments: ['staging'],
      } as any,
    ])
    vi.mocked(listBugPlatforms).mockResolvedValue([{
      id: 'zentao-main', name: '测试环境', type: 'zentao', auth_mode: 'feishu_sso',
      bot_mappings: [
        { bot_key: '/repo/base|codex', env: 'test' },
        { bot_key: '/repo/payments|codex', env: 'staging' },
      ], enabled: true,
    }])
    const wrapper = await mountedInbox()
    await wrapper.get('[data-action="toggle-platform-config"]').trigger('click')

    const environmentLabels = wrapper.findAll('.bot-env-field')
    expect(environmentLabels).toHaveLength(2)
    expect(new Set(environmentLabels.map(label => label.get('.form-control').attributes('id'))).size).toBe(2)
    const environmentLabel = environmentLabels[0]
    const environmentControl = environmentLabel.get('.form-control')
    expect(environmentLabel.text()).toContain('机器人环境')
    expect(environmentLabel.attributes('for')).toBe(environmentControl.attributes('id'))
    const accessibleNameIDs = environmentControl.attributes('aria-labelledby')
    expect(accessibleNameIDs).toBeDefined()
    const labelledBy = accessibleNameIDs!.split(' ')
    expect(labelledBy).toHaveLength(2)
    expect(wrapper.get(`[id="${labelledBy[0]}"]`).text()).toBe('Base')
    expect(wrapper.get(`[id="${labelledBy[1]}"]`).text()).toBe('机器人环境')

    await wrapper.get('[data-action="toggle-bot-picker"]').trigger('click')
    const searchLabel = wrapper.get('.bot-search-field')
    expect(searchLabel.text()).toContain('搜索机器人')
    expect(searchLabel.attributes('for')).toBe(searchLabel.get('input').attributes('id'))

    const manualLabel = wrapper.get('.manual-bug-field')
    expect(manualLabel.text()).toContain('指定 Bug')
    expect(manualLabel.attributes('for')).toBe(manualLabel.get('input').attributes('id'))
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
    expect(wrapper.find('.sync-access-section').exists()).toBe(true)
    expect(wrapper.find('.config-footer').exists()).toBe(true)
    const newPlatform = wrapper.get('[data-action="new-platform"]')
    expect(newPlatform.text()).toContain('新建平台')
    expect(newPlatform.find('svg[aria-hidden="true"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('从平台同步')
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

    expect(wrapper.text()).toContain('已登录')
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

    const closeButton = wrapper.get('button.attachment-preview-close[aria-label="关闭附件预览"]')
    expect(closeButton.find('svg[aria-hidden="true"]').exists()).toBe(true)
    expect(closeButton.text()).toBe('')

    await closeButton.trigger('click')

    expect(wrapper.find('.attachment-preview-modal').exists()).toBe(false)
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

  it('does not load or render legacy runs and saved bot context', async () => {
    vi.mocked(listBugs).mockResolvedValue([{ ...bug, last_context: '旧机器人上下文' }])
    vi.mocked(listBugInvestigationRuns).mockResolvedValue([{
      id: 'run-1', bug_id: 'zentao-840', bot_key: 'base|codex', status: 'succeeded',
      final_message: '## 历史结论\n\n缓存配置错误',
    }])

    const wrapper = await mountedInbox()

    expect(listBugInvestigationRuns).not.toHaveBeenCalled()
    expect(wrapper.find('.legacy-history').exists()).toBe(false)
    expect(wrapper.find('.generated-context-panel').exists()).toBe(false)
    expect(wrapper.text()).not.toContain('历史运行记录（只读）')
    expect(wrapper.text()).not.toContain('旧机器人上下文')
  })
})
