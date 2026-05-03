// schema_scan_node.go —— Node/TypeScript TypeORM/Sequelize/Mongoose/Prisma 表名扫描。
package analyzer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	// TypeORM: @Entity({name: "users"}) / @Entity("users") / @Entity()
	reJsTypeORMEntity = regexp.MustCompile(`@Entity\s*\(\s*(?:\{[^}]*?name\s*:\s*['"](\w+)['"]|['"](\w+)['"])`)
	reJsTypeORMClass  = regexp.MustCompile(`@Entity\s*\([^)]*\)\s*export\s+class\s+(\w+)`)
	// Sequelize: sequelize.define("users", {...}) / Model.init({...}, {tableName: "users"})
	reJsSequelizeDefine = regexp.MustCompile(`sequelize\.define\s*\(\s*['"](\w+)['"]`)
	reJsSequelizeTbl    = regexp.MustCompile(`tableName\s*:\s*['"](\w+)['"]`)
	// Mongoose: mongoose.model("User", ...) / model("User", ...)
	reJsMongooseModel = regexp.MustCompile(`mongoose\.model\s*\(\s*['"](\w+)['"]|^model\s*\(\s*['"](\w+)['"]`)
	// Prisma schema: model User { @@map("users") }
	rePrismaModel = regexp.MustCompile(`model\s+(\w+)\s*\{`)
	rePrismaMap   = regexp.MustCompile(`@@map\s*\(\s*"(\w+)"\s*\)`)
)

func scanNodeSchema(repoPath string, include []string) []SchemaTable {
	files, _ := walkFiles(repoPath, include, func(p string) bool {
		return strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".ts") ||
			strings.HasSuffix(p, ".jsx") || strings.HasSuffix(p, ".tsx") ||
			strings.HasSuffix(p, ".mjs") || strings.HasSuffix(p, ".prisma")
	})
	out := []SchemaTable{}
	for _, fp := range files {
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		text := string(data)
		rel, _ := filepath.Rel(repoPath, fp)
		isPrisma := strings.HasSuffix(fp, ".prisma")

		// TypeORM
		for _, m := range reJsTypeORMEntity.FindAllStringSubmatch(text, -1) {
			name := m[1]
			if name == "" {
				name = m[2]
			}
			if name != "" {
				out = append(out, SchemaTable{
					Name: name, Kind: "table", Type: "mysql",
					SourceFile: rel, Strategy: "orm_annotation",
				})
			}
		}
		// TypeORM class without explicit name → use class name
		for _, m := range reJsTypeORMClass.FindAllStringSubmatch(text, -1) {
			name := m[1]
			out = append(out, SchemaTable{
				Name: snakeCase(name), Kind: "table", Type: "mysql",
				EntityName: name, SourceFile: rel, Strategy: "orm_annotation",
			})
		}
		// Sequelize
		for _, m := range reJsSequelizeDefine.FindAllStringSubmatch(text, -1) {
			out = append(out, SchemaTable{
				Name: m[1], Kind: "table", Type: "mysql",
				SourceFile: rel, Strategy: "orm_api_call",
			})
		}
		for _, m := range reJsSequelizeTbl.FindAllStringSubmatch(text, -1) {
			out = append(out, SchemaTable{
				Name: m[1], Kind: "table", Type: "mysql",
				SourceFile: rel, Strategy: "orm_api_call",
			})
		}
		// Mongoose
		for _, m := range reJsMongooseModel.FindAllStringSubmatch(text, -1) {
			name := m[1]
			if name == "" {
				name = m[2]
			}
			if name != "" {
				out = append(out, SchemaTable{
					Name: strings.ToLower(name) + "s", Kind: "collection", Type: "mongodb",
					EntityName: name, SourceFile: rel, Strategy: "orm_api_call",
				})
			}
		}
		// Prisma
		if isPrisma {
			models := rePrismaModel.FindAllStringSubmatch(text, -1)
			maps := rePrismaMap.FindAllStringSubmatch(text, -1)
			// 简化:取每个 model 的名字,如果 schema.prisma 里有 @@map 就用 map 值
			usedMap := make(map[string]bool, len(maps))
			for _, m := range maps {
				usedMap[m[1]] = true
				out = append(out, SchemaTable{
					Name: m[1], Kind: "table", Type: "mysql",
					SourceFile: rel, Strategy: "orm_annotation",
				})
			}
			for _, m := range models {
				name := m[1]
				// 没显式 @@map 时用 model 名
				if !usedMap[snakeCase(name)] {
					out = append(out, SchemaTable{
						Name: snakeCase(name), Kind: "table", Type: "mysql",
						EntityName: name, SourceFile: rel, Strategy: "orm_annotation",
					})
				}
			}
		}
	}
	return out
}
