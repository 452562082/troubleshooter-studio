<script lang="ts">
let bugBotPickerSequence = 0
</script>

<script setup lang="ts">
import type { BotMatch } from '../lib/bridge/bugs'

defineProps<{ matches: BotMatch[]; selectedKey: string; loading?: boolean }>()
const emit = defineEmits<{ select: [key: string] }>()

const pickerInstanceID = `bug-bot-picker-${++bugBotPickerSequence}`
const groupName = `${pickerInstanceID}-group`
</script>

<template>
  <fieldset class="bot-picker" :disabled="loading" :aria-labelledby="`${pickerInstanceID}-title`">
    <legend :id="`${pickerInstanceID}-title`">选择排障机器人</legend>
    <p v-if="loading" class="empty-state" role="status" aria-live="polite">匹配中...</p>
    <p v-else-if="matches.length === 0" class="empty-state" role="status">暂无匹配的排障机器人</p>
    <div v-if="matches.length" class="bot-options">
      <label
        v-for="match in matches"
        :key="match.bot.key"
        class="bot-option"
        :class="{ selected: selectedKey === match.bot.key }"
      >
        <input
          type="radio"
          :name="groupName"
          :value="match.bot.key"
          :checked="selectedKey === match.bot.key"
          :disabled="loading"
          @change="emit('select', match.bot.key)"
        >
        <span class="bot-copy">
          <span class="bot-heading">
            <strong>{{ match.bot.name || match.bot.system_id || match.bot.path }}</strong>
            <em>{{ match.bot.target }}</em>
          </span>
          <small v-if="match.bot.env">环境 {{ match.bot.env }}</small>
          <small v-if="match.reasons.length">{{ match.reasons.join(' · ') }}</small>
        </span>
        <span v-if="selectedKey === match.bot.key" class="selection-copy">已选择</span>
      </label>
    </div>
  </fieldset>
</template>

<style scoped>
.bot-picker { min-width: 0; margin: 0; padding: 0; border: 0; color: var(--c-text); }
.bot-picker legend { width: 100%; margin-bottom: var(--sp-2); color: var(--c-ink); font-size: var(--fs-base); font-weight: 700; }
.bot-options { display: grid; gap: var(--sp-2); min-width: 0; }
.bot-option {
  position: relative; min-width: 0; min-height: 44px; padding: 10px 68px 10px 10px;
  display: grid; grid-template-columns: auto minmax(0, 1fr); align-items: start; gap: var(--sp-2);
  border: 1px solid var(--c-line); border-radius: var(--r-md); background: var(--c-surf);
  cursor: pointer; transition: border-color .16s ease, background .16s ease;
}
.bot-option:hover { border-color: var(--c-line-2); background: var(--c-surf-2); }
.bot-option:focus-within { outline: 2px solid var(--c-accent-hover); outline-offset: 2px; }
.bot-option.selected { border-color: var(--c-accent); background: #eff6ff; }
.bot-option input { margin: 2px 0 0; accent-color: var(--c-accent-hover); }
.bot-copy { min-width: 0; display: grid; gap: var(--sp-1); }
.bot-heading { min-width: 0; display: flex; flex-wrap: wrap; align-items: baseline; gap: 6px; }
.bot-heading strong { color: var(--c-ink); font-size: var(--fs-base); overflow-wrap: anywhere; }
.bot-heading em { color: var(--c-accent-hover); font-size: var(--fs-xs); font-style: normal; }
.bot-copy small { color: var(--c-muted); font-size: var(--fs-xs); overflow-wrap: anywhere; }
.selection-copy { position: absolute; top: 10px; right: 10px; color: #1d4ed8; font-size: var(--fs-xs); font-weight: 700; }
.empty-state { min-height: 44px; margin: 0; display: grid; place-items: center; color: var(--c-muted); font-size: var(--fs-sm); text-align: center; }
.bot-picker:disabled, .bot-picker[disabled] { opacity: .65; }
@media (prefers-reduced-motion: reduce) { .bot-option { transition: none; } }
</style>
