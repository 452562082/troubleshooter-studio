import * as App from '../../../wailsjs/go/main/App'
import { isDesktop } from './shared'

const desktopApp = App as Record<string, (...args: any[]) => Promise<any>>

export interface BugAttachment {
  id?: string
  name: string
  type?: string
  local_path?: string
  remote_url?: string
}

export interface BugRecord {
  id: string
  source: string
  source_id?: string
  title: string
  description?: string
  steps?: string
  expected?: string
  actual?: string
  status?: string
  severity?: string
  priority?: string
  product?: string
  module?: string
  bug_type?: string
  os?: string
  browser?: string
  keywords?: string
  assignee?: string
  reporter?: string
  created_at?: string
  updated_at?: string
  env?: string
  bot_env?: string
  system_id?: string
  frontend_repo?: string
  service_hints?: string[]
  frontend_url?: string
  api_paths?: string[]
  trace_ids?: string[]
  request_ids?: string[]
  attachments?: BugAttachment[]
  selected_bot_key?: string
  last_context?: string
  last_context_at?: string
  raw_preview?: string
}

export interface BotRef {
  key: string
  system_id: string
  target: string
  path: string
  name?: string
  agent_id?: string
  role?: string
  internal_agents?: Array<{ id: string, role: string }>
  env?: string
  envs?: string[]
}

export interface BotMatch {
  bot: BotRef
  score: number
  reasons: string[]
}

export type InvestigationStatus = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled'

export interface InvestigationEvent {
  at?: string
  type: string
  message: string
  raw?: Record<string, unknown>
  meta?: Record<string, unknown>
}

export interface InvestigationRun {
  id: string
  bug_id: string
  bot_key: string
  status: InvestigationStatus
  started_at?: string
  finished_at?: string
  prompt_preview?: string
  events?: InvestigationEvent[]
  final_message?: string
  error?: string
}

export interface BugInvestigationInput {
  bug_id: string
  bot: BotRef
}

export interface BugInvestigationCancelInput {
  run_id: string
}

export interface BugPlatform {
  id: string
  name: string
  type: string
  base_url?: string
  account?: string
  env?: string
  auth_mode?: string
  session_header?: string
  password?: string
  token?: string
  hook_secret?: string
  bot_env?: string
  bot_mappings?: PlatformBotMapping[]
  enabled: boolean
  poll_enabled?: boolean
  poll_interval_minutes?: number
  created_at?: string
  updated_at?: string
}

export interface PlatformBotMapping {
  bot_key: string
  env?: string
}

export interface BugPlatformInput {
  id?: string
  name: string
  type: string
  base_url?: string
  account?: string
  env?: string
  auth_mode?: string
  session_header?: string
  password?: string
  token?: string
  hook_secret?: string
  bot_env?: string
  bot_mappings?: PlatformBotMapping[]
  enabled: boolean
  poll_enabled?: boolean
  poll_interval_minutes?: number
}

export interface BugSyncResult {
  platform_id: string
  fetched: number
  stored: number
  selected_bug_id?: string
}

export interface BugFetchInput {
  platform_id: string
  bug_id: string
}

export interface BugAttachmentPreviewInput {
  platform_id: string
  bug_id: string
  attachment_index: number
}

export interface BugAttachmentPreviewResult {
  name: string
  content_type: string
  data_url: string
}

export interface BugLoginInput {
  platform_id: string
}

export interface BugPlatformDeleteInput {
  platform_id: string
}

export interface BugLoginResult {
  platform_id: string
  auth_mode: string
  session_saved: boolean
  cookie_count: number
  message?: string
}

export async function listBugPlatforms(): Promise<BugPlatform[]> {
  if (!isDesktop()) return []
  const r = await desktopApp.ListBugPlatforms()
  return Array.isArray(r) ? r as BugPlatform[] : []
}

export async function saveBugPlatform(input: BugPlatformInput): Promise<BugPlatform> {
  if (!isDesktop()) throw new Error('Bug 平台配置只在桌面 app 可用')
  return desktopApp.SaveBugPlatform(input) as Promise<BugPlatform>
}

export async function deleteBugPlatform(input: BugPlatformDeleteInput): Promise<void> {
  if (!isDesktop()) throw new Error('删除 Bug 平台只在桌面 app 可用')
  await desktopApp.DeleteBugPlatform(input)
}

export async function bugHookBaseURL(): Promise<string> {
  if (!isDesktop()) return ''
  return desktopApp.BugHookBaseURL() as Promise<string>
}

export async function listBugs(): Promise<BugRecord[]> {
  if (!isDesktop()) return []
  const r = await desktopApp.ListBugs()
  return Array.isArray(r) ? r as BugRecord[] : []
}

export async function syncBugPlatform(platformID: string): Promise<BugSyncResult> {
  if (!isDesktop()) throw new Error('同步 Bug 只在桌面 app 可用')
  return desktopApp.SyncBugPlatform(platformID) as Promise<BugSyncResult>
}

export async function fetchBugByID(input: BugFetchInput): Promise<BugSyncResult> {
  if (!isDesktop()) throw new Error('拉取 Bug 只在桌面 app 可用')
  return desktopApp.FetchBugByID(input) as Promise<BugSyncResult>
}

export async function previewBugAttachment(input: BugAttachmentPreviewInput): Promise<BugAttachmentPreviewResult> {
  if (!isDesktop()) throw new Error('附件预览只在桌面 app 可用')
  return desktopApp.PreviewBugAttachment(input) as Promise<BugAttachmentPreviewResult>
}

export async function loginBugPlatform(input: BugLoginInput): Promise<BugLoginResult> {
  if (!isDesktop()) throw new Error('飞书授权登录只在桌面 app 可用')
  return desktopApp.LoginBugPlatform(input) as Promise<BugLoginResult>
}

export async function clearBugPlatformLogin(input: BugLoginInput): Promise<BugLoginResult> {
  if (!isDesktop()) throw new Error('清除登录态只在桌面 app 可用')
  return desktopApp.ClearBugPlatformLogin(input) as Promise<BugLoginResult>
}

export async function matchBugBots(bugID: string): Promise<BotMatch[]> {
	if (!isDesktop()) return []
	const r = await desktopApp.MatchBugBots(bugID)
	return Array.isArray(r) ? r.map(normalizeBotMatch) : []
}

function normalizeBotMatch(raw: any): BotMatch {
	return {
		bot: raw?.bot || { key: '', system_id: '', target: '', path: '' },
		score: Number(raw?.score) || 0,
		reasons: Array.isArray(raw?.reasons) ? raw.reasons : [],
	}
}

export async function startBugInvestigation(input: BugInvestigationInput): Promise<InvestigationRun> {
	if (!isDesktop()) throw new Error('启动排障只在桌面 app 可用')
	return normalizeInvestigationRun(await desktopApp.StartBugInvestigation(input))
}

export async function cancelBugInvestigation(input: BugInvestigationCancelInput): Promise<void> {
	if (!isDesktop()) throw new Error('停止排障只在桌面 app 可用')
	await desktopApp.CancelBugInvestigation(input)
}

export async function listBugInvestigationRuns(bugID: string): Promise<InvestigationRun[]> {
	if (!isDesktop()) return []
	const r = await desktopApp.ListBugInvestigationRuns(bugID)
	return Array.isArray(r) ? r.map(normalizeInvestigationRun) : []
}

function normalizeInvestigationRun(raw: any): InvestigationRun {
	return {
		id: String(raw?.id ?? ''),
		bug_id: String(raw?.bug_id ?? ''),
		bot_key: String(raw?.bot_key ?? ''),
		status: raw?.status || 'queued',
		started_at: raw?.started_at,
		finished_at: raw?.finished_at,
		prompt_preview: raw?.prompt_preview,
		events: Array.isArray(raw?.events) ? raw.events : [],
		final_message: raw?.final_message || '',
		error: raw?.error || '',
	}
}

export async function generateBugContext(input: { bug_id: string; bot: BotRef }): Promise<string> {
	if (!isDesktop()) throw new Error('生成排障上下文只在桌面 app 可用')
	return desktopApp.GenerateBugContext(input)
}
