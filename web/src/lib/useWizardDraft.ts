// useWizardDraft —— InitPage 草稿持久化的 localStorage 读写助手。
//
// 两个独立 key:
//   - INIT_WIZARD_KEY   主 draft blob(system / agent / repos / cred 表都在里头)
//   - KUBOARD_STATE_KEY Kuboard 资源树缓存独立存,大 draft 经常因 quota 静默失败时,
//                       这层 fallback 让 kuboard 数据不会被波及;即使主 draft 没存上,
//                       只要这个 key 存了,下次进来下拉 options 仍可用。
//
// 只导出读 helpers + key 常量。写侧(auto-save watch)还跟 InitPage 里 30+ 个 reactive
// 字段交织,留在原地。

export const INIT_WIZARD_KEY = 'tsf-init-wizard-v1'
export const INIT_KUBOARD_STATE_KEY = 'tsf-init-wizard-kuboard-state-v1'

export function loadInitWizardDraft(): any {
  try {
    const raw = localStorage.getItem(INIT_WIZARD_KEY)
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}

export function loadInitKuboardState(): any {
  try {
    const raw = localStorage.getItem(INIT_KUBOARD_STATE_KEY)
    return raw ? JSON.parse(raw) : null
  } catch {
    return null
  }
}
