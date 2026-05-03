<script setup lang="ts">
// GlobalReposRootBlock —— Step 5 顶部"全局默认 clone 父目录"小块。
// 它跟下面的 RepoListItem 列表是两件事:这个是 system-wide 偏好(写 ~/.tshoot/config.json),
// 列表里每行的本地路径才会进 yaml。拆开避免 Step 5 wrapper 越长越乱。

defineProps<{
  reposRootInput: string
  resolvedReposRoot: string
  globalDefaultReposRoot: string
  displayPath: (p: string) => string
}>()

defineEmits<{
  (e: 'pick'): void
  (e: 'save'): void
}>()
</script>

<template>
  <div class="global-default-block">
    <label class="global-default-label">
      🌐 默认 clone 父目录(全局)
      <span class="field-hint">
        — 远程仓库默认 clone 到 <code>&lt;这里&gt;/&lt;repo.name&gt;/</code>
        <span v-if="globalDefaultReposRoot" class="saved-indicator">✓ 已保存</span>
        <span v-else>(未设置 · 将使用 <code>{{ displayPath(resolvedReposRoot) }}</code>)</span>
      </span>
    </label>
    <div class="global-default-row">
      <input
        :value="displayPath(reposRootInput) || displayPath(resolvedReposRoot)"
        type="text"
        :placeholder="displayPath(resolvedReposRoot)"
        readonly
        class="path-readonly"
        :title="reposRootInput || resolvedReposRoot"
      />
      <button type="button" class="btn" @click="$emit('pick')">
        {{ reposRootInput ? '重新选…' : '选目录…' }}
      </button>
      <button
        type="button"
        class="btn"
        :disabled="!reposRootInput.trim() || reposRootInput.trim() === globalDefaultReposRoot"
        @click="$emit('save')"
        title="把当前路径写入 ~/.tshoot/config.json;下次打开 Studio 自动用"
      >💾 设为全局默认</button>
    </div>
  </div>
</template>
