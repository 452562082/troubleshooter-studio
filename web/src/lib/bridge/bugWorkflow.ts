import * as App from '../../../wailsjs/go/main/App'
import { isDesktop } from './shared'

const desktopApp = App as Record<string, (...args: any[]) => Promise<any>>
const desktopOnly = 'Incident Case 工作流只在桌面 app 可用'

export type CaseStatus = 'pending_validation' | 'validating' | 'waiting_evidence' | 'reproduced' |
  'not_reproduced' | 'investigating' | 'root_cause_ready' | 'waiting_fix_approval' |
  'fixing' | 'fix_failed' | 'fix_pushed' | 'waiting_merge_approval' | 'merging' |
  'merge_conflict' | 'waiting_deployment' | 'deployment_unverified' |
  'deployment_verified' | 'regression_validating' | 'fixed_verified' |
  'still_reproduces' | 'legacy_archived'

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
export interface DeploymentObservation { id: string; case_id: string; environment: string; expected_commits: Record<string, string>; observed_version: string; observed_images: Record<string, string>; observed_commits: Record<string, string>; verification_source: string; result: string }
export interface TransitionEvent { id: string; case_id: string; from_status: CaseStatus; to_status: CaseStatus; event_type: string; actor_type: string; actor_id: string; idempotency_key: string; payload_json: Record<string, unknown>; created_at: string }

export interface IncidentCaseDetail {
  case: IncidentCase
  attempts: PhaseAttempt[]
  artifacts: EvidenceArtifact[]
  approvals: Approval[]
  code_changes: CodeChange[]
  deployment_observations: DeploymentObservation[]
  events: TransitionEvent[]
}

interface WorkflowCommandInput { case_id: string; expected_version: number; idempotency_key: string; actor_id: string }
export interface StartIncidentCaseInput extends WorkflowCommandInput { input_json?: Record<string, unknown> }
export interface ContinueIncidentCaseInput extends WorkflowCommandInput { phase: Phase; input_json?: Record<string, unknown> }
export interface ApproveIncidentFixInput extends WorkflowCommandInput { root_cause_attempt_id: string; input_json?: Record<string, unknown> }
export interface ApproveIncidentMergeInput extends WorkflowCommandInput { fix_commits: Record<string, string>; target_branches: Record<string, string> }
export interface NotifyIncidentDeployedInput extends WorkflowCommandInput { observed_version: string; observed_commits?: Record<string, string>; input_json?: Record<string, unknown> }
export interface CancelIncidentAttemptInput extends WorkflowCommandInput { attempt_id: string }

export async function listIncidentCases(): Promise<IncidentCase[]> {
  if (!isDesktop()) return []
  const result = await desktopApp.ListIncidentCases()
  return Array.isArray(result) ? result.map(normalizeCase) : []
}

export async function getIncidentCase(caseID: string): Promise<IncidentCaseDetail> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeDetail(await desktopApp.GetIncidentCase(caseID))
}

export async function startIncidentCase(input: StartIncidentCaseInput): Promise<IncidentCase> { return mutate('StartIncidentCase', input) }
export async function continueIncidentCase(input: ContinueIncidentCaseInput): Promise<IncidentCase> { return mutate('ContinueIncidentCase', input) }
export async function approveIncidentFix(input: ApproveIncidentFixInput): Promise<IncidentCase> { return mutate('ApproveIncidentFix', input) }
export async function approveIncidentMerge(input: ApproveIncidentMergeInput): Promise<IncidentCase> { return mutate('ApproveIncidentMerge', input) }
export async function notifyIncidentDeployed(input: NotifyIncidentDeployedInput): Promise<IncidentCase> { return mutate('NotifyIncidentDeployed', input) }
export async function cancelIncidentAttempt(input: CancelIncidentAttemptInput): Promise<IncidentCase> { return mutate('CancelIncidentAttempt', input) }

async function mutate(method: string, input: unknown): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await desktopApp[method](input))
}

function normalizeCase(raw: any): IncidentCase {
  return {
    ...raw,
    id: String(raw?.id ?? ''),
    bug_id: String(raw?.bug_id ?? ''),
    source: String(raw?.source ?? ''),
    system_id: String(raw?.system_id ?? ''),
    environment: String(raw?.environment ?? ''),
    current_attempt_id: String(raw?.current_attempt_id ?? ''),
    selected_bot_key: String(raw?.selected_bot_key ?? ''),
    version: raw?.version,
  } as IncidentCase
}

function normalizeDetail(raw: any): IncidentCaseDetail {
  return {
    case: normalizeCase(raw?.case),
    attempts: Array.isArray(raw?.attempts) ? raw.attempts : [],
    artifacts: Array.isArray(raw?.artifacts) ? raw.artifacts : [],
    approvals: Array.isArray(raw?.approvals) ? raw.approvals : [],
    code_changes: Array.isArray(raw?.code_changes) ? raw.code_changes : [],
    deployment_observations: Array.isArray(raw?.deployment_observations) ? raw.deployment_observations : [],
    events: Array.isArray(raw?.events) ? raw.events : [],
  }
}
