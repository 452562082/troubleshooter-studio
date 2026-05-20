#!/usr/bin/env bash
# 把本地 build 好的 dmg + 跨平台 CLI 二进制上传到 GitHub Release(供 tag 页下载)。
# 跟 publish-gitlab-release.sh 对偶,GitHub 这边走 gh CLI(GitHub-hosted runner 自带)。
#
# 流程:
#   1) 校 tag 已 push 到 GitHub(没 push 的 tag,create release 会失败)
#   2) 收 dist/ 下产物清单(dmg.zip + CLI binaries)
#   3) 同名 release 已存在 → 删了重建(force overwrite,简化幂等;跟 GitLab 版同策略)
#   4) gh release create + gh release upload
#
# 用法:
#   GITHUB_TOKEN=<token> VERSION=v0.1.0 bash scripts/publish-github-release.sh
# 或通过 Makefile:
#   make release-publish-github VERSION=v0.1.0
#
# 必需 env:
#   GITHUB_TOKEN  GitHub Actions 自动注入,本地需 Personal Access Token (scope=repo)
#   VERSION       要发布的 tag 名(如 v0.1.0),必须已 git push 过
#
# 可选 env:
#   GITHUB_REPO   默认 452562082/troubleshooter-studio
#   RELEASE_NOTES 默认自动生成(scripts/changelog.sh 输出 + README 链接)
#
# 依赖:
#   gh   GitHub CLI,macos-latest / ubuntu-latest runner 自带(本地 brew install gh)
set -euo pipefail

: "${GITHUB_TOKEN:?需要 env GITHUB_TOKEN(GitHub Settings → Developer settings → Tokens,scope=repo)}"
: "${VERSION:?需要 env VERSION(如 v0.1.0)}"

GITHUB_REPO="${GITHUB_REPO:-452562082/troubleshooter-studio}"

if ! command -v gh >/dev/null 2>&1; then
  echo "✗ 缺 gh CLI:brew install gh(macOS)/ apt install gh(Ubuntu)" >&2
  exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# 跟 GitLab 版同源:本版 changelog(commit subject 自动归集)
CHANGELOG_BODY="$("$SCRIPT_DIR/changelog.sh" "$VERSION" 2>/dev/null || echo "$VERSION 发布")"
RELEASE_NOTES="${RELEASE_NOTES:-$(cat <<NOTES
$CHANGELOG_BODY

---

📦 **下载安装**(macOS dmg / 跨平台 CLI)+ macOS "已损坏" 解决 → 见 [README](https://github.com/${GITHUB_REPO}/blob/main/README.md#下载与安装)
NOTES
)}"

echo "▶ 仓库: $GITHUB_REPO"
echo "▶ tag : $VERSION"

# 1) 校 tag 已存在(gh api 比 git ls-remote 更直接,且只用 GITHUB_TOKEN)
echo "[1/4] 校验 GitHub tag $VERSION 已 push..."
if ! gh api "repos/$GITHUB_REPO/git/refs/tags/$VERSION" -q '.ref' >/dev/null 2>&1; then
  echo "✗ GitHub 上 tag $VERSION 不存在。先 git push 这个 tag 再跑。" >&2
  exit 1
fi
echo "  ✓ tag 存在"

# 2) 收 dist/ 下产物清单
echo "[2/4] 扫描 dist/ 产物 + 准备 zip 包装..."
ASSETS=()
dmg="dist/TroubleshooterStudio-$VERSION.dmg"
if [[ -f "$dmg" ]]; then
  # dmg 直接 HTTP 上传/下载会丢 macOS xattr → Finder 图标变成默认 disk image 图标。
  # ditto -c -k --rsrc 打成 zip,xattr 嵌进 zip;用户双击 zip 解压会自动恢复 xattr。
  # 跟 GitLab 版同策略。
  dmg_zip="$dmg.zip"
  echo "  ▶ 用 ditto 把 $dmg 打成 $dmg_zip(保留 xattr,解压后图标恢复)"
  rm -f "$dmg_zip"
  ( cd "$(dirname "$dmg")" && ditto -c -k --rsrc "$(basename "$dmg")" "$(basename "$dmg_zip")" )
  ASSETS+=("$dmg_zip")
else
  echo "  [warn] 找不到 $dmg(没跑 make desktop-dmg?)" >&2
fi
# CLI 跨平台二进制
shopt -s nullglob
for f in dist/bin/tshoot-"$VERSION"-*; do
  ASSETS+=("$f")
done
shopt -u nullglob

if [[ ${#ASSETS[@]} -eq 0 ]]; then
  echo "✗ dist/ 没找到任何 $VERSION 产物。先跑 make desktop-dmg + make release。" >&2
  exit 1
fi
echo "  ✓ 待上传 ${#ASSETS[@]} 个产物:"
for a in "${ASSETS[@]}"; do
  size=$(du -h "$a" | cut -f1)
  echo "    - $a($size)"
done

# 3) 老 release 在的话先删(幂等)。gh release view 不存在时 exit 1,吞掉。
echo "[3/4] 检查同名 Release 是否已存在..."
if gh release view "$VERSION" --repo "$GITHUB_REPO" >/dev/null 2>&1; then
  echo "  Release $VERSION 已存在,删除重建..."
  # --cleanup-tag=false:tag 是上一步 release.sh push 上去的,删 release 时别动 tag
  gh release delete "$VERSION" --repo "$GITHUB_REPO" --yes --cleanup-tag=false
fi

# 4) 创建 Release + 上传 assets
# --notes-file 走 stdin 避免 shell 引号转义灾难
echo "[4/4] 创建 Release $VERSION 并上传 ${#ASSETS[@]} 个产物..."
echo "$RELEASE_NOTES" | gh release create "$VERSION" \
  --repo "$GITHUB_REPO" \
  --title "$VERSION" \
  --notes-file - \
  "${ASSETS[@]}"

echo "✓ Release $VERSION 已发布"
echo "  https://github.com/$GITHUB_REPO/releases/tag/$VERSION"
