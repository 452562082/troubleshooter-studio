package generator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/discover"
)

// writeTshootMeta 写产物根下的 tshoot.json 锚点，discover 靠这个文件识别机器人。
// dir 是"产物根"——对 openclaw 是 <OutputDir>/templates/workspace-template/，
// 对 claude-code/cursor/standalone 是各自的输出目录。
//
// 字段取自 g.Ctx（system.id / name）+ g.TshootVersion + g.SystemYAMLSource。
// SystemYAMLSource 为空时字段留空字符串，不影响 JSON 结构，只影响后续 apply 能不能找到"真源"。
func (g *Generator) writeTshootMeta(dir, target string) error {
	meta := discover.Meta{
		SchemaVersion: 1,
		TshootVersion: g.TshootVersion,
		SystemID:      g.Ctx.System.ID,
		SystemName:    g.Ctx.System.Name,
		Target:        target,
		GeneratedAt:   time.Now().UTC().Format(time.RFC3339),
		SystemYAML:    string(g.SystemYAMLSource),
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal tshoot.json: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, discover.MetaFilename)
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
