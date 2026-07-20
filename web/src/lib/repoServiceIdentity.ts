// 仓库的“服务名”有两种不同用途：
//   1. 配置服务：参与配置中心、数据层扫描；
//   2. 运行时身份：用于仓库 ↔ Deployment/日志/调用链映射。
// 前端只有第 2 种身份，不能因为不读 Nacos/数据库就把它从运行时视图清掉。

const CONFIG_SERVICE_ROLES = new Set(['backend', 'gateway', 'middleware', 'admin'])

export function isConfigServiceRole(role?: string): boolean {
  return CONFIG_SERVICE_ROLES.has((role || 'backend').trim() || 'backend')
}

export function supportsRuntimeServiceNames(role?: string): boolean {
  return isConfigServiceRole(role) || role === 'frontend'
}

function splitServiceNames(value?: string): string[] {
  return (value || '').split(',').map(item => item.trim()).filter(Boolean)
}

export function serviceNamesForRole(role: string | undefined, repoName: string, current?: string): string {
  if (!supportsRuntimeServiceNames(role)) return ''
  const names = splitServiceNames(current)
  return names.length > 0 ? names.join(', ') : repoName.trim()
}

export interface ScannedServiceIdentityInput {
  role?: string
  repoName: string
  detectedServiceNames?: string[]
  previousRole?: string
  previousServiceNames?: string
}

// 扫描后的身份收敛规则：
// - 后端服务保留原规则：单入口用扫描值，多入口/未识别用 repo.name 等待用户确认；
// - 前端单入口默认用稳定的 repo.name（package.json.name 常是 @scope/app，不等于 Deployment）；
// - 扫到多个可部署前端入口时保留完整列表；
// - 用户已为前端确认过运行时名称时，重新扫描不能覆盖；
// - 非运行节点清空，避免进入后续映射。
export function serviceNamesAfterScan(input: ScannedServiceIdentityInput): string {
  const repoName = input.repoName.trim()
  const detected = (input.detectedServiceNames || []).map(item => item.trim()).filter(Boolean)

  if (isConfigServiceRole(input.role)) {
    return detected.length === 1 ? detected[0] : repoName
  }
  if (input.role === 'frontend') {
    const previous = splitServiceNames(input.previousServiceNames)
    const legacyPackageIdentity = previous.length === 1 && /^@[^/]+\/[^/]+$/.test(previous[0])
    const previousIsRepoFallback = previous.length === 1 && previous[0] === repoName
    if (detected.length > 1 && (
      input.previousRole !== 'frontend' || previous.length === 0 ||
      legacyPackageIdentity || previousIsRepoFallback
    )) {
      return Array.from(new Set(detected)).join(', ')
    }
    if (input.previousRole === 'frontend' && previous.length > 0) return previous.join(', ')
    return repoName
  }
  return ''
}

export interface RuntimeIdentityRepo {
  name: string
  role?: string
  service_names?: string
}

// 这里只补“非配置服务”的运行时身份。后端服务仍由 allServiceNames 提供，保证
// 配置中心/数据层与原逻辑完全隔离。
export function runtimeOnlyServiceNames(repo: RuntimeIdentityRepo): string[] {
  if (repo.role === 'frontend') {
    const explicit = splitServiceNames(repo.service_names)
    return explicit.length > 0 ? explicit : [repo.name.trim()].filter(Boolean)
  }
  if (repo.role === 'mobile') {
    return [repo.name.trim()].filter(Boolean)
  }
  return []
}
