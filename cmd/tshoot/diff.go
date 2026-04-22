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

func runDiff(args []string) error {
	fs := flag.NewFlagSet("diff", flag.ExitOnError)
	input := fs.String("i", "", "system.yaml 路径 (必填)")
	against := fs.String("against", "", "既有产物目录 (默认使用 system.yaml 中 generation.output_dir)")
	analysisFile := fs.String("analysis", "", "可选 analysis.json")
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
		existingDir = cfg.Generation.OutputDir
	}
	if !filepath.IsAbs(existingDir) {
		existingDir, _ = filepath.Abs(existingDir)
	}
	if _, err := os.Stat(existingDir); err != nil {
		return fmt.Errorf("existing output not found: %s", existingDir)
	}

	tr := *tmplDir
	if tr == "" {
		tr = resolveTemplateDir()
	}

	tmp, err := os.MkdirTemp("", "tshoot-diff-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	// 把 existingDir 内容复制到 tmp，让 gen 流程的 snapshot 机制在 tmp 上生效，
	// 这样 diff 能反映"带 preserve 的 gen"后的实际差异，而非裸 gen。
	if err := copyDir(existingDir, tmp); err != nil {
		return fmt.Errorf("copy existing: %w", err)
	}

	g := generator.New(cfg, tr, tmp)
	if *analysisFile != "" {
		if err := g.LoadAnalysis(*analysisFile); err != nil {
			return err
		}
	}
	if err := g.Generate(); err != nil {
		return err
	}
	rep, err := generator.Diff(existingDir, tmp)
	if err != nil {
		return err
	}
	if *format == "json" {
		data, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	printDiffReport(rep)
	if len(rep.Files) == 0 && len(rep.ConfigMapChanges) == 0 {
		fmt.Println("\n下一步：产物已是最新，无需 gen")
	} else {
		fmt.Printf("\n下一步：满意就 tshoot gen -i %s 应用；不满意继续改 system.yaml\n", *input)
	}
	return nil
}

func printDiffReport(rep *generator.DiffReport) {
	if len(rep.Files) == 0 {
		fmt.Println("[files] no changes")
	} else {
		fmt.Printf("[files] %d change(s):\n", len(rep.Files))
		for _, f := range rep.Files {
			icon := "·"
			switch f.Kind {
			case "added":
				icon = "+"
			case "removed":
				icon = "-"
			case "modified":
				icon = "~"
			}
			fmt.Printf("  %s %s\n", icon, f.RelPath)
		}
	}
	if len(rep.ConfigMapChanges) == 0 {
		fmt.Println("[config-map] no row changes")
		return
	}
	fmt.Printf("[config-map] %d row change(s):\n", len(rep.ConfigMapChanges))
	for _, r := range rep.ConfigMapChanges {
		switch r.Kind {
		case "added":
			fmt.Printf("  + %s/%s  [status=%s]\n", r.Env, r.Service, r.NewStatus)
		case "removed":
			fmt.Printf("  - %s/%s  [was status=%s]\n", r.Env, r.Service, r.OldStatus)
		case "status-change":
			icon := "~"
			if r.OldStatus == "verified" && r.NewStatus == "inferred" {
				icon = "⚠"
			}
			fmt.Printf("  %s %s/%s  status: %s → %s\n", icon, r.Env, r.Service, r.OldStatus, r.NewStatus)
		case "fields-change":
			fmt.Printf("  ~ %s/%s  %s\n", r.Env, r.Service, r.Detail)
		}
	}
}
