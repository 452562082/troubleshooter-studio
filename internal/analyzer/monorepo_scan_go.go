// monorepo_scan_go.go —— Go cmd/<x>/main.go 多入口探测路径(命中 ≥ 2 才算 monorepo)。
package analyzer

import (
	"os"
	"path/filepath"
	"strings"
)

func detectGoCmdDirs(repoPath string) []SubmoduleHint {
	cmdDir := filepath.Join(repoPath, "cmd")
	entries, err := os.ReadDir(cmdDir)
	if err != nil {
		return nil
	}
	var hints []SubmoduleHint
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		mainGo := filepath.Join(cmdDir, e.Name(), "main.go")
		if _, err := os.Stat(mainGo); err != nil {
			continue
		}
		sub := filepath.ToSlash(filepath.Join("cmd", e.Name()))
		full := filepath.Join(cmdDir, e.Name())
		role := RecommendRole("go", e.Name(), full)
		hints = append(hints, SubmoduleHint{
			Name:    e.Name(),
			SubPath: sub,
			Stack:   "go",
			Role:    role.Role,
			Reason:  "cmd/" + e.Name() + "/main.go + " + role.Reason,
		})
	}
	return hints
}
