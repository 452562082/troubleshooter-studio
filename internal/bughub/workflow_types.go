package bughub

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
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
	CaseWaitingRemediation   CaseStatus = "waiting_remediation"
	CaseRemediationApplied   CaseStatus = "remediation_applied"
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
	CaseResetArchived        CaseStatus = "reset_archived"
)

func (s CaseStatus) valid() bool {
	switch s {
	case CasePendingValidation,
		CaseValidating,
		CaseWaitingEvidence,
		CaseReproduced,
		CaseNotReproduced,
		CaseInvestigating,
		CaseRootCauseReady,
		CaseWaitingFixApproval,
		CaseWaitingRemediation,
		CaseRemediationApplied,
		CaseFixing,
		CaseFixFailed,
		CaseFixPushed,
		CaseWaitingMergeApproval,
		CaseMerging,
		CaseMergeConflict,
		CaseWaitingDeployment,
		CaseDeploymentUnverified,
		CaseDeploymentVerified,
		CaseRegressionValidating,
		CaseFixedVerified,
		CaseStillReproduces,
		CaseLegacyArchived,
		CaseResetArchived:
		return true
	default:
		return false
	}
}

func IsTerminalCaseStatus(status CaseStatus) bool {
	return status == CaseFixedVerified || status == CaseLegacyArchived || status == CaseResetArchived
}

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

type AttemptStatus string

const (
	AttemptStatusQueued      AttemptStatus = "queued"
	AttemptStatusRunning     AttemptStatus = "running"
	AttemptStatusSucceeded   AttemptStatus = "succeeded"
	AttemptStatusFailed      AttemptStatus = "failed"
	AttemptStatusCancelled   AttemptStatus = "cancelled"
	AttemptStatusInterrupted AttemptStatus = "interrupted"
)

func AttemptStatusFromInvestigationStatus(status InvestigationStatus) (AttemptStatus, error) {
	attemptStatus := AttemptStatus(status)
	if !attemptStatus.valid() || attemptStatus == AttemptStatusInterrupted {
		return "", fmt.Errorf("unsupported investigation status %q", status)
	}
	return attemptStatus, nil
}

func (s AttemptStatus) InvestigationStatus() (InvestigationStatus, error) {
	if !s.valid() || s == AttemptStatusInterrupted {
		return "", fmt.Errorf("attempt status %q has no investigation status equivalent", s)
	}
	return InvestigationStatus(s), nil
}

func (s AttemptStatus) valid() bool {
	switch s {
	case AttemptStatusQueued,
		AttemptStatusRunning,
		AttemptStatusSucceeded,
		AttemptStatusFailed,
		AttemptStatusCancelled,
		AttemptStatusInterrupted:
		return true
	default:
		return false
	}
}

type IncidentCase struct {
	ID                 string     `json:"id"`
	BugID              string     `json:"bug_id"`
	Source             string     `json:"source"`
	SystemID           string     `json:"system_id"`
	Environment        string     `json:"environment"`
	Status             CaseStatus `json:"status"`
	CycleNumber        int        `json:"cycle_number"`
	CurrentAttemptID   string     `json:"current_attempt_id"`
	SelectedBotKey     string     `json:"selected_bot_key"`
	ResetFromCaseID    string     `json:"reset_from_case_id,omitempty"`
	SupersededByCaseID string     `json:"superseded_by_case_id,omitempty"`
	Version            int64      `json:"version"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
	ClosedAt           *time.Time `json:"closed_at"`
}

func (c IncidentCase) Clone() IncidentCase {
	cloned := c
	cloned.ClosedAt = cloneTimePtr(c.ClosedAt)
	return cloned
}

func (c IncidentCase) Validate() error {
	if blank(c.ID) {
		return fmt.Errorf("incident case ID is required")
	}
	if blank(c.BugID) {
		return fmt.Errorf("incident case bug ID is required")
	}
	if c.CycleNumber < 1 {
		return fmt.Errorf("incident case cycle number must be positive")
	}
	if !c.Status.valid() {
		return fmt.Errorf("unsupported incident case status %q", c.Status)
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
	ID              string          `json:"id"`
	CaseID          string          `json:"case_id"`
	CycleNumber     int             `json:"cycle_number"`
	Phase           Phase           `json:"phase"`
	Mode            AttemptMode     `json:"mode"`
	Status          AttemptStatus   `json:"status"`
	AgentTarget     string          `json:"agent_target"`
	BotKey          string          `json:"bot_key"`
	InputJSON       json.RawMessage `json:"input_json"`
	OutputJSON      json.RawMessage `json:"output_json"`
	ParentAttemptID string          `json:"parent_attempt_id"`
	StartedAt       time.Time       `json:"started_at"`
	FinishedAt      *time.Time      `json:"finished_at"`
	ErrorCode       string          `json:"error_code"`
	ErrorMessage    string          `json:"error_message"`
	Usage           AgentUsage      `json:"usage"`
}

func (a PhaseAttempt) Clone() PhaseAttempt {
	cloned := a
	cloned.InputJSON = CloneRawMessage(a.InputJSON)
	cloned.OutputJSON = CloneRawMessage(a.OutputJSON)
	cloned.FinishedAt = cloneTimePtr(a.FinishedAt)
	return cloned
}

func (a PhaseAttempt) Validate() error {
	return a.ValidateWithOptions(AttemptValidationOptions{})
}

type AttemptValidationOptions struct {
	AllowLegacyMigration bool
}

func (a PhaseAttempt) ValidateWithOptions(options AttemptValidationOptions) error {
	if blank(a.ID) {
		return fmt.Errorf("phase attempt ID is required")
	}
	if blank(a.CaseID) {
		return fmt.Errorf("phase attempt case ID is required")
	}
	if a.CycleNumber < 1 {
		return fmt.Errorf("phase attempt cycle number must be positive")
	}
	if !a.Status.valid() {
		return fmt.Errorf("unsupported phase attempt status %q", a.Status)
	}
	switch a.Phase {
	case PhaseValidation:
		if a.Mode != AttemptReproduce {
			return fmt.Errorf("validation phase requires reproduce mode")
		}
	case PhaseRegression:
		if a.Mode != AttemptRegression {
			return fmt.Errorf("regression phase requires regression mode")
		}
	case PhaseInvestigation, PhaseFix:
		if a.Mode != "" {
			return fmt.Errorf("phase %q does not accept an attempt mode", a.Phase)
		}
	case PhaseLegacy:
		if !options.AllowLegacyMigration {
			return fmt.Errorf("legacy phase attempts may only be created during migration")
		}
		if a.Mode != "" {
			return fmt.Errorf("legacy phase does not accept an attempt mode")
		}
	default:
		return fmt.Errorf("unsupported phase %q", a.Phase)
	}
	if err := validateJSONObject("phase attempt input", a.InputJSON, true); err != nil {
		return err
	}
	if err := validateJSONObject("phase attempt output", a.OutputJSON, true); err != nil {
		return err
	}
	return a.Usage.Validate()
}

type RedactionStatus string

const (
	RedactionStatusPending     RedactionStatus = "pending"
	RedactionStatusRedacted    RedactionStatus = "redacted"
	RedactionStatusNotRequired RedactionStatus = "not_required"
)

type EvidenceArtifact struct {
	ID              string          `json:"id"`
	CaseID          string          `json:"case_id"`
	AttemptID       string          `json:"attempt_id"`
	Kind            string          `json:"kind"`
	PathOrReference string          `json:"path_or_reference"`
	SHA256          string          `json:"sha256"`
	CapturedAt      time.Time       `json:"captured_at"`
	Environment     string          `json:"environment"`
	Version         string          `json:"version"`
	RequestID       string          `json:"request_id"`
	TraceID         string          `json:"trace_id"`
	RedactionStatus RedactionStatus `json:"redaction_status"`
}

func (a EvidenceArtifact) Validate() error {
	if blank(a.ID) || blank(a.CaseID) || blank(a.AttemptID) {
		return fmt.Errorf("evidence artifact ID, case ID, and attempt ID are required")
	}
	if blank(a.Kind) || blank(a.PathOrReference) {
		return fmt.Errorf("evidence artifact kind and path or reference are required")
	}
	sha, err := hex.DecodeString(a.SHA256)
	if err != nil || len(sha) != 32 {
		return fmt.Errorf("evidence artifact SHA256 must be a 64-character hexadecimal digest")
	}
	switch a.RedactionStatus {
	case RedactionStatusPending, RedactionStatusRedacted, RedactionStatusNotRequired:
		return nil
	default:
		return fmt.Errorf("unsupported evidence redaction status %q", a.RedactionStatus)
	}
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

func (c CodeChange) Clone() CodeChange {
	cloned := c
	cloned.TestEvidence = CloneRawMessage(c.TestEvidence)
	return cloned
}

func (c CodeChange) Validate() error {
	if blank(c.ID) || blank(c.CaseID) || blank(c.AttemptID) {
		return fmt.Errorf("code change ID, case ID, and attempt ID are required")
	}
	if blank(c.Repo) || blank(c.BaseBranch) || blank(c.FixBranch) || blank(c.FixCommit) {
		return fmt.Errorf("code change repository, base branch, fix branch, and fix commit are required")
	}
	if len(c.TestEvidence) == 0 || !json.Valid(c.TestEvidence) {
		return fmt.Errorf("code change test evidence must be valid JSON")
	}
	return nil
}

type ApprovalKind string

const (
	ApprovalStartFix               ApprovalKind = "start_fix"
	ApprovalCompleteRemediation    ApprovalKind = "complete_remediation"
	ApprovalMergeEnvironmentBranch ApprovalKind = "merge_environment_branch"
)

type RemediationApprovalScope struct {
	RootCauseAttemptID string          `json:"root_cause_attempt_id"`
	CycleNumber        int             `json:"cycle_number"`
	RootCauseType      RootCauseType   `json:"root_cause_type"`
	Mode               RemediationMode `json:"mode"`
	Target             string          `json:"target"`
	RecommendedAction  string          `json:"recommended_action"`
	Rollback           string          `json:"rollback,omitempty"`
	Verification       string          `json:"verification"`
	Summary            string          `json:"summary"`
	Evidence           string          `json:"evidence"`
	BindingID          string          `json:"binding_id"`
}

type Approval struct {
	ID             string            `json:"id"`
	CaseID         string            `json:"case_id"`
	Kind           ApprovalKind      `json:"kind"`
	Actor          string            `json:"actor"`
	ApprovedAt     time.Time         `json:"approved_at"`
	CaseVersion    int64             `json:"case_version"`
	ScopeJSON      json.RawMessage   `json:"scope_json"`
	FixCommits     map[string]string `json:"fix_commits"`
	TargetBranches map[string]string `json:"target_branches"`
}

func (a Approval) Clone() Approval {
	cloned := a
	cloned.ScopeJSON = CloneRawMessage(a.ScopeJSON)
	cloned.FixCommits = CloneStringMap(a.FixCommits)
	cloned.TargetBranches = CloneStringMap(a.TargetBranches)
	return cloned
}

func (a Approval) Validate() error {
	if blank(a.ID) {
		return fmt.Errorf("approval ID is required")
	}
	if blank(a.CaseID) {
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
	if blank(a.Actor) {
		return fmt.Errorf("approval actor is required")
	}
	var scopeObject map[string]json.RawMessage
	if err := json.Unmarshal(a.ScopeJSON, &scopeObject); err != nil || len(scopeObject) == 0 {
		return fmt.Errorf("approval scope must be a non-empty JSON object")
	}
	switch a.Kind {
	case ApprovalStartFix:
		var scope struct {
			RootCauseAttemptID string `json:"root_cause_attempt_id"`
		}
		if err := json.Unmarshal(a.ScopeJSON, &scope); err != nil {
			return fmt.Errorf("decode start-fix approval scope: %w", err)
		}
		if blank(scope.RootCauseAttemptID) {
			return fmt.Errorf("start-fix approval scope requires root_cause_attempt_id")
		}
	case ApprovalCompleteRemediation:
		var scope RemediationApprovalScope
		if err := json.Unmarshal(a.ScopeJSON, &scope); err != nil {
			return fmt.Errorf("decode remediation approval scope: %w", err)
		}
		if blank(scope.RootCauseAttemptID) || scope.CycleNumber < 1 || blank(scope.Summary) || blank(scope.Evidence) || blank(scope.BindingID) {
			return fmt.Errorf("remediation approval scope is incomplete")
		}
		if err := validateRemediationPlan(scope.RootCauseType, RemediationPlan{Mode: scope.Mode, Target: scope.Target, Summary: scope.RecommendedAction, Rollback: scope.Rollback, Verification: scope.Verification}); err != nil {
			return fmt.Errorf("invalid remediation approval scope: %w", err)
		}
	case ApprovalMergeEnvironmentBranch:
		if err := validateNonEmptyStringMap("approval fix commits", a.FixCommits); err != nil {
			return err
		}
		if err := validateNonEmptyStringMap("approval target branches", a.TargetBranches); err != nil {
			return err
		}
		if !sameStringMapKeys(a.FixCommits, a.TargetBranches) {
			return fmt.Errorf("merge approval fix commits and target branches must cover the same repositories")
		}
	default:
		return fmt.Errorf("unsupported approval kind %q", a.Kind)
	}
	return nil
}

func validateNonEmptyStringMap(name string, values map[string]string) error {
	if len(values) == 0 {
		return fmt.Errorf("%s are required", name)
	}
	return validateStringMapEntries(name, values)
}

func validateStringMapEntries(name string, values map[string]string) error {
	for key, value := range values {
		if blank(key) || blank(value) {
			return fmt.Errorf("%s must not contain empty keys or values", name)
		}
	}
	return nil
}

func sameStringMapKeys(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key := range left {
		if _, ok := right[key]; !ok {
			return false
		}
	}
	return true
}

type DeploymentResult string

const (
	DeploymentResultMatched     DeploymentResult = "matched"
	DeploymentResultMismatched  DeploymentResult = "mismatched"
	DeploymentResultUnavailable DeploymentResult = "unavailable"
)

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
	ObservedAt         time.Time         `json:"observed_at"`
	DiagnosticCode     string            `json:"diagnostic_code,omitempty"`
	DiagnosticMessage  string            `json:"diagnostic_message,omitempty"`
	// VerifiedCommitAncestors records the expected ancestor proved for an
	// observed descendant commit. Exact matches do not need an entry.
	VerifiedCommitAncestors map[string]string `json:"verified_commit_ancestors,omitempty"`
	Result                  DeploymentResult  `json:"result"`
}

func (o DeploymentObservation) Clone() DeploymentObservation {
	cloned := o
	cloned.ExpectedCommits = CloneStringMap(o.ExpectedCommits)
	cloned.ObservedImages = CloneStringMap(o.ObservedImages)
	cloned.ObservedCommits = CloneStringMap(o.ObservedCommits)
	cloned.VerifiedCommitAncestors = CloneStringMap(o.VerifiedCommitAncestors)
	cloned.UserNotifiedAt = cloneTimePtr(o.UserNotifiedAt)
	cloned.VerifiedAt = cloneTimePtr(o.VerifiedAt)
	return cloned
}

func (o DeploymentObservation) Validate() error {
	if blank(o.ID) || blank(o.CaseID) {
		return fmt.Errorf("deployment observation ID and case ID are required")
	}
	if blank(o.Environment) || blank(o.VerificationSource) {
		return fmt.Errorf("deployment observation environment and verification source are required")
	}
	if o.ObservedAt.IsZero() {
		return fmt.Errorf("deployment observation observed_at is required")
	}
	if len(o.DiagnosticCode) > 64 || len(o.DiagnosticMessage) > 256 || strings.ContainsAny(o.DiagnosticCode+o.DiagnosticMessage, "\r\n") {
		return fmt.Errorf("deployment observation diagnostics must be bounded single-line text")
	}
	if len(o.ExpectedCommits) == 0 {
		if o.VerificationSource != "manual-remediation" || o.DiagnosticCode != "remediation_completed" {
			return fmt.Errorf("deployment expected commits are required")
		}
	} else if err := validateNonEmptyStringMap("deployment expected commits", o.ExpectedCommits); err != nil {
		return err
	}
	if err := validateStringMapEntries("deployment observed images", o.ObservedImages); err != nil {
		return err
	}
	if err := validateStringMapEntries("deployment observed commits", o.ObservedCommits); err != nil {
		return err
	}
	if err := validateStringMapEntries("deployment verified commit ancestors", o.VerifiedCommitAncestors); err != nil {
		return err
	}
	for repo, ancestor := range o.VerifiedCommitAncestors {
		if expected, ok := o.ExpectedCommits[repo]; !ok || ancestor != expected {
			return fmt.Errorf("deployment verified commit ancestor must bind an expected repository commit")
		}
		if observed, ok := o.ObservedCommits[repo]; !ok || observed == ancestor {
			return fmt.Errorf("deployment descendant proof requires a distinct observed repository commit")
		}
	}
	switch o.Result {
	case DeploymentResultMatched:
		if o.VerifiedAt == nil || o.VerifiedAt.IsZero() {
			return fmt.Errorf("matched deployment observation requires verified_at")
		}
		for repo, expectedCommit := range o.ExpectedCommits {
			observedCommit, ok := o.ObservedCommits[repo]
			if !ok || (observedCommit != expectedCommit && o.VerifiedCommitAncestors[repo] != expectedCommit) {
				return fmt.Errorf("matched deployment observation requires every expected repository commit")
			}
		}
		return nil
	case DeploymentResultMismatched, DeploymentResultUnavailable:
		return nil
	default:
		return fmt.Errorf("unsupported deployment observation result %q", o.Result)
	}
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

func (e TransitionEvent) Clone() TransitionEvent {
	cloned := e
	cloned.PayloadJSON = CloneRawMessage(e.PayloadJSON)
	return cloned
}

func (e TransitionEvent) Validate() error {
	if blank(e.ID) || blank(e.CaseID) {
		return fmt.Errorf("transition event ID and case ID are required")
	}
	if !CanTransition(e.FromStatus, e.ToStatus) {
		return fmt.Errorf("transition event has invalid status edge %s -> %s", e.FromStatus, e.ToStatus)
	}
	if blank(e.EventType) || blank(e.ActorType) || blank(e.ActorID) || blank(e.IdempotencyKey) {
		return fmt.Errorf("transition event type, actor type, actor ID, and idempotency key are required")
	}
	if len(e.PayloadJSON) == 0 || !json.Valid(e.PayloadJSON) {
		return fmt.Errorf("transition event payload must be valid JSON")
	}
	return nil
}

func CloneRawMessage(value json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}
	return append(json.RawMessage(nil), value...)
}

func validateJSONObject(name string, value json.RawMessage, allowEmpty bool) error {
	if len(strings.TrimSpace(string(value))) == 0 {
		return fmt.Errorf("%s must be a JSON object", name)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil || object == nil {
		return fmt.Errorf("%s must be a JSON object", name)
	}
	if !allowEmpty && len(object) == 0 {
		return fmt.Errorf("%s must be a non-empty JSON object", name)
	}
	return nil
}

func CloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func blank(value string) bool {
	return strings.TrimSpace(value) == ""
}
