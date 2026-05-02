<script setup lang="ts">
// BotIdentityStep —— Step 3 机器人身份(名称 + AI 平台 target 卡片)。
// 从 InitPage 抽出来,InitPage 调用变 <BotIdentityStep ... /> 一行。
//
// props 形态对齐 InitPage 现有 reactive object / closure helper(同 ConfigSourceStep /
// ObservabilityStep / DataStoreStep 的迁移取舍)。Target enum / openclaw 探测三态都
// 透传,本组件只组合现有 helper 不做新逻辑。

import type { OpenClawModelEntry } from '../lib/bridge'
import { Target } from '../lib/constants'
import TargetInstallBadge from './TargetInstallBadge.vue'

interface AgentForm { name: string; id?: string }
type OpenClawDetectStatus = 'idle' | 'loading' | 'ok' | 'not-installed' | 'error'

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
  targetCanBeEnabled: (t: string) => boolean
  targetDetectedInstalled: (t: string) => boolean | null
  targetBadgeProps: (t: string) => { detected: boolean | null | undefined; versionText?: string; title?: string }
  forceEnableMissingTarget: Record<string, boolean>
  customInstallRoots: Record<string, string>
  targetDeployPaths: Record<string, string>
  targetDeployPathHints: Record<string, string>
  anyTargetSelected: boolean
  targetModels: Record<string, string>

  // OpenClaw 探测
  openclawDetectStatus: OpenClawDetectStatus
  openclawDetectError: string
  openclawDetectedModels: OpenClawModelEntry[]
  openclawResolvedDir: string
  openclawVersion: string
  openclawAuthProviders: string[]
  openclawInstallDir: string
}>()
void props  // 给 IDE 提示;运行时 Vue 自己用 props 不需要显式引用

const emit = defineEmits<{
  pickCustomInstallRoot: [t: string]
  clearCustomInstallRoot: [t: string]
  refreshAITools: []
  pickOpenClawInstallDir: []
  runOpenClawDetect: [installDir: string]
  modelChange: [t: string, e: Event]
}>()
</script>

<template>
  <div class="card lg">
    <h2>机器人身份</h2>
    <p class="help-text" style="margin-bottom:14px">
      给机器人起个名字,选要部署到哪些 AI 平台。
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

    <!-- agent.id:AI 平台里的稳定标识(OpenClaw agents.list[*].id / Claude Code / Cursor subagent 名),
         同时也作为 OpenClaw workspace 目录名(~/.openclaw/workspace/<id>/)。
         跟随 system.id 自动派生,只读;想改请回 Step 1 改 system.id。 -->
    <div class="form-group">
      <label>
        AI 平台标识
        <span class="help-icon" title="OpenClaw agents.list[*].id + workspace 目录名;Claude Code / Cursor 的 subagent 名。从 system.id 派生(<system.id>-troubleshooter)。">?</span>
        <span class="auto-tag">自动派生</span>
      </label>
      <input
        :value="agent.id || agentIdDefault"
        type="text"
        readonly
        class="readonly-input"
        title="只读;跟随 system.id 自动派生"
      />
    </div>

    <!-- 部署平台卡片:每家一张,勾选的卡片内联露出该 target 相关配置(模型 / 工作区名)。
         claude-code / cursor 不消费模型,只展示"模型由用户客户端自己选"。
         openclaw 是唯一需要工作区名的,勾选时多一行输入框。 -->
    <div class="form-group">
      <label>
        部署到哪些 AI 平台 <span class="required">*</span>
        <span class="field-hint">— 可多选;勾了哪些,相关配置(模型 / 工作区)就展开填</span>
      </label>
      <div class="target-grid">
        <div
          v-for="t in targetOptions"
          :key="t"
          class="target-card"
          :class="{ selected: enabledTargets[t], 'target-disabled': targetDetectedInstalled(t) === false && !forceEnableMissingTarget[t] }"
        >
          <label class="target-card-head">
            <input
              type="checkbox"
              v-model="enabledTargets[t]"
              :disabled="!targetCanBeEnabled(t)"
              :title="!targetCanBeEnabled(t) ? '本机未检测到该 AI 平台,先安装或点下方「我已自行安装」再勾选' : ''"
            />
            <span class="target-title">{{ targetLabels[t] }}</span>
            <TargetInstallBadge v-bind="targetBadgeProps(t)" />
          </label>
          <div class="target-hint">{{ targetDescriptions[t] }}</div>
          <!-- 未检测到 + 没强制启用 → 露出"我已自行安装/选目录/重新扫描"操作条 -->
          <div
            v-if="t !== 'openclaw' && targetDetectedInstalled(t) === false && !forceEnableMissingTarget[t]"
            class="target-missing-actions"
          >
            <span>本机未找到 {{ targetLabels[t] }} —— 先安装,或</span>
            <button type="button" class="btn-link" @click="forceEnableMissingTarget[t] = true">
              我已自行安装,继续
            </button>
            <button type="button" class="btn-link" @click="emit('pickCustomInstallRoot', t)">📁 选安装目录…</button>
            <button type="button" class="btn-link" @click="emit('refreshAITools')">🔄 重新扫描</button>
          </div>
          <div
            v-else-if="t !== 'openclaw' && targetDetectedInstalled(t) === false && forceEnableMissingTarget[t]"
            class="target-missing-actions overridden"
          >
            <span v-if="customInstallRoots[t]">📁 自定义安装目录:</span>
            <span v-else>⚠ 未检测到本机安装,已强制启用(默认部署 ~/.{{ t }})</span>
            <code v-if="customInstallRoots[t]" :title="customInstallRoots[t]">{{ customInstallRoots[t] }}</code>
            <button type="button" class="btn-link" @click="emit('pickCustomInstallRoot', t)">
              {{ customInstallRoots[t] ? '改目录…' : '📁 选安装目录…' }}
            </button>
            <button v-if="customInstallRoots[t]" type="button" class="btn-link" @click="emit('clearCustomInstallRoot', t)">清除</button>
            <button
              type="button"
              class="btn-link"
              @click="() => { forceEnableMissingTarget[t] = false; enabledTargets[t] = false; emit('clearCustomInstallRoot', t) }"
            >撤销</button>
            <button type="button" class="btn-link" @click="emit('refreshAITools')">🔄 重新扫描</button>
          </div>
          <!-- 勾选后展示 install.sh 跑完后的最终落地位置 —— AI 平台从这里读 agent。 -->
          <div v-if="enabledTargets[t]" class="target-deploy-path">
            <span class="target-deploy-path-label">部署位置</span>
            <span class="auto-tag" :title="targetDeployPathHints[t]">自动</span>
            <code :title="targetDeployPaths[t]">{{ targetDeployPaths[t] || '…' }}</code>
          </div>

          <!-- 勾选后才展开下面配置区。claude-code / cursor 没有要配的字段,
               直接不渲染 target-body,免得露出空白容器很难看 -->
          <div v-if="enabledTargets[t] && t === 'openclaw'" class="target-body">
            <!-- OpenClaw 模型:只从本地 openclaw 配置读,不给手填回路。
                 Why: openclaw gateway 只认自己 config.yaml 里声明过的 model id。 -->
            <template v-if="t === 'openclaw'">
              <div v-if="openclawDetectStatus === 'loading'" class="target-field target-note">
                <span class="scan-spinner-mini"></span>正在读 OpenClaw 配置…
              </div>
              <div v-else-if="openclawDetectStatus === 'not-installed'" class="target-field openclaw-warn">
                <div>⚠ 本机未检测到 OpenClaw 安装(默认找 <code>~/.openclaw</code>)</div>
                <div style="margin-top:4px">
                  请先安装 OpenClaw 并配置好 <code>config.yaml</code> 里的
                  <code>models:</code> 字段,然后回来点"重新扫描";
                  或者手动选择 OpenClaw 安装目录。
                </div>
                <div class="openclaw-warn-actions">
                  <button type="button" class="btn" @click="emit('runOpenClawDetect', '')">🔄 重新扫描</button>
                  <button type="button" class="btn" @click="emit('pickOpenClawInstallDir')">选择安装目录…</button>
                </div>
              </div>
              <div v-else-if="openclawDetectStatus === 'error'" class="target-field openclaw-warn">
                <div>✗ 读 OpenClaw 配置失败: {{ openclawDetectError }}</div>
                <div class="openclaw-warn-actions">
                  <button type="button" class="btn" @click="emit('pickOpenClawInstallDir')">改选目录…</button>
                  <button type="button" class="btn" @click="emit('runOpenClawDetect', openclawInstallDir)">重试</button>
                </div>
              </div>
              <div v-else-if="openclawDetectStatus === 'ok' && openclawDetectedModels.length > 0" class="target-field">
                <label class="target-field-label">
                  使用的模型
                  <span class="auto-tag">读自 {{ openclawResolvedDir }}{{ openclawVersion ? ` · v${openclawVersion}` : '' }}</span>
                  <button type="button" class="btn-link" @click="emit('pickOpenClawInstallDir')">改目录</button>
                  <button type="button" class="btn-link" @click="emit('runOpenClawDetect', openclawInstallDir)">🔄 重读</button>
                </label>
                <select
                  :value="targetModels[Target.Openclaw]"
                  @change="(e: Event) => emit('modelChange', 'openclaw', e)"
                >
                  <!-- model.id 已是完整 "<provider>/<model>" 格式(openclaw 约定),直接用 id 作 option value -->
                  <option
                    v-for="m in openclawDetectedModels"
                    :key="m.id"
                    :value="m.id"
                  >{{ m.label || m.id }}</option>
                </select>
                <div v-if="openclawAuthProviders.length" class="target-hint" style="padding-left:0;margin-top:4px">
                  已配置凭证 provider: {{ openclawAuthProviders.join(', ') }}
                </div>
              </div>
              <!-- 目录找到 + openclaw.json 能解析,但三个模型源全空:
                   typical case 是用户刚装 openclaw 还没 configure 过 / 没装过任何 agent。 -->
              <div v-else-if="openclawDetectStatus === 'ok'" class="target-field openclaw-warn">
                <div>
                  ⚠ 找到 OpenClaw 安装(<code>{{ openclawResolvedDir }}</code>),
                  但<strong>配置里还没声明任何模型</strong>
                </div>
                <div style="margin-top:4px">
                  openclaw.json 里的 <code>agents.defaults.model.primary</code> /
                  <code>agents.defaults.models</code> / <code>agents.list[].model</code> 三处都空。
                  先跑一次 <code>openclaw configure</code> 选默认模型,
                  或装一个 agent 让它产生 model 记录,再回来"重新扫描"。
                </div>
                <div class="openclaw-warn-actions">
                  <button type="button" class="btn" @click="emit('runOpenClawDetect', openclawInstallDir)">🔄 重新扫描</button>
                  <button type="button" class="btn" @click="emit('pickOpenClawInstallDir')">改选目录…</button>
                </div>
              </div>
            </template>

          </div>
        </div>
      </div>
      <div v-if="!anyTargetSelected" class="error-text" style="margin-top:6px">
        至少勾选一个部署目标
      </div>
    </div>
  </div>
</template>
