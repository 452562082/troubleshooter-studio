import * as App from '../../../wailsjs/go/main/App'
import { isDesktop } from './shared'

const desktopOnly = 'Incident Case 工作流只在桌面 app 可用'

export type CaseStatus = 'pending_validation' | 'validating' | 'waiting_evidence' | 'reproduced' |
  'not_reproduced' | 'investigating' | 'root_cause_ready' | 'waiting_fix_approval' |
  'fixing' | 'fix_failed' | 'fix_pushed' | 'waiting_merge_approval' | 'merging' |
  'merge_conflict' | 'waiting_deployment' | 'deployment_unverified' |
  'deployment_verified' | 'regression_validating' | 'fixed_verified' |
  'still_reproduces' | 'legacy_archived' | 'reset_archived'

export type Phase = 'validation' | 'investigation' | 'fix' | 'regression' | 'legacy'
export type AttemptMode = 'reproduce' | 'regression' | ''
export type AttemptStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'interrupted'

export interface IncidentCase {
  id: string
  bug_id: string
  source: string
  system_id: string
  environment: string
  status: CaseStatus
  cycle_number: number
  current_attempt_id: string
  selected_bot_key: string
  reset_from_case_id?: string
  superseded_by_case_id?: string
  version: number
  created_at: string
  updated_at: string
  closed_at?: string | null
}

export interface PhaseAttempt {
  id: string
  case_id: string
  cycle_number: number
  phase: Phase
  mode: AttemptMode
  status: AttemptStatus
  agent_target: string
  bot_key: string
  input_json: Record<string, unknown>
  output_json: Record<string, unknown>
  parent_attempt_id: string
  started_at: string
  finished_at?: string | null
  error_code: string
  error_message: string
  usage: { input_tokens?: number; output_tokens?: number; duration?: number }
}

export interface EvidenceArtifact { id: string; case_id: string; attempt_id: string; kind: string; path_or_reference: string; sha256: string; captured_at: string; environment: string; version: string; request_id: string; trace_id: string; redaction_status: string }
export interface Approval { id: string; case_id: string; kind: string; actor: string; approved_at: string; case_version: number; scope_json: Record<string, unknown>; fix_commits: Record<string, string>; target_branches: Record<string, string> }
export interface CodeChange { id: string; case_id: string; attempt_id: string; repo: string; base_branch: string; fix_branch: string; fix_commit: string; test_evidence: unknown; target_environment_branch: string; merge_base_head: string; merge_commit: string; push_remote: string; push_status: string }
export interface DeploymentObservation { id: string; case_id: string; environment: string; expected_commits: Record<string, string>; observed_version: string; observed_images: Record<string, string>; observed_commits: Record<string, string>; verified_commit_ancestors?: Record<string, string>; observed_at?: string; diagnostic_code?: string; diagnostic_message?: string; verification_source: string; result: string }
export interface TransitionEvent { id: string; case_id: string; from_status: CaseStatus; to_status: CaseStatus; event_type: string; actor_type: string; actor_id: string; idempotency_key: string; payload_json: Record<string, unknown>; created_at: string }
export interface WorkflowMetrics {
  completed_cases: number
  open_cases: number
  median_stage_duration: Record<string, number>
  oldest_waiting_deployment_age: number
  agent_execution_duration: number
  human_deployment_wait: number
  retry_count: number
  agent_input_tokens: number
  agent_output_tokens: number
  blocker_distribution: Record<string, number>
  automation_ratio: number
  first_regression_success_rate: number
  still_reproduces_rate: number
}
export interface WorkflowReminder { case_id: string; bug_id: string; environment: string; waiting_since: string; waiting_age: number; sequence: number; reservation_key: string; delivery_attempt: number }

export interface IncidentCaseDetail {
  case: IncidentCase
  attempts: PhaseAttempt[]
  artifacts: EvidenceArtifact[]
  approvals: Approval[]
  code_changes: CodeChange[]
  deployment_observations: DeploymentObservation[]
  events: TransitionEvent[]
  deployment_verification?: { provider: 'manual' | 'http' | 'k8s' | 'unavailable'; available: boolean; hint: string }
}

export interface IncidentPhaseEvent {
  at?: string
  type?: string
  message?: string
  raw?: unknown
  meta: Record<string, unknown>
}

export type IncidentCaseEventPayload = {
  kind: 'snapshot'
  case: IncidentCase
  snapshot: IncidentCaseDetail
  phase_event?: IncidentPhaseEvent
} | {
  kind: 'startup_error'
  error: { message: string; retryable: boolean }
}

interface WorkflowCommandInput { case_id: string; expected_version: number; idempotency_key: string; actor_id: string }
export interface StartIncidentCaseInput extends WorkflowCommandInput { bug_id?: string; bot_key?: string; input_json?: Record<string, unknown> }
export interface ResetIncidentCaseInput extends WorkflowCommandInput { new_case_id: string; bot_key: string; input_json?: Record<string, unknown> }
export interface ContinueIncidentCaseInput extends WorkflowCommandInput { phase: Phase; input_json?: Record<string, unknown> }
export interface ApproveIncidentFixInput extends WorkflowCommandInput { root_cause_attempt_id: string; input_json?: Record<string, unknown> }
export interface ApproveIncidentMergeInput extends WorkflowCommandInput { fix_commits: Record<string, string>; target_branches: Record<string, string>; target_heads?: Record<string, string> }
export interface NotifyIncidentDeployedInput extends WorkflowCommandInput { observed_version: string; observed_commits?: Record<string, string>; version_source?: string; notification_text?: string; input_json?: Record<string, unknown> }
export interface CancelIncidentAttemptInput extends WorkflowCommandInput { attempt_id: string }

export async function listIncidentCases(): Promise<IncidentCase[]> {
  if (!isDesktop()) return []
  const result = await App.ListIncidentCases()
  return Array.isArray(result) ? result.map(normalizeCase) : []
}

export async function getIncidentWorkflowMetrics(): Promise<WorkflowMetrics> {
  if (!isDesktop()) return emptyWorkflowMetrics()
  return { ...emptyWorkflowMetrics(), ...(await App.GetIncidentWorkflowMetrics()) } as WorkflowMetrics
}

export async function listPendingIncidentWorkflowReminders(): Promise<WorkflowReminder[]> {
  if (!isDesktop()) return []
  const result = await App.ListPendingIncidentWorkflowReminders()
  return Array.isArray(result) ? result as WorkflowReminder[] : []
}

export async function ackIncidentWorkflowReminder(input: { case_id: string; reservation_key: string; delivery_attempt: number; actor_id: string }): Promise<void> {
  if (!isDesktop()) throw new Error(desktopOnly)
  await App.AckIncidentWorkflowReminder(input)
}

export async function getIncidentCase(caseID: string): Promise<IncidentCaseDetail> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeDetail(await App.GetIncidentCase(caseID))
}

export async function startIncidentCase(input: StartIncidentCaseInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.StartIncidentCase(input))
}
export async function resetIncidentCase(input: ResetIncidentCaseInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.ResetIncidentCase(input))
}
export async function continueIncidentCase(input: ContinueIncidentCaseInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.ContinueIncidentCase(input))
}
export async function approveIncidentFix(input: ApproveIncidentFixInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.ApproveIncidentFix(input))
}
export async function approveIncidentMerge(input: ApproveIncidentMergeInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.ApproveIncidentMerge({ ...input, target_heads: input.target_heads || {} }))
}
export async function notifyIncidentDeployed(input: NotifyIncidentDeployedInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.NotifyIncidentDeployed(input))
}
export async function cancelIncidentAttempt(input: CancelIncidentAttemptInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.CancelIncidentAttempt(input))
}

function record(raw: unknown): Record<string, unknown> {
  return raw !== null && typeof raw === 'object' ? raw as Record<string, unknown> : {}
}

function emptyWorkflowMetrics(): WorkflowMetrics {
  return { completed_cases: 0, open_cases: 0, median_stage_duration: {}, oldest_waiting_deployment_age: 0, agent_execution_duration: 0, human_deployment_wait: 0, retry_count: 0, agent_input_tokens: 0, agent_output_tokens: 0, blocker_distribution: {}, automation_ratio: 0, first_regression_success_rate: 0, still_reproduces_rate: 0 }
}

function normalizeCase(raw: unknown): IncidentCase {
  const source = record(raw)
  return {
    ...source,
    id: String(source.id ?? ''),
    bug_id: String(source.bug_id ?? ''),
    source: String(source.source ?? ''),
    system_id: String(source.system_id ?? ''),
    environment: String(source.environment ?? ''),
    current_attempt_id: String(source.current_attempt_id ?? ''),
    selected_bot_key: String(source.selected_bot_key ?? ''),
    reset_from_case_id: String(source.reset_from_case_id ?? ''),
    superseded_by_case_id: String(source.superseded_by_case_id ?? ''),
    version: source.version,
  } as IncidentCase
}

function normalizeDetail(raw: unknown): IncidentCaseDetail {
  const source = record(raw)
  return {
    case: normalizeCase(source.case),
    attempts: Array.isArray(source.attempts) ? source.attempts as PhaseAttempt[] : [],
    artifacts: Array.isArray(source.artifacts) ? source.artifacts as EvidenceArtifact[] : [],
    approvals: Array.isArray(source.approvals) ? source.approvals as Approval[] : [],
    code_changes: Array.isArray(source.code_changes) ? source.code_changes as CodeChange[] : [],
    deployment_observations: Array.isArray(source.deployment_observations) ? source.deployment_observations as DeploymentObservation[] : [],
    events: Array.isArray(source.events) ? source.events as TransitionEvent[] : [],
  }
}

export function normalizeIncidentCaseEvent(raw: unknown): IncidentCaseEventPayload {
  const source = record(raw)
  const error = record(source.error)
  if (source.kind === 'startup_error') {
    return {
      kind: 'startup_error',
      error: {
        message: String(error.message ?? 'Incident workflow startup failed'),
        retryable: error.retryable === true,
      },
    }
  }
  const phase = record(source.phase_event)
  return {
    kind: 'snapshot',
    case: normalizeCase(source.case),
    snapshot: normalizeDetail(source.snapshot),
    ...(source.phase_event ? { phase_event: { ...phase, meta: record(phase.meta) } } : {}),
  }
}
