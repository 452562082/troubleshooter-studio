// Package discover 负责从本机文件系统反向识别已安装的排障机器人。
//
// 核心锚点是各 target 产物根下的 tshoot.json —— tshoot 在 gen 时写入它，
// install.sh 会把它拷到最终部署路径（~/.openclaw/workspace/<name>/tshoot.json
// 或目标项目根）。discover 扫到这个文件 = 这是 Troubleshooter Studio 生成的机器人。
package discover

// MetaFilename 是 tshoot 写到每个产物根的元数据文件名。
// 客户端（discover / agent-edit）靠找这个文件识别机器人。
//
// 注意:刻意不带开头的点,因为 macOS Spotlight (mdfind) 默认跳过 dotfile,
// 这会让桌面 app 无法零配置扫出 embedded / claude-code / cursor 散落在
// 用户目录的机器人。用 tshoot.json(无点) 换来 Spotlight 全盘索引,
// 代价是这个文件会出现在 ls / Finder 里 —— 可接受。
const MetaFilename = "tshoot.json"

const (
	RoleTroubleshooter = "troubleshooter"
	RoleValidator      = "validator"
)

type InternalAgent struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}

// Meta 是 tshoot.json 的 schema。字段尽量稳定，新增字段要向后兼容。
type Meta struct {
	// SchemaVersion 让未来扩字段时不破坏旧机器人的 discover
	SchemaVersion int `json:"schema_version"`

	// TshootVersion 是生成本产物时的 tshoot 版本（main.version 注入）
	TshootVersion string `json:"tshoot_version"`

	// SystemID / SystemName 来自 troubleshooter.yaml 的 system 块，供 UI 展示
	SystemID   string `json:"system_id"`
	SystemName string `json:"system_name"`

	// AgentID / Role 是旧字段:早期纠偏版本曾把内部 agent 暴露成多个 bot。
	// 新模型下 Studio 仍只发现一台机器人,InternalAgents 描述这台机器人里有哪些执行入口。
	AgentID        string          `json:"agent_id,omitempty"`
	Role           string          `json:"role,omitempty"`
	InternalAgents []InternalAgent `json:"internal_agents,omitempty"`

	// Target 是本产物的部署形态：openclaw / claude-code / cursor / embedded
	Target string `json:"target"`

	// GeneratedAt RFC3339 时间戳
	GeneratedAt string `json:"generated_at"`

	// TroubleshooterYAML 是生成本产物时的完整 troubleshooter.yaml 原文（含注释）。
	// 这是产物的"真源"：后续要修改，Web UI / tshoot agent 直接从这里读、改完 re-gen 覆盖。
	TroubleshooterYAML string `json:"troubleshooter_yaml"`

	// UserEdits 是已部署后用户通过 Web UI / tshoot agent 做的增量编辑
	// （相对 TroubleshooterYAML 的 patch）。暂时保留字段，阶段 2 才用。
	UserEdits map[string]any `json:"user_edits,omitempty"`
}

// DiscoveredAgent 是 Scan() 返回的单个发现结果。
type DiscoveredAgent struct {
	// Meta 来自解析 tshoot.json
	Meta Meta `json:"meta"`

	// Path 是产物根目录绝对路径（含 tshoot.json 的那一层）
	Path string `json:"path"`

	// ModTime 是 tshoot.json 的修改时间，近似反映"最近一次 apply/install 的时间"
	ModTime string `json:"mod_time"`

	// 快速概览字段（从 TroubleshooterYAML 解析填充），方便 UI 不用再解析一次
	EnvCount     int      `json:"env_count"`
	Environments []string `json:"environments,omitempty"`
	RepoCount    int      `json:"repo_count"`
	SkillCount   int      `json:"skill_count"`
	Targets      []string `json:"targets,omitempty"` // troubleshooter.yaml 里声明的全部 targets（可能这个机器人只是其中之一）

	// IDEAvailable 标"对应 IDE 二进制本机仍可探测到"。zero value=false,
	// 由调用方(bindings_repo.go DiscoverBots)在 Scan 后 enrichment 填。Scan 自身
	// 不依赖 aitools 包,这字段对纯 discover 调用方(CLI tshoot discover)是 false +
	// 无意义(不影响展示),BotsPage 用它标 "⚠ IDE 已卸载,机器人不可用"。
	// openclaw target 始终视为 available(openclaw 是产品自带,不靠探测三方 IDE)。
	IDEAvailable bool `json:"ide_available"`

	// Ghost 标"~/.tshoot/config.json deployed_bots 里有但 disk 上 tshoot.json 不在"。
	// 用户外部 rm -rf ~/.<target>/skills/<name>/ 清掉机器人后,UI 仍能从这条记录
	// "幽灵显示"卡片,提供"重新部署"或"忘掉它"入口。Ghost=true 时 Meta.TroubleshooterYAML
	// 通常为空(yaml 在 disk 上没了),操作按钮要相应 disable。
	Ghost bool `json:"ghost"`
}
