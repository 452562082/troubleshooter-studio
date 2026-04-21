# troubleshooter-factory — 一键构建 / 测试 / 发布
#
# 常用:
#   make              # 同 `make build` —— 开发模式(不重构前端,适合改 Go)
#   make web          # 构建前端 + 拷到 embed 目标
#   make build        # 出单平台二进制 bin/factory,version 从 git 读
#   make release      # 交叉编译出 dist/bin/factory-<os>-<arch>
#   make test         # 全量 go test,含 race
#   make lint         # go vet + gofmt -l
#   make demo         # make build 后立即 ./bin/factory demo
#   make clean        # 清临时产物

SHELL := /bin/bash

# ── 版本信息:从 git 读;没 tag 时 dev ─────────────────────────────
VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

BIN     ?= bin/factory
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
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/factory
	@echo "✓ $(BIN) ready"

# ── 发布构建:先 web,再多平台交叉编译 ─────────────────────────────
.PHONY: release
release: web
	@echo "▶ cross-compiling to dist/bin/"
	@mkdir -p dist/bin
	@for p in $(PLATFORMS); do \
	  os=$${p%/*}; arch=$${p#*/}; \
	  out="dist/bin/factory-$(VERSION)-$$os-$$arch"; \
	  echo "  → $$out"; \
	  GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
	    go build -ldflags "$(LDFLAGS)" -o "$$out" ./cmd/factory || exit 1; \
	done
	@ls -lh dist/bin/
	@echo "✓ release artifacts in dist/bin/"

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
