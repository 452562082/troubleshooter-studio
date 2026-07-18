<script setup lang="ts">
// DataStoreServiceBlock —— Step 7 数据层每个 (env, service) 的卡片。
// 显示:服务名 + 关联 dataId + 扫描状态 + 该服务下识别出的所有数据层组件
// (redis / mysql / doris / mongodb 等),每个组件 head 带连通性测试按钮,字段表单可编辑。
//
// 父端(InitPage)负责 reactive 状态 + 探测/删除等副作用,本组件接 props/emits 透出 UI。
// scannedTypes / dsProbeResults 直接传 reactive 引用 —— v-model 在子组件里写也能正确传播
// 回父端 reactive(Vue 3 proxy 跨组件边界仍工作)。

import type { DSScanState, DSProbeState } from '../lib/dsTypes'

defineProps<{
  envID: string
  svc: string
  /** scanStateOf(envID, svc):'ok' / 'empty' / 'skipped' / 'error' / undefined(=pending) */
  scanState: DSScanState | undefined
  /** serviceConfigSel[svcKey] —— "来源 dataId" 提示 */
  dataIdHint: string | undefined
  /** scannedDS[envID][svc] —— { dsKey: { fKey: value } };传 reactive 引用,v-model 直接写 */
  scannedTypes: Record<string, Record<string, string>>
  /** dsProbeResults reactive map;子组件按 probeKey 读 */
  dsProbeResults: Record<string, DSProbeState>
  /** 父端 reactive secret 显隐 set 的查询函数 */
  isRevealed: (key: string) => boolean
  dsLabel: (dsKey: string) => string
  dsFieldLabel: (dsKey: string, fKey: string) => string
  dsFieldIsSecret: (dsKey: string, fKey: string) => boolean
  probeKey: (envID: string, svc: string, dsKey: string) => string
}>()

const emit = defineEmits<{
  toggleReveal: [key: string]
  removeDS: [dsKey: string]
  probeDS: [dsKey: string]
}>()

function revealKey(envID: string, svc: string, dsKey: string, fKey: string) {
  return `ds:${envID}:${svc}:${dsKey}:${fKey}`
}
</script>

<template>
  <div :class="['ds-svc-block', 'status-' + (scanState?.status || 'pending')]">
    <div class="ds-svc-head">
      <span class="ds-svc-name">📁 {{ svc }}</span>
      <span
        v-if="dataIdHint"
        class="ds-svc-dataid"
        :title="'来源 dataId: ' + dataIdHint"
      >← {{ dataIdHint }}</span>
      <span
        v-if="scanState"
        :class="['ds-svc-status', 'status-' + scanState.status]"
      >
        <template v-if="scanState.status === 'ok'">✓ 已识别</template>
        <template v-else-if="scanState.status === 'empty'">△ 已读取 · 未识别到数据层</template>
        <template v-else-if="scanState.status === 'skipped'">⊘ 跳过</template>
        <template v-else-if="scanState.status === 'error'">✗ 拉取失败</template>
      </span>
    </div>

    <div v-if="scanState?.reason" class="ds-status-reason">
      {{ scanState.reason }}
    </div>

    <div
      v-if="Object.keys(scannedTypes || {}).length > 0"
      class="ds-item-list"
    >
      <div
        v-for="dsKey in Object.keys(scannedTypes).sort()"
        :key="dsKey"
        class="ds-item"
      >
        <div class="ds-item-head">
          <span class="ds-item-badge">🗄 {{ dsLabel(dsKey) }}</span>
          <button
            v-if="dsProbeResults[probeKey(envID, svc, dsKey)]?.status === 'loading'"
            type="button"
            class="ds-item-probe loading"
            disabled
          >测试中…</button>
          <button
            v-else
            type="button"
            class="ds-item-probe"
            :class="dsProbeResults[probeKey(envID, svc, dsKey)]?.status || 'idle'"
            :title="dsProbeResults[probeKey(envID, svc, dsKey)]?.detail || dsProbeResults[probeKey(envID, svc, dsKey)]?.error || '点击测试连通性 (TCP dial + 协议握手,不读不写数据)'"
            @click="emit('probeDS', dsKey)"
          >
            <template v-if="dsProbeResults[probeKey(envID, svc, dsKey)]?.status === 'ok'">
              ✓ 已连通 · {{ dsProbeResults[probeKey(envID, svc, dsKey)]?.latency }}
            </template>
            <template v-else-if="dsProbeResults[probeKey(envID, svc, dsKey)]?.status === 'fail'">
              ✗ 连接异常,点击重试
            </template>
            <template v-else>🔌 连通性测试</template>
          </button>
          <button
            type="button"
            class="ds-item-delete"
            :title="`不把本服务识别到的 ${dsLabel(dsKey)} 纳入机器人能力；重新读取配置可恢复`"
            @click="emit('removeDS', dsKey)"
          >✕</button>
        </div>
        <div
          v-if="dsProbeResults[probeKey(envID, svc, dsKey)]?.status === 'fail'"
          class="ds-probe-error"
        >
          {{ dsProbeResults[probeKey(envID, svc, dsKey)]?.error }}
        </div>
        <div class="ds-item-fields">
          <div
            v-for="fKey in Object.keys(scannedTypes[dsKey])"
            :key="fKey"
            class="cc-field"
          >
            <label class="cc-field-label">
              {{ dsFieldLabel(dsKey, fKey) }}
              <span
                v-if="dsFieldIsSecret(dsKey, fKey)"
                class="cc-scope-tag secret"
                title="Secret:会写入 yaml,分享时注意范围"
              >🔒 Secret</span>
            </label>
            <div class="cc-field-row">
              <input
                v-model="scannedTypes[dsKey][fKey]"
                :type="dsFieldIsSecret(dsKey, fKey) && !isRevealed(revealKey(envID, svc, dsKey, fKey)) ? 'password' : 'text'"
                autocomplete="off"
                spellcheck="false"
                class="cc-input"
              />
              <button
                v-if="dsFieldIsSecret(dsKey, fKey)"
                type="button"
                class="btn-link cc-reveal"
                :title="isRevealed(revealKey(envID, svc, dsKey, fKey)) ? '隐藏明文' : '显示明文'"
                @click="emit('toggleReveal', revealKey(envID, svc, dsKey, fKey))"
              >{{ isRevealed(revealKey(envID, svc, dsKey, fKey)) ? '🙈' : '👁' }}</button>
            </div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
