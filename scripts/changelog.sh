#!/usr/bin/env bash
# changelog.sh —— 抽取"上一个 tag 到 HEAD"之间的 commits,渲染成 release notes。
#
# 调用:
#   scripts/changelog.sh              # dry-run,打印到 stdout(给 `make release-notes` 用)
#   scripts/changelog.sh v0.7.0       # 同上,但首行写 "Release v0.7.0"(给 `make release-tag` 用)
#
# 输出格式(stdout):
#   Release vX.Y.Z   (有 VERSION 入参时)
#                    (空行)
#   ## 本版 N 条改动 (vPrev → HEAD)
#                    (空行)
#   - <commit subject> (<short sha>)
#   - ...
#
# 拒绝输出的边界:
#   - 上一 tag 到 HEAD 没有 commits → 退 1 报错(tag 啥都没改是诈骗)
#   - 不在 git 仓库 → 退 1
#
# 不分类 conventional commit 前缀 / 不分组 — commit message 信息密度够,
# annotation 是给人看的,机械分类反而失真。
set -euo pipefail

VERSION="${1:-}"

if ! git rev-parse --git-dir >/dev/null 2>&1; then
    echo "❌ 不在 git 仓库" >&2
    exit 1
fi

# 上一个 tag(annotated 或 lightweight 都收)。如果没有 tag(新仓),取 root commit。
last_tag=$(git describe --tags --abbrev=0 2>/dev/null || true)
if [ -z "$last_tag" ]; then
    range="HEAD"
    range_label="<repo start>..HEAD"
else
    range="${last_tag}..HEAD"
    range_label="${last_tag} → HEAD"
fi

# subject + short sha,排除 merge commits(squash 工作流下没 merge,但加 --no-merges 防御)
commits=$(git log "$range" --no-merges --pretty=format:"- %s (%h)")

if [ -z "$commits" ]; then
    echo "❌ 范围内($range_label)无 commits,无 changelog 内容" >&2
    exit 1
fi

count=$(echo "$commits" | wc -l | tr -d ' ')

# 输出:有 VERSION 加首行,无则只出 changelog 段
if [ -n "$VERSION" ]; then
    echo "Release $VERSION"
    echo ""
fi
echo "## 本版 $count 条改动 ($range_label)"
echo ""
echo "$commits"
