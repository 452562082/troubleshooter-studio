#!/usr/bin/env bash
# release.sh —— 一站式 release(算版本 → 创 tag → push → publish)。
#
# **本脚本仅供 CI 调用**,本地 release 已禁用 — 强制所有 release 都过 main pipeline,
# 版本号决策有 audit trail,git history 干净统一(都是 CI 用同一身份打 tag)。
#
# 用法(CI):
#   scripts/release.sh patch     # vX.Y.Z → vX.Y.(Z+1)
#   scripts/release.sh minor     # vX.Y.Z → vX.(Y+1).0
#   scripts/release.sh major     # vX.Y.Z → v(X+1).0.0
#
# 用法(本地)— 仅 dry-run 预览:
#   想看下次发版的 changelog 长啥样:  make release-notes
#   想看下版本号会算成几:            scripts/release.sh patch --print-only
#   想发布?提交 PR/MR → main → 由 GitHub Actions / GitLab CI 发布
#
# CI 触发流(.github/workflows/ci.yml / .gitlab-ci.yml 调用):
#   1. 校 $CI 已设(CI 环境标志,本地误调用直接拒绝)
#   2. 由对应平台校验发布凭据和 tag push 权限
#   3. 拿上一 tag,按 LEVEL 算下一版本号(SemVer 严格 vX.Y.Z)
#   4. 工作树脏 / tag 重名检查
#   5. scripts/changelog.sh 生成 release notes(commit subject 自动归集)
#   6. git tag -a -F - 写 annotation
#   7. git push origin <branch> + git push origin <tag>
#   8. 按 PUBLISH_TARGET 多平台编译、打 dmg，并上传到当前 CI 平台的 Release
set -euo pipefail

LEVEL="${1:-}"
PRINT_ONLY=""
if [ "${2:-}" = "--print-only" ]; then
    PRINT_ONLY="1"
fi

case "$LEVEL" in
    patch|minor|major) ;;
    *)
        echo "usage: scripts/release.sh patch|minor|major [--print-only]" >&2
        exit 2
        ;;
esac

# ── 防护:本地误跑直接拒 ────────────────────────────────────────────
# CI 环境(GitLab/GitHub Actions/Buildkite/...)都会设 CI=true。本地默认无。
# 加 --print-only 例外:本地预览版本号(算给看,不动任何东西)。
if [ -z "${CI:-}" ] && [ -z "$PRINT_ONLY" ]; then
    cat >&2 <<'EOF'
❌ scripts/release.sh 仅供 CI 调用,本地 release 已禁用。

想发版:
  1) 提 PR/MR 合到 main
  2) 等待 main 的 GitHub Actions / GitLab CI 发布任务
  详见 docs/CI-RELEASE.md

本地预览(不动 git):
  make release-notes                          # 看下次发版会是什么 changelog
  scripts/release.sh patch --print-only       # 看版本号会算成几
EOF
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# ── 1. 算下一版本号(从上一 tag 推断)───────────────────────────────
last=$(git describe --tags --abbrev=0 2>/dev/null || true)
if [ -z "$last" ]; then
    echo "❌ 仓库还没 tag,自动 bump 没起点。手工打 v0.1.0 让 CI 接力:" >&2
    echo "   git tag -a v0.1.0 -m 'initial'  &&  git push origin v0.1.0" >&2
    exit 1
fi
if ! echo "$last" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "❌ 上一 tag '$last' 不是 vX.Y.Z 严格格式,bump 拒绝" >&2
    exit 1
fi

ver="${last#v}"
IFS='.' read -r major minor patch <<< "$ver"
case "$LEVEL" in
    patch) patch=$((patch + 1)) ;;
    minor) minor=$((minor + 1)); patch=0 ;;
    major) major=$((major + 1)); minor=0; patch=0 ;;
esac
NEXT="v${major}.${minor}.${patch}"

if [ -n "$PRINT_ONLY" ]; then
    echo "$NEXT"
    exit 0
fi

# ── 1.5 idempotency check:上一 tag 已经在 HEAD → publish-only 重试 ─────
# 痛点:macOS runner 跑 release-publish(跨平台编 5 个 triple binary + dmg 打包 + GitLab
# API 多文件 upload)总耗时常 15-25 分钟,撞 .release-base 的 30min timeout 不算罕见。
# 一旦 publish 阶段失败,tag 已经在 line 110-128 push 到远端了,GitLab Release 却没建好。
# 此时 last tag commit == HEAD,用户重点 release:* 按钮会被 changelog.sh 的"范围无 commits"
# 防护卡死(refuse 打 tag,因为 NEXT 跟 last 之间确实没新 commit)→ 无法 retry,只能本地
# make release-publish 兜底,但 token 配置麻烦,体验差。
#
# 检测到 "tag 阶段成功 / publish 阶段未完成" 的 dirty state → 跳过 bump+tag,直接重跑
# publish。publish-gitlab-release.sh 头注释明示 "同名 release 已存在 → 删了重建,
# 简化幂等",所以重跑安全(产物会被新一次的覆盖,不会出现混合状态)。
last_commit=$(git rev-parse "${last}^{commit}")
head_commit=$(git rev-parse HEAD)
if [ "$last_commit" = "$head_commit" ]; then
    echo "⚠ 上一 tag '$last' 已经指向当前 HEAD —— 大概率上次 release 走到 publish 阶段失败 / 超时" >&2
    echo "▶ 进入 publish-only 重试模式:跳过 bump+tag,直接重跑 $last 的 publish" >&2
    echo "  (publish 脚本对同名 release force overwrite,幂等安全)" >&2
    make "${PUBLISH_TARGET:-release-publish}" VERSION="$last"
    echo ""
    echo "✓ republish $last 完成"
    exit 0
fi

echo "▶ 自动 bump:$last → $NEXT"

# ── 2. 工作树/重名检查 ─────────────────────────────────────────────
if ! git diff --quiet || ! git diff --cached --quiet; then
    echo "❌ 工作树有未 commit 改动,refuse 打 tag(防把脏改动当 release)" >&2
    git status --short >&2
    exit 1
fi
if git rev-parse --verify --quiet "$NEXT" >/dev/null; then
    echo "❌ tag $NEXT 已存在(本地或远端),refuse 覆盖" >&2
    exit 1
fi

# ── 3. 生成 changelog → 写 tag annotation ──────────────────────────
msg=$("$SCRIPT_DIR/changelog.sh" "$NEXT") || {
    echo "❌ 生成 changelog 失败" >&2
    exit 1
}
echo "─── 写入 $NEXT tag annotation ───"
echo "$msg"
echo "──────────────────────────────────"
echo "$msg" | git tag -a "$NEXT" -F -

# ── 4. push commits + tag(失败回滚 local tag)────────────────────
remote=$(git config --get "branch.$(git symbolic-ref --short HEAD).remote" 2>/dev/null || echo origin)
# 显式给 HEAD:branch refspec —— 本地开发(有 upstream)和 CI(detached HEAD checkout
# 后我们手动 git checkout -B main,没设 upstream tracking)都能过。少了显式 ref
# git push 在 CI 上会 fatal: The current branch main has no upstream branch.
branch=$(git symbolic-ref --short HEAD 2>/dev/null || echo main)
echo "▶ push to $remote ($branch)"
git push "$remote" "HEAD:$branch" || {
    echo "❌ push commits 失败" >&2
    git tag -d "$NEXT"
    exit 1
}
git push "$remote" "$NEXT" || {
    echo "❌ push tag 失败" >&2
    git tag -d "$NEXT"
    exit 1
}

# ── 5. 编多平台 binary + 打 dmg + 上传 Release ─────────────────────
# 默认走 release-publish(GitLab,内部 = check-token + desktop-dmg + release + publish-gitlab-release.sh)。
# GitHub Actions 在 job env 里 export PUBLISH_TARGET=release-publish-github 走 GitHub 链路。
# 对应 Token 必须已注入(GitLab 是 .release-base before_script 校验;GitHub 是 secrets.GITHUB_TOKEN 自动注入)。
PUBLISH_TARGET="${PUBLISH_TARGET:-release-publish}"
echo "▶ 编 + 上传 Release(make $PUBLISH_TARGET)"
make "$PUBLISH_TARGET" VERSION="$NEXT"

echo ""
echo "✓ release $NEXT 完成"
