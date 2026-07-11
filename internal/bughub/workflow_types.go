package bughub

import (
	"encoding/json"
	"fmt"
	"time"
)

type CaseStatus string

const (
	CasePendingValidation    CaseStatus = "pending_validation"
	CaseValidating           CaseStatus = "validating"
	CaseWaitingEvidence      CaseStatus = "waiting_evidence"
	CaseReproduced           CaseStatus = "reproduced"
	CaseNotReproduced        CaseStatus = "not_reproduced"
	CaseInvestigating        CaseStatus = "investigating"
	CaseRootCauseReady       CaseStatus = "root_cause_ready"
	CaseWaitingFixApproval   CaseStatus = "waiting_fix_approval"
	CaseFixing               CaseStatus = "fixing"
	CaseFixFailed            CaseStatus = "fix_failed"
	CaseFixPushed            CaseStatus = "fix_pushed"
	CaseWaitingMergeApproval CaseStatus = "waiting_merge_approval"
	CaseMerging              CaseStatus = "merging"
	CaseMergeConflict        CaseStatus = "merge_conflict"
	CaseWaitingDeployment    CaseStatus = "waiting_deployment"
	CaseDeploymentUnverified CaseStatus = "deployment_unverified"
	CaseDeploymentVerified   CaseStatus = "deployment_verified"
	CaseRegressionValidating CaseStatus = "regression_validating"
	CaseFixedVerified        CaseStatus = "fixed_verified"
	CaseStillReproduces      CaseStatus = "still_reproduces"
	CaseLegacyArchived       CaseStatus = "legacy_archived"
)

type Phase string

const (
	PhaseValidation    Phase = "validation"
	PhaseInvestigation Phase = "investigation"
	PhaseFix           Phase = "fix"
	PhaseRegression    Phase = "regression"
	PhaseLegacy        Phase = "legacy"
)

type AttemptMode string

const (
	AttemptReproduce  AttemptMode = "reproduce"
	AttemptRegression AttemptMode = "regression"
)

type IncidentCase struct {
	ID               string     `json:"id"`
	BugID            string     `json:"bug_id"`
	Source           string     `json:"source"`
	SystemID         string     `json:"system_id"`
	Environment      string     `json:"environment"`
	Status           CaseStatus `json:"status"`
	CycleNumber      int        `json:"cycle_number"`
	CurrentAttemptID string     `json:"current_attempt_id"`
	SelectedBotKey   string     `json:"selected_bot_key"`
	Version          int64      `json:"version"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ClosedAt         *time.Time `json:"closed_at"`
}

func (c IncidentCase) Validate() error {
	if c.ID == "" {
		return fmt.Errorf("incident case ID is required")
	}
	if c.BugID == "" {
		return fmt.Errorf("incident case bug ID is required")
	}
	if c.CycleNumber < 1 {
		return fmt.Errorf("incident case cycle number must be positive")
	}
	return nil
}

type AgentUsage struct {
	InputTokens  int64         `json:"input_tokens,omitempty"`
	OutputTokens int64         `json:"output_tokens,omitempty"`
	Duration     time.Duration `json:"duration,omitempty"`
}

func (u AgentUsage) Validate() error {
	if u.InputTokens < 0 {
		return fmt.Errorf("agent input tokens must not be negative")
	}
	if u.OutputTokens < 0 {
		return fmt.Errorf("agent output tokens must not be negative")
	}
	if u.Duration < 0 {
		return fmt.Errorf("agent duration must not be negative")
	}
	return nil
}

type PhaseAttempt struct {
	ID              string              `json:"id"`
	CaseID          string              `json:"case_id"`
	CycleNumber     int                 `json:"cycle_number"`
	Phase           Phase               `json:"phase"`
	Mode            AttemptMode         `json:"mode"`
	Status          InvestigationStatus `json:"status"`
	AgentTarget     string              `json:"agent_target"`
	BotKey          string              `json:"bot_key"`
	InputJSON       json.RawMessage     `json:"input_json"`
	OutputJSON      json.RawMessage     `json:"output_json"`
	ParentAttemptID string              `json:"parent_attempt_id"`
	StartedAt       time.Time           `json:"started_at"`
	FinishedAt      *time.Time          `json:"finished_at"`
	ErrorCode       string              `json:"error_code"`
	ErrorMessage    string              `json:"error_message"`
	Usage           AgentUsage          `json:"usage"`
}

func (a PhaseAttempt) Validate() error {
	if a.ID == "" {
		return fmt.Errorf("phase attempt ID is required")
	}
	if a.CaseID == "" {
		return fmt.Errorf("phase attempt case ID is required")
	}
	if a.CycleNumber < 1 {
		return fmt.Errorf("phase attempt cycle number must be positive")
	}
	return a.Usage.Validate()
}

type EvidenceArtifact struct {
	ID              string    `json:"id"`
	CaseID          string    `json:"case_id"`
	AttemptID       string    `json:"attempt_id"`
	Kind            string    `json:"kind"`
	PathOrReference string    `json:"path_or_reference"`
	SHA256          string    `json:"sha256"`
	CapturedAt      time.Time `json:"captured_at"`
	Environment     string    `json:"environment"`
	Version         string    `json:"version"`
	RequestID       string    `json:"request_id"`
	TraceID         string    `json:"trace_id"`
	RedactionStatus string    `json:"redaction_status"`
}

type CodeChange struct {
	ID                      string          `json:"id"`
	CaseID                  string          `json:"case_id"`
	AttemptID               string          `json:"attempt_id"`
	Repo                    string          `json:"repo"`
	BaseBranch              string          `json:"base_branch"`
	FixBranch               string          `json:"fix_branch"`
	FixCommit               string          `json:"fix_commit"`
	TestEvidence            json.RawMessage `json:"test_evidence"`
	TargetEnvironmentBranch string          `json:"target_environment_branch"`
	MergeBaseHead           string          `json:"merge_base_head"`
	MergeCommit             string          `json:"merge_commit"`
	PushRemote              string          `json:"push_remote"`
	PushStatus              string          `json:"push_status"`
}

type Approval struct {
	ID             string            `json:"id"`
	CaseID         string            `json:"case_id"`
	Kind           string            `json:"kind"`
	Actor          string            `json:"actor"`
	ApprovedAt     time.Time         `json:"approved_at"`
	CaseVersion    int64             `json:"case_version"`
	ScopeJSON      json.RawMessage   `json:"scope_json"`
	FixCommits     map[string]string `json:"fix_commits"`
	TargetBranches map[string]string `json:"target_branches"`
}

func (a Approval) Validate() error {
	if a.ID == "" {
		return fmt.Errorf("approval ID is required")
	}
	if a.CaseID == "" {
		return fmt.Errorf("approval case ID is required")
	}
	if a.CaseVersion < 1 {
		return fmt.Errorf("approval case version must be positive")
	}
	if len(a.ScopeJSON) == 0 {
		return fmt.Errorf("approval scope is required")
	}
	if !json.Valid(a.ScopeJSON) {
		return fmt.Errorf("approval scope must be valid JSON")
	}
	return nil
}

type DeploymentObservation struct {
	ID                 string            `json:"id"`
	CaseID             string            `json:"case_id"`
	Environment        string            `json:"environment"`
	ExpectedCommits    map[string]string `json:"expected_commits"`
	UserNotifiedAt     *time.Time        `json:"user_notified_at"`
	VerifiedAt         *time.Time        `json:"verified_at"`
	VerificationSource string            `json:"verification_source"`
	ObservedVersion    string            `json:"observed_version"`
	ObservedImages     map[string]string `json:"observed_images"`
	ObservedCommits    map[string]string `json:"observed_commits"`
	Result             string            `json:"result"`
}

type TransitionEvent struct {
	ID             string          `json:"id"`
	CaseID         string          `json:"case_id"`
	FromStatus     CaseStatus      `json:"from_status"`
	ToStatus       CaseStatus      `json:"to_status"`
	EventType      string          `json:"event_type"`
	ActorType      string          `json:"actor_type"`
	ActorID        string          `json:"actor_id"`
	IdempotencyKey string          `json:"idempotency_key"`
	PayloadJSON    json.RawMessage `json:"payload_json"`
	CreatedAt      time.Time       `json:"created_at"`
}
