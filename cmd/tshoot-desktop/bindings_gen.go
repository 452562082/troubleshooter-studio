// bindings_gen.go —— Generator 一族 binding:Gen / Plan / Diff / GenPreview。
// 把 yaml 实跑 / 干跑 / 干跑 + 落盘预览的三套都收在这里。
//
// 跟 bindings_core.go 同样属于"只读 / 干跑"类(Gen 真写盘但写到用户指定 outDir,不动
// 已装 workspace),按域分文件:bindings_core.go 留 Validate/Doctor 验证类。

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/xiaolong/troubleshooter-studio/internal/agent"
	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
	"github.com/xiaolong/troubleshooter-studio/internal/userconfig"
)

// Gen 按 troubleshooter.yaml 实际落盘生成机器人产物(写到 outputDir;后续要部署还得走
// ImportAndDeploy 或 RunInstall 把产物装到 AI 平台)。
// outputDir 为空时默认 ./dist;相对路径解析成绝对路径,让 UI 能稳定展示"产物在 /abs/path/xxx"。
//
// 自动 analyzer:Gen 内部从 ~/.tshoot/config.json 读 system 对应的本地仓库路径,
// 命中即跑一遍 analyzerpipe.Run,把 findings / dependency_scan / schema_scan 折进
// generator,产物里 service-dependency-map.upstream/downstream + data-schema-map.tables
// 自动填齐(用户走 BotsPage / Editor 部署路径不再"两份 map 全空白手填")。
// 缺失路径用 GetMissingRepoPaths 取,UI 应在 Gen 之前弹 prompt 让用户补。
func (a *App) Gen(yamlText, outputDir string) (*generator.GenSummary, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	if outputDir == "" {
		outputDir = "./dist"
	}
	if !filepath.IsAbs(outputDir) {
		abs, _ := filepath.Abs(outputDir)
		outputDir = abs
	}
	g := generator.New(cfg, a.templateRoot, outputDir)
	g.TshootVersion = version
	g.SystemYAMLSource = []byte(yamlText)

	// auto-analyze:从 userconfig 读已存的本地仓库路径,命中即跑一遍 analyzer。
	// 跑失败 / 路径全空 都不阻塞 gen,只是产物里两份 map 走 fallback(yaml 反推 / 留空)。
	saved := userconfig.GetRepoPathsForSystem(cfg.System.ID)
	if result, aerr := agent.RunAutoAnalyze(agent.RunAutoAnalyzeOptions{
		Cfg:       cfg,
		RepoPaths: saved,
		OnLog: func(msg string) {
			wailsruntime.EventsEmit(a.ctx, "gen:log", msg)
		},
	}); aerr == nil && result != nil {
		g.LoadAnalysisReport(result.Report)
	} else if aerr != nil {
		// 不中断 gen;只是日志告诉用户"分析没跑成,产物某些字段会空"
		wailsruntime.EventsEmit(a.ctx, "gen:log", "[warn] auto-analyze failed: "+aerr.Error())
	}

	if err := g.Generate(); err != nil {
		return nil, err
	}
	return g.Summary, nil
}

// Plan 干跑一次 gen,返回 Plan 结构(skills / files 分布 / config-map 投影)。
// 等价 POST /api/plan。不落盘(generator 写到临时目录后清掉)。
func (a *App) Plan(yamlText string) (*generator.Plan, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	outDir, err := os.MkdirTemp("", "tshoot-plan-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(outDir)

	g := generator.New(cfg, a.templateRoot, outDir)
	return g.BuildPlan("")
}

// Diff 跟 Plan 用同一个底层(generator.BuildPlan),区别是传入 existingDir,
// 让底层 diffFileSets 给出 create / modify / remove 三类文件差异,用于新产物
// vs 现有产物的对比预览。existingDir 为空时等价 Plan。
func (a *App) Diff(yamlText, existingDir string) (*generator.Plan, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	outDir, err := os.MkdirTemp("", "tshoot-diff-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(outDir)

	g := generator.New(cfg, a.templateRoot, outDir)
	return g.BuildPlan(existingDir)
}

// ── GenPreview 一族 ──────────────────────────────────────────────────

// GenPreviewFile 一份产物文件的预览条目。
// Binary=true 时 Content 留空,前端显示"二进制文件,无法预览";
// Truncated=true 时 Content 是截断版本(头 200KB),前端展示头部即可。
//
// Path 加 target 前缀,跟用户最终看到的目录结构对齐:
//
//	openclaw     openclaw/agents/<name>.md(实际落到 ~/.openclaw/workspace/<id>/agents/<name>.md)
//	claude-code  claude-code/agents/<name>.md(实际落到 ~/.claude/agents/<name>.md)
//	cursor       cursor/agents/<name>.md(实际落到 ~/.cursor/agents/<name>.md)
//
// staging 中的 templates/workspace-template/ 前缀已被剥掉,跟用户最终看到的目录结构对齐。
type GenPreviewFile struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Binary    bool   `json:"binary"`
	Truncated bool   `json:"truncated"`
	Content   string `json:"content,omitempty"`
}

// GenPreviewResult 给前端的产物预览数据。复用 Plan 的 skill 决策,加全文件树。
type GenPreviewResult struct {
	System         string                    `json:"system"`
	ConfigCenter   string                    `json:"config_center"`
	Targets        []string                  `json:"targets"`
	SkillsIncluded []generator.SkillDecision `json:"skills_included"`
	SkillsSkipped  []generator.SkillDecision `json:"skills_skipped"`
	Files          []GenPreviewFile          `json:"files"`
}

// GenPreview 按 yaml 的 generation.targets 真跑一遍 generator,把每个 target 的产物
// 都读回来给 UI 预览。跟 cmd/tshoot/gen.go 保持同一逻辑(openclaw staging 复用 / 单
// claude-code 时建临时 staging 等),用户在 EditorPage 看到的就是"真实部署到 AI 平台
// 的内容",不是 staging 中间形态。
//
// payload 控制:单文件 200KB 上限(超出截断),含 NUL 字节视为二进制(只标记不返内容)。
func (a *App) GenPreview(yamlText string) (*GenPreviewResult, error) {
	cfg, err := config.LoadFromBytes([]byte(yamlText))
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "tshoot-preview-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)

	// 主 outDir(openclaw 落地)+ 兄弟目录(<outDir>-claude-code / <outDir>-cursor)
	outDir := filepath.Join(tmp, "openclaw")
	g := generator.New(cfg, a.templateRoot, outDir)
	g.TshootVersion = version
	g.SystemYAMLSource = []byte(yamlText)

	targets := cfg.Generation.ResolvedTargets()
	hasOpenclaw, hasOther := false, false
	for _, t := range targets {
		switch t {
		case "openclaw":
			hasOpenclaw = true
		case "claude-code", "cursor", "codex":
			hasOther = true
		}
	}
	// 没勾 openclaw 但有 claude-code / cursor:跟 cmd/tshoot/gen.go 同样套路,先建临时
	// staging 让 GenerateClaudeCode/Cursor 复用 workspace 渲染,完事即清。
	if hasOther && !hasOpenclaw {
		stagingDir, err := os.MkdirTemp("", "tshoot-preview-staging-*")
		if err != nil {
			return nil, fmt.Errorf("create staging: %w", err)
		}
		defer os.RemoveAll(stagingDir)
		origOut := g.OutputDir
		g.OutputDir = stagingDir
		if err := g.Generate(); err != nil {
			g.OutputDir = origOut
			return nil, fmt.Errorf("stage workspace: %w", err)
		}
		g.OutputDir = origOut
		g.SharedStaging = stagingDir
	}

	// 按 target 各跑一遍,失败任一直接返错(让用户看到 yaml 里某个 target 渲染哪儿挂了)
	for _, target := range targets {
		switch target {
		case "openclaw":
			if err := g.Generate(); err != nil {
				return nil, fmt.Errorf("gen openclaw: %w", err)
			}
			if hasOther {
				g.SharedStaging = g.OutputDir
			}
		case "claude-code":
			if err := g.GenerateClaudeCode(); err != nil {
				return nil, fmt.Errorf("gen claude-code: %w", err)
			}
		case "cursor":
			if err := g.GenerateCursor(); err != nil {
				return nil, fmt.Errorf("gen cursor: %w", err)
			}
		case "codex":
			if err := g.GenerateCodex(); err != nil {
				return nil, fmt.Errorf("gen codex: %w", err)
			}
		}
	}

	res := &GenPreviewResult{
		System:       cfg.System.ID,
		ConfigCenter: cfg.Infrastructure.PrimaryConfigCenter().Type,
		Targets:      targets,
	}
	if plan, err := g.BuildPlan(""); err == nil && plan != nil {
		res.SkillsIncluded = plan.SkillsIncluded
		res.SkillsSkipped = plan.SkillsSkipped
	}

	// 每个 target 对应一个产物根目录;walk 时给每条文件加 target/ 前缀,
	// openclaw 还要剥掉 staging 里的 "templates/workspace-template/" 前缀,
	// 让用户看到的路径就是 ~/.openclaw/workspace/<id>/ 下的真实结构。
	type targetRoot struct {
		target string
		root   string
		strip  string // 走 staging 的需要剥前缀
	}
	var roots []targetRoot
	for _, t := range targets {
		switch t {
		case "openclaw":
			roots = append(roots, targetRoot{target: "openclaw", root: outDir, strip: filepath.Join("templates", "workspace-template")})
		case "claude-code":
			roots = append(roots, targetRoot{target: "claude-code", root: outDir + "-claude-code"})
		case "cursor":
			roots = append(roots, targetRoot{target: "cursor", root: outDir + "-cursor"})
		case "codex":
			roots = append(roots, targetRoot{target: "codex", root: outDir + "-codex"})
		}
	}

	const previewMax = 200 * 1024
	for _, r := range roots {
		if _, err := os.Stat(r.root); err != nil {
			continue // target 没生成成功就跳
		}
		_ = filepath.Walk(r.root, func(p string, info os.FileInfo, walkErr error) error {
			if walkErr != nil || info == nil || info.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(r.root, p)
			if err != nil {
				return nil
			}
			// openclaw:剥 templates/workspace-template/ 前缀;不在那个子树下的(比如根级
			// scripts/install.sh)就照原样保留,加 target 前缀以区分。
			if r.strip != "" && strings.HasPrefix(rel, r.strip+string(filepath.Separator)) {
				rel = rel[len(r.strip)+1:]
			}
			displayPath := filepath.ToSlash(filepath.Join(r.target, rel))

			f := GenPreviewFile{Path: displayPath, Size: info.Size()}
			data, readErr := os.ReadFile(p)
			if readErr != nil {
				res.Files = append(res.Files, f)
				return nil
			}
			for _, b := range data {
				if b == 0 {
					f.Binary = true
					break
				}
			}
			if !f.Binary {
				if int64(len(data)) > previewMax {
					f.Content = string(data[:previewMax])
					f.Truncated = true
				} else {
					f.Content = string(data)
				}
			}
			res.Files = append(res.Files, f)
			return nil
		})
	}
	sort.Slice(res.Files, func(i, j int) bool { return res.Files[i].Path < res.Files[j].Path })
	return res, nil
}
