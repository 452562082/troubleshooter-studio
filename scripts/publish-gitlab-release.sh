#!/usr/bin/env bash
# 把本地 build 好的 dmg + 跨平台 CLI 二进制上传到 GitLab Release(供 tag 页下载)。
#
# 流程:
#   1) 校 tag 已 push 到 GitLab(没 push 的 tag 创建 release 会失败)
#   2) 收 dist/ 下产物清单(dmg + CLI binaries)
#   3) 同名 release 已存在 → 删了重建(force overwrite,简化幂等)
#   4) 每个产物 POST 到 /api/v4/projects/<id>/uploads,拿回相对 URL
#   5) POST /api/v4/projects/<id>/releases,把上传 URL 挂到 release assets.links
#
# 用法:
#   GITLAB_TOKEN=<token> VERSION=v0.1.0 bash scripts/publish-gitlab-release.sh
# 或通过 Makefile:
#   make release-publish VERSION=v0.1.0
#
# 必需 env:
#   GITLAB_TOKEN  GitLab Personal Access Token,scope=api(Settings → Access Tokens)
#   VERSION       要发布的 tag 名(如 v0.1.0),必须已 git push 过
#
# 可选 env:
#   GITLAB_HOST   默认 https://gitlab.quguazhan.com
#   PROJECT_PATH  默认 xiaolong/troubleshooter-studio
#   RELEASE_NOTES 默认自动生成"<version> 自动发布"
#
# 依赖:
#   curl  macOS 自带
#   jq    解析 GitLab 返回的 JSON(brew install jq;不装的话脚本会先提示)
set -euo pipefail

: "${GITLAB_TOKEN:?需要 env GITLAB_TOKEN(GitLab → Preferences → Access Tokens,scope=api)}"
: "${VERSION:?需要 env VERSION(如 v0.1.0)}"

GITLAB_HOST="${GITLAB_HOST:-https://gitlab.quguazhan.com}"
PROJECT_PATH="${PROJECT_PATH:-xiaolong/troubleshooter-studio}"
RELEASE_NOTES="${RELEASE_NOTES:-$VERSION 自动发布(make release-publish)}"

if ! command -v jq >/dev/null 2>&1; then
  echo "✗ 缺 jq:brew install jq" >&2
  exit 1
fi

# URL-encode "/" → "%2F" 做项目 ID
PROJECT_ENC=$(printf %s "$PROJECT_PATH" | sed 's|/|%2F|g')
API="$GITLAB_HOST/api/v4/projects/$PROJECT_ENC"

# curl 公共 flag:静默 + 失败时打印 stderr + 跟随 redirect
CURL=(curl -sS -L --fail-with-body -H "PRIVATE-TOKEN: $GITLAB_TOKEN")

echo "▶ 项目: $PROJECT_PATH"
echo "▶ host: $GITLAB_HOST"
echo "▶ tag : $VERSION"

# 1) 校 tag 已存在
echo "[1/5] 校验 GitLab tag $VERSION 已 push..."
http_code=$(curl -sS -o /dev/null -w '%{http_code}' \
    -H "PRIVATE-TOKEN: $GITLAB_TOKEN" \
    "$API/repository/tags/$VERSION")
if [[ "$http_code" != "200" ]]; then
  echo "✗ GitLab 上 tag $VERSION 不存在(HTTP $http_code)。先 git push 这个 tag 再跑。" >&2
  exit 1
fi
echo "  ✓ tag 存在"

# 2) 收 dist/ 下产物清单
echo "[2/5] 扫描 dist/ 产物..."
ASSETS=()
dmg="dist/TroubleshooterStudio-$VERSION.dmg"
if [[ -f "$dmg" ]]; then
  ASSETS+=("$dmg")
else
  echo "  [warn] 找不到 $dmg(没跑 make desktop-dmg?)" >&2
fi
# CLI 跨平台二进制(make release 出到 dist/bin/tshoot-<VERSION>-<os>-<arch>)
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

# 3) 老 release 在的话先删(简化幂等;真要保留旧 release asset 改成"追加"即可)
echo "[3/5] 检查同名 Release 是否已存在..."
http_code=$(curl -sS -o /dev/null -w '%{http_code}' \
    -H "PRIVATE-TOKEN: $GITLAB_TOKEN" \
    "$API/releases/$VERSION")
if [[ "$http_code" == "200" ]]; then
  echo "  Release $VERSION 已存在,删除重建..."
  "${CURL[@]}" -X DELETE "$API/releases/$VERSION" >/dev/null
elif [[ "$http_code" != "404" ]]; then
  echo "✗ 检查 release 状态失败(HTTP $http_code)" >&2
  exit 1
fi

# 4) 每个 asset POST 到 /uploads,拿回 relative URL
echo "[4/5] 上传 ${#ASSETS[@]} 个产物到 /uploads ..."
LINKS=()
for f in "${ASSETS[@]}"; do
  fname=$(basename "$f")
  printf '  ↑ %s ... ' "$fname"
  resp=$("${CURL[@]}" -F "file=@$f" "$API/uploads")
  rel_url=$(printf %s "$resp" | jq -r '.url')
  if [[ -z "$rel_url" || "$rel_url" == "null" ]]; then
    echo "✗ 上传失败,响应:$resp" >&2
    exit 1
  fi
  full_url="$GITLAB_HOST/$PROJECT_PATH$rel_url"
  echo "✓"
  LINKS+=("{\"name\":\"$fname\",\"url\":\"$full_url\",\"link_type\":\"package\"}")
done

# 5) 创建 Release(挂 asset.links)
echo "[5/5] 创建 Release $VERSION ..."
links_csv=$(IFS=,; echo "${LINKS[*]}")
release_payload=$(jq -n \
    --arg tag "$VERSION" \
    --arg desc "$RELEASE_NOTES" \
    --argjson links "[$links_csv]" \
    '{tag_name: $tag, description: $desc, assets: {links: $links}}')

"${CURL[@]}" -X POST \
    -H "Content-Type: application/json" \
    -d "$release_payload" \
    "$API/releases" >/dev/null

echo "✓ Release $VERSION 已发布"
echo "  $GITLAB_HOST/$PROJECT_PATH/-/releases/$VERSION"
