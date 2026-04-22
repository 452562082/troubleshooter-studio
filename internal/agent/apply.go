// Package agent 实现阶段 2 的"读-改-部署闭环"：
// 从 discover 识别到的机器人 (tshoot.json) 出发，用新的 system.yaml 重新渲染产物，
// rsync 回活的 workspace 路径，保留 preserve_on_regenerate 列表中的用户手改。
//
// 这个包刻意**不**调 install.sh —— install.sh 的职责是"首次 bootstrap + 收凭证"，
// agent apply 的职责是"系统架构 / 映射表更新"，两者互补：
//   - 改凭证：编辑 scripts/.env 重跑 install.sh（机制已存在）
//   - 改环境/仓库/skill/映射：tshoot agent edit + apply（本包提供）
package agent

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/discover"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

// ApplyOptions 控制 apply 的行为。
type ApplyOptions struct {
	// NewYAML 是要应用的新 system.yaml 字节（含注释）。必须能过 config.LoadFromBytes。
	NewYAML []byte
	// TemplateRoot 是 tshoot 模板根（tshoot discover 所在的同一个 tshoot）。
	TemplateRoot string
	// TshootVersion 写回 tshoot.json 的 tshoot_version 字段。
	TshootVersion string
	// DryRun 为 true 时只渲染 + 打印会变的文件列表，不真写 workspace。
	DryRun bool
}

// Result 是 apply 的摘要，给 CLI 用来打印下一步。
type Result struct {
	AgentPath        string   `json:"agent_path"`
	Target           string   `json:"target"`
	FilesWritten     int      `json:"files_written"`
	FilesPreserved   []string `json:"files_preserved,omitempty"`
	FilesRemoved     []string `json:"files_removed,omitempty"`
	TSFJSONUpdated   bool     `json:"tsf_json_updated"`
	NeedsRestartHint string   `json:"needs_restart_hint,omitempty"`
}

// Apply 用 NewYAML 替换 agent 的活配置，重新 render 并 rsync 到 agent.Path。
// agent 来自 discover.Scan() 的结果；agent.Path 是 workspace 根（含 tshoot.json 的那一层）。
//
// 支持 4 种 target：openclaw / claude-code / cursor / standalone。
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
	g.SystemYAMLSource = opts.NewYAML

	// 按 target 渲染
	switch ag.Meta.Target {
	case "openclaw":
		err = g.Generate()
	case "claude-code":
		err = g.GenerateClaudeCode()
	case "cursor":
		err = g.GenerateCursor()
	case "standalone":
		err = g.GenerateStandalone()
	default:
		return nil, fmt.Errorf("unsupported target: %q", ag.Meta.Target)
	}
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

	// preserve 列表（仅新 yaml 声明的；其他 target 通常为空，无副作用）
	preserveSet := map[string]bool{}
	for _, p := range cfg.Generation.PreserveOnRegenerate {
		preserveSet[p] = true
	}

	var preserved, removed []string
	written := 0

	srcFiles, err := listRel(src)
	if err != nil {
		return nil, err
	}
	dstFiles, _ := listRel(ag.Path)

	// src → dst
	for _, rel := range srcFiles {
		// 来自 generator 的 tshoot.json 不直接覆盖 —— 我们最后在 step 3 单独写，
		// 否则 generator 写的 generated_at 会先覆盖一次，再被我们这里覆盖一次，冗余。
		if rel == discover.MetaFilename {
			continue
		}
		if preserveSet[rel] {
			if _, err := os.Stat(filepath.Join(ag.Path, rel)); err == nil {
				preserved = append(preserved, rel)
				continue
			}
		}
		if opts.DryRun {
			written++
			continue
		}
		if err := copyFile(filepath.Join(src, rel), filepath.Join(ag.Path, rel)); err != nil {
			return nil, fmt.Errorf("write %s: %w", rel, err)
		}
		written++
	}

	// dst 里但 src 没有的文件 → 判定是不是 tshoot 管辖的产物，是的话移除
	for _, rel := range dstFiles {
		if rel == discover.MetaFilename || preserveSet[rel] {
			continue
		}
		if !inList(srcFiles, rel) {
			if !looksLikeFactoryArtifact(rel, ag.Meta.Target) {
				continue
			}
			removed = append(removed, rel)
			if !opts.DryRun {
				_ = os.Remove(filepath.Join(ag.Path, rel))
			}
		}
	}

	// 刷 tshoot.json
	tsfUpdated := false
	if !opts.DryRun {
		if err := writeTSFMeta(ag.Path, ag.Meta.Target, cfg, opts.NewYAML, opts.TshootVersion); err != nil {
			return nil, fmt.Errorf("refresh tshoot.json: %w", err)
		}
		tsfUpdated = true
	}

	return &Result{
		AgentPath:        ag.Path,
		Target:           ag.Meta.Target,
		FilesWritten:     written,
		FilesPreserved:   preserved,
		FilesRemoved:     removed,
		TSFJSONUpdated:   tsfUpdated,
		NeedsRestartHint: restartHint,
	}, nil
}

// ImportAndApply 是"首次部署"路径：没有现存 tshoot.json，直接按 yaml + target + dest
// 渲染产物 + 写到 dest。定位是"给已有 yaml 的人跳过 Init 向导 7 步"。
//
// 语义分两种：
//   - target=openclaw：产出完整 dist 包（含 scripts/install.sh），用户仍需手动跑一次
//     install.sh 完成 agent 注册 + MCP 装配 + 凭证收集（这步 apply 不代替）。
//   - target in {claude-code, cursor, standalone}：直接 rsync 产物到 dest，可立即使用。
func ImportAndApply(yamlBytes []byte, target, destPath string, opts ApplyOptions) (*Result, error) {
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

	// openclaw 走完整 gen，产物包含 scripts/install.sh（agent apply 的 src 子树里没它）
	if target == "openclaw" {
		if opts.DryRun {
			return &Result{
				AgentPath: destPath,
				Target:    target,
				FilesWritten: 0,
				NeedsRestartHint: "dry-run：会生成 openclaw 产物到 " + destPath + "；真部署时桌面端会走 install.sh 自动化收凭证 + 注册 MCP",
			}, nil
		}
		g := generator.New(cfg, opts.TemplateRoot, destPath)
		g.TshootVersion = opts.TshootVersion
		g.SystemYAMLSource = yamlBytes
		if err := g.Generate(); err != nil {
			return nil, fmt.Errorf("openclaw gen: %w", err)
		}
		written := countFilesUnder(destPath)
		return &Result{
			AgentPath:        destPath,
			Target:           target,
			FilesWritten:     written,
			TSFJSONUpdated:   true, // g.Generate 尾部会写 tshoot.json
			NeedsRestartHint: "已生成 openclaw 产物。桌面端下一步会调 RunInstall 写凭证并跑 install.sh；CLI 用户请 cd '" + destPath + "' && bash scripts/install.sh，再 `openclaw gateway restart`",
		}, nil
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

// resolveApplySource 按 target 算出"应用源"（staging 里对应的产物子树）和重启提示。
func resolveApplySource(baseOut, target string) (src, hint string) {
	switch target {
	case "openclaw":
		// agent.Path 是 ~/.openclaw/workspace/<name>/；对应产物根下的 templates/workspace-template/
		src = filepath.Join(baseOut, "templates", "workspace-template")
		hint = "若本次新增了 env / 切换了配置中心类型，重跑 `bash scripts/install.sh` 注册新 MCP + 填凭证，再 `openclaw gateway restart`；只是改映射不用动。"
	case "claude-code":
		src = baseOut + "-claude-code"
		hint = "Claude Code 会在下次对话启动时重新读 CLAUDE.md + skills/；正在开的 session 需要 `/clear` 或重启 `claude` CLI 才能吃到新版。"
	case "cursor":
		src = baseOut + "-cursor"
		hint = "Cursor 自动感知 .cursorrules + .cursor/rules/*.mdc 变动；下一个新建的 AI 对话就会用新规则。"
	case "standalone":
		src = baseOut + "-standalone"
		hint = "本机 venv 跑的：Ctrl+C 停掉 server.py 再重启 `python3 server.py` 即可。Docker 跑的：`docker compose down && docker compose up --build`。"
	}
	return
}

// listRel 返回 root 下所有文件的相对路径（跳过空目录）。
func listRel(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out = append(out, rel)
		return nil
	})
	return out, err
}

func inList(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// looksLikeFactoryArtifact 判断 workspace 下某个文件是 tshoot 一开始生成的（可以 remove）
// 还是用户手工放的（不要乱动）。按 target 区分管辖面：不同 target 产物结构不一样。
func looksLikeFactoryArtifact(rel, target string) bool {
	common := []string{"skills/", "scripts/"}
	var prefixes []string
	switch target {
	case "openclaw":
		prefixes = append(prefixes, "SOUL.md", "IDENTITY.md", "AGENTS.md", "USER.md",
			"CHECKLIST.md", "TOOLS.md", ".clawhub/")
	case "claude-code":
		prefixes = append(prefixes, "CLAUDE.md", "install.sh")
	case "cursor":
		prefixes = append(prefixes, ".cursorrules", ".cursor/", "install.sh")
	case "standalone":
		prefixes = append(prefixes, "server.py", "index.html", "Dockerfile",
			"docker-compose.yaml", "requirements.txt", "system-prompt.md",
			"install.sh", "README.md")
	default:
		return false
	}
	prefixes = append(prefixes, common...)
	for _, p := range prefixes {
		if strings.HasPrefix(rel, p) || rel == strings.TrimSuffix(p, "/") {
			return true
		}
	}
	return false
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(src); err == nil {
		mode = info.Mode()
	}
	return os.WriteFile(dst, data, mode)
}

func writeTSFMeta(dir, target string, cfg *config.SystemConfig, yamlSrc []byte, version string) error {
	meta := map[string]any{
		"schema_version": 1,
		"tshoot_version": version,
		"system_id":       cfg.System.ID,
		"system_name":     cfg.System.Name,
		"target":          target,
		"generated_at":    time.Now().UTC().Format(time.RFC3339),
		"system_yaml":     string(yamlSrc),
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, discover.MetaFilename), append(data, '\n'), 0o644)
}
