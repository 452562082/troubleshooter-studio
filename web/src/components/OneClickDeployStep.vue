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
  // deployProgressLine 后端透过来的最近一行进度(主要是 mcp-grafana 下载状态)。
  // 部署中(deployLoading=true)时显示在按钮下方,告诉用户"在干嘛"避免误判卡死。
  deployProgressLine?: string
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
      <div v-if="deployLoading && deployProgressLine" class="deploy-progress-line">
        <span class="deploy-progress-dot" />
        <span class="deploy-progress-text">{{ deployProgressLine }}</span>
      </div>
      <div v-if="deployError" class="alert error">{{ deployError }}</div>
    </div>
  </div>
</template>

<style scoped>
.deploy-progress-line {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 10px;
  padding: 8px 12px;
  background: #f5f7fa;
  border-radius: 6px;
  font-size: 13px;
  color: #555;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
.deploy-progress-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: #4a90e2;
  flex-shrink: 0;
  animation: deploy-pulse 1.2s ease-in-out infinite;
}
.deploy-progress-text {
  flex: 1;
  word-break: break-all;
}
@keyframes deploy-pulse {
  0%, 100% { opacity: 0.4; }
  50% { opacity: 1; }
}
</style>
