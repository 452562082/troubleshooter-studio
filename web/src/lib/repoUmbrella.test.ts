// countUmbrellaChildren 单测:覆盖 f88824d fix 的退化场景 + 正常路径,
// 防回归"空 name + 空 parent_repo 误匹配"或者修复时把正常逻辑改坏。
import { describe, expect, it } from 'vitest'
import { countUmbrellaChildren } from './repoUmbrella'

describe('countUmbrellaChildren', () => {
  it('正常 umbrella name "truss" + N 个 child(parent_repo="truss") → 返回 N', () => {
    const repos = [
      { parent_repo: 'truss' },
      { parent_repo: 'truss' },
      { parent_repo: 'truss' },
      { parent_repo: 'other-monorepo' },
      { parent_repo: '' },
      {},
    ]
    expect(countUmbrellaChildren(repos, 'truss')).toBe(3)
  })

  it('targetName 空 → 直接 return 0(防 "" === "" 跟空 parent_repo 误匹配)', () => {
    // 用户截图复现的退化场景:新加仓库 name="" + 多个 child 行 parent_repo 也空
    const repos = [
      { parent_repo: '' },
      { parent_repo: '' },
      { parent_repo: '' },
      { parent_repo: '' },
      { parent_repo: '' },
      { parent_repo: '' },
    ]
    expect(countUmbrellaChildren(repos, '')).toBe(0)
    // 全空格 trim 后空 → 同款防御
    expect(countUmbrellaChildren(repos, '   ')).toBe(0)
  })

  it('parent_repo 前后含空格 → trim 后比较', () => {
    const repos = [
      { parent_repo: '  truss  ' },
      { parent_repo: 'truss' },
      { parent_repo: 'trussX' },
    ]
    expect(countUmbrellaChildren(repos, 'truss')).toBe(2)
  })

  it('targetName 前后含空格 → trim 后比较', () => {
    const repos = [{ parent_repo: 'truss' }, { parent_repo: 'truss' }]
    expect(countUmbrellaChildren(repos, '  truss  ')).toBe(2)
  })

  it('repos 空数组 → 0', () => {
    expect(countUmbrellaChildren([], 'truss')).toBe(0)
  })

  it('targetName 非空但无 child 匹配 → 0', () => {
    const repos = [
      { parent_repo: 'other' },
      { parent_repo: '' },
      {},
    ]
    expect(countUmbrellaChildren(repos, 'truss')).toBe(0)
  })

  it('parent_repo undefined / 字段缺失 → 安全处理为空字符串', () => {
    const repos = [
      {},
      { parent_repo: undefined },
      { parent_repo: 'truss' },
    ]
    expect(countUmbrellaChildren(repos, 'truss')).toBe(1)
  })
})
