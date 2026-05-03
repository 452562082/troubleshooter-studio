// useURLProbe —— Step 4 域名 api/web 自动连通性测试 composable。
//
// 行为:用户填 api_domain / web_domain 时 800ms 防抖触发 GET 探测;不显示按钮。
// key = `${envIndex}:${kind}` (kind = api / web)。重新填 / 切 env 顺序都能正确刷新。
//
// 切到目标 step 时主动对已存在的 env 值跑一次探测(不等用户重新输入)。
import { reactive, watch, type Ref } from 'vue'
import { isDesktop, probeURL } from './bridge'
import type { URLProbeState } from './probeTypes'

interface ProbeEnvLike {
  api_domain: string
  web_domain: string
}

export function useURLProbe(
  currentStep: Ref<number>,
  environments: ProbeEnvLike[],
  /** 进入哪一步触发批量重试(InitPage 当前是 Step 3 = 环境列表) */
  triggerStep: number,
) {
  const urlProbeResults = reactive<Record<string, URLProbeState>>({})
  const urlProbeTimers: Record<string, ReturnType<typeof setTimeout>> = {}

  function urlProbeKey(envIdx: number, kind: 'api' | 'web'): string {
    return `${envIdx}:${kind}`
  }

  function scheduleURLProbe(envIdx: number, kind: 'api' | 'web', rawURL: string) {
    const k = urlProbeKey(envIdx, kind)
    if (urlProbeTimers[k]) clearTimeout(urlProbeTimers[k])
    const url = (rawURL || '').trim()
    if (!url) {
      delete urlProbeResults[k]
      return
    }
    urlProbeTimers[k] = setTimeout(async () => {
      if (!isDesktop()) return
      urlProbeResults[k] = { status: 'loading' }
      try {
        const r = await probeURL(url)
        urlProbeResults[k] = r.ok
          ? { status: 'ok', latency: r.latency, detail: r.detail }
          : { status: 'fail', error: r.error || '不可达' }
      } catch (e: any) {
        urlProbeResults[k] = { status: 'fail', error: String(e?.message || e) }
      }
    }, 800)
  }

  // 切到目标 step / 已存在的 env 值,做一次主动探测(不等用户重新输入)
  watch(currentStep, (s) => {
    if (s !== triggerStep) return
    environments.forEach((env, i) => {
      if (env.api_domain) scheduleURLProbe(i, 'api', env.api_domain)
      if (env.web_domain) scheduleURLProbe(i, 'web', env.web_domain)
    })
  }, { immediate: true })

  return { urlProbeResults, urlProbeKey, scheduleURLProbe }
}
