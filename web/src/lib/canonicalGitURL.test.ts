// 跟 internal/gitclone/canonical_test.go 对齐。两端语义不一致会导致部署期 web 接受、
// Go 端 findInSiblingDirs 反而丢、或反过来,改任一边都要同步。
import { describe, expect, it } from 'vitest'
import { canonicalizeGitURL } from './canonicalGitURL'

describe('canonicalizeGitURL', () => {
  it.each([
    // scp-style ssh
    ['git@github.com:shop/order-service.git', 'github.com/shop/order-service'],
    ['git@github.com:shop/order-service', 'github.com/shop/order-service'],
    // https
    ['https://github.com/shop/order-service.git', 'github.com/shop/order-service'],
    ['https://github.com/shop/order-service/', 'github.com/shop/order-service'],
    ['http://gitlab.com/a/b.git', 'gitlab.com/a/b'],
    // ssh://
    ['ssh://git@github.com/shop/order-service.git', 'github.com/shop/order-service'],
    ['ssh://github.com/shop/order-service', 'github.com/shop/order-service'],
    // ssh:// 带 port —— port 必须丢掉,跟 https 形式才能匹配(critical case)
    ['ssh://git@gitlab.quguazhan.com:2222/service/truss.git', 'gitlab.quguazhan.com/service/truss'],
    ['ssh://git@gitlab.example.com:22/foo/bar', 'gitlab.example.com/foo/bar'],
    // scp-style 不带 port(冒号后是路径不是数字)
    ['git@gitlab.quguazhan.com:service/truss.git', 'gitlab.quguazhan.com/service/truss'],
    // git:// 协议
    ['git://github.com/shop/order-service.git', 'github.com/shop/order-service'],
    // 大小写
    ['GIT@GITHUB.COM:Shop/Order-Service.GIT', 'github.com/shop/order-service'],
    // 空串
    ['', ''],
  ])('canonicalizes %s → %s', (input, want) => {
    expect(canonicalizeGitURL(input)).toBe(want)
  })

  it('cross-protocol same repo canonicalizes equal (incl. ssh-with-port)', () => {
    const same = [
      'git@gitlab.quguazhan.com:service/truss.git',
      'https://gitlab.quguazhan.com/service/truss',
      'ssh://git@gitlab.quguazhan.com:2222/service/truss.git',
    ]
    const first = canonicalizeGitURL(same[0])
    expect(first).toBe('gitlab.quguazhan.com/service/truss')
    for (const u of same.slice(1)) {
      expect(canonicalizeGitURL(u)).toBe(first)
    }
  })
})
