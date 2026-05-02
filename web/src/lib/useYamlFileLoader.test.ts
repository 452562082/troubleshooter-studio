import { describe, it, expect, vi, beforeEach } from 'vitest'
import { useYamlFileLoader } from './useYamlFileLoader'

vi.mock('./bridge', () => ({
  isDesktop: vi.fn(),
  openYAML: vi.fn(),
}))

import { isDesktop, openYAML } from './bridge'

describe('useYamlFileLoader', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  describe('loadFileNative', () => {
    it('calls onLoaded with content when openYAML resolves with path', async () => {
      vi.mocked(isDesktop).mockReturnValue(true)
      vi.mocked(openYAML).mockResolvedValue({ path: '/tmp/x.yaml', content: 'system:\n  id: x\n' })
      const onLoaded = vi.fn()
      const onError = vi.fn()
      const { loadFileNative } = useYamlFileLoader({ onLoaded, onError })
      await loadFileNative()
      expect(onLoaded).toHaveBeenCalledWith('system:\n  id: x\n')
      expect(onError).not.toHaveBeenCalled()
    })

    it('skips silently when isDesktop=false', async () => {
      vi.mocked(isDesktop).mockReturnValue(false)
      const onLoaded = vi.fn()
      await useYamlFileLoader({ onLoaded }).loadFileNative()
      expect(onLoaded).not.toHaveBeenCalled()
      expect(openYAML).not.toHaveBeenCalled()
    })

    it('returns silently when user cancels (path undefined)', async () => {
      vi.mocked(isDesktop).mockReturnValue(true)
      vi.mocked(openYAML).mockResolvedValue({ path: '', content: '' } as any)
      const onLoaded = vi.fn()
      await useYamlFileLoader({ onLoaded }).loadFileNative()
      expect(onLoaded).not.toHaveBeenCalled()
    })

    it('reports error via onError when openYAML throws', async () => {
      vi.mocked(isDesktop).mockReturnValue(true)
      vi.mocked(openYAML).mockRejectedValue(new Error('boom'))
      const onLoaded = vi.fn()
      const onError = vi.fn()
      await useYamlFileLoader({ onLoaded, onError }).loadFileNative()
      expect(onLoaded).not.toHaveBeenCalled()
      expect(onError).toHaveBeenCalledWith('加载文件失败: boom')
    })

    it('treats empty content as empty string', async () => {
      vi.mocked(isDesktop).mockReturnValue(true)
      vi.mocked(openYAML).mockResolvedValue({ path: '/tmp/x.yaml', content: '' })
      const onLoaded = vi.fn()
      await useYamlFileLoader({ onLoaded }).loadFileNative()
      expect(onLoaded).toHaveBeenCalledWith('')
    })
  })

  describe('loadFileBrowser', () => {
    it('reads file content via FileReader and calls onLoaded', async () => {
      const onLoaded = vi.fn()
      const { loadFileBrowser } = useYamlFileLoader({ onLoaded })
      const file = new File(['system:\n  id: y\n'], 'x.yaml', { type: 'application/yaml' })
      const input = document.createElement('input')
      input.type = 'file'
      // 真 FileReader 走 readAsText;happy-dom 支持
      Object.defineProperty(input, 'files', { value: [file], writable: false })
      const ev = { target: input } as unknown as Event
      loadFileBrowser(ev)
      // 异步 reader,等一下
      await new Promise(r => setTimeout(r, 10))
      expect(onLoaded).toHaveBeenCalledWith('system:\n  id: y\n')
    })

    it('does nothing when no file selected', () => {
      const onLoaded = vi.fn()
      const input = document.createElement('input')
      Object.defineProperty(input, 'files', { value: null, writable: false })
      useYamlFileLoader({ onLoaded }).loadFileBrowser({ target: input } as unknown as Event)
      expect(onLoaded).not.toHaveBeenCalled()
    })
  })
})
