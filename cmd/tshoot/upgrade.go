package main

import (
	"flag"
	"fmt"

	"encoding/json"
	"path/filepath"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/upgrade"
)

func runUpgrade(args []string) error {
	fs := flag.NewFlagSet("upgrade", flag.ExitOnError)
	input := fs.String("i", "", "system.yaml 路径 (必填)")
	tmplDir := fs.String("t", "", "模板根目录 (默认: 可执行文件旁的 templates/)")
	analysisFile := fs.String("analysis", "", "可选 analysis.json")
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
	outDir := cfg.Generation.OutputDir
	if !filepath.IsAbs(outDir) {
		outDir, _ = filepath.Abs(outDir)
	}
	tr := *tmplDir
	if tr == "" {
		tr = resolveTemplateDir()
	}

	res, err := upgrade.Run(upgrade.Options{
		Config:       cfg,
		TemplateRoot: tr,
		OutputDir:    outDir,
		AnalysisPath: *analysisFile,
	})
	if err != nil {
		return err
	}

	if *format == "json" {
		data, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	}
	fmt.Printf("[ok] backup → %s\n", res.BackupPath)
	fmt.Printf("schema: %s → %s", res.SchemaFrom, res.SchemaTo)
	if res.SchemaMigrated {
		fmt.Print(" (changed)")
	}
	fmt.Println()
	for _, w := range res.Warnings {
		fmt.Printf("⚠ %s\n", w)
	}
	if s := res.GenSummary; s != nil {
		fmt.Printf("gen: %d skills / %d files / %d preserved / %d prior-overrides\n",
			s.SkillsIncludedCount, s.FilesWritten, s.PreservedCount, s.PriorOverridesCount)
	}
	fmt.Printf("diff: %d file change(s) / %d config-map row change(s)\n",
		res.FilesChanged, res.ConfigMapChanges)
	if res.FilesChanged > 0 {
		fmt.Println("  变化文件（最多 8 行）:")
		n := min(res.FilesChanged, 8)
		for _, f := range res.DiffReport.Files[:n] {
			fmt.Printf("    %s  %s\n", f.Kind, f.RelPath)
		}
		if res.FilesChanged > 8 {
			fmt.Printf("    ...（还有 %d）\n", res.FilesChanged-8)
		}
	}
	fmt.Println()
	if res.FilesChanged == 0 && res.ConfigMapChanges == 0 {
		fmt.Println("下一步：产物已同步，无需部署动作")
	} else {
		fmt.Printf("下一步：tshoot install --path '%s' --target openclaw   # 部署新版\n", outDir)
		fmt.Printf("   回滚：rm -rf '%s' && mv '%s' '%s'\n", outDir, res.BackupPath, outDir)
	}
	return nil
}
