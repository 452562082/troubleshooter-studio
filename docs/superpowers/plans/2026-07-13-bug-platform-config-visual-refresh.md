# Bug Platform Config Visual Refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reorganize the Bug inbox platform configuration into three compact, accessible sections with readable disclosure states and clear primary, secondary, icon, and destructive action hierarchy.

**Architecture:** Keep all existing state, API calls, handlers, and confirmation flows in `BugInboxPage.vue`; only restructure its template and scoped styles. Specify the new semantic structure and action behavior in `BugInboxPage.test.ts` before implementation so the visual refresh cannot silently remove existing interactions.

**Tech Stack:** Vue 3 Single File Components, TypeScript, scoped CSS, Vitest, Vue Test Utils

## Global Constraints

- Do not change platform save, login, clear-login, sync, manual fetch, delete, bot mapping, or Hook URL APIs and data flow.
- Do not introduce a new business component or change the global button design system.
- Use visible labels for platform fields and retain native checkbox, `disabled`, `aria-pressed`, and confirmation semantics.
- Use inline SVG icons with a 24×24 viewBox instead of text `+` and `×` controls.
- Desktop controls use a 36–38px visual height; controls return to at least 44px below 640px.
- Preserve the existing 375, 768, 1024, and 1440px responsive contract without horizontal overflow.
- Preserve reduced-motion support and visible keyboard focus.

---

### Task 1: Refresh the Bug platform configuration panel

**Files:**
- Modify: `web/src/pages/BugInboxPage.test.ts`
- Modify: `web/src/pages/BugInboxPage.vue`
- Reference: `docs/superpowers/specs/2026-07-13-bug-platform-config-visual-refresh-design.md`

**Interfaces:**
- Consumes: existing refs `configOpen`, `platforms`, `selectedPlatformID`, `platformDraft`, `botPickerOpen`, `configuredPlatformBots`, `addableBotRefs`, `manualBugID`, and `hookURL`.
- Consumes: existing handlers `newPlatform`, `savePlatform`, `deleteSelectedPlatform`, `loginSelectedPlatform`, `clearSelectedPlatformLogin`, `addPlatformBot`, `removePlatformBot`, `syncSelectedPlatform`, `fetchManualBug`, and `copyHookURL`.
- Produces: `#bug-platform-config`, `.platform-details-section`, `.bot-mapping-section`, `.sync-access-section`, `.config-footer`, and compact SVG action controls while preserving every existing `data-action` selector.

- [ ] **Step 1: Add failing semantic, icon, and interaction tests**

Add these cases to `web/src/pages/BugInboxPage.test.ts` after the existing platform configuration save test:

```ts
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

    const removeBot = wrapper.get('button.icon-button[aria-label="移除机器人"]')
    expect(removeBot.find('svg[aria-hidden="true"]').exists()).toBe(true)
    expect(removeBot.text()).toBe('')
    await removeBot.trigger('click')
    expect(wrapper.find('.bot-config-row').exists()).toBe(false)

    const footer = wrapper.get('.config-footer')
    expect(footer.findAll('button')[0].attributes('data-action')).toBe('delete-platform')
    expect(footer.findAll('button').at(-1)?.attributes('data-action')).toBe('save-platform')
    expect(footer.get('[data-action="delete-platform"]').classes()).toContain('danger-link')
    expect(footer.get('[data-action="save-platform"]').classes()).toContain('primary-button')
  })
```

In `it('preserves the platform config structure while ticket loading is pending', ...)`, replace the old text-character assertion:

```ts
    expect(wrapper.get('button.add-platform[aria-label="新增平台"]').text()).toBe('+')
```

with:

```ts
    const newPlatform = wrapper.get('[data-action="new-platform"]')
    expect(newPlatform.text()).toContain('新建平台')
    expect(newPlatform.find('svg[aria-hidden="true"]').exists()).toBe(true)
```

In the same test, replace the removed `.ops-row` assertion with the new section and footer contracts:

```ts
    expect(wrapper.find('.sync-access-section').exists()).toBe(true)
    expect(wrapper.find('.config-footer').exists()).toBe(true)
```

In `it('keeps platform configuration collapsed and saves mapped bots with their environment', ...)`, update the two placeholder-based selectors to the new labelled field placeholders:

```ts
    await wrapper.get('input[placeholder="如：测试环境"]').setValue('禅道')
    await wrapper.get('input[placeholder="https://bug-platform.example.com"]').setValue('https://zentao.example.com')
```

In `it('clears a saved platform login session', ...)`, replace the saved-session display assertion with the final status copy:

```ts
    expect(wrapper.text()).toContain('已登录')
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
npm --prefix web test -- --run src/pages/BugInboxPage.test.ts
```

Expected: FAIL because the current disclosure has no ARIA state or SVG icons, the three named sections and visible labels do not exist, and add/remove actions still use text characters.

- [ ] **Step 3: Implement the accessible disclosure control**

Replace the existing `.bug-header` configuration button in `web/src/pages/BugInboxPage.vue` with:

```vue
      <button
        class="config-disclosure"
        :class="{ expanded: configOpen }"
        type="button"
        data-action="toggle-platform-config"
        :aria-expanded="configOpen"
        aria-controls="bug-platform-config"
        @click="configOpen = !configOpen"
      >
        <svg aria-hidden="true" viewBox="0 0 24 24" fill="none">
          <path d="M4 7h10M18 7h2M4 17h2M10 17h10M14 4v6M6 14v6" stroke="currentColor" stroke-width="2" stroke-linecap="round" />
        </svg>
        <span>{{ configOpen ? '收起配置' : '平台配置' }}</span>
        <svg aria-hidden="true" viewBox="0 0 24 24" fill="none">
          <path :d="configOpen ? 'm6 15 6-6 6 6' : 'm6 9 6 6 6-6'" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      </button>
```

- [ ] **Step 4: Reorganize the existing controls into three sections and one action footer**

Replace the current platform configuration `<section>` with the following structure. Keep the existing expressions and handlers exactly as shown:

```vue
    <section
      id="bug-platform-config"
      class="platform-config"
      :class="{ open: configOpen }"
      aria-label="Bug 平台配置"
      data-density="compact"
      data-responsive-viewports="375,768,1024,1440"
      data-overflow-safe="true"
    >
      <section class="platform-config-section platform-details-section" aria-labelledby="platform-details-title">
        <header class="section-heading">
          <div><h2 id="platform-details-title">平台信息</h2><p>管理平台连接、授权方式和启用状态。</p></div>
        </header>
        <div class="platform-list">
          <div class="platform-tabs">
            <button
              v-for="platform in platforms"
              :key="platform.id"
              type="button"
              class="platform-chip"
              :class="{ active: selectedPlatformID === platform.id }"
              :aria-pressed="selectedPlatformID === platform.id"
              @click="selectedPlatformID = platform.id"
            >
              {{ platform.name }}<span>{{ platform.enabled ? '启用' : '停用' }}</span>
            </button>
          </div>
          <button class="compact-button secondary-button add-platform" type="button" data-action="new-platform" @click="newPlatform">
            <svg aria-hidden="true" viewBox="0 0 24 24" fill="none"><path d="M12 5v14M5 12h14" stroke="currentColor" stroke-width="2" stroke-linecap="round" /></svg>
            新建平台
          </button>
        </div>
        <div class="config-grid">
          <div class="config-row basic-row">
            <label class="field-label"><span>平台名称</span><input v-model="platformDraft.name" class="form-control" placeholder="如：测试环境"></label>
            <label class="field-label"><span>平台类型</span><select v-model="platformDraft.type" class="form-control"><option value="zentao">禅道</option><option value="generic">通用 Webhook</option></select></label>
            <label class="field-label"><span>平台地址</span><input v-model="platformDraft.base_url" class="form-control" placeholder="https://bug-platform.example.com"></label>
          </div>
          <div class="config-row auth-row">
            <label class="field-label"><span>登录方式</span><select v-model="platformDraft.auth_mode" class="form-control"><option value="feishu_sso">飞书授权登录</option><option value="api_token">API Token</option><option value="password">账号密码</option></select></label>
            <label v-if="platformDraft.auth_mode === 'password'" class="field-label"><span>密码</span><input v-model="platformDraft.password" class="form-control" type="password" placeholder="留空沿用已保存值"></label>
            <label v-if="platformDraft.auth_mode === 'api_token'" class="field-label"><span>API Token</span><input v-model="platformDraft.token" class="form-control" type="password" placeholder="留空沿用已保存值"></label>
            <div v-if="platformDraft.auth_mode === 'feishu_sso'" class="login-field">
              <span class="login-status-badge" :class="{ ok: selectedPlatformHasSession }">{{ selectedPlatformHasSession ? '已登录' : '未登录' }}</span>
              <button class="compact-button secondary-button" type="button" data-action="login-platform" :disabled="platformSaving || platformLoggingIn" @click="loginSelectedPlatform">{{ platformLoggingIn ? '等待授权' : '登录平台' }}</button>
              <button class="compact-button ghost-button" type="button" data-action="clear-platform-login" :disabled="loginClearing || platformLoggingIn || !selectedPlatformHasSession" @click="clearSelectedPlatformLogin">清除登录态</button>
            </div>
            <label class="toggle-control"><input v-model="platformDraft.enabled" type="checkbox"><span class="toggle-track" aria-hidden="true"><span></span></span><span>启用平台</span></label>
          </div>
        </div>
      </section>

      <section class="platform-config-section bot-mapping-section" aria-labelledby="bot-mapping-title">
        <header class="section-heading bot-config-title">
          <div><h2 id="bot-mapping-title">排障机器人</h2><p>平台映射只用于后续故障闭环选人。</p></div>
          <button class="compact-button secondary-button" type="button" data-action="toggle-bot-picker" :disabled="allBotRefs.length === 0" @click="botPickerOpen = !botPickerOpen">
            <svg aria-hidden="true" viewBox="0 0 24 24" fill="none"><path d="M12 5v14M5 12h14" stroke="currentColor" stroke-width="2" stroke-linecap="round" /></svg>
            {{ botPickerOpen ? '收起' : '添加机器人' }}
          </button>
        </header>
        <p v-if="configuredPlatformBots.length === 0" class="empty compact">{{ allBotRefs.length ? '还未添加排障机器人' : '暂无已安装机器人' }}</p>
        <div v-else class="bot-config-list">
          <div v-for="item in configuredPlatformBots" :key="item.mapping.bot_key" class="bot-config-row">
            <span class="bot-config-main"><strong>{{ botDisplayName(item.bot) }}</strong><small>{{ item.bot.target || '未知类型' }} · {{ item.bot.path }}</small></span>
            <select v-if="item.bot.envs?.length" class="form-control" :value="item.mapping.env" @change="setPlatformBotEnv(item.mapping.bot_key, eventValue($event))"><option v-for="env in item.bot.envs" :key="env" :value="env">{{ env }}</option></select>
            <input v-else class="form-control" :value="item.mapping.env" placeholder="机器人环境" @input="setPlatformBotEnv(item.mapping.bot_key, eventValue($event))">
            <button class="icon-button danger-icon-button" type="button" aria-label="移除机器人" @click="removePlatformBot(item.mapping.bot_key)">
              <svg aria-hidden="true" viewBox="0 0 24 24" fill="none"><path d="M4 7h16M9 7V4h6v3m-8 0 1 13h8l1-13M10 11v5m4-5v5" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
            </button>
          </div>
        </div>
        <div v-if="botPickerOpen" class="bot-picker">
          <input v-model="botPickerQuery" class="form-control" placeholder="搜索机器人名称、类型、路径">
          <p v-if="addableBotRefs.length === 0" class="empty compact">没有可添加的机器人</p>
          <button v-for="bot in addableBotRefs" :key="bot.key" type="button" class="bot-picker-row" :data-bot-key="bot.key" @click="addPlatformBot(bot)"><span class="bot-config-main"><strong>{{ botDisplayName(bot) }}</strong><small>{{ bot.target }} · {{ bot.path }}</small></span><span>添加</span></button>
        </div>
      </section>

      <section class="platform-config-section sync-access-section" aria-labelledby="sync-access-title">
        <header class="section-heading"><div><h2 id="sync-access-title">同步与接入</h2><p>同步指派给我的 Bug，或按 ID 主动拉取。</p></div></header>
        <div class="sync-settings">
          <label class="toggle-control"><input v-model="platformDraft.poll_enabled" type="checkbox"><span class="toggle-track" aria-hidden="true"><span></span></span><span>后台定时同步</span></label>
          <label class="interval-control">每 <input v-model.number="platformDraft.poll_interval_minutes" aria-label="后台同步间隔分钟" type="number" min="1" :disabled="!platformDraft.poll_enabled"> 分钟</label>
        </div>
        <div class="trigger-row">
          <button class="compact-button accent-button" type="button" data-action="sync-platform" :disabled="!selectedPlatform || syncingBugs" @click="syncSelectedPlatform">同步我的 Bug</button>
          <input v-model="manualBugID" class="form-control" placeholder="Bug ID 或飞书消息" @keyup.enter="fetchManualBug">
          <button class="compact-button secondary-button" type="button" data-action="fetch-bug" :disabled="!selectedPlatform || !manualBugID.trim() || fetchingBug" @click="fetchManualBug">拉取指定 Bug</button>
        </div>
        <div class="hook-row"><strong>Hook URL</strong><code>{{ hookURL || '保存平台后生成' }}</code><button class="compact-button secondary-button" type="button" data-action="copy-hook-url" :disabled="!hookURL" @click="copyHookURL">复制</button></div>
      </section>

      <footer class="config-footer">
        <button class="danger-link" type="button" data-action="delete-platform" :disabled="platformDeleting || !platformDraft.id" @click="deleteSelectedPlatform">
          <svg aria-hidden="true" viewBox="0 0 24 24" fill="none"><path d="M4 7h16M9 7V4h6v3m-8 0 1 13h8l1-13" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" /></svg>
          删除平台
        </button>
        <button class="compact-button primary-button" type="button" data-action="save-platform" :disabled="platformSaving || platformLoggingIn" @click="savePlatform">保存配置</button>
      </footer>
    </section>
```

- [ ] **Step 5: Replace the platform-panel styles with compact, state-specific styles**

Keep the generic `.btn` styles used outside the panel. Replace the existing platform configuration styles from `.platform-config` through `.hook-row code` with:

```css
.config-disclosure { min-height: 38px; padding: 0 12px; display: inline-flex; align-items: center; gap: 7px; border: 1px solid var(--c-accent-hover); border-radius: var(--r-md); background: var(--c-accent-hover); color: #fff; font: inherit; font-weight: 600; cursor: pointer; transition: background-color 180ms ease, border-color 180ms ease, color 180ms ease; }
.config-disclosure svg { width: 17px; height: 17px; flex: 0 0 auto; }
.config-disclosure:hover { background: #1d4ed8; border-color: #1d4ed8; }
.config-disclosure.expanded { background: #eff6ff; border-color: #93c5fd; color: #1d4ed8; }
.config-disclosure.expanded:hover { background: #dbeafe; border-color: #60a5fa; color: #1e40af; }
.config-disclosure:focus-visible { outline: 2px solid var(--c-accent-hover); outline-offset: 2px; }
.platform-config { min-width: 0; display: none; gap: var(--sp-2); padding: var(--sp-2); border: 1px solid var(--c-line); border-radius: var(--r-lg); background: var(--c-surf-2); }
.platform-config.open { display: grid; }
.platform-config-section { min-width: 0; display: grid; gap: var(--sp-2); padding: var(--sp-3); border: 1px solid var(--c-line); border-radius: var(--r-lg); background: var(--c-surf); }
.section-heading, .platform-list, .platform-tabs, .config-footer, .hook-row { min-width: 0; display: flex; align-items: center; gap: var(--sp-2); }
.section-heading, .platform-list, .config-footer { justify-content: space-between; }
.section-heading h2 { margin: 0; color: var(--c-ink); font-size: var(--fs-md); }
.section-heading p { margin: 2px 0 0; color: var(--c-muted); font-size: var(--fs-sm); }
.platform-tabs { flex-wrap: wrap; }
.platform-chip { min-height: 36px; padding: 4px 10px; border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf); color: var(--c-text); cursor: pointer; }
.platform-chip.active { border-color: #93c5fd; background: #eff6ff; color: #1d4ed8; box-shadow: inset 0 0 0 1px #bfdbfe; }
.platform-chip span { display: block; color: var(--c-muted); font-size: var(--fs-xs); }
.config-grid { min-width: 0; display: grid; gap: var(--sp-2); }
.config-row { min-width: 0; display: grid; gap: var(--sp-2); }
.basic-row { grid-template-columns: minmax(160px, 1fr) 150px minmax(220px, 1.4fr); }
.auth-row { grid-template-columns: minmax(160px, .8fr) minmax(280px, 1.4fr) auto; align-items: end; }
.field-label { min-width: 0; display: grid; gap: 4px; color: var(--c-muted); font-size: var(--fs-sm); }
.platform-config .form-control { min-height: 36px; }
.login-field, .sync-settings { min-width: 0; display: flex; align-items: center; gap: var(--sp-2); flex-wrap: wrap; }
.login-status-badge { padding: 3px 8px; border: 1px solid #fed7aa; border-radius: 999px; background: #fff7ed; color: #9a3412; font-size: var(--fs-xs); font-weight: 600; }
.login-status-badge.ok { border-color: #bbf7d0; background: #f0fdf4; color: #166534; }
.compact-button { min-height: 36px; padding: 0 11px; display: inline-flex; align-items: center; justify-content: center; gap: 6px; border: 1px solid transparent; border-radius: var(--r-md); font: inherit; font-size: var(--fs-sm); font-weight: 600; cursor: pointer; transition: background-color 180ms ease, border-color 180ms ease, color 180ms ease; }
.compact-button svg, .danger-link svg { width: 16px; height: 16px; flex: 0 0 auto; }
.secondary-button { border-color: var(--c-line-2); background: var(--c-surf); color: var(--c-text); }
.secondary-button:hover:not(:disabled) { border-color: #93c5fd; background: #eff6ff; color: #1d4ed8; }
.ghost-button { background: transparent; color: var(--c-muted); }
.ghost-button:hover:not(:disabled) { background: var(--c-surf-3); color: var(--c-text); }
.primary-button { border-color: var(--c-accent-hover); background: var(--c-accent-hover); color: #fff; }
.primary-button:hover:not(:disabled) { border-color: #1d4ed8; background: #1d4ed8; }
.accent-button { border-color: var(--c-primary); background: var(--c-primary); color: #fff; }
.accent-button:hover:not(:disabled) { border-color: var(--c-primary-hover); background: var(--c-primary-hover); }
.compact-button:disabled, .danger-link:disabled { opacity: .5; cursor: not-allowed; }
.compact-button:focus-visible, .danger-link:focus-visible, .icon-button:focus-visible { outline: 2px solid var(--c-accent-hover); outline-offset: 2px; }
.toggle-control { min-height: 36px; display: inline-flex; align-items: center; gap: 7px; color: var(--c-text); white-space: nowrap; cursor: pointer; }
.toggle-control input { position: absolute; opacity: 0; pointer-events: none; }
.toggle-track { width: 34px; height: 20px; padding: 2px; display: inline-flex; align-items: center; border-radius: 999px; background: var(--c-line-2); transition: background-color 180ms ease; }
.toggle-track > span { width: 16px; height: 16px; border-radius: 50%; background: #fff; box-shadow: 0 1px 2px rgba(15, 23, 42, .2); transition: transform 180ms ease; }
.toggle-control input:checked + .toggle-track { background: var(--c-accent-hover); }
.toggle-control input:checked + .toggle-track > span { transform: translateX(14px); }
.toggle-control input:focus-visible + .toggle-track { outline: 2px solid var(--c-accent-hover); outline-offset: 2px; }
.bot-config-list, .bot-picker { min-width: 0; display: grid; gap: var(--sp-2); }
.bot-config-row { min-width: 0; display: grid; grid-template-columns: minmax(0, 1fr) minmax(120px, 180px) 40px; align-items: center; gap: var(--sp-2); padding: 7px 8px; border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf-2); }
.bot-config-main { min-width: 0; display: grid; gap: 2px; }
.bot-config-main small { color: var(--c-muted); font-size: var(--fs-xs); overflow-wrap: anywhere; }
.icon-button { width: 40px; height: 40px; padding: 0; display: inline-grid; place-items: center; border: 0; border-radius: 999px; background: transparent; color: var(--c-muted); cursor: pointer; transition: background-color 180ms ease, color 180ms ease; }
.icon-button svg { width: 18px; height: 18px; }
.danger-icon-button:hover { background: var(--c-danger-bg); color: var(--c-danger); }
.bot-picker-row { width: 100%; min-width: 0; min-height: 40px; padding: 8px 10px; display: flex; justify-content: space-between; gap: var(--sp-2); border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf); color: var(--c-text); text-align: left; cursor: pointer; }
.interval-control { min-height: 36px; display: inline-flex; align-items: center; gap: 6px; color: var(--c-muted); }
.interval-control input { width: 72px; min-height: 36px; padding: 0 8px; }
.trigger-row { min-width: 0; display: grid; grid-template-columns: auto minmax(180px, 1fr) auto; gap: var(--sp-2); }
.hook-row { flex-wrap: wrap; }
.hook-row code { min-width: 0; flex: 1; padding: 7px 9px; overflow-wrap: anywhere; border-radius: var(--r-sm); background: var(--c-surf-2); color: var(--c-muted); }
.danger-link { min-height: 36px; padding: 0 6px; display: inline-flex; align-items: center; gap: 6px; border: 0; background: transparent; color: var(--c-danger); font: inherit; font-size: var(--fs-sm); font-weight: 600; cursor: pointer; }
.danger-link:hover:not(:disabled) { color: #7f1d1d; text-decoration: underline; text-underline-offset: 3px; }
```

Replace the current 900px and 640px platform-panel responsive rules and extend reduced-motion handling with:

```css
@media (prefers-reduced-motion: reduce) {
  .config-disclosure, .compact-button, .toggle-track, .toggle-track > span, .icon-button, .attachment-preview-close { transition: none; }
}
@media (max-width: 900px) {
  .basic-row, .auth-row { grid-template-columns: minmax(0, 1fr); }
  .inbox-workspace { grid-template-columns: minmax(0, 1fr); }
  .ticket-list-panel { max-height: 360px; }
}
@media (max-width: 640px) {
  .bug-header, .section-heading, .platform-list, .hook-row { align-items: stretch; flex-direction: column; }
  .platform-config .form-control, .compact-button, .danger-link, .toggle-control { min-height: 44px; }
  .trigger-row, .bot-config-row { grid-template-columns: minmax(0, 1fr); }
  .bot-config-row .icon-button { justify-self: end; width: 44px; height: 44px; }
  .config-footer { align-items: stretch; }
  .config-footer .primary-button { min-width: 140px; }
}
```

- [ ] **Step 6: Run the focused test and verify GREEN**

Run:

```bash
npm --prefix web test -- --run src/pages/BugInboxPage.test.ts
```

Expected: all `BugInboxPage.test.ts` cases PASS, including the new section, disclosure, SVG, removal, footer hierarchy, and existing API behavior cases.

- [ ] **Step 7: Run complete verification**

Run:

```bash
npm --prefix web test -- --run
npm --prefix web run build
make lint
git diff --check
```

Expected: all Web tests pass; the production build and TypeScript checks exit 0; lint reports `go vet + gofmt clean`; `git diff --check` prints nothing. If the build removes `internal/webui/dist/.gitkeep`, restore its tracked content before staging.

- [ ] **Step 8: Review the resulting diff against the design checklist**

Run:

```bash
git diff -- web/src/pages/BugInboxPage.vue web/src/pages/BugInboxPage.test.ts
git status --short
```

Confirm the diff contains only the platform configuration template/style refresh and its tests; it must not modify Bug inbox data loading, API handlers, incident navigation, or backend files.

- [ ] **Step 9: Commit the tested implementation**

Run:

```bash
git add web/src/pages/BugInboxPage.vue web/src/pages/BugInboxPage.test.ts
git diff --cached --check
git commit -m "fix: refine Bug platform configuration layout"
```

Expected: one implementation commit containing only the Vue page and its test.
