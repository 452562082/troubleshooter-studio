<script setup lang="ts">
// EnvListStep —— Step 4 环境列表外壳:标题 + 提示 + 多行 EnvListItem + 加号按钮。
// 不持有 environments / 探测态;父端 InitPage 仍是 owner,本组件只渲染并把行内事件冒上去。

import EnvListItem from './EnvListItem.vue'
import type { URLProbeState } from '../lib/probeTypes'

interface EnvItem {
  id: string
  api_domain: string
  web_domain: string
  is_prod: boolean
}

defineProps<{
  environments: EnvItem[]
  urlProbeResults: Record<string, URLProbeState>
  urlProbeKey: (envIdx: number, kind: 'api' | 'web') => string
  hasError: (key: string) => boolean
}>()

defineEmits<{
  (e: 'probe', envIdx: number, kind: 'api' | 'web', url: string): void
  (e: 'remove', envIdx: number): void
  (e: 'add'): void
}>()
</script>

<template>
  <div class="card lg">
    <h2>环境列表</h2>
    <p class="help-text">
      填写业务系统的运行环境（如 dev / test / prod）及访问入口。运行时定位统一在“可观测性”步骤配置。
    </p>
    <EnvListItem
      v-for="(env, i) in environments"
      :key="i"
      :env="env"
      :api-probe="urlProbeResults[urlProbeKey(i, 'api')]"
      :web-probe="urlProbeResults[urlProbeKey(i, 'web')]"
      :has-id-error="hasError(`env.${i}.id`)"
      :has-api-error="hasError(`env.${i}.api_domain`)"
      :disable-remove="environments.length <= 1"
      @probe="(kind, url) => $emit('probe', i, kind, url)"
      @remove="$emit('remove', i)"
    />
    <button class="btn add-environment-button" type="button" @click="$emit('add')">+ 添加环境</button>
  </div>
</template>

<style scoped>
.add-environment-button { margin-top: 2px; }
</style>
