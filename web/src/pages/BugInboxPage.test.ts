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
import { toast } from '../lib/toast'
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

  it('keeps the sync action in the Bug list heading without absolute overlap', async () => {
    vi.mocked(listBugs).mockResolvedValue([bug])
    const wrapper = await mountedInbox()
    const panel = wrapper.get('.ticket-list-panel')
    const action = panel.get('.list-heading .list-actions [data-action="sync-enabled-platforms"]')

    expect(action.text()).toContain('同步我的 Bug')
    expect(panel.find(':scope > [data-action="sync-enabled-platforms"]').exists()).toBe(false)

    const source = readFileSync('src/pages/BugInboxPage.vue', 'utf8')
    expect(source).not.toMatch(/\.refresh-button\s*\{[^}]*position:\s*absolute/)
    expect(source).not.toContain('.ticket-list-panel :deep(.list-heading) { padding-right: 112px; }')
  })

  it('declares concrete mobile touch-target and full-width save contracts', () => {
    const source = readFileSync('src/pages/BugInboxPage.vue', 'utf8')
    const mobileCSS = source.split('@media (max-width: 640px) {')[1]?.split('\n}')[0] || ''

    expect(mobileCSS).toMatch(/\.config-disclosure,[^{]*\.platform-chip,[^{]*\.bot-picker-row,[^{]*\.platform-config \.interval-control input \{ min-height: 44px; \}/)
    expect(mobileCSS).toContain('.compact-button, .danger-link, .toggle-control { min-height: 44px; }')
    expect(mobileCSS).toContain('.platform-config .form-control, .platform-config .compact-button, .platform-config .toggle-control { min-height: 44px; }')
    expect(mobileCSS).toContain('.bot-config-row .icon-button { justify-self: end; width: 44px; height: 44px; }')
    expect(mobileCSS).toContain('.config-footer { align-items: stretch; flex-direction: column; }')
    expect(mobileCSS).toContain('.config-footer .danger-link { align-self: flex-start; }')
    expect(mobileCSS).toContain('.config-footer .primary-button { width: 100%; min-width: 0; }')
  })

  it('declares consistent desktop control sizing and responsive select widths', () => {
    const source = readFileSync('src/pages/BugInboxPage.vue', 'utf8')
    const selectRule = source.match(/\.platform-config select\.form-control \{([^}]*)\}/)?.[1] || ''
    const reducedMotionCSS = source.split('@media (prefers-reduced-motion: reduce) {')[1]?.split('\n}')[0] || ''
    const reducedMotionSelectRule = reducedMotionCSS.match(/[^{}]*\.platform-config select\.form-control[^{}]*\{([^}]*)\}/)?.[1] || ''
    const mediumCSS = source.split('@media (max-width: 1200px) {')[1]?.split('\n}')[0] || ''
    const narrowCSS = source.split('@media (max-width: 900px) {')[1]?.split('\n}')[0] || ''
    const mediumBreakpointIndex = source.indexOf('@media (max-width: 1200px) {')
    const narrowBreakpointIndex = source.indexOf('@media (max-width: 900px) {')
    const mobileBreakpointIndex = source.indexOf('@media (max-width: 640px) {')

    expect(source).toContain('.platform-config { --config-control-height: 40px;')
    expect(source).toContain('.platform-config .form-control { min-height: var(--config-control-height); }')
    expect(source).toContain('.compact-button { min-height: 36px;')
    expect(source).toContain('.platform-config .compact-button { min-height: var(--config-control-height); }')
    expect(source).toContain('.platform-config .toggle-control { min-height: var(--config-control-height); }')
    expect(source).toContain('.platform-config .interval-control { min-height: var(--config-control-height); }')
    expect(source).toContain('.platform-config .interval-control input { min-height: var(--config-control-height); }')
    expect(selectRule).toContain('appearance: none;')
    expect(selectRule).toContain('-webkit-appearance: none;')
    expect(selectRule).toContain('padding: 0 40px 0 12px;')
    expect(selectRule).toContain('background-image: url("data:image/svg+xml,')
    expect(selectRule).toContain('background-repeat: no-repeat;')
    expect(selectRule).toContain('background-position: right 12px center;')
    expect(selectRule).toContain('background-size: 16px 16px;')
    expect(selectRule).toContain('cursor: pointer;')
    expect(selectRule).toContain('transition: border-color 180ms ease, box-shadow 180ms ease, background-color 180ms ease;')
    expect(source).toContain('.platform-config select.form-control:hover:not(:disabled) { border-color: #93c5fd; }')
    expect(source).toContain('.platform-config select.form-control:focus-visible { border-color: var(--c-accent-hover); }')
    expect(source).toContain('.platform-config input:disabled, .platform-config select:disabled {')
    expect(reducedMotionSelectRule).toContain('transition: none;')
    expect(source).toContain('.basic-row { grid-template-columns: minmax(220px, 1fr) minmax(200px, .6fr) minmax(320px, 1.35fr); }')
    expect(source).toContain('.bot-config-row { min-width: 0; display: grid; grid-template-columns: minmax(0, 1fr) minmax(240px, 280px) 40px;')
    expect(source).toContain('.icon-button { width: 40px; height: 40px;')
    expect(mediumCSS).toContain('.basic-row { grid-template-columns: minmax(0, 1fr) minmax(200px, .65fr); }')
    expect(mediumCSS).toContain('.basic-row .field-label:last-child { grid-column: 1 / -1; }')
    expect(narrowCSS).toContain('.basic-row, .auth-row { grid-template-columns: minmax(0, 1fr); }')
    expect(mediumBreakpointIndex).toBeGreaterThan(-1)
    expect(narrowBreakpointIndex).toBeGreaterThan(mediumBreakpointIndex)
    expect(mobileBreakpointIndex).toBeGreaterThan(narrowBreakpointIndex)
  })

  it('declares readable panel disabled colors and red danger-icon focus treatment', () => {
    const source = readFileSync('src/pages/BugInboxPage.vue', 'utf8')
    const selectRuleIndex = source.indexOf('.platform-config select.form-control {')
    const disabledRuleMatch = source.match(/\.platform-config input:disabled, \.platform-config select:disabled \{([^}]*)\}/)
    const disabledRule = disabledRuleMatch?.[1] || ''
    const disabledRuleIndex = disabledRuleMatch ? source.indexOf(disabledRuleMatch[0]) : -1

    expect(source).not.toMatch(/\.compact-button:disabled[^}]*opacity/)
    expect(source).toMatch(/\.compact-button:disabled,[\s\S]*?\.icon-button:disabled \{[^}]*border-color:[^;]+;[^}]*background:[^;]+;[^}]*color:[^;]+;[^}]*cursor: not-allowed;/)
    expect(source).toMatch(/\.compact-button:disabled svg,[\s\S]*?\.icon-button:disabled svg \{ color: [^;]+; \}/)
    expect(selectRuleIndex).toBeGreaterThan(-1)
    expect(disabledRuleIndex).toBeGreaterThan(selectRuleIndex)
    expect(disabledRule).toContain('border-color: var(--c-line);')
    expect(disabledRule).toContain('background-color: var(--c-surf-3);')
    expect(disabledRule).not.toMatch(/(?:^|;)\s*background\s*:/)
    expect(disabledRule).toContain('color: #64748b;')
    expect(disabledRule).toContain('cursor: not-allowed;')
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

  it('does not expose Cursor as a configurable incident workflow bot', async () => {
    vi.mocked(discoverBots).mockResolvedValue([
      {
        path: '/repo/base-codex', ghost: false,
        meta: { system_id: 'base', system_name: 'Base Codex', target: 'codex', agent_id: 'base-troubleshooter' },
        environments: ['test'],
      } as any,
      {
        path: '/repo/base-cursor', ghost: false,
        meta: { system_id: 'base', system_name: 'Base Cursor', target: 'cursor', agent_id: 'base-troubleshooter' },
        environments: ['test'],
      } as any,
    ])
    vi.mocked(listBugPlatforms).mockResolvedValue([{
      id: 'zentao-main', name: '测试环境', type: 'zentao', auth_mode: 'feishu_sso',
      bot_mappings: [{ bot_key: '/repo/base-cursor|cursor', env: 'test' }], enabled: true,
    }])

    const wrapper = await mountedInbox()
    await wrapper.get('[data-action="toggle-platform-config"]').trigger('click')

    expect(wrapper.findAll('.bot-config-row')).toHaveLength(0)
    await wrapper.get('[data-action="toggle-bot-picker"]').trigger('click')
    expect(wrapper.find('[data-bot-key="/repo/base-cursor|cursor"]').exists()).toBe(false)
    expect(wrapper.find('[data-bot-key="/repo/base-codex|codex"]').exists()).toBe(true)
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

  it('keeps the sync/access layout ordered and responsive', async () => {
    const wrapper = await mountedInbox()
    await wrapper.get('[data-action="toggle-platform-config"]').trigger('click')

    const syncAccess = wrapper.get('.sync-access-section')
    expect(syncAccess.find('[data-action="sync-platform"]').exists()).toBe(false)
    const controlRow = syncAccess.get(':scope > .sync-control-row')
    expect(Array.from(controlRow.element.children).map(element => element.classList[0])).toEqual([
      'sync-settings',
      'sync-control-divider',
      'manual-bug-row',
    ])
    expect(controlRow.get('.sync-control-divider').attributes('aria-hidden')).toBe('true')
    expect(controlRow.find('.manual-bug-row .manual-bug-field').exists()).toBe(true)
    expect(controlRow.find('.manual-bug-row [data-action="fetch-bug"]').exists()).toBe(true)
    expect(controlRow.element.nextElementSibling).toBe(syncAccess.get(':scope > .hook-row').element)

    const source = readFileSync('src/pages/BugInboxPage.vue', 'utf8')
    const tabletCSS = source.split('@media (max-width: 900px) {')[1]?.split('\n}')[0] || ''
    const mobileCSS = source.split('@media (max-width: 640px) {')[1]?.split('\n}')[0] || ''
    expect(source).toContain('.sync-control-row { min-width: 0; display: grid; grid-template-columns: auto 1px minmax(0, 1fr);')
    expect(source).toContain('.sync-control-divider { align-self: stretch; width: 1px; background: var(--c-line); }')
    expect(tabletCSS).toContain('.sync-control-row { grid-template-columns: minmax(0, 1fr); }')
    expect(tabletCSS).toContain('.sync-control-divider { width: 100%; height: 1px; }')
    expect(mobileCSS).toContain('.manual-bug-row .compact-button { width: 100%; }')
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

    const sync = wrapper.get('[data-action="sync-enabled-platforms"]')
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

  it('synchronizes every enabled platform and refreshes the local list once', async () => {
    vi.mocked(listBugPlatforms).mockResolvedValue([
      { id: 'zentao-a', name: '禅道 A', type: 'zentao', enabled: true },
      { id: 'zentao-off', name: '停用平台', type: 'zentao', enabled: false },
      { id: 'zentao-b', name: '禅道 B', type: 'zentao', enabled: true },
    ])
    let resolveFirst!: (result: { platform_id: string; fetched: number; stored: number }) => void
    vi.mocked(syncBugPlatform)
      .mockImplementationOnce(() => new Promise(resolve => {
        resolveFirst = resolve
      }))
      .mockResolvedValueOnce({ platform_id: 'zentao-b', fetched: 1, stored: 1 })
    const wrapper = await mountedInbox()
    vi.mocked(listBugs).mockClear()

    await wrapper.get('[data-action="sync-enabled-platforms"]').trigger('click')
    await wrapper.vm.$nextTick()

    expect(vi.mocked(syncBugPlatform).mock.calls).toEqual([['zentao-a']])
    resolveFirst({ platform_id: 'zentao-a', fetched: 2, stored: 2 })
    await flushPromises()

    expect(vi.mocked(syncBugPlatform).mock.calls).toEqual([['zentao-a'], ['zentao-b']])
    expect(listBugs).toHaveBeenCalledTimes(1)
    expect(wrapper.find('[data-action="sync-platform"]').exists()).toBe(false)
  })

  it('continues synchronizing after a failed platform and reports its name', async () => {
    vi.mocked(listBugPlatforms).mockResolvedValue([
      { id: 'zentao-a', name: '禅道 A', type: 'zentao', enabled: true },
      { id: 'zentao-b', name: '禅道 B', type: 'zentao', enabled: true },
    ])
    vi.mocked(syncBugPlatform)
      .mockRejectedValueOnce(new Error('登录过期'))
      .mockResolvedValueOnce({ platform_id: 'zentao-b', fetched: 1, stored: 1 })
    const wrapper = await mountedInbox()
    vi.mocked(listBugs).mockClear()

    await wrapper.get('[data-action="sync-enabled-platforms"]').trigger('click')
    await flushPromises()

    expect(vi.mocked(syncBugPlatform).mock.calls).toEqual([['zentao-a'], ['zentao-b']])
    expect(listBugs).toHaveBeenCalledTimes(1)
    expect(toast.error).toHaveBeenCalledWith(expect.stringContaining('禅道 A'))
  })

  it('shows 请先启用 Bug 平台 when no enabled platform exists', async () => {
    vi.mocked(listBugPlatforms).mockResolvedValue([
      { id: 'zentao-off', name: '停用平台', type: 'zentao', enabled: false },
    ])
    const wrapper = await mountedInbox()
    vi.mocked(listBugs).mockClear()

    await wrapper.get('[data-action="sync-enabled-platforms"]').trigger('click')
    await flushPromises()

    expect(syncBugPlatform).not.toHaveBeenCalled()
    expect(listBugs).not.toHaveBeenCalled()
    expect(toast.info).toHaveBeenCalledWith('请先启用 Bug 平台')
  })

  it('shows a disabled loading state while synchronizing enabled platforms', async () => {
    vi.mocked(listBugPlatforms).mockResolvedValue([
      { id: 'zentao-main', name: '测试环境', type: 'zentao', enabled: true },
    ])
    let resolveSync!: (result: { platform_id: string; fetched: number; stored: number }) => void
    vi.mocked(syncBugPlatform).mockImplementationOnce(() => new Promise(resolve => {
      resolveSync = resolve
    }))
    const wrapper = await mountedInbox()

    const sync = wrapper.get('[data-action="sync-enabled-platforms"]')
    await sync.trigger('click')
    await wrapper.vm.$nextTick()
    expect(sync.attributes('disabled')).toBeDefined()
    expect(sync.text()).toContain('同步中…')
    expect(sync.get('svg').classes()).toContain('spinning')

    resolveSync({ platform_id: 'zentao-main', fetched: 1, stored: 1 })
    await flushPromises()
    expect(sync.attributes('disabled')).toBeUndefined()
    expect(sync.text()).toContain('同步我的 Bug')
    expect(sync.get('svg').classes()).not.toContain('spinning')
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
    expect(wrapper.get('[data-action="sync-enabled-platforms"]').text()).toContain('同步我的 Bug')
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

    await wrapper.get('[data-action="sync-enabled-platforms"]').trigger('click')
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
