<script setup lang="ts">
import { ref, computed } from 'vue'
import { doctor as bridgeDoctor } from '../lib/bridge'

interface Issue {
  severity: string
  category: string
  target: string
  message: string
  suggest?: string
}

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
  errorMsg.value = ''
  hasResult.value = false
}

const yamlContent = ref('')
const reposRoot = ref('')
const loading = ref(false)
const errorMsg = ref('')
const issues = ref<Issue[]>([])
const hasResult = ref(false)

const counts = computed(() => {
  let errors = 0, warnings = 0, infos = 0
  for (const i of issues.value) {
    if (i.severity === 'error') errors++
    else if (i.severity === 'warning') warnings++
    else infos++
  }
  return { errors, warnings, infos }
})

function loadFile(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  const reader = new FileReader()
  reader.onload = () => {
    yamlContent.value = reader.result as string
    errorMsg.value = ''
    hasResult.value = false
  }
  reader.readAsText(file)
  input.value = ''
}

async function runDoctor() {
  errorMsg.value = ''
  issues.value = []
  hasResult.value = false
  loading.value = true

  try {
    const data = (await bridgeDoctor(yamlContent.value, reposRoot.value)) as {
      issues?: Issue[]
    }
    issues.value = data.issues || []
    hasResult.value = true
  } catch (e: any) {
    errorMsg.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

function severityIcon(s: string) {
  if (s === 'error') return '\u2716'
  if (s === 'warning') return '\u26A0'
  return '\u2139'
}
</script>

<template>
  <div class="page">
    <h1>Doctor 诊断</h1>

    <div class="info-box">
      <div class="info-box-title">使用说明</div>
      <div>Doctor 对比 system.yaml 声明与代码仓库实际情况，检测 8 种漂移（missing-repo / stack-mismatch / origin-mismatch 等）</div>
    </div>

    <div class="form-group">
      <label class="form-label">system.yaml</label>
      <div class="toolbar">
        <label class="btn">
          加载文件
          <input type="file" accept=".yaml,.yml" hidden @change="loadFile" />
        </label>
        <button class="btn" @click="loadExample">加载示例</button>
      </div>
      <textarea
        v-model="yamlContent"
        class="yaml-editor"
        placeholder="# 在此粘贴 system.yaml 内容..."
        spellcheck="false"
      />
    </div>

    <div class="form-group">
      <label class="form-label">仓库根目录路径</label>
      <input
        v-model="reposRoot"
        type="text"
        class="text-input"
        placeholder="/path/to/repos（可选）"
      />
    </div>

    <button class="btn primary" :disabled="loading" @click="runDoctor">
      {{ loading ? '诊断中...' : '运行诊断' }}
    </button>

    <div v-if="errorMsg" class="alert error">{{ errorMsg }}</div>

    <div v-if="loading" class="loading-bar">
      <div class="loading-track"><div class="loading-fill" /></div>
      <span>诊断中...</span>
    </div>

    <template v-if="hasResult && !loading">
      <div class="summary-bar">
        <span class="summary-badge badge-red">{{ counts.errors }} 个错误</span>
        <span class="summary-badge badge-orange">{{ counts.warnings }} 个警告</span>
        <span class="summary-badge badge-blue">{{ counts.infos }} 个提示</span>
      </div>

      <div v-if="issues.length === 0" class="alert success">
        未发现问题，系统配置正常！
      </div>

      <div v-for="(issue, idx) in issues" :key="idx" class="issue-card" :class="'issue-' + issue.severity">
        <div class="issue-header">
          <span class="issue-icon">{{ severityIcon(issue.severity) }}</span>
          <span class="issue-category">{{ issue.category }}</span>
          <span class="issue-sep">&mdash;</span>
          <span class="issue-target">{{ issue.target }}</span>
        </div>
        <div class="issue-message">{{ issue.message }}</div>
        <div v-if="issue.suggest" class="issue-suggest">{{ issue.suggest }}</div>
      </div>
    </template>
  </div>
</template>

<style scoped>

.form-group {
  margin-bottom: 16px;
}
.form-label {
  display: block;
  font-size: 13px;
  font-weight: 600;
  color: #475569;
  margin-bottom: 6px;
  text-transform: uppercase;
  letter-spacing: 0.4px;
}

.toolbar {
  /* 8px 按钮到 textarea 偏紧,用户反馈"贴在一起"。跟 EditorPage / AnalyzePage
   * 的视觉间距看齐;Doctor 这里 toolbar 跟 label 分行,比同行版(Plan/Gen/Diff)
   * 需要多一点气,设 10px。 */
  margin-bottom: 10px;
  display: flex;
  gap: 8px;
}

.yaml-editor {
  width: 100%;
  min-height: 200px;
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

.text-input {
  width: 100%;
  max-width: 500px;
  padding: 8px 12px;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  border-radius: 6px;
  font-size: 14px;
  color: #1e293b;
  outline: none;
  transition: border-color 0.15s;
}
.text-input:focus {
  border-color: #3b82f6;
}

.loading-bar {
  margin-top: 16px;
  display: flex;
  align-items: center;
  gap: 12px;
  color: #64748b;
  font-size: 14px;
}
.loading-track {
  width: 200px;
  height: 4px;
  background: #e2e8f0;
  border-radius: 2px;
  overflow: hidden;
}
.loading-fill {
  width: 40%;
  height: 100%;
  background: #3b82f6;
  border-radius: 2px;
  animation: loading-slide 1s ease-in-out infinite;
}
@keyframes loading-slide {
  0% { transform: translateX(-100%); }
  100% { transform: translateX(350%); }
}

.summary-bar {
  margin-top: 20px;
  margin-bottom: 16px;
  display: flex;
  gap: 10px;
}
.summary-badge {
  display: inline-block;
  padding: 4px 12px;
  border-radius: 12px;
  font-size: 13px;
  font-weight: 600;
}
.badge-red { background: #fee2e2; color: #991b1b; }
.badge-orange { background: #fff7ed; color: #9a3412; }
.badge-blue { background: #dbeafe; color: #1e40af; }

.issue-card {
  background: #fff;
  border: 1px solid #e2e8f0;
  border-left: 4px solid #94a3b8;
  border-radius: 6px;
  padding: 12px 16px;
  margin-bottom: 10px;
}
.issue-error {
  border-left-color: #ef4444;
}
.issue-warning {
  border-left-color: #f97316;
}
.issue-info {
  border-left-color: #3b82f6;
}

.issue-header {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-bottom: 4px;
  font-size: 14px;
  font-weight: 600;
  color: #1e293b;
}
.issue-icon {
  font-size: 14px;
}
.issue-error .issue-icon { color: #ef4444; }
.issue-warning .issue-icon { color: #f97316; }
.issue-info .issue-icon { color: #3b82f6; }

.issue-category {
  color: #64748b;
  font-weight: 500;
}
.issue-sep {
  color: #cbd5e1;
}
.issue-target {
  color: #1e293b;
}

.issue-message {
  font-size: 13px;
  color: #334155;
  line-height: 1.5;
}

.issue-suggest {
  margin-top: 4px;
  font-size: 13px;
  color: #94a3b8;
  font-style: italic;
}
</style>
