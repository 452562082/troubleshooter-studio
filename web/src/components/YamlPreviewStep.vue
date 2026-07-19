<script setup lang="ts">
// YamlPreviewStep —— Step 9 yaml 预览 + 三连按钮(验证 / 复制 / 导出)。
// 父端把生成好的 yamlOutput 和按钮 loading 状态传进来,点击事件 emit 回去走原有逻辑。

import type { TargetId } from '../lib/constants'
import type { ResourceCoverage } from '../lib/resourceCoverage'
import ResourceCoveragePanel from './ResourceCoveragePanel.vue'

defineProps<{
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
    <h2>预览 + 生成</h2>
    <div class="target-readonly-row">
      <span class="target-readonly-label">本次部署目标:</span>
      <span
        v-for="t in targetOptions"
        v-show="enabledTargets[t]"
        :key="t"
        class="target-readonly-chip"
      >{{ targetLabels[t] }}</span>
      <span v-if="!anyTargetSelected" class="error-text">
        Step 2 没勾选任何部署目标,无法生成产物
      </span>
    </div>
    <ResourceCoveragePanel :coverage="resourceCoverage" />
    <div class="yaml-preview">
      <pre><code>{{ yamlOutput }}</code></pre>
    </div>
    <div class="action-bar">
      <button class="btn primary" :disabled="validateLoading" @click="$emit('validate')">
        {{ validateLoading ? '验证中...' : '✓ 验证' }}
      </button>
      <button class="btn" @click="$emit('copy')">
        {{ copySuccess ? '已复制 ✓' : '📋 复制到剪贴板' }}
      </button>
      <button class="btn" @click="$emit('download')">⬇ 导出</button>
    </div>
    <div v-if="validateResult" class="validate-result" :class="{ success: validateResult.ok, fail: !validateResult.ok }">
      {{ validateResult.message }}
    </div>
  </div>
</template>
