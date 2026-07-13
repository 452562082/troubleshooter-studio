// Package tshoot 把 templates/ + examples/ 嵌入到 tshoot 二进制，
// 让 `go install github.com/xiaolong/troubleshooter-studio/cmd/tshoot@latest`
// 装出来的二进制也能直接跑 demo / gen，不需要仓库里 clone templates 和 examples。
//
// 使用方式：
//
//	import "github.com/xiaolong/troubleshooter-studio"
//	tshoot.TemplatesFS.ReadFile("templates/workspace/AGENTS.md.tmpl")
//
// 运行时如果磁盘上 templates/ / examples/ 存在（开发在仓库里跑），优先用磁盘；
// 否则把 embed 内容 extract 到 os.TempDir() 再用。具体策略见 cmd/tshoot/main.go。
package tshoot

import "embed"

// TemplatesFS 根含子目录 "templates/"
//
//go:embed all:templates
var TemplatesFS embed.FS

// ExamplesFS 根含子目录 "examples/"
//
//go:embed all:examples
var ExamplesFS embed.FS

// SchemaFS 根含子目录 "schema/"
//
//go:embed all:schema
var SchemaFS embed.FS
