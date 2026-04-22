<script setup lang="ts">
import { computed, ref } from 'vue'
import yaml from 'js-yaml'
import { useRouter } from 'vue-router'
import {
  importAndDeploy,
  isDesktop,
  openDir,
  plan as bridgePlan,
  validate as bridgeValidate,
} from '../lib/bridge'
import { toast } from '../lib/toast'
import { useDeployPath } from '../lib/useDeployPath'

const router = useRouter()

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

async function apiCall(endpoint: 'validate' | 'plan', label: string) {
  errorMsg.value = ''
  successMsg.value = ''
  resultData.value = null
  resultTitle.value = ''
  loading.value = label

  try {
    if (endpoint === 'validate') {
      const r = await bridgeValidate(yamlContent.value)
      successMsg.value = `验证通过！系统: ${r.system} (${r.name}) | ${r.envs} 个环境 | ${r.repos} 个仓库`
    } else {
      resultTitle.value = label
      resultData.value = await bridgePlan(yamlContent.value)
    }
  } catch (e: any) {
    errorMsg.value = e.message || String(e)
  } finally {
    loading.value = ''
  }
}

// ── 一键部署 ──
// 之前这里还有个"执行生成"按钮调 gen(yaml, '') 写到 ./dist/<id>-<target>。
// 桌面端的坑:.app bundle 启动时 CWD 是 MacOS/ 目录,产物会写进 bundle 里
// 下次 make desktop-app 就被覆盖,用户找不到产物。删那个按钮,改成跟 InitPage
// Step 7 同款的一键部署:显式选 target + destPath,走 importAndDeploy,部署后跳 /bots。
const deployTarget = ref<'openclaw' | 'claude-code' | 'cursor' | 'standalone'>('openclaw')
const deployDestPath = ref('')
const deployLoading = ref(false)
const deployError = ref<string | null>(null)

// 从编辑器里的 yaml 解出 system.id 当默认路径计算基准。解析失败兜空串。
const systemIdFromYaml = computed<string>(() => {
  try {
    const parsed: any = yaml.load(yamlContent.value)
    return parsed?.system?.id || ''
  } catch { return '' }
})

const { isManagedTarget, customPathExpanded, autoDefaultPath, resetCustomPath } = useDeployPath(
  deployTarget,
  systemIdFromYaml,
  deployDestPath,
)

async function pickDeployDestPath() {
  if (!isDesktop()) { deployError.value = '选目录需要桌面 app 环境'; return }
  try {
    const p = await openDir('选择部署目标路径')
    if (p) deployDestPath.value = p
  } catch (e: any) {
    deployError.value = String(e?.message || e)
  }
}

async function runOneClickDeploy() {
  deployError.value = null
  if (!yamlContent.value.trim()) { deployError.value = '请先填 yaml'; return }
  if (!deployDestPath.value.trim()) { deployError.value = '请选部署目标路径'; return }
  if (!isDesktop()) {
    deployError.value = '一键部署只在桌面 app 可用;浏览器模式请去已装机器人页导入'
    return
  }
  // 先 validate,让失败落在前端 toast 而不是后端半路爆
  try {
    await bridgeValidate(yamlContent.value)
  } catch (e: any) {
    deployError.value = `yaml 校验失败: ${String(e?.message || e)};先点"验证"修复`
    return
  }
  deployLoading.value = true
  try {
    await importAndDeploy(yamlContent.value, deployTarget.value, deployDestPath.value)
    toast.success(`部署完成,已写到 ${deployDestPath.value}`)
    router.push('/bots')
  } catch (e: any) {
    deployError.value = String(e?.message || e)
  } finally {
    deployLoading.value = false
  }
}
</script>

<template>
  <div class="page">
    <h1>System YAML 编辑器</h1>

    <div class="info-box">
      <div class="info-box-title">YAML 沙盒 — 调试用</div>
      <div>拿来快速试改 yaml:验证语法 + 预演 gen 结果(skill 列表 / 文件变化 / config-map 投影)。真要部署到机器人用下方「一键部署」,或去 <router-link to="/bots">已装机器人</router-link> 导入。</div>
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
      <!-- 原有的"执行生成"已删 —— 它调 gen(yaml, '') 写到 ./dist/<id>,桌面端 CWD 是
           .app bundle 里的 MacOS/,产物进 bundle 下次 make desktop-app 就覆盖,用户找不到。
           想真落盘部署走下面的"一键部署",显式选 destPath 避免 CWD 坑。 -->
    </div>

    <textarea
      v-model="yamlContent"
      class="yaml-editor"
      :class="{ 'has-error': errorMsg }"
      placeholder="# 在此粘贴或加载 system.yaml 内容..."
      spellcheck="false"
    />

    <div v-if="successMsg" class="alert success">{{ successMsg }}</div>
    <div v-if="errorMsg" class="alert error">{{ errorMsg }}</div>

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

    <!-- 一键部署:替代原"执行生成"。显式选 target + destPath,绕开 CWD 坑 -->
    <div class="deploy-inline">
      <div class="deploy-inline-title">🚀 一键部署</div>
      <p class="help-text" style="margin-bottom:10px;">
        调通 yaml 后直接装到机器人。跟「已装机器人页 → 导入 yaml 一键部署」走同一条后端,装完跳到那里看新装的卡。
      </p>
      <div class="deploy-inline-row">
        <div class="deploy-inline-field">
          <label>目标平台</label>
          <select v-model="deployTarget" :disabled="deployLoading">
            <option value="openclaw">OpenClaw</option>
            <option value="claude-code">Claude Code</option>
            <option value="cursor">Cursor IDE</option>
            <option value="standalone">Standalone</option>
          </select>
        </div>
        <!-- standalone/openclaw:Studio 托管,显示默认路径 + 折叠"自定义" -->
        <div v-if="isManagedTarget && !customPathExpanded" class="deploy-inline-field flex auto-path-field">
          <label>部署位置 <span class="auto-tag">自动管理</span></label>
          <div class="auto-path-display">
            <code>{{ autoDefaultPath || '…' }}</code>
            <button type="button" class="btn-link" @click="customPathExpanded = true">自定义 →</button>
          </div>
        </div>
        <div v-else class="deploy-inline-field flex">
          <label>
            部署目标路径
            <button v-if="isManagedTarget" type="button" class="btn-link" @click="resetCustomPath">恢复默认</button>
          </label>
          <div class="deploy-inline-path">
            <input
              v-model="deployDestPath"
              type="text"
              :placeholder="isManagedTarget ? autoDefaultPath : '项目根路径(如 ~/my-project)'"
              :disabled="deployLoading"
            />
            <button type="button" class="btn" :disabled="deployLoading" @click="pickDeployDestPath">选目录…</button>
          </div>
        </div>
      </div>
      <div class="deploy-inline-actions">
        <button
          type="button"
          class="btn primary"
          :disabled="deployLoading || !deployDestPath.trim() || !yamlContent.trim()"
          @click="runOneClickDeploy"
        >
          {{ deployLoading ? '部署中…' : '一键部署' }}
        </button>
      </div>
      <div v-if="deployError" class="alert error">{{ deployError }}</div>
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

/* ── 一键部署块(跟 InitPage Step 7 同款样式) ── */
.deploy-inline {
  margin-top: 18px; padding: 16px 18px;
  background: #eff6ff; border: 1px solid #bfdbfe; border-radius: 8px;
}
.deploy-inline-title {
  font-weight: 600; color: #1e40af; margin-bottom: 4px; font-size: 14px;
}
.deploy-inline-row { display: flex; gap: 12px; margin-bottom: 10px; flex-wrap: wrap; }
.deploy-inline-field { display: flex; flex-direction: column; gap: 4px; min-width: 180px; }
.deploy-inline-field.flex { flex: 1; }
.deploy-inline-field label { font-size: 12px; font-weight: 600; color: #334155; }
.deploy-inline-field select,
.deploy-inline-path input {
  padding: 7px 10px; border: 1px solid #cbd5e1; border-radius: 6px; font-size: 13px;
}
.deploy-inline-path { display: flex; gap: 6px; }
.deploy-inline-path input { flex: 1; font-family: monospace; }
.deploy-inline-actions { display: flex; justify-content: flex-end; }
.help-text { color: var(--c-muted); font-size: var(--fs-sm); line-height: 1.6; }

/* standalone/openclaw 自动路径展示,跟 InitPage Step 7 同款 */
.auto-path-field label { display: flex; align-items: center; gap: 6px; }
.auto-tag {
  font-size: 10px; font-weight: 500; color: #065f46;
  background: #d1fae5; padding: 1px 6px; border-radius: 8px; letter-spacing: 0.2px;
}
.auto-path-display {
  display: flex; align-items: center; gap: 10px;
  padding: 7px 10px; background: var(--c-surf-3); border-radius: 6px;
  border: 1px dashed var(--c-line-2);
}
.auto-path-display code {
  flex: 1; font-size: 12px; color: #1e40af; background: transparent; padding: 0;
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.btn-link {
  padding: 0; border: none; background: transparent; color: #1e40af;
  font-size: 11px; font-weight: 500; cursor: pointer; font-family: inherit;
  text-decoration: underline; text-decoration-style: dotted; text-underline-offset: 3px;
}
.btn-link:hover { color: #1e3a8a; }

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
