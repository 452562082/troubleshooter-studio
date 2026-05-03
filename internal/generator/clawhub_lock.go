// clawhub_lock.go —— OpenClaw 工作区识别锚点 + Summary 计数 helpers。
// .clawhub/lock.json 给 OpenClaw 客户端用,让它认得"这是个 tshoot 生成的 workspace"。
// 三个 count* 给 GenSummary 字段填值,放一起方便调用方一眼看到"总览口径"在哪算。
package generator

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xiaolong/troubleshooter-studio/internal/analyzer"
)

// writeClawhubLock 按输出目录下实际存在的 skills/*/ 目录列举,避免与模板过滤逻辑重复判断。
func (g *Generator) writeClawhubLock() error {
	wsRoot := filepath.Join(g.OutputDir, "templates", "workspace-template")
	skillsDir := filepath.Join(wsRoot, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		// 没有 skills 目录也写一份空 lock,保证 OpenClaw 能识别工作区
		entries = nil
	}
	now := time.Now().UnixMilli()
	type skillEntry struct {
		Version     string `json:"version"`
		InstalledAt int64  `json:"installedAt"`
	}
	skills := map[string]skillEntry{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		skills[e.Name()] = skillEntry{Version: "0.0.0-tshoot", InstalledAt: now}
	}
	lock := struct {
		Version int                   `json:"version"`
		Skills  map[string]skillEntry `json:"skills"`
	}{
		Version: 1,
		Skills:  skills,
	}
	data, err := json.MarshalIndent(lock, "", "  ")
	if err != nil {
		return err
	}
	dst := filepath.Join(wsRoot, ".clawhub", "lock.json")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, append(data, '\n'), 0o644)
}

func countSkills(outputDir string) int {
	skillsDir := filepath.Join(outputDir, "templates", "workspace-template", "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			n++
		}
	}
	return n
}

func countFiles(outputDir string) int {
	n := 0
	_ = filepath.WalkDir(outputDir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			n++
		}
		return nil
	})
	return n
}

func countOverrides(m map[string]map[string]analyzer.Finding) int {
	n := 0
	for _, byEnv := range m {
		n += len(byEnv)
	}
	return n
}
