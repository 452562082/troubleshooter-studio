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
#
# 发布(本地仅 dry-run,真发布走 GitLab CI manual button — 详见 docs/CI-RELEASE.md):
#   make release-notes              # 看下次发版会是什么 changelog(只 print,不动 git)
#   scripts/release.sh patch --print-only    # 看版本号会算成几(本地预览)
#   make release-tag VERSION=v0.7.0 # ⚠ 仅在迁移/特殊场景用:本地打个 tag 不 push 不 publish
#                                   # 真要发版本应该:提 MR 合到 main → 在 Pipeline 点 release:* 按钮
#   make release-publish VERSION=v0.7.0 # 对已有 tag 重传 binary(需 GITLAB_TOKEN,运维场景)
#
# 已删:make bump-{patch,minor,major} / make tag-and-release —— 强制所有 release 走 CI,
# 版本号决策有 audit trail,git history 干净统一(都是 CI 用同一身份打 tag)。

SHELL := /bin/bash

# ── 版本信息:从 git 读;没 tag 时 dev ─────────────────────────────
VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

BIN     ?= bin/tshoot
WEB_SRC := web
WEB_DIST := internal/webui/dist

# 多平台矩阵(可按需扩)。windows 编译时 release recipe 自动加 .exe 后缀。
PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64

.PHONY: default
default: build

# ── 前端:npm build + 拷到 embed 目标 ────────────────────────────
.PHONY: web
# SKIP_WEB_BUILD=1 + 已有 $(WEB_DIST)/index.html 时跳过整段 — 给 GitLab CI 的 desktop job
# 用:它从 web job 的 artifact 拿到 internal/webui/dist,不需要再 npm ci 重 build,
# 省 2-3 min 单次 pipeline 时间。Makefile recipe 行间是独立 shell,跳过逻辑必须**整段**
# 用 if/else 写一行,否则 exit 0 不影响后续 recipe 行。
web:
	@if [ -n "$$SKIP_WEB_BUILD" ] && [ -f "$(WEB_DIST)/index.html" ]; then \
		echo "▶ web build SKIPPED (SKIP_WEB_BUILD=1 + 已有 $(WEB_DIST)/index.html)"; \
	else \
		echo "▶ building web frontend ($(WEB_SRC) → $(WEB_DIST))"; \
		cd $(WEB_SRC) && npm ci --ignore-scripts --silent && npm run build && cd - >/dev/null; \
		rm -rf $(WEB_DIST); \
		mkdir -p $(WEB_DIST); \
		cp -R $(WEB_SRC)/dist/. $(WEB_DIST)/; \
		echo "✓ web embedded"; \
	fi

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
	  ext=""; \
	  if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
	  out="dist/bin/tshoot-$(VERSION)-$$os-$$arch$$ext"; \
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
BROWSER_RUNTIME_STAGE ?= .cache/desktop-browser-runtime
.PHONY: desktop-app
desktop-app: desktop
	@echo "▶ preparing pinned Chromium for the desktop bundle"
	@runtime_src="$$(go run ./cmd/tshoot-browser-runtime --root "$(BROWSER_RUNTIME_STAGE)")"; \
	 icon_src="cmd/tshoot-desktop/build/appicon.png"; \
	 [ -f "cmd/tshoot-desktop/build/appicon.macos.png" ] && icon_src="cmd/tshoot-desktop/build/appicon.macos.png"; \
	 BIN=$(DESKTOP_BIN) BUNDLE_DIR=$(BUNDLE_DIR) BUNDLE_NAME=$(BUNDLE_NAME) \
	 BUNDLE_ID=$(BUNDLE_ID) VERSION=$(VERSION) \
	 BROWSER_RUNTIME_SRC="$$runtime_src" \
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

# ── 一键发版本到 GitLab Release ─────────────────────────────────────
# 流程:dmg + 跨平台 CLI binary 全 build → curl 调 GitLab API 上传 + 创 Release
# 前置:
#   1) git tag $(VERSION) 已 push 到远端
#   2) 环境变量 GITLAB_TOKEN(GitLab Settings → Access Tokens,scope=api)
# 用法:
#   make release-publish                          # VERSION 来自 git describe
#   make release-publish VERSION=v0.1.1           # 显式指定
.PHONY: release-publish
release-publish: check-token desktop-dmg release
	@VERSION=$(VERSION) bash scripts/publish-gitlab-release.sh

# ── 一键发版本到 GitHub Release(对偶 release-publish,跑在 GitHub Actions macos-latest)──
# 流程与 GitLab 版相同:dmg + 跨平台 CLI binary → gh release create + upload。
# 鉴权用 $GITHUB_TOKEN(GitHub Actions 自动注入,本地需 PAT scope=repo)。
# 本地手动调:make release-publish-github VERSION=v0.1.0 GITHUB_TOKEN=ghp_xxx
.PHONY: release-publish-github
release-publish-github: check-token-github desktop-dmg release
	@VERSION=$(VERSION) bash scripts/publish-github-release.sh

# 提早检测 GITLAB_TOKEN 缺失,免得 bump-patch / bump-minor / bump-major 跑了 2 分钟 build
# 才挂在最后一步。
.PHONY: check-token
check-token:
	@if [ -z "$$GITLAB_TOKEN" ]; then \
	  echo "✗ 缺 env GITLAB_TOKEN(GitLab → Preferences → Access Tokens,scope=api)" >&2; \
	  echo "  设到 ~/.zshrc:export GITLAB_TOKEN=glpat-xxx,然后 source ~/.zshrc 再来" >&2; \
	  exit 1; \
	fi

# 同上,GitHub 版用 $GITHUB_TOKEN(GitHub Actions 内自动注入,本地需 gh auth token / PAT)
.PHONY: check-token-github
check-token-github:
	@if [ -z "$$GITHUB_TOKEN" ]; then \
	  echo "✗ 缺 env GITHUB_TOKEN(GitHub → Settings → Developer settings → Tokens,scope=repo)" >&2; \
	  echo "  GitHub Actions 内已自动注入,如果是本地跑:export GITHUB_TOKEN=\$$(gh auth token)" >&2; \
	  exit 1; \
	fi

# 注:本地一键发布(make tag-and-release / bump-{patch,minor,major})已删 — 强制
# release 走 GitLab CI manual button,真正的 release 流程见 docs/CI-RELEASE.md。
# 想本地 dry-run:make release-notes 看 changelog,scripts/release.sh patch --print-only 看版本号。

# ── 快速试跑:build 后立即 demo ──────────────────────────────────
.PHONY: demo
demo: build
	./$(BIN) demo

# ── 测试 / lint ─────────────────────────────────────────────────
.PHONY: test
test:
	go test -race -cover ./...
	./scripts/check-go-coverage.sh
	./scripts/test-skill-scripts.sh

.PHONY: audit
audit:
	cd $(WEB_SRC) && npm audit --audit-level=moderate
	@if command -v govulncheck >/dev/null 2>&1; then \
	  govulncheck ./...; \
	else \
	  echo "govulncheck not installed; install with: go install golang.org/x/vuln/cmd/govulncheck@v1.5.0"; \
	  exit 1; \
	fi

.PHONY: lint
lint:
	go vet ./...
	@out="$$(git ls-files -z '*.go' | xargs -0 gofmt -l)"; \
	if [ -n "$$out" ]; then \
	  echo "gofmt 未通过:"; echo "$$out"; exit 1; \
	fi
	@echo "✓ go vet + gofmt clean"
	cd $(WEB_SRC) && npx vue-tsc --noEmit

# ── 清理 ────────────────────────────────────────────────────────
.PHONY: clean
clean:
	rm -rf bin/ dist/bin/ $(WEB_DIST)/assets $(WEB_DIST)/index.html
	@echo "✓ cleaned bin/, dist/bin/, embedded web dist (placeholder 保留)"

# ── 发布:从 commits 自动生成 changelog 塞进 annotated tag ──────
#
# 设计:annotated tag 的 message 自动填上"上次 tag 到现在的 commits",
# push 后 GitLab/GitHub 的 Tags 页面就能看到这版改了啥,不用单独维护 CHANGELOG.md。
#
# 用法:
#   make release-notes              # dry-run,只打印将来要写的 annotation,不改 git
#   make release-tag VERSION=v0.7.0 # 真创建 annotated tag(本地;之后 git push --tags)

# scripts/changelog.sh:抽取上一个 tag 到 HEAD 的 commit subjects + 简要分类。
# 不写在 Makefile 内 shell heredoc 里(Makefile 多行 + $$ 转义易错且难调试)。
.PHONY: release-notes
release-notes:
	@scripts/changelog.sh

.PHONY: release-tag
release-tag:
	@if [ -z "$(VERSION)" ]; then \
		echo "Usage: make release-tag VERSION=v0.7.0"; exit 1; \
	fi
	@if ! echo "$(VERSION)" | grep -qE '^v[0-9]+\.[0-9]+\.[0-9]+$$'; then \
		echo "❌ VERSION 必须 vX.Y.Z 格式,实际:$(VERSION)"; exit 1; \
	fi
	@if git rev-parse --verify --quiet "$(VERSION)" >/dev/null; then \
		echo "❌ tag $(VERSION) 已存在,refusing 覆盖"; exit 1; \
	fi
	@if [ -n "$$(git status --porcelain)" ]; then \
		echo "❌ 工作区有未提交改动,refuse 打 tag(防把脏改动当 release):"; \
		git status --short; exit 1; \
	fi
	@msg=$$(scripts/changelog.sh "$(VERSION)") || exit 1; \
	echo "─── 即将写入 $(VERSION) tag annotation ───"; \
	echo "$$msg"; \
	echo "──────────────────────────────────────────"; \
	echo "$$msg" | git tag -a "$(VERSION)" -F -
	@echo "✓ tag $(VERSION) 已创建"
	@echo "  推送到远端:git push troubleshooter-studio $(VERSION)"
	@echo "  撤销:git tag -d $(VERSION)"
