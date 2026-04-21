// Package discover 负责从本机文件系统反向识别已安装的排障机器人。
//
// 核心锚点是各 target 产物根下的 .tshoot.json —— tshoot 在 gen 时写入它，
// install.sh 会把它拷到最终部署路径（~/.openclaw/workspace/<name>/.tshoot.json
// 或目标项目根）。discover 扫到这个文件 = 这是 Troubleshooter Studio 生成的机器人。
package discover

// MetaFilename 是 tshoot 写到每个产物根的元数据文件名。
// 客户端（discover / agent-edit）靠找这个文件识别机器人。
const MetaFilename = ".tshoot.json"

// Meta 是 .tshoot.json 的 schema。字段尽量稳定，新增字段要向后兼容。
type Meta struct {
	// SchemaVersion 让未来扩字段时不破坏旧机器人的 discover
	SchemaVersion int `json:"schema_version"`

	// TshootVersion 是生成本产物时的 tshoot 版本（main.version 注入）
	TshootVersion string `json:"tshoot_version"`

	// SystemID / SystemName 来自 system.yaml 的 system 块，供 UI 展示
	SystemID   string `json:"system_id"`
	SystemName string `json:"system_name"`

	// Target 是本产物的部署形态：openclaw / claude-code / cursor / standalone
	Target string `json:"target"`

	// GeneratedAt RFC3339 时间戳
	GeneratedAt string `json:"generated_at"`

	// SystemYAML 是生成本产物时的完整 system.yaml 原文（含注释）。
	// 这是产物的"真源"：后续要修改，Web UI / tshoot agent 直接从这里读、改完 re-gen 覆盖。
	SystemYAML string `json:"system_yaml"`

	// UserEdits 是已部署后用户通过 Web UI / tshoot agent 做的增量编辑
	// （相对 SystemYAML 的 patch）。暂时保留字段，阶段 2 才用。
	UserEdits map[string]any `json:"user_edits,omitempty"`
}

// DiscoveredAgent 是 Scan() 返回的单个发现结果。
type DiscoveredAgent struct {
	// Meta 来自解析 .tshoot.json
	Meta Meta `json:"meta"`

	// Path 是产物根目录绝对路径（含 .tshoot.json 的那一层）
	Path string `json:"path"`

	// ModTime 是 .tshoot.json 的修改时间，近似反映"最近一次 apply/install 的时间"
	ModTime string `json:"mod_time"`

	// 快速概览字段（从 SystemYAML 解析填充），方便 UI 不用再解析一次
	EnvCount   int      `json:"env_count"`
	RepoCount  int      `json:"repo_count"`
	SkillCount int      `json:"skill_count"`
	Targets    []string `json:"targets,omitempty"` // system.yaml 里声明的全部 targets（可能这个机器人只是其中之一）
}
