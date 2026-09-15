export interface ObsConnectionSourceData {
  creds: Record<string, Record<string, string>>
}

export interface ResolveObsFieldContext {
  toolInputs: Record<string, string>
  sourceCreds: Record<string, ObsConnectionSourceData | undefined>
  toolKeyFor: (cat: 'obs' | 'ds', tool: string, envID: string, field: string) => string
}

/** Resolve an observability field, including the supported K8s-runtime reuse path. */
export function resolveObsFieldValue(
  ctx: ResolveObsFieldContext,
  tool: string,
  envID: string,
  field: string,
): string {
  const direct = (ctx.toolInputs[ctx.toolKeyFor('obs', tool, envID, field)] || '').trim()
  if (direct || tool !== 'k8s_runtime') return direct

  const provider = (
    ctx.toolInputs[ctx.toolKeyFor('obs', 'k8s_runtime', envID, 'provider')] || 'kuboard'
  ).trim() || 'kuboard'
  if (field === 'provider') return provider

  if (provider === 'one2all') {
    const shared = ctx.sourceCreds.one2all?.creds?.['_shared_'] || {}
    if (field === 'url') return (shared.mcp_url || '').trim()
    if (field === 'api_key') return (shared.token || '').trim()
    return ''
  }

  const kuboard = ctx.sourceCreds.kuboard?.creds?.[envID] || {}
  return (kuboard[field] || '').trim()
}

export function obsConnectionReuseLabel(ctx: ResolveObsFieldContext, envID: string): string {
  const directURL = (ctx.toolInputs[ctx.toolKeyFor('obs', 'k8s_runtime', envID, 'url')] || '').trim()
  if (directURL || !resolveObsFieldValue(ctx, 'k8s_runtime', envID, 'url')) return ''
  return resolveObsFieldValue(ctx, 'k8s_runtime', envID, 'provider') === 'one2all'
    ? '复用配置源 one2all 连接'
    : '复用配置源 Kuboard 连接'
}

export function isEffectiveObsFieldHidden(
  ctx: ResolveObsFieldContext,
  tool: string,
  envID: string,
  field: CredField,
  fallback: (tool: string, envID: string, field: CredField) => boolean,
): boolean {
  if (tool !== 'k8s_runtime') return fallback(tool, envID, field)
  return isCredFieldHidden(field, sibling => resolveObsFieldValue(ctx, tool, envID, sibling))
}
import { isCredFieldHidden, type CredField } from './credFields'
