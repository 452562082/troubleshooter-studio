<script setup lang="ts">
// AnalyzeRepoFindingsCard —— AnalyzePage 单仓扫描结果卡。
// 包括:仓库 head(名 + stack + verified 标)+ 服务名 chips + 配置中心 findings 列表 +
// notes(中性事实)+ warnings(异常)。
//
// 父端 v-for=repo in result.report.repos,把每条 repo 透过来。本组件无内部状态。

interface Finding {
  source_file: string
  data_id?: string
  namespace_id?: string
  group?: string
  app_id?: string
  kv_prefix?: string
  env_profile?: string
}
interface RepoReport {
  name: string
  stack?: string
  verified?: boolean
  service_names?: string[]
  findings?: Finding[]
  notes?: string[]
  warnings?: string[]
}

defineProps<{
  repo: RepoReport
}>()
</script>

<template>
  <div class="card">
    <div class="card-header">
      <span class="name">{{ repo.name }}</span>
      <span v-if="repo.stack" class="tag gray">{{ repo.stack }}</span>
      <span v-if="repo.verified" class="tag green">verified</span>
    </div>

    <div v-if="repo.service_names?.length" class="detail">
      <strong>扫到的服务名:</strong>
      <span v-for="s in repo.service_names" :key="s" class="tag blue">{{ s }}</span>
    </div>

    <div v-if="repo.findings?.length" class="detail">
      <strong>配置中心线索({{ repo.findings.length }} 条):</strong>
      <div v-for="(f, i) in repo.findings" :key="i" class="finding">
        <span class="src">{{ f.source_file }}</span>
        <span v-if="f.data_id" class="kv">dataId={{ f.data_id }}</span>
        <span v-if="f.namespace_id" class="kv">namespace={{ f.namespace_id }}</span>
        <span v-if="f.group" class="kv">group={{ f.group }}</span>
        <span v-if="f.app_id" class="kv">appId={{ f.app_id }}</span>
        <span v-if="f.kv_prefix" class="kv">前缀={{ f.kv_prefix }}</span>
        <span v-if="f.env_profile" class="tag orange">{{ f.env_profile }}</span>
      </div>
    </div>

    <!-- Notes:扫描发现的中性事实(框架、API URL、build tool 等),跟 Warnings 分开渲染 -->
    <div v-if="repo.notes?.length" class="detail">
      <strong>📋 扫描发现({{ repo.notes.length }} 条):</strong>
      <div v-for="n in repo.notes" :key="n" class="note-line">{{ n }}</div>
    </div>

    <div v-if="repo.warnings?.length" class="detail warn">
      <strong>⚠ 异常提示:</strong>
      <div v-for="w in repo.warnings" :key="w" class="warn-line">{{ w }}</div>
    </div>

    <div v-if="!repo.findings?.length && !repo.warnings?.length && !repo.notes?.length" class="detail muted">
      没扫到配置中心线索,也没异常提示
    </div>
  </div>
</template>

<style scoped>
.card { border: 1px solid var(--c-border, #e2e8f0); border-radius: 8px; padding: 14px 16px; margin-bottom: 12px; background: #fff; }
.card-header { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 8px; }

.tag { display: inline-block; padding: 3px 10px; border-radius: 12px; font-size: 12px; font-weight: 500; }
.tag.blue { background: #dbeafe; color: #1e40af; }
.tag.green { background: #d1fae5; color: #065f46; }
.tag.orange { background: #fef3c7; color: #92400e; }
.tag.gray { background: #f1f5f9; color: #475569; }

.name { font-weight: 700; color: #1e293b; font-size: 15px; }

.detail { margin-bottom: 8px; font-size: 13px; color: #475569; }
.detail strong { color: #334155; margin-right: 6px; }
.detail.muted { color: #94a3b8; font-style: italic; }
.detail.warn { color: #92400e; }

.finding {
  display: flex; flex-wrap: wrap; gap: 6px; padding: 4px 0; border-bottom: 1px solid #f1f5f9; align-items: center;
}
.src { font-family: monospace; font-size: 12px; color: #3b82f6; }
.kv { font-family: monospace; font-size: 12px; background: #f1f5f9; padding: 1px 6px; border-radius: 3px; }

.warn-line { font-size: 12px; padding: 2px 0; }
.note-line {
  font-size: 12px; padding: 2px 0;
  color: #475569; font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
}
</style>
