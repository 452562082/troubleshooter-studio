# CI / Release

正式发版只走 CI，不在开发者工作区直接运行发布脚本。GitHub Actions 和 GitLab CI 都能从 `main` 发布同一个语义版本，并分别生成各自 Release；两端是分发镜像，版本真源是两端保持一致的 `vX.Y.Z` tag 历史。

## 发布入口

| 平台 | 默认行为 | 产物目标 | 鉴权 |
|---|---|---|---|
| GitHub Actions | `main` push 自动按 commit marker 选择 patch/minor/major | GitHub Release | `secrets.GITHUB_TOKEN`，`contents: write` |
| GitLab CI | `main` pipeline 自动按 commit marker 选择 patch/minor/major；minor/major 另保留 manual 兜底 | GitLab Release | protected `GITLAB_TOKEN`，`api + write_repository` |

两端 job 的版本规则相同：

| job | 触发条件 | 版本变化 |
|---|---|---|
| patch | `main` commit message 不含 release marker | `vX.Y.Z` → `vX.Y.(Z+1)` |
| minor | head commit message 含 `[release:minor]` | `vX.Y.Z` → `vX.(Y+1).0` |
| major | head commit message 含 `[release:major]` | `vX.Y.Z` → `v(X+1).0.0` |

一次平台内的 pipeline 只会自动运行一个 release job。GitHub 与 GitLab 是两个独立远端，因此可能并行创建同名 tag 和 Release；只有两端 tag 历史和 `main` commit 一致时，结果才会一致。

## 正常流程

1. 在 feature/test 分支开发。
2. 提交 PR/MR，等待 Go、Web、skill scripts 和桌面构建通过。
3. 合并到 `main`；需要 minor/major 时把 marker 写进最终 merge/squash commit。
4. 检查 GitHub Actions 和 GitLab pipeline 是否都基于同一个 commit 成功。
5. 核对两端 Release 的 tag、commit 和资产名称一致：
   - [GitHub Releases](https://github.com/452562082/troubleshooter-studio/releases)
   - [GitLab Releases](https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/releases)

Release 产物包括：

- macOS dmg zip。
- 跨平台 CLI binary。
- 自动生成的 changelog。

## Tag 同步规则

- 首次启用某个平台前，先把完整 tag 历史同步过去；不能只推最新 tag。
- 两端 `main` 不指向同一 commit，或最新可达 tag 不一致时，先修复镜像同步，不要继续发版。
- 某一端已经成功创建 tag、但资产发布失败时，重跑该端 publish-only 恢复路径；不要手工 bump 新版本绕过失败。
- GitLab minor/major 的 manual 按钮只用于该端漏发或 runner 故障恢复。另一个平台已经发布时，必须确认当前 HEAD、上一个 tag 和目标版本完全一致后再点。

首次同步示例：

```bash
git fetch --tags --all
git push <github-remote> --tags
git push <gitlab-remote> --tags
```

## CI 内部流程

两端都调用 `scripts/release.sh patch|minor|major`：

1. checkout 可访问上一 tag 的历史。
2. 配置 CI 专用 Git identity，并在当前 `main` commit 上运行。
3. 计算下一版本并拒绝空 release、重名 tag 或脏工作树。
4. 生成 changelog，创建 annotated tag 并推送到当前 CI 所在远端。
5. 根据 `PUBLISH_TARGET` 发布：
   - GitHub：`make release-publish-github VERSION=<tag>`。
   - GitLab：`make release-publish VERSION=<tag>`。

GitHub 使用官方 macOS runner。GitLab 需要带 `macos` 标签的 runner、Xcode 工具链、`jq`，以及 masked/protected `GITLAB_TOKEN`。

`scripts/release.sh` 会检查 `$CI`，普通本地环境直接发版会被拒绝。

## minor / major marker

推荐 squash merge，把 PR/MR 标题写成：

```text
feat(api): add schema endpoint [release:minor]
```

使用 merge commit 时，在最终 merge message 加 marker；rebase merge 时，确保 `main` 上触发 pipeline 的 head commit 带 marker。

## 常见问题

| 现象 | 原因 | 处理 |
|---|---|---|
| `仓库还没 tag,自动 bump 没起点` | 当前远端没有可达的 `vX.Y.Z` tag，或 CI 是浅克隆 | 同步完整 tag；GitLab 检查 `GIT_DEPTH` 和 shallow clone 设置 |
| 两端生成了不同版本 | GitHub/GitLab tag 历史或 `main` commit 漂移 | 停止发布，先对齐 commit 和全部 tag |
| `上一 tag 已指向当前 HEAD` | tag 已成功，资产发布失败 | 只重跑当前平台的 publish 恢复，不创建新 tag |
| `permission denied to push tag` | token 权限不足或 protected tag 限制 | GitHub 检查 `contents: write`；GitLab 检查 token scope、Maintainer 权限和 `v*` 保护规则 |
| GitLab release job 长时间 pending | 没有在线的 `macos` runner | 恢复 runner 后重跑该 job |
| patch 没触发 | commit message 含 minor/major marker | 检查对应 minor/major job |

## 本地允许的只读操作

本地只预览版本和 changelog，不创建 tag、不上传资产：

```bash
scripts/release.sh patch --print-only
make release-notes
```

只有 CI 控制面整体不可用且已进入人工发布事故流程时，才允许由维护者在隔离工作区补发已有 tag 的资产；人工操作必须记录目标远端、tag、commit 和资产摘要，不能从普通开发工作树直接执行。
