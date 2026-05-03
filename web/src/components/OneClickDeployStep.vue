<script setup lang="ts">
// OneClickDeployStep —— Step 10 一键部署面板:列出本次会装到的目标 + 总按钮。
// deploySummary / deployLoading / deployError 都在父端,本组件只渲染 + 转发 click。

import type { TargetId } from '../lib/constants'

interface DeploySummaryItem {
  target: TargetId
  label: string
  path: string
}

defineProps<{
  deploySummary: DeploySummaryItem[]
  deployLoading: boolean
  deployError: string | null
}>()

defineEmits<{
  (e: 'run-deploy'): void
}>()
</script>

<template>
  <div class="card lg">
    <h2>一键部署</h2>
    <p class="help-text" style="margin-bottom:14px;">
      按 Step 2 勾选的目标一次性部署,直接复用前面填的凭证,<strong>跑完即生效</strong>。
      OpenClaw 若有字段前面没填,会回退到「已装机器人」页让你补齐。
    </p>
    <div v-if="deploySummary.length === 0" class="alert warn">
      Step 2 没勾选任何部署目标,无法一键部署。请回 Step 2 至少勾选一个 AI 平台。
    </div>
    <div v-else class="deploy-final-block">
      <div class="deploy-targets-line">
        将部署到:
        <span v-for="(item, i) in deploySummary" :key="item.target">
          <span class="deploy-target-chip">{{ item.label }}</span><span v-if="i < deploySummary.length - 1">、</span>
        </span>
      </div>
      <div class="deploy-inline-actions">
        <button
          type="button"
          class="btn primary deploy-final-btn"
          :disabled="deployLoading || deploySummary.length === 0"
          @click="$emit('run-deploy')"
        >
          {{ deployLoading ? '部署中…' : `🚀 部署到 ${deploySummary.length} 个目标` }}
        </button>
      </div>
      <div v-if="deployError" class="alert error">{{ deployError }}</div>
    </div>
  </div>
</template>
