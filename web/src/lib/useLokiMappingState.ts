// useLokiMappingState —— Step 7 Loki 标签映射 per-env state 容器(类型 + 兜底初始化 + getter)。
//
// dsList / dsUID / labels / *LabelKey / *LabelValues / envValue / serviceValues 这一坨
// 是同一棵 per-env "Loki UI 状态树",形状重 + 跨 saved 恢复要兜底,集中放一处。
//
// 写侧 runners(loadLokiDatasources / loadLokiLabels / scheduleGrafanaDsAutoload /
// onEnvLabelKeyChanged 等)还跟 toolInputs / sourceCreds / pushLog 多块状态交织,
// 留在 InitPage,直接 mutate 本 composable 暴露的 lokiMappingByEnv。
import { reactive } from 'vue'
import type { GrafanaDatasource } from './bridge'

export interface LokiMappingPerEnv {
  dsList: GrafanaDatasource[]
  dsUID: string
  dsListStatus: 'idle' | 'loading' | 'ok' | 'fail'
  dsListError?: string
  labels: string[]
  labelStatus: 'idle' | 'loading' | 'ok' | 'fail'
  labelError?: string
  envLabelKey: string
  serviceLabelKey: string
  envLabelValues: string[]
  serviceLabelValues: string[]
  envValue: string
  serviceValues: Record<string, string>
  // serviceMatchTried[svc] = true 表示 auto-match 已经跑过这个服务但没找到候选,
  // UI 据此区分"未触发自动匹配(默认空)"vs"匹配过但没找到(应该提示用户)"。
  // 用户手挑后 serviceValues[svc] 非空,UI 自然不再显示"未匹配"提示。
  serviceMatchTried?: Record<string, boolean>
}

export function makeEmptyLokiMappingPerEnv(): LokiMappingPerEnv {
  return {
    dsList: [], dsUID: '', dsListStatus: 'idle',
    labels: [], labelStatus: 'idle',
    envLabelKey: '', serviceLabelKey: '',
    envLabelValues: [], serviceLabelValues: [],
    envValue: '', serviceValues: {},
    serviceMatchTried: {},
  }
}

export function useLokiMappingState(initial?: Record<string, LokiMappingPerEnv>) {
  // saved 里可能存的是切走时的瞬态 'loading'(watcher 在 await 中途触发的快照),
  // 重 mount 后状态卡死成 'loading' 永远转圈。这里在恢复时把所有瞬态 status 一律
  // 重置成 'idle',让 onMounted/triggerStep7Init 重新拉一次。
  const seed = initial ?? {}
  for (const m of Object.values(seed)) {
    if (!m) continue
    if (m.dsListStatus === 'loading') m.dsListStatus = 'idle'
    if (m.labelStatus === 'loading') m.labelStatus = 'idle'
  }
  const lokiMappingByEnv = reactive<Record<string, LokiMappingPerEnv>>(seed)

  function getLokiMapping(envID: string): LokiMappingPerEnv {
    if (!lokiMappingByEnv[envID]) {
      lokiMappingByEnv[envID] = makeEmptyLokiMappingPerEnv()
    } else {
      // 防御:saved 里可能是被 quota 兜底瘦身后的残缺对象(缺 dsList/labels/*LabelValues 等)。
      // 补齐所有字段,免得模板访问 undefined.length 之类直接 throw 把整个页面拉白屏。
      const lm = lokiMappingByEnv[envID] as Partial<LokiMappingPerEnv>
      if (!Array.isArray(lm.dsList)) lm.dsList = []
      if (!lm.dsListStatus) lm.dsListStatus = 'idle'
      if (!Array.isArray(lm.labels)) lm.labels = []
      if (!lm.labelStatus) lm.labelStatus = 'idle'
      if (typeof lm.dsUID !== 'string') lm.dsUID = ''
      if (typeof lm.envLabelKey !== 'string') lm.envLabelKey = ''
      if (typeof lm.serviceLabelKey !== 'string') lm.serviceLabelKey = ''
      if (!Array.isArray(lm.envLabelValues)) lm.envLabelValues = []
      if (!Array.isArray(lm.serviceLabelValues)) lm.serviceLabelValues = []
      if (typeof lm.envValue !== 'string') lm.envValue = ''
      if (!lm.serviceValues || typeof lm.serviceValues !== 'object') lm.serviceValues = {}
    }
    return lokiMappingByEnv[envID]
  }

  return { lokiMappingByEnv, getLokiMapping }
}
