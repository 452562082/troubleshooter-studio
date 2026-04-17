package generator

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/xiaolong/troubleshooter-factory/internal/analyzer"
	"github.com/xiaolong/troubleshooter-factory/internal/config"
)

type Context struct {
	*config.SystemConfig
	AgentID string
	// Findings[service][env] -> Finding；env 为空串表示对所有环境生效
	Findings map[string]map[string]analyzer.Finding
	// PriorOverrides[service][env] -> Finding；来自上次生成产物中的人工 verified 行
	PriorOverrides map[string]map[string]analyzer.Finding
}

type Generator struct {
	TemplateRoot string
	OutputDir    string
	Ctx          *Context
	Summary      *GenSummary
}

// GenSummary 描述一次 Generate 的实际产出结构，便于 CLI 以 text / json 等格式展示
type GenSummary struct {
	System              string `json:"system"`
	ConfigCenter        string `json:"config_center"`
	OutputDir           string `json:"output_dir"`
	SkillsIncludedCount int    `json:"skills_included_count"`
	FilesWritten        int    `json:"files_written"`
	PreservedCount      int    `json:"preserved_count"`
	PriorOverridesCount int    `json:"prior_overrides_count"`
	AnalyzerHitsCount   int    `json:"analyzer_hits_count"`
}

func New(cfg *config.SystemConfig, templateRoot, outputDir string) *Generator {
	return &Generator{
		TemplateRoot: templateRoot,
		OutputDir:    outputDir,
		Ctx: &Context{
			SystemConfig:   cfg,
			AgentID:        cfg.System.ID + "-troubleshooter",
			Findings:       map[string]map[string]analyzer.Finding{},
			PriorOverrides: map[string]map[string]analyzer.Finding{},
		},
	}
}

// LoadAnalysis 合并 analyzer 产出的 findings 到 Context
func (g *Generator) LoadAnalysis(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read analysis: %w", err)
	}
	var report analyzer.Report
	if err := json.Unmarshal(data, &report); err != nil {
		return fmt.Errorf("parse analysis: %w", err)
	}
	for _, ra := range report.Repos {
		if len(ra.Findings) == 0 || len(ra.ServiceNames) == 0 {
			continue
		}
		for _, svc := range ra.ServiceNames {
			if g.Ctx.Findings[svc] == nil {
				g.Ctx.Findings[svc] = map[string]analyzer.Finding{}
			}
			for _, nf := range ra.Findings {
				env := nf.EnvProfile
				if _, dup := g.Ctx.Findings[svc][env]; !dup {
					g.Ctx.Findings[svc][env] = nf
				}
			}
		}
	}
	return nil
}

func (g *Generator) Generate() error {
	// 1) 从现有产物提取 preserved 文件内容 + config-map 人工行
	snap, err := SnapshotExisting(g.OutputDir, g.Ctx.Generation.PreserveOnRegenerate)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	// 若 config_center 切换（nacos→apollo 等），不继承 prior overrides，避免字段错配
	if snap.OriginalCenter == "" || snap.OriginalCenter == g.Ctx.Infrastructure.ConfigCenter.Type {
		for svc, byEnv := range snap.ConfigOverrides {
			if g.Ctx.PriorOverrides[svc] == nil {
				g.Ctx.PriorOverrides[svc] = map[string]analyzer.Finding{}
			}
			for env, f := range byEnv {
				g.Ctx.PriorOverrides[svc][env] = f
			}
		}
	}

	if err := os.RemoveAll(g.OutputDir); err != nil {
		return fmt.Errorf("clean output: %w", err)
	}
	if err := os.MkdirAll(g.OutputDir, 0o755); err != nil {
		return fmt.Errorf("create output: %w", err)
	}

	// workspace → templates/workspace-template/
	wsSrc := filepath.Join(g.TemplateRoot, "workspace")
	wsDst := filepath.Join(g.OutputDir, "templates", "workspace-template")
	if err := g.walkAndRender(wsSrc, wsDst); err != nil {
		return fmt.Errorf("workspace: %w", err)
	}

	// scripts → scripts/
	scSrc := filepath.Join(g.TemplateRoot, "scripts")
	scDst := filepath.Join(g.OutputDir, "scripts")
	if err := g.walkAndRender(scSrc, scDst); err != nil {
		return fmt.Errorf("scripts: %w", err)
	}

	if err := g.writeReadme(); err != nil {
		return fmt.Errorf("readme: %w", err)
	}

	// 写 .clawhub/lock.json：列出本次生成的 skills（OpenClaw 工作区元数据）
	if err := g.writeClawhubLock(); err != nil {
		return fmt.Errorf("clawhub lock: %w", err)
	}

	// mark shell scripts executable
	for _, name := range []string{"install.sh", "self-test.sh", "uninstall.sh"} {
		p := filepath.Join(g.OutputDir, "scripts", name)
		if _, err := os.Stat(p); err == nil {
			_ = os.Chmod(p, 0o755)
		}
	}

	// 还原 preserved 文件（覆盖刚渲染的默认版本）
	if err := snap.Restore(g.OutputDir); err != nil {
		return fmt.Errorf("restore preserved: %w", err)
	}

	// 填 Summary：供 CLI 按 text/json 渲染；不再直接 Printf
	g.Summary = &GenSummary{
		System:              g.Ctx.System.ID,
		ConfigCenter:        g.Ctx.Infrastructure.ConfigCenter.Type,
		OutputDir:           g.OutputDir,
		SkillsIncludedCount: countSkills(g.OutputDir),
		FilesWritten:        countFiles(g.OutputDir),
		PreservedCount:      len(snap.PreservedFiles),
		PriorOverridesCount: countOverrides(g.Ctx.PriorOverrides),
		AnalyzerHitsCount:   countOverrides(g.Ctx.Findings),
	}
	return nil
}

func countSkills(outputDir string) int {
	skillsDir := filepath.Join(outputDir, "templates", "workspace-template", "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			n++
		}
	}
	return n
}

func countFiles(outputDir string) int {
	n := 0
	_ = filepath.WalkDir(outputDir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

// writeClawhubLock 生成 ~/.openclaw 工作区识别用的 .clawhub/lock.json
// 按输出目录下实际存在的 skills/*/ 目录来列举，避免与模板过滤逻辑重复判断
func (g *Generator) writeClawhubLock() error {
	wsRoot := filepath.Join(g.OutputDir, "templates", "workspace-template")
	skillsDir := filepath.Join(wsRoot, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		// 没有 skills 目录也写一份空 lock，保证 OpenClaw 能识别工作区
		entries = nil
	}
	now := time.Now().UnixMilli()
	type skillEntry struct {
		Version     string `json:"version"`
		InstalledAt int64  `json:"installedAt"`
	}
	skills := map[string]skillEntry{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		skills[e.Name()] = skillEntry{Version: "0.0.0-factory", InstalledAt: now}
	}
	lock := struct {
		Version int                   `json:"version"`
		Skills  map[string]skillEntry `json:"skills"`
	}{
		Version: 1,
		Skills:  skills,
	}
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	dst := filepath.Join(wsRoot, ".clawhub", "lock.json")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, append(data, '\n'), 0o644)
}

func countOverrides(m map[string]map[string]analyzer.Finding) int {
	n := 0
	for _, byEnv := range m {
		n += len(byEnv)
	}
	return n
}

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
	// skills whitelist filtering
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

	// infrastructure-driven: skip runtime-query skills for disabled data stores
	for _, ds := range g.Ctx.Infrastructure.DataStores {
		if !ds.Enabled && skillName == dataStoreSkillName(ds.Type) {
			return true
		}
	}
	// skip config-executor if no config center
	if skillName == "config-executor" {
		t := g.Ctx.Infrastructure.ConfigCenter.Type
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

func (g *Generator) writeReadme() error {
	content := fmt.Sprintf(`# %s Troubleshooter Agent

由 troubleshooter-factory 生成。系统：%s (%s)

## 快速开始
%s
cd "$(dirname "$0")"
bash scripts/install.sh
bash scripts/self-test.sh
%s

## 卸载
%s
bash scripts/uninstall.sh
%s

## 安装位置
- Agent 工作区：~/.openclaw/workspace/%s
- 配置文件：~/.openclaw/openclaw.json
`,
		g.Ctx.System.Name,
		g.Ctx.System.Name, g.Ctx.System.ID,
		"```bash", "```",
		"```bash", "```",
		g.Ctx.Agent.WorkspaceName,
	)
	return os.WriteFile(filepath.Join(g.OutputDir, "README.md"), []byte(content), 0o644)
}
