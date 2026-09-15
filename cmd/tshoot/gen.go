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
	input := fs.String("i", "", "troubleshooter.yaml 路径 (必填)")
	output := fs.String("o", "", "输出目录 (默认 ./dist)")
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
		outDir = "./dist"
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
		g.TroubleshooterYAMLSource = data
	}
	if *analysisFile != "" {
		if err := g.LoadAnalysis(*analysisFile); err != nil {
			return err
		}
	}
	// 三平台共享一次临时 workspace 渲染，结束后清理 staging。
	targets := cfg.Generation.ResolvedTargets()
	{
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
		if err := g.GenerateTarget(target); err != nil {
			return err
		}
		fmt.Printf("[ok] %s output → %s-%s\n", target, outDir, target)
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
	if g.Summary.PriorOverridesCount > 0 {
		fmt.Printf("[ok] applied %d prior manual override(s)\n", g.Summary.PriorOverridesCount)
	}
	for _, t := range targets {
		fmt.Printf("下一步（%s）：tshoot install --path '%s-%s' --target %s\n", t, outDir, t, t)
	}
	return nil
}
