# Incident Timeline Collapse Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep the incident Case timeline compact by showing the newest three events by default and placing the complete history in a bounded, independently scrollable expanded view.

**Architecture:** Keep the behavior local to `BugCaseLifecycle.vue`: derive newest-first and visible event lists from props, maintain one non-persisted expansion flag, and reset it only when the selected Case changes or the list no longer needs expansion. Cover the interaction and CSS contracts in the existing component test so no backend, workflow, or shared-state changes are required.

**Tech Stack:** Vue 3 Composition API, TypeScript, Vue Test Utils, Vitest, scoped CSS

## Global Constraints

- Default to the newest 3 events and keep newest-first ordering.
- Display `过程时间线 · 共 N 条`; hide the toggle when `N <= 3`.
- Use the explicit toggle copy `展开全部` and `收起` with a directional arrow.
- Expanded history must use `max-height: clamp(280px, 38vh, 520px)`, `overflow-y: auto`, `overscroll-behavior: contain`, and `scrollbar-gutter: stable`.
- Use a real button with a minimum 44×44px target, `aria-expanded`, `aria-controls="case-timeline-events"`, and a visible `:focus-visible` state.
- Reset to collapsed when `detail.case.id` changes; preserve the current choice when the same Case receives new events.
- Keep the title controls and event content free of horizontal overflow at 375px, 768px, 1024px, and 1440px viewports.
- Do not persist expansion state or change event data, event labels, event ordering truth, workflow state, or backend APIs.
- Preserve the user-owned deletion of `internal/webui/dist/.gitkeep`; never stage or restore it during this task.

---

## File Map

- Modify `web/src/components/BugCaseLifecycle.test.ts`: add typed timeline fixtures and interaction/style-contract coverage.
- Modify `web/src/components/BugCaseLifecycle.vue`: add derived event lists, local expansion state, Case-change reset, accessible controls, and bounded-scroll styling.
- No new runtime files, dependencies, backend handlers, or persistent state.

### Task 1: Add the compact, expandable Case timeline

**Files:**
- Modify: `web/src/components/BugCaseLifecycle.test.ts`
- Modify: `web/src/components/BugCaseLifecycle.vue`

**Interfaces:**
- Consumes: `IncidentCaseDetail.events: TransitionEvent[]` and `IncidentCaseDetail.case.id: string`.
- Produces: local `timelineExpanded: Ref<boolean>`, `timelineEvents: ComputedRef<TransitionEvent[]>`, `timelineCanExpand: ComputedRef<boolean>`, and `visibleTimelineEvents: ComputedRef<TransitionEvent[]>`.
- Produces DOM contract: `#case-timeline-events`, `.timeline-toggle`, `.timeline-events.is-expanded`, `aria-expanded`, and `aria-controls="case-timeline-events"`.

- [ ] **Step 1: Add typed timeline fixtures and failing interaction tests**

Update the type import in `web/src/components/BugCaseLifecycle.test.ts`:

```ts
import type { CaseStatus, IncidentCase, IncidentCaseDetail, TransitionEvent } from '../lib/bridge/bugWorkflow'
```

Add this helper below `detail`:

```ts
function timelineEvents(count: number, caseID = 'case-1'): TransitionEvent[] {
  return Array.from({ length: count }, (_, index) => ({
    id: `${caseID}-event-${index + 1}`,
    case_id: caseID,
    from_status: 'validating',
    to_status: 'waiting_evidence',
    event_type: `event_${index + 1}`,
    actor_type: 'agent',
    actor_id: 'validator',
    idempotency_key: `${caseID}-event-${index + 1}`,
    payload_json: {},
    created_at: `2026-07-11T${String(index + 10).padStart(2, '0')}:00:00Z`,
  }))
}
```

Add these tests inside the existing top-level `describe('BugCaseLifecycle', callback)` block:

```ts
it('previews the newest three timeline events and expands or collapses the full history', async () => {
  const snapshot = detail('waiting_evidence')
  snapshot.events = timelineEvents(6)
  const wrapper = mount(BugCaseLifecycle, { props: { cases: [snapshot.case], detail: snapshot } })

  expect(wrapper.find('.timeline-heading').text()).toContain('共 6 条')
  expect(wrapper.findAll('#case-timeline-events li').map(item => item.find('strong').text())).toEqual([
    'event_6', 'event_5', 'event_4',
  ])

  const toggle = wrapper.get<HTMLButtonElement>('.timeline-toggle')
  expect(toggle.text()).toContain('展开全部')
  expect(toggle.attributes('aria-expanded')).toBe('false')
  expect(toggle.attributes('aria-controls')).toBe('case-timeline-events')

  await toggle.trigger('click')
  expect(wrapper.findAll('#case-timeline-events li')).toHaveLength(6)
  expect(wrapper.get('.timeline-toggle').text()).toContain('收起')
  expect(wrapper.get('.timeline-toggle').attributes('aria-expanded')).toBe('true')
  expect(wrapper.get('#case-timeline-events').classes()).toContain('is-expanded')

  await wrapper.get('.timeline-toggle').trigger('click')
  expect(wrapper.findAll('#case-timeline-events li').map(item => item.find('strong').text())).toEqual([
    'event_6', 'event_5', 'event_4',
  ])
  expect(wrapper.get('.timeline-toggle').attributes('aria-expanded')).toBe('false')
})

it.each([1, 3])('does not show a timeline toggle for %i events', count => {
  const snapshot = detail('waiting_evidence')
  snapshot.events = timelineEvents(count)
  const wrapper = mount(BugCaseLifecycle, { props: { cases: [snapshot.case], detail: snapshot } })

  expect(wrapper.find('.timeline-heading').text()).toContain(`共 ${count} 条`)
  expect(wrapper.find('.timeline-toggle').exists()).toBe(false)
  expect(wrapper.findAll('#case-timeline-events li')).toHaveLength(count)
})

it('keeps the timeline empty state without an unnecessary toggle', () => {
  const snapshot = detail('waiting_evidence')
  snapshot.events = []
  const wrapper = mount(BugCaseLifecycle, { props: { cases: [snapshot.case], detail: snapshot } })

  expect(wrapper.find('.timeline-heading').text()).toContain('共 0 条')
  expect(wrapper.find('.timeline-toggle').exists()).toBe(false)
  expect(wrapper.find('#case-timeline-events').exists()).toBe(false)
  expect(wrapper.find('.timeline .empty-state').text()).toBe('暂无状态事件')
})

it('preserves expansion for same-Case updates and collapses when the Case changes', async () => {
  const caseA = detail('waiting_evidence')
  caseA.events = timelineEvents(6, 'case-1')
  const wrapper = mount(BugCaseLifecycle, { props: { cases: [caseA.case], detail: caseA } })

  await wrapper.get('.timeline-toggle').trigger('click')
  const updatedCaseA = { ...caseA, events: timelineEvents(7, 'case-1') }
  await wrapper.setProps({ detail: updatedCaseA })
  expect(wrapper.get('.timeline-toggle').attributes('aria-expanded')).toBe('true')
  expect(wrapper.findAll('#case-timeline-events li')).toHaveLength(7)

  const caseB = { ...detail('waiting_evidence'), case: incident('waiting_evidence', 'case-2'), events: timelineEvents(6, 'case-2') }
  await wrapper.setProps({ cases: [caseB.case], detail: caseB })
  expect(wrapper.get('.timeline-toggle').attributes('aria-expanded')).toBe('false')
  expect(wrapper.findAll('#case-timeline-events li').map(item => item.find('strong').text())).toEqual([
    'event_6', 'event_5', 'event_4',
  ])
})

it('clears stale expansion when the event count no longer needs a toggle', async () => {
  const snapshot = detail('waiting_evidence')
  snapshot.events = timelineEvents(6)
  const wrapper = mount(BugCaseLifecycle, { props: { cases: [snapshot.case], detail: snapshot } })

  await wrapper.get('.timeline-toggle').trigger('click')
  await wrapper.setProps({ detail: { ...snapshot, events: timelineEvents(3) } })
  expect(wrapper.find('.timeline-toggle').exists()).toBe(false)
  expect(wrapper.findAll('#case-timeline-events li')).toHaveLength(3)

  await wrapper.setProps({ detail: { ...snapshot, events: timelineEvents(6) } })
  expect(wrapper.get('.timeline-toggle').attributes('aria-expanded')).toBe('false')
  expect(wrapper.findAll('#case-timeline-events li').map(item => item.find('strong').text())).toEqual([
    'event_6', 'event_5', 'event_4',
  ])
})

it('contains bounded timeline scrolling and accessible toggle style contracts', () => {
  const source = readFileSync('src/components/BugCaseLifecycle.vue', 'utf8')

  expect(source).toMatch(/\.timeline-events\.is-expanded \{[^}]*max-height: clamp\(280px, 38vh, 520px\);/)
  expect(source).toMatch(/\.timeline-events\.is-expanded \{[^}]*overflow-y: auto;/)
  expect(source).toMatch(/\.timeline-events\.is-expanded \{[^}]*overflow-x: hidden;/)
  expect(source).toMatch(/\.timeline-events\.is-expanded \{[^}]*overscroll-behavior: contain;/)
  expect(source).toMatch(/\.timeline-events\.is-expanded \{[^}]*scrollbar-gutter: stable;/)
  expect(source).toMatch(/\.timeline-heading \{[^}]*flex-wrap: wrap;/)
  expect(source).toMatch(/\.timeline-toggle \{[^}]*min-width: 44px;[^}]*min-height: 44px;/)
  expect(source).toMatch(/\.timeline-toggle:focus-visible \{[^}]*outline:/)
  expect(source).toMatch(/\.timeline-toggle-icon \{[^}]*transition: transform 180ms ease;/)
  expect(source).toMatch(/@media \(prefers-reduced-motion: reduce\)/)
})
```

- [ ] **Step 2: Run the focused test and confirm the new expectations fail**

Run:

```bash
cd web && npx vitest run src/components/BugCaseLifecycle.test.ts
```

Expected: FAIL because `.timeline-heading`, `.timeline-toggle`, derived three-event preview, expansion state, and bounded-scroll CSS do not exist yet. Existing component tests should continue reaching their assertions.

- [ ] **Step 3: Add timeline state and derived event lists**

Change the Vue import in `web/src/components/BugCaseLifecycle.vue`:

```ts
import { computed, nextTick, ref, watch } from 'vue'
```

Immediately after the existing `currentCase` computed value, add:

```ts
const TIMELINE_PREVIEW_COUNT = 3
const timelineExpanded = ref(false)
const timelineEvents = computed(() => [...(props.detail?.events ?? [])].reverse())
const timelineCanExpand = computed(() => timelineEvents.value.length > TIMELINE_PREVIEW_COUNT)
const visibleTimelineEvents = computed(() => {
  if (timelineExpanded.value && timelineCanExpand.value) return timelineEvents.value
  return timelineEvents.value.slice(0, TIMELINE_PREVIEW_COUNT)
})

watch(() => props.detail?.case.id, () => {
  timelineExpanded.value = false
})

watch(() => props.detail?.events.length ?? 0, count => {
  if (count <= TIMELINE_PREVIEW_COUNT) timelineExpanded.value = false
})
```

This keeps same-Case updates expanded while ensuring a Case switch or a list with at most three events cannot retain a stale expanded state.

- [ ] **Step 4: Replace the timeline template with the accessible disclosure control**

Replace the existing `<section class="timeline">` block in `web/src/components/BugCaseLifecycle.vue` with:

```vue
<section class="timeline" aria-labelledby="timeline-title">
  <header class="timeline-heading">
    <div>
      <h3 id="timeline-title">过程时间线</h3>
      <span aria-label="时间线事件总数">· 共 {{ detail.events.length }} 条</span>
    </div>
    <button
      v-if="timelineCanExpand"
      class="timeline-toggle"
      type="button"
      :aria-expanded="timelineExpanded"
      aria-controls="case-timeline-events"
      @click="timelineExpanded = !timelineExpanded"
    >
      <span>{{ timelineExpanded ? '收起' : '展开全部' }}</span>
      <svg class="timeline-toggle-icon" :class="{ 'is-expanded': timelineExpanded }" viewBox="0 0 20 20" aria-hidden="true">
        <path d="m5 7.5 5 5 5-5" fill="none" stroke="currentColor" stroke-linecap="round" stroke-linejoin="round" stroke-width="1.8" />
      </svg>
    </button>
  </header>
  <ol
    v-if="detail.events.length > 0"
    id="case-timeline-events"
    class="timeline-events"
    :class="{ 'is-expanded': timelineExpanded && timelineCanExpand }"
    aria-label="Case 时间线"
  >
    <li v-for="event in visibleTimelineEvents" :key="event.id">
      <span class="timeline-dot" aria-hidden="true"></span>
      <div><strong>{{ event.event_type }}</strong><span>{{ statusLabel(event.from_status) }} → {{ statusLabel(event.to_status) }}</span><small>{{ fmtTime(event.created_at) }} · {{ event.actor_type }}</small></div>
    </li>
  </ol>
  <p v-else class="empty-state">暂无状态事件</p>
</section>
```

- [ ] **Step 5: Add compact header, toggle, and bounded-scroll styles**

Replace the current timeline heading/list rules in `web/src/components/BugCaseLifecycle.vue` with:

```css
.timeline-heading { min-width: 0; display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: var(--sp-2); margin-bottom: var(--sp-3); }
.timeline-heading > div { min-width: 0; display: flex; align-items: baseline; flex-wrap: wrap; gap: 4px; }
.timeline-heading h3 { margin: 0; color: var(--c-ink); font-size: var(--fs-base); }
.timeline-heading span { color: var(--c-muted); font-size: var(--fs-xs); }
.timeline-toggle { min-width: 44px; min-height: 44px; display: inline-flex; align-items: center; justify-content: center; gap: 6px; padding: 8px 10px; border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf); color: var(--c-text); font: inherit; font-size: var(--fs-sm); font-weight: 600; cursor: pointer; }
.timeline-toggle:hover { border-color: #93c5fd; background: #eff6ff; color: #1d4ed8; }
.timeline-toggle:focus-visible { outline: 3px solid rgba(37, 99, 235, .55); outline-offset: 2px; }
.timeline-toggle-icon { width: 16px; height: 16px; flex: 0 0 auto; transition: transform 180ms ease; }
.timeline-toggle-icon.is-expanded { transform: rotate(180deg); }
.timeline-events { margin: 0; padding: 0; list-style: none; }
.timeline-events.is-expanded { max-height: clamp(280px, 38vh, 520px); padding-right: var(--sp-1); overflow-x: hidden; overflow-y: auto; overscroll-behavior: contain; scrollbar-gutter: stable; }
```

Keep the existing `.timeline li`, `.timeline-dot`, `.timeline li > div`, and `.timeline strong` rules. Narrow the existing broad descendant rule so it styles only event metadata and cannot override the new toggle text:

```css
.timeline li span, .timeline li small { color: var(--c-muted); font-size: var(--fs-xs); }
```

The existing reduced-motion media rule reduces the arrow transition to `.01ms`; no second motion query is needed.

- [ ] **Step 6: Run focused tests and correct only failures within the approved timeline scope**

Run:

```bash
cd web && npx vitest run src/components/BugCaseLifecycle.test.ts
```

Expected: PASS, including the existing lifecycle and dialog behavior tests plus the new preview, toggle, Case reset, empty-state, and CSS-contract tests.

- [ ] **Step 7: Run the complete Web verification suite**

Run:

```bash
cd web
npm test
npx vue-tsc --noEmit
npm run build
```

Expected: all Vitest suites pass, TypeScript reports no errors, and the production build completes successfully. After the build, verify `git status --short` still shows the pre-existing ` D internal/webui/dist/.gitkeep` separately from the two intended source changes; do not stage it.

- [ ] **Step 8: Review the final diff and commit only the component and its test**

Run:

```bash
git diff --check
git diff -- web/src/components/BugCaseLifecycle.vue web/src/components/BugCaseLifecycle.test.ts
git status --short
git add web/src/components/BugCaseLifecycle.vue web/src/components/BugCaseLifecycle.test.ts
git diff --cached --check
git commit -m "feat: collapse incident case timeline"
```

Expected: the staged diff contains only the Vue component and its test; `internal/webui/dist/.gitkeep` remains an unstaged user-owned deletion.
