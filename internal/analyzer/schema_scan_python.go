// schema_scan_python.go —— Python SQLAlchemy/Django/peewee 表名扫描 + models/ 目录文件名兜底。
package analyzer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// SQLAlchemy: __tablename__ = "users"
	rePyTablename = regexp.MustCompile(`__tablename__\s*=\s*['"](\w+)['"]`)
	// Django: class Meta: db_table = "users"
	rePyDjangoMeta = regexp.MustCompile(`(?s)class\s+Meta:.*?db_table\s*=\s*['"](\w+)['"]`)
	// peewee: class Meta: table_name = "users"
	rePyPeewee = regexp.MustCompile(`(?s)class\s+Meta:.*?table_name\s*=\s*['"](\w+)['"]`)
	// 上面任一命中时关联的 class 名(class X(Base): / class X(models.Model):)
	rePyClass = regexp.MustCompile(`class\s+(\w+)\s*\(\s*(?:Base|models\.Model|Document|BaseModel)\s*\)`)
)

func scanPythonSchema(repoPath string, include []string) []SchemaTable {
	files, _ := walkFiles(repoPath, include, func(p string) bool {
		return strings.HasSuffix(p, ".py")
	})
	out := []SchemaTable{}
	for _, fp := range files {
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		text := string(data)
		rel, _ := filepath.Rel(repoPath, fp)
		for _, m := range rePyTablename.FindAllStringSubmatch(text, -1) {
			out = append(out, SchemaTable{
				Name: m[1], Kind: "table", Type: "mysql",
				SourceFile: rel, Strategy: "orm_annotation",
			})
		}
		for _, m := range rePyDjangoMeta.FindAllStringSubmatch(text, -1) {
			out = append(out, SchemaTable{
				Name: m[1], Kind: "table", Type: "mysql",
				SourceFile: rel, Strategy: "orm_annotation",
			})
		}
		for _, m := range rePyPeewee.FindAllStringSubmatch(text, -1) {
			out = append(out, SchemaTable{
				Name: m[1], Kind: "table", Type: "mysql",
				SourceFile: rel, Strategy: "orm_annotation",
			})
		}
		// 文件名兜底:models/ 目录或 *_model.py
		isModel := strings.Contains(rel, "/models/") || strings.HasSuffix(filepath.Base(rel), "_model.py") ||
			filepath.Base(filepath.Dir(rel)) == "models"
		if isModel {
			for _, m := range rePyClass.FindAllStringSubmatch(text, -1) {
				name := m[1]
				out = append(out, SchemaTable{
					Name: snakeCase(name) + "s", Kind: "table", Type: "mysql",
					EntityName: name, SourceFile: rel, Strategy: "file_name",
				})
			}
		}
	}
	return out
}
