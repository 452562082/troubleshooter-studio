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
	// FixKey + FixValue 让 `factory doctor --fix` 能机器化地修该条 issue；
	// 空串表示该条只能人工处理（如 missing-repo / origin-mismatch）。
	// 当前支持的 FixKey 语义见 internal/doctor/fixer.go 的 Fix()。
	FixKey   string `json:"fix_key,omitempty"`
	FixValue string `json:"fix_value,omitempty"`
}

type Report struct {
	Issues []Issue `json:"issues"`
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
