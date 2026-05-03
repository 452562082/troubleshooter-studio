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
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { genPreview, isDesktop, plan as bridgePlan, validate as bridgeValidate } from '../lib/bridge'
import type { GenPreviewResult } from '../lib/bridge'
import { useYamlFileLoader } from '../lib/useYamlFileLoader'
import { useAsyncStatus } from '../lib/useAsyncStatus'
import HealthIssuesCard, { type HealthIssue } from '../components/HealthIssuesCard.vue'
import PlanResultCard from '../components/PlanResultCard.vue'
import PreviewResultCard from '../components/PreviewResultCard.vue'
import YamlErrorCard from '../components/YamlErrorCard.vue'

// 跟"代码扫描"页对接的 localStorage key:AnalyzePage 用户点"应用到 YAML 沙盒"时
// 把改写过的 yaml 内容塞进这个 key 然后跳到本页;本页 onMounted 读出来 → 自动填进
// textarea + 显示一行 banner 让用户知道是从代码扫描带过来的。读完即清,避免下次进来又弹。
const PENDING_FROM_ANALYZE_KEY = 'tsf-pending-yaml-from-analyze'
// 显示 banner:用户从代码扫描带过来的 yaml,告诉他"已应用",可以直接验证。
const fromAnalyzeBanner = ref(false)

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
  targets: [openclaw]
  skills_whitelist: [routing, config-executor, redis-runtime-query, diagram-generator]
  preserve_on_regenerate: [SOUL.md]

meta:
  schema_version: "0.1"
`

const yamlContent = ref('')

// 进页面时检查"代码扫描应用过来的 yaml":有就自动填 + 弹 banner。读完即清。
onMounted(() => {
  try {
    const pending = localStorage.getItem(PENDING_FROM_ANALYZE_KEY)
    if (pending) {
      yamlContent.value = pending
      fromAnalyzeBanner.value = true
      localStorage.removeItem(PENDING_FROM_ANALYZE_KEY)
    }
  } catch { /* localStorage 异常不阻塞页面 */ }
})
// loading / errorMsg / successMsg + 异步样板由 useAsyncStatus composable 提供
const { loading, errorMsg, successMsg, run: runAsync, reset: resetAsync } = useAsyncStatus()
const resultTitle = ref('')
const resultData = ref<any>(null)

// 验证按钮的额外发现:HealthCheck 出来的 warn/info/error 列表,验证通过后展示。
// 跟 errorMsg 不同,issues 不阻断后续操作,仅供配置完整度提醒。
const validateIssues = ref<HealthIssue[]>([])

// 产物预览结果:跑一次 GenPreview 把生成的所有文件读回来。
// PreviewResultCard 组件内部管理 activePath / 文件分组 / 大小格式化。
const previewResult = ref<GenPreviewResult | null>(null)

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
// errorLineNumber 由 YamlErrorCard 通过 @parsed 事件回传。
watch(
  () => errorLineNumber.value,
  async (line) => {
    await nextTick()
    if (!textareaRef.value || !line) return
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
  validateIssues.value = []
}

// 桌面走 osascript 原生弹窗;浏览器走 <input type=file> + FileReader。
const { loadFileNative, loadFileBrowser } = useYamlFileLoader({
  onLoaded: (content) => {
    yamlContent.value = content
    resetAsync(() => {
      resultData.value = null
      validateIssues.value = []
    })
  },
  onError: (msg) => { errorMsg.value = msg },
})

// EditorPage 异步操作清旧本地态(resultData / preview) + 走 useAsyncStatus 的 run。
function clearLocal() {
  resultData.value = null
  resultTitle.value = ''
  validateIssues.value = []
  previewResult.value = null
}

async function apiCall(endpoint: 'validate' | 'plan', label: string) {
  await runAsync(label, async () => {
    if (endpoint === 'validate') {
      const r = await bridgeValidate(yamlContent.value)
      const errs = (r.issues || []).filter((i: HealthIssue) => i.severity === 'error').length
      const warns = (r.issues || []).filter((i: HealthIssue) => i.severity === 'warn').length
      const infos = (r.issues || []).filter((i: HealthIssue) => i.severity === 'info').length
      let extra = ''
      if (errs + warns + infos > 0) {
        const parts: string[] = []
        if (errs) parts.push(`${errs} 个矛盾`)
        if (warns) parts.push(`${warns} 个缺口`)
        if (infos) parts.push(`${infos} 条提示`)
        extra = ` | 健康检查:${parts.join(' / ')}`
      }
      successMsg.value = `验证通过！系统: ${r.system} (${r.name}) | ${r.envs} 个环境 | ${r.repos} 个仓库${extra}`
      validateIssues.value = (r.issues || []) as HealthIssue[]
    } else {
      resultTitle.value = label
      resultData.value = await bridgePlan(yamlContent.value)
    }
  }, clearLocal)
}

// 产物预览:真跑一次 generator 到 tmp,把所有产物文件读回来。
// 文件树渲染 + activePath / fmtSize 都搬到 PreviewResultCard 内部。
async function runPreview() {
  await runAsync('预览产物', async () => {
    previewResult.value = await genPreview(yamlContent.value)
  }, clearLocal)
}

// ── Plan 文件目录树 / 健康检查面板 / 错误诊断 都搬到子组件:
//   <PlanResultCard :data="resultData" />
//   <HealthIssuesCard :issues="validateIssues" />
//   <YamlErrorCard :error-msg="errorMsg" :yaml-content="yamlContent" />
// 子组件内部各自管 collapse 状态 + 排序 + 翻译,EditorPage 只透 raw 数据。

// 给 textarea gutter 高亮用的"yaml 语法错行号"
const errorLineNumber = ref<number | null>(null)
function onErrorParsed(p: { lineNumber?: number } | null) {
  errorLineNumber.value = p?.lineNumber ?? null
}
</script>

<template>
  <div class="page">
    <h1>YAML 沙盒</h1>

    <div v-if="fromAnalyzeBanner" class="alert success" style="margin-bottom: 12px;">
      ✓ 已把代码扫描的发现合并到下方 yaml(service_names / config-center 字段已更新)。
      点"<strong>✓ 验证</strong>"确认后,回「创建向导」末步部署,或在本页直接导出。
      <button type="button" class="btn-link" @click="fromAnalyzeBanner = false" style="float:right">知道了</button>
    </div>

    <div class="info-box">
      <div class="info-box-title">YAML 沙盒 — 只读校验 yaml,不动代码、不装机器人</div>
      <div class="info-box-body">
        <p class="info-box-lead">粘贴 system.yaml,快速判断「语法是否正确 / 部署后会是什么样」。</p>
        <ul class="info-box-actions">
          <li>
            <strong>✓ 验证</strong>
            <span>—— 语法 / 必填字段 / 格式校验,叠加配置健康检查(可观测性 wiring、仓库覆盖 env、多源 config_source、被静默跳过的 skill 等)</span>
          </li>
          <li>
            <strong>📋 生成计划</strong>
            <span>—— 干跑生成器,产出"启用哪些 skill、多少文件、配置映射几条"摘要,不写盘</span>
          </li>
          <li>
            <strong>📂 预览产物</strong>
            <span>—— 真跑生成器到临时目录,逐文件展开 SKILL.md / scripts 实际内容</span>
          </li>
        </ul>
        <p class="info-box-redirect">
          想<strong>扫代码反推 yaml</strong> → <router-link to="/analyze">代码扫描</router-link>;
          想<strong>真装到机器人</strong> → <router-link to="/bots">已装机器人</router-link> 或 <router-link to="/init">创建向导</router-link> 末步一键部署。
        </p>
      </div>
    </div>

    <div class="toolbar">
      <button v-if="isDesktop()" class="btn" @click="loadFileNative">加载文件</button>
      <label v-else class="btn">
        加载文件
        <input type="file" accept=".yaml,.yml" hidden @change="loadFileBrowser" />
      </label>
      <button class="btn" @click="loadExample">加载示例</button>
      <button class="btn primary" :disabled="!!loading" @click="apiCall('validate', '验证')">
        ✓ 验证
      </button>
      <button class="btn primary" :disabled="!!loading" @click="apiCall('plan', '生成计划')">
        📋 生成计划
      </button>
      <button v-if="isDesktop()" class="btn primary" :disabled="!!loading" @click="runPreview">
        📂 预览产物
      </button>
    </div>

    <div class="editor-wrap" :class="{ 'has-error': errorMsg }">
      <div ref="gutterRef" class="gutter" aria-hidden="true">
        <div
          v-for="n in lineCount"
          :key="n"
          class="gutter-line"
          :class="{ err: n === errorLineNumber }"
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

    <HealthIssuesCard :issues="validateIssues" />

    <YamlErrorCard
      :error-msg="errorMsg"
      :yaml-content="yamlContent"
      @parsed="onErrorParsed"
    />

    <PlanResultCard
      v-if="resultData && resultTitle === '生成计划'"
      :data="resultData"
    />

    <PreviewResultCard
      v-if="previewResult"
      :result="previewResult"
    />
  </div>
</template>

<style scoped>

.info-box-body {
  font-size: 13px;
  line-height: 1.65;
}
.info-box-lead {
  margin: 0 0 10px;
  color: var(--c-text);
}
.info-box-actions {
  list-style: none;
  margin: 0 0 12px;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.info-box-actions li {
  display: flex;
  align-items: baseline;
  gap: 8px;
  padding: 4px 0;
}
.info-box-actions li strong {
  flex-shrink: 0;
  min-width: 92px;
  font-weight: 600;
  color: var(--c-ink);
}
.info-box-actions li span {
  color: var(--c-text);
  font-size: 12.5px;
}
.info-box-redirect {
  margin: 0;
  padding-top: 10px;
  border-top: 1px dashed var(--c-line);
  font-size: 12.5px;
  color: var(--c-muted);
}
.info-box-redirect strong { color: var(--c-text); }
.info-box-redirect a { color: var(--c-accent); text-decoration: none; }
.info-box-redirect a:hover { text-decoration: underline; }

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

/* 错误诊断卡 / Plan / Preview / 健康检查 4 段 CSS 已搬到对应子组件;.badge 系列和 .sub-text 上提到 design.css */
</style>
