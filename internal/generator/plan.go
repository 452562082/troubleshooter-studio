package generator

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Plan 描述一次 gen 将要产生的变化与决策，供 `tshoot plan` 输出
type Plan struct {
	System         string              `json:"system"`
	ConfigCenter   string              `json:"config_center"`
	SkillsIncluded []SkillDecision     `json:"skills_included"`
	SkillsSkipped  []SkillDecision     `json:"skills_skipped"`
	FilesCreate    []string            `json:"files_create"`
	FilesModify    []string            `json:"files_modify"`
	FilesRemove    []string            `json:"files_remove"`
	PriorOverrides []OverrideRef       `json:"prior_overrides"`
	AnalyzerHits   []AnalyzerHitRef    `json:"analyzer_hits"`
	ConfigMap      ConfigMapProjection `json:"config_map_projection"`
}

type SkillDecision struct {
	Name   string `json:"name"`
	Reason string `json:"reason,omitempty"`
}

type OverrideRef struct {
	Env     string `json:"env"`
	Service string `json:"service"`
}

type AnalyzerHitRef struct {
	Service string `json:"service"`
	Env     string `json:"env,omitempty"` // 空串=对所有环境生效
	Source  string `json:"source,omitempty"`
}

type ConfigMapProjection struct {
	VerifiedFromAnalyzer int `json:"verified_from_analyzer"`
	VerifiedFromPrior    int `json:"verified_from_prior"`
	Inferred             int `json:"inferred"`
	Total                int `json:"total"`
}

// BuildPlan 基于现有产物 existingDir 干跑一次 gen 到临时目录，回读产出整理出 Plan
// existingDir 为空或不存在时 → 以"首次生成"视角出计划
func (g *Generator) BuildPlan(existingDir string) (*Plan, error) {
	tmp, err := os.MkdirTemp("", "tshoot-plan-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	// 若有现存产物，先复制到 tmp，让 Generate 的 SnapshotExisting 机制在 tmp 上生效
	if existingDir != "" {
		if info, err := os.Stat(existingDir); err == nil && info.IsDir() {
			if err := copyDirForPlan(existingDir, tmp); err != nil {
				return nil, fmt.Errorf("copy existing: %w", err)
			}
		}
	}

	// 克一个新 generator 指向 tmp，保留原 ctx 的 Findings（但 PriorOverrides 会在 Generate 里由 snapshot 填充）
	g.OutputDir = tmp
	if err := g.Generate(); err != nil {
		return nil, err
	}

	plan := &Plan{
		System:       g.Ctx.System.ID,
		ConfigCenter: g.Ctx.Infrastructure.PrimaryConfigCenter().Type,
	}

	// skills included/skipped
	plan.SkillsIncluded, plan.SkillsSkipped = g.skillDecisions()

	// files 三态
	plan.FilesCreate, plan.FilesModify, plan.FilesRemove = diffFileSets(existingDir, tmp)

	// prior overrides（已在 g.Ctx.PriorOverrides 中）
	for svc, byEnv := range g.Ctx.PriorOverrides {
		for env := range byEnv {
			plan.PriorOverrides = append(plan.PriorOverrides, OverrideRef{Env: env, Service: svc})
		}
	}
	sort.Slice(plan.PriorOverrides, func(i, j int) bool {
		a, b := plan.PriorOverrides[i], plan.PriorOverrides[j]
		if a.Env != b.Env {
			return a.Env < b.Env
		}
		return a.Service < b.Service
	})

	// analyzer hits
	for svc, byEnv := range g.Ctx.Findings {
		for env, f := range byEnv {
			plan.AnalyzerHits = append(plan.AnalyzerHits, AnalyzerHitRef{
				Service: svc, Env: env, Source: f.SourceFile,
			})
		}
	}
	sort.Slice(plan.AnalyzerHits, func(i, j int) bool {
		a, b := plan.AnalyzerHits[i], plan.AnalyzerHits[j]
		if a.Service != b.Service {
			return a.Service < b.Service
		}
		return a.Env < b.Env
	})

	// config-map 统计（解析刚生成的 config-map.yaml）
	cmPath := filepath.Join(tmp, "templates", "workspace-template", "skills", "routing", "references", "config-map.yaml")
	plan.ConfigMap = summarizeConfigMap(cmPath)

	return plan, nil
}

// skillDecisions 通过 TemplateRoot 枚举候选 skill + 调 shouldSkipDir 判断
func (g *Generator) skillDecisions() (included []SkillDecision, skipped []SkillDecision) {
	skillsDir := filepath.Join(g.TemplateRoot, "workspace", "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return nil, nil
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		rel := filepath.Join("skills", e.Name())
		if g.shouldSkipDir(rel) {
			skipped = append(skipped, SkillDecision{Name: e.Name(), Reason: skipReason(g, e.Name())})
		} else {
			included = append(included, SkillDecision{Name: e.Name()})
		}
	}
	sort.Slice(included, func(i, j int) bool { return included[i].Name < included[j].Name })
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Name < skipped[j].Name })
	return
}

func skipReason(g *Generator, skill string) string {
	whitelist := g.Ctx.Generation.SkillsWhitelist
	if len(whitelist) > 0 {
		inWL := false
		for _, w := range whitelist {
			if w == skill {
				inWL = true
				break
			}
		}
		if !inWL {
			return "not in skills_whitelist"
		}
	}
	if skill == "code-intelligence-query" && !g.Ctx.CodeIntelligence.UsesCodeGraph() {
		return "code_intelligence.enabled=false"
	}
	for _, ds := range g.Ctx.Infrastructure.DataStores {
		if !ds.Enabled && skill == dataStoreSkillName(ds.Type) {
			return fmt.Sprintf("data_store.%s.enabled=false", ds.Type)
		}
	}
	if skill == "config-executor" {
		t := g.Ctx.Infrastructure.PrimaryConfigCenter().Type
		if t == "" || t == "none" {
			return "config_center.type=none"
		}
	}
	return "skipped"
}

func diffFileSets(oldDir, newDir string) (create, modify, remove []string) {
	oldFiles := listAllFiles(oldDir)
	newFiles := listAllFiles(newDir)
	seen := map[string]bool{}
	for k := range oldFiles {
		seen[k] = true
	}
	for k := range newFiles {
		seen[k] = true
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		oldHash, inOld := oldFiles[k]
		newHash, inNew := newFiles[k]
		switch {
		case !inOld && inNew:
			create = append(create, k)
		case inOld && !inNew:
			remove = append(remove, k)
		case oldHash != newHash:
			modify = append(modify, k)
		}
	}
	return
}

func listAllFiles(root string) map[string]int {
	out := map[string]int{}
	if root == "" {
		return out
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return out
	}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		info, _ := d.Info()
		// 用 size 作为粗略签名，比 sha256 快；足够判增删改三态
		out[rel] = int(info.Size())
		return nil
	})
	return out
}

func summarizeConfigMap(path string) ConfigMapProjection {
	p := ConfigMapProjection{}
	data, err := os.ReadFile(path)
	if err != nil {
		return p
	}
	var root struct {
		Environments map[string]map[string]map[string]yaml.Node `yaml:"environments"`
	}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return p
	}
	for _, services := range root.Environments {
		for _, fields := range services {
			p.Total++
			var status string
			if n, ok := fields["status"]; ok {
				_ = n.Decode(&status)
			}
			switch status {
			case "verified":
				if _, hasSource := fields["source"]; hasSource {
					p.VerifiedFromAnalyzer++
				} else {
					p.VerifiedFromPrior++
				}
			case "inferred":
				p.Inferred++
			}
		}
	}
	return p
}

// copyDirForPlan 浅拷贝（与 CLI main.go 里 copyDir 等价，复制到此避免跨包循环）
func copyDirForPlan(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
