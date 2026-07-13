// render_walk.go —— workspace 模板树的扫描 + 单文件渲染/拷贝实现。
//
// walkAndRender 是 Generate() 唯一的"模板 → 产物"入口:遍历 workspace/ 下所有文件,
// .tmpl 走 text/template 渲染并去掉后缀,其它文件原样 copy。
// shouldSkipDir 在遍历过程里挑掉:
//   - skills_whitelist 没列的 skill 目录
//   - 需要显式配置但未启用的 skill(当前为 code-intelligence-query)
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
			if shouldSkipGeneratedArtifact(d.Name()) {
				return fs.SkipDir
			}
			if g.shouldSkipDir(rel) {
				return fs.SkipDir
			}
			return os.MkdirAll(filepath.Join(dstRoot, rel), 0o755)
		}

		if shouldSkipGeneratedArtifact(d.Name()) {
			return nil
		}
		if g.shouldSkipFile(rel) {
			return nil
		}

		outPath := filepath.Join(dstRoot, rel)
		if strings.HasSuffix(path, ".tmpl") {
			outPath = strings.TrimSuffix(outPath, ".tmpl")
			return g.renderFile(path, outPath)
		}
		return copyFile(path, outPath)
	})
}

func shouldSkipGeneratedArtifact(name string) bool {
	if name == "__pycache__" {
		return true
	}
	if strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py") {
		return true
	}
	if strings.HasSuffix(name, ".pyc") || strings.HasSuffix(name, ".pyo") {
		return true
	}
	return false
}

func (g *Generator) shouldSkipFile(rel string) bool {
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, "skills/config-executor/") {
		return false
	}
	configType := g.Ctx.Infrastructure.PrimaryConfigCenter().Type
	if rel == "skills/config-executor/references/nacos-api-notes.md" {
		return configType != "nacos"
	}
	if !strings.HasPrefix(rel, "skills/config-executor/scripts/") {
		return false
	}
	name := filepath.Base(rel)
	switch configType {
	case "nacos":
		return !map[string]bool{
			"nacos_config.py":               true,
			"nacos_diff.py":                 true,
			"nacos_mcp.py":                  true,
			"resolve_runtime_from_nacos.py": true,
		}[name]
	case "apollo":
		return name != "apollo_config.py"
	case "consul":
		return name != "consul_config.py"
	case "kuboard":
		return name != "kuboard_config.py"
	case "env-vars":
		return name != "resolve_runtime_static.py"
	default:
		return true
	}
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

	if !g.alwaysIncludeSkill(skillName) && !skillEnabled(g.Ctx, skillName) {
		return true
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

// serviceTopologySkillEnabled mirrors analyzerpipe's activation boundary: a
// cross-repository topology needs routing plus at least two runtime service
// nodes backed by existing local checkout directories.
func serviceTopologySkillEnabled(ctx *Context) bool {
	if ctx == nil || !skillEnabledForWhitelist(ctx, "routing") {
		return false
	}
	runnable := 0
	for _, repo := range ctx.Repos {
		if !repo.IsServiceNode() {
			continue
		}
		path := strings.TrimSpace(ctx.RepoLocalPaths[repo.Name])
		if path == "" {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			continue
		}
		runnable++
	}
	return runnable >= 2
}

func skillEnabledForWhitelist(ctx *Context, name string) bool {
	if ctx == nil || len(ctx.Generation.SkillsWhitelist) == 0 {
		return ctx != nil
	}
	for _, allowed := range ctx.Generation.SkillsWhitelist {
		if allowed == name {
			return true
		}
	}
	return false
}

func (g *Generator) alwaysIncludeSkill(skillName string) bool {
	switch skillName {
	case "bug-fixer", "bug-verifier", "api-verifier", "attachment-evidence-verifier", "frontend-repro-investigator":
		return true
	case "grafana-observability-query":
		obs := g.Ctx.Infrastructure.Observability
		return obs.Grafana.Enabled || obs.Loki.Enabled || obs.Prometheus.Enabled
	default:
		return false
	}
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
