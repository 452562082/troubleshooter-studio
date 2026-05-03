<script setup lang="ts">
// SystemBasicInfoStep —— Step 2 系统基本信息(显示名 / ID / 描述)。
// 自带 ID 自动派生闭环:用户输 system.name → slugify → 写 system.id;手改过 ID 后 lock 住,
// 不再被 name 变动覆盖。idManualOverride 走 v-model 暴露给父端 save 草稿。
//
// 设计注:
//   - system 是父端的 reactive 对象,本组件直接 mutate 字段(reactivity 跨边界 OK)
//   - 把 id 派生逻辑放本组件而不是父端,Step 2 卸载/挂载时 watch 自动跟随;
//     用户只在 Step 2 改 name,所以 watch 只在该步存在期间起作用就够。

import { computed, watch, onMounted } from 'vue'

const props = defineProps<{
  /** 父端 reactive 的 system 对象,本组件直接改字段 */
  system: { name: string; id: string; description: string }
  /** 校验错误探测函数,模板里按字段名查 */
  hasError: (key: string) => boolean
}>()

const idManualOverride = defineModel<boolean>('idManualOverride', { required: true })

function slugifyToId(name: string): string {
  const s = (name || '')
    .toLowerCase()
    .replace(/[^\x00-\x7F]/g, '')
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
    .slice(0, 32)
  if (!s || !/^[a-z0-9]/.test(s)) return ''
  return s
}

watch(() => props.system.name, (val) => {
  if (idManualOverride.value) return
  const derived = slugifyToId(val)
  if (derived) props.system.id = derived
})

onMounted(() => {
  if (!props.system.id && !idManualOverride.value) {
    const derived = slugifyToId(props.system.name)
    if (derived) props.system.id = derived
  }
})

function markIdManual() {
  idManualOverride.value = true
}
function resetIdAuto() {
  idManualOverride.value = false
  props.system.id = slugifyToId(props.system.name)
}

const idAutoFailed = computed(() => {
  if (!props.system.name.trim()) return false
  return slugifyToId(props.system.name) === ''
})
const idCanAutoDerive = computed(() => slugifyToId(props.system.name) !== '')
</script>

<template>
  <div class="card lg">
    <h2>系统基本信息</h2>
    <p class="help-text" style="margin-bottom:14px">
      填一下机器人服务的业务系统:展示名、ID、一句话描述。
    </p>

    <div class="form-group">
      <label>系统显示名 <span class="required">*</span>
        <span class="field-hint">— 机器人打招呼 / 文档标题都用这个(可中文)</span>
      </label>
      <input
        v-model="system.name"
        type="text"
        placeholder="我的系统"
        :class="{ error: hasError('system.name') }"
      />
      <span v-if="hasError('system.name')" class="error-text">必填</span>
    </div>

    <div class="form-group">
      <label>
        系统 ID
        <span class="help-icon" title="机器可读标识(ASCII),用作目录名、agent id 前缀、MCP 实例名。默认从「系统显示名」自动派生(Shop → shop)。纯中文名派生不出来时会露出手填输入框。">?</span>
        <span v-if="idManualOverride" class="field-hint">(已手改,改完不再跟随系统名)</span>
        <span v-else-if="idCanAutoDerive" class="field-hint">— 自动从系统名派生</span>
        <span v-else-if="idAutoFailed" class="field-hint" style="color:#b45309">— 系统名全是中文,派生不出,请手填</span>
      </label>

      <div v-if="!idManualOverride && idCanAutoDerive" class="id-autoderive">
        <code class="id-badge">{{ system.id || '(填完系统名后自动生成)' }}</code>
        <button type="button" class="btn-link" @click="markIdManual">自定义 ID →</button>
      </div>

      <div v-else>
        <div class="id-input-row">
          <input
            v-model="system.id"
            type="text"
            placeholder="my-system (仅小写字母/数字/短横线,首字符 [a-z0-9])"
            :class="{ error: hasError('system.id') }"
            @input="markIdManual"
          />
          <button
            v-if="idManualOverride && idCanAutoDerive"
            type="button"
            class="btn-link"
            @click="resetIdAuto"
            title="恢复：从系统名自动派生"
          >↺ 跟随系统名</button>
        </div>
        <span v-if="hasError('system.id')" class="error-text">仅允许 [a-z0-9-],首字符必须是字母或数字</span>
      </div>
    </div>

    <div class="form-group">
      <label>系统描述</label>
      <textarea v-model="system.description" placeholder="一句话描述你的系统（选填）" rows="3" />
    </div>
  </div>
</template>
