import { describe, expect, it, vi } from 'vitest'
import yaml from 'js-yaml'
import { hydratePortableYAMLFromKeychain } from './portableYAML'

describe('hydratePortableYAMLFromKeychain', () => {
  it('restores source, observability, and datastore secrets using wizard key names', async () => {
    const input = `
system:
  id: base
infrastructure:
  config_centers:
    - id: one2all
      type: one2all
      endpoints:
        - url: http://one2all/mcp
          token: "{{ONE2ALL_TOKEN}}"
  observability:
    grafana:
      enabled: true
      endpoints:
        - env: test
          url: https://grafana.test
          api_key: "{{GRAFANA_API_KEY_TEST}}"
  data_stores:
    - id: mysql
      type: mysql
      endpoints:
        - env: test
          service: base
          dsn: "{{MYSQL_DSN_TEST}}"
`
    const values: Record<string, string> = {
      'base:source:one2all:_shared_:token': 'one2all-secret',
      'base:obs:grafana:test:api_key': 'grafana-"secret"',
      'base:datastore:test:base:mysql:dsn': 'u:p@tcp(mysql:3306)/base',
    }
    const loader = vi.fn(async (key: string) => ({
      ok: Boolean(values[key]),
      api_key: values[key] || '',
    }))

    const output = await hydratePortableYAMLFromKeychain(input, loader)
    const parsed = yaml.load(output) as any
    expect(parsed.infrastructure.config_centers[0].endpoints[0].token).toBe('one2all-secret')
    expect(parsed.infrastructure.observability.grafana.endpoints[0].api_key).toBe('grafana-"secret"')
    expect(parsed.infrastructure.data_stores[0].endpoints[0].dsn).toBe('u:p@tcp(mysql:3306)/base')
    expect(output).toContain('包含可直接部署的明文凭据')
  })
})
