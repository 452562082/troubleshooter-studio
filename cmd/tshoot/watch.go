package main

import (
	"flag"
	"fmt"
	"time"

	"encoding/json"
	"path/filepath"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
	"github.com/xiaolong/troubleshooter-studio/internal/watcher"
)

func runWatch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	input := fs.String("i", "", "troubleshooter.yaml 路径 (必填)")
	output := fs.String("o", "", "现有产物目录 (默认 ./dist)")
	analysisFile := fs.String("analysis", "", "可选 analysis.json")
	tmplDir := fs.String("t", "", "模板根目录 (默认: 可执行文件旁的 templates/)")
	interval := fs.Duration("interval", time.Second, "轮询间隔")
	format := fs.String("format", "text", "text / json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return fmt.Errorf("-i is required")
	}
	tr := *tmplDir
	if tr == "" {
		tr = resolveTemplateDir()
	}

	paths := []string{*input, tr}
	if *analysisFile != "" {
		paths = append(paths, *analysisFile)
	}

	if *format != "json" {
		fmt.Printf("watching: %v (interval=%s)\nCtrl+C 退出\n\n", paths, *interval)
	}

	run := func() {
		now := time.Now().Format("15:04:05")
		emitErr := func(stage string, err error) {
			if *format == "json" {
				data, _ := json.Marshal(map[string]any{"time": now, "stage": stage, "error": err.Error()})
				fmt.Println(string(data))
			} else {
				fmt.Printf("[%s] [error] %s: %v\n\n", now, stage, err)
			}
		}
		cfg, err := config.Load(*input)
		if err != nil {
			emitErr("load", err)
			return
		}
		existingDir := *output
		if existingDir == "" {
			existingDir = "./dist"
		}
		if !filepath.IsAbs(existingDir) {
			existingDir, _ = filepath.Abs(existingDir)
		}
		g := generator.New(cfg, tr, existingDir)
		if *analysisFile != "" {
			if err := g.LoadAnalysis(*analysisFile); err != nil {
				emitErr("analysis", err)
				return
			}
		}
		plan, err := g.BuildPlan(existingDir)
		if err != nil {
			emitErr("plan", err)
			return
		}
		cm := plan.ConfigMap
		if *format == "json" {
			data, _ := json.Marshal(map[string]any{
				"time": now, "stage": "ok",
				"skills_included": len(plan.SkillsIncluded),
				"files_create":    len(plan.FilesCreate),
				"files_modify":    len(plan.FilesModify),
				"files_remove":    len(plan.FilesRemove),
				"prior_overrides": len(plan.PriorOverrides),
				"analyzer_hits":   len(plan.AnalyzerHits),
				"config_map":      cm,
			})
			fmt.Println(string(data))
		} else {
			fmt.Printf("[%s] skills=%d files=%dC/%dM/%dR prior=%d hits=%d config-map=%dV+%dP+%dI\n",
				now,
				len(plan.SkillsIncluded),
				len(plan.FilesCreate), len(plan.FilesModify), len(plan.FilesRemove),
				len(plan.PriorOverrides), len(plan.AnalyzerHits),
				cm.VerifiedFromAnalyzer, cm.VerifiedFromPrior, cm.Inferred)
		}
	}

	w := watcher.New(paths, *interval)
	run() // 立即跑一次，不等变化
	w.Loop(run)
	return nil
}
