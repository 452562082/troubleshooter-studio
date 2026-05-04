package upgrade

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/config"
	"github.com/xiaolong/troubleshooter-studio/internal/generator"
)

// CurrentSchemaVersion 当前 tshoot 支持的 system.yaml schema 版本
const CurrentSchemaVersion = "0.1"

// Result upgrade 操作的结果，供 CLI 展示
type Result struct {
	BackupPath       string                `json:"backup_path"`
	SchemaFrom       string                `json:"schema_from"`
	SchemaTo         string                `json:"schema_to"`
	SchemaMigrated   bool                  `json:"schema_migrated"`
	Warnings         []string              `json:"warnings,omitempty"`
	GenSummary       *generator.GenSummary `json:"gen_summary"`
	DiffReport       *generator.DiffReport `json:"diff"`
	FilesChanged     int                   `json:"files_changed"`
	ConfigMapChanges int                   `json:"config_map_changes"`
}

// Options upgrade 入参
type Options struct {
	Config       *config.SystemConfig
	TemplateRoot string
	OutputDir    string // 绝对路径；现有产物必须存在
	AnalysisPath string // 可选
}

// Run 执行 upgrade 流程：
//  1. 要求 existing 存在；不存在则返回错误（建议改用 gen）
//  2. schema 版本比对（目前只有 0.1，仅做兜底）
//  3. 把 existing 重命名为 <out>.bak.<ts>
//  4. 将 backup 复制回 out 让 gen 的 preserve 机制生效
//  5. 跑 gen（带可选 analysis）
//  6. diff(backup, new-out)，组装 Result
func Run(opts Options) (*Result, error) {
	if opts.OutputDir == "" {
		return nil, fmt.Errorf("OutputDir required")
	}
	if info, err := os.Stat(opts.OutputDir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("existing output not found at %s; run `tshoot gen` first", opts.OutputDir)
	}

	res := &Result{
		SchemaFrom: opts.Config.Meta.SchemaVersion,
		SchemaTo:   CurrentSchemaVersion,
	}
	if res.SchemaFrom == "" {
		res.Warnings = append(res.Warnings, "system.yaml meta.schema_version 为空，按当前版本处理")
		res.SchemaFrom = CurrentSchemaVersion
	}
	if res.SchemaFrom != CurrentSchemaVersion {
		// 目前只支持 0.1；未来有 migration hook 可挂在这里
		res.Warnings = append(res.Warnings,
			fmt.Sprintf("检测到 schema %q 与当前 %q 不一致；当前版本尚无自动迁移",
				res.SchemaFrom, CurrentSchemaVersion))
		res.SchemaMigrated = true // 标记为需要人工 review
	}

	// 备份。秒精度时,1 秒内连跑两次 upgrade 第二次 Rename 会撞已存在的 backup
	// (Linux POSIX rename 要求目标 dir 为空,撞 EEXIST 中断;macOS APFS 边界行为不可靠)。
	// 加纳秒后 9 位让两次 backup 几乎不可能撞。跟 internal/agent/install_native.go 的
	// nanoTimestamp 同款思路,各自 inline 不跨包耦合。
	now := time.Now()
	ts := now.Format("20060102-150405") + fmt.Sprintf(".%09d", now.Nanosecond())
	backup := opts.OutputDir + ".bak." + ts
	if err := os.Rename(opts.OutputDir, backup); err != nil {
		return nil, fmt.Errorf("backup rename: %w", err)
	}
	res.BackupPath = backup

	// 复制 backup → OutputDir，让 gen 的 SnapshotExisting 基于此运作
	if err := copyTree(backup, opts.OutputDir); err != nil {
		return nil, fmt.Errorf("restore from backup for preserve: %w", err)
	}

	// gen
	g := generator.New(opts.Config, opts.TemplateRoot, opts.OutputDir)
	if opts.AnalysisPath != "" {
		if err := g.LoadAnalysis(opts.AnalysisPath); err != nil {
			return nil, fmt.Errorf("load analysis: %w", err)
		}
	}
	if err := g.Generate(); err != nil {
		return nil, fmt.Errorf("generate: %w", err)
	}
	res.GenSummary = g.Summary

	// diff
	rep, err := generator.Diff(backup, opts.OutputDir)
	if err != nil {
		return nil, fmt.Errorf("diff: %w", err)
	}
	res.DiffReport = rep
	res.FilesChanged = len(rep.Files)
	res.ConfigMapChanges = len(rep.ConfigMapChanges)
	return res, nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		info, _ := d.Info()
		mode := os.FileMode(0o644)
		if info != nil {
			mode = info.Mode()
		}
		return os.WriteFile(target, data, mode)
	})
}
