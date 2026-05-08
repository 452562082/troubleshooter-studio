#!/usr/bin/env bash
# TroubleshooterStudio macOS 一键安装脚本(curl | bash 入口)。
#
# 用户体验:复制粘贴一行命令,自动下最新 release dmg → 解压 → 装到 /Applications/ →
# xattr 清 quarantine → open .app 启动。**全程无 Gatekeeper 拦截**(因为 curl/bash/
# xattr/open 都是 macOS 自带签名工具,跑它们不被拦;.app 被 xattr 清掉 quarantine 后
# 也不被拦)。
#
# 跟双击 .command 路径区别:
#   - .command 第一次双击被 macOS Gatekeeper SIGKILL(系统拦未签名脚本)
#   - 而 curl | bash:bash 是签名 shell,跑 install.sh 不被拦,一次成功
#
# 用法:
#
#   公开 GitLab 项目:
#     curl -fsSL https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/raw/main/scripts/install.sh | bash
#
#   私有 GitLab 项目:
#     export GITLAB_TOKEN=glpat-xxx   # GitLab Settings → Access Tokens(scope=read_api)
#     curl -fsSL -H "PRIVATE-TOKEN: $GITLAB_TOKEN" \
#       https://gitlab.quguazhan.com/xiaolong/troubleshooter-studio/-/raw/main/scripts/install.sh \
#       | bash
#
#   指定版本(否则装最新):
#     VERSION=v0.1.5 curl ... | bash
set -euo pipefail

# ── 配置(env 可覆盖)──────────────────────────────────────────
GITLAB_HOST="${GITLAB_HOST:-https://gitlab.quguazhan.com}"
PROJECT_PATH="${PROJECT_PATH:-xiaolong/troubleshooter-studio}"
APP_NAME="${APP_NAME:-TroubleshooterStudio}"
VERSION="${VERSION:-}"     # 空 = 自动查最新 release tag
TARGET_DIR="${TARGET_DIR:-/Applications}"

PROJECT_ENC=$(printf %s "$PROJECT_PATH" | sed 's|/|%2F|g')
API="$GITLAB_HOST/api/v4/projects/$PROJECT_ENC"

# fetch 函数封装 curl + 可选鉴权头。封装是因为:
#  - macOS 自带 bash 3.2(Apple 没升级);set -u 下 ${empty_array[@]} 报 unbound
#  - 用函数避免到处判断 token + 数组展开兼容性问题
#  - 公开项目零 token 直接调,私有项目 export GITLAB_TOKEN 后透明加 -H
fetch() {
    if [[ -n "${GITLAB_TOKEN:-}" ]]; then
        curl -fsSL -H "PRIVATE-TOKEN: $GITLAB_TOKEN" "$@"
    else
        curl -fsSL "$@"
    fi
}

echo ""
echo "═══════════════════════════════════════════════════════════════"
echo "  $APP_NAME 一键安装(curl | bash 模式,无 Gatekeeper 拦截)"
echo "═══════════════════════════════════════════════════════════════"
echo ""

# ── 平台校验 ────────────────────────────────────────────────────
if [[ "$(uname)" != "Darwin" ]]; then
    echo "✗ 本脚本仅支持 macOS(本机 $(uname))" >&2
    exit 1
fi

# ── 1. 找最新 release tag(VERSION 没指定时)──────────────────
if [[ -z "$VERSION" ]]; then
    echo "▶ 查询最新 release ..."
    resp=$(fetch "$API/releases?per_page=1" 2>/dev/null || true)
    if [[ -z "$resp" || "$resp" == "[]" ]]; then
        echo "✗ 拿不到 release 列表(项目可能私有,设 GITLAB_TOKEN env 后重跑)" >&2
        echo "  GitLab → Preferences → Access Tokens → 创建 scope=read_api 的 token" >&2
        exit 1
    fi
    VERSION=$(printf %s "$resp" | python3 -c "import json,sys; d=json.load(sys.stdin); print(d[0]['tag_name'])" 2>/dev/null || true)
    if [[ -z "$VERSION" ]]; then
        echo "✗ 解析 release 失败" >&2
        exit 1
    fi
    echo "  → 最新版本 $VERSION"
else
    echo "▶ 安装指定版本 $VERSION"
fi

# ── 2. 下 dmg.zip ──────────────────────────────────────────────
PKG_NAME="${PROJECT_PATH##*/}"
PKG_VER="${VERSION#v}"
ZIP_NAME="$APP_NAME-$VERSION.dmg.zip"
ZIP_URL="$API/packages/generic/$PKG_NAME/$PKG_VER/$ZIP_NAME"

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
if ! fetch --progress-bar -o "$zip_path" "$ZIP_URL"; then
    echo "✗ 下载失败 — 检查网络 / 项目权限 / token" >&2
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

# ── 4. 挂载 dmg(只读) ─────────────────────────────────────────
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
