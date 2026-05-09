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

# 范围决定:
# - 如果 VERSION 是已存在的 tag(publish 阶段调用)→ "上一 tag..VERSION"(算 VERSION 这版本本身的改动)
# - 如果 VERSION 是未来要打的 tag(release-tag/bump 阶段调用,tag 还没在)→ "上一 tag..HEAD"
# - 没传 VERSION(release-notes dry-run)→ "上一 tag..HEAD"(预览即将发布的版本)
if [ -n "$VERSION" ] && git rev-parse --verify --quiet "$VERSION" >/dev/null; then
    # VERSION 已是 tag,算"VERSION 之前的最近 tag → VERSION"范围
    last_tag=$(git describe --tags --abbrev=0 "${VERSION}^" 2>/dev/null || true)
    end_ref="$VERSION"
    end_label="$VERSION"
else
    # 没传 / VERSION 还没作为 tag 存在 → 拿当前 HEAD 之前的最近 tag
    last_tag=$(git describe --tags --abbrev=0 2>/dev/null || true)
    end_ref="HEAD"
    end_label="HEAD"
fi
if [ -z "$last_tag" ]; then
    range="$end_ref"
    range_label="<repo start>..$end_label"
else
    range="${last_tag}..${end_ref}"
    range_label="${last_tag} → ${end_label}"
fi

# subject + short sha,排除 merge commits(squash 工作流下没 merge,但加 --no-merges 防御)
commits=$(git log "$range" --no-merges --pretty=format:"- %s (%h)")

if [ -z "$commits" ]; then
    echo "❌ 范围内($range_label)无 commits,空 release 没意义,refuse 打 tag" >&2
    echo "" >&2
    echo "可能原因:刚打过 ${last_tag} 还没新提交就再 bump,新 tag 会指向跟上一 tag 同一个 commit" >&2
    echo "解法:" >&2
    echo "  1) 先做几个改动 commit,再 bump(99% 场景)" >&2
    echo "  2) 撤上一 tag 重打:" >&2
    echo "       git tag -d ${last_tag}" >&2
    echo "       git push troubleshooter-studio --delete ${last_tag}" >&2
    echo "       (改代码后)make bump-minor" >&2
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
