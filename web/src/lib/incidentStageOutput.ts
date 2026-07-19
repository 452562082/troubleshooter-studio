import type { PhaseAttempt } from './bridge/bugWorkflow'

export type StageTone = 'neutral' | 'success' | 'warning' | 'danger' | 'info'
export interface StageField { label: string; value: string; mono?: boolean }
export interface StageSection { title: string; tone?: StageTone; text?: string; fields?: StageField[]; items?: string[]; groups?: StageField[][]; groupLabel?: string; emptyText?: string }
export interface StageAttemptPresentation {
  phaseLabel: string
  attemptStatusLabel: string
  resultLabel: string
  tone: StageTone
  environment: string
  startedAt: string
  finishedAt: string
  sections: StageSection[]
}

const phaseLabels: Record<PhaseAttempt['phase'], string> = { validation: '验证', investigation: '排障', fix: '修复', regression: '回归验证', legacy: '历史运行' }
const attemptStatusLabels: Record<PhaseAttempt['status'], string> = { queued: '排队中', running: '执行中', succeeded: '已完成', failed: '失败', cancelled: '已取消', interrupted: '已中断' }
const resultLabels: Record<string, { label: string; tone: StageTone }> = {
  reproduced: { label: '已复现', tone: 'warning' }, not_reproduced: { label: '未复现', tone: 'info' }, insufficient_info: { label: '信息不足', tone: 'warning' },
  fixed_verified: { label: '修复已验证', tone: 'success' }, still_reproduces: { label: '仍可复现', tone: 'danger' }, root_cause_ready: { label: '根因已确认', tone: 'success' },
  fixed_pushed: { label: '修复已推送', tone: 'success' }, blocked: { label: '修复受阻', tone: 'warning' }, failed: { label: '修复失败', tone: 'danger' },
}

type DataRecord = Record<string, unknown>

const confidenceLabels: Record<string, string> = { high: '高', medium: '中', low: '低' }
const fieldLabels: Record<string, string> = {
  summary: '阶段结论', conclusion: '阶段结论', report: '阶段结论', notes: '备注', metadata: '元数据', owner: '负责人', retried: '是否重试',
  kind: '类型', path: '路径', captured_at: '采集时间', environment: '环境', version: '版本', request_id: 'Request ID', trace_id: 'Trace ID', repo: '仓库', summary_text: '变更',
  command: '命令', result: '结果', note: '备注', skipped_reason: '跳过原因', base_branch: '基础分支', fix_branch: '修复分支', commit: '提交',
  pushed: '已推送', target_environment_branch: '目标环境分支', push_remote: '远端',
}
const monoKeys = new Set(['path', 'request_id', 'trace_id', 'repo', 'command', 'base_branch', 'fix_branch', 'commit', 'target_environment_branch', 'push_remote'])

function recordValue(value: unknown): DataRecord {
  return value !== null && typeof value === 'object' && !Array.isArray(value) ? value as DataRecord : {}
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function stringList(value: unknown): string[] {
  return Array.isArray(value) ? value.flatMap(item => typeof item === 'string' && item.trim() ? [item.trim()] : []) : []
}

function scalarList(value: unknown): string[] {
  return Array.isArray(value) ? value.map(scalarText).filter(Boolean) : []
}

function objectList(value: unknown): DataRecord[] {
  return Array.isArray(value) ? value.map(recordValue).filter(item => Object.keys(item).length > 0) : []
}

function scalarText(value: unknown): string {
  if (typeof value === 'string') return value.trim()
  if (typeof value === 'number') return String(value)
  if (typeof value === 'boolean') return value ? '是' : '否'
  if (Array.isArray(value)) return `包含 ${value.length} 项`
  if (value !== null && typeof value === 'object') return `包含 ${Object.keys(value).length} 项`
  return ''
}

function fieldLabel(key: string, generic = false): string {
  return fieldLabels[key] || (generic ? '内容' : key.split('_').join(' '))
}

function fieldsFromRecord(value: DataRecord, renameSummary = false, generic = false): StageField[] {
  return Object.entries(value).flatMap(([key, raw]) => {
    const text = scalarText(raw)
    if (!text) return []
    const normalizedKey = renameSummary && key === 'summary' ? 'summary_text' : key
    return [{ label: fieldLabel(normalizedKey, generic), value: text, ...(monoKeys.has(key) ? { mono: true } : {}) }]
  })
}

function resultFor(value: string, attempt: PhaseAttempt): { label: string; tone: StageTone } {
  return resultLabels[value] || { label: '阶段结果', tone: attempt.status === 'failed' ? 'danger' : 'neutral' }
}

function basePresentation(attempt: PhaseAttempt, result: { label: string; tone: StageTone }, environment: string): StageAttemptPresentation {
  return {
    phaseLabel: phaseLabels[attempt.phase], attemptStatusLabel: attemptStatusLabels[attempt.status], resultLabel: result.label, tone: result.tone,
    environment, startedAt: attempt.started_at, finishedAt: attempt.finished_at || '', sections: [],
  }
}

function textSection(title: string, value: unknown, tone?: StageTone): StageSection | undefined {
  const text = stringValue(value)
  return text ? { title, ...(tone ? { tone } : {}), text } : undefined
}

function listSection(title: string, value: unknown, tone?: StageTone): StageSection | undefined {
  const items = stringList(value)
  return items.length ? { title, ...(tone ? { tone } : {}), items } : undefined
}

function evidenceSection(title: string, value: unknown): StageSection {
  const groups = objectList(value).map(item => fieldsFromRecord(item).filter(field => ['类型', '路径', '采集时间', '环境', '版本', 'Request ID', 'Trace ID'].includes(field.label)))
    .filter(group => group.length > 0)
  return groups.length ? { title, groups, groupLabel: '证据' } : { title, emptyText: '尚无有效证据' }
}

function presentValidation(attempt: PhaseAttempt, output: DataRecord): StageAttemptPresentation {
  const view = basePresentation(attempt, resultFor(stringValue(output.verification_status), attempt), stringValue(output.environment))
  for (const section of [
    textSection('预期表现', output.expected_behavior),
    textSection('实际观察', output.observed_behavior),
    listSection('还需补充', output.gaps, 'warning'),
  ]) if (section) view.sections.push(section)
  view.sections.push(evidenceSection('验证证据', output.evidence))
  return view
}

function presentInvestigation(attempt: PhaseAttempt, output: DataRecord): StageAttemptPresentation {
  const view = basePresentation(attempt, resultFor(stringValue(output.investigation_status), attempt), stringValue(output.environment))
  const confidence = confidenceLabels[stringValue(output.confidence)] || stringValue(output.confidence)
  for (const section of [
    textSection('根因结论', output.root_cause),
    textSection('置信度', confidence),
    listSection('还需补充', output.gaps, 'warning'),
  ]) if (section) view.sections.push(section)
  view.sections.push(evidenceSection('排障证据', output.evidence))
  return view
}

function presentFix(attempt: PhaseAttempt, output: DataRecord): StageAttemptPresentation {
  const view = basePresentation(attempt, resultFor(stringValue(output.fix_status), attempt), stringValue(output.environment))
  for (const section of [textSection('阻塞原因', output.blocked_reason, 'warning'), textSection('部署说明', output.deployment_notice)]) {
    if (section) view.sections.push(section)
  }
  const changes = objectList(output.changes).map(item => fieldsFromRecord(item, true)).filter(group => group.length > 0)
  if (changes.length) view.sections.push({ title: '代码变更', groups: changes })
  const tests = objectList(output.tests).map(item => fieldsFromRecord(item)).filter(group => group.length > 0)
  if (tests.length) view.sections.push({ title: '测试结果', groups: tests })
  const branches = objectList(output.branches).map(item => fieldsFromRecord(item)).filter(group => group.length > 0)
  if (branches.length) view.sections.push({ title: '分支与提交', groups: branches })
  const risks = listSection('风险', output.risks, 'warning')
  if (risks) view.sections.push(risks)
  view.sections.push(evidenceSection('修复证据', output.evidence))
  return view
}

function fallbackSections(output: DataRecord): StageSection[] {
  const sections: StageSection[] = []
  const conclusionKey = ['summary', 'conclusion', 'report'].find(key => stringValue(output[key]))
  if (conclusionKey) sections.push({ title: '阶段结论', text: stringValue(output[conclusionKey]) })
  const ignored = new Set(['summary', 'conclusion', 'report', 'environment', 'scenario_hash', 'verification_status', 'investigation_status', 'fix_status'])
  for (const [key, value] of Object.entries(output)) {
    if (ignored.has(key)) continue
    const title = fieldLabels[key] || '补充信息'
    if (Array.isArray(value)) {
      const items = scalarList(value)
      const groups = objectList(value).map(item => fieldsFromRecord(item, false, true)).filter(group => group.length > 0)
      if (items.length || groups.length) sections.push({ title, ...(items.length ? { items } : {}), ...(groups.length ? { groups } : {}) })
      continue
    }
    if (value !== null && typeof value === 'object') {
      const group = fieldsFromRecord(recordValue(value), false, true)
      if (group.length) sections.push({ title, groups: [group] })
      continue
    }
    const text = scalarText(value)
    if (text) sections.push({ title, text })
  }
  return sections
}

function presentOutput(attempt: PhaseAttempt, output: DataRecord): StageAttemptPresentation {
  if ((attempt.phase === 'validation' || attempt.phase === 'regression') && typeof output.verification_status === 'string') return presentValidation(attempt, output)
  if (attempt.phase === 'investigation' && typeof output.investigation_status === 'string') return presentInvestigation(attempt, output)
  if (attempt.phase === 'fix' && typeof output.fix_status === 'string') return presentFix(attempt, output)
  return {
    phaseLabel: phaseLabels[attempt.phase], attemptStatusLabel: attemptStatusLabels[attempt.status], resultLabel: '阶段结果', tone: attempt.status === 'failed' ? 'danger' : 'neutral',
    environment: stringValue(output.environment), startedAt: attempt.started_at, finishedAt: attempt.finished_at || '', sections: fallbackSections(output),
  }
}

export function presentStageAttempt(attempt: PhaseAttempt): StageAttemptPresentation {
  const output = recordValue(attempt.output_json)
  if (output.kind !== 'phase_completion_intent') return presentOutput(attempt, output)

  const command = recordValue(output.command)
  const nestedOutput = recordValue(command.OutputJSON)
  if (output.version === 1 && Object.keys(nestedOutput).length > 0) return presentOutput(attempt, nestedOutput)

  const pending = basePresentation(attempt, { label: '结果待恢复', tone: 'info' }, '')
  pending.sections.push({ title: '阶段结果', text: '阶段结果正在恢复，请稍后刷新' })
  return pending
}
