package main

import (
	"flag"
	"fmt"
	"os"

	"encoding/json"
	"path/filepath"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

func runGen(args []string) error {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	input := fs.String("i", "", "system.yaml 路径 (必填)")
	output := fs.String("o", "", "输出目录 (默认: system.yaml 中 generation.output_dir 或 ./dist)")
	tmplDir := fs.String("t", "", "模板根目录 (默认: 可执行文件旁的 templates/)")
	analysisFile := fs.String("analysis", "", "可选：analyzer 产出的 analysis.json，用于升级 config-map 的 inferred 行为 verified")
	format := fs.String("format", "text", "text / json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		fs.Usage()
		return fmt.Errorf("-i is required")
	}

	cfg, err := config.Load(*input)
	if err != nil {
		return err
	}

	outDir := *output
	if outDir == "" {
		outDir = cfg.Generation.OutputDir
	}
	if !filepath.IsAbs(outDir) {
		abs, _ := filepath.Abs(outDir)
		outDir = abs
	}

	tr := *tmplDir
	if tr == "" {
		tr = resolveTemplateDir()
	}
	if _, err := os.Stat(tr); err != nil {
		return fmt.Errorf("template dir not found: %s", tr)
	}

	g := generator.New(cfg, tr, outDir)
	g.TshootVersion = version
	if data, err := os.ReadFile(*input); err == nil {
		g.SystemYAMLSource = data
	}
	if *analysisFile != "" {
		if err := g.LoadAnalysis(*analysisFile); err != nil {
			return err
		}
	}
	// 按 target 生成。多 target 时共享一次 workspace 渲染（staging）：
	//   - 若 targets 含 openclaw，先跑 openclaw → 复用它的产物作为 staging
	//   - 否则建临时 staging，跑一次 Generate()，用完删掉
	// 这样 4 target 全开只渲染 1 次 workspace，而不是 4 次。
	targets := cfg.Generation.ResolvedTargets()
	hasOpenclaw := false
	hasOther := false
	for _, t := range targets {
		switch t {
		case "openclaw":
			hasOpenclaw = true
		case "claude-code", "cursor", "embedded":
			hasOther = true
		}
	}
	if hasOther && !hasOpenclaw {
		stagingDir, err := os.MkdirTemp("", "tshoot-shared-*")
		if err != nil {
			return fmt.Errorf("create staging: %w", err)
		}
		defer os.RemoveAll(stagingDir)
		origOut := g.OutputDir
		g.OutputDir = stagingDir
		if err := g.Generate(); err != nil {
			g.OutputDir = origOut
			return fmt.Errorf("stage workspace: %w", err)
		}
		g.OutputDir = origOut
		g.SharedStaging = stagingDir
	}
	for _, target := range targets {
		switch target {
		case "openclaw":
			if err := g.Generate(); err != nil {
				return err
			}
			if hasOther {
				g.SharedStaging = g.OutputDir
			}
		case "claude-code":
			if err := g.GenerateClaudeCode(); err != nil {
				return err
			}
			fmt.Printf("[ok] claude-code output → %s-claude-code\n", outDir)
		case "cursor":
			if err := g.GenerateCursor(); err != nil {
				return err
			}
			fmt.Printf("[ok] cursor output → %s-cursor\n", outDir)
		case "embedded":
			if err := g.GenerateEmbedded(); err != nil {
				return err
			}
			fmt.Printf("[ok] embedded output → %s-embedded\n", outDir)
		}
	}

	if *format == "json" {
		data, err := json.MarshalIndent(g.Summary, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	// text 模式
	if *analysisFile != "" {
		fmt.Printf("[ok] analysis loaded from %s\n", *analysisFile)
	}
	if g.Summary.PreservedCount > 0 {
		fmt.Printf("[ok] restored %d preserved file(s)\n", g.Summary.PreservedCount)
	}
	if g.Summary.PriorOverridesCount > 0 {
		fmt.Printf("[ok] applied %d prior manual override(s)\n", g.Summary.PriorOverridesCount)
	}
	if hasOpenclaw {
		fmt.Printf("[ok] generated to %s\n", outDir)
		fmt.Printf("下一步：cd '%s' && bash scripts/install.sh\n", outDir)
		fmt.Printf("     或：先看变化 tshoot diff -i <system.yaml>\n")
	} else {
		// openclaw 未在 targets 中：outDir 本身不会被创建，只有 <outDir>-<target>/ 兄弟目录
		fmt.Printf("[ok] generation complete (openclaw target not requested)\n")
		for _, t := range targets {
			if t == "openclaw" {
				continue
			}
			fmt.Printf("下一步（%s）：bash '%s-%s/install.sh'\n", t, outDir, t)
		}
	}
	return nil
}
