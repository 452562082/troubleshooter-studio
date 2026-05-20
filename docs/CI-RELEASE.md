# CI/CD 自动 Release 配置

`.github/workflows/ci.yml` 里的 `release-patch` / `release-minor` / `release-major`
三个 job 是**唯一**发版本入口 —— 本地 `make bump-*` / `tag-and-release` 已删(强制
release 走 CI,版本号决策有 audit trail)。底层调 `scripts/release.sh patch|minor|major`,
带 `$CI` 环境变量校验防本地误调。

**典型流程**(用户视角):
1. 在 `test`(或 feature)分支干活
2. 提 PR 合到 `main`(PR check 自动跑 lint/test/web/desktop build 反馈)
3. 合并到 `main`:
   - **默认 → 自动 patch**(vX.Y.Z → vX.Y.(Z+1)),无需任何操作
   - 想发 minor → PR 标题 / squash msg 写 `[release:minor]` → 自动 minor(patch 跳过)
   - 想发 major → PR 标题 / squash msg 写 `[release:major]` → 自动 major
4. 5-10 分钟后 [GitHub Releases 页](https://github.com/452562082/troubleshooter-studio/releases)
   就有新版本(dmg.zip + 6 个跨平台 CLI binary,描述 = 自动 changelog)
5. 一次合并永远只产出 1 个 release(三个 release-* job `if` 互斥)

---

## 一次性配置

GitHub Actions 比自托管 CI 简单很多 —— **无需自配 runner、无需配 token**:

- macOS runner: GitHub 提供的 `macos-latest` runner 已自带 Xcode CLT / Go / Node / `gh` CLI,
  desktop-dmg(hdiutil + sips + iconutil)+ Wails cgo build 直接能跑
- 鉴权: workflow 已声明 `permissions: contents: write`,GitHub 自动注入的
  `secrets.GITHUB_TOKEN` 就足够 push tag + 创 Release,**不需要自己签 PAT**

唯一管理员需做的:**初次启用时把历史 tag 从本地推到 GitHub**,否则 `release.sh` 找不到
上一 tag 算不出下一版本号:

```bash
git push origin --tags
```

---

## 触发条件细节

`release-patch` 的 `if`:

```yaml
if: ${{ github.event_name == 'push'
     && github.ref == 'refs/heads/main'
     && !contains(github.event.head_commit.message, '[release:minor]')
     && !contains(github.event.head_commit.message, '[release:major]') }}
```

`release-minor` / `release-major` 对偶,只在 commit msg 含对应 marker 时触发。
三个 job `if` 互斥 → 一次 push 只跑一个。

**[release:minor] / [release:major] marker 怎么塞进 commit msg**:

- **Squash merge**(推荐):GitHub 把 PR commits 压成一条,默认用 PR title。
  PR title 改成 `feat(api): new schema endpoint [release:minor]`,squash 后
  main 上 head_commit.message 就含 `[release:minor]` → 自动跑 minor。
- **Merge commit**:默认 msg 是 "Merge pull request #N from ..."。
  在 PR 合并时点 "Edit message" 加 `[release:minor]` 到 msg 末尾。
- **Rebase merge**:每个 commit 都单独进 main,head commit msg 是 PR 最后一个
  commit 的 msg → 那个 commit 的 msg 含 marker 才触发。

---

## 工作流详细步骤

merge 到 main 时 release 三 job 之一被触发,内部按顺序:

1. **checkout (fetch-depth: 0)** — release.sh 需要全 history 算 changelog
2. **setup-go / setup-node** — 用 `go.mod` 里的 Go 版本(当前 1.25.10)+ Node 20
3. **setup git identity** — 用 `github-actions[bot]` 身份提 tag,
   `git checkout -B main` 让 detached HEAD checkout 变成 branch(release.sh 用 git push 需要 branch)
4. **scripts/release.sh `<level>`** — 顺序做:
   - 算下一版本号(从上一 tag bump,严格 vX.Y.Z 格式校验)
   - 幂等检测:上一 tag 已指向当前 HEAD → 进 publish-only 重试模式
   - 工作树/重名检查
   - `scripts/changelog.sh` 生成 release notes(commit subject 自动归集)
   - `git tag -a -F -` 写 annotation
   - `git push origin HEAD:main` + `git push origin <tag>`
   - `make release-publish-github VERSION=<tag>`:
     - `desktop-dmg`(`.app` bundle → `.dmg`,需 macOS)
     - `release`(跨平台编 6 个 binary 到 `dist/bin/`)
     - `scripts/publish-github-release.sh`:
       - `gh release view` 检查同名 release(在的话 `gh release delete --yes --cleanup-tag=false` 删了重建,幂等)
       - `gh release create <tag> --notes-file <changelog>` + 一次性 upload 所有 assets

---

## 排查表

| 现象 | 原因 | 修复 |
|---|---|---|
| `❌ 仓库还没 tag,自动 bump 没起点` | 仓库无 vX.Y.Z 严格格式 tag | 本地 `git tag -a v0.1.0 -m initial && git push origin v0.1.0`,再 trigger release |
| `❌ 范围内无 commits,空 release 没意义` | 上一 tag 跟当前 HEAD 同一 commit | 先 commit 改动再 bump;或撤上一 tag 重打:`git tag -d <tag> && git push origin --delete <tag>` |
| `⚠ 上一 tag 已指向当前 HEAD —— 进 publish-only 重试模式` | 上次 publish 阶段失败(通常 macOS runner 超时 / build 中途挂),tag 已 push 但 release 没建 | 自动重试 publish(`make release-publish-github VERSION=<last_tag>`),幂等安全 |
| `gh: command not found` | 极少数情况 macos-latest 镜像没装 gh | 加一步 `brew install gh`(应该自带,如果没有提 issue 给 actions/runner-images) |
| `error: failed to push some refs ... refusing to allow ... workflow scope` | OAuth token scope 不足(只在通过 OAuth https push 出现,Actions 默认 token 不会撞) | 用 SSH push 或重新 `gh auth refresh -s workflow` |
| `permission denied to push tag` | workflow `permissions` 默认 readonly | 检查 ci.yml 三个 release-* job 都声明了 `permissions: contents: write` |
| `release-patch` job 没触发 | commit msg 含 `[release:minor]` / `[release:major]` → patch 让位是预期 | 看 `release-minor` / `release-major` job 是不是跑了 |

---

## 兜底 / 手动发版

`scripts/release.sh patch|minor|major`(不带 `--print-only`)在本地直接调会被拒,
看到 `❌ scripts/release.sh 仅供 CI 调用`。这是设计:本地 release 已禁,强制都走 CI。

**真有需要手动补发**(CI 全部挂了):

```bash
# 1) 本地预览版本号
scripts/release.sh patch --print-only           # 看会算成几

# 2) 手动打 tag 并 push(完全不用 release.sh)
git tag -a v0.9.19 -m "manual release v0.9.19"
git push origin v0.9.19

# 3) 手动跑 publish(本机需 macOS + GITHUB_TOKEN)
export GITHUB_TOKEN=$(gh auth token)
make release-publish-github VERSION=v0.9.19
```

**只想看下次发版 changelog 不动 git**:

```bash
make release-notes
```
