<script setup lang="ts">
// Identity is maintained by the wizard, including draft restoration and import.
defineProps<{
  system: { name: string; id: string; description: string }
  hasError: (key: string) => boolean
}>()
</script>

<template>
  <section class="card lg">
    <h2>项目名称</h2>
    <p class="help-text">从仓库名称自动填入，也可以改成更熟悉的中文名称。</p>
    <div class="form-group">
      <label for="wizard-system-name">显示名称 <span class="required">*</span></label>
      <input id="wizard-system-name" v-model="system.name" placeholder="例如：订单系统" :class="{ error: hasError('system.name') }" />
    </div>
    <details class="wizard-advanced" >
      <summary>项目描述与自动标识</summary>
      <p class="help-text">内部标识：<code data-test="system-id">{{ system.id || '填写项目名称后自动生成' }}</code> · 自动维护，无需填写</p>
      <div class="form-group">
        <label for="wizard-system-description">项目描述（选填）</label>
        <textarea id="wizard-system-description" v-model="system.description" rows="2" placeholder="简单介绍这个项目" />
      </div>
    </details>
  </section>
</template>
