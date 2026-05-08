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
RELEASE_NOTES="${RELEASE_NOTES:-$(cat <<NOTES
$VERSION 自动发布(make release-publish)。

## ⚠️ macOS 显示"已损坏"怎么办

**这不是真损坏**,是因为本应用未做苹果数字签名(没花 \$99/年办 Apple Developer Account),macOS 14+ 把"未签名 + 来自互联网"统一报"已损坏"。**dmg 里自带"一键解锁.command",双击即可放行**(详见下方"安装"步骤)。

## 下载与安装

**macOS 桌面 app**:
1. 下载 \`TroubleshooterStudio-$VERSION.dmg.zip\`
2. **双击解压**(必须用 macOS 自带 Archive Utility,第三方解压可能丢图标)
3. 双击解出来的 \`.dmg\` → Finder 弹安装窗口
4. 拖 \`.app\` 到右边 \`Applications\`
5. 第一次打开如果报"已损坏":**双击 dmg 里的 "2️⃣ 双击解锁(可能要点两次).command"** → Terminal 自动跑解锁 + 启动应用。**macOS 15+ 对未签名脚本会拦截一次**,如果首次双击 Terminal 显示 "killed",再双击一次就通(系统行为,无害)

**CLI(macOS / Linux / Windows)**:
- 按平台选 \`tshoot-$VERSION-<os>-<arch>\` 下载
- macOS / Linux:\`chmod +x tshoot-...\` + 拷到 \`/usr/local/bin/tshoot\`
- 跑 \`tshoot --help\` 看子命令
NOTES
)}"

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
echo "[2/5] 扫描 dist/ 产物 + 准备 zip 包装..."
ASSETS=()
dmg="dist/TroubleshooterStudio-$VERSION.dmg"
if [[ -f "$dmg" ]]; then
  # dmg 直接走 HTTP 上传 / 下载会丢 macOS xattr(com.apple.ResourceFork +
  # FinderInfo),导致 dmg 文件本身的 Finder icon 在用户 Downloads 里变成默认
  # disk image 图标。用 ditto -c -k --keepParent --rsrc 打成 zip,xattr 嵌进 zip;
  # 用户用 macOS Archive Utility(双击 zip)解压时自动恢复 xattr → dmg 文件
  # 带回 robot 图标。
  # 行业标准做法:VS Code / Bitwarden 等很多 mac app 都用 zip 包 dmg 分发。
  dmg_zip="$dmg.zip"
  echo "  ▶ 用 ditto 把 $dmg 打成 $dmg_zip(保留 xattr,解压后图标恢复)"
  rm -f "$dmg_zip"
  # cd 进 dmg 同级 + 只传 basename,zip 内部就只剩 dmg 文件本身,无 dist/ 子目录
  # (--keepParent 会保留输入路径的父目录段,不是我们想要的)
  ( cd "$(dirname "$dmg")" && ditto -c -k --rsrc "$(basename "$dmg")" "$(basename "$dmg_zip")" )
  ASSETS+=("$dmg_zip")
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

# 4) 每个 asset PUT 到 Generic Packages API。
#    /projects/:id/uploads 是 markdown 附件,私有项目下载要 session 鉴权 → 容易 404;
#    Generic Packages 是 GitLab 为 release asset 设计的标准端点,URL 永久可读,
#    release 页"Download asset"按钮直接 work。
#    package_name 用项目 short name,package_version 用 tag(去掉前缀 v)。
PKG_NAME="${PROJECT_PATH##*/}"      # xiaolong/troubleshooter-studio → troubleshooter-studio
PKG_VER="${VERSION#v}"               # v0.1.0 → 0.1.0(generic packages 不接受 v 前缀)
echo "[4/5] 上传 ${#ASSETS[@]} 个产物到 Generic Packages($PKG_NAME / $PKG_VER)..."
LINKS=()
for f in "${ASSETS[@]}"; do
  fname=$(basename "$f")
  printf '  ↑ %s ... ' "$fname"
  upload_url="$API/packages/generic/$PKG_NAME/$PKG_VER/$fname"
  http_code=$(curl -sS -L -X PUT \
      -H "PRIVATE-TOKEN: $GITLAB_TOKEN" \
      --upload-file "$f" \
      -o /dev/null -w '%{http_code}' \
      "$upload_url")
  case "$http_code" in
    200|201) echo "✓" ;;
    409)
      # 撞同名 package file。GitLab 默认 generic_packages.duplicates_allowed=false,
      # 同 (package_name, version, file_name) 不允许重传。release 重发场景必撞,
      # 自动找到旧 file 删了再 PUT,免去用户手动到 Settings 开 Allow duplicates。
      echo -n "(同名已存在,删旧 file 重传) "
      pkg_id=$(curl -sS -L \
          -H "PRIVATE-TOKEN: $GITLAB_TOKEN" \
          --data-urlencode "package_name=$PKG_NAME" \
          --data-urlencode "package_version=$PKG_VER" \
          -G "$API/packages" \
          | jq -r '.[0].id // empty')
      if [[ -z "$pkg_id" ]]; then
        echo "✗ 找不到旧 package id 自动删除失败" >&2
        exit 1
      fi
      file_id=$(curl -sS -L \
          -H "PRIVATE-TOKEN: $GITLAB_TOKEN" \
          "$API/packages/$pkg_id/package_files" \
          | jq -r --arg n "$fname" '.[] | select(.file_name==$n) | .id' \
          | head -1)
      if [[ -n "$file_id" ]]; then
        curl -sS -L -X DELETE \
            -H "PRIVATE-TOKEN: $GITLAB_TOKEN" \
            "$API/packages/$pkg_id/package_files/$file_id" >/dev/null
      fi
      # 重 PUT
      http_code=$(curl -sS -L -X PUT \
          -H "PRIVATE-TOKEN: $GITLAB_TOKEN" \
          --upload-file "$f" \
          -o /dev/null -w '%{http_code}' \
          "$upload_url")
      if [[ "$http_code" == "200" || "$http_code" == "201" ]]; then
        echo "✓"
      else
        echo "✗ 删旧 file 后重传仍失败 HTTP $http_code" >&2
        exit 1
      fi
      ;;
    *)
      echo "✗ 上传失败 HTTP $http_code" >&2
      exit 1
      ;;
  esac
  echo "✓"
  LINKS+=("{\"name\":\"$fname\",\"url\":\"$upload_url\",\"link_type\":\"package\"}")
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
