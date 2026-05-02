<script setup lang="ts">
// URLProbeBadge —— 4 态(idle / loading / ok / fail)的连通性探测徽章。
// 用于:
//   - Step 3 EnvListItem 的 api / web 域名探测
//   - Step 8 obsProbeResults 的可观测性工具连通性
//   - 任何其它走 URLProbeState 形状的探测面
// idle / undefined → 不渲染 anything(让父端布局保持紧凑)。

import type { URLProbeState } from '../lib/probeTypes'

defineProps<{
  state: URLProbeState | undefined
}>()
</script>

<template>
  <span v-if="state?.status === 'loading'" class="url-probe-badge loading">测试中…</span>
  <span v-else-if="state?.status === 'ok'" class="url-probe-badge ok" :title="state.detail">✓ {{ state.latency }}</span>
  <span v-else-if="state?.status === 'fail'" class="url-probe-badge fail" :title="state.error">✗ {{ state.error }}</span>
</template>
