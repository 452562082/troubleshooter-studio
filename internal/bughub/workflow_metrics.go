package bughub

import (
	"context"
	"math"
	"sort"
	"strconv"
	"time"
)

const (
	WorkflowStageValidation     = "validation"
	WorkflowStageInvestigation  = "investigation"
	WorkflowStageFix            = "fix"
	WorkflowStageDeploymentWait = "deployment_wait"
	WorkflowStageRegression     = "regression"
	WorkflowStageLeadTime       = "lead_time"
)

// WorkflowCaseHistory is a read-only projection used by the deterministic
// metrics fold. It contains no callback capable of advancing a Case.
type WorkflowCaseHistory struct {
	Case     IncidentCase
	Events   []TransitionEvent
	Attempts []PhaseAttempt
}

// WorkflowMetrics is derived entirely from durable workflow history. Duration
// values use time.Duration (nanoseconds in JSON), matching PhaseAttempt usage.
type WorkflowMetrics struct {
	CompletedCases             int                      `json:"completed_cases"`
	OpenCases                  int                      `json:"open_cases"`
	MedianStageDuration        map[string]time.Duration `json:"median_stage_duration"`
	OldestWaitingDeploymentAge time.Duration            `json:"oldest_waiting_deployment_age"`
	AgentExecutionDuration     time.Duration            `json:"agent_execution_duration"`
	HumanDeploymentWait        time.Duration            `json:"human_deployment_wait"`
	RetryCount                 int                      `json:"retry_count"`
	AgentInputTokens           int64                    `json:"agent_input_tokens"`
	AgentOutputTokens          int64                    `json:"agent_output_tokens"`
	BlockerDistribution        map[string]int           `json:"blocker_distribution"`
	AutomationRatio            float64                  `json:"automation_ratio"`
	FirstRegressionSuccessRate float64                  `json:"first_regression_success_rate"`
	StillReproducesRate        float64                  `json:"still_reproduces_rate"`
}

// WorkflowMetrics returns a point-in-time read-only projection. It performs no
// Case mutation and deliberately does not call the orchestrator.
func (s *CaseStore) WorkflowMetrics(ctx context.Context, now time.Time) (WorkflowMetrics, error) {
	cases, err := s.ListCases(ctx)
	if err != nil {
		return WorkflowMetrics{}, err
	}
	histories := make([]WorkflowCaseHistory, 0, len(cases))
	for _, incident := range cases {
		events, err := s.ListEvents(ctx, incident.ID)
		if err != nil {
			return WorkflowMetrics{}, err
		}
		attempts, err := s.ListAttempts(ctx, AttemptFilter{CaseID: incident.ID})
		if err != nil {
			return WorkflowMetrics{}, err
		}
		histories = append(histories, WorkflowCaseHistory{Case: incident, Events: events, Attempts: attempts})
	}
	return FoldWorkflowMetrics(now, histories), nil
}

func FoldWorkflowMetrics(now time.Time, histories []WorkflowCaseHistory) WorkflowMetrics {
	now = now.UTC()
	metrics := WorkflowMetrics{
		MedianStageDuration: make(map[string]time.Duration),
		BlockerDistribution: map[string]int{
			string(CaseWaitingEvidence):      0,
			string(CaseMergeConflict):        0,
			string(CaseDeploymentUnverified): 0,
		},
	}
	durations := map[string][]time.Duration{}
	automatic, eligible := 0, 0
	firstRegressionSuccesses, firstRegressionCases := 0, 0
	still, regressionOutcomes := 0, 0

	for _, history := range histories {
		incident := history.Case
		if incident.Status != CaseLegacyArchived && incident.Status != CaseResetArchived {
			if incident.ClosedAt != nil {
				metrics.CompletedCases = saturatingAddInt(metrics.CompletedCases, 1)
			} else {
				metrics.OpenCases = saturatingAddInt(metrics.OpenCases, 1)
			}
		}

		events := append([]TransitionEvent(nil), history.Events...)
		sort.SliceStable(events, func(i, j int) bool {
			if events[i].CreatedAt.Equal(events[j].CreatedAt) {
				return events[i].ID < events[j].ID
			}
			return events[i].CreatedAt.Before(events[j].CreatedAt)
		})
		entered := map[CaseStatus]time.Time{CasePendingValidation: incident.CreatedAt.UTC()}
		if len(events) > 0 && events[0].FromStatus != CasePendingValidation {
			entered[events[0].FromStatus] = incident.CreatedAt.UTC()
		}
		firstRegressionSeen := false
		for _, event := range events {
			at := event.CreatedAt.UTC()
			if event.FromStatus == event.ToStatus { // audit events do not split stage clocks
				continue
			}
			startedAt, found := entered[event.FromStatus]
			if stage := metricStageForStatus(event.FromStatus); stage != "" && found && !at.Before(startedAt) {
				d := at.Sub(startedAt)
				durations[stage] = append(durations[stage], d)
				if stage == WorkflowStageDeploymentWait {
					metrics.HumanDeploymentWait = saturatingAddDuration(metrics.HumanDeploymentWait, d)
				}
			}
			if isWorkflowBlocker(event.ToStatus) {
				key := string(event.ToStatus)
				metrics.BlockerDistribution[key] = saturatingAddInt(metrics.BlockerDistribution[key], 1)
			}
			if isAutomationOutcome(event.ToStatus) {
				eligible = saturatingAddInt(eligible, 1)
				if event.ActorType != "user" && event.ActorType != "human" {
					automatic = saturatingAddInt(automatic, 1)
				}
			}
			if event.FromStatus == CaseRegressionValidating && (event.ToStatus == CaseFixedVerified || event.ToStatus == CaseStillReproduces) {
				regressionOutcomes = saturatingAddInt(regressionOutcomes, 1)
				if event.ToStatus == CaseStillReproduces {
					still = saturatingAddInt(still, 1)
				}
				if !firstRegressionSeen {
					firstRegressionSeen = true
					firstRegressionCases = saturatingAddInt(firstRegressionCases, 1)
					if event.ToStatus == CaseFixedVerified {
						firstRegressionSuccesses = saturatingAddInt(firstRegressionSuccesses, 1)
					}
				}
			}
			entered[event.ToStatus] = at
		}
		if incident.Status == CaseWaitingDeployment {
			waitSince := entered[CaseWaitingDeployment]
			if waitSince.IsZero() {
				waitSince = incident.UpdatedAt.UTC()
			}
			if !now.Before(waitSince) && now.Sub(waitSince) > metrics.OldestWaitingDeploymentAge {
				metrics.OldestWaitingDeploymentAge = now.Sub(waitSince)
			}
		}
		leadEnd := now
		if incident.ClosedAt != nil {
			leadEnd = incident.ClosedAt.UTC()
		}
		if !leadEnd.Before(incident.CreatedAt) && incident.Status != CaseLegacyArchived && incident.Status != CaseResetArchived {
			durations[WorkflowStageLeadTime] = append(durations[WorkflowStageLeadTime], leadEnd.Sub(incident.CreatedAt.UTC()))
		}

		attemptGroups := make(map[string]int)
		for _, attempt := range history.Attempts {
			if attempt.Phase == PhaseLegacy {
				continue
			}
			metrics.AgentExecutionDuration = saturatingAddDuration(metrics.AgentExecutionDuration, attempt.Usage.Duration)
			metrics.AgentInputTokens = saturatingAddInt64(metrics.AgentInputTokens, attempt.Usage.InputTokens)
			metrics.AgentOutputTokens = saturatingAddInt64(metrics.AgentOutputTokens, attempt.Usage.OutputTokens)
			key := string(attempt.Phase) + ":" + string(attempt.Mode) + ":" + strconv.Itoa(attempt.CycleNumber)
			attemptGroups[key] = saturatingAddInt(attemptGroups[key], 1)
		}
		for _, count := range attemptGroups {
			if count > 1 {
				metrics.RetryCount = saturatingAddInt(metrics.RetryCount, count-1)
			}
		}
	}
	for stage, values := range durations {
		metrics.MedianStageDuration[stage] = medianDuration(values)
	}
	if eligible > 0 {
		metrics.AutomationRatio = float64(automatic) / float64(eligible)
	}
	if firstRegressionCases > 0 {
		metrics.FirstRegressionSuccessRate = float64(firstRegressionSuccesses) / float64(firstRegressionCases)
	}
	if regressionOutcomes > 0 {
		metrics.StillReproducesRate = float64(still) / float64(regressionOutcomes)
	}
	return metrics
}

func saturatingAddInt(left, right int) int {
	if right > 0 && left > math.MaxInt-right {
		return math.MaxInt
	}
	if right < 0 && left < math.MinInt-right {
		return math.MinInt
	}
	return left + right
}

func saturatingAddInt64(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	if right < 0 && left < math.MinInt64-right {
		return math.MinInt64
	}
	return left + right
}

func saturatingAddDuration(left, right time.Duration) time.Duration {
	return time.Duration(saturatingAddInt64(int64(left), int64(right)))
}

func metricStageForStatus(status CaseStatus) string {
	switch status {
	case CaseValidating:
		return WorkflowStageValidation
	case CaseInvestigating:
		return WorkflowStageInvestigation
	case CaseFixing:
		return WorkflowStageFix
	case CaseWaitingDeployment:
		return WorkflowStageDeploymentWait
	case CaseRegressionValidating:
		return WorkflowStageRegression
	default:
		return ""
	}
}

func isWorkflowBlocker(status CaseStatus) bool {
	return status == CaseWaitingEvidence || status == CaseMergeConflict || status == CaseDeploymentUnverified
}

func isAutomationOutcome(status CaseStatus) bool {
	switch status {
	case CaseReproduced, CaseNotReproduced, CaseRootCauseReady, CaseFixPushed,
		CaseWaitingDeployment, CaseDeploymentVerified, CaseFixedVerified, CaseStillReproduces:
		return true
	default:
		return false
	}
}

func medianDuration(values []time.Duration) time.Duration {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	middle := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return ordered[middle]
	}
	return ordered[middle-1] + (ordered[middle]-ordered[middle-1])/2
}
