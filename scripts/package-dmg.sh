#!/usr/bin/env bash
# 把已 build 的 .app bundle 打成可分发 .dmg(macOS,系统自带 hdiutil,不依赖 brew)。
# 由 Makefile 的 desktop-dmg target 调用。
#
# 产物 .dmg 双击挂载 → 用户拖 .app 到 Applications 文件夹软链 → 完成安装。
# 不签名不公证(项目本身没苹果开发者证书),用户首次启动仍需 Gatekeeper 放行。
#
# 需要的环境变量:
#   APP_BUNDLE   .app bundle 绝对/相对路径(如 dist/TroubleshooterStudio.app)
#   VOLUME_NAME  挂载后的卷标(用户在 Finder 看到的名字)
#   DMG_OUT      输出 .dmg 路径(如 dist/TroubleshooterStudio-v0.1.0.dmg)
set -euo pipefail

: "${APP_BUNDLE:?APP_BUNDLE required}"
: "${VOLUME_NAME:?VOLUME_NAME required}"
: "${DMG_OUT:?DMG_OUT required}"

if [[ ! -d "$APP_BUNDLE" ]]; then
  echo "✗ APP_BUNDLE 不存在:$APP_BUNDLE(先跑 make desktop-app)" >&2
  exit 1
fi

# 临时 staging:.app + Applications 软链(让用户在 dmg 窗口里直接拖到右边的快捷方式)
staging=$(mktemp -d)
trap 'rm -rf "$staging"' EXIT

cp -R "$APP_BUNDLE" "$staging/"
ln -s /Applications "$staging/Applications"

# 老 dmg 删了再生成 — hdiutil create 不会覆盖
rm -f "$DMG_OUT"
mkdir -p "$(dirname "$DMG_OUT")"

# UDZO = compressed read-only(分发标准格式);-fs HFS+ 兼容老系统
hdiutil create \
  -volname "$VOLUME_NAME" \
  -srcfolder "$staging" \
  -ov \
  -format UDZO \
  -fs HFS+ \
  "$DMG_OUT" >/dev/null

size=$(du -h "$DMG_OUT" | cut -f1)
echo "✓ $DMG_OUT($size)"
echo "  分发: 双击 .dmg → 拖 .app 到 Applications → Launchpad / Spotlight 搜启动"
echo "  首次启动 Gatekeeper 拦截:右键 .app → 打开 → 确认放行"
