// role_hint.go —— 自动推断仓库角色(role)
//
// 规则按可信度排序,**第一条命中即返回**(注释每条都说明命中证据,UI 显示给用户看):
//   1. 仓库名子串匹配(高可信:命名约定通常严格)
//   2. 顶层目录结构(只有 docs / 只有 terraform → docs / infra)
//   3. 技术栈专属信号(stack=node 看 package.json deps;stack=java 看 pom packaging;stack=go 看 main 包)
//   4. 都不命中 → backend(默认,大部分微服务都是这个)
//
// 设计:本函数**只读 repo 根目录的少量文件**(package.json / pom.xml / go.mod 等),
// 不做深度扫描 —— 速度优先。命中即返回,避免重复 IO。
//
// 各 stack 的具体推断规则已按域拆到子文件:
//
//	role_hint_node.go    roleFromNode    package.json deps + main/bin
//	role_hint_jvm.go     roleFromJava    pom.xml / build.gradle 字符串匹配
//	role_hint_go.go      roleFromGo      cmd/<x>/main.go 入口
//	role_hint_python.go  roleFromPython  requirements.txt / pyproject.toml 框架词
//	role_hint_php.go     roleFromPHP     composer.json + 入口文件
package analyzer

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// 仓库名子串 → role 的映射表。命中第一个即返回。
// 顺序很重要:更具体的(api-gateway)排前面,更模糊的(api)排后面。
var nameRoleHints = []struct {
	patterns []string
	role     string
	reason   string
}{
	// admin / 管理后台
	{[]string{"admin", "console", "dashboard", "manage", "backstage", "operation"}, "admin", "仓库名含管理后台关键字"},
	// gateway / BFF / 聚合层
	{[]string{"gateway", "-bff", "bff-", "apigw", "edge-svc", "proxy-svc"}, "gateway", "仓库名含网关 / BFF 关键字"},
	// mobile
	{[]string{"-ios", "ios-", "-android", "android-", "-mobile", "mobile-", "-app-rn"}, "mobile", "仓库名含移动端关键字"},
	// frontend
	{[]string{"-web", "web-", "-front", "front-", "-h5", "h5-", "-pc", "pc-", "-fe", "fe-", "-ui", "ui-"}, "frontend", "仓库名含前端关键字"},
	// middleware / worker / 异步任务
	{[]string{"worker", "task-", "-task", "cron-", "-cron", "consumer", "producer", "-job", "job-", "schedule"}, "middleware", "仓库名含 worker / 调度器 / 异步任务关键字"},
	// common-lib / SDK / 公共库
	{[]string{"common-", "-common", "core-", "-core", "-sdk", "sdk-", "shared-", "-shared", "-utils", "utils-", "-lib", "lib-", "components-", "-components"}, "common-lib", "仓库名含公共库 / SDK 关键字"},
	// infra
	{[]string{"-infra", "infra-", "terraform", "-helm", "helm-", "k8s-", "-k8s", "deploy-", "ops-"}, "infra", "仓库名含基础设施 / 部署关键字"},
	// docs
	{[]string{"-docs", "docs-", "-wiki", "wiki-", "knowledge-", "-handbook"}, "docs", "仓库名含文档关键字"},
}

// RecommendRole 给 (stack, repoName, repoPath) 推荐一个 role + 理由说明。
// repoPath 为空(纯远程仓库,本机没 clone)时退化到只看仓库名;非空时进一步看文件信号。
//
// 返回的 Role 一定是合法枚举值(跟 config.RoleXxx 对齐);命中不到任何规则时
// 返回 backend + "默认兜底"。
func RecommendRole(stack, repoName, repoPath string) RoleHint {
	lname := strings.ToLower(repoName)

	// ── 1. 仓库名锚点匹配(无 IO,最可靠)──
	// 模式里的 `-` 是锚点不是字面量:`pc-` 表示"以 pc- 开头"或"有独立 pc 段",
	// `-pc` 表示"以 -pc 结尾"或"有独立 pc 段"。这样 grpc-server 不会因为子串
	// 含 "pc-" 被误判成 frontend(grpc 整体是一个 token,跟 pc 不等)。
	// 不带 `-` 的模式(如 gateway / admin / worker)仍走 substring,因为这些词
	// 即便嵌在长 token 里也大概率成立(microgateway / adminapi / workersvc)。
	for _, h := range nameRoleHints {
		for _, p := range h.patterns {
			if matchesNamePattern(lname, p) {
				return RoleHint{Role: h.role, Reason: h.reason + " (含 " + p + ")"}
			}
		}
	}

	// ── 2. 顶层目录结构判定(需要 repoPath)──
	if repoPath != "" {
		if r := roleFromTopLevelStructure(repoPath); r.Role != "" {
			return r
		}
		// ── 3. 技术栈专属信号 ──
		if r := roleFromStackFiles(stack, repoPath); r.Role != "" {
			return r
		}
		// ── 4. 文件内容扫描:全是配置/数据 / 全是文档 / 全空 ──
		// 解决 mongodb-configs / 各种 conf 仓库被错误兜底到 backend 的问题。
		if r := roleFromContentScan(repoPath); r.Role != "" {
			return r
		}
	}

	// ── 5. 兜底 ──
	return RoleHint{Role: "backend", Reason: "默认(没命中名字 / 文件结构规则,大概率是后端服务)"}
}

// matchesNamePattern 按"-"锚点判断仓库名是否命中模式。
//
//	pat = "pc-"  → trailing dash:lname 以 "pc-" 开头,或某 token == "pc"
//	pat = "-pc"  → leading dash :lname 以 "-pc" 结尾,或某 token == "pc"
//	pat = "-pc-" → 两端 dash    :某 token == "pc"
//	pat = "pc"   → 无 dash      :裸 substring(给 gateway/admin/worker 这类长词用)
//
// token 由分隔符 -, _, /, . 切出。grpc-server 切成 [grpc, server],"pc" ∉ tokens
// 且 "pc-" 不是前缀 → 不命中 frontend 规则,符合直觉。
func matchesNamePattern(lname, pat string) bool {
	if pat == "" {
		return false
	}
	leadingDash := strings.HasPrefix(pat, "-")
	trailingDash := strings.HasSuffix(pat, "-")
	core := strings.Trim(pat, "-")
	if core == "" {
		return false
	}
	tokens := strings.FieldsFunc(lname, func(r rune) bool {
		return r == '-' || r == '_' || r == '/' || r == '.'
	})
	tokenEquals := func() bool {
		for _, t := range tokens {
			if t == core {
				return true
			}
		}
		return false
	}
	switch {
	case leadingDash && trailingDash:
		return tokenEquals()
	case leadingDash:
		return strings.HasSuffix(lname, "-"+core) || tokenEquals()
	case trailingDash:
		return strings.HasPrefix(lname, core+"-") || tokenEquals()
	default:
		return strings.Contains(lname, core)
	}
}

// roleFromContentScan 看顶层文件扩展名分布:
//   - 占比 90%+ 的 .md / .rst / .adoc → docs
//   - 占比 90%+ 的 .yaml/.yml/.json/.toml/.ini/.conf/.properties/.xml/.sql → infra
//   - 完全无文件 → 兜底,返回空让上层 fallback
//   - 命中任一源码扩展名 → 让 backend 兜底走完(返回空)
//
// 解决 mongodb-configs 这种"git submodule 声明了但里面没代码"的场景被错判成 backend 的问题。
func roleFromContentScan(repoPath string) RoleHint {
	codeExts := map[string]bool{
		".go": true, ".java": true, ".kt": true, ".scala": true,
		".py": true, ".rb": true, ".php": true, ".rs": true, ".swift": true, ".m": true,
		".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".vue": true,
		".c": true, ".cpp": true, ".cc": true, ".h": true, ".hpp": true,
	}
	configExts := map[string]bool{
		".yaml": true, ".yml": true, ".json": true, ".toml": true, ".ini": true,
		".conf": true, ".cnf": true, ".properties": true, ".xml": true,
		".sql": true, ".env": true, ".csv": true, ".tsv": true,
	}
	docExts := map[string]bool{
		".md": true, ".rst": true, ".adoc": true, ".txt": true,
	}
	totalCode, totalConfig, totalDoc, totalOther := 0, 0, 0, 0
	walk := filepath.Walk(repoPath, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		// 跳常见噪音目录
		if info.IsDir() {
			name := strings.ToLower(info.Name())
			if name == ".git" || name == "node_modules" || name == "vendor" ||
				name == "dist" || name == "build" || name == "target" ||
				strings.HasPrefix(name, ".") && p != repoPath {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(info.Name()))
		switch {
		case codeExts[ext]:
			totalCode++
		case configExts[ext]:
			totalConfig++
		case docExts[ext]:
			totalDoc++
		default:
			totalOther++
		}
		// 早退:扫到 50 个文件足以判;再多浪费 IO
		if totalCode+totalConfig+totalDoc+totalOther > 50 {
			return errors.New("enough")
		}
		return nil
	})
	_ = walk
	total := totalCode + totalConfig + totalDoc
	if total == 0 {
		return RoleHint{} // 空仓库,让上层兜底
	}
	if totalCode > 0 {
		return RoleHint{} // 有任意源码,不在这层判
	}
	// 走到这里:totalCode == 0 且至少有一个 config / doc 文件
	if totalDoc > 0 && totalDoc >= totalConfig {
		return RoleHint{Role: "docs", Reason: "全是文档文件 (.md/.rst/...)"}
	}
	return RoleHint{Role: "infra", Reason: "全是配置 / 数据文件 (.yaml/.json/...) 无源码"}
}

// roleFromTopLevelStructure 看根目录有没有"明显标志"的子目录 / 文件:
// 只有 docs 内容 → docs;只有 IaC 配置 → infra。
//
// "有源码"的判定不仅靠 cmd/internal/src 这种命名约定,还要扫顶层每个非过滤子目录里
// 有没有 manifest 文件(go.mod / package.json / pom.xml / ...)—— 解决"truss 这种把
// 服务平铺到顶层 commerce/ user/ ..." 被误判 docs 的问题。
func roleFromTopLevelStructure(repoPath string) RoleHint {
	entries, err := os.ReadDir(repoPath)
	if err != nil {
		return RoleHint{}
	}
	hasDocs, hasInfra, hasCode := false, false, false
	infraEvidence, docsEvidence := "", ""
	for _, e := range entries {
		name := strings.ToLower(e.Name())
		if !e.IsDir() {
			// 顶层 .md / .pdf / .png 不算"代码"
			if name == "readme.md" || name == "license" || strings.HasPrefix(name, ".") {
				continue
			}
			ext := filepath.Ext(name)
			switch ext {
			case ".go", ".java", ".py", ".js", ".ts", ".php", ".rb", ".rs", ".kt", ".swift", ".m":
				hasCode = true
			case ".tf", ".tfvars":
				hasInfra = true
				if infraEvidence == "" {
					infraEvidence = "顶层 .tf"
				}
			}
			continue
		}
		// 命名约定子目录直接判
		switch name {
		case "src", "lib", "internal", "pkg", "app", "cmd", "service", "services", "controllers", "handlers", "models", "main":
			hasCode = true
			continue
		case "docs", "doc", "wiki", "specs", "design":
			hasDocs = true
			if docsEvidence == "" {
				docsEvidence = e.Name() + "/"
			}
			continue
		case "terraform", "infra", "infrastructure", "helm", "kubernetes", "k8s", "kustomize", "ansible", "puppet", "manifests", "deploy", "deployment":
			hasInfra = true
			if infraEvidence == "" {
				infraEvidence = e.Name() + "/"
			}
			continue
		}
		// 命名不匹配的子目录:看里面有没有 manifest 文件(go.mod / package.json / pom.xml / ...)
		// 任一命中 → 算 hasCode,避免 truss 这种"顶层平铺多服务"被误判 docs
		if stackFromManifest(filepath.Join(repoPath, e.Name())) != "" {
			hasCode = true
		}
	}
	if hasInfra && !hasCode {
		return RoleHint{Role: "infra", Reason: "顶层只有 IaC 目录 (" + infraEvidence + "),无源码"}
	}
	if hasDocs && !hasCode && !hasInfra {
		return RoleHint{Role: "docs", Reason: "顶层只有 " + docsEvidence + ",无源码 / IaC"}
	}
	return RoleHint{}
}

// roleFromStackFiles 看 stack 专属的入口文件 / 依赖清单。
func roleFromStackFiles(stack, repoPath string) RoleHint {
	switch strings.ToLower(stack) {
	case "node":
		return roleFromNode(repoPath)
	case "java":
		return roleFromJava(repoPath)
	case "go":
		return roleFromGo(repoPath)
	case "python":
		return roleFromPython(repoPath)
	case "php":
		return roleFromPHP(repoPath)
	}
	return RoleHint{}
}

