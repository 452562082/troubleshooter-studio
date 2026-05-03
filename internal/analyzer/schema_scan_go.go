// schema_scan_go.go —— Go 仓库 ORM/struct/TableName/文件名多策略扫表名。
package analyzer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// db.Table("orders") / .Table("orders")
	reGoTableCall = regexp.MustCompile(`\.Table\(\s*"(\w+)"`)
	// gorm tag: `gorm:"column:foo"`  → 整 struct 至少有该 tag 即视为 entity
	reGoGormTag = regexp.MustCompile(`gorm:\s*"[^"]+"`)
	// type X struct { ... } 配合上面 tag 找 struct 名
	reGoStruct = regexp.MustCompile(`type\s+(\w+)\s+struct\s*\{`)
	// TableName() 方法形如 "func (X) TableName() string { return \"orders\" }"
	reGoTableName = regexp.MustCompile(`(?s)func\s+\(\s*\w+\s+\*?(\w+)\s*\)\s+TableName\s*\(\s*\)\s+string\s*\{[^}]*?return\s+"(\w+)"`)
	// db: tag 形如 `db:"order_id"`
	reGoDBTag = regexp.MustCompile("db:\\s*\"\\w+\"")
)

func scanGoSchema(repoPath string, include []string) []SchemaTable {
	files, _ := walkFiles(repoPath, include, func(p string) bool {
		return strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go")
	})
	out := []SchemaTable{}
	for _, fp := range files {
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		text := string(data)
		rel, _ := filepath.Rel(repoPath, fp)
		// .Table("orders")
		for _, m := range reGoTableCall.FindAllStringSubmatch(text, -1) {
			out = append(out, SchemaTable{
				Name: m[1], Kind: "table", Type: "mysql",
				SourceFile: rel, Strategy: "orm_api_call",
			})
		}
		// TableName() 方法 → 同时给 entity 名
		for _, m := range reGoTableName.FindAllStringSubmatch(text, -1) {
			out = append(out, SchemaTable{
				Name: m[2], Kind: "table", Type: "mysql",
				EntityName: m[1], SourceFile: rel, Strategy: "orm_api_call",
			})
		}
		// 含 gorm tag / db tag 的 struct 视为 entity(取 struct 名小写当 table 兜底)
		hasORMTag := reGoGormTag.MatchString(text) || reGoDBTag.MatchString(text)
		if hasORMTag {
			for _, m := range reGoStruct.FindAllStringSubmatch(text, -1) {
				name := m[1]
				out = append(out, SchemaTable{
					Name: snakeCase(name), Kind: "table", Type: "mysql",
					EntityName: name, SourceFile: rel, Strategy: "orm_annotation",
				})
			}
		}
		// 文件名兜底
		base := filepath.Base(rel)
		if strings.Contains(strings.ToLower(base), "entity") || strings.Contains(rel, "/entities/") || strings.Contains(rel, "/models/") {
			for _, m := range reGoStruct.FindAllStringSubmatch(text, -1) {
				name := m[1]
				out = append(out, SchemaTable{
					Name: snakeCase(name), Kind: "table", Type: "mysql",
					EntityName: name, SourceFile: rel, Strategy: "file_name",
				})
			}
		}
	}
	return out
}

// snakeCase: User → user, OrderItem → order_item, HTTPClient → http_client
// 跨 4 个 stack 的 schema 扫描共用(放这里因为 Go 是第一个用它的实现)。
func snakeCase(s string) string {
	var sb strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			// 前一个是小写时插下划线;前一个也是大写时不插(HTTPClient → http_client)
			prev := rune(s[i-1])
			if prev >= 'a' && prev <= 'z' {
				sb.WriteByte('_')
			} else if i+1 < len(s) {
				next := rune(s[i+1])
				if next >= 'a' && next <= 'z' {
					sb.WriteByte('_')
				}
			}
		}
		if r >= 'A' && r <= 'Z' {
			sb.WriteRune(r + 32)
		} else {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
