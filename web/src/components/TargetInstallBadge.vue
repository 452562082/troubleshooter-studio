<script setup lang="ts">
// TargetInstallBadge —— Step 2 部署目标卡上的"已装/未装"小徽章。
// 把 4 家 target 各自的 v-if 块(claude-code / cursor / codex 走 aitoolsResult,
// openclaw 走 openclawDetectStatus)统一成一个组件,父端把 detect 结果归一成
// detected/versionText/title 三个 prop 传进来。
//
// detected:
//   true   → 显示 "✓ vX" / "✓ 已装"
//   false  → 显示 "⚠ 未检测到"
//   null   → 显示 "扫描中…"(loading 态);父端没拿到结果时也传 null
//   undef  → 完全不渲染(对应原来"openclawDetectStatus === 'idle'" 不出 badge)

defineProps<{
  detected: boolean | null | undefined
  /** 检测到时的版本文案,如 "1.2.3";空时显示 "已装"。仅 detected=true 用 */
  versionText?: string
  /** 鼠标悬停 title;通常是 detect note 或 path */
  title?: string
}>()
</script>

<template>
  <span
    v-if="detected !== undefined"
    class="target-install-badge"
    :class="detected ? 'ok' : 'miss'"
    :title="title || ''"
  >
    <template v-if="detected === true">
      {{ versionText ? `✓ v${versionText}` : '✓ 已装' }}
    </template>
    <template v-else-if="detected === null">扫描中…</template>
    <template v-else>⚠ 未检测到</template>
  </span>
</template>
