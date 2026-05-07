#!/usr/bin/env bash
# 把任意 1024x1024 PNG 后处理成 macOS Big Sur+ 规范的 app icon:
#   - squircle / rounded 形状裁切(macOS Dock 期望的"超椭圆"圆角,而不是简单方角)
#   - 内容缩到中心 824x824(留 100px 边距让 Dock 渲染图标时不顶到框边)
#   - 透明背景
#
# 跑一遍把 cmd/tshoot-desktop/build/appicon.png 处理后输出到 appicon.macos.png。
# package-macos.sh 优先读 .macos.png,fallback 原 png,这样原图保留不动,新图可单独 commit。
#
# 用 macOS 自带 swift,无 brew 依赖。
set -euo pipefail

SRC="${1:-cmd/tshoot-desktop/build/appicon.png}"
OUT="${2:-cmd/tshoot-desktop/build/appicon.macos.png}"

if [[ ! -f "$SRC" ]]; then
  echo "✗ 源 PNG 不存在: $SRC" >&2
  exit 1
fi

if ! command -v swift >/dev/null 2>&1; then
  echo "✗ 找不到 swift(应该在 Xcode CLT 里:xcode-select --install)" >&2
  exit 1
fi

swift_script=$(mktemp -t tshoot-icon-norm).swift
cat >"$swift_script" <<'SWIFT'
import Cocoa

let args = CommandLine.arguments
guard args.count == 3 else {
    fputs("usage: swift normalize-icon.swift <src.png> <out.png>\n", stderr)
    exit(1)
}
let srcPath = args[1]
let outPath = args[2]

guard let srcImage = NSImage(contentsOfFile: srcPath) else {
    fputs("✗ 读不到 \(srcPath)\n", stderr)
    exit(2)
}

// macOS Big Sur+ App icon 规范(参考 Apple HIG):
//   - icon canvas: 1024×1024
//   - 内容区域:中心 824×824(各侧 100px 边距,Dock 渲染时这部分留给阴影)
//   - 圆角:824 × 0.225 ≈ 185px(squircle 视觉近似)
//   - 阴影:Dock 自动加,这里不画
let canvasSize: Int = 1024
let inset: CGFloat = 100
let contentSize = CGFloat(canvasSize) - 2 * inset
let cornerRadius: CGFloat = contentSize * 0.225

// 直接在 NSBitmapImageRep 上画,比 NSImage.lockFocus pattern 更稳定
// (lockFocus 在 macOS 14+ 编码 PNG 偶发 CGImageDestinationFinalize 失败)。
guard let bitmap = NSBitmapImageRep(
    bitmapDataPlanes: nil,
    pixelsWide: canvasSize, pixelsHigh: canvasSize,
    bitsPerSample: 8, samplesPerPixel: 4,
    hasAlpha: true, isPlanar: false,
    colorSpaceName: .deviceRGB,
    bytesPerRow: 0, bitsPerPixel: 0
) else {
    fputs("✗ 创建 NSBitmapImageRep 失败\n", stderr)
    exit(3)
}

NSGraphicsContext.saveGraphicsState()
guard let gctx = NSGraphicsContext(bitmapImageRep: bitmap) else {
    fputs("✗ 创建 GraphicsContext 失败\n", stderr)
    exit(3)
}
NSGraphicsContext.current = gctx

let contentRect = NSRect(x: inset, y: inset, width: contentSize, height: contentSize)
let path = NSBezierPath(roundedRect: contentRect, xRadius: cornerRadius, yRadius: cornerRadius)
path.addClip()
// 把源图整张缩到 contentRect(原图填整个 canvas → 现在缩到 824×824 居中)
srcImage.draw(in: contentRect, from: .zero, operation: .sourceOver, fraction: 1.0)

NSGraphicsContext.restoreGraphicsState()

guard let pngData = bitmap.representation(using: .png, properties: [:]) else {
    fputs("✗ PNG 编码失败\n", stderr)
    exit(3)
}

do {
    try pngData.write(to: URL(fileURLWithPath: outPath))
} catch {
    fputs("✗ 写文件失败: \(error)\n", stderr)
    exit(4)
}
SWIFT

swift "$swift_script" "$SRC" "$OUT"
rm -f "$swift_script"

echo "✓ $OUT(squircle 圆角 + 824px 中心内容 + 100px 边距)"
