// Package factory 把 templates/ + examples/ 嵌入到 factory 二进制，
// 让 `go install github.com/xiaolong/troubleshooter-factory/cmd/factory@latest`
// 装出来的二进制也能直接跑 demo / gen，不需要仓库里 clone templates 和 examples。
//
// 使用方式：
//   import tsf "github.com/xiaolong/troubleshooter-factory"
//   tsf.TemplatesFS.ReadFile("templates/workspace/AGENTS.md.tmpl")
//
// 运行时如果磁盘上 templates/ / examples/ 存在（开发在仓库里跑），优先用磁盘；
// 否则把 embed 内容 extract 到 os.TempDir() 再用。具体策略见 cmd/factory/main.go。
package factory

import "embed"

// TemplatesFS 根含子目录 "templates/"
//
//go:embed all:templates
var TemplatesFS embed.FS

// ExamplesFS 根含子目录 "examples/"
//
//go:embed all:examples
var ExamplesFS embed.FS
