package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"net/http"

	"github.com/xiaolong/troubleshooter-factory/api"
	"github.com/xiaolong/troubleshooter-factory/internal/analyzer"
	"github.com/xiaolong/troubleshooter-factory/internal/config"
	"github.com/xiaolong/troubleshooter-factory/internal/doctor"
	"github.com/xiaolong/troubleshooter-factory/internal/generator"
	"github.com/xiaolong/troubleshooter-factory/internal/gitclone"
	"github.com/xiaolong/troubleshooter-factory/internal/initwizard"
	"github.com/xiaolong/troubleshooter-factory/internal/skillscaffold"
	"github.com/xiaolong/troubleshooter-factory/internal/upgrade"
	"github.com/xiaolong/troubleshooter-factory/internal/watcher"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "gen", "generate":
		if err := runGen(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "validate":
		if err := runValidate(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "analyze":
		if err := runAnalyze(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "diff":
		if err := runDiff(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "init":
		if err := runInit(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "doctor":
		code, err := runDoctor(os.Args[2:])
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		os.Exit(code)
	case "plan":
		if err := runPlan(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "skill":
		if err := runSkill(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "watch":
		if err := runWatch(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "serve":
		if err := runServe(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "upgrade":
		if err := runUpgrade(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println(`troubleshooter-factory — 排障机器人生成器

用法:
  factory init [-o <system.yaml>]                          # 交互向导生成 system.yaml
  factory gen -i <system.yaml> [-o <output_dir>] [-t <template_dir>] [--analysis <analysis.json>]
  factory plan -i <system.yaml> [--analysis <analysis.json>] [--against <dir>] [--format=text|json]
  factory watch -i <system.yaml> [--analysis <analysis.json>] [--interval 1s]
  factory analyze -i <system.yaml> --repos-root <dir> [-o <analysis.json>] [--auto-clone] [--branch <name>]
  factory doctor -i <system.yaml> [--repos-root <dir>] [--format=text|json]
  factory diff -i <system.yaml> [--analysis <analysis.json>] [--against <dir>]
  factory upgrade -i <system.yaml> [--analysis <analysis.json>] [--format=text|json]
  factory skill new <name> [-t <template_dir>] [--description "..."] [--with-scripts] [--with-references]
  factory serve [--port 8080] [-t <template_dir>]              # 启动 Web UI
  factory validate -i <system.yaml>

子命令:
  init       交互式问答生成一份最小可用 system.yaml
  gen        基于 system.yaml 生成部署包（保留 preserve_on_regenerate 文件与人工 verified 行）
  plan       干跑一次 gen，展示将生成/保留/应用的内容与 config-map 分布（不写盘）
  watch      文件变化时自动重跑 plan（system.yaml / templates/ / analysis.json）
  analyze    扫描已 clone 的仓库，抽取 service_names 与配置中心线索
  doctor     对比 system.yaml 声明与 analyzer 实测，报告漂移
  diff       预览本次生成相对现有产物的变化（不写盘）
  upgrade    备份现有产物到 <out>.bak.<ts>，重跑 gen（保留人工行），输出 diff
  serve      启动 Web UI（HTTP API + 前端界面）
  skill      skill 脚手架（skill new <name> 在模板库里生成新 skill 骨架）
  validate   仅校验 system.yaml`)
}

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
	if *analysisFile != "" {
		if err := g.LoadAnalysis(*analysisFile); err != nil {
			return err
		}
	}
	// 按 target 生成
	targets := cfg.Generation.ResolvedTargets()
	for _, target := range targets {
		switch target {
		case "openclaw":
			if err := g.Generate(); err != nil {
				return err
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
		case "standalone":
			if err := g.GenerateStandalone(); err != nil {
				return err
			}
			fmt.Printf("[ok] standalone output → %s-standalone\n", outDir)
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
	fmt.Printf("[ok] generated to %s\n", outDir)
	return nil
}

func runAnalyze(args []string) error {
	fs := flag.NewFlagSet("analyze", flag.ExitOnError)
	input := fs.String("i", "", "system.yaml 路径 (必填)")
	reposRoot := fs.String("repos-root", "", "仓库 checkout 根目录 (必填)；每个仓库在其下以 repos[].name 命名")
	output := fs.String("o", "analysis.json", "输出文件路径")
	autoClone := fs.Bool("auto-clone", false, "缺仓库时自动 shallow clone (需要 git + 凭证)")
	cloneBranch := fs.String("branch", "", "auto-clone 时的分支名 (默认:第一个环境对应分支 > 仓库默认分支)")
	format := fs.String("format", "text", "text / json (stdout 摘要的格式，不影响 -o 输出文件)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" || *reposRoot == "" {
		fs.Usage()
		return fmt.Errorf("-i and --repos-root are required")
	}
	cfg, err := config.Load(*input)
	if err != nil {
		return err
	}
	if *autoClone {
		if err := os.MkdirAll(*reposRoot, 0o755); err != nil {
			return fmt.Errorf("mkdir repos-root: %w", err)
		}
	}

	reg := analyzer.NewRegistry(cfg.Infrastructure.ConfigCenter.Type)
	report := analyzer.Report{SchemaVersion: "0.1", ConfigCenter: cfg.Infrastructure.ConfigCenter.Type}
	type repoSummary struct {
		Name             string `json:"name"`
		Status           string `json:"status"` // analyzed / cloned-then-analyzed / skipped / clone-failed
		ServiceNameCount int    `json:"service_name_count"`
		FindingCount     int    `json:"finding_count"`
		Error            string `json:"error,omitempty"`
	}
	var perRepo []repoSummary
	text := *format != "json"

	for _, repo := range cfg.Repos {
		repoPath := filepath.Join(*reposRoot, repo.Name)
		status := "analyzed"
		if _, err := os.Stat(repoPath); err != nil {
			if !*autoClone {
				perRepo = append(perRepo, repoSummary{Name: repo.Name, Status: "skipped", Error: "not-found"})
				if text {
					fmt.Fprintf(os.Stderr, "[skip] repo %s not found at %s (use --auto-clone to fetch)\n", repo.Name, repoPath)
				}
				continue
			}
			branch := pickCloneBranch(*cloneBranch, repo, cfg.Environments)
			if text {
				fmt.Printf("[clone] %s → %s (branch=%s, depth=%d)\n", repo.URL, repoPath, orDefault(branch, "<default>"), repo.Analysis.ShallowDepth)
			}
			if err := gitclone.Clone(gitclone.Options{
				URL:    repo.URL,
				Dest:   repoPath,
				Branch: branch,
				Depth:  repo.Analysis.ShallowDepth,
				Stderr: os.Stderr,
			}); err != nil {
				perRepo = append(perRepo, repoSummary{Name: repo.Name, Status: "clone-failed", Error: err.Error()})
				if text {
					fmt.Fprintf(os.Stderr, "[skip] clone %s failed: %v\n", repo.Name, err)
				}
				continue
			}
			status = "cloned-then-analyzed"
		}
		a, err := reg.Get(repo.Stack)
		if err != nil {
			perRepo = append(perRepo, repoSummary{Name: repo.Name, Status: "skipped", Error: err.Error()})
			if text {
				fmt.Fprintf(os.Stderr, "[skip] %s: %v\n", repo.Name, err)
			}
			continue
		}
		ra, err := a.Analyze(repoPath, repo.Analysis.IncludePaths)
		if err != nil {
			return fmt.Errorf("analyze %s: %w", repo.Name, err)
		}
		ra.Name = repo.Name
		report.Repos = append(report.Repos, *ra)
		perRepo = append(perRepo, repoSummary{
			Name: repo.Name, Status: status,
			ServiceNameCount: len(ra.ServiceNames),
			FindingCount:     len(ra.Findings),
		})
		if text {
			fmt.Printf("[ok] analyzed %s: %d service_names, %d findings\n",
				repo.Name, len(ra.ServiceNames), len(ra.Findings))
		}
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*output, data, 0o644); err != nil {
		return err
	}

	if *format == "json" {
		summary := map[string]any{
			"output":        *output,
			"config_center": cfg.Infrastructure.ConfigCenter.Type,
			"repos":         perRepo,
		}
		out, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(out))
	} else {
		fmt.Printf("[ok] analysis written to %s\n", *output)
	}
	return nil
}

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

	tmp, err := os.MkdirTemp("", "factory-diff-*")
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

func runPlan(args []string) error {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	input := fs.String("i", "", "system.yaml 路径 (必填)")
	analysisFile := fs.String("analysis", "", "可选 analysis.json")
	against := fs.String("against", "", "现有产物目录 (默认使用 system.yaml 中 generation.output_dir)")
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
	}
	return nil
}

func printPlanText(p *generator.Plan) {
	fmt.Printf("# factory plan — system: %s (config_center=%s)\n\n", p.System, p.ConfigCenter)

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
		limit := n
		if limit > sampleLimit {
			limit = sampleLimit
		}
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

	if len(p.Preserved) > 0 {
		fmt.Printf("\nPreserved from existing (%d):\n", len(p.Preserved))
		for _, f := range p.Preserved {
			fmt.Printf("  · %s\n", f)
		}
	}

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

func runDoctor(args []string) (int, error) {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	input := fs.String("i", "", "system.yaml 路径 (必填)")
	reposRoot := fs.String("repos-root", "", "仓库 checkout 根目录 (可选，留空则只做静态检查)")
	format := fs.String("format", "text", "text / json")
	if err := fs.Parse(args); err != nil {
		return 1, err
	}
	if *input == "" {
		return 1, fmt.Errorf("-i is required")
	}
	cfg, err := config.Load(*input)
	if err != nil {
		return 1, err
	}
	rep, err := doctor.Check(cfg, *reposRoot)
	if err != nil {
		return 1, err
	}
	switch *format {
	case "json":
		data, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			return 1, err
		}
		fmt.Println(string(data))
	default:
		printDoctorText(rep)
	}
	errs, _, _ := rep.Counts()
	if errs > 0 {
		return 2, nil
	}
	return 0, nil
}

func printDoctorText(rep *doctor.Report) {
	errs, warns, infos := rep.Counts()
	if len(rep.Issues) == 0 {
		fmt.Println("[ok] 无漂移 — system.yaml 与代码一致")
		return
	}
	for _, i := range rep.Issues {
		icon := "·"
		switch i.Severity {
		case doctor.SeverityError:
			icon = "✘"
		case doctor.SeverityWarning:
			icon = "⚠"
		case doctor.SeverityInfo:
			icon = "ℹ"
		}
		fmt.Printf("%s [%s] %s\n", icon, i.Category, i.Target)
		fmt.Printf("   %s\n", i.Message)
		if i.Suggest != "" {
			fmt.Printf("   ↳ 建议：%s\n", i.Suggest)
		}
	}
	fmt.Printf("\n合计：%d error / %d warning / %d info\n", errs, warns, infos)
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	output := fs.String("o", "system.yaml", "输出 system.yaml 路径")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// 覆盖保护
	if _, err := os.Stat(*output); err == nil {
		fmt.Printf("%s 已存在。覆盖吗？[y/N]: ", *output)
		var ans string
		fmt.Scanln(&ans)
		ans = strings.ToLower(strings.TrimSpace(ans))
		if ans != "y" && ans != "yes" {
			return fmt.Errorf("aborted by user")
		}
	}
	w := initwizard.New(os.Stdin, os.Stdout)
	ans, err := w.Run()
	if err != nil {
		return err
	}
	f, err := os.Create(*output)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := ans.WriteYAML(f); err != nil {
		return err
	}
	fmt.Printf("\n[ok] wrote %s\n", *output)
	fmt.Println("下一步:")
	fmt.Printf("  factory validate -i %s\n", *output)
	fmt.Printf("  factory gen -i %s\n", *output)
	return nil
}

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
		n := res.FilesChanged
		if n > 8 {
			n = 8
		}
		for _, f := range res.DiffReport.Files[:n] {
			fmt.Printf("    %s  %s\n", f.Kind, f.RelPath)
		}
		if res.FilesChanged > 8 {
			fmt.Printf("    ...（还有 %d）\n", res.FilesChanged-8)
		}
	}
	return nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 8080, "HTTP 监听端口")
	tmplDir := fs.String("t", "", "模板根目录 (默认: 可执行文件旁的 templates/)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tr := *tmplDir
	if tr == "" {
		tr = resolveTemplateDir()
	}
	srv := &api.Server{TemplateRoot: tr}
	router := api.NewRouter(srv, nil) // nil = 开发模式，不 embed 前端
	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("Web UI: http://localhost%s\n", addr)
	fmt.Printf("API:    http://localhost%s/api/\n", addr)
	fmt.Println("前端开发模式: cd web && npm run dev （Vite 会 proxy /api 到此端口）")
	fmt.Println("Ctrl+C 退出")
	return http.ListenAndServe(addr, router)
}

func runSkill(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: factory skill new <name> [flags]")
	}
	switch args[0] {
	case "new":
		return runSkillNew(args[1:])
	default:
		return fmt.Errorf("unknown skill subcommand: %s (supported: new)", args[0])
	}
}

func runSkillNew(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("skill name required: factory skill new <name> [flags]")
	}
	name := args[0]
	fs := flag.NewFlagSet("skill new", flag.ExitOnError)
	tmplDir := fs.String("t", "", "模板根目录 (默认: 可执行文件旁或 CWD 的 templates/)")
	desc := fs.String("description", "", "skill 一行描述，填 front matter 的 description")
	withScripts := fs.Bool("with-scripts", false, "创建 scripts/ 目录 + README 占位")
	withRefs := fs.Bool("with-references", false, "创建 references/ 目录 + README 占位")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	tr := *tmplDir
	if tr == "" {
		tr = resolveTemplateDir()
	}
	dst, err := skillscaffold.New(skillscaffold.Options{
		TemplateRoot: tr,
		Name:         name,
		Description:  *desc,
		WithScripts:  *withScripts,
		WithRefs:     *withRefs,
	})
	if err != nil {
		return err
	}
	fmt.Printf("[ok] scaffolded skill → %s\n", dst)
	fmt.Println("下一步:")
	fmt.Println("  1) 编辑 SKILL.md.tmpl 补全描述与执行流程")
	fmt.Printf("  2) 在 system.yaml 的 generation.skills_whitelist 中加入 \"%s\"\n", name)
	fmt.Println("  3) factory plan -i system.yaml 预览")
	return nil
}

func runWatch(args []string) error {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	input := fs.String("i", "", "system.yaml 路径 (必填)")
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
		existingDir := cfg.Generation.OutputDir
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
				"preserved":       len(plan.Preserved),
				"prior_overrides": len(plan.PriorOverrides),
				"analyzer_hits":   len(plan.AnalyzerHits),
				"config_map":      cm,
			})
			fmt.Println(string(data))
		} else {
			fmt.Printf("[%s] skills=%d files=%dC/%dM/%dR preserved=%d prior=%d hits=%d config-map=%dV+%dP+%dI\n",
				now,
				len(plan.SkillsIncluded),
				len(plan.FilesCreate), len(plan.FilesModify), len(plan.FilesRemove),
				len(plan.Preserved), len(plan.PriorOverrides), len(plan.AnalyzerHits),
				cm.VerifiedFromAnalyzer, cm.VerifiedFromPrior, cm.Inferred)
		}
	}

	w := watcher.New(paths, *interval)
	run() // 立即跑一次，不等变化
	w.Loop(run)
	return nil
}

func runValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	input := fs.String("i", "", "system.yaml 路径 (必填)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return fmt.Errorf("-i is required")
	}
	_, err := config.Load(*input)
	if err != nil {
		return err
	}
	fmt.Println("[ok] system.yaml is valid")
	return nil
}

// pickCloneBranch 按优先级选分支：CLI 显式 > 第一个 env 的 branch > "" (=远端默认)
func pickCloneBranch(cliBranch string, repo config.Repo, envs []config.Environment) string {
	if cliBranch != "" {
		return cliBranch
	}
	for _, env := range envs {
		if b, ok := repo.EnvBranches[env.ID]; ok && b != "" {
			return b
		}
	}
	return ""
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}

func resolveTemplateDir() string {
	exe, err := os.Executable()
	if err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "templates")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, "templates")
}
