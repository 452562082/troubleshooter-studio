import { describe, it, expect } from 'vitest'
import { yamlStr, emitLokiLabelMapping, hasAnyLokiMapping } from './yamlEmit'

describe('yamlStr', () => {
  it('quotes empty string', () => {
    expect(yamlStr('')).toBe('""')
  })
  it('plain ASCII passes through quoted', () => {
    expect(yamlStr('shop')).toBe('"shop"')
  })
  it('escapes embedded double quote', () => {
    expect(yamlStr('a"b')).toBe('"a\\"b"')
  })
  it('escapes backslash', () => {
    expect(yamlStr('C:\\Users')).toBe('"C:\\\\Users"')
  })
  it('quotes strings with special yaml chars', () => {
    // 含冒号 → 强制 quote(避免被解析成 mapping)
    expect(yamlStr('a: b')).toBe('"a: b"')
    expect(yamlStr('foo*bar')).toBe('"foo*bar"')
  })
  it('quotes strings with leading/trailing space', () => {
    expect(yamlStr(' x')).toBe('" x"')
    expect(yamlStr('x ')).toBe('"x "')
  })
})

describe('hasAnyLokiMapping', () => {
  it('returns false when no env has full mapping', () => {
    expect(hasAnyLokiMapping({
      environments: [{ id: 'dev' }],
      lokiMappingByEnv: { dev: { envLabelKey: '', serviceLabelKey: '' } },
      allServiceNames: ['order'],
    })).toBe(false)
  })
  it('returns true when at least one env has both keys', () => {
    expect(hasAnyLokiMapping({
      environments: [{ id: 'dev' }, { id: 'prod' }],
      lokiMappingByEnv: {
        dev: { envLabelKey: 'env', serviceLabelKey: 'app' },
        prod: { envLabelKey: '', serviceLabelKey: '' },
      },
      allServiceNames: ['order'],
    })).toBe(true)
  })
  it('skips envs with empty id', () => {
    expect(hasAnyLokiMapping({
      environments: [{ id: '' }],
      lokiMappingByEnv: { '': { envLabelKey: 'env', serviceLabelKey: 'app' } },
      allServiceNames: ['order'],
    })).toBe(false)
  })
})

describe('emitLokiLabelMapping', () => {
  it('emits env + service map with envValue and serviceValues', () => {
    const lines: string[] = []
    emitLokiLabelMapping(lines, '      ', {
      environments: [{ id: 'dev' }],
      lokiMappingByEnv: {
        dev: {
          envLabelKey: 'env',
          serviceLabelKey: 'app',
          envValue: 'development',
          serviceValues: { order: 'order-svc', shipping: '' }, // 空值跳过
        },
      },
      allServiceNames: ['order', 'shipping'],
    })
    const out = lines.join('\n')
    expect(out).toContain('label_mapping_by_env:')
    expect(out).toContain('      dev:')
    expect(out).toContain('        env_label: "env"')
    expect(out).toContain('        service_label: "app"')
    expect(out).toContain('        env: "development"')
    expect(out).toContain('"order":')
    expect(out).toContain('app: "order-svc"')
    expect(out).not.toContain('"shipping":') // 空 serviceValue 不输出
  })
  it('emits nothing when no env has full mapping', () => {
    const lines: string[] = []
    emitLokiLabelMapping(lines, '      ', {
      environments: [{ id: 'dev' }],
      lokiMappingByEnv: {},
      allServiceNames: ['order'],
    })
    expect(lines).toEqual([])
  })
  it('includes grafana_ds_uid when set', () => {
    const lines: string[] = []
    emitLokiLabelMapping(lines, '  ', {
      environments: [{ id: 'dev' }],
      lokiMappingByEnv: {
        dev: { envLabelKey: 'env', serviceLabelKey: 'app', dsUID: 'loki-1' },
      },
      allServiceNames: [],
    })
    expect(lines.join('\n')).toContain('grafana_ds_uid: "loki-1"')
  })
})
