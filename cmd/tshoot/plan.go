package main

import (
	"flag"
	"fmt"

	"encoding/json"
	"path/filepath"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

func runPlan(args []string) error {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	input := fs.String("i", "", "troubleshooter.yaml 路径 (必填)")
	analysisFile := fs.String("analysis", "", "可选 analysis.json")
	against := fs.String("against", "", "现有产物目录 (默认 ./dist)")
	tmplDir := fs.String("t", "", "模板根目录 (默认: 可执行文件旁的 templates/)")
	format := fs.String("format", "text", "text / json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return fmt.Errorf("-i is required")
	}
	cfg, err := config.Load(*input)
	if err != nil {
		return err
	}
	existingDir := *against
	if existingDir == "" {
		existingDir = "./dist"
	}
	if !filepath.IsAbs(existingDir) {
		existingDir, _ = filepath.Abs(existingDir)
	}

	tr := *tmplDir
	if tr == "" {
		tr = resolveTemplateDir()
	}

	g := generator.New(cfg, tr, existingDir)
	if *analysisFile != "" {
		if err := g.LoadAnalysis(*analysisFile); err != nil {
			return err
		}
	}
	plan, err := g.BuildPlan(existingDir)
	if err != nil {
		return err
	}
	switch *format {
	case "json":
		data, err := json.MarshalIndent(plan, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
	default:
		printPlanText(plan)
		fmt.Println("\n下一步：满意就 tshoot gen -i", *input, "真落盘")
		if len(plan.FilesModify) > 0 || len(plan.FilesRemove) > 0 {
			fmt.Println("     或：先看行级 diff — tshoot diff -i", *input)
		}
	}
	return nil
}

func printPlanText(p *generator.Plan) {
	fmt.Printf("# tshoot plan — system: %s (config_center=%s)\n\n", p.System, p.ConfigCenter)

	fmt.Printf("Skills included (%d):\n", len(p.SkillsIncluded))
	for _, s := range p.SkillsIncluded {
		fmt.Printf("  + %s\n", s.Name)
	}
	if len(p.SkillsSkipped) > 0 {
		fmt.Printf("\nSkills skipped (%d):\n", len(p.SkillsSkipped))
		for _, s := range p.SkillsSkipped {
			fmt.Printf("  - %s  (%s)\n", s.Name, s.Reason)
		}
	}

	fmt.Printf("\nFiles: %d create / %d modify / %d remove\n",
		len(p.FilesCreate), len(p.FilesModify), len(p.FilesRemove))
	const sampleLimit = 8
	printSample := func(label, icon string, xs []string) {
		if len(xs) == 0 {
			return
		}
		fmt.Printf("  %s %s:\n", icon, label)
		n := len(xs)
		limit := min(n, sampleLimit)
		for _, x := range xs[:limit] {
			fmt.Printf("      %s\n", x)
		}
		if n > sampleLimit {
			fmt.Printf("      ...（还有 %d 个）\n", n-sampleLimit)
		}
	}
	printSample("create", "+", p.FilesCreate)
	printSample("modify", "~", p.FilesModify)
	printSample("remove", "-", p.FilesRemove)

	if len(p.PriorOverrides) > 0 {
		fmt.Printf("\nPrior manual overrides (%d):\n", len(p.PriorOverrides))
		for _, o := range p.PriorOverrides {
			fmt.Printf("  · %s/%s\n", o.Env, o.Service)
		}
	}

	if len(p.AnalyzerHits) > 0 {
		fmt.Printf("\nAnalyzer hits (%d):\n", len(p.AnalyzerHits))
		for _, h := range p.AnalyzerHits {
			env := h.Env
			if env == "" {
				env = "<all envs>"
			}
			fmt.Printf("  · %s / %s  ← %s\n", h.Service, env, h.Source)
		}
	}

	cm := p.ConfigMap
	fmt.Printf("\nconfig-map projection: total=%d, verified(analyzer)=%d, verified(prior)=%d, inferred=%d\n",
		cm.Total, cm.VerifiedFromAnalyzer, cm.VerifiedFromPrior, cm.Inferred)
}
