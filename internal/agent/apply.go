// Package agent 负责按配置生成并部署三平台排障机器人。
package agent

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

var ensureCodeGraphForDeploy = EnsureCodeGraphInstalled
var prepareCodeGraphForDeploy = PrepareCodeGraphIndexes
var installNativeForApply = InstallNative
var mergeMCPIntoIDESettingsForApply = MergeMCPIntoIDESettings

// ApplyOptions 控制 apply 的行为。
type ApplyOptions struct {
	// NewYAML 是要应用的新 troubleshooter.yaml 字节（含注释）。必须能过 config.LoadFromBytes。
	NewYAML []byte
	// TemplateRoot 是 tshoot 模板根（tshoot discover 所在的同一个 tshoot）。
	TemplateRoot string
	// TshootVersion 写回 tshoot.json 的 tshoot_version 字段。
	TshootVersion string
	// DryRun 为 true 时只渲染 + 打印会变的文件列表，不真写 workspace。
	DryRun bool
	// RepoLocalPaths 仓库名 → 本机绝对路径,生成 repo-path-map.yaml 用。
	// troubleshooter.yaml 不含此信息(可分享性),由 wizard / CLI 调用方额外传入。
	// 空 map 时产物里的 repo-path-map.yaml 是占位样子(提示用户跑向导)。
	RepoLocalPaths map[string]string
	// IDECreds 给 claude-code / cursor 安装时,把 mcp.servers 配置注入 ~/.claude.json
	// (user-scope dotfile,Claude Code CLI 强绑死位置)或 ~/.cursor/mcp.json 用。
	// key = env-var name(如 NACOS_ADDR_DEV),value = 实际值。
	// 注入的 env 字段值会变成 {{NACOS_ADDR_DEV}} 占位符让用户手填。
	IDECreds map[string]string
	// OnLog(可空)apply 链路里需要"用户感知"的进度回调,目前主要用在 mcp-grafana
	// 二进制下载(首次部署 ~30 MiB,易让用户误以为卡死)。desktop binding 把它接到
	// wails event "install:log" → UI 部署进度区;CLI 没传就 nil-safe 跳过。
	OnLog func(line string)
}

// Result 是 apply 的摘要，给 CLI 用来打印下一步。
type Result struct {
	AgentPath        string                `json:"agent_path"`
	Target           string                `json:"target"`
	FilesWritten     int                   `json:"files_written"`
	FilesRemoved     []string              `json:"files_removed,omitempty"`
	TSFJSONUpdated   bool                  `json:"tsf_json_updated"`
	NeedsRestartHint string                `json:"needs_restart_hint,omitempty"`
	CodeGraph        *CodeGraphIndexReport `json:"codegraph,omitempty"`
}

// PrepareCodeGraphForDeploy best-effort prepares CodeGraph indexes for an opted-in
// system. Setup failures are reported as a visible fallback and never fail deploy.
func PrepareCodeGraphForDeploy(ctx context.Context, cfg *config.SystemConfig, repoPaths map[string]string, onLog func(string)) *CodeGraphIndexReport {
	if cfg == nil || !cfg.CodeIntelligence.UsesCodeGraph() {
		return nil
	}
	log := onLog
	if log == nil {
		log = func(string) {}
	}
	repos := BuildCodeGraphRepoTargets(cfg, repoPaths)
	binary, err := ensureCodeGraphForDeploy(onLog)
	if err != nil {
		log("[codegraph] warn: binary unavailable; rg/read fallback enabled: " + err.Error())
		return &CodeGraphIndexReport{Total: len(repos)}
	}
	report := prepareCodeGraphForDeploy(ctx, CodeGraphIndexOptions{
		BinaryPath:     binary,
		SystemID:       cfg.System.ID,
		Repos:          repos,
		OnProgress:     onLog,
		InitTimeout:    120 * time.Second,
		SyncTimeout:    30 * time.Second,
		MaxConcurrency: 2,
	})
	return &report
}

// Apply 用 NewYAML 替换 agent 的活配置，重新 render 并 rsync 到工作目录。
// agent 来自 discover.Scan() 的结果。
//
// 注意"显示路径" vs "工作目录":
//
//	ag.Path = 真实部署位置(UI 卡片显示用,~/.claude/skills/<name>/ 之类)
//	workDir = discover.WorkDirFor(ag) = 实际写产物的位置:
//	  Claude Code/Cursor → ~/.tshoot/<target>/<system_id>/ staging 中间包
//	                       (Apply 写 staging,然后 InstallNative 同步到 ~/.claude / ~/.cursor)
//
// 每种 target 走各自的 generator 方法，然后把相应的产物子树 rsync 到 agent.Path。
func Apply(ag discover.DiscoveredAgent, opts ApplyOptions) (*Result, error) {
	if len(opts.NewYAML) == 0 {
		return nil, fmt.Errorf("NewYAML is required")
	}
	cfg, err := config.LoadFromBytes(opts.NewYAML)
	if err != nil {
		return nil, fmt.Errorf("new yaml invalid: %w", err)
	}

	// 父 tmp：让 generator 和它衍生的 <outDir>-<target> 目录都在里面，cleanup 一把清。
	parent, err := os.MkdirTemp("", "tshoot-agent-apply-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(parent)

	baseOut := filepath.Join(parent, "out")
	g := generator.New(cfg, opts.TemplateRoot, baseOut)
	g.TshootVersion = opts.TshootVersion
	g.TroubleshooterYAMLSource = opts.NewYAML
	g.RepoLocalPaths = opts.RepoLocalPaths
	// auto-analyze:同 ImportAndApply,有本地路径就跑一遍把 service-dependency-map +
	// data-schema-map 填齐。失败 / 空路径都不阻塞。OnLog 透传让桌面 UI 看到进度,
	// auto-analyze 内部有 60s timeout 兜底,不会卡死部署主流程。
	if result, aerr := RunAutoAnalyze(RunAutoAnalyzeOptions{
		Cfg:       cfg,
		RepoPaths: opts.RepoLocalPaths,
		OnLog:     opts.OnLog,
	}); aerr == nil && result != nil {
		g.LoadAnalysisResult(result)
	}

	// 按 target 渲染
	err = g.GenerateTarget(ag.Meta.Target)
	if err != nil {
		return nil, fmt.Errorf("render for target %q: %w", ag.Meta.Target, err)
	}

	src, restartHint := resolveApplySource(baseOut, ag.Meta.Target)
	if src == "" {
		return nil, fmt.Errorf("no staging source for target %q", ag.Meta.Target)
	}
	if _, statErr := os.Stat(src); statErr != nil {
		return nil, fmt.Errorf("staging %s missing: %w", src, statErr)
	}

	var removed []string
	written := 0

	srcFiles, err := listRel(src)
	if err != nil {
		return nil, err
	}
	// workDir 是 Apply 的实际写产物目录(staging 中间包,Claude Code/Cursor 双段式必走;
	workDir := discover.WorkDirFor(ag)
	dstFiles, _ := listRel(workDir)

	// src → dst
	for _, rel := range srcFiles {
		// 来自 generator 的 tshoot.json 不直接覆盖 —— 我们最后在 step 3 单独写，
		// 否则 generator 写的 generated_at 会先覆盖一次，再被我们这里覆盖一次，冗余。
		if rel == discover.MetaFilename {
			continue
		}
		if opts.DryRun {
			written++
			continue
		}
		if err := copyFile(filepath.Join(src, rel), filepath.Join(workDir, rel)); err != nil {
			return nil, fmt.Errorf("write %s: %w", rel, err)
		}
		written++
	}

	// dst 里但 src 没有的文件 → 判定是不是 tshoot 管辖的产物，是的话移除
	for _, rel := range dstFiles {
		if rel == discover.MetaFilename {
			continue
		}
		if !inList(srcFiles, rel) {
			if !looksLikeFactoryArtifact(rel, ag.Meta.Target) {
				continue
			}
			removed = append(removed, rel)
			if !opts.DryRun {
				_ = os.Remove(filepath.Join(workDir, rel))
			}
		}
	}

	// 刷 tshoot.json(写到 workDir;Claude Code/Cursor 装步骤会再拷一份到 ag.Path 真实位置)
	tsfUpdated := false
	if !opts.DryRun {
		if err := writeTSFMeta(workDir, ag.Meta.Target, cfg, opts.NewYAML, opts.TshootVersion, opts.RepoLocalPaths); err != nil {
			return nil, fmt.Errorf("refresh tshoot.json: %w", err)
		}
		tsfUpdated = true
	}

	// claude-code / cursor 是"中间包 → 用户级目录"两段式部署:这里 staging 已 rsync,
	// 注意:本路径同时覆盖"重新 apply"(改了 yaml 后回写)的场景,避免活配置和用户级
	// 目录脱节。
	if !opts.DryRun && (ag.Meta.Target == "claude-code" || ag.Meta.Target == "cursor" || ag.Meta.Target == "codex" || ag.Meta.Target == "opencode") {
		if err := installNativeForApply(workDir, ag.Meta.Target); err != nil {
			return nil, fmt.Errorf("native install (%s): %w", ag.Meta.Target, err)
		}
		// 顺带把 cfg 派生的 mcpServers 注入 IDE 配置(claude-code → ~/.claude.json,
		// cursor → ~/.cursor/mcp.json,codex → agent toml 内联段),让装完的 agent
		// 能直接调到 nacos / grafana / loki 等 MCP。creds 走 opts.IDECreds(桌面端 wizard 传),
		// 没有(CLI 装时)就用 {{ENV_VAR}} 占位符,用户事后自己填。
		if err := mergeMCPIntoIDESettingsForApply(ag.Meta.Target, cfg, opts.IDECreds, opts.OnLog); err != nil {
			return nil, fmt.Errorf("merge mcp settings (%s): %w", ag.Meta.Target, err)
		}
		// kuboard / apollo / consul / env-vars 类型走脚本读 creds.json,**不通过 MCP**。
		// ~/.tshoot/<id>-creds.json(平台无关位置),resolve_runtime_*.py 已加双路径回退。
		// creds=nil(regen 等无凭证场景)同样跳过(避免空覆盖)。
		if opts.IDECreds != nil {
			if err := WriteIDECredsFile(cfg, opts.IDECreds); err != nil {
				return nil, fmt.Errorf("write ide creds (%s): %w", ag.Meta.Target, err)
			}
		}
	}

	var codeGraph *CodeGraphIndexReport
	if !opts.DryRun {
		codeGraph = PrepareCodeGraphForDeploy(context.Background(), cfg, opts.RepoLocalPaths, opts.OnLog)
	}
	return &Result{
		AgentPath:        ag.Path,
		Target:           ag.Meta.Target,
		FilesWritten:     written,
		FilesRemoved:     removed,
		TSFJSONUpdated:   tsfUpdated,
		NeedsRestartHint: restartHint,
		CodeGraph:        codeGraph,
	}, nil
}

// ImportAndApply 是"首次部署"路径：没有现存 tshoot.json，直接按 yaml + target + dest
// 渲染产物 + 写到 dest。定位是"给已有 yaml 的人跳过 Init 向导 7 步"。
//
// 语义分两种：
//
//	  install.sh 完成 agent 注册 + MCP 装配 + 凭证收集（这步 apply 不代替）。
//	- target in {claude-code, cursor, embedded}：直接 rsync 产物到 dest，可立即使用。
//	  embedded 装完 Studio 扫到 tshoot.json 即可开对话,无独立部署步骤。
func ImportAndApply(yamlBytes []byte, target, destPath string, opts ApplyOptions) (*Result, error) {
	if _, err := ParseIDETarget(target); err != nil {
		return nil, err
	}
	if destPath == "" {
		return nil, fmt.Errorf("dest_path required")
	}
	cfg, err := config.LoadFromBytes(yamlBytes)
	if err != nil {
		return nil, fmt.Errorf("yaml invalid: %w", err)
	}
	if err := os.MkdirAll(destPath, 0o755); err != nil {
		return nil, fmt.Errorf("create dest %s: %w", destPath, err)
	}

	// 其他 3 target：构造 fake DiscoveredAgent，复用 Apply 的 rsync 逻辑
	fake := discover.DiscoveredAgent{
		Meta: discover.Meta{
			SchemaVersion: 1,
			SystemID:      cfg.System.ID,
			SystemName:    cfg.System.Name,
			Target:        target,
		},
		Path: destPath,
	}
	opts.NewYAML = yamlBytes
	// Apply 内部会对 claude-code / cursor 自动跑 InstallNative,这里不再重复。
	return Apply(fake, opts)
}

// countFilesUnder 数一个目录下的文件数（用于 Result.FilesWritten）。
func countFilesUnder(root string) int {
	n := 0
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}
