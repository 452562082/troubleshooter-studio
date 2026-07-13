import { shallowMount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import BugInboxPage from './BugInboxPage.vue'
import BugWorkbenchPage from './BugWorkbenchPage.vue'

describe('BugWorkbenchPage compatibility wrapper', () => {
  it('delegates the temporary /bugs route to the browse-only inbox', () => {
    const wrapper = shallowMount(BugWorkbenchPage)

    expect(wrapper.findComponent(BugInboxPage).exists()).toBe(true)
    expect(wrapper.find('.workbench-view-tabs').exists()).toBe(false)
    expect(wrapper.find('.case-lifecycle').exists()).toBe(false)
  })
})
