<script setup lang="ts">
// ServiceChecklist —— Step 5 主源 + 副源都用的"本环境包含的服务"勾选清单。
// 每个 chip = 1 个服务,勾上 = 把它路由到当前 sourceID。
//
// 父端拥有 serviceSourceMap reactive(svc → sourceID),getServiceSource / setServiceSource
// 由父端实现,子组件只渲染 + 触发 emit。

defineProps<{
  /** 待勾选的服务列表(主源传 allServiceNames;副源传 filter 后的剩余 + 已勾自己的) */
  services: string[]
  /** 当前面板代表的 source ID(如 'nacos' 主源 / 'kuboard' 副源) */
  sourceID: string
  /** 提示文本(主源 vs 副源各有不同措辞);允许 raw HTML 走 v-html */
  hintHtml: string
  /** getServiceSource(svc) → 当前服务被勾给哪个 sourceID;空串 = 没勾 */
  getServiceSource: (svc: string) => string
}>()

const emit = defineEmits<{
  /** 用户点 chip 切换勾选 → 父端 setServiceSource(svc, checked ? sourceID : '') */
  toggle: [svc: string, checked: boolean]
}>()
</script>

<template>
  <div class="cc-svc-checklist">
    <div class="cc-svc-checklist-head">
      <span class="cc-svc-checklist-title">本环境包含的服务</span>
      <!-- v-html:hintHtml 含 <code> 标签(主源/副源各传不同 type 名),需要 raw 渲染 -->
      <span class="cc-svc-checklist-hint" v-html="hintHtml"></span>
    </div>
    <div class="cc-svc-checklist-grid">
      <label
        v-for="svc in services"
        :key="svc"
        class="cc-svc-checklist-item"
        :class="{ checked: getServiceSource(svc) === sourceID }"
      >
        <input
          type="checkbox"
          :checked="getServiceSource(svc) === sourceID"
          @change="emit('toggle', svc, ($event.target as HTMLInputElement).checked)"
        />
        <span class="cc-svc-checklist-name">{{ svc }}</span>
      </label>
    </div>
  </div>
</template>
