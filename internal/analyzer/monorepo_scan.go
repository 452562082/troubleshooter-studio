// monorepo_scan.go —— 自动识别 monorepo 子模块。
//
// 用户给一个仓库本地路径,本文件的 dispatcher 按优先级跑各 detect* 路径,
// 命中即返回每个子模块的 {Name, SubPath, Stack, RoleHint};Wizard UI 据此给"一键拆成 N 行"按钮。
//
// 各探测路径已按域拆到子文件:
//
//	monorepo_scan_git.go      detectGitSubmodules     .gitmodules(独立 git repo,可信度最高)
//	monorepo_scan_node.go     detectNodeWorkspaces    pnpm/lerna/yarn/npm workspaces
//	monorepo_scan_java.go     detectJavaModules       parent pom.xml <modules>
//	monorepo_scan_go.go       detectGoCmdDirs         cmd/<x>/main.go(≥ 2 才认)
//	monorepo_scan_generic.go  detectGenericServiceDir + stackFromManifest helper
//	monorepo_scan_toplevel.go detectTopLevelServices  顶层平铺(必须 manifest+可部署信号双命中)
package analyzer

import (
	"os"
)

// SubmoduleHint 一个子模块的探测结果。
type SubmoduleHint struct {
	Name    string `json:"name"`     // 推荐的服务名(从子目录名 / package.json:name 抽)
	SubPath string `json:"sub_path"` // 相对仓库根的路径
	Stack   string `json:"stack"`    // 该子模块自身的 stack(可能跟父仓库不同,如 web/admin 是 node)
	Role    string `json:"role"`     // 推荐角色(走 RecommendRole 同款规则)
	Reason  string `json:"reason"`   // 命中证据
	// URL 仅在 .gitmodules 路径下非空 —— 那是真"独立 git repo + 子目录共置"场景,
	// 每个 submodule 有自己的 git URL。其它检测路径(workspaces / pom modules / cmd 多入口 /
	// top-level services)是"同一仓库子目录",共用父仓 URL,本字段空。
	// Split 时:URL 非空 → 当独立仓库展开(自己的 url + 自己的本地路径 + 无 sub_path);
	//          URL 空 → 当同仓子目录展开(父 url + 父本地路径 + 各自 sub_path)。
	URL string `json:"url,omitempty"`
}

// DetectSubmodules 扫 repoPath 顶层,返回 0 / 1 / N 条子模块。
//
//	返回 0 条 = 不是 monorepo,UI 不显示拆分按钮
//	返回 1 条 = 边界情况(如 services/ 下只有 1 个),仍当 monorepo 处理(为 future-proof)
//	返回 N 条 = monorepo,UI 弹"检测到 N 个子模块,一键拆分"
//
// 不要求绝对完备:命中常见模式即可,非典型 monorepo 用户走"+ 子模块"手动加。
func DetectSubmodules(repoPath string) []SubmoduleHint {
	if repoPath == "" {
		return nil
	}
	if _, err := os.Stat(repoPath); err != nil {
		return nil
	}

	// 1. Git submodules(.gitmodules):umbrella repo 显式声明的子模块,可信度最高
	if hints := detectGitSubmodules(repoPath); len(hints) > 0 {
		return hints
	}
	// 2. Node workspaces:package.json:workspaces / lerna.json / pnpm-workspace.yaml / turbo.json
	if hints := detectNodeWorkspaces(repoPath); len(hints) > 0 {
		return hints
	}
	// 3. Java multi-module:parent pom.xml 含 <modules>
	if hints := detectJavaModules(repoPath); len(hints) > 0 {
		return hints
	}
	// 4. Go: cmd/<x>/main.go(命中多个就是 monorepo)
	if hints := detectGoCmdDirs(repoPath); len(hints) > 1 {
		return hints
	}
	// 5. 通用:services/ / packages/ / apps/ / modules/ 下各子目录有自己的 manifest 文件
	if hints := detectGenericServiceDir(repoPath); len(hints) > 1 {
		return hints
	}
	// 6. 顶层平铺多服务:repo 根直接挂多个有 manifest 的子目录(非 services/ wrapper),
	// 跨语言 monorepo / 老项目结构常见。命中 ≥ 2 才认。
	if hints := detectTopLevelServices(repoPath); len(hints) > 1 {
		return hints
	}
	return nil
}
