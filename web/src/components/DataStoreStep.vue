<script setup lang="ts">
// DataStoreStep —— Step 7 数据层整段(顶部"从配置中心读取"+ 全环境连通性 + 每 env 服务卡阵列)。
// 从 InitPage 抽出来,InitPage 调用变 <DataStoreStep ... /> 一行。
//
// props 形态对齐 InitPage 现有 reactive object / closure helper(同 ConfigSourceStep / ObservabilityStep
// 的迁移取舍),不重新设计签名以最小化迁移风险。

import type { DSScanState, DSProbeState } from '../lib/dsTypes'
import CredsShareWarning from './CredsShareWarning.vue'
import DataStoreServiceBlock from './DataStoreServiceBlock.vue'

interface Environment { id: string; is_prod: boolean }

const props = defineProps<{
  // 顶部行
  dsImportStatus: 'idle' | 'loading' | 'ok' | 'error'
  dsImportStats: { scanned: number; matched: number }
  canAutoImportDS: boolean
  probingAll: boolean
  probingAllStats: { done: number; total: number; fail: number }
  probingByEnv: Record<string, boolean>

  // env / svc 数据
  environments: Environment[]
  allServiceNames: string[]
  scannedDS: Record<string, Record<string, Record<string, Record<string, string>>>>
  serviceConfigSel: Record<string, string>
  dsProbeResults: Record<string, DSProbeState>

  // helper(全部 InitPage 那边定义)
  scanStateOf: (envID: string, svc: string) => DSScanState | undefined
  svcKey: (envID: string, svc: string) => string
  isRevealed: (k: string) => boolean
  dsLabel: (dsKey: string) => string
  dsFieldLabel: (dsKey: string, field: string) => string
  dsFieldIsSecret: (dsKey: string, field: string) => boolean
  probeKey: (envID: string, svc: string, dsKey: string) => string
}>()

const emit = defineEmits<{
  autoImportDataStores: []
  probeAllAcrossEnvs: []
  toggleReveal: [key: string]
  removeDS: [envID: string, svc: string, dsKey: string]
  probeDS: [envID: string, svc: string, dsKey: string]
}>()

// 全部环境一键连通性测试按钮 disable 条件:scannedDS 全空 → 没东西可测
const probeAllDisabled = () => {
  for (const env of Object.values(props.scannedDS)) {
    for (const svc of Object.values(env)) {
      if (Object.keys(svc).length > 0) return false
    }
  }
  return true
}
</script>

<template>
  <div class="card lg">
    <h2>数据层</h2>
    <p class="help-text">
      从配置中心读取各服务配置,自动识别用到的数据层(Redis / MySQL / MongoDB 等),按 <strong>环境 → 服务 → 组件</strong> 展示。字段可直接编辑,改的是本地副本,不会回写配置中心。
    </p>

    <CredsShareWarning :margin-bottom="18">
      <li>本页字段(含密码、token 等凭证)将保存至 <code>system.yaml</code>。</li>
      <li>部署时,生成器把对应值注入目标 AI 平台的 MCP Server 环境变量。</li>
      <li><strong>system.yaml 含明文凭证</strong>,请仅在可信范围内分享。</li>
    </CredsShareWarning>

    <div class="ds-autoimport-row">
      <!-- loading / idle 分别用独立 <button> + :key,避免 WebKit GPU layer 残影 -->
      <button
        v-if="dsImportStatus === 'loading'"
        :key="'ds-import-loading'"
        class="btn primary cc-preload-btn"
        disabled
      >
        <span class="cc-preload-spinner" aria-hidden="true"></span>
        读取中…
      </button>
      <button
        v-else
        :key="'ds-import-idle'"
        class="btn primary cc-preload-btn"
        :disabled="!canAutoImportDS"
        @click="emit('autoImportDataStores')"
      >📥 从配置中心读取</button>
      <span v-if="!canAutoImportDS" class="ds-autoimport-hint">
        需先在 Step 5 完成配置源扫描 + 映射服务 dataId
      </span>
      <span v-else-if="dsImportStatus === 'ok'" class="cc-preload-summary">
        ✓ 成功拉 {{ dsImportStats.scanned }} / 应拉 {{ environments.length * allServiceNames.length }} 条配置(env × service),识别 {{ dsImportStats.matched }} 个数据层
      </span>

      <!-- 全部环境一键连通性测试 -->
      <button
        v-if="probingAll"
        :key="'probe-all-envs-loading'"
        class="btn cc-preload-btn ds-probe-all-btn"
        disabled
        style="margin-left:auto;"
      >
        <span class="cc-preload-spinner" aria-hidden="true"></span>
        测试中… {{ probingAllStats.done }} / {{ probingAllStats.total }}
        <span v-if="probingAllStats.fail > 0" style="color:#dc2626;">(✗ {{ probingAllStats.fail }})</span>
      </button>
      <button
        v-else
        :key="'probe-all-envs-idle'"
        class="btn cc-preload-btn ds-probe-all-btn"
        :disabled="probeAllDisabled()"
        title="对所有环境的所有数据层组件批量执行连通性测试(跨 env 并行,env 内串行)"
        style="margin-left:auto;"
        @click="emit('probeAllAcrossEnvs')"
      >🔌 全部环境一键连通性测试</button>
    </div>

    <!-- 按 env → 全部 service(allServiceNames) → ds 层级完整展示;
         缺失项(没映射 / 拉失败 / 未识别)也会出现一条,明确标原因。 -->
    <div class="ds-hierarchy">
      <div v-for="env in environments" :key="env.id" class="ds-env-section">
        <div class="ds-env-title">
          <span class="cc-env-label">{{ env.id || '(未命名 env)' }}</span>
          <span v-if="env.is_prod" class="cc-env-prod-tag">prod</span>
          <span class="ds-env-count">
            {{ allServiceNames.length }} 个服务 ·
            已识别 {{ Object.values(scannedDS[env.id] || {}).filter(s => Object.keys(s).length > 0).length }}
          </span>
          <span
            v-if="probingByEnv[env.id]"
            class="ds-env-probe-all loading"
          >
            <span class="cc-preload-spinner" aria-hidden="true"></span>
            测试中…
          </span>
        </div>

        <div v-if="allServiceNames.length === 0" class="ds-empty">
          Step 4 还没填 <code>service_names</code>,这里没服务可扫
        </div>

        <div v-else class="ds-svc-container">
          <DataStoreServiceBlock
            v-for="svc in allServiceNames"
            :key="svc"
            :env-i-d="env.id"
            :svc="svc"
            :scan-state="scanStateOf(env.id, svc)"
            :data-id-hint="serviceConfigSel[svcKey(env.id, svc)]"
            :scanned-types="scannedDS[env.id]?.[svc] || {}"
            :ds-probe-results="dsProbeResults"
            :is-revealed="isRevealed"
            :ds-label="dsLabel"
            :ds-field-label="dsFieldLabel"
            :ds-field-is-secret="dsFieldIsSecret"
            :probe-key="probeKey"
            @toggle-reveal="(k) => emit('toggleReveal', k)"
            @remove-d-s="(dsKey) => emit('removeDS', env.id, svc, dsKey)"
            @probe-d-s="(dsKey) => emit('probeDS', env.id, svc, dsKey)"
          />
        </div>
      </div>
    </div>
  </div>
</template>
