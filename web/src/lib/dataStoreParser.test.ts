import { describe, expect, it } from 'vitest'
import { DS_MATCHERS, parseK8sConfigMapDataContents } from './dataStoreParser'

function matchAll(root: any): Record<string, Record<string, string>> {
  const out: Record<string, Record<string, string>> = {}
  for (const m of DS_MATCHERS) {
    const hit = m.matchYAML(root)
    if (hit) out[m.dsKey] = hit
  }
  return out
}

describe('parseK8sConfigMapDataContents', () => {
  it('merges multiple ConfigMaps before datastore matching', () => {
    const root = parseK8sConfigMapDataContents([
      JSON.stringify({
        'datasource.yaml': `
redis:
  url: redis://cache:6379/0
mongodb:
  uri: mongodb://mongo:27017/app
mysql:
  dsn: user:pass@tcp(mysql:3306)/app
elasticsearch:
  url: http://es:9200
kafka:
  brokers: kafka:9092
`,
      }),
      JSON.stringify({
        'business.yaml': `
distributed_lock:
  ttl: 30
tasks:
  cleanup: true
`,
      }),
    ])

    expect(root.distributed_lock.ttl).toBe(30)
    expect(root.tasks.cleanup).toBe(true)

    const hits = matchAll(root)
    expect(hits.redis.url).toBe('redis://cache:6379/0')
    expect(hits.mongodb.uri).toBe('mongodb://mongo:27017/app')
    expect(hits.mysql.dsn).toBe('user:pass@tcp(mysql:3306)/app')
    expect(hits.elasticsearch.url).toBe('http://es:9200')
    expect(hits.kafka.brokers).toBe('kafka:9092')
  })

  it('also recognizes flat env-style ConfigMap keys', () => {
    const root = parseK8sConfigMapDataContents([
      JSON.stringify({
        REDIS_URL: 'redis://cache:6379/1',
        DB_CONNECTION: 'mysql',
        DB_HOST: 'mysql',
        DB_PORT: '3306',
        DB_DATABASE: 'app',
        DB_USERNAME: 'user',
        DB_PASSWORD: 'pass',
      }),
    ])

    const hits = matchAll(root)
    expect(hits.redis.url).toBe('redis://cache:6379/1')
    expect(hits.mysql.dsn).toBe('user:pass@tcp(mysql:3306)/app')
  })
})
