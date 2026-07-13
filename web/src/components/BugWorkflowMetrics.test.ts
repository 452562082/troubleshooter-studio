import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import BugWorkflowMetrics from './BugWorkflowMetrics.vue'

const day = 24 * 60 * 60 * 1_000_000_000

function metrics(completedCases = 5) {
  return {
    completed_cases: completedCases,
    open_cases: 3,
    median_stage_duration: {
      validation: 30 * 60 * 1_000_000_000,
      investigation: 2 * 60 * 60 * 1_000_000_000,
      fix: 4 * 60 * 60 * 1_000_000_000,
      deployment_wait: day,
      regression: 45 * 60 * 1_000_000_000,
      lead_time: 2 * day,
    },
    oldest_waiting_deployment_age: 3 * day,
    agent_execution_duration: 5 * 60 * 60 * 1_000_000_000,
    human_deployment_wait: day,
    retry_count: 2,
    agent_input_tokens: 1200,
    agent_output_tokens: 300,
    blocker_distribution: { waiting_evidence: 2, merge_conflict: 1, deployment_unverified: 3 },
    automation_ratio: 0.8,
    first_regression_success_rate: 0.6,
    still_reproduces_rate: 0.4,
  }
}

describe('BugWorkflowMetrics', () => {
  it('hides rates until five cases are completed', () => {
    const wrapper = mount(BugWorkflowMetrics, { props: { metrics: metrics(4) } })
    expect(wrapper.find('[aria-label="故障闭环指标"]').exists()).toBe(false)
  })

  it('shows compact outcome, wait and blocker metrics', () => {
    const wrapper = mount(BugWorkflowMetrics, { props: { metrics: metrics() } })
    expect(wrapper.find('[aria-label="故障闭环指标"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('进行中 3')
    expect(wrapper.text()).toContain('最长待部署 3天')
    expect(wrapper.text()).toContain('首次回归成功 60%')
    expect(wrapper.findAll('.metric-grid div').map(item => [item.find('dt').text(), item.find('dd').text()])).toContainEqual(['验证', '30分钟'])
    expect(wrapper.findAll('.metric-grid div').map(item => [item.find('dt').text(), item.find('dd').text()])).toContainEqual(['人工部署等待', '1天'])
    expect(wrapper.text()).toContain('部署未确认 3')
  })
})
