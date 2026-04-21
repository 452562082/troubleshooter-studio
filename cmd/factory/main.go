package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"net/http"

	tsf "github.com/xiaolong/troubleshooter-factory"
	"github.com/xiaolong/troubleshooter-factory/api"
	"github.com/xiaolong/troubleshooter-factory/internal/analyzer"
	"github.com/xiaolong/troubleshooter-factory/internal/config"
	"github.com/xiaolong/troubleshooter-factory/internal/doctor"
	"github.com/xiaolong/troubleshooter-factory/internal/generator"
	"github.com/xiaolong/troubleshooter-factory/internal/gitclone"
	"github.com/xiaolong/troubleshooter-factory/internal/initwizard"
	"github.com/xiaolong/troubleshooter-factory/internal/skillscaffold"
	"github.com/xiaolong/troubleshooter-factory/internal/upgrade"
	"github.com/xiaolong/troubleshooter-factory/internal/webui"
	"github.com/xiaolong/troubleshooter-factory/internal/watcher"
)

// 通过 -ldflags "-X main.version=v0.2.0 -X main.commit=abcdef" 注入；未注入时保持 dev
var (
	version = "dev"
	commit  = ""
)

func main() {
	if len(os.Args) < 2 {
		printWelcome()
		os.Exit(0)
	}

	switch os.Args[1] {
	case "--version", "-v", "version":
		if commit != "" {
			fmt.Printf("factory %s (%s)\n", version, commit)
		} else {
			fmt.Printf("factory %s\n", version)
		}
		return
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
	case "demo":
		if err := runDemo(os.Args[2:]); err != nil {
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

// printWelcome 在 `factory`（无参）时打印，给第一次接触的人一个清晰的起点。
// 跟 usage() 不同：usage 是完整命令手册，welcome 只告诉用户下一步该做什么。
func printWelcome() {
	fmt.Printf(`troubleshooter-factory — 排障机器人生成器  (version %s)

第一次使用？三种上手方式（按推荐顺序）：
  1) 零配置试跑（看产物长啥样，30 秒）：
       factory demo

  2) 可视化 Web UI（有交互向导 + YAML 编辑器）：
       factory serve --port 8080

  3) 命令行向导生成一份 system.yaml，然后 gen：
       factory init -o system.yaml
       factory gen  -i system.yaml

已有 system.yaml？常用命令：
  factory validate -i system.yaml          # 校验格式
  factory plan     -i system.yaml          # 预览会生成什么
  factory gen      -i system.yaml          # 真落盘
  factory doctor   -i system.yaml          # 检查声明 vs 实态漂移

完整命令列表：factory --help
版本信息：    factory --version
`, version)
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
  factory demo [--keep]                                         # 零配置试跑（用内置 examples 走完整流程）
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
  demo       零配置试跑：用内置 examples/shop-system.yaml + examples/fake-repos 跑完整 pipeline
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
		case "claude-code", "cursor", "standalone":
			hasOther = true
		}
	}
	if hasOther && !hasOpenclaw {
		stagingDir, err := os.MkdirTemp("", "factory-shared-*")
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
	if hasOpenclaw {
		fmt.Printf("[ok] generated to %s\n", outDir)
		fmt.Printf("下一步：cd '%s' && bash scripts/install.sh\n", outDir)
		fmt.Printf("     或：先看变化 factory diff -i <system.yaml>\n")
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
		fmt.Printf("下一步：factory plan -i %s --analysis %s   # 预览 findings 如何应用\n", *input, *output)
		fmt.Printf("     或：factory gen  -i %s --analysis %s   # 直接应用并落盘\n", *input, *output)
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
	if len(rep.Files) == 0 && len(rep.ConfigMapChanges) == 0 {
		fmt.Println("\n下一步：产物已是最新，无需 gen")
	} else {
		fmt.Printf("\n下一步：满意就 factory gen -i %s 应用；不满意继续改 system.yaml\n", *input)
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
		fmt.Println("\n下一步：满意就 factory gen -i", *input, "真落盘")
		if len(plan.FilesModify) > 0 || len(plan.FilesRemove) > 0 {
			fmt.Println("     或：先看行级 diff — factory diff -i", *input)
		}
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
	fix := fs.Bool("fix", false, "对机器可修复的 issue 生成 yaml patch，显示 diff 并询问是否写回（自动备份为 .bak.<ts>）")
	yes := fs.Bool("y", false, "配合 --fix 使用：跳过交互确认直接写回")
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

	if *fix {
		if err := applyDoctorFixes(*input, rep.Issues, *yes); err != nil {
			return 1, err
		}
	}

	errs, _, _ := rep.Counts()
	if errs > 0 {
		return 2, nil
	}
	return 0, nil
}

// applyDoctorFixes 读 system.yaml、对所有有 FixKey 的 issue 生成 patch，
// 让用户 review 一遍，确认后走行级精确替换写回，并备份原文件。
func applyDoctorFixes(yamlPath string, issues []doctor.Issue, skipConfirm bool) error {
	patches, err := doctor.PlanFixes(yamlPath, issues)
	if err != nil {
		return fmt.Errorf("plan fixes: %w", err)
	}
	if len(patches) == 0 {
		fmt.Println("\n[fix] 无机器可修复的 issue（missing-repo / origin-mismatch / service-drift 等只能人工处理）")
		return nil
	}
	fmt.Println("\n[fix] 将应用以下 patch:")
	for _, p := range patches {
		fmt.Printf("  line %d  %s:  %s  →  %s    (来自 %s)\n", p.Line, p.Path, p.From, p.To, p.FromIssue)
	}
	if !skipConfirm {
		fmt.Print("\n确认写回 " + yamlPath + " ？[y/N]: ")
		var ans string
		fmt.Scanln(&ans)
		ans = strings.ToLower(strings.TrimSpace(ans))
		if ans != "y" && ans != "yes" {
			fmt.Println("[fix] 已取消，未改动文件")
			return nil
		}
	}
	backup, err := doctor.ApplyAndWrite(yamlPath, patches)
	if err != nil {
		return fmt.Errorf("write %s: %w", yamlPath, err)
	}
	fmt.Printf("[ok] 已写回 %s（%d 项）；备份 %s\n", yamlPath, len(patches), backup)
	fmt.Println("下一步：factory validate -i " + yamlPath + " 确认无误，再 factory upgrade 重生成")
	return nil
}

func printDoctorText(rep *doctor.Report) {
	errs, warns, infos := rep.Counts()
	if len(rep.Issues) == 0 {
		fmt.Println("[ok] 无漂移 — system.yaml 与代码一致")
		fmt.Println("下一步：放心 factory gen 生成部署包")
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
	switch {
	case errs > 0:
		fmt.Println("下一步：按每条 ↳ 建议修正 system.yaml，然后 factory upgrade 重跑 + 对比 diff")
	case warns > 0:
		fmt.Println("下一步：可选修正上述 warning（或暂时忽略），factory gen 仍可照常进行")
	default:
		fmt.Println("下一步：info 级别无需处理；factory gen 正常")
	}
}

func runInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	output := fs.String("o", "system.yaml", "输出 system.yaml 路径")
	input := fs.String("i", "", "可选：已有 system.yaml，用作字段预填（改动哪里就回答哪里，其余回车接受）")
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

	// 预填来源优先级：-i > ~/.factory/init-draft.yaml > 无
	draftPath := filepath.Join(initDraftDir(), "init-draft.yaml")
	if *input != "" {
		cfg, err := config.Load(*input)
		if err != nil {
			return fmt.Errorf("load -i %s: %w", *input, err)
		}
		w.Defaults = answersFromConfig(cfg)
		fmt.Printf("[prefill] 从 %s 预填字段；回车接受已有值\n\n", *input)
	} else if info, err := os.Stat(draftPath); err == nil {
		fmt.Printf("[draft] 检测到上次中断的草稿 %s（%s 前）\n", draftPath, humanSince(info.ModTime()))
		fmt.Print("  继续用它作为预填？[Y/n]: ")
		var ans string
		fmt.Scanln(&ans)
		ans = strings.ToLower(strings.TrimSpace(ans))
		if ans == "" || ans == "y" || ans == "yes" {
			if cfg, err := config.Load(draftPath); err == nil {
				w.Defaults = answersFromConfig(cfg)
				fmt.Println()
			} else {
				fmt.Fprintf(os.Stderr, "  草稿解析失败（忽略）：%v\n\n", err)
			}
		} else {
			_ = os.Remove(draftPath)
			fmt.Println("  已丢弃草稿")
			fmt.Println()
		}
	}

	// signal handler：Ctrl+C 把当前快照落盘为 draft，下次可续
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		if snap := w.Snapshot(); snap != nil {
			_ = os.MkdirAll(initDraftDir(), 0o755)
			if f, err := os.Create(draftPath); err == nil {
				_ = snap.WriteYAML(f)
				_ = f.Close()
				fmt.Fprintf(os.Stderr, "\n[中断] 已保存草稿 → %s\n  下次 factory init 会询问是否继续\n", draftPath)
			}
		} else {
			fmt.Fprintln(os.Stderr, "\n[中断]")
		}
		os.Exit(130)
	}()

	ans, err := w.Run()
	if err != nil {
		return err
	}
	// 正常完成 → 删除草稿
	_ = os.Remove(draftPath)

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

func initDraftDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".factory")
	}
	return filepath.Join(os.TempDir(), "factory")
}

func humanSince(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时", int(d.Hours()))
	default:
		return fmt.Sprintf("%d 天", int(d.Hours()/24))
	}
}

// answersFromConfig 把已有 SystemConfig 转成 init 向导的 Answers，供 -i 预填或续 draft 使用。
// 未显式声明的字段走 wizard 的原生默认。
func answersFromConfig(cfg *config.SystemConfig) *initwizard.Answers {
	a := &initwizard.Answers{
		SystemID:             cfg.System.ID,
		SystemName:           cfg.System.Name,
		SystemDescription:    cfg.System.Description,
		AgentName:            cfg.Agent.Name,
		AgentModel:           cfg.Agent.Model,
		WorkspaceName:        cfg.Agent.WorkspaceName,
		ConfigCenterType:     cfg.Infrastructure.ConfigCenter.Type,
		GrafanaEnabled:       cfg.Infrastructure.Observability.Grafana.Enabled,
		LokiEnabled:          cfg.Infrastructure.Observability.Loki.Enabled,
		PrometheusEnabled:    cfg.Infrastructure.Observability.Prometheus.Enabled,
		DataStoresEnabled:    map[string]bool{},
		OutputDir:            cfg.Generation.OutputDir,
		Targets:              cfg.Generation.Targets,
		FeishuProjectEnabled: false,
	}
	for _, e := range cfg.Environments {
		a.Envs = append(a.Envs, initwizard.EnvAnswer{ID: e.ID, APIDomain: e.APIDomain, IsProd: e.IsProd})
	}
	for _, r := range cfg.Repos {
		branches := map[string]string{}
		for k, v := range r.EnvBranches {
			branches[k] = v
		}
		a.Repos = append(a.Repos, initwizard.RepoAnswer{
			Name: r.Name, URL: r.URL, Role: r.Role, Stack: r.Stack, Framework: r.Framework,
			ServiceNames: r.ServiceNames, EnvBranches: branches,
		})
	}
	for _, ds := range cfg.Infrastructure.DataStores {
		a.DataStoresEnabled[ds.Type] = ds.Enabled
	}
	for _, m := range cfg.Infrastructure.Messaging {
		if m.Platform == "lark" && m.Enabled {
			a.LarkEnabled = true
			a.LarkAttachment = m.AttachmentSend
		}
	}
	for _, pt := range cfg.Infrastructure.ProjectTracking {
		if pt.Platform == "feishu_project" && pt.Enabled {
			a.FeishuProjectEnabled = true
		}
	}
	return a
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
		fmt.Printf("下一步：cd '%s' && bash scripts/install.sh   # 部署新版\n", outDir)
		fmt.Printf("   回滚：rm -rf '%s' && mv '%s' '%s'\n", outDir, res.BackupPath, outDir)
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
	router := api.NewRouter(srv, webui.Distribution())
	addr := fmt.Sprintf(":%d", *port)
	fmt.Printf("Web UI: http://localhost%s\n", addr)
	fmt.Printf("API:    http://localhost%s/api/\n", addr)
	fmt.Println("前端开发模式: cd web && npm run dev （Vite 会 proxy /api 到此端口）")
	fmt.Println("Ctrl+C 退出")
	return http.ListenAndServe(addr, router)
}

// runDemo 是"零配置试跑":用内置 examples/shop-system.yaml + examples/fake-repos 跑完整
// pipeline（validate → analyze → plan → gen），产出一个临时目录，打印产物树 + 关键下一步。
// 目的是让新用户 30 秒内看到 factory 能干什么，无需准备任何输入。
func runDemo(args []string) error {
	fs := flag.NewFlagSet("demo", flag.ExitOnError)
	keep := fs.Bool("keep", false, "保留 demo 目录，不自动清理")
	sysFlag := fs.String("i", "", "可选：自定义 system.yaml（默认 examples/shop-system.yaml）")
	reposFlag := fs.String("repos-root", "", "可选：自定义 repos 根目录（默认 examples/fake-repos）")
	if err := fs.Parse(args); err != nil {
		return err
	}

	tmplRoot := resolveTemplateDir()
	examplesDir := resolveExamplesDir()

	sysPath := *sysFlag
	if sysPath == "" {
		sysPath = filepath.Join(examplesDir, "shop-system.yaml")
	}
	reposRoot := *reposFlag
	if reposRoot == "" {
		reposRoot = filepath.Join(examplesDir, "fake-repos")
	}
	if _, err := os.Stat(sysPath); err != nil {
		return fmt.Errorf("demo system.yaml 未找到: %s (%w)\n提示：templates / examples 都优先从可执行文件旁 / CWD 取；都不在会从二进制内嵌的 embed.FS extract 出来。", sysPath, err)
	}
	if _, err := os.Stat(reposRoot); err != nil {
		return fmt.Errorf("demo repos-root 未找到: %s (%w)", reposRoot, err)
	}

	demoDir, err := os.MkdirTemp("", "factory-demo-*")
	if err != nil {
		return err
	}
	if !*keep {
		defer func() {
			fmt.Printf("\n[cleanup] rm -rf %s（用 --keep 可保留）\n", demoDir)
			_ = os.RemoveAll(demoDir)
		}()
	}

	fmt.Println("=== factory demo ===")
	fmt.Printf("  system.yaml: %s\n", sysPath)
	fmt.Printf("  repos-root:  %s\n", reposRoot)
	fmt.Printf("  demo out:    %s\n", demoDir)

	// 1) validate（config.Load 会做结构校验）
	cfg, err := config.Load(sysPath)
	if err != nil {
		return fmt.Errorf("[1/3] validate 失败: %w", err)
	}
	fmt.Println("\n[1/3] validate ✓ system.yaml 结构合法")

	// 改写 output_dir 到 demo 目录
	outDir := filepath.Join(demoDir, "out")
	cfg.Generation.OutputDir = outDir

	// 2) plan（干跑，不写盘，展示将生成什么）
	g := generator.New(cfg, tmplRoot, outDir)
	planRes, err := g.BuildPlan("")
	if err != nil {
		return fmt.Errorf("[2/3] plan: %w", err)
	}
	fmt.Printf("[2/3] plan     ✓ %d 个 skill / %d 个待创建文件\n",
		len(planRes.SkillsIncluded), len(planRes.FilesCreate))

	// 3) gen（真落盘到 demoDir/out）。
	// 注意：BuildPlan 会把 g.OutputDir 改成它自己的临时目录，需要在 Generate 前恢复。
	g.OutputDir = outDir
	if err := g.Generate(); err != nil {
		return fmt.Errorf("[3/3] gen: %w", err)
	}
	s := g.Summary
	fmt.Printf("[3/3] gen      ✓ 产物写入 %s (%d 个文件)\n", outDir, s.FilesWritten)

	// 打印产物目录树(2 层深度)
	fmt.Println("\n产物概览:")
	printTree(outDir, "", 2)

	// 打开指引
	fmt.Println("\n── 看看 factory 做了什么 ────────────────────────────────────────")
	fmt.Printf("  cat '%s/scripts/install.sh' | head -80       # 安装脚本\n", outDir)
	fmt.Printf("  cat '%s/templates/workspace-template/IDENTITY.md'   # 机器人身份 + 典型求助示例\n", outDir)
	fmt.Printf("  cat '%s/templates/workspace-template/skills/routing/SKILL.md'  # 主 skill\n", outDir)
	fmt.Println()
	fmt.Println("  想看 analyze 从 fake-repos 抽到什么配置线索：")
	fmt.Printf("    %s analyze -i %s --repos-root %s\n", os.Args[0], sysPath, reposRoot)
	fmt.Println()
	fmt.Println("  想看 multi-target（claude-code/cursor/standalone）各长啥样：")
	fmt.Println("    在自己的 system.yaml 的 generation.targets 里加上它们，再跑 factory gen")
	_ = reposRoot // 预留给未来 analyze/doctor 集成
	return nil
}

// printTree 打印目录树，depth 控制深度（0=只打当前层）
func printTree(root, prefix string, depth int) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for i, e := range entries {
		last := i == len(entries)-1
		branch := "├── "
		childPre := prefix + "│   "
		if last {
			branch = "└── "
			childPre = prefix + "    "
		}
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		fmt.Println(prefix + branch + name)
		if e.IsDir() && depth > 0 {
			printTree(filepath.Join(root, e.Name()), childPre, depth-1)
		}
	}
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
	fmt.Printf("下一步：factory plan -i %s                 # 预览会生成什么\n", *input)
	fmt.Printf("     或：factory gen  -i %s                 # 直接落盘\n", *input)
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

// resolveTemplateDir 按优先级定位 templates 根目录：
//  1. 可执行文件旁的 templates/
//  2. 当前工作目录下的 templates/
//  3. 以上都不在（例如 `go install` 出来的二进制 + 在任意目录跑）→ 从 embed.FS
//     extract 到一个每进程复用的临时目录
func resolveTemplateDir() string {
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "templates")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if wd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(wd, "templates")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	// embed fallback
	if dir, err := extractEmbeddedTemplates(); err == nil {
		return dir
	}
	// 最后兜底，返回一个不存在的路径，让调用方的 os.Stat 报清晰错误
	wd, _ := os.Getwd()
	return filepath.Join(wd, "templates")
}

// resolveExamplesDir 对齐 resolveTemplateDir，但负责 examples/ 根目录。
func resolveExamplesDir() string {
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "examples")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if wd, err := os.Getwd(); err == nil {
		candidate := filepath.Join(wd, "examples")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if dir, err := extractEmbeddedExamples(); err == nil {
		return dir
	}
	wd, _ := os.Getwd()
	return filepath.Join(wd, "examples")
}

// extract 缓存：同一进程 extract 一次、复用；避免 serve 场景每次请求都 extract
var (
	embeddedTemplatesDir string
	embeddedExamplesDir  string
)

func extractEmbeddedTemplates() (string, error) {
	if embeddedTemplatesDir != "" {
		if _, err := os.Stat(embeddedTemplatesDir); err == nil {
			return embeddedTemplatesDir, nil
		}
	}
	dir, err := extractEmbeddedFS(tsf.TemplatesFS, "templates", "factory-templates-")
	if err != nil {
		return "", err
	}
	embeddedTemplatesDir = dir
	return dir, nil
}

func extractEmbeddedExamples() (string, error) {
	if embeddedExamplesDir != "" {
		if _, err := os.Stat(embeddedExamplesDir); err == nil {
			return embeddedExamplesDir, nil
		}
	}
	dir, err := extractEmbeddedFS(tsf.ExamplesFS, "examples", "factory-examples-")
	if err != nil {
		return "", err
	}
	embeddedExamplesDir = dir
	return dir, nil
}

// extractEmbeddedFS 把 embed.FS 里 rootSubdir 下的所有文件写到一个新的 tmp 目录，返回该目录路径。
// 跳过 .DS_Store 之类的隐藏系统文件。
func extractEmbeddedFS(fsys embed.FS, rootSubdir, tmpPrefix string) (string, error) {
	tmp, err := os.MkdirTemp("", tmpPrefix+"*")
	if err != nil {
		return "", err
	}
	err = fs.WalkDir(fsys, rootSubdir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(rootSubdir, p)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		if strings.HasSuffix(rel, ".DS_Store") {
			return nil
		}
		target := filepath.Join(tmp, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(rel, ".sh") || strings.HasSuffix(rel, ".py") {
			mode = 0o755
		}
		return os.WriteFile(target, data, mode)
	})
	if err != nil {
		_ = os.RemoveAll(tmp)
		return "", fmt.Errorf("extract embed %s: %w", rootSubdir, err)
	}
	return tmp, nil
}
