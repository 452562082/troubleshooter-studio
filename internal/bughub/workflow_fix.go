package bughub

import (
	"context"
	"errors"
	"fmt"
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
	if store == nil || strings.TrimSpace(attemptID) == "" || attemptID != incident.CurrentAttemptID {
		return ErrApprovalScope
	}
	attempts, err := store.ListAttempts(ctx, AttemptFilter{CaseID: incident.ID})
	if err != nil {
		return err
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
		return ErrApprovalScope
	}
	result, err := ParseInvestigationResult(latest.OutputJSON)
	if err != nil || result.InvestigationStatus != "root_cause_ready" || result.Confidence != "high" || len(result.Gaps) != 0 || result.Environment != incident.Environment {
		return ErrApprovalScope
	}
	return nil
}

func ParseFixResult(data []byte) (FixResult, error) {
	var result FixResult
	if err := decodeStrictYAML(data, &result); err != nil {
		return FixResult{}, fmt.Errorf("parse fix result: %w", err)
	}
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
