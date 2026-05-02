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

defineProps<{
  /** 本行的环境对象;reactive 直引,改字段不必 emit */
  env: { id: string; api_domain: string; web_domain: string; is_prod: boolean }
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
</script>

<template>
  <div class="dynamic-row">
    <div class="row-fields">
      <div class="form-group compact" style="flex: 0 0 100px">
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
      <div class="form-group compact">
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
      <div class="form-group compact">
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
      <div class="form-group compact checkbox-group">
        <label title="is_prod=true 时机器人更保守:执行写入/重启类动作前会二次确认;OpenClaw 客户端 UI 也会标红。">
          <input type="checkbox" v-model="env.is_prod" />
          生产环境
          <span class="help-icon">?</span>
        </label>
      </div>
      <button class="btn-icon remove" @click="emit('remove')" :disabled="disableRemove" title="删除">
        &times;
      </button>
    </div>
  </div>
</template>
