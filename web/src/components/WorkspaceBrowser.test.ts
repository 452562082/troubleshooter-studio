import { flushPromises, mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import WorkspaceBrowser from './WorkspaceBrowser.vue'

const bridgeMocks = vi.hoisted(() => ({
  listBotWorkspaceFiles: vi.fn(),
  readBotWorkspaceFile: vi.fn(),
}))

vi.mock('../lib/bridge', () => ({
  listBotWorkspaceFiles: bridgeMocks.listBotWorkspaceFiles,
  readBotWorkspaceFile: bridgeMocks.readBotWorkspaceFile,
  writeBotWorkspaceFile: vi.fn(),
  revealInFinder: vi.fn(),
  isDesktop: () => true,
}))

vi.mock('../lib/toast', () => ({
  toast: {
    error: vi.fn(),
    success: vi.fn(),
  },
}))

describe('WorkspaceBrowser', () => {
  it('scopes the file tree to the selected internal agent', async () => {
    bridgeMocks.listBotWorkspaceFiles.mockResolvedValue({
      name: 'base',
      path: '',
      is_dir: true,
      children: [
        {
          name: 'agents',
          path: 'agents',
          is_dir: true,
          children: [
            { name: 'base-troubleshooter.md', path: 'agents/base-troubleshooter.md', is_dir: false, size: 10 },
            { name: 'base-validator.md', path: 'agents/base-validator.md', is_dir: false, size: 10 },
          ],
        },
        {
          name: 'skills',
          path: 'skills',
          is_dir: true,
          children: [
            {
              name: 'base-troubleshooter',
              path: 'skills/base-troubleshooter',
              is_dir: true,
              children: [
                { name: 'incident-investigator', path: 'skills/base-troubleshooter/incident-investigator', is_dir: true, children: [] },
              ],
            },
            {
              name: 'base-validator',
              path: 'skills/base-validator',
              is_dir: true,
              children: [
                {
                  name: 'bug-verifier',
                  path: 'skills/base-validator/bug-verifier',
                  is_dir: true,
                  children: [
                    { name: 'SKILL.md', path: 'skills/base-validator/bug-verifier/SKILL.md', is_dir: false, size: 10 },
                  ],
                },
                {
                  name: 'empty-skill',
                  path: 'skills/base-validator/empty-skill',
                  is_dir: true,
                  children: [],
                },
                {
                  name: 'config-executor',
                  path: 'skills/base-validator/config-executor',
                  is_dir: true,
                  children: [
                    { name: 'references', path: 'skills/base-validator/config-executor/references', is_dir: true, children: [] },
                    { name: 'scripts', path: 'skills/base-validator/config-executor/scripts', is_dir: true, children: [] },
                    { name: 'SKILL.md', path: 'skills/base-validator/config-executor/SKILL.md', is_dir: false, size: 10 },
                  ],
                },
              ],
            },
          ],
        },
      ],
    })
    bridgeMocks.readBotWorkspaceFile.mockResolvedValue({
      content: '# validator skill',
      is_binary: false,
      truncated: false,
      size: 17,
    })

    const wrapper = mount(WorkspaceBrowser, {
      props: {
        rootPath: '/Users/xiaolong/.claude/skills/base-troubleshooter',
        bot: { meta: { target: 'claude-code', system_id: 'base' }, path: '/Users/xiaolong/.claude/skills/base-troubleshooter' },
        initialPath: 'skills/base-validator/bug-verifier/SKILL.md',
        agentScope: 'base-validator',
      },
    })
    await flushPromises()

    expect(wrapper.text()).toContain('base-validator')
    expect(wrapper.text()).toContain('bug-verifier')
    expect(wrapper.text()).toContain('SKILL.md')
    expect(wrapper.text()).toContain('config-executor')
    expect(wrapper.text()).not.toContain('empty-skill')
    expect(wrapper.text()).not.toContain('references')
    expect(wrapper.text()).not.toContain('scripts')
    expect(wrapper.text()).not.toContain('base-troubleshooter.md')
    expect(wrapper.text()).not.toContain('incident-investigator')
  })
})
