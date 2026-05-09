#!/usr/bin/env bash
# bump.sh —— 算下一版本号(SemVer)。给 Makefile bump-{patch,minor,major} 用。
#
# 调用:
#   scripts/bump.sh patch   # v0.6.3 → 输出 v0.6.4
#   scripts/bump.sh minor   # v0.6.3 → 输出 v0.7.0
#   scripts/bump.sh major   # v0.6.3 → 输出 v1.0.0
#
# 失败:
#   - 上一 tag 不是 vX.Y.Z 严格格式 → 退 1
#   - 仓库无 tag(首次发布)→ 退 1,提示用 release-tag VERSION=v0.1.0 显式指定
#   - LEVEL 不是 patch/minor/major → 退 2
#
# stdout 只输出版本号(带 v 前缀),给 Makefile 用 $$(...)  捕获。错误信息进 stderr。
set -euo pipefail

LEVEL="${1:-}"
case "$LEVEL" in
    patch|minor|major) ;;
    *)
        echo "❌ usage: bump.sh patch|minor|major" >&2
        exit 2
        ;;
esac

last=$(git describe --tags --abbrev=0 2>/dev/null || true)
if [ -z "$last" ]; then
    echo "❌ 仓库还没 tag,自动 bump 没有起点。请先用:" >&2
    echo "   make release-tag VERSION=v0.1.0" >&2
    exit 1
fi

# 严格 vX.Y.Z 格式校验(不接 -alpha / -rc 等 pre-release)
if ! echo "$last" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
    echo "❌ 上一 tag '$last' 不是 vX.Y.Z 严格格式,bump 拒绝(避免误判奇怪 tag)" >&2
    echo "   想从这个版本起步:make release-tag VERSION=vX.Y.Z 显式指定" >&2
    exit 1
fi

# 拆字段
ver="${last#v}"             # 去 v 前缀
IFS='.' read -r major minor patch <<< "$ver"

case "$LEVEL" in
    patch) patch=$((patch + 1)) ;;
    minor) minor=$((minor + 1)); patch=0 ;;
    major) major=$((major + 1)); minor=0; patch=0 ;;
esac

next="v${major}.${minor}.${patch}"

# 防 race:next 已存在(有人手工打过这版本)→ 拒绝
if git rev-parse --verify --quiet "$next" >/dev/null; then
    echo "❌ 计算出的下一版本 $next 已存在,refuse" >&2
    echo "   实际场景:你手动打过 $next 了,或上次 bump 没 push 也没删" >&2
    exit 1
fi

echo "$next"
