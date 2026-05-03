<script setup lang="ts">
// YamlErrorCard —— EditorPage 验证失败时的友好错误展示。
// 把后端 `parse yaml: line N` / `validate: <field> required` 等不同档错误识别成
// 三档(yaml-syntax / field-missing / field-invalid),分别给行号 / 字段路径 / 翻译文案。

import { computed } from 'vue'

interface ParsedError {
  kind: 'yaml-syntax' | 'field-missing' | 'field-invalid' | 'unknown'
  lineNumber?: number
  fieldPath?: string
  detail: string
  sourceLine?: string
}

const props = defineProps<{
  /** 原始错误消息(后端传来) */
  errorMsg: string
  /** 当前 yaml 内容,yaml-syntax 错时用来抽出问题行的上下文 */
  yamlContent: string
}>()

defineEmits<{
  /** 父端订阅这个事件可拿到 lineNumber,用于在 textarea gutter 高亮对应行 */
  parsed: [parsed: ParsedError | null]
}>()

const parsed = computed<ParsedError | null>(() => {
  if (!props.errorMsg) return null
  const raw = props.errorMsg

  // 档 1:yaml 语法错
  const yamlLineMatch = raw.match(/yaml:\s*line\s+(\d+):\s*(.+)/)
  if (yamlLineMatch) {
    const lineNum = parseInt(yamlLineMatch[1], 10)
    const lines = props.yamlContent.split('\n')
    return {
      kind: 'yaml-syntax',
      lineNumber: lineNum,
      detail: translateYamlError(yamlLineMatch[2]),
      sourceLine: lines[lineNum - 1] || '',
    }
  }

  // 档 2 & 3:validate: <field> required / must match / ...
  const validateMatch = raw.match(/validate:\s*(.+)/)
  if (validateMatch) {
    const body = validateMatch[1]
    const pathMatch = body.match(/^([\w.[\]-]+)\s+(.*)$/)
    if (pathMatch) {
      const field = pathMatch[1]
      const rest = pathMatch[2]
      if (rest.startsWith('required')) {
        return { kind: 'field-missing', fieldPath: field, detail: translateSchemaError(rest) }
      }
      return { kind: 'field-invalid', fieldPath: field, detail: translateSchemaError(rest) }
    }
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

defineExpose({ parsed })
</script>

<template>
  <div v-if="parsed" class="err-card" :class="'kind-' + parsed.kind">
    <div class="err-header">
      <span class="err-icon">✖</span>
      <span class="err-kind-label">
        {{
          parsed.kind === 'yaml-syntax' ? 'YAML 语法错' :
          parsed.kind === 'field-missing' ? '字段缺失' :
          parsed.kind === 'field-invalid' ? '字段非法' : '验证失败'
        }}
      </span>
      <span v-if="parsed.lineNumber" class="err-locator">第 {{ parsed.lineNumber }} 行</span>
      <span v-else-if="parsed.fieldPath" class="err-locator">字段 <code>{{ parsed.fieldPath }}</code></span>
    </div>
    <div class="err-body">{{ parsed.detail }}</div>
    <pre v-if="parsed.sourceLine" class="err-source"><code>{{ parsed.lineNumber }} | {{ parsed.sourceLine }}</code></pre>
    <details class="err-raw">
      <summary>原始错误信息</summary>
      <pre><code>{{ errorMsg }}</code></pre>
    </details>
  </div>
</template>

<style scoped>
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
.err-raw { font-size: 11px; color: #991b1b; }
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
</style>
