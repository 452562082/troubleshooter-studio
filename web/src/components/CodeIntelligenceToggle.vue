<script setup lang="ts">
withDefaults(defineProps<{
  modelValue?: boolean
}>(), {
  modelValue: false,
})

const emit = defineEmits<{
  (e: 'update:modelValue', value: boolean): void
}>()
</script>

<template>
  <div class="code-intelligence-toggle">
    <label class="code-intelligence-toggle__control">
      <input
        type="checkbox"
        :checked="modelValue"
        aria-describedby="codegraph-disclosure"
        @change="emit('update:modelValue', ($event.target as HTMLInputElement).checked)"
      >
      <span>启用 CodeGraph 代码智能</span>
    </label>
    <div id="codegraph-disclosure" class="code-intelligence-toggle__disclosure">
      <p>首次会下载约 200 MB+ 的本地工具，并在可分析仓库创建或更新 <code>.codegraph/</code>。</p>
      <p>索引仅存本机；Studio 会关闭 CodeGraph telemetry。失败不影响部署，机器人会回退到 <code>git diff + rg + Read</code>。</p>
    </div>
  </div>
</template>

<style scoped>
.code-intelligence-toggle {
  margin-top: 16px;
  padding: 14px 16px;
  border: 1px solid #bfdbfe;
  border-radius: 8px;
  background: #eff6ff;
  color: #1e3a8a;
}

.code-intelligence-toggle__control {
  display: flex;
  min-height: 44px;
  align-items: center;
  gap: 10px;
  font-weight: 650;
  cursor: pointer;
}

.code-intelligence-toggle__control input {
  width: 18px;
  height: 18px;
  margin: 0;
  accent-color: #2563eb;
  cursor: pointer;
}

.code-intelligence-toggle__control input:focus-visible {
  outline: 3px solid rgba(37, 99, 235, 0.35);
  outline-offset: 3px;
}

.code-intelligence-toggle__disclosure {
  margin-left: 28px;
  color: #334155;
  font-size: 13px;
  line-height: 1.65;
}

.code-intelligence-toggle__disclosure p {
  margin: 2px 0;
}

.code-intelligence-toggle__disclosure code {
  color: #1e40af;
  font-size: 0.95em;
}

@media (max-width: 640px) {
  .code-intelligence-toggle {
    padding: 12px;
  }

  .code-intelligence-toggle__disclosure {
    margin-left: 0;
  }
}
</style>
