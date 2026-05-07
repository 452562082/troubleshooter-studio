#!/usr/bin/env bash
# 把已 build 的 .app bundle 打成可分发 .dmg(macOS,系统自带 hdiutil + SetFile,不依赖 brew)。
# 由 Makefile 的 desktop-dmg target 调用。
#
# 产物 .dmg 双击挂载 → 用户拖 .app 到 Applications 文件夹软链 → 完成安装。
# 不签名不公证(项目本身没苹果开发者证书),用户首次启动仍需 Gatekeeper 放行。
#
# 卷标图标:从 .app 内 Contents/Resources/icon.icns 复用(package-macos.sh 已经把
# appicon.png 转好放进去),挂载后用户在 Finder 看到的就是工作台图标 — 不重复转。
#
# 需要的环境变量:
#   APP_BUNDLE   .app bundle 绝对/相对路径(如 dist/TroubleshooterStudio.app)
#   VOLUME_NAME  挂载后的卷标(用户在 Finder 看到的名字;不能含空格)
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
tmp_rw=$(mktemp -t tshoot-dmg-rw).dmg
attach_dev=""

# 失败 / 退出时统一清理:detach 还挂着的 device + 删 staging + 删中间 RW dmg
cleanup() {
  if [[ -n "$attach_dev" ]]; then
    hdiutil detach "$attach_dev" -quiet 2>/dev/null || hdiutil detach "$attach_dev" -force -quiet 2>/dev/null || true
  fi
  rm -rf "$staging" "$tmp_rw"
}
trap cleanup EXIT

cp -R "$APP_BUNDLE" "$staging/"
ln -s /Applications "$staging/Applications"

# 老 dmg 删了再生成(hdiutil create 不会覆盖)
rm -f "$DMG_OUT"
mkdir -p "$(dirname "$DMG_OUT")"

icns_src="$APP_BUNDLE/Contents/Resources/icon.icns"
have_icon=0
if [[ -f "$icns_src" ]] && command -v SetFile >/dev/null 2>&1; then
  have_icon=1
fi

if [[ $have_icon -eq 1 ]]; then
  # 两步走:1) 出 RW dmg 挂载 → 放 .VolumeIcon.icns + SetFile -a C 标记
  #         2) detach → convert 成压缩 UDZO 出最终产物
  # 单步 hdiutil create -format UDZO 没法塞 volume icon(不可写)。
  hdiutil create \
    -volname "$VOLUME_NAME" \
    -srcfolder "$staging" \
    -ov -format UDRW -fs HFS+ \
    "$tmp_rw" >/dev/null

  attach_dev=$(hdiutil attach -readwrite -noverify -nobrowse "$tmp_rw" | awk '/^\/dev\// {print $1; exit}')
  if [[ -z "$attach_dev" ]]; then
    echo "✗ 挂载 RW dmg 失败" >&2
    exit 1
  fi

  cp "$icns_src" "/Volumes/$VOLUME_NAME/.VolumeIcon.icns"
  SetFile -a C "/Volumes/$VOLUME_NAME"

  # 显式 detach,然后清空 attach_dev 避免 trap 再 detach 一次
  hdiutil detach "$attach_dev" -quiet
  attach_dev=""

  hdiutil convert "$tmp_rw" -format UDZO -o "$DMG_OUT" >/dev/null
else
  # 没 icon 走简单路径(单步 UDZO)
  echo "[warn] 找不到 $icns_src 或 SetFile,出无图标 dmg" >&2
  hdiutil create \
    -volname "$VOLUME_NAME" \
    -srcfolder "$staging" \
    -ov -format UDZO -fs HFS+ \
    "$DMG_OUT" >/dev/null
fi

size=$(du -h "$DMG_OUT" | cut -f1)
echo "✓ $DMG_OUT($size)"
echo "  分发: 双击 .dmg → 拖 .app 到 Applications → Launchpad / Spotlight 搜启动"
echo "  首次启动 Gatekeeper 拦截:右键 .app → 打开 → 确认放行"
