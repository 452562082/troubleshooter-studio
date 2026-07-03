// schema_scan_java.go —— Java/Kotlin 的 @Entity / @Table / @TableName / mybatis xml 扫表名。
package analyzer

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// @Entity 注解或 @Table(name="orders") 注解
	reJavaEntity    = regexp.MustCompile(`@Entity(?:\([^)]*\))?\s+(?:public\s+)?(?:abstract\s+)?class\s+(\w+)`)
	reJavaTableAnno = regexp.MustCompile(`@Table\s*\(\s*name\s*=\s*"(\w+)"`)
	reJavaTableName = regexp.MustCompile(`@TableName\s*\(\s*"?(\w+)"?\s*\)`)
	// mybatis xml: <mapper namespace="com.x.UserMapper"> + <select id="...">SELECT * FROM users WHERE ...</select>
	reMybatisMapper = regexp.MustCompile(`<mapper\s+namespace\s*=\s*"([\w\.]+)"`)
	reMybatisRMap   = regexp.MustCompile(`<resultMap\s+[^>]*type\s*=\s*"([\w\.]+)"`)
)

func scanJavaSchema(ctx context.Context, repoPath string, include []string) []SchemaTable {
	files, _ := walkFilesContext(ctx, repoPath, include, func(p string) bool {
		return strings.HasSuffix(p, ".java") || strings.HasSuffix(p, ".kt") || strings.HasSuffix(p, ".xml")
	})
	out := []SchemaTable{}
	for _, fp := range files {
		if ctx != nil && ctx.Err() != nil {
			break
		}
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		text := string(data)
		rel, _ := filepath.Rel(repoPath, fp)
		isXml := strings.HasSuffix(fp, ".xml")

		// Entity 类名 → table 名(snake_case 兜底)
		for _, m := range reJavaEntity.FindAllStringSubmatch(text, -1) {
			name := m[1]
			tbl := snakeCase(name)
			out = append(out, SchemaTable{
				Name: tbl, Kind: "table", Type: "mysql",
				EntityName: name, SourceFile: rel, Strategy: "orm_annotation",
			})
		}
		// 显式 @Table(name="...") 取真名
		for _, m := range reJavaTableAnno.FindAllStringSubmatch(text, -1) {
			out = append(out, SchemaTable{
				Name: m[1], Kind: "table", Type: "mysql",
				SourceFile: rel, Strategy: "orm_annotation",
			})
		}
		// 显式 @TableName("...") (mybatis-plus)
		for _, m := range reJavaTableName.FindAllStringSubmatch(text, -1) {
			out = append(out, SchemaTable{
				Name: m[1], Kind: "table", Type: "mysql",
				SourceFile: rel, Strategy: "orm_annotation",
			})
		}
		// mybatis xml mapper namespace 反推 entity 名
		if isXml {
			for _, m := range reMybatisMapper.FindAllStringSubmatch(text, -1) {
				parts := strings.Split(m[1], ".")
				cls := parts[len(parts)-1]
				cls = strings.TrimSuffix(cls, "Mapper")
				out = append(out, SchemaTable{
					Name: snakeCase(cls), Kind: "table", Type: "mysql",
					EntityName: cls, SourceFile: rel, Strategy: "orm_annotation",
				})
			}
			for _, m := range reMybatisRMap.FindAllStringSubmatch(text, -1) {
				parts := strings.Split(m[1], ".")
				cls := parts[len(parts)-1]
				out = append(out, SchemaTable{
					Name: snakeCase(cls), Kind: "table", Type: "mysql",
					EntityName: cls, SourceFile: rel, Strategy: "orm_annotation",
				})
			}
		}
	}
	return out
}
