package bughub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
)

type FixBranchResult struct {
	Repo                    string `yaml:"repo" json:"repo"`
	BaseBranch              string `yaml:"base_branch" json:"base_branch"`
	FixBranch               string `yaml:"fix_branch" json:"fix_branch"`
	Commit                  string `yaml:"commit" json:"commit"`
	Pushed                  bool   `yaml:"pushed" json:"pushed"`
	TargetEnvironmentBranch string `yaml:"target_environment_branch" json:"target_environment_branch"`
	PushRemote              string `yaml:"push_remote" json:"push_remote"`
}

type FixChangeResult struct {
	Repo    string `yaml:"repo" json:"repo"`
	Summary string `yaml:"summary" json:"summary"`
}

type FixTestResult struct {
	Repo          string `yaml:"repo" json:"repo"`
	Commit        string `yaml:"commit" json:"commit"`
	Command       string `yaml:"command" json:"command"`
	Result        string `yaml:"result" json:"result"`
	Note          string `yaml:"note,omitempty" json:"note,omitempty"`
	SkippedReason string `yaml:"skipped_reason,omitempty" json:"skipped_reason,omitempty"`
}

type FixResult struct {
	FixStatus        string              `yaml:"fix_status" json:"fix_status"`
	Environment      string              `yaml:"environment" json:"environment"`
	Branches         []FixBranchResult   `yaml:"branches" json:"branches"`
	Changes          []FixChangeResult   `yaml:"changes" json:"changes"`
	Tests            []FixTestResult     `yaml:"tests" json:"tests"`
	DeploymentNotice string              `yaml:"deployment_notice" json:"deployment_notice"`
	Risks            []string            `yaml:"risks" json:"risks"`
	BlockedReason    string              `yaml:"blocked_reason,omitempty" json:"blocked_reason,omitempty"`
	Evidence         []ArtifactReference `yaml:"evidence" json:"evidence"`
}

func StartFixApprovalKey(caseID, rootCauseAttemptID string, caseVersion int64) string {
	return fmt.Sprintf("start-fix:%s:%s:%d", strings.TrimSpace(caseID), strings.TrimSpace(rootCauseAttemptID), caseVersion)
}

func validateFixApprovalRootCause(ctx context.Context, store *CaseStore, incident IncidentCase, attemptID string) error {
	result, err := validatedRootCauseResult(ctx, store, incident, attemptID)
	if err != nil {
		return err
	}
	if !result.UsesCodeFixWorkflow() {
		return ErrApprovalScope
	}
	return nil
}

func validatedRootCauseResult(ctx context.Context, store *CaseStore, incident IncidentCase, attemptID string) (InvestigationResult, error) {
	if store == nil || strings.TrimSpace(attemptID) == "" || attemptID != incident.CurrentAttemptID {
		return InvestigationResult{}, ErrApprovalScope
	}
	attempts, err := store.ListAttempts(ctx, AttemptFilter{CaseID: incident.ID})
	if err != nil {
		return InvestigationResult{}, err
	}
	var latest *PhaseAttempt
	for index := range attempts {
		attempt := attempts[index]
		if attempt.Phase == PhaseInvestigation && attempt.CycleNumber == incident.CycleNumber {
			copy := attempt.Clone()
			latest = &copy
		}
	}
	if latest == nil || latest.ID != attemptID || latest.Status != AttemptStatusSucceeded {
		return InvestigationResult{}, ErrApprovalScope
	}
	result, err := ParseInvestigationResult(latest.OutputJSON)
	if err != nil || result.InvestigationStatus != "root_cause_ready" || result.Confidence != "high" || len(result.Gaps) != 0 || result.Environment != incident.Environment {
		return InvestigationResult{}, ErrApprovalScope
	}
	return result, nil
}

func ParseFixResult(data []byte) (FixResult, error) {
	var result FixResult
	if err := decodeStrictYAML(data, &result); err != nil {
		return FixResult{}, fmt.Errorf("parse fix result: %w", err)
	}
	normalizeFixResult(&result)
	if strings.TrimSpace(result.FixStatus) == "" || strings.TrimSpace(result.Environment) == "" {
		return FixResult{}, errors.New("fix status and environment are required")
	}
	if result.Branches == nil || result.Changes == nil || result.Tests == nil || result.Risks == nil {
		return FixResult{}, errors.New("fix branches, changes, tests, and risks fields are required")
	}
	if strings.TrimSpace(result.DeploymentNotice) == "" {
		return FixResult{}, errors.New("fix deployment notice is required")
	}
	switch result.FixStatus {
	case "fixed_pushed":
		if err := validateFixedPushedResult(result); err != nil {
			return FixResult{}, err
		}
	case "blocked", "failed":
		if strings.TrimSpace(result.BlockedReason) == "" {
			return FixResult{}, fmt.Errorf("%s requires blocked_reason", result.FixStatus)
		}
	default:
		return FixResult{}, fmt.Errorf("unsupported fix status %q", result.FixStatus)
	}
	return result, nil
}

func normalizeFixResult(result *FixResult) {
	result.FixStatus = strings.TrimSpace(result.FixStatus)
	result.Environment = strings.TrimSpace(result.Environment)
	result.DeploymentNotice = strings.TrimSpace(result.DeploymentNotice)
	result.BlockedReason = strings.TrimSpace(result.BlockedReason)
	for index := range result.Branches {
		branch := &result.Branches[index]
		branch.Repo = strings.TrimSpace(branch.Repo)
		branch.BaseBranch = strings.TrimSpace(branch.BaseBranch)
		branch.FixBranch = strings.TrimSpace(branch.FixBranch)
		branch.Commit = strings.TrimSpace(branch.Commit)
		branch.TargetEnvironmentBranch = strings.TrimSpace(branch.TargetEnvironmentBranch)
		branch.PushRemote = strings.TrimSpace(branch.PushRemote)
	}
	for index := range result.Changes {
		result.Changes[index].Repo = strings.TrimSpace(result.Changes[index].Repo)
		result.Changes[index].Summary = strings.TrimSpace(result.Changes[index].Summary)
	}
	for index := range result.Tests {
		test := &result.Tests[index]
		test.Repo = strings.TrimSpace(test.Repo)
		test.Commit = strings.TrimSpace(test.Commit)
		test.Command = strings.TrimSpace(test.Command)
		test.Result = strings.TrimSpace(test.Result)
		test.Note = strings.TrimSpace(test.Note)
		test.SkippedReason = strings.TrimSpace(test.SkippedReason)
	}
}

func validateFixedPushedResult(result FixResult) error {
	if len(result.Branches) == 0 {
		return errors.New("fixed_pushed requires branches")
	}
	branches := make(map[string]FixBranchResult, len(result.Branches))
	for _, branch := range result.Branches {
		if strings.TrimSpace(branch.Repo) == "" || strings.TrimSpace(branch.BaseBranch) == "" || strings.TrimSpace(branch.FixBranch) == "" || strings.TrimSpace(branch.Commit) == "" || !branch.Pushed || strings.TrimSpace(branch.TargetEnvironmentBranch) == "" || strings.TrimSpace(branch.PushRemote) == "" {
			return errors.New("fixed_pushed requires successful push, commit, and branch context for every repository")
		}
		if _, exists := branches[branch.Repo]; exists {
			return fmt.Errorf("fixed_pushed contains duplicate repository %q", branch.Repo)
		}
		if branch.FixBranch == branch.BaseBranch || branch.FixBranch == branch.TargetEnvironmentBranch {
			return fmt.Errorf("fixed_pushed fix branch for %s must differ from the source baseline and environment branches", branch.Repo)
		}
		branches[branch.Repo] = branch
	}
	changed := make(map[string]bool, len(branches))
	for _, change := range result.Changes {
		if strings.TrimSpace(change.Repo) == "" || strings.TrimSpace(change.Summary) == "" {
			return errors.New("fixed_pushed change requires repository and summary")
		}
		if _, ok := branches[change.Repo]; !ok {
			return fmt.Errorf("change references unreported repository %q", change.Repo)
		}
		changed[change.Repo] = true
	}
	tested := make(map[string]bool, len(branches))
	for _, test := range result.Tests {
		branch, ok := branches[test.Repo]
		if !ok || strings.TrimSpace(test.Commit) == "" || test.Commit != branch.Commit || strings.TrimSpace(test.Command) == "" {
			return errors.New("fixed_pushed test evidence must match a reported repository and commit")
		}
		switch test.Result {
		case "passed":
		case "skipped":
			if strings.TrimSpace(test.SkippedReason) == "" {
				return errors.New("skipped fix test requires skipped_reason")
			}
		default:
			return fmt.Errorf("fixed_pushed test result must be passed or explicitly skipped, got %q", test.Result)
		}
		tested[test.Repo] = true
	}
	for repo := range branches {
		if !changed[repo] {
			return fmt.Errorf("fixed_pushed requires a change summary for %s", repo)
		}
		if !tested[repo] {
			return fmt.Errorf("fixed_pushed requires test evidence or an explicit skip for %s", repo)
		}
	}
	return nil
}

func validateFixCompletionPayload(command CompleteAttemptCommand) error {
	if command.Outcome != PhaseOutcomeFixPushed {
		if len(command.CodeChanges) != 0 {
			return errors.New("non-fix-pushed completion must not contain code changes")
		}
		return nil
	}
	if len(command.CodeChanges) == 0 {
		return errors.New("fix-pushed completion requires at least one code change")
	}
	result, err := ParseFixResult(command.OutputJSON)
	if err != nil {
		return fmt.Errorf("validate fix-pushed output: %w", err)
	}
	if result.FixStatus != "fixed_pushed" {
		return fmt.Errorf("fix-pushed outcome does not match fix status %q", result.FixStatus)
	}
	if len(command.CodeChanges) != len(result.Branches) {
		return errors.New("fix-pushed code changes do not cover every output repository")
	}
	branches := make(map[string]FixBranchResult, len(result.Branches))
	for _, branch := range result.Branches {
		branches[branch.Repo] = branch
	}
	seen := make(map[string]struct{}, len(command.CodeChanges))
	for _, change := range command.CodeChanges {
		if _, duplicate := seen[change.Repo]; duplicate {
			return fmt.Errorf("fix-pushed completion contains duplicate repository %q", change.Repo)
		}
		seen[change.Repo] = struct{}{}
		branch, found := branches[change.Repo]
		if !found {
			return fmt.Errorf("fix-pushed code change repository %q is absent from output", change.Repo)
		}
		if change.BaseBranch != branch.BaseBranch || change.FixBranch != branch.FixBranch || change.FixCommit != branch.Commit || change.PushStatus != "pushed" || change.TargetEnvironmentBranch != branch.TargetEnvironmentBranch || change.PushRemote != branch.PushRemote {
			return fmt.Errorf("fix-pushed code change for %s diverges from output branch scope", change.Repo)
		}
		expectedTests := make([]FixTestResult, 0)
		for _, test := range result.Tests {
			if test.Repo == change.Repo {
				expectedTests = append(expectedTests, test)
			}
		}
		actualTests, err := decodeFixTestEvidence(change.TestEvidence)
		if err != nil {
			return fmt.Errorf("decode test evidence for %s: %w", change.Repo, err)
		}
		if !reflect.DeepEqual(actualTests, expectedTests) {
			return fmt.Errorf("fix-pushed test evidence for %s diverges from output", change.Repo)
		}
	}
	return nil
}

func validateCompletionAttemptPhase(phase Phase, command CompleteAttemptCommand) error {
	if command.Outcome == PhaseOutcomeFixPushed && phase != PhaseFix {
		return errors.New("fix-pushed completion requires a fix phase attempt")
	}
	if command.Outcome == PhaseOutcomeSystemFailed && phase != PhaseValidation && phase != PhaseRegression {
		return errors.New("system-failed completion requires a validation or regression attempt")
	}
	return nil
}

func decodeFixTestEvidence(raw json.RawMessage) ([]FixTestResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var tests []FixTestResult
	if err := decoder.Decode(&tests); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("fix test evidence must contain one JSON array")
	}
	return tests, nil
}
