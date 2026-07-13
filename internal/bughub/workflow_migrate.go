package bughub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

type LegacyImportResult struct {
	Cases    int
	Attempts int
}

func ImportLegacyRuns(ctx context.Context, store *CaseStore, runsPath string) (LegacyImportResult, error) {
	if store == nil {
		return LegacyImportResult{}, fmt.Errorf("case store is required")
	}
	if strings.TrimSpace(runsPath) == "" {
		return LegacyImportResult{}, fmt.Errorf("legacy runs path is required")
	}
	data, err := os.ReadFile(runsPath)
	if err != nil {
		return LegacyImportResult{}, fmt.Errorf("read legacy runs: %w", err)
	}
	fileDigest := sha256.Sum256(data)
	migrationKey := "runs-json-v1:" + hex.EncodeToString(fileDigest[:])
	if len(strings.TrimSpace(string(data))) == 0 {
		data = []byte("[]")
	}
	var runs []InvestigationRun
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&runs); err != nil {
		return LegacyImportResult{}, fmt.Errorf("decode legacy runs: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return LegacyImportResult{}, fmt.Errorf("decode legacy runs: trailing JSON value")
	}
	uniqueRuns := make(map[string]InvestigationRun, len(runs))
	for index, run := range runs {
		if strings.TrimSpace(run.ID) == "" || strings.TrimSpace(run.BugID) == "" {
			return LegacyImportResult{}, fmt.Errorf("legacy run %d requires ID and bug ID", index)
		}
		if previous, ok := uniqueRuns[run.ID]; ok {
			if !sameLegacyRun(previous, run) {
				return LegacyImportResult{}, fmt.Errorf("conflicting duplicate legacy run %q", run.ID)
			}
			continue
		}
		uniqueRuns[run.ID] = run
	}
	for id, run := range uniqueRuns {
		uniqueRuns[id] = redactLegacyRun(run)
	}

	byBug := make(map[string][]InvestigationRun)
	runBug := make(map[string]string, len(uniqueRuns))
	for _, run := range uniqueRuns {
		byBug[run.BugID] = append(byBug[run.BugID], run)
		runBug[run.ID] = run.BugID
	}
	bugIDs := make([]string, 0, len(byBug))
	for bugID := range byBug {
		bugIDs = append(bugIDs, bugID)
	}
	sort.Strings(bugIDs)
	batch := legacyImportBatch{MigrationKey: migrationKey}
	for _, bugID := range bugIDs {
		bugRuns := byBug[bugID]
		sort.Slice(bugRuns, func(i, j int) bool {
			if bugRuns[i].StartedAt.Equal(bugRuns[j].StartedAt) {
				return bugRuns[i].ID < bugRuns[j].ID
			}
			return bugRuns[i].StartedAt.Before(bugRuns[j].StartedAt)
		})
		caseID := deterministicWorkflowID("legacy-case:" + bugID)
		createdAt, updatedAt := legacyCaseTimes(bugRuns)
		batch.Cases = append(batch.Cases, IncidentCase{
			ID: caseID, BugID: bugID, Source: "legacy-runs-json",
			Status: CaseLegacyArchived, CycleNumber: 1, Version: 1,
			CreatedAt: createdAt, UpdatedAt: updatedAt,
		})
		for _, run := range bugRuns {
			parentAttemptID := ""
			if run.ContinuationOf != "" && runBug[run.ContinuationOf] == run.BugID {
				parentAttemptID = deterministicWorkflowID("legacy-attempt:" + run.ContinuationOf)
			}
			attempt, err := legacyAttempt(caseID, run, parentAttemptID)
			if err != nil {
				return LegacyImportResult{}, err
			}
			batch.Attempts = append(batch.Attempts, attempt)
		}
	}
	return store.importLegacyBatch(ctx, batch)
}

func legacyAttempt(caseID string, run InvestigationRun, parentAttemptID string) (PhaseAttempt, error) {
	input := json.RawMessage(`{}`)
	if run.PromptPreview != "" {
		encoded, err := json.Marshal(struct {
			PromptPreview string `json:"prompt_preview"`
		}{PromptPreview: run.PromptPreview})
		if err != nil {
			return PhaseAttempt{}, fmt.Errorf("encode legacy run %q input: %w", run.ID, err)
		}
		input = encoded
	}
	output := json.RawMessage(`{}`)
	if run.ID != "" {
		encoded, err := json.Marshal(struct {
			OriginalRunID string               `json:"original_run_id"`
			Continuation  string               `json:"continuation_of,omitempty"`
			Events        []InvestigationEvent `json:"events,omitempty"`
			FinalMessage  string               `json:"final_message,omitempty"`
			Error         string               `json:"error,omitempty"`
			LegacyStatus  InvestigationStatus  `json:"legacy_status,omitempty"`
		}{run.ID, run.ContinuationOf, run.Events, run.FinalMessage, run.Error, run.Status})
		if err != nil {
			return PhaseAttempt{}, fmt.Errorf("encode legacy run %q output: %w", run.ID, err)
		}
		output = encoded
	}
	status, err := AttemptStatusFromInvestigationStatus(run.Status)
	if err != nil || status == AttemptStatusQueued || status == AttemptStatusRunning {
		status = AttemptStatusInterrupted
	}
	startedAt := run.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Unix(0, 0).UTC()
	}
	return PhaseAttempt{
		ID: deterministicWorkflowID("legacy-attempt:" + run.ID), CaseID: caseID,
		CycleNumber: 1, Phase: PhaseLegacy, Status: status, BotKey: run.BotKey,
		InputJSON: input, OutputJSON: output, StartedAt: startedAt,
		FinishedAt: cloneTimePtr(run.FinishedAt), ParentAttemptID: parentAttemptID, ErrorMessage: run.Error,
	}, nil
}

func redactLegacyRun(run InvestigationRun) InvestigationRun {
	run.PromptPreview = redactSensitiveText(run.PromptPreview)
	run.FinalMessage = redactSensitiveText(run.FinalMessage)
	run.Error = redactSensitiveText(run.Error)
	for index := range run.Events {
		run.Events[index].Type = redactSensitiveText(run.Events[index].Type)
		run.Events[index].Message = redactSensitiveText(run.Events[index].Message)
		run.Events[index].Raw = redactSensitiveAny(run.Events[index].Raw)
		if run.Events[index].Meta != nil {
			redacted := redactSensitiveAny(map[string]any(run.Events[index].Meta))
			run.Events[index].Meta = redacted.(map[string]any)
		}
	}
	return run
}

func legacyCaseTimes(runs []InvestigationRun) (time.Time, time.Time) {
	createdAt := time.Unix(0, 0).UTC()
	updatedAt := createdAt
	set := false
	for _, run := range runs {
		candidate := run.StartedAt
		if candidate.IsZero() {
			candidate = time.Unix(0, 0).UTC()
		}
		if !set || candidate.Before(createdAt) {
			createdAt = candidate
		}
		if !set || candidate.After(updatedAt) {
			updatedAt = candidate
		}
		if run.FinishedAt != nil && run.FinishedAt.After(updatedAt) {
			updatedAt = *run.FinishedAt
		}
		set = true
	}
	return createdAt, updatedAt
}

func sameLegacyRun(left, right InvestigationRun) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func deterministicWorkflowID(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}
