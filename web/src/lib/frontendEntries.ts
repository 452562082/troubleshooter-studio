// 前端入口的“简化向导”规则。
//
// 用户只需要说明“这是哪个端”以及入口 URL；稳定 ID 和设备类型由 Studio
// 确定性补齐。YAML 导入的高级字段仍由 importer/generator 原样保留，避免旧配置
// 因为 UI 简化而丢信息。

export interface EditableFrontendEntry {
  id: string
  name: string
  url: string
  repo: string
  device_profile: string
  aliases: string
  product_hints: string
  module_hints: string
  path_prefixes: string
}

export const FRONTEND_KIND_SUGGESTIONS = [
  'C端',
  '管理端',
  '运营平台',
  '商家端',
  '官网',
  '小程序',
  '其他前端',
] as const

export function createFrontendEntry(url = '', name = ''): EditableFrontendEntry {
  return {
    id: '',
    name,
    url,
    repo: '',
    device_profile: '',
    aliases: '',
    product_hints: '',
    module_hints: '',
    path_prefixes: '',
  }
}

function inferredIDBase(name: string): string {
  const normalized = name.trim().toLowerCase().replace(/\s+/g, '')
  if (/小程序|miniprogram|mini-program/.test(normalized)) return 'mini-program'
  if (/运营|operation/.test(normalized)) return 'operations'
  if (/管理|后台|admin/.test(normalized)) return 'admin'
  if (/商家|merchant/.test(normalized)) return 'merchant'
  if (/c端|用户端|consumer|\bh5\b/.test(normalized)) return 'consumer-h5'
  if (/官网|website|official/.test(normalized)) return 'website'

  const ascii = normalized
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
  return ascii || 'frontend'
}

/** 为缺少显式 ID 的简化入口生成环境内唯一、后端合法的稳定 ID。 */
export function resolveFrontendEntryID(
  explicitID: string,
  name: string,
  usedIDs: Set<string>,
): string {
  const explicit = explicitID.trim()
  const base = explicit || inferredIDBase(name)
  let candidate = base
  let suffix = 2
  while (usedIDs.has(candidate)) {
    candidate = `${base}-${suffix}`
    suffix++
  }
  usedIDs.add(candidate)
  return candidate
}

/** 设备仅作弱识别信号；无法可靠判断时不写，让运行时使用其它证据。 */
export function inferFrontendDeviceProfile(name: string): string {
  const normalized = name.trim().toLowerCase().replace(/\s+/g, '')
  if (/小程序|移动|手机|mobile|\bh5\b|c端|用户端|consumer/.test(normalized)) return 'mobile'
  if (/管理|后台|运营|商家|官网|admin|operation|merchant|website/.test(normalized)) return 'desktop'
  return ''
}
