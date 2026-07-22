<script setup lang="ts">
// EnvListItem —— Step 3 环境列表的单行编辑器。
// 父组件(InitPage)负责拥有 environments 数组 + URL probe 状态 + 校验错误集,
// 把单行渲染 / 探测触发委托给本组件。
//
// 跟"普通 list item"组件的区别:
//   - URL 探测态(loading / ok / fail / latency / detail / error)按 api/web 分别传入,
//     避免 reactive map 跨组件边界
//   - v-model 走 reactive object env(InitPage 里 environments 里的 row 直接传引用),
//     不拆 4 个独立 prop;改字段直接写 env.id 等,父端 reactivity 走 Vue 自动追踪
//
// emits:
//   - probe(kind, url) —— 用户在某字段输入时触发 800ms 防抖探测,父端接住
//   - remove() —— 用户点 × 删除本行,父端按 index 删

import type { URLProbeState } from '../lib/probeTypes'
import URLProbeBadge from './URLProbeBadge.vue'
import { createFrontendEntry, FRONTEND_KIND_SUGGESTIONS } from '../lib/frontendEntries'

interface FrontendEntryItem {
  id: string; name: string; url: string; repo: string; device_profile: string
  aliases: string; product_hints: string; module_hints: string; path_prefixes: string
}

const props = defineProps<{
  /** 本行的环境对象;reactive 直引,改字段不必 emit */
  env: { id: string; api_domain: string; web_domain: string; frontend_entries: FrontendEntryItem[]; is_prod: boolean }
  /** API 域名探测态;undefined / status='idle' 时不显示 badge */
  apiProbe: URLProbeState | undefined
  /** env.id 字段是否有校验错(父端从 validationErrors 集合算) */
  hasIdError: boolean
  /** env.api_domain 字段是否有校验错 */
  hasApiError: boolean
  /** 删除按钮是否 disabled(父端根据 environments.length 算,只剩 1 行时 disabled) */
  disableRemove: boolean
  /** 简化前端入口的字段级校验 */
  hasEntryError: (entryIndex: number, field: 'name' | 'url') => boolean
}>()

const emit = defineEmits<{
  probe: [kind: 'api' | 'web', url: string]
  remove: []
}>()

if (!Array.isArray(props.env.frontend_entries)) props.env.frontend_entries = []

function addFrontendEntry() {
  props.env.frontend_entries.push(createFrontendEntry())
}

function removeFrontendEntry(index: number) {
  props.env.frontend_entries.splice(index, 1)
}

function isSuggestedFrontendKind(name: string): boolean {
  return (FRONTEND_KIND_SUGGESTIONS as readonly string[]).includes(name)
}
</script>

<template>
  <article class="dynamic-row environment-card" data-test="environment-card">
    <div class="row-fields environment-fields">
      <div class="form-group compact environment-id-field">
        <label>环境 ID
          <span class="help-icon" title="环境短标识(dev/test/staging/prod)。每个 env 会注册一套独立的 MCP 实例:nacos-mcp-server-<ID>、grafana-mcp-server-<ID> 等。">?</span>
        </label>
        <input
          v-model="env.id"
          type="text"
          placeholder="dev"
          :class="{ error: hasIdError }"
        />
      </div>
      <div class="form-group compact environment-domain-field">
        <label>API 域名
          <span class="help-icon" title="后端接口域名,机器人做接口实测 / 日志查询时拼 URL 用。建议带 http/https 前缀明确协议(内部 dev 常 http,公网 prod 多 https);不带也行,下游按 https 兜底。">?</span>
          <URLProbeBadge :state="apiProbe" />
        </label>
        <input
          v-model="env.api_domain"
          type="text"
          placeholder="https://api-dev.example.com"
          :class="{ error: hasApiError }"
          @input="emit('probe', 'api', env.api_domain)"
        />
      </div>
      <div class="form-group compact checkbox-group environment-production-field">
        <label title="is_prod=true 时机器人更保守:执行写入/重启类动作前会二次确认;OpenClaw 客户端 UI 也会标红。">
          <input type="checkbox" v-model="env.is_prod" />
          生产环境
          <span class="help-icon">?</span>
        </label>
      </div>
      <button
        class="btn-icon remove environment-remove"
        type="button"
        @click="emit('remove')"
        :disabled="disableRemove"
        :aria-label="`删除 ${env.id || '当前'} 环境`"
        title="删除环境"
      >
        <svg viewBox="0 0 24 24" aria-hidden="true">
          <path d="M6 6l12 12M18 6L6 18" />
        </svg>
      </button>
    </div>
    <details class="frontend-entries">
      <summary>前端应用入口 <span class="auto-tag">{{ env.frontend_entries.length || '未配置' }}</span></summary>
      <p class="entry-help">每个前端只需选择应用类型并填写入口 URL。仓库在下一步添加完成后再关联；稳定标识和设备类型由 Studio 自动处理。</p>
      <div v-for="(entry, entryIndex) in env.frontend_entries" :key="entryIndex" class="frontend-entry-card">
        <div class="frontend-entry-header">
          <span class="entry-index" aria-hidden="true">{{ entryIndex + 1 }}</span>
          <div class="entry-title">
            <strong>前端应用 {{ entryIndex + 1 }}</strong>
            <span>{{ entry.name || '请选择应用类型' }}</span>
          </div>
          <button
            class="entry-remove"
            type="button"
            @click="removeFrontendEntry(entryIndex)"
            :aria-label="`删除前端应用 ${entryIndex + 1}`"
            title="删除前端应用"
          >
            <svg viewBox="0 0 24 24" aria-hidden="true">
              <path d="M6 6l12 12M18 6L6 18" />
            </svg>
          </button>
        </div>
        <div class="entry-grid entry-grid-simple">
          <label class="entry-field" :for="`frontend-kind-${env.id || 'env'}-${entryIndex}`">
            <span class="entry-field-label">应用类型</span>
            <span class="entry-control select-control" :class="{ error: hasEntryError(entryIndex, 'name') }">
              <svg class="control-leading-icon" viewBox="0 0 24 24" aria-hidden="true">
                <rect x="3" y="4" width="18" height="16" rx="2" />
                <path d="M3 9h18M8 9v11" />
              </svg>
              <select
                :id="`frontend-kind-${env.id || 'env'}-${entryIndex}`"
                v-model="entry.name"
                class="frontend-kind-select"
              >
                <option value="" disabled>请选择这是哪个端</option>
                <option
                  v-if="entry.name && !isSuggestedFrontendKind(entry.name)"
                  :value="entry.name"
                >{{ entry.name }}（已导入）</option>
                <option v-for="kind in FRONTEND_KIND_SUGGESTIONS" :key="kind" :value="kind">{{ kind }}</option>
              </select>
              <svg class="select-chevron" viewBox="0 0 24 24" aria-hidden="true">
                <path d="m7 10 5 5 5-5" />
              </svg>
            </span>
          </label>
          <label class="entry-field" :for="`frontend-url-${env.id || 'env'}-${entryIndex}`">
            <span class="entry-field-label">Web 入口 URL</span>
            <span class="entry-control url-control" :class="{ error: hasEntryError(entryIndex, 'url') }">
              <svg class="control-leading-icon" viewBox="0 0 24 24" aria-hidden="true">
                <circle cx="12" cy="12" r="9" />
                <path d="M3 12h18M12 3c2.4 2.5 3.6 5.5 3.6 9S14.4 18.5 12 21M12 3C9.6 5.5 8.4 8.5 8.4 12s1.2 6.5 3.6 9" />
              </svg>
              <input
                :id="`frontend-url-${env.id || 'env'}-${entryIndex}`"
                v-model.trim="entry.url"
                type="url"
                inputmode="url"
                autocomplete="url"
                placeholder="https://admin-test.example.com"
                @input="emit('probe', 'web', entry.url)"
              />
            </span>
          </label>
        </div>
      </div>
      <button class="btn add-entry" type="button" @click="addFrontendEntry">
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M5 12h14" /></svg>
        添加前端应用入口
      </button>
    </details>
  </article>
</template>

<style scoped>
.environment-card {
  margin: 14px 0;
  padding: 16px;
  border: 1px solid var(--c-line);
  border-radius: 10px;
  background: var(--c-surf);
}
.environment-card:last-of-type { border-bottom: 1px solid var(--c-line); }
.environment-fields {
  display: grid;
  grid-template-columns: minmax(108px, 0.55fr) minmax(300px, 2fr) max-content 44px;
  gap: 14px;
  align-items: end;
}
.environment-fields > * { min-width: 0; }
.environment-id-field,
.environment-domain-field { width: 100%; }
.environment-production-field {
  min-height: 38px;
  padding: 0 10px;
  border: 1px solid var(--c-line);
  border-radius: var(--r-md);
  background: var(--c-surf-2);
}
.frontend-entries { margin-top: 14px; border-top: 1px solid var(--c-line); padding-top: 13px; }
.frontend-entries summary {
  width: fit-content;
  cursor: pointer;
  font-weight: 650;
  color: var(--c-ink);
  user-select: none;
}
.entry-help { margin: 9px 0 14px; color: var(--c-muted); font-size: 13px; line-height: 1.55; }
.frontend-entry-card {
  margin: 10px 0 12px;
  padding: 0;
  overflow: hidden;
  border: 1px solid var(--c-line);
  border-radius: 10px;
  background: var(--c-surf);
  box-shadow: 0 1px 2px rgba(15, 23, 42, 0.04);
  transition: border-color 180ms ease, box-shadow 180ms ease;
}
.frontend-entry-card:focus-within {
  border-color: #bfdbfe;
  box-shadow: 0 4px 14px rgba(37, 99, 235, 0.08);
}
.frontend-entry-header {
  display: flex;
  align-items: center;
  gap: 10px;
  min-height: 54px;
  padding: 8px 10px 8px 14px;
  border-bottom: 1px solid var(--c-line);
  background: linear-gradient(180deg, #fff 0%, var(--c-surf-2) 100%);
}
.entry-index {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  flex: none;
  border-radius: 8px;
  color: #1d4ed8;
  background: #eff6ff;
  font-size: 12px;
  font-weight: 700;
}
.entry-title { min-width: 0; display: flex; align-items: baseline; gap: 8px; }
.entry-title strong { color: var(--c-ink); font-size: 13px; }
.entry-title span { overflow: hidden; color: var(--c-muted); font-size: 12px; text-overflow: ellipsis; white-space: nowrap; }
.entry-remove {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 40px;
  height: 40px;
  margin-left: auto;
  flex: none;
  border: 1px solid transparent;
  border-radius: 8px;
  color: #dc2626;
  background: transparent;
  cursor: pointer;
  transition: color 180ms ease, background 180ms ease, border-color 180ms ease;
}
.entry-remove:hover { border-color: #fecaca; background: var(--c-danger-bg); color: var(--c-danger); }
.entry-remove:focus-visible { outline: 2px solid var(--c-accent); outline-offset: 2px; }
.entry-remove svg { width: 18px; height: 18px; fill: none; stroke: currentColor; stroke-width: 2; stroke-linecap: round; }
.entry-grid { display: grid; gap: 18px; align-items: end; padding: 14px; }
.entry-grid-simple { grid-template-columns: minmax(210px, 0.72fr) minmax(340px, 1.7fr); }
.entry-field { display: grid; min-width: 0; gap: 7px; }
.entry-field-label { color: var(--c-text); font-size: 12px; font-weight: 600; }
.entry-control {
  position: relative;
  display: flex;
  align-items: center;
  min-width: 0;
  height: 42px;
  border: 1px solid var(--c-line-2);
  border-radius: 8px;
  color: var(--c-text);
  background: var(--c-surf);
  transition: border-color 180ms ease, box-shadow 180ms ease, background 180ms ease;
}
.entry-control:hover { border-color: #94a3b8; }
.entry-control:focus-within { border-color: var(--c-accent); box-shadow: 0 0 0 3px rgba(59, 130, 246, 0.12); }
.entry-control.error { border-color: #ef4444; background: #fffafa; }
.control-leading-icon {
  width: 18px;
  height: 18px;
  margin-left: 12px;
  flex: none;
  fill: none;
  stroke: var(--c-muted);
  stroke-width: 1.7;
  stroke-linecap: round;
  stroke-linejoin: round;
  pointer-events: none;
}
.entry-control select,
.entry-control input {
  width: 100%;
  height: 40px;
  min-width: 0;
  margin: 0;
  padding: 0 12px 0 10px;
  border: 0;
  border-radius: 8px;
  outline: 0;
  color: var(--c-ink);
  background: transparent;
  box-shadow: none;
  font-size: 13px;
}
.entry-control select:focus,
.entry-control input:focus { border: 0; outline: 0; box-shadow: none; }
.frontend-kind-select { appearance: none; cursor: pointer; padding-right: 38px !important; }
.select-chevron {
  position: absolute;
  right: 12px;
  width: 17px;
  height: 17px;
  fill: none;
  stroke: var(--c-muted);
  stroke-width: 2;
  stroke-linecap: round;
  stroke-linejoin: round;
  pointer-events: none;
}
.add-entry {
  min-height: 40px;
  margin-top: 2px;
  border-style: dashed;
  color: #1d4ed8;
  background: #f8fbff;
}
.add-entry:hover:not(:disabled) { border-color: #93c5fd; background: #eff6ff; }
.add-entry:focus-visible { outline: 2px solid var(--c-accent); outline-offset: 2px; }
.add-entry svg { width: 16px; height: 16px; fill: none; stroke: currentColor; stroke-width: 2; stroke-linecap: round; }
.environment-remove {
  width: 38px;
  height: 38px;
  margin: 0;
}
.environment-remove svg {
  width: 18px;
  height: 18px;
  fill: none;
  stroke: currentColor;
  stroke-width: 2;
  stroke-linecap: round;
}
@media (max-width: 1100px) {
  .environment-fields {
    grid-template-columns: minmax(100px, 0.6fr) minmax(260px, 1.4fr) 44px;
  }
  .environment-production-field {
    grid-column: 1 / -2;
    width: fit-content;
  }
  .environment-remove { grid-column: -2 / -1; }
}

@media (max-width: 760px) {
  .environment-card { padding: 12px; }
  .environment-fields { grid-template-columns: 1fr 44px; }
  .environment-domain-field,
  .environment-production-field { grid-column: 1 / -1; }
  .environment-id-field { grid-column: 1; }
  .environment-remove { grid-column: 2; grid-row: 1; justify-self: end; }
  .entry-grid { gap: 13px; padding: 12px; }
  .entry-grid-simple { grid-template-columns: 1fr; }
  .entry-title { display: grid; gap: 1px; }
  .entry-control { height: 44px; }
  .entry-control select,
  .entry-control input { height: 42px; font-size: 16px; }
  .add-entry { width: 100%; justify-content: center; min-height: 44px; }
}

@media (prefers-reduced-motion: reduce) {
  .frontend-entry-card,
  .entry-remove,
  .entry-control { transition: none; }
}
</style>
