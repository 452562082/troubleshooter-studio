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

import { computed, useId } from 'vue'
import type { URLProbeState } from '../lib/probeTypes'
import { emptyDeploymentVerification, type DeploymentVerificationState } from '../lib/yamlGenerator'
import URLProbeBadge from './URLProbeBadge.vue'

const props = defineProps<{
  /** 本行的环境对象;reactive 直引,改字段不必 emit */
  env: { id: string; api_domain: string; web_domain: string; is_prod: boolean; deployment_verification?: DeploymentVerificationState }
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

if (!props.env.deployment_verification) props.env.deployment_verification = emptyDeploymentVerification()
const verification = computed(() => props.env.deployment_verification as DeploymentVerificationState)
const fieldID = useId()
const deploymentMapText = computed({
  get: () => Object.entries(verification.value.k8s.deployments_by_repo).map(([repo, deployment]) => `${repo}=${deployment}`).join('\n'),
  set: (value: string) => {
    const mappings: Record<string, string> = {}
    for (const line of value.split('\n')) {
      const separator = line.indexOf('=')
      if (separator <= 0) continue
      const repo = line.slice(0, separator).trim()
      const deployment = line.slice(separator + 1).trim()
      if (repo && deployment) mappings[repo] = deployment
    }
    verification.value.k8s.deployments_by_repo = mappings
  },
})

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
    <fieldset class="deployment-verification-fields">
      <legend>部署版本验证</legend>
      <div class="form-group compact">
        <label :for="`${fieldID}-provider`">验证方式</label>
        <select :id="`${fieldID}-provider`" v-model="verification.provider">
          <option value="manual">人工提供版本证明</option>
          <option value="http">HTTP 版本接口</option>
          <option value="k8s">K8s Deployment</option>
        </select>
      </div>
      <template v-if="verification.provider === 'http'">
        <div class="form-group compact">
          <label :for="`${fieldID}-http-url`">版本接口 URL</label>
          <input :id="`${fieldID}-http-url`" v-model="verification.http.url" type="url" placeholder="https://api-test.example.com/version" />
        </div>
        <div class="form-group compact">
          <label :for="`${fieldID}-json-pointer`">JSON Pointer</label>
          <input :id="`${fieldID}-json-pointer`" v-model="verification.http.json_pointer" type="text" placeholder="/git/commit" />
        </div>
      </template>
      <template v-else-if="verification.provider === 'k8s'">
        <div class="form-group compact">
          <label :for="`${fieldID}-cluster`">集群</label>
          <input :id="`${fieldID}-cluster`" v-model="verification.k8s.cluster" type="text" />
        </div>
        <div class="form-group compact">
          <label :for="`${fieldID}-namespace`">Namespace</label>
          <input :id="`${fieldID}-namespace`" v-model="verification.k8s.namespace" type="text" />
        </div>
        <div class="form-group compact">
          <label :for="`${fieldID}-deployments`">仓库到 Deployment 映射（每行 repo=deployment）</label>
          <textarea :id="`${fieldID}-deployments`" v-model="deploymentMapText" rows="2" placeholder="admin-web=admin-web" />
        </div>
        <div class="form-group compact">
          <label :for="`${fieldID}-annotation`">Commit annotation</label>
          <input :id="`${fieldID}-annotation`" v-model="verification.k8s.commit_annotation" type="text" placeholder="app.example.com/git-commit" @input="verification.k8s.image_label = ''" />
        </div>
        <div class="form-group compact">
          <label :for="`${fieldID}-image-label`">或 image label</label>
          <input :id="`${fieldID}-image-label`" v-model="verification.k8s.image_label" type="text" placeholder="git-commit" @input="verification.k8s.commit_annotation = ''" />
        </div>
      </template>
    </fieldset>
  </div>
</template>

<style scoped>
.deployment-verification-fields {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
  gap: 12px;
  margin-top: 12px;
  padding: 12px;
  border: 1px solid var(--border-color, #d1d5db);
  border-radius: 8px;
}
.deployment-verification-fields legend { padding: 0 6px; font-weight: 600; }
.deployment-verification-fields input:focus-visible,
.deployment-verification-fields select:focus-visible,
.deployment-verification-fields textarea:focus-visible { outline: 2px solid #2563eb; outline-offset: 2px; }
</style>
