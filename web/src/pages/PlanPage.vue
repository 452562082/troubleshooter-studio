<script setup lang="ts">
import { ref, computed } from 'vue'

interface SkillDecision {
  name: string
  reason?: string
}

interface OverrideRef {
  env: string
  service: string
}

interface AnalyzerHitRef {
  service: string
  env?: string
  source?: string
}

interface ConfigMapProjection {
  verified_from_analyzer: number
  verified_from_prior: number
  inferred: number
  total: number
}

interface Plan {
  system: string
  config_center: string
  skills_included: SkillDecision[]
  skills_skipped: SkillDecision[]
  files_create: string[]
  files_modify: string[]
  files_remove: string[]
  preserved: string[]
  prior_overrides: OverrideRef[]
  analyzer_hits: AnalyzerHitRef[]
  config_map_projection: ConfigMapProjection
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
  plan.value = null
}

const yaml = ref('')
const loading = ref(false)
const error = ref('')
const plan = ref<Plan | null>(null)
const expandFiles = ref(false)

const fileCountSummary = computed(() => {
  if (!plan.value) return ''
  const c = plan.value.files_create?.length ?? 0
  const m = plan.value.files_modify?.length ?? 0
  const r = plan.value.files_remove?.length ?? 0
  return `${c} 创建 / ${m} 修改 / ${r} 删除`
})

const configBar = computed(() => {
  if (!plan.value) return []
  const p = plan.value.config_map_projection
  if (!p || p.total === 0) return []
  return [
    { label: 'Analyzer 验证', count: p.verified_from_analyzer, color: '#22c55e' },
    { label: 'Prior 验证', count: p.verified_from_prior, color: '#3b82f6' },
    { label: '推断', count: p.inferred, color: '#f59e0b' },
  ]
})

async function runPlan() {
  if (!yaml.value.trim()) return
  loading.value = true
  error.value = ''
  plan.value = null
  try {
    const resp = await fetch('/api/plan', {
      method: 'POST',
      headers: { 'Content-Type': 'text/yaml' },
      body: yaml.value,
    })
    const data = await resp.json()
    if (!resp.ok) throw new Error(data.error || '请求失败')
    plan.value = data
  } catch (e: any) {
    error.value = e.message
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="page">
    <h1>计划预览</h1>

    <div class="info-box">
      <div class="info-box-title">使用说明</div>
      <div>Plan 是 gen 的预演——展示将生成哪些文件、启用哪些 skill、config-map 中有多少 verified / inferred 行，但不实际写盘</div>
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
      <button class="btn-primary" :disabled="loading || !yaml.trim()" @click="runPlan">
        {{ loading ? '分析中...' : '预览计划' }}
      </button>
    </div>

    <div v-if="error" class="error-banner">{{ error }}</div>

    <div v-if="plan" class="results">
      <!-- System info -->
      <div class="card">
        <div class="card-title">系统信息</div>
        <div class="info-row">
          <span class="info-label">系统</span>
          <span class="info-value">{{ plan.system }}</span>
        </div>
        <div class="info-row">
          <span class="info-label">配置中心</span>
          <span class="info-value">{{ plan.config_center }}</span>
        </div>
      </div>

      <!-- Skills -->
      <div class="card">
        <div class="card-title">Skills</div>
        <div class="badge-group">
          <span
            v-for="s in plan.skills_included"
            :key="'inc-' + s.name"
            class="badge badge-green"
            :title="s.reason || '已包含'"
          >{{ s.name }}</span>
          <span
            v-for="s in plan.skills_skipped"
            :key="'skip-' + s.name"
            class="badge badge-gray"
            :title="s.reason || '已跳过'"
          >{{ s.name }}
            <span v-if="s.reason" class="badge-reason">{{ s.reason }}</span>
          </span>
        </div>
        <div v-if="!plan.skills_included?.length && !plan.skills_skipped?.length" class="empty-hint">
          无 skill 决策信息
        </div>
      </div>

      <!-- Files -->
      <div class="card">
        <div class="card-title">
          文件变化
          <span class="card-subtitle">{{ fileCountSummary }}</span>
        </div>
        <div class="file-counts">
          <span class="fc fc-create">+{{ plan.files_create?.length ?? 0 }} 创建</span>
          <span class="fc fc-modify">~{{ plan.files_modify?.length ?? 0 }} 修改</span>
          <span class="fc fc-remove">-{{ plan.files_remove?.length ?? 0 }} 删除</span>
        </div>
        <button class="btn-text" @click="expandFiles = !expandFiles">
          {{ expandFiles ? '收起文件列表' : '展开文件列表' }}
        </button>
        <div v-if="expandFiles" class="file-list">
          <div v-for="f in plan.files_create" :key="'c-' + f" class="file-item file-create">+ {{ f }}</div>
          <div v-for="f in plan.files_modify" :key="'m-' + f" class="file-item file-modify">~ {{ f }}</div>
          <div v-for="f in plan.files_remove" :key="'r-' + f" class="file-item file-remove">- {{ f }}</div>
        </div>
      </div>

      <!-- Config Map Projection -->
      <div class="card">
        <div class="card-title">Config-Map 投影</div>
        <div v-if="configBar.length" class="bar-chart">
          <div class="bar-track">
            <div
              v-for="seg in configBar"
              :key="seg.label"
              class="bar-segment"
              :style="{
                width: (seg.count / plan.config_map_projection.total * 100) + '%',
                background: seg.color,
              }"
              :title="seg.label + ': ' + seg.count"
            />
          </div>
          <div class="bar-legend">
            <span v-for="seg in configBar" :key="seg.label" class="legend-item">
              <span class="legend-dot" :style="{ background: seg.color }" />
              {{ seg.label }}: {{ seg.count }}
            </span>
            <span class="legend-item legend-total">总计: {{ plan.config_map_projection.total }}</span>
          </div>
        </div>
        <div v-else class="empty-hint">无 config-map 投影数据</div>
      </div>

      <!-- Preserved -->
      <div class="card">
        <div class="card-title">保留文件 <span class="card-subtitle">{{ plan.preserved?.length ?? 0 }} 项</span></div>
        <div v-if="plan.preserved?.length">
          <div v-for="f in plan.preserved" :key="f" class="list-item">{{ f }}</div>
        </div>
        <div v-else class="empty-hint">无保留文件</div>
      </div>

      <!-- Prior Overrides -->
      <div class="card">
        <div class="card-title">Prior Overrides <span class="card-subtitle">{{ plan.prior_overrides?.length ?? 0 }} 项</span></div>
        <div v-if="plan.prior_overrides?.length">
          <div v-for="o in plan.prior_overrides" :key="o.env + o.service" class="list-item">
            <span class="tag tag-blue">{{ o.env }}</span>
            <span class="tag tag-slate">{{ o.service }}</span>
          </div>
        </div>
        <div v-else class="empty-hint">无 prior overrides</div>
      </div>

      <!-- Analyzer Hits -->
      <div class="card">
        <div class="card-title">Analyzer Hits <span class="card-subtitle">{{ plan.analyzer_hits?.length ?? 0 }} 项</span></div>
        <div v-if="plan.analyzer_hits?.length">
          <div v-for="h in plan.analyzer_hits" :key="h.service + h.env" class="list-item">
            <span class="tag tag-green">{{ h.service }}</span>
            <span v-if="h.env" class="tag tag-blue">{{ h.env }}</span>
            <span v-if="h.source" class="tag tag-slate">{{ h.source }}</span>
          </div>
        </div>
        <div v-else class="empty-hint">无 analyzer hits</div>
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

.btn-text {
  background: none;
  border: none;
  color: #3b82f6;
  font-size: 13px;
  cursor: pointer;
  padding: 4px 0;
  margin-top: 8px;
}
.btn-text:hover { text-decoration: underline; }

.error-banner {
  background: #fef2f2;
  border: 1px solid #fecaca;
  color: #dc2626;
  padding: 10px 14px;
  border-radius: 6px;
  font-size: 14px;
  margin-bottom: 20px;
}

.results { display: flex; flex-direction: column; gap: 16px; }

.card {
  background: #fff;
  border: 1px solid #e2e8f0;
  border-radius: 8px;
  padding: 16px 20px;
  box-shadow: 0 1px 3px rgba(0,0,0,0.04);
}

.card-title {
  font-size: 15px;
  font-weight: 600;
  color: #1e293b;
  margin-bottom: 12px;
}
.card-subtitle {
  font-size: 13px;
  font-weight: 400;
  color: #94a3b8;
  margin-left: 8px;
}

.info-row { display: flex; gap: 12px; margin-bottom: 6px; font-size: 14px; }
.info-label { color: #64748b; min-width: 120px; }
.info-value { color: #1e293b; font-weight: 500; }

.badge-group { display: flex; flex-wrap: wrap; gap: 8px; }
.badge {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  border-radius: 12px;
  font-size: 13px;
  font-weight: 500;
}
.badge-green { background: #dcfce7; color: #166534; }
.badge-gray { background: #f1f5f9; color: #64748b; }
.badge-reason { font-size: 11px; font-weight: 400; opacity: 0.8; }

.file-counts { display: flex; gap: 16px; margin-bottom: 4px; }
.fc { font-size: 14px; font-weight: 600; }
.fc-create { color: #16a34a; }
.fc-modify { color: #d97706; }
.fc-remove { color: #dc2626; }

.file-list {
  margin-top: 8px;
  max-height: 300px;
  overflow-y: auto;
  border: 1px solid #e2e8f0;
  border-radius: 6px;
  padding: 8px 12px;
  background: #f8fafc;
}
.file-item {
  font-family: 'SF Mono', 'Fira Code', 'Consolas', monospace;
  font-size: 12px;
  padding: 3px 0;
  line-height: 1.6;
}
.file-create { color: #16a34a; }
.file-modify { color: #d97706; }
.file-remove { color: #dc2626; }

.bar-chart { margin-top: 4px; }
.bar-track {
  display: flex;
  height: 24px;
  border-radius: 6px;
  overflow: hidden;
  background: #f1f5f9;
}
.bar-segment {
  min-width: 2px;
  transition: width 0.3s;
}
.bar-legend {
  display: flex;
  flex-wrap: wrap;
  gap: 16px;
  margin-top: 10px;
  font-size: 13px;
  color: #475569;
}
.legend-item { display: flex; align-items: center; gap: 6px; }
.legend-dot { width: 10px; height: 10px; border-radius: 50%; display: inline-block; }
.legend-total { font-weight: 600; color: #1e293b; }

.list-item {
  font-size: 13px;
  padding: 4px 0;
  color: #334155;
  display: flex;
  gap: 8px;
  align-items: center;
}

.tag {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 500;
}
.tag-blue { background: #dbeafe; color: #1d4ed8; }
.tag-green { background: #dcfce7; color: #166534; }
.tag-slate { background: #f1f5f9; color: #475569; }

.empty-hint { font-size: 13px; color: #94a3b8; font-style: italic; }
</style>
