# CI 与发版

正式发布走 CI。两端独立生成 Release，只有 `main` commit 和可达的 `vX.Y.Z` tag 历史一致，才能得到一致版本。

## 开发检查

先按[开发指南](../CONTRIBUTING.md)准备环境并运行 `make ci`，再推送 `test` 或提交 PR/MR。

| 检查 | GitHub Actions | GitLab CI |
|---|---|---|
| Go lint、测试、覆盖率、审计 | 自动 | 自动 |
| 前端测试、类型检查、构建、审计 | 自动 | 自动 |
| 共享 Python 脚本 | 自动 | 自动 |
| macOS 桌面构建 | 自动 | main/MR 手动任务，需 macOS Runner |

`test` 只验证、不发布。修复推到 `test` 不代表 `main` 已更新；旧运行始终对应旧 commit，重跑它也不会使用新代码。查看 CI 时核对分支、提交和失败步骤。

配置入口：[GitHub workflow](../.github/workflows/ci.yml)、[GitLab CI](../.gitlab-ci.yml)；真实平台验收见[测试指南](testing.md)。

## 发布流程

1. 检查待合并提交的 CI，通过 PR/MR 合入 `main`。
2. 核对两端 `main` commit 和 tag 历史一致，最终提交信息决定版本变化。
3. 检查两端发布任务成功，核对 Release 的 tag、commit 和资产。

| 最终 main 提交信息 | 自动发布 |
|---|---|
| 无 release marker | patch：`vX.Y.Z → vX.Y.(Z+1)` |
| 含 `[release:minor]` | minor：`vX.Y.Z → vX.(Y+1).0` |
| 含 `[release:major]` | major：`vX.Y.Z → v(X+1).0.0` |

每次只使用一个 marker；写入最终 merge/squash commit。GitLab 的 minor/major 保留手动兜底，不能在另一端已发布后未经核对重复升版。

两端调用 `scripts/release.sh`：检查工作区和上一 tag、计算版本、创建并推送 tag，再发布 macOS DMG 压缩包、跨平台 CLI 与 changelog。

- GitHub 使用 macOS Runner 和 `GITHUB_TOKEN` 的 `contents: write` 权限。
- GitLab 需要 `macos` 标签 Runner、Xcode、Go、Node、jq，以及 masked/protected 的 `GITLAB_TOKEN`（`api + write_repository`），同时允许维护者推送受保护的 `v*` tag。
- 分发入口：[GitHub Releases](https://github.com/452562082/troubleshooter-studio/releases)、[GitLab Releases](https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/releases)。

首次启用另一发布端需同步完整 tag 历史。两端 commit 或上一 tag 不一致时先停止发布并对齐；tag 已成功但资产失败时仅补发该 tag 的资产，不再创建新版本。

## 失败排查

| 现象 | 检查与处理 |
|---|---|
| 修复后仍看到旧错误 | 核对分支和 commit，打开最新提交的运行 |
| GitLab 停在 Preparing executor，出现 EOF 或镜像落盘错误 | 尚未执行代码检查；修复 Runner 网络、镜像拉取或容器存储后重试 |
| 没有可达 tag | 同步 tag 历史，检查浅克隆与 `GIT_DEPTH` |
| 两端版本不同 | 停止发布，对齐 main commit 和 tag 历史 |
| tag 已存在、资产缺失 | 使用当前平台的 publish 恢复路径补发，不重复 bump |
| 推送 tag 被拒绝 | 检查令牌权限、维护者身份和 protected tag 规则 |
| 桌面/发布任务 pending | 确认匹配的 macOS Runner 在线 |

## 本地预览

```bash
scripts/release.sh patch --print-only
make release-notes
```

以上不创建 tag、不上传资产。普通开发工作区不执行正式发布；仅 CI 控制面整体不可用且进入人工发布事故流程时，维护者可在隔离工作区补发已有 tag，记录远端、commit 与资产摘要。
