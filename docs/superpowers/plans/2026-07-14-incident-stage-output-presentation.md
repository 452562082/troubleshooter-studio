# Incident Stage Output Presentation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace raw Incident Case stage JSON with phase-aware Chinese result cards and keep the stage-output viewport following the newest result.

**Architecture:** Add a pure TypeScript presenter that converts each stable `PhaseAttempt.output_json` contract into a small semantic view model, then render that model in a focused Vue component while `BugCaseArtifacts` continues to own attempt ordering, legacy projection, and scrolling. Keep raw structured data unchanged in the bridge and database; only the presentation path changes.

**Tech Stack:** Vue 3 Composition API, TypeScript, Vue single-file components, Vitest, Vue Test Utils, happy-dom.

## Global Constraints

- Never expose raw stage JSON, snake_case field labels, a “查看原始数据” control, or executable HTML.
- Validation/regression, investigation, and fix attempts must use their stable phase contracts; legacy attempts keep the existing inert limited-Markdown projection.
- The newest non-legacy attempt stays at the bottom and is expanded; older attempts remain keyboard-accessible native `<details>` elements.
- On initial render, Case change, attempt addition/removal, or attempt phase/status/output/error mutation, set only `.attempt-output-scroll.scrollTop` to its `scrollHeight` after Vue finishes rendering.
- Do not call `scrollIntoView`, `window.scrollTo`, or otherwise move the page viewport.
- With no data change, do not periodically reset a user’s manual scroll position.
- Preserve desktop `height: clamp(320px, 45vh, 640px)` and mobile `height: clamp(280px, 42vh, 480px)`, internal vertical scrolling, no horizontal scrolling, keyboard focus, and overscroll containment.
- Do not change Go contracts, Wails bridge types, SQLite schema, Agent prompts, Case transitions, or lifecycle actions.
- Keep Vue interpolation as the only output path; do not add `v-html`.
- Preserve the user’s unrelated deletion of `internal/webui/dist/.gitkeep`; never stage it.

---

### Task 1: Build the phase-aware presentation model

**Files:**
- Create: `web/src/lib/incidentStageOutput.ts`
- Create: `web/src/lib/incidentStageOutput.test.ts`

**Interfaces:**
- Consumes: `PhaseAttempt` from `web/src/lib/bridge/bugWorkflow.ts`.
- Produces: `presentStageAttempt(attempt: PhaseAttempt): StageAttemptPresentation` plus exported `StageTone`, `StageField`, `StageSection`, and `StageAttemptPresentation` types.
- The Vue rendering task consumes only this semantic model and never indexes `output_json` itself.

- [ ] **Step 1: Write failing presenter tests for every stable phase contract**

Create `web/src/lib/incidentStageOutput.test.ts` with focused fixtures and assertions:

```ts
import { describe, expect, it } from 'vitest'
import type { PhaseAttempt } from './bridge/bugWorkflow'
import { presentStageAttempt } from './incidentStageOutput'

function attempt(phase: PhaseAttempt['phase'], output_json: Record<string, unknown>, status: PhaseAttempt['status'] = 'succeeded'): PhaseAttempt {
  return { id: `${phase}-1`, case_id: 'case-1', cycle_number: 1, phase, mode: phase === 'validation' ? 'reproduce' : phase === 'regression' ? 'regression' : '', status, agent_target: 'codex', bot_key: 'base|codex', input_json: {}, output_json, parent_attempt_id: '', started_at: '2026-07-14T10:00:00Z', finished_at: '2026-07-14T10:01:00Z', error_code: '', error_message: '', usage: {} }
}

describe('presentStageAttempt', () => {
  it('presents validation results as conclusion-first Chinese sections', () => {
    const view = presentStageAttempt(attempt('validation', {
      verification_status: 'insufficient_info', environment: 'test',
      expected_behavior: '显示全部匹配用户', observed_behavior: 'APP 只显示一名用户',
      gaps: ['缺少测试账号', '缺少 Network 导出'], evidence: [], scenario_hash: 'internal-only',
    }, 'failed'))

    expect(view).toMatchObject({ phaseLabel: '验证', attemptStatusLabel: '失败', resultLabel: '信息不足', tone: 'warning', environment: 'test' })
    expect(view.sections.map(section => section.title)).toEqual(['预期表现', '实际观察', '还需补充', '验证证据'])
    expect(view.sections[2].items).toEqual(['缺少测试账号', '缺少 Network 导出'])
    expect(view.sections[3].emptyText).toBe('尚无有效证据')
    expect(JSON.stringify(view)).not.toContain('scenario_hash')
  })

  it('presents investigation root cause and confidence', () => {
    const view = presentStageAttempt(attempt('investigation', {
      investigation_status: 'root_cause_ready', environment: 'test', root_cause: '昵称搜索只取首条精确匹配', confidence: 'high', gaps: [], evidence: [],
    }))

    expect(view).toMatchObject({ phaseLabel: '排障', resultLabel: '根因已确认', tone: 'success', environment: 'test' })
    expect(view.sections[0]).toMatchObject({ title: '根因结论', fields: [{ label: '', value: '昵称搜索只取首条精确匹配' }] })
    expect(view.sections[1]).toMatchObject({ title: '置信度', fields: [{ label: '', value: '高' }] })
  })

  it('presents fix changes, tests, branches, deployment notice and risks', () => {
    const view = presentStageAttempt(attempt('fix', {
      fix_status: 'fixed_pushed', environment: 'test', deployment_notice: '请部署 api:test', blocked_reason: '',
      changes: [{ repo: 'api', summary: '修复模糊搜索合并逻辑' }],
      tests: [{ repo: 'api', commit: 'abc123', command: 'go test ./...', result: 'passed' }],
      branches: [{ repo: 'api', base_branch: 'main', fix_branch: 'fix/840', commit: 'abc123', pushed: true, target_environment_branch: 'test', push_remote: 'origin' }],
      risks: ['需要观察搜索延迟'], evidence: [],
    }))

    expect(view).toMatchObject({ phaseLabel: '修复', resultLabel: '修复已推送', tone: 'success', environment: 'test' })
    expect(view.sections.map(section => section.title)).toEqual(['部署说明', '代码变更', '测试结果', '分支与提交', '风险', '修复证据'])
    expect(view.sections[1].groups?.[0]).toContainEqual({ label: '仓库', value: 'api', mono: true })
    expect(view.sections[2].groups?.[0]).toContainEqual({ label: '命令', value: 'go test ./...', mono: true })
  })

  it('uses structured fallback content without serializing unknown output', () => {
    const view = presentStageAttempt(attempt('investigation', { summary: '旧版阶段结论', notes: ['第一项', '第二项'], metadata: { owner: 'search-team', retried: true } }))
    expect(view.resultLabel).toBe('阶段结果')
    expect(view.sections).toEqual([
      { title: '阶段结论', fields: [{ label: '', value: '旧版阶段结论' }] },
      { title: '备注', items: ['第一项', '第二项'] },
      { title: '元数据', groups: [[{ label: '负责人', value: 'search-team' }, { label: '是否重试', value: '是' }]] },
    ])
    const visibleText = view.sections.flatMap(section => [
      ...(section.fields || []).map(field => `${field.label}${field.value}`),
      ...(section.items || []),
      ...(section.groups || []).flatMap(group => group.map(field => `${field.label}${field.value}`)),
    ]).join(' ')
    expect(visibleText).not.toContain('{"')
  })
})
```

- [ ] **Step 2: Run the presenter test and verify RED**

Run:

```bash
cd web
npx vitest run src/lib/incidentStageOutput.test.ts
```

Expected: FAIL because `incidentStageOutput.ts` and `presentStageAttempt` do not exist.

- [ ] **Step 3: Implement the semantic presenter**

Create the complete `web/src/lib/incidentStageOutput.ts`:

```ts
import type { PhaseAttempt } from './bridge/bugWorkflow'

export type StageTone = 'neutral' | 'success' | 'warning' | 'danger' | 'info'
export interface StageField { label: string; value: string; mono?: boolean }
export interface StageSection { title: string; tone?: StageTone; fields?: StageField[]; items?: string[]; groups?: StageField[][]; emptyText?: string }
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
  kind: '类型', path: '路径', environment: '环境', version: '版本', request_id: 'Request ID', trace_id: 'Trace ID', repo: '仓库', summary_text: '变更',
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

function fieldLabel(key: string): string {
  return fieldLabels[key] || key.replaceAll('_', ' ')
}

function fieldsFromRecord(value: DataRecord, renameSummary = false): StageField[] {
  return Object.entries(value).flatMap(([key, raw]) => {
    const text = scalarText(raw)
    if (!text) return []
    const normalizedKey = renameSummary && key === 'summary' ? 'summary_text' : key
    return [{ label: fieldLabel(normalizedKey), value: text, ...(monoKeys.has(key) ? { mono: true } : {}) }]
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
  return text ? { title, ...(tone ? { tone } : {}), fields: [{ label: '', value: text }] } : undefined
}

function listSection(title: string, value: unknown, tone?: StageTone): StageSection | undefined {
  const items = stringList(value)
  return items.length ? { title, ...(tone ? { tone } : {}), items } : undefined
}

function evidenceSection(title: string, value: unknown): StageSection {
  const groups = objectList(value).map(item => fieldsFromRecord(item).filter(field => ['类型', '路径', '环境', '版本', 'Request ID', 'Trace ID'].includes(field.label)))
    .filter(group => group.length > 0)
  return groups.length ? { title, groups } : { title, emptyText: '尚无有效证据' }
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
  if (conclusionKey) sections.push({ title: '阶段结论', fields: [{ label: '', value: stringValue(output[conclusionKey]) }] })
  const ignored = new Set(['summary', 'conclusion', 'report', 'environment', 'scenario_hash', 'verification_status', 'investigation_status', 'fix_status'])
  for (const [key, value] of Object.entries(output)) {
    if (ignored.has(key)) continue
    const title = fieldLabel(key)
    if (Array.isArray(value)) {
      const items = stringList(value)
      if (items.length === value.length && items.length) sections.push({ title, items })
      else {
        const groups = objectList(value).map(item => fieldsFromRecord(item)).filter(group => group.length > 0)
        if (groups.length) sections.push({ title, groups })
      }
      continue
    }
    if (value !== null && typeof value === 'object') {
      const group = fieldsFromRecord(recordValue(value))
      if (group.length) sections.push({ title, groups: [group] })
      continue
    }
    const text = scalarText(value)
    if (text) sections.push({ title, fields: [{ label: '', value: text }] })
  }
  return sections
}

export function presentStageAttempt(attempt: PhaseAttempt): StageAttemptPresentation {
  const output = recordValue(attempt.output_json)
  if ((attempt.phase === 'validation' || attempt.phase === 'regression') && typeof output.verification_status === 'string') return presentValidation(attempt, output)
  if (attempt.phase === 'investigation' && typeof output.investigation_status === 'string') return presentInvestigation(attempt, output)
  if (attempt.phase === 'fix' && typeof output.fix_status === 'string') return presentFix(attempt, output)
  return {
    phaseLabel: phaseLabels[attempt.phase], attemptStatusLabel: attemptStatusLabels[attempt.status], resultLabel: '阶段结果', tone: attempt.status === 'failed' ? 'danger' : 'neutral',
    environment: stringValue(output.environment), startedAt: attempt.started_at, finishedAt: attempt.finished_at || '', sections: fallbackSections(output),
  }
}
```

- [ ] **Step 4: Run the presenter test and verify GREEN**

Run:

```bash
cd web
npx vitest run src/lib/incidentStageOutput.test.ts
```

Expected: all presenter tests pass.

- [ ] **Step 5: Type-check and commit the presenter**

Run:

```bash
cd web
npx vue-tsc --noEmit
cd ..
git diff --check
git add web/src/lib/incidentStageOutput.ts web/src/lib/incidentStageOutput.test.ts
git commit -m "feat: model incident stage output for users"
```

Expected: type checking exits 0; the commit contains only the presenter and its tests.

---

### Task 2: Render semantic stage cards and collapse history

**Files:**
- Create: `web/src/components/BugStageAttemptOutput.vue`
- Create: `web/src/components/BugStageAttemptOutput.test.ts`
- Modify: `web/src/components/BugCaseArtifacts.vue:1-241`
- Modify: `web/src/components/BugCaseArtifacts.test.ts:1-126`

**Interfaces:**
- Consumes: `presentStageAttempt(attempt)` and `StageSection` from Task 1.
- Produces: `BugStageAttemptOutput` props `{ attempt: PhaseAttempt; latest: boolean }` and a semantic `.stage-attempt` `<details>` root.
- `BugCaseArtifacts` supplies attempts in backend order and marks exactly the last non-legacy attempt as latest.

- [ ] **Step 1: Write failing component tests for readable structure and safety**

Create `web/src/components/BugStageAttemptOutput.test.ts`:

```ts
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { PhaseAttempt } from '../lib/bridge/bugWorkflow'
import BugStageAttemptOutput from './BugStageAttemptOutput.vue'

const validation: PhaseAttempt = {
  id: 'validation-1', case_id: 'case-1', cycle_number: 1, phase: 'validation', mode: 'reproduce', status: 'failed', agent_target: 'codex', bot_key: 'base|codex', input_json: {},
  output_json: { verification_status: 'insufficient_info', environment: 'test', expected_behavior: '显示两名用户', observed_behavior: '<img src=x onerror=alert(1)> 只显示一名用户', gaps: ['缺少测试账号'], evidence: [] },
  parent_attempt_id: '', started_at: '2026-07-14T10:00:00Z', error_code: 'needs_evidence', error_message: '证据不足', usage: {},
}

describe('BugStageAttemptOutput', () => {
  it('renders a latest result as an expanded Chinese semantic card', () => {
    const wrapper = mount(BugStageAttemptOutput, { props: { attempt: validation, latest: true } })
    expect(wrapper.get('details').attributes()).toHaveProperty('open')
    expect(wrapper.get('summary').text()).toContain('验证')
    expect(wrapper.get('summary').text()).toContain('信息不足')
    expect(wrapper.text()).toContain('预期表现')
    expect(wrapper.text()).toContain('实际观察')
    expect(wrapper.text()).toContain('还需补充')
    expect(wrapper.text()).toContain('尚无有效证据')
    expect(wrapper.text()).not.toContain('verification_status')
    expect(wrapper.text()).not.toContain('{')
    expect(wrapper.find('img').exists()).toBe(false)
  })

  it('keeps an older result collapsed and exposes its error in readable text', () => {
    const wrapper = mount(BugStageAttemptOutput, { props: { attempt: validation, latest: false } })
    expect(wrapper.get('details').attributes('open')).toBeUndefined()
    expect(wrapper.get('[data-attempt-error]').text()).toBe('证据不足')
    expect(wrapper.get('summary').attributes('aria-label')).toContain('验证')
  })
})
```

Extend `web/src/components/BugCaseArtifacts.test.ts` with a detail containing two validation attempts and assert:

```ts
it('renders current attempts as semantic history with only the latest expanded', () => {
  const first = { ...detail.attempts[0], id: 'validation-old', phase: 'validation' as const, mode: 'reproduce' as const, output_json: { verification_status: 'not_reproduced', environment: 'test', evidence: [], gaps: [] } }
  const latest = { ...first, id: 'validation-latest', status: 'failed' as const, output_json: { verification_status: 'insufficient_info', environment: 'test', expected_behavior: '显示两名用户', observed_behavior: '只显示一名用户', evidence: [], gaps: ['缺少 Network 导出'] } }
  const wrapper = mount(BugCaseArtifacts, { props: { detail: { ...detail, attempts: [first, latest] } } })
  const attempts = wrapper.findAll('.stage-attempt')
  expect(attempts).toHaveLength(2)
  expect(attempts[0].attributes('open')).toBeUndefined()
  expect(attempts[1].attributes()).toHaveProperty('open')
  expect(wrapper.text()).not.toContain('verification_status')
  expect(wrapper.find('[data-raw-output]').exists()).toBe(false)
})
```

In the existing “keeps the stage title outside a responsive keyboard-scrollable output region” test, replace the raw `.artifact-item` count with the new attempt roots:

```ts
expect(scroll.findAll('.stage-attempt, .legacy-attempt')).toHaveLength(detail.attempts.length)
```

- [ ] **Step 2: Run the component tests and verify RED**

Run:

```bash
cd web
npx vitest run src/components/BugStageAttemptOutput.test.ts src/components/BugCaseArtifacts.test.ts
```

Expected: FAIL because the semantic component does not exist and `BugCaseArtifacts` still renders `<pre>{{ displayJSON(attempt.output_json) }}</pre>`.

- [ ] **Step 3: Implement `BugStageAttemptOutput.vue`**

Create a component that computes `view = presentStageAttempt(props.attempt)` and renders this structure:

```vue
<script setup lang="ts">
import { computed } from 'vue'
import type { PhaseAttempt } from '../lib/bridge/bugWorkflow'
import { presentStageAttempt } from '../lib/incidentStageOutput'

const props = defineProps<{ attempt: PhaseAttempt; latest: boolean }>()
const view = computed(() => presentStageAttempt(props.attempt))
</script>

<template>
  <details class="stage-attempt" :class="`tone-${view.tone}`" :open="latest">
    <summary :aria-label="`${view.phaseLabel}，${view.resultLabel}，${view.attemptStatusLabel}`">
      <span class="stage-phase">{{ view.phaseLabel }}</span>
      <span class="stage-result">{{ view.resultLabel }}</span>
      <span class="stage-attempt-status">{{ view.attemptStatusLabel }}</span>
      <time v-if="view.finishedAt || view.startedAt" :datetime="view.finishedAt || view.startedAt">{{ view.finishedAt || view.startedAt }}</time>
    </summary>
    <div class="stage-result-body">
      <p v-if="view.environment" class="stage-environment">环境 <strong>{{ view.environment }}</strong></p>
      <p v-if="attempt.error_message" data-attempt-error class="stage-error" role="alert">{{ attempt.error_message }}</p>
      <p v-if="view.sections.length === 0" class="stage-empty">本次暂无可展示的阶段结果</p>
      <section v-for="section in view.sections" :key="section.title" class="stage-section" :class="section.tone ? `tone-${section.tone}` : ''">
        <h4>{{ section.title }}</h4>
        <dl v-if="section.fields?.length" class="stage-fields"><div v-for="field in section.fields" :key="`${field.label}-${field.value}`"><dt v-if="field.label">{{ field.label }}</dt><dd :class="{ mono: field.mono }">{{ field.value }}</dd></div></dl>
        <ul v-if="section.items?.length"><li v-for="item in section.items" :key="item">{{ item }}</li></ul>
        <div v-if="section.groups?.length" class="stage-groups"><dl v-for="(group, index) in section.groups" :key="index"><div v-for="field in group" :key="`${field.label}-${field.value}`"><dt>{{ field.label }}</dt><dd :class="{ mono: field.mono }">{{ field.value }}</dd></div></dl></div>
        <p v-if="section.emptyText" class="stage-empty">{{ section.emptyText }}</p>
      </section>
    </div>
  </details>
</template>

<style scoped>
.stage-attempt { min-width: 0; border: 1px solid var(--c-line); border-left-width: 4px; border-radius: var(--r-md); background: #fff; color: var(--c-text); }
.stage-attempt + .stage-attempt { margin-top: var(--sp-2); }
.stage-attempt > summary { display: flex; align-items: center; gap: 8px; min-height: 44px; padding: 8px 10px; cursor: pointer; list-style: none; color: var(--c-ink); }
.stage-attempt > summary::-webkit-details-marker { display: none; }
.stage-attempt > summary::before { content: '›'; color: var(--c-muted); font-size: 18px; transform: rotate(0deg); transition: transform 160ms ease; }
.stage-attempt[open] > summary::before { transform: rotate(90deg); }
.stage-attempt > summary:focus-visible { outline: 3px solid rgba(37, 99, 235, .55); outline-offset: 2px; border-radius: var(--r-md); }
.stage-phase { font-weight: 700; }
.stage-result { border: 1px solid var(--c-line); border-radius: 999px; padding: 2px 8px; background: var(--c-soft); font-size: var(--fs-xs); font-weight: 700; }
.stage-attempt-status, time { color: var(--c-muted); font-size: var(--fs-xs); }
time { margin-left: auto; overflow-wrap: anywhere; }
.tone-success { border-left-color: #15803d; }
.tone-warning { border-left-color: #d97706; }
.tone-danger { border-left-color: #dc2626; }
.tone-info { border-left-color: #2563eb; }
.tone-success > summary .stage-result { border-color: #bbf7d0; background: #f0fdf4; color: #166534; }
.tone-warning > summary .stage-result { border-color: #fde68a; background: #fffbeb; color: #92400e; }
.tone-danger > summary .stage-result { border-color: #fecaca; background: #fef2f2; color: #991b1b; }
.tone-info > summary .stage-result { border-color: #bfdbfe; background: #eff6ff; color: #1d4ed8; }
.stage-result-body { display: grid; gap: var(--sp-2); padding: 0 12px 12px 34px; }
.stage-environment, .stage-error, .stage-empty { margin: 0; font-size: var(--fs-sm); }
.stage-environment { color: var(--c-muted); }
.stage-environment strong { color: var(--c-ink); }
.stage-error { border-radius: var(--r-sm); padding: 8px 10px; background: #fef2f2; color: #991b1b; }
.stage-section { min-width: 0; border-top: 1px solid var(--c-line); padding-top: var(--sp-2); }
.stage-section.tone-warning { border-left: 3px solid #d97706; padding-left: 10px; }
.stage-section h4 { margin: 0 0 6px; color: var(--c-ink); font-size: var(--fs-sm); }
.stage-section ul { margin: 0; padding-left: 20px; }
.stage-section li { margin: 3px 0; line-height: 1.55; overflow-wrap: anywhere; }
.stage-fields, .stage-groups { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; margin: 0; }
.stage-fields > div, .stage-groups > dl { min-width: 0; margin: 0; border-radius: var(--r-sm); padding: 8px 10px; background: var(--c-soft); }
.stage-groups > dl { display: grid; gap: 5px; }
.stage-groups dl > div { min-width: 0; display: grid; grid-template-columns: minmax(72px, auto) minmax(0, 1fr); gap: 8px; }
dt { color: var(--c-muted); font-size: var(--fs-xs); }
dd { min-width: 0; margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.55; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; }
.stage-empty { color: var(--c-muted); }
@media (max-width: 640px) {
  .stage-attempt > summary { flex-wrap: wrap; }
  time { width: 100%; margin-left: 26px; }
  .stage-result-body { padding-left: 12px; }
  .stage-fields, .stage-groups { grid-template-columns: minmax(0, 1fr); }
}
</style>
```

Do not add a nested fixed height; scrolling remains owned by the parent.

- [ ] **Step 4: Replace raw current-attempt rendering in `BugCaseArtifacts.vue`**

Import the component and add the two computed values:

```ts
import BugStageAttemptOutput from './BugStageAttemptOutput.vue'

const currentAttempts = computed(() => props.detail.attempts.filter(item => item.phase !== 'legacy'))
const latestCurrentAttemptID = computed(() => currentAttempts.value.at(-1)?.id || '')
```

Replace the non-legacy article loop with:

```vue
<BugStageAttemptOutput
  v-for="attempt in currentAttempts"
  :key="attempt.id"
  :attempt="attempt"
  :latest="attempt.id === latestCurrentAttemptID"
/>
```

Keep `displayJSON` for code-change test evidence and keep the entire legacy projection unchanged. Add `aria-live="polite"` and `aria-relevant="additions text"` to `.attempt-output-scroll`; do not add any raw-data control.

- [ ] **Step 5: Run focused tests and verify GREEN**

Run:

```bash
cd web
npx vitest run src/lib/incidentStageOutput.test.ts src/components/BugStageAttemptOutput.test.ts src/components/BugCaseArtifacts.test.ts
```

Expected: all presenter and component tests pass, including existing legacy hostile-text and responsive-scroll tests.

- [ ] **Step 6: Type-check and commit semantic rendering**

Run:

```bash
cd web
npx vue-tsc --noEmit
cd ..
git diff --check
git add web/src/components/BugStageAttemptOutput.vue web/src/components/BugStageAttemptOutput.test.ts web/src/components/BugCaseArtifacts.vue web/src/components/BugCaseArtifacts.test.ts
git commit -m "feat: present incident stage results clearly"
```

Expected: type checking exits 0; the commit contains only the new stage component and its parent integration/tests.

---

### Task 3: Follow the newest stage output without moving the page

**Files:**
- Modify: `web/src/components/BugCaseArtifacts.vue:1-241`
- Modify: `web/src/components/BugCaseArtifacts.test.ts:1-126`

**Interfaces:**
- Consumes: `.attempt-output-scroll`, `props.detail.case.id`, and the nested `props.detail.attempts` collection.
- Produces: `followLatestStageOutput(): Promise<void>`, invoked only by a deep immediate Vue watcher after relevant reactive data changes.
- Does not expose a new component event or application state.

- [ ] **Step 1: Write failing auto-scroll tests**

Update the Vitest import to include `afterEach` and `vi`, then add:

```ts
afterEach(() => vi.restoreAllMocks())

it('scrolls the stage viewport to the bottom initially and after nested attempt updates', async () => {
  vi.spyOn(Element.prototype, 'scrollHeight', 'get').mockReturnValue(640)
  const pageScroll = vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
  const scrollIntoView = vi.spyOn(HTMLElement.prototype, 'scrollIntoView').mockImplementation(() => undefined)

  const wrapper = mount(BugCaseArtifacts, { props: { detail } })
  await wrapper.vm.$nextTick()
  await wrapper.vm.$nextTick()
  const viewport = wrapper.get<HTMLElement>('.attempt-output-scroll').element
  expect(viewport.scrollTop).toBe(640)

  viewport.scrollTop = 120
  const appended = { ...detail.attempts[0], id: 'investigate-2', status: 'failed' as const, output_json: { summary: '新的阶段结论' }, error_message: '新错误' }
  await wrapper.setProps({ detail: { ...detail, attempts: [...detail.attempts, appended] } })
  await wrapper.vm.$nextTick()
  await wrapper.vm.$nextTick()
  expect(viewport.scrollTop).toBe(640)
  expect(scrollIntoView).not.toHaveBeenCalled()
  expect(pageScroll).not.toHaveBeenCalled()
})

it('follows a switched Case and does not reset scrolling without a data change', async () => {
  vi.spyOn(Element.prototype, 'scrollHeight', 'get').mockReturnValue(480)
  const wrapper = mount(BugCaseArtifacts, { props: { detail } })
  await wrapper.vm.$nextTick()
  await wrapper.vm.$nextTick()
  const viewport = wrapper.get<HTMLElement>('.attempt-output-scroll').element

  viewport.scrollTop = 90
  await wrapper.vm.$nextTick()
  expect(viewport.scrollTop).toBe(90)

  await wrapper.setProps({ detail: { ...detail, case: { ...detail.case, id: 'case-2' } } })
  await wrapper.vm.$nextTick()
  await wrapper.vm.$nextTick()
  expect(viewport.scrollTop).toBe(480)
})
```

- [ ] **Step 2: Run the auto-scroll tests and verify RED**

Run:

```bash
cd web
npx vitest run src/components/BugCaseArtifacts.test.ts -t "scrolls the stage viewport|follows a switched Case"
```

Expected: FAIL because the stage viewport has no template ref or watcher and remains at its previous `scrollTop`.

- [ ] **Step 3: Implement scoped follow-to-bottom behavior**

Change the Vue imports to include `nextTick`, `ref`, and `watch`, then add:

```ts
const attemptOutputScroll = ref<HTMLElement | null>(null)

async function followLatestStageOutput(): Promise<void> {
  await nextTick()
  const viewport = attemptOutputScroll.value
  if (viewport) viewport.scrollTop = viewport.scrollHeight
}

watch(
  () => [props.detail.case.id, props.detail.attempts],
  followLatestStageOutput,
  { immediate: true, deep: true },
)
```

Bind the existing viewport directly:

```vue
<div ref="attemptOutputScroll" class="attempt-output-scroll" role="region" aria-label="阶段输出内容" aria-live="polite" aria-relevant="additions text" tabindex="0">
```

Do not use a timer, mutation observer, interval, `scrollIntoView`, or window scrolling. The deep watcher is the only trigger, so manual scrolling is preserved until Case/attempt data actually changes.

- [ ] **Step 4: Run focused tests and verify GREEN**

Run:

```bash
cd web
npx vitest run src/components/BugCaseArtifacts.test.ts src/components/BugStageAttemptOutput.test.ts src/lib/incidentStageOutput.test.ts
```

Expected: all stage-output tests pass, including legacy safety, responsive containment, semantic rendering, history folding, and auto-scroll.

- [ ] **Step 5: Run the complete Web verification**

Run from `web/`:

```bash
npm test
npx vue-tsc --noEmit
npm run build
```

Expected: all Vitest suites pass; TypeScript exits 0; Vite reports a successful production build.

- [ ] **Step 6: Inspect scope and commit auto-scroll**

Run:

```bash
cd ..
git diff --check
git status --short
git diff -- web/src/components/BugCaseArtifacts.vue web/src/components/BugCaseArtifacts.test.ts
```

Expected: only the two planned files are uncommitted besides the pre-existing unrelated `internal/webui/dist/.gitkeep` deletion.

Commit only the planned files:

```bash
git add web/src/components/BugCaseArtifacts.vue web/src/components/BugCaseArtifacts.test.ts
git commit -m "feat: follow the latest incident stage output"
```

- [ ] **Step 7: Verify the final commit range**

Run:

```bash
git status --short
git log --oneline -4
git diff HEAD~3..HEAD --stat
```

Expected: the three implementation commits contain only the presenter, semantic stage component, parent integration, and tests; `internal/webui/dist/.gitkeep` remains an unstaged user-owned deletion.
