<script setup lang="ts">
// AnalyzeDiffCard —— AnalyzePage "yaml vs 代码实态" 对照卡。
// 左上角 tag 概览 + 顶部"复制片段 / 应用到沙盒"两个动作 + 每仓库一行 missing/new 详情。
// diff 计算逻辑保留在 lib/yamlCodeDiff.ts;本组件只负责展示 + 触发回调。

import type { YamlVsCodeDiff } from '../lib/yamlCodeDiff'

defineProps<{
  diff: YamlVsCodeDiff
  /** role → 中文说明,用于非业务服务行的提示文案 */
  nonServiceRoleHint: (role: string) => string
}>()

defineEmits<{
  copySnippet: []
  applyToYaml: []
}>()
</script>

<template>
  <div class="card diff-card">
    <div class="card-header">
      <span class="name">⚖️ 对照 troubleshooter.yaml</span>
      <span v-if="diff.totalNew > 0" class="tag green">代码有但 yaml 没写 {{ diff.totalNew }} 项</span>
      <span v-if="diff.totalMissing > 0" class="tag orange">yaml 写了但代码没扫到 {{ diff.totalMissing }} 项</span>
      <span v-if="diff.configCenterMismatch" class="tag red">配置中心对不上</span>
      <span v-if="diff.totalNew === 0 && diff.totalMissing === 0 && !diff.configCenterMismatch" class="tag green">完全一致</span>
      <div class="apply-actions" style="margin-left:auto; display:flex; gap:6px;">
        <button
          v-if="diff.totalNew > 0"
          class="btn small"
          title="把建议的 yaml 片段复制到剪贴板,贴回 troubleshooter.yaml 的 repos 下就能补全 service_names"
          @click="$emit('copySnippet')"
        >
          📋 复制补丁片段
        </button>
        <button
          class="btn small primary"
          title="把代码扫描发现的 service_names / config_center 差异合并到 yaml,跳到 YAML 沙盒做验证"
          @click="$emit('applyToYaml')"
        >
          ✨ 应用到 YAML 沙盒
        </button>
      </div>
    </div>

    <div v-if="diff.configCenterMismatch" class="detail warn">
      <strong>⚠ 配置中心类型对不上:</strong>
      yaml 写的是 <code>{{ diff.configCenterYaml || '(空)' }}</code>,代码里实际扫到 <code>{{ diff.configCenterCode }}</code>。
      回 yaml 把 <code>infrastructure.config_center.type</code> 改一下。
    </div>

    <div v-for="r in diff.repos" :key="r.name" class="diff-row">
      <div class="diff-row-head">
        <strong>{{ r.name }}</strong>
        <span v-if="r.isServiceRole" class="muted">yaml 写了 {{ r.yamlServices.length }} 个服务 · 代码里扫到 {{ r.codeServices.length }} 个</span>
        <span v-else class="muted">
          <span class="tag gray">{{ r.effectiveRole }}</span>
          不参与服务对账
        </span>
      </div>
      <template v-if="r.isServiceRole">
        <div v-if="r.newInCode.length" class="detail">
          <span class="tag green" style="min-width: 110px;">代码里多出来的</span>
          <span v-for="s in r.newInCode" :key="s" class="tag blue">{{ s }}</span>
        </div>
        <div v-if="r.missingInCode.length" class="detail">
          <span class="tag orange" style="min-width: 110px;">yaml 写了但没扫到</span>
          <span v-for="s in r.missingInCode" :key="s" class="tag gray">{{ s }}</span>
        </div>
        <div v-if="r.newInCode.length === 0 && r.missingInCode.length === 0" class="detail muted">
          ✓ 完全一致
        </div>
      </template>
      <template v-else>
        <div class="detail muted skip-row">
          ℹ️ {{ nonServiceRoleHint(r.effectiveRole) }}
        </div>
      </template>
    </div>
  </div>
</template>

<style scoped>
/* yaml vs 代码 diff 卡:红框不合适(不是错),用蓝色强调,tag 并排展示差异 */
.card { border: 1px solid var(--c-border, #e2e8f0); border-radius: 8px; padding: 14px 16px; margin-bottom: 12px; background: #fff; }
.card-header { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 8px; }
.diff-card { border-left: 4px solid #3b82f6; background: #eff6ff; }

.tag { display: inline-block; padding: 3px 10px; border-radius: 12px; font-size: 12px; font-weight: 500; }
.tag.blue { background: #dbeafe; color: #1e40af; }
.tag.green { background: #d1fae5; color: #065f46; }
.tag.orange { background: #fef3c7; color: #92400e; }
.tag.gray { background: #f1f5f9; color: #475569; }
.tag.red { background: #fee2e2; color: #991b1b; }

.name { font-weight: 700; color: #1e293b; font-size: 15px; }

.detail { margin-bottom: 8px; font-size: 13px; color: #475569; }
.detail strong { color: #334155; margin-right: 6px; }
.detail.muted { color: #94a3b8; font-style: italic; }
.detail.warn { color: #92400e; }

.muted { color: #64748b; font-size: 12px; }

.diff-row {
  margin-top: 10px; padding-top: 10px;
  border-top: 1px dashed #bfdbfe;
}
.diff-row-head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 6px; }
.diff-row-head strong { color: #1e40af; }
.diff-row-head .muted { font-size: 12px; color: #64748b; }
.diff-row .detail { display: flex; align-items: center; flex-wrap: wrap; gap: 6px; margin-bottom: 4px; }
.diff-row .skip-row { color: #64748b; font-style: italic; padding: 4px 0; }
</style>
