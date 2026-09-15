<script setup lang="ts">
// RepoServiceChips —— 仓库的服务名 chip 列表 + inline "+" 输入。
// 父端持有 svcAddInputs[index] reactive,本组件 v-model 直接绑(允许父端读出当前输入)。

defineProps<{
  repo: { _scanning?: boolean; role?: string }
  index: number
  /** 已识别 / 用户已加的服务名 */
  serviceNames: string[]
  /** 父端 svcAddInputs reactive map(以 index 为 key) */
  svcAddInputs: Record<number, string>
}>()

const emit = defineEmits<{
  removeServiceName: [r: any, svc: string]
  addServiceName: [r: any, idx: number]
}>()
</script>

<template>
  <div class="form-group">
    <label>
      {{ repo.role === 'frontend' ? '前端运行时服务名' : '服务名' }}
      <span
        class="help-icon"
        :title="repo.role === 'frontend'
          ? '用于前端仓库与 Web 域名、K8s Deployment、日志和调用链的映射；不会进入配置中心或数据层扫描。首次默认使用仓库名，删除全部服务名可停用这些运行时映射。'
          : 'config-map 以此为 key。扫描会自动识别(monorepo 列所有子模块);识别不全时点 + 手动补,不想要的点 ✕ 删。'"
      >?</span>
      <span v-if="serviceNames.length" class="field-hint">
        — {{ serviceNames.length }} 个(✕ 删 / + 补)
      </span>
      <span v-else class="field-hint">
        {{ repo.role === 'frontend' ? '(已停用运行时映射，添加服务名可重新启用)' : '(扫一下自动填,或点下方 + 手动补)' }}
      </span>
    </label>
    <div v-if="repo._scanning" class="service-chips-row">
      <span class="auto-scanning"><span class="scan-spinner-mini"></span>扫描中…</span>
    </div>
    <div v-else class="service-chips-row">
      <span
        v-for="svc in serviceNames"
        :key="svc"
        class="service-chip"
      >
        <span class="service-chip-name">{{ svc }}</span>
        <button
          type="button"
          class="service-chip-x"
          :title="`删除 ${svc}`"
          @click="emit('removeServiceName', repo, svc)"
        >✕</button>
      </span>
      <span class="service-chip-add">
        <input
          v-model="svcAddInputs[index]"
          type="text"
          :placeholder="serviceNames.length ? '+ 补一个服务名' : '+ 手填服务名'"
          @keydown.enter.prevent="emit('addServiceName', repo, index)"
        />
        <button
          type="button"
          class="service-chip-add-btn"
          :disabled="!(svcAddInputs[index] || '').trim()"
          title="添加(Enter 也行;逗号/空格分隔可一次加多个)"
          @click="emit('addServiceName', repo, index)"
        >+</button>
      </span>
    </div>
  </div>
</template>
