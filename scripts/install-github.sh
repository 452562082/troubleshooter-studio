#!/usr/bin/env bash
# TroubleshooterStudio macOS 一键安装脚本(GitHub Release 版)。
# 对偶 install.sh(GitLab 版),区别只在前两步:查最新 release tag + 下 dmg.zip URL。
# 解压 / 挂载 / 拷贝 / 清 quarantine / 启动 全部复用 macOS 标准流程。
#
# 用户体验:复制粘贴一行命令,自动下最新 release dmg → 解压 → 装到 /Applications/ →
# xattr 清 quarantine → open .app 启动。**全程无 Gatekeeper 拦截**(因为 curl/bash/
# xattr/open 都是 macOS 自带签名工具,跑它们不被拦;.app 被 xattr 清掉 quarantine 后
# 也不被拦)。
#
# 用法:
#
#   curl -fsSL https://raw.githubusercontent.com/452562082/troubleshooter-studio/main/scripts/install-github.sh | bash
#
#   指定版本(否则装最新):
#     VERSION=v0.9.18 curl -fsSL https://raw.githubusercontent.com/452562082/troubleshooter-studio/main/scripts/install-github.sh | bash
#
#   私有仓库(非默认 — 默认仓是 public 的,无需 token):
#     export GITHUB_TOKEN=ghp_xxx   # Settings → Developer settings → Tokens(scope=repo)
#     curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" \
#       https://raw.githubusercontent.com/.../scripts/install-github.sh | bash
set -euo pipefail

# ── 配置(env 可覆盖)──────────────────────────────────────────
GITHUB_REPO="${GITHUB_REPO:-452562082/troubleshooter-studio}"
APP_NAME="${APP_NAME:-TroubleshooterStudio}"
VERSION="${VERSION:-}"     # 空 = 自动查最新 release tag
TARGET_DIR="${TARGET_DIR:-/Applications}"

API="https://api.github.com/repos/$GITHUB_REPO"

# fetch:跟 install.sh 同款封装,故意不加 -s(silent),让 --progress-bar 大文件能显示进度。
# 私有仓库 export GITHUB_TOKEN 后自动加 Bearer 鉴权头。
fetch() {
    if [[ -n "${GITHUB_TOKEN:-}" ]]; then
        curl --connect-timeout 30 --speed-time 60 --speed-limit 1024 \
             -H "Authorization: Bearer $GITHUB_TOKEN" \
             -H "Accept: application/vnd.github+json" \
             "$@"
    else
        curl --connect-timeout 30 --speed-time 60 --speed-limit 1024 \
             -H "Accept: application/vnd.github+json" \
             "$@"
    fi
}

# ── 平台校验 ────────────────────────────────────────────────────
if [[ "$(uname)" != "Darwin" ]]; then
    echo "✗ 本脚本仅支持 macOS(本机 $(uname))" >&2
    exit 1
fi

# ── 1. 找最新 release tag(VERSION 没指定时)──────────────────
# 用 github.com/.../releases/latest 的 web 302 redirect 法,**不走 api.github.com**
# (避开匿名用户 60 次/小时的 rate limit 短时间用 install 脚本测试会撞)。
# redirect 目标长这样:https://github.com/owner/repo/releases/tag/vX.Y.Z
# 取最后一段就是 tag。无 release 时 GitHub 返回 404 不 redirect。
if [[ -z "$VERSION" ]]; then
    echo "▶ 查询最新 release ..."
    final_url=$(curl -sLI -o /dev/null -w '%{url_effective}' \
        --connect-timeout 30 \
        "https://github.com/$GITHUB_REPO/releases/latest" 2>/dev/null || true)
    # final_url 形如 .../releases/tag/v0.9.21;basename 拿 tag
    if [[ "$final_url" == *"/releases/tag/"* ]]; then
        VERSION="${final_url##*/}"
        echo "  → 最新版本 $VERSION"
    else
        echo "✗ 拿不到 release(仓库可能还没发版,或私有仓需 GITHUB_TOKEN)" >&2
        echo "  浏览器手动查: https://github.com/$GITHUB_REPO/releases" >&2
        exit 1
    fi
else
    echo "▶ 安装指定版本 $VERSION"
fi

# ── 2. 下 dmg.zip(GitHub Releases asset 公开仓库无需 token)─────
ZIP_NAME="$APP_NAME-$VERSION.dmg.zip"
ZIP_URL="https://github.com/$GITHUB_REPO/releases/download/$VERSION/$ZIP_NAME"

tmp=$(mktemp -d)
trap '
  # 清理:卸挂载点 + 删 tmp
  if [[ -n "${mount_root:-}" ]]; then
    hdiutil detach "$mount_root" -quiet 2>/dev/null || hdiutil detach "$mount_root" -force -quiet 2>/dev/null || true
  fi
  rm -rf "$tmp"
' EXIT

zip_path="$tmp/$ZIP_NAME"
echo ""
echo "▶ 下载 $ZIP_NAME ..."
# -L 跟 GitHub release asset 的 302 redirect 到 objects.githubusercontent.com
if ! fetch -L --progress-bar -o "$zip_path" "$ZIP_URL"; then
    echo "✗ 下载失败 — 检查网络 / release 是否存在 dmg.zip 资产 / 私有仓库需 token" >&2
    exit 1
fi
echo "  → $(du -h "$zip_path" | cut -f1)"

# ── 3. 解压(ditto -x 保留 xattr,跟 macOS Archive Utility 行为一致)──
echo ""
echo "▶ 解压 ..."
ditto -x -k --rsrc "$zip_path" "$tmp"
dmg_path=$(find "$tmp" -name "*.dmg" -type f -not -name "._*" | head -1)
if [[ -z "$dmg_path" ]]; then
    echo "✗ zip 里找不到 .dmg" >&2
    exit 1
fi

# ── 4. 挂载 dmg(只读)─────────────────────────────────────────
echo ""
echo "▶ 挂载 dmg ..."
mount_root=$(hdiutil attach -nobrowse -noverify -mountrandom "$tmp" "$dmg_path" 2>/dev/null \
    | awk '/^\/dev\// {root=$NF} END {print root}')
if [[ -z "$mount_root" || ! -d "$mount_root" ]]; then
    echo "✗ 挂载失败" >&2
    exit 1
fi

src_app="$mount_root/$APP_NAME.app"
if [[ ! -d "$src_app" ]]; then
    echo "✗ dmg 里没找到 $APP_NAME.app" >&2
    exit 1
fi

# ── 5. 安装到 /Applications/ ──────────────────────────────────
target_app="$TARGET_DIR/$APP_NAME.app"
if [[ -d "$target_app" ]]; then
    echo "▶ 已存在旧版,移到废纸篓做备份 ..."
    trash="$HOME/.Trash/$APP_NAME.app.$(date +%Y%m%d-%H%M%S)"
    mv "$target_app" "$trash" 2>/dev/null || {
        echo "✗ 移旧版失败,可能 /Applications 没写入权限。换 sudo 再试" >&2
        exit 1
    }
fi

echo "▶ 拷贝 .app 到 $TARGET_DIR/ ..."
cp -R "$src_app" "$target_app"

# ── 6. 卸载 dmg + 清 quarantine xattr ─────────────────────────
hdiutil detach "$mount_root" -quiet
mount_root=""

echo "▶ 清 quarantine xattr ..."
xattr -dr com.apple.quarantine "$target_app" 2>/dev/null || true

# 刷新 LaunchServices 缓存,让 Gatekeeper 重新评估(.app 已无 quarantine → 直接放行)
LSREGISTER=/System/Library/Frameworks/CoreServices.framework/Versions/A/Frameworks/LaunchServices.framework/Versions/A/Support/lsregister
[[ -x "$LSREGISTER" ]] && "$LSREGISTER" -f "$target_app" 2>/dev/null || true

# ── 7. 启动 .app ──────────────────────────────────────────────
echo ""
echo "✓ 安装完成 — $target_app"
echo "▶ 启动 $APP_NAME ..."
open "$target_app"

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "  完成!以后双击 .app 直接开,不会再有 Gatekeeper 拦截。"
echo "═══════════════════════════════════════════════════════════════"
