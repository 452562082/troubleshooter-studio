package bughub

const workflowStoreSchema = `
CREATE TABLE IF NOT EXISTS incident_cases (
  id TEXT PRIMARY KEY,
  bug_id TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT '',
  system_id TEXT NOT NULL DEFAULT '',
  environment TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  cycle_number INTEGER NOT NULL CHECK (cycle_number >= 1),
  current_attempt_id TEXT NOT NULL DEFAULT '',
  selected_bot_key TEXT NOT NULL DEFAULT '',
  version INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  closed_at TEXT
);
CREATE TABLE IF NOT EXISTS phase_attempts (
  id TEXT PRIMARY KEY,
  case_id TEXT NOT NULL REFERENCES incident_cases(id),
  cycle_number INTEGER NOT NULL,
  phase TEXT NOT NULL,
  mode TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL,
  agent_target TEXT NOT NULL DEFAULT '',
  bot_key TEXT NOT NULL DEFAULT '',
  input_json TEXT NOT NULL DEFAULT '{}',
  output_json TEXT NOT NULL DEFAULT '{}',
  parent_attempt_id TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL,
  finished_at TEXT,
  error_code TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  input_tokens INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  duration_nanos INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS transition_events (
  id TEXT PRIMARY KEY,
  case_id TEXT NOT NULL REFERENCES incident_cases(id),
  from_status TEXT NOT NULL,
  to_status TEXT NOT NULL,
  event_type TEXT NOT NULL,
  actor_type TEXT NOT NULL,
  actor_id TEXT NOT NULL DEFAULT '',
  idempotency_key TEXT NOT NULL UNIQUE,
  payload_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS evidence_artifacts (
  id TEXT PRIMARY KEY, case_id TEXT NOT NULL REFERENCES incident_cases(id),
  attempt_id TEXT NOT NULL REFERENCES phase_attempts(id), kind TEXT NOT NULL,
  path_or_reference TEXT NOT NULL, sha256 TEXT NOT NULL, captured_at TEXT NOT NULL,
  environment TEXT NOT NULL DEFAULT '', version TEXT NOT NULL DEFAULT '',
  request_id TEXT NOT NULL DEFAULT '', trace_id TEXT NOT NULL DEFAULT '',
  redaction_status TEXT NOT NULL,
  UNIQUE(attempt_id, sha256, kind)
);
CREATE TABLE IF NOT EXISTS code_changes (
  id TEXT PRIMARY KEY, case_id TEXT NOT NULL REFERENCES incident_cases(id),
  attempt_id TEXT NOT NULL REFERENCES phase_attempts(id), repo TEXT NOT NULL,
  base_branch TEXT NOT NULL, fix_branch TEXT NOT NULL, fix_commit TEXT NOT NULL,
  test_evidence_json TEXT NOT NULL DEFAULT '[]', target_environment_branch TEXT NOT NULL,
  merge_base_head TEXT NOT NULL DEFAULT '', merge_commit TEXT NOT NULL DEFAULT '',
  push_remote TEXT NOT NULL DEFAULT '', push_status TEXT NOT NULL DEFAULT '',
  UNIQUE(case_id, repo, fix_commit, target_environment_branch)
);
CREATE TABLE IF NOT EXISTS approvals (
  id TEXT PRIMARY KEY, case_id TEXT NOT NULL REFERENCES incident_cases(id),
  kind TEXT NOT NULL, actor TEXT NOT NULL, approved_at TEXT NOT NULL,
  case_version INTEGER NOT NULL, scope_json TEXT NOT NULL,
  fix_commits_json TEXT NOT NULL, target_branches_json TEXT NOT NULL,
  idempotency_key TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS deployment_observations (
  id TEXT PRIMARY KEY, case_id TEXT NOT NULL REFERENCES incident_cases(id),
  environment TEXT NOT NULL, expected_commits_json TEXT NOT NULL,
  user_notified_at TEXT, verification_source TEXT NOT NULL,
  observed_version TEXT NOT NULL DEFAULT '', observed_images_json TEXT NOT NULL DEFAULT '{}',
  observed_commits_json TEXT NOT NULL DEFAULT '{}', verified_at TEXT,
  result TEXT NOT NULL, idempotency_key TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS schema_migrations (
  key TEXT PRIMARY KEY, applied_at TEXT NOT NULL, detail_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_cases_status_updated ON incident_cases(status, updated_at);
CREATE INDEX IF NOT EXISTS idx_attempts_case_started ON phase_attempts(case_id, started_at);
CREATE INDEX IF NOT EXISTS idx_events_case_created ON transition_events(case_id, created_at);
`
