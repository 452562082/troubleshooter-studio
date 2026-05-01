// expand.go —— 把"单仓多入口"信号(cmd/<x>/main.go / services/<x>/ / pom modules / npm
// workspaces 等)展开成服务名,作用于已有的 RepoAnalysis。
//
// 历史:这套逻辑原来只在 internal/analyzerpipe/pipeline.go 里执行,doctor 走另一条
// 直接调 analyzer.Analyze 的路径,导致 doctor 拿到的 ra.ServiceNames 只有根 module 名,
// 跟 yaml 里"<repo>-<entry>"格式的 service_names 对不上,误报 service-drift。
// 现在统一到这里,两端都调 ExpandCmdEntriesAsServiceNames,行为一致。
//
// 命名规则跟前端 InitPage::qualifyServiceName 同款,确保 yaml 自动填和 doctor / analyze
// 三端命名空间统一。
package analyzer

import (
	"strings"
)

// ExpandCmdEntriesAsServiceNames 把 ra.ServiceNames 替换为"cmd 入口 + repo 前缀"列表
// (仅当探测到多入口信号时)。单入口 / 没探测到的仓库保持原 ServiceNames 不动。
//
// gitmodules 路径(hint.URL 非空)被排除 —— 它们是独立 git 仓库,不属于本仓的 cmd 入口,
// 应该当独立 repo 处理。
func ExpandCmdEntriesAsServiceNames(ra *RepoAnalysis, repoName, repoPath string) {
	if ra == nil {
		return
	}
	hints := DetectSubmodules(repoPath)
	var cmdHints []SubmoduleHint
	for _, h := range hints {
		if h.URL == "" {
			cmdHints = append(cmdHints, h)
		}
	}
	if len(cmdHints) == 0 {
		return
	}
	merged := []string{}
	for _, h := range cmdHints {
		qualified := QualifyServiceName(repoName, h.Name)
		if qualified == "" {
			continue
		}
		if !containsStr(merged, qualified) {
			merged = append(merged, qualified)
		}
	}
	if len(merged) > 0 {
		ra.ServiceNames = merged
	}
}

// QualifyServiceName 给 cmd entry 名加 `<repo>-` 前缀消歧义,跟前端 InitPage 同款规则。
//
//	entry == repo                   → 不重复加前缀(repo=order, cmd/order → order)
//	entry 已带 repo 名作前/后缀     → 已消歧,直接用
//	其它                            → `<repo>-<entry>`
func QualifyServiceName(repoName, entryName string) string {
	repo := strings.TrimSpace(repoName)
	ent := strings.TrimSpace(entryName)
	if repo == "" {
		return ent
	}
	if ent == "" {
		return repo
	}
	if ent == repo {
		return ent
	}
	if strings.HasPrefix(ent, repo+"-") || strings.HasPrefix(ent, repo+"_") ||
		strings.HasSuffix(ent, "-"+repo) || strings.HasSuffix(ent, "_"+repo) {
		return ent
	}
	return repo + "-" + ent
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
