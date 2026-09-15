<script setup lang="ts">
import { computed, inject, ref, watch, reactive, watchEffect } from 'vue'
import type { DSScanState, DSProbeState } from '../lib/dsTypes'
import type { CredField } from '../lib/credFields'
import { WizardStoreKey } from '../lib/wizardStore'
import { dataConnections, type DataConnection } from '../lib/dataConnections'

const wizard = inject(WizardStoreKey)!
const props = defineProps<{
  storeSpecs: Array<{ key: string; label: string; fields: CredField[] }>
  dataStoreType: (id: string) => string
  dsImportStatus: 'idle' | 'loading' | 'ok' | 'error'
  dsImportStats: { scanned: number; matched: number }
  canAutoImportDS: boolean
  probingAll: boolean
  probingAllStats: { done: number; total: number; fail: number }
  probingByEnv: Record<string, boolean>
  scannedDS: Record<string, Record<string, Record<string, Record<string, string>>>>
  serviceConfigSel: Record<string, string>
  dsProbeResults: Record<string, DSProbeState>
  scanStateOf: (env: string, svc: string) => DSScanState | undefined
  dsLabel: (key: string) => string
  dsFieldLabel: (key: string, field: string) => string
  dsFieldIsSecret: (key: string, field: string) => boolean
  probeKey: (env: string, svc: string, key: string) => string
}>()
const emit = defineEmits<{
  autoImportDataStores: []
  probeAllAcrossEnvs: []
  removeDS: [env: string, svc: string, key: string]
  probeDS: [env: string, svc: string, key: string]
  addConnection: [env: string, services: string[], type: string]
  openConfig: []
  dismissScanError: [env: string, svc: string]
}>()
const adding = ref(false)
const env = ref('')
const type = ref('redis')
const services = ref<string[]>([])
watch(() => wizard.environments.map(e => e.id), ids => { if (!ids.includes(env.value)) env.value = ids[0] || '' }, { immediate: true })
watch(() => wizard.allServiceNames, names => {
  services.value = services.value.filter(s => names.includes(s))
  if (names.length === 1) services.value = [...names]
}, { immediate: true })
const groups = computed(() => Object.fromEntries(wizard.environments.map(e => [e.id, dataConnections(props.scannedDS[e.id] || {}, wizard.allServiceNames)])))
const expanded = reactive<Record<string, boolean>>({})
const previousStates = new Map<string, string>()
function connectionKey(envID: string, group: DataConnection) { return `${envID}:${group.id}:${group.members[0].service}` }
watchEffect(() => {
  for (const [envID, entries] of Object.entries(groups.value)) {
    for (const group of entries) {
      const key = connectionKey(envID, group)
      const current = state(envID, group)
      if (!(key in expanded)) expanded[key] = ['待补充', '连接异常'].includes(current)
      if (current === '连接异常' && previousStates.get(key) !== current) expanded[key] = true
      previousStates.set(key, current)
    }
  }
})
const count = computed(() => Object.values(groups.value).reduce((n, list) => n + list.length, 0))
function state(envID: string, group: DataConnection) {
  const fields = props.storeSpecs.find(s => s.key === props.dataStoreType(group.id))?.fields || []
  if (fields.some(f => !f.optional && !group.fields[f.key]?.trim())) return '待补充'
  const states = group.members.map(m => props.dsProbeResults[props.probeKey(envID, m.service, m.key)]?.status)
  if (states.includes('fail')) return '连接异常'
  if (states.includes('loading')) return '检查中'
  return states.every(s => s === 'ok') ? '连接检查通过' : '待检查'
}
function update(envID: string, group: DataConnection, field: string, value: string) {
  for (const m of group.members) {
    props.scannedDS[envID][m.service][m.key][field] = value
    delete props.dsProbeResults[props.probeKey(envID, m.service, m.key)]
  }
}
function apply(action: 'probeDS' | 'removeDS', envID: string, group: DataConnection) {
  for (const m of group.members) {
    if (action === 'probeDS') emit('probeDS', envID, m.service, m.key)
    else emit('removeDS', envID, m.service, m.key)
  }
}
function add() {
  if (!env.value || !services.value.length) return
  emit('addConnection', env.value, [...services.value], type.value)
  adding.value = false
}
</script>

<template>
  <section class="card lg data-connections">
    <div class="connection-heading"><div><h2>数据库与缓存</h2><p>自动识别连接，或直接添加；同一连接可供多个服务使用。</p></div><button class="btn primary" @click="adding = !adding">{{ adding ? '取消添加' : '添加连接' }}</button></div>
    <form v-if="adding" class="connection-add" @submit.prevent="add">
      <label>环境<select v-model="env" required><option v-for="e in wizard.environments" :key="e.id" :value="e.id">{{ e.id }}</option></select></label>
      <label>连接类型<select v-model="type"><option v-for="s in storeSpecs" :key="s.key" :value="s.key">{{ s.label }}</option></select></label>
      <fieldset><legend>关联服务（可多选）</legend><label v-for="svc in wizard.allServiceNames" :key="svc" class="service-option"><input v-model="services" type="checkbox" :value="svc">{{ svc }}</label><p v-if="!wizard.allServiceNames.length">请先在“选择项目”添加后端服务。</p></fieldset>
      <button class="btn primary" :disabled="!env || !services.length">添加并填写连接</button>
    </form>
    <div class="connection-tools">
      <button v-if="canAutoImportDS" class="btn" :disabled="dsImportStatus === 'loading'" @click="emit('autoImportDataStores')">{{ dsImportStatus === 'loading' ? '识别中…' : '从配置自动识别' }}</button>
      <button v-else class="btn link" @click="emit('openConfig')">连接配置源以自动识别</button>
      <span v-if="dsImportStatus === 'ok'">已读取 {{ dsImportStats.scanned }} 条配置</span>
      <button v-if="count" class="btn" :disabled="probingAll" @click="emit('probeAllAcrossEnvs')">{{ probingAll ? `检查中 ${probingAllStats.done}/${probingAllStats.total}` : '检查所有连接' }}</button>
    </div>
    <p v-if="!count && !adding" class="connection-empty">还没有添加连接。不使用数据库查询时，可直接继续创建。</p>
    <section v-for="e in wizard.environments" :key="e.id" class="connection-environment">
      <h3 v-if="groups[e.id]?.length">{{ e.id }} <small>{{ groups[e.id].length }} 个连接</small></h3>
      <template v-for="svc in wizard.allServiceNames" :key="svc">
        <div v-if="scanStateOf(e.id, svc)?.status === 'error'" class="connection-error">{{ svc }}：配置读取失败，请检查配置源后重试。<button class="btn link" @click="emit('openConfig')">检查配置源</button><button class="btn link" @click="emit('dismissScanError', e.id, svc)">跳过此次识别</button></div>
      </template>
      <details v-for="g in groups[e.id]" :key="g.id + ':' + g.members[0].service" class="connection-item" :open="expanded[connectionKey(e.id, g)]" @toggle="expanded[connectionKey(e.id, g)] = ($event.target as HTMLDetailsElement).open">
        <summary><strong>{{ dsLabel(g.id) }}</strong><span>{{ g.members.map(m => m.service).join('、') }}</span><small :class="{ issue: ['待补充','连接异常'].includes(state(e.id, g)) }">{{ state(e.id, g) }}</small></summary>
        <div class="connection-fields">
          <label v-for="field in Object.keys(g.fields)" :key="field">{{ dsFieldLabel(g.id, field) }}<input :value="g.fields[field]" :type="dsFieldIsSecret(g.id, field) ? 'password' : 'text'" autocomplete="off" :placeholder="storeSpecs.find(s => s.key === dataStoreType(g.id))?.fields.find(f => f.key === field)?.placeholder" @input="update(e.id, g, field, ($event.target as HTMLInputElement).value)"></label>
        </div>
        <div class="connection-tools"><button class="btn" :disabled="['待补充','检查中'].includes(state(e.id, g))" @click="apply('probeDS', e.id, g)">检查连接</button><button class="btn link" @click="apply('removeDS', e.id, g)">移除此连接</button></div>
      </details>
    </section>
    <p class="connection-footnote">密码仅保存在系统钥匙串；这里的修改只影响机器人连接配置。</p>
  </section>
</template>
<style scoped>
.connection-heading,.connection-tools { display:flex; align-items:center; justify-content:space-between; gap:12px; flex-wrap:wrap; }
p { color:#64748b; font-size:13px; line-height:1.6; margin:6px 0; }
.connection-add { margin:18px 0; padding:18px; border:1px solid #bfdbfe; border-radius:12px; background:#f8fbff; display:grid; grid-template-columns:1fr 1fr; gap:16px; }
label { display:flex; flex-direction:column; gap:6px; font-size:13px; color:#475569; }
select,input:not([type=checkbox]) { box-sizing:border-box; width:100%; min-width:0; min-height:40px; padding:8px 10px; border:1px solid #cbd5e1; border-radius:7px; background:white; font:inherit; }
fieldset { grid-column:1/-1; border:0; padding:0; }
legend { font-size:13px; color:#475569; margin-bottom:8px; }
.service-option { display:inline-flex; flex-direction:row; align-items:center; margin-right:16px; min-height:40px; }
.connection-tools { justify-content:flex-start; margin:16px 0; font-size:13px; color:#64748b; }
.connection-empty { padding:24px; background:#f8fafc; border-radius:10px; text-align:center; }
h3 { font-size:14px; margin:20px 0 10px; } h3 small { color:#64748b; font-weight:400; margin-left:8px; }
.connection-item { border:1px solid #e2e8f0; border-radius:10px; margin:10px 0; padding:0 16px; }
summary { cursor:pointer; display:flex; align-items:center; flex-wrap:wrap; gap:12px; min-height:56px; font-size:14px; }
summary::before { content:'›'; color:#64748b; } details[open]>summary::before { transform:rotate(90deg); }
summary span { flex:1; color:#64748b; font-size:12px; overflow-wrap:anywhere; } summary small { color:#475569; background:#f1f5f9; border-radius:5px; padding:4px 8px; }
summary small.issue { background:#fff7ed; color:#9a3412; }
.connection-fields { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:14px; padding:12px 0; }
.connection-error { background:#fff7ed; color:#9a3412; padding:12px; font-size:13px; border-radius:8px; }
.connection-footnote { margin-top:20px; }
button:focus-visible,summary:focus-visible,input:focus-visible,select:focus-visible { outline:2px solid #2563eb; outline-offset:2px; }
@media(max-width:800px) { .connection-add,.connection-fields { grid-template-columns:1fr; } }
</style>
