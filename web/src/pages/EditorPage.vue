<script setup lang="ts">
import { ref } from 'vue'
import { validate as bridgeValidate, plan as bridgePlan, gen as bridgeGen } from '../lib/bridge'

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

const yamlContent = ref('')
const loading = ref('')
const errorMsg = ref('')
const successMsg = ref('')
const resultTitle = ref('')
const resultData = ref<any>(null)

function loadExample() {
  yamlContent.value = exampleYaml
  errorMsg.value = ''
  successMsg.value = ''
  resultData.value = null
}

function loadFile(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  const reader = new FileReader()
  reader.onload = () => {
    yamlContent.value = reader.result as string
    errorMsg.value = ''
    successMsg.value = ''
    resultData.value = null
  }
  reader.readAsText(file)
  input.value = ''
}

async function apiCall(endpoint: 'validate' | 'plan' | 'gen', label: string) {
  errorMsg.value = ''
  successMsg.value = ''
  resultData.value = null
  resultTitle.value = ''
  loading.value = label

  try {
    if (endpoint === 'validate') {
      const r = await bridgeValidate(yamlContent.value)
      successMsg.value = `验证通过！系统: ${r.system} (${r.name}) | ${r.envs} 个环境 | ${r.repos} 个仓库`
    } else if (endpoint === 'plan') {
      resultTitle.value = label
      resultData.value = await bridgePlan(yamlContent.value)
    } else {
      resultTitle.value = label
      resultData.value = await bridgeGen(yamlContent.value, '')
    }
  } catch (e: any) {
    errorMsg.value = e.message || String(e)
  } finally {
    loading.value = ''
  }
}
</script>

<template>
  <div class="page">
    <h1>System YAML 编辑器</h1>

    <div class="info-box">
      <div class="info-box-title">使用说明</div>
      <div>在此编辑 system.yaml，支持「验证」「预览计划」「执行生成」三种操作</div>
    </div>

    <div class="toolbar">
      <label class="btn">
        加载文件
        <input type="file" accept=".yaml,.yml" hidden @change="loadFile" />
      </label>
      <button class="btn" @click="loadExample">加载示例</button>
      <button class="btn primary" :disabled="!!loading" @click="apiCall('validate', '验证')">
        {{ loading === '验证' ? '验证中...' : '验证' }}
      </button>
      <button class="btn primary" :disabled="!!loading" @click="apiCall('plan', '生成计划')">
        {{ loading === '生成计划' ? '计划中...' : '生成计划' }}
      </button>
      <button class="btn accent" :disabled="!!loading" @click="apiCall('gen', '执行生成')">
        {{ loading === '执行生成' ? '生成中...' : '执行生成' }}
      </button>
    </div>

    <textarea
      v-model="yamlContent"
      class="yaml-editor"
      :class="{ 'has-error': errorMsg }"
      placeholder="# 在此粘贴或加载 system.yaml 内容..."
      spellcheck="false"
    />

    <div v-if="successMsg" class="banner banner-success">{{ successMsg }}</div>
    <div v-if="errorMsg" class="banner banner-error">{{ errorMsg }}</div>

    <!-- Plan result -->
    <div v-if="resultData && resultTitle === '生成计划'" class="result-card">
      <h2>生成计划: {{ resultData.system }}</h2>
      <div class="result-grid">
        <div class="result-section">
          <h3>已包含 Skills ({{ resultData.skills_included?.length || 0 }})</h3>
          <ul v-if="resultData.skills_included?.length">
            <li v-for="s in resultData.skills_included" :key="s.name">
              <strong>{{ s.name }}</strong>
              <span v-if="s.reason" class="sub-text"> — {{ s.reason }}</span>
            </li>
          </ul>
          <p v-else class="sub-text">无</p>
        </div>
        <div class="result-section">
          <h3>已跳过 Skills ({{ resultData.skills_skipped?.length || 0 }})</h3>
          <ul v-if="resultData.skills_skipped?.length">
            <li v-for="s in resultData.skills_skipped" :key="s.name">
              <strong>{{ s.name }}</strong>
              <span v-if="s.reason" class="sub-text"> — {{ s.reason }}</span>
            </li>
          </ul>
          <p v-else class="sub-text">无</p>
        </div>
      </div>
      <div class="result-grid">
        <div class="result-section">
          <h3>文件变化</h3>
          <p><span class="badge badge-green">创建: {{ resultData.files_create?.length || 0 }}</span></p>
          <p><span class="badge badge-blue">修改: {{ resultData.files_modify?.length || 0 }}</span></p>
          <p><span class="badge badge-red">删除: {{ resultData.files_remove?.length || 0 }}</span></p>
          <p><span class="badge badge-gray">保留: {{ resultData.preserved?.length || 0 }}</span></p>
        </div>
        <div class="result-section">
          <h3>Config-Map 投影</h3>
          <table class="mini-table">
            <tr><td>来自分析器</td><td>{{ resultData.config_map_projection?.verified_from_analyzer ?? 0 }}</td></tr>
            <tr><td>来自 Prior</td><td>{{ resultData.config_map_projection?.verified_from_prior ?? 0 }}</td></tr>
            <tr><td>推断</td><td>{{ resultData.config_map_projection?.inferred ?? 0 }}</td></tr>
            <tr><td><strong>总计</strong></td><td><strong>{{ resultData.config_map_projection?.total ?? 0 }}</strong></td></tr>
          </table>
        </div>
      </div>
    </div>

    <!-- Gen result -->
    <div v-if="resultData && resultTitle === '执行生成'" class="result-card">
      <h2>生成结果摘要</h2>
      <table class="mini-table">
        <tr><td>系统</td><td>{{ resultData.system }}</td></tr>
        <tr><td>配置中心</td><td>{{ resultData.config_center }}</td></tr>
        <tr><td>输出目录</td><td><code>{{ resultData.output_dir }}</code></td></tr>
        <tr><td>包含 Skills</td><td>{{ resultData.skills_included_count }}</td></tr>
        <tr><td>写入文件数</td><td>{{ resultData.files_written }}</td></tr>
        <tr><td>保留文件数</td><td>{{ resultData.preserved_count }}</td></tr>
        <tr><td>Prior Overrides</td><td>{{ resultData.prior_overrides_count }}</td></tr>
        <tr><td>Analyzer Hits</td><td>{{ resultData.analyzer_hits_count }}</td></tr>
      </table>
    </div>
  </div>
</template>

<style scoped>

.toolbar {
  display: flex;
  gap: 8px;
  margin-bottom: 12px;
  flex-wrap: wrap;
}

.yaml-editor {
  width: 100%;
  min-height: 500px;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  border-radius: 6px;
  padding: 12px 16px;
  font-family: 'SF Mono', 'Fira Code', 'Cascadia Code', monospace;
  font-size: 13px;
  line-height: 1.5;
  color: #1e293b;
  resize: vertical;
  outline: none;
  transition: border-color 0.15s;
}
.yaml-editor:focus {
  border-color: #3b82f6;
}
.yaml-editor.has-error {
  border-color: #ef4444;
}

.banner {
  margin-top: 12px;
  padding: 10px 16px;
  border-radius: 6px;
  font-size: 14px;
  font-weight: 500;
}
.banner-success {
  background: #ecfdf5;
  color: #065f46;
  border: 1px solid #a7f3d0;
}
.banner-error {
  background: #fef2f2;
  color: #991b1b;
  border: 1px solid #fecaca;
}

.result-card {
  margin-top: 20px;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  padding: 20px 24px;
}
.result-card h2 {
  font-size: 18px;
  color: #1e293b;
  margin-bottom: 16px;
  padding-bottom: 8px;
  border-bottom: 1px solid #e2e8f0;
}
.result-card h3 {
  font-size: 14px;
  color: #475569;
  margin-bottom: 8px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.result-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 20px;
  margin-bottom: 16px;
}

.result-section ul {
  list-style: none;
  padding: 0;
}
.result-section li {
  padding: 4px 0;
  font-size: 13px;
  color: #334155;
}

.sub-text {
  color: #94a3b8;
  font-size: 13px;
}

.badge {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 600;
}
.badge-green { background: #dcfce7; color: #166534; }
.badge-blue { background: #dbeafe; color: #1e40af; }
.badge-red { background: #fee2e2; color: #991b1b; }
.badge-gray { background: #f1f5f9; color: #475569; }

.mini-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.mini-table td {
  padding: 6px 12px;
  border-bottom: 1px solid #e2e8f0;
  color: #334155;
}
.mini-table tr:last-child td {
  border-bottom: none;
}
.mini-table td:first-child {
  color: #64748b;
  width: 180px;
}
.mini-table code {
  background: #e2e8f0;
  padding: 2px 6px;
  border-radius: 3px;
  font-size: 12px;
}
</style>
