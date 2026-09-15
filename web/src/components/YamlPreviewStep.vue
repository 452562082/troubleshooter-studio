<script setup lang="ts">
// YamlPreviewStep —— Step 9 yaml 预览 + 三连按钮(验证 / 复制 / 导出)。
// 父端把生成好的 yamlOutput 和按钮 loading 状态传进来,点击事件 emit 回去走原有逻辑。

import { ref } from 'vue'
const showConfig = ref(false)
import type { TargetId } from '../lib/constants'
import type { ResourceCoverage } from '../lib/resourceCoverage'
import ResourceCoveragePanel from './ResourceCoveragePanel.vue'

defineProps<{
  systemName?: string
  environmentNames?: string[]
  repoNames?: string[]
  yamlOutput: string
  validateLoading: boolean
  validateResult: { ok: boolean; message: string } | null
  copySuccess: boolean
  targetOptions: readonly TargetId[]
  enabledTargets: Record<string, boolean>
  targetLabels: Record<string, string>
  anyTargetSelected: boolean
  resourceCoverage: ResourceCoverage
}>()

defineEmits<{
  (e: 'validate'): void
  (e: 'copy'): void
  (e: 'download'): void
}>()
</script>

<template>
  <div class="card lg">
    <h2>确认机器人配置</h2>
    <p class="help-text">检查项目、环境和能力覆盖，确认后即可创建。</p>
    <dl class="wizard-summary">
      <div><dt>项目</dt><dd>{{ systemName || '当前项目' }}</dd></div>
      <div><dt>运行环境</dt><dd>{{ environmentNames?.join('、') || '尚未配置' }}</dd></div>
      <div><dt>代码仓库</dt><dd>{{ repoNames?.join('、') || '尚未配置' }}</dd></div>
    </dl>
    <div class="target-readonly-row">
      <span class="target-readonly-label">本次部署目标:</span>
      <span
        v-for="t in targetOptions"
        v-show="enabledTargets[t]"
        :key="t"
        class="target-readonly-chip"
      >{{ targetLabels[t] }}</span>
      <span v-if="!anyTargetSelected" class="error-text">
        请在“运行方式”选择至少一个 AI 平台
      </span>
    </div>
    <ResourceCoveragePanel v-if="resourceCoverage.rows.length" :coverage="resourceCoverage" />
    <p v-else class="help-text">当前已配置代码仓库，尚未识别具体服务。可返回“选择项目”扫描仓库，或先创建后继续补充。</p>
    <button type="button" class="btn link" :aria-expanded="showConfig" @click="showConfig = !showConfig">
      {{ showConfig ? '收起配置文件' : '高级：查看或导出配置文件' }}
    </button>
    <div v-if="showConfig" class="wizard-config-details">
    <div class="yaml-preview">
      <pre><code>{{ yamlOutput }}</code></pre>
    </div>
    <div class="portable-export-note">
      预览、复制和导出都包含当前明文凭据；仅未填写的字段保留占位符。请勿截图外传或提交到版本库。
    </div>
    <div class="action-bar">
      <button class="btn primary" :disabled="validateLoading" @click="$emit('validate')">
        {{ validateLoading ? '检查中…' : '检查配置格式' }}
      </button>
      <button class="btn" @click="$emit('copy')">
        {{ copySuccess ? '已复制 ✓' : '📋 复制到剪贴板' }}
      </button>
      <button class="btn" @click="$emit('download')">⬇ 导出可部署配置</button>
    </div>
    </div>
    <div v-if="validateResult" class="validate-result" :class="{ success: validateResult.ok, fail: !validateResult.ok }">
      {{ validateResult.message }}
    </div>
  </div>
</template>
