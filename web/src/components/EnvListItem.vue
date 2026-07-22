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

interface FrontendEntryItem {
  id: string; name: string; url: string; repo: string; device_profile: string
  aliases: string; product_hints: string; module_hints: string; path_prefixes: string
}

const props = defineProps<{
  /** 本行的环境对象;reactive 直引,改字段不必 emit */
  env: { id: string; api_domain: string; web_domain: string; frontend_entries: FrontendEntryItem[]; is_prod: boolean }
  /** API 域名探测态;undefined / status='idle' 时不显示 badge */
  apiProbe: URLProbeState | undefined
  /** Web 域名探测态 */
  webProbe: URLProbeState | undefined
  /** env.id 字段是否有校验错(父端从 validationErrors 集合算) */
  hasIdError: boolean
  /** env.api_domain 字段是否有校验错 */
  hasApiError: boolean
  /** 删除按钮是否 disabled(父端根据 environments.length 算,只剩 1 行时 disabled) */
  disableRemove: boolean
}>()

const emit = defineEmits<{
  probe: [kind: 'api' | 'web', url: string]
  remove: []
}>()

if (!Array.isArray(props.env.frontend_entries)) props.env.frontend_entries = []

function addFrontendEntry() {
  props.env.frontend_entries.push({ id: '', name: '', url: '', repo: '', device_profile: 'desktop', aliases: '', product_hints: '', module_hints: '', path_prefixes: '' })
}

function removeFrontendEntry(index: number) {
  props.env.frontend_entries.splice(index, 1)
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
      <div class="form-group compact environment-domain-field">
        <label>Web 域名
          <span class="auto-tag">选填</span>
          <span class="help-icon" title="前端入口域名(管理后台 / 用户站)。机器人排障时知道 '用户在哪个 URL 看到 bug' vs '后端哪个接口报错'。单域名系统留空即可。建议带 http/https 前缀。">?</span>
          <URLProbeBadge :state="webProbe" />
        </label>
        <input
          v-model="env.web_domain"
          type="text"
          placeholder="https://www-dev.example.com"
          @input="emit('probe', 'web', env.web_domain)"
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
      <p class="entry-help">有 C 端、管理端等多个前端时在这里分别配置。机器人会结合工单 URL、产品/模块、文字和截图尺寸自动识别；证据不足时要求人工选择。</p>
      <div v-for="(entry, entryIndex) in env.frontend_entries" :key="entryIndex" class="frontend-entry-card">
        <div class="entry-grid entry-grid-primary">
          <label>入口 ID<input v-model="entry.id" placeholder="consumer-h5" /></label>
          <label>显示名称<input v-model="entry.name" placeholder="C 端 H5" /></label>
          <label>入口 URL<input v-model="entry.url" placeholder="https://m-test.example.com" /></label>
          <label>前端仓库<input v-model="entry.repo" placeholder="base-frontend" /></label>
          <label>设备
            <select v-model="entry.device_profile">
              <option value="desktop">桌面</option>
              <option value="mobile">手机</option>
              <option value="tablet">平板</option>
            </select>
          </label>
          <button class="btn-icon remove" type="button" @click="removeFrontendEntry(entryIndex)" aria-label="删除前端入口">×</button>
        </div>
        <div class="entry-grid entry-grid-hints">
          <label>名称/页面别名<input v-model="entry.aliases" placeholder="C端, H5, 用户端" /></label>
          <label>产品提示<input v-model="entry.product_hints" placeholder="用户中心, 商城" /></label>
          <label>模块提示<input v-model="entry.module_hints" placeholder="搜索, 个人主页" /></label>
          <label>路径前缀<input v-model="entry.path_prefixes" placeholder="/search, /user" /></label>
        </div>
      </div>
      <button class="btn add-entry" type="button" @click="addFrontendEntry">+ 添加前端应用入口</button>
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
  grid-template-columns: minmax(108px, 0.55fr) minmax(240px, 1.45fr) minmax(240px, 1.45fr) max-content 44px;
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
.frontend-entries { margin-top: 14px; border-top: 1px solid var(--c-line); padding-top: 12px; }
.frontend-entries summary { cursor: pointer; font-weight: 650; user-select: none; }
.entry-help { margin: 9px 0 12px; color: var(--c-text-3); font-size: 13px; }
.frontend-entry-card { margin: 10px 0; padding: 12px; border: 1px solid var(--c-line); border-radius: 8px; background: var(--c-bg); }
.entry-grid { display: grid; gap: 10px; align-items: end; }
.entry-grid-primary { grid-template-columns: 0.8fr 1fr 1.7fr 1fr 0.75fr 36px; }
.entry-grid-hints { grid-template-columns: repeat(4, minmax(0, 1fr)); margin-top: 10px; }
.entry-grid label { min-width: 0; font-size: 12px; color: var(--c-text-2); }
.entry-grid input, .entry-grid select { width: 100%; margin-top: 5px; }
.add-entry { margin-top: 4px; }
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
    grid-template-columns: minmax(100px, 0.6fr) minmax(220px, 1.4fr) minmax(220px, 1.4fr) 44px;
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
}
</style>
