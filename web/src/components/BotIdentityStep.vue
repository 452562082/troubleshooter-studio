<script setup lang="ts">
// BotIdentityStep —— Step 3 机器人身份(名称 + AI 平台 target 卡片)。
// 从 InitPage 抽出来,InitPage 调用变 <BotIdentityStep ... /> 一行。
//
// props 形态对齐 InitPage 现有 reactive object / closure helper(同 ConfigSourceStep /
// 透传,本组件只组合现有 helper 不做新逻辑。

import { reactive } from 'vue'
import { probeAgentAvailability } from '../lib/bridge/aitools'
import TargetInstallBadge from './TargetInstallBadge.vue'

interface AgentForm { name: string; id?: string }

const props = defineProps<{
  agent: AgentForm
  agentNameDefault: string
  agentIdDefault: string
  hasError: (key: string) => boolean

  // 部署目标卡
  targetOptions: readonly string[]
  targetLabels: Record<string, string>
  targetDescriptions: Record<string, string>
  enabledTargets: Record<string, boolean>
  targetDetectedInstalled: (t: string) => boolean | null
  targetBadgeProps: (t: string) => { detected: boolean | null | undefined; versionText?: string; title?: string }
  aitoolsRefreshing: boolean
  targetDeployPaths: Record<string, string>
  targetDeployPathHints: Record<string, string>
  anyTargetSelected: boolean
  targetModels: Record<string, string>

}>()
void props  // 给 IDE 提示;运行时 Vue 自己用 props 不需要显式引用

const emit = defineEmits<{
  refreshAITools: []
  modelChange: [t: string, e: Event]
}>()
const availability = reactive<Record<string, {status: string; message: string}>>({})
async function checkPlatform(target: string) {
  if (availability[target]?.status === 'loading') return
  availability[target] = { status: 'loading', message: '正在调用当前默认模型，最多等待 75 秒…' }
  try { availability[target] = await probeAgentAvailability(target) }
  catch { availability[target] = { status: 'error', message: '检查未完成，请确认桌面工作台已更新后重试' } }
}
</script>

<template>
  <div class="card lg">
    <h2>使用哪个 AI 平台？</h2>
    <p class="help-text" style="margin-bottom:14px">
      先选一个常用平台，需要时也可以部署到多个平台。
    </p>
    <div class="form-group">
      <label>机器人名称 <span class="required">*</span></label>
      <input
        v-model="agent.name"
        type="text"
        :placeholder="agentNameDefault"
        :class="{ error: hasError('agent.name') }"
      />
    </div>


    <div class="form-group">
      <label>
        AI 平台 <span class="required">*</span>
        <span class="field-hint">— 选择常用平台，可多选</span>
      </label>
      <div class="target-grid">
        <div
          v-for="t in targetOptions"
          :key="t"
          class="target-card"
          :class="{ selected: enabledTargets[t], 'target-disabled': targetDetectedInstalled(t) === false }"
        >
          <label class="target-card-head">
            <input
              type="checkbox"
              v-model="enabledTargets[t]"
              :disabled="targetDetectedInstalled(t) === false"
              :title="targetDetectedInstalled(t) === false ? `本机未检测到 ${targetLabels[t]},安装命令行工具后重新检测` : ''"
            />
            <span class="target-title">{{ targetLabels[t] }}</span>
            <TargetInstallBadge v-bind="targetBadgeProps(t)" />
          </label>
          <div class="target-hint">{{ targetDescriptions[t] }}</div>
          <!-- detector 没扫到 → checkbox disabled,提示"先装 IDE 再回来",
               不提供 force enable 兜底(避免用户装到孤儿目录的 confusion)。
               若 detector 漏扫(IDE 装在非标准位置),用户可点重新扫描重试。 -->
          <div
            v-if="targetDetectedInstalled(t) === false"
            class="target-missing-actions"
          >
            <span>⚠ 本机未检测到 {{ targetLabels[t] }}，请安装对应的命令行工具后重新检测</span>
            <button
              type="button"
              class="btn-link"
              :disabled="aitoolsRefreshing"
              @click="emit('refreshAITools')"
            >{{ aitoolsRefreshing ? '⏳ 扫描中…' : '🔄 重新扫描' }}</button>
          </div>
          <div v-if="enabledTargets[t]" class="platform-check">
            <p aria-live="polite" :class="availability[t]?.status">{{ availability[t]?.message || '账号与模型尚未检查' }}</p>
            <button type="button" class="btn" :disabled="availability[t]?.status === 'loading'" @click="checkPlatform(t)">
              {{ availability[t]?.status === 'loading' ? '检查中…' : '检查账号与模型' }}
            </button>
            <small>发送一条简短测试消息，使用平台当前默认模型。</small>
          </div>
          <!-- 勾选后展示 install.sh 跑完后的最终落地位置 —— AI 平台从这里读 agent。 -->
          <details v-if="enabledTargets[t]" class="wizard-advanced"><summary>部署位置与内部标识</summary>
          <div class="target-deploy-path">
            <span class="target-deploy-path-label">部署位置</span>
            <span class="auto-tag" :title="targetDeployPathHints[t]">自动</span>
            <code :title="targetDeployPaths[t]">{{ targetDeployPaths[t] || '…' }}</code>
          </div><code>{{ agent.id || agentIdDefault }}</code></details>

          <!-- 勾选后才展开下面配置区。claude-code / cursor 没有要配的字段,
               直接不渲染 target-body,免得露出空白容器很难看 -->
        </div>
      </div>
      <div v-if="!anyTargetSelected" class="error-text" style="margin-top:6px">
        至少勾选一个部署目标
      </div>
    </div>
  </div>
</template>

<style scoped>
.platform-check { margin-top: 14px; padding-top: 12px; border-top: 1px solid #e2e8f0; }
.platform-check p { font-size: 13px; color: #64748b; margin: 0 0 8px; }
.platform-check .ready { color: #15803d; }
.platform-check .error { color: #b91c1c; }
.platform-check small { display: block; margin-top: 8px; font-size: 12px; color: #64748b; }
</style>
