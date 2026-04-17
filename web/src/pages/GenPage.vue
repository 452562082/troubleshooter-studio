<script setup lang="ts">
import { ref } from 'vue'

interface GenSummary {
  system: string
  config_center: string
  output_dir: string
  skills_included_count: number
  files_written: number
  preserved_count: number
  prior_overrides_count: number
  analyzer_hits_count: number
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
  yaml.value = exampleYaml
  error.value = ''
  summary.value = null
  statCards.value = []
}

const yaml = ref('')
const loading = ref(false)
const error = ref('')
const summary = ref<GenSummary | null>(null)

const statCards = ref<{ label: string; value: number | string; color: string }[]>([])

async function runGen() {
  if (!yaml.value.trim()) return
  loading.value = true
  error.value = ''
  summary.value = null
  statCards.value = []
  try {
    const resp = await fetch('/api/gen', {
      method: 'POST',
      headers: { 'Content-Type': 'text/yaml' },
      body: yaml.value,
    })
    const data = await resp.json()
    if (!resp.ok) throw new Error(data.error || '请求失败')
    summary.value = data
    statCards.value = [
      { label: 'Skills 数量', value: data.skills_included_count, color: '#3b82f6' },
      { label: '写入文件数', value: data.files_written, color: '#22c55e' },
      { label: '保留文件数', value: data.preserved_count, color: '#8b5cf6' },
      { label: 'Prior Overrides', value: data.prior_overrides_count, color: '#f59e0b' },
      { label: 'Analyzer Hits', value: data.analyzer_hits_count, color: '#06b6d4' },
    ]
  } catch (e: any) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="page">
    <h1>执行生成</h1>

    <div class="info-box">
      <div class="info-box-title">使用说明</div>
      <div>执行生成：根据 system.yaml 的 <code>generation.targets</code> 产出排障机器人部署包，可一次生成四种形态，<strong>每种都带 <code>install.sh</code> 一键部署</strong>：</div>
      <ul class="info-box-list">
        <li><code>openclaw</code> — <code>bash install.sh</code> 部署到 OpenClaw（含 self-test.sh + workspace 模板）</li>
        <li><code>claude-code</code> — <code>bash install.sh &lt;project-dir&gt;</code> 把 CLAUDE.md + skills/ 装入项目根</li>
        <li><code>cursor</code> — <code>bash install.sh &lt;project-dir&gt;</code> 把 .cursorrules + .cursor/rules/ + skills/ 装入项目根</li>
        <li><code>standalone</code> — <code>bash install.sh</code> 建 venv + 装依赖；或 <code>docker compose up --build</code> 一键起容器</li>
      </ul>
      <div class="info-box-warn">&#x26A0; 注意：此操作会写盘到 output_dir 目录（多 target 会产出 <code>&lt;id&gt;-&lt;target&gt;/</code> 兄弟目录）</div>
    </div>

    <div class="input-section">
      <div class="input-header">
        <label class="label">system.yaml</label>
        <button class="btn-example" @click="loadExample">加载示例</button>
      </div>
      <textarea
        v-model="yaml"
        class="yaml-input"
        placeholder="粘贴 system.yaml 内容..."
        spellcheck="false"
      />
      <button class="btn-primary" :disabled="loading || !yaml.trim()" @click="runGen">
        {{ loading ? '生成中...' : '执行生成' }}
      </button>
    </div>

    <!-- Error state -->
    <div v-if="error" class="error-banner">
      <span class="error-icon">&#x2716;</span>
      <span>{{ error }}</span>
    </div>

    <!-- Success state -->
    <div v-if="summary" class="results">
      <div class="success-banner">
        <span class="success-icon">&#x2714;</span>
        <div>
          <div class="success-title">生成成功</div>
          <div class="success-path">输出目录：{{ summary.output_dir }}</div>
        </div>
      </div>

      <!-- System info row -->
      <div class="sys-info">
        <span class="sys-chip">{{ summary.system }}</span>
        <span class="sys-chip sys-chip-muted">{{ summary.config_center }}</span>
      </div>

      <!-- Number cards dashboard -->
      <div class="dashboard">
        <div
          v-for="card in statCards"
          :key="card.label"
          class="stat-card"
        >
          <div class="stat-number" :style="{ color: card.color }">{{ card.value }}</div>
          <div class="stat-label">{{ card.label }}</div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.page h1 { font-size: 24px; margin-bottom: 12px; color: #1e293b; }

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
.info-box-warn {
  margin-top: 6px;
  color: #b45309;
  font-weight: 500;
}
.info-box-list {
  margin: 6px 0 6px 18px;
  padding: 0;
  line-height: 1.7;
}
.info-box-list li {
  margin: 2px 0;
}
.info-box code {
  background: rgba(0, 0, 0, 0.06);
  padding: 1px 6px;
  border-radius: 4px;
  font-family: 'SF Mono', 'Fira Code', 'Consolas', monospace;
  font-size: 12.5px;
}

.input-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
}
.btn-example {
  padding: 4px 12px;
  background: #f1f5f9;
  border: 1px solid #d1d5db;
  border-radius: 6px;
  cursor: pointer;
  font-size: 12px;
  color: #475569;
}
.btn-example:hover { background: #e2e8f0; }

.input-section { margin-bottom: 24px; }

.label {
  display: block;
  font-size: 13px;
  font-weight: 600;
  color: #475569;
  margin-bottom: 6px;
}

.yaml-input {
  width: 100%;
  height: 200px;
  font-family: 'SF Mono', 'Fira Code', 'Consolas', monospace;
  font-size: 13px;
  padding: 12px;
  border: 1px solid #cbd5e1;
  border-radius: 6px;
  resize: vertical;
  background: #f8fafc;
  color: #1e293b;
  line-height: 1.5;
}
.yaml-input:focus { outline: none; border-color: #3b82f6; box-shadow: 0 0 0 3px rgba(59,130,246,0.1); }

.btn-primary {
  margin-top: 12px;
  padding: 8px 20px;
  background: #3b82f6;
  color: #fff;
  border: none;
  border-radius: 6px;
  font-size: 14px;
  font-weight: 500;
  cursor: pointer;
  transition: background 0.15s;
}
.btn-primary:hover:not(:disabled) { background: #2563eb; }
.btn-primary:disabled { opacity: 0.5; cursor: not-allowed; }

.error-banner {
  display: flex;
  align-items: center;
  gap: 10px;
  background: #fef2f2;
  border: 1px solid #fecaca;
  color: #dc2626;
  padding: 12px 16px;
  border-radius: 8px;
  font-size: 14px;
  margin-bottom: 20px;
}
.error-icon { font-size: 18px; }

.results { display: flex; flex-direction: column; gap: 20px; }

.success-banner {
  display: flex;
  align-items: center;
  gap: 14px;
  background: #f0fdf4;
  border: 1px solid #bbf7d0;
  padding: 16px 20px;
  border-radius: 8px;
}
.success-icon { font-size: 28px; color: #16a34a; }
.success-title { font-size: 16px; font-weight: 600; color: #166534; }
.success-path {
  font-size: 13px;
  color: #4ade80;
  margin-top: 4px;
  font-family: 'SF Mono', 'Fira Code', 'Consolas', monospace;
}

.sys-info { display: flex; gap: 10px; }
.sys-chip {
  display: inline-block;
  padding: 4px 12px;
  background: #eff6ff;
  color: #1d4ed8;
  border-radius: 6px;
  font-size: 14px;
  font-weight: 500;
}
.sys-chip-muted { background: #f1f5f9; color: #64748b; }

.dashboard {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(160px, 1fr));
  gap: 16px;
}

.stat-card {
  background: #fff;
  border: 1px solid #e2e8f0;
  border-radius: 10px;
  padding: 20px;
  text-align: center;
  box-shadow: 0 1px 3px rgba(0,0,0,0.04);
  transition: box-shadow 0.15s;
}
.stat-card:hover { box-shadow: 0 4px 12px rgba(0,0,0,0.08); }

.stat-number {
  font-size: 36px;
  font-weight: 700;
  line-height: 1.1;
}

.stat-label {
  font-size: 13px;
  color: #64748b;
  margin-top: 6px;
  font-weight: 500;
}
</style>
