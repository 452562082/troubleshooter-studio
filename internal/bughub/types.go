package bughub

import "time"

type Attachment struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Type      string `json:"type,omitempty"`
	LocalPath string `json:"local_path,omitempty"`
	RemoteURL string `json:"remote_url,omitempty"`
}

type Bug struct {
	ID             string       `json:"id"`
	Source         string       `json:"source"`
	SourceID       string       `json:"source_id,omitempty"`
	PlatformID     string       `json:"platform_id,omitempty"`
	Title          string       `json:"title"`
	Description    string       `json:"description,omitempty"`
	Steps          string       `json:"steps,omitempty"`
	Expected       string       `json:"expected,omitempty"`
	Actual         string       `json:"actual,omitempty"`
	Status         string       `json:"status,omitempty"`
	Severity       string       `json:"severity,omitempty"`
	Priority       string       `json:"priority,omitempty"`
	Product        string       `json:"product,omitempty"`
	Module         string       `json:"module,omitempty"`
	BugType        string       `json:"bug_type,omitempty"`
	OS             string       `json:"os,omitempty"`
	Browser        string       `json:"browser,omitempty"`
	Keywords       string       `json:"keywords,omitempty"`
	Assignee       string       `json:"assignee,omitempty"`
	Reporter       string       `json:"reporter,omitempty"`
	CreatedAt      time.Time    `json:"created_at,omitempty"`
	UpdatedAt      time.Time    `json:"updated_at,omitempty"`
	Env            string       `json:"env,omitempty"`
	BotEnv         string       `json:"bot_env,omitempty"`
	SystemID       string       `json:"system_id,omitempty"`
	FrontendRepo   string       `json:"frontend_repo,omitempty"`
	ServiceHints   []string     `json:"service_hints,omitempty"`
	FrontendURL    string       `json:"frontend_url,omitempty"`
	APIPaths       []string     `json:"api_paths,omitempty"`
	TraceIDs       []string     `json:"trace_ids,omitempty"`
	RequestIDs     []string     `json:"request_ids,omitempty"`
	Attachments    []Attachment `json:"attachments,omitempty"`
	SelectedBotKey string       `json:"selected_bot_key,omitempty"`
	LastContext    string       `json:"last_context,omitempty"`
	LastContextAt  time.Time    `json:"last_context_at,omitempty"`
	RawPreview     string       `json:"raw_preview,omitempty"`
}

type BotRef struct {
	Key            string             `json:"key"`
	SystemID       string             `json:"system_id"`
	Target         string             `json:"target"`
	Path           string             `json:"path"`
	Name           string             `json:"name,omitempty"`
	AgentID        string             `json:"agent_id,omitempty"`
	Role           string             `json:"role,omitempty"`
	InternalAgents []BotInternalAgent `json:"internal_agents,omitempty"`
	Env            string             `json:"env,omitempty"`
	Envs           []string           `json:"envs,omitempty"`
}

type BotInternalAgent struct {
	ID   string `json:"id"`
	Role string `json:"role"`
}

type BotMatch struct {
	Bot     BotRef   `json:"bot"`
	Score   int      `json:"score"`
	Reasons []string `json:"reasons"`
}
