<script setup lang="ts">
// WelcomeStep —— Step 1 欢迎页:两个大按钮(从零开始 / 导入 yaml)。
// 不持有任何状态,纯转发用户选择给 InitPage。

defineEmits<{
  (e: 'start'): void
  (e: 'import'): void
}>()
</script>

<template>
  <div class="card lg welcome-card">
    <h2>开始创建机器人</h2>
    <p class="help-text" style="margin-bottom: 24px;">
      本向导帮你生成 system.yaml 配置 + 一键部署到 OpenClaw / Claude Code / Cursor / Codex CLI。
      选下方任一方式开始 —— 草稿会自动保存,中途可关闭 app 下次继续。
    </p>
    <div class="welcome-choices">
      <button
        type="button"
        class="welcome-choice"
        @click="$emit('start')"
      >
        <div class="welcome-choice-icon">✏️</div>
        <div class="welcome-choice-text">
          <strong>从零开始配置</strong>
          <span>按表单一步步填,默认值已经按主流约定预设好</span>
        </div>
      </button>
      <button
        type="button"
        class="welcome-choice"
        @click="$emit('import')"
      >
        <div class="welcome-choice-icon">📥</div>
        <div class="welcome-choice-text">
          <strong>导入已有 system.yaml</strong>
          <span>有 yaml 文件直接反填到向导,继续编辑调整</span>
        </div>
      </button>
    </div>
  </div>
</template>
