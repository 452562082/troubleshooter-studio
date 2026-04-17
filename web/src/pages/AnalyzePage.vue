<script setup lang="ts">
import { ref } from 'vue'

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

async function runAnalyze() {
  if (!yamlContent.value.trim()) { error.value = '请先填写或加载 system.yaml'; return }
  if (!reposRoot.value.trim()) { error.value = '请填写仓库根目录路��'; return }
  loading.value = true
  error.value = ''
  result.value = null
  try {
    const params = new URLSearchParams({ repos_root: reposRoot.value })
    if (autoClone.value) params.set('auto_clone', 'true')
    const resp = await fetch(`/api/analyze?${params}`, { method: 'POST', body: yamlContent.value })
    const data = await resp.json()
    if (!resp.ok) { error.value = data.error || `HTTP ${resp.status}`; return }
    result.value = data
  } catch (e: any) {
    error.value = e.message || '请求失败'
  } finally {
    loading.value = false
  }
}
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
          <label class="file-btn">加载文件 <input type="file" accept=".yaml,.yml" @change="loadFile" hidden /></label>
          <button class="file-btn" @click="loadExample">加载示例</button>
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

    <button class="btn-primary" @click="runAnalyze" :disabled="loading">
      {{ loading ? '分析中...' : '运行分析' }}
    </button>

    <div v-if="error" class="banner red">{{ error }}</div>

    <!-- 分析结果 -->
    <div v-if="result" class="results">
      <div class="summary-bar">
        <span class="tag blue">config_center: {{ result.config_center || '-' }}</span>
        <span class="tag green">{{ result.repos?.length || 0 }} 个仓库</span>
      </div>

      <div v-for="repo in result.repos" :key="repo.name" class="card">
        <div class="card-header">
          <span class="name">{{ repo.name }}</span>
          <span class="tag gray">{{ repo.stack }}</span>
          <span v-if="repo.verified" class="tag green">verified</span>
          <span v-if="repo.status" class="tag" :class="repo.status === 'analyzed' ? 'blue' : 'orange'">{{ repo.status }}</span>
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
.page { max-width: 900px; }
h1 { color: #1e293b; margin-bottom: 12px; }

.info-box {
  background: #eff6ff;
  border: 1px solid #bfdbfe;
  border-radius: 8px;
  padding: 12px 16px;
  margin-bottom: 20px;
  font-size: 14px;
  color: #1e40af;
  line-height: 1.6;
}
.info-box-title {
  font-weight: 600;
  margin-bottom: 4px;
}
.info-box-note {
  margin-top: 6px;
  font-size: 13px;
  color: #3b82f6;
  font-style: italic;
}

.label-row-actions {
  display: flex;
  gap: 6px;
}

.form-section { margin-bottom: 16px; }
.label-row { display: flex; justify-content: space-between; align-items: center; margin-bottom: 6px; }
.label-row label { font-weight: 600; color: #334155; font-size: 14px; }
.file-btn {
  padding: 5px 12px; background: #f1f5f9; border: 1px solid #d1d5db;
  border-radius: 6px; cursor: pointer; font-size: 12px; color: #475569; font-weight: 400;
}
.file-btn:hover { background: #e2e8f0; }

textarea {
  width: 100%; min-height: 180px; padding: 10px 14px; border: 1px solid #e2e8f0; border-radius: 6px;
  font-family: 'SF Mono', 'Fira Code', monospace; font-size: 13px; line-height: 1.5;
  background: #f8fafc; resize: vertical; box-sizing: border-box;
}
textarea:focus { outline: none; border-color: #3b82f6; }
textarea.err { border-color: #ef4444; }

.form-row { display: flex; gap: 16px; margin-bottom: 16px; align-items: flex-end; }
.field { flex: 1; }
.field label { display: block; font-weight: 600; color: #334155; margin-bottom: 6px; font-size: 14px; }
.field input[type="text"] {
  width: 100%; padding: 8px 12px; border: 1px solid #d1d5db; border-radius: 6px; font-size: 14px; box-sizing: border-box;
}
.field input[type="text"]:focus { outline: none; border-color: #3b82f6; }
.field.check { flex: none; }
.field.check label { font-weight: 400; font-size: 14px; color: #475569; cursor: pointer; display: flex; align-items: center; gap: 6px; }

.btn-primary {
  padding: 10px 24px; background: #3b82f6; color: white; border: none; border-radius: 6px;
  font-size: 14px; font-weight: 600; cursor: pointer;
}
.btn-primary:hover { background: #2563eb; }
.btn-primary:disabled { background: #94a3b8; cursor: not-allowed; }

.banner { margin-top: 12px; padding: 10px 14px; border-radius: 6px; font-size: 13px; }
.banner.red { background: #fef2f2; border: 1px solid #fecaca; color: #991b1b; }

.results { margin-top: 20px; }
.summary-bar { display: flex; gap: 8px; margin-bottom: 16px; }

.tag {
  display: inline-block; padding: 3px 10px; border-radius: 12px; font-size: 12px; font-weight: 500;
}
.tag.blue { background: #dbeafe; color: #1e40af; }
.tag.green { background: #d1fae5; color: #065f46; }
.tag.orange { background: #fef3c7; color: #92400e; }
.tag.gray { background: #f1f5f9; color: #475569; }

.card {
  background: white; border: 1px solid #e2e8f0; border-radius: 8px; padding: 14px 18px; margin-bottom: 12px;
}
.card-header { display: flex; align-items: center; gap: 8px; margin-bottom: 8px; }
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
</style>
