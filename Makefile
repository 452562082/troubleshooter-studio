# troubleshooter-studio — 一键构建 / 测试 / 发布
#
# 常用:
#   make              # 同 `make build` —— 开发模式(不重构前端,适合改 Go)
#   make web          # 构建前端 + 拷到 embed 目标
#   make build        # 出单平台 CLI 二进制 bin/tshoot,version 从 git 读
#   make desktop      # 出 Wails 桌面 app (cmd/tshoot-desktop)
#   make release      # 交叉编译出 dist/bin/tshoot-<os>-<arch>
#   make test         # 全量 go test,含 race
#   make lint         # go vet + gofmt -l
#   make demo         # make build 后立即 ./bin/tshoot demo
#   make clean        # 清临时产物

SHELL := /bin/bash

# ── 版本信息:从 git 读;没 tag 时 dev ─────────────────────────────
VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

BIN     ?= bin/tshoot
WEB_SRC := web
WEB_DIST := internal/webui/dist

# 多平台矩阵(可按需扩)
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64

.PHONY: default
default: build

# ── 前端:npm build + 拷到 embed 目标 ────────────────────────────
.PHONY: web
web:
	@echo "▶ building web frontend ($(WEB_SRC) → $(WEB_DIST))"
	cd $(WEB_SRC) && npm install --silent && npm run build
	@rm -rf $(WEB_DIST)
	@mkdir -p $(WEB_DIST)
	cp -R $(WEB_SRC)/dist/. $(WEB_DIST)/
	@echo "✓ web embedded"

# ── 开发构建:不重构前端,适合只改 Go ─────────────────────────────
.PHONY: build
build:
	@echo "▶ go build $(BIN) (version=$(VERSION) commit=$(COMMIT))"
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/tshoot
	@echo "✓ $(BIN) ready"

# ── 发布构建:先 web,再多平台交叉编译 ─────────────────────────────
.PHONY: release
release: web
	@echo "▶ cross-compiling to dist/bin/"
	@mkdir -p dist/bin
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; \
	  out="dist/bin/tshoot-$(VERSION)-$$os-$$arch"; \
	  echo "  → $$out"; \
	  GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
	    go build -ldflags "$(LDFLAGS)" -o "$$out" ./cmd/tshoot || exit 1; \
	done
	@ls -lh dist/bin/
	@echo "✓ release artifacts in dist/bin/"

# ── app 图标:从 assets/app-icon.svg 重渲染 cmd/tshoot-desktop/build/appicon.png ──
# macOS 自带的 qlmanage 能渲染 SVG,免装额外工具。改完 svg 要跑 make icon 刷新 png,
# 再 make desktop-app 时 scripts/package-macos.sh 会 sips + iconutil 转成 .icns。
.PHONY: icon
icon:
	@echo "▶ rendering assets/app-icon.svg → cmd/tshoot-desktop/build/appicon.png (1024x1024)"
	@tmp=$$(mktemp -d); \
	 qlmanage -t -s 1024 -o $$tmp assets/app-icon.svg >/dev/null 2>&1; \
	 cp $$tmp/app-icon.svg.png cmd/tshoot-desktop/build/appicon.png; \
	 rm -rf $$tmp
	@echo "✓ cmd/tshoot-desktop/build/appicon.png ready"

# ── 按 cmd/tshoot-desktop/App 的方法生成前端绑定:web/wailsjs/go/main/App.{d.ts,js}
# + web/wailsjs/go/models.ts。改了 Go 端 binding 或引用类型后跑一下。
# 需要 wails CLI(go install github.com/wailsapp/wails/v2/cmd/wails@latest)
.PHONY: wails-gen
wails-gen:
	@echo "▶ wails generate module (从 cmd/tshoot-desktop/ 扫 App methods)"
	@cd cmd/tshoot-desktop && wails generate module
	@echo "✓ web/wailsjs/go/ 更新完成,记得 git add"

# ── 桌面 app (Wails v2):单独 target,不影响 CLI 构建 ──────────────
# Wails v2 构建踩坑提醒:
#  1. build tags `desktop production` 必须带,不然 wails.Run 会主动拒跑,
#     提示 "Wails applications will not build without the correct build tags."
#  2. cgo 必须开启(WKWebView / WebView2 绑定)
#  3. macOS 下 Wails 用 UTType(UniformTypeIdentifiers.framework),要在 CGO_LDFLAGS
#     里显式加 -framework;wails build 会自动注入,go build 要自己带。
#     -mmacosx-version-min=10.13 保证同时兼容老系统(UTType 在运行时做 availability 判断)。
DESKTOP_BIN ?= bin/tshoot-desktop
DESKTOP_CGO_LDFLAGS_DARWIN := -framework UniformTypeIdentifiers -mmacosx-version-min=10.13
.PHONY: desktop
desktop: web
	@echo "▶ building desktop app ($(DESKTOP_BIN))"
	CGO_ENABLED=1 CGO_LDFLAGS="$(DESKTOP_CGO_LDFLAGS_DARWIN)" \
	  go build -tags "desktop production" -ldflags "$(LDFLAGS)" -o $(DESKTOP_BIN) ./cmd/tshoot-desktop
	@echo "✓ $(DESKTOP_BIN) ready"

# ── 桌面 app 开发模式(dev tag,允许 Vite hot-reload + devtools):备用 ────────
.PHONY: desktop-dev
desktop-dev: web
	@echo "▶ building desktop app with dev tag ($(DESKTOP_BIN)-dev)"
	CGO_ENABLED=1 CGO_LDFLAGS="$(DESKTOP_CGO_LDFLAGS_DARWIN)" \
	  go build -tags "desktop dev" -ldflags "$(LDFLAGS)" -o $(DESKTOP_BIN)-dev ./cmd/tshoot-desktop
	@echo "✓ $(DESKTOP_BIN)-dev ready"

# ── .app bundle(macOS):双击不再弹 Terminal ──────────────────────
# 裸二进制在 Finder 双击会被 macOS 用 Terminal 启动(弹出终端窗口);
# .app 包里有 Info.plist + MacOS/ 目录,macOS 认它是 GUI app,直接起 WebView 窗口。
# 打包细节走 scripts/package-macos.sh(Makefile recipe 跨 shell 行做 heredoc 麻烦)
BUNDLE_NAME  := TroubleshooterStudio
BUNDLE_DIR   := dist/$(BUNDLE_NAME).app
BUNDLE_ID    := studio.troubleshooter.desktop
.PHONY: desktop-app
desktop-app: desktop
	@icon_src="cmd/tshoot-desktop/build/appicon.png"; \
	 [ -f "cmd/tshoot-desktop/build/appicon.macos.png" ] && icon_src="cmd/tshoot-desktop/build/appicon.macos.png"; \
	 BIN=$(DESKTOP_BIN) BUNDLE_DIR=$(BUNDLE_DIR) BUNDLE_NAME=$(BUNDLE_NAME) \
	 BUNDLE_ID=$(BUNDLE_ID) VERSION=$(VERSION) \
	 ICON_SRC="$$icon_src" \
	 bash scripts/package-macos.sh

# ── 把原 appicon.png 后处理成 macOS 规范图标(squircle + 边距 + 透明背景)─────
# 输出到 appicon.macos.png(原图保留),desktop-app 优先用 .macos.png。
.PHONY: icon-macos
icon-macos:
	@bash scripts/normalize-icon.sh

# ── .dmg 安装包(macOS 标准分发格式,系统自带 hdiutil 不依赖 brew)──────
# 双击 .dmg 挂载 → 拖 .app 到 Applications 软链 → 装机完成,Launchpad/Spotlight 直接搜
DMG_OUT := dist/$(BUNDLE_NAME)-$(VERSION).dmg
.PHONY: desktop-dmg
desktop-dmg: desktop-app
	@APP_BUNDLE=$(BUNDLE_DIR) VOLUME_NAME=$(BUNDLE_NAME) DMG_OUT=$(DMG_OUT) \
	 bash scripts/package-dmg.sh

# ── 快速试跑:build 后立即 demo ──────────────────────────────────
.PHONY: demo
demo: build
	./$(BIN) demo

# ── 测试 / lint ─────────────────────────────────────────────────
.PHONY: test
test:
	go test -race -cover ./...

.PHONY: lint
lint:
	go vet ./...
	@if [ -n "$$(gofmt -l .)" ]; then \
	  echo "gofmt 未通过:"; gofmt -l .; exit 1; \
	fi
	@echo "✓ go vet + gofmt clean"
	cd $(WEB_SRC) && npx vue-tsc --noEmit

# ── 清理 ────────────────────────────────────────────────────────
.PHONY: clean
clean:
	rm -rf bin/ dist/bin/ $(WEB_DIST)/assets $(WEB_DIST)/index.html
	@echo "✓ cleaned bin/, dist/bin/, embedded web dist (placeholder 保留)"
