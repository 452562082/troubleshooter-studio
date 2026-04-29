// schema_scan.go —— 跨语言多策略扫"本仓库的业务表 / collection / 缓存 prefix"。
//
// 跟 dependency_scan 类比但维度换成数据层 schema。多策略叠加最大化命中率,但仍是
// best-effort —— **冷门栈或自定义命名约定会漏**。漏的部分用户在生成的 yaml 里手填补全。
//
// 扫描策略(按命中精度排):
//
//   1. ORM annotation/api(最准):
//      - Java   @Entity / @Table(name="...") / @TableName("...") / mybatis <mapper namespace=...>
//      - Go     `gorm:"column:..."` tag / `db:"..."` / `bun:"table:..."` / `db.Table("...")` / `Model(&User{})`
//      - Python SQLAlchemy `__tablename__ = "..."` / Django `class Meta: db_table = "..."` / peewee
//      - Node   TypeORM `@Entity({name:"..."})` / Sequelize `define("name",...)` / Prisma `@@map("...")` / Mongoose `model("...",...)`
//
//   2. SQL 字符串扫描(兜底,各语言通用):
//      `CREATE TABLE \w+` / `INSERT INTO \w+` / `FROM \w+\s+WHERE` / `UPDATE \w+ SET`
//
//   3. 文件名 / 目录提示(最弱):
//      *Entity*.{go,java,kt} / entities/*.{go,java} / models/*.py / *.prisma
//
// 同一 (table, type) 多策略命中保留高优先级 strategy。
package analyzer

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ScanSchema 单仓库扫,产出 SchemaTable 列表。stack 决定优先用哪种 ORM 模式扫,
// 但 SQL string + 文件名两种通用策略所有 stack 都会跑(万一 ORM 没识别还能兜)。
func ScanSchema(stack, repoPath string, includePaths []string) []SchemaTable {
	var all []SchemaTable
	switch stack {
	case "go":
		all = append(all, scanGoSchema(repoPath, includePaths)...)
	case "java":
		all = append(all, scanJavaSchema(repoPath, includePaths)...)
	case "python":
		all = append(all, scanPythonSchema(repoPath, includePaths)...)
	case "node":
		all = append(all, scanNodeSchema(repoPath, includePaths)...)
	}
	// 通用 SQL 字符串扫(所有 stack 都跑)
	all = append(all, scanSQLLiterals(stack, repoPath, includePaths)...)
	return dedupeSchemaTables(all)
}

func dedupeSchemaTables(in []SchemaTable) []SchemaTable {
	// (name, kind) 去重,优先级:orm_annotation > orm_api_call > sql_literal > file_name
	priority := map[string]int{
		"orm_annotation": 4, "orm_api_call": 3, "sql_literal": 2, "file_name": 1,
	}
	best := map[string]SchemaTable{}
	for _, t := range in {
		if t.Name == "" {
			continue
		}
		key := strings.ToLower(t.Name) + "|" + t.Kind
		exist, ok := best[key]
		if !ok || priority[t.Strategy] > priority[exist.Strategy] {
			best[key] = t
		}
	}
	out := make([]SchemaTable, 0, len(best))
	for _, t := range best {
		out = append(out, t)
	}
	return out
}

// ── 通用 SQL literal 扫描(所有语言) ────────────────────────────────
var (
	reSQLCreate = regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?` + "`" + `?(\w+)` + "`" + `?`)
	reSQLInsert = regexp.MustCompile(`(?i)INSERT\s+INTO\s+` + "`" + `?(\w+)` + "`" + `?`)
	reSQLFrom   = regexp.MustCompile(`(?i)FROM\s+` + "`" + `?(\w+)` + "`" + `?\s+(?:WHERE|LIMIT|ORDER|GROUP|JOIN|HAVING)`)
	reSQLUpdate = regexp.MustCompile(`(?i)UPDATE\s+` + "`" + `?(\w+)` + "`" + `?\s+SET`)
)

func scanSQLLiterals(stack, repoPath string, include []string) []SchemaTable {
	exts := []string{".go", ".java", ".kt", ".py", ".js", ".ts", ".jsx", ".tsx", ".mjs", ".sql", ".xml", ".rb", ".php"}
	files, _ := walkFiles(repoPath, include, func(p string) bool {
		for _, e := range exts {
			if strings.HasSuffix(p, e) {
				return true
			}
		}
		return false
	})
	out := []SchemaTable{}
	for _, fp := range files {
		data, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		text := string(data)
		rel, _ := filepath.Rel(repoPath, fp)
		add := func(name string) {
			n := strings.ToLower(strings.TrimSpace(name))
			if n == "" || isSQLKeyword(n) {
				return
			}
			out = append(out, SchemaTable{
				Name: n, Kind: "table", Type: "mysql",
				SourceFile: rel, Strategy: "sql_literal",
			})
		}
		for _, m := range reSQLCreate.FindAllStringSubmatch(text, -1) {
			add(m[1])
		}
		for _, m := range reSQLInsert.FindAllStringSubmatch(text, -1) {
			add(m[1])
		}
		for _, m := range reSQLFrom.FindAllStringSubmatch(text, -1) {
			add(m[1])
		}
		for _, m := range reSQLUpdate.FindAllStringSubmatch(text, -1) {
			add(m[1])
		}
	}
	_ = stack
	return out
}

// 常见 SQL 关键字误命中过滤
func isSQLKeyword(s string) bool {
	switch s {
	case "select", "from", "where", "and", "or", "not", "null", "true", "false",
		"join", "on", "limit", "order", "group", "by", "having", "as", "in", "exists",
		"if", "case", "when", "then", "else", "end", "table", "set", "values":
		return true
	}
	return false
}

// ── Go ─────────────────────────────────────────────────────────────────
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

// ── Java ───────────────────────────────────────────────────────────────
var (
	// @Entity 注解或 @Table(name="orders") 注解
	reJavaEntity = regexp.MustCompile(`@Entity(?:\([^)]*\))?\s+(?:public\s+)?(?:abstract\s+)?class\s+(\w+)`)
	reJavaTableAnno = regexp.MustCompile(`@Table\s*\(\s*name\s*=\s*"(\w+)"`)
	reJavaTableName = regexp.MustCompile(`@TableName\s*\(\s*"?(\w+)"?\s*\)`)
	// mybatis xml: <mapper namespace="com.x.UserMapper"> + <select id="...">SELECT * FROM users WHERE ...</select>
	reMybatisMapper = regexp.MustCompile(`<mapper\s+namespace\s*=\s*"([\w\.]+)"`)
	reMybatisRMap   = regexp.MustCompile(`<resultMap\s+[^>]*type\s*=\s*"([\w\.]+)"`)
)

func scanJavaSchema(repoPath string, include []string) []SchemaTable {
	files, _ := walkFiles(repoPath, include, func(p string) bool {
		return strings.HasSuffix(p, ".java") || strings.HasSuffix(p, ".kt") || strings.HasSuffix(p, ".xml")
	})
	out := []SchemaTable{}
	for _, fp := range files {
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

// ── Python ─────────────────────────────────────────────────────────────
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

// ── Node ───────────────────────────────────────────────────────────────
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
