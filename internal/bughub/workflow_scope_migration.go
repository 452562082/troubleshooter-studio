package bughub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// retireVerificationCases preserves all historical evidence and cancels only
// removed executable phases. Existing investigation, fix and merge work remains recoverable.
func (s *CaseStore) retireVerificationCases(ctx context.Context) error {
	const key = "workflow-investigation-fix-submission-v1"
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var marker string
	err = tx.QueryRowContext(ctx, `SELECT key FROM schema_migrations WHERE key=?`, key).Scan(&marker)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,status,version FROM incident_cases WHERE status IN ('pending_validation','validating','reproduced','not_reproduced','waiting_deployment','deployment_verified','deployment_unverified','regression_validating','still_reproduces','remediation_applied') OR (status='waiting_evidence' AND current_attempt_id IN (SELECT id FROM phase_attempts WHERE phase IN ('validation','regression')))`)
	if err != nil {
		return err
	}
	type retired struct {
		id      string
		status  CaseStatus
		version int64
	}
	var cases []retired
	for rows.Next() {
		var c retired
		if err = rows.Scan(&c.id, &c.status, &c.version); err != nil {
			_ = rows.Close()
			return err
		}
		cases = append(cases, c)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	now := formatStoreTime(time.Now().UTC())
	for _, c := range cases {
		// Archive rather than invent a submitted or verified outcome for old work.
		_, err = tx.ExecContext(ctx, `UPDATE incident_cases SET status=?,closed_at=?,updated_at=?,version=version+1 WHERE id=? AND version=?`, CaseLegacyArchived, now, now, c.id, c.version)
		if err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"reason": "automated_verification_removed", "previous_status": string(c.status)})
		_, err = tx.ExecContext(ctx, `INSERT INTO transition_events(id,case_id,from_status,to_status,event_type,actor_type,actor_id,idempotency_key,payload_json,created_at,request_fingerprint,result_case_json) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, stableID("event", key+":"+c.id), c.id, c.status, CaseLegacyArchived, "workflow_scope_changed", "studio", "migration", key+":"+c.id, string(payload), now, "", "{}")
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE phase_attempts SET status='cancelled',finished_at=?,run_claim_token='',error_code='phase_retired',error_message='自动复现与验证已移除；历史证据保留，可重新开启排障。' WHERE phase IN ('validation','regression') AND status IN ('queued','running')`, now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(key,applied_at,detail_json) VALUES(?,?,?)`, key, now, `{"scope":"investigation_fix_submission"}`)
	if err != nil {
		return err
	}
	return tx.Commit()
}
