<script lang="ts">
import type { IncidentCase, IncidentCaseDetail as ActionDetail, IncidentEvidenceImageInput, IncidentEvidenceFileInput } from '../lib/bridge/bugWorkflow'
export type CasePrimaryAction = {
  kind: 'start_investigation' | 'supply_evidence' | 'approve_fix' | 'reconsider_remediation' | 'dispute_root_cause' | 'redo_fix' | 'complete_remediation' | 'continue_fix' | 'approve_merge' | 'supply_merge_decision' | 'cancel_attempt' | 'continue_legacy'
  label: string
  approval?: boolean
}
export function primaryActionFor(subject: IncidentCase | ActionDetail): CasePrimaryAction | undefined {
  const incident = 'case' in subject ? subject.case : subject
  const actions: Partial<Record<IncidentCase['status'], CasePrimaryAction>> = {
    pending_investigation: { kind: 'start_investigation', label: '开始排障' },
    investigating: { kind: 'cancel_attempt', label: '停止排障' },
    waiting_evidence: { kind: 'supply_evidence', label: '补充证据并继续' },
    waiting_fix_approval: { kind: 'approve_fix', label: '允许修复', approval: true },
    waiting_remediation: { kind: 'complete_remediation', label: '记录人工处置', approval: true },
    fixing: { kind: 'cancel_attempt', label: '停止修复' },
    fix_failed: { kind: 'continue_fix', label: '补充信息并继续修复' },
    waiting_merge_approval: { kind: 'approve_merge', label: '审阅并提交', approval: true },
    merge_conflict: { kind: 'supply_merge_decision', label: '补充合并处理决定' },
    legacy_archived: { kind: 'continue_legacy', label: '开启新一轮排障' },
  }
  return actions[incident.status]
}
export function statusLabel(status: string): string {
  return ({ pending_investigation: '待排障', investigating: '排障中', waiting_evidence: '待补充证据', root_cause_ready: '根因已就绪', waiting_fix_approval: '待授权修复', waiting_remediation: '待人工处置', fixing: '修复中', fix_failed: '修复受阻', fix_pushed: '修复已推送', waiting_merge_approval: '待审阅提交', merging: '提交中', merge_conflict: '合并冲突', submitted: '已提交，待人工验证', remediation_recorded: '处置已记录，待人工验证', legacy_archived: '旧流程已归档', reset_archived: '已重启归档', fixed_verified: '历史：已验证' } as Record<string, string>)[status] || `历史：${status}`
}
</script>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import type { IncidentCaseDetail, IncidentPhaseEvent } from '../lib/bridge/bugWorkflow'
import { selectIncidentEvidence, type IncidentEvidenceSelection } from '../lib/bridge/bugWorkflow'
import { isDesktop } from '../lib/bridge/shared'
import BugAgentProgress from './BugAgentProgress.vue'
import BugCaseArtifacts from './BugCaseArtifacts.vue'
const props = defineProps<{ detail: IncidentCaseDetail | null; bugTitle?: string; pending?: boolean; error?: string; phaseEvents?: IncidentPhaseEvent[]; loadFixBranches?: (caseID: string, rootCauseID: string) => Promise<Record<string, string[]>> }>()
type ActionPayload = { kind: CasePrimaryAction['kind']; input?: string; evidence?: string; images?: IncidentEvidenceImageInput[]; files?: IncidentEvidenceFileInput[]; rootCauseAttemptID?: string; caseVersion?: number; sourceBaselines?: Record<string, string> }
const emit = defineEmits<{ primary: [payload: ActionPayload]; refresh: [] }>()
const current = computed(() => props.detail?.case)
const attempt = computed(() => props.detail?.attempts.find(a => a.id === current.value?.current_attempt_id))
const action = computed(() => props.detail ? primaryActionFor(props.detail) : undefined)
const root = computed(() => [...(props.detail?.attempts || [])].reverse().find(a => a.cycle_number === current.value?.cycle_number && a.phase === 'investigation' && a.status === 'succeeded'))
const changes = computed(() => (props.detail?.code_changes || []).filter(c => c.attempt_id === current.value?.current_attempt_id))
const canReassess = computed(() => ['waiting_fix_approval', 'waiting_remediation', 'waiting_merge_approval'].includes(current.value?.status || ''))
const stageIndex = computed(() => {
  const status = current.value?.status || ''
  if (['waiting_merge_approval', 'merging', 'merge_conflict', 'submitted'].includes(status)) return 2
  if (['waiting_fix_approval', 'waiting_remediation', 'fixing', 'fix_failed', 'fix_pushed', 'remediation_recorded'].includes(status) || (status === 'waiting_evidence' && attempt.value?.phase === 'fix')) return 1
  return ['pending_investigation', 'investigating', 'waiting_evidence', 'root_cause_ready'].includes(status) ? 0 : -1
})
const completed = computed(() => ['submitted', 'remediation_recorded'].includes(current.value?.status || ''))
const nextStep = computed(() => ({
  pending_investigation: '根据工单描述与附件，分析问题根因。',
  investigating: '正在分析工单、日志和源码，进度会自动更新。',
  waiting_evidence: '请补充结果中列出的信息，然后继续处理。',
  waiting_fix_approval: '请先阅读排障结论，确认后授权修复。',
  waiting_remediation: '请按处理建议执行，并记录人工处置结果。',
  fixing: '正在修改代码并运行工程测试。',
  fix_failed: '请查看受阻原因，补充信息后继续修复。',
  waiting_merge_approval: '请审阅代码变更与测试结果，再授权提交。',
  merging: '正在合并并推送修复，请稍候。',
  merge_conflict: '提交遇到冲突，请补充处理决定。',
} as Record<string, string>)[current.value?.status || ''] || '')
const dialog = ref<{ action: CasePrimaryAction; caseID: string; version: number; rootID: string } | null>(null)
const input = ref(''), evidence = ref(''), dialogError = ref('')
const baselines = ref<Array<{ repo: string; branch: string }>>([])
const options = ref<Record<string, string[]>>({})
const loadingBranches = ref(false)
const images = ref<IncidentEvidenceImageInput[]>([]), files = ref<IncidentEvidenceFileInput[]>([])
const readingFiles = ref(false)
const evidenceFileInput = ref<HTMLInputElement | null>(null)
const panel = ref<HTMLElement | null>(null)
let trigger: HTMLElement | null = null
let generation = 0
watch(() => [current.value?.id, current.value?.version], () => closeDialog())
function closeDialog() {
  generation++
  dialog.value = null
  loadingBranches.value = false
  readingFiles.value = false
  nextTick(() => trigger?.focus())
}
function repositories(output: Record<string, unknown> | undefined): string[] {
  const remediation = output?.remediation as Record<string, unknown> | undefined
  const declared = remediation?.repositories
  const chain = Array.isArray(output?.call_chain) ? output.call_chain : []
  const raw = Array.isArray(declared) && declared.length ? declared : chain.map(h => h?.repo)
  return [...new Set(raw.filter((r): r is string => typeof r === 'string' && Boolean(r.trim())).map(r => r.trim()))]
}
async function openAction(selected: CasePrimaryAction, event: MouseEvent) {
  if (props.pending || !current.value) return
  if (['cancel_attempt', 'continue_legacy', 'start_investigation'].includes(selected.kind)) { emit('primary', { kind: selected.kind }); return }
  const snapshot = { action: selected, caseID: current.value.id, version: current.value.version, rootID: root.value?.id || '' }
  trigger = event.currentTarget as HTMLElement
  dialog.value = snapshot
  input.value = ''; evidence.value = ''; dialogError.value = ''; images.value = []; files.value = []
  baselines.value = repositories(root.value?.output_json).map(repo => ({ repo, branch: '' }))
  options.value = {}
  const request = ++generation
  await nextTick()
  panel.value?.focus()
  if (selected.kind !== 'approve_fix' || !props.loadFixBranches) return
  loadingBranches.value = true
  try {
    const result = await props.loadFixBranches(snapshot.caseID, snapshot.rootID)
    if (generation === request) options.value = result
  } catch (error) {
    if (generation === request) dialogError.value = error instanceof Error ? error.message : String(error)
  } finally { if (generation === request) loadingBranches.value = false }
}
const confirmDisabled = computed(() => {
  if (!dialog.value || props.pending || readingFiles.value || loadingBranches.value) return true
  const kind = dialog.value.action.kind
  if (kind === 'approve_fix') return !dialog.value.rootID || !baselines.value.length || baselines.value.some(b => !b.repo.trim())
  if (kind === 'approve_merge') return !changes.value.length
  if (kind === 'supply_evidence') return !input.value.trim() && !images.value.length && !files.value.length
  if (kind === 'complete_remediation') return !input.value.trim() || !evidence.value.trim()
  return !input.value.trim()
})
function confirmAction() {
  const request = dialog.value
  if (!request || confirmDisabled.value || current.value?.id !== request.caseID || current.value.version !== request.version) return
  emit('primary', { kind: request.action.kind, caseVersion: request.version, rootCauseAttemptID: request.rootID, input: input.value.trim(), evidence: evidence.value.trim(), images: [...images.value], files: [...files.value], sourceBaselines: Object.fromEntries(baselines.value.map(b => [b.repo, b.branch.trim()])) })
  closeDialog()
}
function appendEvidence(selection: IncidentEvidenceSelection) {
  if (images.value.length + selection.images.length > 4 || files.value.length + selection.files.length > 4) throw new Error('PNG/JPEG 截图和其他文件各最多添加 4 个')
  images.value.push(...selection.images)
  files.value.push(...selection.files)
}
async function chooseEvidence() {
  if (!dialog.value || readingFiles.value || props.pending) return
  if (!isDesktop()) { evidenceFileInput.value?.click(); return }
  const request = generation
  readingFiles.value = true
  dialogError.value = ''
  try {
    const selection = await selectIncidentEvidence()
    if (generation === request) appendEvidence(selection)
  } catch (error) {
    if (generation === request) dialogError.value = error instanceof Error ? error.message : String(error)
  } finally { if (generation === request) readingFiles.value = false }
}
async function selectEvidence(event: Event) {
  const target = event.target as HTMLInputElement
  const selected = [...(target.files || [])]
  target.value = ''
  if (!dialog.value || readingFiles.value) return
  const request = generation
  readingFiles.value = true
  dialogError.value = ''
  try {
    const selection: IncidentEvidenceSelection = { images: [], files: [] }
    const isImage = (file: File) => /\.(png|jpe?g)$/i.test(file.name)
    if (images.value.length + selected.filter(isImage).length > 4 || files.value.length + selected.filter(file => !isImage(file)).length > 4) throw new Error('PNG/JPEG 截图和其他文件各最多添加 4 个')
    for (const file of selected) {
      if (!file.size || file.size > 16 * 1024 * 1024) throw new Error('文件不能为空，且单个不超过 16 MB')
      const data = await new Promise<string>((resolve, reject) => {
        const reader = new FileReader()
        reader.onload = () => resolve(String(reader.result).split(',')[1] || '')
        reader.onerror = () => reject(new Error('读取附件失败'))
        reader.onabort = () => reject(new Error('附件读取已取消'))
        reader.readAsDataURL(file)
      })
      if (generation !== request) return
      if (isImage(file)) selection.images.push({ name: file.name, mime_type: /\.png$/i.test(file.name) ? 'image/png' : 'image/jpeg', base64_data: data })
      else selection.files.push({ name: file.name, mime_type: file.type || 'application/octet-stream', base64_data: data })
    }
    appendEvidence(selection)
  } catch (error) { if (generation === request) dialogError.value = error instanceof Error ? error.message : String(error) }
  finally { if (generation === request) readingFiles.value = false }
}

function trapFocus(event: KeyboardEvent) {
  if (event.key !== 'Tab') return
  const items = [...(panel.value?.querySelectorAll<HTMLElement>('button:not(:disabled), input:not(:disabled):not([hidden]), textarea, select, [tabindex="0"]') || [])]
  if (!items.length) { event.preventDefault(); return }
  const first = items[0], last = items[items.length - 1]
  if (event.shiftKey && (document.activeElement === first || document.activeElement === panel.value)) { event.preventDefault(); last.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
}
</script>

<template>
  <section class="case-lifecycle" aria-label="排障任务" tabindex="-1">
    <template v-if="detail && current">
      <header class="case-heading" :data-case-id="current.id">
        <div><span class="eyebrow">处理任务 · 第 {{ current.cycle_number }} 轮</span><h2>{{ bugTitle || '排障任务' }}</h2><p class="case-meta"><span :data-status="current.status" class="status-badge" :class="{ complete: completed }">{{ statusLabel(current.status) }}</span><span>{{ current.environment }}</span></p></div>
        <button class="btn refresh-button" aria-label="刷新故障闭环" @click="emit('refresh')">刷新</button>
      </header>
      <ol class="stages" aria-label="处理流程"><li v-for="(stage, index) in ['排障', '修复', '提交']" :key="stage" :class="{ active: index === stageIndex && !completed, done: index < stageIndex || (completed && current.status === 'submitted') }" :aria-current="index === stageIndex && !completed ? 'step' : undefined"><span class="stage-number">{{ index < stageIndex || (completed && current.status === 'submitted') ? '✓' : index + 1 }}</span><span>{{ stage }}</span></li></ol>
      <p v-if="current.status === 'submitted' || current.status === 'remediation_recorded'" class="notice workflow-loop-hint" role="status">工作台处理已完成。请由人工验收并更新工单状态。</p>
      <p v-if="current.status === 'legacy_archived'" class="notice workflow-loop-hint">此任务属于旧流程，记录和附件已保留。可开启新一轮排障。</p>
      <p v-if="error" class="error live-error" role="alert">{{ error }}</p>
      <div v-if="action || nextStep" class="current-action-card">
        <p v-if="nextStep">{{ nextStep }}</p>
        <div class="actions">
        <button v-if="action" :class="['btn primary-action', action.kind === 'cancel_attempt' ? 'stop-button' : 'primary']" data-primary-action :disabled="pending" @click="openAction(action, $event)">{{ action.label }}</button>
        <button v-if="canReassess" class="btn dispute-action" :disabled="pending" @click="openAction({ kind: 'dispute_root_cause', label: '质疑根因，重新排障' }, $event)">质疑根因</button>
        <button v-if="canReassess" class="btn" :class="current.status === 'waiting_merge_approval' ? 'rework-action' : 'reconsider-action'" :disabled="pending" @click="openAction({ kind: current.status === 'waiting_merge_approval' ? 'redo_fix' : 'reconsider_remediation', label: '重新评估处理方案' }, $event)">调整方案</button>
      </div>
      </div>
      <BugAgentProgress v-if="attempt && ['investigating', 'fixing'].includes(current.status)" :attempt="attempt" :events="phaseEvents || []" />
      <BugCaseArtifacts :key="current.id" :detail="detail" />
      <details v-if="detail.events.length" class="timeline"><summary>操作记录（{{ detail.events.length }}）</summary><ol><li v-for="event in [...detail.events].reverse()" :key="event.id">{{ new Date(event.created_at).toLocaleString('zh-CN', { hour12: false }) }} · {{ statusLabel(event.to_status) }} <span>{{ event.actor_id }}</span></li></ol></details>
    </template>
    <p v-else>请选择排障任务</p>
    <div v-if="dialog" class="backdrop" @click.self="closeDialog" @keydown.esc="closeDialog">
      <section ref="panel" class="dialog" role="dialog" aria-modal="true" aria-labelledby="action-dialog-title" tabindex="-1" @keydown="trapFocus">
        <h2 id="action-dialog-title">{{ dialog.action.label }}</h2>
        <template v-if="dialog.action.kind === 'approve_fix'">
          <p>确认修复涉及的仓库和开发基线。留空使用该仓库默认开发基线，修复将运行工程测试。</p>
          <p v-if="loadingBranches">正在读取分支…</p>
          <label v-for="(baseline, index) in baselines" :key="baseline.repo" class="source-baseline-row">{{ baseline.repo }}<input type="text" :id="`fix-baseline-${index}`" v-model="baseline.branch" :list="`branches-${index}`" placeholder="默认开发基线" /><datalist :id="`branches-${index}`"><option v-for="branch in options[baseline.repo] || []" :key="branch" :value="branch" /></datalist></label>
          <p v-if="!baselines.length" class="error">根因结论未指定代码仓库，请调整方案后再修复。</p>
        </template>
        <template v-else-if="dialog.action.kind === 'approve_merge'">
          <p>授权合并并推送以下修复。完成后状态为“已提交，待人工验证”。</p>
          <dl v-for="change in changes" :key="change.id"><dt>{{ change.repo }}</dt><dd>修复提交：{{ change.fix_commit }}</dd><dd>开发基线：{{ change.base_branch }}</dd><dd>环境分支：{{ change.target_environment_branch }}</dd><dd>目标版本：{{ change.merge_base_head }}</dd></dl>
        </template>
        <template v-else>
          <label>说明<textarea :id="dialog.action.kind === 'complete_remediation' ? 'remediation-summary' : dialog.action.kind === 'reconsider_remediation' ? 'remediation-proposal' : dialog.action.kind === 'redo_fix' ? 'fix-rework-feedback' : dialog.action.kind === 'dispute_root_cause' ? 'root-cause-dispute-reason' : 'supplemental-evidence'" v-model="input" rows="5" placeholder="填写补充信息、处理决定或调整理由" /></label>
          <label v-if="dialog.action.kind === 'complete_remediation'">处置记录<textarea id="remediation-evidence" v-model="evidence" rows="3" placeholder="记录实际执行的动作及相关证据" /></label>
          <div v-if="dialog.action.kind === 'supply_evidence'" class="evidence-picker">
            <button type="button" class="btn" data-select-evidence :disabled="readingFiles || pending" @click="chooseEvidence">{{ readingFiles ? '正在选择或读取…' : '添加截图或证据文件' }}</button>
            <input ref="evidenceFileInput" hidden type="file" multiple accept=".png,.jpg,.jpeg,.gif,.webp,.csv,.tsv,.txt,.json,.xml,.pdf,.xls,.xlsx,.doc,.docx,.ppt,.pptx,.mp3,.wav,.m4a,.mp4,.mov,.webm" @change="selectEvidence" />
            <p class="attachment-hint">PNG/JPEG 截图和其他文件各最多 4 个，单个不超过 16 MB。</p>
            <p class="attachment-count" role="status">{{ images.length || files.length ? `已选择 ${images.length} 张截图、${files.length} 个文件` : '尚未添加附件' }}</p>
          </div>
          <ul v-if="images.length || files.length" class="attachment-list"><li v-for="(file, index) in images" :key="`image-${index}`">{{ file.name }} <button type="button" :disabled="readingFiles" :aria-label="`移除 ${file.name}`" @click="images.splice(index, 1)">移除</button></li><li v-for="(file, index) in files" :key="`file-${index}`">{{ file.name }} <button type="button" :disabled="readingFiles" :aria-label="`移除 ${file.name}`" @click="files.splice(index, 1)">移除</button></li></ul>
        </template>
        <p v-if="dialogError" class="error" role="alert">{{ dialogError }}</p>
        <footer><button class="btn" @click="closeDialog">取消</button><button class="btn primary" data-confirm data-dialog-confirm :disabled="confirmDisabled" @click="confirmAction">确认</button></footer>
      </section>
    </div>
  </section>
</template>
<style scoped>
.case-lifecycle { min-width: 0; display: grid; gap: 16px; padding: 20px; font-size: 13px; line-height: 1.6; container: case-content / inline-size; border: 1px solid var(--c-line); border-radius: 12px; background: var(--c-surf); overflow-wrap: anywhere; box-shadow: 0 4px 20px #0f172a04; }
.case-heading, .actions, footer { display: flex; gap: 12px; align-items: center; flex-wrap: wrap; }
.case-heading, footer { justify-content: space-between; }
.case-heading > div { flex: 1; min-width: 0; }
.eyebrow { color: var(--c-muted); font-size: 11px; font-weight: 600; letter-spacing: .05em; }
h2 { margin: 8px 0; color: var(--c-ink); font-size: 18px; line-height: 1.4; }
.case-meta { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; margin: 0; color: var(--c-muted); font-size: 12px; }
.status-badge { padding: 4px 9px; border-radius: 6px; color: #1d4ed8; background: #eff6ff; font-weight: 600; }
.status-badge.complete { color: #15803d; background: #f0fdf4; }
.stages { display: flex; list-style: none; margin: 0; padding: 12px 14px; background: #f8fafc; border: 1px solid #edf1f6; border-radius: 10px; gap: 12px; }
.stages li { display: flex; align-items: center; gap: 9px; flex: 1; color: var(--c-muted); font-size: 13px; font-weight: 600; }
.stages li:not(:last-child)::after { content: ''; flex: 1; height: 1px; margin-left: 8px; background: var(--c-line); }
.stage-number { display: grid; place-items: center; width: 26px; height: 26px; border-radius: 50%; background: var(--c-surf-2); font-size: 12px; }
.stages .active { color: #2563eb; } .active .stage-number { color: white; background: #2563eb; box-shadow: 0 0 0 4px #eff6ff; }
.stages .done { color: #15803d; } .done .stage-number { background: #f0fdf4; }
.current-action-card { padding: 12px 14px; background: #f8fafc; border: 1px solid var(--c-line); border-radius: 10px; display: flex; justify-content: space-between; align-items: center; flex-wrap: wrap; gap: 12px; }
.current-action-card > p { flex: 1 1 240px; margin: 0; font-size: 13px; color: var(--c-muted); line-height: 1.6; }
.btn { min-height: 36px; padding: 8px 14px; border: 1px solid var(--c-line); border-radius: 8px; color: var(--c-text); background: var(--c-surf); font: inherit; font-size: 13px; cursor: pointer; }
.btn:hover:not(:disabled) { background: var(--c-surf-2); border-color: #94a3b8; }
.btn.primary { color: white; background: #2563eb; border-color: #2563eb; font-weight: 600; }
.btn.primary:hover:not(:disabled) { background: #1d4ed8; }
.btn.stop-button { color: #b91c1c; border-color: #fecaca; }
.btn:disabled { opacity: .5; cursor: not-allowed; }
.btn:focus-visible, summary:focus-visible { outline: 2px solid #2563eb; outline-offset: 3px; }
.notice { margin: 0; padding: 12px 14px; background: #eff6ff; color: #1e40af; border-radius: 8px; font-size: 13px; line-height: 1.6; }
.evidence-picker { margin: 16px 0; }
.dialog .attachment-hint, .dialog .attachment-count { margin: 8px 0 0; color: var(--c-muted); font-size: 12px; }
.attachment-list { padding: 0; list-style: none; display: grid; gap: 8px; }
.attachment-list li { display: flex; justify-content: space-between; align-items: center; gap: 12px; padding: 8px 10px; border-radius: 8px; background: var(--c-surf-2); font-size: 13px; overflow-wrap: anywhere; }
.attachment-list button { flex: none; padding: 4px 8px; background: transparent; border: 0; color: var(--c-accent); cursor: pointer; }
.error { margin: 0; color: var(--c-danger,#b91c1c); }
.timeline { border-top: 1px solid var(--c-line); font-size: 12px; color: var(--c-muted); }
.timeline summary { padding-top: 14px; cursor: pointer; }
.timeline ol { padding-left: 20px; display: grid; gap: 10px; }
.backdrop { position: fixed; inset: 0; background: #0f172a80; display: grid; place-items: center; z-index: 100; padding: 16px; }
.dialog { width: min(640px,100%); max-height: 85vh; overflow: auto; background: var(--c-surf); border-radius: 16px; padding: 24px; box-sizing: border-box; box-shadow: 0 24px 80px #0f172a30; }
.dialog p { margin: 0; font-size: 13px; line-height: 1.65; color: var(--c-muted); }
label { display: grid; gap: 8px; margin: 12px 0; font-size: 13px; }
input, textarea { min-width: 0; width: 100%; box-sizing: border-box; padding: 10px; border: 1px solid var(--c-line); border-radius: 8px; background: var(--c-surf); color: var(--c-text); font: inherit; }
dd { margin: 4px 0; } footer { margin-top: 20px; }
@media (max-width: 700px) { .case-lifecycle { padding: 16px; gap: 16px; } h2 { font-size: 18px; } .stages { gap: 8px; } .actions { gap: 8px; } }

.status-badge[data-status="waiting_evidence"], .status-badge[data-status="waiting_fix_approval"], .status-badge[data-status="waiting_merge_approval"], .status-badge[data-status="waiting_remediation"] { color: #92400e; background: #fffbeb; }
.status-badge[data-status="fix_failed"], .status-badge[data-status="merge_conflict"] { color: #b91c1c; background: #fef2f2; }
.status-badge[data-status="legacy_archived"], .status-badge[data-status="reset_archived"] { color: #475569; background: #f1f5f9; }
.case-heading h2 { font-size: 17px; line-height: 1.5; font-weight: 650; margin: 5px 0 10px; }
.case-heading > .btn { flex-shrink: 0; font-size: 12px; }
.actions { gap: 8px; }
.current-action-card .dispute-action, .current-action-card .reconsider-action, .current-action-card .rework-action { background: transparent; }
.timeline summary { padding: 12px 0 0; }
.timeline ol { max-height: 260px; overflow: auto; line-height: 1.7; }
.backdrop { background: #0f172a66; backdrop-filter: blur(3px); }
.dialog { display: grid; gap: 16px; border: 1px solid #e2e8f0; }
.dialog h2 { margin: 0; padding-bottom: 14px; border-bottom: 1px solid var(--c-line); font-size: 18px; line-height: 1.4; }
.dialog label { display: grid; gap: 8px; min-width: 0; font-size: 12px; font-weight: 600; color: var(--c-text); }
.dialog input, .dialog textarea { box-sizing: border-box; width: 100%; min-width: 0; margin: 0; padding: 10px 12px; font: inherit; font-size: 13px; line-height: 1.6; font-weight: 400; border: 1px solid var(--c-line-2); border-radius: 8px; }
.dialog textarea { resize: vertical; }
.dialog .source-baseline-row { padding: 12px; background: #f8fafc; border: 1px solid var(--c-line); border-radius: 8px; }
.dialog > dl { margin: 0; padding: 12px; border: 1px solid var(--c-line); border-radius: 8px; font-size: 12px; line-height: 1.7; }
.dialog > dl dt { font-weight: 600; color: var(--c-ink); margin-bottom: 6px; }
.dialog > dl dd { margin: 0; overflow-wrap: anywhere; }
.dialog footer { justify-content: flex-end; padding-top: 16px; border-top: 1px solid var(--c-line); }
.dialog footer .btn { min-width: 80px; }
.dialog .evidence-picker { margin: 0; padding: 16px; border: 1px dashed #cbd5e1; background: #f8fafc; border-radius: 10px; }
.dialog .error { color: #b91c1c; padding: 10px 12px; border-radius: 8px; background: #fef2f2; }
@container case-content (max-width: 500px) {
  .case-heading { align-items: flex-start; gap: 8px; }
  .case-heading h2 { font-size: 16px; }
  .stages { gap: 8px; padding: 10px; }
  .stages li { gap: 6px; font-size: 12px; }
  .stages li:not(:last-child)::after { margin-left: 0; }
  .actions { width: 100%; }
  .actions .btn { flex: 1 1 auto; }
}
@media (max-width: 640px) { .case-lifecycle { padding: 14px; } .dialog { padding: 18px; } }
@media (pointer: coarse) { .btn, .attachment-list button { min-height: 44px; } }
</style>
