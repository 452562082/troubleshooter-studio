// useObsProbe —— Step 7 可观测性工具 (grafana / jaeger / elk / loki / clickhouse / kafka …)
// 自动连通性测试 composable。跟 useURLProbe 同模式但每个工具:
//   - 取主 URL 字段(多数是 'url',ELK 是 'kibana_url')
//   - 按工具是否有 auth_mode 字段决定走 user/pass 还是 api_key Bearer
//   - 通过 toolInputs map 读各字段当前值,key 用 toolKeyFor 拼
//
// 不接管"切到 Step 7 时主动重试"那段 —— 那段还跟 grafana datasources / loki labels /
// k8s_runtime workload 三个独立子流程交织,留在 InitPage 的 triggerStep7Init 里。
import { reactive } from 'vue'
import { isDesktop, probeURLAuth } from './bridge'
import type { CredField } from './credFields'

export interface ToolSpec {
  key: string
  label: string
  description: string
  fields: CredField[]
}

export interface OBSProbeState {
  status: 'idle' | 'loading' | 'ok' | 'fail'
  latency?: string
  detail?: string
  error?: string
}

// 每个 obs 工具的 "主 URL 字段" key —— 多数是 'url',ELK 是 'kibana_url'
function obsPrimaryURLField(spec: ToolSpec): string {
  if (spec.fields.find(f => f.key === 'url')) return 'url'
  if (spec.fields.find(f => f.key === 'kibana_url')) return 'kibana_url'
  return ''
}

export function useObsProbe(
  obsToolSpecs: readonly ToolSpec[],
  toolInputs: Record<string, string>,
  toolKeyFor: (cat: 'obs' | 'ds', toolKey: string, envID: string, fieldKey: string) => string,
) {
  const obsProbeResults = reactive<Record<string, OBSProbeState>>({})
  const obsProbeTimers: Record<string, ReturnType<typeof setTimeout>> = {}

  function obsProbeKey(toolKey: string, envID: string): string {
    return `${toolKey}::${envID}`
  }

  function scheduleObsProbe(toolKey: string, envID: string) {
    const spec = obsToolSpecs.find(s => s.key === toolKey)
    if (!spec) return
    const urlField = obsPrimaryURLField(spec)
    if (!urlField) return
    const k = obsProbeKey(toolKey, envID)
    if (obsProbeTimers[k]) clearTimeout(obsProbeTimers[k])
    const url = (toolInputs[toolKeyFor('obs', toolKey, envID, urlField)] || '').trim()
    if (!url) {
      delete obsProbeResults[k]
      return
    }
    // 仅当工具有 auth_mode 字段(grafana 类二选一鉴权)时按 mode 过滤,避免 stale draft
    // 同时带上 api_key + user/pass 让后端 httpGet 走错鉴权路径(优先 Bearer)。其它工具
    // (elk / clickhouse / kafka 等只有 user/pass 一种鉴权方式)按原行为透传。
    const hasAuthMode = spec.fields.some(f => f.key === 'auth_mode')
    let user = '', pass = '', apiKey = ''
    if (hasAuthMode) {
      const authMode = (toolInputs[toolKeyFor('obs', toolKey, envID, 'auth_mode')] || '').trim()
      const useApiKey = authMode !== 'username_password'
      apiKey = useApiKey ? (toolInputs[toolKeyFor('obs', toolKey, envID, 'api_key')] || '') : ''
      user = useApiKey ? '' : (toolInputs[toolKeyFor('obs', toolKey, envID, 'user')] || '').trim()
      pass = useApiKey ? '' : (toolInputs[toolKeyFor('obs', toolKey, envID, 'pass')] || '')
    } else {
      user = (toolInputs[toolKeyFor('obs', toolKey, envID, 'user')] || '').trim()
      pass = toolInputs[toolKeyFor('obs', toolKey, envID, 'pass')] || ''
      apiKey = toolInputs[toolKeyFor('obs', toolKey, envID, 'api_key')] || ''
    }
    obsProbeTimers[k] = setTimeout(async () => {
      if (!isDesktop()) return
      obsProbeResults[k] = { status: 'loading' }
      try {
        const r = await probeURLAuth(url, user, pass, apiKey)
        obsProbeResults[k] = r.ok
          ? { status: 'ok', latency: r.latency, detail: r.detail }
          : { status: 'fail', error: r.error || '不可达' }
      } catch (e: any) {
        obsProbeResults[k] = { status: 'fail', error: String(e?.message || e) }
      }
    }, 800)
  }

  return { obsProbeResults, obsProbeKey, scheduleObsProbe }
}
