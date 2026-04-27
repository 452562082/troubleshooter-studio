<script setup lang="ts">
// EditorPage 定位:YAML 调试沙盒。只做两件事:
//   1. 验证:检查 yaml 语法 + 字段合法性,错误指出行/字段
//   2. 生成计划:干跑 gen,看会生成什么 skill/文件/config-map 投影
//
// 不做:
//   - 不做"执行生成"(桌面端 CWD = .app bundle 里,产物写进 bundle 下次被覆盖,坑)
//   - 不做"一键部署"(BotsPage 的"导入 YAML 一键部署"已经覆盖相同场景,这里重复就删了)
// 真要部署:在 BotsPage 导入 yaml 或去创建向导走 Step 7 一键部署。
//
// 验证错误显示增强:原先只把 raw error 字符串丢出去,用户看"parse yaml: yaml: line 5: ..."
// 不友好。现在解析错误文本:yaml 语法错按"第 N 行"高亮,schema 错按"字段 xxx"高亮,
// 并且把当前行源码截一段展示,让用户更快定位到问题。
import { computed, nextTick, ref, watch } from 'vue'
import { plan as bridgePlan, validate as bridgeValidate } from '../lib/bridge'

const exampleYaml = `system:
  id: demo
  name: "Demo"
  description: "示例系统"

agent:
  name: "Demo排障机器人"
  workspace_name: demo-bot
  model: anthropic/claude-sonnet-4-6

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

// ── 行号 gutter ──
// textarea 本身不支持行号,左边做个 <div class="gutter"> 同步滚动显示行号。
// 错误行高亮 + 验证失败时自动滚到那行。
const textareaRef = ref<HTMLTextAreaElement | null>(null)
const gutterRef = ref<HTMLDivElement | null>(null)

const lineCount = computed<number>(() => {
  // 至少 1 行(空 yaml 也显示 "1")。split('\n') 在末尾有换行时会多出空元素,
  // 这不影响行号显示,但我们要保证数量跟 textarea 里的行数一致。
  const text = yamlContent.value
  if (!text) return 1
  return text.split('\n').length
})

function onTextareaScroll() {
  if (textareaRef.value && gutterRef.value) {
    gutterRef.value.scrollTop = textareaRef.value.scrollTop
  }
}

// 当验证失败时,自动滚 textarea 到出错行,让用户不用手动找。
// computed parsedError 改变时触发(见下方)。
watch(
  () => errorMsg.value,
  async () => {
    await nextTick()
    if (!textareaRef.value || !parsedError.value?.lineNumber) return
    const line = parsedError.value.lineNumber
    // 19.5 = font-size 13px * line-height 1.5。粗糙估算,只要落在视口内就好。
    const lineHeight = 19.5
    // 定位到错误行 - 3 让它出现在视口上沿附近,别贴顶,留点上下文
    const targetTop = Math.max(0, (line - 3) * lineHeight)
    textareaRef.value.scrollTop = targetTop
    if (gutterRef.value) gutterRef.value.scrollTop = targetTop
  },
)

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
    // 控制台打全栈,方便用户截图给我看;errorMsg 给 UI 展示
    console.error('[EditorPage]', endpoint, '调用失败:', e)
    errorMsg.value = e?.message || String(e) || `${endpoint} 调用失败,请看控制台`
  } finally {
    loading.value = ''
  }
}

// ── 错误诊断 ──
// config.LoadFromBytes 返回的错误格式大概三档:
//   1. "parse yaml: yaml: line 5: mapping values are not allowed in this context"
//      → 语法错,可以抽出 line N
//   2. "validate: system.id required" / "validate: repos[shop].url required"
//      → schema 错,抽出字段路径
//   3. "validate: system.id must match [a-z0-9][a-z0-9-]*, got \"Shop\""
//      → 格式错,带当前值
//
// 把这三档识别出来分别展示,比甩一串英文给用户友好得多。
interface ParsedError {
  kind: 'yaml-syntax' | 'field-missing' | 'field-invalid' | 'unknown'
  lineNumber?: number  // yaml-syntax 有
  fieldPath?: string   // field-* 有 (如 "system.id" / "repos[shop].url")
  detail: string       // 原始错误消息(翻译友好点)
  sourceLine?: string  // yaml-syntax 的上下文,从 yamlContent 里截第 N 行
}

const parsedError = computed<ParsedError | null>(() => {
  if (!errorMsg.value) return null
  const raw = errorMsg.value

  // 档 1:yaml 语法错
  const yamlLineMatch = raw.match(/yaml:\s*line\s+(\d+):\s*(.+)/)
  if (yamlLineMatch) {
    const lineNum = parseInt(yamlLineMatch[1], 10)
    const lines = yamlContent.value.split('\n')
    return {
      kind: 'yaml-syntax',
      lineNumber: lineNum,
      detail: translateYamlError(yamlLineMatch[2]),
      // yaml 库报的行号是 1-based,array 是 0-based
      sourceLine: lines[lineNum - 1] || '',
    }
  }

  // 档 2 & 3:validate: <field> required / must match / ...
  const validateMatch = raw.match(/validate:\s*(.+)/)
  if (validateMatch) {
    const body = validateMatch[1]
    // field 路径:system.id / agent.name / repos[0].name / repos[shop].url /
    //           environments[0].id / ...
    const pathMatch = body.match(/^([\w.[\]-]+)\s+(.*)$/)
    if (pathMatch) {
      const field = pathMatch[1]
      const rest = pathMatch[2]
      if (rest.startsWith('required')) {
        return { kind: 'field-missing', fieldPath: field, detail: translateSchemaError(rest) }
      }
      return { kind: 'field-invalid', fieldPath: field, detail: translateSchemaError(rest) }
    }
    // "duplicate environment id: dev" 这种,没有 field 前缀
    return { kind: 'field-invalid', detail: translateSchemaError(body) }
  }

  return { kind: 'unknown', detail: raw }
})

function translateYamlError(msg: string): string {
  // yaml 库的几条常见错信息翻译成人话
  if (msg.includes('mapping values are not allowed in this context')) {
    return '缩进或冒号错位:这一行前面可能少了 `-`(数组项)或多了空格'
  }
  if (msg.includes('did not find expected key')) {
    return '缺少字段名或缩进不对齐:检查上下行的对齐'
  }
  if (msg.includes('could not find expected')) {
    return '语法截断:引号 / 方括号 / 花括号没闭合'
  }
  if (msg.includes('found character that cannot start any token')) {
    return '有非法字符:可能是全角符号或多余的制表符'
  }
  return msg
}

function translateSchemaError(msg: string): string {
  if (msg === 'required') return '是必填字段,请补上'
  if (msg.startsWith('must match')) return `格式不合法 —— ${msg}`
  if (msg.startsWith('duplicate')) return `重复的 id/name ${msg}`
  if (msg.includes('references unknown env')) return `引用了不存在的 env(检查 environments 里有没有对应 id)`
  return msg
}
</script>

<template>
  <div class="page">
    <h1>System YAML 调试器</h1>

    <div class="info-box">
      <div class="info-box-title">YAML 调试沙盒</div>
      <div>
        把已有的 system.yaml 粘进来做快速检查 —— 不会真生成、也不会装到机器人。<br/>
        <strong>✓ 验证</strong>:语法 / 必填字段 / 格式问题,出错时直接定位到行号或字段;
        <strong>📋 生成计划</strong>:试着跑一遍生成,看会启用哪些技能、产出多少文件、配置中心映射几条。<br/>
        想真装到机器人?去 <router-link to="/bots">已装机器人</router-link> 导入 yaml 部署,
        或从 <router-link to="/init">创建向导</router-link> 末步一键部署。
      </div>
    </div>

    <div class="toolbar">
      <label class="btn">
        加载文件
        <input type="file" accept=".yaml,.yml" hidden @change="loadFile" />
      </label>
      <button class="btn" @click="loadExample">加载示例</button>
      <button class="btn primary" :disabled="!!loading" @click="apiCall('validate', '验证')">
        ✓ 验证
      </button>
      <button class="btn primary" :disabled="!!loading" @click="apiCall('plan', '生成计划')">
        📋 生成计划
      </button>
    </div>

    <div class="editor-wrap" :class="{ 'has-error': errorMsg }">
      <div ref="gutterRef" class="gutter" aria-hidden="true">
        <div
          v-for="n in lineCount"
          :key="n"
          class="gutter-line"
          :class="{ err: n === parsedError?.lineNumber }"
        >{{ n }}</div>
      </div>
      <textarea
        ref="textareaRef"
        v-model="yamlContent"
        class="yaml-editor"
        placeholder="# 在此粘贴或加载 system.yaml 内容..."
        spellcheck="false"
        @scroll="onTextareaScroll"
      />
    </div>

    <div v-if="successMsg" class="alert success">{{ successMsg }}</div>

    <!-- 错误卡片:分档展示,比裸 raw error 友好 -->
    <div v-if="parsedError" class="err-card" :class="'kind-' + parsedError.kind">
      <div class="err-header">
        <span class="err-icon">✖</span>
        <span class="err-kind-label">
          {{
            parsedError.kind === 'yaml-syntax' ? 'YAML 语法错' :
            parsedError.kind === 'field-missing' ? '字段缺失' :
            parsedError.kind === 'field-invalid' ? '字段非法' : '验证失败'
          }}
        </span>
        <span v-if="parsedError.lineNumber" class="err-locator">第 {{ parsedError.lineNumber }} 行</span>
        <span v-else-if="parsedError.fieldPath" class="err-locator">字段 <code>{{ parsedError.fieldPath }}</code></span>
      </div>
      <div class="err-body">{{ parsedError.detail }}</div>
      <!-- yaml 语法错把问题行截出来给用户看上下文 -->
      <pre v-if="parsedError.sourceLine" class="err-source"><code>{{ parsedError.lineNumber }} | {{ parsedError.sourceLine }}</code></pre>
      <details class="err-raw">
        <summary>原始错误信息</summary>
        <pre><code>{{ errorMsg }}</code></pre>
      </details>
    </div>

    <!-- Plan result -->
    <div v-if="resultData && resultTitle === '生成计划'" class="result-card">
      <h2>生成计划:{{ resultData.system }}</h2>
      <div class="result-grid">
        <div class="result-section">
          <h3>会启用的技能 ({{ resultData.skills_included?.length || 0 }})</h3>
          <ul v-if="resultData.skills_included?.length">
            <li v-for="s in resultData.skills_included" :key="s.name">
              <strong>{{ s.name }}</strong>
              <span v-if="s.reason" class="sub-text"> — {{ s.reason }}</span>
            </li>
          </ul>
          <p v-else class="sub-text">无</p>
        </div>
        <div class="result-section">
          <h3>会跳过的技能 ({{ resultData.skills_skipped?.length || 0 }})</h3>
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
          <h3>会产出的文件</h3>
          <p><span class="badge badge-green">新建 {{ resultData.files_create?.length || 0 }}</span></p>
          <p><span class="badge badge-blue">改动 {{ resultData.files_modify?.length || 0 }}</span></p>
          <p><span class="badge badge-red">删除 {{ resultData.files_remove?.length || 0 }}</span></p>
          <p><span class="badge badge-gray">保留 {{ resultData.preserved?.length || 0 }}</span></p>
        </div>
        <div class="result-section">
          <h3>配置中心映射条数</h3>
          <table class="mini-table">
            <tr><td>仓库扫描得到</td><td>{{ resultData.config_map_projection?.verified_from_analyzer ?? 0 }}</td></tr>
            <tr><td>用户手填</td><td>{{ resultData.config_map_projection?.verified_from_prior ?? 0 }}</td></tr>
            <tr><td>规则推断</td><td>{{ resultData.config_map_projection?.inferred ?? 0 }}</td></tr>
            <tr><td><strong>总计</strong></td><td><strong>{{ resultData.config_map_projection?.total ?? 0 }}</strong></td></tr>
          </table>
        </div>
      </div>
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


/* ── 编辑器 + 行号 gutter ── */
/* 结构:.editor-wrap 横向 flex, .gutter 固定宽, textarea 占剩余空间
 * 两者 line-height + font-size 必须完全一致,gutter 滚动跟着 textarea 同步
 * (onTextareaScroll 做的),这样行号跟正文对齐。 */
.editor-wrap {
  display: flex;
  min-height: 500px;
  background: #f8fafc;
  border: 1px solid #e2e8f0;
  border-radius: 6px;
  overflow: hidden;              /* 让 gutter 圆角跟随外框 */
  transition: border-color 0.15s;
  resize: vertical;
  /* Firefox/Chrome 都能让 flex 容器 resize */
  min-height: 500px;
}
.editor-wrap:focus-within { border-color: #3b82f6; }
.editor-wrap.has-error { border-color: #ef4444; }

.gutter {
  flex: 0 0 auto;
  min-width: 40px;
  padding: 12px 8px 12px 10px;
  background: #f1f5f9;
  border-right: 1px solid #e2e8f0;
  color: #94a3b8;
  font-family: 'SF Mono', 'Fira Code', 'Cascadia Code', monospace;
  font-size: 13px;
  line-height: 1.5;
  text-align: right;
  user-select: none;
  overflow: hidden;              /* scrollTop 通过 JS 同步,不自己滚 */
  font-variant-numeric: tabular-nums;
}
.gutter-line {
  height: 1.5em;                 /* 跟 textarea line-height 对齐 */
  white-space: nowrap;
}
.gutter-line.err {
  color: #991b1b; font-weight: 700; background: #fee2e2;
  margin: 0 -8px 0 -10px; padding: 0 8px 0 10px;  /* 背景吃满 gutter 宽度 */
}

.yaml-editor {
  flex: 1;
  min-height: 500px;
  background: transparent;
  border: none;
  padding: 12px 16px;
  font-family: 'SF Mono', 'Fira Code', 'Cascadia Code', monospace;
  font-size: 13px;
  line-height: 1.5;
  color: #1e293b;
  resize: none;
  outline: none;
}

/* ── 错误诊断卡片 ── */
.err-card {
  margin-top: 12px;
  padding: 14px 16px;
  background: #fef2f2;
  border: 1px solid #fecaca;
  border-left: 4px solid #dc2626;
  border-radius: 6px;
  color: #7f1d1d;
}
.err-header {
  display: flex; align-items: center; gap: 10px;
  margin-bottom: 8px;
  font-size: 13px; font-weight: 600;
}
.err-icon { color: #dc2626; font-size: 15px; }
.err-kind-label { color: #991b1b; }
.err-locator {
  margin-left: auto; padding: 2px 8px;
  background: #fee2e2; border-radius: 10px;
  font-size: 11px; font-weight: 500; color: #7f1d1d;
  font-variant-numeric: tabular-nums;
}
.err-locator code {
  background: transparent; padding: 0; color: inherit;
  font-family: 'SF Mono', monospace;
}
.err-body {
  color: #7f1d1d; font-size: 13px; line-height: 1.6;
  margin-bottom: 8px;
}
.err-source {
  background: #1e293b; color: #fbbf24;
  padding: 10px 12px; border-radius: 4px;
  font-family: 'SF Mono', monospace; font-size: 12px;
  margin-bottom: 8px;
  white-space: pre-wrap; word-break: break-all;
}
.err-raw {
  font-size: 11px; color: #991b1b;
}
.err-raw summary {
  cursor: pointer; user-select: none;
  font-weight: 500; padding: 4px 0;
}
.err-raw pre {
  background: #fff; border: 1px solid #fecaca; border-radius: 4px;
  padding: 8px 10px; font-family: 'SF Mono', monospace;
  white-space: pre-wrap; word-break: break-all;
  margin-top: 4px; color: #7f1d1d;
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
