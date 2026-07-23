import * as App from '../../../wailsjs/go/main/App'
import { main as WailsMain } from '../../../wailsjs/go/models'
import { isDesktop } from './shared'

const desktopOnly = 'Incident Case 工作流只在桌面 app 可用'

export type CaseStatus = 'pending_validation' | 'validating' | 'waiting_evidence' | 'reproduced' |
  'not_reproduced' | 'investigating' | 'root_cause_ready' | 'waiting_fix_approval' |
  'waiting_remediation' | 'remediation_applied' |
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
  frontend_entry?: FrontendEntryBinding
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

export interface FrontendEntryBinding {
  id: string; name: string; url: string; config_url?: string; repo?: string; device_profile?: string
  resolution_source: string; score?: number; reason?: string; config_sha256?: string
}
export interface FrontendEntryCandidate { binding: FrontendEntryBinding; score: number; reasons: string[] }
export interface FrontendEntryResolution {
  status: 'selected' | 'ambiguous' | 'unavailable'
  selected?: FrontendEntryBinding
  candidates?: FrontendEntryCandidate[]
  message?: string
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

export interface IncidentArtifact { id: string; case_id: string; attempt_id: string; kind: string; sha256: string; size: number; captured_at: string; environment: string; version: string; request_id: string; trace_id: string }
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
export type IncidentBrowserRuntimeState = 'ready' | 'installing' | 'broken'
export interface IncidentBrowserRuntimeStatus {
  state: IncidentBrowserRuntimeState
  version: string
  error_code: string
  message: string
}

export interface IncidentCaseDetail {
  case: IncidentCase
  attempts: PhaseAttempt[]
  phase_events?: IncidentPhaseEvent[]
  artifacts: IncidentArtifact[]
  approvals: Approval[]
  code_changes: CodeChange[]
  deployment_observations: DeploymentObservation[]
  events: TransitionEvent[]
  deployment_verification?: { provider: 'manual' | 'http' | 'k8s' | 'unavailable'; available: boolean; hint: string }
  bug_ticket_resolution?: { state: 'not_ready' | 'pending' | 'resolved' | 'unknown'; source_status?: string }
}

export interface IncidentPhaseEvent {
  at?: string
  type?: string
  message?: string
  raw?: unknown
  meta: Record<string, unknown>
}

export const incidentBrowserProgressCodes = [
  'browser_launching',
  'browser_context_preparing',
  'browser_evidence_preparing',
  'browser_starting',
  'browser_action_started',
  'browser_action_completed',
  'browser_plan_generating',
  'browser_repair_generating',
  'browser_result_evaluating',
  'browser_login_opened',
  'browser_login_completed',
  'browser_runtime_installing',
  'browser_runtime_importing',
  'browser_runtime_dependencies_installing',
  'browser_runtime_downloading',
  'browser_runtime_probing',
  'browser_runtime_ready',
  'action_started',
  'action_completed',
  'runtime_preparing',
] as const
export type IncidentBrowserProgressCode = typeof incidentBrowserProgressCodes[number]

export type IncidentCaseEventPayload = {
  kind: 'snapshot'
  case: IncidentCase
  snapshot: IncidentCaseDetail
  phase_event?: IncidentPhaseEvent
} | {
  kind: 'startup_error'
  error: { message: string; retryable: boolean }
}

export interface WorkflowCommandInput { case_id: string; expected_version: number; idempotency_key: string; actor_id: string }
export interface IncidentBrowserCommandInput extends WorkflowCommandInput { attempt_id: string }
export interface IncidentArtifactPreview { artifact_id: string; mime_type: 'image/png'; base64_data: string; size: number }
export interface StartIncidentCaseInput extends WorkflowCommandInput { bug_id?: string; bot_key?: string; bot_environment?: string; frontend_entry_id?: string; input_json?: Record<string, unknown> }
export interface ResetIncidentCaseInput extends WorkflowCommandInput { new_case_id: string; bot_key: string; bot_environment?: string; frontend_entry_id?: string; input_json?: Record<string, unknown> }
export interface WorkflowWarning { code: string; message: string }
export interface ResetIncidentCaseResult { case: IncidentCase; warnings: WorkflowWarning[] }
export type IncidentWorkflowConflictCode = 'case_version_conflict' | 'idempotency_conflict'

export class IncidentWorkflowCommandError extends Error {
  constructor(public readonly code: IncidentWorkflowConflictCode, message: string) {
    super(message)
    this.name = 'IncidentWorkflowCommandError'
  }
}

function incidentWorkflowConflictCode(error: unknown): IncidentWorkflowConflictCode | '' {
  if (error instanceof IncidentWorkflowCommandError) return error.code
  const message = (error instanceof Error ? error.message : String(error)).trim().toLocaleLowerCase()
  const sentinel = /^workflow_conflict:(case_version_conflict|idempotency_conflict)(?::|$)/.exec(message)
  if (sentinel) return sentinel[1] as IncidentWorkflowConflictCode
  if (/^incident case version conflict(?::\s*expected\s+\d+,\s*current\s+\d+)?$/.test(message)) return 'case_version_conflict'
  if (/^idempotency key conflicts with committed request(?::[^\r\n]*)?$/.test(message) || message === '幂等键与已提交请求冲突') return 'idempotency_conflict'
  return ''
}

export function isIncidentWorkflowConflict(error: unknown): boolean {
  return incidentWorkflowConflictCode(error) !== ''
}
export interface ContinueIncidentCaseInput extends WorkflowCommandInput { phase: Phase; input_json?: Record<string, unknown> }
export interface IncidentEvidenceImageInput { name: string; mime_type: 'image/png' | 'image/jpeg'; base64_data: string }
export interface UploadIncidentEvidenceImagesInput { case_id: string; attempt_id: string; expected_version: number; images: IncidentEvidenceImageInput[] }
export interface IncidentEvidenceImage { artifact_id: string; name: string; mime_type: 'image/png'; size: number }
export interface IncidentEvidenceFileInput { name: string; mime_type: string; base64_data: string }
export interface UploadIncidentEvidenceFilesInput { case_id: string; attempt_id: string; expected_version: number; files: IncidentEvidenceFileInput[] }
export interface IncidentEvidenceFile { artifact_id: string; name: string; mime_type: string; size: number }
export interface ApproveIncidentFixInput extends WorkflowCommandInput { root_cause_attempt_id: string; input_json?: Record<string, unknown> }
export interface ReconsiderIncidentRemediationInput extends WorkflowCommandInput { root_cause_attempt_id: string; proposal: string }
export interface DisputeIncidentRootCauseInput extends WorkflowCommandInput { root_cause_attempt_id: string; reason: string; evidence_artifact_ids?: string[] }
export interface CompleteIncidentRemediationInput extends WorkflowCommandInput { root_cause_attempt_id: string; summary: string; evidence: string }
export interface ApproveIncidentMergeInput extends WorkflowCommandInput { fix_commits: Record<string, string>; target_branches: Record<string, string>; target_heads?: Record<string, string> }
export interface NotifyIncidentDeployedInput extends WorkflowCommandInput { observed_version?: string; observed_commits?: Record<string, string>; version_source?: string; notification_text?: string; input_json?: Record<string, unknown> }
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

export async function getIncidentBrowserRuntimeStatus(): Promise<IncidentBrowserRuntimeStatus> {
  if (!isDesktop()) return { state: 'ready', version: 'preview', error_code: '', message: '' }
  return normalizeIncidentBrowserRuntimeStatus(await App.GetIncidentBrowserRuntimeStatus())
}

export async function prepareIncidentBrowserRuntime(): Promise<void> {
  if (!isDesktop()) throw new Error(desktopOnly)
  await App.PrepareIncidentBrowserRuntime()
}

export async function ackIncidentWorkflowReminder(input: { case_id: string; reservation_key: string; delivery_attempt: number; actor_id: string }): Promise<void> {
  if (!isDesktop()) throw new Error(desktopOnly)
  await App.AckIncidentWorkflowReminder(input)
}

export async function getIncidentCase(caseID: string): Promise<IncidentCaseDetail> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeDetail(await App.GetIncidentCase(caseID))
}
export async function listIncidentFixBranches(caseID: string, rootCauseAttemptID: string): Promise<Record<string, string[]>> {
  if (!isDesktop()) return {}
  const raw = record(await App.ListIncidentFixBranches(caseID, rootCauseAttemptID))
  const result: Record<string, string[]> = {}
  for (const [repo, branches] of Object.entries(raw)) {
    if (!repo.trim() || !Array.isArray(branches)) continue
    result[repo] = [...new Set(branches.map(branch => typeof branch === 'string' ? branch.trim() : '').filter(Boolean))]
  }
  return result
}

export async function startIncidentCase(input: StartIncidentCaseInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.StartIncidentCase(input))
}
export async function resolveIncidentFrontendEntry(input: { bug_id: string; bot_key: string; bot_environment?: string; frontend_entry_id?: string }): Promise<FrontendEntryResolution> {
  if (!isDesktop()) return { status: 'unavailable', message: desktopOnly }
  return await App.ResolveIncidentFrontendEntry(input) as FrontendEntryResolution
}
export async function resetIncidentCase(input: ResetIncidentCaseInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.ResetIncidentCase(input))
}
export async function resetIncidentCaseWithWarnings(input: ResetIncidentCaseInput): Promise<ResetIncidentCaseResult> {
  if (!isDesktop()) throw new Error(desktopOnly)
  let raw: unknown
  try {
    raw = await App.ResetIncidentCaseWithWarnings(input)
  } catch (error) {
    const code = incidentWorkflowConflictCode(error)
    if (code) throw new IncidentWorkflowCommandError(code, error instanceof Error ? error.message : String(error))
    throw error
  }
  const result = record(raw)
  const warnings = Array.isArray(result.warnings)
    ? result.warnings.map(item => {
      const warning = record(item)
      return { code: String(warning.code ?? ''), message: String(warning.message ?? '') }
    })
    : []
  return { case: normalizeCase(result.case), warnings }
}
export async function continueIncidentCase(input: ContinueIncidentCaseInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.ContinueIncidentCase(input))
}
export async function uploadIncidentEvidenceImages(input: UploadIncidentEvidenceImagesInput): Promise<IncidentEvidenceImage[]> {
  if (!isDesktop()) throw new Error(desktopOnly)
  const raw = await App.UploadIncidentEvidenceImages(new WailsMain.UploadIncidentEvidenceImagesInput(input))
  if (!Array.isArray(raw)) throw new Error('补充证据上传返回了无效结果')
  return raw.map(item => {
    const value = record(item)
    const size = value.size
    if (typeof value.artifact_id !== 'string' || !value.artifact_id || typeof value.name !== 'string' || value.mime_type !== 'image/png' || typeof size !== 'number' || !Number.isSafeInteger(size) || size <= 0) {
      throw new Error('补充证据上传返回了无效图片')
    }
    return { artifact_id: value.artifact_id, name: value.name, mime_type: 'image/png', size }
  })
}
export async function uploadIncidentEvidenceFiles(input: UploadIncidentEvidenceFilesInput): Promise<IncidentEvidenceFile[]> {
  if (!isDesktop()) throw new Error(desktopOnly)
  const raw = await App.UploadIncidentEvidenceFiles(new WailsMain.UploadIncidentEvidenceFilesInput(input))
  if (!Array.isArray(raw)) throw new Error('测试文件上传返回了无效结果')
  return raw.map(item => {
    const value = record(item)
    const size = value.size
    if (typeof value.artifact_id !== 'string' || !value.artifact_id || typeof value.name !== 'string' || !value.name || typeof value.mime_type !== 'string' || typeof size !== 'number' || !Number.isSafeInteger(size) || size <= 0) {
      throw new Error('测试文件上传返回了无效文件')
    }
    return { artifact_id: value.artifact_id, name: value.name, mime_type: value.mime_type, size }
  })
}
export async function approveIncidentFix(input: ApproveIncidentFixInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.ApproveIncidentFix(input))
}
export async function reconsiderIncidentRemediation(input: ReconsiderIncidentRemediationInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.ReconsiderIncidentRemediation(input))
}
export async function disputeIncidentRootCause(input: DisputeIncidentRootCauseInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.DisputeIncidentRootCause(input))
}
export async function completeIncidentRemediation(input: CompleteIncidentRemediationInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.CompleteIncidentRemediation(input))
}
export async function approveIncidentMerge(input: ApproveIncidentMergeInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.ApproveIncidentMerge({ ...input, target_heads: input.target_heads || {} }))
}
export async function notifyIncidentDeployed(input: NotifyIncidentDeployedInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.NotifyIncidentDeployed({ ...input, observed_version: input.observed_version || '', observed_commits: input.observed_commits || {} }))
}
export async function cancelIncidentAttempt(input: CancelIncidentAttemptInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.CancelIncidentAttempt(input))
}
export async function openIncidentBrowserLogin(input: IncidentBrowserCommandInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.OpenIncidentBrowserLogin(input))
}
export async function repairIncidentBrowserRuntime(input: IncidentBrowserCommandInput): Promise<IncidentCase> {
  if (!isDesktop()) throw new Error(desktopOnly)
  return normalizeCase(await App.RepairIncidentBrowserRuntime(input))
}
export async function clearIncidentBrowserSession(input: IncidentBrowserCommandInput): Promise<void> {
  if (!isDesktop()) throw new Error(desktopOnly)
  await App.ClearIncidentBrowserSession(input)
}
export async function getIncidentArtifactPreview(caseID: string, artifactID: string): Promise<IncidentArtifactPreview> {
  if (!isDesktop()) throw new Error(desktopOnly)
  const raw = record(await App.GetIncidentArtifactPreview(caseID, artifactID))
  const base64Data = typeof raw.base64_data === 'string' ? raw.base64_data : ''
  const size = raw.size
  const maxPreviewBytes = 16 * 1024 * 1024
  const canonicalBase64 = /^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/
  if (raw.artifact_id !== artifactID || raw.mime_type !== 'image/png' || typeof size !== 'number' || !Number.isSafeInteger(size) || size <= 0 || size > maxPreviewBytes || base64Data.length !== Math.ceil(size / 3) * 4 || !canonicalBase64.test(base64Data)) {
    throw new Error('故障证据预览不是有效的 PNG 图片')
  }
  let decoded = ''
  try { decoded = atob(base64Data) } catch { throw new Error('故障证据预览不是有效的 PNG 图片') }
  const signature = [0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]
  if (decoded.length !== size || btoa(decoded) !== base64Data || signature.some((byte, index) => decoded.charCodeAt(index) !== byte)) {
    throw new Error('故障证据预览不是有效的 PNG 图片')
  }
  return { artifact_id: artifactID, mime_type: 'image/png', base64_data: base64Data, size }
}
export async function saveIncidentArtifact(caseID: string, artifactID: string): Promise<boolean> {
  if (!isDesktop()) throw new Error(desktopOnly)
  const saved = await App.SaveIncidentArtifact(caseID, artifactID)
  if (typeof saved !== 'boolean') throw new Error('保存故障证据返回了无效结果')
  return saved
}

function record(raw: unknown): Record<string, unknown> {
  return raw !== null && typeof raw === 'object' ? raw as Record<string, unknown> : {}
}

export function normalizeIncidentBrowserRuntimeStatus(raw: unknown): IncidentBrowserRuntimeStatus {
  const source = record(raw)
  const state = source.state === 'ready' || source.state === 'installing' || source.state === 'broken'
    ? source.state
    : 'broken'
  return {
    state,
    version: typeof source.version === 'string' ? source.version : '',
    error_code: typeof source.error_code === 'string' ? source.error_code : '',
    message: typeof source.message === 'string' ? source.message : '',
  }
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

function normalizeArtifact(raw: unknown): IncidentArtifact {
  const source = record(raw)
  return {
    id: String(source.id ?? ''),
    case_id: String(source.case_id ?? ''),
    attempt_id: String(source.attempt_id ?? ''),
    kind: String(source.kind ?? ''),
    sha256: String(source.sha256 ?? ''),
    size: typeof source.size === 'number' ? source.size : 0,
    captured_at: String(source.captured_at ?? ''),
    environment: String(source.environment ?? ''),
    version: String(source.version ?? ''),
    request_id: String(source.request_id ?? ''),
    trace_id: String(source.trace_id ?? ''),
  }
}

function normalizeDetail(raw: unknown): IncidentCaseDetail {
  const source = record(raw)
  const deploymentVerification = record(source.deployment_verification)
  return {
    case: normalizeCase(source.case),
    attempts: Array.isArray(source.attempts) ? source.attempts as PhaseAttempt[] : [],
    phase_events: Array.isArray(source.phase_events) ? source.phase_events.map(item => {
      const event = record(item)
      return {
        ...(typeof event.at === 'string' ? { at: event.at } : {}),
        ...(typeof event.type === 'string' ? { type: event.type } : {}),
        ...(typeof event.message === 'string' ? { message: event.message } : {}),
        meta: record(event.meta),
      }
    }) : [],
    artifacts: Array.isArray(source.artifacts) ? source.artifacts.map(normalizeArtifact) : [],
    approvals: Array.isArray(source.approvals) ? source.approvals as Approval[] : [],
    code_changes: Array.isArray(source.code_changes) ? source.code_changes as CodeChange[] : [],
    deployment_observations: Array.isArray(source.deployment_observations) ? source.deployment_observations as DeploymentObservation[] : [],
    events: Array.isArray(source.events) ? source.events as TransitionEvent[] : [],
    deployment_verification: {
      provider: ['manual', 'http', 'k8s', 'unavailable'].includes(String(deploymentVerification.provider))
        ? String(deploymentVerification.provider) as 'manual' | 'http' | 'k8s' | 'unavailable'
        : 'unavailable',
      available: deploymentVerification.available === true,
      hint: String(deploymentVerification.hint ?? ''),
    },
    bug_ticket_resolution: {
      state: ['not_ready', 'pending', 'resolved', 'unknown'].includes(String(record(source.bug_ticket_resolution).state))
        ? String(record(source.bug_ticket_resolution).state) as 'not_ready' | 'pending' | 'resolved' | 'unknown'
        : 'unknown',
      source_status: String(record(source.bug_ticket_resolution).source_status ?? ''),
    },
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
