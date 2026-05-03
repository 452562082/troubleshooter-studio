// render_walk.go —— workspace 模板树的扫描 + 单文件渲染/拷贝实现。
//
// walkAndRender 是 Generate() 唯一的"模板 → 产物"入口:遍历 workspace/ 下所有文件,
// .tmpl 走 text/template 渲染并去掉后缀,其它文件原样 copy。
// shouldSkipDir 在遍历过程里挑掉:
//   - skills_whitelist 没列的 skill 目录
//   - infra 里 disabled 的 data store 对应的 *-runtime-query
//   - 没声明 config_center 时跳 config-executor
package generator

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

func (g *Generator) walkAndRender(srcRoot, dstRoot string) error {
	return filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		if d.IsDir() {
			if g.shouldSkipDir(rel) {
				return fs.SkipDir
			}
			return os.MkdirAll(filepath.Join(dstRoot, rel), 0o755)
		}

		outPath := filepath.Join(dstRoot, rel)
		if strings.HasSuffix(path, ".tmpl") {
			outPath = strings.TrimSuffix(outPath, ".tmpl")
			return g.renderFile(path, outPath)
		}
		return copyFile(path, outPath)
	})
}

func (g *Generator) shouldSkipDir(rel string) bool {
	const skillsPrefix = "skills" + string(filepath.Separator)
	if !strings.HasPrefix(rel, skillsPrefix) {
		return false
	}
	parts := strings.SplitN(rel, string(filepath.Separator), 3)
	if len(parts) < 2 {
		return false
	}
	skillName := parts[1]

	whitelist := g.Ctx.Generation.SkillsWhitelist
	if len(whitelist) > 0 {
		found := false
		for _, w := range whitelist {
			if w == skillName {
				found = true
				break
			}
		}
		if !found {
			return true
		}
	}

	for _, ds := range g.Ctx.Infrastructure.DataStores {
		if !ds.Enabled && skillName == dataStoreSkillName(ds.Type) {
			return true
		}
	}
	if skillName == "config-executor" {
		t := g.Ctx.Infrastructure.PrimaryConfigCenter().Type
		if t == "" || t == "none" {
			return true
		}
	}
	return false
}

func (g *Generator) renderFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tpl, err := template.New(filepath.Base(src)).Funcs(funcMap()).Parse(string(data))
	if err != nil {
		return fmt.Errorf("parse %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := tpl.Execute(f, g.Ctx); err != nil {
		return fmt.Errorf("render %s: %w", src, err)
	}
	return nil
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
