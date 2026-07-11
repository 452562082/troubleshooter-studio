import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import CodeIntelligenceToggle from './CodeIntelligenceToggle.vue'

describe('CodeIntelligenceToggle', () => {
  it('defaults unchecked and emits v-model updates', async () => {
    const wrapper = mount(CodeIntelligenceToggle)
    const input = wrapper.get('input[type="checkbox"]')

    expect((input.element as HTMLInputElement).checked).toBe(false)

    await input.setValue(true)

    expect(wrapper.emitted('update:modelValue')).toEqual([[true]])
  })

  it('discloses the local footprint, storage, telemetry, and fallback contract', () => {
    const wrapper = mount(CodeIntelligenceToggle)
    const text = wrapper.text()

    expect(text).toContain('启用 CodeGraph 代码智能')
    expect(text).toContain('约 200 MB+')
    expect(text).toContain('.codegraph')
    expect(text).toContain('索引仅存本机')
    expect(text).toContain('关闭 CodeGraph telemetry')
    expect(text).toContain('失败不影响部署')
    expect(text).toContain('回退到 git diff + rg + Read')
  })
})
