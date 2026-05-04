// serviceMatchHelpers —— "由具体到泛化"服务名候选 + 段对齐前缀判定。
//
// 三家自动匹(useCCHubPreload / useKuboardPreload / useLokiLabels)长得几乎一样的小工具,
// 抽这里共用,避免三份各改各的飘开。
//
// 暴露:
//   - serviceMatchKeys(svc)              ['community-grpc-server', 'community-grpc', 'community']
//   - startsAtBoundary(loc, cand)        loc===cand 或 loc.startsWith(cand + '-/_/.')
//   - boundaryHasAnywhere(low, cand)     允许前缀(base-/app-),适合 loki app 标签等场景

// 给一个服务名生成"由具体到泛化"的候选键序列,用于 dataId / cm / loki app / kuboard 三联匹配。
// 例:
//   community-grpc-server → [community-grpc-server, community-grpc, community]
//   api-truss             → [api-truss, api]
//   order                 → [order]
//
// 背景:wizard 给同仓多入口加了 `<repo>-` 前缀做命名消歧(避免跨仓 cmd/grpc-server 撞名),
// 但 nacos / apollo 的 dataId 经常只用 `<repo>` 这一级(`community-test.yaml`),
// 不会带 cmd entry。如果死按完整服务名找,带前缀的 4 个 *-grpc-server 全部匹不到。
// 退化策略:从最右段开始逐段砍,试更短的前缀,直到命中或剩 1 段。
export function serviceMatchKeys(svc: string): string[] {
  const parts = svc.toLowerCase().split('-').filter(Boolean)
  const out: string[] = []
  for (let i = parts.length; i >= 1; i--) {
    out.push(parts.slice(0, i).join('-'))
  }
  return out
}

// 段对齐"开头"判定:locator 等于 cand,或 locator 以 cand + 分隔符开头(- / _ / .)。
// 这样 "community" 不会误命中 "communityfeed-test.yaml",但能命中 "community-test.yaml"。
export function startsAtBoundary(loc: string, cand: string): boolean {
  return loc === cand ||
    loc.startsWith(cand + '-') ||
    loc.startsWith(cand + '.') ||
    loc.startsWith(cand + '_')
}

// boundaryHasAnywhere:label 值要么以 cand 开头、要么含 -cand- / -cand 边界(允许前缀加 base- / app- 这种)。
// loki app 标签常有 base-/app- 前缀,纯 startsAtBoundary 太严会漏 base-admin-truss-dev → admin-truss。
// 仅在 useLokiLabels 用 —— 其他源标签命名更规范,startsAtBoundary 已够。
export function boundaryHasAnywhere(low: string, cand: string): boolean {
  if (startsAtBoundary(low, cand)) return true
  return low.includes('-' + cand + '-') || low.endsWith('-' + cand) ||
    low.includes('_' + cand + '_') || low.endsWith('_' + cand)
}
