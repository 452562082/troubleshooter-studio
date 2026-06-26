# CI / Release

发版只走 GitHub Actions。不要在本地直接跑 release 脚本发版本。

## 发版入口

`.github/workflows/ci.yml` 里有 3 个互斥 job：

| job | 触发条件 | 版本变化 |
|---|---|---|
| `release-patch` | push 到 `main`，commit message 不含 release marker | `vX.Y.Z` -> `vX.Y.(Z+1)` |
| `release-minor` | head commit message 含 `[release:minor]` | `vX.Y.Z` -> `vX.(Y+1).0` |
| `release-major` | head commit message 含 `[release:major]` | `vX.Y.Z` -> `v(X+1).0.0` |

一次 merge 只会产出一个 release。

## 正常流程

1. 在 feature/test 分支开发。
2. 提 PR，等待 lint/test/web/desktop build 通过。
3. 合并到 `main`。
4. 5-10 分钟后检查 [GitHub Releases](https://github.com/452562082/troubleshooter-studio/releases)。

Release 产物：

- macOS dmg zip
- 6 个跨平台 CLI binary
- 自动 changelog

## minor / major 怎么触发

推荐 squash merge，把 PR 标题写成：

```text
feat(api): add schema endpoint [release:minor]
```

其他 merge 方式：

- merge commit：合并时编辑 merge message，加 marker
- rebase merge：最后一个 commit message 需要含 marker

## 首次配置

GitHub Actions 使用官方 runner 和 `secrets.GITHUB_TOKEN`，不需要自建 runner 或 PAT。首次启用前，把历史 tag 推到 GitHub：

```bash
git push origin --tags
```

否则 `scripts/release.sh` 无法从上一 tag 计算下一版本。

## release job 做什么

1. checkout 全历史。
2. setup Go 和 Node 20。
3. 设置 `github-actions[bot]` git 身份。
4. 执行 `scripts/release.sh patch|minor|major`：
   - 计算下一版本号
   - 检查重名 tag、工作树、空 release
   - 生成 changelog
   - 创建 annotated tag
   - push `main` 和 tag
   - 执行 `make release-publish-github VERSION=<tag>`
5. publish 阶段构建 dmg 和 CLI binary，并用 `gh release create` 上传。

`scripts/release.sh` 带 `$CI` 校验，本地直接发版会拒绝。

## 常见问题

| 现象 | 原因 | 处理 |
|---|---|---|
| `仓库还没 tag,自动 bump 没起点` | GitHub 上没有 `vX.Y.Z` tag | 本地创建初始 tag 并 push |
| `范围内无 commits,空 release 没意义` | 上一 tag 已指向当前 HEAD | 先合入新 commit，或删除错误 tag 后重打 |
| `上一 tag 已指向当前 HEAD` | 上次 tag 已 push，但 publish 失败 | CI 会进入 publish-only 重试 |
| `permission denied to push tag` | workflow token 权限不足 | 检查 release job 是否有 `permissions: contents: write` |
| `release-patch` 没触发 | commit message 含 minor/major marker | 看对应 release job |
| `gh: command not found` | runner 镜像异常 | 临时加 `brew install gh`，并反馈 runner image |

初始 tag 示例：

```bash
git tag -a v0.1.0 -m initial
git push origin v0.1.0
```

## 手动兜底

只有 CI 全挂且必须补发时才手动做：

```bash
# 预览版本号
scripts/release.sh patch --print-only

# 手动打 tag
git tag -a v0.9.19 -m "manual release v0.9.19"
git push origin v0.9.19

# 本机需 macOS + GITHUB_TOKEN
export GITHUB_TOKEN=$(gh auth token)
make release-publish-github VERSION=v0.9.19
```

只看下次 changelog：

```bash
make release-notes
```
