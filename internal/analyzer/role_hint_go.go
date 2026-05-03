// role_hint_go.go —— Go 仓库角色推断:看 main 包入口。
//   有 cmd/<x>/main.go 或顶层 main.go → backend(典型可执行服务)
//   有 go.mod 但无任何 main → common-lib
package analyzer

import (
	"os"
	"path/filepath"
)

func roleFromGo(repoPath string) RoleHint {
	if _, err := os.Stat(filepath.Join(repoPath, "main.go")); err == nil {
		// 不一定 backend;可能 cli 工具。但 go web 服务最常见的 main.go 是 server,先按 backend 推
		return RoleHint{Role: "backend", Reason: "顶层有 main.go(可执行服务入口)"}
	}
	cmd, err := os.ReadDir(filepath.Join(repoPath, "cmd"))
	if err == nil {
		for _, e := range cmd {
			if !e.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(repoPath, "cmd", e.Name(), "main.go")); err == nil {
				return RoleHint{Role: "backend", Reason: "有 cmd/" + e.Name() + "/main.go 入口"}
			}
		}
	}
	// go.mod 存在但没找到 main + 没 cmd —— 多半是 lib
	if _, err := os.Stat(filepath.Join(repoPath, "go.mod")); err == nil {
		return RoleHint{Role: "common-lib", Reason: "go.mod 存在但无 main 包,看着是公共库"}
	}
	return RoleHint{}
}
