import { describe, it, expect, vi, beforeEach } from 'vitest'
import { copyToClipboard } from './clipboard'

const writeText = vi.fn()
const execCommand = vi.fn()

beforeEach(() => {
  writeText.mockReset()
  execCommand.mockReset()
  // happy-dom 没有 document.execCommand,直接装一个
  Object.defineProperty(document, 'execCommand', { value: execCommand, configurable: true, writable: true })
  Object.defineProperty(navigator, 'clipboard', {
    value: { writeText },
    configurable: true,
  })
})

describe('copyToClipboard', () => {
  it('returns true when navigator.clipboard.writeText succeeds', async () => {
    writeText.mockResolvedValue(undefined)
    expect(await copyToClipboard('hello')).toBe(true)
    expect(writeText).toHaveBeenCalledWith('hello')
    expect(execCommand).not.toHaveBeenCalled()
  })

  it('falls back to execCommand when navigator.clipboard rejects', async () => {
    writeText.mockRejectedValue(new Error('insecure'))
    execCommand.mockReturnValue(true)
    expect(await copyToClipboard('hello')).toBe(true)
    expect(execCommand).toHaveBeenCalledWith('copy')
  })

  it('returns false when execCommand also fails', async () => {
    writeText.mockRejectedValue(new Error('x'))
    execCommand.mockReturnValue(false)
    expect(await copyToClipboard('hello')).toBe(false)
  })

  it('appends + removes the textarea cleanly on fallback', async () => {
    writeText.mockRejectedValue(new Error('x'))
    execCommand.mockReturnValue(true)
    const before = document.body.children.length
    await copyToClipboard('hello')
    expect(document.body.children.length).toBe(before)
  })
})
