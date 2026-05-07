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

# 推 .app 在挂载窗口里的文件名(AppleScript 用)。基本就是 APP_BUNDLE 的 basename。
APP_NAME_IN_DMG=$(basename "$APP_BUNDLE")

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

# 防卡:之前失败 build 可能留下 /Volumes/<VOLUME_NAME> 的 stale 挂载(典型 readonly,
# 因为是 dmg attach 失败的残骸)。新挂同名卷时,macOS 优先选第一份 → 后续 AppleScript
# tell disk "<VOLUME_NAME>" 拿到的是 stale readonly 卷,导致布局写错地方 + .VolumeIcon
# 之类的文件丢。开 build 前一律 force-detach 所有同名挂载(含 macOS 自动加的 " 1" / " 2"
# 后缀变体)。
mount | sed -nE "s|^[^ ]+ on (/Volumes/$VOLUME_NAME( [0-9]+)?) .*|\\1|p" | while IFS= read -r vol_path; do
  hdiutil detach "$vol_path" -force -quiet 2>/dev/null || true
done

# 老 dmg 删了再生成(hdiutil create 不会覆盖)
rm -f "$DMG_OUT"
mkdir -p "$(dirname "$DMG_OUT")"

icns_src="$APP_BUNDLE/Contents/Resources/icon.icns"
have_icon=0
if [[ -f "$icns_src" ]] && command -v SetFile >/dev/null 2>&1; then
  have_icon=1
  # **不**在这里把 .VolumeIcon.icns 放 staging —— AppleScript 配窗口时 Finder 会重新
  # enumerate 卷把 dotfile 当垃圾清掉(实测!.VolumeIcon.icns 永远在 update without
  # registering applications 之后消失)。改成两次 attach 流程:
  #   1) RW attach → AppleScript 配 .DS_Store 窗口布局 → detach
  #   2) 重新 RW attach → cp .VolumeIcon.icns + SetFile -a C → detach
  # 第二次 attach 不开 Finder,纯 fs 操作,Finder 不来"清理"。
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

  # 第一次 attach 的活只有一件:用 AppleScript 写 .DS_Store 配窗口布局。
  # **不要**在这次 attach 期间放 .VolumeIcon.icns —— Finder enumerate 会清掉它。
  #
  # 用 AppleScript 跟 Finder 通信配置挂载窗口的视觉布局:
  #   - 窗口 600×400 居中
  #   - icon view + icon size 128
  #   - 隐藏 toolbar / statusbar(让窗口干净像安装界面,不像普通文件浏览)
  #   - .app 摆左边(150,170)、Applications 摆右边(450,170),中间留拖拽距离
  # Finder 把这套配置写到 /Volumes/<vol>/.DS_Store,convert 时被打进 dmg。
  # 后续用户每次挂载 dmg,Finder 都用 .DS_Store 里的布局开窗口 — 标准市面安装体验。
  #
  # 失败容错:AppleScript 在某些 sandbox / 自动化禁用环境下会拒,失败不阻塞 build,
  # 用户拿到的 dmg 仍可用(只是布局退化到 Finder 默认网格)。
  osascript >/dev/null 2>&1 <<APPLESCRIPT || echo "  [warn] AppleScript 配置 Finder 窗口失败,dmg 布局退化到默认(应用仍可装,只是窗口不漂亮)" >&2
tell application "Finder"
  tell disk "$VOLUME_NAME"
    open
    set current view of container window to icon view
    set toolbar visible of container window to false
    set statusbar visible of container window to false
    set the bounds of container window to {200, 200, 800, 600}
    set viewOptions to the icon view options of container window
    set arrangement of viewOptions to not arranged
    set icon size of viewOptions to 128
    set position of item "$APP_NAME_IN_DMG" of container window to {150, 170}
    set position of item "Applications" of container window to {450, 170}
    update without registering applications
    delay 1
    close
  end tell
end tell
APPLESCRIPT

  # 给 Finder 写 .DS_Store 留点 buffer(它是异步)
  sync
  sleep 1

  # detach 第一次 attach,Finder 失去对这个卷的"所有权"
  hdiutil detach "$attach_dev" -quiet
  attach_dev=""

  # 第二次 attach:这次纯 fs 操作放 .VolumeIcon.icns + SetFile,不开 Finder
  attach_dev=$(hdiutil attach -readwrite -noverify -nobrowse "$tmp_rw" | awk '/^\/dev\// {print $1; exit}')
  if [[ -z "$attach_dev" ]]; then
    echo "✗ 第二次挂载 RW dmg(放 volume icon 用)失败" >&2
    exit 1
  fi
  cp "$icns_src" "/Volumes/$VOLUME_NAME/.VolumeIcon.icns"
  SetFile -a C "/Volumes/$VOLUME_NAME"
  sync
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

# 给 .dmg 文件本身设 Finder custom icon —— 用户在 Downloads 看到 dmg 文件就是工作台图标,
# 不是 macOS 默认的"白色文档+下载箭头"通用 disk image 图标。
# 用 macOS 自带的 swift 调 NSWorkspace.setIcon(),不引入 brew 依赖(swift 在 Xcode CLT 里)。
if [[ $have_icon -eq 1 ]] && command -v swift >/dev/null 2>&1; then
  swift_script=$(mktemp -t tshoot-seticon).swift
  cat >"$swift_script" <<'SWIFT'
import Cocoa
let args = CommandLine.arguments
guard args.count == 3, let icon = NSImage(contentsOfFile: args[1]) else { exit(1) }
let ok = NSWorkspace.shared.setIcon(icon, forFile: args[2], options: [])
exit(ok ? 0 : 2)
SWIFT
  if swift "$swift_script" "$icns_src" "$DMG_OUT" 2>/dev/null; then
    echo "  ✓ 设上 dmg 文件 Finder 图标"
  else
    echo "  [warn] setIcon 失败,dmg 文件在 Finder 仍是默认 disk image 图标(挂载后 volume 仍有图标)" >&2
  fi
  rm -f "$swift_script"
fi

size=$(du -h "$DMG_OUT" | cut -f1)
echo "✓ $DMG_OUT($size)"
echo "  分发: 双击 .dmg → 拖 .app 到 Applications → Launchpad / Spotlight 搜启动"
echo "  首次启动 Gatekeeper 拦截:右键 .app → 打开 → 确认放行"
