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
      <section v-for="(section, sectionIndex) in view.sections" :key="`${sectionIndex}-${section.title}`" class="stage-section" :class="section.tone ? `tone-${section.tone}` : ''">
        <h4>{{ section.title }}</h4>
        <p v-if="section.text" class="stage-text">{{ section.text }}</p>
        <dl v-if="section.fields?.length" class="stage-fields"><div v-for="(field, fieldIndex) in section.fields" :key="fieldIndex"><dt>{{ field.label }}</dt><dd :class="{ mono: field.mono }">{{ field.value }}</dd></div></dl>
        <ul v-if="section.items?.length"><li v-for="(item, itemIndex) in section.items" :key="itemIndex">{{ item }}</li></ul>
        <div v-if="section.groups?.length" class="stage-groups"><dl v-for="(group, index) in section.groups" :key="index"><div v-for="(field, fieldIndex) in group" :key="fieldIndex"><dt>{{ field.label }}</dt><dd :class="{ mono: field.mono }">{{ field.value }}</dd></div></dl></div>
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
.stage-text { margin: 0; white-space: pre-wrap; overflow-wrap: anywhere; line-height: 1.55; }
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
