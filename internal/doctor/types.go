package doctor

// Severity 级别
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	SeverityInfo    = "info"
)

// 常见 Category，便于机器消费
const (
	CatMissingRepo        = "missing-repo"
	CatStackMismatch      = "stack-mismatch"
	CatServiceDrift       = "service-drift"
	CatConfigCenterDrift  = "config-center-drift"
	CatConfigCenterUnused = "config-center-unused"
	CatDataStoreUnused    = "data-store-unused"
	CatEnvProfileUnused   = "env-profile-unused"
	CatUndeclaredProfile  = "undeclared-env-profile"
	CatOriginMismatch     = "origin-mismatch"
)

type Issue struct {
	Severity string `json:"severity"`
	Category string `json:"category"`
	Target   string `json:"target"`
	Message  string `json:"message"`
	Suggest  string `json:"suggest,omitempty"`
	// FixKey + FixValue 让 `tshoot doctor --fix` 能机器化地修该条 issue；
	// 空串表示该条只能人工处理（如 missing-repo / origin-mismatch）。
	// 当前支持的 FixKey 语义见 internal/doctor/fixer.go 的 Fix()。
	FixKey   string `json:"fix_key,omitempty"`
	FixValue string `json:"fix_value,omitempty"`
}

type Report struct {
	Issues []Issue `json:"issues"`
	// ScannedRepoPaths 是本次"深度扫描"实际用到的仓库路径表(repo.Name → 绝对路径)。
	// 桌面 app 的 Doctor binding 会从 ~/.tshoot/config.json 自动注入,UI 据此显示
	// "扫了哪几个仓库 / 哪些没扫到",用户能直观判断诊断结果置信度。
	// 没跑深度扫(reposRoot 空 + 没保存路径)时为 nil。
	ScannedRepoPaths map[string]string `json:"scanned_repo_paths,omitempty"`
}

func (r *Report) add(i Issue) { r.Issues = append(r.Issues, i) }

// Counts 返回各 severity 计数，CLI 汇总用
func (r *Report) Counts() (errs, warns, infos int) {
	for _, i := range r.Issues {
		switch i.Severity {
		case SeverityError:
			errs++
		case SeverityWarning:
			warns++
		case SeverityInfo:
			infos++
		}
	}
	return
}
