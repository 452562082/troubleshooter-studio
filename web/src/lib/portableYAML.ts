import yaml from 'js-yaml'
import { loadInfraCred, type InfraCredLoadResult } from './bridge/infraCred'

type CredLoader = (key: string) => Promise<InfraCredLoadResult>
type YAMLRecord = Record<string, any>

const placeholderPattern = /^\{\{\s*([^{}\s]+)\s*\}\}$/
const portableBanner = '# 警告：此文件包含可直接部署的明文凭据，请通过受控渠道传输，禁止提交到版本库。'

function asRecord(value: unknown): YAMLRecord | null {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as YAMLRecord : null
}

function placeholderName(value: unknown): string {
  if (typeof value !== 'string') return ''
  return value.match(placeholderPattern)?.[1] || ''
}

function secretKey(systemID: string, kind: string, ...parts: string[]): string {
  return [systemID || 'draft', kind, ...parts].join(':')
}

/**
 * 已部署机器人只保存脱敏 YAML；真实基础设施凭据仍在系统钥匙串。
 * 导出时按 YAML 路径恢复这些值，不改变 tshoot.json 和编辑器草稿的脱敏属性。
 */
export async function hydratePortableYAMLFromKeychain(
  yamlText: string,
  loader: CredLoader = loadInfraCred,
): Promise<string> {
  const parsed = asRecord(yaml.load(yamlText))
  if (!parsed) throw new Error('YAML 顶层必须是对象')
  const systemID = String(asRecord(parsed.system)?.id || '').trim()
  const infra = asRecord(parsed.infrastructure) || {}
  const candidates: Array<{ placeholder: string; key: string }> = []
  const addEndpointFields = (endpoint: YAMLRecord, keyForField: (field: string) => string) => {
    for (const [field, value] of Object.entries(endpoint)) {
      const placeholder = placeholderName(value)
      if (placeholder) candidates.push({ placeholder, key: keyForField(field) })
    }
  }

  const configCenters = Array.isArray(infra.config_centers)
    ? infra.config_centers
    : (asRecord(infra.config_center) ? [infra.config_center] : [])
  for (const rawCenter of configCenters) {
    const center = asRecord(rawCenter)
    if (!center) continue
    const sourceID = String(center.id || center.type || '').trim()
    const sourceType = String(center.type || '').trim()
    for (const rawEndpoint of Array.isArray(center.endpoints) ? center.endpoints : []) {
      const endpoint = asRecord(rawEndpoint)
      if (!endpoint) continue
      const env = sourceType === 'one2all' ? '_shared_' : String(endpoint.env || '').trim()
      addEndpointFields(endpoint, field => secretKey(systemID, 'source', sourceID, env, field))
    }
  }

  const observability = asRecord(infra.observability) || {}
  for (const [tool, rawSpec] of Object.entries(observability)) {
    const spec = asRecord(rawSpec)
    if (!spec) continue
    for (const rawEndpoint of Array.isArray(spec.endpoints) ? spec.endpoints : []) {
      const endpoint = asRecord(rawEndpoint)
      if (!endpoint) continue
      const env = String(endpoint.env || '').trim()
      addEndpointFields(endpoint, field => secretKey(systemID, 'obs', tool, env, field))
    }
  }

  for (const rawStore of Array.isArray(infra.data_stores) ? infra.data_stores : []) {
    const store = asRecord(rawStore)
    if (!store) continue
    const storeID = String(store.id || store.type || '').trim()
    for (const rawEndpoint of Array.isArray(store.endpoints) ? store.endpoints : []) {
      const endpoint = asRecord(rawEndpoint)
      if (!endpoint) continue
      const env = String(endpoint.env || '').trim()
      const service = String(endpoint.service || '').trim()
      addEndpointFields(endpoint, field => secretKey(systemID, 'datastore', env, service, storeID, field))
    }
  }

  const replacements = new Map<string, string>()
  for (const candidate of candidates) {
    if (replacements.has(candidate.placeholder)) continue
    const result = await loader(candidate.key)
    if (result.ok && result.api_key) replacements.set(candidate.placeholder, result.api_key)
  }

  let portable = yamlText
  for (const [placeholder, value] of replacements) {
    portable = portable.split(`"{{${placeholder}}}"`).join(JSON.stringify(value))
    portable = portable.split(`'{{${placeholder}}}'`).join(JSON.stringify(value))
    portable = portable.split(`{{${placeholder}}}`).join(JSON.stringify(value))
  }
  if (!portable.includes('包含可直接部署的明文凭据')) portable = `${portableBanner}\n${portable}`
  return portable
}
