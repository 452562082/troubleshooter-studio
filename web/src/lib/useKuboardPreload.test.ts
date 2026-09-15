import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useKuboardPreload, type UseKuboardPreloadDeps } from './useKuboardPreload'

const mocks = vi.hoisted(() => ({ list: vi.fn(), error: vi.fn() }))
vi.mock('./bridge', () => ({ kuboardListResources: mocks.list }))
vi.mock('./bridge/shared', () => ({ isDesktop: () => true }))
vi.mock('./logStore', () => ({ pushLog: vi.fn() }))
vi.mock('./toast', () => ({ toast: { error: mocks.error, info: vi.fn(), success: vi.fn() } }))

function fixture(): UseKuboardPreloadDeps {
  return {
    kuboardStateByEnv: {}, persistKuboardState: vi.fn(),
    sourceCreds: { ops: { creds: { dev: { url: 'https://kb.example', access_key: 'id.secret' } } } },
    kuboardSvcMap: {}, allServiceNames: { value: [] }, getServiceSource: () => 'ops',
  }
}

describe('Kuboard MCP discovery in the wizard', () => {
  beforeEach(() => vi.clearAllMocks())
  it('allows v4 API keys without a v3 username and saves discovered MCP URL', async () => {
    const deps = fixture()
    mocks.list.mockResolvedValue({ clusters: [], mcp_url: 'https://kb.example/mcp' })
    await useKuboardPreload(deps).runKuboardPreloadFromSource('ops', 'dev')
    expect(mocks.list).toHaveBeenCalledWith('https://kb.example', '', '', 'id.secret', '')
    expect(deps.sourceCreds.ops.creds.dev.mcp_url).toBe('https://kb.example/mcp')
    expect(deps.kuboardStateByEnv.dev.status).toBe('ok')
    expect(mocks.error).not.toHaveBeenCalled()
  })
  it('keeps legacy HTTP working and clears a previously discovered MCP URL', async () => {
    const deps = fixture()
    deps.sourceCreds.ops.creds.dev.mcp_url = 'https://kb.example/mcp'
    mocks.list.mockResolvedValue({ clusters: [] })
    await useKuboardPreload(deps).runKuboardPreloadFromSource('ops', 'dev')
    expect(deps.sourceCreds.ops.creds.dev.mcp_url).toBe('')
    expect(deps.kuboardStateByEnv.dev.status).toBe('ok')
  })
  it('does not report MCP support when resource authentication fails', async () => {
    const deps = fixture()
    mocks.list.mockRejectedValue(new Error('HTTP 401'))
    await useKuboardPreload(deps).runKuboardPreloadFromSource('ops', 'dev')
    expect(deps.sourceCreds.ops.creds.dev.mcp_url).toBeUndefined()
    expect(deps.kuboardStateByEnv.dev.status).toBe('error')
  })
})
