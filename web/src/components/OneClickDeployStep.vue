<script setup lang="ts">
// OneClickDeployStep —— Step 10 一键部署面板:列出本次会装到的目标 + 总按钮。
// deploySummary / deployLoading / deployError 都在父端,本组件只渲染 + 转发 click。

import type { TargetId } from '../lib/constants'
import type { CodeGraphIndexReport } from '../lib/bridge/install'

interface DeploySummaryItem {
  target: TargetId
  label: string
  path: string
}

withDefaults(defineProps<{
  deploySummary: DeploySummaryItem[]
  deployLoading: boolean
  deployError: string | null
  // deployProgressLine 后端透过来的最近一行进度(主要是 mcp-grafana 下载状态)。
  // 部署中(deployLoading=true)时显示在按钮下方,告诉用户"在干嘛"避免误判卡死。
  deployProgressLine?: string
  codeGraphReport?: CodeGraphIndexReport | null
  codeGraphRetrying?: boolean
  codeGraphRetryFeedback?: string
  codeGraphRetryState?: 'idle' | 'loading' | 'success' | 'error'
}>(), {
  deployProgressLine: '',
  codeGraphReport: null,
  codeGraphRetrying: false,
  codeGraphRetryFeedback: '',
  codeGraphRetryState: 'idle',
})

defineEmits<{
  (e: 'run-deploy'): void
  (e: 'retry-codegraph'): void
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
          :disabled="deployLoading || codeGraphRetrying || deploySummary.length === 0"
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

    <section v-if="codeGraphReport" class="codegraph-report" aria-labelledby="codegraph-report-title">
      <div class="codegraph-report__heading">
        <h3 id="codegraph-report-title">
          CodeGraph {{ codeGraphReport.ready }}/{{ codeGraphReport.total }} repos ready
        </h3>
        <button
          v-if="codeGraphReport.ready < codeGraphReport.total"
          type="button"
          class="btn codegraph-report__retry"
          data-test="retry-codegraph"
          :disabled="codeGraphRetrying || deployLoading"
          @click="$emit('retry-codegraph')"
        >
          {{ codeGraphRetrying ? '重新索引中…' : '重新索引失败仓库' }}
        </button>
      </div>

      <ul class="codegraph-repos" aria-label="CodeGraph 仓库索引状态">
        <li v-for="repo in codeGraphReport.repos" :key="`${repo.name}:${repo.path}`" class="codegraph-repo">
          <div class="codegraph-repo__topline">
            <strong>{{ repo.name }}</strong>
            <span class="codegraph-status" :class="`codegraph-status--${repo.status}`" :data-status="repo.status">
              {{ repo.status }}
            </span>
            <span class="codegraph-action">{{ repo.action }}</span>
          </div>
          <div v-if="repo.path" class="codegraph-repo__path">{{ repo.path }}</div>
          <div class="codegraph-repo__metrics">
            <span v-if="repo.file_count !== undefined">{{ repo.file_count }} files</span>
            <span v-if="repo.node_count !== undefined">{{ repo.node_count }} nodes</span>
            <span v-if="repo.edge_count !== undefined">{{ repo.edge_count }} edges</span>
          </div>
          <p v-if="repo.detail" class="codegraph-repo__detail">{{ repo.detail }}</p>
        </li>
      </ul>

      <div
        class="codegraph-retry-feedback"
        :class="`codegraph-retry-feedback--${codeGraphRetryState}`"
        data-test="codegraph-retry-feedback"
        aria-live="polite"
        aria-atomic="true"
      >{{ codeGraphRetryFeedback || '\u00a0' }}</div>
    </section>
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

.codegraph-report {
  margin-top: 20px;
  padding-top: 18px;
  border-top: 1px solid #e2e8f0;
}

.codegraph-report__heading {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
}

.codegraph-report__heading h3 {
  margin: 0;
  color: #0f172a;
  font-size: 16px;
}

.codegraph-report__retry {
  min-height: 44px;
  cursor: pointer;
}

.codegraph-report__retry:focus-visible {
  outline: 3px solid rgba(37, 99, 235, 0.35);
  outline-offset: 2px;
}

.codegraph-report__retry:disabled {
  cursor: not-allowed;
}

.codegraph-repos {
  display: grid;
  gap: 8px;
  margin: 12px 0 0;
  padding: 0;
  list-style: none;
}

.codegraph-repo {
  min-width: 0;
  padding: 10px 12px;
  border: 1px solid #e2e8f0;
  border-radius: 7px;
  background: #f8fafc;
}

.codegraph-repo__topline,
.codegraph-repo__metrics {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.codegraph-status,
.codegraph-action {
  padding: 2px 7px;
  border-radius: 999px;
  font-size: 12px;
  line-height: 1.5;
}

.codegraph-status--ready {
  background: #dcfce7;
  color: #166534;
}

.codegraph-status--skipped {
  background: #e2e8f0;
  color: #334155;
}

.codegraph-status--warn {
  background: #fef3c7;
  color: #92400e;
}

.codegraph-action {
  background: #dbeafe;
  color: #1e40af;
}

.codegraph-repo__path {
  margin-top: 5px;
  overflow-wrap: anywhere;
  color: #64748b;
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}

.codegraph-repo__metrics {
  margin-top: 5px;
  color: #475569;
  font-size: 12px;
}

.codegraph-repo__detail {
  margin: 6px 0 0;
  color: #7c2d12;
  font-size: 13px;
  line-height: 1.5;
}

.codegraph-retry-feedback {
  min-height: 21px;
  margin-top: 8px;
  color: #475569;
  font-size: 13px;
  line-height: 21px;
}

.codegraph-retry-feedback--success {
  color: #166534;
}

.codegraph-retry-feedback--error {
  color: #b91c1c;
}

@media (max-width: 640px) {
  .codegraph-report__heading,
  .codegraph-report__retry {
    width: 100%;
  }

  .codegraph-report__retry {
    justify-content: center;
  }
}

@media (prefers-reduced-motion: reduce) {
  .deploy-progress-dot {
    animation: none;
  }
}
</style>
