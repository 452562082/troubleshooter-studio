<script setup lang="ts">
import { ref } from 'vue'
import { diff as bridgeDiff, isDesktop } from '../lib/bridge'

interface FileChange {
  path: string
  kind: 'added' | 'modified' | 'removed'
}

interface ConfigRowChange {
  env: string
  service: string
  old_status: string
  new_status: string
  kind: string
}

interface DiffResult {
  file_changes: FileChange[]
  config_row_changes: ConfigRowChange[]
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
  diffResult.value = null
  apiNotReady.value = false
}

const yaml = ref('')
const targetDir = ref('')
const loading = ref(false)
const error = ref('')
const diffResult = ref<DiffResult | null>(null)
const apiNotReady = ref(false)

function kindIcon(kind: string) {
  if (kind === 'added') return '+'
  if (kind === 'modified') return '~'
  return '-'
}

function isDowngrade(row: ConfigRowChange) {
  return row.old_status === 'verified' && row.new_status === 'inferred'
}

async function runDiff() {
  if (!yaml.value.trim() || !targetDir.value.trim()) return
  loading.value = true
  error.value = ''
  diffResult.value = null
  apiNotReady.value = false
  try {
    if (!isDesktop()) {
      apiNotReady.value = true
      return
    }
    diffResult.value = (await bridgeDiff(yaml.value, targetDir.value)) as unknown as typeof diffResult.value
  } catch (e: any) {
    error.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="page">
    <h1>差异对比</h1>

    <div class="info-box">
      <div class="info-box-title">使用说明</div>
      <div>Diff 对比现有产物与新一轮生成的差异，精确到文件级和 config-map 行级。verified→inferred 降级会标 &#x26A0; 警告</div>
    </div>

    <div class="input-section">
      <div class="input-header">
        <label class="label">system.yaml</label>
        <button class="btn small" @click="loadExample">加载示例</button>
      </div>
      <textarea
        v-model="yaml"
        class="yaml-input"
        placeholder="粘贴 system.yaml 内容..."
        spellcheck="false"
      />

      <label class="label label-mt">对比目标目录</label>
      <input
        v-model="targetDir"
        class="text-input"
        type="text"
        placeholder="已有输出目录的绝对路径，例如 /path/to/dist"
      />

      <button class="btn primary" :disabled="loading || !yaml.trim() || !targetDir.trim()" @click="runDiff">
        {{ loading ? '对比中...' : '执行对比' }}
      </button>
    </div>

    <!-- API not ready placeholder -->
    <div v-if="apiNotReady" class="alert info">
      <span class="placeholder-icon">&#x1F6A7;</span>
      <div>
        <div class="placeholder-title">API 开发中</div>
        <div class="placeholder-desc">diff 接口尚未实现，以下为 UI 预览布局。</div>
      </div>
    </div>

    <!-- Error -->
    <div v-if="error && !apiNotReady" class="alert error">{{ error }}</div>

    <!-- Preview: show sample layout when API not ready or when we have results -->
    <div v-if="apiNotReady" class="results preview-mode">
      <!-- Sample file changes -->
      <div class="card">
        <div class="card-title">文件变化（示例）</div>
        <div class="file-change added">
          <span class="change-icon">+</span>
          <span class="change-path">skills/routing/references/new-service.yaml</span>
        </div>
        <div class="file-change modified">
          <span class="change-icon">~</span>
          <span class="change-path">skills/routing/references/existing-service.yaml</span>
        </div>
        <div class="file-change removed">
          <span class="change-icon">-</span>
          <span class="change-path">skills/routing/references/old-service.yaml</span>
        </div>
      </div>

      <!-- Sample config-map row changes -->
      <div class="card">
        <div class="card-title">Config-Map 行变化（示例）</div>
        <table class="diff-table">
          <thead>
            <tr>
              <th>Env</th>
              <th>Service</th>
              <th>旧状态</th>
              <th>新状态</th>
              <th>Kind</th>
            </tr>
          </thead>
          <tbody>
            <tr>
              <td>prod</td>
              <td>order-svc</td>
              <td><span class="status-tag status-verified">verified</span></td>
              <td><span class="status-tag status-verified">verified</span></td>
              <td>unchanged</td>
            </tr>
            <tr class="row-downgrade">
              <td>staging</td>
              <td>pay-svc</td>
              <td><span class="status-tag status-verified">verified</span></td>
              <td><span class="status-tag status-inferred">inferred</span></td>
              <td>
                <span class="downgrade-warn">&#x26A0;</span> downgrade
              </td>
            </tr>
            <tr>
              <td>dev</td>
              <td>user-svc</td>
              <td><span class="status-tag status-none">-</span></td>
              <td><span class="status-tag status-inferred">inferred</span></td>
              <td>added</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- Real results -->
    <div v-if="diffResult" class="results">
      <div class="card">
        <div class="card-title">文件变化</div>
        <div
          v-for="fc in diffResult.file_changes"
          :key="fc.path"
          :class="['file-change', fc.kind]"
        >
          <span class="change-icon">{{ kindIcon(fc.kind) }}</span>
          <span class="change-path">{{ fc.path }}</span>
        </div>
        <div v-if="!diffResult.file_changes?.length" class="empty-hint">无文件变化</div>
      </div>

      <div class="card">
        <div class="card-title">Config-Map 行变化</div>
        <table v-if="diffResult.config_row_changes?.length" class="diff-table">
          <thead>
            <tr>
              <th>Env</th>
              <th>Service</th>
              <th>旧状态</th>
              <th>新状态</th>
              <th>Kind</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="(row, i) in diffResult.config_row_changes"
              :key="i"
              :class="{ 'row-downgrade': isDowngrade(row) }"
            >
              <td>{{ row.env }}</td>
              <td>{{ row.service }}</td>
              <td>
                <span :class="['status-tag', 'status-' + row.old_status]">{{ row.old_status }}</span>
              </td>
              <td>
                <span :class="['status-tag', 'status-' + row.new_status]">{{ row.new_status }}</span>
              </td>
              <td>
                <span v-if="isDowngrade(row)" class="downgrade-warn">&#x26A0;</span>
                {{ row.kind }}
              </td>
            </tr>
          </tbody>
        </table>
        <div v-else class="empty-hint">无 config-map 行变化</div>
      </div>
    </div>
  </div>
</template>

<style scoped>

.input-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  /* 同 PlanPage:给 header 行到 textarea 留 6px,参考 AnalyzePage .label-row */
  margin-bottom: 6px;
}
.input-header .label { margin-bottom: 0; }
.label-mt { margin-top: 14px; }

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

.text-input {
  width: 100%;
  padding: 10px 12px;
  font-size: 14px;
  border: 1px solid #cbd5e1;
  border-radius: 6px;
  background: #f8fafc;
  color: #1e293b;
}
.text-input:focus { outline: none; border-color: #3b82f6; box-shadow: 0 0 0 3px rgba(59,130,246,0.1); }

.results { display: flex; flex-direction: column; gap: 16px; }
.preview-mode { opacity: 0.75; }

.file-change {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 5px 0;
  font-size: 13px;
}
.change-icon {
  width: 20px;
  height: 20px;
  border-radius: 4px;
  display: flex;
  align-items: center;
  justify-content: center;
  font-weight: 700;
  font-size: 14px;
  flex-shrink: 0;
}
.change-path {
  font-family: 'SF Mono', 'Fira Code', 'Consolas', monospace;
  font-size: 12px;
  color: #334155;
}
.file-change.added .change-icon { background: #dcfce7; color: #16a34a; }
.file-change.modified .change-icon { background: #fef3c7; color: #d97706; }
.file-change.removed .change-icon { background: #fee2e2; color: #dc2626; }

.diff-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.diff-table th {
  text-align: left;
  padding: 8px 12px;
  background: #f8fafc;
  color: #64748b;
  font-weight: 600;
  border-bottom: 2px solid #e2e8f0;
  font-size: 12px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
}
.diff-table td {
  padding: 8px 12px;
  border-bottom: 1px solid #f1f5f9;
  color: #334155;
}
.diff-table tbody tr:hover { background: #f8fafc; }

.status-tag {
  display: inline-block;
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 12px;
  font-weight: 500;
}
.status-verified { background: #dcfce7; color: #166534; }
.status-inferred { background: #fef3c7; color: #92400e; }
.status-none { background: #f1f5f9; color: #94a3b8; }

.row-downgrade { background: #fffbeb; }
.downgrade-warn { color: #d97706; font-size: 15px; margin-right: 4px; }

.empty-hint { font-size: 13px; color: #94a3b8; font-style: italic; }
</style>
