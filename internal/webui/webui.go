// Package webui 把前端构建产物嵌入 tshoot 二进制，让 `tshoot serve` 一键提供 API + 前端。
//
// 生产构建流程：
//  1. cd web && npm ci --ignore-scripts && npm run build # 生成 web/dist/
//  2. cp -R web/dist/. internal/webui/dist/         # 把产物拷贝到 embed 目标
//  3. go build -o bin/tshoot ./cmd/tshoot
//
// 未执行步骤 2 时，dist/ 目录里只有一个空 .gitkeep，embed 仍能成功；
// Distribution() 会回退到 placeholder/index.html，浏览器访问时会提示"请先构建前端"。
//
// 开发模式依然可以走两个进程：Go `tshoot serve` + Vite `npm run dev`，
// Vite 会把 /api 代理到 Go。embed 的前端只在生产模式生效（index.html 存在时）。
package webui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

//go:embed all:placeholder
var placeholderFS embed.FS

// Distribution 返回可以直接挂到 http.FileServer 的 fs.FS：
//   - 如果 dist/index.html 存在（即 npm run build 的产物已拷入），返回真前端；
//   - 否则回退到 placeholder/，只含一张"请先构建前端"的提示页。
func Distribution() fs.FS {
	if sub, err := fs.Sub(distFS, "dist"); err == nil {
		if _, err := fs.Stat(sub, "index.html"); err == nil {
			return sub
		}
	}
	sub, _ := fs.Sub(placeholderFS, "placeholder")
	return sub
}
