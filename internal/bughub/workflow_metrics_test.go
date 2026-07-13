package bughub

import (
	"math"
	"testing"
	"time"
)

func TestWorkflowMetricsSaturatesExtremeCrossCaseTotals(t *testing.T) {
	t0 := time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)
	t1 := time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)
	histories := []WorkflowCaseHistory{
		{Case: IncidentCase{ID: "a", BugID: "a", Status: CaseWaitingDeployment, CycleNumber: 1, CreatedAt: t0, UpdatedAt: t0}, Events: []TransitionEvent{metricEvent("a-wait", CaseMerging, CaseWaitingDeployment, "merge_pushed", "git", t0), metricEvent("a-done", CaseWaitingDeployment, CaseDeploymentVerified, "deployment_verified", "studio", t1)}, Attempts: []PhaseAttempt{{ID: "a1", CaseID: "a", CycleNumber: 1, Phase: PhaseFix, Status: AttemptStatusSucceeded, Usage: AgentUsage{Duration: time.Duration(math.MaxInt64), InputTokens: math.MaxInt64, OutputTokens: math.MaxInt64}}}},
		{Case: IncidentCase{ID: "b", BugID: "b", Status: CaseWaitingDeployment, CycleNumber: 1, CreatedAt: t0, UpdatedAt: t0}, Events: []TransitionEvent{metricEvent("b-wait", CaseMerging, CaseWaitingDeployment, "merge_pushed", "git", t0), metricEvent("b-done", CaseWaitingDeployment, CaseDeploymentVerified, "deployment_verified", "studio", t1)}, Attempts: []PhaseAttempt{{ID: "b1", CaseID: "b", CycleNumber: 1, Phase: PhaseFix, Status: AttemptStatusSucceeded, Usage: AgentUsage{Duration: time.Second, InputTokens: 1, OutputTokens: 1}}}},
	}
	metrics := FoldWorkflowMetrics(t1, histories)
	if metrics.AgentExecutionDuration != time.Duration(math.MaxInt64) || metrics.HumanDeploymentWait != time.Duration(math.MaxInt64) || metrics.AgentInputTokens != math.MaxInt64 || metrics.AgentOutputTokens != math.MaxInt64 {
		t.Fatalf("overflowed totals=%+v", metrics)
	}
	if saturatingAddInt(math.MaxInt, 1) != math.MaxInt || saturatingAddInt64(math.MaxInt64, 1) != math.MaxInt64 || saturatingAddDuration(time.Duration(math.MaxInt64), time.Second) != time.Duration(math.MaxInt64) {
		t.Fatal("saturating helpers wrapped")
	}
}

func TestWorkflowMetricsFoldSeparatesAgentExecutionAndHumanWait(t *testing.T) {
	t0 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	closed := t0.Add(28 * time.Hour)
	incident := IncidentCase{ID: "fixed", BugID: "bug-1", Status: CaseFixedVerified, CycleNumber: 1, CreatedAt: t0, UpdatedAt: closed, ClosedAt: &closed}
	events := []TransitionEvent{
		metricEvent("v-start", CasePendingValidation, CaseValidating, "validation_started", "studio", t0.Add(time.Hour)),
		metricEvent("v-done", CaseValidating, CaseReproduced, "validation_reproduced", "agent", t0.Add(3*time.Hour)),
		metricEvent("i-start", CaseReproduced, CaseInvestigating, "investigation_started", "studio", t0.Add(3*time.Hour)),
		metricEvent("i-done", CaseInvestigating, CaseRootCauseReady, "root_cause_ready", "agent", t0.Add(7*time.Hour)),
		metricEvent("f-wait", CaseRootCauseReady, CaseWaitingFixApproval, "fix_waiting_approval", "studio", t0.Add(7*time.Hour)),
		metricEvent("f-start", CaseWaitingFixApproval, CaseFixing, "fix_approved", "user", t0.Add(9*time.Hour)),
		metricEvent("f-done", CaseFixing, CaseFixPushed, "fix_pushed", "agent", t0.Add(14*time.Hour)),
		metricEvent("d-wait", CaseMerging, CaseWaitingDeployment, "merge_pushed", "git", t0.Add(15*time.Hour)),
		metricEvent("d-done", CaseWaitingDeployment, CaseDeploymentVerified, "deployment_verified", "studio", t0.Add(25*time.Hour)),
		metricEvent("r-start", CaseDeploymentVerified, CaseRegressionValidating, "regression_started", "studio", t0.Add(25*time.Hour)),
		metricEvent("r-done", CaseRegressionValidating, CaseFixedVerified, "regression_fixed", "agent", closed),
	}
	attempts := []PhaseAttempt{
		{ID: "v1", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusFailed, StartedAt: t0.Add(time.Hour), Usage: AgentUsage{Duration: time.Hour, InputTokens: 100, OutputTokens: 10}},
		{ID: "v2", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseValidation, Mode: AttemptReproduce, Status: AttemptStatusSucceeded, StartedAt: t0.Add(2 * time.Hour), Usage: AgentUsage{Duration: 90 * time.Minute, InputTokens: 200, OutputTokens: 20}},
		{ID: "i1", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseInvestigation, Status: AttemptStatusSucceeded, StartedAt: t0.Add(3 * time.Hour), Usage: AgentUsage{Duration: 3 * time.Hour}},
		{ID: "f1", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseFix, Status: AttemptStatusSucceeded, StartedAt: t0.Add(9 * time.Hour), Usage: AgentUsage{Duration: 4 * time.Hour}},
		{ID: "r1", CaseID: incident.ID, CycleNumber: 1, Phase: PhaseRegression, Mode: AttemptRegression, Status: AttemptStatusSucceeded, StartedAt: t0.Add(25 * time.Hour), Usage: AgentUsage{Duration: 2 * time.Hour}},
	}

	metrics := FoldWorkflowMetrics(t0.Add(40*time.Hour), []WorkflowCaseHistory{{Case: incident, Events: events, Attempts: attempts}})
	if metrics.CompletedCases != 1 || metrics.OpenCases != 0 {
		t.Fatalf("case counts=%+v", metrics)
	}
	wantStages := map[string]time.Duration{"validation": 2 * time.Hour, "investigation": 4 * time.Hour, "fix": 5 * time.Hour, "deployment_wait": 10 * time.Hour, "regression": 3 * time.Hour, "lead_time": 28 * time.Hour}
	for stage, want := range wantStages {
		if got := metrics.MedianStageDuration[stage]; got != want {
			t.Errorf("stage %s=%s want %s", stage, got, want)
		}
	}
	if metrics.AgentExecutionDuration != 11*time.Hour+30*time.Minute {
		t.Errorf("agent execution=%s", metrics.AgentExecutionDuration)
	}
	if metrics.HumanDeploymentWait != 10*time.Hour {
		t.Errorf("deployment wait=%s", metrics.HumanDeploymentWait)
	}
	if metrics.RetryCount != 1 {
		t.Errorf("retries=%d", metrics.RetryCount)
	}
	if metrics.AgentInputTokens != 300 || metrics.AgentOutputTokens != 30 {
		t.Errorf("tokens=%d/%d", metrics.AgentInputTokens, metrics.AgentOutputTokens)
	}
	if metrics.FirstRegressionSuccessRate != 1 || metrics.StillReproducesRate != 0 || metrics.AutomationRatio != 1 {
		t.Errorf("rates=%+v", metrics)
	}
}

func TestWorkflowMetricsFoldCountsBlockersStillReproducesAndCurrentWaitAge(t *testing.T) {
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	histories := []WorkflowCaseHistory{
		{Case: IncidentCase{ID: "evidence", BugID: "b1", Status: CaseWaitingEvidence, CycleNumber: 1, CreatedAt: now.Add(-6 * time.Hour)}, Events: []TransitionEvent{metricEvent("e", CaseValidating, CaseWaitingEvidence, "validation_waiting_evidence", "agent", now.Add(-5*time.Hour))}},
		{Case: IncidentCase{ID: "conflict", BugID: "b2", Status: CaseMergeConflict, CycleNumber: 1, CreatedAt: now.Add(-5 * time.Hour)}, Events: []TransitionEvent{metricEvent("m", CaseMerging, CaseMergeConflict, "merge_conflict", "git", now.Add(-4*time.Hour))}},
		{Case: IncidentCase{ID: "unverified", BugID: "b3", Status: CaseDeploymentUnverified, CycleNumber: 1, CreatedAt: now.Add(-4 * time.Hour)}, Events: []TransitionEvent{metricEvent("u", CaseWaitingDeployment, CaseDeploymentUnverified, "deployment_unverified", "studio", now.Add(-3*time.Hour))}},
		{Case: IncidentCase{ID: "waiting", BugID: "b4", Status: CaseWaitingDeployment, CycleNumber: 1, CreatedAt: now.Add(-30 * time.Hour)}, Events: []TransitionEvent{metricEvent("w", CaseMerging, CaseWaitingDeployment, "merge_pushed", "git", now.Add(-26*time.Hour))}},
		{Case: IncidentCase{ID: "repeat", BugID: "b5", Status: CaseInvestigating, CycleNumber: 2, CreatedAt: now.Add(-20 * time.Hour)}, Events: []TransitionEvent{
			metricEvent("r1", CaseRegressionValidating, CaseStillReproduces, "regression_still_reproduces", "agent", now.Add(-10*time.Hour)),
			metricEvent("r2", CaseStillReproduces, CaseInvestigating, "next_cycle_investigation_started", "studio", now.Add(-10*time.Hour)),
		}},
	}

	metrics := FoldWorkflowMetrics(now, histories)
	if metrics.BlockerDistribution["waiting_evidence"] != 1 || metrics.BlockerDistribution["merge_conflict"] != 1 || metrics.BlockerDistribution["deployment_unverified"] != 1 {
		t.Fatalf("blockers=%v", metrics.BlockerDistribution)
	}
	if metrics.StillReproducesRate != 1 || metrics.FirstRegressionSuccessRate != 0 {
		t.Fatalf("regression rates=%+v", metrics)
	}
	if metrics.OldestWaitingDeploymentAge != 26*time.Hour {
		t.Fatalf("wait age=%s", metrics.OldestWaitingDeploymentAge)
	}
}

func TestWorkflowMetricsExcludesArchivedCasesFromOutcomes(t *testing.T) {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	closed := now.Add(-time.Hour)
	histories := []WorkflowCaseHistory{
		{Case: IncidentCase{ID: "legacy", BugID: "legacy", Status: CaseLegacyArchived, CycleNumber: 1, CreatedAt: now.Add(-3 * time.Hour), ClosedAt: &closed}},
		{Case: IncidentCase{ID: "reset", BugID: "reset", Status: CaseResetArchived, CycleNumber: 1, CreatedAt: now.Add(-2 * time.Hour), ClosedAt: &closed}},
	}

	metrics := FoldWorkflowMetrics(now, histories)
	if metrics.CompletedCases != 0 || metrics.OpenCases != 0 {
		t.Fatalf("archived cases counted in outcomes: %+v", metrics)
	}
	if _, found := metrics.MedianStageDuration[WorkflowStageLeadTime]; found {
		t.Fatalf("archived cases contributed lead time: %+v", metrics.MedianStageDuration)
	}
}

func metricEvent(id string, from, to CaseStatus, eventType, actor string, at time.Time) TransitionEvent {
	return TransitionEvent{ID: id, CaseID: "case", FromStatus: from, ToStatus: to, EventType: eventType, ActorType: actor, ActorID: actor, IdempotencyKey: id, PayloadJSON: []byte(`{}`), CreatedAt: at}
}
