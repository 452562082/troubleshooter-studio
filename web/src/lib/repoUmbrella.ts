// repoUmbrella —— umbrella 父仓被 child 行的 parent_repo 引用次数判定。
//
// 抽出来的动机(从 InitPage 原 inline filter 抽成纯函数):
// (1) removeRepo 跟 countUmbrellaChildren 两份相同的 filter,DRY 违反,改一个忘另一个
// (2) 空 name 仓库 trim 后跟"parent_repo 也空"的 child 行互相 '' === '' 误匹配
//     (用户截图复现:新加仓库 name 还没填 / parent_repo 空字符串的 child 行
//     互相匹配 → 把新仓库误判为 umbrella → "被 6 个子模块引用" hint /
//     URL 字段 readonly / 删除按钮 disabled,见 f88824d fix commit)
//
// 业务语义:本仓是 umbrella + 有 child 引用时,身份字段(URL / 本地路径 / clone 父目录 /
// source 切换)必须锁住,否则用户改 URL / 切到别的本地目录 → umbrella 指向另一个项目 →
// children 路径定位全部错位(parent_repo 引用的本仓 name 不会跟着变)。要改身份必须先
// 删干净 children。

export interface RepoUmbrellaCheck {
  /** child 行声明的 parent_repo 字段(等于某个 umbrella 仓库 name 时建立引用) */
  parent_repo?: string
}

/**
 * 统计 repos 里 parent_repo 等于 targetName 的 child 行数。
 *
 * targetName 空(新加仓库 name 还没填)→ 直接 return 0,不进 filter:
 * 否则 '' === '' 会跟所有 "parent_repo 也空" 的 child 行误匹配,把新仓库当成 umbrella。
 */
export function countUmbrellaChildren(
  repos: readonly RepoUmbrellaCheck[],
  targetName: string,
): number {
  const myName = targetName.trim()
  if (!myName) return 0
  return repos.filter(r => (r.parent_repo || '').trim() === myName).length
}
