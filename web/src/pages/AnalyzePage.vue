<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { analyze as bridgeAnalyze, isDesktop, type AnalyzeResult } from '../lib/bridge'
import { toast } from '../lib/toast'
import { EventsOn, EventsOff } from '../../wailsjs/runtime/runtime'

const exampleYaml = `system:
  id: demo
  name: "Demo"
  description: "示例系统"

agent:
  name: "Demo排障机器人"
  workspace_name: "Demo排障机器人"
  model: openai-codex/gpt-5.3-codex

environments:
  - id: dev
    api_domain: api-dev.demo.com
    is_prod: false
  - id: prod
    api_domain: api.demo.com
    is_prod: true

repos:
  - name: demo-api
    url: git@github.com:demo/demo-api.git
    role: backend
    stack: go
    env_branches:
      dev: develop
      prod: main

infrastructure:
  config_center:
    type: nacos
    endpoints:
      - env: dev
        addr: nacos-dev:8848
      - env: prod
        addr: nacos-prod:8848
  observability:
    grafana: {enabled: true, url_by_env: {dev: "http://grafana-dev", prod: "http://grafana-prod"}}
    loki: {enabled: true, via_grafana: true}
    prometheus: {enabled: true, via_grafana: true}
  data_stores:
    - {type: redis, enabled: true, discovery: from_config_center, readonly_enforced: true}
    - {type: mongodb, enabled: false}
    - {type: elasticsearch, enabled: false}
  messaging: []
  project_tracking: []

generation:
  target_host: openclaw
  output_dir: ./dist/demo
  skills_whitelist: [routing, config-executor, redis-runtime-query, diagram-generator]
  preserve_on_regenerate: [SOUL.md]

meta:
  schema_version: "0.1"
`

function loadExample() {
  yamlContent.value = exampleYaml
  error.value = ''
  result.value = null
}

const yamlContent = ref('')
const reposRoot = ref('')
const autoClone = ref(false)
const loading = ref(false)
const result = ref<any>(null)
const error = ref('')

function loadFile(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0]
  if (!file) return
  const reader = new FileReader()
  reader.onload = () => { yamlContent.value = reader.result as string }
  reader.readAsText(file)
}

// analyze:log 事件流(analyzerpipe.OnProgress 每行 EventsEmit)
const progressLog = ref('')

async function runAnalyze() {
  if (!yamlContent.value.trim()) { error.value = '请先填写或加载 system.yaml'; return }
  if (!reposRoot.value.trim()) { error.value = '请填写仓库根目录路径'; return }
  if (!isDesktop()) {
    error.value = 'Analyze 仅在桌面 app 可用;浏览器 tshoot serve 模式请改用 CLI:\n  tshoot analyze -i <yaml> --repos-root ... -o analysis.json'
    return
  }
  loading.value = true
  error.value = ''
  result.value = null
  progressLog.value = ''
  try {
    const r = (await bridgeAnalyze(yamlContent.value, reposRoot.value, autoClone.value)) as AnalyzeResult
    result.value = r
    toast.success(`analyze 完成: ${r.per_repo?.length ?? 0} 个仓库,共 ${r.report?.repos?.length ?? 0} 条 report`)
  } catch (e: any) {
    error.value = e.message || String(e)
    toast.error(`analyze 失败: ${e.message || e}`)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  EventsOn('analyze:log', (line: string) => {
    progressLog.value += line + '\n'
  })
})
onUnmounted(() => {
  EventsOff('analyze:log')
})
</script>

<template>
  <div class="page">
    <h1>系统分析</h1>

    <div class="info-box">
      <div class="info-box-title">使用说明</div>
      <div>输入 system.yaml + 仓库根目录路径，分析器会扫描每个仓库抽取 service name 和配置中心线索。结果会标记 verified（机械确认）</div>
      <div class="info-box-note">注意：analyze 需要仓库代码实际存在于指定路径下</div>
    </div>

    <div class="form-section">
      <div class="label-row">
        <label>system.yaml</label>
        <div class="label-row-actions">
          <label class="btn small">加载文件 <input type="file" accept=".yaml,.yml" @change="loadFile" hidden /></label>
          <button class="btn small" @click="loadExample">加载示例</button>
        </div>
      </div>
      <textarea v-model="yamlContent" placeholder="粘贴 system.yaml 内容..." spellcheck="false" :class="{ err: error }" />
    </div>

    <div class="form-row">
      <div class="field">
        <label>仓库根目录（repos-root）</label>
        <input v-model="reposRoot" type="text" placeholder="例：./repos 或 /home/user/repos" />
      </div>
      <div class="field check">
        <label><input type="checkbox" v-model="autoClone" /> 自动 clone 缺失仓库</label>
      </div>
    </div>

    <button class="btn accent" @click="runAnalyze" :disabled="loading">
      {{ loading ? '分析中...' : '运行分析' }}
    </button>

    <div v-if="error" class="alert error">{{ error }}</div>

    <!-- 实时进度日志(analyze:log 事件) -->
    <pre v-if="loading && progressLog" class="progress-log">{{ progressLog }}</pre>

    <!-- 分析结果 -->
    <div v-if="result" class="results">
      <div class="summary-bar">
        <span class="tag blue">config_center: {{ result.report?.config_center || '-' }}</span>
        <span class="tag green">{{ result.report?.repos?.length || 0 }} 个仓库有 findings</span>
        <span class="tag gray">{{ result.per_repo?.length || 0 }} 个仓库处理过</span>
      </div>

      <!-- 每仓库状态摘要(per_repo) -->
      <div v-if="result.per_repo?.length" class="per-repo-grid">
        <div
          v-for="rs in result.per_repo"
          :key="rs.name"
          class="repo-status"
          :class="rs.status"
        >
          <span class="name">{{ rs.name }}</span>
          <span class="status-tag">{{ rs.status }}</span>
          <span v-if="rs.service_name_count" class="muted">{{ rs.service_name_count }} svc</span>
          <span v-if="rs.finding_count" class="muted">{{ rs.finding_count }} findings</span>
          <span v-if="rs.error" class="err">{{ rs.error }}</span>
        </div>
      </div>

      <!-- 详细 findings(report.repos) -->
      <div v-for="repo in result.report?.repos || []" :key="repo.name" class="card">
        <div class="card-header">
          <span class="name">{{ repo.name }}</span>
          <span v-if="repo.stack" class="tag gray">{{ repo.stack }}</span>
          <span v-if="repo.verified" class="tag green">verified</span>
        </div>

        <div v-if="repo.service_names?.length" class="detail">
          <strong>Services：</strong>
          <span v-for="s in repo.service_names" :key="s" class="tag blue">{{ s }}</span>
        </div>

        <div v-if="repo.findings?.length" class="detail">
          <strong>Findings（{{ repo.findings.length }}）：</strong>
          <div v-for="(f, i) in repo.findings" :key="i" class="finding">
            <span class="src">{{ f.source_file }}</span>
            <span v-if="f.data_id" class="kv">dataId={{ f.data_id }}</span>
            <span v-if="f.namespace_id" class="kv">ns={{ f.namespace_id }}</span>
            <span v-if="f.group" class="kv">group={{ f.group }}</span>
            <span v-if="f.app_id" class="kv">appId={{ f.app_id }}</span>
            <span v-if="f.kv_prefix" class="kv">prefix={{ f.kv_prefix }}</span>
            <span v-if="f.env_profile" class="tag orange">{{ f.env_profile }}</span>
          </div>
        </div>

        <div v-if="repo.warnings?.length" class="detail warn">
          <strong>Warnings：</strong>
          <div v-for="w in repo.warnings" :key="w" class="warn-line">{{ w }}</div>
        </div>

        <div v-if="!repo.findings?.length && !repo.warnings?.length" class="detail muted">
          无 findings 和 warnings
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* .page / .btn / .info-box* 来自 design.css;这里只写个性化 */

.label-row-actions { display: flex; gap: 6px; }
.form-section { margin-bottom: var(--sp-4); }
.label-row { display: flex; justify-content: space-between; align-items: center; margin-bottom: 6px; }
.label-row label { font-weight: 600; color: var(--c-text); font-size: var(--fs-md); }
/* 文件 input 用 <label class="btn small"> 当触发器,需要 overflow/display 让嵌套 input hidden 不影响布局 */
label.btn { cursor: pointer; }

textarea {
  width: 100%; min-height: 180px; padding: 10px var(--sp-3); border: 1px solid var(--c-line); border-radius: var(--r-md);
  font-family: 'SF Mono', 'Fira Code', monospace; font-size: var(--fs-base); line-height: 1.5;
  background: var(--c-surf-2); resize: vertical; box-sizing: border-box;
}
textarea:focus { outline: none; border-color: var(--c-accent); }
textarea.err { border-color: #ef4444; }

.form-row { display: flex; gap: var(--sp-4); margin-bottom: var(--sp-4); align-items: flex-end; }
.field { flex: 1; }
.field label { display: block; font-weight: 600; color: var(--c-text); margin-bottom: 6px; font-size: var(--fs-md); }
.field input[type="text"] {
  width: 100%; padding: 8px var(--sp-3); border: 1px solid var(--c-line-2); border-radius: var(--r-md);
  font-size: var(--fs-md); box-sizing: border-box;
}
.field input[type="text"]:focus { outline: none; border-color: var(--c-accent); }
.field.check { flex: none; }
.field.check label { font-weight: 400; font-size: var(--fs-md); color: var(--c-text); cursor: pointer; display: flex; align-items: center; gap: 6px; }

/* banner 用旧 class 名,但样式等价于 .alert.error;保留 class 避免改 template */
.banner { margin-top: var(--sp-3); padding: 10px var(--sp-3); border-radius: var(--r-md); font-size: var(--fs-base); }

.results { margin-top: var(--sp-5); }
.summary-bar { display: flex; gap: var(--sp-2); margin-bottom: var(--sp-4); }

.tag {
  display: inline-block; padding: 3px 10px; border-radius: 12px; font-size: 12px; font-weight: 500;
}
.tag.blue { background: #dbeafe; color: #1e40af; }
.tag.green { background: #d1fae5; color: #065f46; }
.tag.orange { background: #fef3c7; color: #92400e; }
.tag.gray { background: #f1f5f9; color: #475569; }

.name { font-weight: 700; color: #1e293b; font-size: 15px; }

.detail { margin-bottom: 8px; font-size: 13px; color: #475569; }
.detail strong { color: #334155; margin-right: 6px; }
.detail.muted { color: #94a3b8; font-style: italic; }

.finding {
  display: flex; flex-wrap: wrap; gap: 6px; padding: 4px 0; border-bottom: 1px solid #f1f5f9; align-items: center;
}
.src { font-family: monospace; font-size: 12px; color: #3b82f6; }
.kv { font-family: monospace; font-size: 12px; background: #f1f5f9; padding: 1px 6px; border-radius: 3px; }

.detail.warn { color: #92400e; }
.warn-line { font-size: 12px; padding: 2px 0; }

/* 新增:实时进度日志 + 每仓库摘要 grid */
.progress-log {
  background: #0f172a; color: #e2e8f0; padding: 10px 12px; border-radius: 6px;
  font-family: 'SFMono-Regular', Menlo, monospace; font-size: 11px;
  max-height: 240px; overflow: auto; white-space: pre-wrap; word-break: break-all;
  margin-top: 12px; border-left: 3px solid #22c55e;
}
.per-repo-grid {
  display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
  gap: 8px; margin: 12px 0;
}
.repo-status {
  display: flex; gap: 6px; align-items: center; flex-wrap: wrap;
  padding: 6px 10px; border-radius: 4px; font-size: 12px;
  background: #f8fafc; border: 1px solid #e2e8f0;
}
.repo-status .name { font-weight: 600; flex: 1; }
.repo-status .status-tag { font-family: monospace; font-size: 10px; padding: 1px 6px; background: #e2e8f0; border-radius: 3px; }
.repo-status .muted { color: #64748b; font-size: 11px; }
.repo-status .err { color: #b91c1c; font-size: 11px; }
.repo-status.analyzed { border-color: #86efac; }
.repo-status.cloned-then-analyzed { border-color: #60a5fa; }
.repo-status.skipped { opacity: 0.7; }
.repo-status.clone-failed { background: #fef2f2; border-color: #fecaca; }
</style>
