package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/xiaolong/troubleshooter-factory/internal/analyzer"
	"github.com/xiaolong/troubleshooter-factory/internal/config"
	"github.com/xiaolong/troubleshooter-factory/internal/gitclone"
)

// Check 按 system.yaml 与 reposRoot 做漂移检测
// 如果 reposRoot 为 ""，只做仓库无关的静态检查（config_center 声明一致性等）
func Check(cfg *config.SystemConfig, reposRoot string) (*Report, error) {
	report := &Report{}
	ccType := cfg.Infrastructure.ConfigCenter.Type

	// 按环境索引允许的 profile 名
	declaredEnvs := map[string]bool{}
	for _, e := range cfg.Environments {
		declaredEnvs[e.ID] = true
	}

	// 聚合所有仓库的发现，用于"声明的组件在代码里没找到"这类全局判断
	var allFindings []analyzer.Finding
	anyRepoScanned := false

	if reposRoot != "" {
		reg := analyzer.NewRegistry(ccType)
		for _, repo := range cfg.Repos {
			repoPath := filepath.Join(reposRoot, repo.Name)
			if _, err := os.Stat(repoPath); err != nil {
				report.add(Issue{
					Severity: SeverityError,
					Category: CatMissingRepo,
					Target:   "repos[" + repo.Name + "]",
					Message:  fmt.Sprintf("仓库目录不存在：%s", repoPath),
					Suggest:  "git clone " + repo.URL + " " + repoPath,
				})
				continue
			}
			anyRepoScanned = true

			// origin URL 校验（git 不可用/非 git 仓库静默跳过）
			checkOriginMismatch(report, repo, repoPath)

			// stack 自动识别
			detected := detectStack(repoPath)
			if detected != "" && detected != repo.Stack {
				report.add(Issue{
					Severity: SeverityWarning,
					Category: CatStackMismatch,
					Target:   "repos[" + repo.Name + "].stack",
					Message:  fmt.Sprintf("声明 stack=%q，但检测到 %q（根据标记文件）", repo.Stack, detected),
					Suggest:  fmt.Sprintf("将 system.yaml 中该仓库的 stack 改为 %q", detected),
					FixKey:   "repo." + repo.Name + ".stack",
					FixValue: detected,
				})
			}

			// analyzer
			a, err := reg.Get(repo.Stack)
			if err != nil {
				// 不支持的 stack 就不扫仓库，但不算错
				continue
			}
			ra, err := a.Analyze(repoPath, repo.Analysis.IncludePaths)
			if err != nil {
				return nil, fmt.Errorf("analyze %s: %w", repo.Name, err)
			}
			allFindings = append(allFindings, ra.Findings...)

			checkServiceDrift(report, repo, ra)
			checkConfigCenterDrift(report, repo, ra, ccType)
			checkRepoProfileMatch(report, repo, ra, declaredEnvs)
		}
	}

	// 全局检查
	if ccType != "" && ccType != "none" && anyRepoScanned && len(allFindings) == 0 {
		report.add(Issue{
			Severity: SeverityWarning,
			Category: CatConfigCenterUnused,
			Target:   "infrastructure.config_center",
			Message:  fmt.Sprintf("声明 config_center=%q，但在所有扫描的仓库里都没发现相关配置", ccType),
			Suggest:  "如果系统确实不用配置中心，改为 type: none",
			FixKey:   "config-center.type",
			FixValue: "none",
		})
	}
	checkDataStoreReferences(report, cfg, allFindings, anyRepoScanned)

	return report, nil
}

func checkOriginMismatch(r *Report, repo config.Repo, repoPath string) {
	if !gitclone.Available() {
		return
	}
	actual, err := gitclone.ReadOrigin(repoPath)
	if err != nil {
		// 非 git 仓库或无 origin —— 不是致命问题，给 info 提示
		if err == gitclone.ErrNotGitRepo {
			r.add(Issue{
				Severity: SeverityInfo,
				Category: CatOriginMismatch,
				Target:   "repos[" + repo.Name + "]",
				Message:  fmt.Sprintf("%s 不是 git 仓库或未设置 origin", repoPath),
			})
		}
		return
	}
	if gitclone.CanonicalURL(actual) != gitclone.CanonicalURL(repo.URL) {
		r.add(Issue{
			Severity: SeverityWarning,
			Category: CatOriginMismatch,
			Target:   "repos[" + repo.Name + "]",
			Message:  fmt.Sprintf("声明 url=%q，但该目录实际 origin=%q", repo.URL, actual),
			Suggest:  "确认 system.yaml 的 url 是否正确，或重新 clone 到正确路径",
		})
	}
}

func checkServiceDrift(r *Report, repo config.Repo, ra *analyzer.RepoAnalysis) {
	declared := map[string]bool{}
	for _, s := range repo.ServiceNames {
		declared[s] = true
	}
	detected := map[string]bool{}
	for _, s := range ra.ServiceNames {
		detected[s] = true
	}

	// 声明了但没检测到
	for s := range declared {
		if !detected[s] {
			r.add(Issue{
				Severity: SeverityWarning,
				Category: CatServiceDrift,
				Target:   "repos[" + repo.Name + "].service_names",
				Message:  fmt.Sprintf("声明 service_name=%q 但分析器未在代码中找到（go.mod / pom.xml / package.json）", s),
				Suggest:  "确认服务名拼写，或删除 system.yaml 中多余的 service_name",
			})
		}
	}
	// 检测到但没声明
	for s := range detected {
		// 若 repo 没有显式声明 service_names，跳过（默认将用 repo.Name）
		if len(repo.ServiceNames) == 0 {
			continue
		}
		if !declared[s] {
			r.add(Issue{
				Severity: SeverityInfo,
				Category: CatServiceDrift,
				Target:   "repos[" + repo.Name + "].service_names",
				Message:  fmt.Sprintf("代码中检测到 service %q 但 system.yaml 未声明", s),
				Suggest:  "将该 service 加入 system.yaml 的 service_names",
			})
		}
	}
}

func checkConfigCenterDrift(r *Report, repo config.Repo, ra *analyzer.RepoAnalysis, declared string) {
	if declared == "" || declared == "none" {
		if len(ra.Findings) > 0 {
			types := uniqueFindingTypes(ra.Findings)
			r.add(Issue{
				Severity: SeverityWarning,
				Category: CatConfigCenterDrift,
				Target:   "repos[" + repo.Name + "]",
				Message:  fmt.Sprintf("声明 config_center=none，但代码中发现 %s 配置", strings.Join(types, "/")),
				Suggest:  "更新 system.yaml 的 config_center.type，或清理配置",
			})
		}
		return
	}
	// 声明了某类型但发现其他类型
	for _, f := range ra.Findings {
		if f.ConfigCenter != declared {
			r.add(Issue{
				Severity: SeverityWarning,
				Category: CatConfigCenterDrift,
				Target:   fmt.Sprintf("repos[%s] %s", repo.Name, f.SourceFile),
				Message:  fmt.Sprintf("该文件看起来包含 %s 配置，但 system.yaml 声明 %s", f.ConfigCenter, declared),
				Suggest: fmt.Sprintf("确认实际配置类型：若应为 %s，将 infrastructure.config_center.type 改为 %s 后跑 factory upgrade；否则清理该文件或在 repos[*].analysis.include_paths 中排除",
					f.ConfigCenter, f.ConfigCenter),
			})
		}
	}
}

func checkRepoProfileMatch(r *Report, repo config.Repo, ra *analyzer.RepoAnalysis, declaredEnvs map[string]bool) {
	for _, f := range ra.Findings {
		if f.EnvProfile == "" {
			continue
		}
		if !declaredEnvs[f.EnvProfile] {
			r.add(Issue{
				Severity: SeverityInfo,
				Category: CatUndeclaredProfile,
				Target:   fmt.Sprintf("repos[%s] %s", repo.Name, f.SourceFile),
				Message:  fmt.Sprintf("配置文件 profile=%q 未在 environments 中声明", f.EnvProfile),
				Suggest:  fmt.Sprintf("在 system.yaml environments 中添加 id=%q，或重命名文件", f.EnvProfile),
			})
		}
	}
}

func checkDataStoreReferences(r *Report, cfg *config.SystemConfig, all []analyzer.Finding, anyRepoScanned bool) {
	if !anyRepoScanned {
		return
	}
	// 若 data_store 启用但没有任何仓库的配置解析脚本能推断出来连接信息，
	// 我们当前 analyzer 不直接抽取 redis/mongo/es 连接，但可以通过 server_addr 关键字粗检
	for _, ds := range cfg.Infrastructure.DataStores {
		if !ds.Enabled {
			continue
		}
		if !mentionsInFindings(all, ds.Type) {
			r.add(Issue{
				Severity: SeverityInfo,
				Category: CatDataStoreUnused,
				Target:   fmt.Sprintf("infrastructure.data_stores[type=%s]", ds.Type),
				Message:  fmt.Sprintf("%s 启用但未在任何仓库的配置中发现相关关键字", ds.Type),
				Suggest:  "运行 runtime-query 前请确认该组件确实被业务使用",
			})
		}
	}
}

// mentionsInFindings 在所有 findings 的已知字符串字段里做粗略 keyword 检查
func mentionsInFindings(all []analyzer.Finding, keyword string) bool {
	needle := strings.ToLower(keyword)
	for _, f := range all {
		for _, v := range []string{f.SourceFile, f.DataID, f.Group, f.NamespaceID,
			f.AppID, f.Cluster, f.KVPrefix, f.DefaultContext, f.ServerAddr} {
			if strings.Contains(strings.ToLower(v), needle) {
				return true
			}
		}
		for _, n := range f.Namespaces {
			if strings.Contains(strings.ToLower(n), needle) {
				return true
			}
		}
	}
	return false
}

func uniqueFindingTypes(fs []analyzer.Finding) []string {
	seen := map[string]bool{}
	for _, f := range fs {
		if f.ConfigCenter != "" {
			seen[f.ConfigCenter] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
