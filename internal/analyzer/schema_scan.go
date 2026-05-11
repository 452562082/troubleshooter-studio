// schema_scan.go —— 跨语言多策略扫"本仓库的业务表 / collection / 缓存 prefix"。
//
// 跟 dependency_scan 类比但维度换成数据层 schema。多策略叠加最大化命中率,但仍是
// best-effort —— **冷门栈或自定义命名约定会漏**。漏的部分用户在生成的 yaml 里手填补全。
//
// 扫描策略(按命中精度排):
//
//  1. ORM annotation/api(最准):
//     - Java   @Entity / @Table(name="...") / @TableName("...") / mybatis <mapper namespace=...>
//     - Go     `gorm:"column:..."` tag / `db:"..."` / `bun:"table:..."` / `db.Table("...")` / `Model(&User{})`
//     - Python SQLAlchemy `__tablename__ = "..."` / Django `class Meta: db_table = "..."` / peewee
//     - Node   TypeORM `@Entity({name:"..."})` / Sequelize `define("name",...)` / Prisma `@@map("...")` / Mongoose `model("...",...)`
//
//  2. SQL 字符串扫描(兜底,各语言通用):
//     `CREATE TABLE \w+` / `INSERT INTO \w+` / `FROM \w+\s+WHERE` / `UPDATE \w+ SET`
//
//  3. 文件名 / 目录提示(最弱):
//     *Entity*.{go,java,kt} / entities/*.{go,java} / models/*.py / *.prisma
//
// 同一 (table, type) 多策略命中保留高优先级 strategy。
//
// 各 stack 实现已按域拆到子文件:
//
//	schema_scan_go.go      Go(gorm tag / TableName() / .Table() + snakeCase 共享 helper)
//	schema_scan_java.go    Java/Kotlin(@Entity / @Table / @TableName / mybatis xml)
//	schema_scan_python.go  Python(SQLAlchemy / Django / peewee)
//	schema_scan_node.go    Node/TS(TypeORM / Sequelize / Mongoose / Prisma)
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
