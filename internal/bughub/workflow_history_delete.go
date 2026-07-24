package bughub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var ErrActiveCaseHistory = errors.New("Bug has an active incident Case")

type CaseHistoryDeleteResult struct {
	BugID          string   `json:"bug_id"`
	CaseIDs        []string `json:"case_ids"`
	CleanupWarning string   `json:"cleanup_warning,omitempty"`
}

// DeleteTerminalCaseHistoryForBug permanently removes every terminal Case for
// one Bug and all SQLite-owned child records. The operation is refused when
// any Case for the Bug is still active, so user-confirmed history cleanup can
// never cancel an Agent or hide an unfinished workflow.
//
// Published evidence directories are first moved to a private tombstone under
// artifactsRoot. A database rollback restores them; after commit the tombstone
// is removed. Cleanup failure after commit is reported as a warning because the
// authoritative history has already been deleted.
func DeleteTerminalCaseHistoryForBug(ctx context.Context, store *CaseStore, artifactsRoot, bugID string) (CaseHistoryDeleteResult, error) {
	result := CaseHistoryDeleteResult{BugID: strings.TrimSpace(bugID), CaseIDs: []string{}}
	if store == nil || store.db == nil {
		return result, errors.New("case store is required")
	}
	if result.BugID == "" {
		return result, errors.New("bug id is required")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return result, fmt.Errorf("begin incident history deletion: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT id,status FROM incident_cases WHERE bug_id=? ORDER BY created_at,id`, result.BugID)
	if err != nil {
		return result, fmt.Errorf("list incident history for Bug: %w", err)
	}
	for rows.Next() {
		var caseID string
		var status CaseStatus
		if err := rows.Scan(&caseID, &status); err != nil {
			rows.Close()
			return result, fmt.Errorf("read incident history for Bug: %w", err)
		}
		if !IsTerminalCaseStatus(status) {
			rows.Close()
			return CaseHistoryDeleteResult{BugID: result.BugID, CaseIDs: []string{}}, fmt.Errorf("%w: Case %s is %s", ErrActiveCaseHistory, caseID, status)
		}
		result.CaseIDs = append(result.CaseIDs, caseID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return result, fmt.Errorf("read incident history for Bug: %w", err)
	}
	if err := rows.Close(); err != nil {
		return result, fmt.Errorf("close incident history rows: %w", err)
	}
	if len(result.CaseIDs) == 0 {
		if err := tx.Commit(); err != nil {
			return result, fmt.Errorf("commit empty incident history deletion: %w", err)
		}
		return result, nil
	}

	staged, err := stageCaseArtifactDirectories(artifactsRoot, result.CaseIDs)
	if err != nil {
		return CaseHistoryDeleteResult{BugID: result.BugID, CaseIDs: []string{}}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = staged.restore()
		}
	}()

	caseSubquery := `SELECT id FROM incident_cases WHERE bug_id=?`
	attemptSubquery := `SELECT id FROM phase_attempts WHERE case_id IN (` + caseSubquery + `)`
	deletes := []struct {
		label string
		query string
	}{
		{"browser recovery operations", `DELETE FROM browser_recovery_operations WHERE case_id IN (` + caseSubquery + `)`},
		{"reset cancellation operations", `DELETE FROM reset_cancellation_operations WHERE case_id IN (` + caseSubquery + `)`},
		{"validation recipes", `DELETE FROM validation_recipes WHERE case_id IN (` + caseSubquery + `)`},
		{"fix checkpoints", `DELETE FROM fix_checkpoints WHERE attempt_id IN (` + attemptSubquery + `)`},
		{"evidence artifacts", `DELETE FROM evidence_artifacts WHERE case_id IN (` + caseSubquery + `)`},
		{"code changes", `DELETE FROM code_changes WHERE case_id IN (` + caseSubquery + `)`},
		{"approvals", `DELETE FROM approvals WHERE case_id IN (` + caseSubquery + `)`},
		{"deployment observations", `DELETE FROM deployment_observations WHERE case_id IN (` + caseSubquery + `)`},
		{"transition events", `DELETE FROM transition_events WHERE case_id IN (` + caseSubquery + `)`},
		{"phase attempts", `DELETE FROM phase_attempts WHERE case_id IN (` + caseSubquery + `)`},
		{"incident cases", `DELETE FROM incident_cases WHERE bug_id=?`},
	}
	for _, deletion := range deletes {
		if _, err := tx.ExecContext(ctx, deletion.query, result.BugID); err != nil {
			return CaseHistoryDeleteResult{BugID: result.BugID, CaseIDs: []string{}}, fmt.Errorf("delete %s: %w", deletion.label, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return CaseHistoryDeleteResult{BugID: result.BugID, CaseIDs: []string{}}, fmt.Errorf("commit incident history deletion: %w", err)
	}
	committed = true
	if err := staged.remove(); err != nil {
		result.CleanupWarning = "故障闭环记录已删除，但部分本地证据文件清理失败"
	}
	return result, nil
}

type stagedCaseArtifacts struct {
	root    string
	staging string
	moved   map[string]string
}

func stageCaseArtifactDirectories(artifactsRoot string, caseIDs []string) (stagedCaseArtifacts, error) {
	staged := stagedCaseArtifacts{moved: map[string]string{}}
	if strings.TrimSpace(artifactsRoot) == "" {
		return staged, nil
	}
	root, err := filepath.Abs(artifactsRoot)
	if err != nil {
		return staged, fmt.Errorf("resolve incident artifact root: %w", err)
	}
	staged.root = root
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return staged, nil
	}
	if err != nil {
		return staged, fmt.Errorf("inspect incident artifact root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return staged, errors.New("incident artifact root must be a real directory")
	}
	staging, err := os.MkdirTemp(root, ".delete-history-")
	if err != nil {
		return staged, fmt.Errorf("create incident history cleanup staging: %w", err)
	}
	staged.staging = staging
	sorted := append([]string(nil), caseIDs...)
	sort.Strings(sorted)
	for _, caseID := range sorted {
		component := artifactStorageCaseComponent(caseID)
		source := filepath.Join(root, component)
		sourceInfo, statErr := os.Lstat(source)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			_ = staged.restore()
			return stagedCaseArtifacts{}, fmt.Errorf("inspect Case artifact directory: %w", statErr)
		}
		if !sourceInfo.IsDir() && sourceInfo.Mode()&os.ModeSymlink == 0 {
			_ = staged.restore()
			return stagedCaseArtifacts{}, errors.New("Case artifact path is not a directory")
		}
		destination := filepath.Join(staging, component)
		if err := os.Rename(source, destination); err != nil {
			_ = staged.restore()
			return stagedCaseArtifacts{}, fmt.Errorf("stage Case artifact directory: %w", err)
		}
		staged.moved[source] = destination
	}
	return staged, nil
}

func (s stagedCaseArtifacts) restore() error {
	var first error
	for source, staged := range s.moved {
		if _, err := os.Lstat(staged); errors.Is(err, os.ErrNotExist) {
			continue
		}
		if _, err := os.Lstat(source); err == nil {
			if first == nil {
				first = errors.New("cannot restore Case artifacts over an existing path")
			}
			continue
		}
		if err := os.Rename(staged, source); err != nil && first == nil {
			first = err
		}
	}
	if s.staging != "" {
		if err := os.Remove(s.staging); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
	}
	return first
}

func (s stagedCaseArtifacts) remove() error {
	if s.staging == "" {
		return nil
	}
	return os.RemoveAll(s.staging)
}
