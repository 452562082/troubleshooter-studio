import { describe, it, expect } from 'vitest'
import { computeYamlCodeDiff } from './yamlCodeDiff'

describe('computeYamlCodeDiff', () => {
  it('handles empty yaml + empty report', () => {
    const d = computeYamlCodeDiff({}, {})
    expect(d.repos).toEqual([])
    expect(d.totalNew).toBe(0)
    expect(d.totalMissing).toBe(0)
    expect(d.configCenterMismatch).toBe(false)
  })

  it('flags new services found in code but not yaml', () => {
    const d = computeYamlCodeDiff(
      { repos: [{ name: 'order', service_names: ['order-svc'] }] },
      { repos: [{ name: 'order', service_names: ['order-svc', 'order-worker'] }] },
    )
    expect(d.repos[0].newInCode).toEqual(['order-worker'])
    expect(d.totalNew).toBe(1)
    expect(d.totalMissing).toBe(0)
  })

  it('flags missing services declared in yaml but not seen in code', () => {
    const d = computeYamlCodeDiff(
      { repos: [{ name: 'order', service_names: ['order-svc', 'order-worker'] }] },
      { repos: [{ name: 'order', service_names: ['order-svc'] }] },
    )
    expect(d.repos[0].missingInCode).toEqual(['order-worker'])
    expect(d.totalMissing).toBe(1)
  })

  it('parses csv string service_names', () => {
    const d = computeYamlCodeDiff(
      { repos: [{ name: 'order', service_names: 'a, b , c' }] },
      { repos: [{ name: 'order', service_names: ['a', 'b', 'c'] }] },
    )
    expect(d.repos[0].yamlServices).toEqual(['a', 'b', 'c'])
    expect(d.totalNew).toBe(0)
    expect(d.totalMissing).toBe(0)
  })

  it('falls back to repo.name as yaml service when service_names absent (service role)', () => {
    const d = computeYamlCodeDiff(
      { repos: [{ name: 'order', role: 'backend' }] },
      { repos: [{ name: 'order', service_names: ['order'] }] },
    )
    expect(d.repos[0].yamlServices).toEqual(['order'])
    expect(d.repos[0].missingInCode).toEqual([])
  })

  it('skips service_names diff for non-service roles', () => {
    const d = computeYamlCodeDiff(
      { repos: [{ name: 'web', role: 'frontend' }] },
      { repos: [{ name: 'web', service_names: ['web-app-1'] }] },
    )
    expect(d.repos[0].isServiceRole).toBe(false)
    expect(d.repos[0].newInCode).toEqual([])
    expect(d.repos[0].missingInCode).toEqual([])
    expect(d.repos[0].yamlServices).toEqual([])
  })

  it('uses analyzer role_hint when yaml role missing', () => {
    const d = computeYamlCodeDiff(
      { repos: [{ name: 'web' }] },
      { repos: [{ name: 'web', role_hint: { role: 'frontend' } }] },
    )
    expect(d.repos[0].effectiveRole).toBe('frontend')
    expect(d.repos[0].isServiceRole).toBe(false)
  })

  it('yaml role wins over analyzer hint', () => {
    const d = computeYamlCodeDiff(
      { repos: [{ name: 'x', role: 'backend' }] },
      { repos: [{ name: 'x', role_hint: { role: 'frontend' } }] },
    )
    expect(d.repos[0].effectiveRole).toBe('backend')
    expect(d.repos[0].isServiceRole).toBe(true)
  })

  it('detects config_center mismatch', () => {
    const d = computeYamlCodeDiff(
      { infrastructure: { config_center: { type: 'nacos' } } },
      { config_center: 'apollo' },
    )
    expect(d.configCenterMismatch).toBe(true)
    expect(d.configCenterYaml).toBe('nacos')
    expect(d.configCenterCode).toBe('apollo')
  })

  it('does not flag mismatch when code is unknown', () => {
    expect(computeYamlCodeDiff(
      { infrastructure: { config_center: { type: 'nacos' } } },
      { config_center: 'unknown' },
    ).configCenterMismatch).toBe(false)
  })

  it('does not flag mismatch when both empty', () => {
    expect(computeYamlCodeDiff({}, {}).configCenterMismatch).toBe(false)
  })

  it('totalNew / totalMissing sum across multiple repos', () => {
    const d = computeYamlCodeDiff(
      { repos: [
        { name: 'a', service_names: ['x'] },
        { name: 'b', service_names: ['y'] },
      ] },
      { repos: [
        { name: 'a', service_names: ['x', 'x2'] },     // +1 new
        { name: 'b', service_names: [] },               // 1 missing
      ] },
    )
    expect(d.totalNew).toBe(1)
    expect(d.totalMissing).toBe(1)
  })
})
