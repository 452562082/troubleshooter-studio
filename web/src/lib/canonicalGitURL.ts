// canonicalizeGitURL 把 git URL 归一化(ssh:// / https:// / scp 形式) → "host/owner/repo"
// 小写、无 .git 后缀。给"用户选的本地目录的 origin 跟 yaml 锁定 URL 比对"用,跨协议
// 不必改写 yaml 也能匹配。跟 internal/gitclone/clone.go 的 CanonicalURL 一致语义。
//
// 关键 case(常踩坑):ssh URL 带 port `ssh://git@host:2222/owner/repo.git`,
// `:` 后面是数字 port 而不是 path,**必须丢掉 port**才能跟同仓 https 形式
// (`https://host/owner/repo.git`)归一化后相等。
//
// 改这里**必须**同步 internal/gitclone/clone.go 的 CanonicalURL,两边语义一致是
// 部署时 web 拒绝/接受、Go 端 findInSiblingDirs 命中/丢失能保持一致的前提。
export function canonicalizeGitURL(raw: string): string {
  let s = (raw || '').trim()
  if (!s) return ''
  s = s.replace(/^ssh:\/\//, '').replace(/^git\+ssh:\/\//, '')
  s = s.replace(/^https?:\/\//, '').replace(/^git:\/\//, '')
  // user@host:path / user@host:port/path → host:path / host:port/path
  const at = s.indexOf('@')
  if (at >= 0 && !s.slice(0, at).includes('/')) {
    s = s.slice(at + 1)
  }
  // 处理第一个 ':'(出现在第一个 '/' 之前才算):
  //   - 后接数字 + '/' → ssh port,丢掉
  //   - 否则 → scp 风格 path 分隔,':' 换 '/'
  const colon = s.indexOf(':')
  const slash = s.indexOf('/')
  if (colon >= 0 && (slash === -1 || colon < slash)) {
    const after = s.slice(colon + 1)
    const slashAfter = after.indexOf('/')
    if (slashAfter >= 0 && /^\d+$/.test(after.slice(0, slashAfter))) {
      // host:PORT/path → host/path
      s = s.slice(0, colon) + after.slice(slashAfter)
    } else {
      // host:owner/repo(scp)→ host/owner/repo;或末尾无 '/' 的 host:something → host/something
      s = s.slice(0, colon) + '/' + after
    }
  }
  return s.toLowerCase().replace(/\/+$/, '').replace(/\.git$/, '')
}
